package grpcclient_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// healthMethod discovers one method of the health service over reflection, the
// same way the UI gets hold of it.
func healthMethod(t *testing.T, c *grpcclient.Client, name string) grpcclient.Method {
	t.Helper()

	services, err := c.ListServices(context.Background())
	require.NoError(t, err)

	svc, ok := findService(services, healthService)
	require.True(t, ok, "%s not discovered", healthService)

	m, ok := findMethod(svc, name)
	require.True(t, ok, "%s.%s not discovered", healthService, name)
	return m
}

// checkRequest builds a HealthCheckRequest for a service name.
func checkRequest(t *testing.T, m grpcclient.Method, service string) proto.Message {
	t.Helper()

	md := m.InputDescriptor()
	require.NotNil(t, md)

	msg := dynamicpb.NewMessage(md)
	fd := md.Fields().ByName("service")
	require.NotNil(t, fd, "HealthCheckRequest has no service field")
	msg.Set(fd, protoreflect.ValueOfString(service))
	return msg
}

// responseField reads a field off a decoded response.
func responseField(t *testing.T, msg proto.Message, name string) protoreflect.Value {
	t.Helper()

	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "no field %q on %s", name, m.Descriptor().FullName())
	return m.Get(fd)
}

func TestClient_InvokeUnary(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	resp, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""))

	require.NoError(t, err)
	require.NotNil(t, resp)

	t.Run("decodes the response against the output descriptor", func(t *testing.T) {
		assert.Equal(t, "grpc.health.v1.HealthCheckResponse",
			string(resp.Message.ProtoReflect().Descriptor().FullName()))

		// 1 is SERVING; the health server reports the overall server that way.
		assert.EqualValues(t, 1, responseField(t, resp.Message, "status").Enum())
	})

	t.Run("times the call", func(t *testing.T) {
		assert.Positive(t, resp.Duration)
	})
}

// A server answering NotFound is the service working correctly. The status has
// to survive the trip to the UI intact, because it is the answer.
func TestClient_InvokeUnary_NonOKStatus(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	resp, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, "no.such.Service"))

	require.Error(t, err)
	assert.Nil(t, resp)

	st, ok := status.FromError(err)
	require.True(t, ok, "the gRPC status did not survive wrapping: %v", err)
	assert.Equal(t, codes.NotFound, st.Code())

	callStatus, ok := grpcclient.StatusOf(err)
	require.True(t, ok)
	assert.Equal(t, "NotFound", callStatus.Name)
	assert.EqualValues(t, codes.NotFound, callStatus.Code)
	assert.Contains(t, err.Error(), "grpc.health.v1.Health.Check",
		"the error must say which method failed")
}

func TestClient_InvokeUnary_ContextCancelled(t *testing.T) {
	ts := startTestServer(t, withSlowUnary(5*time.Second))
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	start := time.Now()
	_, err := c.InvokeUnary(ctx, method, checkRequest(t, method, ""))

	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "cancelling did not abort the call")

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Canceled, st.Code())
}

func TestClient_InvokeUnary_DeadlineExceeded(t *testing.T) {
	ts := startTestServer(t, withSlowUnary(5*time.Second))
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.InvokeUnary(ctx, method, checkRequest(t, method, ""))

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.DeadlineExceeded, st.Code())
}

func TestClient_InvokeUnary_ServerUnreachable(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	method := healthMethod(t, c, "Check")
	ts.stop()

	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""))

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unavailable, st.Code())
}

// A server that dies mid-call must surface as a failed call, not as a hang.
func TestClient_InvokeUnary_ServerClosesMidCall(t *testing.T) {
	ts := startTestServer(t, withSlowUnary(5*time.Second))
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	go func() {
		time.Sleep(20 * time.Millisecond)
		ts.stop()
	}()

	start := time.Now()
	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""))

	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "the call outlived the server")
	_, ok := status.FromError(err)
	assert.True(t, ok, "a dead server must still produce a gRPC status: %v", err)
}

func TestClient_InvokeUnary_RejectsStreamingMethods(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	method := healthMethod(t, c, "Watch")
	require.Equal(t, grpcclient.KindServerStreaming, method.Kind())

	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""))

	require.ErrorIs(t, err, grpcclient.ErrStreamingUnsupported)
}

func TestClient_InvokeUnary_RejectsBadArguments(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	t.Run("no descriptor", func(t *testing.T) {
		_, err := c.InvokeUnary(context.Background(),
			grpcclient.Method{FullName: "demo.v1.Demo.Do"}, checkRequest(t, method, ""))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no descriptor")
	})

	t.Run("no request", func(t *testing.T) {
		_, err := c.InvokeUnary(context.Background(), method, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no request message")
	})
}

func TestStatusOf(t *testing.T) {
	tests := map[string]struct {
		err      error
		wantOK   bool
		wantName string
		wantCode uint32
		wantMsg  string
	}{
		"nil": {err: nil},
		"plain error": {
			err: errors.New("build request: bad value"),
		},
		"bare status": {
			err:      status.Error(codes.NotFound, "no such thing"),
			wantOK:   true,
			wantName: "NotFound",
			wantCode: uint32(codes.NotFound),
			wantMsg:  "no such thing",
		},
		"wrapped status": {
			err:      fmt.Errorf("invoke demo.v1.Demo.Do: %w", status.Error(codes.Unavailable, "connection refused")),
			wantOK:   true,
			wantName: "Unavailable",
			wantCode: uint32(codes.Unavailable),
			wantMsg:  "connection refused",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := grpcclient.StatusOf(tt.err)

			assert.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			assert.Equal(t, tt.wantName, got.Name)
			assert.Equal(t, tt.wantCode, got.Code)
			assert.Equal(t, tt.wantMsg, got.Message)
		})
	}
}

func TestCallStatus_String(t *testing.T) {
	assert.Equal(t, "NotFound (5): no such thing",
		grpcclient.CallStatus{Code: 5, Name: "NotFound", Message: "no such thing"}.String())
	assert.Equal(t, "Canceled (1)",
		grpcclient.CallStatus{Code: 1, Name: "Canceled"}.String())
}
