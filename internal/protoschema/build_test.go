package protoschema_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/alonshuld/grpctui/internal/protoschema"
)

// value reads a field back off a built message.
func value(t *testing.T, msg proto.Message, name string) any {
	t.Helper()

	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "no field %q on %s", name, m.Descriptor().FullName())
	return m.Get(fd).Interface()
}

// isSet reports whether a field was set at all, as opposed to being left at its
// default.
func isSet(t *testing.T, msg proto.Message, name string) bool {
	t.Helper()

	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "no field %q on %s", name, m.Descriptor().FullName())
	return m.Has(fd)
}

func TestForm_Build(t *testing.T) {
	tests := map[string]struct {
		field string
		input string
		want  any
	}{
		"string":              {"text", "hello", "hello"},
		"string keeps spaces": {"text", "  padded  ", "  padded  "},
		"bool true":           {"flag", "true", true},
		"int32":               {"count", "-7", int32(-7)},
		"int32 at max":        {"count", "2147483647", int32(math.MaxInt32)},
		"int64":               {"total", "9007199254740993", int64(9007199254740993)},
		"sint32":              {"delta", "-3", int32(-3)},
		"uint32":              {"port", "50051", uint32(50051)},
		"uint64":              {"size", "18446744073709551615", uint64(math.MaxUint64)},
		"fixed64":             {"offset", "42", uint64(42)},
		"float":               {"ratio", "1.5", float32(1.5)},
		"double":              {"weight", "2.25", 2.25},
		"bytes as base64":     {"payload", "aGVsbG8=", []byte("hello")},
		"enum by name":        {"colour", "COLOUR_BLUE", protoreflect.EnumNumber(2)},
		"enum by number":      {"colour", "2", protoreflect.EnumNumber(2)},
		// proto3 enums are open: a value this descriptor has never heard of is
		// still a legal thing to send.
		"unknown enum number":    {"colour", "99", protoreflect.EnumNumber(99)},
		"numbers tolerate space": {"count", "  12  ", int32(12)},
	}

	form := testForm(t)
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			msg, err := form.Build(map[string]string{tt.field: tt.input})

			require.NoError(t, err)
			assert.Equal(t, tt.want, value(t, msg, tt.field))
		})
	}
}

// "false" and "" are different answers for a bool, and asserting only the value
// cannot tell them apart: an unset proto3 bool reads back as false too. What
// distinguishes them is whether the field was set at all.
func TestForm_BuildDistinguishesFalseFromUnset(t *testing.T) {
	form := testForm(t)

	explicit, err := form.Build(map[string]string{"flag": "false"})
	require.NoError(t, err)
	assert.Equal(t, false, value(t, explicit, "flag"))

	// `flag` has no presence, so an explicit false is indistinguishable from
	// unset once it reaches the wire — which is exactly why the same value on a
	// field that does have presence has to survive.
	assert.False(t, isSet(t, explicit, "flag"))

	optional, err := form.Build(map[string]string{"verbose": "false"})
	require.NoError(t, err)
	assert.Equal(t, false, value(t, optional, "verbose"))
	assert.True(t, isSet(t, optional, "verbose"), "an optional bool must be sendable as an explicit false")

	absent, err := form.Build(map[string]string{})
	require.NoError(t, err)
	assert.False(t, isSet(t, absent, "verbose"))
}

// An empty value means "leave this field alone". Setting it explicitly would
// put every skipped field on the wire, which is both wrong and noisy.
func TestForm_BuildLeavesEmptyValuesUnset(t *testing.T) {
	form := testForm(t)

	msg, err := form.Build(map[string]string{
		"text":  "",
		"count": "",
		"flag":  "true",
	})

	require.NoError(t, err)
	assert.False(t, isSet(t, msg, "text"))
	assert.False(t, isSet(t, msg, "count"))
	assert.True(t, isSet(t, msg, "flag"))
}

// A field with explicit presence is the one case where "" is a value rather
// than the absence of one, and the caller has no other way to say it.
func TestForm_BuildSendsAnExplicitEmptyValueForPresenceFields(t *testing.T) {
	form := testForm(t)

	t.Run("present and empty is explicit", func(t *testing.T) {
		msg, err := form.Build(map[string]string{"note": ""})

		require.NoError(t, err)
		assert.True(t, isSet(t, msg, "note"), `an optional string must be sendable as ""`)
		assert.Empty(t, value(t, msg, "note"))
	})

	t.Run("absent is still unset", func(t *testing.T) {
		msg, err := form.Build(map[string]string{"text": "elsewhere"})

		require.NoError(t, err)
		assert.False(t, isSet(t, msg, "note"),
			"a field nobody filled in must not be sent; the form leaves it out of the map")
	})

	// A number has no empty form to mean: an empty box is nothing typed, not a
	// value, so presence changes nothing here.
	t.Run("numbers with presence are not affected", func(t *testing.T) {
		msg, err := form.Build(map[string]string{"count": ""})

		require.NoError(t, err)
		assert.False(t, isSet(t, msg, "count"))
	})
}

// Round-tripping an explicitly empty optional keeps it explicit, which is what
// v0.6 needs to reload such a request out of history without quietly dropping
// the field.
func TestForm_RoundTripKeepsAnExplicitEmptyValue(t *testing.T) {
	form := testForm(t)

	msg, err := form.Build(map[string]string{"note": ""})
	require.NoError(t, err)

	values := form.Values(msg)
	assert.Equal(t, map[string]string{"note": ""}, values)

	again, err := form.Build(values)
	require.NoError(t, err)
	assert.True(t, isSet(t, again, "note"))
}

func TestForm_BuildIgnoresUnsupportedAndUnknownFields(t *testing.T) {
	form := testForm(t)

	msg, err := form.Build(map[string]string{
		"nested":  "{}",
		"tags":    "a,b",
		"by_name": "x",
		"nope":    "unknown field name",
		"text":    "kept",
	})

	require.NoError(t, err, "fields the form cannot edit must not fail the build")
	assert.Equal(t, "kept", value(t, msg, "text"))
	assert.False(t, isSet(t, msg, "nested"))
	assert.False(t, isSet(t, msg, "by_name"))
}

func TestForm_BuildRejectsBadValues(t *testing.T) {
	tests := map[string]struct {
		field string
		input string
		want  string
	}{
		"not a number":      {"count", "twelve", "expected a whole number"},
		"float in an int":   {"count", "1.5", "expected a whole number"},
		"int32 overflow":    {"count", "2147483648", "out of range for int32"},
		"negative uint":     {"port", "-1", "expected a whole number that is not negative"},
		"uint64 overflow":   {"size", "18446744073709551616", "out of range for uint64"},
		"not a float":       {"ratio", "wide", "expected a number"},
		"not a bool":        {"flag", "yes please", "expected true or false"},
		"not base64":        {"payload", "not base64!", "expected base64"},
		"unknown enum name": {"colour", "COLOUR_TEAL", "expected one of COLOUR_UNSPECIFIED, COLOUR_RED, COLOUR_BLUE"},
	}

	form := testForm(t)
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			msg, err := form.Build(map[string]string{tt.field: tt.input})

			require.Error(t, err)
			assert.Nil(t, msg)

			var fieldErr *protoschema.FieldError
			require.ErrorAs(t, err, &fieldErr, "a bad value must name its field")
			assert.Equal(t, tt.field, fieldErr.Field)
			assert.Equal(t, tt.input, fieldErr.Value)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// Reporting one bad field at a time turns filling in a form into a guessing
// game, so every failure is collected.
func TestForm_BuildReportsEveryBadValue(t *testing.T) {
	form := testForm(t)

	_, err := form.Build(map[string]string{
		"count": "twelve",
		"flag":  "maybe",
		"ratio": "wide",
	})

	require.Error(t, err)

	var joined interface{ Unwrap() []error }
	require.ErrorAs(t, err, &joined)

	names := make([]string, 0, len(joined.Unwrap()))
	for _, e := range joined.Unwrap() {
		var fieldErr *protoschema.FieldError
		require.ErrorAs(t, e, &fieldErr)
		names = append(names, fieldErr.Field)
	}
	assert.ElementsMatch(t, []string{"count", "flag", "ratio"}, names)
}

// Build and Values are inverses: what goes into a request comes back out of it.
// v0.6 reloads saved requests through exactly this path.
func TestForm_RoundTrip(t *testing.T) {
	form := testForm(t)

	values := map[string]string{
		"text":    "hello",
		"flag":    "true",
		"count":   "-7",
		"total":   "9007199254740993",
		"port":    "50051",
		"size":    "18446744073709551615",
		"delta":   "-3",
		"offset":  "42",
		"ratio":   "1.5",
		"weight":  "2.25",
		"payload": "aGVsbG8=",
		"colour":  "COLOUR_BLUE",
		"note":    "an optional note",
	}

	msg, err := form.Build(values)
	require.NoError(t, err)

	assert.Equal(t, values, form.Values(msg))
}

// proto3 enums are open, so a number this descriptor has never heard of is a
// legal thing to send — and has to come back out again. There is no name to
// give it, so Values falls back to the number, which is the half of the open
// enum contract that the parse-side test cannot reach.
func TestForm_ValuesFormatsAnUnknownEnumNumber(t *testing.T) {
	form := testForm(t)

	msg, err := form.Build(map[string]string{"colour": "99"})
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"colour": "99"}, form.Values(msg))
}

func TestForm_ValuesOmitsUnsetFields(t *testing.T) {
	form := testForm(t)

	msg, err := form.Build(map[string]string{"text": "only this"})
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"text": "only this"}, form.Values(msg))
}

func TestForm_ValuesOfNilMessage(t *testing.T) {
	assert.Empty(t, testForm(t).Values(nil))
}
