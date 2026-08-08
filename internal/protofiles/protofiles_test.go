package protofiles_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/protofiles"
)

// greeterProto is the schema most of these tests compile. It imports a
// well-known type on purpose: resolving google/protobuf/timestamp.proto without
// a copy on disk is the part a user meets first and the part most likely to
// break.
const greeterProto = `syntax = "proto3";
package demo.v1;

import "google/protobuf/timestamp.proto";

message HelloRequest {
  string name = 1;
  google.protobuf.Timestamp at = 2;
}
message HelloReply { string message = 1; }

service Greeter {
  rpc SayHello(HelloRequest) returns (HelloReply);
  rpc Chat(stream HelloRequest) returns (stream HelloReply);
}
`

// write puts files into a temp directory and returns it. Keys are
// slash-separated paths relative to the directory.
func write(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	return dir
}

func TestCompile(t *testing.T) {
	t.Parallel()

	dir := write(t, map[string]string{"api/v1/greeter.proto": greeterProto})

	// Both spellings name the same file, and both have to work: the first is
	// what somebody types without thinking, the second is what an existing
	// protoc invocation already says.
	tests := map[string]protofiles.Options{
		"a path on disk": {
			Files: []string{filepath.Join(dir, "api", "v1", "greeter.proto")},
		},
		"relative to an import path": {
			Files:       []string{"api/v1/greeter.proto"},
			ImportPaths: []string{dir},
		},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := protofiles.Compile(context.Background(), opts)
			require.NoError(t, err)
			require.False(t, schema.Empty())

			services := schema.Services()
			require.Len(t, services, 1)
			assert.Equal(t, "demo.v1.Greeter", string(services[0].FullName()))
			assert.Equal(t, 2, services[0].Methods().Len())

			// The imported well-known type resolved, or the request message
			// would not have its second field.
			input := services[0].Methods().Get(0).Input()
			require.Equal(t, 2, input.Fields().Len())
			assert.Equal(t, "google.protobuf.Timestamp",
				string(input.Fields().Get(1).Message().FullName()))
		})
	}
}

func TestCompile_NoFiles(t *testing.T) {
	t.Parallel()

	schema, err := protofiles.Compile(context.Background(), protofiles.Options{})
	require.NoError(t, err)
	assert.True(t, schema.Empty())
	assert.Empty(t, schema.Services())
	assert.Empty(t, schema.Files())
}

func TestCompile_Errors(t *testing.T) {
	t.Parallel()

	broken := write(t, map[string]string{"broken.proto": "syntax = \"proto3\";\nmessage {\n"})
	missingImport := write(t, map[string]string{
		"a.proto": "syntax = \"proto3\";\nimport \"nowhere.proto\";\n",
	})

	tests := map[string]struct {
		opts protofiles.Options
		want string
	}{
		"a file that is not there": {
			opts: protofiles.Options{Files: []string{"nosuch.proto"}},
			want: "nosuch.proto",
		},
		"a syntax error": {
			opts: protofiles.Options{Files: []string{filepath.Join(broken, "broken.proto")}},
			want: "broken.proto",
		},
		"an import that resolves to nothing": {
			opts: protofiles.Options{Files: []string{filepath.Join(missingImport, "a.proto")}},
			want: "nowhere.proto",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := protofiles.Compile(context.Background(), tc.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.True(t, schema.Empty())
		})
	}
}

// TestCompile_TwoFilesImportingEachOther pins the reason [Compile] derives a
// file's import path from the same resolution that found it. Compiled under two
// different names, one file becomes two as far as the linker is concerned, and
// the duplicate symbols are refused.
func TestCompile_TwoFilesImportingEachOther(t *testing.T) {
	t.Parallel()

	dir := write(t, map[string]string{
		"types.proto": `syntax = "proto3";
package demo.v1;
message Item { string text = 1; }
`,
		"service.proto": `syntax = "proto3";
package demo.v1;
import "types.proto";
service Items { rpc Get(Item) returns (Item); }
`,
	})

	schema, err := protofiles.Compile(context.Background(), protofiles.Options{
		Files: []string{
			filepath.Join(dir, "service.proto"),
			filepath.Join(dir, "types.proto"),
		},
	})
	require.NoError(t, err)

	services := schema.Services()
	require.Len(t, services, 1)
	assert.Equal(t, "demo.v1.Items", string(services[0].FullName()))
	assert.Len(t, schema.Files(), 2)
}

// TestSchema_ServicesSorted pins the ordering, which matches what reflection
// discovery does: the method tree has to look the same whichever path filled
// it.
func TestSchema_ServicesSorted(t *testing.T) {
	t.Parallel()

	dir := write(t, map[string]string{
		"z.proto": `syntax = "proto3";
package demo.v1;
message M { string s = 1; }
service Zebra { rpc Do(M) returns (M); }
service Aardvark { rpc Do(M) returns (M); }
`,
	})

	schema, err := protofiles.Compile(context.Background(), protofiles.Options{
		Files: []string{filepath.Join(dir, "z.proto")},
	})
	require.NoError(t, err)

	var names []string
	for _, sd := range schema.Services() {
		names = append(names, string(sd.FullName()))
	}
	assert.Equal(t, []string{"demo.v1.Aardvark", "demo.v1.Zebra"}, names)
}

// TestCompile_MessagesOnly pins that a file with no service is not an error: a
// schema importing three message-only files is entirely ordinary.
func TestCompile_MessagesOnly(t *testing.T) {
	t.Parallel()

	dir := write(t, map[string]string{
		"types.proto": "syntax = \"proto3\";\npackage demo.v1;\nmessage Item { string text = 1; }\n",
	})

	schema, err := protofiles.Compile(context.Background(), protofiles.Options{
		Files: []string{filepath.Join(dir, "types.proto")},
	})
	require.NoError(t, err)
	assert.False(t, schema.Empty())
	assert.Empty(t, schema.Services())
}

// TestCompile_Cancelled pins that ctx bounds the work: grpctui must not hang
// before it has drawn a frame.
func TestCompile_Cancelled(t *testing.T) {
	t.Parallel()

	dir := write(t, map[string]string{"api.proto": greeterProto})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := protofiles.Compile(ctx, protofiles.Options{
		Files: []string{filepath.Join(dir, "api.proto")},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
