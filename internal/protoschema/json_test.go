package protoschema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/protoschema"
)

func TestMarshalJSON(t *testing.T) {
	form := testForm(t)
	msg, err := form.Build(map[string]string{
		"text":   "hello",
		"count":  "7",
		"total":  "9007199254740993",
		"colour": "COLOUR_BLUE",
	})
	require.NoError(t, err)

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
	form := testForm(t)
	msg, err := form.Build(map[string]string{"text": "hello", "count": "7"})
	require.NoError(t, err)

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
