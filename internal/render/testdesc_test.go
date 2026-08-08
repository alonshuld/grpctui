package render_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/alonshuld/grpctui/internal/protofiles"
)

// The messages these tests gloss. They are compiled from source rather than
// generated because what is being tested is a walk over *descriptors*: the
// responses grpctui annotates are dynamicpb messages built from whatever a
// server or a .proto file described, and a generated Go type would be a
// different thing wearing the same name.
//
//	syntax = "proto3";
//	package demo.v1;
//
//	import "google/protobuf/duration.proto";
//	import "google/protobuf/struct.proto";
//	import "google/protobuf/timestamp.proto";
//
//	message Inner {
//	  google.protobuf.Timestamp updated_at = 1;
//	}
//
//	message Item {
//	  string name = 1;
//	  google.protobuf.Timestamp at = 2;
//	  google.protobuf.Timestamp created_at = 3;
//	  google.protobuf.Duration ttl = 4;
//	  Inner inner = 5;
//	  repeated Inner items = 6;
//	  google.protobuf.Struct payload = 7;
//	  map<string, Inner> by_key = 8;
//	}
const fixtureProto = `syntax = "proto3";
package demo.v1;

import "google/protobuf/duration.proto";
import "google/protobuf/struct.proto";
import "google/protobuf/timestamp.proto";

message Inner {
  google.protobuf.Timestamp updated_at = 1;
}

message Item {
  string name = 1;
  google.protobuf.Timestamp at = 2;
  google.protobuf.Timestamp created_at = 3;
  google.protobuf.Duration ttl = 4;
  Inner inner = 5;
  repeated Inner items = 6;
  google.protobuf.Struct payload = 7;
  map<string, Inner> by_key = 8;
}
`

// fixtureFile compiles the schema once for the whole package. It panics rather
// than taking a *testing.T: a fixture that will not compile is a mistake in
// this file, not a test failure worth reporting per test.
var fixtureFile = sync.OnceValue(func() protoreflect.FileDescriptor {
	dir, err := os.MkdirTemp("", "render-fixture")
	if err != nil {
		panic(fmt.Sprintf("make a temp dir: %v", err))
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "fixture.proto")
	if err := os.WriteFile(path, []byte(fixtureProto), 0o600); err != nil {
		panic(fmt.Sprintf("write the fixture: %v", err))
	}

	schema, err := protofiles.Compile(context.Background(), protofiles.Options{Files: []string{path}})
	if err != nil {
		panic(fmt.Sprintf("compile the fixture: %v", err))
	}
	return schema.Files()[0]
})

// message builds an empty message of the named type.
func message(t *testing.T, name string) *dynamicpb.Message {
	t.Helper()

	md := fixtureFile().Messages().ByName(protoreflect.Name(name))
	require.NotNil(t, md, "no message named %q in the fixture", name)
	return dynamicpb.NewMessage(md)
}

// set puts a message-typed field on another message, by field name.
func set(t *testing.T, msg *dynamicpb.Message, field string, value proto.Message) {
	t.Helper()

	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(field))
	require.NotNil(t, fd, "no field named %q", field)
	msg.Set(fd, protoreflect.ValueOfMessage(value.ProtoReflect()))
}

// appendItem grows a repeated message field by one and returns the new entry.
func appendItem(t *testing.T, msg *dynamicpb.Message, field string, value proto.Message) {
	t.Helper()

	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(field))
	require.NotNil(t, fd, "no field named %q", field)

	list := msg.Mutable(fd).List()
	list.Append(protoreflect.ValueOfMessage(value.ProtoReflect()))
}

// putEntry adds one entry to a map field whose values are messages.
func putEntry(t *testing.T, msg *dynamicpb.Message, field, key string, value proto.Message) {
	t.Helper()

	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(field))
	require.NotNil(t, fd, "no field named %q", field)

	msg.Mutable(fd).Map().Set(
		protoreflect.ValueOfString(key).MapKey(),
		protoreflect.ValueOfMessage(value.ProtoReflect()),
	)
}
