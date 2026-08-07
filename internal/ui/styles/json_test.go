package styles

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The classification is tested through the scanner rather than through
// [Styles.HighlightJSON], because lipgloss renders a style away to nothing on a
// terminal without colour — which is every terminal a test runs in. Asserting
// on the coloured output would therefore assert nothing at all.
func TestScanJSON(t *testing.T) {
	tests := map[string]struct {
		src  string
		want []jsonToken
	}{
		"a key and a string value": {
			src: `{"a": "b"}`,
			want: []jsonToken{
				{"{", jsonPunct},
				{`"a"`, jsonKey},
				{":", jsonPunct},
				{" ", jsonPlain},
				{`"b"`, jsonString},
				{"}", jsonPunct},
			},
		},
		"numbers": {
			src: `[1, -2.5, 6e23]`,
			want: []jsonToken{
				{"[", jsonPunct},
				{"1", jsonNumber},
				{",", jsonPunct},
				{" ", jsonPlain},
				{"-2.5", jsonNumber},
				{",", jsonPunct},
				{" ", jsonPlain},
				{"6e23", jsonNumber},
				{"]", jsonPunct},
			},
		},
		"literals": {
			src: `[true,false,null]`,
			want: []jsonToken{
				{"[", jsonPunct},
				{"true", jsonLiteral},
				{",", jsonPunct},
				{"false", jsonLiteral},
				{",", jsonPunct},
				{"null", jsonLiteral},
				{"]", jsonPunct},
			},
		},
		// The colon can be anywhere after the string, which is what makes a key a
		// key: protobuf's JSON is indented, so it usually is not far.
		"a key across a newline": {
			src: "\"a\"\n  : 1",
			want: []jsonToken{
				{`"a"`, jsonKey},
				{"\n  ", jsonPlain},
				{":", jsonPunct},
				{" ", jsonPlain},
				{"1", jsonNumber},
			},
		},
		// An escaped quote does not end the string, and a colon inside one does
		// not make the string before it a key.
		"escapes inside a string": {
			src: `{"a\":": "b\\"}`,
			want: []jsonToken{
				{"{", jsonPunct},
				{`"a\":"`, jsonKey},
				{":", jsonPunct},
				{" ", jsonPlain},
				{`"b\\"`, jsonString},
				{"}", jsonPunct},
			},
		},
		"a string nobody closed": {
			src:  `{"a`,
			want: []jsonToken{{"{", jsonPunct}, {`"a`, jsonString}},
		},
		"not JSON at all": {
			src:  "status: SERVING",
			want: []jsonToken{{"status", jsonPlain}, {":", jsonPunct}, {" SERVING", jsonPlain}},
		},
		"empty": {src: "", want: nil},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, scanJSON(tt.src))
		})
	}
}

// Whatever the scanner decides, every byte it was given has to come back out in
// the order it arrived: a highlighter that dropped or reordered a character
// would be editing the server's answer.
func TestScanJSON_KeepsTheDocumentIntact(t *testing.T) {
	sources := []string{
		"{\n  \"greeting\": \"hello, world\",\n  \"count\": 3\n}",
		`{"nested": {"list": [1, true, null, "x"], "empty": {}}}`,
		`{"unicode": "héllo ✓", "escaped": "a\"b\\c"}`,
		"malformed {{{ \"a\": ,,, ]",
		"",
	}

	for _, src := range sources {
		var out strings.Builder
		for _, tok := range scanJSON(src) {
			out.WriteString(tok.text)
		}
		assert.Equal(t, src, out.String())
	}
}

func TestStyles_HighlightJSON(t *testing.T) {
	const src = "{\n  \"greeting\": \"hello\",\n  \"count\": 3\n}"

	out := New().HighlightJSON(src)

	require.Equal(t, strings.Count(src, "\n"), strings.Count(out, "\n"),
		"highlighting must not add or remove lines")
	for i, line := range strings.Split(out, "\n") {
		assert.Equal(t, lipgloss.Width(strings.Split(src, "\n")[i]), lipgloss.Width(line),
			"highlighting changed the visible width of line %d", i)
	}
}
