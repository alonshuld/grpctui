package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/ui/keys"
)

// `grpctui keys` is one of the paths that returns before the TUI starts, so it
// is exercised end to end here through run() rather than by calling printKeys
// directly — that is what makes the dispatch part of what is tested.

func TestRun_Keys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	code := runWithin(t, runTimeout, []string{cmdKeys}, &stdout, &stderr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.Empty(t, stderr.String())

	page := stdout.String()
	for _, group := range keys.Default().Reference() {
		assert.Contains(t, page, group.Title)
		for _, action := range group.Actions {
			assert.Contains(t, page, action.Name, "the reference must name every action")
			assert.Contains(t, page, action.Detail)
		}
	}
	assert.Contains(t, page, "keys:", "the page has to say how to remap one")
}

// TestRun_KeysFollowsTheConfigFile is the reason this is a command rather than
// only a page in docs/: a reference that named keys the user has replaced would
// be worse than none at all.
func TestRun_KeysFollowsTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	path := filepath.Join(dir, "grpctui", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("keys:\n  send: ctrl+enter\n  traffic: \"\"\n"), 0o600))

	var stdout, stderr bytes.Buffer
	code := runWithin(t, runTimeout, []string{cmdKeys}, &stdout, &stderr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	page := stdout.String()
	assert.Contains(t, page, "ctrl+enter")
	assert.NotContains(t, page, "ctrl+s", "the key that was replaced must not still be listed")
	assert.Contains(t, page, "unbound", "an action the user took the key away from still gets a line")
}

// TestRun_KeysReportsABadConfigFile pins that the command does not fall back to
// the built-in keymap when the config cannot produce one. A keymap that will not
// load is exactly what somebody runs this to find out about.
func TestRun_KeysReportsABadConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	path := filepath.Join(dir, "grpctui", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("keys:\n  sned: ctrl+enter\n"), 0o600))

	var stdout, stderr bytes.Buffer
	code := runWithin(t, runTimeout, []string{cmdKeys}, &stdout, &stderr)

	assert.Equal(t, exitError, code)
	assert.Contains(t, stderr.String(), "sned")
	assert.Empty(t, stdout.String(), "a half-answer is worse than none")
}

func TestRun_KeysRejectsAnArgument(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	code := runWithin(t, runTimeout, []string{cmdKeys, "send"}, &stdout, &stderr)

	assert.Equal(t, exitUsage, code)
	assert.Contains(t, stderr.String(), "takes no arguments")
}

// TestRun_KeysHonoursConfigFlag pins that -config means here what it means
// everywhere else, which is the whole reason the command shares registerFlags.
func TestRun_KeysHonoursConfigFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	path := filepath.Join(t.TempDir(), "ci.yaml")
	require.NoError(t, os.WriteFile(path, []byte("keys:\n  quit: ctrl+q\n"), 0o600))

	var stdout, stderr bytes.Buffer
	code := runWithin(t, runTimeout, []string{cmdKeys, "-config", path}, &stdout, &stderr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.Contains(t, stdout.String(), "ctrl+q")
}
