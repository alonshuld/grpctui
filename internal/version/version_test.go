package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersion(t *testing.T) {
	// Under `go test` the main module reports "" or "(devel)", so the ldflags
	// fallback is what shows through.
	assert.Equal(t, "dev", Version())
}

func TestVersion_LdflagsFallbackIsSet(t *testing.T) {
	// Guards the -X path: GoReleaser sets this symbol, so renaming it silently
	// makes every release binary report "dev".
	assert.NotEmpty(t, version)
}
