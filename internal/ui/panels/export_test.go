package panels_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

func newExport(t *testing.T) panels.Export {
	t.Helper()

	e := panels.NewExport(keys.Default(), styles.New())
	e.SetSize(80, 20)
	return e
}

func TestExport_ClosedByDefault(t *testing.T) {
	assert.False(t, newExport(t).Opened())
}

func TestExport_ShowsTheCommand(t *testing.T) {
	e := newExport(t)
	e.Open("grpcurl \\\n  -plaintext \\\n  localhost:50051 demo.v1.Greeter/SayHello")

	require.True(t, e.Opened())
	view := e.View()
	assert.Contains(t, view, "Export as grpcurl")
	assert.Contains(t, view, "-plaintext")
	assert.Contains(t, view, "demo.v1.Greeter/SayHello")
}

func TestExport_ShowsANoticeWhenThereIsNothingToExport(t *testing.T) {
	e := newExport(t)
	e.OpenNotice("Select a method first.")

	assert.True(t, e.Opened())
	assert.Contains(t, e.View(), "Select a method first.")
	assert.Empty(t, e.Command())
}

func TestExport_Closes(t *testing.T) {
	tests := map[string]tea.KeyMsg{
		"esc": {Type: tea.KeyEsc},
		"X":   {Type: tea.KeyRunes, Runes: []rune("X")},
	}

	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			e := newExport(t)
			e.Open("grpcurl localhost:50051 demo.v1.Greeter/SayHello")

			e, _ = e.Update(key)
			assert.False(t, e.Opened())
		})
	}
}

// A command long enough to need scrolling is the ordinary case: three headers
// and a body already run past a short modal.
func TestExport_Scrolls(t *testing.T) {
	e := panels.NewExport(keys.Default(), styles.New())
	e.SetSize(80, 10)

	lines := make([]string, 40)
	for i := range lines {
		lines[i] = "  -H 'header-" + strings.Repeat("x", i%5) + ": <value>' \\"
	}
	lines[39] = "  localhost:50051 demo.v1.Greeter/SayHello"
	e.Open(strings.Join(lines, "\n"))

	require.NotContains(t, e.View(), "demo.v1.Greeter/SayHello")

	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	assert.Contains(t, e.View(), "demo.v1.Greeter/SayHello", "G reaches the last line")

	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	assert.NotContains(t, e.View(), "demo.v1.Greeter/SayHello", "g goes back to the top")
}

// The text on screen is what gets copied, so a test that asserts on what would
// leave the tool asserts on this rather than on the rendering.
func TestExport_Command(t *testing.T) {
	e := newExport(t)
	command := "grpcurl \\\n  -plaintext \\\n  localhost:50051 demo.v1.Greeter/SayHello"
	e.Open(command)

	assert.Equal(t, command, e.Command())
}

// Reopening replaces what is shown rather than appending to it, and puts the
// view back at the top.
func TestExport_ReopenReplaces(t *testing.T) {
	e := newExport(t)
	e.Open("first")
	e.Open("second")

	assert.Equal(t, "second", e.Command())
	assert.Contains(t, e.View(), "second")
	assert.NotContains(t, e.View(), "first")
}

func TestExport_IgnoresKeysWhileClosed(t *testing.T) {
	e := newExport(t)
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	assert.False(t, e.Opened())
}
