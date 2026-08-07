package grpcclient

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jhump/protoreflect/v2/grpcreflect"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ErrReflectionUnavailable reports that the target does not serve the gRPC
// server reflection API. It is the one discovery failure the UI treats as a
// dedicated screen rather than a generic error, because the fix (enable
// reflection, or point at a different target) is specific.
var ErrReflectionUnavailable = errors.New("server reflection is not available on this target")

// Service is a service exposed by the target, as reported by reflection.
type Service struct {
	// Name is the fully-qualified service name, e.g. "grpctui.demo.v1.Greeter".
	Name string

	// Methods are the service's methods in declaration order.
	Methods []Method
}

// Method is a single RPC on a [Service].
type Method struct {
	// Name is the bare method name, e.g. "SayHello".
	Name string

	// FullName is the fully-qualified method name, e.g.
	// "grpctui.demo.v1.Greeter.SayHello".
	FullName string

	// InputType and OutputType are fully-qualified message names.
	InputType  string
	OutputType string

	ClientStreaming bool
	ServerStreaming bool

	// Descriptor is the underlying method descriptor. The UI must not touch
	// it; internal/protoschema turns its input message into a form-field tree
	// from v0.2, and internal/grpcclient invokes against it.
	Descriptor protoreflect.MethodDescriptor
}

// InputDescriptor returns the descriptor of the method's request message, or
// nil when the method carries no descriptor at all. internal/protoschema turns
// it into a form-field tree.
func (m Method) InputDescriptor() protoreflect.MessageDescriptor {
	if m.Descriptor == nil {
		return nil
	}
	return m.Descriptor.Input()
}

// Kind describes a method's streaming shape.
type Kind string

// The four gRPC method kinds.
const (
	KindUnary           Kind = "unary"
	KindServerStreaming Kind = "server-streaming"
	KindClientStreaming Kind = "client-streaming"
	KindBidiStreaming   Kind = "bidi-streaming"
)

// Kind reports the method's streaming shape.
func (m Method) Kind() Kind {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		return KindBidiStreaming
	case m.ClientStreaming:
		return KindClientStreaming
	case m.ServerStreaming:
		return KindServerStreaming
	default:
		return KindUnary
	}
}

// ListServices discovers every service the target exposes, along with each
// service's methods, using server reflection.
//
// ctx bounds the whole discovery — the reflection stream is torn down when it
// is cancelled. Errors from the target keep their gRPC status, so
// [status.FromError] still works on the returned error; a target without
// reflection yields an error matching [ErrReflectionUnavailable].
//
// md rides along on the reflection stream. Discovery is an RPC like any other,
// so a server that gates its API behind a header gates reflection behind the
// same one — a target that answers grpcurl but not grpctui is nearly always
// this.
func (c *Client) ListServices(ctx context.Context, md Metadata) ([]Service, error) {
	start := time.Now()

	ctx, err := md.attach(ctx)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}

	// grpcreflect binds a context (and one reflection stream) per client, so
	// this is constructed per call rather than cached on c.
	rc := grpcreflect.NewClientAuto(ctx, c.conn)
	defer rc.Reset()

	names, err := rc.ListServices()
	if err != nil {
		return nil, discoveryError("list services", err)
	}
	slices.Sort(names)

	resolver := rc.AsResolver()
	services := make([]Service, 0, len(names))
	for _, name := range names {
		desc, err := resolver.FindDescriptorByName(name)
		if err != nil {
			return nil, discoveryError(fmt.Sprintf("resolve service %q", name), err)
		}
		sd, ok := desc.(protoreflect.ServiceDescriptor)
		if !ok {
			return nil, fmt.Errorf("resolve service %q: reflection returned a %T, not a service", name, desc)
		}
		services = append(services, serviceFromDescriptor(sd))
	}

	c.logger.Info("discovered services",
		zap.String("target", c.target),
		zap.Int("services", len(services)),
		zap.Duration("took", time.Since(start)),
	)
	return services, nil
}

func serviceFromDescriptor(sd protoreflect.ServiceDescriptor) Service {
	mds := sd.Methods()
	svc := Service{
		Name:    string(sd.FullName()),
		Methods: make([]Method, 0, mds.Len()),
	}
	for i := range mds.Len() {
		md := mds.Get(i)
		svc.Methods = append(svc.Methods, Method{
			Name:            string(md.Name()),
			FullName:        string(md.FullName()),
			InputType:       string(md.Input().FullName()),
			OutputType:      string(md.Output().FullName()),
			ClientStreaming: md.IsStreamingClient(),
			ServerStreaming: md.IsStreamingServer(),
			Descriptor:      md,
		})
	}
	return svc
}

// discoveryError wraps err with what was being attempted, mapping the
// "reflection is not implemented" case onto [ErrReflectionUnavailable] while
// keeping the original status reachable through the wrap chain.
func discoveryError(op string, err error) error {
	if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
		return fmt.Errorf("%s: %w: %w", op, ErrReflectionUnavailable, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}
