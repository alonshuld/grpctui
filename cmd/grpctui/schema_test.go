package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/protofiles"
)

const greeter = `syntax = "proto3";
package demo.v1;

message HelloRequest { string name = 1; }
message HelloReply { string message = 1; }

service Greeter { rpc SayHello(HelloRequest) returns (HelloReply); }
`

// protoFile writes a .proto into a temp directory and returns its path.
func protoFile(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "api.proto")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestCompileSchema(t *testing.T) {
	path := protoFile(t, greeter)

	t.Run("nothing configured", func(t *testing.T) {
		schema, err := compileSchema(context.Background(), config.Config{}, options{})
		require.NoError(t, err)
		assert.True(t, schema.Empty(), "discovery should go on asking the target")
	})

	t.Run("from the flags", func(t *testing.T) {
		schema, err := compileSchema(context.Background(), config.Config{},
			options{protoFiles: pathList{path}})
		require.NoError(t, err)
		require.Len(t, schema.Services(), 1)
		assert.Equal(t, "demo.v1.Greeter", string(schema.Services()[0].FullName()))
	})

	t.Run("from the config file", func(t *testing.T) {
		schema, err := compileSchema(context.Background(),
			config.Config{Proto: config.Proto{Files: []string{path}}}, options{})
		require.NoError(t, err)
		assert.Len(t, schema.Services(), 1)
	})

	// The flags replace the file's list rather than adding to it: quietly
	// compiling both is how you end up debugging a duplicate-symbol error about
	// a file you did not mention.
	t.Run("the flags replace the file", func(t *testing.T) {
		other := protoFile(t, `syntax = "proto3";
package other.v1;
message M { string s = 1; }
service Other { rpc Do(M) returns (M); }
`)

		schema, err := compileSchema(context.Background(),
			config.Config{Proto: config.Proto{Files: []string{path}}},
			options{protoFiles: pathList{other}})
		require.NoError(t, err)

		require.Len(t, schema.Services(), 1)
		assert.Equal(t, "other.v1.Other", string(schema.Services()[0].FullName()))
	})
}

func TestCompileSchema_Errors(t *testing.T) {
	tests := map[string]struct {
		cfg  config.Config
		opts options
		want string
	}{
		"a file that is not there": {
			opts: options{protoFiles: pathList{"nosuch.proto"}},
			want: "nosuch.proto",
		},
		"a ${VAR} nobody exported": {
			cfg:  config.Config{Proto: config.Proto{Files: []string{"${GRPCTUI_TEST_UNSET}/a.proto"}}},
			want: "GRPCTUI_TEST_UNSET",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := compileSchema(context.Background(), tt.cfg, tt.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestCompileSchema_NoServices pins that files which compile but declare
// nothing to call are refused. The user supplied a schema in order to browse an
// API, and an empty tree with nothing to say why is the worst possible answer.
func TestCompileSchema_NoServices(t *testing.T) {
	path := protoFile(t, "syntax = \"proto3\";\npackage demo.v1;\nmessage M { string s = 1; }\n")

	_, err := compileSchema(context.Background(), config.Config{},
		options{protoFiles: pathList{path}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declare no services")
}

// TestDialOptions pins that a compiled schema reaches every connection this
// process opens. A switcher that dropped it would fall back to reflection on
// exactly the targets that do not have it.
func TestDialOptions(t *testing.T) {
	logger := zap.NewNop()

	assert.Len(t, dialOptions(protofiles.Schema{}, logger), 1)

	schema, err := protofiles.Compile(context.Background(),
		protofiles.Options{Files: []string{protoFile(t, greeter)}})
	require.NoError(t, err)
	assert.Len(t, dialOptions(schema, logger), 2)
}

func TestPathList(t *testing.T) {
	var list pathList

	require.NoError(t, list.Set("a.proto"))
	require.NoError(t, list.Set("b.proto"))
	assert.Equal(t, pathList{"a.proto", "b.proto"}, list)
	assert.Equal(t, "a.proto, b.proto", list.String())

	require.Error(t, list.Set(""))
}
