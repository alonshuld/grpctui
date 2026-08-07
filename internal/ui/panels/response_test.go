package panels_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

func newResponse(t *testing.T) panels.Response {
	t.Helper()

	r := panels.NewResponse(keys.Default(), styles.New())
	r.SetSize(60, 12)
	return r
}

func responseMethod() grpcclient.Method {
	return grpcclient.Method{
		Name:       "SayHello",
		FullName:   "demo.v1.Greeter.SayHello",
		InputType:  "demo.v1.HelloRequest",
		OutputType: "demo.v1.HelloReply",
	}
}

func TestResponse_EmptyBeforeAnythingIsSelected(t *testing.T) {
	assert.Contains(t, newResponse(t).View(), "Send a request to see the response.")
}

// With a method selected but nothing sent, the panel is the place that says
// what shape the answer will take.
func TestResponse_NamesTheOutputTypeOnceAMethodIsSelected(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())

	view := r.View()
	assert.Contains(t, view, "demo.v1.HelloReply")
	assert.Contains(t, view, "ctrl+s")
}

func TestResponse_InFlight(t *testing.T) {
	r := newResponse(t)

	cmd := r.SetInFlight(responseMethod())

	require.NotNil(t, cmd, "the loading state must come with a spinner tick")
	assert.True(t, r.InFlight())
	assert.Contains(t, r.View(), "Calling SayHello…")
}

func TestResponse_Success(t *testing.T) {
	r := newResponse(t)
	r.SetInFlight(responseMethod())

	r.SetSuccess("{\n  \"greeting\": \"hi\"\n}", protoschema.FormatJSON, 12*time.Millisecond)

	assert.False(t, r.InFlight())
	view := r.View()
	assert.Contains(t, view, "OK")
	assert.Contains(t, view, "12ms")
	assert.Contains(t, view, `"greeting": "hi"`)
}

// A gRPC status is an answer from the service, and the panel says which one.
func TestResponse_FailureWithAStatus(t *testing.T) {
	r := newResponse(t)
	status := grpcclient.CallStatus{Code: 5, Name: "NotFound", Message: "no such greeting"}

	r.SetFailure("invoke demo.v1.Greeter.SayHello: rpc error: code = NotFound", status, true, 3*time.Millisecond)

	view := r.View()
	assert.Contains(t, view, "NotFound (5)")
	assert.Contains(t, view, "no such greeting")
	assert.NotContains(t, view, "rpc error", "the raw error text is not for the panel")
}

// A failure with no status never reached the wire, and inventing a code for it
// would be a lie.
func TestResponse_FailureWithoutAStatus(t *testing.T) {
	r := newResponse(t)

	r.SetFailure("the request could not be encoded", grpcclient.CallStatus{}, false, 0)

	view := r.View()
	assert.Contains(t, view, "Call failed")
	assert.Contains(t, view, "could not be encoded")
}

func TestResponse_LongMessagesWrapInsteadOfOverflowing(t *testing.T) {
	r := newResponse(t)
	long := strings.Repeat("a very long status message ", 10)

	r.SetFailure(long, grpcclient.CallStatus{Code: 3, Name: "InvalidArgument", Message: long}, true, 0)

	for line := range strings.SplitSeq(r.View(), "\n") {
		assert.LessOrEqual(t, len([]rune(line)), 60, "line ran past the panel: %q", line)
	}
}

// Resizing the terminal must re-wrap whatever is on screen. A body still
// wrapped to the previous width does not spill out of the panel — the viewport
// truncates it — so the failure mode is silent: the right-hand end of every
// line simply goes missing.
func TestResponse_RewrapsOnResize(t *testing.T) {
	const tail = "LASTWORD"
	long := strings.Repeat("a very long status message ", 10) + tail

	cases := map[string]func(r *panels.Response){
		"with a status": func(r *panels.Response) {
			r.SetFailure("wrapped", grpcclient.CallStatus{Code: 3, Name: "InvalidArgument", Message: long}, true, 0)
		},
		"without a status": func(r *panels.Response) {
			r.SetFailure(long, grpcclient.CallStatus{}, false, 0)
		},
	}

	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			r := panels.NewResponse(keys.Default(), styles.New())
			r.SetSize(80, 24)
			fail(&r)
			require.Contains(t, r.View(), tail, "the message was cut off before the resize")

			r.SetSize(30, 24)

			assert.Contains(t, r.View(), tail, "the body kept the old wrap width and lost its right-hand end")
			for line := range strings.SplitSeq(r.View(), "\n") {
				assert.LessOrEqual(t, len([]rune(line)), 30, "line ran past the panel: %q", line)
			}
		})
	}
}

func TestResponse_Clear(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())
	r.SetSuccess(`{"greeting": "hi"}`, protoschema.FormatJSON, time.Millisecond)

	r.Clear()

	view := r.View()
	assert.NotContains(t, view, "greeting")
	assert.Contains(t, view, "demo.v1.HelloReply", "clearing a result keeps the selected method")
}

// A new method means the previous method's answer is no longer an answer to
// anything on screen.
func TestResponse_SetMethodDropsTheOldResult(t *testing.T) {
	r := newResponse(t)
	r.SetSuccess(`{"greeting": "hi"}`, protoschema.FormatJSON, time.Millisecond)

	r.SetMethod(responseMethod())

	assert.NotContains(t, r.View(), "greeting")
}

// The spinner has to keep turning while the panel is blurred: a call the user
// tabbed away from is still running.
func TestResponse_SpinnerTicksWhileBlurred(t *testing.T) {
	r := newResponse(t)
	cmd := r.SetInFlight(responseMethod())
	require.NotNil(t, cmd)

	tick, ok := cmd().(spinner.TickMsg)
	require.True(t, ok, "expected a spinner tick, got %T", cmd())

	r.Blur()
	r, next := r.Update(tick)

	assert.NotNil(t, next, "the spinner stopped when the panel lost focus")
	assert.True(t, r.InFlight())
}

func TestResponse_IgnoresTicksWhenIdle(t *testing.T) {
	r := newResponse(t)
	cmd := r.SetInFlight(responseMethod())
	tick, ok := cmd().(spinner.TickMsg)
	require.True(t, ok)

	r.SetSuccess("{}", protoschema.FormatJSON, time.Millisecond)
	_, next := r.Update(tick)

	assert.Nil(t, next, "the spinner kept ticking after the call finished")
}

func TestResponse_ScrollsALongBody(t *testing.T) {
	r := newResponse(t)
	r.SetSize(60, 8)
	r.Focus()

	// Numbered, so that scrolling actually changes what is on screen.
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d", i)
	}
	r.SetSuccess(strings.Join(lines, "\n"), protoschema.FormatJSON, time.Millisecond)

	top := r.View()
	r, _ = r.Update(keyMsg("j"))

	assert.NotEqual(t, top, r.View(), "j did not scroll the body")
	assert.LessOrEqual(t, len(strings.Split(r.View(), "\n")), 8, "the panel drew past its height")
}

func TestResponse_SurvivesDegenerateSizes(t *testing.T) {
	for _, size := range []struct{ w, h int }{{0, 0}, {1, 1}, {3, 2}, {80, 0}, {-4, -4}} {
		r := panels.NewResponse(keys.Default(), styles.New())
		r.SetSize(size.w, size.h)
		r.SetFailure("nope", grpcclient.CallStatus{Code: 2, Name: "Unknown"}, true, 0)

		assert.NotPanics(t, func() { _ = r.View() })
	}
}

// The viewport truncates any line wider than the panel, so a body with a long
// line is not merely off screen to the right — it is unreachable unless
// something scrolls horizontally.
func TestResponse_ScrollsALineWiderThanThePanel(t *testing.T) {
	const tail = "END-OF-THE-LINE"

	r := newResponse(t)
	r.SetSize(30, 8)
	r.Focus()
	r.SetSuccess(`{"detail": "`+strings.Repeat("x", 80)+tail+`"}`, protoschema.FormatJSON, time.Millisecond)

	require.NotContains(t, r.View(), tail, "the line was expected to start off screen")

	for range 20 {
		r, _ = r.Update(keyMsg("L"))
	}
	assert.Contains(t, r.View(), tail, "no keystroke reached the right-hand end of the line")

	for range 20 {
		r, _ = r.Update(keyMsg("H"))
	}
	assert.Contains(t, r.View(), `"detail"`, "scrolling back left did not return to the start")
}

func TestResponse_ScrollsHorizontallyWithShiftArrows(t *testing.T) {
	const tail = "END-OF-THE-LINE"

	r := newResponse(t)
	r.SetSize(30, 8)
	r.Focus()
	r.SetSuccess(strings.Repeat("x", 80)+tail, protoschema.FormatJSON, time.Millisecond)

	for range 20 {
		r, _ = r.Update(keyMsg("shift+right"))
	}
	assert.Contains(t, r.View(), tail)
}

// A new result starts at the left again: keeping the previous body's horizontal
// offset would open the next one part-way through a line.
func TestResponse_ClearResetsTheHorizontalOffset(t *testing.T) {
	r := newResponse(t)
	r.SetSize(30, 8)
	r.Focus()
	r.SetSuccess(strings.Repeat("x", 80)+"TAIL", protoschema.FormatJSON, time.Millisecond)

	for range 20 {
		r, _ = r.Update(keyMsg("L"))
	}
	require.Contains(t, r.View(), "TAIL")

	r.SetSuccess(`{"greeting": "hi"}`, protoschema.FormatJSON, time.Millisecond)

	assert.Contains(t, r.View(), `{"greeting": "hi"}`)
}

// grpc-go lets a server raise a bare code with no message at all. Preferring
// the empty status message over the error text leaves the panel showing a code
// above nothing.
func TestResponse_FailureWithAStatusButNoMessage(t *testing.T) {
	r := newResponse(t)

	r.SetFailure("invoke demo.v1.Greeter.SayHello: rpc error: code = NotFound",
		grpcclient.CallStatus{Code: 5, Name: "NotFound"}, true, 3*time.Millisecond)

	view := r.View()
	assert.Contains(t, view, "NotFound (5)")
	assert.Contains(t, view, "demo.v1.Greeter.SayHello",
		"with no status message the raw error is the only text there is")
}

// A body that is not JSON is still a successful call, and the panel says which
// it is rather than leaving the user to wonder why the braces went missing.
func TestResponse_SaysWhenTheBodyIsNotJSON(t *testing.T) {
	r := newResponse(t)

	r.SetSuccess(`type_url:"type.googleapis.com/some.server.only.Detail"`,
		protoschema.FormatText, 4*time.Millisecond)

	view := r.View()
	assert.Contains(t, view, "OK", "a call that came back is a success whatever its body looks like")
	assert.Contains(t, view, "protobuf text")
	assert.Contains(t, view, "some.server.only.Detail")
}

func TestResponse_SaysNothingAboutFormatForJSON(t *testing.T) {
	r := newResponse(t)

	r.SetSuccess(`{"greeting": "hi"}`, protoschema.FormatJSON, time.Millisecond)

	assert.NotContains(t, r.View(), "protobuf text")
}

// Highlighting a body colours it and does nothing else. The colours themselves
// are gone by the time a test can see them — lipgloss strips them on a terminal
// without any — so what is asserted here is that the text came through: a
// highlighter that dropped a character would be quietly editing the response.
func TestResponse_HighlightingLeavesTheBodyIntact(t *testing.T) {
	bodies := map[protoschema.Format]string{
		protoschema.FormatJSON: "{\n  \"greeting\": \"hello, world\",\n  \"count\": 3,\n  \"ok\": true\n}",
		protoschema.FormatText: "greeting: \"hello, world\"\ncount: 3",
	}

	for format, body := range bodies {
		t.Run(string(format), func(t *testing.T) {
			r := newResponse(t)
			r.SetSuccess(body, format, time.Millisecond)

			view := r.View()
			for line := range strings.SplitSeq(body, "\n") {
				assert.Contains(t, view, line)
			}
		})
	}
}

// --- the stream log --------------------------------------------------------

// streamMethod is a server-streaming method, which is what the panel calls
// "watching" rather than "streaming".
func streamMethod() grpcclient.Method {
	m := responseMethod()
	m.Name, m.FullName = "SayHelloStream", "demo.v1.Greeter.SayHelloStream"
	m.ServerStreaming = true
	return m
}

func TestResponse_StreamingAppendsBothDirections(t *testing.T) {
	r := newResponse(t)

	cmd := r.SetStreaming(streamMethod())
	require.NotNil(t, cmd, "an open stream must come with a spinner tick")
	require.True(t, r.Streaming())
	require.True(t, r.Active(), "an open stream is something esc can cancel")

	r.AppendSent(`{"name": "world"}`, protoschema.FormatJSON, 0)
	r.AppendReceived(`{"greeting": "hello"}`, protoschema.FormatJSON, 40*time.Millisecond)

	view := r.View()
	assert.Contains(t, view, "Watching")
	assert.Contains(t, view, "→ 1", "an outbound message is marked as one")
	assert.Contains(t, view, `"name"`)
	assert.Contains(t, view, "← 1", "an inbound message is marked as one")
	assert.Contains(t, view, `"greeting"`)
	assert.Equal(t, 1, r.Sent())
	assert.Equal(t, 1, r.Received())

	summary, ok := r.StreamSummary()
	require.True(t, ok)
	assert.Contains(t, summary, "→1 ←1")
}

// A client-streaming call is a conversation the user is still having, and
// calling it "watching" would be wrong about who is doing the talking.
func TestResponse_StreamingLabelsAConversation(t *testing.T) {
	m := streamMethod()
	m.ClientStreaming = true

	r := newResponse(t)
	r.SetStreaming(m)

	assert.Contains(t, r.View(), "Streaming")
	assert.NotContains(t, r.View(), "Watching")
}

func TestResponse_StreamFinishesCleanly(t *testing.T) {
	r := newResponse(t)
	r.SetStreaming(streamMethod())
	r.AppendReceived(`{"greeting": "hello"}`, protoschema.FormatJSON, time.Second)

	r.FinishStream("", grpcclient.CallStatus{}, false, 2*time.Second)

	view := r.View()
	assert.False(t, r.Streaming())
	assert.False(t, r.Active())
	assert.Contains(t, view, "OK")
	assert.Contains(t, view, "stream finished")
	assert.Contains(t, view, `"greeting"`, "the log outlives the stream")
	assert.Contains(t, view, "→0 ←1")

	_, ok := r.StreamSummary()
	assert.False(t, ok, "a finished stream is not progress to report")
}

func TestResponse_StreamFinishesOnAStatus(t *testing.T) {
	r := newResponse(t)
	r.SetStreaming(streamMethod())
	r.AppendReceived(`{"greeting": "hello"}`, protoschema.FormatJSON, time.Second)

	status := grpcclient.CallStatus{Code: 7, Name: "PermissionDenied", Message: "not allowed"}
	r.FinishStream("stream failed", status, true, 2*time.Second)

	view := r.View()
	assert.Contains(t, view, "PermissionDenied (7)")
	assert.Contains(t, view, "not allowed")
	assert.Contains(t, view, `"greeting"`, "what arrived before the failure is still the answer")
}

// A stream that fails before reaching the wire has no status to explain it, and
// the raw error is then the only text there is.
func TestResponse_StreamFinishesWithoutAStatus(t *testing.T) {
	r := newResponse(t)
	r.SetStreaming(streamMethod())

	r.FinishStream("dial tcp: connection refused", grpcclient.CallStatus{}, false, time.Second)

	assert.Contains(t, r.View(), "connection refused")
}

// FinishStream on a panel that is not streaming would otherwise overwrite a
// unary result with a verdict about a stream that never ran.
func TestResponse_FinishStreamIgnoresANonStream(t *testing.T) {
	r := newResponse(t)
	r.SetSuccess(`{"greeting": "hello"}`, protoschema.FormatJSON, time.Millisecond)

	r.FinishStream("", grpcclient.CallStatus{}, false, time.Second)

	assert.Contains(t, r.View(), "OK")
	assert.NotContains(t, r.View(), "stream finished")
}

// A watch stream is unbounded by design, so the log has to be bounded. Dropping
// the oldest keeps the tail, which is what a user watching one is looking at.
func TestResponse_StreamLogIsBounded(t *testing.T) {
	r := newResponse(t)
	r.SetSize(60, 4000) // tall enough that nothing is merely scrolled away
	r.SetStreaming(streamMethod())

	const messages = 600
	for i := range messages {
		r.AppendReceived(fmt.Sprintf(`{"n": %d}`, i), protoschema.FormatJSON, time.Duration(i)*time.Millisecond)
	}

	view := r.View()
	assert.Equal(t, messages, r.Received(), "the counts describe the call, not the window onto it")
	assert.Contains(t, view, "earlier messages dropped")
	assert.Contains(t, view, `"n": 599`, "the newest message is kept")
	assert.NotContains(t, view, `"n": 0,`, "the oldest are dropped")
}

// The panel follows the tail while the user is at the bottom, and stops the
// moment they scroll up to read something.
func TestResponse_StreamFollowsTheTailUntilScrolled(t *testing.T) {
	r := newResponse(t)
	r.SetStreaming(streamMethod())
	r.Focus()

	for i := range 20 {
		r.AppendReceived(fmt.Sprintf(`{"n": %d}`, i), protoschema.FormatJSON, 0)
	}
	require.Contains(t, r.View(), `"n": 19`, "the panel should be following the tail")

	r, _ = r.Update(keyMsg("g")) // top

	r.AppendReceived(`{"n": 20}`, protoschema.FormatJSON, 0)

	view := r.View()
	assert.NotContains(t, view, `"n": 20`, "a scrolled-away user must not be yanked back")
	assert.Contains(t, view, `"n": 0`)
}

// The clock keeps up with the log even between spinner ticks.
func TestResponse_StreamElapsed(t *testing.T) {
	r := newResponse(t)
	r.SetStreaming(streamMethod())

	r.AppendReceived(`{}`, protoschema.FormatJSON, 1500*time.Millisecond)
	assert.Equal(t, 1500*time.Millisecond, r.Elapsed())

	r.SetElapsed(3 * time.Second)
	assert.Contains(t, r.View(), "3s")

	// And a panel not streaming has no clock to set.
	r.FinishStream("", grpcclient.CallStatus{}, false, 3*time.Second)
	r.SetElapsed(9 * time.Second)
	assert.Equal(t, 3*time.Second, r.Elapsed())
}

// The spinner has to keep turning while a stream is open even though the panel
// does not have focus — the whole point is watching it from elsewhere.
func TestResponse_StreamSpinnerTurnsWithoutFocus(t *testing.T) {
	r := newResponse(t)
	r.SetStreaming(streamMethod())
	r.Blur()

	_, cmd := r.Update(spinner.TickMsg{})

	assert.NotNil(t, cmd, "the watching indicator stopped animating")
}
