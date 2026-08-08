package requests_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/requests"
)

func TestLoadHistory_MissingFile(t *testing.T) {
	h, err := requests.LoadHistory(filepath.Join(t.TempDir(), "nope.yaml"), 0)
	require.NoError(t, err, "a history nobody has written yet is not an error")
	assert.Equal(t, 0, h.Len())
}

func TestLoadHistory_NoPath(t *testing.T) {
	h, err := requests.LoadHistory("", 0)
	require.NoError(t, err)
	assert.Equal(t, 0, h.Len())

	h.Add(requests.Request{Method: "svc.Method"})
	assert.Equal(t, 1, h.Len(), "a history with no file still works for the session")
	require.NoError(t, h.Save(), "saving a history with no file is a no-op, not a failure")
}

func TestLoadHistory_Malformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.yaml")
	require.NoError(t, os.WriteFile(path, []byte("requests: [oh dear\n"), 0o600))

	_, err := requests.LoadHistory(path, 0)
	require.Error(t, err, "a history that exists but cannot be read must not be silently replaced")

	// Quoted, not raw: the error renders the path with %q, which doubles the
	// separators of a Windows path. Comparing against the bare string passes on
	// Unix and fails on Windows for a reason that has nothing to do with history.
	assert.Contains(t, err.Error(), strconv.Quote(path), "the error must name the file")
}

func TestLoadHistory_UnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.yaml")
	require.NoError(t, os.WriteFile(path, []byte("requests:\n  - method: a.B\n    secret: shh\n"), 0o600))

	_, err := requests.LoadHistory(path, 0)
	require.Error(t, err)
}

func TestHistory_AddIsNewestFirst(t *testing.T) {
	var h requests.History
	h.Add(requests.Request{Method: "a.B"})
	h.Add(requests.Request{Method: "c.D"})

	entries := h.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, "c.D", entries[0].Method)
	assert.Equal(t, "a.B", entries[1].Method)
}

func TestHistory_AddDropsTheOldest(t *testing.T) {
	h := requests.History{Limit: 2}
	h.Add(requests.Request{Method: "a.B"})
	h.Add(requests.Request{Method: "c.D"})
	h.Add(requests.Request{Method: "e.F"})

	require.Equal(t, 2, h.Len())
	assert.Equal(t, "e.F", at(t, h, 0).Method)
	assert.Equal(t, "c.D", at(t, h, 1).Method)
}

// Pressing ctrl+s twice to retry a call that timed out must not push the
// request before it out of reach.
func TestHistory_AddCollapsesARepeat(t *testing.T) {
	sentAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	var h requests.History
	h.Add(requests.Request{Method: "a.B", Body: map[string]any{"x": 1}, SentAt: sentAt})
	h.Add(requests.Request{Method: "c.D"})
	h.Add(requests.Request{Method: "c.D"})

	require.Equal(t, 2, h.Len())

	t.Run("keeps the newer timestamp", func(t *testing.T) {
		var h requests.History
		h.Add(requests.Request{Method: "a.B", SentAt: sentAt})
		h.Add(requests.Request{Method: "a.B", SentAt: sentAt.Add(time.Minute)})

		require.Equal(t, 1, h.Len())
		assert.Equal(t, sentAt.Add(time.Minute), at(t, h, 0).SentAt)
	})

	t.Run("a different body is a different call", func(t *testing.T) {
		var h requests.History
		h.Add(requests.Request{Method: "a.B", Body: map[string]any{"x": 1}})
		h.Add(requests.Request{Method: "a.B", Body: map[string]any{"x": 2}})
		assert.Equal(t, 2, h.Len())
	})

	t.Run("the same call after another is a separate event", func(t *testing.T) {
		var h requests.History
		h.Add(requests.Request{Method: "a.B"})
		h.Add(requests.Request{Method: "c.D"})
		h.Add(requests.Request{Method: "a.B"})
		assert.Equal(t, 3, h.Len())
	})
}

// The root model is a bubbletea value model: a copy taken before a send is
// still rendered after it, and must not see the send appear in a slice it
// shares with the newer copy.
func TestHistory_AddDoesNotWriteThroughACopy(t *testing.T) {
	h := requests.History{Limit: 8}
	h.Add(requests.Request{Method: "a.B"})

	before := h
	h.Add(requests.Request{Method: "c.D"})

	require.Equal(t, 1, before.Len())
	assert.Equal(t, "a.B", at(t, before, 0).Method)
	assert.Equal(t, 2, h.Len())
}

func TestHistory_SaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "history.yaml")

	h, err := requests.LoadHistory(path, 0)
	require.NoError(t, err)

	h.Add(requests.Request{
		Method:  "helloworld.Greeter.SayHello",
		Kind:    "unary",
		Target:  "localhost:50051",
		Profile: "local",
		Headers: []string{"x-tenant-id"},
		Body:    map[string]any{"name": "alice"},
		SentAt:  time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, h.Save())

	reloaded, err := requests.LoadHistory(path, 0)
	require.NoError(t, err)
	require.Equal(t, 1, reloaded.Len())

	got := at(t, reloaded, 0)
	assert.Equal(t, "helloworld.Greeter.SayHello", got.Method)
	assert.Equal(t, []string{"x-tenant-id"}, got.Headers)
	assert.Equal(t, map[string]any{"name": "alice"}, got.Body)
	assert.Equal(t, time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC), got.SentAt.UTC())
}

// A bearer token is a header value, and a history file sits in the state
// directory for weeks. Only names are ever written.
func TestHistory_SaveWritesNoHeaderValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.yaml")

	h := requests.History{Path: path}
	h.Add(requests.Request{
		Method:  "a.B",
		Headers: []string{"authorization", "x-api-key"},
	})
	require.NoError(t, h.Save())

	body, err := os.ReadFile(path) // #nosec G304 -- a path this test just made.
	require.NoError(t, err)

	assert.Contains(t, string(body), "authorization")
	assert.NotContains(t, string(body), "Bearer")
}

func TestHistory_SaveIsNotWorldReadable(t *testing.T) {
	if isWindows() {
		t.Skip("file modes are not POSIX permissions on Windows")
	}

	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "history.yaml")

	h := requests.History{Path: path}
	h.Add(requests.Request{Method: "a.B"})
	require.NoError(t, h.Save())

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
}

// A history hand-trimmed to more than the limit is cut on load, so that what is
// in memory and what is on disk agree from the start.
func TestLoadHistory_TruncatesToTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.yaml")

	h := requests.History{Path: path, Limit: 10}
	for i := range 10 {
		h.Add(requests.Request{Method: "a.B", Body: map[string]any{"i": i}})
	}
	require.NoError(t, h.Save())

	reloaded, err := requests.LoadHistory(path, 3)
	require.NoError(t, err)
	assert.Equal(t, 3, reloaded.Len())
}

func TestHistory_At(t *testing.T) {
	var h requests.History
	h.Add(requests.Request{Method: "a.B"})

	_, ok := h.At(-1)
	assert.False(t, ok)
	_, ok = h.At(1)
	assert.False(t, ok)
	_, ok = h.At(0)
	assert.True(t, ok)
}

// at reads the nth entry, failing the test when there is none.
func at(t *testing.T, h requests.History, i int) requests.Request {
	t.Helper()

	r, ok := h.At(i)
	require.True(t, ok, "no history entry %d", i)
	return r
}

func isWindows() bool { return os.PathSeparator == '\\' }
