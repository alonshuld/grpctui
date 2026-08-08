package panels

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// ThemeSelectedMsg asks the root model to redraw in another theme.
type ThemeSelectedMsg struct {
	Index int
	Theme styles.Theme
}

// Themes is the theme switcher: the built-in palettes and whatever the config
// file adds, one per line, with the active one marked.
//
// Like [Profiles] it is a chooser rather than an editor. A theme is eight
// colours written in the config file, where they can be kept and shared; what
// belongs in the TUI is seeing them applied without restarting — which is the
// only way to judge a terminal palette at all.
type Themes struct {
	keys   keys.KeyMap
	styles styles.Styles

	themes []styles.Theme
	active int
	cursor int
	offset int

	open   bool
	width  int
	height int
}

// NewThemes builds a switcher over the built-in themes.
func NewThemes(km keys.KeyMap, st styles.Styles) Themes {
	t := Themes{keys: km, styles: st}
	t.SetThemes(styles.BuiltinThemes(), 0)
	return t
}

// SetThemes replaces the list and says which entry is in use.
func (t *Themes) SetThemes(themes []styles.Theme, active int) {
	t.themes = themes
	t.SetActive(active)
}

// SetActive marks the theme in use and puts the cursor on it.
func (t *Themes) SetActive(active int) {
	t.active = clampIndex(active, len(t.themes))
	t.cursor = t.active
	t.clampOffset()
}

// Active returns the theme in use.
func (t Themes) Active() (styles.Theme, bool) {
	if t.active < 0 || t.active >= len(t.themes) {
		return styles.Theme{}, false
	}
	return t.themes[t.active], true
}

// Len reports how many themes there are.
func (t Themes) Len() int { return len(t.themes) }

// Next is the theme after the active one, wrapping round.
//
// It is what the light/dark toggle uses. With only the built-ins configured the
// cycle is auto → dark → light → auto, which is exactly the toggle the roadmap
// asks for; with custom themes in the file it walks those too, so one key
// serves both.
func (t Themes) Next() (int, styles.Theme, bool) {
	if len(t.themes) == 0 {
		return 0, styles.Theme{}, false
	}
	at := (t.active + 1) % len(t.themes)
	return at, t.themes[at], true
}

// Open shows the switcher with the cursor on the active theme.
func (t *Themes) Open() {
	if len(t.themes) == 0 {
		return
	}
	t.open = true
	t.cursor = t.active
	t.clampOffset()
}

// Close hides the switcher.
func (t *Themes) Close() { t.open = false }

// Opened reports whether the switcher is on screen.
func (t Themes) Opened() bool { return t.open }

// SetSize sets the area the switcher may draw into.
func (t *Themes) SetSize(width, height int) {
	t.width = width
	t.height = height
	t.clampOffset()
}

// Update handles the switcher's keys while it is open.
//
// Moving the cursor emits a selection rather than waiting for enter: a theme is
// judged by looking at it, and a chooser that made you commit before showing
// you the colours would be a chooser you had to open four times. esc leaves
// whatever the cursor last landed on in place, which is what the user has been
// looking at.
func (t Themes) Update(msg tea.Msg) (Themes, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !t.open {
		return t, nil
	}

	if cursor, moved := moveCursor(keyMsg, t.keys, t.cursor, len(t.themes)); moved {
		t.moveTo(cursor)
		return t, t.preview()
	}

	switch {
	case key.Matches(keyMsg, t.keys.Cancel), key.Matches(keyMsg, t.keys.Themes):
		t.Close()
	case key.Matches(keyMsg, t.keys.Select):
		t.Close()
		return t, t.preview()
	}
	return t, nil
}

// preview asks for the theme under the cursor.
func (t Themes) preview() tea.Cmd {
	if t.cursor < 0 || t.cursor >= len(t.themes) {
		return nil
	}
	selected := ThemeSelectedMsg{Index: t.cursor, Theme: t.themes[t.cursor]}
	return func() tea.Msg { return selected }
}

func (t *Themes) moveTo(i int) {
	t.cursor = clampIndex(i, len(t.themes))
	t.clampOffset()
}

func (t *Themes) clampOffset() {
	t.offset = clampWindow(t.cursor, t.offset, t.visibleRows(), len(t.themes))
}

// visibleRows is how many themes fit, after the title and the help line.
func (t Themes) visibleRows() int {
	const chrome = 3
	if t.height <= 0 {
		return len(t.themes)
	}
	return max(t.height-chrome, 1)
}

// View renders the switcher as a box the root model centres on the screen.
func (t Themes) View() string {
	lines := []string{t.styles.PanelTitle.Render("Themes"), ""}

	visible := min(t.visibleRows(), len(t.themes)-t.offset)
	for i := t.offset; i < t.offset+visible; i++ {
		lines = append(lines, t.row(i))
	}

	lines = append(lines, "", t.styles.Hint.Render("↑/↓ preview · enter keep · esc close"))
	return t.styles.Panel.Render(strings.Join(lines, "\n"))
}

func (t Themes) row(i int) string {
	theme := t.themes[i]

	marker := "  "
	if i == t.active {
		marker = t.styles.FieldValue.Render(glyphOn) + " "
	}

	name := t.styles.Service.Render(padCell(theme.Name, t.labelWidth()))
	text := marker + name + "  " + t.styles.Muted.Render(theme.Description)

	if i != t.cursor {
		return styles.Truncate("  "+text, t.rowWidth())
	}
	return t.styles.Cursor.Render(styles.Truncate("❯ "+text, t.rowWidth()))
}

func (t Themes) labelWidth() int {
	w := 0
	for _, theme := range t.themes {
		w = max(w, len(theme.Name))
	}
	return min(w, maxNameWidth)
}

// rowWidth is the width a row may occupy inside the box.
func (t Themes) rowWidth() int {
	const chrome = 4 // the box's border and padding
	if t.width <= chrome {
		return maxNameWidth
	}
	return t.width - chrome
}
