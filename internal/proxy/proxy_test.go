package proxy_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/alonshuld/grpctui/internal/proxy"
)

const bufSize = 1 << 20

// bufTarget bypasses the DNS resolver grpc.NewClient uses by default: neither
// of these names is real, and letting one reach a resolver turns the test into
// a timeout.
const bufTarget = "passthrough:///bufnet"

// harness is a real health server, a real proxy in front of it, and a real
// client talking to the proxy — three gRPC stacks and no port between them.
//
// The whole point of this package is what happens on the wire, so there is
// nothing useful to test with a fake: a proxy that forwards correctly to a mock
// tells you only that the mock was called.
type harness struct {
	client healthpb.HealthClient
	proxy  *proxy.Proxy
	health *health.Server

	// received is the metadata the upstream server actually saw, which is the
	// only way to check that the proxy changed nothing on the way through.
	mu       sync.Mutex
	received metadata.MD
}

func newHarness(t *testing.T, opts ...proxy.Option) *harness {
	t.Helper()

	h := &harness{}

	// The upstream server, with an interceptor that records what reached it.
	upstreamLis := bufconn.Listen(bufSize)
	upstream := grpc.NewServer(grpc.UnaryInterceptor(
		func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			h.mu.Lock()
			h.received = md.Copy()
			h.mu.Unlock()
			return handler(ctx, req)
		}))
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(upstream, healthServer)
	go func() { _ = upstream.Serve(upstreamLis) }()

	// The connection the proxy forwards over.
	forward, err := grpc.NewClient(bufTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return upstreamLis.DialContext(ctx)
		}),
	)
	require.NoError(t, err)

	// The proxy, on a listener of its own.
	proxyLis := bufconn.Listen(bufSize)
	p := proxy.New("", forward, append([]proxy.Option{
		proxy.WithListener(proxyLis),
		proxy.WithLogger(zaptest.NewLogger(t)),
	}, opts...)...)
	served := make(chan error, 1)
	go func() { served <- p.Serve() }()

	// The observed client, pointed at the proxy rather than at the server.
	conn, err := grpc.NewClient(bufTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return proxyLis.DialContext(ctx)
		}),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = conn.Close()
		p.Stop()
		<-served
		_ = forward.Close()
		upstream.Stop()
		_ = upstreamLis.Close()
	})

	h.client, h.proxy, h.health = healthpb.NewHealthClient(conn), p, healthServer
	return h
}

// upstreamMetadata is what the server behind the proxy received.
func (h *harness) upstreamMetadata() metadata.MD {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.received
}

// collect reads events until one of kind [proxy.Ended] arrives, or the test
// gives up. Every call ends, so waiting for that is what makes the assertions
// below deterministic rather than timing-dependent.
func (h *harness) collect(t *testing.T) []proxy.Event {
	t.Helper()

	var events []proxy.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-h.proxy.Events():
			require.True(t, ok, "the event channel closed before the call ended")
			events = append(events, event)
			if event.Kind == proxy.Ended {
				return events
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the call to end; got %d events", len(events))
			return nil
		}
	}
}

func kinds(events []proxy.Event) []proxy.EventKind {
	out := make([]proxy.EventKind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestProxyForwardsAUnaryCall(t *testing.T) {
	h := newHarness(t)

	resp, err := h.client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())

	events := h.collect(t)
	assert.Equal(t,
		[]proxy.EventKind{proxy.Started, proxy.Sent, proxy.Received, proxy.Ended},
		kinds(events))

	for _, e := range events {
		assert.Equal(t, "/grpc.health.v1.Health/Check", e.Method)
		assert.Equal(t, 1, e.Call)
	}
	assert.False(t, events[3].HasStatus, "a call that succeeded has no status to report")
}

// The bytes reported have to be the message, not a description of it: they are
// what the raw view renders and what a replay is rebuilt from.
func TestProxyReportsTheMessageBytes(t *testing.T) {
	h := newHarness(t)

	_, err := h.client.Check(context.Background(),
		&healthpb.HealthCheckRequest{Service: ""})
	require.NoError(t, err)

	events := h.collect(t)
	require.Len(t, events, 4)

	sent := events[1]
	require.Equal(t, proxy.Sent, sent.Kind)
	assert.False(t, sent.Truncated)

	received := events[2]
	require.Equal(t, proxy.Received, received.Kind)

	// SERVING is 1 in field 1, which is a varint: tag 0x08, value 0x01.
	assert.Equal(t, []byte{0x08, 0x01}, received.Wire)
}

func TestProxyCarriesTheStatusOfAFailedCall(t *testing.T) {
	h := newHarness(t)

	_, err := h.client.Check(context.Background(),
		&healthpb.HealthCheckRequest{Service: "nobody.serves.this"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))

	events := h.collect(t)
	end := events[len(events)-1]

	require.Equal(t, proxy.Ended, end.Kind)
	require.True(t, end.HasStatus)
	assert.Equal(t, "NotFound", end.Status.Name)
	assert.EqualValues(t, codes.NotFound, end.Status.Code)
}

func TestProxyForwardsAServerStream(t *testing.T) {
	h := newHarness(t)

	stream, err := h.client.Watch(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)

	// The health server sends the current status immediately, then again on
	// every change — so two messages come back for one sent.
	first, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, first.GetStatus())

	h.health.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	second, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, second.GetStatus())

	// Ending the call from the client side is what a user pressing esc does.
	h.proxy.Stop()

	var received int
	for event := range h.proxy.Events() {
		if event.Kind == proxy.Received {
			received++
		}
	}
	assert.GreaterOrEqual(t, received, 2, "both streamed messages should have been reported")
}

// A proxy that changed the metadata would be showing the user something the
// observed client never sent, which is the one thing a wire-watching tool may
// not do. So the client's own headers arrive intact — and nothing else does.
func TestProxyPassesMetadataThrough(t *testing.T) {
	h := newHarness(t)

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-tenant", "acme")
	_, err := h.client.Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)

	got := h.upstreamMetadata()
	require.NotNil(t, got, "the interceptor never ran")
	assert.Equal(t, []string{"acme"}, got.Get("x-tenant"))
	assert.Empty(t, got.Get("authorization"),
		"the proxy must not add a credential of its own")
}

func TestProxyNumbersConcurrentCalls(t *testing.T) {
	h := newHarness(t)

	const calls = 3
	for range calls {
		_, err := h.client.Check(context.Background(), &healthpb.HealthCheckRequest{})
		require.NoError(t, err)
	}

	seen := make(map[int]bool)
	for range calls {
		for _, event := range h.collect(t) {
			seen[event.Call] = true
		}
	}
	assert.Equal(t, map[int]bool{1: true, 2: true, 3: true}, seen)
}

// Nothing may block the traffic being watched, so an event nobody reads is
// dropped and counted rather than waited on.
func TestProxyDropsEventsRatherThanBlocking(t *testing.T) {
	// Nothing reads the channel in this test, so a proxy whose buffer holds one
	// event has to lose the rest — and, crucially, the calls still succeed.
	h := newHarness(t, proxy.WithBuffer(1))
	assert.Equal(t, 0, h.proxy.Dropped())

	for range 5 {
		_, err := h.client.Check(context.Background(), &healthpb.HealthCheckRequest{})
		require.NoError(t, err, "a full event buffer must not fail the traffic")
	}
	assert.Positive(t, h.proxy.Dropped())
}

func TestProxyRefusesWithoutAnUpstream(t *testing.T) {
	p := proxy.New("127.0.0.1:0", nil)
	assert.Error(t, p.Listen())
}

func TestProxyEventsCloseWhenItStops(t *testing.T) {
	h := newHarness(t)
	h.proxy.Stop()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-h.proxy.Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the event channel did not close")
		}
	}
}

func TestEventKindString(t *testing.T) {
	for kind, want := range map[proxy.EventKind]string{
		proxy.Started:      "started",
		proxy.Sent:         "sent",
		proxy.Received:     "received",
		proxy.Ended:        "ended",
		proxy.EventKind(9): "unknown",
	} {
		assert.Equal(t, want, kind.String())
	}
}

// A listener the caller supplied is used as it stands, which is what lets the
// tests above run without a port.
func TestProxyAddr(t *testing.T) {
	lis := bufconn.Listen(bufSize)
	t.Cleanup(func() { _ = lis.Close() })

	p := proxy.New("127.0.0.1:9999", nil, proxy.WithListener(lis))
	assert.Equal(t, lis.Addr().String(), p.Addr())

	assert.Equal(t, "127.0.0.1:9999", proxy.New("127.0.0.1:9999", nil).Addr(),
		"before binding, the address asked for is the best answer there is")
}
