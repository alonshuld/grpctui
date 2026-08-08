package config_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/testdocs"
)

// docsPath is the specification these keys are promised by. v1.0's guarantee is
// about a *documented* format, so a key that exists and is not written down is
// a key nobody can rely on.
const docsPath = "../../docs/formats.md"

// TestDocsNameEveryConfigKey walks the config struct and checks that every YAML
// key it accepts appears in docs/formats.md.
//
// It is the same trick flagGroups uses in cmd/grpctui: the failure it prevents —
// a key added to the struct and never written down — is otherwise silent, and
// found by a user who cannot make it work rather than by a test.
func TestDocsNameEveryConfigKey(t *testing.T) {
	body, err := os.ReadFile(docsPath)
	require.NoError(t, err)

	// The missing keys are collected and reported together rather than asserted
	// one at a time: assert.Contains prints the haystack on failure, and the
	// haystack here is the whole specification.
	missing := testdocs.Missing(string(body), reflect.TypeFor[config.Config]())

	assert.Empty(t, missing, "config keys missing from %s", docsPath)
}
