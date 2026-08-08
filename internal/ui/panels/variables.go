package panels

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
	"github.com/alonshuld/grpctui/internal/vars"
)

// VariableBoundMsg asks the root model to bind a variable for the rest of the
// session.
type VariableBoundMsg struct {
	Name  string
	Value string
}

// VariableUnboundMsg asks the root model to drop a variable.
type VariableUnboundMsg struct {
	Name string
}

// VariableCapturedMsg asks the root model to read a value out of the last
// response and bind it. The panel does not do the reading itself: it has no
// response, and the path may be wrong, which is a thing to report rather than a
// thing to guess at.
type VariableCapturedMsg struct {
	Name string
	Path string
}

// variablesMode is what the panel is doing.
type variablesMode int

const (
	variablesClosed variablesMode = iota

	// variablesList is the browser proper: the bindings, with a cursor.
	variablesList

	// variablesBind is the prompt for a name and a value, typed by hand.
	variablesBind

	// variablesCapture is the prompt for a name and the path in the last
	// response to read it from. Every key is a character in both, which is why
	// they are modes and not flags.
	variablesCapture
)

// Variables is the list of what the active environment binds, and the two ways
// to add to it: typing a value, or capturing one out of the last response.
//
// Capturing is what makes a chain of requests possible — the id a create call
// returned becomes the id the next call asks about — and it is why this is an
// editor where the environment switcher is only a chooser. What it writes is
// session state: a captured value is never persisted, because the canonical
// capture is the token a login call issues, and grpctui's standing rule is that
// such a value never reaches a surface that outlives the call.
//
// Values are shown, unlike credentials elsewhere in grpctui. A variable is
// something the user typed into a form and has to be able to read back; what is
// kept off the ambient surfaces — the status bar, the log, every file — is the
// value, and this panel is neither ambient nor persistent: it is a modal the
// user opened on purpose.
type Variables struct {
	keys   keys.KeyMap
	styles styles.Styles

	set         vars.Set
	environment string

	// input is the binding while typing one and the capture while capturing one.
	// One input serves both because they never happen at once.
	input textinput.Model

	// notice reports the outcome of the last binding, or why there was none.
	notice string
	failed bool

	mode   variablesMode
	cursor int
	offset int

	width  int
	height int
}

// NewVariables builds an empty variables panel.
func NewVariables(km keys.KeyMap, st styles.Styles) Variables {
	input := textinput.New()
	input.Prompt = ""
	input.TextStyle = st.FieldValue
	input.PlaceholderStyle = st.FieldDisabled

	// A blinking cursor would make every frame time-dependent, which costs a
	// redraw a second for no information and makes golden files racy.
	input.Cursor.SetMode(cursor.CursorStatic)

	return Variables{keys: km, styles: st, input: input}
}

// SetVariables replaces what the panel shows, and names the environment they
// came from.
func (v *Variables) SetVariables(set vars.Set, environment string) {
	v.set = set
	v.environment = environment
	v.moveTo(v.cursor)
}

// Set returns the bindings the panel is showing.
func (v Variables) Set() vars.Set { return v.set }

// Open shows the list.
func (v *Variables) Open() {
	v.mode = variablesList
	v.notice, v.failed = "", false
	v.input.Blur()
	v.moveTo(v.cursor)
}

// OpenCapture shows the prompt for reading a value out of the last response,
// seeded with a suggestion.
func (v *Variables) OpenCapture(suggested string) {
	v.mode = variablesCapture
	v.notice, v.failed = "", false
	v.startInput(suggested)
}

// Close hides the panel.
func (v *Variables) Close() {
	v.mode = variablesClosed
	v.input.Blur()
}

// Opened reports whether the panel is on screen. While it is, it owns every key
// but ctrl+c.
func (v Variables) Opened() bool { return v.mode != variablesClosed }

// SetNotice reports the outcome of a binding, leaving the prompt open on a
// failure so the name or the path can be corrected rather than retyped.
func (v *Variables) SetNotice(text string, failed bool) {
	v.notice, v.failed = text, failed
	if !failed {
		return
	}
	v.input.Focus()
	v.input.CursorEnd()
}

// SetSize sets the area the panel may draw into.
func (v *Variables) SetSize(width, height int) {
	v.width = width
	v.height = height
	v.resizeInput()
	v.clampOffset()
}

func (v *Variables) resizeInput() {
	// bubbles/textinput pads its value to Width and then leaves room for the
	// cursor past the end, so a view of Width occupies Width+1 cells.
	v.input.Width = max(v.inner()-lipgloss.Width(v.prompt())-1, minValueWidth)
}

// Update handles the panel's keys while it is open.
func (v Variables) Update(msg tea.Msg) (Variables, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !v.Opened() {
		return v, nil
	}

	if v.mode == variablesList {
		return v.updateList(keyMsg)
	}
	return v.updatePrompt(keyMsg)
}

func (v Variables) updateList(msg tea.KeyMsg) (Variables, tea.Cmd) {
	switch {
	case key.Matches(msg, v.keys.Up):
		v.moveTo(v.cursor - 1)
	case key.Matches(msg, v.keys.Down):
		v.moveTo(v.cursor + 1)
	case key.Matches(msg, v.keys.PageUp):
		v.moveTo(v.cursor - v.visibleRows())
	case key.Matches(msg, v.keys.PageDown):
		v.moveTo(v.cursor + v.visibleRows())
	case key.Matches(msg, v.keys.Top):
		v.moveTo(0)
	case key.Matches(msg, v.keys.Bottom):
		v.moveTo(v.set.Len() - 1)

	case key.Matches(msg, v.keys.Add):
		v.mode = variablesBind
		v.startInput("")

	case key.Matches(msg, v.keys.Select):
		// Seeded with the whole binding, so that correcting a value is an edit
		// rather than retyping the name in front of it.
		v.mode = variablesBind
		v.startInput(v.assignment())

	case key.Matches(msg, v.keys.Capture):
		v.mode = variablesCapture
		v.startInput("")

	case key.Matches(msg, v.keys.Remove):
		return v, v.unbind()

	case key.Matches(msg, v.keys.Cancel), key.Matches(msg, v.keys.Variables):
		v.Close()
	}
	return v, nil
}

// updatePrompt routes keys to the input. enter submits, esc goes back to the
// list rather than closing the panel: abandoning a half-typed binding should
// not also throw away the list you opened to look at.
func (v Variables) updatePrompt(msg tea.KeyMsg) (Variables, tea.Cmd) {
	switch {
	case key.Matches(msg, v.keys.Select):
		return v, v.submit()

	case key.Matches(msg, v.keys.Cancel):
		v.mode = variablesList
		v.notice, v.failed = "", false
		v.input.Blur()
		return v, nil
	}

	var cmd tea.Cmd
	v.input, cmd = v.input.Update(msg)
	return v, cmd
}

// submit turns what was typed into a request for the root model, complaining
// here about the two things this panel can judge on its own: the shape of the
// input, and whether the name is one a reference could ever spell.
func (v *Variables) submit() tea.Cmd {
	capture := v.mode == variablesCapture

	name, rest, ok := vars.SplitAssignment(v.input.Value())
	if !ok {
		v.SetNotice("Write it as "+strings.TrimSpace(v.prompt())+".", true)
		return nil
	}
	if err := vars.ValidateName(name); err != nil {
		v.SetNotice(err.Error(), true)
		return nil
	}

	if capture {
		if strings.TrimSpace(rest) == "" {
			v.SetNotice("Give the path in the response to read from.", true)
			return nil
		}
		captured := VariableCapturedMsg{Name: name, Path: strings.TrimSpace(rest)}
		return func() tea.Msg { return captured }
	}

	bound := VariableBoundMsg{Name: name, Value: rest}
	return func() tea.Msg { return bound }
}

// unbind asks for the variable under the cursor to be dropped.
func (v *Variables) unbind() tea.Cmd {
	variable, ok := v.current()
	if !ok {
		return nil
	}

	unbound := VariableUnboundMsg{Name: variable.Name}
	return func() tea.Msg { return unbound }
}

func (v *Variables) startInput(value string) {
	v.input.SetValue(value)
	v.input.Focus()
	v.input.CursorEnd()
	v.resizeInput()
}

func (v Variables) current() (vars.Variable, bool) {
	if v.cursor < 0 || v.cursor >= v.set.Len() {
		return vars.Variable{}, false
	}
	return v.set.All()[v.cursor], true
}

// assignment renders the variable under the cursor as the text the prompt
// accepts back.
func (v Variables) assignment() string {
	variable, ok := v.current()
	if !ok {
		return ""
	}
	return variable.Name + "=" + variable.Value
}

func (v *Variables) moveTo(i int) {
	v.cursor = clampIndex(i, v.set.Len())
	v.clampOffset()
}

func (v *Variables) clampOffset() {
	v.offset = clampWindow(v.cursor, v.offset, v.visibleRows(), v.set.Len())
}

// visibleRows is how many variables fit, after the title, the blank line and
// the help line.
func (v Variables) visibleRows() int {
	const chrome = 5
	if v.height <= 0 {
		return max(v.set.Len(), 1)
	}
	return max(v.height-chrome, 1)
}

const (
	bindPrompt    = "name=value  "
	capturePrompt = "name=path  "

	variablesHint = "enter edit · a add · d remove · ctrl+p capture · esc close"
	promptHint    = "enter bind · esc back"

	// capturedMark says a value came out of a response rather than out of the
	// config file, which is the difference between one that survives switching
	// environment and one that does not.
	capturedMark = "captured"
)

// View renders the panel as a box the root model centres on the screen.
func (v Variables) View() string {
	// lipgloss counts padding inside Width, so the box is asked for the content
	// width plus it — otherwise the widest row is a cell or two too long and
	// wraps onto a line of its own.
	box := v.styles.Panel.Width(v.inner() + v.styles.Panel.GetHorizontalPadding())

	if v.mode != variablesList {
		return box.Render(strings.Join(v.promptLines(), "\n"))
	}

	lines := []string{v.styles.PanelTitle.Render(v.title()), ""}

	if v.set.Len() == 0 {
		lines = append(lines, v.styles.Muted.Render(
			"Nothing bound. Press a to add one, or ctrl+p to capture one from the response."))
	} else {
		visible := min(v.visibleRows(), v.set.Len()-v.offset)
		for i := v.offset; i < v.offset+visible; i++ {
			lines = append(lines, v.row(i))
		}
	}

	if v.notice != "" {
		lines = append(lines, "", v.noticeLine())
	}
	lines = append(lines, "", v.styles.Hint.Render(variablesHint))
	return box.Render(strings.Join(lines, "\n"))
}

// title names the environment the bindings belong to, so that a list of values
// is never read against the wrong one.
func (v Variables) title() string {
	if v.environment == "" {
		return fmt.Sprintf("Variables (%d)", v.set.Len())
	}
	return fmt.Sprintf("Variables · %s (%d)", v.environment, v.set.Len())
}

func (v Variables) promptLines() []string {
	title := "Bind a variable"
	hint := promptHint
	if v.mode == variablesCapture {
		title = "Capture from the response"
		hint = "enter capture · esc back"
	}

	lines := []string{
		v.styles.PanelTitle.Render(title),
		"",
		v.styles.Label.Render(v.prompt()) + v.input.View(),
	}

	if v.mode == variablesCapture {
		lines = append(lines, "",
			styles.Truncate(v.styles.Muted.Render(
				"the path is the one the form shows: user.id, items[0].sku"), v.inner()))
	}

	if v.notice != "" {
		lines = append(lines, "", v.noticeLine())
	}
	return append(lines, "", v.styles.Hint.Render(hint))
}

func (v Variables) prompt() string {
	if v.mode == variablesCapture {
		return capturePrompt
	}
	return bindPrompt
}

func (v Variables) noticeLine() string {
	style := v.styles.Hint
	if v.failed {
		style = v.styles.FieldError
	}
	return styles.Truncate(style.Render(v.notice), v.inner())
}

func (v Variables) row(i int) string {
	variable := v.set.All()[i]

	name := v.styles.FieldName.Render(padCell(variable.Name, v.nameWidth()))
	value := v.styles.FieldValue.Render(variable.Value)
	if variable.Value == "" {
		value = v.styles.FieldDisabled.Render(emptyLabel)
	}

	text := name + "  " + value
	if variable.Captured {
		text += "  " + v.styles.Muted.Render("· "+capturedMark)
	}

	if i != v.cursor {
		return styles.Truncate("  "+text, v.inner())
	}
	return v.styles.Cursor.Render(styles.Truncate("❯ "+text, v.inner()))
}

func (v Variables) nameWidth() int {
	w := 0
	for _, variable := range v.set.All() {
		w = max(w, lipgloss.Width(variable.Name))
	}
	return min(max(w, 1), maxNameWidth)
}

// inner is the box's content width: enough for the hint line and the widest
// binding, bounded by the screen.
func (v Variables) inner() int {
	want := max(minBoxWidth, lipgloss.Width(variablesHint))
	for _, variable := range v.set.All() {
		const gaps = 2 + 2 // the cursor gutter and the gap between the columns
		want = max(want, gaps+v.nameWidth()+lipgloss.Width(variable.Value))
	}
	return min(want, v.available())
}

// available is the widest the box's contents may be on this screen. A terminal
// too narrow to hold a bordered box at all still has to yield a number the box
// can be built from, so the result has a floor; the root model clamps what
// comes out, as it does for every other panel with a minimum it cannot always
// be given.
func (v Variables) available() int {
	const chrome = 4 // the box's border and padding
	return max(v.width-chrome, minValueWidth)
}
