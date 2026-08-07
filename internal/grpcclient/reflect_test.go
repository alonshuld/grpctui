package grpcclient_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

const healthService = "grpc.health.v1.Health"

func findService(services []grpcclient.Service, name string) (grpcclient.Service, bool) {
	for _, svc := range services {
		if svc.Name == name {
			return svc, true
		}
	}
	return grpcclient.Service{}, false
}

func findMethod(svc grpcclient.Service, name string) (grpcclient.Method, bool) {
	for _, m := range svc.Methods {
		if m.Name == name {
			return m, true
		}
	}
	return grpcclient.Method{}, false
}

func TestClient_ListServices(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)

	services, err := c.ListServices(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, services)

	t.Run("returns services sorted by name", func(t *testing.T) {
		names := make([]string, len(services))
		for i, svc := range services {
			names[i] = svc.Name
		}
		assert.IsIncreasing(t, names)
	})

	t.Run("discovers the registered service", func(t *testing.T) {
		svc, ok := findService(services, healthService)
		require.True(t, ok, "expected %s in %v", healthService, services)

		// Asserted by membership, not by count: grpc-go has added methods to
		// the health service before and may again.
		names := make([]string, len(svc.Methods))
		for i, m := range svc.Methods {
			names[i] = m.Name
		}
		assert.Subset(t, names, []string{"Check", "Watch"})
	})

	t.Run("reports method shape", func(t *testing.T) {
		svc, ok := findService(services, healthService)
		require.True(t, ok)

		tests := map[string]struct {
			method     string
			wantKind   grpcclient.Kind
			wantInput  string
			wantOutput string
		}{
			"unary": {
				method:     "Check",
				wantKind:   grpcclient.KindUnary,
				wantInput:  "grpc.health.v1.HealthCheckRequest",
				wantOutput: "grpc.health.v1.HealthCheckResponse",
			},
			"server streaming": {
				method:     "Watch",
				wantKind:   grpcclient.KindServerStreaming,
				wantInput:  "grpc.health.v1.HealthCheckRequest",
				wantOutput: "grpc.health.v1.HealthCheckResponse",
			},
		}

		for name, tt := range tests {
			t.Run(name, func(t *testing.T) {
				m, ok := findMethod(svc, tt.method)
				require.True(t, ok, "method %q not found", tt.method)

				assert.Equal(t, healthService+"."+tt.method, m.FullName)
				assert.Equal(t, tt.wantKind, m.Kind())
				assert.Equal(t, tt.wantInput, m.InputType)
				assert.Equal(t, tt.wantOutput, m.OutputType)
				assert.NotNil(t, m.Descriptor, "descriptor must survive for protoschema")
			})
		}
	})
}

func TestClient_ListServices_ReflectionUnavailable(t *testing.T) {
	ts := startTestServer(t, withoutReflection())
	c := ts.client(t)

	_, err := c.ListServices(context.Background(), nil)

	require.Error(t, err)
	require.ErrorIs(t, err, grpcclient.ErrReflectionUnavailable)

	st, ok := status.FromError(err)
	require.True(t, ok, "gRPC status must survive the wrap: %v", err)
	assert.Equal(t, codes.Unimplemented, st.Code())
}

func TestClient_ListServices_ServerDown(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	ts.stop()

	_, err := c.ListServices(context.Background(), nil)

	require.Error(t, err)
	require.NotErrorIs(t, err, grpcclient.ErrReflectionUnavailable)

	st, ok := status.FromError(err)
	require.True(t, ok, "gRPC status must survive the wrap: %v", err)
	assert.Equal(t, codes.Unavailable, st.Code())
}

func TestClient_ListServices_ContextCancelled(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.ListServices(ctx, nil)

	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok, "gRPC status must survive the wrap: %v", err)
	assert.Equal(t, codes.Canceled, st.Code())
}

func TestClient_ListServices_DeadlineExceeded(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	_, err := c.ListServices(ctx, nil)

	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok, "gRPC status must survive the wrap: %v", err)
	assert.Equal(t, codes.DeadlineExceeded, st.Code())
}

func TestMethod_Kind(t *testing.T) {
	tests := map[string]struct {
		client, server bool
		want           grpcclient.Kind
	}{
		"unary":            {false, false, grpcclient.KindUnary},
		"server streaming": {false, true, grpcclient.KindServerStreaming},
		"client streaming": {true, false, grpcclient.KindClientStreaming},
		"bidi streaming":   {true, true, grpcclient.KindBidiStreaming},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			m := grpcclient.Method{ClientStreaming: tt.client, ServerStreaming: tt.server}
			assert.Equal(t, tt.want, m.Kind())
		})
	}
}

func TestDial_EmptyTarget(t *testing.T) {
	_, err := grpcclient.Dial("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty target")
}

func TestClient_Target(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	assert.Equal(t, bufTarget, c.Target())
}

func TestClient_Close_Idempotent(t *testing.T) {
	ts := startTestServer(t)
	c, err := grpcclient.Dial(bufTarget,
		grpcclient.WithGRPCDialOptions(ts.dialer()))
	require.NoError(t, err)

	require.NoError(t, c.Close())
	// A second Close reports the already-closed error rather than panicking.
	require.Error(t, c.Close())

	var nilClient *grpcclient.Client
	assert.NoError(t, nilClient.Close())
}
