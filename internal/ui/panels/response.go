package panels

import (
	"strings"
	"time"

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
	responseOK
	responseFailed
)

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

// InFlight reports whether a call is running.
func (r Response) InFlight() bool { return r.state == responseInFlight }

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
	if r.state == responseFailed {
		r.viewport.SetContent(styles.Wrap(r.body, r.viewport.Width))
		return
	}
	r.viewport.SetContent(r.rendered)
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
		if r.state != responseInFlight {
			return r, nil
		}
		var cmd tea.Cmd
		r.spinner, cmd = r.spinner.Update(tick)
		return r, cmd
	}

	if !r.focused {
		return r, nil
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
	case responseOK, responseFailed:
		return strings.Join([]string{r.statusLine(), "", r.viewport.View()}, "\n")
	default:
		return r.emptyView()
	}
}

func (r Response) emptyView() string {
	if r.method.OutputType == "" {
		return r.styles.Muted.Render("Send a request to see the response.")
	}
	return strings.Join([]string{
		r.styles.Label.Render(r.method.OutputType),
		"",
		r.styles.Muted.Render("Press ctrl+s to send the request."),
	}, "\n")
}

// statusLine renders the one-line verdict above the body.
//
// Only a successful call is timed. A failure's duration says more about where
// it failed than about the service — and a per-call latency breakdown is a v0.8
// feature, not something to approximate here.
func (r Response) statusLine() string {
	switch {
	case r.state == responseOK:
		line := r.styles.StatusOK.Render("OK") + "  " + r.styles.Muted.Render(formatDuration(r.duration))
		if r.format != protoschema.FormatJSON {
			line += "  " + r.styles.Muted.Render("· protobuf text (unknown Any type)")
		}
		return styles.Truncate(line, r.width)
	case r.hasCode:
		return styles.Truncate(r.styles.StatusError.Render(r.status.CodeName()), r.width)
	default:
		return styles.Truncate(r.styles.StatusError.Render("Call failed"), r.width)
	}
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
