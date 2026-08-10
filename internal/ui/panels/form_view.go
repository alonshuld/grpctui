// form_view.go renders the field tree. A frame is a []line — one per visible
// row — and the column widths are measured once for the whole frame.

package panels

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

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
	// A reference is shown as itself, whatever kind of row holds it: what the
	// row says is not what the server will see, and a form where a template and
	// a literal look identical is one you cannot read at a glance.
	if node.Refers() {
		return f.styles.FieldRef.Render(node.Value())
	}

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
