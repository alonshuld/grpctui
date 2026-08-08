package grpcclient

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc/stats"
)

// Timing is where one call's time went, and how much it carried.
//
// It is measured by gRPC itself rather than around the invoke: a wall-clock
// duration says a call took 800ms, and this says whether that was spent opening
// a connection, waiting for the server to answer, or streaming the answer back.
// That distinction is the difference between "the service is slow" and "the
// service is far away", which is the question a debugging tool exists to
// settle.
//
// The zero value means nothing was measured — a client built without the stats
// handler, which is what a UI test's fake is. Every consumer therefore has to
// treat a zero [Timing.Total] as "no breakdown available" rather than as an
// instant call.
type Timing struct {
	// Connect is how long the call waited before its request headers went out:
	// picking a transport, and on a cold connection resolving the name, opening
	// the socket and completing the TLS handshake.
	//
	// It is near zero on a warm connection, and that is not a measurement error
	// — grpctui has usually already run a reflection sweep over the same
	// connection by the time a call is made, so the honest answer for the second
	// call onwards is that connecting cost nothing.
	Connect time.Duration

	// FirstByte is from the start of the call to the response headers arriving,
	// which is the server's think time plus one round trip. On a streaming call
	// it is the time to the server's first word rather than to its answer.
	FirstByte time.Duration

	// Total is the whole call, from gRPC's own start to its own end — slightly
	// less than the wall time around the invoke, which also covers building the
	// request and decoding the response.
	Total time.Duration

	// RequestBytes and ResponseBytes are the payload sizes on the wire,
	// compression and gRPC's five-byte framing included. They come from the same
	// handler and sit on the same status line as the latency, because "slow" and
	// "large" are the two answers to the same question.
	RequestBytes  int
	ResponseBytes int
}

// Measured reports whether there is a breakdown to show.
func (t Timing) Measured() bool { return t.Total > 0 }

// callTiming collects one call's events.
//
// The mutex is not decoration: gRPC calls a stats handler from whichever
// goroutine the event happened on — the one that wrote the headers, the one
// reading the transport — and the invoking goroutine reads the result when the
// call returns.
type callTiming struct {
	mu sync.Mutex

	begin     time.Time
	outHeader time.Time
	inHeader  time.Time
	end       time.Time

	requestBytes  int
	responseBytes int
}

// timingKey is the context key a call's collector hangs off. It is a private
// type so that nothing outside this package can put a value on the same key.
type timingKey struct{}

// withTiming returns ctx carrying a fresh collector, and the collector.
//
// Passing it through the context rather than through the handler is what lets
// one handler — installed once, at dial time — serve every concurrent call on
// the connection without a map keyed by anything.
func withTiming(ctx context.Context) (context.Context, *callTiming) {
	t := &callTiming{}
	return context.WithValue(ctx, timingKey{}, t), t
}

func timingFrom(ctx context.Context) *callTiming {
	t, _ := ctx.Value(timingKey{}).(*callTiming)
	return t
}

// result reports what was collected.
func (t *callTiming) result() Timing {
	if t == nil {
		return Timing{}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.begin.IsZero() || t.end.IsZero() {
		// A call that never reached the wire — a transport that would not open,
		// a context already cancelled — has no breakdown to give, and inventing
		// one from a half-filled struct would be worse than showing none.
		return Timing{}
	}

	timing := Timing{
		Total:         t.end.Sub(t.begin),
		RequestBytes:  t.requestBytes,
		ResponseBytes: t.responseBytes,
	}
	if !t.outHeader.IsZero() {
		timing.Connect = t.outHeader.Sub(t.begin)
	}
	if !t.inHeader.IsZero() {
		timing.FirstByte = t.inHeader.Sub(t.begin)
	}
	return timing
}

// statsHandler records call timings into whichever collector the call's context
// carries. A call made without one — reflection, or anything else that does not
// ask — is ignored at the cost of a nil check.
type statsHandler struct {
	// now is the clock, injectable so that a test can assert on a breakdown
	// rather than on the fact that one is non-negative.
	now func() time.Time
}

// TagRPC implements [stats.Handler]. There is nothing to tag: the collector is
// already on the context, put there by the caller that wants the measurement.
func (statsHandler) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }

// HandleRPC implements [stats.Handler].
//
// The two header events carry no timestamp of their own — unlike the payload
// ones — so they are stamped as they are handled. gRPC calls a handler
// synchronously on the goroutine the event happened on, so that is the event's
// time to within a scheduling hop.
func (h statsHandler) HandleRPC(ctx context.Context, rpc stats.RPCStats) {
	t := timingFrom(ctx)
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	switch s := rpc.(type) {
	case *stats.Begin:
		t.begin = s.BeginTime
	case *stats.OutHeader:
		t.outHeader = h.clock()
	case *stats.OutPayload:
		t.requestBytes += s.WireLength
	case *stats.InHeader:
		t.inHeader = h.clock()
	case *stats.InPayload:
		t.responseBytes += s.WireLength
		// A server that sends no headers of its own before its first message —
		// or a trailers-only response — leaves InHeader unseen, and the first
		// byte is then the payload's.
		if t.inHeader.IsZero() {
			t.inHeader = s.RecvTime
		}
	case *stats.End:
		t.end = s.EndTime
	}
}

func (h statsHandler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// TagConn implements [stats.Handler].
func (statsHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }

// HandleConn implements [stats.Handler].
//
// Connection events are deliberately ignored. They arrive on the connection's
// own context rather than on any call's, so there is nothing to attribute them
// to; what a user wants to know — how much of *this* call was spent getting a
// transport — is the gap before its headers went out, which [HandleRPC]
// already measures.
func (statsHandler) HandleConn(context.Context, stats.ConnStats) {}
