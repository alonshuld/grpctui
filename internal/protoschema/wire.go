package protoschema

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Wire serialises a message to its protobuf encoding — the bytes a raw wire
// view shows.
//
// It is a re-encoding rather than a capture of what actually arrived. gRPC
// hands its stats handlers the decoded message and not the frame it came in, so
// there is nothing to capture; what this produces is the same message under the
// same encoding, with fields in number order. A server that wrote its fields
// out of order, or used a non-minimal varint, will differ here in layout while
// meaning exactly the same thing — which is the honest caveat, and the reason
// the panel labels the view "as re-encoded".
func Wire(msg proto.Message) ([]byte, error) {
	if msg == nil {
		return nil, errors.New("encode message: no message")
	}

	// Deterministic, so that two calls returning the same message produce the
	// same bytes and a diff of two raw views shows only what really changed.
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("encode message: %w", err)
	}
	return raw, nil
}

// WireField is one field as it appears on the wire: what a decoder sees before
// it has a descriptor to give the field a name.
//
// It is deliberately schema-free. The point of a raw view is to show what
// arrived when the decoded one looks wrong — an unknown field, a field the
// client's descriptor says is a string and the server wrote as an int — and
// consulting the descriptor to render it would hide exactly those cases.
type WireField struct {
	// Number is the field number, and Type the wire type's name.
	Number int32
	Type   string

	// Size is how many bytes the whole field occupies, tag included.
	Size int

	// Value is the payload rendered for a human: a number, a quoted string, or
	// a byte count for something that is neither.
	Value string
}

// The wire type names, spelled as protobuf's encoding documentation spells
// them rather than as protowire's Go constants do.
const (
	wireVarint  = "varint"
	wireFixed64 = "64-bit"
	wireBytes   = "len"
	wireGroup   = "group"
	wireFixed32 = "32-bit"
)

// WireFields parses raw protobuf bytes into the fields they encode, without a
// descriptor.
//
// A truncated or malformed buffer stops the walk and returns what was read up
// to that point along with the error: a body that goes wrong halfway is exactly
// the body somebody opened the raw view to look at, and the fields before the
// damage are the useful part.
func WireFields(raw []byte) ([]WireField, error) {
	var fields []WireField

	for len(raw) > 0 {
		number, typ, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			return fields, fmt.Errorf("field %d: malformed tag", len(fields)+1)
		}

		value, valueLen := consumeValue(raw[tagLen:], typ)
		if valueLen < 0 {
			return fields, fmt.Errorf("field %d: malformed value", number)
		}

		fields = append(fields, WireField{
			Number: int32(number),
			Type:   wireTypeName(typ),
			Size:   tagLen + valueLen,
			Value:  value,
		})
		raw = raw[tagLen+valueLen:]
	}
	return fields, nil
}

// consumeValue reads one field's payload, returning it rendered and how many
// bytes it took. A negative length is protowire's way of saying the buffer is
// malformed, and is passed straight back.
func consumeValue(raw []byte, typ protowire.Type) (string, int) {
	switch typ {
	case protowire.VarintType:
		v, n := protowire.ConsumeVarint(raw)
		if n < 0 {
			return "", n
		}
		return strconv.FormatUint(v, 10), n

	case protowire.Fixed64Type:
		v, n := protowire.ConsumeFixed64(raw)
		if n < 0 {
			return "", n
		}
		return fmt.Sprintf("0x%016x", v), n

	case protowire.Fixed32Type:
		v, n := protowire.ConsumeFixed32(raw)
		if n < 0 {
			return "", n
		}
		return fmt.Sprintf("0x%08x", v), n

	case protowire.BytesType:
		v, n := protowire.ConsumeBytes(raw)
		if n < 0 {
			return "", n
		}
		return renderBytes(v), n

	case protowire.StartGroupType:
		// Groups are proto2's deprecated nested-message encoding. They are rare
		// enough that rendering their contents is not worth a recursive walk, and
		// common enough in old schemas to be worth not choking on.
		v, n := protowire.ConsumeGroup(protowire.Number(0), raw)
		if n < 0 {
			return "", n
		}
		return byteCount(len(v)), n

	default:
		return "", -1
	}
}

// maxInlineBytes is how much of a length-delimited field is spelled out before
// it is reported as a size alone. It is a line's worth: the raw view is for
// spotting what a field holds, and a 4 KB blob spread over the panel tells you
// less than "4.0 KB" does.
const maxInlineBytes = 48

// renderBytes shows a length-delimited payload as the thing it most likely is.
//
// Valid printable UTF-8 is shown as a quoted string, which covers strings and
// most of what a debugging session cares about. Everything else — a nested
// message, a packed repeated field, real binary — is reported by size, because
// guessing at a nested message means re-parsing bytes that may not be one.
func renderBytes(raw []byte) string {
	if len(raw) == 0 {
		return `""`
	}
	if !printable(raw) {
		return byteCount(len(raw))
	}

	text := string(raw)
	if len(text) > maxInlineBytes {
		return strconv.Quote(text[:maxInlineBytes]) + "… " + byteCount(len(raw))
	}
	return strconv.Quote(text)
}

// printable reports whether raw is text a person could read: valid UTF-8 with
// no control characters other than the ordinary whitespace.
func printable(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for _, r := range string(raw) {
		switch {
		case r == '\t', r == '\n', r == '\r':
		case r < 0x20, r == 0x7f:
			return false
		}
	}
	return true
}

func wireTypeName(typ protowire.Type) string {
	switch typ {
	case protowire.VarintType:
		return wireVarint
	case protowire.Fixed64Type:
		return wireFixed64
	case protowire.BytesType:
		return wireBytes
	case protowire.StartGroupType, protowire.EndGroupType:
		return wireGroup
	case protowire.Fixed32Type:
		return wireFixed32
	default:
		return "?"
	}
}

// byteCount renders a size the way a status line should: bytes up to a
// kilobyte, then one decimal place.
func byteCount(n int) string {
	const unit = 1024
	if n < unit {
		return strconv.Itoa(n) + " B"
	}

	value, suffix := float64(n)/unit, "KB"
	if value >= unit {
		value, suffix = value/unit, "MB"
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + suffix
}

// ByteCount renders a byte size for display. It is the same rendering the raw
// view uses for a payload it will not spell out, exported so the response
// panel's status line agrees with the body underneath it.
func ByteCount(n int) string { return byteCount(n) }

// Hexdump renders bytes as offset, hex and ASCII columns — `hexdump -C`'s
// layout, which is the one every terminal debugging session already reads.
//
// It is written here rather than taken from encoding/hex's Dump so that the
// column widths are ours: hex.Dump hard-codes sixteen bytes a line and a
// trailing newline, and the panel needs neither fixed.
func Hexdump(raw []byte, perLine int) string {
	if perLine <= 0 {
		perLine = 16
	}
	if len(raw) == 0 {
		return ""
	}

	var b strings.Builder
	for offset := 0; offset < len(raw); offset += perLine {
		line := raw[offset:min(offset+perLine, len(raw))]

		fmt.Fprintf(&b, "%08x  ", offset)
		for i := range perLine {
			if i < len(line) {
				fmt.Fprintf(&b, "%02x ", line[i])
			} else {
				b.WriteString("   ")
			}
			// A gap down the middle, so a byte's column can be counted at a
			// glance rather than by sliding a finger along the row.
			if i == perLine/2-1 {
				b.WriteByte(' ')
			}
		}

		b.WriteString(" |")
		for _, c := range line {
			if c < 0x20 || c >= 0x7f {
				b.WriteByte('.')
				continue
			}
			b.WriteByte(c)
		}
		b.WriteString("|")

		if offset+perLine < len(raw) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// DecodeWire decodes raw protobuf bytes against a descriptor.
//
// It is the other direction of [Wire], and it exists for one caller: a request
// the passive proxy watched go past, which arrives as bytes because the proxy
// has no schema, and becomes a filled-in form once the UI has found the method
// in what reflection discovered.
func DecodeWire(desc protoreflect.MessageDescriptor, raw []byte) (proto.Message, error) {
	if desc == nil {
		return nil, errors.New("decode message: no descriptor")
	}

	msg := dynamicpb.NewMessage(desc)
	if err := proto.Unmarshal(raw, msg); err != nil {
		return nil, fmt.Errorf("decode message as %s: %w", desc.FullName(), err)
	}
	return msg, nil
}
