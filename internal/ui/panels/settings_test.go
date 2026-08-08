package panels_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// withColour makes lipgloss emit escape codes for the duration of one test.
//
// Under `go test` stdout is not a terminal, so lipgloss detects no colour
// support and every style renders as bare text — which would make a test that
// asserts a theme reached the frame assert nothing at all. No test in this
// package runs in parallel, so setting the profile globally and putting it back
// is safe.
func withColour(t *testing.T) {
	t.Helper()

	before := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(before) })
}

// loud is a theme nothing else uses, so that a frame drawn in it is
// recognisable by its escape codes alone.
func loud() styles.Styles {
	return styles.NewTheme(styles.Theme{Name: "loud", Palette: styles.Palette{
		Primary:   lipgloss.Color("#ff00ff"),
		Secondary: lipgloss.Color("#00ff00"),
		Muted:     lipgloss.Color("#ff8800"),
		Border:    lipgloss.Color("#0000ff"),
		Error:     lipgloss.Color("#ff0000"),
		Success:   lipgloss.Color("#00ffff"),
		Text:      lipgloss.Color("#ffffff"),
		Inverted:  lipgloss.Color("#000000"),
	}})
}

// TestResponse_SetStyles pins the one panel that holds text coloured by a
// *previous* theme: a body is highlighted once when it arrives, so switching
// theme has to build every such rendering again.
func TestResponse_SetStyles(t *testing.T) {
	withColour(t)

	r := newResponse(t)
	r.SetMethod(responseMethod())
	r.SetSuccess(okResult(`{"greeting": "hi"}`))

	before := r.View()
	r.SetStyles(loud())
	after := r.View()

	assert.NotEqual(t, before, after, "the response kept its old colours")
	assert.Contains(t, stripANSI(after), "greeting", "the body survived the switch")
	assert.Contains(t, after, "255;0;255", "the new primary colour is not in the frame")
}

// TestResponse_SetStyles_StreamLog is the same for a stream's entries, each of
// which is highlighted on arrival.
func TestResponse_SetStyles_StreamLog(t *testing.T) {
	withColour(t)

	r := newResponse(t)
	r.SetMethod(responseMethod())
	r.SetStreaming(responseMethod())
	r.AppendReceived(`{"greeting": "one"}`, protoschema.FormatJSON, time.Second, nil)

	before := r.View()
	r.SetStyles(loud())
	after := r.View()

	assert.NotEqual(t, before, after)
	assert.Contains(t, stripANSI(after), "one")
}

// TestResponse_SetKeys pins that a remapped key reaches the viewport's own
// copy: a remapping that stopped at our struct would leave the body scrolling
// on the old keys.
func TestResponse_SetKeys(t *testing.T) {
	km, err := keys.Default().Apply(map[string]string{"page-down": "ctrl+f"})
	require.NoError(t, err)

	r := newResponse(t)
	r.SetKeys(km)
	r.SetMethod(responseMethod())
	var body strings.Builder
	for i := range 100 {
		fmt.Fprintf(&body, "{\"line\": %d}\n", i)
	}
	r.SetSuccess(okResult(body.String()))
	r.Focus()

	top := r.View()
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	assert.NotEqual(t, top, r.View(), "ctrl+f did not page the response down")
}

// TestForm_SetStyles pins that a panel caching styled rows rebuilds them.
func TestForm_SetStyles(t *testing.T) {
	withColour(t)

	f := newForm(t)
	f.SetMethod(grpcclient.Service{Name: "demo.v1.Greeter"}, formMethod(t))

	before := f.View()
	f.SetStyles(loud())

	assert.NotEqual(t, before, f.View())
	assert.Contains(t, stripANSI(f.View()), "name")
}

// TestPanels_SetStyles walks every panel that only stores the new set, so that
// one added without a SetStyles fails here rather than by staying stubbornly
// the old colour.
func TestPanels_SetStyles(t *testing.T) {
	withColour(t)

	km, st := keys.Default(), styles.New()

	tests := map[string]func(styles.Styles) string{
		"tree": func(s styles.Styles) string {
			p := panels.NewTree(km, st)
			p.SetSize(40, 10)
			p.SetServices(testServices())
			p.SetStyles(s)
			return p.View()
		},
		"metadata": func(s styles.Styles) string {
			p := panels.NewMetadata(km, st)
			p.SetSize(40, 10)
			p.SetStyles(s)
			return p.View()
		},
		"profiles": func(s styles.Styles) string {
			p := panels.NewProfiles(km, st)
			p.SetSize(60, 10)
			p.SetProfiles(testProfiles(), 0)
			p.Open()
			p.SetStyles(s)
			return p.View()
		},
		"themes": func(s styles.Styles) string {
			p := panels.NewThemes(km, st)
			p.SetSize(60, 10)
			p.Open()
			p.SetStyles(s)
			return p.View()
		},
	}

	for name, render := range tests {
		t.Run(name, func(t *testing.T) {
			assert.NotEqual(t, render(st), render(loud()), "%s kept its old colours", name)
		})
	}
}

// TestPanels_SetKeys walks the panels whose keys are only stored, proving each
// actually matches against the map it was handed rather than the one it was
// built with.
func TestPanels_SetKeys(t *testing.T) {
	km, err := keys.Default().Apply(map[string]string{"down": "ctrl+n"})
	require.NoError(t, err)

	t.Run("tree", func(t *testing.T) {
		p := panels.NewTree(keys.Default(), styles.New())
		p.SetSize(40, 10)
		p.SetServices(testServices())
		p.Focus()

		before := p.View()
		p.SetKeys(km)
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
		assert.NotEqual(t, before, p.View(), "ctrl+n did not move the cursor")
	})

	t.Run("themes", func(t *testing.T) {
		p := panels.NewThemes(km, styles.New())
		p.SetSize(60, 10)
		p.SetKeys(km)
		p.Open()

		before := p.View()
		p, cmd := p.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
		assert.NotEqual(t, before, p.View())
		require.NotNil(t, cmd, "moving the cursor should preview the theme")
	})
}

// TestAnnotations pins that a renderer's gloss reaches the frame, beside the
// value it describes and not instead of it — internal/render annotates rather
// than rewriting, and the panel has to keep that true.
func TestResponse_Annotations(t *testing.T) {
	body := "{\n  \"createdAt\": \"2026-08-08T09:00:00Z\",\n  \"name\": \"ada\"\n}"

	r := newResponse(t)
	r.SetMethod(responseMethod())

	result := okResult(body)
	result.Notes = []render.Annotation{{Line: 2, Text: "3 hours ago"}}
	r.SetSuccess(result)

	view := stripANSI(r.View())
	require.Contains(t, view, "3 hours ago")

	// On the timestamp's line, after it — never in place of it.
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(line, "3 hours ago") {
			assert.Contains(t, line, "2026-08-08T09:00:00Z",
				"the gloss replaced the value instead of sitting beside it")
			assert.Less(t, strings.Index(line, "2026-08-08"), strings.Index(line, "3 hours ago"))
			return
		}
	}
	t.Fatal("the gloss is on no line")
}

// TestResponse_Annotations_OutOfRange pins that a note naming a line the body
// does not have is dropped. It can only happen if the body and the notes came
// from different messages, and hanging a gloss off an unrelated field would be
// worse than saying nothing.
func TestResponse_Annotations_OutOfRange(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())

	result := okResult(`{"greeting": "hi"}`)
	result.Notes = []render.Annotation{{Line: 99, Text: "nowhere"}, {Line: 0, Text: "also nowhere"}}
	r.SetSuccess(result)

	view := stripANSI(r.View())
	assert.NotContains(t, view, "nowhere")
	assert.Contains(t, view, "greeting")
}

// TestResponse_Annotations_SurviveAThemeSwitch pins that the glosses are kept
// beside the text they annotate rather than baked into it once.
func TestResponse_Annotations_SurviveAThemeSwitch(t *testing.T) {
	body := "{\n  \"createdAt\": \"2026-08-08T09:00:00Z\"\n}"

	r := newResponse(t)
	r.SetMethod(responseMethod())

	result := okResult(body)
	result.Notes = []render.Annotation{{Line: 2, Text: "3 hours ago"}}
	r.SetSuccess(result)

	r.SetStyles(loud())
	assert.Contains(t, stripANSI(r.View()), "3 hours ago")
}
