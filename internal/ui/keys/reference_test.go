package keys_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	bkey "github.com/charmbracelet/bubbles/key"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/ui/keys"
)

// TestReferenceCoversEveryBinding is the guarantee v1.0 made: the reference is
// *complete*. A binding added to the keymap and not filed under a heading would
// otherwise be missing from `grpctui keys` and from docs/keybindings.md, and
// nobody would find out until they went looking for it.
func TestReferenceCoversEveryBinding(t *testing.T) {
	t.Parallel()

	var listed []string
	for _, g := range keys.Default().Reference() {
		for _, a := range g.Actions {
			listed = append(listed, a.Name)
		}
	}
	slices.Sort(listed)

	assert.Equal(t, keys.Names(), listed,
		"every keymap action must appear in the reference exactly once")
}

// TestReferenceNamesNoStrangers is the other direction: a heading listing a
// field that no longer exists would silently drop out of the page, which looks
// exactly like a binding that was never filed.
func TestReferenceNamesNoStrangers(t *testing.T) {
	t.Parallel()

	seen := make(map[string]int)
	for _, g := range keys.Default().Reference() {
		for _, a := range g.Actions {
			seen[a.Name]++
		}
	}

	for _, name := range keys.Names() {
		assert.Equal(t, 1, seen[name], "action %q appears %d times in the reference", name, seen[name])
	}
	assert.Len(t, seen, len(keys.Names()))
}

func TestReferenceEveryGroupHasATitleAndActions(t *testing.T) {
	t.Parallel()

	groups := keys.Default().Reference()
	require.NotEmpty(t, groups)

	for _, g := range groups {
		assert.NotEmpty(t, g.Title)
		assert.NotEmpty(t, g.Actions, "group %q has no actions", g.Title)
	}
}

func TestReferenceEveryActionHasKeysAndADescription(t *testing.T) {
	t.Parallel()

	for _, g := range keys.Default().Reference() {
		for _, a := range g.Actions {
			assert.NotEmpty(t, a.Keys, "%s has no default key", a.Name)
			assert.NotEmpty(t, a.Desc, "%s has no description", a.Name)
			assert.True(t, a.Bound())
		}
	}
}

// TestReferenceEveryActionHasADetail is the half of completeness a count cannot
// catch: a binding filed under a heading with no sentence beside it is present
// in the page and useless in it.
func TestReferenceEveryActionHasADetail(t *testing.T) {
	t.Parallel()

	for _, g := range keys.Default().Reference() {
		for _, a := range g.Actions {
			assert.NotEmpty(t, a.Detail, "%s has no detail for the reference page", a.Name)
			assert.NotEqual(t, a.Name, a.Detail,
				"%s's detail repeats its name, which tells a reader nothing", a.Name)
		}
	}
}

// TestReferenceSpellsTheSpaceBar pins the one key bubbletea names in a way a
// printed page cannot show: " " on a line looks like a binding that is missing.
func TestReferenceSpellsTheSpaceBar(t *testing.T) {
	t.Parallel()

	toggle := findAction(t, keys.Default().Reference(), "toggle")
	assert.Equal(t, "space", toggle.String())
	assert.Contains(t, keys.Reference(keys.Default()), "space")
}

// TestReferenceFollowsTheKeymapItIsGiven is why Reference is a method: a user
// who has remapped send needs the reference to name the key they actually
// press, not the one the binary shipped with.
func TestReferenceFollowsTheKeymapItIsGiven(t *testing.T) {
	t.Parallel()

	remapped, err := keys.Default().Apply(map[string]string{"send": "ctrl+enter"})
	require.NoError(t, err)

	send := findAction(t, remapped.Reference(), "send")
	assert.Equal(t, []string{"ctrl+enter"}, send.Keys)
	assert.Equal(t, "ctrl+enter", send.String())
	assert.Equal(t, "send", send.Desc, "remapping a key must not rewrite what it does")
}

// TestReferenceListsAnUnboundAction pins that a binding the user has taken away
// still gets a line. "This does nothing now" is information; a missing row
// looks like a binding that never existed.
func TestReferenceListsAnUnboundAction(t *testing.T) {
	t.Parallel()

	remapped, err := keys.Default().Apply(map[string]string{"traffic": ""})
	require.NoError(t, err)

	traffic := findAction(t, remapped.Reference(), "traffic")
	assert.Empty(t, traffic.Keys)
	assert.False(t, traffic.Bound())
	assert.Equal(t, "unbound", traffic.String())
}

func TestReferenceText(t *testing.T) {
	t.Parallel()

	text := keys.Reference(keys.Default())

	for _, g := range keys.Default().Reference() {
		assert.Contains(t, text, g.Title)
		for _, a := range g.Actions {
			assert.Contains(t, text, a.Name)
			assert.Contains(t, text, a.Detail)
		}
	}
	assert.Contains(t, text, "ctrl+s", "the rendered keys have to be the ones you press")
}

// TestReferenceTextIsAligned pins the two things that make a forty-line table
// readable: the action column starts in the same place throughout rather than
// per group, and no line runs past a terminal's width.
func TestReferenceTextIsAligned(t *testing.T) {
	t.Parallel()

	lines := strings.Split(strings.TrimRight(keys.Reference(keys.Default()), "\n"), "\n")
	require.NotEmpty(t, lines)

	for _, line := range lines {
		assert.LessOrEqual(t, len(line), 100, "the reference must fit a terminal: %q", line)
	}

	// Each detail is a distinct sentence, so finding the line it is on and where
	// on that line it starts says exactly what alignment means here.
	column := -1
	for _, g := range keys.Default().Reference() {
		for _, a := range g.Actions {
			line := lineWith(t, lines, a.Detail)
			at := strings.Index(line, a.Detail)
			if column < 0 {
				column = at
			}
			assert.Equal(t, column, at, "the description column moves on %q", line)
		}
	}
	assert.Positive(t, column)
}

func lineWith(t *testing.T, lines []string, want string) string {
	t.Helper()

	for _, line := range lines {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no line holds %q", want)
	return ""
}

// TestReferenceHasEveryBindingFieldFiled walks the struct itself rather than
// Names(), so a field added with a name Names() somehow misses is still caught.
func TestReferenceHasEveryBindingFieldFiled(t *testing.T) {
	t.Parallel()

	var fields int
	for _, f := range reflect.VisibleFields(reflect.TypeFor[keys.KeyMap]()) {
		if f.Type == reflect.TypeFor[bkey.Binding]() {
			fields++
		}
	}

	var listed int
	for _, g := range keys.Default().Reference() {
		listed += len(g.Actions)
	}
	assert.Equal(t, fields, listed)
}

func findAction(t *testing.T, groups []keys.Group, name string) keys.Action {
	t.Helper()

	for _, g := range groups {
		for _, a := range g.Actions {
			if a.Name == name {
				return a
			}
		}
	}
	t.Fatalf("no action named %q in the reference", name)
	return keys.Action{}
}
