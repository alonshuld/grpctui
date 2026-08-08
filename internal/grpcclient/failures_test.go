package grpcclient_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// The failure paths of the reflection/invoke/streaming core, gathered here
// because they share a reason for existing rather than a subject.
//
// v1.0's promise is full coverage of exactly this package: it is the layer that
// breaks silently, since a wrong answer from it looks like a wrong answer from
// the server. The happy paths are tested beside the code they exercise; what is
// here is the branches nothing else reaches — a dial that cannot be made, a
// send onto a call that is already over, a status carrier with no status in it.

// TestDial_RefusedByGRPC covers the one way creating a client fails.
//
// It is not a bad address: grpc.NewClient resolves lazily, so every target
// string this package could be handed is accepted and the failure arrives on
// the first call instead. What it does reject at construction is an option it
// cannot make sense of, which reaches here through WithGRPCDialOptions — and
// the point of the test is that the wrapper names the target when it does,
// since "invalid character 'n'" on its own says nothing about which connection
// went wrong.
func TestDial_RefusedByGRPC(t *testing.T) {
	_, err := grpcclient.Dial("api.example.com:443",
		grpcclient.WithGRPCDialOptions(grpc.WithDefaultServiceConfig("{not json")))

	require.Error(t, err)
	require.ErrorContains(t, err, "dial")
	assert.ErrorContains(t, err, "api.example.com:443",
		"the error has to name the target: it is the one thing the user typed")
}

// TestDialProfile_UnnamedProfile pins the message for a profile that has no
// name — the top-level settings, in practice. Naming a profile "" would be
// worse than saying nothing about which one it was.
func TestDialProfile_UnnamedProfile(t *testing.T) {
	_, err := grpcclient.DialProfile(grpcclient.Profile{})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), `profile ""`)
}

func TestDialProfile_NamedProfile(t *testing.T) {
	_, err := grpcclient.DialProfile(grpcclient.Profile{Name: "staging"})

	require.Error(t, err)
	assert.ErrorContains(t, err, `profile "staging"`)
}

// TestClient_Conn is the seam internal/proxy forwards through. It is checked
// here rather than only there so that a change to it fails in the package that
// owns it.
func TestClient_Conn(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)

	assert.NotNil(t, c.Conn())
}

// TestStatusOf_CarrierWithoutAStatus covers the error that claims to carry a
// gRPC status and does not. It is not hypothetical: an error type can implement
// GRPCStatus() and return nil for a case it does not consider a status, and
// dereferencing that would panic the UI on a response.
func TestStatusOf_CarrierWithoutAStatus(t *testing.T) {
	_, ok := grpcclient.StatusOf(statuslessError{})

	assert.False(t, ok, "an error carrying no status is not a status")
}

type statuslessError struct{}

func (statuslessError) Error() string              { return "nothing in particular" }
func (statuslessError) GRPCStatus() *status.Status { return nil }

func TestStatusOf_PlainError(t *testing.T) {
	_, ok := grpcclient.StatusOf(errors.New("connection refused"))

	assert.False(t, ok)
}

func TestStatusOf_Status(t *testing.T) {
	st, ok := grpcclient.StatusOf(status.Error(codes.NotFound, "no such user"))

	require.True(t, ok)
	assert.Equal(t, uint32(codes.NotFound), st.Code)
	assert.Equal(t, "NotFound", st.Name)
	assert.Equal(t, "no such user", st.Message,
		"the server's own message, not the wrapped error string")
}

// TestMethod_InputDescriptor_NoDescriptor covers a Method that carries no
// descriptor at all — what a record recalled out of history holds before it has
// been matched against a live schema.
func TestMethod_InputDescriptor_NoDescriptor(t *testing.T) {
	assert.Nil(t, grpcclient.Method{FullName: "a.B.C"}.InputDescriptor())
}

// TestMetadata_BinaryHeaderThatIsNotBase64 covers a -bin value that cannot be
// decoded. "abcde" is five characters, which is not a valid length in any of
// the four base64 alphabets.
//
// It is refused by validation rather than at attach time — the two check the
// same thing, and validation gets there first — so what this pins is that the
// call does not go out and the message names the header. The decode inside
// attach stays as the belt to that braces: they are separate functions, and one
// of them losing the check should not put an undecodable value on the wire.
func TestMetadata_BinaryHeaderThatIsNotBase64(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)

	_, err := c.ListServices(context.Background(), grpcclient.Metadata{
		{Key: "trace-bin", Value: "abcde"},
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "trace-bin",
		"the header that could not be sent has to be named: there may be six of them")
}

// TestMetadata_EmptyBinaryHeader pins that a -bin header with nothing in it is
// sent as empty rather than refused. An empty value is a legal header value,
// and base64 of nothing is nothing.
func TestMetadata_EmptyBinaryHeader(t *testing.T) {
	var rec headerRecorder
	ts := startTestServer(t, withHeaderCapture(&rec))
	c := ts.client(t)

	_, err := c.ListServices(context.Background(), grpcclient.Metadata{
		{Key: "trace-bin", Value: ""},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{""}, rec.values("trace-bin"))
}

// TestMetadata_EveryHeaderDisabled covers the case where the panel holds
// headers and none of them are on: the call goes out with no metadata rather
// than with an empty one.
func TestMetadata_EveryHeaderDisabled(t *testing.T) {
	var rec headerRecorder
	ts := startTestServer(t, withHeaderCapture(&rec))
	c := ts.client(t)

	_, err := c.ListServices(context.Background(), grpcclient.Metadata{
		{Key: "x-tenant", Value: "acme", Disabled: true},
		{Key: "x-trace", Value: "abc", Disabled: true},
	})

	require.NoError(t, err)
	assert.Empty(t, rec.values("x-tenant"))
	assert.Empty(t, rec.values("x-trace"))
}

// TestAuth_NoCredentialToSend pins that an auth block with nothing in it adds
// no header. A configured-but-empty bearer is what a `${TOKEN}` that expanded
// to nothing would produce, and sending "Bearer " is worse than sending
// nothing: the server answers 401 rather than the anonymous answer.
func TestAuth_NoCredentialToSend(t *testing.T) {
	tests := map[string]grpcclient.Auth{
		"bearer with no token":                 {Kind: grpcclient.AuthBearer},
		"basic with neither name nor password": {Kind: grpcclient.AuthBasic},
	}

	for name, auth := range tests {
		t.Run(name, func(t *testing.T) {
			var rec headerRecorder
			ts := startTestServer(t, withHeaderCapture(&rec))
			c := ts.client(t, grpcclient.WithAuth(auth))

			_, err := c.ListServices(context.Background(), nil)

			require.NoError(t, err)
			assert.Empty(t, rec.values("authorization"))
		})
	}
}

// TestClient_InvokeStream_ClosedConnection covers the open that cannot be made.
// The UI's stream chain starts here, and a nil stream returned with a nil error
// would panic on the first receive.
func TestClient_InvokeStream_ClosedConnection(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Ticks")
	require.NoError(t, c.Close())

	s, err := c.InvokeStream(context.Background(), method, nil)

	require.Error(t, err)
	assert.Nil(t, s)
	assert.ErrorContains(t, err, method.FullName)
}

// TestStream_SendAfterTheCallDied is grpc-go's contract, preserved rather than
// papered over: a send onto a call that is already over reports a bare io.EOF,
// and the reason belongs to Recv. Returning "EOF" to the user as the failure
// would name the messenger.
//
// The call is killed by stopping the server under it, which is what a bidi
// stream watching a service through a deploy actually meets.
func TestStream_SendAfterTheCallDied(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Chat")
	require.Equal(t, grpcclient.KindBidiStreaming, method.Kind())

	s := openStream(t, c, method, nil)
	require.NoError(t, s.Send(item(t, method, "hello")))

	ts.stop()

	// Which send notices the dead transport is a race with it, so keep sending
	// until one does. The wait between attempts is what makes this a bound in
	// *time* rather than a bound in iterations: a hundred sends complete in
	// microseconds on a fast machine, which is no wait at all, and a test that
	// depends on how quickly a loop spins is the kind that passes here and fails
	// on a loaded CI runner.
	var err error
	for range 200 {
		if err = s.Send(item(t, method, "anyone there")); err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.ErrorIs(t, err, io.EOF, "a send onto a dead call is a bare io.EOF")

	// Drained rather than read once. Chat echoes, so the reply to the first
	// message may already be sitting in the receive buffer when the server dies —
	// and then the next Recv hands back that message, not the failure. Reading a
	// single one made this pass or fail on whether the echo had landed yet.
	_, recvErr := drain(t, s)

	require.Error(t, recvErr)
	assert.NotErrorIs(t, recvErr, io.EOF,
		"Recv holds the reason, and a killed server is not 'the stream ended cleanly'")
}

// TestStream_SendWrapsANonEOFFailure covers the other branch of the same send:
// an error that is *not* io.EOF is wrapped with the method name, because that
// one is the reason itself and the panel showing it has to say which call it
// belongs to.
//
// It provokes the branch by sending twice on a server-streaming method, where
// grpc-go has already closed the sending half after the one request the shape
// allows. That is the only reliable way to get a non-EOF error out of Send —
// killing the transport yields io.EOF, which is the other branch.
//
// It deliberately asserts nothing about what Recv says afterwards. A failed
// SendMsg finishes the client stream with the client's own error, so Recv
// reports that rather than the server's PermissionDenied — an artefact of the
// misuse, not behaviour worth pinning. That the server's status survives a
// stream is stream_test.go's job, against a call nobody has misused.
func TestStream_SendWrapsANonEOFFailure(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Fail")
	require.Equal(t, grpcclient.KindServerStreaming, method.Kind())

	s := openStream(t, c, method, nil)
	require.NoError(t, s.Send(item(t, method, "one")))

	err := s.Send(item(t, method, "two"))

	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF, "this one is a reason, not 'go and read Recv'")
	assert.ErrorContains(t, err, method.FullName)
}

// TestStream_SendAfterCloseSend pins the other refusal, which is grpctui's own
// rather than grpc-go's: the sending half is shut, and saying so beats letting
// the message vanish.
func TestStream_SendAfterCloseSend(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Collect")

	s := openStream(t, c, method, nil)
	require.NoError(t, s.CloseSend())

	err := s.Send(item(t, method, "too late"))

	require.ErrorIs(t, err, grpcclient.ErrSendClosed)
	assert.NotErrorIs(t, err, io.EOF, "this one is grpctui refusing, not the call being over")
}

// TestStream_CloseSendTwice pins that closing the sending half again is a
// no-op. The UI runs it on more than one path out, and an error the second time
// would be an error nobody could avoid.
func TestStream_CloseSendTwice(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Collect")

	s := openStream(t, c, method, nil)

	require.NoError(t, s.CloseSend())
	require.NoError(t, s.CloseSend())
}

// TestStream_RecvAfterTheEnd covers the second ending. A stream ends exactly
// once, but the UI's receive chain issues one more Recv than the server has
// answers for, so this is the ordinary path rather than an odd one.
func TestStream_RecvAfterTheEnd(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c := ts.client(t)
	method := streamerMethod(t, "Ticks")

	s := openStream(t, c, method, nil)
	require.NoError(t, s.Send(item(t, method, "tick")))
	require.NoError(t, s.CloseSend())

	_, err := drain(t, s)
	require.ErrorIs(t, err, io.EOF)

	_, again := s.Recv()
	assert.ErrorIs(t, again, io.EOF, "a stream that has ended keeps saying so")
}
