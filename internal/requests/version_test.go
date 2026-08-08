package requests_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/format"
	"github.com/alonshuld/grpctui/internal/requests"
)

// The stored formats' version key. Collections are the files most exposed to
// this: one is committed beside a project, replayed in CI, and read by whatever
// grpctui the next person has installed. docs/formats.md is the promise these
// tests hold to.

func TestCollections_SaveStampsTheFormatVersion(t *testing.T) {
	dir := t.TempDir()
	c, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	require.NoError(t, c.Save("smoke", requests.Request{Name: "login", Method: "auth.Auth.Login"}))

	body, err := os.ReadFile(filepath.Join(dir, "smoke.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(body), "version: 1")
}

func TestHistory_SaveStampsTheFormatVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.yaml")
	h, err := requests.LoadHistory(path, 0)
	require.NoError(t, err)

	h.Add(requests.Request{Method: "helloworld.Greeter.SayHello"})
	require.NoError(t, h.Save())

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), "version: 1")
}

// TestLoadCollections_WithoutAVersion is the backward-compatibility case, and
// the one that matters most: every collection written before v1.0 has no
// version key, and all of them must keep working untouched.
func TestLoadCollections_WithoutAVersion(t *testing.T) {
	dir := t.TempDir()
	writeCollection(t, dir, "old.yaml", "requests:\n  - name: login\n    method: auth.Auth.Login\n")

	c, err := requests.LoadCollections(dir)

	require.NoError(t, err)
	require.Equal(t, 1, c.Len())
	col := c.All()[0]
	assert.True(t, col.Version.Absent())
	assert.Equal(t, format.Current, col.Version.Or())
	require.Len(t, col.Requests, 1)
	assert.Equal(t, "auth.Auth.Login", col.Requests[0].Method)
}

// TestCollections_ResavingStampsAnUnversionedFile pins that reading an old file
// and saving into it writes the current version: the entries written are this
// binary's shape, so the file has to say so.
func TestCollections_ResavingStampsAnUnversionedFile(t *testing.T) {
	dir := t.TempDir()
	writeCollection(t, dir, "old.yaml", "requests:\n  - name: login\n    method: auth.Auth.Login\n")

	c, err := requests.LoadCollections(dir)
	require.NoError(t, err)
	require.NoError(t, c.Save("old", requests.Request{Name: "logout", Method: "auth.Auth.Logout"}))

	body, err := os.ReadFile(filepath.Join(dir, "old.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(body), "version: 1")
	assert.Contains(t, string(body), "auth.Auth.Login", "the entries already there must survive")
	assert.Contains(t, string(body), "auth.Auth.Logout")
}

func TestLoadCollections_LaterVersion(t *testing.T) {
	dir := t.TempDir()
	writeCollection(t, dir, "future.yaml",
		"version: 4\nrequests:\n  - name: login\n    method: auth.Auth.Login\n    retries: 3\n")

	_, err := requests.LoadCollections(dir)

	require.Error(t, err)
	require.ErrorIs(t, err, format.ErrUnsupported)
	require.ErrorContains(t, err, "upgrade grpctui")
	require.ErrorContains(t, err, "future.yaml", "the error has to name which of the files in the directory it is")
	require.ErrorContains(t, err, "collection ",
		"a path alone does not say which kind of file this is, and docs/formats.md quotes the message")
	assert.NotContains(t, err.Error(), "retries",
		"the unknown key is a symptom of the version, and naming it sends the reader after the wrong thing")
}

func TestLoadHistory_LaterVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 7\nrequests: []\n"), 0o600))

	_, err := requests.LoadHistory(path, 0)

	require.Error(t, err)
	require.ErrorIs(t, err, format.ErrUnsupported)
	// Quoted, as every path in an error from this package is — on Windows that
	// is not the same string as the path, since %q doubles the separators.
	require.ErrorContains(t, err, strconv.Quote(path))
	require.ErrorContains(t, err, "history ",
		"grpctui wrote this file itself, and a message that could equally be about a collection hides that")
}

// TestLoadCollections_UnknownKeyWithoutAVersionStillFails pins that the version
// pass has not made a typo in a hand-edited collection acceptable. A silently
// ignored field name is how a request goes out without the header you thought
// you had written.
func TestLoadCollections_UnknownKeyWithoutAVersionStillFails(t *testing.T) {
	dir := t.TempDir()
	writeCollection(t, dir, "typo.yaml", "requests:\n  - name: login\n    methd: auth.Auth.Login\n")

	_, err := requests.LoadCollections(dir)

	require.Error(t, err)
	require.ErrorContains(t, err, "methd")
	assert.NotErrorIs(t, err, format.ErrUnsupported)
}

func writeCollection(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}
