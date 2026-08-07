package ui_test

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui"
)

// Where a method sits in the tree, counted in j presses from the top: two
// services, Echo first, then Greeter's four methods.
const (
	stepsToSayHello       = 3
	stepsToSayHelloStream = 4
	stepsToCollectHellos  = 5
	stepsToConverse       = 6
)

// settleQuiet is how long the harness waits for the model to go quiet before
// deciding it has. Every command in these tests is in-memory, so the only thing
// that ever takes this long is a receive on a stream the server has nothing
// more to say on — which is precisely the state the tests want to observe.
const settleQuiet = 250 * time.Millisecond

// harness drives a model the way the bubbletea runtime does: commands run on
// their own goroutines, and the messages they produce go back into the model
// along with the commands *those* produce.
//
// The synchronous helpers the rest of this package uses cannot drive a stream.
// They execute a command and wait for it, and an open stream's receive does not
// return until the server says something — so a test would hang on the first
// one instead of seeing the panel it opened.
type harness struct {
	t     *testing.T
	model ui.Model

	// inbox is where commands post their messages. It is generously buffered so
	// that a command whose message nobody is waiting for — the receive still
	// outstanding when a test finishes — cannot leak a blocked goroutine.
	inbox chan tea.Msg
}

func newHarness(t *testing.T, m ui.Model) *harness {
	t.Helper()
	return &harness{t: t, model: m, inbox: make(chan tea.Msg, 256)}
}

// streamTermHeight is the window these tests run in. It is far taller than the
// rest of the package's, because a stream log grows a block per message and the
// panel follows the tail: at 24 rows a test asserting on the third message back
// would be asserting on how far the panel has scrolled.
const streamTermHeight = 60

// streaming returns a harness on a model that has discovered the demo services
// and selected the method at steps, with the request form focused.
func streaming(t *testing.T, client ui.Client, steps int, opts ...ui.Option) *harness {
	t.Helper()
	return streamingAt(t, client, steps, streamTermHeight, opts...)
}

// streamingAt is [streaming] in a window of a chosen height.
func streamingAt(t *testing.T, client ui.Client, steps, height int, opts ...ui.Option) *harness {
	t.Helper()

	m := selectMethod(t, settled(t, newModel(t, client, opts...)), steps)
	next, _ := m.Update(tea.WindowSizeMsg{Width: termWidth, Height: height})
	return newHarness(t, asModel(t, next))
}

// fakeClock is the clock a streaming test runs on. It is read from the
// goroutines the model's commands run on and written by the test, so every
// access takes the lock — a stream's elapsed time is not worth a data race.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// newClock starts a clock at a fixed instant. The instant itself never appears
// on screen: only differences from it do.
func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// press sends keystrokes and lets everything they set off run to a standstill.
func (h *harness) press(keys ...string) {
	h.t.Helper()

	for _, k := range keys {
		next, cmd := h.model.Update(keyMsg(k))
		h.model = asModel(h.t, next)
		h.exec(cmd)
	}
	h.settle()
}

// exec runs one command off the test goroutine, posting whatever it produces
// into the inbox — batches flattened, exactly as the runtime does.
func (h *harness) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				h.exec(c)
			}
		case nil:
		default:
			h.inbox <- msg
		}
	}()
}

// settle feeds the model everything in the inbox, and everything the resulting
// commands produce, until nothing has arrived for [settleQuiet].
//
// Spinner ticks are dropped: each one asks for the next, so feeding them back
// would mean the model never goes quiet. Tests that need the tick's own effect
// send one themselves.
func (h *harness) settle() {
	h.t.Helper()

	for {
		select {
		case msg := <-h.inbox:
			if _, isTick := msg.(spinner.TickMsg); isTick {
				continue
			}
			next, cmd := h.model.Update(msg)
			h.model = asModel(h.t, next)
			h.exec(cmd)
		case <-time.After(settleQuiet):
			return
		}
	}
}

// view renders the model as it currently stands.
func (h *harness) view() string { return h.model.View() }

func TestModel_ServerStreamingAppendsMessagesLive(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToSayHelloStream)

	h.press("ctrl+s")
	stream := client.stream(t)

	require.Equal(t, "demo.v1.Greeter.SayHelloStream", stream.Method().FullName)
	assert.Len(t, stream.outbound(), 1, "the one request goes out with the stream")
	assert.True(t, stream.sendingClosed(),
		"a method the client does not stream into has nothing more to send")

	view := h.view()
	assert.Contains(t, view, "Watching", "an open server stream says so")
	assert.Contains(t, view, "→1 ←0")

	// Now the server starts answering, one message at a time.
	stream.reply(reply(stream.Method(), map[string]any{"greeting": "hello, one", "count": int32(1)}))
	h.settle()

	view = h.view()
	assert.Contains(t, view, "hello, one")
	assert.Contains(t, view, "← 1", "an inbound message is marked as one")
	assert.Contains(t, view, "→1 ←1")

	stream.reply(reply(stream.Method(), map[string]any{"greeting": "hello, two", "count": int32(2)}))
	h.settle()

	view = h.view()
	assert.Contains(t, view, "hello, one", "earlier messages stay on screen")
	assert.Contains(t, view, "hello, two")
	assert.Contains(t, view, "← 2")
	assert.Contains(t, view, "Watching", "the stream is still open")

	stream.finish()
	h.settle()

	view = h.view()
	assert.Contains(t, view, "OK")
	assert.Contains(t, view, "stream finished")
	assert.Contains(t, view, "hello, two", "the log survives the stream ending")
	assert.NotContains(t, view, "Watching")
	assert.Equal(t, 1, stream.closed(), "a finished stream is let go of")
}

func TestModel_ClientStreamingQueuesMessages(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToCollectHellos)

	h.press("ctrl+s")
	stream := client.stream(t)

	require.Equal(t, "demo.v1.Greeter.CollectHellos", stream.Method().FullName)
	assert.False(t, stream.sendingClosed(),
		"a client-streaming call keeps sending until the user says otherwise")

	// Two more messages, sent without waiting for anything in between.
	h.press("ctrl+s", "ctrl+s")

	assert.Len(t, stream.outbound(), 3)
	assert.Contains(t, h.view(), "→3 ←0")
	assert.Contains(t, h.view(), "→ 3", "each outbound message is numbered")

	// ctrl+e ends the request stream; the answer comes back after it.
	h.press("ctrl+e")

	assert.True(t, stream.sendingClosed())
	assert.Contains(t, h.view(), "sending closed")

	stream.reply(reply(stream.Method(), map[string]any{"greeting": "heard 3", "count": int32(3)}))
	stream.finish()
	h.settle()

	view := h.view()
	assert.Contains(t, view, "heard 3")
	assert.Contains(t, view, "OK")
	assert.Contains(t, view, "→3 ←1")
}

func TestModel_BidiStreamingInterleavesBothDirections(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToConverse)

	h.press("ctrl+s")
	stream := client.stream(t)
	require.Equal(t, "demo.v1.Greeter.Converse", stream.Method().FullName)

	stream.reply(reply(stream.Method(), map[string]any{"greeting": "and hello to you"}))
	h.settle()

	h.press("ctrl+s")
	stream.reply(reply(stream.Method(), map[string]any{"greeting": "still here"}))
	h.settle()

	view := h.view()
	assert.Contains(t, view, "→ 1")
	assert.Contains(t, view, "and hello to you")
	assert.Contains(t, view, "→ 2")
	assert.Contains(t, view, "still here")
	assert.Contains(t, view, "→2 ←2")

	// The log reads in the order things happened, which is the whole point of a
	// bidi view: the first answer comes before the second question.
	assert.Less(t, indexOf(t, view, "and hello to you"), indexOf(t, view, "→ 2"),
		"the log is out of order:\n%s", view)
}

func TestModel_StreamCancelledWithEsc(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToSayHelloStream)

	h.press("ctrl+s")
	stream := client.stream(t)

	stream.reply(reply(stream.Method(), map[string]any{"greeting": "one"}))
	h.settle()
	require.Contains(t, h.view(), "one")

	// esc is the single keybinding that stops a stream, and what it leaves on
	// screen is the cancellation — not a blank panel.
	h.press("esc")

	view := h.view()
	assert.Contains(t, view, "Canceled")
	assert.Contains(t, view, "one", "what the stream already carried stays put")
	assert.NotContains(t, view, "Watching")
}

func TestModel_StreamEndingOnAStatus(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToSayHelloStream)

	h.press("ctrl+s")
	stream := client.stream(t)

	stream.reply(reply(stream.Method(), map[string]any{"greeting": "before the failure"}))
	h.settle()

	stream.fail(fmt.Errorf("stream demo.v1.Greeter.SayHelloStream: %w",
		status.Error(codes.PermissionDenied, "not allowed to watch this")))
	h.settle()

	view := h.view()
	assert.Contains(t, view, "PermissionDenied (7)", "the gRPC status reaches the screen intact")
	assert.Contains(t, view, "not allowed to watch this")
	assert.Contains(t, view, "before the failure",
		"the messages that arrived before the failure are still the answer")
}

func TestModel_StreamThatCannotBeOpened(t *testing.T) {
	client := healthyClient()
	client.streamErr = fmt.Errorf("stream demo.v1.Greeter.SayHelloStream: %w",
		status.Error(codes.Unavailable, "connection refused"))

	h := streaming(t, client, stepsToSayHelloStream)
	h.press("ctrl+s")

	view := h.view()
	assert.Contains(t, view, "Unavailable (14)")
	assert.NotContains(t, view, "Watching")
}

// Selecting another method while a stream is running has to close it. Left
// open, it would go on receiving into a panel that has moved on — and go on
// holding a gRPC call open for a method nobody is looking at.
func TestModel_SelectingAnotherMethodClosesTheStream(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToSayHelloStream)

	h.press("ctrl+s")
	stream := client.stream(t)
	require.Contains(t, h.view(), "Watching")

	// Back to the tree, up one, and select the unary method above it.
	h.press("shift+tab", "k", "enter")

	assert.Equal(t, 1, stream.closed(), "the stream was left running")
	assert.NotContains(t, h.view(), "Watching")

	// And a message that was already on its way is not drawn under the new
	// method's name.
	stream.reply(reply(stream.Method(), map[string]any{"greeting": "too late"}))
	h.settle()

	assert.NotContains(t, h.view(), "too late")
}

// A second ctrl+s on a server-streaming method restarts the stream rather than
// being swallowed — the same thing a second ctrl+s does to a unary call.
func TestModel_RestartingAServerStream(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToSayHelloStream)

	h.press("ctrl+s")
	first := client.stream(t)
	first.reply(reply(first.Method(), map[string]any{"greeting": "from the first stream"}))
	h.settle()
	require.Contains(t, h.view(), "from the first stream")

	h.press("ctrl+s")
	second := client.stream(t)

	require.NotSame(t, first, second, "ctrl+s must open a new stream")
	assert.Equal(t, 1, first.closed(), "the stream being replaced is closed")
	assert.NotContains(t, h.view(), "from the first stream", "the log starts again")

	// The abandoned stream's messages must not reappear in the new one's log.
	first.reply(reply(first.Method(), map[string]any{"greeting": "from the first stream"}))
	h.settle()

	assert.NotContains(t, h.view(), "from the first stream")
}

// A send that fails after the stream has already broken reports io.EOF, and the
// reason belongs to the receiving half. Saying "EOF" on screen would be the
// least useful description of what happened.
func TestModel_SendFailureDefersToTheReceivingHalf(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToConverse)

	h.press("ctrl+s")
	stream := client.stream(t)

	stream.mu.Lock()
	stream.sendErr = io.EOF
	stream.mu.Unlock()

	h.press("ctrl+s")
	assert.NotContains(t, h.view(), "EOF")

	stream.fail(fmt.Errorf("stream demo.v1.Greeter.Converse: %w",
		status.Error(codes.Internal, "the server gave up")))
	h.settle()

	view := h.view()
	assert.Contains(t, view, "Internal (13)")
	assert.Contains(t, view, "the server gave up")
}

// Sending after the request stream is closed is a mistake worth naming, not a
// silent no-op and not a crash.
func TestModel_SendAfterEndingTheRequestStream(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToCollectHellos)

	h.press("ctrl+s")
	stream := client.stream(t)

	h.press("ctrl+e")
	require.True(t, stream.sendingClosed())

	h.press("ctrl+s")

	assert.Len(t, stream.outbound(), 1, "nothing more went out")
	assert.Contains(t, h.view(), "sending half is closed")
}

// ctrl+e with nothing open is a key pressed at the wrong moment, not an error.
func TestModel_EndingSendingWithNoStream(t *testing.T) {
	h := streaming(t, healthyClient(), stepsToSayHello)

	assert.NotPanics(t, func() { h.press("ctrl+e") })
	assert.NotContains(t, h.view(), "sending closed")
}

// The elapsed clock advances while the stream is quiet, which is what says a
// watch is still running rather than stuck.
func TestModel_StreamElapsedAdvancesOnTicks(t *testing.T) {
	client := healthyClient()
	clock := newClock()
	h := streaming(t, client, stepsToSayHelloStream, ui.WithClock(clock.Now))

	h.press("ctrl+s")
	client.stream(t)
	require.Contains(t, h.view(), "0s")

	clock.advance(3200 * time.Millisecond)
	next, _ := h.model.Update(spinner.TickMsg{Time: clock.Now()})
	h.model = asModel(t, next)

	assert.Contains(t, h.view(), "3.2s")
}

// The status bar carries the counts and the clock as well as the panel does:
// the response panel scrolls, and this is the number you watch while it moves.
func TestModel_StatusBarReportsStreamProgress(t *testing.T) {
	client := healthyClient()
	h := streaming(t, client, stepsToSayHelloStream)

	require.NotContains(t, h.view(), "→0 ←0", "an idle model says nothing about streams")

	h.press("ctrl+s")
	stream := client.stream(t)
	stream.reply(reply(stream.Method(), map[string]any{"greeting": "one"}))
	h.settle()

	assert.Contains(t, h.view(), "→1 ←1")
}

// --- golden-file tests -----------------------------------------------------

// The streaming screens, shot through the harness rather than through teatest.
//
// A live stream draws a spinner, whose frame depends on how many ticks have
// been delivered, and a clock. The harness delivers no ticks and the clock is
// the test's own, so what these files capture is the panel and not the moment
// it was taken.
func TestModel_StreamGolden(t *testing.T) {
	const goldenHeight = 34

	tests := map[string]func(h *harness, client *fakeClient, clock *fakeClock){
		// A server stream part-way through: two messages in, still watching.
		"watching a stream": func(h *harness, client *fakeClient, clock *fakeClock) {
			h.press("ctrl+s")
			stream := client.stream(h.t)

			clock.advance(120 * time.Millisecond)
			stream.reply(reply(stream.Method(), map[string]any{"greeting": "hello, world", "count": int32(1)}))
			h.settle()

			clock.advance(880 * time.Millisecond)
			stream.reply(reply(stream.Method(), map[string]any{"greeting": "hello again", "count": int32(2)}))
			h.settle()
		},

		// And the same stream once the server has finished with it.
		"finished stream": func(h *harness, client *fakeClient, clock *fakeClock) {
			h.press("ctrl+s")
			stream := client.stream(h.t)

			clock.advance(150 * time.Millisecond)
			stream.reply(reply(stream.Method(), map[string]any{"greeting": "hello, world", "count": int32(1)}))
			h.settle()

			clock.advance(90 * time.Millisecond)
			stream.finish()
			h.settle()
		},

		// A stream that ended badly, with what it carried still on screen.
		"failed stream": func(h *harness, client *fakeClient, clock *fakeClock) {
			h.press("ctrl+s")
			stream := client.stream(h.t)

			clock.advance(200 * time.Millisecond)
			stream.fail(fmt.Errorf("stream demo.v1.Greeter.SayHelloStream: %w",
				status.Error(codes.PermissionDenied, "caller is not allowed to watch")))
			h.settle()
		},
	}

	for name, script := range tests {
		t.Run(name, func(t *testing.T) {
			client := healthyClient()
			clock := newClock()
			h := streamingAt(t, client, stepsToSayHelloStream, goldenHeight, ui.WithClock(clock.Now))

			script(h, client, clock)
			teatest.RequireEqualOutput(t, []byte(h.view()))
		})
	}
}

// The bidi view is the one v0.5 exists for: both directions in one log, told
// apart by arrow and by colour.
func TestModel_BidiStreamGolden(t *testing.T) {
	client := healthyClient()
	clock := newClock()
	h := streamingAt(t, client, stepsToConverse, 34, ui.WithClock(clock.Now))

	h.press("ctrl+s")
	stream := client.stream(t)

	clock.advance(45 * time.Millisecond)
	stream.reply(reply(stream.Method(), map[string]any{"greeting": "and hello to you", "count": int32(1)}))
	h.settle()

	clock.advance(310 * time.Millisecond)
	h.press("ctrl+s")

	clock.advance(60 * time.Millisecond)
	stream.reply(reply(stream.Method(), map[string]any{"greeting": "still here", "count": int32(2)}))
	h.settle()

	teatest.RequireEqualOutput(t, []byte(h.view()))
}

// The transport layer is reached through an interface so that the UI can be
// tested without a server. This is the assertion that the real one still fits
// through it — including the streaming half added in v0.5.
var _ ui.Client = (*grpcclient.Client)(nil)

// indexOf reports where text appears in a view, failing the test when it does
// not appear at all.
func indexOf(t *testing.T, view, text string) int {
	t.Helper()

	i := strings.Index(view, text)
	require.GreaterOrEqual(t, i, 0, "%q is not on screen", text)
	return i
}
