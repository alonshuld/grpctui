package main

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protofiles"
)

// compileSchema compiles the .proto files the flags and the config file name,
// or returns the empty schema when there are none — in which case discovery
// goes on asking the target, which is grpctui's normal path.
//
// The flags replace the file's list rather than adding to it. A config file
// naming a project's protos is the standing arrangement; `--proto other.proto`
// on top of it is somebody looking at one other service, and quietly compiling
// both is how you end up debugging a duplicate-symbol error about a file you
// did not mention.
func compileSchema(ctx context.Context, cfg config.Config, opts options) (protofiles.Schema, error) {
	files, imports, err := cfg.Proto.Paths()
	if err != nil {
		return protofiles.Schema{}, err
	}
	if len(opts.protoFiles) > 0 {
		files, imports = opts.protoFiles, opts.importPaths
	}
	if len(opts.importPaths) > 0 {
		imports = opts.importPaths
	}

	schema, err := protofiles.Compile(ctx, protofiles.Options{
		Files:       files,
		ImportPaths: imports,
	})
	if err != nil {
		return protofiles.Schema{}, err
	}

	// Files that compile but declare no service are almost certainly not what
	// the user meant: they have supplied a schema in order to browse an API, and
	// the tree would come up empty with nothing to say why.
	if !schema.Empty() && len(schema.Services()) == 0 {
		return protofiles.Schema{}, fmt.Errorf("proto files declare no services: %v", files)
	}
	return schema, nil
}

// dialOptions are the options every connection this process opens is dialled
// with: the logger, and the compiled schema when there is one.
//
// It exists so that the places a client is created — startup, the profile
// switcher, and headless mode — cannot disagree about it. A switcher that
// dropped the schema would fall back to reflection on exactly the targets that
// do not have it, and the failure would look like the connection rather than
// like the flag.
func dialOptions(schema protofiles.Schema, logger *zap.Logger) []grpcclient.DialOption {
	opts := []grpcclient.DialOption{grpcclient.WithLogger(logger)}
	if !schema.Empty() {
		opts = append(opts, grpcclient.WithSchema(schema.Services()))
	}
	return opts
}
