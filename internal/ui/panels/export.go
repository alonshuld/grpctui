package panels

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// Export shows the request in the form rendered as a grpcurl command, for
// copying somewhere else.
//
// It is a modal that only displays: there is nothing to choose, and the one
// thing the user does with it — select the text and copy it — is the terminal's
// job rather than grpctui's. Writing to the clipboard would mean either a
// dependency on a clipboard library or an OSC 52 escape written straight to a
// screen bubbletea owns, and neither is worth it for a box of text that a mouse
// drag already handles.
//
// What it shows never contains a credential; see internal/export.
type Export struct {
	keys   keys.KeyMap
	styles styles.Styles

	// lines is the command, already split, since the panel scrolls it.
	lines []string

	// notice replaces the command when there is nothing to export — no method
	// selected, or a form that does not build.
	notice string

	opened bool
	offset int

	width  int
	height int
}

// NewExport builds an empty export panel.
func NewExport(km keys.KeyMap, st styles.Styles) Export {
	return Export{keys: km, styles: st}
}

// Open shows a rendered command.
func (e *Export) Open(command string) {
	e.lines = strings.Split(command, "\n")
	e.notice = ""
	e.opened = true
	e.offset = 0
}

// OpenNotice shows why there is nothing to export, rather than an empty box.
func (e *Export) OpenNotice(text string) {
	e.lines = nil
	e.notice = text
	e.opened = true
	e.offset = 0
}

// Close hides the panel.
func (e *Export) Close() { e.opened = false }

// Opened reports whether the panel is on screen. While it is, it owns every key
// but ctrl+c.
func (e Export) Opened() bool { return e.opened }

// Command is the text on screen, for a test that wants to assert on what would
// be copied rather than on how it is drawn.
func (e Export) Command() string { return strings.Join(e.lines, "\n") }

// SetSize sets the area the panel may draw into.
func (e *Export) SetSize(width, height int) {
	e.width = width
	e.height = height
	e.clampOffset()
}

// Update handles the panel's keys while it is open.
func (e Export) Update(msg tea.Msg) (Export, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !e.opened {
		return e, nil
	}

	switch {
	case key.Matches(keyMsg, e.keys.Up):
		e.offset--
	case key.Matches(keyMsg, e.keys.Down):
		e.offset++
	case key.Matches(keyMsg, e.keys.PageUp):
		e.offset -= e.visibleRows()
	case key.Matches(keyMsg, e.keys.PageDown):
		e.offset += e.visibleRows()
	case key.Matches(keyMsg, e.keys.Top):
		e.offset = 0
	case key.Matches(keyMsg, e.keys.Bottom):
		e.offset = len(e.lines)
	case key.Matches(keyMsg, e.keys.Cancel), key.Matches(keyMsg, e.keys.Export):
		e.Close()
		return e, nil
	}

	e.clampOffset()
	return e, nil
}

func (e *Export) clampOffset() {
	e.offset = clampWindow(e.offset, e.offset, e.visibleRows(), len(e.lines))
}

// visibleRows is how many lines of the command fit, after the title, the blank
// line and the hint.
func (e Export) visibleRows() int {
	const chrome = 5
	if e.height <= 0 {
		return max(len(e.lines), 1)
	}
	return max(e.height-chrome, 1)
}

const exportHint = "j/k scroll · esc close"

// View renders the panel as a box the root model centres on the screen.
func (e Export) View() string {
	box := e.styles.Panel.Width(e.inner() + e.styles.Panel.GetHorizontalPadding())

	lines := []string{e.styles.PanelTitle.Render("Export as grpcurl"), ""}

	if e.notice != "" {
		lines = append(lines, e.styles.Muted.Render(e.notice))
	} else {
		visible := min(e.visibleRows(), len(e.lines)-e.offset)
		for i := e.offset; i < e.offset+visible; i++ {
			lines = append(lines, e.row(i))
		}
	}

	return box.Render(strings.Join(append(lines, "", e.styles.Hint.Render(exportHint)), "\n"))
}

// row renders one line of the command. A comment is dimmed so that what is
// actually runnable stands out from what explains it.
func (e Export) row(i int) string {
	line := e.lines[i]
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return styles.Truncate(e.styles.Muted.Render(line), e.inner())
	}
	return styles.Truncate(e.styles.Value.Render(line), e.inner())
}

// inner is the box's content width: enough for the longest line of the command,
// bounded by the screen.
func (e Export) inner() int {
	want := max(minBoxWidth, lipgloss.Width(exportHint))
	for _, line := range e.lines {
		want = max(want, lipgloss.Width(line))
	}
	if e.notice != "" {
		want = max(want, lipgloss.Width(e.notice))
	}
	return min(want, e.available())
}

func (e Export) available() int {
	const chrome = 4 // the box's border and padding
	return max(e.width-chrome, minValueWidth)
}
