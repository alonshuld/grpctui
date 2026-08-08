package keys_test

import (
	"testing"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/ui/keys"
)

// helpWidth is the narrowest terminal the expanded help bar must fit in. It is
// the width the UI's golden files are rendered at, and the point past which
// bubbles/help truncates a column away rather than wrapping it — which is a
// keybinding silently disappearing from the only place it is documented.
const helpWidth = 100

func TestFullHelpFits(t *testing.T) {
	h := help.New()
	h.ShowAll = true

	// Width is deliberately left at zero: the bar is measured unconstrained, so
	// that a column over budget shows up as a number here rather than as a
	// truncation nobody notices.
	view := h.View(keys.Default())

	assert.LessOrEqual(t, lipgloss.Width(view), helpWidth,
		"the expanded help bar no longer fits; move a binding to another column "+
			"or shorten a description rather than letting the last column truncate:\n%s", view)
}

// Every binding has to be reachable from the help bar or be one of the few
// documented elsewhere, or adding a key quietly makes it undiscoverable.
func TestEveryBindingIsDocumented(t *testing.T) {
	km := keys.Default()

	shown := make(map[string]bool)
	for _, column := range km.FullHelp() {
		for _, binding := range column {
			for _, k := range binding.Keys() {
				shown[k] = true
			}
		}
	}

	// ForceQuit is deliberately absent — Quit already documents "quit" — and
	// Filter belongs to the request browser, which prints its own hint line.
	undocumented := map[string]bool{"ctrl+c": true, "/": true}

	for name, binding := range map[string]key.Binding{
		"Up": km.Up, "Down": km.Down, "PageUp": km.PageUp, "PageDown": km.PageDown,
		"Top": km.Top, "Bottom": km.Bottom,
		"ScrollLeft": km.ScrollLeft, "ScrollRight": km.ScrollRight,
		"Select": km.Select, "Expand": km.Expand, "Collapse": km.Collapse, "Toggle": km.Toggle,
		"Add": km.Add, "Remove": km.Remove,
		"Send": km.Send, "Cancel": km.Cancel, "EndStream": km.EndStream,
		"NextPanel": km.NextPanel, "PrevPanel": km.PrevPanel, "Profiles": km.Profiles,
		"HistoryPrev": km.HistoryPrev, "HistoryNext": km.HistoryNext,
		"Requests": km.Requests, "Save": km.Save, "Filter": km.Filter,
		"Environments": km.Environments, "Variables": km.Variables, "Capture": km.Capture,
		"Retry": km.Retry, "Help": km.Help, "Quit": km.Quit, "ForceQuit": km.ForceQuit,
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, binding.Keys(), "%s is bound to nothing", name)

			first := binding.Keys()[0]
			if undocumented[first] {
				return
			}
			assert.True(t, shown[first], "%s (%s) is in no help column", name, first)
		})
	}
}

// The keys a modal owns must not collide with the ones it forwards, and the
// v0.6 additions must not shadow anything that already worked.
func TestNoDuplicateKeys(t *testing.T) {
	km := keys.Default()

	seen := make(map[string]string)
	for _, column := range km.FullHelp() {
		for _, binding := range column {
			for _, k := range binding.Keys() {
				if other, ok := seen[k]; ok {
					t.Errorf("%q is bound twice: %s and %s", k, other, binding.Help().Desc)
				}
				seen[k] = binding.Help().Desc
			}
		}
	}
}
