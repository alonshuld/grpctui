package config_test

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/config"
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
	docs := string(body)

	// The missing keys are collected and reported together rather than asserted
	// one at a time: assert.Contains prints the haystack on failure, and the
	// haystack here is the whole specification.
	var missing []string
	for _, key := range yamlKeys(reflect.TypeFor[config.Config]()) {
		if !documented(docs, key) {
			missing = append(missing, key)
		}
	}
	assert.Empty(t, missing, "config keys missing from %s", docsPath)
}

// documented reports whether the specification covers key, either by naming it
// or by covering everything under one of its prefixes with a `.*`.
//
// The wildcard is the escape hatch for a block that appears twice: a profile
// takes the same `tls` and `auth` keys the top level does, and `profiles[].tls.*`
// beside a documented `tls.ca_cert` says so more usefully than six duplicated
// table rows would.
func documented(docs, key string) bool {
	if strings.Contains(docs, key) {
		return true
	}
	for prefix := key; ; {
		i := strings.LastIndex(prefix, ".")
		if i < 0 {
			return false
		}
		prefix = prefix[:i]
		if strings.Contains(docs, prefix+".*") {
			return true
		}
	}
}

// yamlKeys lists the dotted YAML keys a struct accepts, descending into nested
// structs and into the element type of a slice of them — `tls.ca_cert`,
// `profiles[].name`. A key tagged "-" is not part of the file.
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
