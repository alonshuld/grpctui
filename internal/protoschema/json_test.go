package protoschema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/alonshuld/grpctui/internal/protoschema"
)

func TestMarshalJSON(t *testing.T) {
	msg := buildAll(t, map[string]string{
		"text":   "hello",
		"count":  "7",
		"total":  "9007199254740993",
		"colour": "COLOUR_BLUE",
	})

	out, err := protoschema.MarshalJSON(msg)
	require.NoError(t, err)

	assert.Contains(t, out, `"text": "hello"`)
	assert.Contains(t, out, `"count": 7`)
	assert.Contains(t, out, `"colour": "COLOUR_BLUE"`)

	t.Run("keeps protobuf's JSON mapping", func(t *testing.T) {
		assert.Contains(t, out, `"total": "9007199254740993"`,
			"64-bit integers are strings in protobuf JSON, because JSON numbers lose them")
		assert.Contains(t, out, `"retryCount": 0`, "field names are lowerCamelCase in protobuf JSON")
	})

	t.Run("shows fields the server left at their default", func(t *testing.T) {
		assert.Contains(t, out, `"flag": false`)
		assert.Contains(t, out, `"tags": []`)
	})

	t.Run("indents one level per depth", func(t *testing.T) {
		assert.True(t, strings.HasPrefix(out, "{\n  \""), "unexpected start:\n%s", out)
		assert.True(t, strings.HasSuffix(out, "\n}"), "unexpected end:\n%s", out)
	})
}

// protojson deliberately varies its own whitespace between builds, which would
// make the response panel's output unstable and its golden files unusable. The
// re-indent step is what stops that leaking out of this package.
func TestMarshalJSON_IsStable(t *testing.T) {
	msg := buildAll(t, map[string]string{"text": "hello", "count": "7"})

	first, err := protoschema.MarshalJSON(msg)
	require.NoError(t, err)

	for range 5 {
		again, err := protoschema.MarshalJSON(msg)
		require.NoError(t, err)
		require.Equal(t, first, again)
	}

	assert.NotContains(t, first, `:  `, "protojson's random double space reached the output")
}

func TestMarshalJSON_NilMessage(t *testing.T) {
	_, err := protoschema.MarshalJSON(nil)
	assert.Error(t, err)
}

// An Any this client has never seen the payload type for is the one thing that
// stops protojson dead. The call still succeeded, so refusing to render its
// answer would report a perfectly good response as a failed call.
func TestMarshalJSON_UnresolvableAny(t *testing.T) {
	msg := &anypb.Any{
		TypeUrl: "type.googleapis.com/some.server.only.Detail",
		Value:   []byte{0x0a, 0x03, 'a', 'b', 'c'},
	}

	_, err := protoschema.MarshalJSON(msg)
	require.Error(t, err, "protojson is expected to refuse this; the fallback below is why it matters")
	assert.Contains(t, err.Error(), "unable to resolve")
}

func TestMarshal(t *testing.T) {
	t.Run("prefers JSON", func(t *testing.T) {
		msg := build(t, "text", "hello")

		body, format, err := protoschema.Marshal(msg)

		require.NoError(t, err)
		assert.Equal(t, protoschema.FormatJSON, format)
		assert.Contains(t, body, `"text": "hello"`)
	})

	// prototext degrades where protojson refuses: it prints the Any's type URL
	// and its bytes rather than failing, so the user still sees what came back.
	t.Run("falls back to text for an unresolvable Any", func(t *testing.T) {
		msg := &anypb.Any{
			TypeUrl: "type.googleapis.com/some.server.only.Detail",
			Value:   []byte{0x0a, 0x03, 'a', 'b', 'c'},
		}

		body, format, err := protoschema.Marshal(msg)

		require.NoError(t, err, "a successful call must not be reported as a failure")
		assert.Equal(t, protoschema.FormatText, format)
		assert.Contains(t, body, "some.server.only.Detail")
		assert.Contains(t, body, "abc", "the payload bytes are the point of showing it at all")
	})

	t.Run("a nil message is still an error", func(t *testing.T) {
		_, _, err := protoschema.Marshal(nil)
		assert.Error(t, err)
	})
}

// A request is rendered for the stream log, where every unfilled field of the
// form would otherwise be a line of noise per message sent.
func TestMarshalRequest(t *testing.T) {
	t.Run("leaves out what the user did not fill in", func(t *testing.T) {
		msg := buildAll(t, map[string]string{"text": "hello"})

		body, format, err := protoschema.MarshalRequest(msg)

		require.NoError(t, err)
		assert.Equal(t, protoschema.FormatJSON, format)
		assert.Contains(t, body, `"text": "hello"`)
		assert.NotContains(t, body, `"flag"`, "an untouched field is not part of the request")
		assert.NotContains(t, body, `"tags"`)
	})

	// The asymmetry with a response is the whole point, so it is pinned here.
	t.Run("a response still shows its defaults", func(t *testing.T) {
		msg := buildAll(t, map[string]string{"text": "hello"})

		body, err := protoschema.MarshalJSON(msg)

		require.NoError(t, err)
		assert.Contains(t, body, `"flag": false`)
	})

	t.Run("falls back to text for an unresolvable Any", func(t *testing.T) {
		msg := &anypb.Any{
			TypeUrl: "type.googleapis.com/some.server.only.Detail",
			Value:   []byte{0x0a, 0x03, 'a', 'b', 'c'},
		}

		body, format, err := protoschema.MarshalRequest(msg)

		require.NoError(t, err, "a message that went out must still produce a log line")
		assert.Equal(t, protoschema.FormatText, format)
		assert.Contains(t, body, "some.server.only.Detail")
	})

	t.Run("a nil message is still an error", func(t *testing.T) {
		_, _, err := protoschema.MarshalRequest(nil)
		assert.Error(t, err)
	})
}
