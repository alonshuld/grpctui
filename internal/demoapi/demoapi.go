// Package demoapi serves a small made-up API, so that a recording of grpctui
// has something worth discovering at the other end.
//
// It is a fixture, not a product: the responses are canned, nothing is stored,
// and every call succeeds. What makes it useful is that it is a *real* server —
// real reflection, real descriptors, real streams — so the tapes in docs/demos
// record grpctui doing exactly what it does against anything else.
//
// There is no generated code behind it. The service is compiled from the
// embedded demo.proto at startup and registered from those descriptors, with
// [dynamicpb] messages on both sides of every call, which is the same trick
// internal/grpcclient's streaming tests use and the reason adding a method here
// is a proto edit and a canned reply rather than a code generation step.
package demoapi

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bufbuild/protocompile"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

//go:embed demo.proto
var source embed.FS

// fileName is the embedded file, and the name its descriptors carry.
const fileName = "demo.proto"

// streamGap is how long the server waits between two messages of a stream. It
// is a demo: fast enough not to be tedious, slow enough that a GIF shows the
// messages arriving one after another rather than all at once.
const streamGap = 900 * time.Millisecond

// schema compiles the embedded proto and puts it in the global registry, once
// per process. Registering is what makes reflection able to answer for these
// descriptors, and doing it twice is an error — so a second server in the same
// process (two tests, say) reuses the first one's work.
var schema = sync.OnceValues(func() (protoreflect.FileDescriptor, error) {
	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: func(path string) (io.ReadCloser, error) { return source.Open(path) },
		}),
		SourceInfoMode: protocompile.SourceInfoNone,
	}

	files, err := compiler.Compile(context.Background(), fileName)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", fileName, err)
	}

	fd := files[0]
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		return nil, fmt.Errorf("register %s: %w", fileName, err)
	}
	return fd, nil
})

// Register serves every service demo.proto declares on srv.
//
// It registers real [grpc.ServiceDesc]s rather than handing the lot to
// grpc.UnknownServiceHandler, because reflection lists what the server says it
// has registered — and a service reflection cannot see is one grpctui cannot
// discover, which would defeat the point of the fixture.
func Register(srv *grpc.Server) error {
	fd, err := schema()
	if err != nil {
		return err
	}

	services := fd.Services()
	for i := range services.Len() {
		desc := serviceDesc(services.Get(i))
		srv.RegisterService(&desc, nil)
	}
	return nil
}

// Services returns the fully-qualified name of every service Register serves,
// in declaration order.
func Services() ([]string, error) {
	fd, err := schema()
	if err != nil {
		return nil, err
	}

	services := fd.Services()
	out := make([]string, 0, services.Len())
	for i := range services.Len() {
		out = append(out, string(services.Get(i).FullName()))
	}
	return out, nil
}

// serviceDesc builds the registration grpc wants from the descriptors the
// compiler produced. The handler type is nil, and RegisterService is passed a
// nil implementation to match: there is no service struct here, only closures
// over method descriptors.
func serviceDesc(sd protoreflect.ServiceDescriptor) grpc.ServiceDesc {
	out := grpc.ServiceDesc{
		ServiceName: string(sd.FullName()),
		HandlerType: (*any)(nil),
		Metadata:    fileName,
	}

	methods := sd.Methods()
	for i := range methods.Len() {
		md := methods.Get(i)
		switch {
		case md.IsStreamingClient() || md.IsStreamingServer():
			out.Streams = append(out.Streams, grpc.StreamDesc{
				StreamName:    string(md.Name()),
				Handler:       streamHandler(md),
				ServerStreams: md.IsStreamingServer(),
				ClientStreams: md.IsStreamingClient(),
			})
		default:
			out.Methods = append(out.Methods, grpc.MethodDesc{
				MethodName: string(md.Name()),
				Handler:    unaryHandler(md),
			})
		}
	}
	return out
}

// unaryHandler answers one message with one canned reply.
func unaryHandler(md protoreflect.MethodDescriptor) grpc.MethodHandler {
	return func(_ any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := dynamicpb.NewMessage(md.Input())
		if err := dec(in); err != nil {
			return nil, err
		}

		handler := func(context.Context, any) (any, error) { return reply(md, 0) }
		if interceptor == nil {
			return handler(ctx, in)
		}
		info := &grpc.UnaryServerInfo{FullMethod: fullMethod(md)}
		return interceptor(ctx, in, info, handler)
	}
}

// streamHandler drives whichever of the three streaming shapes md is.
func streamHandler(md protoreflect.MethodDescriptor) grpc.StreamHandler {
	return func(_ any, stream grpc.ServerStream) error {
		switch {
		case md.IsStreamingClient() && md.IsStreamingServer():
			return chat(md, stream)
		case md.IsStreamingClient():
			return collect(md, stream)
		default:
			return watch(md, stream)
		}
	}
}

// watch answers one request with a series of replies, spaced out so a stream
// looks like a stream.
//
// A method named in [endless] never runs out: it wraps round its replies and
// keeps going until the client goes away. That is the difference between a
// watch and a list, and it is the behaviour the streaming demo is about — a
// stream carries no timeout, and `esc` is what ends it.
func watch(md protoreflect.MethodDescriptor, stream grpc.ServerStream) error {
	in := dynamicpb.NewMessage(md.Input())
	if err := stream.RecvMsg(in); err != nil {
		return err
	}

	count := replyCount(md)
	for i := 0; endless[string(md.FullName())] || i < count; i++ {
		if i > 0 && !sleep(stream.Context(), streamGap) {
			return stream.Context().Err()
		}
		out, err := reply(md, i)
		if err != nil {
			return err
		}
		if err := stream.SendMsg(out); err != nil {
			return err
		}
	}
	return nil
}

// collect drains the client's messages and answers once, counting what arrived
// so that the reply is a function of the call rather than a constant.
func collect(md protoreflect.MethodDescriptor, stream grpc.ServerStream) error {
	var received int
	for {
		in := dynamicpb.NewMessage(md.Input())
		err := stream.RecvMsg(in)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		received++
	}

	out, err := reply(md, 0)
	if err != nil {
		return err
	}
	setInt32(out, "accepted", int32(received))
	return stream.SendMsg(out)
}

// chat answers each message with one of its own, which is what makes a bidi
// call worth watching: the two directions interleave.
func chat(md protoreflect.MethodDescriptor, stream grpc.ServerStream) error {
	for i := 0; ; i++ {
		in := dynamicpb.NewMessage(md.Input())
		err := stream.RecvMsg(in)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		out, err := reply(md, i)
		if err != nil {
			return err
		}
		if err := stream.SendMsg(out); err != nil {
			return err
		}
	}
}

// reply builds the i'th canned reply for md, wrapping round when a stream asks
// for more than were written.
func reply(md protoreflect.MethodDescriptor, i int) (proto.Message, error) {
	out := dynamicpb.NewMessage(md.Output())

	texts := replies[string(md.FullName())]
	if len(texts) == 0 {
		return out, nil
	}

	text := expand(texts[i%len(texts)])
	if err := protojson.Unmarshal([]byte(text), out); err != nil {
		return nil, fmt.Errorf("canned reply for %s: %w", md.FullName(), err)
	}
	return out, nil
}

// replyCount is how many messages a server-streaming call sends.
func replyCount(md protoreflect.MethodDescriptor) int {
	if n := len(replies[string(md.FullName())]); n > 0 {
		return n
	}
	return 1
}

// expand fills in the two time tokens a canned reply may carry. A demo whose
// timestamps are from the day it was written renders as "2 years ago", which
// is a poor advertisement for a renderer whose whole job is that gloss.
func expand(text string) string {
	now := time.Now().UTC()
	text = strings.ReplaceAll(text, "@now", now.Format(time.RFC3339))
	return strings.ReplaceAll(text, "@recent", now.Add(-3*time.Minute).Format(time.RFC3339))
}

// setInt32 sets a named int32 field, and does nothing if the message has no
// such field: the caller is filling in a count where one is wanted, not
// asserting a shape.
func setInt32(msg proto.Message, name string, value int32) {
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil || fd.Kind() != protoreflect.Int32Kind {
		return
	}
	m.Set(fd, protoreflect.ValueOfInt32(value))
}

// sleep waits for d, and reports false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func fullMethod(md protoreflect.MethodDescriptor) string {
	return "/" + string(md.Parent().FullName()) + "/" + string(md.Name())
}
