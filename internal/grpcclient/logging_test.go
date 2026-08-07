package grpcclient_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// secretPayload is a value that exists nowhere but inside a request message. If
// it turns up in a log record, a payload was logged.
const secretPayload = "s3cr3t-payload-value"

// secretCredential is a value that exists nowhere but inside a credential. If
// it turns up in a log record, a token was logged.
const secretCredential = "s3cr3t-bearer-token"

// observed dials the test server with a logger that records everything.
func observed(t *testing.T, ts *testServer, opts ...grpcclient.DialOption) (*grpcclient.Client, *observer.ObservedLogs) {
	t.Helper()

	core, logs := observer.New(zapcore.DebugLevel)
	opts = append(opts,
		grpcclient.WithGRPCDialOptions(ts.dialer()),
		grpcclient.WithLogger(zap.New(core)))

	c, err := grpcclient.Dial(bufTarget, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	return c, logs
}

// text renders a log record — message and every field value — as one string, so
// a single assertion covers whatever shape a future field takes.
func text(t *testing.T, entry observer.LoggedEntry) string {
	t.Helper()

	fields, err := json.Marshal(entry.ContextMap())
	require.NoError(t, err)
	return entry.Message + " " + string(fields)
}

// A request body may hold anything the user typed, and from v0.4 the metadata
// beside it holds bearer tokens and mTLS keys. The logger writes to a file that
// outlives the session, so what goes into it is a standing decision rather than
// a per-call one: field names and sizes, never contents.
func TestClient_NeverLogsPayloads(t *testing.T) {
	ts := startTestServer(t, withServingService(secretPayload))
	c, logs := observed(t, ts)

	method := healthMethod(t, c, "Check")

	// The success path first, because it is the one that logs about the bodies
	// — and the payload it carries is the secret, so an entry that renders the
	// request rather than measuring it fails here.
	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, secretPayload), nil)
	require.NoError(t, err)

	// Then a service nobody registered, so the call fails: the path that logs
	// zap.Error, where an echoed request value would most plausibly leak.
	_, err = c.InvokeUnary(context.Background(), method, checkRequest(t, method, secretPayload+"-absent"), nil)
	require.Error(t, err)

	records := logs.All()
	require.NotEmpty(t, records, "the client logged nothing at all, so this proves nothing")
	for _, entry := range records {
		assert.NotContains(t, text(t, entry), secretPayload,
			"a request payload reached the log")
	}

	succeeded := logs.FilterMessage("call succeeded").All()
	require.Len(t, succeeded, 1)

	fields := succeeded[0].ContextMap()
	assert.Contains(t, fields, "request_bytes", "the success record should size the bodies")
	assert.Contains(t, fields, "response_bytes")
	assert.NotContains(t, fields, "request", "sizes, not contents")
	assert.NotContains(t, fields, "response")
}

// A stream carries as many payloads as the user cares to send, and logs a line
// per message, so the same rule has to hold on every one of them.
func TestClient_NeverLogsStreamPayloads(t *testing.T) {
	ts := startTestServer(t, withStreamer())
	c, logs := observed(t, ts)

	method := streamerMethod(t, "Ticks")
	md := grpcclient.Metadata{{Key: "x-api-key", Value: secretCredential}}

	s, err := c.InvokeStream(context.Background(), method, md)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.Send(item(t, method, secretPayload)))
	require.NoError(t, s.CloseSend())

	// The server echoes the request text back, so draining the stream puts the
	// secret through the receiving half's logging as well as the sending half's.
	_, err = drain(t, s)
	require.ErrorIs(t, err, io.EOF)

	records := logs.All()
	require.NotEmpty(t, records, "the client logged nothing at all, so this proves nothing")
	for _, entry := range records {
		assert.NotContains(t, text(t, entry), secretPayload, "a stream payload reached the log")
		assert.NotContains(t, text(t, entry), secretCredential, "a header value reached the log")
	}

	opened := logs.FilterMessage("opened stream").All()
	require.Len(t, opened, 1)
	assert.Equal(t, []any{"x-api-key"}, opened[0].ContextMap()["headers"],
		"header names are what a log can usefully carry")

	finished := logs.FilterMessage("stream finished").All()
	require.Len(t, finished, 1, "a stream that ended should say so exactly once")

	fields := finished[0].ContextMap()
	assert.EqualValues(t, 1, fields["sent"])
	assert.EqualValues(t, ticks, fields["received"])
}

// The same standing decision applied to credentials: a bearer token and a
// header value are the two things in a session most worth not writing to a file
// that outlives it. Header *names* are fair game and genuinely useful.
func TestClient_NeverLogsCredentials(t *testing.T) {
	ts := startTestServer(t)
	c, logs := observed(t, ts, grpcclient.WithAuth(grpcclient.Auth{
		Kind:  grpcclient.AuthBearer,
		Token: secretCredential,
	}))

	method := healthMethod(t, c, "Check")
	md := grpcclient.Metadata{{Key: "x-api-key", Value: secretCredential}}

	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""), md)
	require.NoError(t, err)

	records := logs.All()
	require.NotEmpty(t, records, "the client logged nothing at all, so this proves nothing")
	for _, entry := range records {
		assert.NotContains(t, text(t, entry), secretCredential, "a credential reached the log")
	}

	succeeded := logs.FilterMessage("call succeeded").All()
	require.Len(t, succeeded, 1)
	assert.Equal(t, []any{"x-api-key"}, succeeded[0].ContextMap()["headers"],
		"header names are what a log can usefully carry")
}

// Sending a token in the clear is allowed — grpctui exists for local servers
// and port-forwards — but it is not something to do silently.
func TestClient_WarnsAboutCredentialsOverPlaintext(t *testing.T) {
	ts := startTestServer(t)

	_, logs := observed(t, ts, grpcclient.WithAuth(grpcclient.Auth{
		Kind:  grpcclient.AuthBearer,
		Token: secretCredential,
	}))

	warnings := logs.FilterMessage("sending credentials over a plaintext connection").All()
	require.Len(t, warnings, 1)
	assert.Equal(t, zapcore.WarnLevel, warnings[0].Level)
	assert.NotContains(t, text(t, warnings[0]), secretCredential)
}

func TestClient_DoesNotWarnAboutCredentialsOverTLS(t *testing.T) {
	ts := startTestServer(t)

	_, logs := observed(t, ts,
		grpcclient.WithSecurity(grpcclient.Security{TLS: true, InsecureSkipVerify: true}),
		grpcclient.WithAuth(grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: secretCredential}))

	assert.Empty(t, logs.FilterMessage("sending credentials over a plaintext connection").All())
}
