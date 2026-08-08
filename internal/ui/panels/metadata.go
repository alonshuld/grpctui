package panels

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// Layout constants for a header row.
const (
	maxHeaderKeyWidth = 24
	minHeaderValWidth = 8

	// maxMetadataHeight caps how much of the right-hand column the headers may
	// take. A request with a dozen headers is real, and letting it push the
	// response panel down to its minimum would be trading the thing being
	// debugged for the thing being sent; past the cap the panel scrolls.
	maxMetadataHeight = 8
)

// The two cells of a header row.
const (
	columnKey = iota
	columnValue
	columnCount
)

// Metadata is the request-headers panel: the `authorization`, `x-api-key` and
// tenant headers that a real service wants alongside the payload.
//
// The headers it holds are sent with every RPC grpctui makes — including
// reflection, since a server that gates its API behind a header gates its
// schema behind the same one.
//
// A row has two cells rather than one. h/l move between them, which is what
// those keys already mean everywhere else in grpctui (in and out of something),
// and enter edits whichever cell is under the cursor.
type Metadata struct {
	keys   keys.KeyMap
	styles styles.Styles

	headers []grpcclient.Header

	// input edits the cell under the cursor. One is enough: only one cell is
	// ever edited at a time.
	input   textinput.Model
	editing bool

	// notice is a complaint about the headers as a whole rather than about one
	// row — a {{variable}} reference nothing binds, in practice, which belongs
	// to the send rather than to the row it sits on.
	notice string

	cursor int
	column int
	offset int

	width   int
	height  int
	focused bool

	// lines is the rendered panel, cached the same way the request form caches
	// its own: a keystroke asks for it several times over, through the scroll
	// clamp and the root model's layout as well as this panel's View.
	lines []line
}

// NewMetadata builds an empty headers panel.
func NewMetadata(km keys.KeyMap, st styles.Styles) Metadata {
	m := Metadata{keys: km, styles: st, input: newFieldInput(st)}
	m.refresh()
	return m
}

// SetHeaders replaces the panel's contents — what switching connection profile
// does, since headers belong to the connection.
func (m *Metadata) SetHeaders(md grpcclient.Metadata) {
	m.headers = md.Clone()
	m.notice = ""
	m.editing = false
	m.input.Blur()
	m.cursor = 0
	m.column = columnKey
	m.offset = 0
	m.refresh()
}

// Headers returns the headers as the transport layer wants them. The panel's
// own copy is not shared: a call in flight must not see a header the user
// edited after sending it.
func (m Metadata) Headers() grpcclient.Metadata {
	return grpcclient.Metadata(m.headers).Clone()
}

// Validate reports whether the headers can be sent, so that the root model can
// refuse a call rather than have the transport layer reject it.
func (m Metadata) Validate() error {
	return grpcclient.Metadata(m.headers).Validate()
}

// FocusFirstInvalid puts the cursor on the first header that cannot be sent,
// so that a refused call explains itself where the mistake was made rather than
// eight lines below the fold. It reports whether there was one.
func (m *Metadata) FocusFirstInvalid() bool {
	for i, h := range m.headers {
		if h.Disabled {
			continue
		}
		if grpcclient.ValidateHeader(h) != nil {
			m.StopEditing()
			m.moveTo(i)
			return true
		}
	}
	return false
}

// SetNotice puts a line at the foot of the panel, for a complaint that belongs
// to the headers as a whole. The next send replaces it.
func (m *Metadata) SetNotice(text string) {
	m.notice = text
	m.refresh()
}

// Len reports how many headers the panel holds, for the status bar.
func (m Metadata) Len() int { return len(m.headers) }

// Enabled reports how many headers would actually be sent.
func (m Metadata) Enabled() int { return len(grpcclient.Metadata(m.headers).Enabled()) }

// SetSize sets the panel's inner content area.
func (m *Metadata) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.refresh()
}

// Focus gives the panel focus.
func (m *Metadata) Focus() {
	m.focused = true
	m.refresh()
}

// Blur removes focus, ending any edit in progress.
func (m *Metadata) Blur() {
	m.StopEditing()
	m.focused = false
	m.refresh()
}

// Focused reports whether the panel has focus.
func (m Metadata) Focused() bool { return m.focused }

// Editing reports whether a cell is being typed into. While it is, the root
// model must leave every key alone but ctrl+c, ctrl+s and the panel switches.
func (m Metadata) Editing() bool { return m.editing }

// StopEditing ends any edit in progress.
func (m *Metadata) StopEditing() {
	if !m.editing {
		return
	}
	m.editing = false
	m.input.Blur()
	m.refresh()
}

// ContentHeight reports how many lines the panel wants, capped so that a long
// header list scrolls rather than swallowing the response panel.
func (m Metadata) ContentHeight() int { return min(len(m.render()), maxMetadataHeight) }

// Visible reports whether the panel is worth a box on screen.
//
// An empty headers panel is three rows of border saying "nothing here", taken
// from the response — which is the panel a user is actually reading. It appears
// when it has something to show, and when tab arrives at it.
func (m Metadata) Visible() bool { return len(m.headers) > 0 || m.focused }

// Update handles key messages when the panel is focused.
func (m Metadata) Update(msg tea.Msg) (Metadata, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !m.focused {
		return m, nil
	}
	if m.editing {
		return m.updateEditing(keyMsg)
	}
	return m.updateBrowsing(keyMsg)
}

// updateEditing routes keys to the text input. Only the keys that end the edit
// are intercepted; everything else is a character.
func (m Metadata) updateEditing(msg tea.KeyMsg) (Metadata, tea.Cmd) {
	if key.Matches(msg, m.keys.Cancel) || key.Matches(msg, m.keys.Select) {
		m.StopEditing()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.setCell(m.input.Value())
	m.refresh()
	return m, cmd
}

func (m Metadata) updateBrowsing(msg tea.KeyMsg) (Metadata, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.moveCursor(-1)
	case key.Matches(msg, m.keys.Down):
		m.moveCursor(1)
	case key.Matches(msg, m.keys.PageUp):
		m.moveCursor(-m.pageSize())
	case key.Matches(msg, m.keys.PageDown):
		m.moveCursor(m.pageSize())
	case key.Matches(msg, m.keys.Top):
		m.moveTo(0)
	case key.Matches(msg, m.keys.Bottom):
		m.moveTo(len(m.headers) - 1)
	case key.Matches(msg, m.keys.Collapse):
		m.moveColumn(-1)
	case key.Matches(msg, m.keys.Expand):
		m.moveColumn(1)
	case key.Matches(msg, m.keys.Add):
		m.addHeader()
	case key.Matches(msg, m.keys.Remove):
		m.removeHeader()
	case key.Matches(msg, m.keys.Toggle):
		m.toggleHeader()
	case key.Matches(msg, m.keys.Select):
		m.startEditing()
	}
	return m, nil
}

// addHeader appends an empty header and opens it for typing: adding one and
// then having to press enter to fill it in would be two keystrokes for one
// intention.
func (m *Metadata) addHeader() {
	m.headers = append(m.headers, grpcclient.Header{})
	m.column = columnKey
	m.moveTo(len(m.headers) - 1)
	m.startEditing()
}

// removeHeader deletes the header under the cursor. The cursor stays where it
// is, so the next header slides up under it and clearing several is one
// keystroke each.
func (m *Metadata) removeHeader() {
	if len(m.headers) == 0 {
		return
	}
	m.headers = append(m.headers[:m.cursor], m.headers[m.cursor+1:]...)
	m.moveTo(m.cursor)
}

// toggleHeader parks a header without deleting it: off the wire, still on
// screen, one keystroke from coming back. Retyping a bearer token to test
// whether it was the problem is exactly the sort of thing this tool exists to
// avoid.
func (m *Metadata) toggleHeader() {
	if len(m.headers) == 0 {
		return
	}
	m.headers[m.cursor].Disabled = !m.headers[m.cursor].Disabled
	m.refresh()
}

func (m *Metadata) startEditing() {
	if len(m.headers) == 0 {
		return
	}
	m.editing = true
	m.input.SetValue(m.cell())
	m.input.Focus()
	m.input.CursorEnd()
	m.refresh()
}

// cell returns the text of the cell under the cursor.
func (m Metadata) cell() string {
	if len(m.headers) == 0 {
		return ""
	}
	if m.column == columnValue {
		return m.headers[m.cursor].Value
	}
	return m.headers[m.cursor].Key
}

func (m *Metadata) setCell(text string) {
	if len(m.headers) == 0 {
		return
	}
	if m.column == columnValue {
		m.headers[m.cursor].Value = text
		return
	}
	m.headers[m.cursor].Key = text
}

func (m *Metadata) moveColumn(delta int) {
	m.column = min(max(m.column+delta, 0), columnCount-1)
	m.refresh()
}

func (m *Metadata) moveCursor(delta int) { m.moveTo(m.cursor + delta) }

func (m *Metadata) moveTo(i int) {
	m.cursor = clampIndex(i, len(m.headers))
	m.clampOffset()
}

func (m *Metadata) clampOffset() {
	m.refresh()
	m.offset = clampWindow(m.cursorLine(m.lines), m.offset, m.height, len(m.lines))
}

func (m Metadata) cursorLine(lines []line) int {
	for i, l := range lines {
		if l.field == m.cursor {
			return i
		}
	}
	return 0
}

// pageSize counts the header rows on screen rather than the panel's height: a
// row with an error under it owns two lines, and paging by the height would
// step over rows the user never saw.
func (m Metadata) pageSize() int {
	lines := m.render()

	rows := 0
	for i := m.offset; i < len(lines) && i < m.offset+m.height; i++ {
		if lines[i].field != noField {
			rows++
		}
	}
	return max(rows-1, 1)
}

// View renders the panel body. It does not draw its own border; the root model
// frames it.
func (m Metadata) View() string {
	lines := m.render()

	visible := m.height
	if visible <= 0 || visible > len(lines)-m.offset {
		visible = len(lines) - m.offset
	}
	if visible <= 0 {
		return ""
	}

	out := make([]string, 0, visible)
	for i := m.offset; i < m.offset+visible; i++ {
		out = append(out, lines[i].text)
	}
	return strings.Join(out, "\n")
}

func (m Metadata) render() []line {
	if m.lines != nil {
		return m.lines
	}
	return m.build()
}

func (m *Metadata) refresh() {
	m.resizeInput()
	m.lines = m.build()
}

// build lays the panel out as lines, before scrolling.
func (m Metadata) build() []line {
	if len(m.headers) == 0 {
		lines := []line{plain(styles.Truncate(m.styles.Muted.Render("No headers. Press a to add one."), m.width))}
		return append(lines, m.noticeLines()...)
	}

	lines := make([]line, 0, len(m.headers))
	for i, h := range m.headers {
		lines = append(lines, line{text: m.headerRow(i), field: i})
		if msg := m.rowError(i, h); msg != "" {
			lines = append(lines, plain(m.indented(m.styles.FieldError.Render("⚠ "+msg))))
		}
	}
	return append(lines, m.noticeLines()...)
}

// noticeLines renders the panel-level complaint from the last send attempt, if
// there was one.
func (m Metadata) noticeLines() []line {
	if m.notice == "" {
		return nil
	}
	return []line{plain(styles.Truncate(m.styles.FieldError.Render("⚠ "+m.notice), m.width))}
}

// rowError reports what is wrong with a header, and nothing at all for the two
// cases where a complaint would be premature: a row being typed into, and a row
// that was just added and has nothing in it yet.
func (m Metadata) rowError(i int, h grpcclient.Header) string {
	switch {
	case m.editing && i == m.cursor:
		return ""
	case h.Key == "" && h.Value == "":
		return ""
	case h.Disabled:
		return ""
	}

	if err := grpcclient.ValidateHeader(h); err != nil {
		return err.Error()
	}
	return ""
}

func (m Metadata) headerRow(i int) string {
	h := m.headers[i]

	prefix := "  "
	if i == m.cursor {
		markerStyle := m.styles.CursorUnfocused
		if !m.focused {
			markerStyle = m.styles.Marker
		}
		prefix = markerStyle.Render("❯ ")
	}

	// A disabled header stays legible but stops looking like something that is
	// about to be sent.
	glyph := glyphOn
	glyphStyle := m.styles.FieldValue
	if h.Disabled {
		glyph = glyphOff
		glyphStyle = m.styles.FieldDisabled
	}

	row := prefix + glyphStyle.Render(glyph) + " " +
		m.keyCell(i, h) + " " + m.valueCell(i, h)
	return styles.Truncate(row, m.width)
}

func (m Metadata) keyCell(i int, h grpcclient.Header) string {
	if m.editing && i == m.cursor && m.column == columnKey {
		return m.input.View()
	}

	text := padCell(h.Key, m.keyWidth())
	style := m.styles.FieldName
	if h.Disabled {
		style = m.styles.FieldDisabled
	}
	return m.selectable(style, i, columnKey).Render(text)
}

func (m Metadata) valueCell(i int, h grpcclient.Header) string {
	if m.editing && i == m.cursor && m.column == columnValue {
		return m.input.View()
	}

	if h.Value == "" {
		if i == m.cursor && m.focused {
			return m.styles.FieldDisabled.Render("press enter to edit")
		}
		return ""
	}

	style := m.styles.FieldValue
	if h.Disabled {
		style = m.styles.FieldDisabled
	}
	return m.selectable(style, i, columnValue).Render(h.Value)
}

// selectable underlines the cell the cursor is in. Which of a row's two cells
// enter would edit has to be visible, and underlining says so on a monochrome
// terminal as well as a colour one.
func (m Metadata) selectable(style lipgloss.Style, i, column int) lipgloss.Style {
	if !m.focused || i != m.cursor || column != m.column {
		return style
	}
	return style.Bold(true).Underline(true)
}

// indented aligns an error line under the key column.
func (m Metadata) indented(s string) string {
	const glyphWidth = 2 // the enabled/disabled glyph and the space after it
	return styles.Truncate(strings.Repeat(" ", rowPrefixWidth+glyphWidth)+s, m.width)
}

// resizeInput gives the text input the width of the cell it is editing.
func (m *Metadata) resizeInput() {
	const gaps = 4 // the glyph, a space either side of it, and the cursor

	width := m.width - rowPrefixWidth - gaps
	if m.column == columnValue {
		width -= m.keyWidth() + 1
	}
	m.input.Width = max(width, minHeaderValWidth)
}

// keyWidth sizes the key column to its widest entry, up to a limit.
func (m Metadata) keyWidth() int {
	w := 0
	for _, h := range m.headers {
		w = max(w, lipgloss.Width(h.Key))
	}
	return min(w, maxHeaderKeyWidth)
}
