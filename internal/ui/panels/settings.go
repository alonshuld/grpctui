package panels

import (
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// This file holds every panel's SetStyles and SetKeys — the two things a panel
// is handed at construction and may be handed again later, when the user
// switches theme or the config file has remapped a key.
//
// They are gathered here rather than spread across eleven files because each is
// one feature rather than eleven, and because what a panel has to do beyond
// storing the new value is the interesting part: one that caches styled text
// has to build it again, and one that has handed its keymap to a bubbles
// component has to hand over the new one.
//
// The rule for a new panel is short: store it, and redo whatever you did with
// the old one at construction.

// setInputStyles restyles a text input, which is the one component that keeps
// its own copy of two of ours.
func setInputStyles(ti *textinput.Model, st styles.Styles) {
	ti.TextStyle = st.FieldValue
	ti.PlaceholderStyle = st.FieldDisabled
}

// SetStyles swaps the theme.
func (t *Tree) SetStyles(st styles.Styles) { t.styles = st }

// SetStyles swaps the theme. The form caches its rows already styled, so they
// are built again.
func (f *Form) SetStyles(st styles.Styles) {
	f.styles = st
	setInputStyles(&f.input, st)
	f.refresh()
}

// SetStyles swaps the theme. The headers are cached styled, as the form's rows
// are.
func (m *Metadata) SetStyles(st styles.Styles) {
	m.styles = st
	setInputStyles(&m.input, st)
	m.refresh()
}

// SetStyles swaps the theme.
//
// The response is the one panel that holds text coloured by a *previous*
// theme: a body is highlighted once when it arrives, precisely so that it is
// not re-highlighted on every resize. Every such rendering is therefore made
// again here, from the plain text each was built from.
func (r *Response) SetStyles(st styles.Styles) {
	r.styles = st
	r.spinner.Style = lipgloss.NewStyle().Foreground(st.Palette.Primary)

	r.rendered = r.body
	if r.format == protoschema.FormatJSON {
		r.rendered = annotate(st.HighlightJSON(r.body), r.notes, st)
	}
	for i := range r.entries {
		e := &r.entries[i]
		e.rendered = e.body
		if e.kind != streamNote && e.format == protoschema.FormatJSON {
			e.rendered = annotate(st.HighlightJSON(e.body), e.notes, st)
		}
	}
	r.setBody()
}

// SetStyles swaps the theme.
func (p *Profiles) SetStyles(st styles.Styles) { p.styles = st }

// SetStyles swaps the theme.
func (r *Requests) SetStyles(st styles.Styles) {
	r.styles = st
	setInputStyles(&r.input, st)
}

// SetStyles swaps the theme.
func (e *Environments) SetStyles(st styles.Styles) { e.styles = st }

// SetStyles swaps the theme.
func (v *Variables) SetStyles(st styles.Styles) {
	v.styles = st
	setInputStyles(&v.input, st)
}

// SetStyles swaps the theme. The export panel holds the command as plain text
// and colours it as it draws, so there is nothing to rebuild.
func (e *Export) SetStyles(st styles.Styles) { e.styles = st }

// SetStyles swaps the theme.
func (t *Traffic) SetStyles(st styles.Styles) { t.styles = st }

// SetStyles swaps the theme — including on the switcher that asked for the
// swap, which is open while it happens and would otherwise be the one thing on
// screen still wearing the old colours.
func (t *Themes) SetStyles(st styles.Styles) { t.styles = st }

// SetKeys replaces the keybindings.
func (t *Tree) SetKeys(km keys.KeyMap) { t.keys = km }

// SetKeys replaces the keybindings.
func (f *Form) SetKeys(km keys.KeyMap) { f.keys = km }

// SetKeys replaces the keybindings.
func (m *Metadata) SetKeys(km keys.KeyMap) { m.keys = km }

// SetKeys replaces the keybindings, including the viewport's own copy — the
// response panel translates ours into the scroll keys bubbles understands, and
// a remapping that stopped at our struct would leave the body scrolling on the
// old ones.
func (r *Response) SetKeys(km keys.KeyMap) {
	r.keys = km
	r.viewport.KeyMap = viewportKeys(km)
}

// SetKeys replaces the keybindings.
func (p *Profiles) SetKeys(km keys.KeyMap) { p.keys = km }

// SetKeys replaces the keybindings.
func (r *Requests) SetKeys(km keys.KeyMap) { r.keys = km }

// SetKeys replaces the keybindings.
func (e *Environments) SetKeys(km keys.KeyMap) { e.keys = km }

// SetKeys replaces the keybindings.
func (v *Variables) SetKeys(km keys.KeyMap) { v.keys = km }

// SetKeys replaces the keybindings.
func (e *Export) SetKeys(km keys.KeyMap) { e.keys = km }

// SetKeys replaces the keybindings.
func (t *Traffic) SetKeys(km keys.KeyMap) { t.keys = km }

// SetKeys replaces the keybindings.
func (t *Themes) SetKeys(km keys.KeyMap) { t.keys = km }
