package panels

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// SendRequestMsg asks the root model to invoke a method. [Form.Submit] returns
// one only once every value in the form has converted cleanly into a request
// message, so the root never has to think about validation; sending it as a
// message is how anything else — v0.6's history, say — asks for the same call.
type SendRequestMsg struct {
	Service grpcclient.Service
	Method  grpcclient.Method
	Request proto.Message
}

// Layout constants for a field row.
const (
	maxNameWidth   = 24
	maxTypeWidth   = 22
	minValueWidth  = 8
	rowPrefixWidth = 2

	// boolTrue and boolFalse are the two values a bool field stores once it has
	// been toggled. A toggled-off bool holds "false" rather than "", so that an
	// `optional bool` can be sent as an explicit false; an untouched one holds
	// "" and is left out of the request entirely. For a plain proto3 bool the
	// distinction costs nothing — setting one to false puts nothing on the wire.
	boolTrue  = "true"
	boolFalse = "false"

	// unsetLabel and emptyLabel mark the two states a field with explicit
	// presence can be in that an ordinary field cannot: never filled in, and
	// filled in with nothing. Only such a field ever shows either.
	unsetLabel = "unset"
	emptyLabel = `""`
)

// Form is the request form for the selected method: one row per field of the
// method's input message, filled in and sent with ctrl+s.
//
// It has two modes. Browsing moves the cursor between fields with j/k, the same
// as everywhere else in grpctui; editing hands every key to the field's text
// input, which is why q stops meaning "quit" while it lasts. The root model
// asks [Form.Editing] before claiming a key for itself.
type Form struct {
	keys   keys.KeyMap
	styles styles.Styles

	service grpcclient.Service
	method  grpcclient.Method
	schema  protoschema.Form
	fields  []formField

	selected bool
	editing  bool
	cursor   int
	offset   int

	// fieldErrs holds the last build's per-field complaints, keyed by field
	// name; notice holds a problem with the form as a whole.
	fieldErrs map[string]string
	notice    string

	// lines is the rendered panel, cached. One keystroke asks for it four to six
	// times over — the scroll clamp, the root model's layout, this panel's own
	// View — and building it is O(fields) of styled string assembly, which is
	// the difference between a responsive form and a sluggish one on the large
	// schemas v1.0 targets. Every mutation refreshes it; [Form.render] rebuilds
	// on the fly when it is nil, so a Form that has never been touched is still
	// correct.
	lines []line

	width   int
	height  int
	focused bool
}

// formField pairs a schema field with the text input holding its value. Bool
// fields keep an input too — never focused, toggled with space — so that every
// field has exactly one home for its value.
type formField struct {
	field protoschema.Field
	input textinput.Model

	// touched records that the user has changed this field's value, which is
	// how an explicitly cleared field is told apart from one never visited. It
	// is the difference between sending an `optional string` as "" and leaving
	// it unset; see [protoschema.Field.AcceptsEmpty].
	touched bool
}

// NewForm builds an empty request form.
func NewForm(km keys.KeyMap, st styles.Styles) Form {
	return Form{keys: km, styles: st}
}

// SetMethod rebuilds the form for a method, discarding whatever was typed into
// the previous one.
func (f *Form) SetMethod(svc grpcclient.Service, m grpcclient.Method) {
	f.service = svc
	f.method = m
	f.schema = protoschema.NewForm(m.InputDescriptor())
	f.selected = true
	f.editing = false
	f.cursor = 0
	f.offset = 0
	f.fieldErrs = nil
	f.notice = ""

	f.fields = make([]formField, 0, len(f.schema.Fields))
	for _, field := range f.schema.Fields {
		f.fields = append(f.fields, formField{field: field, input: newFieldInput(f.styles)})
	}
	f.resizeInputs()
	f.moveTo(0)
}

// newFieldInput builds the text input backing one field.
func newFieldInput(st styles.Styles) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.TextStyle = st.FieldValue
	ti.PlaceholderStyle = st.FieldDisabled

	// A blinking cursor would make every frame of the UI time-dependent, which
	// costs a redraw a second for no information and makes golden files racy.
	ti.Cursor.SetMode(cursor.CursorStatic)
	return ti
}

// Clear returns the panel to its empty state.
func (f *Form) Clear() {
	*f = Form{keys: f.keys, styles: f.styles, width: f.width, height: f.height, focused: f.focused}
	f.refresh()
}

// SetSize sets the panel's inner content area.
func (f *Form) SetSize(width, height int) {
	f.width = width
	f.height = height
	f.resizeInputs()
	f.clampOffset()
}

// Focus gives the panel focus.
func (f *Form) Focus() {
	f.focused = true
	f.refresh()
}

// Blur removes focus from the panel, ending any edit in progress.
func (f *Form) Blur() {
	f.StopEditing()
	f.focused = false
	f.refresh()
}

// Focused reports whether the panel has focus.
func (f Form) Focused() bool { return f.focused }

// Editing reports whether a field is being typed into. While it is, the root
// model must leave every key alone but ctrl+c, ctrl+s and the panel switches.
func (f Form) Editing() bool { return f.editing }

// StopEditing ends any edit in progress.
func (f *Form) StopEditing() {
	if !f.editing {
		return
	}
	f.editing = false
	f.fields[f.cursor].input.Blur()
	f.refresh()
}

// Method returns the method the form was built for, if any.
func (f Form) Method() (grpcclient.Method, bool) { return f.method, f.selected }

// ContentHeight reports how many lines the form wants, so the root model can
// give the response panel everything the form does not need.
func (f Form) ContentHeight() int { return len(f.render()) }

// Update handles key messages when the panel is focused.
func (f Form) Update(msg tea.Msg) (Form, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !f.focused || !f.selected {
		return f, nil
	}
	if f.editing {
		return f.updateEditing(keyMsg)
	}
	return f.updateBrowsing(keyMsg)
}

// updateEditing routes keys to the focused text input. Only the keys that end
// the edit are intercepted; everything else — including j, k and q — is a
// character.
func (f Form) updateEditing(msg tea.KeyMsg) (Form, tea.Cmd) {
	if key.Matches(msg, f.keys.Cancel) || key.Matches(msg, f.keys.Select) {
		f.StopEditing()
		return f, nil
	}

	before := f.fields[f.cursor].input.Value()

	var cmd tea.Cmd
	f.fields[f.cursor].input, cmd = f.fields[f.cursor].input.Update(msg)

	// Touched on a change, not on entering the edit: a user who opens a field
	// and escapes straight back out has not asked for an explicit empty value.
	if f.fields[f.cursor].input.Value() != before {
		f.fields[f.cursor].touched = true
	}
	f.refresh()
	return f, cmd
}

func (f Form) updateBrowsing(msg tea.KeyMsg) (Form, tea.Cmd) {
	switch {
	case key.Matches(msg, f.keys.Up):
		f.moveCursor(-1)
	case key.Matches(msg, f.keys.Down):
		f.moveCursor(1)
	case key.Matches(msg, f.keys.PageUp):
		f.moveCursor(-f.pageSize())
	case key.Matches(msg, f.keys.PageDown):
		f.moveCursor(f.pageSize())
	case key.Matches(msg, f.keys.Top):
		f.moveTo(0)
	case key.Matches(msg, f.keys.Bottom):
		f.moveTo(len(f.fields) - 1)
	case key.Matches(msg, f.keys.Toggle):
		f.toggle()
	case key.Matches(msg, f.keys.Select):
		f.startEditing()
	}
	return f, nil
}

// Submit converts the form's values into a request message, or explains on the
// panel why it cannot.
func (f *Form) Submit() (SendRequestMsg, bool) {
	// Every path below changes what the panel shows — an error row appears, or
	// the last attempt's rows go away — so the cache is refreshed once, here,
	// rather than at each of the four returns.
	defer f.refresh()

	f.fieldErrs = nil
	f.notice = ""

	switch {
	case !f.selected:
		f.notice = "Select a method first."
		return SendRequestMsg{}, false
	case f.method.Kind() != grpcclient.KindUnary:
		f.notice = "Streaming methods are callable from v0.5."
		return SendRequestMsg{}, false
	}

	req, err := f.schema.Build(f.values())
	if err != nil {
		f.setBuildError(err)
		return SendRequestMsg{}, false
	}
	return SendRequestMsg{Service: f.service, Method: f.method, Request: req}, true
}

// values collects what the user typed, keyed by field name.
//
// A field the user has never touched is left out altogether rather than mapped
// to "". That absence is what tells [protoschema.Form.Build] the difference
// between a field nobody filled in and one deliberately cleared — which for an
// `optional` string is the difference between sending nothing and sending "".
func (f Form) values() map[string]string {
	out := make(map[string]string, len(f.fields))
	for _, ff := range f.fields {
		value := ff.input.Value()
		if value == "" && !ff.touched {
			continue
		}
		out[ff.field.Name] = value
	}
	return out
}

// setBuildError splits a build failure into the per-field complaints the rows
// show and, for anything that is not about one field, a panel-level notice.
func (f *Form) setBuildError(err error) {
	f.fieldErrs = make(map[string]string)

	errs := []error{err}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		errs = joined.Unwrap()
	}

	for _, e := range errs {
		var fieldErr *protoschema.FieldError
		if errors.As(e, &fieldErr) {
			f.fieldErrs[fieldErr.Field] = fieldErr.Err.Error()
			continue
		}
		f.notice = e.Error()
	}
}

func (f *Form) startEditing() {
	ff, ok := f.current()
	if !ok || !ff.field.Editable() || ff.field.Kind == protoschema.KindBool {
		return
	}
	f.editing = true
	f.fields[f.cursor].input.Focus()
	f.fields[f.cursor].input.CursorEnd()
	f.refresh()
}

// toggle flips a bool field between an explicit true and an explicit false.
// Neither is the untouched state the field started in, which is why toggling
// twice is not the same as never toggling at all: for an `optional bool` the
// first sends false and the second sends nothing.
func (f *Form) toggle() {
	ff, ok := f.current()
	if !ok || ff.field.Kind != protoschema.KindBool {
		return
	}

	value := boolTrue
	if ff.input.Value() == boolTrue {
		value = boolFalse
	}
	f.fields[f.cursor].input.SetValue(value)
	f.fields[f.cursor].touched = true
	f.refresh()
}

func (f Form) current() (formField, bool) {
	if f.cursor < 0 || f.cursor >= len(f.fields) {
		return formField{}, false
	}
	return f.fields[f.cursor], true
}

func (f *Form) moveCursor(delta int) { f.moveTo(f.cursor + delta) }

func (f *Form) moveTo(i int) {
	switch {
	case len(f.fields) == 0:
		f.cursor = 0
	case i < 0:
		f.cursor = 0
	case i >= len(f.fields):
		f.cursor = len(f.fields) - 1
	default:
		f.cursor = i
	}
	f.clampOffset()
}

// pageSize reports how many fields a page jump moves the cursor by.
//
// It counts the field rows currently on screen rather than the panel's height,
// because the two are not the same number: the header takes three lines before
// the first field, and every error row and enum hint takes another. Paging by
// the height would step over fields the user never saw.
func (f Form) pageSize() int {
	lines := f.render()

	rows := 0
	for i := f.offset; i < len(lines) && i < f.offset+f.height; i++ {
		if lines[i].field != noField {
			rows++
		}
	}

	// One row of overlap, so a page jump keeps a landmark from the page before.
	return max(rows-1, 1)
}

// clampOffset scrolls just far enough to keep the cursor's row on screen.
//
// It refreshes the render cache first. Every mutation that moves the cursor or
// resizes the panel comes through here, so doing it in one place is what keeps
// the cache from going stale.
func (f *Form) clampOffset() {
	f.refresh()

	lines := f.lines
	if f.height <= 0 || len(lines) <= f.height {
		f.offset = 0
		return
	}

	row := f.cursorLine(lines)
	if row < f.offset {
		f.offset = row
	}
	if row >= f.offset+f.height {
		f.offset = row - f.height + 1
	}
	if maxOffset := len(lines) - f.height; f.offset > maxOffset {
		f.offset = maxOffset
	}
	if f.offset < 0 {
		f.offset = 0
	}
}

func (f Form) cursorLine(lines []line) int {
	for i, l := range lines {
		if l.field == f.cursor {
			return i
		}
	}
	return 0
}

// resizeInputs gives every text input the width left over after the name and
// type columns.
//
// One cell more than the columns is taken: bubbles/textinput pads its value to
// Width and then leaves room for the cursor past the end, so a view of Width
// occupies Width+1 cells.
func (f *Form) resizeInputs() {
	const gaps = 3 // one either side of the type column, one for the cursor
	width := max(f.width-f.nameWidth()-f.typeWidth()-rowPrefixWidth-gaps, minValueWidth)
	for i := range f.fields {
		f.fields[i].input.Width = width
	}
}

// View renders the panel body. It does not draw its own border; the root model
// frames it.
func (f Form) View() string {
	lines := f.render()

	visible := f.height
	if visible <= 0 || visible > len(lines)-f.offset {
		visible = len(lines) - f.offset
	}
	if visible <= 0 {
		return ""
	}

	out := make([]string, 0, visible)
	for i := f.offset; i < f.offset+visible; i++ {
		out = append(out, lines[i].text)
	}
	return strings.Join(out, "\n")
}

// line is one rendered row. field indexes the form field the row belongs to, or
// [noField] for headers, hints and error rows, so that scrolling can find the
// cursor without re-deriving the layout.
type line struct {
	text  string
	field int
}

const noField = -1

// render returns the panel's rows, from the cache when there is one.
func (f Form) render() []line {
	if f.lines != nil {
		return f.lines
	}
	return f.build()
}

// refresh rebuilds the render cache. Every mutation calls it.
func (f *Form) refresh() { f.lines = f.build() }

// build lays the whole panel out as rows, before scrolling.
func (f Form) build() []line {
	if !f.selected {
		lines := []line{plain(f.styles.Muted.Render("Select a method to build a request."))}
		return append(lines, f.noticeLines()...)
	}

	lines := f.header()
	if len(f.fields) == 0 {
		lines = append(lines, plain(f.styles.Muted.Render("This request has no fields.")))
	}

	for i, ff := range f.fields {
		lines = append(lines, line{text: f.fieldRow(i), field: i})
		if msg, ok := f.fieldErrs[ff.field.Name]; ok {
			lines = append(lines, plain(f.indented(f.styles.FieldError.Render("⚠ "+msg))))
		}
		if hint := f.hint(i); hint != "" {
			lines = append(lines, plain(f.indented(f.styles.FieldDisabled.Render(hint))))
		}
	}

	return append(lines, f.noticeLines()...)
}

// noticeLines renders the panel-level complaint from the last send attempt, if
// there was one.
func (f Form) noticeLines() []line {
	if f.notice == "" {
		return nil
	}
	return []line{plain(""), plain(styles.Truncate(f.styles.FieldError.Render(f.notice), f.width))}
}

func plain(text string) line { return line{text: text, field: noField} }

// header names the method the form builds a request for. The response type
// belongs to the response panel, not here.
func (f Form) header() []line {
	name := f.styles.PanelTitle.Render(f.method.FullName)
	kind := f.styles.FieldType.Render(string(f.method.Kind()))

	return []line{
		plain(styles.Truncate(name+"  "+kind, f.width)),
		plain(styles.Truncate(f.styles.Label.Render(f.method.InputType), f.width)),
		plain(""),
	}
}

func (f Form) fieldRow(i int) string {
	ff := f.fields[i]

	prefix := "  "
	name := f.styles.FieldName.Render(padCell(ff.field.Name, f.nameWidth()))
	if i == f.cursor {
		// A gutter glyph, not a highlighted row: the row already carries a text
		// input, and a background behind it would fight the cursor inside it.
		markerStyle := f.styles.CursorUnfocused
		if !f.focused {
			markerStyle = f.styles.Marker
		}
		prefix = markerStyle.Render("❯ ")
		name = f.styles.FieldName.Bold(true).Render(padCell(ff.field.Name, f.nameWidth()))
	}

	row := prefix + name + " " +
		f.styles.FieldType.Render(padCell(ff.field.Type, f.typeWidth())) + " " +
		f.valueCell(i)
	return styles.Truncate(row, f.width)
}

// valueCell renders the right-hand column: the live text input while editing,
// and a static rendering of the value otherwise.
func (f Form) valueCell(i int) string {
	ff := f.fields[i]

	switch {
	case !ff.field.Editable():
		return f.styles.FieldDisabled.Render(ff.field.Note)
	case f.editing && i == f.cursor:
		return ff.input.View()
	case ff.field.Kind == protoschema.KindBool:
		// A bool with presence has three states, not two: an untouched one is
		// not sent at all, and rendering it as "false" would claim it was.
		if !ff.touched && ff.field.HasPresence() {
			return f.styles.FieldDisabled.Render(unsetLabel)
		}
		return f.styles.FieldValue.Render(boolLabel(ff.input.Value()))
	case ff.input.Value() != "":
		return f.styles.FieldValue.Render(ff.input.Value())
	case ff.touched && ff.field.HasPresence():
		// Cleared rather than never visited, which for a field with presence is
		// the difference between sending "" and sending nothing. The two look
		// identical unless the row says so.
		return f.styles.FieldValue.Render(emptyLabel)
	case i == f.cursor && f.focused:
		return f.styles.FieldDisabled.Render("press enter to edit")
	default:
		return ""
	}
}

// hint spells out what the cursor's field will accept. Enums are the case that
// needs it: v0.2 types their values in by hand, and nothing else on screen says
// what they are. (v0.3 replaces this with a picker.)
func (f Form) hint(i int) string {
	ff := f.fields[i]
	if i != f.cursor || !f.focused || ff.field.Kind != protoschema.KindEnum {
		return ""
	}

	names := make([]string, 0, len(ff.field.Enum))
	for _, ev := range ff.field.Enum {
		names = append(names, ev.Name)
	}
	return "one of: " + strings.Join(names, ", ")
}

// indented aligns a continuation row under the value column.
func (f Form) indented(s string) string {
	pad := rowPrefixWidth + f.nameWidth() + 1
	return styles.Truncate(strings.Repeat(" ", pad)+s, f.width)
}

func boolLabel(value string) string {
	if value == boolTrue {
		return boolTrue
	}
	return "false"
}

func (f Form) nameWidth() int {
	return f.columnWidth(func(ff formField) string { return ff.field.Name }, maxNameWidth)
}

func (f Form) typeWidth() int {
	return f.columnWidth(func(ff formField) string { return ff.field.Type }, maxTypeWidth)
}

// columnWidth sizes a column to its widest entry, up to a limit.
func (f Form) columnWidth(of func(formField) string, limit int) int {
	w := 0
	for _, ff := range f.fields {
		w = max(w, lipgloss.Width(of(ff)))
	}
	return min(w, limit)
}

// padCell fits s into exactly width cells, truncating or padding as needed.
func padCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = styles.Truncate(s, width)
	if pad := width - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}
