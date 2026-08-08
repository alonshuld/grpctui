package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/runner"
	"github.com/alonshuld/grpctui/internal/vars"
)

// clock ticks a fixed amount per reading, so that a report's durations are a
// function of how many calls were made and nothing else.
func clock() func() time.Time {
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		at = at.Add(10 * time.Millisecond)
		return at
	}
}

// request builds a saved request against the fixture's Echo method.
func request(name, text string) requests.Request {
	return requests.Request{
		Name:   name,
		Method: service + ".Echo",
		Body:   map[string]any{"text": text},
	}
}

// run replays entries and returns the report and the text written.
func run(t *testing.T, client runner.Client, entries []requests.Request, opts runner.Options) (runner.Report, string) {
	t.Helper()

	var out strings.Builder
	opts.Requests = entries
	if opts.Now == nil {
		opts.Now = clock()
	}

	report, err := runner.Run(context.Background(), client, &out, opts)
	require.NoError(t, err)
	return report, out.String()
}

func TestRun_Success(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{"": {reply: "hello"}}}
	report, out := run(t, client, []requests.Request{
		request("first", "a"),
		request("second", "b"),
	}, runner.Options{})

	assert.Zero(t, report.Failed)
	assert.Zero(t, report.Skipped)
	require.Len(t, report.Results, 2)

	for _, r := range report.Results {
		assert.True(t, r.OK())
		assert.Equal(t, "OK", r.Status)
		assert.Empty(t, r.Error)
		require.Len(t, r.Body, 1)
		assert.Contains(t, r.Body[0], "hello")
	}

	assert.Contains(t, out, "✓ first")
	assert.Contains(t, out, "✓ second")
	assert.Contains(t, out, "2 of 2 passed")

	// A successful body is deliberately not printed: a smoke-test log with a
	// JSON document per request is one nobody scrolls through.
	assert.NotContains(t, out, "hello")
}

// TestRun_Order pins that requests go out in the order the file lists them.
// A login before the call that uses its token is most of what a collection
// means.
func TestRun_Order(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{"": {reply: "ok"}}}
	_, _ = run(t, client, []requests.Request{
		request("one", "first"),
		request("two", "second"),
		request("three", "third"),
	}, runner.Options{})

	require.Len(t, client.calls, 3)
	assert.Equal(t, "first", text(client.calls[0].request))
	assert.Equal(t, "second", text(client.calls[1].request))
	assert.Equal(t, "third", text(client.calls[2].request))
}

// TestRun_ContinuesPastAFailure pins that every request is attempted. A smoke
// test that stopped at the first problem would tell you about one thing when it
// could have told you about three.
func TestRun_ContinuesPastAFailure(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{
		"":    {reply: "ok"},
		"bad": {err: refused(codes.NotFound, "no such user")},
	}}

	report, out := run(t, client, []requests.Request{
		request("good", "a"),
		request("broken", "bad"),
		request("also good", "c"),
	}, runner.Options{})

	assert.Equal(t, 1, report.Failed)
	require.Len(t, report.Results, 3)
	assert.True(t, report.Results[0].OK())
	assert.False(t, report.Results[1].OK())
	assert.True(t, report.Results[2].OK())

	assert.Equal(t, "NotFound", report.Results[1].Status)
	assert.Contains(t, report.Results[1].Error, "no such user")

	assert.Contains(t, out, "✗ broken")
	assert.Contains(t, out, "NotFound")
	assert.Contains(t, out, "2 of 3 passed")
	assert.Len(t, client.calls, 3)
}

// TestRun_MissingMethod pins that a request naming a method this connection
// does not have fails without ever reaching the wire.
func TestRun_MissingMethod(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{"": {reply: "ok"}}}
	report, out := run(t, client, []requests.Request{
		{Name: "gone", Method: "demo.v1.Runner.Vanished"},
	}, runner.Options{})

	assert.Equal(t, 1, report.Failed)
	assert.Contains(t, report.Results[0].Error, "demo.v1.Runner.Vanished")
	assert.Contains(t, out, "0 of 1 passed")
	assert.Empty(t, client.calls)
}

// TestRun_ServerStreaming pins that a watch is drained and every message
// reported, and that the request half is closed straight away — a server
// waiting for a second message would hang.
func TestRun_ServerStreaming(t *testing.T) {
	t.Parallel()

	client := &fakeClient{streamed: []string{"one", "two", "three"}}
	report, _ := run(t, client, []requests.Request{{
		Name:   "watch",
		Method: service + ".Watch",
		Body:   map[string]any{"text": "go"},
	}}, runner.Options{})

	assert.Zero(t, report.Failed)
	require.Len(t, report.Results, 1)
	require.Len(t, report.Results[0].Body, 3)
	assert.Contains(t, report.Results[0].Body[0], "one")
	assert.Contains(t, report.Results[0].Body[2], "three")
}

// TestRun_ServerStreamingFails pins that a stream ending badly is the failure
// it is, with whatever it managed to carry still reported.
func TestRun_ServerStreamingFails(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		streamed:  []string{"one"},
		streamErr: refused(codes.Unavailable, "the server went away"),
	}
	report, out := run(t, client, []requests.Request{{
		Name:   "watch",
		Method: service + ".Watch",
	}}, runner.Options{})

	assert.Equal(t, 1, report.Failed)
	assert.Equal(t, "Unavailable", report.Results[0].Status)
	assert.Len(t, report.Results[0].Body, 1)

	// A failure's body is printed, because it is the thing worth having.
	assert.Contains(t, out, "one")
}

// TestRun_SkipsClientStreaming pins the one shape a saved request cannot
// express, and that skipping it does not fail the run: a collection holding a
// bidi call is not a broken deployment.
func TestRun_SkipsClientStreaming(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{"": {reply: "ok"}}}
	report, out := run(t, client, []requests.Request{
		{Name: "collect", Method: service + ".Collect"},
		{Name: "chat", Method: service + ".Chat"},
		request("echo", "a"),
	}, runner.Options{})

	assert.Zero(t, report.Failed)
	assert.Equal(t, 2, report.Skipped)
	assert.True(t, report.Results[0].Skipped)
	assert.True(t, report.Results[1].Skipped)
	assert.False(t, report.Results[2].Skipped)

	assert.Contains(t, report.Results[0].Error, "queue of messages")
	assert.Contains(t, out, "– collect")
	assert.Contains(t, out, "1 of 3 passed, 2 skipped")

	// Neither streaming request reached the wire.
	require.Len(t, client.calls, 1)
	assert.Equal(t, service+".Echo", client.calls[0].method)
}

// TestRun_Variables pins that a saved request is a template: the {{name}}
// references it carries are resolved against the environment, exactly as the
// request panel does it.
func TestRun_Variables(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{"": {reply: "ok"}}}
	report, _ := run(t, client, []requests.Request{{
		Name:   "templated",
		Method: service + ".Echo",
		Body:   map[string]any{"text": "user-{{tenant}}"},
	}}, runner.Options{
		Variables: vars.NewSet([]vars.Variable{{Name: "tenant", Value: "acme"}}),
	})

	require.Zero(t, report.Failed)
	require.Len(t, client.calls, 1)
	assert.Equal(t, "user-acme", text(client.calls[0].request))
}

// TestRun_Values pins the other half of a template: a reference protobuf's JSON
// mapping cannot hold — "{{count}}" is not an int64 — is carried in Values and
// put back on the row it was typed into.
func TestRun_Values(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{"": {reply: "ok"}}}
	report, _ := run(t, client, []requests.Request{{
		Name:   "templated",
		Method: service + ".Echo",
		Body:   map[string]any{"text": "x"},
		Values: map[string]string{"count": "{{n}}"},
	}}, runner.Options{
		Variables: vars.NewSet([]vars.Variable{{Name: "n", Value: "42"}}),
	})

	require.Zero(t, report.Failed, "%+v", report.Results)
	require.Len(t, client.calls, 1)

	msg := client.calls[0].request.ProtoReflect()
	assert.EqualValues(t, 42, msg.Get(msg.Descriptor().Fields().ByName("count")).Int())
}

// TestRun_UnboundVariable pins that a reference nothing binds fails the request
// rather than being sent verbatim.
func TestRun_UnboundVariable(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{"": {reply: "ok"}}}
	report, _ := run(t, client, []requests.Request{{
		Name:   "templated",
		Method: service + ".Echo",
		Body:   map[string]any{"text": "{{nobody}}"},
	}}, runner.Options{})

	assert.Equal(t, 1, report.Failed)
	assert.Contains(t, report.Results[0].Error, "nobody")
	assert.Empty(t, client.calls)
}

// TestRun_Headers pins that every call carries the metadata it was given.
func TestRun_Headers(t *testing.T) {
	t.Parallel()

	md := grpcclient.Metadata{{Key: "authorization", Value: "Bearer secret"}}

	client := &fakeClient{unary: map[string]unaryAnswer{"": {reply: "ok"}}}
	_, out := run(t, client, []requests.Request{request("one", "a")}, runner.Options{Metadata: md})

	require.Len(t, client.calls, 1)
	assert.Equal(t, md, client.calls[0].headers)

	// grpctui's standing rule: a credential never reaches a surface that
	// outlives the call, and a CI log is exactly such a surface.
	assert.NotContains(t, out, "secret")
}

// TestRun_BodyDoesNotLeakCredentials is the same rule for the JSON report,
// which is the most machine-readable — and therefore most copied — surface
// there is.
func TestRun_BodyDoesNotLeakCredentials(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{"": {reply: "ok"}}}
	_, out := run(t, client, []requests.Request{request("one", "a")}, runner.Options{
		Format:   runner.FormatJSON,
		Metadata: grpcclient.Metadata{{Key: "authorization", Value: "Bearer secret"}},
	})

	assert.NotContains(t, out, "secret")
	assert.NotContains(t, out, "authorization")
}

func TestRun_JSONFormat(t *testing.T) {
	t.Parallel()

	client := &fakeClient{unary: map[string]unaryAnswer{
		"":    {reply: "ok"},
		"bad": {err: refused(codes.PermissionDenied, "nope")},
	}}
	report, out := run(t, client, []requests.Request{
		request("good", "a"),
		request("broken", "bad"),
	}, runner.Options{Format: runner.FormatJSON})

	var decoded runner.Report
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))

	assert.Equal(t, report.Failed, decoded.Failed)
	assert.Equal(t, client.Target(), decoded.Target)
	require.Len(t, decoded.Results, 2)
	assert.Equal(t, "good", decoded.Results[0].Name)
	assert.Equal(t, "PermissionDenied", decoded.Results[1].Status)

	// Unlike the text report, JSON carries every body: it is for something
	// parsing the log rather than somebody reading it.
	assert.NotEmpty(t, decoded.Results[0].Body)
}

// TestRun_DiscoveryFails pins the difference between a failure of the run and a
// failed request. A server that would not answer at all is the former, and the
// caller gets an error rather than a report full of failures.
func TestRun_DiscoveryFails(t *testing.T) {
	t.Parallel()

	client := &fakeClient{discoverErr: errors.New("connection refused")}

	var out strings.Builder
	_, err := runner.Run(context.Background(), client, &out, runner.Options{
		Requests: []requests.Request{request("one", "a")},
		Now:      clock(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
	assert.Empty(t, out.String())
}

// TestRun_Timeout pins that a call is bounded, which is the only thing that can
// end a watch stream: a smoke test that waits forever is a broken build rather
// than a failed one.
func TestRun_Timeout(t *testing.T) {
	t.Parallel()

	client := &fakeClient{slow: true, unary: map[string]unaryAnswer{"": {reply: "ok"}}}

	var out strings.Builder
	report, err := runner.Run(context.Background(), client, &out, runner.Options{
		Requests: []requests.Request{request("slow", "a")},
		Timeout:  10 * time.Millisecond,
		Now:      clock(),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Failed)
	assert.Contains(t, report.Results[0].Error, "context deadline exceeded")
}

func TestRun_NoRequests(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	report, out := run(t, client, nil, runner.Options{})

	assert.Empty(t, report.Results)
	assert.Zero(t, report.Failed)
	assert.Contains(t, out, "0 of 0 passed")
}

func TestParseFormat(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"text", "json"} {
		got, err := runner.ParseFormat(name)
		require.NoError(t, err)
		assert.EqualValues(t, name, got)
	}

	_, err := runner.ParseFormat("yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yaml")
}

// TestReport_Skipped pins that a skipped request is not counted as a failure,
// which is what the process's exit code follows.
func TestResult_OK(t *testing.T) {
	t.Parallel()

	assert.True(t, runner.Result{}.OK())
	assert.False(t, runner.Result{Error: "boom"}.OK())
	assert.False(t, runner.Result{Skipped: true, Error: "not attempted"}.OK())
}
