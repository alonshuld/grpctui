package ui_test

import (
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// The fixture below is this file, built by hand so that the UI tests need
// neither protoc nor a server:
//
//	syntax = "proto3";
//	package demo.v1;
//
//	enum Volume { VOLUME_UNSPECIFIED = 0; VOLUME_QUIET = 1; VOLUME_LOUD = 2; }
//
//	message EchoRequest  { string message = 1; }
//	message EchoReply    { string message = 1; }
//	message Address      { string city = 1; string postcode = 2; }
//	message HelloRequest {
//	  string   name    = 1;
//	  int32    times   = 2;
//	  bool     shout   = 3;
//	  Volume   volume  = 4;
//	  repeated string aliases = 5;
//	  Address  address = 6;
//	  oneof greeting { string custom = 7; bool automatic = 8; }
//	}
//	message HelloReply   { string greeting = 1; int32 count = 2; }
//
//	service Echo    { rpc Echo(EchoRequest) returns (EchoReply); }
//	service Greeter {
//	  rpc SayHello(HelloRequest) returns (HelloReply);
//	  rpc SayHelloStream(HelloRequest) returns (stream HelloReply);
//	  rpc CollectHellos(stream HelloRequest) returns (HelloReply);
//	  rpc Converse(stream HelloRequest) returns (stream HelloReply);
//	}
//
// All four method kinds are present on purpose: from v0.5 each one is driven
// differently, and a fixture with only unary and server-streaming methods would
// let the other two paths go untested.
//
// The descriptors are real, so the request form built from them is real too —
// which is the whole point: a fixture with no descriptor would let a broken
// form panel pass its tests by rendering nothing.
//
// It is built once, and panics rather than taking a *testing.T: a fixture that
// will not compile is a mistake in this file, not a test failure, and threading
// a t through every caller would put one inside each fake the golden runs
// install.
var demoFile = sync.OnceValue(func() protoreflect.FileDescriptor {
	fd, err := protodesc.NewFile(demoFileProto(), nil)
	if err != nil {
		panic(fmt.Sprintf("build the demo descriptors: %v", err))
	}
	return fd
})

// testServices returns the demo file as the transport layer would report it.
func testServices() []grpcclient.Service {
	fd := demoFile()
	services := make([]grpcclient.Service, 0, fd.Services().Len())
	for i := range fd.Services().Len() {
		services = append(services, serviceFrom(fd.Services().Get(i)))
	}
	return services
}

func serviceFrom(sd protoreflect.ServiceDescriptor) grpcclient.Service {
	mds := sd.Methods()
	svc := grpcclient.Service{
		Name:    string(sd.FullName()),
		Methods: make([]grpcclient.Method, 0, mds.Len()),
	}
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
	return svc
}

func demoFileProto() *descriptorpb.FileDescriptorProto {
	const pkg = "demo.v1"

	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("demo/v1/demo.proto"),
		Package: proto.String(pkg),
		Syntax:  proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Volume"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("VOLUME_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("VOLUME_QUIET"), Number: proto.Int32(1)},
				{Name: proto.String("VOLUME_LOUD"), Number: proto.Int32(2)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:  proto.String("EchoRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{stringField("message", 1)},
			},
			{
				Name:  proto.String("EchoReply"),
				Field: []*descriptorpb.FieldDescriptorProto{stringField("message", 1)},
			},
			{
				Name: proto.String("Address"),
				Field: []*descriptorpb.FieldDescriptorProto{
					stringField("city", 1),
					stringField("postcode", 2),
				},
			},
			{
				Name: proto.String("HelloRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					stringField("name", 1),
					typedField("times", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32),
					typedField("shout", 3, descriptorpb.FieldDescriptorProto_TYPE_BOOL),
					enumField("volume", 4, "."+pkg+".Volume"),
					repeatedField(stringField("aliases", 5)),
					messageField("address", 6, "."+pkg+".Address"),
					oneofField(stringField("custom", 7), 0),
					oneofField(typedField("automatic", 8, descriptorpb.FieldDescriptorProto_TYPE_BOOL), 0),
				},
				OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("greeting")}},
			},
			{
				Name: proto.String("HelloReply"),
				Field: []*descriptorpb.FieldDescriptorProto{
					stringField("greeting", 1),
					typedField("count", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32),
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("Echo"),
				Method: []*descriptorpb.MethodDescriptorProto{
					rpc("Echo", "."+pkg+".EchoRequest", "."+pkg+".EchoReply", false, false),
				},
			},
			{
				Name: proto.String("Greeter"),
				Method: []*descriptorpb.MethodDescriptorProto{
					rpc("SayHello", "."+pkg+".HelloRequest", "."+pkg+".HelloReply", false, false),
					rpc("SayHelloStream", "."+pkg+".HelloRequest", "."+pkg+".HelloReply", false, true),
					rpc("CollectHellos", "."+pkg+".HelloRequest", "."+pkg+".HelloReply", true, false),
					rpc("Converse", "."+pkg+".HelloRequest", "."+pkg+".HelloReply", true, true),
				},
			},
		},
	}
}

func typedField(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:   proto.String(name),
		Number: proto.Int32(number),
		Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:   kind.Enum(),
	}
}

func stringField(name string, number int32) *descriptorpb.FieldDescriptorProto {
	return typedField(name, number, descriptorpb.FieldDescriptorProto_TYPE_STRING)
}

func enumField(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
	f := typedField(name, number, descriptorpb.FieldDescriptorProto_TYPE_ENUM)
	f.TypeName = proto.String(typeName)
	return f
}

func messageField(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
	f := typedField(name, number, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	f.TypeName = proto.String(typeName)
	return f
}

func repeatedField(f *descriptorpb.FieldDescriptorProto) *descriptorpb.FieldDescriptorProto {
	f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	return f
}

func oneofField(f *descriptorpb.FieldDescriptorProto, index int32) *descriptorpb.FieldDescriptorProto {
	f.OneofIndex = proto.Int32(index)
	return f
}

func rpc(name, in, out string, clientStreaming, serverStreaming bool) *descriptorpb.MethodDescriptorProto {
	m := &descriptorpb.MethodDescriptorProto{
		Name:       proto.String(name),
		InputType:  proto.String(in),
		OutputType: proto.String(out),
	}
	if clientStreaming {
		m.ClientStreaming = proto.Bool(true)
	}
	if serverStreaming {
		m.ServerStreaming = proto.Bool(true)
	}
	return m
}
