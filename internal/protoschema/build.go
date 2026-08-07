package protoschema

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// FieldError reports a form value that does not fit its field's type. Its
// message is shown to the user next to the offending field, so it is phrased
// for a human rather than lifted from strconv.
type FieldError struct {
	// Field is the protobuf field name.
	Field string

	// Value is the text the user typed.
	Value string

	Err error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Err.Error() }

func (e *FieldError) Unwrap() error { return e.Err }

// Build turns filled-in form values, keyed by field name, into a request
// message.
//
// A field absent from values is left alone, and so is one whose value is empty
// — "" and "unset" are the same thing for an ordinary proto3 field, and treating
// them differently would put empty strings on the wire for every field the user
// skipped. Values for [KindUnsupported] fields are ignored.
//
// The exception is a field with explicit presence — proto3's `optional`, or a
// proto2 optional — whose type has an empty value the user can actually mean.
// For those, a present-but-empty entry is sent as an explicit empty value, which
// is the only way the caller can say "" rather than "nothing". Which is why an
// untouched field must be left out of values entirely rather than mapped to "":
// see [Field.AcceptsEmpty].
//
// Every unparseable value is reported, not just the first: a form that fails
// one field at a time is miserable to fill in. The returned error joins one
// [*FieldError] per bad field.
func (f Form) Build(values map[string]string) (proto.Message, error) {
	if f.desc == nil {
		return nil, errors.New("build request: form has no message descriptor")
	}

	msg := dynamicpb.NewMessage(f.desc)
	var errs []error

	for _, field := range f.Fields {
		raw, ok := values[field.Name]
		if !ok || !field.Editable() {
			continue
		}
		if raw == "" && !field.AcceptsEmpty() {
			continue
		}

		v, err := field.parse(raw)
		if err != nil {
			errs = append(errs, &FieldError{Field: field.Name, Value: raw, Err: err})
			continue
		}
		msg.Set(field.desc, v)
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return msg, nil
}

// Values reads a message back into the flat map [Form.Build] accepts. Fields
// left at their default are omitted, so Build(Values(m)) reproduces m.
//
// It is what makes the build/decode round trip testable, and what v0.6 needs to
// reload a request out of history.
func (f Form) Values(msg proto.Message) map[string]string {
	out := make(map[string]string, len(f.Fields))
	if msg == nil {
		return out
	}

	m := msg.ProtoReflect()
	for _, field := range f.Fields {
		if !field.Editable() || !m.Has(field.desc) {
			continue
		}
		out[field.Name] = field.format(m.Get(field.desc))
	}
	return out
}

// parse converts one raw form value into a protobuf value.
func (f Field) parse(raw string) (protoreflect.Value, error) {
	switch f.Kind {
	case KindString:
		return protoreflect.ValueOfString(raw), nil
	case KindBool:
		return parseBool(raw)
	case KindInt:
		return f.parseInt(raw)
	case KindUint:
		return f.parseUint(raw)
	case KindFloat:
		return f.parseFloat(raw)
	case KindBytes:
		return parseBytes(raw)
	case KindEnum:
		return f.parseEnum(raw)
	default:
		return protoreflect.Value{}, errors.New("field is not editable yet")
	}
}

// format renders a protobuf value as the text [Field.parse] accepts back.
func (f Field) format(v protoreflect.Value) string {
	switch f.Kind {
	case KindBytes:
		return base64.StdEncoding.EncodeToString(v.Bytes())
	case KindEnum:
		if ev := f.desc.Enum().Values().ByNumber(v.Enum()); ev != nil {
			return string(ev.Name())
		}
		return strconv.FormatInt(int64(v.Enum()), 10)
	case KindFloat:
		return strconv.FormatFloat(v.Float(), 'g', -1, f.bits())
	default:
		// String, bool and both integer families all round-trip through the
		// protobuf value's own formatting.
		return v.String()
	}
}

func parseBool(raw string) (protoreflect.Value, error) {
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return protoreflect.Value{}, errors.New("expected true or false")
	}
	return protoreflect.ValueOfBool(b), nil
}

func parseBytes(raw string) (protoreflect.Value, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return protoreflect.Value{}, errors.New("expected base64")
	}
	return protoreflect.ValueOfBytes(b), nil
}

func (f Field) parseInt(raw string) (protoreflect.Value, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, f.bits())
	if err != nil {
		return protoreflect.Value{}, f.numberError(err, "expected a whole number")
	}
	if f.bits() == 32 {
		//nolint:gosec // ParseInt with bitSize 32 already bounds n to int32.
		return protoreflect.ValueOfInt32(int32(n)), nil
	}
	return protoreflect.ValueOfInt64(n), nil
}

func (f Field) parseUint(raw string) (protoreflect.Value, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, f.bits())
	if err != nil {
		return protoreflect.Value{}, f.numberError(err, "expected a whole number that is not negative")
	}
	if f.bits() == 32 {
		//nolint:gosec // ParseUint with bitSize 32 already bounds n to uint32.
		return protoreflect.ValueOfUint32(uint32(n)), nil
	}
	return protoreflect.ValueOfUint64(n), nil
}

func (f Field) parseFloat(raw string) (protoreflect.Value, error) {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), f.bits())
	if err != nil {
		return protoreflect.Value{}, f.numberError(err, "expected a number")
	}
	if f.bits() == 32 {
		return protoreflect.ValueOfFloat32(float32(n)), nil
	}
	return protoreflect.ValueOfFloat64(n), nil
}

// parseEnum accepts a value name ("STATUS_SERVING") or a number ("1").
// Unrecognised numbers are allowed through: proto3 enums are open, and a server
// may well answer with a value this client's descriptor predates.
func (f Field) parseEnum(raw string) (protoreflect.Value, error) {
	name := strings.TrimSpace(raw)
	if ev := f.desc.Enum().Values().ByName(protoreflect.Name(name)); ev != nil {
		return protoreflect.ValueOfEnum(ev.Number()), nil
	}
	if n, err := strconv.ParseInt(name, 10, 32); err == nil {
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(n)), nil
	}

	names := make([]string, 0, len(f.Enum))
	for _, ev := range f.Enum {
		names = append(names, ev.Name)
	}
	return protoreflect.Value{}, fmt.Errorf("expected one of %s", strings.Join(names, ", "))
}

// numberError turns a strconv failure into something worth showing a user,
// keeping the range case distinct from the syntax case.
func (f Field) numberError(err error, syntax string) error {
	if errors.Is(err, strconv.ErrRange) {
		return fmt.Errorf("out of range for %s", f.Type)
	}
	return errors.New(syntax)
}

// bits reports the width of a numeric field, which decides both how strconv
// parses it and which protoreflect.ValueOf constructor stores it.
func (f Field) bits() int {
	switch f.desc.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.FloatKind:
		return 32
	default:
		return 64
	}
}
