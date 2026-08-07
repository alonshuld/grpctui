package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/config"
)

// writeConfig puts body in a temp file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoad(t *testing.T) {
	tests := map[string]struct {
		body       string
		wantTarget string
	}{
		"a target": {
			body:       "target: localhost:50051\n",
			wantTarget: "localhost:50051",
		},
		"comments and blank lines": {
			body:       "# the service I debug most\n\ntarget: example.com:443\n",
			wantTarget: "example.com:443",
		},
		"an empty file": {
			body: "",
		},
		"only comments": {
			body: "# nothing set yet\n",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := config.Load(writeConfig(t, tt.body))

			require.NoError(t, err)
			assert.Equal(t, tt.wantTarget, cfg.Target)
		})
	}
}

// The default path is a suggestion, not a requirement: grpctui's pitch is that
// it needs no configuration at all.
func TestLoad_MissingFileIsNotAnError(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))

	require.NoError(t, err)
	assert.Empty(t, cfg.Target)
}

// Load shrugs at a missing file, and only at a missing file. A path that is
// there but cannot be read is the user having meant something grpctui could not
// do; starting up with no target instead turns that into a mystery.
//
// The unreadable path here is a directory, which exists on every platform
// grpctui builds for and cannot be decoded as YAML on any of them. A path whose
// parent is a regular file would be neater, but Windows reports that as
// fs.ErrNotExist and the test would assert the opposite of what it means.
func TestLoad_UnreadablePathIsAnError(t *testing.T) {
	dir := t.TempDir()

	_, err := config.Load(dir)

	require.Error(t, err, "a config path that exists but cannot be read was ignored")
	assert.Contains(t, err.Error(), dir, "the error must name the path it failed on")
}

func TestLoad_EmptyPath(t *testing.T) {
	cfg, err := config.Load("")

	require.NoError(t, err)
	assert.Empty(t, cfg.Target)
}

func TestLoad_Rejects(t *testing.T) {
	tests := map[string]struct {
		body string
		want string
	}{
		"malformed YAML": {
			body: "target: [unclosed\n",
			want: "parse config",
		},
		// Silently ignoring a typo'd key is the worst thing a config loader can
		// do: the setting looks applied and is not.
		"an unknown key": {
			body: "targett: localhost:50051\n",
			want: "field targett not found",
		},
		"a target of the wrong type": {
			body: "target:\n  host: localhost\n",
			want: "parse config",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tt.body))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// A file the user named by hand has to be there. Load's shrug at a missing
// file is for the default path, which is a suggestion; applying it to an
// explicit --config turns a typo into a silent no-op.
func TestLoadFile_MissingIsAnError(t *testing.T) {
	_, err := config.LoadFile(filepath.Join(t.TempDir(), "absent.yaml"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "open config")
}

func TestLoadFile_ReadsTheSameAsLoad(t *testing.T) {
	path := writeConfig(t, "target: localhost:50051\n")

	cfg, err := config.LoadFile(path)

	require.NoError(t, err)
	assert.Equal(t, "localhost:50051", cfg.Target)
}

func TestLoadFile_EmptyPath(t *testing.T) {
	// An empty path is "no config file was asked for at all", which the flag
	// layer allows with --config="".
	cfg, err := config.LoadFile("")

	require.NoError(t, err)
	assert.Empty(t, cfg.Target)
}

func TestDefaultPath(t *testing.T) {
	t.Run("follows XDG_CONFIG_HOME", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		assert.Equal(t, filepath.Join(dir, "grpctui", "config.yaml"), config.DefaultPath())
	})

	t.Run("falls back to the home directory", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		home, err := os.UserHomeDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".config", "grpctui", "config.yaml"), config.DefaultPath())
	})
}
