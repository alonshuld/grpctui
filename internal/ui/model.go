// Package ui holds grpctui's bubbletea models.
//
// One root [Model] owns focus and the panel layout; the panels themselves live
// in internal/ui/panels as child models. Blocking work never happens inside
// Update — reflection and RPCs are tea.Cmds that return a message.
//
// Nothing here imports google.golang.org/grpc. The UI talks to the transport
// layer through the small [Client] interface and the plain types in
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
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// DefaultDiscoveryTimeout bounds a single reflection sweep.
const DefaultDiscoveryTimeout = 10 * time.Second

// DefaultCallTimeout bounds a single unary call. It is generous on purpose:
// the user can give up sooner with esc, and a tool for debugging misbehaving
// services should not be the thing that decides a slow server has failed.
const DefaultCallTimeout = 60 * time.Second

// Discoverer is the discovery half of the transport layer.
type Discoverer interface {
	Target() string
	ListServices(ctx context.Context) ([]grpcclient.Service, error)
}

// Invoker is the invocation half of the transport layer.
type Invoker interface {
	InvokeUnary(ctx context.Context, method grpcclient.Method, req proto.Message) (*grpcclient.UnaryResponse, error)
}

// Client is the slice of the transport layer the UI depends on. Keeping it an
// interface here — rather than taking *grpcclient.Client — is what lets the UI
// tests run with no server at all.
type Client interface {
	Discoverer
	Invoker
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
	focusRequest
	focusResponse

	// focusCount is how many panels tab cycles through.
	focusCount = int(focusResponse) + 1
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

// WithCallTimeout overrides [DefaultCallTimeout].
func WithCallTimeout(d time.Duration) Option {
	return func(m *Model) {
		if d > 0 {
			m.callTimeout = d
		}
	}
}

// Model is grpctui's root model.
type Model struct {
	client Client
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

	tree     panels.Tree
	request  panels.Form
	response panels.Response

	state    state
	err      error
	focus    focus
	services []grpcclient.Service

	// callSeq numbers calls so that a result arriving after the user has moved
	// on — a slow call the user cancelled and replaced — can be dropped instead
	// of overwriting the newer one.
	callSeq int

	// cancelCall aborts the call in flight, if any.
	cancelCall context.CancelFunc

	width  int
	height int

	// helpH is the height of the help bar, measured whenever it can have
	// changed rather than on every frame: measuring means rendering the whole
	// bar, and computeLayout runs twice per keystroke purely for this number.
	helpH int

	discoveryTimeout time.Duration
	callTimeout      time.Duration
}

// New builds the root model for a target.
func New(client Client, opts ...Option) Model {
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
		request:          panels.NewForm(km, st),
		response:         panels.NewResponse(km, st),
		state:            stateConnecting,
		discoveryTimeout: DefaultDiscoveryTimeout,
		callTimeout:      DefaultCallTimeout,
	}
	for _, opt := range opts {
		opt(&m)
	}
	m.syncFocus()
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
		m.measureHelp()
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
		m.request.Clear()
		m.response.Clear()
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
		// A call still in flight was made against the method the user has just
		// left, so it is cancelled and its sequence number retired. Without
		// that, its answer arrives after the panels have moved on and is drawn
		// under the new method's name as if it belonged to it — and, since the
		// panel is no longer in its in-flight state, esc has stopped being able
		// to cancel it.
		m.retireCall()

		// Focus deliberately stays on the tree: selecting builds the request
		// form but does not move the user into it, so j/k keep walking the
		// method list. Tab is how you enter the form — the same way lazygit and
		// k9s behave.
		m.request.SetMethod(msg.Service, msg.Method)
		m.response.SetMethod(msg.Method)
		m.layout()
		m.logger.Debug("method selected", zap.String("method", msg.Method.FullName))
		return m, nil

	case panels.SendRequestMsg:
		return m, m.startCall(msg)

	case callFinishedMsg:
		return m.finishCall(msg)

	case spinner.TickMsg:
		return m.tick(msg)
	}

	return m.updatePanels(msg)
}

// handleKey handles the keys the root model owns. It reports whether the key
// was consumed, so panel keys fall through untouched.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	// These work everywhere, including inside a text field being edited: ctrl+c
	// because there must always be a way out, and send and the panel switches
	// because filling in the last field and firing the call is the whole
	// workflow.
	switch {
	case key.Matches(msg, m.keys.ForceQuit):
		return tea.Quit, true

	case key.Matches(msg, m.keys.Send):
		if m.state != stateReady {
			return nil, true
		}
		return m.send(), true

	case key.Matches(msg, m.keys.NextPanel):
		return nil, m.movePanel(1)

	case key.Matches(msg, m.keys.PrevPanel):
		return nil, m.movePanel(-1)
	}

	// While a field is being edited every remaining key is a character, not a
	// command — q types a q.
	if m.request.Editing() {
		return nil, false
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit, true

	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.measureHelp()
		m.layout()
		return nil, true

	case key.Matches(msg, m.keys.Retry) && m.state == stateFailed:
		m.state = stateConnecting
		m.err = nil
		return tea.Batch(m.spinner.Tick, m.discover()), true

	case key.Matches(msg, m.keys.Cancel) && m.response.InFlight():
		m.abortCall()
		return nil, true
	}
	return nil, false
}

// send builds a request from the form and starts the call, or leaves the form
// showing why it could not.
//
// A call already in flight is replaced rather than protected: [Model.startCall]
// cancels it and the sequence number drops its answer, which is what the user
// pressing send again is asking for. Swallowing the keystroke instead would
// look like the key had stopped working.
func (m *Model) send() tea.Cmd {
	req, ok := m.request.Submit()
	if !ok {
		// The form grew an error row, so the split between it and the response
		// panel has to be recomputed.
		m.layout()
		return nil
	}
	return m.startCall(req)
}

// startCall issues one unary call, tagged with a sequence number so that a
// result the user has moved on from can be discarded.
func (m *Model) startCall(req panels.SendRequestMsg) tea.Cmd {
	m.abortCall()
	m.callSeq++

	ctx, cancel := context.WithTimeout(m.ctx, m.callTimeout)
	m.cancelCall = cancel

	m.logger.Info("calling method",
		zap.String("target", m.client.Target()),
		zap.String("method", req.Method.FullName),
	)

	tick := m.response.SetInFlight(req.Method)
	m.layout()
	return tea.Batch(tick, invoke(ctx, cancel, m.client, req, m.callSeq))
}

// invoke runs the call off the Update goroutine, decoding the response there
// too so that the panel receives text and never a protobuf message.
func invoke(ctx context.Context, cancel context.CancelFunc, client Invoker, req panels.SendRequestMsg, seq int) tea.Cmd {
	return func() tea.Msg {
		defer cancel()

		resp, err := client.InvokeUnary(ctx, req.Method, req.Request)
		if err != nil {
			st, ok := grpcclient.StatusOf(err)
			return callFinishedMsg{seq: seq, err: err, status: st, hasStatus: ok}
		}

		body, format, err := protoschema.Marshal(resp.Message)
		if err != nil {
			return callFinishedMsg{seq: seq, err: err, duration: resp.Duration}
		}
		return callFinishedMsg{seq: seq, body: body, format: format, duration: resp.Duration}
	}
}

func (m Model) finishCall(msg callFinishedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.callSeq {
		m.logger.Debug("dropping stale call result", zap.Int("seq", msg.seq))
		return m, nil
	}
	m.cancelCall = nil

	if msg.err != nil {
		m.response.SetFailure(msg.err.Error(), msg.status, msg.hasStatus, msg.duration)
	} else {
		m.response.SetSuccess(msg.body, msg.format, msg.duration)
	}
	m.layout()
	return m, nil
}

// abortCall cancels the call in flight, if any. The cancellation surfaces as an
// ordinary failed call, so the panel needs no separate "cancelled" state.
func (m *Model) abortCall() {
	if m.cancelCall == nil {
		return
	}
	m.cancelCall()
	m.cancelCall = nil
}

// retireCall abandons the call in flight, if any: it is cancelled and its
// sequence number bumped, so the answer already on its way is dropped rather
// than shown.
//
// That is the difference from [Model.abortCall], which the user presses esc for
// and which is meant to put "Canceled" on screen. Here there is nothing left to
// put it under.
func (m *Model) retireCall() {
	if m.cancelCall == nil {
		return
	}
	retired := m.callSeq
	m.abortCall()
	m.callSeq++
	m.logger.Debug("abandoned an in-flight call", zap.Int("seq", retired))
}

// tick feeds a spinner tick to whichever spinner it belongs to. Each
// spinner.Model ignores ticks that are not its own, so both can be fed
// unconditionally.
func (m Model) tick(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	if m.state == stateConnecting {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	var cmd tea.Cmd
	m.response, cmd = m.response.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m Model) updatePanels(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.state != stateReady {
		return m, nil
	}

	var cmds []tea.Cmd
	var cmd tea.Cmd

	m.tree, cmd = m.tree.Update(msg)
	cmds = append(cmds, cmd)

	m.request, cmd = m.request.Update(msg)
	cmds = append(cmds, cmd)

	m.response, cmd = m.response.Update(msg)
	cmds = append(cmds, cmd)

	// Editing a field changes how tall the form wants to be only when it grows
	// a hint row, but recomputing is cheap and getting it wrong leaves the
	// panels overlapping.
	m.layout()

	return m, tea.Batch(cmds...)
}

// movePanel cycles focus. It reports the key as consumed even before discovery
// lands, so tab never leaks through to a panel that is not on screen.
func (m *Model) movePanel(delta int) bool {
	if m.state != stateReady {
		return true
	}
	m.focus = focus((int(m.focus) + delta + focusCount) % focusCount)
	m.syncFocus()
	return true
}

func (m *Model) syncFocus() {
	m.tree.Blur()
	m.request.Blur()
	m.response.Blur()

	switch m.focus {
	case focusRequest:
		m.request.Focus()
	case focusResponse:
		m.response.Focus()
	default:
		m.tree.Focus()
	}
}

// View renders the whole screen.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		// No WindowSizeMsg yet: bubbletea sends one immediately, so this frame
		// is never actually seen.
		return ""
	}

	var screen string
	switch m.state {
	case stateConnecting:
		screen = m.centred(fmt.Sprintf("%s Connecting to %s…",
			m.spinner.View(), m.styles.Value.Render(m.client.Target())))
	case stateFailed:
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

	right := lipgloss.JoinVertical(lipgloss.Left,
		m.framePanel("Request", m.request.View(), l.rightW, l.requestH, m.request.Focused()),
		m.framePanel("Response", m.response.View(), l.rightW, l.responseH, m.response.Focused()),
	)

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		m.framePanel("Services", m.tree.View(), l.treeW, l.bodyH, m.tree.Focused()),
		right,
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

// layoutSizes is the geometry of one frame: a service tree down the left, the
// request form over the response down the right.
type layoutSizes struct {
	treeW     int
	rightW    int
	bodyH     int
	requestH  int
	responseH int
}

// layout recomputes panel sizes; called whenever anything that affects the
// available space changes — including the form growing a row, which steals
// height from the response.
func (m *Model) layout() {
	l := m.computeLayout()

	w, h := innerSize(m.styles.Panel, l.treeW, l.bodyH)
	m.tree.SetSize(w, h)

	w, h = innerSize(m.styles.Panel, l.rightW, l.requestH)
	m.request.SetSize(w, h)

	w, h = innerSize(m.styles.Panel, l.rightW, l.responseH)
	m.response.SetSize(w, h)
}

func (m Model) computeLayout() layoutSizes {
	const (
		minTreeWidth   = 24
		maxTreeWidth   = 48
		minPanelHeight = 3
	)

	l := layoutSizes{}
	l.treeW = min(max(m.width*2/5, minTreeWidth), maxTreeWidth)
	l.treeW = min(l.treeW, m.width)
	l.rightW = m.width - l.treeW

	// The status bar takes one row; the help bar takes however many it needs.
	l.bodyH = max(m.height-1-m.helpHeight(), minPanelHeight)

	// The form gets the height it asks for and the response takes the rest: a
	// three-field request should not reserve half the screen.
	chrome := m.styles.Panel.GetVerticalBorderSize() + m.styles.Panel.GetVerticalPadding() + 1
	l.requestH, l.responseH = splitHeight(l.bodyH, m.request.ContentHeight()+chrome, minPanelHeight)
	return l
}

// helpHeight reports how many rows the help bar occupies, from the cached
// measurement. It falls back to measuring so that a Model nobody has sized —
// one built straight from [New] in a test — still lays out correctly.
func (m Model) helpHeight() int {
	if m.helpH > 0 {
		return m.helpH
	}
	return lipgloss.Height(m.help.View(m.keys))
}

// measureHelp re-measures the help bar. Only two things change its height: the
// terminal width, and `?`.
func (m *Model) measureHelp() { m.helpH = lipgloss.Height(m.help.View(m.keys)) }

// splitHeight divides the right-hand column, giving the top panel the height it
// wants within what is left after the bottom one's minimum. A column too short
// to satisfy both minimums is halved instead, because a panel of zero rows
// renders as a broken box rather than as nothing.
func splitHeight(total, want, minEach int) (top, bottom int) {
	if total < 2*minEach {
		top = total / 2
		return top, total - top
	}
	top = min(max(want, minEach), total-minEach)
	return top, total - top
}
