// Package format is the version stamped on the files grpctui reads and writes.
//
// v1.0 is the release that promises the config file, a collection and the
// history will keep working, so it is the release that has to be able to say
// which shape a file is in. A `version:` key is what says it, and this package
// is the one place that decides what the current one is and what to do with a
// file carrying another.
//
// The rules are short:
//
//   - An absent version means [Current]. Every file written before v1.0 keeps
//     working untouched, and a user who never types the key never has to know
//     it exists — which is most of them, since grpctui writes it for them.
//   - A version this binary knows is read.
//   - A version *newer* than this binary knows is refused, naming both numbers.
//     That error is the entire point of the key: without it, a config file from
//     a later grpctui fails with "field xyz not found", which sends the reader
//     hunting for a typo they did not make instead of upgrading.
//   - A version below the first one, or one that is not a number, is refused as
//     malformed.
//
// # The compatibility guarantee
//
// Within a major format version, a file keeps working. New keys may be added
// and are optional; an existing key does not change meaning, change type, or
// disappear. That is what makes a collection worth committing beside a project
// and worth running in CI, which is the whole reason collections are files
// rather than rows in a database.
//
// Breaking any of that means [Current] goes up, and an older grpctui then says
// so rather than half-reading the file. docs/formats.md is the written version
// of this paragraph, aimed at the user rather than at the reader of this
// package.
package format

import (
	"errors"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Current is the format version this grpctui writes and the highest one it can
// read.
//
// It is deliberately a single number shared by the config file, collections and
// the history rather than one per file. They are versioned together because
// they are released together: three numbers to reason about would be three
// chances to get a compatibility claim wrong, and there is no world in which a
// user has the collection format from one release and the config format from
// another.
const Current Version = 1

// First is the oldest format version this grpctui can read. It is the same as
// [Current] until there is a second one, at which point the difference between
// them is the compatibility window.
const First Version = 1

// Version is the value of a file's `version:` key.
//
// Zero is "absent", not "invalid": YAML leaves a missing key at the zero value,
// and treating that as [Current] is what lets every file written before v1.0 be
// read unchanged.
type Version int

// Absent reports whether the file carried no version at all.
func (v Version) Absent() bool { return v == 0 }

// Or returns v, or [Current] when the file carried no version.
func (v Version) Or() Version {
	if v.Absent() {
		return Current
	}
	return v
}

func (v Version) String() string { return strconv.Itoa(int(v)) }

// UnsupportedError is returned by [Version.Check] for a file this grpctui
// cannot read.
//
// It carries both numbers because the two directions need different advice: a
// file from the future wants a newer grpctui, and a file with a nonsense
// version in it wants an editor.
type UnsupportedError struct {
	// Path is the file, as the caller named it.
	Path string

	// Have is the version the file claims.
	Have Version

	// Known is the newest version this grpctui understands.
	Known Version
}

func (e *UnsupportedError) Error() string {
	if e.Have > e.Known {
		return fmt.Sprintf(
			"%s is written in format version %s and this grpctui reads up to %s: upgrade grpctui",
			e.Path, e.Have, e.Known)
	}
	return fmt.Sprintf(
		"%s claims format version %s, which is not a version: the versions this grpctui reads are %s to %s",
		e.Path, e.Have, First, e.Known)
}

// Is reports UnsupportedError as matching [ErrUnsupported], so a caller that
// only wants to distinguish "wrong format version" from "malformed YAML" can
// say so with errors.Is.
func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }

// ErrUnsupported matches every [UnsupportedError] under errors.Is.
var ErrUnsupported = errors.New("unsupported format version")

// Check reports whether a file carrying version v can be read, naming path in
// the error. An absent version passes: see the package comment.
func (v Version) Check(path string) error {
	if v.Absent() {
		return nil
	}
	if v < First || v > Current {
		return &UnsupportedError{Path: path, Have: v, Known: Current}
	}
	return nil
}

// Check reads the `version:` key out of a YAML document and reports whether
// this grpctui can read a file in that format, naming path in the error.
//
// It is a pass of its own, run before the caller's real decode, because that
// decode rejects unknown keys — which is exactly what a file from a later
// grpctui is made of. Left to the strict pass, a config file written by the
// next release fails with "field renderers not found", and the reader goes
// looking for a typo they did not make.
//
// A document this cannot parse at all yields no error: the caller's own decode
// is about to fail on it with a better message than a version reader could
// write. The one thing this reports is the version key itself.
func Check(body []byte, path string) error {
	version, err := read(body)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return version.Check(path)
}

// read finds the version key in a YAML document, or reports zero when the
// document has no mapping at its root, no such key, or cannot be parsed.
func read(body []byte) (Version, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		// Deliberately swallowed: the caller's own decode is about to fail on
		// this document with a message about the whole file, which is the better
		// one. Reporting it here would report it twice, in the wrong words.
		//nolint:nilerr // see above.
		return 0, nil
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return 0, nil
	}

	// A YAML mapping node's Content alternates key, value, key, value.
	pairs := doc.Content[0].Content
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i].Value != "version" {
			continue
		}
		var v Version
		if err := v.UnmarshalYAML(pairs[i+1]); err != nil {
			return 0, err
		}
		return v, nil
	}
	return 0, nil
}

// UnmarshalYAML decodes the `version:` key.
//
// It is written out rather than left to yaml's own int decoding so that
// `version: one` reports what the key is for. yaml's message for that names a
// Go type, which tells a person editing a config file nothing they can act on.
//
// A written-down zero is refused here rather than passed to [Version.Check],
// which cannot tell it apart from a key that was never there — and those two
// deserve opposite answers.
func (v *Version) UnmarshalYAML(node *yaml.Node) error {
	var n int
	if err := node.Decode(&n); err != nil {
		return fmt.Errorf("version must be a whole number like %s, got %q", Current, node.Value)
	}
	if n == 0 {
		return fmt.Errorf("version must be at least %s, got %d", First, n)
	}
	*v = Version(n)
	return nil
}
