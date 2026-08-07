// Package panels holds grpctui's child models — one file per panel. Each panel
// owns its own Update/View and knows nothing about its siblings; the root model
// in internal/ui wires them together and decides which one has focus.
package panels

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// MethodSelectedMsg is emitted when the user selects a method row.
type MethodSelectedMsg struct {
	Service grpcclient.Service
	Method  grpcclient.Method
}

// row is one visible line of the tree: a service, or a method under an
// expanded service.
type row struct {
	service int
	method  int // -1 for a service row
}

func (r row) isService() bool { return r.method < 0 }

// Tree is the service/method browser. Services expand to reveal their methods;
// selecting a method emits a [MethodSelectedMsg].
type Tree struct {
	keys   keys.KeyMap
	styles styles.Styles

	services []grpcclient.Service
	expanded map[string]bool

	rows   []row
	cursor int
	offset int

	width   int
	height  int
	focused bool
}

// NewTree builds an empty tree.
func NewTree(km keys.KeyMap, st styles.Styles) Tree {
	return Tree{
		keys:     km,
		styles:   st,
		expanded: make(map[string]bool),
	}
}

// SetServices replaces the tree's contents, expanding every service so the
// full API surface is visible on first connect. The cursor returns to the top.
func (t *Tree) SetServices(services []grpcclient.Service) {
	t.services = services
	t.expanded = make(map[string]bool, len(services))
	for _, svc := range services {
		t.expanded[svc.Name] = true
	}
	t.cursor = 0
	t.offset = 0
	t.rebuild()
}

// SetSize sets the panel's inner content area.
func (t *Tree) SetSize(width, height int) {
	t.width = width
	t.height = height
	t.clampOffset()
}

// Focus and Blur toggle whether the panel receives key messages.
func (t *Tree) Focus() { t.focused = true }

// Blur removes focus from the panel.
func (t *Tree) Blur() { t.focused = false }

// Focused reports whether the panel has focus.
func (t Tree) Focused() bool { return t.focused }

// Len reports the number of visible rows.
func (t Tree) Len() int { return len(t.rows) }

// Selection returns the method under the cursor. ok is false when the tree is
// empty or the cursor is on a service row.
func (t Tree) Selection() (grpcclient.Service, grpcclient.Method, bool) {
	r, ok := t.currentRow()
	if !ok || r.isService() {
		return grpcclient.Service{}, grpcclient.Method{}, false
	}
	svc := t.services[r.service]
	return svc, svc.Methods[r.method], true
}

// CursorService returns the service the cursor sits on or under.
func (t Tree) CursorService() (grpcclient.Service, bool) {
	r, ok := t.currentRow()
	if !ok {
		return grpcclient.Service{}, false
	}
	return t.services[r.service], true
}

// Update handles key messages when the panel is focused.
func (t Tree) Update(msg tea.Msg) (Tree, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !t.focused {
		return t, nil
	}

	switch {
	case key.Matches(keyMsg, t.keys.Up):
		t.moveCursor(-1)
	case key.Matches(keyMsg, t.keys.Down):
		t.moveCursor(1)
	case key.Matches(keyMsg, t.keys.PageUp):
		t.moveCursor(-t.pageSize())
	case key.Matches(keyMsg, t.keys.PageDown):
		t.moveCursor(t.pageSize())
	case key.Matches(keyMsg, t.keys.Top):
		t.moveTo(0)
	case key.Matches(keyMsg, t.keys.Bottom):
		t.moveTo(len(t.rows) - 1)
	case key.Matches(keyMsg, t.keys.Expand):
		t.setExpanded(true)
	case key.Matches(keyMsg, t.keys.Collapse):
		t.collapse()
	case key.Matches(keyMsg, t.keys.Select):
		return t, t.selectCurrent()
	}
	return t, nil
}

// View renders the tree body. It does not draw its own border; the root model
// frames it.
func (t Tree) View() string {
	if len(t.services) == 0 {
		return t.styles.Muted.Render("No services discovered.")
	}

	visible := t.height
	if visible <= 0 || visible > len(t.rows)-t.offset {
		visible = len(t.rows) - t.offset
	}

	lines := make([]string, 0, visible)
	for i := t.offset; i < t.offset+visible; i++ {
		lines = append(lines, t.renderRow(i))
	}
	return strings.Join(lines, "\n")
}

func (t Tree) renderRow(i int) string {
	r := t.rows[i]
	svc := t.services[r.service]

	var text string
	if r.isService() {
		marker := "▸"
		if t.expanded[svc.Name] {
			marker = "▾"
		}
		text = fmt.Sprintf("%s %s", t.styles.Marker.Render(marker), t.styles.Service.Render(svc.Name))
	} else {
		m := svc.Methods[r.method]
		text = "   " + t.styles.Method.Render(m.Name)
		if tag := streamTag(m); tag != "" {
			text += " " + t.styles.StreamTag.Render(tag)
		}
	}

	// The cursor gets a gutter glyph as well as a highlight: colour alone
	// would leave it invisible on a monochrome terminal — and in golden files.
	if i != t.cursor {
		return styles.Truncate("  "+text, t.width)
	}

	cursorStyle := t.styles.Cursor
	if !t.focused {
		cursorStyle = t.styles.CursorUnfocused
	}
	return cursorStyle.Render(styles.Truncate("❯ "+text, t.width))
}

// streamTag labels a method's streaming shape; unary methods get no tag, since
// they are the common case and the noise is not worth it.
func streamTag(m grpcclient.Method) string {
	switch m.Kind() {
	case grpcclient.KindServerStreaming:
		return "«stream"
	case grpcclient.KindClientStreaming:
		return "stream»"
	case grpcclient.KindBidiStreaming:
		return "«stream»"
	default:
		return ""
	}
}

func (t *Tree) rebuild() {
	t.rows = t.rows[:0]
	for si, svc := range t.services {
		t.rows = append(t.rows, row{service: si, method: -1})
		if !t.expanded[svc.Name] {
			continue
		}
		for mi := range svc.Methods {
			t.rows = append(t.rows, row{service: si, method: mi})
		}
	}
	t.moveTo(t.cursor)
}

func (t Tree) currentRow() (row, bool) {
	if t.cursor < 0 || t.cursor >= len(t.rows) {
		return row{}, false
	}
	return t.rows[t.cursor], true
}

func (t *Tree) moveCursor(delta int) { t.moveTo(t.cursor + delta) }

func (t *Tree) moveTo(i int) {
	switch {
	case len(t.rows) == 0:
		t.cursor = 0
	case i < 0:
		t.cursor = 0
	case i >= len(t.rows):
		t.cursor = len(t.rows) - 1
	default:
		t.cursor = i
	}
	t.clampOffset()
}

// clampOffset scrolls the viewport just far enough to keep the cursor visible.
func (t *Tree) clampOffset() {
	if t.height <= 0 {
		t.offset = 0
		return
	}
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+t.height {
		t.offset = t.cursor - t.height + 1
	}
	if maxOffset := len(t.rows) - t.height; t.offset > maxOffset {
		t.offset = maxOffset
	}
	if t.offset < 0 {
		t.offset = 0
	}
}

func (t Tree) pageSize() int {
	if t.height > 1 {
		return t.height - 1
	}
	return 1
}

// setExpanded expands the service on or above the cursor.
func (t *Tree) setExpanded(expanded bool) {
	r, ok := t.currentRow()
	if !ok {
		return
	}
	name := t.services[r.service].Name
	if t.expanded[name] == expanded {
		return
	}
	t.expanded[name] = expanded
	t.rebuild()
}

// collapse folds the current service. From a method row it first jumps to the
// parent service, which is the behaviour a file-tree user expects.
func (t *Tree) collapse() {
	r, ok := t.currentRow()
	if !ok {
		return
	}
	if !r.isService() {
		t.moveTo(t.serviceRowIndex(r.service))
		return
	}
	t.setExpanded(false)
}

func (t Tree) serviceRowIndex(service int) int {
	for i, r := range t.rows {
		if r.service == service && r.isService() {
			return i
		}
	}
	return t.cursor
}

// selectCurrent toggles a service row, or emits a [MethodSelectedMsg] for a
// method row.
func (t *Tree) selectCurrent() tea.Cmd {
	r, ok := t.currentRow()
	if !ok {
		return nil
	}
	if r.isService() {
		t.setExpanded(!t.expanded[t.services[r.service].Name])
		return nil
	}

	svc := t.services[r.service]
	selected := MethodSelectedMsg{Service: svc, Method: svc.Methods[r.method]}
	return func() tea.Msg { return selected }
}
