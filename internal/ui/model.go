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
	"io"
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
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
	"github.com/alonshuld/grpctui/internal/vars"
)

// DefaultDiscoveryTimeout bounds a single reflection sweep.
const DefaultDiscoveryTimeout = 10 * time.Second

// DefaultCallTimeout bounds a single unary call. It is generous on purpose:
// the user can give up sooner with esc, and a tool for debugging misbehaving
// services should not be the thing that decides a slow server has failed.
//
// It deliberately does not apply to a stream. Watching a server-streaming
// method for an hour is the feature, and a timeout would make the tool decide
// when a watch has gone on long enough; esc ends one, and nothing else does.
const DefaultCallTimeout = 60 * time.Second

// Discoverer is the discovery half of the transport layer.
type Discoverer interface {
	Target() string
	ListServices(ctx context.Context, md grpcclient.Metadata) ([]grpcclient.Service, error)
}

// Invoker is the invocation half of the transport layer.
type Invoker interface {
	InvokeUnary(ctx context.Context, method grpcclient.Method, req proto.Message, md grpcclient.Metadata) (*grpcclient.UnaryResponse, error)
}

// Streamer opens streaming calls. It is separate from [Invoker] because the two
// hand back different things: a unary call returns its answer, a stream returns
// something to drive.
type Streamer interface {
	InvokeStream(ctx context.Context, method grpcclient.Method, md grpcclient.Metadata) (grpcclient.Stream, error)
}

// Client is the slice of the transport layer the UI depends on. Keeping it an
// interface here — rather than taking *grpcclient.Client — is what lets the UI
// tests run with no server at all.
type Client interface {
	Discoverer
	Invoker
	Streamer
}

// Dialer opens a connection for a profile. The root model uses it to switch
// between saved connections without restarting, which is the whole point of
// having profiles.
type Dialer interface {
	Dial(profile grpcclient.Profile) (Client, error)
}

// DialerFunc adapts a function to [Dialer].
type DialerFunc func(profile grpcclient.Profile) (Client, error)

// Dial implements [Dialer].
func (f DialerFunc) Dial(profile grpcclient.Profile) (Client, error) { return f(profile) }

type state int

const (
	stateConnecting state = iota
	stateReady
	stateFailed
)

type focus int

// The tab cycle, in the order the panels are laid out: the tree down the left,
// then the right-hand column from top to bottom.
const (
	focusTree focus = iota
	focusRequest
	focusMetadata
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

// WithClock replaces the clock the UI reads for a stream's elapsed time. A test
// that freezes it gets a stream panel whose every line is decided by the
// messages it was fed.
func WithClock(now func() time.Time) Option {
	return func(m *Model) {
		if now != nil {
			m.now = now
		}
	}
}

// WithDialer supplies the way to open a connection for a profile. Without one
// the profile switcher can list connections but not move between them, which is
// what a UI test with a hand-made client gets.
func WithDialer(d Dialer) Option {
	return func(m *Model) {
		if d != nil {
			m.dialer = d
		}
	}
}

// WithProfiles supplies the saved connections and says which one the client
// passed to [New] belongs to. The active profile's headers seed the metadata
// panel.
func WithProfiles(profiles []grpcclient.Profile, active int) Option {
	return func(m *Model) {
		m.profiles.SetProfiles(profiles, active)
		if p, ok := m.profiles.Active(); ok {
			m.metadata.SetHeaders(p.Metadata)
		}
	}
}

// WithEnvironments supplies the named variable sets and says which one to start
// in. A negative index — which is what a config file with no `environments` key
// yields — starts with nothing bound, and every {{variable}} reference is then
// text like any other.
func WithEnvironments(envs []vars.Environment, active int) Option {
	return func(m *Model) {
		m.environments.SetEnvironments(envs, active)
		m.installEnvironment()
	}
}

// WithHistory supplies the requests already sent. Without one the model keeps
// a session-only history: [ and ] still walk it, it is simply not written down.
func WithHistory(h requests.History) Option {
	return func(m *Model) { m.history = h }
}

// WithCollections supplies the saved requests. Without one the browser shows
// history alone and saving reports that there is nowhere to save to.
func WithCollections(c requests.Collections) Option {
	return func(m *Model) { m.browser.SetCollections(c) }
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
	metadata panels.Metadata
	response panels.Response

	// profiles is the connection switcher, browser the saved-request list,
	// environments the variable-set switcher and variables the list of what the
	// active one binds. All four are modal rather than panels in the tab cycle:
	// while one is open it owns the keyboard and covers the body.
	//
	// variables owns the bindings themselves — see [Model.bindings] — so that
	// there is one copy of them rather than one the panel draws and another the
	// requests resolve against.
	profiles     panels.Profiles
	browser      panels.Requests
	environments panels.Environments
	variables    panels.Variables

	// history is every request sent, newest first. It lives on the model rather
	// than behind the browser because [ and ] walk it with the browser closed.
	history requests.History

	// historyAt is where [ and ] have walked to, or -1 when the form holds
	// something the user built rather than something recalled. Stepping back
	// from -1 lands on the newest entry.
	historyAt int

	// lastCollection is where the previous save went, and what the next save
	// prompt suggests. Somebody keeping one collection should never have to type
	// its name twice.
	lastCollection string

	// dialer opens a connection for a profile, and ownsClient records whether
	// the client currently held came from it — the one [New] was given belongs
	// to the caller and is not this model's to close.
	dialer     Dialer
	ownsClient bool

	state    state
	err      error
	focus    focus
	services []grpcclient.Service

	// callSeq numbers calls so that a result arriving after the user has moved
	// on — a slow call the user cancelled and replaced — can be dropped instead
	// of overwriting the newer one.
	callSeq int

	// connSeq does the same for connections: two quick presses in the switcher
	// leave two dials in flight, and only the later one's client may be kept.
	connSeq int

	// cancelCall aborts the call in flight, if any — a unary one or a stream.
	cancelCall context.CancelFunc

	// stream is the streaming call in flight, if any, and streamStart when it
	// was opened.
	stream      grpcclient.Stream
	streamStart time.Time

	// sendQueue holds the request messages waiting to go out on the stream, and
	// sending records that one of them is on its way. gRPC allows one sender at
	// a time, so pressing send twice in quick succession has to queue rather
	// than race — and a client-streaming call is a queue of messages by nature.
	sendQueue []proto.Message
	sending   bool

	// closeAfterQueue records that the user has asked to close the sending half,
	// which happens once whatever is already queued has gone out. Closing
	// straight away would throw away messages the user has already sent.
	closeAfterQueue bool

	// now is the clock the UI reads for a stream's elapsed time. Tests replace
	// it, so that a frame is a function of the messages that produced it and a
	// golden file of one is reproducible.
	now func() time.Time

	// pendingTarget is the address being dialled, so the connecting screen names
	// where it is going rather than where it has been.
	pendingTarget string

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
		metadata:         panels.NewMetadata(km, st),
		response:         panels.NewResponse(km, st),
		profiles:         panels.NewProfiles(km, st),
		browser:          panels.NewRequests(km, st),
		environments:     panels.NewEnvironments(km, st),
		variables:        panels.NewVariables(km, st),
		historyAt:        noHistory,
		lastCollection:   requests.DefaultCollection,
		state:            stateConnecting,
		now:              time.Now,
		discoveryTimeout: DefaultDiscoveryTimeout,
		callTimeout:      DefaultCallTimeout,
	}
	for _, opt := range opts {
		opt(&m)
	}

	// The browser reads the same clock the model does, so that a golden file of
	// a list of "5m ago" is decided by the messages that produced it.
	m.browser.SetClock(m.now)
	m.browser.SetHistory(m.history)

	// The form resolves against whatever is bound, including nothing: a model
	// built with no environments still has to expand a reference to a variable
	// the user binds later with v.
	m.request.SetResolver(m.bindings())

	m.syncFocus()
	return m
}

// bindings are the variables in force, which the variables panel owns.
func (m Model) bindings() vars.Set { return m.variables.Set() }

// installEnvironment points the variables panel and the request form at the
// active environment's bindings.
//
// Switching environment replaces the bindings rather than merging them: a value
// captured while pointed at staging is staging's, and carrying it into
// production would be the single most expensive thing this feature could do.
func (m *Model) installEnvironment() {
	env, _ := m.environments.Active()
	m.variables.SetVariables(env.Set(), env.Name)
	m.request.SetResolver(m.bindings())
}

// bind adds or replaces one variable for the rest of the session. Nothing is
// written to disk: a binding captured out of a response is very often a token,
// and grpctui's standing rule is that such a value never reaches a surface that
// outlives the call.
func (m *Model) bind(v vars.Variable) {
	env, _ := m.environments.Active()
	m.variables.SetVariables(m.bindings().With(v), env.Name)
	m.request.SetResolver(m.bindings())
}

// noHistory is [Model.historyAt] when the form does not hold a recalled
// request.
const noHistory = -1

// Init starts discovery and the loading spinner.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.discover())
}

// discover runs one reflection sweep off the Update goroutine.
//
// It carries the metadata panel's headers: discovery is an RPC like any other,
// so a server that gates its API behind a header gates its schema behind the
// same one.
func (m Model) discover() tea.Cmd {
	client, parent, timeout := m.client, m.ctx, m.discoveryTimeout

	// A reference nothing binds is left as written here rather than refusing the
	// sweep. Discovery is not something the user asked for at this moment, and a
	// connection that will not even list its services because a variable is
	// missing is a worse answer than a server that says Unauthenticated.
	md, err := m.headers()
	if err != nil {
		m.logger.Debug("discovering with unresolved headers", zap.Error(err))
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()

		services, err := client.ListServices(ctx, md)
		if err != nil {
			return discoveryFailedMsg{err: err}
		}
		return servicesDiscoveredMsg{services: services}
	}
}

// connect opens a connection for a profile off the Update goroutine. Dialling
// does no I/O of its own, but reading a CA bundle and a client key does, and
// Update is not the place for it.
func (m Model) connect(msg panels.ProfileSelectedMsg) tea.Cmd {
	dialer, seq := m.dialer, m.connSeq
	return func() tea.Msg {
		client, err := dialer.Dial(msg.Profile)
		return clientConnectedMsg{
			seq:     seq,
			index:   msg.Index,
			profile: msg.Profile,
			client:  client,
			err:     err,
		}
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
		// The form now holds something the user built rather than something
		// recalled, so the history walk starts over: `[` from here means "the
		// last thing I sent", not "one before wherever I had walked to".
		m.historyAt = noHistory

		m.request.SetMethod(msg.Service, msg.Method)
		m.response.SetMethod(msg.Method)
		m.layout()
		m.logger.Debug("method selected", zap.String("method", msg.Method.FullName))
		return m, nil

	case panels.SendRequestMsg:
		return m, m.dispatch(msg)

	case panels.LoadRequestMsg:
		return m.loadRequest(msg)

	case panels.SaveRequestMsg:
		return m, m.saveRequest(msg)

	case requestSavedMsg:
		return m.finishSave(msg)

	case historySavedMsg:
		if msg.err != nil {
			// History is a convenience, not the call. A state directory that
			// cannot be written to is worth a line in the log and nothing on
			// screen: the request went out either way.
			m.logger.Warn("could not write history", zap.Error(msg.err))
		}
		return m, nil

	case callFinishedMsg:
		return m.finishCall(msg)

	case streamOpenedMsg:
		return m.streamOpened(msg)

	case streamSentMsg:
		return m.streamSent(msg)

	case streamRecvMsg:
		return m.streamReceived(msg)

	case panels.ProfileSelectedMsg:
		return m, m.startConnect(msg)

	case panels.EnvironmentSelectedMsg:
		return m, m.switchEnvironment(msg)

	case panels.VariableBoundMsg:
		m.bind(vars.Variable{Name: msg.Name, Value: msg.Value})
		m.variables.Open()
		m.layout()
		m.logger.Debug("bound a variable", zap.String("name", msg.Name))
		return m, nil

	case panels.VariableUnboundMsg:
		env, _ := m.environments.Active()
		m.variables.SetVariables(m.bindings().Without(msg.Name), env.Name)
		m.request.SetResolver(m.bindings())
		m.layout()
		return m, nil

	case panels.VariableCapturedMsg:
		return m.capture(msg)

	case clientConnectedMsg:
		return m.finishConnect(msg)

	case spinner.TickMsg:
		return m.tick(msg)
	}

	return m.updatePanels(msg)
}

// handleKey handles the keys the root model owns. It reports whether the key
// was consumed, so panel keys fall through untouched.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	// ctrl+c comes before even the switcher: there must always be a way out.
	if key.Matches(msg, m.keys.ForceQuit) {
		return tea.Quit, true
	}

	// The modals own every remaining key while they are open, including the
	// panel switches — each is a choice to finish, not a place to tab out of.
	// The browser and the variables panel come before the send below, because
	// inside them ctrl+s means "load and send this one" and ctrl+p means
	// "capture into this prompt".
	if m.profiles.Opened() {
		var cmd tea.Cmd
		m.profiles, cmd = m.profiles.Update(msg)
		return cmd, true
	}
	if m.environments.Opened() {
		var cmd tea.Cmd
		m.environments, cmd = m.environments.Update(msg)
		return cmd, true
	}
	if m.browser.Opened() {
		var cmd tea.Cmd
		m.browser, cmd = m.browser.Update(msg)
		m.layout()
		return cmd, true
	}
	if m.variables.Opened() {
		var cmd tea.Cmd
		m.variables, cmd = m.variables.Update(msg)
		m.layout()
		return cmd, true
	}

	// These work everywhere, including inside a text field being edited: send
	// and the panel switches, because filling in the last field and firing the
	// call is the whole workflow.
	switch {
	case key.Matches(msg, m.keys.Send):
		if m.state != stateReady {
			return nil, true
		}
		return m.send(), true

	case key.Matches(msg, m.keys.EndStream):
		return m.endSending(), true

	case key.Matches(msg, m.keys.Requests):
		// ctrl+r rather than a letter precisely so that it reaches here from
		// inside a half-typed field: recalling the request you meant is the
		// answer to "I am typing this out again".
		m.openBrowser()
		return nil, true

	case key.Matches(msg, m.keys.Capture):
		// ctrl+p for the same reason: reading an id out of the response and
		// putting it in the field you are already typing into is the ordinary
		// way a chain of requests gets built.
		m.startCapture()
		return nil, true

	case key.Matches(msg, m.keys.NextPanel):
		return nil, m.movePanel(1)

	case key.Matches(msg, m.keys.PrevPanel):
		return nil, m.movePanel(-1)
	}

	// While a field is being edited every remaining key is a character, not a
	// command — q types a q, and p types a p.
	if m.editing() {
		return nil, false
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit, true

	case key.Matches(msg, m.keys.Profiles):
		m.profiles.Open()
		return nil, true

	case key.Matches(msg, m.keys.Environments):
		m.environments.Open()
		return nil, true

	case key.Matches(msg, m.keys.Variables):
		m.variables.Open()
		m.layout()
		return nil, true

	case key.Matches(msg, m.keys.HistoryPrev):
		return m.stepHistory(1), true

	case key.Matches(msg, m.keys.HistoryNext):
		return m.stepHistory(-1), true

	case key.Matches(msg, m.keys.Save):
		m.startSave()
		return nil, true

	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.measureHelp()
		m.layout()
		return nil, true

	case key.Matches(msg, m.keys.Retry) && m.state == stateFailed:
		m.state = stateConnecting
		m.err = nil
		return tea.Batch(m.spinner.Tick, m.discover()), true

	case key.Matches(msg, m.keys.Cancel) && m.response.Active():
		m.abortCall()
		return nil, true
	}
	return nil, false
}

// editing reports whether a text field somewhere is being typed into.
func (m Model) editing() bool { return m.request.Editing() || m.metadata.Editing() }

// send builds a request from the form and starts the call, or leaves the form
// showing why it could not.
//
// A call already in flight is replaced rather than protected: [Model.startCall]
// cancels it and the sequence number drops its answer, which is what the user
// pressing send again is asking for. Swallowing the keystroke instead would
// look like the key had stopped working.
func (m *Model) send() tea.Cmd {
	// A malformed header is refused here rather than at the transport layer, so
	// that the complaint lands on the row that caused it. The cursor is moved
	// onto that row and the panel given focus, because a panel capped at eight
	// lines can have the offending header scrolled out of sight.
	if err := m.metadata.Validate(); err != nil {
		m.metadata.FocusFirstInvalid()
		m.focus = focusMetadata
		m.syncFocus()
		m.layout()
		m.logger.Debug("refused to send with invalid headers", zap.Error(err))
		return nil
	}

	req, ok := m.request.Submit()
	if !ok {
		// The form grew an error row, so the split between it and the response
		// panel has to be recomputed.
		m.layout()
		return nil
	}
	return m.dispatch(req)
}

// dispatch sends one request, by whichever shape its method has, and records it
// in history on the way past.
//
// It is the single door every send goes through, which is why the history entry
// is written here rather than in each of the three places that put a message on
// the wire — including the one a [panels.LoadRequestMsg] arrives by, where
// there is no form submission to hang it off.
func (m *Model) dispatch(req panels.SendRequestMsg) tea.Cmd {
	// The headers are resolved here rather than in each of the three senders,
	// for the same reason the history entry is written here: there is one door,
	// and a reference nobody bound must refuse the call whichever way in it came.
	m.metadata.SetNotice("")

	md, err := m.headers()
	if err != nil {
		m.metadata.SetNotice(err.Error())
		m.focus = focusMetadata
		m.syncFocus()
		m.layout()
		m.logger.Debug("refused to send with unresolved headers", zap.Error(err))
		return nil
	}

	record := m.record(req, md)

	if req.Method.Kind() == grpcclient.KindUnary {
		return tea.Batch(record, m.startCall(req, md))
	}
	return tea.Batch(record, m.sendOnStream(req, md))
}

// headers are the metadata the next call carries, with every {{variable}}
// reference expanded.
//
// A reference nothing binds is refused rather than sent as written: an
// `authorization: Bearer {{token}}` that goes out literally comes back
// Unauthenticated, which is the most misleading answer the server could
// possibly give. Disabled headers are skipped — one parked precisely because it
// referred to something is not a reason to refuse the call.
func (m Model) headers() (grpcclient.Metadata, error) {
	md := m.metadata.Headers()
	set := m.bindings()

	var errs []error
	for i, h := range md {
		if h.Disabled {
			continue
		}
		value, err := set.Resolve(h.Value)
		if err != nil {
			errs = append(errs, fmt.Errorf("header %q: %w", h.Key, err))
			continue
		}
		md[i].Value = value
	}
	return md, errors.Join(errs...)
}

// record adds one sent request to history and returns the command that writes
// history out.
//
// The entry carries header *names* and no values, and the request as it was
// *typed* rather than as it was sent. A history file sits in the state
// directory for weeks and a collection is meant to be committed, so the
// standing rule that a credential never reaches a surface outliving the call
// applies to both — and since v0.7 the most likely credential in a request body
// is a {{token}} captured out of a login response.
func (m *Model) record(req panels.SendRequestMsg, md grpcclient.Metadata) tea.Cmd {
	entry, ok := m.entry(req, md)
	if !ok {
		return nil
	}

	m.history.Add(entry)
	m.browser.SetHistory(m.history)

	// The form now holds the newest entry, so [ should step to the one before it
	// rather than back to what was just sent.
	m.historyAt = 0

	return saveHistory(m.history)
}

// entry builds the record of one request, for history and for a collection
// alike — they hold the same thing for different reasons, so there is one place
// that decides what it holds.
//
// The body comes from the request's template, which is the same message with
// its {{variable}} references left as they were written. A template that could
// not be built falls back to the request that went out: a call worth making is
// worth recording imperfectly rather than not at all.
func (m Model) entry(req panels.SendRequestMsg, md grpcclient.Metadata) (requests.Request, bool) {
	message, values := req.Template, req.Values
	if message == nil {
		message = req.Request
	}

	body, err := protoschema.EncodeBody(message)
	if err != nil {
		// The request itself is fine — it built from the form. Only recording it
		// failed, and dropping the entry beats refusing the call.
		m.logger.Warn("could not record the request",
			zap.String("method", req.Method.FullName),
			zap.Error(err),
		)
		return requests.Request{}, false
	}

	entry := requests.Request{
		Method:  req.Method.FullName,
		Kind:    string(req.Method.Kind()),
		Target:  m.client.Target(),
		Headers: md.Keys(),
		Body:    body,
		Values:  values,
		SentAt:  m.now().UTC(),
	}
	if profile, ok := m.profiles.Active(); ok {
		entry.Profile = profile.Label()
	}
	return entry, true
}

// saveHistory writes history out off the Update goroutine.
func saveHistory(h requests.History) tea.Cmd {
	return func() tea.Msg { return historySavedMsg{err: h.Save()} }
}

// sendOnStream puts one request message on the stream, opening it first if it
// is not already running.
//
// That is the same keystroke meaning two things by context, and deliberately:
// ctrl+s is "send what the form says", and on a client-streaming call that is a
// thing you do repeatedly before ending the request stream with ctrl+e.
func (m *Model) sendOnStream(req panels.SendRequestMsg, md grpcclient.Metadata) tea.Cmd {
	// A method the client does not stream into carries exactly one request, so
	// there is no second message to send on its stream: ctrl+s starts a new one,
	// the same way a second ctrl+s replaces a unary call in flight.
	//
	// A client-streaming stream whose sending half the user has already closed
	// is deliberately not restarted. They ended the request stream and are
	// waiting for the answer to it; throwing that away and starting again is the
	// one thing they cannot have meant.
	if m.stream == nil || !req.Method.ClientStreaming {
		return m.startStream(req, md)
	}

	m.sendQueue = append(m.sendQueue, req.Request)
	return m.nextSend()
}

// loadRequest fills the form in from the browser's choice, and sends it when
// that is what was asked for.
//
// The browser is a different way in from the history walk, so it puts the walk
// back to the start: `[` after picking something out of the list means "the
// last thing I sent" rather than one step from wherever the walk had reached
// before the list was opened. A collection entry has no place in that sequence
// at all until it is sent.
func (m Model) loadRequest(msg panels.LoadRequestMsg) (tea.Model, tea.Cmd) {
	if m.state != stateReady || !m.load(msg.Request) {
		return m, nil
	}
	m.historyAt = noHistory

	if !msg.Send {
		return m, nil
	}

	req, ok := m.request.Submit()
	if !ok {
		m.layout()
		return m, nil
	}
	return m, m.dispatch(req)
}

// openBrowser shows the saved-request list.
func (m *Model) openBrowser() {
	m.browser.Open()
	m.layout()
}

// startCapture asks which value of the last response to bind, and to what.
//
// It refuses when there is nothing to read from rather than opening a prompt
// over an empty response: a prompt that can only fail is a worse answer than
// the reason it would have failed for.
func (m *Model) startCapture() {
	_, format, ok := m.responseBody()
	switch {
	case !ok:
		m.variables.Open()
		m.variables.SetNotice("There is no response to capture from yet.", true)
	case format != protoschema.FormatJSON:
		m.variables.Open()
		m.variables.SetNotice("This response is protobuf text, so there is no path to read.", true)
	default:
		m.variables.OpenCapture("")
	}
	m.layout()
}

// responseBody is the message a capture would read from.
func (m Model) responseBody() (string, protoschema.Format, bool) {
	return m.response.LastMessage()
}

// capture reads one value out of the last response and binds it.
//
// The lookup happens here rather than in the panel because only the root model
// has the response, and because a path that names nothing is a thing to report
// on the prompt — with the prompt still up, so it can be corrected — rather
// than a thing to guess at.
func (m Model) capture(msg panels.VariableCapturedMsg) (tea.Model, tea.Cmd) {
	body, _, ok := m.responseBody()
	if !ok {
		m.variables.SetNotice("There is no response to capture from yet.", true)
		m.layout()
		return m, nil
	}

	value, err := protoschema.LookupJSON(body, msg.Path)
	if err != nil {
		m.variables.SetNotice(err.Error(), true)
		m.layout()
		m.logger.Debug("could not capture from the response",
			zap.String("name", msg.Name), zap.Error(err))
		return m, nil
	}

	m.bind(vars.Variable{Name: msg.Name, Value: value, Captured: true})
	m.variables.Open()
	m.layout()

	// The name and where it came from, never what it is worth: a captured value
	// is a bearer token as often as not.
	m.logger.Info("captured a variable",
		zap.String("name", msg.Name), zap.String("path", msg.Path))
	return m, nil
}

// switchEnvironment installs another environment's bindings, and reconnects
// when it points somewhere else.
//
// Reconnecting is the point of an environment having a target at all: "staging"
// usually means both a different host and a different account id, and having to
// switch those separately is how a request meant for staging reaches
// production. The connection keeps the active profile's security and
// credentials — how to connect is the profile's business, where to connect is
// the environment's.
func (m *Model) switchEnvironment(msg panels.EnvironmentSelectedMsg) tea.Cmd {
	m.environments.SetActive(msg.Index)
	m.installEnvironment()
	m.layout()

	m.logger.Info("switched environment",
		zap.String("environment", msg.Environment.Name),
		zap.Strings("variables", m.bindings().Names()),
	)

	if msg.Environment.Target == "" || msg.Environment.Target == m.client.Target() {
		return nil
	}

	profile, ok := m.profiles.Active()
	if !ok {
		m.logger.Warn("cannot follow the environment's target: no profile is active",
			zap.String("target", msg.Environment.Target))
		return nil
	}

	profile.Target = msg.Environment.Target
	return m.startConnect(panels.ProfileSelectedMsg{
		Index:   m.profiles.ActiveIndex(),
		Profile: profile,
	})
}

// startSave asks where to put the request in the form.
//
// The request is built first, and a form that does not build stops here with
// its error rows showing rather than behind a modal that covers them. That also
// means the prompt only ever opens for something that can actually be saved.
func (m *Model) startSave() {
	if m.state != stateReady {
		return
	}
	if _, ok := m.request.Submit(); !ok {
		m.layout()
		return
	}

	method, ok := m.request.Method()
	if !ok {
		return
	}

	m.browser.OpenSave(m.lastCollection + "/" + shortName(method.FullName))
	m.layout()
}

// stepHistory walks the sent requests and loads the one it lands on. A positive
// delta goes back in time, which is the direction `[` points.
func (m *Model) stepHistory(delta int) tea.Cmd {
	if m.state != stateReady || m.history.Len() == 0 {
		return nil
	}

	// From a form the user built themselves, `[` lands on the newest entry and
	// `]` does nothing: there is nothing newer than what is on screen.
	next := m.historyAt + delta
	if m.historyAt == noHistory {
		if delta < 0 {
			return nil
		}
		next = 0
	}
	if next < 0 || next >= m.history.Len() {
		return nil
	}

	entry, ok := m.history.At(next)
	if !ok {
		return nil
	}
	if !m.load(entry) {
		return nil
	}

	m.historyAt = next
	return nil
}

// load fills the tree, the form and the notice line in from a saved request,
// and reports whether it could.
//
// Header values are deliberately not restored: the record never held any. What
// the record does hold — the names — is said on the notice line when the live
// connection is not already sending them, so that a call that came back
// Unauthenticated has an explanation rather than a mystery.
func (m *Model) load(entry requests.Request) bool {
	svc, method, ok := m.tree.SelectMethod(entry.Method)
	if !ok {
		m.request.SetNotice(entry.Method + " is not on this connection.")
		m.layout()
		m.logger.Debug("recalled a method this connection does not have",
			zap.String("method", entry.Method))
		return false
	}

	msg, err := protoschema.DecodeBody(method.InputDescriptor(), entry.Body)
	if err != nil {
		m.request.SetNotice(err.Error())
		m.layout()
		m.logger.Warn("could not rebuild a saved request",
			zap.String("method", entry.Method), zap.Error(err))
		return false
	}

	m.retireCall()
	m.request.SetMethod(svc, method)
	m.request.Load(msg)

	// The references the body could not carry go back on the rows they were
	// typed into, which is what makes a recalled request a template again rather
	// than the zeroes its body had to hold. A path the method no longer has is
	// worth saying out loud: the alternative is a request quietly missing the
	// field you thought you had set.
	notice := m.missingHeaders(entry)
	if err := m.request.LoadValues(entry.Values); err != nil {
		notice = err.Error()
		m.logger.Warn("could not restore a saved request's variables",
			zap.String("method", entry.Method), zap.Error(err))
	}

	m.response.SetMethod(method)
	m.request.SetNotice(notice)
	m.layout()

	m.logger.Debug("recalled a request", zap.String("method", entry.Method))
	return true
}

// missingHeaders names the headers a recalled request went out with that the
// connection is not sending now, or "" when there are none.
func (m Model) missingHeaders(entry requests.Request) string {
	if len(entry.Headers) == 0 {
		return ""
	}

	have := make(map[string]bool, len(entry.Headers))
	for _, key := range m.metadata.Headers().Keys() {
		have[key] = true
	}

	var missing []string
	for _, key := range entry.Headers {
		if !have[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "This was sent with " + strings.Join(missing, ", ") + "."
}

// saveRequest writes the form's request into a collection.
func (m *Model) saveRequest(msg panels.SaveRequestMsg) tea.Cmd {
	req, ok := m.request.Submit()
	if !ok {
		// Between opening the prompt and answering it the modal owned the
		// keyboard, so the form cannot have changed — but a build that fails
		// twice is still a save that must not silently not happen.
		m.browser.SetNotice("The request no longer builds.", true)
		m.layout()
		return nil
	}

	// The headers are recorded by name, so an unresolved reference in one is no
	// reason to refuse a save: what goes in the file is the same either way.
	md, _ := m.headers()

	entry, ok := m.entry(req, md)
	if !ok {
		m.browser.SetNotice("The request could not be written down.", true)
		m.layout()
		return nil
	}
	entry.Name = msg.Name

	m.logger.Info("saving a request",
		zap.String("collection", msg.Collection),
		zap.String("name", msg.Name),
		zap.String("method", entry.Method),
	)
	return saveCollection(m.browser.Collections(), msg.Collection, entry)
}

// saveCollection writes one collection out off the Update goroutine, handing
// back the updated set so the model can install it.
func saveCollection(c requests.Collections, collection string, entry requests.Request) tea.Cmd {
	return func() tea.Msg {
		err := c.Save(collection, entry)
		return requestSavedMsg{
			collections: c,
			collection:  collection,
			name:        entry.Name,
			err:         err,
		}
	}
}

// finishSave reports the outcome of a save, leaving the prompt open on a
// failure so the name can be corrected rather than retyped.
func (m Model) finishSave(msg requestSavedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.browser.SetNotice(msg.err.Error(), true)
		m.layout()
		m.logger.Error("could not save the request", zap.Error(msg.err))
		return m, nil
	}

	m.browser.SetCollections(msg.collections)
	m.browser.SetNotice("", false)
	m.lastCollection = msg.collection
	m.layout()
	return m, nil
}

// shortName is a method's own name, without its package and service — the half
// of it worth suggesting as the name of a saved request.
func shortName(fullName string) string {
	if i := strings.LastIndex(fullName, "."); i >= 0 {
		return fullName[i+1:]
	}
	return fullName
}

// startStream opens a streaming call, tagged with a sequence number from the
// same counter unary calls use — only one call, of either shape, is ever in
// flight.
func (m *Model) startStream(req panels.SendRequestMsg, md grpcclient.Metadata) tea.Cmd {
	m.abortCall()
	m.dropStream()
	m.callSeq++

	// No timeout: see [DefaultCallTimeout]. The stream ends when the server
	// finishes, when the user presses esc, or when the process does.
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelCall = cancel
	m.streamStart = m.now()

	// The first request message goes out as soon as the stream is open. A
	// method the client does not stream into has exactly one, so its sending
	// half is closed behind it — leaving it open would keep a finished request
	// waiting on a user who has nothing left to say.
	m.sendQueue = []proto.Message{req.Request}
	m.closeAfterQueue = !req.Method.ClientStreaming

	// Header names, never their values.
	m.logger.Info("opening stream",
		zap.String("target", m.client.Target()),
		zap.String("method", req.Method.FullName),
		zap.String("kind", string(req.Method.Kind())),
		zap.Strings("headers", md.Keys()),
	)

	tick := m.response.SetStreaming(req.Method)
	m.layout()
	return tea.Batch(tick, openStream(ctx, m.client, req.Method, md, m.callSeq))
}

// openStream opens the call off the Update goroutine.
func openStream(ctx context.Context, client Streamer, method grpcclient.Method, md grpcclient.Metadata, seq int) tea.Cmd {
	return func() tea.Msg {
		stream, err := client.InvokeStream(ctx, method, md)
		return streamOpenedMsg{seq: seq, stream: stream, err: err}
	}
}

// streamOpened starts the two halves of the stream running: the queue that
// feeds it, and the chain of receives that drains it.
func (m Model) streamOpened(msg streamOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.callSeq {
		// A stream the user has already moved on from. It is closed here rather
		// than left to the garbage collector: an abandoned gRPC stream holds its
		// call open until something cancels it.
		if msg.stream != nil {
			_ = msg.stream.Close()
		}
		m.logger.Debug("dropping a stale stream", zap.Int("seq", msg.seq))
		return m, nil
	}

	if msg.err != nil {
		m.cancelCall = nil
		m.sendQueue, m.closeAfterQueue = nil, false
		st, hasStatus := grpcclient.StatusOf(msg.err)
		m.response.FinishStream(msg.err.Error(), st, hasStatus, m.elapsed())
		m.layout()
		m.logger.Error("could not open the stream", zap.Error(msg.err))
		return m, nil
	}

	m.stream = msg.stream
	return m, tea.Batch(m.nextSend(), receive(m.stream, m.callSeq, m.elapsed))
}

// nextSend issues the next thing the send queue is waiting to do: one message,
// or — once the queue has drained — the close the user asked for.
//
// Only one send runs at a time. gRPC allows a single sender per stream, and the
// order messages arrive in is part of what a client-streaming call means, so
// two of them may not race even when the transport would tolerate it.
func (m *Model) nextSend() tea.Cmd {
	if m.sending || m.stream == nil {
		return nil
	}

	if len(m.sendQueue) > 0 {
		req := m.sendQueue[0]
		m.sendQueue = m.sendQueue[1:]
		m.sending = true
		return send(m.stream, req, m.callSeq, m.elapsed)
	}

	if m.closeAfterQueue {
		m.closeAfterQueue = false
		m.sending = true
		return closeSending(m.stream, m.callSeq, m.elapsed)
	}
	return nil
}

// endSending closes the stream's sending half once whatever is queued has gone
// out. With no stream open the key does nothing, and says so in the log rather
// than on screen: it is not an error, just a key pressed at the wrong moment.
func (m *Model) endSending() tea.Cmd {
	if m.stream == nil {
		m.logger.Debug("no stream to end")
		return nil
	}
	m.closeAfterQueue = true
	return m.nextSend()
}

// send puts one request message on the stream, rendering it for the log on the
// same goroutine so that the panel receives text and never a protobuf message.
func send(stream grpcclient.Stream, req proto.Message, seq int, at func() time.Duration) tea.Cmd {
	return func() tea.Msg {
		body, format, err := protoschema.MarshalRequest(req)
		if err != nil {
			// The message is going out regardless — it is valid protobuf, or the
			// form would not have built it. Only the rendering failed, and saying
			// so in the log beats dropping the line.
			body, format = "(the request could not be rendered: "+err.Error()+")", protoschema.FormatText
		}

		if err := stream.Send(req); err != nil {
			return streamSentMsg{seq: seq, err: err, at: at()}
		}
		return streamSentMsg{seq: seq, body: body, format: format, at: at()}
	}
}

// closeSending closes the sending half off the Update goroutine.
func closeSending(stream grpcclient.Stream, seq int, at func() time.Duration) tea.Cmd {
	return func() tea.Msg {
		err := stream.CloseSend()
		return streamSentMsg{seq: seq, closedSend: true, err: err, at: at()}
	}
}

// receive reads one message off the stream. Each result issues the next
// receive, which is how a chain of tea.Cmds drains a stream without ever
// blocking Update.
func receive(stream grpcclient.Stream, seq int, at func() time.Duration) tea.Cmd {
	return func() tea.Msg {
		msg, err := stream.Recv()
		switch {
		case errors.Is(err, io.EOF):
			return streamRecvMsg{seq: seq, done: true, at: at()}
		case err != nil:
			st, hasStatus := grpcclient.StatusOf(err)
			return streamRecvMsg{seq: seq, err: err, status: st, hasStatus: hasStatus, at: at()}
		}

		body, format, err := protoschema.Marshal(msg)
		if err != nil {
			return streamRecvMsg{seq: seq, err: err, at: at()}
		}
		return streamRecvMsg{seq: seq, body: body, format: format, at: at()}
	}
}

// streamSent records a request message having gone out, and starts whatever the
// queue holds next.
func (m Model) streamSent(msg streamSentMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.callSeq {
		m.logger.Debug("dropping a stale stream send", zap.Int("seq", msg.seq))
		return m, nil
	}
	m.sending = false

	switch {
	case errors.Is(msg.err, io.EOF):
		// The stream is already over and the reason belongs to the receiving
		// half, which is about to report it. Anything said here would be a
		// second, less informative version of the same event.
		m.sendQueue, m.closeAfterQueue = nil, false
		return m, nil

	case msg.err != nil:
		m.sendQueue, m.closeAfterQueue = nil, false
		m.response.AppendNote(msg.err.Error(), msg.at)
		m.logger.Warn("could not send on the stream", zap.Error(msg.err))

	case msg.closedSend:
		m.response.AppendNote("sending closed", msg.at)

	default:
		m.response.AppendSent(msg.body, msg.format, msg.at)
	}

	m.layout()
	return m, m.nextSend()
}

// streamReceived appends one response message to the log and asks for the next,
// or closes the log when the stream has ended.
func (m Model) streamReceived(msg streamRecvMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.callSeq {
		m.logger.Debug("dropping a stale stream message", zap.Int("seq", msg.seq))
		return m, nil
	}

	if msg.done || msg.err != nil {
		return m.finishStream(msg)
	}

	m.response.AppendReceived(msg.body, msg.format, msg.at)
	m.layout()
	return m, receive(m.stream, m.callSeq, m.elapsed)
}

// finishStream closes the stream out, leaving everything it carried on screen.
func (m Model) finishStream(msg streamRecvMsg) (tea.Model, tea.Cmd) {
	message := ""
	if msg.err != nil {
		message = msg.err.Error()
	}
	m.response.FinishStream(message, msg.status, msg.hasStatus, msg.at)

	m.cancelCall = nil
	m.dropStream()
	m.layout()

	m.logger.Debug("stream ended",
		zap.Bool("ok", msg.done),
		zap.Duration("took", msg.at),
	)
	return m, nil
}

// dropStream lets go of the stream, along with anything queued to go out on it.
func (m *Model) dropStream() {
	if m.stream != nil {
		_ = m.stream.Close()
		m.stream = nil
	}
	m.sendQueue = nil
	m.sending = false
	m.closeAfterQueue = false
}

// elapsed reports how long the current stream has been running. It is passed
// into the stream's commands as a function, so that each one timestamps itself
// when it actually happens rather than when it was created.
func (m Model) elapsed() time.Duration { return m.now().Sub(m.streamStart) }

// startCall issues one unary call, tagged with a sequence number so that a
// result the user has moved on from can be discarded.
func (m *Model) startCall(req panels.SendRequestMsg, md grpcclient.Metadata) tea.Cmd {
	m.abortCall()
	m.dropStream()
	m.callSeq++

	ctx, cancel := context.WithTimeout(m.ctx, m.callTimeout)
	m.cancelCall = cancel

	// Header names, never their values: this log outlives the session and the
	// values are where the bearer token is.
	m.logger.Info("calling method",
		zap.String("target", m.client.Target()),
		zap.String("method", req.Method.FullName),
		zap.Strings("headers", md.Keys()),
	)

	tick := m.response.SetInFlight(req.Method)
	m.layout()
	return tea.Batch(tick, invoke(ctx, cancel, m.client, req, md, m.callSeq))
}

// invoke runs the call off the Update goroutine, decoding the response there
// too so that the panel receives text and never a protobuf message.
func invoke(ctx context.Context, cancel context.CancelFunc, client Invoker, req panels.SendRequestMsg, md grpcclient.Metadata, seq int) tea.Cmd {
	return func() tea.Msg {
		defer cancel()

		resp, err := client.InvokeUnary(ctx, req.Method, req.Request, md)
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

// startConnect opens the connection a profile describes, tagged with a sequence
// number so that a dial the user has moved on from cannot install its client.
func (m *Model) startConnect(msg panels.ProfileSelectedMsg) tea.Cmd {
	if m.dialer == nil {
		m.logger.Warn("cannot switch connection: no dialer configured")
		return nil
	}

	m.retireCall()
	m.connSeq++
	m.state = stateConnecting
	m.err = nil
	m.pendingTarget = msg.Profile.Target

	m.logger.Info("connecting",
		zap.String("profile", msg.Profile.Label()),
		zap.String("target", msg.Profile.Target),
		zap.String("security", msg.Profile.Security.Mode()),
		zap.String("auth", msg.Profile.Auth.Describe()),
	)
	return tea.Batch(m.spinner.Tick, m.connect(msg))
}

// finishConnect installs a new client, or leaves the old connection alone and
// says why the new one could not be opened.
//
// A failed dial deliberately keeps the previous client: the user still has a
// working connection, and taking it away because a second one could not be
// opened would turn a typo in a certificate path into a lost session.
func (m Model) finishConnect(msg clientConnectedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.connSeq {
		m.logger.Debug("dropping stale connection", zap.Int("seq", msg.seq))
		if closer, ok := msg.client.(io.Closer); ok && msg.err == nil {
			_ = closer.Close()
		}
		return m, nil
	}

	m.pendingTarget = ""

	if msg.err != nil {
		m.state = stateFailed
		m.err = fmt.Errorf("connect to %s: %w", msg.profile.Label(), msg.err)
		m.logger.Error("connection failed",
			zap.String("profile", msg.profile.Label()),
			zap.Error(msg.err),
		)
		return m, nil
	}

	m.closeClient()
	m.client = msg.client
	m.ownsClient = true
	m.profiles.SetActive(msg.index)

	// The headers belong to the connection, so switching replaces them rather
	// than carrying the previous profile's `authorization` across to a server
	// that never issued it.
	m.metadata.SetHeaders(msg.profile.Metadata)

	m.services = nil
	m.tree.SetServices(nil)
	m.request.Clear()
	m.response.Clear()
	m.state = stateConnecting

	// History spans connections — it is a record of what you did, not of one
	// server — but the walk through it does not: the form has just been emptied,
	// so `[` starts again from the newest entry.
	m.historyAt = noHistory
	m.layout()

	return m, tea.Batch(m.spinner.Tick, m.discover())
}

// Close releases the connection the model holds, if the model opened it, along
// with any stream still running on it. The client [New] was given belongs to
// whoever passed it in.
//
// bubbletea has no teardown hook, so this is called on the final model that
// Run returns.
func (m Model) Close() error {
	if m.stream != nil {
		_ = m.stream.Close()
	}
	if !m.ownsClient {
		return nil
	}
	if closer, ok := m.client.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// closeClient drops the connection being replaced.
func (m *Model) closeClient() {
	if !m.ownsClient {
		return
	}
	if closer, ok := m.client.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			m.logger.Debug("closing the previous connection", zap.Error(err))
		}
	}
	m.ownsClient = false
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
// than shown. A stream is dropped with it, queued messages and all.
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
	m.dropStream()
	m.callSeq++
	m.logger.Debug("abandoned an in-flight call", zap.Int("seq", retired))
}

// tick feeds a spinner tick to whichever spinner it belongs to. Each
// spinner.Model ignores ticks that are not its own, so both can be fed
// unconditionally.
func (m Model) tick(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// A stream's elapsed time advances on the tick rather than on its messages:
	// a watch that has gone quiet is still running, and a clock frozen beside a
	// turning spinner would read as a hung UI.
	if m.response.Streaming() {
		m.response.SetElapsed(m.elapsed())
	}

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

	m.metadata, cmd = m.metadata.Update(msg)
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
	m.metadata.Blur()
	m.response.Blur()

	switch m.focus {
	case focusRequest:
		m.request.Focus()
	case focusMetadata:
		m.metadata.Focus()
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

// innerSize reports the content area left inside a panel of the given outer
// size, after its border, its padding, and the one-line title.
func innerSize(s lipgloss.Style, width, height int) (w, h int) {
	w = width - s.GetHorizontalBorderSize() - s.GetHorizontalPadding()
	h = height - s.GetVerticalBorderSize() - s.GetVerticalPadding() - 1
	return max(w, 0), max(h, 0)
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

// layoutSizes is the geometry of one frame: a service tree down the left, and
// down the right the request form, the headers, and the response.
type layoutSizes struct {
	treeW     int
	rightW    int
	bodyH     int
	requestH  int
	metadataH int
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

	w, h = innerSize(m.styles.Panel, l.rightW, l.metadataH)
	m.metadata.SetSize(w, h)

	w, h = innerSize(m.styles.Panel, l.rightW, l.responseH)
	m.response.SetSize(w, h)

	m.profiles.SetSize(m.width, m.height)
	m.browser.SetSize(m.width, m.height)
	m.environments.SetSize(m.width, m.height)
	m.variables.SetSize(m.width, m.height)
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

	// Each of the top two panels gets the height it asks for and the response
	// takes the rest: a three-field request with one header should not reserve
	// half the screen between them.
	chrome := m.styles.Panel.GetVerticalBorderSize() + m.styles.Panel.GetVerticalPadding() + 1

	metadataWant := 0
	if m.metadata.Visible() {
		metadataWant = m.metadata.ContentHeight() + chrome
	}

	l.requestH, l.metadataH, l.responseH = splitColumn(l.bodyH,
		m.request.ContentHeight()+chrome, metadataWant, minPanelHeight)
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

// splitHeight divides a column in two, giving the top panel the height it wants
// within what is left after the bottom one's minimum. A column too short to
// satisfy both minimums is halved instead, because a panel of zero rows renders
// as a broken box rather than as nothing.
func splitHeight(total, want, minEach int) (top, bottom int) {
	if total < 2*minEach {
		top = total / 2
		return top, total - top
	}
	top = min(max(want, minEach), total-minEach)
	return top, total - top
}

// splitColumn divides the right-hand column three ways: the form and the
// headers each get what they ask for, and the response takes the rest.
//
// The headers are settled first and against the *whole* column, so that a form
// tall enough to fill the screen cannot squeeze them out — a header list is
// small, bounded, and the thing you are most likely to be changing when a call
// keeps coming back Unauthenticated.
func splitColumn(total, wantTop, wantMiddle, minEach int) (top, middle, bottom int) {
	// A middle panel that wants nothing is not on screen, and the column is the
	// two-panel one it has always been.
	if wantMiddle <= 0 {
		top, bottom = splitHeight(total, wantTop, minEach)
		return top, 0, bottom
	}

	if total < 3*minEach {
		// Too short for three panels at their minimum. Thirds keep all three
		// visible, which beats one of them rendering as a broken box.
		top = total / 3
		middle = total / 3
		return top, middle, total - top - middle
	}

	middle = min(max(wantMiddle, minEach), total-2*minEach)
	top, bottom = splitHeight(total-middle, wantTop, minEach)
	return top, middle, bottom
}
