package protoschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"github.com/alonshuld/grpctui/internal/protoschema"
)

func TestEncodeBody(t *testing.T) {
	msg := buildAll(t, map[string]string{
		"text":   "hello",
		"count":  "7",
		"total":  "9007199254740993",
		"colour": "COLOUR_BLUE",
	})

	body, err := protoschema.EncodeBody(msg)
	require.NoError(t, err)

	fields, ok := body.(map[string]any)
	require.True(t, ok, "want a mapping, got %T", body)

	assert.Equal(t, "hello", fields["text"])
	assert.InDelta(t, 7.0, fields["count"], 0)
	assert.Equal(t, "COLOUR_BLUE", fields["colour"])

	t.Run("keeps protobuf's JSON mapping", func(t *testing.T) {
		assert.Equal(t, "9007199254740993", fields["total"],
			"64-bit integers are strings in protobuf JSON, because JSON numbers lose them")
	})

	t.Run("leaves out what the user did not fill in", func(t *testing.T) {
		assert.NotContains(t, fields, "flag")
		assert.NotContains(t, fields, "tags")
		assert.NotContains(t, fields, "retryCount")
	})
}

func TestEncodeBody_NoMessage(t *testing.T) {
	body, err := protoschema.EncodeBody(nil)
	require.NoError(t, err)
	assert.Nil(t, body)
}

func TestDecodeBody(t *testing.T) {
	msg, err := protoschema.DecodeBody(scalarsDescriptor(t), map[string]any{
		"text":   "hello",
		"count":  7,
		"total":  "9007199254740993",
		"colour": "COLOUR_BLUE",
	})
	require.NoError(t, err)

	assert.Equal(t, "hello", value(t, msg, "text"))
	assert.Equal(t, int32(7), value(t, msg, "count"))
	assert.Equal(t, int64(9007199254740993), value(t, msg, "total"))
}

func TestDecodeBody_Empty(t *testing.T) {
	msg, err := protoschema.DecodeBody(scalarsDescriptor(t), nil)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.False(t, isSet(t, msg, "text"))
}

// A collection outlives the schema it was written against. A field that has
// since been renamed has to be reported, not skipped: a request quietly sent
// without the field you thought you had set is the failure this feature exists
// to prevent.
func TestDecodeBody_UnknownField(t *testing.T) {
	_, err := protoschema.DecodeBody(scalarsDescriptor(t), map[string]any{"nope": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "grpctui.test.v1.Scalars")
}

func TestDecodeBody_NoDescriptor(t *testing.T) {
	_, err := protoschema.DecodeBody(nil, map[string]any{"text": "x"})
	require.Error(t, err)
}

// A body read back out of a hand-written collection arrives with map[any]any
// mappings when YAML could not prove every key was a string. Those still have
// to decode, and a key that genuinely is not a string has to say so.
func TestDecodeBody_YAMLMappings(t *testing.T) {
	t.Run("string keys", func(t *testing.T) {
		var body any
		require.NoError(t, yaml.Unmarshal([]byte("text: hello\nnested:\n  note: hi\n"), &body))

		msg, err := protoschema.DecodeBody(scalarsDescriptor(t), body)
		require.NoError(t, err)
		assert.Equal(t, "hello", value(t, msg, "text"))
	})

	t.Run("a key that is not a string", func(t *testing.T) {
		_, err := protoschema.DecodeBody(scalarsDescriptor(t), map[any]any{1: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a string")
	})
}

// The pair is what history and collections rely on: a request saved and
// reloaded has to be the request that was sent, field for field.
func TestBodyRoundTrip(t *testing.T) {
	cases := map[string]struct {
		open   []string
		values map[string]string
	}{
		"scalars": {values: map[string]string{
			"text":   "hello",
			"count":  "7",
			"total":  "9007199254740993",
			"ratio":  "1.5",
			"colour": "COLOUR_BLUE",
		}},
		"a nested message": {
			open:   []string{"nested"},
			values: map[string]string{"nested.note": "inside", "nested.depth": "2"},
		},
		"a oneof variant": {
			open:   []string{"choice"},
			values: map[string]string{"choice.by_name": "picked"},
		},
		"an explicit empty string": {values: map[string]string{
			// A field with presence set to "" is not the same as one never
			// touched, and the JSON mapping is the only thing carrying that.
			"note": "",
		}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			form := testForm(t)
			for _, path := range tc.open {
				open(t, form, path)
			}
			for path, v := range tc.values {
				row(t, form, path).SetValue(v)
			}

			sent, err := form.Build()
			require.NoError(t, err)

			body, err := protoschema.EncodeBody(sent)
			require.NoError(t, err)

			// Through YAML and back, which is the trip a collection actually
			// makes: what survives an in-memory round trip is not the question.
			encoded, err := yaml.Marshal(body)
			require.NoError(t, err)
			var reloaded any
			require.NoError(t, yaml.Unmarshal(encoded, &reloaded))

			got, err := protoschema.DecodeBody(scalarsDescriptor(t), reloaded)
			require.NoError(t, err)
			assert.True(t, proto.Equal(sent, got),
				"round trip changed the message\nsent: %v\ngot:  %v", sent, got)
		})
	}
}

// The oneof picker has to come back active, or reloading a request would leave
// the form showing a variant nobody had chosen.
func TestBodyRoundTrip_LoadsIntoAForm(t *testing.T) {
	form := testForm(t)
	open(t, form, "choice")
	row(t, form, "choice.by_id").SetValue("42")
	row(t, form, "text").SetValue("hello")

	sent, err := form.Build()
	require.NoError(t, err)

	body, err := protoschema.EncodeBody(sent)
	require.NoError(t, err)

	restored, err := protoschema.DecodeBody(scalarsDescriptor(t), body)
	require.NoError(t, err)

	reloaded := testForm(t)
	reloaded.Load(restored)

	assert.Equal(t, "hello", row(t, reloaded, "text").Value())
	assert.Equal(t, "by_id", row(t, reloaded, "choice").Active())

	open(t, reloaded, "choice")
	assert.Equal(t, "42", row(t, reloaded, "choice.by_id").Value())
}
