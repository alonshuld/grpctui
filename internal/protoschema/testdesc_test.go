package protoschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The fixture below is this message, built by hand rather than generated:
//
//	syntax = "proto3";
//	package grpctui.test.v1;
//
//	enum Colour {
//	  COLOUR_UNSPECIFIED = 0;
//	  COLOUR_RED         = 1;
//	  COLOUR_BLUE        = 2;
//	}
//
//	message Nested { string note = 1; }
//
//	message Scalars {
//	  string   text    = 1;
//	  bool     flag    = 2;
//	  int32    count   = 3;
//	  int64    total   = 4;
//	  uint32   port    = 5;
//	  uint64   size    = 6;
//	  sint32   delta   = 7;
//	  fixed64  offset  = 8;
//	  float    ratio   = 9;
//	  double   weight  = 10;
//	  bytes    payload = 11;
//	  Colour   colour  = 12;
//	  Nested   nested  = 13;
//	  repeated string tags = 14;
//	  map<string, string> labels = 15;
//	  oneof choice { string by_name = 16; int32 by_id = 17; }
//	  optional string note = 18;
//	  int32 retry_count = 19;
//	  optional bool verbose = 20;
//	}
//
// Generating it would mean a protoc dependency in a repo that otherwise needs
// only the Go toolchain, and compiling it from source text at test time would
// mean pulling in a compiler. Building the descriptor directly costs one
// verbose function and nothing else.
const (
	testPackage = "grpctui.test.v1"
	testMessage = "grpctui.test.v1.Scalars"
)

func scalarsDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()

	fd, err := protodesc.NewFile(testFileProto(), nil)
	require.NoError(t, err)

	md := fd.Messages().ByName("Scalars")
	require.NotNil(t, md, "Scalars is missing from the test file")
	return md
}

func testFileProto() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("grpctui/test/v1/test.proto"),
		Package: proto.String(testPackage),
		Syntax:  proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Colour"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("COLOUR_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("COLOUR_RED"), Number: proto.Int32(1)},
				{Name: proto.String("COLOUR_BLUE"), Number: proto.Int32(2)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:  proto.String("Nested"),
				Field: []*descriptorpb.FieldDescriptorProto{scalarField("note", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)},
			},
			scalarsProto(),
		},
	}
}

// A descriptor literal is a data table: it is long because the message is, and
// splitting it would hide the shape it describes.
func scalarsProto() *descriptorpb.DescriptorProto {
	const (
		colour = "." + testPackage + ".Colour"
		nested = "." + testPackage + ".Nested"
		entry  = "." + testMessage + ".LabelsEntry"
	)

	msg := &descriptorpb.DescriptorProto{
		Name: proto.String("Scalars"),
		Field: []*descriptorpb.FieldDescriptorProto{
			scalarField("text", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			scalarField("flag", 2, descriptorpb.FieldDescriptorProto_TYPE_BOOL),
			scalarField("count", 3, descriptorpb.FieldDescriptorProto_TYPE_INT32),
			scalarField("total", 4, descriptorpb.FieldDescriptorProto_TYPE_INT64),
			scalarField("port", 5, descriptorpb.FieldDescriptorProto_TYPE_UINT32),
			scalarField("size", 6, descriptorpb.FieldDescriptorProto_TYPE_UINT64),
			scalarField("delta", 7, descriptorpb.FieldDescriptorProto_TYPE_SINT32),
			scalarField("offset", 8, descriptorpb.FieldDescriptorProto_TYPE_FIXED64),
			scalarField("ratio", 9, descriptorpb.FieldDescriptorProto_TYPE_FLOAT),
			scalarField("weight", 10, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE),
			scalarField("payload", 11, descriptorpb.FieldDescriptorProto_TYPE_BYTES),
			namedField("colour", 12, descriptorpb.FieldDescriptorProto_TYPE_ENUM, colour),
			namedField("nested", 13, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, nested),
			repeated(scalarField("tags", 14, descriptorpb.FieldDescriptorProto_TYPE_STRING)),
			repeated(namedField("labels", 15, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, entry)),
			inOneof(scalarField("by_name", 16, descriptorpb.FieldDescriptorProto_TYPE_STRING), 0),
			inOneof(scalarField("by_id", 17, descriptorpb.FieldDescriptorProto_TYPE_INT32), 0),
			proto3Optional(scalarField("note", 18, descriptorpb.FieldDescriptorProto_TYPE_STRING), 1),
			// A multi-word name, to pin protobuf's lowerCamelCase JSON mapping.
			scalarField("retry_count", 19, descriptorpb.FieldDescriptorProto_TYPE_INT32),
			// A bool with explicit presence, where false and unset are different
			// things on the wire — unlike `flag` above.
			proto3Optional(scalarField("verbose", 20, descriptorpb.FieldDescriptorProto_TYPE_BOOL), 2),
		},
		// The synthetic oneofs backing `optional note` and `optional verbose`
		// must follow every real one, which is why "choice" is declared first.
		OneofDecl: []*descriptorpb.OneofDescriptorProto{
			{Name: proto.String("choice")},
			{Name: proto.String("_note")},
			{Name: proto.String("_verbose")},
		},
		NestedType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("LabelsEntry"),
			Field: []*descriptorpb.FieldDescriptorProto{
				scalarField("key", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
				scalarField("value", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			},
			Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
		}},
	}
	return msg
}

func scalarField(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:   proto.String(name),
		Number: proto.Int32(number),
		Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:   kind.Enum(),
	}
}

func namedField(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
	f := scalarField(name, number, kind)
	f.TypeName = proto.String(typeName)
	return f
}

func repeated(f *descriptorpb.FieldDescriptorProto) *descriptorpb.FieldDescriptorProto {
	f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	return f
}

func inOneof(f *descriptorpb.FieldDescriptorProto, index int32) *descriptorpb.FieldDescriptorProto {
	f.OneofIndex = proto.Int32(index)
	return f
}

func proto3Optional(f *descriptorpb.FieldDescriptorProto, index int32) *descriptorpb.FieldDescriptorProto {
	f.OneofIndex = proto.Int32(index)
	f.Proto3Optional = proto.Bool(true)
	return f
}
