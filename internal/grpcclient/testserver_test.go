package grpcclient_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/test/bufconn"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

const bufSize = 1024 * 1024

// testServer is an in-memory gRPC server. It serves the health service —
// already registered in the global proto registry, so the tests need no
// generated code — which conveniently has one unary method (Check) and one
// server-streaming method (Watch).
type testServer struct {
	lis    *bufconn.Listener
	server *grpc.Server
}

type serverOption func(*serverConfig)

type serverConfig struct {
	reflect bool
	health  bool
	delay   time.Duration
	serving []string
}

// withServingService makes the health server answer SERVING for name, so that a
// call carrying a chosen payload succeeds instead of coming back NotFound.
func withServingService(name string) serverOption {
	return func(cfg *serverConfig) { cfg.serving = append(cfg.serving, name) }
}

// withoutReflection starts a server that does not serve the reflection API.
func withoutReflection() serverOption {
	return func(cfg *serverConfig) { cfg.reflect = false }
}

// withSlowUnary makes every unary call take d, so that cancellation and
// deadlines have something to interrupt. Reflection is a streaming RPC and is
// left at full speed.
func withSlowUnary(d time.Duration) serverOption {
	return func(cfg *serverConfig) { cfg.delay = d }
}

func startTestServer(t *testing.T, opts ...serverOption) *testServer {
	t.Helper()

	cfg := serverConfig{reflect: true, health: true}
	for _, opt := range opts {
		opt(&cfg)
	}

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(grpc.UnaryInterceptor(delayInterceptor(cfg.delay)))
	if cfg.health {
		hs := health.NewServer()
		for _, name := range cfg.serving {
			hs.SetServingStatus(name, healthpb.HealthCheckResponse_SERVING)
		}
		healthpb.RegisterHealthServer(srv, hs)
	}
	if cfg.reflect {
		reflection.Register(srv)
	}

	ts := &testServer{lis: lis, server: srv}
	go func() {
		// Serve returns ErrServerStopped on Stop; nothing to assert here.
		_ = srv.Serve(lis)
	}()
	t.Cleanup(ts.stop)
	return ts
}

// delayInterceptor holds every unary call for d, or until the client gives up.
func delayInterceptor(d time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if d <= 0 {
			return handler(ctx, req)
		}
		select {
		case <-time.After(d):
			return handler(ctx, req)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// dialer returns a grpc dial option that routes connections to this server.
func (ts *testServer) dialer() grpc.DialOption {
	return grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return ts.lis.DialContext(ctx)
	})
}

// stop shuts the server down immediately. Safe to call more than once.
func (ts *testServer) stop() {
	ts.server.Stop()
	_ = ts.lis.Close()
}

// bufTarget bypasses the DNS resolver that grpc.NewClient uses by default —
// "bufnet" is not a real name, and letting it reach a resolver turns every
// test into a DNS timeout.
const bufTarget = "passthrough:///bufnet"

// client dials ts and registers cleanup.
func (ts *testServer) client(t *testing.T) *grpcclient.Client {
	t.Helper()
	c, err := grpcclient.Dial(bufTarget,
		grpcclient.WithGRPCDialOptions(ts.dialer()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}
