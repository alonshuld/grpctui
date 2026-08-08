package panels

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
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

// streamKind is what a line of the stream log records.
type streamKind int

const (
	// streamSent and streamReceived are the two directions a message travels.
	// They are told apart by colour and by the arrow in the gutter, because
	// colour alone is unreadable on a monochrome terminal — and in a golden
	// file.
	streamSent streamKind = iota
	streamReceived

	// streamNote marks something that happened to the stream itself rather than
	// a message on it: the sending half closing, the call ending.
	streamNote
)

// maxStreamEntries is how many lines of the log the panel keeps.
//
// A watch stream is unbounded by design — that is the point of watching one —
// so the log has to be bounded somewhere, or a session left open overnight ends
// as an out-of-memory. Dropping the oldest keeps what a user actually looks at,
// which is the tail; the panel says so rather than quietly losing them.
const maxStreamEntries = 500

// streamEntry is one line of the stream log.
type streamEntry struct {
	kind streamKind

	// body is the rendered message, and rendered the same text with its JSON
	// highlighted. As for a unary response, the highlighting depends on the text
	// and not on the panel's size, so it is done once, on arrival.
	body     string
	rendered string
	format   protoschema.Format

	// at is how long into the stream this happened, which is the only timing a
	// stream can usefully show: a per-message latency needs the request it
	// answers, and on a server stream there is no such pairing.
	at time.Duration

	// index numbers the entry within its own direction, so the log reads as two
	// interleaved sequences rather than one confusing one.
	index int
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

// AppendSent records a request message going out.
func (r *Response) AppendSent(body string, format protoschema.Format, at time.Duration) {
	r.sent++
	r.append(streamEntry{kind: streamSent, body: body, format: format, at: at, index: r.sent})
}

// AppendReceived records a response message arriving.
func (r *Response) AppendReceived(body string, format protoschema.Format, at time.Duration) {
	r.received++
	r.append(streamEntry{kind: streamReceived, body: body, format: format, at: at, index: r.received})
}

// AppendNote records something that happened to the stream itself.
func (r *Response) AppendNote(text string, at time.Duration) {
	r.append(streamEntry{kind: streamNote, body: text, rendered: text, at: at})
}

// FinishStream closes the log and puts the call's verdict on the status line,
// keeping everything the stream carried on screen.
//
// message is the failure's text, shown when the stream ended badly and had no
// status of its own to explain it — the same distinction [Response.SetFailure]
// makes for a unary call.
func (r *Response) FinishStream(message string, status grpcclient.CallStatus, hasStatus bool, took time.Duration) {
	if r.state != responseStreaming {
		return
	}

	r.duration = took
	r.status = status
	r.hasCode = hasStatus

	switch {
	case hasStatus && status.Message != "":
		r.state = responseFailed
		r.AppendNote(status.CodeName()+": "+status.Message, took)
	case hasStatus:
		r.state = responseFailed
		r.AppendNote(status.CodeName(), took)
	case message != "":
		r.state = responseFailed
		r.AppendNote(message, took)
	default:
		r.state = responseOK
		r.AppendNote("stream finished", took)
	}
}

// SetElapsed updates how long the open stream has been running. The root model
// owns the clock, so that a frame of the UI is a function of its messages and
// nothing else — which is what makes a golden file of one reproducible.
func (r *Response) SetElapsed(d time.Duration) {
	if r.state == responseStreaming {
		r.duration = d
	}
}

// append adds one line to the stream log, dropping the oldest once the log is
// full, and keeps the view pinned to the tail unless the user has scrolled
// away from it.
func (r *Response) append(entry streamEntry) {
	entry.rendered = entry.body
	if entry.kind != streamNote && entry.format == protoschema.FormatJSON {
		entry.rendered = r.styles.HighlightJSON(entry.body)
	}

	// Following the tail is what a stream panel is for, but a user who has
	// scrolled up is reading something and must not be yanked away from it.
	follow := r.viewport.AtBottom()

	// The clock catches up with whatever just happened. Waiting for the next
	// tick to do it would leave the status line reading 0s beside a message
	// stamped a second in.
	if r.state == responseStreaming {
		r.duration = max(r.duration, entry.at)
	}

	r.entries = append(r.entries, entry)
	if len(r.entries) > maxStreamEntries {
		r.dropped += len(r.entries) - maxStreamEntries
		r.entries = r.entries[len(r.entries)-maxStreamEntries:]
	}

	r.setBody()
	if follow {
		r.viewport.GotoBottom()
	}
}

// LastMessage returns the most recent message the panel is showing and how it
// was rendered: the response of a unary call, or the last message received on a
// stream. It reports false when there is nothing to read a value out of.
//
// It is what a capture reads from. Going back to the rendered text rather than
// to the message it came from means nothing has to be kept alive between a call
// finishing and the user deciding, several keystrokes later, that they want the
// id out of it.
func (r Response) LastMessage() (string, protoschema.Format, bool) {
	for _, e := range slices.Backward(r.entries) {
		if e.kind == streamReceived {
			return e.body, e.format, true
		}
	}

	if r.state != responseOK || r.body == "" {
		return "", r.format, false
	}
	return r.body, r.format, true
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

// StreamSummary reports what an open stream has carried and for how long, for
// the status bar. It reports false when no stream is running.
func (r Response) StreamSummary() (string, bool) {
	if r.state != responseStreaming {
		return "", false
	}
	return r.counts() + " " + formatDuration(r.duration), true
}

// SetSuccess shows a decoded response body. format is how that body was
// rendered: anything but [protoschema.FormatJSON] is called out on the status
// line, because a user who asked for JSON and got something else is owed an
// explanation rather than left to wonder.
func (r *Response) SetSuccess(body string, format protoschema.Format, took time.Duration) {
	r.Clear()
	r.state = responseOK
	r.body = body
	r.format = format
	r.duration = took

	// Only JSON is highlighted. protobuf's text format is a different language,
	// and colouring it by JSON's rules would put emphasis in the wrong places.
	r.rendered = body
	if format == protoschema.FormatJSON {
		r.rendered = r.styles.HighlightJSON(body)
	}

	r.setBody()
	r.viewport.GotoTop()
}

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

// streamLog renders the whole log: a header line per entry saying which way the
// message went and when, and the message itself underneath.
func (r Response) streamLog() string {
	blocks := make([]string, 0, len(r.entries)+1)

	if r.dropped > 0 {
		blocks = append(blocks, r.styles.Muted.Render(
			fmt.Sprintf("… %d earlier %s dropped", r.dropped, plural(r.dropped, "message"))))
	}
	for _, e := range r.entries {
		blocks = append(blocks, r.entryBlock(e))
	}
	return strings.Join(blocks, "\n\n")
}

// entryBlock renders one entry of the stream log.
func (r Response) entryBlock(e streamEntry) string {
	at := r.styles.Muted.Render(formatDuration(e.at))

	if e.kind == streamNote {
		return styles.Truncate(r.styles.Muted.Render("· "+e.body)+"  "+at, r.width)
	}

	style, arrow := r.styles.StreamReceived, "←"
	if e.kind == streamSent {
		style, arrow = r.styles.StreamSent, "→"
	}

	header := style.Render(fmt.Sprintf("%s %d", arrow, e.index)) + "  " + at
	if e.format != protoschema.FormatJSON {
		header += "  " + r.styles.Muted.Render("· protobuf text (unknown Any type)")
	}
	return styles.Truncate(header, r.width) + "\n" + e.rendered
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

// View renders the panel body.
func (r Response) View() string {
	switch r.state {
	case responseInFlight:
		return r.spinner.View() + " " + r.styles.Muted.Render("Calling "+r.method.Name+"…")
	case responseStreaming, responseOK, responseFailed:
		return strings.Join([]string{r.statusLine(), "", r.viewport.View()}, "\n")
	default:
		return r.emptyView()
	}
}

func (r Response) emptyView() string {
	if r.method.OutputType == "" {
		return r.styles.Muted.Render("Send a request to see the response.")
	}

	hint := "Press ctrl+s to send the request."
	if r.method.Kind() != grpcclient.KindUnary {
		hint = "Press ctrl+s to open the stream."
	}
	return strings.Join([]string{
		r.styles.Label.Render(r.method.OutputType),
		"",
		r.styles.Muted.Render(hint),
	}, "\n")
}

// statusLine renders the one-line verdict above the body.
//
// Only a successful call is timed. A failure's duration says more about where
// it failed than about the service — and a per-call latency breakdown is a v0.8
// feature, not something to approximate here. A stream is the exception: it is
// timed whatever it ends as, because how long it ran is most of what happened.
func (r Response) statusLine() string {
	switch {
	case r.state == responseStreaming:
		line := r.spinner.View() + " " + r.styles.StreamReceived.Render(r.watchLabel()) +
			"  " + r.styles.Muted.Render(r.counts()+"  "+formatDuration(r.duration))
		return styles.Truncate(line, r.width)

	case r.state == responseOK:
		line := r.styles.StatusOK.Render("OK") + "  " + r.styles.Muted.Render(r.timing())
		if r.format != protoschema.FormatJSON {
			line += "  " + r.styles.Muted.Render("· protobuf text (unknown Any type)")
		}
		return styles.Truncate(line, r.width)

	case r.hasCode:
		return styles.Truncate(
			r.styles.StatusError.Render(r.status.CodeName())+"  "+r.styles.Muted.Render(r.timing()), r.width)

	default:
		return styles.Truncate(
			r.styles.StatusError.Render("Call failed")+"  "+r.styles.Muted.Render(r.timing()), r.width)
	}
}

// watchLabel names what the open stream is doing. A server-streaming call has
// nothing left to say once its one request is out, so it is *watching*; the
// other two are still a conversation.
func (r Response) watchLabel() string {
	if r.method.Kind() == grpcclient.KindServerStreaming {
		return "Watching"
	}
	return "Streaming"
}

// timing renders what to say about a finished call's duration: a stream reports
// its length and how much it carried, a unary call just its duration.
func (r Response) timing() string {
	if len(r.entries) == 0 {
		return formatDuration(r.duration)
	}
	return r.counts() + "  " + formatDuration(r.duration)
}

// counts renders the two message tallies, in the same arrows the log uses.
func (r Response) counts() string {
	return fmt.Sprintf("→%d ←%d", r.sent, r.received)
}

// formatDuration renders a call duration at a resolution a human cares about.
func formatDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return d.Round(time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	default:
		return d.Round(time.Microsecond).String()
	}
}
