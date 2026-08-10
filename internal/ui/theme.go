// theme.go holds the two settings a running session can change: the palette
// and the keymap. They arrive the same way — every panel has a SetStyles and a
// SetKeys, and applyTheme/applyKeys hand the new value to each in turn.

package ui

import (
	"github.com/charmbracelet/lipgloss"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// applyTheme rebuilds the styles from a theme and hands them to everything that
// draws.
//
// There is one list here rather than a loop over an interface because the
// panels are concrete types held by value: a []interface{ SetStyles(...) } would
// hold copies, and the copies are what would change colour. The compiler
// catches a panel left out of it only by the frame staying stubbornly the old
// colour, so the rule in internal/ui/panels/settings.go is the real guard —
// [Model.applyKeys] below is the same list for the same reason.
func (m *Model) applyTheme(theme styles.Theme) {
	m.styles = styles.NewTheme(theme)
	st := m.styles

	m.spinner.Style = lipgloss.NewStyle().Foreground(st.Palette.Primary)
	m.help.Styles.ShortKey = st.Label
	m.help.Styles.ShortDesc = st.Muted
	m.help.Styles.FullKey = st.Label
	m.help.Styles.FullDesc = st.Muted

	m.tree.SetStyles(st)
	m.request.SetStyles(st)
	m.metadata.SetStyles(st)
	m.response.SetStyles(st)
	m.profiles.SetStyles(st)
	m.browser.SetStyles(st)
	m.environments.SetStyles(st)
	m.variables.SetStyles(st)
	m.export.SetStyles(st)
	m.traffic.SetStyles(st)
	m.themes.SetStyles(st)
}

// applyKeys hands a keymap to every panel, which is what makes a remapping in
// the config file reach the keys the panels actually match against.
func (m *Model) applyKeys(km keys.KeyMap) {
	m.tree.SetKeys(km)
	m.request.SetKeys(km)
	m.metadata.SetKeys(km)
	m.response.SetKeys(km)
	m.profiles.SetKeys(km)
	m.browser.SetKeys(km)
	m.environments.SetKeys(km)
	m.variables.SetKeys(km)
	m.export.SetKeys(km)
	m.traffic.SetKeys(km)
	m.themes.SetKeys(km)
}

// switchTheme applies a theme the switcher chose and marks it active.
//
// The help bar is measured again because a theme can change how wide it
// renders — a bold key style is wider than a plain one — and a stale
// measurement leaves a row of the layout either overlapping or empty.
func (m *Model) switchTheme(msg panels.ThemeSelectedMsg) {
	m.themes.SetActive(msg.Index)
	m.applyTheme(msg.Theme)
	m.measureHelp()
	m.layout()
	m.logger.Debug("switched theme", zap.String("theme", msg.Theme.Name))
}
