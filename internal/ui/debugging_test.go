package ui_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/proxy"
	"github.com/alonshuld/grpctui/internal/ui"
)

// --- raw wire view ----------------------------------------------------------

// The bytes shown are produced from the message the call returned, so a test
// that asserts on them is asserting on the whole path: invoke, re-encode,
// render.
func TestModel_RawViewShowsTheResponseBytes(t *testing.T) {
	client := healthyClient()
	client.invoke = func(_ context.Context, method grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		return &grpcclient.UnaryResponse{
			Message:  reply(method, map[string]any{"greeting": "hello"}),
			Duration: 12 * time.Millisecond,
		}, nil
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello

	m, cmd := press(t, m, "ctrl+s")
	m = applyChain(t, m, cmd)
	require.Contains(t, m.View(), "hello")

	m, _ = press(t, m, "w")
	view := m.View()
	assert.Contains(t, view, "raw bytes")
	assert.Contains(t, view, "00000000", "a hex dump")
	assert.Contains(t, view, "hello", "the string is legible in the field listing")

	m, _ = press(t, m, "w")
	assert.NotContains(t, m.View(), "raw bytes", "w toggles back")
}

// The toggles work with the form focused, which is where you are when a
// surprising answer arrives.
func TestModel_RawViewTogglesFromTheForm(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	m = applyChain(t, m, cmd)

	m, _ = press(t, m, "w")
	assert.Contains(t, m.View(), "raw bytes")
}

// While a field is being typed into, w is a w.
func TestModel_RawViewKeyIsACharacterWhileEditing(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	m = applyChain(t, m, cmd)

	m, _ = press(t, m, "enter") // start editing the first field
	m = typeText(t, m, "w")

	view := m.View()
	assert.NotContains(t, view, "raw bytes")
	assert.Contains(t, view, "w", "the character reached the field")
}

// --- diffing ----------------------------------------------------------------

func TestModel_DiffComparesWithTheLastResponse(t *testing.T) {
	client := healthyClient()

	var count int32
	client.invoke = func(_ context.Context, method grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		count++
		return &grpcclient.UnaryResponse{
			Message:  reply(method, map[string]any{"greeting": "hello", "count": count}),
			Duration: time.Millisecond,
		}, nil
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	m = applyChain(t, m, cmd)

	m, _ = press(t, m, "D")
	assert.Contains(t, m.View(), "first response",
		"there is nothing to compare the first answer with")

	m, cmd = press(t, m, "ctrl+s")
	m = applyChain(t, m, cmd)

	view := m.View()
	assert.Contains(t, view, "diff vs previous")
	assert.Contains(t, view, `- `)
	assert.Contains(t, view, `+ `)
}

// The comparison is per method: two calls to different methods must not be
// diffed against each other.
func TestModel_DiffIsPerMethod(t *testing.T) {
	client := healthyClient()
	client.invoke = func(_ context.Context, method grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		return &grpcclient.UnaryResponse{
			Message:  dynamicMessage(method),
			Duration: time.Millisecond,
		}, nil
	}

	m := settled(t, newModel(t, client))

	// demo.v1.Echo.Echo first…
	m = selectMethod(t, m, 1)
	m, cmd := press(t, m, "ctrl+s")
	m = applyChain(t, m, cmd)

	// …then demo.v1.Greeter.SayHello, which has never been called.
	m, _ = press(t, m, "shift+tab")
	m = selectMethod(t, m, 2)
	m, cmd = press(t, m, "ctrl+s")
	m = applyChain(t, m, cmd)

	m, _ = press(t, m, "D")
	assert.Contains(t, m.View(), "first response")
}

// --- latency breakdown ------------------------------------------------------

func TestModel_ShowsTheLatencyBreakdown(t *testing.T) {
	client := healthyClient()
	client.invoke = func(_ context.Context, method grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		return &grpcclient.UnaryResponse{
			Message:  dynamicMessage(method),
			Duration: 30 * time.Millisecond,
			Timing: grpcclient.Timing{
				Connect:       3 * time.Millisecond,
				FirstByte:     20 * time.Millisecond,
				Total:         28 * time.Millisecond,
				RequestBytes:  9,
				ResponseBytes: 15,
			},
		}, nil
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	m = applyChain(t, m, cmd)

	view := m.View()
	assert.Contains(t, view, "connect 3ms")
	assert.Contains(t, view, "first byte 20ms")
}

// --- grpcurl export ---------------------------------------------------------

func TestModel_ExportsTheRequestAsGrpcurl(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(), ui.WithProfiles(goldenProfiles(), 0)))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "enter")
	m = typeText(t, m, "world")
	m, _ = press(t, m, "esc")

	m, _ = press(t, m, "X")

	view := m.View()
	assert.Contains(t, view, "Export as grpcurl")
	assert.Contains(t, view, "demo.v1.Greeter/SayHello")
	assert.Contains(t, view, `"name":"world"`)

	m, _ = press(t, m, "esc")
	assert.NotContains(t, m.View(), "Export as grpcurl")
}

// The rule the whole feature is bounded by: an exported command is written to
// be pasted somewhere else, so no credential may be in it.
func TestModel_ExportNeverCarriesACredential(t *testing.T) {
	const token = "s3cret-token"

	profiles := []grpcclient.Profile{{
		Name:   "staging",
		Target: "localhost:50051",
		Auth:   grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: token},
		Metadata: grpcclient.Metadata{
			{Key: "x-tenant", Value: "acme-secret-tenant"},
		},
	}}

	m := settled(t, newModel(t, healthyClient(), ui.WithProfiles(profiles, 0)))
	m = selectMethod(t, m, 3)
	m, _ = press(t, m, "X")

	view := m.View()
	assert.NotContains(t, view, token)
	assert.NotContains(t, view, "acme-secret-tenant")
	assert.Contains(t, view, "Bearer <token>")
	assert.Contains(t, view, "x-tenant: <x-tenant>")
}

func TestModel_ExportWithoutAMethodSaysSo(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))
	m, _ = press(t, m, "X")

	assert.Contains(t, m.View(), "Select a method first.")
}

// --- passive proxy ----------------------------------------------------------

func TestModel_TrafficKeyExplainsItselfWithoutAProxy(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))
	m, _ = press(t, m, "t")

	assert.Contains(t, m.View(), "--proxy")
}

func TestModel_RecordsProxiedTraffic(t *testing.T) {
	m := proxied(t, unaryTraffic(1, "/demo.v1.Greeter/SayHello", helloRequestWire("world")))

	m, _ = press(t, m, "t")
	view := m.View()
	assert.Contains(t, view, "127.0.0.1:8080")
	assert.Contains(t, view, "SayHello")
	assert.Contains(t, view, "\u21921 \u21901")
}

// This is what makes passive mode more than a log: a request the proxy saw go
// past becomes a form you can edit and send yourself.
func TestModel_LoadsAProxiedRequestIntoTheForm(t *testing.T) {
	m := proxied(t, unaryTraffic(1, "/demo.v1.Greeter/SayHello", helloRequestWire("world")))

	m, _ = press(t, m, "t")
	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)

	view := m.View()
	assert.Contains(t, view, "world", "the request came back as a filled-in form")
	assert.Contains(t, view, "Loaded from the proxy")
}

// A proxy in front of a different service sees methods this connection has
// never heard of, which is a thing to say rather than a thing to crash on.
func TestModel_RefusesToLoadAnUnknownProxiedMethod(t *testing.T) {
	m := proxied(t, unaryTraffic(1, "/other.v1.Thing/Do", []byte{0x08, 0x01}))

	m, _ = press(t, m, "t")
	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)

	assert.Contains(t, m.View(), "other.v1.Thing.Do is not on this connection.")
}

// A request whose bytes are not the method's message is a thing to report, not
// a thing to load half of.
func TestModel_RefusesToLoadAMalformedProxiedRequest(t *testing.T) {
	// A tag claiming twenty bytes follow, with none behind it.
	m := proxied(t, unaryTraffic(1, "/demo.v1.Greeter/SayHello", []byte{0x0a, 0x20}))

	m, _ = press(t, m, "t")
	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)

	assert.Contains(t, m.View(), "decode message")
}

// The events the proxy could not hand over are reported rather than hidden.
func TestModel_ReportsDroppedProxyEvents(t *testing.T) {
	m := proxiedWith(t, func() int { return 4 },
		unaryTraffic(1, "/demo.v1.Greeter/SayHello", helloRequestWire("world")))

	m, _ = press(t, m, "t")
	assert.Contains(t, m.View(), "4 events were dropped")
}

// A closed channel ends the read loop rather than spinning on something that
// will never speak again. applyChain settling at all is the assertion: a loop
// that re-issued itself would run out of hops instead.
func TestModel_StopsReadingWhenTheProxyStops(t *testing.T) {
	m := proxied(t)
	assert.Contains(t, m.View(), "demo.v1.Greeter", "discovery still landed")
}

// --- helpers ----------------------------------------------------------------

// proxied builds a model watching a proxy that reports the given events and
// then stops.
//
// The channel is filled and closed before the model starts, so the read loop
// drains it and ends — which is what lets applyChain settle. A loop left waiting
// on an open channel would hang the test rather than fail it.
func proxied(t *testing.T, batches ...[]proxy.Event) ui.Model {
	t.Helper()
	return proxiedWith(t, nil, batches...)
}

func proxiedWith(t *testing.T, dropped func() int, batches ...[]proxy.Event) ui.Model {
	t.Helper()

	var events []proxy.Event
	for _, batch := range batches {
		events = append(events, batch...)
	}

	ch := make(chan proxy.Event, len(events)+1)
	for _, event := range events {
		ch <- event
	}
	close(ch)

	m := sized(t, newModel(t, healthyClient(),
		ui.WithTraffic(ch, "127.0.0.1:8080", dropped)))
	return applyChain(t, m, m.Init())
}

// unaryTraffic is the four events one proxied unary call produces.
func unaryTraffic(call int, method string, request []byte) []proxy.Event {
	at := time.Now()
	return []proxy.Event{
		{Call: call, Kind: proxy.Started, Method: method, Peer: "10.0.0.1:5555", At: at},
		{Call: call, Kind: proxy.Sent, Method: method, Wire: request, At: at},
		{Call: call, Kind: proxy.Received, Method: method, At: at},
		{Call: call, Kind: proxy.Ended, Method: method, At: at},
	}
}

// helloRequestWire is a demo.v1.HelloRequest with its name set: field 1, a
// string.
func helloRequestWire(name string) []byte {
	return append([]byte{0x0a, byte(len(name))}, name...)
}

// dynamicMessage is an empty response of the method's output type.
func dynamicMessage(method grpcclient.Method) proto.Message {
	return reply(method, nil)
}
