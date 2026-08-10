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
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/proxy"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
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

	// export shows the request in the form as a grpcurl command, and traffic
	// the calls the passive proxy has seen. Both are modal for the same reason
	// the four above are: they are about the session rather than about the
	// request in front of you, and neither belongs in the tab cycle.
	export  panels.Export
	traffic panels.Traffic

	// themes is the palette switcher, and is modal for the same reason again.
	// It owns the list of themes the way variables owns the bindings, so that
	// there is one copy of them rather than one the panel draws and another the
	// model switches between.
	themes panels.Themes

	// history is every request sent, newest first. It lives on the model rather
	// than behind the browser because [ and ] walk it with the browser closed.
	history requests.History

	// responses is the last body each method answered with, which is what the
	// diff view compares the next one against. It is memory only and never
	// written down — a response body holds whatever the server chose to put in
	// it, and grpctui's standing rule about surfaces that outlive a call applies
	// to answers as much as to credentials.
	//
	// It is capped: a long session against a large schema would otherwise keep a
	// body per method for the life of the process. An evicted method simply
	// reports its next response as the first one.
	responses map[string]string

	// events is the passive proxy's stream of what has gone past, and dropped
	// asks it how much it could not hand over. Both are nil unless grpctui was
	// started with --proxy.
	events  <-chan proxy.Event
	dropped func() int

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

	// renderers gloss values inside a response — a timestamp with how long ago
	// it was. The zero registry annotates nothing, which is what a model built
	// without [WithRenderers] does.
	renderers render.Registry

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
		export:           panels.NewExport(km, st),
		traffic:          panels.NewTraffic(km, st),
		themes:           panels.NewThemes(km, st),
		responses:        make(map[string]string),
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
	m.traffic.SetClock(m.now)

	// The form resolves against whatever is bound, including nothing: a model
	// built with no environments still has to expand a reference to a variable
	// the user binds later with v.
	m.request.SetResolver(m.bindings())

	m.syncFocus()
	return m
}

// noHistory is [Model.historyAt] when the form does not hold a recalled
// request.
const noHistory = -1

// Init starts discovery, the loading spinner and — when there is one — the read
// loop over the proxy's events.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, m.discover()}
	if m.events != nil {
		cmds = append(cmds, watchTraffic(m.events))
	}
	return tea.Batch(cmds...)
}
