package requests_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/requests"
)

func templateRequest() requests.Request {
	return requests.Request{
		Name:   "greet",
		Method: "demo.v1.Greeter.SayHello",
		Body:   map[string]any{"name": "{{who}}", "count": 0},
		Values: map[string]string{"count": "{{n}}"},
	}
}

// A saved request records the reference rather than what it expanded to, so a
// collection stays portable and a captured token never reaches a file.
func TestValuesRoundTripThroughAFile(t *testing.T) {
	dir := t.TempDir()

	collections, err := requests.LoadCollections(dir)
	require.NoError(t, err)
	require.NoError(t, collections.Save("team", templateRequest()))

	raw, err := os.ReadFile(filepath.Join(dir, "team.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "{{n}}")
	assert.Contains(t, string(raw), "{{who}}")

	reloaded, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	require.Equal(t, 1, reloaded.Len())
	require.Len(t, reloaded.All()[0].Requests, 1)
	assert.Equal(t, templateRequest(), reloaded.All()[0].Requests[0])
}

// A request with no references writes no `values` key at all: the feature must
// not put a line into every file of every user who never touched it.
func TestValuesAreOmittedWhenThereAreNone(t *testing.T) {
	dir := t.TempDir()

	collections, err := requests.LoadCollections(dir)
	require.NoError(t, err)
	require.NoError(t, collections.Save("team", requests.Request{
		Name:   "greet",
		Method: "demo.v1.Greeter.SayHello",
		Body:   map[string]any{"name": "alice"},
	}))

	raw, err := os.ReadFile(filepath.Join(dir, "team.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "values")
}

func TestSearchFindsAReference(t *testing.T) {
	haystack := templateRequest().Search()

	assert.True(t, requests.NewMatcher("{{n}}").Match(haystack))
	assert.True(t, requests.NewMatcher("count").Match(haystack),
		"the path a reference sits at should be searchable too")
	assert.False(t, requests.NewMatcher("{{nothing}}").Match(haystack))
}

// Two sends of the same call collapse into one history entry; two calls that
// differ only in which variable a field refers to do not.
func TestHistoryDistinguishesRequestsByTheirReferences(t *testing.T) {
	var h requests.History

	h.Add(templateRequest())
	h.Add(templateRequest())
	require.Equal(t, 1, h.Len())

	other := templateRequest()
	other.Values = map[string]string{"count": "{{other}}"}
	h.Add(other)
	assert.Equal(t, 2, h.Len())
}
