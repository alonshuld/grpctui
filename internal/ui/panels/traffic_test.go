package panels_test

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/proxy"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// replayed runs the command a pick produced and narrows it to the message the
// root model would receive.
func replayed(t *testing.T, cmd tea.Cmd) panels.TrafficReplayMsg {
	t.Helper()

	msg, ok := cmd().(panels.TrafficReplayMsg)
	require.True(t, ok, "expected a TrafficReplayMsg")
	return msg
}

// trafficClock is frozen, so that a list of "5m ago" is decided by the events
// that produced it rather than by when the test ran.
var trafficClock = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func newTraffic(t *testing.T) panels.Traffic {
	t.Helper()

	tr := panels.NewTraffic(keys.Default(), styles.New())
	tr.SetClock(func() time.Time { return trafficClock })
	tr.SetSize(80, 20)
	tr.Enable("127.0.0.1:8080")
	return tr
}

// call feeds a whole unary call through, which is four events.
func call(tr *panels.Traffic, id int, method string, request []byte) {
	at := trafficClock.Add(-time.Minute)
	tr.Record(proxy.Event{Call: id, Kind: proxy.Started, Method: method, Peer: "10.0.0.1:5555", At: at})
	tr.Record(proxy.Event{Call: id, Kind: proxy.Sent, Method: method, Wire: request, At: at})
	tr.Record(proxy.Event{Call: id, Kind: proxy.Received, Method: method, At: at})
	tr.Record(proxy.Event{Call: id, Kind: proxy.Ended, Method: method, At: at})
}

func TestTraffic_SaysWhereToPointTheClient(t *testing.T) {
	tr := newTraffic(t)
	tr.Open()

	view := tr.View()
	assert.Contains(t, view, "127.0.0.1:8080")
	assert.Contains(t, view, "Nothing yet")
}

// Without --proxy the key has to explain itself rather than open a list that
// will never fill.
func TestTraffic_ExplainsItselfWhenNotProxying(t *testing.T) {
	tr := panels.NewTraffic(keys.Default(), styles.New())
	tr.SetSize(80, 20)
	tr.Open()

	assert.False(t, tr.Enabled())
	assert.Contains(t, tr.View(), "--proxy")
}

func TestTraffic_FoldsEventsIntoOneCall(t *testing.T) {
	tr := newTraffic(t)
	call(&tr, 1, "/demo.v1.Greeter/SayHello", helloWire("hi"))
	tr.Open()

	require.Equal(t, 1, tr.Len())
	got := tr.Calls()[0]
	assert.Equal(t, 1, got.Sent)
	assert.Equal(t, 1, got.Received)
	assert.True(t, got.Done)
	assert.Equal(t, helloWire("hi"), got.Request)

	view := tr.View()
	assert.Contains(t, view, "SayHello")
	assert.Contains(t, view, "→1 ←1")
	assert.Contains(t, view, "OK")
	assert.Contains(t, view, "1m ago")
}

func TestTraffic_ShowsAFailedCallsCode(t *testing.T) {
	tr := newTraffic(t)
	tr.Record(proxy.Event{Call: 1, Kind: proxy.Started, Method: "/demo.v1.Greeter/SayHello", At: trafficClock})
	tr.Record(proxy.Event{
		Call:      1,
		Kind:      proxy.Ended,
		Method:    "/demo.v1.Greeter/SayHello",
		Status:    grpcclient.CallStatus{Code: 5, Name: "NotFound"},
		HasStatus: true,
		At:        trafficClock,
	})
	tr.Open()

	assert.Contains(t, tr.View(), "NotFound")
}

// A stream that is still running is not a call that succeeded, and saying "OK"
// beside one would be a lie for as long as it stays open.
func TestTraffic_MarksAnOpenCall(t *testing.T) {
	tr := newTraffic(t)
	tr.Record(proxy.Event{Call: 1, Kind: proxy.Started, Method: "/demo.v1.Greeter/Watch", At: trafficClock})
	tr.Record(proxy.Event{Call: 1, Kind: proxy.Received, Method: "/demo.v1.Greeter/Watch", At: trafficClock})
	tr.Open()

	view := tr.View()
	assert.Contains(t, view, "open")
	assert.Contains(t, view, "→0 ←1")
}

// Several calls are in flight at once on a busy connection, so events interleave
// and the call number is what puts them back together.
func TestTraffic_KeepsInterleavedCallsApart(t *testing.T) {
	tr := newTraffic(t)

	tr.Record(proxy.Event{Call: 1, Kind: proxy.Started, Method: "/demo.v1.Greeter/A", At: trafficClock})
	tr.Record(proxy.Event{Call: 2, Kind: proxy.Started, Method: "/demo.v1.Greeter/B", At: trafficClock})
	tr.Record(proxy.Event{Call: 2, Kind: proxy.Received, Method: "/demo.v1.Greeter/B", At: trafficClock})
	tr.Record(proxy.Event{Call: 1, Kind: proxy.Sent, Method: "/demo.v1.Greeter/A", At: trafficClock})

	require.Equal(t, 2, tr.Len())
	assert.Equal(t, 1, tr.Calls()[0].Sent, "the first call's message")
	assert.Equal(t, 0, tr.Calls()[0].Received)
	assert.Equal(t, 1, tr.Calls()[1].Received, "the second call's")
}

// An event for a call that has already fallen off the front is ignored rather
// than resurrecting a row that is gone.
func TestTraffic_IgnoresEventsForUnknownCalls(t *testing.T) {
	tr := newTraffic(t)
	tr.Record(proxy.Event{Call: 99, Kind: proxy.Received, Method: "/demo.v1.Greeter/A"})
	assert.Equal(t, 0, tr.Len())
}

func TestTraffic_DropsTheOldest(t *testing.T) {
	tr := newTraffic(t)
	for i := 1; i <= 260; i++ {
		call(&tr, i, "/demo.v1.Greeter/SayHello", helloWire("hi"))
	}
	tr.Open()

	assert.Equal(t, 200, tr.Len())
	assert.Equal(t, 61, tr.Calls()[0].ID, "the newest two hundred are the ones kept")
	assert.Contains(t, tr.View(), "60 earlier calls dropped")
}

// The events the proxy could not hand over are said out loud: a log quietly
// missing entries is worse than one that admits to it.
func TestTraffic_ReportsLostEvents(t *testing.T) {
	tr := newTraffic(t)
	call(&tr, 1, "/demo.v1.Greeter/SayHello", helloWire("hi"))
	tr.SetLost(7)
	tr.Open()

	assert.Contains(t, tr.View(), "7 events were dropped")
}

func TestTraffic_ReplaysTheCallUnderTheCursor(t *testing.T) {
	tr := newTraffic(t)
	call(&tr, 1, "/demo.v1.Greeter/SayHello", helloWire("hi"))
	tr.Open()

	tr, cmd := tr.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)

	msg, ok := cmd().(panels.TrafficReplayMsg)
	require.True(t, ok)
	assert.Equal(t, "demo.v1.Greeter.SayHello", msg.Method,
		"the wire's slash becomes the dot the rest of grpctui uses")
	assert.Equal(t, helloWire("hi"), msg.Wire)
	assert.False(t, tr.Opened(), "picking a call closes the panel")
}

// A call whose request nobody saw — one the proxy started watching mid-stream,
// or a server-streaming call with an empty message — has nothing to load.
func TestTraffic_RefusesToReplayWithoutARequest(t *testing.T) {
	tr := newTraffic(t)
	tr.Record(proxy.Event{Call: 1, Kind: proxy.Started, Method: "/demo.v1.Greeter/Watch", At: trafficClock})
	tr.Open()

	tr, cmd := tr.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd)
	assert.True(t, tr.Opened())
}

func TestTraffic_Navigates(t *testing.T) {
	tr := newTraffic(t)
	for i := 1; i <= 3; i++ {
		call(&tr, i, "/demo.v1.Greeter/SayHello", helloWire("hi"))
	}
	tr.Open()

	// New calls arrive under a cursor that is already at the tail, so it follows
	// them — which is what makes the panel readable while traffic is flowing.
	tr, cmd := tr.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)

	tr.Open()
	tr, _ = tr.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	tr, cmd = tr.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.Equal(t, "demo.v1.Greeter.SayHello", replayed(t, cmd).Method)
}

// A user reading a call from ten minutes ago must not be dragged away from it
// by traffic arriving underneath.
func TestTraffic_DoesNotFollowTheTailWhileScrolledUp(t *testing.T) {
	tr := newTraffic(t)
	for i := 1; i <= 3; i++ {
		call(&tr, i, "/demo.v1.Greeter/SayHello", helloWire("first"))
	}
	tr.Open()

	tr, _ = tr.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	call(&tr, 4, "/demo.v1.Greeter/SayHello", helloWire("later"))

	tr, cmd := tr.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.Equal(t, helloWire("first"), replayed(t, cmd).Wire,
		"the cursor stayed where it was put")
}

func TestTraffic_Closes(t *testing.T) {
	tests := map[string]tea.KeyMsg{
		"esc": {Type: tea.KeyEsc},
		"t":   {Type: tea.KeyRunes, Runes: []rune("t")},
	}

	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			tr := newTraffic(t)
			tr.Open()

			tr, _ = tr.Update(key)
			assert.False(t, tr.Opened())
		})
	}
}

func TestTrafficCallNames(t *testing.T) {
	tests := []struct {
		method, short, full string
	}{
		{"/demo.v1.Greeter/SayHello", "SayHello", "demo.v1.Greeter.SayHello"},
		{"demo.v1.Greeter/SayHello", "SayHello", "demo.v1.Greeter.SayHello"},
		{"/Bare", "Bare", "Bare"},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			c := panels.TrafficCall{Method: tt.method}
			assert.Equal(t, tt.short, c.ShortMethod())
			assert.Equal(t, tt.full, c.FullName())
		})
	}
}
