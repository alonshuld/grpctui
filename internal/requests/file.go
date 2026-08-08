package requests

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/alonshuld/grpctui/internal/format"
)

// dirPerm and filePerm are what grpctui creates its own files with. A history
// file records which services someone has been calling, and a collection can be
// the shape of an internal API, so neither is world-readable.
const (
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// The two kinds of file this package reads, as they are named in an error.
//
// A path on its own does not say which: the history and a collection are both
// YAML in a directory the user rarely looks at, and "this file is written in
// format version 2" reads very differently once you know grpctui wrote one of
// them itself. internal/config names its file the same way, and docs/formats.md
// quotes the result.
const (
	historyKind    = "history"
	collectionKind = "collection"
)

// readYAML decodes a YAML file into v, naming it kind in any error. A missing
// file leaves v alone and reports no error: an empty history and an absent one
// are the same thing to everyone but the filesystem.
//
// Unknown keys are rejected, as in internal/config and for the same reason —
// a collection is hand-edited, and a silently ignored typo in a field name is
// how a request goes out without the header you thought you had written.
func readYAML(kind, path string, v any) error {
	name := fmt.Sprintf("%s %s", kind, strconv.Quote(path))

	body, err := os.ReadFile(path) // #nosec G304 -- the path is grpctui's own state or a collection the user pointed it at.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open %s: %w", name, err)
	}

	// The format version is read first, on a pass of its own, because rejecting
	// unknown keys is exactly what would break on a file written by a later
	// grpctui — and "field retries not found" is a message about the wrong
	// problem. See internal/format.
	if err := format.Check(body, name); err != nil {
		return err
	}

	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)

	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("parse %s: %w", name, err)
	}
	return nil
}

// writeYAML writes v to path, creating the directory if it is not there yet.
//
// The write goes to a temporary file in the same directory and is renamed over
// the target, so a process killed mid-write leaves the previous file intact
// rather than a truncated one. History is rewritten on every send; a crash
// during the tenth call of the day must not cost the first nine.
func writeYAML(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("create %q: %w", dir, err)
	}

	body, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %q: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".*.tmp")
	if err != nil {
		return fmt.Errorf("create a temporary file in %q: %w", dir, err)
	}
	// Removing the temporary file is a no-op once the rename has succeeded and
	// the cleanup that matters when anything before it has not.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := tmp.Chmod(filePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set permissions on %q: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %q: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %q: %w", tmp.Name(), err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}

// stateDir is $XDG_STATE_HOME/grpctui, falling back to
// ~/.local/state/grpctui. It returns an empty string when neither can be
// determined, which the callers treat as "no history" rather than as a startup
// failure — grpctui's whole pitch is that it needs no configuration.
func stateDir() string {
	return userDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// configDir is $XDG_CONFIG_HOME/grpctui, falling back to ~/.config/grpctui —
// the directory internal/config already reads config.yaml out of.
func configDir() string {
	return userDir("XDG_CONFIG_HOME", ".config")
}

func userDir(env string, fallback ...string) string {
	dir := os.Getenv(env)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(append([]string{home}, fallback...)...)
	}
	return filepath.Join(dir, "grpctui")
}
