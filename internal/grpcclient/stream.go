package grpcclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

// ErrNotStreaming reports [Client.InvokeStream] called on a unary method. A
// unary method could technically be driven as a one-message stream, but doing
// so would give the UI two ways to make the same call and two shapes of result
// to render.
var ErrNotStreaming = errors.New("method is not a streaming method")

// ErrSendClosed reports a send on a stream whose sending half is already
// closed. It is the user pressing send once more after ending the request
// stream, which is a mistake worth naming rather than a transport failure.
var ErrSendClosed = errors.New("the stream's sending half is closed")

// Stream is one in-flight streaming call.
//
// It is an interface rather than a struct for the same reason [Client] is
// reached through one in internal/ui: it is the seam the UI fakes, and a stream
// backed by a real connection cannot be conjured in a test without a server.
//
// Send and Recv may run concurrently — that is the whole point of a bidi
// stream, and grpc-go supports exactly one sender and one receiver at a time.
// Send serialises its callers internally; Recv must be driven from one
// goroutine, which for the UI is the chain of tea.Cmds that walks the stream.
type Stream interface {
	// Method is the method the stream was opened against.
	Method() Method

	// Send puts one request message on the stream.
	//
	// A stream that has already failed reports [io.EOF] here and the real
	// reason from Recv: that is grpc-go's contract, and it is preserved rather
	// than papered over, because the status belongs to the call and arrives
	// once.
	Send(req proto.Message) error

	// CloseSend closes the sending half, telling the server no more request
	// messages are coming. The receiving half stays open.
	CloseSend() error

	// Recv blocks for the next response message. It returns [io.EOF] — exactly,
	// so [errors.Is] finds it — when the server has finished sending and the
	// call succeeded; any other error is the call's failure and keeps its gRPC
	// status.
	Recv() (proto.Message, error)

	// Close abandons the stream, releasing the resources it holds. It is safe
	// to call more than once, and safe to call on a stream that has already
	// finished — which is why the UI can call it unconditionally when the user
	// moves on.
	Close() error
}

// InvokeStream opens a streaming call against method.
//
// ctx bounds the whole stream: cancelling it — or calling [Stream.Close] —
// aborts the call, which is how the user stops watching. It deliberately
// carries no timeout of its own; a server-streaming watch that runs for an hour
// is the feature, not a hung call.
//
// md is the request metadata to send with it, on top of whatever credential the
// connection itself carries.
func (c *Client) InvokeStream(ctx context.Context, method Method, md Metadata) (Stream, error) {
	if method.Descriptor == nil {
		return nil, fmt.Errorf("stream %q: method has no descriptor", method.FullName)
	}
	if method.Kind() == KindUnary {
		return nil, fmt.Errorf("stream %s: %w", method.FullName, ErrNotStreaming)
	}

	ctx, err := md.attach(ctx)
	if err != nil {
		return nil, fmt.Errorf("stream %s: %w", method.FullName, err)
	}

	// Every stream owns a cancel func, and it must be called however the stream
	// ends — grpc-go leaks the call's resources otherwise. [Stream.Close] is
	// that call, and the UI runs it on every path out.
	ctx, cancel := context.WithCancel(ctx)

	desc := &grpc.StreamDesc{
		StreamName:    string(method.Descriptor.Name()),
		ServerStreams: method.ServerStreaming,
		ClientStreams: method.ClientStreaming,
	}

	cs, err := c.conn.NewStream(ctx, desc, methodPath(method.Descriptor))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stream %s: %w", method.FullName, err)
	}

	c.logger.Info("opened stream",
		zap.String("target", c.target),
		zap.String("method", method.FullName),
		zap.String("kind", string(method.Kind())),
		zap.Strings("headers", md.Keys()),
	)

	return &clientStream{
		cs:      cs,
		method:  method,
		cancel:  cancel,
		target:  c.target,
		logger:  c.logger,
		started: time.Now(),
	}, nil
}

// clientStream is the [Stream] backed by a real gRPC call.
type clientStream struct {
	cs      grpc.ClientStream
	method  Method
	cancel  context.CancelFunc
	target  string
	logger  *zap.Logger
	started time.Time

	// sendMu serialises senders, so that two request messages queued in the
	// same instant cannot interleave on the wire. recvMu does the same for
	// receivers, which grpc-go equally forbids running concurrently.
	sendMu sync.Mutex
	recvMu sync.Mutex

	// mu guards the bookkeeping the two halves share: whether the sending half
	// is closed, whether the stream is finished, and the message counts that go
	// into the closing log line.
	mu         sync.Mutex
	sendClosed bool
	closed     bool
	sent       int
	received   int
}

// Method implements [Stream].
func (s *clientStream) Method() Method { return s.method }

// Send implements [Stream].
func (s *clientStream) Send(req proto.Message) error {
	if req == nil {
		return fmt.Errorf("stream %s: no request message", s.method.FullName)
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	if s.sendHalfClosed() {
		return fmt.Errorf("stream %s: %w", s.method.FullName, ErrSendClosed)
	}

	if err := s.cs.SendMsg(req); err != nil {
		// io.EOF here means the call is already over and Recv holds the reason.
		// It is returned unwrapped so the caller can tell it apart with
		// errors.Is and go look there instead of showing "EOF" as a failure.
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		return fmt.Errorf("stream %s: %w", s.method.FullName, err)
	}

	s.mu.Lock()
	s.sent++
	n := s.sent
	s.mu.Unlock()

	// Sizes only: a request message is whatever the user typed into the form,
	// and this log outlives the session.
	s.logger.Debug("sent stream message",
		zap.String("target", s.target),
		zap.String("method", s.method.FullName),
		zap.Int("index", n),
		zap.Int("bytes", proto.Size(req)),
	)
	return nil
}

// CloseSend implements [Stream].
func (s *clientStream) CloseSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	if s.sendHalfClosed() {
		return nil
	}

	if err := s.cs.CloseSend(); err != nil {
		return fmt.Errorf("stream %s: %w", s.method.FullName, err)
	}

	s.mu.Lock()
	s.sendClosed = true
	s.mu.Unlock()

	s.logger.Debug("closed the sending half",
		zap.String("target", s.target),
		zap.String("method", s.method.FullName),
	)
	return nil
}

// Recv implements [Stream].
func (s *clientStream) Recv() (proto.Message, error) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()

	msg := dynamicpb.NewMessage(s.method.Descriptor.Output())
	if err := s.cs.RecvMsg(msg); err != nil {
		if errors.Is(err, io.EOF) {
			s.finished(nil)
			return nil, io.EOF
		}
		s.finished(err)
		return nil, fmt.Errorf("stream %s: %w", s.method.FullName, err)
	}

	s.mu.Lock()
	s.received++
	n := s.received
	s.mu.Unlock()

	s.logger.Debug("received stream message",
		zap.String("target", s.target),
		zap.String("method", s.method.FullName),
		zap.Int("index", n),
		zap.Int("bytes", proto.Size(msg)),
	)
	return msg, nil
}

// Close implements [Stream].
func (s *clientStream) Close() error {
	s.cancel()
	return nil
}

// sendHalfClosed reports whether the sending half has been closed. It is only
// ever read with sendMu held, so a send cannot slip past a concurrent
// CloseSend.
func (s *clientStream) sendHalfClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendClosed
}

// finished writes the one line that says what the whole stream did. It runs
// once — a stream ends exactly once, but Recv may well be called again
// afterwards, and a log entry per extra call would be noise.
func (s *clientStream) finished(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	sent, received := s.sent, s.received
	s.mu.Unlock()

	fields := []zap.Field{
		zap.String("target", s.target),
		zap.String("method", s.method.FullName),
		zap.Int("sent", sent),
		zap.Int("received", received),
		zap.Duration("took", time.Since(s.started)),
	}

	// Debug, not error: a stream ending on a non-OK status is a normal answer
	// from a server, and the user is already being shown it on screen.
	if err != nil {
		s.logger.Debug("stream failed", append(fields, zap.Error(err))...)
		return
	}
	s.logger.Info("stream finished", fields...)
}
