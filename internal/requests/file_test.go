package requests_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alonshuld/grpctui/internal/requests"
)

func TestDefaultPaths(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join("/tmp", "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join("/tmp", "config"))

	assert.Equal(t, filepath.Join("/tmp", "state", "grpctui", "history.yaml"),
		requests.DefaultHistoryFile())

	// Collections sit beside config.yaml, not in the state directory: they are
	// written by a person and meant to be kept.
	assert.Equal(t, filepath.Join("/tmp", "config", "grpctui", "collections"),
		requests.DefaultCollectionsDir())
}

func TestDefaultPaths_NoHomeDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	if isWindows() {
		t.Skip("os.UserHomeDir has other sources on Windows")
	}

	// Nowhere to write is not a startup failure: it turns persistence off, the
	// same way internal/logging turns logging off.
	assert.Empty(t, requests.DefaultHistoryFile())
	assert.Empty(t, requests.DefaultCollectionsDir())
}
