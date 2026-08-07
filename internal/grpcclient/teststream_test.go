package grpcclient_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// The health service the rest of these tests run against has one unary method
// and one server-streaming one, and no client-streaming or bidi method at all —
// so the streaming tests bring their own service:
//
//	syntax = "proto3";
//	package demo.v1;
//
//	message Item    { string text = 1; }
//	message Summary { int32 count = 1; }
//
//	service Streamer {
//	  rpc Ticks(Item) returns (stream Item);           // server-streaming
//	  rpc Collect(stream Item) returns (Summary);      // client-streaming
//	  rpc Chat(stream Item) returns (stream Item);     // bidi
//	  rpc Fail(Item) returns (stream Item);            // fails after one message
//	}
//
// It is served by [streamHandler] through grpc.UnknownServiceHandler rather
// than registered, which is what lets a service exist with no generated code
// behind it. Reflection never sees it; the tests build the [grpcclient.Method]
// from these descriptors directly, exactly as the descriptors reflection hands
// back would be shaped.
const (
	streamerService = "demo.v1.Streamer"

	// ticks is how many messages Ticks sends before finishing.
	ticks = 3
)

// streamerFile builds the demo file descriptor. It panics rather than taking a
// *testing.T: a fixture that will not build is a mistake in this file.
var streamerFile = sync.OnceValue(func() protoreflect.FileDescriptor {
	fd, err := protodesc.NewFile(streamerFileProto(), nil)
	if err != nil {
		panic(fmt.Sprintf("build the streamer descriptors: %v", err))
	}
	return fd
})

// streamerMethod returns one method of the demo service, shaped the way
// reflection would report it.
func streamerMethod(t *testing.T, name string) grpcclient.Method {
	t.Helper()

	sd := streamerFile().Services().Get(0)
	md := sd.Methods().ByName(protoreflect.Name(name))
	require.NotNil(t, md, "no method %q on %s", name, streamerService)

	return grpcclient.Method{
		Name:            string(md.Name()),
		FullName:        string(md.FullName()),
		InputType:       string(md.Input().FullName()),
		OutputType:      string(md.Output().FullName()),
		ClientStreaming: md.IsStreamingClient(),
		ServerStreaming: md.IsStreamingServer(),
		Descriptor:      md,
	}
}

// item builds an Item request message.
func item(t *testing.T, m grpcclient.Method, text string) proto.Message {
	t.Helper()

	desc := m.InputDescriptor()
	require.NotNil(t, desc)

	msg := dynamicpb.NewMessage(desc)
	fd := desc.Fields().ByName("text")
	require.NotNil(t, fd, "%s has no text field", desc.FullName())
	msg.Set(fd, protoreflect.ValueOfString(text))
	return msg
}

// withStreamer serves the demo service on the test server.
func withStreamer() serverOption {
	return func(cfg *serverConfig) { cfg.unknown = streamHandler() }
}

// streamHandler answers every method of the demo service. It decodes into
// dynamicpb messages built from the same descriptors the client uses, which is
// what lets a service with no generated code behave like a real one.
func streamHandler() grpc.StreamHandler {
	return func(_ any, ss grpc.ServerStream) error {
		full, ok := grpc.MethodFromServerStream(ss)
		if !ok {
			return status.Error(codes.Internal, "no method on the stream")
		}

		name := full[strings.LastIndex(full, "/")+1:]
		sd := streamerFile().Services().Get(0)
		md := sd.Methods().ByName(protoreflect.Name(name))
		if md == nil {
			return status.Errorf(codes.Unimplemented, "no method %q", name)
		}

		in := func() *dynamicpb.Message { return dynamicpb.NewMessage(md.Input()) }
		out := func() *dynamicpb.Message { return dynamicpb.NewMessage(md.Output()) }

		switch name {
		case "Ticks":
			return serveTicks(ss, in(), out)
		case "Collect":
			return serveCollect(ss, in, out())
		case "Chat":
			return serveChat(ss, in, out)
		case "Fail":
			return serveFail(ss, in(), out())
		default:
			return status.Errorf(codes.Unimplemented, "no method %q", name)
		}
	}
}

// serveTicks answers one request with [ticks] messages, numbered so a test can
// tell them apart and assert on their order.
func serveTicks(ss grpc.ServerStream, req *dynamicpb.Message, out func() *dynamicpb.Message) error {
	if err := ss.RecvMsg(req); err != nil {
		return err
	}
	for i := 1; i <= ticks; i++ {
		if err := ss.SendMsg(setText(out(), fmt.Sprintf("%s-%d", textOf(req), i))); err != nil {
			return err
		}
	}
	return nil
}

// serveCollect reads the whole request stream and answers once with how many
// messages it saw — the shape of every real client-streaming API.
func serveCollect(ss grpc.ServerStream, in func() *dynamicpb.Message, out *dynamicpb.Message) error {
	count := 0
	for {
		err := ss.RecvMsg(in())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		count++
	}

	fd := out.Descriptor().Fields().ByName("count")
	out.Set(fd, protoreflect.ValueOfInt32(int32(count)))
	return ss.SendMsg(out)
}

// serveChat echoes each request message back as it arrives, so a bidi test can
// interleave sends and receives and see the two halves stay independent.
func serveChat(ss grpc.ServerStream, in, out func() *dynamicpb.Message) error {
	for {
		req := in()
		err := ss.RecvMsg(req)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ss.SendMsg(setText(out(), "echo: "+textOf(req))); err != nil {
			return err
		}
	}
}

// serveFail sends one message and then fails, which is the case that proves a
// stream's status survives the messages that came before it.
func serveFail(ss grpc.ServerStream, req, out *dynamicpb.Message) error {
	if err := ss.RecvMsg(req); err != nil {
		return err
	}
	if err := ss.SendMsg(setText(out, "before the failure")); err != nil {
		return err
	}
	return status.Error(codes.PermissionDenied, "not allowed to watch this")
}

// setText sets an Item's text field and returns the message.
func setText(msg *dynamicpb.Message, s string) *dynamicpb.Message {
	msg.Set(msg.Descriptor().Fields().ByName("text"), protoreflect.ValueOfString(s))
	return msg
}

// textOf reads an Item's text field.
func textOf(msg *dynamicpb.Message) string {
	return msg.Get(msg.Descriptor().Fields().ByName("text")).String()
}

// itemText reads the text field off a message the client decoded.
func itemText(t *testing.T, msg proto.Message) string {
	t.Helper()
	return responseField(t, msg, "text").String()
}

func streamerFileProto() *descriptorpb.FileDescriptorProto {
	const pkg = "demo.v1"

	str := func(name string, number int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:   proto.String(name),
			Number: proto.Int32(number),
			Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		}
	}
	i32 := func(name string, number int32) *descriptorpb.FieldDescriptorProto {
		f := str(name, number)
		f.Type = descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
		return f
	}
	rpc := func(name, in, out string, client, server bool) *descriptorpb.MethodDescriptorProto {
		return &descriptorpb.MethodDescriptorProto{
			Name:            proto.String(name),
			InputType:       proto.String("." + pkg + "." + in),
			OutputType:      proto.String("." + pkg + "." + out),
			ClientStreaming: proto.Bool(client),
			ServerStreaming: proto.Bool(server),
		}
	}

	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("demo/v1/streamer.proto"),
		Package: proto.String(pkg),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:  proto.String("Item"),
				Field: []*descriptorpb.FieldDescriptorProto{str("text", 1)},
			},
			{
				Name:  proto.String("Summary"),
				Field: []*descriptorpb.FieldDescriptorProto{i32("count", 1)},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("Streamer"),
			Method: []*descriptorpb.MethodDescriptorProto{
				rpc("Ticks", "Item", "Item", false, true),
				rpc("Collect", "Item", "Summary", true, false),
				rpc("Chat", "Item", "Item", true, true),
				rpc("Fail", "Item", "Item", false, true),
			},
		}},
	}
}
