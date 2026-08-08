// Package protofiles compiles .proto source into the descriptors grpctui would
// otherwise have got from server reflection.
//
// It is the fallback path, not the preferred one. grpctui's pitch is that
// pointing it at an address is enough, and reflection is what makes that true;
// this package exists for the servers where reflection is switched off — which
// in practice means production, where it is switched off deliberately, and old
// services that never turned it on. What comes out is the same
// [protoreflect.ServiceDescriptor] reflection would have produced, so every
// layer above this one is unchanged: the form tree, the invoker and the wire
// view cannot tell where a descriptor came from.
//
// It is a leaf like internal/vars: it imports protobuf and nothing of ours,
// which is what lets internal/grpcclient depend on it without a cycle.
package protofiles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Schema is a compiled set of .proto files.
//
// The zero Schema is empty and means "no files were given", which is the
// ordinary case: grpctui discovers by reflection unless told otherwise.
type Schema struct {
	files []protoreflect.FileDescriptor
}

// Empty reports whether the schema holds nothing, which is how a caller tells
// "compile these files" from "there were none".
func (s Schema) Empty() bool { return len(s.files) == 0 }

// Files returns the compiled files in the order they were named.
func (s Schema) Files() []protoreflect.FileDescriptor { return slices.Clone(s.files) }

// Services returns every service the files declare, sorted by fully-qualified
// name.
//
// Sorting matches what reflection discovery does, so the method tree looks the
// same whichever path filled it. A file that declares no service contributes
// nothing and is not an error: a request that imports three message-only files
// is entirely normal.
func (s Schema) Services() []protoreflect.ServiceDescriptor {
	var out []protoreflect.ServiceDescriptor
	for _, f := range s.files {
		svcs := f.Services()
		for i := range svcs.Len() {
			out = append(out, svcs.Get(i))
		}
	}
	slices.SortFunc(out, func(a, b protoreflect.ServiceDescriptor) int {
		return strings.Compare(string(a.FullName()), string(b.FullName()))
	})
	return out
}

// Options says what to compile.
type Options struct {
	// Files are the .proto files to compile, as paths on disk or as paths
	// relative to one of ImportPaths. Both spellings work, because both are what
	// people type: `grpctui --proto ./api/v1/greeter.proto` and `--import-path
	// ./api --proto v1/greeter.proto` name the same file.
	Files []string

	// ImportPaths are the directories an `import` statement is resolved against,
	// protoc's -I. Empty means the working directory, which is protoc's default
	// too.
	ImportPaths []string
}

// Compile parses and links the files.
//
// Errors carry the file and line protoc would have reported, because a syntax
// error in a .proto is a thing the user fixes in an editor and needs a position
// for. Compilation is bounded by ctx: a pathological import graph is somebody
// else's bug, not a reason for grpctui to hang before it has drawn a frame.
//
// Well-known imports — google/protobuf/timestamp.proto and the rest — resolve
// without being on disk, since they are compiled into the protobuf runtime this
// binary already links. Requiring the user to find a copy of descriptor.proto
// to call a method taking a Timestamp would be a poor first experience of the
// fallback path.
func Compile(ctx context.Context, opts Options) (Schema, error) {
	if len(opts.Files) == 0 {
		return Schema{}, nil
	}

	imports, files, err := plan(opts)
	if err != nil {
		return Schema{}, err
	}

	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			ImportPaths: imports,
		}),
		// Source info is comments and positions. Errors carry their own spans,
		// and nothing in grpctui renders a field's leading comment, so paying to
		// keep it would be paying for nothing.
		SourceInfoMode: protocompile.SourceInfoNone,
	}

	linked, err := compiler.Compile(ctx, files...)
	if err != nil {
		return Schema{}, fmt.Errorf("compile proto files: %w", err)
	}

	out := Schema{files: make([]protoreflect.FileDescriptor, 0, len(linked))}
	for _, f := range linked {
		out.files = append(out.files, f)
	}
	return out, nil
}

// plan works out what to hand the compiler: the import paths to search, and the
// files named relative to them.
//
// protocompile resolves every file against the import paths and nothing else,
// so a file given as ./api/v1/greeter.proto with no -I would not be found —
// which is exactly what someone reaching for this flag will type first. Each
// file that exists on disk therefore contributes its own directory tree as an
// import path, and is renamed to the part below it.
//
// The renaming matters beyond convenience. A file compiled under two different
// paths is two files as far as the linker is concerned, and it refuses the
// resulting duplicate symbols; deriving both halves from the same resolution is
// what keeps `--proto a/b.proto --proto a/c.proto` importing each other
// working.
func plan(opts Options) (imports, files []string, err error) {
	imports = slices.Clone(opts.ImportPaths)

	var errs []error
	for _, f := range opts.Files {
		name, dir, ok := locate(f, opts.ImportPaths)
		if !ok {
			errs = append(errs, fmt.Errorf("proto file %q: %w", f, os.ErrNotExist))
			continue
		}
		if dir != "" && !slices.Contains(imports, dir) {
			imports = append(imports, dir)
		}
		files = append(files, name)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	return imports, files, nil
}

// locate finds a file and says what to call it.
//
// A file already reachable under one of the import paths keeps the name it was
// given and adds no path — that is the protoc-shaped invocation, and honouring
// it is what makes an existing build's flags work here. Anything else is looked
// for on disk and split into a directory to search and a name below it.
func locate(file string, importPaths []string) (name, dir string, ok bool) {
	for _, ip := range importPaths {
		if exists(filepath.Join(ip, file)) {
			return file, "", true
		}
	}
	if !exists(file) {
		return "", "", false
	}

	dir, name = filepath.Split(filepath.ToSlash(file))
	if dir == "" {
		dir = "."
	}
	return name, filepath.Clean(dir), true
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
