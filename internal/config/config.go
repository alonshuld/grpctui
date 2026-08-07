// Package config loads grpctui's configuration file.
//
// v0.2 holds one setting — the default target — but the file is the seam
// through which connection profiles (v0.4), environments (v0.7) and keybinding
// remapping (v0.9) arrive, so it is a struct with a versioned shape rather than
// a bare string. Unknown keys are rejected: silently ignoring a typo'd setting
// is the worst possible behaviour for a config file.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is grpctui's on-disk configuration.
type Config struct {
	// Target is the address to connect to when none is given on the command
	// line. A target argument always wins over this.
	Target string `yaml:"target"`
}

// DefaultPath reports the default config file path,
// $XDG_CONFIG_HOME/grpctui/config.yaml, falling back to
// ~/.config/grpctui/config.yaml.
//
// It returns an empty string when neither can be determined, which [Load]
// treats as "no config file" rather than as a startup failure — grpctui's whole
// pitch is that it needs no configuration.
func DefaultPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "grpctui", "config.yaml")
}

// Load reads the config file at path, which the caller has not been asked for
// by name — in practice [DefaultPath].
//
// A missing file yields the zero Config and no error: the default path is a
// suggestion, not a requirement. A file that exists but cannot be read or
// parsed is an error, because the user meant something by it.
func Load(path string) (Config, error) {
	return read(path, false)
}

// LoadFile reads a config file the user named on the command line. Every
// failure is an error, a missing file included: they asked for that path by
// name, and quietly starting up without it turns a typo into a mystery.
//
// The distinction is not cosmetic. "Missing is fine" applied to an explicit
// path is also what let `--config` accept a path that could not exist and carry
// on into the TUI — a shrug on one platform and an error on another, depending
// on whether the OS distinguishes ENOTDIR from ENOENT.
func LoadFile(path string) (Config, error) {
	return read(path, true)
}

func read(path string, mustExist bool) (Config, error) {
	if path == "" {
		return Config{}, nil
	}

	f, err := os.Open(path) // #nosec G304 -- the path is the user's own config file, from --config or the XDG default.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !mustExist {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		// An empty file decodes to io.EOF, which means "nothing configured",
		// not "broken".
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	return cfg, nil
}
