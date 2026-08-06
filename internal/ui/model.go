// Package ui holds grpctui's bubbletea models.
//
// One root [Model] owns focus and the panel layout; the panels themselves live
// in internal/ui/panels as child models. Blocking work never happens inside
// Update — reflection and (from v0.2) RPCs are tea.Cmds that return a message.
//
// Nothing here imports google.golang.org/grpc. The UI talks to the transport
// layer through the small [Discoverer] interface and the plain types in
// internal/grpcclient.
package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// DefaultDiscoveryTimeout bounds a single reflection sweep.
const DefaultDiscoveryTimeout = 10 * time.Second

// Discoverer is the slice of the transport layer the UI depends on. Keeping it
// an interface here — rather than taking *grpcclient.Client — is what lets the
// UI tests run with no server at all.
type Discoverer interface {
	Target() string
	ListServices(ctx context.Context) ([]grpcclient.Service, error)
}

type state int

const (
	stateConnecting state = iota
	stateReady
	stateFailed
)

type focus int

const (
	focusTree focus = iota
	focusDetail
)

// Option configures a [Model].
type Option func(*Model)

// WithLogger attaches a logger.
func WithLogger(logger *zap.Logger) Option {
	return func(m *Model) {
		if logger != nil {
			m.logger = logger
		}
	}
}

// WithContext sets the parent context for RPCs the UI issues. Cancelling it
// aborts in-flight work.
func WithContext(ctx context.Context) Option {
	return func(m *Model) {
		if ctx != nil {
			m.ctx = ctx
		}
	}
}

// WithDiscoveryTimeout overrides [DefaultDiscoveryTimeout].
func WithDiscoveryTimeout(d time.Duration) Option {
	return func(m *Model) {
		if d > 0 {
			m.discoveryTimeout = d
		}
	}
}

// Model is grpctui's root model.
type Model struct {
	client Discoverer
	logger *zap.Logger

	// ctx is the parent for every RPC the UI issues. bubbletea's Update
	// signature takes a tea.Msg and nothing else, so there is no parameter to
	// thread a context through; holding it here is what lets a tea.Cmd inherit
	// cancellation from the process.
	//nolint:containedctx // bubbletea's Update/Cmd signatures leave no alternative.
	ctx context.Context

	keys    keys.KeyMap
	styles  styles.Styles
	help    help.Model
	spinner spinner.Model

	tree   panels.Tree
	detail panels.Detail

	state    state
	err      error
	focus    focus
	services []grpcclient.Service

	width  int
	height int

	discoveryTimeout time.Duration
}

// New builds the root model for a target.
func New(client Discoverer, opts ...Option) Model {
	km := keys.Default()
	st := styles.New()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(st.Palette.Primary)

	helpModel := help.New()
	helpModel.Styles.ShortKey = st.Label
	helpModel.Styles.ShortDesc = st.Muted
	helpModel.Styles.FullKey = st.Label
	helpModel.Styles.FullDesc = st.Muted

	m := Model{
		client:           client,
		logger:           zap.NewNop(),
		ctx:              context.Background(),
		keys:             km,
		styles:           st,
		help:             helpModel,
		spinner:          sp,
		tree:             panels.NewTree(km, st),
		detail:           panels.NewDetail(km, st),
		state:            stateConnecting,
		discoveryTimeout: DefaultDiscoveryTimeout,
	}
	for _, opt := range opts {
		opt(&m)
	}
	m.tree.Focus()
	return m
}

// Init starts discovery and the loading spinner.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.discover())
}

// discover runs one reflection sweep off the Update goroutine.
func (m Model) discover() tea.Cmd {
	client, parent, timeout := m.client, m.ctx, m.discoveryTimeout
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()

		services, err := client.ListServices(ctx)
		if err != nil {
			return discoveryFailedMsg{err: err}
		}
		return servicesDiscoveredMsg{services: services}
	}
}

// Update routes messages to the panels and handles global keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		m.layout()
		return m, nil

	case tea.KeyMsg:
		if cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}

	case servicesDiscoveredMsg:
		m.state = stateReady
		m.err = nil
		m.services = msg.services
		m.tree.SetServices(msg.services)
		m.detail.Clear()
		m.layout()
		m.logger.Info("services discovered",
			zap.String("target", m.client.Target()),
			zap.Int("services", len(msg.services)),
		)
		return m, nil

	case discoveryFailedMsg:
		m.state = stateFailed
		m.err = msg.err
		m.logger.Error("discovery failed",
			zap.String("target", m.client.Target()),
			zap.Error(msg.err),
		)
		return m, nil

	case panels.MethodSelectedMsg:
		m.detail.SetMethod(msg.Service, msg.Method)
		m.focus = focusDetail
		m.syncFocus()
		m.logger.Debug("method selected", zap.String("method", msg.Method.FullName))
		return m, nil

	case spinner.TickMsg:
		if m.state != stateConnecting {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m.updatePanels(msg)
}

// handleKey handles the keys the root model owns. It reports whether the key
// was consumed, so panel keys fall through untouched.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit, true

	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.layout()
		return nil, true

	case key.Matches(msg, m.keys.Retry) && m.state == stateFailed:
		m.state = stateConnecting
		m.err = nil
		return tea.Batch(m.spinner.Tick, m.discover()), true

	case key.Matches(msg, m.keys.NextPanel), key.Matches(msg, m.keys.PrevPanel):
		if m.state != stateReady {
			return nil, true
		}
		m.toggleFocus()
		return nil, true
	}
	return nil, false
}

func (m Model) updatePanels(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.state != stateReady {
		return m, nil
	}

	var cmds []tea.Cmd
	var cmd tea.Cmd

	m.tree, cmd = m.tree.Update(msg)
	cmds = append(cmds, cmd)

	m.detail, cmd = m.detail.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m *Model) toggleFocus() {
	if m.focus == focusTree {
		m.focus = focusDetail
	} else {
		m.focus = focusTree
	}
	m.syncFocus()
}

func (m *Model) syncFocus() {
	if m.focus == focusTree {
		m.tree.Focus()
		m.detail.Blur()
		return
	}
	m.tree.Blur()
	m.detail.Focus()
}

// View renders the whole screen.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		// No WindowSizeMsg yet: bubbletea sends one immediately, so this frame
		// is never actually seen.
		return ""
	}

	switch m.state {
	case stateConnecting:
		return m.centred(fmt.Sprintf("%s Connecting to %s…",
			m.spinner.View(), m.styles.Value.Render(m.client.Target())))
	case stateFailed:
		return m.centred(m.errorBox())
	default:
		return m.readyView()
	}
}

func (m Model) readyView() string {
	treeW, detailW, panelH := m.panelSizes()

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		m.framePanel("Services", m.tree.View(), treeW, panelH, m.tree.Focused()),
		m.framePanel("Method", m.detail.View(), detailW, panelH, m.detail.Focused()),
	)

	return strings.Join([]string{body, m.statusBar(), m.help.View(m.keys)}, "\n")
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

	innerW, innerH := innerSize(style, width, height)
	body = lipgloss.NewStyle().
		Width(innerW).
		Height(innerH).
		MaxHeight(innerH).
		Render(body)

	return style.
		Width(width - style.GetHorizontalBorderSize()).
		Height(height - style.GetVerticalBorderSize()).
		Render(titleLine + "\n" + body)
}

// innerSize reports the content area left inside a panel of the given outer
// size, after its border, its padding, and the one-line title.
func innerSize(s lipgloss.Style, width, height int) (w, h int) {
	w = width - s.GetHorizontalBorderSize() - s.GetHorizontalPadding()
	h = height - s.GetVerticalBorderSize() - s.GetVerticalPadding() - 1
	return max(w, 0), max(h, 0)
}

func (m Model) statusBar() string {
	methods := 0
	for _, svc := range m.services {
		methods += len(svc.Methods)
	}

	segments := []string{
		m.styles.Value.Render(m.client.Target()),
		fmt.Sprintf("%d services", len(m.services)),
		fmt.Sprintf("%d methods", methods),
	}
	return m.styles.Status.Render(strings.Join(segments, "  •  "))
}

func (m Model) errorBox() string {
	title := "Connection failed"
	hint := "Check the address and that the server is reachable."
	if errors.Is(m.err, grpcclient.ErrReflectionUnavailable) {
		title = "Server reflection unavailable"
		hint = "The target is reachable but does not serve the reflection API.\n" +
			"Enable reflection on the server, or use .proto-file mode (v0.9)."
	}

	lines := []string{
		m.styles.ErrorTitle.Render(title),
		"",
		m.styles.ErrorBody.Render(wrap(m.err.Error(), m.errorWidth())),
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

// layout recomputes panel sizes; called whenever anything that affects the
// available space changes.
func (m *Model) layout() {
	treeW, detailW, panelH := m.panelSizes()

	w, h := innerSize(m.styles.Panel, treeW, panelH)
	m.tree.SetSize(w, h)

	w, h = innerSize(m.styles.Panel, detailW, panelH)
	m.detail.SetSize(w, h)
}

// panelSizes splits the screen: a service tree on the left, detail on the
// right, with the status and help bars taking the bottom rows.
func (m Model) panelSizes() (treeW, detailW, panelH int) {
	const (
		minTreeWidth = 24
		maxTreeWidth = 48
	)

	treeW = min(max(m.width*2/5, minTreeWidth), maxTreeWidth)
	treeW = min(treeW, m.width)
	detailW = m.width - treeW

	// The status bar takes one row; the help bar takes however many it needs.
	panelH = max(m.height-1-lipgloss.Height(m.help.View(m.keys)), 3)
	return treeW, detailW, panelH
}

// wrap hard-wraps text to width on whitespace.
func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}
