package protoschema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/alonshuld/grpctui/internal/protoschema"
)

func TestWire(t *testing.T) {
	desc := scalarsDescriptor(t)
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("text"), protoreflect.ValueOfString("hello"))

	raw, err := protoschema.Wire(msg)
	require.NoError(t, err)

	// The bytes have to decode back to the same message, or the raw view is
	// showing something the response never was.
	back := dynamicpb.NewMessage(desc)
	require.NoError(t, proto.Unmarshal(raw, back))
	assert.True(t, proto.Equal(msg, back))
}

func TestWireIsStable(t *testing.T) {
	desc := scalarsDescriptor(t)
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("text"), protoreflect.ValueOfString("hello"))
	msg.Set(desc.Fields().ByName("count"), protoreflect.ValueOfInt32(7))

	first, err := protoschema.Wire(msg)
	require.NoError(t, err)
	second, err := protoschema.Wire(msg)
	require.NoError(t, err)

	assert.Equal(t, first, second, "two views of one message must not differ")
}

func TestWireNilMessage(t *testing.T) {
	_, err := protoschema.Wire(nil)
	assert.Error(t, err)
}

func TestWireFields(t *testing.T) {
	// Built by hand rather than from a descriptor: the point of the raw view is
	// that it reads bytes nobody has a schema for.
	var raw []byte
	raw = protowire.AppendTag(raw, 1, protowire.BytesType)
	raw = protowire.AppendString(raw, "world")
	raw = protowire.AppendTag(raw, 2, protowire.VarintType)
	raw = protowire.AppendVarint(raw, 42)
	raw = protowire.AppendTag(raw, 3, protowire.Fixed32Type)
	raw = protowire.AppendFixed32(raw, 0xdeadbeef)
	raw = protowire.AppendTag(raw, 4, protowire.Fixed64Type)
	raw = protowire.AppendFixed64(raw, 1)

	fields, err := protoschema.WireFields(raw)
	require.NoError(t, err)
	require.Len(t, fields, 4)

	assert.Equal(t, protoschema.WireField{Number: 1, Type: "len", Size: 7, Value: `"world"`}, fields[0])
	assert.Equal(t, protoschema.WireField{Number: 2, Type: "varint", Size: 2, Value: "42"}, fields[1])
	assert.Equal(t, protoschema.WireField{Number: 3, Type: "32-bit", Size: 5, Value: "0xdeadbeef"}, fields[2])
	assert.Equal(t, protoschema.WireField{Number: 4, Type: "64-bit", Size: 9, Value: "0x0000000000000001"}, fields[3])
}

func TestWireFieldsBinaryPayload(t *testing.T) {
	var raw []byte
	raw = protowire.AppendTag(raw, 1, protowire.BytesType)
	raw = protowire.AppendBytes(raw, []byte{0x00, 0x01, 0x02, 0xff})

	fields, err := protoschema.WireFields(raw)
	require.NoError(t, err)
	require.Len(t, fields, 1)
	assert.Equal(t, "4 B", fields[0].Value, "binary is reported by size, not spelled out")
}

func TestWireFieldsLongString(t *testing.T) {
	var raw []byte
	raw = protowire.AppendTag(raw, 1, protowire.BytesType)
	raw = protowire.AppendString(raw, strings.Repeat("a", 100))

	fields, err := protoschema.WireFields(raw)
	require.NoError(t, err)
	require.Len(t, fields, 1)
	assert.Contains(t, fields[0].Value, "…")
	assert.Contains(t, fields[0].Value, "100 B")
}

func TestWireFieldsEmptyString(t *testing.T) {
	var raw []byte
	raw = protowire.AppendTag(raw, 1, protowire.BytesType)
	raw = protowire.AppendString(raw, "")

	fields, err := protoschema.WireFields(raw)
	require.NoError(t, err)
	require.Len(t, fields, 1)
	assert.Equal(t, `""`, fields[0].Value)
}

func TestWireFieldsEmptyBuffer(t *testing.T) {
	fields, err := protoschema.WireFields(nil)
	require.NoError(t, err)
	assert.Empty(t, fields)
}

// A body that goes wrong halfway is exactly the body somebody opened the raw
// view to look at, so what was readable before the damage still comes back.
func TestWireFieldsTruncated(t *testing.T) {
	var raw []byte
	raw = protowire.AppendTag(raw, 1, protowire.BytesType)
	raw = protowire.AppendString(raw, "ok")
	raw = protowire.AppendTag(raw, 2, protowire.BytesType)
	raw = append(raw, 0x20) // says twenty bytes follow; none do

	fields, err := protoschema.WireFields(raw)
	require.Error(t, err)
	require.Len(t, fields, 1)
	assert.Equal(t, `"ok"`, fields[0].Value)
}

func TestHexdump(t *testing.T) {
	dump := protoschema.Hexdump([]byte("hello, world"), 8)

	assert.Equal(t, strings.Join([]string{
		"00000000  68 65 6c 6c  6f 2c 20 77  |hello, w|",
		"00000008  6f 72 6c 64               |orld|",
	}, "\n"), dump)
}

func TestHexdumpEmpty(t *testing.T) {
	assert.Empty(t, protoschema.Hexdump(nil, 16))
}

func TestHexdumpNonPrintable(t *testing.T) {
	dump := protoschema.Hexdump([]byte{0x00, 0x7f, 0x41}, 16)
	assert.Contains(t, dump, "|..A|")
}

func TestByteCount(t *testing.T) {
	tests := map[int]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1.0 KB",
		1536:    "1.5 KB",
		1 << 20: "1.0 MB",
	}
	for n, want := range tests {
		t.Run(want, func(t *testing.T) {
			assert.Equal(t, want, protoschema.ByteCount(n))
		})
	}
}
