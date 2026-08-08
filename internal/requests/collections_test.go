package requests_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/requests"
)

func TestLoadCollections_MissingDir(t *testing.T) {
	c, err := requests.LoadCollections(filepath.Join(t.TempDir(), "nope"))
	require.NoError(t, err, "most users will never make one")
	assert.Equal(t, 0, c.Len())
}

func TestLoadCollections_NoDir(t *testing.T) {
	c, err := requests.LoadCollections("")
	require.NoError(t, err)
	assert.Equal(t, 0, c.Len())

	err = c.Save("team", requests.Request{Name: "one", Method: "a.B"})
	require.ErrorIs(t, err, requests.ErrNoCollectionsDir)
}

func TestLoadCollections(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "team.yaml"), `
requests:
  - name: greet
    method: helloworld.Greeter.SayHello
    body:
      name: alice
`)
	write(t, filepath.Join(dir, "alpha.yml"), "requests:\n  - name: one\n    method: a.B\n")
	write(t, filepath.Join(dir, "notes.txt"), "not a collection")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub.yaml"), 0o750))

	c, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	assert.Equal(t, []string{"alpha", "team"}, c.Names(), "collections are ordered by name")

	all := c.All()
	require.Len(t, all, 2)
	require.Len(t, all[1].Requests, 1)
	assert.Equal(t, "greet", all[1].Requests[0].Name)
	assert.Equal(t, map[string]any{"name": "alice"}, all[1].Requests[0].Body)
}

// A collection is hand-edited. A file that cannot be parsed has to be named,
// because a silently skipped one looks exactly like a request never saved.
func TestLoadCollections_Malformed(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "broken.yaml"), "requests: [oh dear\n")

	_, err := requests.LoadCollections(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken.yaml")
}

func TestCollections_Save(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "collections")

	c, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	require.NoError(t, c.Save("team", requests.Request{
		Name:    "greet",
		Method:  "helloworld.Greeter.SayHello",
		Headers: []string{"x-tenant-id"},
		Body:    map[string]any{"name": "alice"},
	}))

	reloaded, err := requests.LoadCollections(dir)
	require.NoError(t, err)
	require.Equal(t, 1, reloaded.Len())

	col := reloaded.All()[0]
	assert.Equal(t, "team", col.Name)
	require.Len(t, col.Requests, 1)
	assert.Equal(t, "greet", col.Requests[0].Name)
	assert.Equal(t, map[string]any{"name": "alice"}, col.Requests[0].Body)

	t.Run("the body is written as nested keys, not as escaped JSON", func(t *testing.T) {
		body := read(t, filepath.Join(dir, "team.yaml"))
		assert.Contains(t, body, "name: alice")
		assert.NotContains(t, body, `{"name"`)
	})
}

func TestCollections_SaveReplacesByName(t *testing.T) {
	dir := t.TempDir()

	c, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	require.NoError(t, c.Save("team", requests.Request{Name: "greet", Body: map[string]any{"name": "alice"}}))
	require.NoError(t, c.Save("team", requests.Request{Name: "greet", Body: map[string]any{"name": "bob"}}))
	require.NoError(t, c.Save("team", requests.Request{Name: "greet-bob", Body: map[string]any{"name": "bob"}}))

	reloaded, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	col := reloaded.All()[0]
	require.Len(t, col.Requests, 2, "saving over a name replaces it; a new name duplicates")
	assert.Equal(t, map[string]any{"name": "bob"}, col.Requests[0].Body)
	assert.Equal(t, "greet-bob", col.Requests[1].Name)
}

// A directory full of a team's collections must not be rewritten because
// somebody saved one request.
func TestCollections_SaveTouchesOneFile(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "other.yaml")
	write(t, other, "# a comment nobody asked grpctui to remove\nrequests:\n  - name: one\n    method: a.B\n")

	c, err := requests.LoadCollections(dir)
	require.NoError(t, err)
	require.NoError(t, c.Save("team", requests.Request{Name: "greet", Method: "a.B"}))

	assert.Contains(t, read(t, other), "# a comment nobody asked grpctui to remove")
}

func TestCollections_SaveInMemory(t *testing.T) {
	dir := t.TempDir()

	c, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	before := c
	require.NoError(t, c.Save("team", requests.Request{Name: "greet", Method: "a.B"}))

	assert.Equal(t, 0, before.Len(), "a copy taken before the save must not see it")
	assert.Equal(t, []string{"team"}, c.Names())
	require.Len(t, c.All()[0].Requests, 1)
}

func TestCollections_SaveRejectsANameThatIsAPath(t *testing.T) {
	dir := t.TempDir()

	c, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	for _, name := range []string{"", "../escape", "a/b", `a\b`, ".", ".."} {
		t.Run("collection "+name, func(t *testing.T) {
			err := c.Save(name, requests.Request{Name: "greet", Method: "a.B"})
			require.Error(t, err)
		})
		t.Run("request "+name, func(t *testing.T) {
			err := c.Save("team", requests.Request{Name: name, Method: "a.B"})
			require.Error(t, err)
		})
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a refused name must not leave a file behind")
}

func TestValidateName(t *testing.T) {
	require.NoError(t, requests.ValidateName("prod-smoke"))
	require.Error(t, requests.ValidateName("two\nlines"))
}

func TestSplitName(t *testing.T) {
	cases := []struct {
		in         string
		collection string
		name       string
	}{
		{"team/greet", "team", "greet"},
		{"greet", requests.DefaultCollection, "greet"},
		{"  team / greet  ", "team", "greet"},
		{"", requests.DefaultCollection, ""},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			collection, name := requests.SplitName(tc.in)
			assert.Equal(t, tc.collection, collection)
			assert.Equal(t, tc.name, name)
		})
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(strings.TrimPrefix(body, "\n")), 0o600))
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path) // #nosec G304 -- a path the test itself built.
	require.NoError(t, err)
	return string(body)
}
