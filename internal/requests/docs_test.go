package requests_test

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/requests"
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
	docs := string(body)

	// Reported together rather than one assertion each: assert.Contains prints
	// the haystack, and the haystack is the whole specification.
	var missing []string
	for _, key := range yamlKeys(reflect.TypeFor[requests.Collection]()) {
		if !strings.Contains(docs, key) {
			missing = append(missing, key)
		}
	}
	assert.Empty(t, missing, "collection keys missing from %s", docsPath)
}

// yamlKeys lists the dotted YAML keys a struct accepts, descending into a
// slice's element type — `requests[].method`. A key tagged "-" is not part of
// the file.
func yamlKeys(t reflect.Type) []string {
	var keys []string

	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}

		ft, prefix := f.Type, name+"."
		if ft.Kind() == reflect.Slice {
			ft, prefix = ft.Elem(), name+"[]."
		}
		if ft.Kind() == reflect.Struct && ft != reflect.TypeFor[time.Time]() {
			for _, nested := range yamlKeys(ft) {
				keys = append(keys, prefix+nested)
			}
			continue
		}
		keys = append(keys, name)
	}
	return keys
}
