package panels

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// Detail shows the selected method's signature.
//
// v0.1 deliberately stops at "here is what this method looks like" — the
// request form (v0.2) and the response viewer replace this body while keeping
// the same panel slot.
type Detail struct {
	keys   keys.KeyMap
	styles styles.Styles

	viewport viewport.Model
	service  grpcclient.Service
	method   grpcclient.Method
	selected bool
	focused  bool
}

// NewDetail builds an empty detail panel.
func NewDetail(km keys.KeyMap, st styles.Styles) Detail {
	return Detail{
		keys:     km,
		styles:   st,
		viewport: viewport.New(0, 0),
	}
}

// SetMethod fills the panel with a method's signature.
func (d *Detail) SetMethod(svc grpcclient.Service, m grpcclient.Method) {
	d.service = svc
	d.method = m
	d.selected = true
	d.viewport.SetContent(d.body())
	d.viewport.GotoTop()
}

// Clear returns the panel to its empty state.
func (d *Detail) Clear() {
	d.selected = false
	d.viewport.SetContent("")
}

// SetSize sets the panel's inner content area.
func (d *Detail) SetSize(width, height int) {
	d.viewport.Width = width
	d.viewport.Height = height
	if d.selected {
		d.viewport.SetContent(d.body())
	}
}

// Focus gives the panel focus.
func (d *Detail) Focus() { d.focused = true }

// Blur removes focus from the panel.
func (d *Detail) Blur() { d.focused = false }

// Focused reports whether the panel has focus.
func (d Detail) Focused() bool { return d.focused }

// Method returns the currently displayed method, if any.
func (d Detail) Method() (grpcclient.Method, bool) { return d.method, d.selected }

// Update scrolls the panel when focused.
func (d Detail) Update(msg tea.Msg) (Detail, tea.Cmd) {
	if !d.focused {
		return d, nil
	}
	var cmd tea.Cmd
	d.viewport, cmd = d.viewport.Update(msg)
	return d, cmd
}

// View renders the panel body.
func (d Detail) View() string {
	if !d.selected {
		return d.styles.Muted.Render("Select a method to see its signature.")
	}
	return d.viewport.View()
}

func (d Detail) body() string {
	field := func(label, value string) string {
		return d.styles.Label.Render(fmt.Sprintf("%-9s", label)) + " " + d.styles.Value.Render(value)
	}

	lines := []string{
		d.styles.PanelTitle.Render(d.method.Name),
		"",
		field("service", d.service.Name),
		field("full", d.method.FullName),
		field("type", string(d.method.Kind())),
		"",
		field("request", d.method.InputType),
		field("response", d.method.OutputType),
	}

	if d.method.Kind() != grpcclient.KindUnary {
		lines = append(lines, "",
			d.styles.Hint.Render("Streaming methods are callable from v0.5."))
	} else {
		lines = append(lines, "",
			d.styles.Hint.Render("Request form and invocation arrive in v0.2."))
	}

	return strings.Join(lines, "\n")
}
