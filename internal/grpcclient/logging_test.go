package grpcclient_test

import (
	"context"
	"encoding/json"
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

// observed dials the test server with a logger that records everything.
func observed(t *testing.T, ts *testServer) (*grpcclient.Client, *observer.ObservedLogs) {
	t.Helper()

	core, logs := observer.New(zapcore.DebugLevel)
	c, err := grpcclient.Dial(bufTarget,
		grpcclient.WithGRPCDialOptions(ts.dialer()),
		grpcclient.WithLogger(zap.New(core)))
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
	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, secretPayload))
	require.NoError(t, err)

	// Then a service nobody registered, so the call fails: the path that logs
	// zap.Error, where an echoed request value would most plausibly leak.
	_, err = c.InvokeUnary(context.Background(), method, checkRequest(t, method, secretPayload+"-absent"))
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
