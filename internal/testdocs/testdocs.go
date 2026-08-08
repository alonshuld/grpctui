// Package testdocs checks a YAML struct against the page that documents it.
//
// v1.0's compatibility promise is about a *documented* format: a key that
// exists and is written down nowhere is a key nobody outside this repo can rely
// on, and one that is found by a user who cannot make it work rather than by a
// test. internal/config and internal/requests both make that promise, about
// different structs in the same page, and neither may import the other — so the
// walk lives here rather than in a copy each.
//
// Nothing but tests imports it, which is why it holds no assertions of its own:
// what to do about a missing key belongs to the test that asked.
package testdocs

import (
	"reflect"
	"strings"
	"time"
)

// Missing lists the YAML keys of t that docs does not cover, in the order the
// struct declares them.
func Missing(docs string, t reflect.Type) []string {
	var missing []string
	for _, key := range Keys(t) {
		if !documented(docs, key) {
			missing = append(missing, key)
		}
	}
	return missing
}

// Keys lists the dotted YAML keys a struct accepts, descending into nested
// structs and into the element type of a slice of them — `tls.ca_cert`,
// `profiles[].name`. A key tagged "-" is not part of the file.
func Keys(t reflect.Type) []string {
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
			for _, nested := range Keys(ft) {
				keys = append(keys, prefix+nested)
			}
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

// documented reports whether the page covers key, either by naming it or by
// covering everything under one of its prefixes with a `.*`.
//
// The wildcard is the escape hatch for a block that appears twice: a profile
// takes the same `tls` and `auth` keys the top level does, and `profiles[].tls.*`
// beside a documented `tls.ca_cert` says so more usefully than six duplicated
// table rows would — and reads correctly to a person, which a test-only
// convention would not.
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
