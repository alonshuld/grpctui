// response_view.go renders a response. The chosen view — decoded, raw bytes or
// a diff against the previous answer — survives the next call, so rendering is
// a function of the view field rather than of what just arrived.

package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/alonshuld/grpctui/internal/diff"
	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// diffContext is how many unchanged lines are kept either side of a change. Two
// is enough to see which object a changed field belongs to without turning the
// diff back into the whole body.
const diffContext = 2

// annotate appends each renderer's gloss to the line it belongs beside.
//
// The gloss sits after the value, behind a marker, and is dimmed: it is
// commentary on the response and must never be mistaken for part of it. That is
// the same reason internal/render annotates rather than rewriting — a reader
// has to be able to tell at a glance what the server said from what grpctui
// worked out about it.
//
// A note whose line is not in the text is dropped rather than clamped. It can
// only happen if the body and the notes came from different messages, and
// hanging "3 minutes ago" off an unrelated field would be worse than saying
// nothing.
func annotate(text string, notes []render.Annotation, st styles.Styles) string {
	if len(notes) == 0 {
		return text
	}

	lines := strings.Split(text, "\n")
	for _, note := range notes {
		i := note.Line - 1
		if i < 0 || i >= len(lines) {
			continue
		}
		lines[i] += st.Hint.Render("  ← " + note.Text)
	}
	return strings.Join(lines, "\n")
}

// rawView renders the response's protobuf encoding: the fields as a decoder
// without a schema sees them, then a hex dump of the same bytes.
//
// Both halves earn their place. The field listing answers "what did the server
// actually put on the wire", which is the question that survives a descriptor
// disagreeing with reality; the hex dump answers "what exactly", which is the
// one you fall back to when the listing itself looks wrong.
func (r Response) rawView() string {
	if len(r.wire) == 0 {
		return r.styles.Muted.Render("There are no bytes to show for this response.")
	}

	lines := []string{r.styles.Muted.Render(
		protoschema.ByteCount(len(r.wire)) + " · as re-encoded from the decoded message")}

	fields, err := protoschema.WireFields(r.wire)
	for _, f := range fields {
		lines = append(lines, styles.Truncate(fmt.Sprintf("%s  %s  %s",
			r.styles.WireField.Render(fmt.Sprintf("%3d", f.Number)),
			r.styles.Muted.Render(padCell(f.Type, wireTypeWidth)),
			f.Value), r.width))
	}
	if err != nil {
		// The fields read so far are still above; this says where reading
		// stopped, which on a malformed body is the interesting part.
		lines = append(lines, r.styles.FieldError.Render(err.Error()))
	}

	dump := protoschema.Hexdump(r.wire, r.bytesPerLine())
	return strings.Join(append(lines, "", dump), "\n")
}

// wireTypeWidth is the width of the wire-type column, enough for the longest
// name ("32-bit") so the values beside it line up.
const wireTypeWidth = 7

// bytesPerLine is how many bytes the hex dump puts on a row: as many as fit,
// halving down from sixteen so that the columns stay a power of two and a byte
// offset can still be read off the row.
func (r Response) bytesPerLine() int {
	// Each byte costs three cells of hex and one of ASCII; the rest is the
	// offset column, the two gaps and the ASCII gutter's bars.
	const chrome = 13

	for _, n := range []int{16, 8, 4} {
		if chrome+4*n <= r.width {
			return n
		}
	}
	return 4
}

// diffView renders what changed since the previous response to this method.
func (r Response) diffView() string {
	if r.body == "" {
		return r.styles.Muted.Render("There is no response to compare.")
	}
	if r.previous == "" {
		return r.styles.Muted.Render(
			"This is the first response from " + r.method.Name + ". Send it again to compare.")
	}

	lines, skips := diff.Unified(r.previous, r.body, diffContext)

	// The skips are indexed against the returned lines, so they are consumed in
	// order as the lines are walked rather than searched for per line.
	out := make([]string, 0, len(lines)+len(skips)+1)
	next := 0
	for i, line := range lines {
		for next < len(skips) && skips[next].At == i {
			out = append(out, r.styles.DiffSkipped.Render(fmt.Sprintf("  … %d unchanged %s",
				skips[next].Lines, plural(skips[next].Lines, "line"))))
			next++
		}
		out = append(out, r.diffLine(line))
	}
	for ; next < len(skips); next++ {
		out = append(out, r.styles.DiffSkipped.Render(fmt.Sprintf("  … %d unchanged %s",
			skips[next].Lines, plural(skips[next].Lines, "line"))))
	}

	if len(out) == 0 {
		return r.styles.Muted.Render("This response is identical to the previous one.")
	}
	return strings.Join(out, "\n")
}

// diffLine renders one line of the diff, with the marker in the gutter that
// makes it readable without colour.
func (r Response) diffLine(line diff.Line) string {
	switch line.Op {
	case diff.Insert:
		return r.styles.DiffAdded.Render("+ " + line.Text)
	case diff.Delete:
		return r.styles.DiffRemoved.Render("- " + line.Text)
	default:
		return r.styles.Muted.Render("  " + line.Text)
	}
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
		line := r.styles.StatusOK.Render("OK") + "  " + r.styles.Muted.Render(r.durationText())
		if r.format != protoschema.FormatJSON {
			line += "  " + r.styles.Muted.Render("· protobuf text (unknown Any type)")
		}
		return styles.Truncate(line+r.viewNote(), r.width)

	case r.hasCode:
		return styles.Truncate(
			r.styles.StatusError.Render(r.status.CodeName())+"  "+
				r.styles.Muted.Render(r.durationText())+r.viewNote(), r.width)

	default:
		return styles.Truncate(
			r.styles.StatusError.Render("Call failed")+"  "+
				r.styles.Muted.Render(r.durationText())+r.viewNote(), r.width)
	}
}

// viewNote says which rendering is on screen, when it is not the ordinary one.
//
// Without it a raw or diff view is a panel showing something that is not the
// response, with nothing to say so — and the two are easy to leave switched on
// and then be confused by several calls later.
func (r Response) viewNote() string {
	switch r.view {
	case viewRaw:
		return "  " + r.styles.WireField.Render("· raw bytes")
	case viewDiff:
		return "  " + r.styles.WireField.Render("· diff vs previous")
	default:
		return ""
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

// durationText renders what to say about a finished call's duration: a stream
// reports its length and how much it carried, a unary call its duration and —
// when gRPC measured one — where that duration went.
func (r Response) durationText() string {
	if len(r.entries) > 0 {
		return r.counts() + "  " + formatDuration(r.duration)
	}
	if !r.timing.Measured {
		return formatDuration(r.duration)
	}

	// Total first, breakdown after: the parts are what you read when the total
	// surprises you, so they are the half a narrow panel may truncate away.
	return formatDuration(r.duration) + "  · " + strings.Join([]string{
		"connect " + formatDuration(r.timing.Connect),
		"first byte " + formatDuration(r.timing.FirstByte),
		"total " + formatDuration(r.timing.Total),
		protoschema.ByteCount(r.timing.RequestBytes) + " out",
		protoschema.ByteCount(r.timing.ResponseBytes) + " in",
	}, " · ")
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
