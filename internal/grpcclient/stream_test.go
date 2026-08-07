package grpcclient_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// openStream opens a stream against the demo service and closes it at the end
// of the test, the way the UI closes one when the user moves on.
func openStream(t *testing.T, c *grpcclient.Client, method grpcclient.Method, md grpcclient.Metadata) grpcclient.Stream {
	t.Helper()

	s, err := c.InvokeStream(context.Background(), method, md)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// drain reads the rest of a stream, returning the messages it carried and the
// error that ended it — io.EOF when the call succeeded.
func drain(t *testing.T, s grpcclient.Stream) ([]proto.Message, error) {
	t.Helper()

	var msgs []proto.Message
	for {
		msg, err := s.Recv()
		if err != nil {
			return msgs, err
		}
		msgs = append(msgs, msg)
	}
}

func TestClient_InvokeStream_ServerStreaming(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Ticks")
	require.Equal(t, grpcclient.KindServerStreaming, method.Kind())

	s := openStream(t, c, method, nil)
	require.NoError(t, s.Send(item(t, method, "tick")))
	require.NoError(t, s.CloseSend())

	msgs, err := drain(t, s)

	require.ErrorIs(t, err, io.EOF, "a completed stream ends in io.EOF")
	require.Len(t, msgs, ticks)
	for i, msg := range msgs {
		assert.Equal(t, "tick-"+string(rune('1'+i)), itemText(t, msg))
	}
	assert.Equal(t, method.FullName, s.Method().FullName)
}

func TestClient_InvokeStream_ClientStreaming(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Collect")
	require.Equal(t, grpcclient.KindClientStreaming, method.Kind())

	s := openStream(t, c, method, nil)
	for _, word := range []string{"one", "two", "three"} {
		require.NoError(t, s.Send(item(t, method, word)))
	}
	require.NoError(t, s.CloseSend())

	msgs, err := drain(t, s)

	require.ErrorIs(t, err, io.EOF)
	require.Len(t, msgs, 1, "a client-streaming call answers exactly once")
	assert.EqualValues(t, 3, responseField(t, msgs[0], "count").Int())
}

func TestClient_InvokeStream_Bidi(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Chat")
	require.Equal(t, grpcclient.KindBidiStreaming, method.Kind())

	s := openStream(t, c, method, nil)

	// Interleaved, not batched: each answer comes back before the next question
	// goes out, which is the property that separates bidi from the other three.
	for _, word := range []string{"hello", "again"} {
		require.NoError(t, s.Send(item(t, method, word)))

		msg, err := s.Recv()
		require.NoError(t, err)
		assert.Equal(t, "echo: "+word, itemText(t, msg))
	}

	require.NoError(t, s.CloseSend())
	_, err := s.Recv()
	assert.ErrorIs(t, err, io.EOF)
}

func TestClient_InvokeStream_NonOKStatus(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Fail")

	s := openStream(t, c, method, nil)
	require.NoError(t, s.Send(item(t, method, "watch")))
	require.NoError(t, s.CloseSend())

	msgs, err := drain(t, s)

	require.Len(t, msgs, 1, "the message sent before the failure still arrives")
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF, "a failed stream must not look like a finished one")

	st, ok := grpcclient.StatusOf(err)
	require.True(t, ok, "the stream's status must survive the wrap: %v", err)
	assert.Equal(t, "PermissionDenied", st.Name)
	assert.Equal(t, "not allowed to watch this", st.Message)
}

func TestClient_InvokeStream_ContextCancelled(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Chat")

	ctx, cancel := context.WithCancel(context.Background())
	s, err := c.InvokeStream(ctx, method, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cancel()

	_, err = s.Recv()
	require.Error(t, err)
	st, ok := grpcclient.StatusOf(err)
	require.True(t, ok)
	assert.Equal(t, codes.Canceled.String(), st.Name)
}

func TestClient_InvokeStream_Close(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Chat")

	s, err := c.InvokeStream(context.Background(), method, nil)
	require.NoError(t, err)

	require.NoError(t, s.Close())
	require.NoError(t, s.Close(), "closing twice is how the UI can close unconditionally")

	_, err = s.Recv()
	st, ok := grpcclient.StatusOf(err)
	require.True(t, ok)
	assert.Equal(t, codes.Canceled.String(), st.Name)
}

func TestClient_InvokeStream_SendAfterCloseSend(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Collect")

	s := openStream(t, c, method, nil)
	require.NoError(t, s.Send(item(t, method, "one")))
	require.NoError(t, s.CloseSend())
	require.NoError(t, s.CloseSend(), "closing a closed sending half is a no-op")

	err := s.Send(item(t, method, "two"))

	assert.ErrorIs(t, err, grpcclient.ErrSendClosed)
}

func TestClient_InvokeStream_ServerClosesMidStream(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Chat")

	s := openStream(t, c, method, nil)
	require.NoError(t, s.Send(item(t, method, "hello")))
	_, err := s.Recv()
	require.NoError(t, err)

	ts.stop()

	start := time.Now()
	_, err = s.Recv()

	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "the stream outlived the server")
	_, ok := status.FromError(err)
	assert.True(t, ok, "a dead server must still produce a gRPC status: %v", err)
}

func TestClient_InvokeStream_SendsMetadata(t *testing.T) {
	rec := &headerRecorder{}
	ts := startTestServer(t, withStreamer(), withHeaderCapture(rec))
	c := ts.client(t)
	method := streamerMethod(t, "Ticks")

	md := grpcclient.Metadata{
		{Key: "x-tenant", Value: "acme"},
		{Key: "x-off", Value: "no", Disabled: true},
	}

	s := openStream(t, c, method, md)
	require.NoError(t, s.Send(item(t, method, "tick")))
	require.NoError(t, s.CloseSend())
	_, err := drain(t, s)
	require.ErrorIs(t, err, io.EOF)

	assert.Equal(t, []string{"acme"}, rec.values("x-tenant"))
	assert.Empty(t, rec.values("x-off"), "a disabled header must not reach the wire")
}

func TestClient_InvokeStream_RejectsBadArguments(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)

	t.Run("unary method", func(t *testing.T) {
		method := healthMethod(t, c, "Check")

		_, err := c.InvokeStream(context.Background(), method, nil)

		assert.ErrorIs(t, err, grpcclient.ErrNotStreaming)
	})

	t.Run("no descriptor", func(t *testing.T) {
		_, err := c.InvokeStream(context.Background(),
			grpcclient.Method{FullName: "demo.v1.Demo.Do", ServerStreaming: true}, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no descriptor")
	})

	t.Run("no request message", func(t *testing.T) {
		s := openStream(t, c, streamerMethod(t, "Chat"), nil)

		err := s.Send(nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no request message")
	})

	t.Run("malformed header", func(t *testing.T) {
		md := grpcclient.Metadata{{Key: "grpc-internal", Value: "no"}}

		_, err := c.InvokeStream(context.Background(), streamerMethod(t, "Ticks"), md)

		assert.ErrorIs(t, err, grpcclient.ErrReservedHeader)
	})
}

func TestClient_InvokeStream_ReflectionDiscoveredMethod(t *testing.T) {
	// The health service's Watch is a real server-streaming method on a real
	// registered service, so this is the whole path the UI walks: discover over
	// reflection, then stream against what came back.
	ts := startTestServer(t, withServingService(""))
	c := ts.client(t)
	method := healthMethod(t, c, "Watch")

	s := openStream(t, c, method, nil)
	require.NoError(t, s.Send(checkRequest(t, method, "")))
	require.NoError(t, s.CloseSend())

	// Watch reports the current status and then stays open, so exactly one
	// message is read rather than the stream drained.
	msg, err := s.Recv()

	require.NoError(t, err)
	assert.Equal(t, "SERVING", enumName(t, msg, "status"))
}

// enumName reads an enum field off a decoded response as its declared name,
// which is what a failure message wants to say rather than a number.
func enumName(t *testing.T, msg proto.Message, field string) string {
	t.Helper()

	fd := msg.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(field))
	require.NotNil(t, fd, "no field %q", field)

	value := fd.Enum().Values().ByNumber(msg.ProtoReflect().Get(fd).Enum())
	require.NotNil(t, value, "no enum value for %v", msg.ProtoReflect().Get(fd))
	return string(value.Name())
}

func TestClient_InvokeStream_ConcurrentSendAndRecv(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Chat")

	s := openStream(t, c, method, nil)

	// The UI drives a bidi stream from two goroutines: a send command and the
	// chain of receive commands. Under -race this is the test that says so.
	const messages = 20
	done := make(chan error, 1)
	go func() {
		msgs, err := drain(t, s)
		if len(msgs) != messages {
			done <- errors.New("received the wrong number of messages")
			return
		}
		done <- err
	}()

	for range messages {
		require.NoError(t, s.Send(item(t, method, "ping")))
	}
	require.NoError(t, s.CloseSend())

	select {
	case err := <-done:
		require.ErrorIs(t, err, io.EOF)
	case <-time.After(10 * time.Second):
		t.Fatal("the receiving half never finished")
	}
}
