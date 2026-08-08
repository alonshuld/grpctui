package runner_test

import (
	"context"
	"fmt"
	"io"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// The service these tests replay collections against:
//
//	syntax = "proto3";
//	package demo.v1;
//
//	message Item { string text = 1; int64 count = 2; }
//
//	service Runner {
//	  rpc Echo(Item) returns (Item);                 // unary
//	  rpc Watch(Item) returns (stream Item);         // server-streaming
//	  rpc Collect(stream Item) returns (Item);       // client-streaming
//	  rpc Chat(stream Item) returns (stream Item);   // bidi
//	}
//
// It is served by a fake rather than a bufconn server, unlike the tests in
// internal/grpcclient. What is under test here is the *replay* — rebuilding a
// saved request, the order calls go out in, the report, the exit code — and a
// real server would only make those slower to exercise without making any of
// them more true. The wire itself is internal/grpcclient's subject.
const service = "demo.v1.Runner"

// fixture builds the descriptors once. It panics rather than taking a
// *testing.T: a fixture that will not build is a mistake in this file.
var fixture = sync.OnceValue(func() protoreflect.FileDescriptor {
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	i64 := descriptorpb.FieldDescriptorProto_TYPE_INT64
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL

	method := func(name string, clientStream, serverStream bool) *descriptorpb.MethodDescriptorProto {
		return &descriptorpb.MethodDescriptorProto{
			Name:            proto.String(name),
			InputType:       proto.String(".demo.v1.Item"),
			OutputType:      proto.String(".demo.v1.Item"),
			ClientStreaming: proto.Bool(clientStream),
			ServerStreaming: proto.Bool(serverStream),
		}
	}

	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("demo/v1/runner.proto"),
		Package: proto.String("demo.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Item"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("text"), Number: proto.Int32(1), Type: &str, Label: &optional, JsonName: proto.String("text")},
				{Name: proto.String("count"), Number: proto.Int32(2), Type: &i64, Label: &optional, JsonName: proto.String("count")},
			},
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("Runner"),
			Method: []*descriptorpb.MethodDescriptorProto{
				method("Echo", false, false),
				method("Watch", false, true),
				method("Collect", true, false),
				method("Chat", true, true),
			},
		}},
	}, nil)
	if err != nil {
		panic(fmt.Sprintf("build the runner descriptors: %v", err))
	}
	return fd
})

// discovered is what the fake client's discovery returns: the fixture's service,
// shaped exactly as reflection would hand it back.
var discovered = sync.OnceValue(func() []grpcclient.Service {
	sd := fixture().Services().Get(0)

	svc := grpcclient.Service{Name: service}
	mds := sd.Methods()
	for i := range mds.Len() {
		md := mds.Get(i)
		svc.Methods = append(svc.Methods, grpcclient.Method{
			Name:            string(md.Name()),
			FullName:        string(md.FullName()),
			InputType:       string(md.Input().FullName()),
			OutputType:      string(md.Output().FullName()),
			ClientStreaming: md.IsStreamingClient(),
			ServerStreaming: md.IsStreamingServer(),
			Descriptor:      md,
		})
	}
	return []grpcclient.Service{svc}
})

// call records one invocation, so a test can assert what actually went out.
type call struct {
	method  string
	request proto.Message
	headers grpcclient.Metadata
}

// fakeClient answers discovery from the fixture and invocations from a script.
type fakeClient struct {
	target string

	// discoverErr fails discovery, which is a failure of the run rather than of
	// any one request.
	discoverErr error

	// unary answers a unary call, keyed by the request's `text` field. The entry
	// under "" answers anything not otherwise listed.
	unary map[string]unaryAnswer

	// streamed is what a server-streaming call sends before finishing, and
	// streamErr how it ends instead of cleanly.
	streamed  []string
	streamErr error

	// slow makes every call wait for the context, which is how the timeout is
	// exercised without a real server that is actually slow.
	slow bool

	calls []call
}

type unaryAnswer struct {
	reply string
	err   error
}

func (c *fakeClient) Target() string {
	if c.target == "" {
		return "fake:0"
	}
	return c.target
}

func (c *fakeClient) ListServices(_ context.Context, _ grpcclient.Metadata) ([]grpcclient.Service, error) {
	if c.discoverErr != nil {
		return nil, c.discoverErr
	}
	return discovered(), nil
}

func (c *fakeClient) InvokeUnary(ctx context.Context, method grpcclient.Method, req proto.Message, md grpcclient.Metadata) (*grpcclient.UnaryResponse, error) {
	c.calls = append(c.calls, call{method: method.FullName, request: req, headers: md})

	if c.slow {
		<-ctx.Done()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	answer, ok := c.unary[text(req)]
	if !ok {
		answer = c.unary[""]
	}
	if answer.err != nil {
		return nil, answer.err
	}
	return &grpcclient.UnaryResponse{Message: item(answer.reply)}, nil
}

func (c *fakeClient) InvokeStream(_ context.Context, method grpcclient.Method, md grpcclient.Metadata) (grpcclient.Stream, error) {
	c.calls = append(c.calls, call{method: method.FullName, headers: md})
	return &fakeStream{method: method, replies: c.streamed, err: c.streamErr}, nil
}

// item builds an Item with the given text.
func item(s string) proto.Message {
	md := fixture().Messages().ByName("Item")
	msg := dynamicpb.NewMessage(md)
	msg.Set(md.Fields().ByName("text"), protoreflect.ValueOfString(s))
	return msg
}

// text reads the `text` field off a request, which is how the script is keyed.
func text(msg proto.Message) string {
	if msg == nil {
		return ""
	}
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName("text")
	if fd == nil {
		return ""
	}
	return m.Get(fd).String()
}

// fakeStream hands back a fixed list of messages and then ends.
type fakeStream struct {
	method  grpcclient.Method
	replies []string
	err     error

	at     int
	closed bool
	sent   []proto.Message
}

func (s *fakeStream) Method() grpcclient.Method { return s.method }

func (s *fakeStream) Send(req proto.Message) error {
	s.sent = append(s.sent, req)
	return nil
}

func (s *fakeStream) CloseSend() error {
	s.closed = true
	return nil
}

func (s *fakeStream) Recv() (proto.Message, error) {
	if s.at < len(s.replies) {
		s.at++
		return item(s.replies[s.at-1]), nil
	}
	if s.err != nil {
		return nil, s.err
	}
	return nil, io.EOF
}

func (s *fakeStream) Close() error { return nil }

// refused is a gRPC status error, as a server that answered would produce.
func refused(code codes.Code, message string) error {
	return status.Error(code, message)
}
