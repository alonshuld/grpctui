// response_stream.go holds the streaming half of the response panel: the log
// of what went each way, bounded, and the summary of a stream that has ended.

package panels

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/styles"
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

	// notes are the renderers' glosses on this message's lines, kept beside the
	// text they annotate so that a theme switch can colour them again.
	notes []render.Annotation

	// at is how long into the stream this happened, which is the only timing a
	// stream can usefully show: a per-message latency needs the request it
	// answers, and on a server stream there is no such pairing.
	at time.Duration

	// index numbers the entry within its own direction, so the log reads as two
	// interleaved sequences rather than one confusing one.
	index int
}

// AppendSent records a request message going out.
func (r *Response) AppendSent(body string, format protoschema.Format, at time.Duration) {
	r.sent++
	r.append(streamEntry{kind: streamSent, body: body, format: format, at: at, index: r.sent})
}

// AppendReceived records a response message arriving, with the renderers'
// glosses on its lines.
func (r *Response) AppendReceived(body string, format protoschema.Format, at time.Duration, notes []render.Annotation) {
	r.received++
	r.append(streamEntry{
		kind:   streamReceived,
		body:   body,
		format: format,
		notes:  notes,
		at:     at,
		index:  r.received,
	})
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
		entry.rendered = annotate(r.styles.HighlightJSON(entry.body), entry.notes, r.styles)
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

// StreamSummary reports what an open stream has carried and for how long, for
// the status bar. It reports false when no stream is running.
func (r Response) StreamSummary() (string, bool) {
	if r.state != responseStreaming {
		return "", false
	}
	return r.counts() + " " + formatDuration(r.duration), true
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
