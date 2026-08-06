// Package version reports the build's version.
package version

import "runtime/debug"

// version is set by GoReleaser via -ldflags. It is deliberately the *fallback*:
// ldflags are not applied when a user runs `go install`, so build info is
// consulted first and this only fills in for builds that have neither.
var version = "dev"

// Version returns the running binary's version.
//
// Build info wins over the ldflags value, because a `go install
// github.com/alonshuld/grpctui/cmd/grpctui@v0.1.0` build carries an accurate
// module version and no ldflags at all — reading them the other way round
// makes every such build report "dev".
func Version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return version
}
