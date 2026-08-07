package grpcclient_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
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
	creds   credentials.TransportCredentials

	// unknown, when set, answers every method of a service the server does not
	// have registered — which is how the streaming tests serve a service that
	// has no generated code behind it at all.
	unknown grpc.StreamHandler

	// headers, when set, records the metadata of every incoming RPC — which is
	// the only way to assert from the outside that a header the client was told
	// to send actually reached the server.
	headers *headerRecorder
}

// withServerTLS serves TLS with cert/key. clientCAs, when non-nil, makes the
// server demand a client certificate signed by it: mutual TLS.
func withServerTLS(t *testing.T, certPath, keyPath string, clientCAs *x509.CertPool) serverOption {
	t.Helper()

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	require.NoError(t, err)

	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if clientCAs != nil {
		cfg.ClientCAs = clientCAs
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return func(sc *serverConfig) { sc.creds = credentials.NewTLS(cfg) }
}

// withHeaderCapture records the metadata every RPC arrives with.
func withHeaderCapture(rec *headerRecorder) serverOption {
	return func(sc *serverConfig) { sc.headers = rec }
}

// headerRecorder collects incoming request metadata across the calls of one
// test. Reflection is a stream and calls are unary, and a test asserting on a
// header wants whichever of them carried it.
type headerRecorder struct {
	mu   sync.Mutex
	seen []metadata.MD
}

func (r *headerRecorder) record(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, md.Copy())
}

// values returns every value seen for key, across every recorded call.
func (r *headerRecorder) values(key string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []string
	for _, md := range r.seen {
		out = append(out, md.Get(key)...)
	}
	return out
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

	srvOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(recordUnary(cfg.headers), delayInterceptor(cfg.delay)),
		grpc.ChainStreamInterceptor(recordStream(cfg.headers)),
	}
	if cfg.creds != nil {
		srvOpts = append(srvOpts, grpc.Creds(cfg.creds))
	}
	if cfg.unknown != nil {
		srvOpts = append(srvOpts, grpc.UnknownServiceHandler(cfg.unknown))
	}

	srv := grpc.NewServer(srvOpts...)
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

// recordUnary and recordStream feed rec the metadata of every incoming call.
func recordUnary(rec *headerRecorder) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if rec != nil {
			rec.record(ctx)
		}
		return handler(ctx, req)
	}
}

func recordStream(rec *headerRecorder) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if rec != nil {
			rec.record(ss.Context())
		}
		return handler(srv, ss)
	}
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
func (ts *testServer) client(t *testing.T, opts ...grpcclient.DialOption) *grpcclient.Client {
	t.Helper()

	c, err := ts.dial(t, opts...)
	require.NoError(t, err)
	return c
}

// dial is [testServer.client] for the tests that expect dialling itself to
// fail.
func (ts *testServer) dial(t *testing.T, opts ...grpcclient.DialOption) (*grpcclient.Client, error) {
	t.Helper()

	opts = append(opts, grpcclient.WithGRPCDialOptions(ts.dialer()))
	c, err := grpcclient.Dial(bufTarget, opts...)
	if c != nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}
