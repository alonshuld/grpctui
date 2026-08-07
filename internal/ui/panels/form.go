package panels

import (
	"errors"
	"fmt"
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
	maxNameWidth   = 28
	maxTypeWidth   = 22
	minValueWidth  = 8
	rowPrefixWidth = 2
	indentWidth    = 2

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

// Glyphs for the two things a row can be beyond a plain field: something that
// holds other rows, and one of a set to pick between.
const (
	glyphExpanded  = "▾"
	glyphCollapsed = "▸"
	glyphOn        = "●"
	glyphOff       = "○"
)

// Form is the request form for the selected method: one row per field of the
// method's input message, filled in and sent with ctrl+s.
//
// Since v0.3 the rows are a tree rather than a list — a nested message expands
// to its fields, a repeated field to its items, an enum to its values, a oneof
// to its variants — but the panel walks a generic [protoschema.Node] and never
// asks what protobuf kind is underneath.
//
// It has two modes. Browsing moves the cursor between rows with j/k, the same
// as everywhere else in grpctui; editing hands every key to the field's text
// input, which is why q stops meaning "quit" while it lasts. The root model
// asks [Form.Editing] before claiming a key for itself.
type Form struct {
	keys   keys.KeyMap
	styles styles.Styles

	service grpcclient.Service
	method  grpcclient.Method
	schema  protoschema.Form

	// rows is the flattened, visible tree: what the panel draws and what the
	// cursor indexes. It is rebuilt whenever the tree's shape changes.
	rows []*protoschema.Node

	// input edits whichever row the cursor is on. One input is enough because
	// only one row is ever edited at a time, and the tree grows and shrinks
	// under it — a per-row input would have to be created and destroyed with
	// every item added.
	input textinput.Model

	selected bool
	editing  bool
	cursor   int
	offset   int

	// fieldErrs holds the last build's complaints, keyed by [protoschema.Node]
	// path; notice holds a problem with the form as a whole.
	fieldErrs map[string]string
	notice    string

	// lines is the rendered panel, cached. One keystroke asks for it four to six
	// times over — the scroll clamp, the root model's layout, this panel's own
	// View — and building it is O(rows) of styled string assembly, which is the
	// difference between a responsive form and a sluggish one on the large
	// schemas v1.0 targets. Every mutation refreshes it; [Form.render] rebuilds
	// on the fly when it is nil, so a Form that has never been touched is still
	// correct.
	lines []line

	width   int
	height  int
	focused bool
}

// NewForm builds an empty request form.
func NewForm(km keys.KeyMap, st styles.Styles) Form {
	return Form{keys: km, styles: st, input: newFieldInput(st)}
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
	f.input.Blur()

	f.rows = f.schema.Rows()
	f.resizeInput()
	f.moveTo(0)
}

// newFieldInput builds the text input rows are edited through.
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
	*f = Form{
		keys:    f.keys,
		styles:  f.styles,
		input:   newFieldInput(f.styles),
		width:   f.width,
		height:  f.height,
		focused: f.focused,
	}
	f.refresh()
}

// SetSize sets the panel's inner content area.
func (f *Form) SetSize(width, height int) {
	f.width = width
	f.height = height
	f.resizeInput()
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

// StopEditing ends any edit in progress, checking what was typed so that a typo
// is reported where it was made rather than when the call is sent.
func (f *Form) StopEditing() {
	if !f.editing {
		return
	}
	f.editing = false
	f.input.Blur()

	if node, ok := f.current(); ok {
		f.setFieldError(node)
	}
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

// updateEditing routes keys to the text input. Only the keys that end the edit
// are intercepted; everything else — including j, k and q — is a character.
func (f Form) updateEditing(msg tea.KeyMsg) (Form, tea.Cmd) {
	if key.Matches(msg, f.keys.Cancel) || key.Matches(msg, f.keys.Select) {
		f.StopEditing()
		return f, nil
	}

	before := f.input.Value()

	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)

	// The row is only touched on a change, not on entering the edit: a user who
	// opens a field and escapes straight back out has not asked for an explicit
	// empty value.
	if node, ok := f.current(); ok && f.input.Value() != before {
		node.SetValue(f.input.Value())
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
		f.moveTo(len(f.rows) - 1)
	case key.Matches(msg, f.keys.Expand):
		f.expand()
	case key.Matches(msg, f.keys.Collapse):
		f.collapse()
	case key.Matches(msg, f.keys.Add):
		f.addItem()
	case key.Matches(msg, f.keys.Remove):
		f.removeItem()
	case key.Matches(msg, f.keys.Toggle):
		f.toggle()
	case key.Matches(msg, f.keys.Select):
		f.selectRow()
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

	if !f.selected {
		f.notice = "Select a method first."
		return SendRequestMsg{}, false
	}

	req, err := f.schema.Build()
	if err != nil {
		f.setBuildError(err)
		return SendRequestMsg{}, false
	}
	return SendRequestMsg{Service: f.service, Method: f.method, Request: req}, true
}

// setBuildError splits a build failure into the per-row complaints the rows
// show and, for anything that is not about one row, a panel-level notice.
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
			f.fieldErrs[fieldErr.Path] = fieldErr.Err.Error()
			continue
		}
		f.notice = e.Error()
	}
}

// setFieldError checks one row on its own, which is what an ending edit can
// report without pretending to know about the rest of the form.
func (f *Form) setFieldError(node *protoschema.Node) {
	err := node.Validate()
	if err == nil {
		delete(f.fieldErrs, node.Path())
		return
	}
	if f.fieldErrs == nil {
		f.fieldErrs = make(map[string]string)
	}
	f.fieldErrs[node.Path()] = err.Error()
}

// selectRow is enter: it edits a value, picks a variant, or opens whatever the
// row holds.
func (f *Form) selectRow() {
	node, ok := f.current()
	if !ok {
		return
	}

	switch {
	case node.Editable():
		f.startEditing(node)
	case node.Radio() || node.Kind() == protoschema.KindBool:
		f.toggle()
	case node.Expandable():
		f.setExpanded(node, !node.Expanded())
	}
}

func (f *Form) startEditing(node *protoschema.Node) {
	f.editing = true
	f.input.SetValue(node.Value())
	f.input.Focus()
	f.input.CursorEnd()
	f.refresh()
}

// toggle flips whatever the row under the cursor can change without typing: a
// bool, the picked variant of a oneof, the chosen value of an enum, or whether
// an empty nested message is sent at all.
func (f *Form) toggle() {
	node, ok := f.current()
	if !ok {
		return
	}

	// Picking an enum value folds the list of values away with it, so the cursor
	// goes back to the row that now shows what was picked.
	keep := node
	if node.Kind() == protoschema.KindChoice {
		keep = node.Parent()
	}

	node.Toggle()
	f.rebuild(keep)
}

func (f *Form) expand() {
	if node, ok := f.current(); ok && node.Expandable() {
		f.setExpanded(node, true)
	}
}

// collapse folds the row under the cursor. From a row that holds nothing it
// jumps to the row that holds it, which is the behaviour a file-tree user
// expects.
func (f *Form) collapse() {
	node, ok := f.current()
	if !ok {
		return
	}
	if node.Expandable() && node.Expanded() {
		f.setExpanded(node, false)
		return
	}
	if parent := node.Parent(); parent != nil {
		f.moveToNode(parent)
	}
}

func (f *Form) setExpanded(node *protoschema.Node, expanded bool) {
	node.SetExpanded(expanded)
	f.rebuild(node)
}

// addItem appends an item to the repeated field the cursor is on or in, and
// puts the cursor on the new item, ready to be filled in.
//
// Adding from inside a list rather than from the list's own row is the same
// keystroke doing the same thing wherever in the list the cursor happens to be,
// which matters once the items are messages several rows tall.
func (f *Form) addItem() {
	node, ok := f.current()
	if !ok {
		return
	}

	for ; node != nil; node = node.Parent() {
		if item := node.AddItem(); item != nil {
			f.rebuild(item)
			return
		}
	}
}

// removeItem takes the item under the cursor out of the list holding it. The
// cursor stays on the row rather than following the item out, so the next item
// slides up under it and clearing several is one keystroke each.
func (f *Form) removeItem() {
	node, ok := f.current()
	if !ok || !node.CanRemove() {
		return
	}

	node.Remove()
	f.rebuild(nil)
}

func (f Form) current() (*protoschema.Node, bool) {
	if f.cursor < 0 || f.cursor >= len(f.rows) {
		return nil, false
	}
	return f.rows[f.cursor], true
}

// rebuild refreshes the row list after the tree's shape has changed, keeping
// the cursor on the row it was on — or, when that row has just been removed, at
// the position it occupied.
func (f *Form) rebuild(keep *protoschema.Node) {
	f.rows = f.schema.Rows()
	f.moveToNode(keep)
}

func (f *Form) moveToNode(node *protoschema.Node) {
	for i, n := range f.rows {
		if n == node {
			f.moveTo(i)
			return
		}
	}
	f.moveTo(f.cursor)
}

func (f *Form) moveCursor(delta int) { f.moveTo(f.cursor + delta) }

func (f *Form) moveTo(i int) {
	f.cursor = clampIndex(i, len(f.rows))
	f.clampOffset()
}

// pageSize reports how many rows a page jump moves the cursor by.
//
// It counts the field rows currently on screen rather than the panel's height,
// because the two are not the same number: the header takes three lines before
// the first field, and every error row takes another. Paging by the height
// would step over rows the user never saw.
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

	// The window counts rendered lines, not rows: a row owns a line plus however
	// many error lines follow it, so the cursor has to be translated into a line
	// before it can be scrolled to.
	f.offset = clampWindow(f.cursorLine(f.lines), f.offset, f.height, len(f.lines))
}

func (f Form) cursorLine(lines []line) int {
	for i, l := range lines {
		if l.field == f.cursor {
			return i
		}
	}
	return 0
}

// resizeInput gives the text input the width left over after the name and type
// columns.
//
// One cell more than the columns is taken: bubbles/textinput pads its value to
// Width and then leaves room for the cursor past the end, so a view of Width
// occupies Width+1 cells.
func (f *Form) resizeInput() {
	const gaps = 3 // one either side of the type column, one for the cursor
	f.input.Width = max(f.width-f.nameWidth()-f.typeWidth()-rowPrefixWidth-gaps, minValueWidth)
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

// line is one rendered row. field indexes the form row the line belongs to, or
// [noField] for headers and error lines, so that scrolling can find the cursor
// without re-deriving the layout.
type line struct {
	text  string
	field int
}

const noField = -1

// render returns the panel's lines, from the cache when there is one.
func (f Form) render() []line {
	if f.lines != nil {
		return f.lines
	}
	return f.build()
}

// refresh rebuilds the render cache. Every mutation calls it — including the
// ones that change the rows, and with them how wide the name column has to be.
func (f *Form) refresh() {
	f.resizeInput()
	f.lines = f.build()
}

// build lays the whole panel out as lines, before scrolling.
func (f Form) build() []line {
	if !f.selected {
		lines := []line{plain(f.styles.Muted.Render("Select a method to build a request."))}
		return append(lines, f.noticeLines()...)
	}

	lines := f.header()
	if len(f.rows) == 0 {
		lines = append(lines, plain(f.styles.Muted.Render("This request has no fields.")))
	}

	for i, node := range f.rows {
		lines = append(lines, line{text: f.fieldRow(i), field: i})
		if msg, ok := f.fieldErrs[node.Path()]; ok {
			lines = append(lines, plain(f.indented(f.styles.FieldError.Render("⚠ "+msg))))
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
	node := f.rows[i]

	prefix := "  "
	nameStyle := f.styles.FieldName
	if i == f.cursor {
		// A gutter glyph, not a highlighted row: the row already carries a text
		// input, and a background behind it would fight the cursor inside it.
		markerStyle := f.styles.CursorUnfocused
		if !f.focused {
			markerStyle = f.styles.Marker
		}
		prefix = markerStyle.Render("❯ ")
		nameStyle = nameStyle.Bold(true)
	}

	row := prefix + nameStyle.Render(padCell(f.label(node), f.nameWidth())) + " " +
		f.styles.FieldType.Render(padCell(node.Type(), f.typeWidth())) + " " +
		f.valueCell(i)
	return styles.Truncate(row, f.width)
}

// label is the left-hand column: the row's name, indented to its depth and
// preceded by whatever glyphs say what kind of row it is.
func (f Form) label(node *protoschema.Node) string {
	label := strings.Repeat(" ", node.Depth()*indentWidth)

	if node.Radio() {
		glyph := glyphOff
		if node.Selected() {
			glyph = glyphOn
		}
		label += glyph + " "
	}
	if node.Expandable() {
		glyph := glyphCollapsed
		if node.Expanded() {
			glyph = glyphExpanded
		}
		label += glyph + " "
	}
	return label + node.Name()
}

// valueCell renders the right-hand column: the live text input while editing, a
// static rendering of the value otherwise, and for a row that holds other rows
// a summary of what is inside it.
func (f Form) valueCell(i int) string {
	node := f.rows[i]

	switch {
	case node.Note() != "":
		return f.styles.FieldDisabled.Render(node.Note())
	case f.editing && i == f.cursor:
		return f.input.View()
	default:
		return f.settledValue(i, node)
	}
}

// settledValue renders the value column of a row that is not being edited.
func (f Form) settledValue(i int, node *protoschema.Node) string {
	switch node.Kind() {
	case protoschema.KindBool:
		// A bool with presence has three states, not two: an untouched one is not
		// sent at all, and rendering it as "false" would claim it was.
		if !node.Touched() && node.HasPresence() {
			return f.styles.FieldDisabled.Render(unsetLabel)
		}
		return f.styles.FieldValue.Render(boolLabel(node.Value()))

	case protoschema.KindEnum:
		return f.summary(i, node.Value(), "press enter to choose")

	case protoschema.KindList, protoschema.KindMap:
		if n := node.Items(); n > 0 {
			return f.styles.FieldValue.Render(fmt.Sprintf("%d %s", n, plural(n, "item")))
		}
		return f.hint(i, "press a to add an item")

	case protoschema.KindMessage:
		filled := ""
		if node.Filled() {
			filled = "set"
		}
		return f.summary(i, filled, "press space to send it empty")

	case protoschema.KindOneof:
		return f.summary(i, node.Active(), "press enter to pick one")

	case protoschema.KindChoice:
		// The radio glyph beside the name has already said everything there is
		// to say about a choice.
		return ""

	default:
		return f.typedValue(i, node)
	}
}

// typedValue renders the value of a row the user types into.
func (f Form) typedValue(i int, node *protoschema.Node) string {
	switch {
	case node.Value() != "":
		return f.styles.FieldValue.Render(node.Value())
	case node.Touched() && node.HasPresence():
		// Cleared rather than never visited, which for a field with presence is
		// the difference between sending "" and sending nothing. The two look
		// identical unless the row says so.
		return f.styles.FieldValue.Render(emptyLabel)
	default:
		return f.hint(i, "press enter to edit")
	}
}

// summary renders what a row holds, falling back to the keystroke that would
// put something in it.
func (f Form) summary(i int, value, hint string) string {
	if value != "" {
		return f.styles.FieldValue.Render(value)
	}
	return f.hint(i, hint)
}

// hint offers the keystroke a row is waiting for, and only for the row the user
// is on: spelling every row's next move out at once is noise, and the row it
// belongs to is the one under the cursor.
func (f Form) hint(i int, text string) string {
	if i != f.cursor || !f.focused {
		return ""
	}
	return f.styles.FieldDisabled.Render(text)
}

// indented aligns a continuation line under the value column.
func (f Form) indented(s string) string {
	pad := rowPrefixWidth + f.nameWidth() + 1
	return styles.Truncate(strings.Repeat(" ", pad)+s, f.width)
}

func boolLabel(value string) string {
	if value == boolTrue {
		return boolTrue
	}
	return boolFalse
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func (f Form) nameWidth() int {
	return f.columnWidth(f.label, maxNameWidth)
}

func (f Form) typeWidth() int {
	return f.columnWidth(func(n *protoschema.Node) string { return n.Type() }, maxTypeWidth)
}

// columnWidth sizes a column to its widest entry, up to a limit.
func (f Form) columnWidth(of func(*protoschema.Node) string, limit int) int {
	w := 0
	for _, node := range f.rows {
		w = max(w, lipgloss.Width(of(node)))
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
