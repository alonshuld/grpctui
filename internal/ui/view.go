// view.go renders a frame: the panels, the status bar and the error box.

package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// View renders the whole screen.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		// No WindowSizeMsg yet: bubbletea sends one immediately, so this frame
		// is never actually seen.
		return ""
	}

	var screen string
	switch {
	case m.profiles.Opened():
		// The switcher covers the body rather than floating over it: lipgloss
		// composes boxes, it does not overlay them, and a half-drawn panel
		// behind a chooser reads as a rendering bug rather than as depth.
		screen = m.centred(m.profiles.View())
	case m.environments.Opened():
		screen = m.centred(m.environments.View())
	case m.variables.Opened():
		screen = m.centred(m.variables.View())
	case m.browser.Opened():
		screen = m.centred(m.browser.View())
	case m.traffic.Opened():
		screen = m.centred(m.traffic.View())
	case m.export.Opened():
		screen = m.centred(m.export.View())
	case m.themes.Opened():
		screen = m.centred(m.themes.View())
	case m.state == stateConnecting:
		screen = m.centred(fmt.Sprintf("%s Connecting to %s…",
			m.spinner.View(), m.styles.Value.Render(m.target())))
	case m.state == stateFailed:
		screen = m.centred(m.errorBox())
	default:
		screen = m.readyView()
	}

	// The last word on how big a frame may be. Panels have minimum heights that
	// a 20x4 terminal cannot satisfy at all, so on a small enough window the
	// layout arithmetic necessarily asks for more rows than exist; a frame
	// larger than the terminal scrolls the screen and strands the previous one
	// above it. Clamping here means every screen — not just the ready one — is
	// bounded, whatever the layout wanted.
	return styles.Clamp(screen, m.width, m.height)
}

func (m Model) readyView() string {
	l := m.computeLayout()

	column := []string{m.framePanel("Request", m.request.View(), l.rightW, l.requestH, m.request.Focused())}
	if l.metadataH > 0 {
		column = append(column,
			m.framePanel(m.metadataTitle(), m.metadata.View(), l.rightW, l.metadataH, m.metadata.Focused()))
	}
	column = append(column,
		m.framePanel("Response", m.response.View(), l.rightW, l.responseH, m.response.Focused()))

	right := lipgloss.JoinVertical(lipgloss.Left, column...)

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		m.framePanel("Services", m.tree.View(), l.treeW, l.bodyH, m.tree.Focused()),
		right,
	)

	return strings.Join([]string{body, m.statusBar(), m.help.View(m.keys)}, "\n")
}

// metadataTitle counts the headers in the panel's title, so that a collapsed or
// scrolled panel still says how many are going out.
func (m Model) metadataTitle() string {
	enabled := m.metadata.Enabled()
	if enabled == 0 {
		return "Headers"
	}
	return fmt.Sprintf("Headers (%d)", enabled)
}

// framePanel draws a bordered, titled box occupying exactly width x height
// cells.
//
// lipgloss counts padding inside Width/Height but adds the border outside
// them, so only the border is subtracted when sizing the style. The panel's
// own content area is narrower still — see [innerSize].
func (m Model) framePanel(title, body string, width, height int, focused bool) string {
	style := m.styles.Panel
	if focused {
		style = m.styles.PanelFocused
	}

	titleLine := m.styles.PanelTitle.Render(title)
	if !focused {
		titleLine = m.styles.Muted.Render(title)
	}

	// lipgloss reads MaxHeight(0) as "no maximum", so a panel with no room left
	// for a body cannot be told to truncate one — it has to be given nothing to
	// draw instead, or the body runs straight through the border below it.
	innerW, innerH := innerSize(style, width, height)
	if innerH <= 0 {
		body = ""
	} else {
		body = lipgloss.NewStyle().
			Width(innerW).
			Height(innerH).
			MaxHeight(innerH).
			Render(body)
	}

	return style.
		Width(max(width-style.GetHorizontalBorderSize(), 0)).
		Height(max(height-style.GetVerticalBorderSize(), 0)).
		Render(titleLine + "\n" + body)
}

// statusBar reports what the next call would do: where it goes, how protected,
// as whom, and with how many headers.
//
// It never renders a credential, only its kind. A terminal is shared over a
// screen share more often than a config file is.
func (m Model) statusBar() string {
	methods := 0
	for _, svc := range m.services {
		methods += len(svc.Methods)
	}

	segments := []string{m.styles.Value.Render(m.target())}

	if profile, ok := m.profiles.Active(); ok {
		if m.profiles.Len() > 1 {
			segments = append(segments, m.styles.Label.Render(profile.Label()))
		}
		segments = append(segments, profile.Security.Mode())
		if profile.Auth.Kind != grpcclient.AuthNone {
			segments = append(segments, profile.Auth.Describe())
		}
	}

	// Which environment a request means, and how much is bound — never what any
	// of it is bound to. A terminal is shared over a screen share more often
	// than a config file is, and since v0.7 a variable can hold a token captured
	// out of a login response.
	if env, ok := m.environments.Active(); ok {
		segment := m.styles.Label.Render(env.Name)
		if n := m.bindings().Len(); n > 0 {
			segment += " " + fmt.Sprintf("%d %s", n, plural(n, "var"))
		}
		segments = append(segments, segment)
	}

	if n := m.metadata.Enabled(); n > 0 {
		segments = append(segments, fmt.Sprintf("%d %s", n, plural(n, "header")))
	}

	// How much an open stream has carried, and for how long. It belongs here as
	// well as on the panel's own status line: the response panel scrolls, and
	// this is the number you watch while it does.
	if summary, ok := m.response.StreamSummary(); ok {
		segments = append(segments, summary)
	}

	segments = append(segments,
		fmt.Sprintf("%d services", len(m.services)),
		fmt.Sprintf("%d methods", methods),
	)
	return m.styles.Status.Render(styles.Truncate(strings.Join(segments, "  •  "), m.width))
}

// target is the address on screen: the one being dialled while a connection is
// being opened, and the connected one otherwise.
func (m Model) target() string {
	if m.pendingTarget != "" {
		return m.pendingTarget
	}
	return m.client.Target()
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func (m Model) errorBox() string {
	title := "Connection failed"
	hint := "Check the address and that the server is reachable."
	if errors.Is(m.err, grpcclient.ErrReflectionUnavailable) {
		title = "Server reflection unavailable"
		// The fix is now a flag rather than a future version, so the hint names
		// it: somebody on this screen wants the next thing to type, not a
		// roadmap entry.
		hint = "The target is reachable but does not serve the reflection API.\n" +
			"Enable reflection on the server, or restart with --proto <file>."
	}

	lines := []string{
		m.styles.ErrorTitle.Render(title),
		"",
		m.styles.ErrorBody.Render(styles.Wrap(m.err.Error(), m.errorWidth())),
		"",
		m.styles.Hint.Render(hint),
		"",
		m.help.ShortHelpView([]key.Binding{m.keys.Retry, m.keys.Quit}),
	}
	return strings.Join(lines, "\n")
}

func (m Model) errorWidth() int {
	const maxErrorWidth = 72
	if m.width-4 < maxErrorWidth {
		return m.width - 4
	}
	return maxErrorWidth
}

// centred places content in the middle of the screen.
func (m Model) centred(content string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}
