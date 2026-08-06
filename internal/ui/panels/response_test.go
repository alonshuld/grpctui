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

	r.SetSuccess("{\n  \"greeting\": \"hi\"\n}", 12*time.Millisecond)

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

func TestResponse_Clear(t *testing.T) {
	r := newResponse(t)
	r.SetMethod(responseMethod())
	r.SetSuccess(`{"greeting": "hi"}`, time.Millisecond)

	r.Clear()

	view := r.View()
	assert.NotContains(t, view, "greeting")
	assert.Contains(t, view, "demo.v1.HelloReply", "clearing a result keeps the selected method")
}

// A new method means the previous method's answer is no longer an answer to
// anything on screen.
func TestResponse_SetMethodDropsTheOldResult(t *testing.T) {
	r := newResponse(t)
	r.SetSuccess(`{"greeting": "hi"}`, time.Millisecond)

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

	r.SetSuccess("{}", time.Millisecond)
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
	r.SetSuccess(strings.Join(lines, "\n"), time.Millisecond)

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
