// Package runner replays a saved collection without the TUI.
//
// It is what makes a collection worth keeping beside a project: the same file
// the browser recalls a request from can be run in CI as a smoke test, so that
// "does staging still answer" is a command rather than a person opening a tool
// and pressing enter eleven times.
//
// What it runs is exactly what the TUI would have sent. A request is rebuilt
// through the same [protoschema.Form] the request panel uses, resolved against
// the same [vars.Set], and invoked through the same transport interface — which
// is the point. A second, simpler path that happened to send something slightly
// different would make a green CI run mean nothing.
//
// Nothing here imports bubbletea or lipgloss: the output is plain text or JSON,
// written to an [io.Writer], and the process's exit code comes from
// [Report.Failed].
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/vars"
)

// Client is the slice of the transport layer a run depends on. It is an
// interface for the reason internal/ui's is: a run has to be testable without a
// server.
type Client interface {
	Target() string
	ListServices(ctx context.Context, md grpcclient.Metadata) ([]grpcclient.Service, error)
	InvokeUnary(ctx context.Context, method grpcclient.Method, req proto.Message, md grpcclient.Metadata) (*grpcclient.UnaryResponse, error)
	InvokeStream(ctx context.Context, method grpcclient.Method, md grpcclient.Metadata) (grpcclient.Stream, error)
}

// Format is how a run reports itself.
type Format string

// The output formats.
const (
	// FormatText is one line per request and a summary, for a human reading a CI
	// log.
	FormatText Format = "text"

	// FormatJSON is one object per request, for something parsing the log.
	FormatJSON Format = "json"
)

// ParseFormat reads a format name.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatText, FormatJSON:
		return Format(s), nil
	default:
		return "", fmt.Errorf("no such format %q: have %q, %q", s, FormatText, FormatJSON)
	}
}

// Options configures a run.
type Options struct {
	// Requests are the calls to make, in order. Order is part of what a
	// collection means — a login before the call that uses its token — so they
	// are never run concurrently.
	Requests []requests.Request

	// Variables are the bindings {{name}} references resolve against.
	Variables vars.Set

	// Metadata are the headers every call carries, on top of whatever the
	// connection itself attaches.
	Metadata grpcclient.Metadata

	// Timeout bounds one call. It bounds a server-streaming call too, which is
	// the only thing that can: a watch stream does not end on its own, and a
	// smoke test that waits forever is a broken build rather than a failed one.
	Timeout time.Duration

	// Format is how to report. Empty means [FormatText].
	Format Format

	// Now is the clock the report reads, so that a golden file of one is
	// reproducible. Empty means [time.Now].
	Now func() time.Time

	// Logger receives the same detail the TUI's does. Header names only, never
	// their values: see the standing rule in CLAUDE.md.
	Logger *zap.Logger
}

// Result is what happened to one request.
type Result struct {
	// Name is the request's name in the collection, and Method the
	// fully-qualified method it called.
	Name   string `json:"name"`
	Method string `json:"method"`

	// Status is the gRPC status the call ended with, "OK" on success. It is
	// present even when Error is set, since a failed call very often has one.
	Status string `json:"status"`

	// Body is the response, rendered as it would be in the response panel. A
	// server-streaming call renders every message it carried, in order.
	Body []string `json:"body,omitempty"`

	// Error is why the request did not succeed, or empty. A request that could
	// not even be built — a method this connection does not have, a reference
	// nothing binds — fails here without ever reaching the wire.
	Error string `json:"error,omitempty"`

	// Skipped says the request was not attempted, and Error why. A skipped
	// request does not fail the run: see [Report.Failed].
	Skipped bool `json:"skipped,omitempty"`

	// Took is the wall time around the call.
	Took Millis `json:"took_ms"`
}

// Millis is a duration that marshals as a number of milliseconds.
//
// A bare [time.Duration] marshals as its nanosecond count, which under a key
// called took_ms would be wrong by a factor of a million — and wrong in the
// direction that makes a fast run look slow to whatever is reading the log.
type Millis time.Duration

// MarshalJSON implements [json.Marshaler]. Fractions are kept: a call that took
// 342µs is 0.342, not 0.
func (m Millis) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(time.Duration(m).Seconds()*1000, 'f', -1, 64)), nil
}

// UnmarshalJSON implements [json.Unmarshaler], so that a report round-trips.
func (m *Millis) UnmarshalJSON(data []byte) error {
	value, err := strconv.ParseFloat(string(data), 64)
	if err != nil {
		return fmt.Errorf("read a duration: %w", err)
	}
	*m = Millis(float64(time.Millisecond) * value)
	return nil
}

// Duration is the value as a [time.Duration].
func (m Millis) Duration() time.Duration { return time.Duration(m) }

// OK reports whether the request succeeded.
func (r Result) OK() bool { return r.Error == "" && !r.Skipped }

// Report is the outcome of a whole run.
type Report struct {
	Target  string   `json:"target"`
	Results []Result `json:"results"`

	// Failed counts the requests that were attempted and did not succeed. A
	// skipped one is not among them: a collection holding a bidi-streaming call
	// is not a broken deployment, and failing CI over it would teach people to
	// stop putting streaming calls in collections.
	Failed int `json:"failed"`

	// Skipped counts the requests that were not attempted.
	Skipped int `json:"skipped"`

	Took Millis `json:"took_ms"`
}

// Run replays the requests and writes the report to out.
//
// Every request is attempted, including the ones after a failure. A smoke test
// that stops at the first problem tells you about one thing when it could have
// told you about four, and the run is over in seconds either way.
//
// The returned error is a failure of the *run* — discovery that did not answer,
// a report that could not be written — and never a request that came back
// NotFound. A call that reached the server and was refused is a result, which
// is what [Report.Failed] counts.
func Run(ctx context.Context, client Client, out io.Writer, opts Options) (Report, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	started := now()
	report := Report{Target: client.Target()}

	services, err := client.ListServices(ctx, opts.Metadata)
	if err != nil {
		return Report{}, fmt.Errorf("discover %s: %w", client.Target(), err)
	}
	methods := index(services)

	logger.Info("running a collection",
		zap.String("target", client.Target()),
		zap.Int("requests", len(opts.Requests)),
		zap.Strings("headers", opts.Metadata.Keys()),
		zap.Strings("variables", opts.Variables.Names()),
	)

	for _, request := range opts.Requests {
		result := run(ctx, client, methods, request, opts, now)
		report.Results = append(report.Results, result)

		switch {
		case result.Skipped:
			report.Skipped++
		case !result.OK():
			report.Failed++
		}
	}
	report.Took = Millis(now().Sub(started))

	if err := write(out, report, opts.Format); err != nil {
		return Report{}, err
	}
	return report, nil
}

// index maps every discovered method by its fully-qualified name, which is what
// a saved request refers to one by.
func index(services []grpcclient.Service) map[string]grpcclient.Method {
	out := make(map[string]grpcclient.Method)
	for _, svc := range services {
		for _, method := range svc.Methods {
			out[method.FullName] = method
		}
	}
	return out
}

// run makes one call.
func run(ctx context.Context, client Client, methods map[string]grpcclient.Method, request requests.Request, opts Options, now func() time.Time) Result {
	result := Result{Name: request.Label(), Method: request.Method}

	method, ok := methods[request.Method]
	if !ok {
		result.Error = request.Method + " is not on this connection"
		return result
	}

	// A client-streaming or bidi call is defined by a *sequence* of request
	// messages, and a collection entry holds one. Sending that single message
	// and calling it a smoke test would be inventing a meaning the file never
	// had, so it is skipped and said so.
	if method.ClientStreaming {
		result.Skipped = true
		result.Error = fmt.Sprintf("%s calls need a queue of messages, which a saved request does not hold", method.Kind())
		return result
	}

	msg, err := build(method, request, opts.Variables)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	callCtx, cancel := withTimeout(ctx, opts.Timeout)
	defer cancel()

	started := now()
	if method.ServerStreaming {
		result.Body, result.Status, err = drain(callCtx, client, method, msg, opts.Metadata)
	} else {
		result.Body, result.Status, err = call(callCtx, client, method, msg, opts.Metadata)
	}
	result.Took = Millis(now().Sub(started))

	if err != nil {
		result.Error = reason(err)
	}
	return result
}

// reason is what to print beside a failed request.
//
// A call the server refused is reported by its status *message* alone, because
// the line already carries the code and grpc-go's error text repeats it: "rpc
// error: code = NotFound desc = unknown service" beside a column that says
// NotFound is three quarters noise. Anything with no status — a request that
// never reached the wire — keeps its full error, which is all there is.
func reason(err error) string {
	if st, ok := grpcclient.StatusOf(err); ok && st.Message != "" {
		return st.Message
	}
	return err.Error()
}

// build rebuilds a saved request into the message to send.
//
// It goes through [protoschema.Form] rather than straight from
// [protoschema.DecodeBody] because a saved request is a *template*: the body
// carries what protobuf's JSON mapping could hold, and Values carries the
// {{name}} references it could not. Loading both and building is exactly what
// the request panel does, and doing anything else here would mean a collection
// that ran differently from the way it was recalled.
func build(method grpcclient.Method, request requests.Request, bindings vars.Set) (proto.Message, error) {
	md := method.InputDescriptor()
	if md == nil {
		return nil, errors.New("this method carries no request descriptor")
	}

	saved, err := protoschema.DecodeBody(md, request.Body)
	if err != nil {
		return nil, err
	}

	form := protoschema.NewForm(md)
	form.SetResolver(bindings)
	form.Load(saved)
	if err := form.LoadValues(request.Values); err != nil {
		return nil, err
	}
	return form.Build()
}

// call makes a unary call and renders its answer.
func call(ctx context.Context, client Client, method grpcclient.Method, msg proto.Message, md grpcclient.Metadata) ([]string, string, error) {
	resp, err := client.InvokeUnary(ctx, method, msg, md)
	if err != nil {
		return nil, statusOf(err), err
	}

	body, _, err := protoschema.Marshal(resp.Message)
	if err != nil {
		return nil, statusOK, err
	}
	return []string{body}, statusOK, nil
}

// drain opens a server-streaming call, sends the one request it takes, and
// reads until the stream ends or the context does.
//
// A watch stream never ends on its own, so the timeout is what ends it — and a
// timeout reached that way is reported as the failure it is. There is no honest
// alternative: a runner that decided a watch had gone on long enough and called
// it a pass would pass on a server sending nothing at all.
func drain(ctx context.Context, client Client, method grpcclient.Method, msg proto.Message, md grpcclient.Metadata) ([]string, string, error) {
	stream, err := client.InvokeStream(ctx, method, md)
	if err != nil {
		return nil, statusOf(err), err
	}

	if err := stream.Send(msg); err != nil && !errors.Is(err, io.EOF) {
		return nil, statusOf(err), err
	}
	// The request half is closed straight away: a server-streaming call takes
	// exactly one message, and a server waiting for a second one would hang.
	if err := stream.CloseSend(); err != nil {
		return nil, statusOf(err), err
	}

	var bodies []string
	for {
		received, err := stream.Recv()
		switch {
		case errors.Is(err, io.EOF):
			return bodies, statusOK, nil
		case err != nil:
			return bodies, statusOf(err), err
		}

		body, _, err := protoschema.Marshal(received)
		if err != nil {
			return bodies, statusOK, err
		}
		bodies = append(bodies, body)
	}
}

// statusOK is what a call that was not refused ended with.
const statusOK = "OK"

// statusOf names the gRPC status an error carries, or "" when it carries none —
// a request that never reached the wire.
func statusOf(err error) string {
	st, ok := grpcclient.StatusOf(err)
	if !ok {
		return ""
	}
	return st.Name
}

// withTimeout bounds one call, or leaves it unbounded when no timeout was
// asked for.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// write reports the run.
func write(out io.Writer, report Report, format Format) error {
	if out == nil {
		return nil
	}
	if format == FormatJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		return nil
	}
	return writeText(out, report)
}

// writeText writes the human-readable report: one line per request, and the
// body of anything that failed.
//
// A successful call's body is deliberately not printed. The run is a smoke
// test, and a log with eleven JSON documents in it is one nobody scrolls
// through; the body of the one that broke is the thing worth having, and
// --format json is there for everything else.
func writeText(out io.Writer, report Report) error {
	var b strings.Builder

	width := 0
	for _, r := range report.Results {
		width = max(width, len(r.Name))
	}

	for _, r := range report.Results {
		fmt.Fprintf(&b, "%s %-*s  %s  %s\n", mark(r), width, r.Name, r.Method, detail(r))
		if r.Error != "" && !r.Skipped {
			for _, body := range r.Body {
				b.WriteString(indent(body))
			}
		}
	}

	fmt.Fprintf(&b, "\n%d of %d passed", len(report.Results)-report.Failed-report.Skipped, len(report.Results))
	if report.Skipped > 0 {
		fmt.Fprintf(&b, ", %d skipped", report.Skipped)
	}
	fmt.Fprintf(&b, " in %s against %s\n", took(report.Took), report.Target)

	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

// mark is the glyph a result leads with. It is a word rather than only a colour
// because a CI log is not a terminal.
func mark(r Result) string {
	switch {
	case r.Skipped:
		return "–"
	case r.OK():
		return "✓"
	default:
		return "✗"
	}
}

// detail is what a result's line ends with: how long it took, or why it did not
// get that far.
func detail(r Result) string {
	if r.Error != "" {
		if r.Status != "" && r.Status != statusOK {
			return r.Status + ": " + r.Error
		}
		return r.Error
	}
	return took(r.Took)
}

// took renders how long a call ran.
//
// Sub-millisecond calls are rounded to microseconds rather than to zero: a
// local server answers in a few hundred microseconds, and a column of "0s"
// would say nothing about a run where every request was fast.
func took(m Millis) string {
	d := m.Duration()
	if d < time.Millisecond {
		return d.Round(time.Microsecond).String()
	}
	return d.Round(time.Millisecond).String()
}

// indent shifts a body under the line it belongs to.
func indent(body string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(strings.TrimRight(body, "\n"), "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}
