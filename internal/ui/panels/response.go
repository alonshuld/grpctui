package panels

import (
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// responseState is what the panel is currently showing.
type responseState int

const (
	responseEmpty responseState = iota
	responseInFlight
	responseStreaming
	responseOK
	responseFailed
)

// responseView is which rendering of the answer the panel is showing.
//
// They are three views of one result rather than three panels: the body, the
// bytes it was encoded from, and what changed since the last call to the same
// method. Switching between them keeps the result — pressing w twice puts you
// back where you were, with the same response still on screen.
type responseView int

const (
	// viewDecoded is the JSON body, which is what the panel has always shown.
	viewDecoded responseView = iota

	// viewRaw is the protobuf encoding: a field-by-field listing of what is on
	// the wire, and a hex dump underneath. It is the view for the case the
	// decoded one cannot answer — a field the server set that this client's
	// descriptor does not have, a string that is not the string you expected.
	viewRaw

	// viewDiff is what changed between this response and the previous one for
	// the same method, which is how you tell "the bug is back" from "the bug
	// moved".
	viewDiff
)

// Result is one finished unary call, as the panel shows it.
//
// It is a struct rather than five parameters because the last three arrived
// together in v0.8 and are all optional: a client that measures nothing, a
// message that could not be re-encoded, and a method called for the first time
// each leave one of them empty, and the panel copes with all three.
type Result struct {
	// Body is the rendered response and Format how it was rendered.
	Body   string
	Format protoschema.Format

	// Wire is the response's protobuf encoding, for the raw view. Empty means
	// there is nothing to show there — see [protoschema.Wire].
	Wire []byte

	// Previous is the body of the last response to the same method, for the
	// diff view. Empty means this is the first.
	Previous string

	// Took is the wall time around the call, and Timing gRPC's own breakdown of
	// it. An unmeasured Timing leaves the status line saying only the total.
	Took   time.Duration
	Timing grpcclient.Timing

	// Notes are the renderers' glosses on individual lines of Body — "3 minutes
	// ago" beside a timestamp. They arrive rendered rather than being computed
	// here because the message they were read from lives a layer up: see
	// internal/render, which annotates rather than rewriting, so Body stays
	// exactly what the server sent.
	Notes []render.Annotation
}

// Response shows the result of the last call: the decoded JSON body, or the
// gRPC status that came back instead.
//
// A failed call is not an error screen. A server answering NotFound is the
// service working correctly, and the panel says so with the same weight it
// gives a success — which is why the status line, not a modal, carries the
// outcome.
type Response struct {
	keys   keys.KeyMap
	styles styles.Styles

	viewport viewport.Model
	spinner  spinner.Model

	state   responseState
	method  grpcclient.Method
	status  grpcclient.CallStatus
	hasCode bool

	// body is the text under the status line, held unwrapped so that a resize
	// can lay it out again. rendered is the same text with its JSON highlighted,
	// which depends on the body and not on the panel's size — so it is coloured
	// once, when the response arrives, rather than on every resize.
	body     string
	rendered string
	format   protoschema.Format
	duration time.Duration

	// notes are the renderers' glosses on the body's lines, kept so that a
	// theme switch can lay them out again.
	notes []render.Annotation

	// view is which of the three renderings is on screen, wire the bytes the
	// raw one shows, previous the body the diff one compares against, and
	// timing where the call's time went.
	//
	// The view deliberately survives a new response: somebody watching a field
	// change across three calls has said, by pressing D, that the diff is what
	// they want to see, and putting them back in the decoded body on every send
	// would make the feature unusable for the thing it is for.
	view     responseView
	wire     []byte
	previous string
	timing   grpcclient.Timing

	// entries is the stream log, oldest first, and dropped counts the entries
	// that fell off the front of it. sent and received count every message the
	// stream carried, dropped ones included — the counts describe the call, not
	// the window onto it.
	entries  []streamEntry
	dropped  int
	sent     int
	received int

	width   int
	height  int
	focused bool
}

// horizontalStep is how far one scroll-left/right keystroke pans the body. It
// is a few columns rather than one so that panning a wide line is a couple of
// keystrokes instead of a couple of dozen.
const horizontalStep = 8

// NewResponse builds an empty response panel.
func NewResponse(km keys.KeyMap, st styles.Styles) Response {
	vp := viewport.New(0, 0)
	vp.KeyMap = viewportKeys(km)
	vp.SetHorizontalStep(horizontalStep)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(st.Palette.Primary)

	return Response{keys: km, styles: st, viewport: vp, spinner: sp}
}

// viewportKeys maps grpctui's keymap onto the viewport's own.
//
// bubbles/viewport ships a default keymap of its own (u/d/b/f, h/l, arrows).
// Left as-is it would put scroll bindings outside the single keymap: absent
// from the `?` help, unreachable by the v0.9 remapping feature, and — for h and
// l — bound to something other than the expand/collapse they mean everywhere
// else. Bindings with no grpctui equivalent are left disabled rather than
// silently keeping their defaults.
//
// Left and Right must be bound to something: the viewport truncates any line
// wider than it is, so without them the tail of a long JSON line is not merely
// off screen but unreachable.
func viewportKeys(km keys.KeyMap) viewport.KeyMap {
	return viewport.KeyMap{
		Up:       km.Up,
		Down:     km.Down,
		PageUp:   km.PageUp,
		PageDown: km.PageDown,
		Left:     km.ScrollLeft,
		Right:    km.ScrollRight,
	}
}

// SetMethod tells the panel which method is selected, so an empty panel can say
// what a response would look like. It clears any previous result.
func (r *Response) SetMethod(m grpcclient.Method) {
	r.method = m
	r.Clear()
}

// Clear drops the last result, keeping the selected method.
func (r *Response) Clear() {
	r.state = responseEmpty
	r.body = ""
	r.rendered = ""
	r.format = protoschema.FormatJSON
	r.status = grpcclient.CallStatus{}
	r.hasCode = false
	r.duration = 0
	r.wire = nil
	r.previous = ""
	r.timing = grpcclient.Timing{}
	r.entries = nil
	r.dropped = 0
	r.sent = 0
	r.received = 0
	r.viewport.SetContent("")
	r.viewport.GotoTop()
	r.viewport.SetXOffset(0)
}

// SetInFlight moves the panel into its loading state and returns the command
// that animates the spinner.
func (r *Response) SetInFlight(m grpcclient.Method) tea.Cmd {
	r.Clear()
	r.method = m
	r.state = responseInFlight
	return r.spinner.Tick
}

// SetStreaming opens a stream log for m and returns the command that animates
// the watching indicator. Everything the stream carries is appended to the log
// from here until [Response.FinishStream].
func (r *Response) SetStreaming(m grpcclient.Method) tea.Cmd {
	r.Clear()
	r.method = m
	r.state = responseStreaming
	return r.spinner.Tick
}

// InFlight reports whether a unary call is running.
func (r Response) InFlight() bool { return r.state == responseInFlight }

// Streaming reports whether a stream is open.
func (r Response) Streaming() bool { return r.state == responseStreaming }

// Active reports whether there is a call to cancel: a unary one waiting for its
// answer, or a stream still open.
func (r Response) Active() bool { return r.InFlight() || r.Streaming() }

// Sent reports how many request messages the stream has carried, which the
// status bar puts beside the elapsed time.
func (r Response) Sent() int { return r.sent }

// Received reports how many response messages the stream has carried.
func (r Response) Received() int { return r.received }

// Elapsed reports how long the current — or last — stream has been running.
func (r Response) Elapsed() time.Duration { return r.duration }

// SetSuccess shows a finished call. [Result.Format] is how the body was
// rendered: anything but [protoschema.FormatJSON] is called out on the status
// line, because a user who asked for JSON and got something else is owed an
// explanation rather than left to wonder.
func (r *Response) SetSuccess(result Result) {
	r.Clear()
	r.state = responseOK
	r.body = result.Body
	r.format = result.Format
	r.duration = result.Took
	r.wire = result.Wire
	r.previous = result.Previous
	r.timing = result.Timing
	r.notes = result.Notes

	// Only JSON is highlighted. protobuf's text format is a different language,
	// and colouring it by JSON's rules would put emphasis in the wrong places.
	r.rendered = result.Body
	if result.Format == protoschema.FormatJSON {
		r.rendered = annotate(r.styles.HighlightJSON(result.Body), result.Notes, r.styles)
	}

	r.setBody()
	r.viewport.GotoTop()
}

// ToggleRaw swaps between the decoded body and the bytes behind it.
func (r *Response) ToggleRaw() { r.setView(viewRaw) }

// ToggleDiff swaps between the decoded body and what changed since the previous
// response to the same method.
func (r *Response) ToggleDiff() { r.setView(viewDiff) }

// setView switches to a rendering, or back to the decoded body when it is
// already showing.
//
// The viewport goes back to the top on a switch. The three renderings have
// nothing to do with each other line for line, so carrying an offset across
// would land the user in the middle of something they have not read.
func (r *Response) setView(view responseView) {
	if r.view == view {
		view = viewDecoded
	}
	r.view = view
	r.setBody()
	r.viewport.GotoTop()
	r.viewport.SetXOffset(0)
}

// Wire is the response's protobuf encoding, or nil when there is none.
func (r Response) Wire() []byte { return r.wire }

// Timing is where the last call's time went.
func (r Response) Timing() grpcclient.Timing { return r.timing }

// SetFailure shows a failed call. status is the gRPC status the call came back
// with, if it reached the wire at all; message is the raw error text, shown
// when it did not.
func (r *Response) SetFailure(message string, status grpcclient.CallStatus, hasStatus bool, took time.Duration) {
	r.Clear()
	r.state = responseFailed
	r.status = status
	r.hasCode = hasStatus
	r.duration = took

	// With a status it is the server's message that belongs on screen; without
	// one the call never reached the wire, and the raw error is all there is.
	//
	// A status may carry no message at all — grpc-go lets a server raise a bare
	// code — and then the raw error is again the only text there is. Preferring
	// an empty status message over it would leave the panel showing a code above
	// a blank body.
	r.body = message
	if hasStatus && status.Message != "" {
		r.body = status.Message
	}
	r.rendered = r.body
	r.setBody()
	r.viewport.GotoTop()
}

// setBody lays the body out for the current width.
//
// A failure's text is wrapped; a JSON body is not — it arrives already
// indented, and re-flowing it would destroy that. Every width change goes
// through here, or a resize leaves the old wrap in place and the viewport
// silently truncates whatever now runs past the edge.
func (r *Response) setBody() {
	switch {
	case r.view == viewRaw:
		r.viewport.SetContent(r.rawView())
	case r.view == viewDiff:
		r.viewport.SetContent(r.diffView())
	case len(r.entries) > 0:
		// A stream log outlives the state that built it: a finished stream still
		// shows every message it carried, so the log wins over the single-body
		// rendering below whenever there is one.
		r.viewport.SetContent(r.streamLog())
	case r.state == responseFailed:
		r.viewport.SetContent(styles.Wrap(r.body, r.viewport.Width))
	default:
		r.viewport.SetContent(r.rendered)
	}
}

// SetSize sets the panel's inner content area. One line is reserved for the
// status line above the body.
func (r *Response) SetSize(width, height int) {
	r.width = width
	r.height = height
	r.viewport.Width = width
	r.viewport.Height = max(height-2, 0)
	r.setBody()
}

// Focus gives the panel focus.
func (r *Response) Focus() { r.focused = true }

// Blur removes focus from the panel.
func (r *Response) Blur() { r.focused = false }

// Focused reports whether the panel has focus.
func (r Response) Focused() bool { return r.focused }

// Update scrolls the body when focused, and keeps the spinner turning while a
// call is in flight — which it must do whether or not the panel has focus.
func (r Response) Update(msg tea.Msg) (Response, tea.Cmd) {
	if tick, ok := msg.(spinner.TickMsg); ok {
		if !r.Active() {
			return r, nil
		}
		var cmd tea.Cmd
		r.spinner, cmd = r.spinner.Update(tick)
		return r, cmd
	}

	if !r.focused {
		return r, nil
	}

	// Top and bottom are handled here rather than through the viewport's own
	// keymap, which has no binding for either. On a stream log they are not a
	// convenience: the panel follows the tail only while the user is at it, so
	// G is how you start following again after scrolling up to read something.
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(keyMsg, r.keys.Top):
			r.viewport.GotoTop()
			return r, nil
		case key.Matches(keyMsg, r.keys.Bottom):
			r.viewport.GotoBottom()
			return r, nil
		}
	}

	var cmd tea.Cmd
	r.viewport, cmd = r.viewport.Update(msg)
	return r, cmd
}
