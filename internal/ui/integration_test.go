package ui_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/test/bufconn"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui"
)

// The rest of this package's tests stub the transport layer. This one wires the
// real one in — a real gRPC server over bufconn, real reflection, the real
// grpcclient — so that a mistake at the seam between the layers cannot hide
// behind a fake. It still needs no network and no TTY.
func TestIntegration_DiscoverAndBrowseARealServer(t *testing.T) {
	client := realClient(t)
	m := settled(t, ui.New(client, ui.WithLogger(zaptest.NewLogger(t))))

	view := m.View()
	require.Contains(t, view, "grpc.health.v1.Health",
		"reflection did not reach the tree:\n%s", view)
	assert.Contains(t, view, "Check")
	assert.Contains(t, view, "Watch «stream", "Watch is server-streaming")

	// Walk down to Check and select it; the request form should be built from
	// the real descriptor, not a fixture.
	m, cmd := press(t, m, "j", "enter")
	require.NotNil(t, cmd)
	m = asModel(t, mustUpdate(m, cmd()))

	view = m.View()
	assert.Contains(t, view, "grpc.health.v1.HealthCheckRequest")
	assert.Contains(t, view, "grpc.health.v1.HealthCheckResponse")
	assert.Contains(t, view, "unary")
	assert.Contains(t, view, "service", "the form found HealthCheckRequest's one field")
}

// The whole v0.2 cycle over a real server: discover, fill the form, send, and
// read the decoded response — with the real grpcclient, real reflection, real
// dynamic invocation, and a real protobuf response.
func TestIntegration_SendAUnaryRequest(t *testing.T) {
	client := realClient(t)
	m := settled(t, ui.New(client, ui.WithLogger(zaptest.NewLogger(t))))

	// grpc.health.v1.Health is the only service; Check is its first method.
	m = selectMethod(t, m, 1)
	require.Contains(t, m.View(), "grpc.health.v1.Health.Check")

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd, "ctrl+s must start a call")
	m = apply(t, m, cmd)

	view := m.View()
	assert.Contains(t, view, "OK")
	assert.Contains(t, view, `"status": "SERVING"`,
		"the health server's response did not reach the panel:\n%s", view)
}

// A server answering NotFound is the service working correctly, and the status
// has to arrive intact rather than as a generic failure.
func TestIntegration_UnaryRequestWithAFailingStatus(t *testing.T) {
	client := realClient(t)
	m := settled(t, ui.New(client, ui.WithLogger(zaptest.NewLogger(t))))

	m = selectMethod(t, m, 1)

	// Ask after a service the health server has never heard of.
	m, _ = press(t, m, "enter")
	m = typeText(t, m, "no.such.Service")
	m, _ = press(t, m, "esc")

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)
	m = apply(t, m, cmd)

	view := m.View()
	assert.Contains(t, view, "NotFound (5)")
	assert.NotContains(t, view, "OK  ")
}

func TestIntegration_ReflectionUnavailable(t *testing.T) {
	client := realClient(t, withoutReflection())
	m := settled(t, ui.New(client, ui.WithLogger(zaptest.NewLogger(t))))

	view := m.View()
	assert.Contains(t, view, "Server reflection unavailable")
	assert.Contains(t, view, "Unimplemented",
		"the gRPC status must reach the screen intact")
}

type serverOption func(*serverConfig)

type serverConfig struct{ reflect bool }

func withoutReflection() serverOption {
	return func(cfg *serverConfig) { cfg.reflect = false }
}

// realClient starts an in-memory gRPC server serving the health service (which
// is registered in the global proto registry, so no generated code is needed)
// and returns a real client pointed at it.
func realClient(t *testing.T, opts ...serverOption) *grpcclient.Client {
	t.Helper()

	cfg := serverConfig{reflect: true}
	for _, opt := range opts {
		opt(&cfg)
	}

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, health.NewServer())
	if cfg.reflect {
		reflection.Register(srv)
	}

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	// passthrough:// keeps grpc.NewClient's default DNS resolver away from the
	// fake "bufnet" name.
	client, err := grpcclient.Dial("passthrough:///bufnet",
		grpcclient.WithLogger(zaptest.NewLogger(t)),
		grpcclient.WithGRPCDialOptions(
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return client
}
