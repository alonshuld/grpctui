package requests_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/testdocs"
)

// docsPath is the specification a collection is promised by. A collection is
// committed beside a project and replayed in CI by whatever grpctui the next
// person has, so what is in it has to be written down somewhere other than the
// struct that happens to read it.
const docsPath = "../../docs/formats.md"

// TestDocsNameEveryCollectionKey checks that every YAML key a collection file
// accepts appears in docs/formats.md. A key added to [requests.Request] and
// never documented is a key nobody outside this repo can rely on.
func TestDocsNameEveryCollectionKey(t *testing.T) {
	body, err := os.ReadFile(docsPath)
	require.NoError(t, err)

	// Reported together rather than one assertion each: assert.Contains prints
	// the haystack, and the haystack is the whole specification.
	missing := testdocs.Missing(string(body), reflect.TypeFor[requests.Collection]())

	assert.Empty(t, missing, "collection keys missing from %s", docsPath)
}
