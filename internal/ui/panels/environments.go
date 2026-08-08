package panels

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
	"github.com/alonshuld/grpctui/internal/vars"
)

// EnvironmentSelectedMsg asks the root model to switch environment. It carries
// the index as well as the environment so the root can record which one is
// active without comparing structs.
type EnvironmentSelectedMsg struct {
	Index       int
	Environment vars.Environment
}

// Environments is the environment switcher: the named variable sets, one per
// line, with the active one marked.
//
// Like the connection switcher it is a chooser rather than an editor —
// environments are written in the config file, where they can be kept beside a
// project and version-controlled — and like it, it is modal: while it is open
// it owns the keyboard, because switching which environment a request means is
// a choice to finish rather than a place to tab out of.
//
// It shows how many variables each environment binds and never one of their
// values. Which environment you are in is worth seeing at a glance; what
// {{token}} currently expands to is not something to put on a screen somebody
// might be sharing.
type Environments struct {
	keys   keys.KeyMap
	styles styles.Styles

	list   []vars.Environment
	active int
	cursor int
	offset int

	open   bool
	width  int
	height int
}

// NewEnvironments builds an empty switcher.
func NewEnvironments(km keys.KeyMap, st styles.Styles) Environments {
	return Environments{keys: km, styles: st, active: noEnvironment}
}

// noEnvironment is [Environments.active] when none are configured, which is the
// ordinary case: environments are optional, and a run without them resolves
// nothing.
const noEnvironment = -1

// SetEnvironments replaces the list and says which entry is in force.
func (e *Environments) SetEnvironments(list []vars.Environment, active int) {
	e.list = list
	e.SetActive(active)
}

// SetActive marks the environment in force and puts the cursor on it.
func (e *Environments) SetActive(active int) {
	if active < 0 || active >= len(e.list) {
		e.active = noEnvironment
		e.cursor = 0
		e.clampOffset()
		return
	}
	e.active = active
	e.cursor = active
	e.clampOffset()
}

// Active returns the environment in force.
func (e Environments) Active() (vars.Environment, bool) {
	if e.active < 0 || e.active >= len(e.list) {
		return vars.Environment{}, false
	}
	return e.list[e.active], true
}

// Len reports how many environments there are. The switcher is worth offering
// only when there is something to switch to.
func (e Environments) Len() int { return len(e.list) }

// Open shows the switcher with the cursor on the active environment.
func (e *Environments) Open() {
	if len(e.list) == 0 {
		return
	}
	e.open = true
	if e.active >= 0 {
		e.cursor = e.active
	}
	e.clampOffset()
}

// Close hides the switcher.
func (e *Environments) Close() { e.open = false }

// Opened reports whether the switcher is on screen. While it is, it owns the
// keyboard.
func (e Environments) Opened() bool { return e.open }

// SetSize sets the area the switcher may draw into.
func (e *Environments) SetSize(width, height int) {
	e.width = width
	e.height = height
	e.clampOffset()
}

// Update handles the switcher's keys while it is open.
func (e Environments) Update(msg tea.Msg) (Environments, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !e.open {
		return e, nil
	}

	if cursor, moved := moveCursor(keyMsg, e.keys, e.cursor, len(e.list)); moved {
		e.moveTo(cursor)
		return e, nil
	}

	switch {
	case key.Matches(keyMsg, e.keys.Cancel), key.Matches(keyMsg, e.keys.Environments):
		e.Close()
	case key.Matches(keyMsg, e.keys.Select):
		return e, e.choose()
	}
	return e, nil
}

// choose closes the switcher and asks for the environment under the cursor.
//
// Picking the one already active is not a no-op: it is how a set of variables
// captured over the course of an afternoon is put back to what the config file
// says, which is the only way to undo a capture short of retyping it.
func (e *Environments) choose() tea.Cmd {
	e.Close()
	if e.cursor < 0 || e.cursor >= len(e.list) {
		return nil
	}

	selected := EnvironmentSelectedMsg{Index: e.cursor, Environment: e.list[e.cursor]}
	return func() tea.Msg { return selected }
}

func (e *Environments) moveTo(i int) {
	e.cursor = clampIndex(i, len(e.list))
	e.clampOffset()
}

func (e *Environments) clampOffset() {
	e.offset = clampWindow(e.cursor, e.offset, e.visibleRows(), len(e.list))
}

// visibleRows is how many environments fit, after the title and the help line.
func (e Environments) visibleRows() int {
	const chrome = 3
	if e.height <= 0 {
		return max(len(e.list), 1)
	}
	return max(e.height-chrome, 1)
}

// View renders the switcher as a box the root model centres on the screen.
func (e Environments) View() string {
	lines := []string{e.styles.PanelTitle.Render("Environments"), ""}

	visible := min(e.visibleRows(), len(e.list)-e.offset)
	for i := e.offset; i < e.offset+visible; i++ {
		lines = append(lines, e.row(i))
	}

	lines = append(lines, "", e.styles.Hint.Render("enter switch · esc close"))
	return e.styles.Panel.Render(strings.Join(lines, "\n"))
}

func (e Environments) row(i int) string {
	env := e.list[i]

	marker := "  "
	if i == e.active {
		marker = e.styles.FieldValue.Render(glyphOn) + " "
	}

	name := e.styles.Service.Render(padCell(env.Label(), e.labelWidth()))
	text := marker + name + "  " + e.styles.Muted.Render(e.describe(env))

	if i != e.cursor {
		return styles.Truncate("  "+text, e.rowWidth())
	}
	return e.styles.Cursor.Render(styles.Truncate("❯ "+text, e.rowWidth()))
}

// describe summarises what switching to an environment would mean: where it
// points, and how much it binds. Never what it binds it to.
func (e Environments) describe(env vars.Environment) string {
	var parts []string
	if env.Target != "" {
		parts = append(parts, env.Target)
	}

	n := len(env.Variables)
	parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "variable")))
	return strings.Join(parts, " · ")
}

func (e Environments) labelWidth() int {
	w := 0
	for _, env := range e.list {
		w = max(w, len(env.Label()))
	}
	return min(w, maxNameWidth)
}

// rowWidth is the width a row may occupy inside the box.
func (e Environments) rowWidth() int {
	const chrome = 4 // the box's border and padding
	if e.width <= chrome {
		return maxNameWidth
	}
	return e.width - chrome
}
