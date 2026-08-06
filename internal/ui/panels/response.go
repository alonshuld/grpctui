package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/grpcclient"
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

	state    responseState
	method   grpcclient.Method
	body     string
	status   grpcclient.CallStatus
	hasCode  bool
	failure  string
	duration time.Duration

	width   int
	height  int
	focused bool
}

// NewResponse builds an empty response panel.
func NewResponse(km keys.KeyMap, st styles.Styles) Response {
	vp := viewport.New(0, 0)
	vp.KeyMap = viewportKeys(km)

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
func viewportKeys(km keys.KeyMap) viewport.KeyMap {
	return viewport.KeyMap{
		Up:       km.Up,
		Down:     km.Down,
		PageUp:   km.PageUp,
		PageDown: km.PageDown,
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
	r.status = grpcclient.CallStatus{}
	r.hasCode = false
	r.failure = ""
	r.duration = 0
	r.viewport.SetContent("")
	r.viewport.GotoTop()
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

// SetSuccess shows a decoded response body.
func (r *Response) SetSuccess(body string, took time.Duration) {
	r.Clear()
	r.state = responseOK
	r.body = body
	r.duration = took
	r.viewport.SetContent(body)
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
	r.failure = message
	r.duration = took

	body := message
	if hasStatus {
		body = status.Message
	}
	r.viewport.SetContent(wrapText(body, r.viewport.Width))
	r.viewport.GotoTop()
}

// SetSize sets the panel's inner content area. One line is reserved for the
// status line above the body.
func (r *Response) SetSize(width, height int) {
	r.width = width
	r.height = height
	r.viewport.Width = width
	r.viewport.Height = max(height-2, 0)

	if r.state == responseFailed && !r.hasCode {
		r.viewport.SetContent(wrapText(r.failure, width))
	}
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
		took := r.styles.Muted.Render(formatDuration(r.duration))
		return truncate(r.styles.StatusOK.Render("OK")+"  "+took, r.width)
	case r.hasCode:
		code := fmt.Sprintf("%s (%d)", r.status.Name, r.status.Code)
		return truncate(r.styles.StatusError.Render(code), r.width)
	default:
		return truncate(r.styles.StatusError.Render("Call failed"), r.width)
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

// wrapText hard-wraps text to width on whitespace.
func wrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}
