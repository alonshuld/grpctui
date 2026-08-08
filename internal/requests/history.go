package requests

import (
	"maps"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/alonshuld/grpctui/internal/format"
)

// DefaultLimit is how many sent requests history keeps. It is a working set,
// not an audit log: past a couple of hundred entries nobody scrolls, and the
// file is rewritten on every send.
const DefaultLimit = 200

// History is the requests already sent, newest first.
//
// It is a value, copied freely — the UI's root model is a bubbletea value model
// and holds one directly. Every mutation allocates a new slice rather than
// appending in place, so a copy taken before a send never sees the send.
type History struct {
	// Path is the file to persist to. An empty path makes the history a
	// session-only one: it still works, it just is not written down, which is
	// what a user who has turned the file off with --history-file= asked for.
	Path string

	// Limit caps the number of entries. Zero means [DefaultLimit].
	Limit int

	entries []Request
}

// historyFile is the on-disk shape. It is a struct rather than a bare list so
// that the file can grow a field later without every existing history becoming
// unparseable — and since v1.0 it carries the format version that says which
// fields to expect.
type historyFile struct {
	Version  format.Version `yaml:"version"`
	Requests []Request      `yaml:"requests"`
}

// DefaultHistoryFile reports the default history path,
// $XDG_STATE_HOME/grpctui/history.yaml, falling back to
// ~/.local/state/grpctui/history.yaml. It returns an empty string when neither
// can be determined, which disables persistence rather than failing startup.
func DefaultHistoryFile() string {
	dir := stateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "history.yaml")
}

// LoadHistory reads the history at path. A missing file yields an empty history
// and no error; a file that exists but cannot be read or parsed is an error,
// because the alternative is silently starting a fresh history over the top of
// one the user still has.
//
// An empty path yields an empty, unpersisted history.
func LoadHistory(path string, limit int) (History, error) {
	h := History{Path: path, Limit: limit}
	if path == "" {
		return h, nil
	}

	var file historyFile
	if err := readYAML(path, &file); err != nil {
		return History{}, err
	}

	// A file hand-trimmed to more than the limit is truncated on load rather
	// than on the next send, so that what is in memory and what is on disk agree
	// from the start.
	h.entries = truncate(file.Requests, h.limit())
	return h, nil
}

// Entries returns the requests, newest first. The slice is the history's own
// and must not be modified; every mutating method replaces it.
func (h History) Entries() []Request { return h.entries }

// Len reports how many requests are recorded.
func (h History) Len() int { return len(h.entries) }

// At returns the nth entry, counting from the newest.
func (h History) At(i int) (Request, bool) {
	if i < 0 || i >= len(h.entries) {
		return Request{}, false
	}
	return h.entries[i], true
}

// Add records a sent request as the newest entry, dropping the oldest once the
// limit is reached.
//
// Consecutive identical sends collapse into one: pressing ctrl+s twice to
// retry a call that timed out should not push the request before it out of
// reach. Only the immediately preceding entry is compared — the same call made
// again after three others is a genuinely separate event.
func (h *History) Add(r Request) {
	if len(h.entries) > 0 && sameRequest(h.entries[0], r) {
		entries := make([]Request, len(h.entries))
		copy(entries, h.entries)
		entries[0] = r
		h.entries = entries
		return
	}

	limit := h.limit()
	entries := make([]Request, 0, min(len(h.entries)+1, limit))
	entries = append(entries, r)
	for _, e := range h.entries {
		if len(entries) >= limit {
			break
		}
		entries = append(entries, e)
	}
	h.entries = entries
}

// Save writes the history out. A history with no path is a no-op, not an error.
func (h History) Save() error {
	if h.Path == "" {
		return nil
	}
	return writeYAML(h.Path, historyFile{Version: format.Current, Requests: h.entries})
}

func (h History) limit() int {
	if h.Limit > 0 {
		return h.Limit
	}
	return DefaultLimit
}

// truncate keeps at most limit entries.
func truncate(entries []Request, limit int) []Request {
	if len(entries) <= limit {
		return entries
	}
	return entries[:limit]
}

// sameRequest reports whether two records describe the same call. The timestamp
// is deliberately not part of it: two sends of one request differ only in when,
// and that is exactly the case being collapsed.
//
// The bodies are compared with reflect.DeepEqual rather than by hand. A body is
// a tree of maps, slices and scalars that a hand-edited collection can put
// anything into — including a mapping with non-string keys, which YAML allows
// and which == would panic on — and this runs once per send, where reflection
// costs nothing worth avoiding.
func sameRequest(a, b Request) bool {
	return a.Method == b.Method &&
		a.Target == b.Target &&
		slices.Equal(a.Headers, b.Headers) &&
		maps.Equal(a.Values, b.Values) &&
		reflect.DeepEqual(a.Body, b.Body)
}
