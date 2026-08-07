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

// ProfileSelectedMsg asks the root model to connect to another profile. It
// carries the index as well as the profile so that the root can record which
// one is active without comparing structs.
type ProfileSelectedMsg struct {
	Index   int
	Profile grpcclient.Profile
}

// Profiles is the connection switcher: the saved host/auth combinations, one
// per line, with the active one marked.
//
// It is a chooser rather than an editor. Profiles are written in the config
// file, where they can be kept beside a project and version-controlled; what
// belongs in the TUI is picking between them without restarting.
type Profiles struct {
	keys   keys.KeyMap
	styles styles.Styles

	profiles []grpcclient.Profile
	active   int
	cursor   int
	offset   int

	open   bool
	width  int
	height int
}

// NewProfiles builds an empty switcher.
func NewProfiles(km keys.KeyMap, st styles.Styles) Profiles {
	return Profiles{keys: km, styles: st}
}

// SetProfiles replaces the list and says which entry is connected.
func (p *Profiles) SetProfiles(profiles []grpcclient.Profile, active int) {
	p.profiles = profiles
	p.SetActive(active)
}

// SetActive marks the connected profile and puts the cursor on it.
func (p *Profiles) SetActive(active int) {
	p.active = clampIndex(active, len(p.profiles))
	p.cursor = p.active
	p.clampOffset()
}

// Active returns the connected profile.
func (p Profiles) Active() (grpcclient.Profile, bool) {
	if p.active < 0 || p.active >= len(p.profiles) {
		return grpcclient.Profile{}, false
	}
	return p.profiles[p.active], true
}

// Len reports how many profiles there are. The switcher is worth offering only
// when there is something to switch to.
func (p Profiles) Len() int { return len(p.profiles) }

// Open shows the switcher with the cursor on the connected profile.
func (p *Profiles) Open() {
	if len(p.profiles) == 0 {
		return
	}
	p.open = true
	p.cursor = p.active
	p.clampOffset()
}

// Close hides the switcher.
func (p *Profiles) Close() { p.open = false }

// Opened reports whether the switcher is on screen. While it is, it owns the
// keyboard: it is a modal choice, and half-applying j/k to the panels behind it
// would be a way to lose your place.
func (p Profiles) Opened() bool { return p.open }

// SetSize sets the area the switcher may draw into.
func (p *Profiles) SetSize(width, height int) {
	p.width = width
	p.height = height
	p.clampOffset()
}

// Update handles the switcher's keys while it is open.
func (p Profiles) Update(msg tea.Msg) (Profiles, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !p.open {
		return p, nil
	}

	switch {
	case key.Matches(keyMsg, p.keys.Up):
		p.moveTo(p.cursor - 1)
	case key.Matches(keyMsg, p.keys.Down):
		p.moveTo(p.cursor + 1)
	case key.Matches(keyMsg, p.keys.Top):
		p.moveTo(0)
	case key.Matches(keyMsg, p.keys.Bottom):
		p.moveTo(len(p.profiles) - 1)
	case key.Matches(keyMsg, p.keys.Cancel), key.Matches(keyMsg, p.keys.Profiles):
		p.Close()
	case key.Matches(keyMsg, p.keys.Select):
		return p, p.choose()
	}
	return p, nil
}

// choose closes the switcher and asks for the profile under the cursor. Picking
// the one already connected is not a no-op: reconnecting is how a user recovers
// a connection the server dropped.
func (p *Profiles) choose() tea.Cmd {
	p.Close()
	if p.cursor < 0 || p.cursor >= len(p.profiles) {
		return nil
	}

	selected := ProfileSelectedMsg{Index: p.cursor, Profile: p.profiles[p.cursor]}
	return func() tea.Msg { return selected }
}

func (p *Profiles) moveTo(i int) {
	p.cursor = clampIndex(i, len(p.profiles))
	p.clampOffset()
}

func (p *Profiles) clampOffset() {
	p.offset = clampWindow(p.cursor, p.offset, p.visibleRows(), len(p.profiles))
}

// visibleRows is how many profiles fit, after the title and the help line.
func (p Profiles) visibleRows() int {
	const chrome = 3
	if p.height <= 0 {
		return len(p.profiles)
	}
	return max(p.height-chrome, 1)
}

// View renders the switcher as a box the root model centres on the screen.
func (p Profiles) View() string {
	lines := []string{p.styles.PanelTitle.Render("Connections"), ""}

	visible := min(p.visibleRows(), len(p.profiles)-p.offset)
	for i := p.offset; i < p.offset+visible; i++ {
		lines = append(lines, p.row(i))
	}

	lines = append(lines, "", p.styles.Hint.Render("enter connect · esc close"))
	return p.styles.Panel.Render(strings.Join(lines, "\n"))
}

func (p Profiles) row(i int) string {
	profile := p.profiles[i]

	marker := "  "
	if i == p.active {
		marker = p.styles.FieldValue.Render(glyphOn) + " "
	}

	name := p.styles.Service.Render(padCell(profile.Label(), p.labelWidth()))
	text := marker + name + "  " + p.styles.Muted.Render(p.describe(profile))

	if i != p.cursor {
		return styles.Truncate("  "+text, p.rowWidth())
	}
	return p.styles.Cursor.Render(styles.Truncate("❯ "+text, p.rowWidth()))
}

// describe summarises what connecting to a profile would mean: where, how
// protected, as whom. It never renders a credential — only its kind.
func (p Profiles) describe(profile grpcclient.Profile) string {
	parts := []string{profile.Target, profile.Security.Mode()}

	if profile.Auth.Kind != grpcclient.AuthNone {
		parts = append(parts, profile.Auth.Describe())
	}
	if n := len(profile.Metadata); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "header")))
	}
	return strings.Join(parts, " · ")
}

func (p Profiles) labelWidth() int {
	w := 0
	for _, profile := range p.profiles {
		w = max(w, len(profile.Label()))
	}
	return min(w, maxNameWidth)
}

// rowWidth is the width a row may occupy inside the box.
func (p Profiles) rowWidth() int {
	const chrome = 4 // the box's border and padding
	if p.width <= chrome {
		return maxNameWidth
	}
	return p.width - chrome
}
