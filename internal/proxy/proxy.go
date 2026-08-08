// Package proxy is grpctui's passive mode: a gRPC server that forwards
// everything it receives to a real one and reports what went past.
//
// It is the answer to a question the rest of grpctui cannot answer — "what is
// the application actually sending?" — and it works the way `mitmproxy` does:
// point the application at grpctui instead of at the service, and every call it
// makes is logged with its method, its messages and its outcome. Nothing has to
// be built by hand, because the traffic is somebody else's.
//
// # How it forwards without a schema
//
// A proxy that decoded messages would need a descriptor for every method it
// might see, which is exactly what it does not have at the moment a call
// arrives. So it decodes nothing: a codec that hands the raw bytes straight
// through is forced onto both halves, every method is served by
// [grpc.UnknownServiceHandler], and each call becomes a bidirectional stream
// pumped in both directions. Unary, server-streaming, client-streaming and
// bidi all reduce to the same shape once nobody is counting messages, which is
// why one handler covers all four.
//
// # What it does not do
//
// It adds no credential of its own. The connection it forwards over is dialled
// without grpctui's auth precisely so that the server sees what the observed
// client sent and nothing else — a wire-watching tool that quietly changed the
// wire would be worse than useless. Incoming metadata is copied across
// verbatim for the same reason.
//
// This is a transport-layer package: it imports google.golang.org/grpc and
// exposes plain Go types, exactly as internal/grpcclient does. Nothing here
// knows about bubbletea.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// Forwarder is the upstream a proxied call is passed to. It is the streaming
// half of [grpc.ClientConnInterface], which is all a proxy needs: every call is
// forwarded as a stream, whatever shape it really is.
type Forwarder interface {
	NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error)
}

// EventKind is what happened to a proxied call.
type EventKind int

const (
	// Started marks a call arriving, before anything has been forwarded.
	Started EventKind = iota

	// Sent is a message travelling from the observed client to the server.
	//
	// The direction names are from the observed client's point of view, which is
	// the one the user is debugging.
	Sent

	// Received is a message coming back from the server.
	Received

	// Ended marks the call finishing, successfully or not.
	Ended
)

// String names the kind, for a log line.
func (k EventKind) String() string {
	switch k {
	case Started:
		return "started"
	case Sent:
		return "sent"
	case Received:
		return "received"
	case Ended:
		return "ended"
	default:
		return "unknown"
	}
}

// Event is one thing that happened to one proxied call.
//
// It carries no gRPC types, so the UI can consume it directly — the same
// arrangement [grpcclient.CallStatus] has, and for the same reason.
type Event struct {
	// Call numbers the call this belongs to, counting from one. Several calls
	// are in flight at once on a busy connection, so events interleave and the
	// number is what puts them back together.
	Call int

	Kind EventKind

	// Method is the path as it appeared on the wire, "/package.Service/Method".
	Method string

	// Peer is the address the call came from.
	Peer string

	// At is when this happened.
	At time.Time

	// Wire is the message's bytes, for [Sent] and [Received]. It is a copy: gRPC
	// reuses its read buffers, so keeping the slice it handed over would give the
	// UI a message that changes underneath it.
	Wire []byte

	// Truncated says [Event.Wire] holds only the front of a message too large to
	// carry — see [MaxPayload]. The call itself is forwarded whole regardless;
	// only what the log keeps is cut.
	Truncated bool

	// Status is how the call ended, on an [Ended] event, and HasStatus whether
	// there was one at all. A clean finish is OK with an empty message.
	Status    grpcclient.CallStatus
	HasStatus bool
}

// MaxPayload is how much of a message the log keeps. A proxy watching a service
// that streams megabyte frames must not grow without bound just because
// somebody left the panel open, and the front of a message is what identifies
// it.
const MaxPayload = 64 << 10

// defaultBuffer is how many events may be waiting for the UI to read them.
//
// It is a buffer rather than an unbounded queue because the one thing a passive
// proxy may never do is slow down the traffic it is watching: a full channel
// drops the event and counts it, and the panel says how many were lost.
const defaultBuffer = 256

// Proxy listens for gRPC calls and forwards them upstream.
type Proxy struct {
	listen   string
	upstream Forwarder
	logger   *zap.Logger

	events  chan Event
	dropped atomic.Int64
	calls   atomic.Int64

	listener net.Listener
	server   *grpc.Server

	// stopped makes Stop idempotent, and is what guarantees the event channel
	// is closed exactly once however many goroutines ask for it.
	stopped sync.Once
}

// Option configures a [Proxy].
type Option func(*Proxy)

// WithLogger attaches a logger.
func WithLogger(logger *zap.Logger) Option {
	return func(p *Proxy) {
		if logger != nil {
			p.logger = logger
		}
	}
}

// WithBuffer sets how many events may be waiting to be read.
func WithBuffer(n int) Option {
	return func(p *Proxy) {
		if n > 0 {
			p.events = make(chan Event, n)
		}
	}
}

// WithListener supplies the socket to serve on instead of binding one. Tests
// pass a bufconn listener, which is what lets the proxy be exercised end to end
// without a port.
func WithListener(lis net.Listener) Option {
	return func(p *Proxy) {
		if lis != nil {
			p.listener = lis
		}
	}
}

// New builds a proxy listening on listen and forwarding to upstream.
//
// It binds nothing: [Proxy.Listen] does that, so a port already in use is a
// startup error the caller can report rather than something that surfaces from
// inside a goroutine.
func New(listen string, upstream Forwarder, opts ...Option) *Proxy {
	p := &Proxy{
		listen:   listen,
		upstream: upstream,
		logger:   zap.NewNop(),
		events:   make(chan Event, defaultBuffer),
	}
	for _, opt := range opts {
		opt(p)
	}

	p.server = grpc.NewServer(
		// The raw codec on both halves is what lets a call be forwarded with no
		// descriptor for it — see the package comment.
		grpc.ForceServerCodec(rawCodec{}),
		grpc.UnknownServiceHandler(p.handle),

		// Stop then waits for every handler to return, which is what makes
		// closing the event channel after it safe: a handler still running is a
		// handler that may still be reporting.
		grpc.WaitForHandlers(true),
	)
	return p
}

// Events is the stream of what has gone past. It is closed when the proxy stops.
func (p *Proxy) Events() <-chan Event { return p.events }

// Dropped reports how many events were discarded because nothing was reading
// them fast enough.
func (p *Proxy) Dropped() int { return int(p.dropped.Load()) }

// Listen binds the listening socket. A proxy given one by [WithListener] is
// already listening and this does nothing.
func (p *Proxy) Listen() error {
	if p.upstream == nil {
		return errors.New("proxy: no upstream to forward to")
	}
	if p.listener != nil {
		return nil
	}

	lis, err := net.Listen("tcp", p.listen)
	if err != nil {
		return fmt.Errorf("proxy: listen on %q: %w", p.listen, err)
	}

	p.listener = lis
	p.logger.Info("proxy listening", zap.String("addr", lis.Addr().String()))
	return nil
}

// Addr is the address actually bound, which is not always the one asked for:
// ":0" is how a test gets a free port.
func (p *Proxy) Addr() string {
	if p.listener == nil {
		return p.listen
	}
	return p.listener.Addr().String()
}

// Serve accepts calls until [Proxy.Stop]. A listener that dies on its own —
// rather than because Stop closed it — stops the proxy too, so the event
// channel closes either way and the UI's read loop always ends.
func (p *Proxy) Serve() error {
	if p.listener == nil {
		if err := p.Listen(); err != nil {
			return err
		}
	}
	defer p.Stop()

	if err := p.server.Serve(p.listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return fmt.Errorf("proxy: serve: %w", err)
	}
	return nil
}

// Stop shuts the proxy down, abandoning calls in flight, and closes the event
// channel. It is safe to call more than once, and from any goroutine.
//
// The channel is closed here rather than where Serve returns because Serve
// returns as soon as the listener closes, which is *before* the calls already
// in flight have finished. Closing it then would leave a handler emitting into
// a closed channel — a panic, and one that would only show up under load.
// grpc.WaitForHandlers makes Stop wait for them instead.
func (p *Proxy) Stop() {
	p.stopped.Do(func() {
		if p.server != nil {
			p.server.Stop()
		}
		close(p.events)
	})
}

// handle forwards one call.
//
// Both directions are pumped at once: the request half runs on its own
// goroutine and the response half on this one. A single loop would deadlock a
// bidirectional call, where the server may not answer until it has heard
// several messages and the client may not send more until it has heard one.
func (p *Proxy) handle(_ any, server grpc.ServerStream) error {
	ctx := server.Context()

	transport := grpc.ServerTransportStreamFromContext(ctx)
	if transport == nil {
		return status.Error(codes.Internal, "grpctui proxy: no transport stream on the call")
	}
	method := transport.Method()

	call := int(p.calls.Add(1))
	from := peerAddr(ctx)
	p.emit(Event{Call: call, Kind: Started, Method: method, Peer: from, At: time.Now()})

	// The client's own metadata is carried across untouched. A proxy that
	// rewrote it would be answering a different question from the one the user
	// is asking.
	outgoing := ctx
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		outgoing = metadata.NewOutgoingContext(ctx, md.Copy())
	}
	outgoing, cancel := context.WithCancel(outgoing)
	defer cancel()

	desc := &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}
	client, err := p.upstream.NewStream(outgoing, desc, method, grpc.ForceCodec(rawCodec{}))
	if err != nil {
		p.finish(call, method, from, err)
		return err
	}

	// Buffered, so that a request pump abandoned by a failing response half can
	// send its result and exit rather than leaking for the life of the process.
	sendErr := make(chan error, 1)
	go func() { sendErr <- p.pumpRequests(call, method, from, server, client) }()

	err = p.pumpResponses(call, method, from, server, client)
	if err == nil {
		// The response half finished cleanly, so whatever the request half made
		// of the call is the last word on it — an error there is the reason a
		// short call was short.
		if e := <-sendErr; e != nil && !errors.Is(e, io.EOF) {
			err = e
		}
	}

	server.SetTrailer(client.Trailer())
	p.finish(call, method, from, err)
	return err
}

// pumpRequests copies the observed client's messages upstream, closing the
// sending half when it runs out.
func (p *Proxy) pumpRequests(call int, method, from string, server grpc.ServerStream, client grpc.ClientStream) error {
	for {
		var msg frame
		switch err := server.RecvMsg(&msg); {
		case errors.Is(err, io.EOF):
			return client.CloseSend()
		case err != nil:
			return err
		}

		p.emitPayload(call, Sent, method, from, msg.payload)

		if err := client.SendMsg(&msg); err != nil {
			// io.EOF here means the call has already failed and the reason
			// belongs to the receiving half — grpc-go's contract, the same one
			// internal/grpcclient's Stream preserves.
			return err
		}
	}
}

// pumpResponses copies the server's messages back, forwarding its headers ahead
// of the first one.
func (p *Proxy) pumpResponses(call int, method, from string, server grpc.ServerStream, client grpc.ClientStream) error {
	header, err := client.Header()
	if err != nil {
		return err
	}
	if err := server.SetHeader(header); err != nil {
		return err
	}

	for {
		var msg frame
		switch err := client.RecvMsg(&msg); {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		}

		p.emitPayload(call, Received, method, from, msg.payload)

		if err := server.SendMsg(&msg); err != nil {
			return err
		}
	}
}

// finish reports a call ending, with the gRPC status it ended on when it had
// one.
func (p *Proxy) finish(call int, method, from string, err error) {
	event := Event{Call: call, Kind: Ended, Method: method, Peer: from, At: time.Now()}
	if err != nil {
		event.Status, event.HasStatus = grpcclient.StatusOf(err)
		if !event.HasStatus {
			event.Status = grpcclient.CallStatus{Name: "Unknown", Message: err.Error()}
		}
	}
	p.emit(event)

	// Debug rather than info: on a busy connection this is one line per call,
	// and the panel is where a user actually watches traffic. No payload and no
	// metadata is logged either way — a proxied body holds whatever the observed
	// application put in it.
	p.logger.Debug("proxied call ended",
		zap.Int("call", call),
		zap.String("method", method),
		zap.Error(err),
	)
}

// emitPayload reports one message, keeping at most [MaxPayload] of it.
func (p *Proxy) emitPayload(call int, kind EventKind, method, from string, payload []byte) {
	wire, truncated := payload, false
	if len(wire) > MaxPayload {
		wire, truncated = wire[:MaxPayload], true
	}

	p.emit(Event{
		Call:      call,
		Kind:      kind,
		Method:    method,
		Peer:      from,
		At:        time.Now(),
		Wire:      wire,
		Truncated: truncated,
	})
}

// emit offers an event to whoever is listening, dropping it if nobody is
// keeping up.
//
// Blocking here would make the proxy's own latency a function of how fast the
// UI redraws, which would change the timings the user is trying to read.
func (p *Proxy) emit(event Event) {
	select {
	case p.events <- event:
	default:
		p.dropped.Add(1)
	}
}

// peerAddr names where a call came from, or "" when gRPC does not say.
func peerAddr(ctx context.Context) string {
	if pr, ok := peer.FromContext(ctx); ok && pr.Addr != nil {
		return pr.Addr.String()
	}
	return ""
}
