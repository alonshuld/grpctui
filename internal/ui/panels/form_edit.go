// form_edit.go holds the editing half of the request form: the key handling,
// the cursor, and the tree operations a keystroke performs on a node.

package panels

import (
	"errors"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alonshuld/grpctui/internal/protoschema"
)

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

	// The template is built from the same tree that just built cleanly, so it
	// can only fail on something the wire form did not care about. That is worth
	// a request recorded without its references rather than a request refused.
	template, values, err := f.schema.Template()
	if err != nil {
		template, values = nil, nil
	}

	return SendRequestMsg{
		Service:  f.service,
		Method:   f.method,
		Request:  req,
		Template: template,
		Values:   values,
	}, true
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
