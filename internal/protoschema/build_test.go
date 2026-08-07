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

// field looks a field descriptor up on a built message.
func field(t *testing.T, msg proto.Message, name string) (protoreflect.Message, protoreflect.FieldDescriptor) {
	t.Helper()

	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "no field %q on %s", name, m.Descriptor().FullName())
	return m, fd
}

// value reads a field back off a built message.
func value(t *testing.T, msg proto.Message, name string) any {
	t.Helper()

	m, fd := field(t, msg, name)
	return m.Get(fd).Interface()
}

// isSet reports whether a field was set at all, as opposed to being left at its
// default.
func isSet(t *testing.T, msg proto.Message, name string) bool {
	t.Helper()

	m, fd := field(t, msg, name)
	return m.Has(fd)
}

// build fills in one row and builds the request.
func build(t *testing.T, path, value string) proto.Message {
	t.Helper()
	return buildAll(t, map[string]string{path: value})
}

// buildAll fills in several rows, by path, and builds the request.
func buildAll(t *testing.T, values map[string]string) proto.Message {
	t.Helper()

	form := testForm(t)
	for path, value := range values {
		row(t, form, path).SetValue(value)
	}

	msg, err := form.Build()
	require.NoError(t, err)
	return msg
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
		// proto3 enums are open: a value this descriptor has never heard of is
		// still a legal thing to send, and a request reloaded out of history
		// (v0.6) can carry one.
		"enum by number":         {"colour", "99", protoreflect.EnumNumber(99)},
		"numbers tolerate space": {"count", "  12  ", int32(12)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, value(t, build(t, tt.field, tt.input), tt.field))
		})
	}
}

// "false" and "" are different answers for a bool, and asserting only the value
// cannot tell them apart: an unset proto3 bool reads back as false too. What
// distinguishes them is whether the field was set at all.
func TestForm_BuildDistinguishesFalseFromUnset(t *testing.T) {
	explicit := build(t, "flag", "false")
	assert.Equal(t, false, value(t, explicit, "flag"))

	// `flag` has no presence, so an explicit false is indistinguishable from
	// unset once it reaches the wire — which is exactly why the same value on a
	// field that does have presence has to survive.
	assert.False(t, isSet(t, explicit, "flag"))

	optional := build(t, "verbose", "false")
	assert.Equal(t, false, value(t, optional, "verbose"))
	assert.True(t, isSet(t, optional, "verbose"), "an optional bool must be sendable as an explicit false")

	untouched, err := testForm(t).Build()
	require.NoError(t, err)
	assert.False(t, isSet(t, untouched, "verbose"))
}

// An empty value means "leave this field alone". Setting it explicitly would
// put every skipped field on the wire, which is both wrong and noisy.
func TestForm_BuildLeavesEmptyValuesUnset(t *testing.T) {
	form := testForm(t)
	row(t, form, "flag").SetValue("true")

	msg, err := form.Build()

	require.NoError(t, err)
	assert.False(t, isSet(t, msg, "text"))
	assert.False(t, isSet(t, msg, "count"))
	assert.True(t, isSet(t, msg, "flag"))
}

// A field with explicit presence is the one case where "" is a value rather
// than the absence of one, and the caller has no other way to say it.
func TestForm_BuildSendsAnExplicitEmptyValueForPresenceFields(t *testing.T) {
	t.Run("filled in and then cleared is explicit", func(t *testing.T) {
		msg := build(t, "note", "")

		assert.True(t, isSet(t, msg, "note"), `an optional string must be sendable as ""`)
		assert.Empty(t, value(t, msg, "note"))
	})

	t.Run("never visited is still unset", func(t *testing.T) {
		msg := build(t, "text", "elsewhere")

		assert.False(t, isSet(t, msg, "note"),
			"a field nobody touched must not be sent, however empty the one beside it is")
	})

	// A number has no empty form to mean: an empty box is nothing typed, not a
	// value, so presence changes nothing here.
	t.Run("numbers with presence are not affected", func(t *testing.T) {
		assert.False(t, isSet(t, build(t, "count", ""), "count"))
	})
}

func TestForm_BuildNestedMessage(t *testing.T) {
	t.Run("untouched is left out", func(t *testing.T) {
		msg, err := testForm(t).Build()

		require.NoError(t, err)
		assert.False(t, isSet(t, msg, "nested"))
	})

	t.Run("a value inside brings the message with it", func(t *testing.T) {
		form := testForm(t)
		open(t, form, "nested")
		row(t, form, "nested.note").SetValue("inside")

		msg, err := form.Build()
		require.NoError(t, err)

		require.True(t, isSet(t, msg, "nested"))
		m, fd := field(t, msg, "nested")
		nested := m.Get(fd).Message()
		assert.Equal(t, "inside", nested.Get(nested.Descriptor().Fields().ByName("note")).String())
	})

	// An empty message is a real thing to send, and the only way to say so is to
	// say so: nothing inside it can carry the intent.
	t.Run("an empty message can be sent deliberately", func(t *testing.T) {
		form := testForm(t)
		row(t, form, "nested").Toggle()

		msg, err := form.Build()
		require.NoError(t, err)
		assert.True(t, isSet(t, msg, "nested"))
	})
}

func TestForm_BuildRepeatedFields(t *testing.T) {
	t.Run("scalars keep their order", func(t *testing.T) {
		form := testForm(t)
		tags := row(t, form, "tags")
		for _, v := range []string{"first", "second"} {
			tags.AddItem().SetValue(v)
		}

		msg, err := form.Build()
		require.NoError(t, err)

		m, fd := field(t, msg, "tags")
		list := m.Get(fd).List()
		require.Equal(t, 2, list.Len())
		assert.Equal(t, "first", list.Get(0).String())
		assert.Equal(t, "second", list.Get(1).String())
	})

	// An item is on screen because the user put it there, so it is sent even
	// empty — unlike a field that was simply never filled in.
	t.Run("an empty item is still an item", func(t *testing.T) {
		form := testForm(t)
		row(t, form, "tags").AddItem()

		msg, err := form.Build()
		require.NoError(t, err)

		m, fd := field(t, msg, "tags")
		require.Equal(t, 1, m.Get(fd).List().Len())
		assert.Empty(t, m.Get(fd).List().Get(0).String())
	})

	t.Run("no items means the field is not sent", func(t *testing.T) {
		msg, err := testForm(t).Build()

		require.NoError(t, err)
		assert.False(t, isSet(t, msg, "tags"))
	})

	t.Run("messages", func(t *testing.T) {
		form := testForm(t)
		item := row(t, form, "notes").AddItem()
		require.NotNil(t, item)
		row(t, form, "notes[0].note").SetValue("a note")

		msg, err := form.Build()
		require.NoError(t, err)

		m, fd := field(t, msg, "notes")
		list := m.Get(fd).List()
		require.Equal(t, 1, list.Len())

		note := list.Get(0).Message()
		assert.Equal(t, "a note", note.Get(note.Descriptor().Fields().ByName("note")).String())
	})

	// An item left empty is sent as its type's zero value, whatever that type
	// is. Each one goes through a different protoreflect constructor, and the
	// wrong constructor is a panic rather than a wrong answer.
	t.Run("an empty item of every element type", func(t *testing.T) {
		zeros := map[string]any{
			"ints":    int32(0),
			"longs":   int64(0),
			"uints":   uint32(0),
			"ulongs":  uint64(0),
			"floats":  float32(0),
			"doubles": float64(0),
			"flags":   false,
			"blobs":   []byte(nil),
			"colours": protoreflect.EnumNumber(0),
			"texts":   "",
		}

		form := protoschema.NewForm(listsDescriptor(t))
		for name := range zeros {
			row(t, form, name).AddItem()
		}

		msg, err := form.Build()
		require.NoError(t, err)

		for name, want := range zeros {
			m, fd := field(t, msg, name)
			list := m.Get(fd).List()
			require.Equal(t, 1, list.Len(), "%s lost its item", name)
			assert.Equal(t, want, list.Get(0).Interface(), "%s is not at its zero value", name)
		}
	})

	t.Run("a bad item is reported against the item", func(t *testing.T) {
		form := protoschema.NewForm(listsDescriptor(t))
		row(t, form, "ints").AddItem().SetValue("twelve")

		_, err := form.Build()

		var fieldErr *protoschema.FieldError
		require.ErrorAs(t, err, &fieldErr)
		assert.Equal(t, "ints[0]", fieldErr.Path)
	})

	t.Run("a bad item names itself", func(t *testing.T) {
		form := testForm(t)
		row(t, form, "notes").AddItem()
		row(t, form, "notes[0].depth").SetValue("deep")

		_, err := form.Build()

		var fieldErr *protoschema.FieldError
		require.ErrorAs(t, err, &fieldErr)
		assert.Equal(t, "notes[0].depth", fieldErr.Path)
	})
}

func TestForm_BuildMap(t *testing.T) {
	form := testForm(t)
	row(t, form, "labels").AddItem()
	row(t, form, "labels[0].key").SetValue("env")
	row(t, form, "labels[0].value").SetValue("staging")

	msg, err := form.Build()
	require.NoError(t, err)

	m, fd := field(t, msg, "labels")
	labels := m.Get(fd).Map()
	require.Equal(t, 1, labels.Len())
	assert.Equal(t, "staging", labels.Get(protoreflect.ValueOfString("env").MapKey()).String())
}

func TestForm_BuildOneof(t *testing.T) {
	t.Run("only the picked variant is sent", func(t *testing.T) {
		form := testForm(t)
		open(t, form, "choice")
		row(t, form, "choice.by_name").SetValue("ada")
		row(t, form, "choice.by_id").SetValue("7")
		row(t, form, "choice.by_id").Toggle()

		msg, err := form.Build()
		require.NoError(t, err)

		assert.True(t, isSet(t, msg, "by_id"))
		assert.False(t, isSet(t, msg, "by_name"), "a oneof sends one variant, not the last one typed into")
		assert.Equal(t, int32(7), value(t, msg, "by_id"))
	})

	// Picking a variant is itself the value: an empty by_name is a request for
	// an empty by_name, which on the wire is a different thing from no oneof.
	t.Run("a picked variant with no value is still sent", func(t *testing.T) {
		form := testForm(t)
		open(t, form, "choice")
		row(t, form, "choice.by_name").Toggle()

		msg, err := form.Build()
		require.NoError(t, err)

		assert.True(t, isSet(t, msg, "by_name"))
		assert.Empty(t, value(t, msg, "by_name"))
	})

	t.Run("a message variant can be picked", func(t *testing.T) {
		form := testForm(t)
		open(t, form, "choice")
		row(t, form, "choice.by_nested").Toggle()

		msg, err := form.Build()
		require.NoError(t, err)
		assert.True(t, isSet(t, msg, "by_nested"))
	})

	t.Run("picking nothing sends nothing", func(t *testing.T) {
		form := testForm(t)
		open(t, form, "choice")
		row(t, form, "choice.by_name").SetValue("ada")
		row(t, form, "choice.by_nested").Toggle()

		msg, err := form.Build()
		require.NoError(t, err)
		assert.False(t, isSet(t, msg, "by_name"))
	})
}

func TestForm_BuildRejectsBadValues(t *testing.T) {
	tests := map[string]struct {
		field string
		input string
		want  string
	}{
		"not a number":    {"count", "twelve", "expected a whole number"},
		"float in an int": {"count", "1.5", "expected a whole number"},
		"int32 overflow":  {"count", "2147483648", "out of range for int32"},
		"negative uint":   {"port", "-1", "expected a whole number that is not negative"},
		"uint64 overflow": {"size", "18446744073709551616", "out of range for uint64"},
		"not a float":     {"ratio", "wide", "expected a number"},
		"not a bool":      {"flag", "yes please", "expected true or false"},
		"not base64":      {"payload", "not base64!", "expected base64"},
		// The picker cannot produce this, but a request reloaded from a file
		// (v0.6) can, and the message has to say what would have been legal.
		"unknown enum name": {"colour", "COLOUR_TEAL", "expected one of COLOUR_UNSPECIFIED, COLOUR_RED, COLOUR_BLUE"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			form := testForm(t)
			row(t, form, tt.field).SetValue(tt.input)

			msg, err := form.Build()

			require.Error(t, err)
			assert.Nil(t, msg)

			var fieldErr *protoschema.FieldError
			require.ErrorAs(t, err, &fieldErr, "a bad value must name its field")
			assert.Equal(t, tt.field, fieldErr.Path)
			assert.Equal(t, tt.field, fieldErr.Name)
			assert.Equal(t, tt.input, fieldErr.Value)
			assert.Contains(t, err.Error(), tt.want)
			assert.EqualError(t, fieldErr.Unwrap(), tt.want,
				"the wrapped error is the message a row shows, without the path in front of it")
		})
	}
}

// Reporting one bad field at a time turns filling in a form into a guessing
// game, so every failure is collected.
func TestForm_BuildReportsEveryBadValue(t *testing.T) {
	form := testForm(t)
	row(t, form, "count").SetValue("twelve")
	row(t, form, "flag").SetValue("maybe")
	row(t, form, "ratio").SetValue("wide")

	_, err := form.Build()
	require.Error(t, err)

	var joined interface{ Unwrap() []error }
	require.ErrorAs(t, err, &joined)

	paths := make([]string, 0, len(joined.Unwrap()))
	for _, e := range joined.Unwrap() {
		var fieldErr *protoschema.FieldError
		require.ErrorAs(t, e, &fieldErr)
		paths = append(paths, fieldErr.Path)
	}
	assert.ElementsMatch(t, []string{"count", "flag", "ratio"}, paths)
}

// Validate is what the panel calls as an edit ends, so that a typo is reported
// where it was made rather than when the call is sent.
func TestNode_Validate(t *testing.T) {
	form := testForm(t)

	count := row(t, form, "count")
	require.NoError(t, count.Validate(), "an empty field is not yet wrong")

	count.SetValue("twelve")
	require.Error(t, count.Validate())

	count.SetValue("12")
	require.NoError(t, count.Validate())
}

func TestForm_Validate(t *testing.T) {
	form := testForm(t)
	require.NoError(t, form.Validate())

	row(t, form, "port").SetValue("-1")
	require.Error(t, form.Validate())
}

// proto2's `required` is the one thing a form can be incomplete about. proto3
// dropped it, so most requests can never fail this way — but a client that
// silently sends an unmarshallable message is worse than one that says why.
func TestForm_BuildRequiredFields(t *testing.T) {
	newTicket := func(t *testing.T) protoschema.Form {
		t.Helper()
		return protoschema.NewForm(requiredDescriptor(t))
	}

	t.Run("a missing required field is reported", func(t *testing.T) {
		_, err := newTicket(t).Build()

		var fieldErr *protoschema.FieldError
		require.ErrorAs(t, err, &fieldErr)
		assert.Equal(t, "id", fieldErr.Path)
		assert.Contains(t, err.Error(), "this field is required")
	})

	t.Run("filling it in is enough", func(t *testing.T) {
		form := newTicket(t)
		row(t, form, "id").SetValue("T-1")

		_, err := form.Build()
		require.NoError(t, err)
	})

	// A message that is not being sent cannot be incomplete: the required field
	// inside an optional message only matters once the message itself does.
	t.Run("an untouched nested message is not incomplete", func(t *testing.T) {
		form := newTicket(t)
		row(t, form, "id").SetValue("T-1")
		open(t, form, "stamp")

		_, err := form.Build()
		require.NoError(t, err)
	})

	t.Run("a nested message being sent is checked", func(t *testing.T) {
		form := newTicket(t)
		row(t, form, "id").SetValue("T-1")
		row(t, form, "stamp").Toggle()

		_, err := form.Build()

		var fieldErr *protoschema.FieldError
		require.ErrorAs(t, err, &fieldErr)
		assert.Equal(t, "stamp.at", fieldErr.Path)
	})
}

// Build and Load are inverses: what goes into a request comes back out of it.
// v0.6 reloads saved requests through exactly this path.
func TestForm_RoundTrip(t *testing.T) {
	form := testForm(t)

	scalars := map[string]string{
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
	for path, v := range scalars {
		row(t, form, path).SetValue(v)
	}

	row(t, form, "tags").AddItem().SetValue("one")
	row(t, form, "tags").AddItem().SetValue("two")

	row(t, form, "labels").AddItem()
	row(t, form, "labels[0].key").SetValue("env")
	row(t, form, "labels[0].value").SetValue("staging")

	open(t, form, "nested")
	row(t, form, "nested.note").SetValue("inside")
	row(t, form, "nested.depth").SetValue("3")

	row(t, form, "notes").AddItem()
	row(t, form, "notes[0].note").SetValue("listed")

	open(t, form, "choice")
	row(t, form, "choice.by_id").SetValue("11")

	want, err := form.Build()
	require.NoError(t, err)

	reloaded := testForm(t)
	reloaded.Load(want)
	got, err := reloaded.Build()
	require.NoError(t, err)

	assert.True(t, proto.Equal(want, got), "reloading a request changed it:\nwant %v\ngot  %v", want, got)
}

// The half of the open-enum contract the parse side cannot reach: a number with
// no name has to come back out as a number.
func TestForm_LoadFormatsAnUnknownEnumNumber(t *testing.T) {
	msg := build(t, "colour", "99")

	reloaded := testForm(t)
	reloaded.Load(msg)

	assert.Equal(t, "99", row(t, reloaded, "colour").Value())
}

func TestForm_LoadOfNilMessage(t *testing.T) {
	form := testForm(t)
	form.Load(nil)

	msg, err := form.Build()
	require.NoError(t, err)
	assert.False(t, isSet(t, msg, "text"))
}
