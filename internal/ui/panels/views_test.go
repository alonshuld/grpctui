package panels_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// stripANSI removes the styling, so a width assertion measures cells a terminal
// would draw rather than the escape sequences that colour them.
func stripANSI(line string) string { return ansi.Strip(line) }

// helloWire is a HelloReply-shaped message: field 1, a string.
func helloWire(greeting string) []byte {
	var raw []byte
	raw = protowire.AppendTag(raw, 1, protowire.BytesType)
	return protowire.AppendString(raw, greeting)
}

func okResult(body string) panels.Result {
	return panels.Result{Body: body, Format: protoschema.FormatJSON, Took: time.Millisecond}
}

// The raw view is a second rendering of the same result, so switching to it and
// back has to leave the response exactly as it was.
func TestResponse_RawViewToggles(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())

	result := okResult(`{"greeting": "hi"}`)
	result.Wire = helloWire("hi")
	r.SetSuccess(result)

	require.Contains(t, r.View(), "greeting")

	r.ToggleRaw()
	raw := r.View()
	assert.Contains(t, raw, "raw bytes", "the status line has to say which view this is")
	assert.Contains(t, raw, "00000000", "the hex dump's first offset")
	assert.Contains(t, raw, `"hi"`, "the field listing spells a string out")
	assert.NotContains(t, raw, "greeting", "the JSON body is not in the raw view")

	r.ToggleRaw()
	assert.Contains(t, r.View(), "greeting")
	assert.NotContains(t, r.View(), "raw bytes")
}

// A response the panel could not re-encode still switches — and says why there
// is nothing there, rather than showing an empty box.
func TestResponse_RawViewWithoutBytes(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())
	r.SetSuccess(okResult(`{"greeting": "hi"}`))

	r.ToggleRaw()
	assert.Contains(t, r.View(), "no bytes to show")
}

func TestResponse_RawViewReportsTheSize(t *testing.T) {
	r := newResponse(t)
	result := okResult("{}")
	result.Wire = helloWire(strings.Repeat("x", 2000))
	r.SetSuccess(result)

	r.ToggleRaw()
	view := r.View()
	assert.Contains(t, view, "KB", "a payload past a kilobyte is reported in kilobytes")
	assert.Contains(t, view, "as re-encoded",
		"the view must be honest that these are not the bytes that arrived")
}

// The hex dump narrows to fit the panel. Without that its rows run past the
// edge, where the viewport truncates them — and a truncated hex row is one
// whose bytes are not merely off screen but unreachable.
func TestResponse_RawViewFitsTheWidth(t *testing.T) {
	for _, tt := range []struct{ width, perLine int }{
		{100, 16},
		{60, 8},
		{40, 4},
	} {
		t.Run(fmt.Sprintf("width %d", tt.width), func(t *testing.T) {
			r := panels.NewResponse(keys.Default(), styles.New())
			r.SetSize(tt.width, 30)

			result := okResult("{}")
			result.Wire = helloWire(strings.Repeat("abcdefgh", 8))
			r.SetSuccess(result)
			r.ToggleRaw()

			var rows int
			for line := range strings.SplitSeq(r.View(), "\n") {
				line = strings.TrimRight(stripANSI(line), " ")
				if !strings.HasPrefix(line, "000000") {
					continue
				}
				rows++

				assert.LessOrEqual(t, len([]rune(line)), tt.width,
					"a hex row runs past the panel")
				assert.True(t, strings.HasSuffix(line, "|"),
					"the ASCII gutter was truncated away: %q", line)
			}
			require.Positive(t, rows, "no hex rows were rendered at all")

			// The first row's offset column tells us how many bytes a row holds,
			// since the second row starts at exactly that offset.
			assert.Contains(t, r.View(), fmt.Sprintf("%08x", tt.perLine))
		})
	}
}

func TestResponse_DiffView(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())

	result := okResult("{\n  \"greeting\": \"hello\",\n  \"id\": 2\n}")
	result.Previous = "{\n  \"greeting\": \"hello\",\n  \"id\": 1\n}"
	r.SetSuccess(result)

	r.ToggleDiff()
	view := r.View()

	assert.Contains(t, view, "diff vs previous")
	assert.Contains(t, view, `- `, "the old line is marked in the gutter")
	assert.Contains(t, view, `+ `, "and the new one")
	assert.Contains(t, view, `"id": 1`)
	assert.Contains(t, view, `"id": 2`)
}

func TestResponse_DiffViewWithoutAPrevious(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())
	r.SetSuccess(okResult(`{"greeting": "hi"}`))

	r.ToggleDiff()
	assert.Contains(t, r.View(), "first response from SayHello")
}

func TestResponse_DiffViewOfAnIdenticalResponse(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())

	body := "{\n  \"greeting\": \"hi\"\n}"
	result := okResult(body)
	result.Previous = body
	r.SetSuccess(result)

	r.ToggleDiff()
	view := r.View()
	assert.NotContains(t, view, "+ ")
	assert.NotContains(t, view, "- ")
	assert.Contains(t, view, "greeting", "an unchanged body is shown whole")
}

// A long unchanged run is collapsed and counted, rather than shown or silently
// dropped.
func TestResponse_DiffViewCollapsesUnchangedRuns(t *testing.T) {
	r := panels.NewResponse(keys.Default(), styles.New())
	r.SetSize(60, 40)
	r.SetMethod(responseMethod())

	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "  \"field\": \"same\","
	}
	previous := strings.Join(lines, "\n")
	changed := append([]string(nil), lines...)
	changed[15] = "  \"field\": \"different\","

	result := okResult(strings.Join(changed, "\n"))
	result.Previous = previous
	r.SetSuccess(result)

	r.ToggleDiff()
	assert.Contains(t, r.View(), "unchanged lines")
}

// The two views are alternatives: turning one on turns the other off, or the
// panel would have to decide which of them it was showing.
func TestResponse_OneViewAtATime(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())

	result := okResult(`{"greeting": "hi"}`)
	result.Wire = helloWire("hi")
	result.Previous = `{"greeting": "hey"}`
	r.SetSuccess(result)

	r.ToggleRaw()
	require.Contains(t, r.View(), "raw bytes")

	r.ToggleDiff()
	view := r.View()
	assert.Contains(t, view, "diff vs previous")
	assert.NotContains(t, view, "raw bytes")
}

// Somebody watching a field change across three calls has said, by pressing D,
// what they want to see. Putting them back in the decoded body on every send
// would make the view useless for the thing it exists for.
func TestResponse_ViewSurvivesTheNextCall(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())
	r.SetSuccess(okResult(`{"id": 1}`))

	r.ToggleDiff()

	second := okResult(`{"id": 2}`)
	second.Previous = `{"id": 1}`
	r.SetSuccess(second)

	assert.Contains(t, r.View(), "diff vs previous")
}

func TestResponse_LatencyBreakdown(t *testing.T) {
	r := panels.NewResponse(keys.Default(), styles.New())
	r.SetSize(160, 12)

	result := okResult("{}")
	result.Took = 25 * time.Millisecond
	result.Timing = grpcclient.Timing{
		Measured:      true,
		Connect:       2 * time.Millisecond,
		FirstByte:     18 * time.Millisecond,
		Total:         24 * time.Millisecond,
		RequestBytes:  12,
		ResponseBytes: 34,
	}
	r.SetSuccess(result)

	view := r.View()
	assert.Contains(t, view, "connect 2ms")
	assert.Contains(t, view, "first byte 18ms")
	assert.Contains(t, view, "total 24ms")
	assert.Contains(t, view, "12 B out")
	assert.Contains(t, view, "34 B in")
	assert.Equal(t, result.Timing, r.Timing())
}

// A client that measures nothing — every fake in these tests — must still show
// the wall-clock duration and no invented breakdown.
func TestResponse_WithoutABreakdown(t *testing.T) {
	r := newResponse(t)

	result := okResult("{}")
	result.Took = 5 * time.Millisecond
	r.SetSuccess(result)

	view := r.View()
	assert.Contains(t, view, "5ms")
	assert.NotContains(t, view, "connect")
	assert.NotContains(t, view, "first byte")
}

// Clearing has to take the v0.8 state with it, or a new method's empty panel
// still holds the last one's bytes.
func TestResponse_ClearDropsEverything(t *testing.T) {
	r := newResponse(t)

	result := okResult("{}")
	result.Wire = helloWire("hi")
	result.Timing = grpcclient.Timing{Measured: true, Total: time.Second}
	r.SetSuccess(result)

	r.Clear()
	assert.Empty(t, r.Wire())
	assert.False(t, r.Timing().Measured)
}

func TestResponse_RawViewOfAFailedCall(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())
	r.SetFailure("nope", grpcclient.CallStatus{Code: 5, Name: "NotFound"}, true, time.Millisecond)

	r.ToggleRaw()
	view := r.View()
	assert.Contains(t, view, "NotFound")
	assert.Contains(t, view, "no bytes to show")
}

// A key the panel does not own must reach the viewport rather than being eaten
// by the toggles, which the root model owns.
func TestResponse_TogglesAreNotPanelKeys(t *testing.T) {
	r := newResponse(t)
	result := okResult(`{"greeting": "hi"}`)
	result.Wire = helloWire("hi")
	r.SetSuccess(result)
	r.Focus()

	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	assert.NotContains(t, r.View(), "raw bytes",
		"w is the root model's key, so the panel must not act on it twice")
}
