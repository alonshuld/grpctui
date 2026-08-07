package protoschema_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The fixture below is this file, built by hand rather than generated:
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
//	message Nested { string note = 1; int32 depth = 2; }
//
//	// A message that contains itself, which a form has to survive: expanding it
//	// eagerly would not terminate.
//	message Tree { string label = 1; repeated Tree children = 2; }
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
//	  oneof choice { string by_name = 16; int32 by_id = 17; Nested by_nested = 18; }
//	  optional string note = 19;
//	  int32 retry_count = 20;
//	  optional bool verbose = 21;
//	  repeated Nested notes = 22;
//	  Tree tree = 23;
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

// The files are built once. Descriptors have identity as well as content —
// dynamicpb rejects a field descriptor from a different build of the same file
// — so a fixture that rebuilt them per call could not load a message produced
// by one form into another.
var (
	testFile = sync.OnceValue(func() protoreflect.FileDescriptor {
		return buildFile(testFileProto())
	})
	requiredFile = sync.OnceValue(func() protoreflect.FileDescriptor {
		return buildFile(requiredFileProto())
	})
)

func scalarsDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	return messageDescriptor(t, testFile(), "Scalars")
}

func treeDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	return messageDescriptor(t, testFile(), "Tree")
}

// listsDescriptor is one repeated field per element type. An item added and
// left empty is sent as its type's zero value, and each of those goes through a
// different protoreflect constructor — a mismatch there is a panic, not a
// wrong answer, so every kind is worth its line:
//
//	message Lists {
//	  repeated int32  ints    = 1;
//	  repeated int64  longs   = 2;
//	  repeated uint32 uints   = 3;
//	  repeated uint64 ulongs  = 4;
//	  repeated float  floats  = 5;
//	  repeated double doubles = 6;
//	  repeated bool   flags   = 7;
//	  repeated bytes  blobs   = 8;
//	  repeated Colour colours = 9;
//	  repeated string texts   = 10;
//	}
func listsDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	return messageDescriptor(t, testFile(), "Lists")
}

func listsProto() *descriptorpb.DescriptorProto {
	return &descriptorpb.DescriptorProto{
		Name: proto.String("Lists"),
		Field: []*descriptorpb.FieldDescriptorProto{
			repeated(scalarField("ints", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32)),
			repeated(scalarField("longs", 2, descriptorpb.FieldDescriptorProto_TYPE_INT64)),
			repeated(scalarField("uints", 3, descriptorpb.FieldDescriptorProto_TYPE_UINT32)),
			repeated(scalarField("ulongs", 4, descriptorpb.FieldDescriptorProto_TYPE_UINT64)),
			repeated(scalarField("floats", 5, descriptorpb.FieldDescriptorProto_TYPE_FLOAT)),
			repeated(scalarField("doubles", 6, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE)),
			repeated(scalarField("flags", 7, descriptorpb.FieldDescriptorProto_TYPE_BOOL)),
			repeated(scalarField("blobs", 8, descriptorpb.FieldDescriptorProto_TYPE_BYTES)),
			repeated(namedField("colours", 9, descriptorpb.FieldDescriptorProto_TYPE_ENUM, "."+testPackage+".Colour")),
			repeated(scalarField("texts", 10, descriptorpb.FieldDescriptorProto_TYPE_STRING)),
		},
	}
}

// requiredDescriptor is a proto2 message with a required field. proto3 has no
// such thing, so validating one takes a file of its own:
//
//	syntax = "proto2";
//	package grpctui.test.v2;
//
//	message Ticket {
//	  required string id    = 1;
//	  optional string label = 2;
//	  optional Stamp  stamp = 3;
//	}
//
//	message Stamp { required int32 at = 1; }
func requiredDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	return messageDescriptor(t, requiredFile(), "Ticket")
}

func requiredFileProto() *descriptorpb.FileDescriptorProto {
	const pkg = "grpctui.test.v2"

	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("grpctui/test/v2/required.proto"),
		Package: proto.String(pkg),
		Syntax:  proto.String("proto2"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Ticket"),
				Field: []*descriptorpb.FieldDescriptorProto{
					required(scalarField("id", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)),
					scalarField("label", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING),
					namedField("stamp", 3, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, "."+pkg+".Stamp"),
				},
			},
			{
				Name: proto.String("Stamp"),
				Field: []*descriptorpb.FieldDescriptorProto{
					required(scalarField("at", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32)),
				},
			},
		},
	}
}

// buildFile panics rather than taking a *testing.T: a fixture that will not
// compile is a mistake in this file, not a test failure.
func buildFile(file *descriptorpb.FileDescriptorProto) protoreflect.FileDescriptor {
	fd, err := protodesc.NewFile(file, nil)
	if err != nil {
		panic(fmt.Sprintf("build %s: %v", file.GetName(), err))
	}
	return fd
}

func messageDescriptor(t *testing.T, file protoreflect.FileDescriptor, name string) protoreflect.MessageDescriptor {
	t.Helper()

	md := file.Messages().ByName(protoreflect.Name(name))
	require.NotNil(t, md, "%s is missing from %s", name, file.Path())
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
				Name: proto.String("Nested"),
				Field: []*descriptorpb.FieldDescriptorProto{
					scalarField("note", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
					scalarField("depth", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32),
				},
			},
			{
				Name: proto.String("Tree"),
				Field: []*descriptorpb.FieldDescriptorProto{
					scalarField("label", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
					repeated(namedField("children", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, "."+testPackage+".Tree")),
				},
			},
			scalarsProto(),
			listsProto(),
		},
	}
}

// A descriptor literal is a data table: it is long because the message is, and
// splitting it would hide the shape it describes.
func scalarsProto() *descriptorpb.DescriptorProto {
	const (
		colour = "." + testPackage + ".Colour"
		nested = "." + testPackage + ".Nested"
		tree   = "." + testPackage + ".Tree"
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
			inOneof(namedField("by_nested", 18, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, nested), 0),
			proto3Optional(scalarField("note", 19, descriptorpb.FieldDescriptorProto_TYPE_STRING), 1),
			// A multi-word name, to pin protobuf's lowerCamelCase JSON mapping.
			scalarField("retry_count", 20, descriptorpb.FieldDescriptorProto_TYPE_INT32),
			// A bool with explicit presence, where false and unset are different
			// things on the wire — unlike `flag` above.
			proto3Optional(scalarField("verbose", 21, descriptorpb.FieldDescriptorProto_TYPE_BOOL), 2),
			repeated(namedField("notes", 22, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, nested)),
			namedField("tree", 23, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, tree),
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

func required(f *descriptorpb.FieldDescriptorProto) *descriptorpb.FieldDescriptorProto {
	f.Label = descriptorpb.FieldDescriptorProto_LABEL_REQUIRED.Enum()
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
