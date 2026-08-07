package grpcclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// ErrNotUnary reports [Client.InvokeUnary] called on a streaming method.
// Streaming methods go through [Client.InvokeStream], which hands back a
// [Stream] to drive rather than a single response.
var ErrNotUnary = errors.New("method is not a unary method")

// UnaryResponse is a completed unary call.
type UnaryResponse struct {
	// Message is the decoded response, a *dynamicpb.Message built from the
	// method's output descriptor.
	Message proto.Message

	// Duration is the wall time the call took, measured around the invoke.
	Duration time.Duration
}

// CallStatus is a gRPC status flattened into plain fields.
//
// The UI renders a failed call's code and message distinctly from a transport
// failure, and it cannot import google.golang.org/grpc to do it — so the status
// is carried across the layer boundary in this form instead.
type CallStatus struct {
	// Code is the numeric gRPC status code, e.g. 5.
	Code uint32

	// Name is the code's name, e.g. "NotFound".
	Name string

	// Message is the server's status message.
	Message string
}

// CodeName renders the code alone, "NotFound (5)". It is what the response
// panel puts on its status line, with the server's message going in the body
// underneath rather than alongside.
func (s CallStatus) CodeName() string {
	return fmt.Sprintf("%s (%d)", s.Name, s.Code)
}

// String renders the whole status on one line, for a log or an error.
func (s CallStatus) String() string {
	if s.Message == "" {
		return s.CodeName()
	}
	return s.CodeName() + ": " + s.Message
}

// StatusOf extracts the gRPC status carried by err, if it has one. It reports
// false for errors that never reached the wire — a request that could not be
// built, say — which the UI shows as a plain failure rather than as a status.
//
// The status is read off the wrapped error directly rather than through
// [status.FromError], which rewrites a wrapped status's message to the full
// error string. That is the right default for a log line and the wrong one for
// a panel that already says which method was called: what belongs on screen is
// the server's own message.
func StatusOf(err error) (CallStatus, bool) {
	var carrier interface{ GRPCStatus() *status.Status }
	if !errors.As(err, &carrier) {
		return CallStatus{}, false
	}

	st := carrier.GRPCStatus()
	if st == nil {
		return CallStatus{}, false
	}
	return CallStatus{
		Code:    uint32(st.Code()),
		Name:    st.Code().String(),
		Message: st.Message(),
	}, true
}

// InvokeUnary calls a unary method with req and decodes the response against
// the method's output descriptor.
//
// ctx bounds the call: cancelling it aborts the RPC, which is how the UI stops
// a request the user gave up on. md is the request metadata to send with it,
// on top of whatever credential the connection itself carries. Errors keep
// their gRPC status, so [StatusOf] — and [status.FromError] — still work on the
// returned error.
func (c *Client) InvokeUnary(ctx context.Context, method Method, req proto.Message, md Metadata) (*UnaryResponse, error) {
	if method.Descriptor == nil {
		return nil, fmt.Errorf("invoke %q: method has no descriptor", method.FullName)
	}
	if method.Kind() != KindUnary {
		return nil, fmt.Errorf("invoke %s: %w", method.FullName, ErrNotUnary)
	}
	if req == nil {
		return nil, fmt.Errorf("invoke %s: no request message", method.FullName)
	}

	ctx, err := md.attach(ctx)
	if err != nil {
		return nil, fmt.Errorf("invoke %s: %w", method.FullName, err)
	}

	path := methodPath(method.Descriptor)
	resp := dynamicpb.NewMessage(method.Descriptor.Output())

	start := time.Now()
	err = c.conn.Invoke(ctx, path, req, resp)
	took := time.Since(start)

	if err != nil {
		// Debug, not error: a non-OK status is a perfectly normal answer from a
		// server, and the user is already being shown it on screen.
		c.logger.Debug("call failed",
			zap.String("target", c.target),
			zap.String("method", method.FullName),
			zap.Duration("took", took),
			zap.Error(err),
		)
		return nil, fmt.Errorf("invoke %s: %w", method.FullName, err)
	}

	// Sizes and header names only: a request or response body may hold anything
	// the user typed, and a header value is where the bearer token lives.
	c.logger.Info("call succeeded",
		zap.String("target", c.target),
		zap.String("method", method.FullName),
		zap.Duration("took", took),
		zap.Int("request_bytes", proto.Size(req)),
		zap.Int("response_bytes", proto.Size(resp)),
		zap.Strings("headers", md.Keys()),
	)

	return &UnaryResponse{Message: resp, Duration: took}, nil
}

// methodPath renders the "/package.Service/Method" path used on the wire.
func methodPath(md protoreflect.MethodDescriptor) string {
	return "/" + string(md.Parent().FullName()) + "/" + string(md.Name())
}
