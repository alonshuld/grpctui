package keys_test

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/ui/keys"
)

// TestNames pins every action name a config file may use.
//
// The names are derived from KeyMap's field names, which makes them a
// compatibility surface disguised as an implementation detail: renaming a field
// renames a config key and silently breaks somebody's file. This test is the
// thing that turns that into a failure here instead.
func TestNames(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"add",
		"bottom",
		"cancel",
		"capture",
		"collapse",
		"diff",
		"down",
		"end-stream",
		"environments",
		"expand",
		"export",
		"filter",
		"force-quit",
		"help",
		"history-next",
		"history-prev",
		"next-panel",
		"page-down",
		"page-up",
		"prev-panel",
		"profiles",
		"quit",
		"raw-view",
		"remove",
		"requests",
		"retry",
		"save",
		"scroll-left",
		"scroll-right",
		"select",
		"send",
		"themes",
		"toggle",
		"top",
		"traffic",
		"up",
		"variables",
	}, keys.Names())
}

func TestActionName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"Up":          "up",
		"HistoryPrev": "history-prev",
		"RawView":     "raw-view",
		"ForceQuit":   "force-quit",
		"EndStream":   "end-stream",
	}

	for field, want := range tests {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, keys.ActionName(field))
		})
	}
}

func TestKeyMap_Apply(t *testing.T) {
	t.Parallel()

	t.Run("one key", func(t *testing.T) {
		t.Parallel()

		km, err := keys.Default().Apply(map[string]string{"send": "ctrl+g"})
		require.NoError(t, err)

		assert.True(t, key.Matches(press("ctrl+g"), km.Send))
		assert.False(t, key.Matches(press("ctrl+s"), km.Send))

		// The description survives; only the key it documents changes.
		assert.Equal(t, "ctrl+g", km.Send.Help().Key)
		assert.Equal(t, "send", km.Send.Help().Desc)
	})

	t.Run("several keys", func(t *testing.T) {
		t.Parallel()

		km, err := keys.Default().Apply(map[string]string{"quit": "q,ctrl+q, Q"})
		require.NoError(t, err)

		for _, pressed := range []string{"q", "ctrl+q", "Q"} {
			assert.True(t, key.Matches(press(pressed), km.Quit), "%q should quit", pressed)
		}
		assert.Equal(t, "q/ctrl+q/Q", km.Quit.Help().Key)
	})

	t.Run("unbinding", func(t *testing.T) {
		t.Parallel()

		// An empty value is the only way to get a key back that grpctui has
		// claimed and the user wants for something else.
		km, err := keys.Default().Apply(map[string]string{"traffic": ""})
		require.NoError(t, err)

		assert.False(t, key.Matches(press("t"), km.Traffic))
		assert.False(t, km.Traffic.Enabled())
	})

	t.Run("nothing to apply", func(t *testing.T) {
		t.Parallel()

		km, err := keys.Default().Apply(nil)
		require.NoError(t, err)
		assert.Equal(t, keys.Default().Send.Keys(), km.Send.Keys())
	})

	// Everything else keeps its built-in keys: a file that remaps one action is
	// not a file that has redefined the keyboard.
	t.Run("the rest are untouched", func(t *testing.T) {
		t.Parallel()

		km, err := keys.Default().Apply(map[string]string{"send": "ctrl+g"})
		require.NoError(t, err)

		assert.Equal(t, keys.Default().Quit.Keys(), km.Quit.Keys())
		assert.Equal(t, keys.Default().Up.Keys(), km.Up.Keys())
	})
}

func TestKeyMap_Apply_Errors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		overrides map[string]string
		want      []string
	}{
		"an action that is not one": {
			overrides: map[string]string{"snd": "ctrl+g"},
			want:      []string{"snd", "send"},
		},
		// Every mistake is reported at once, so a file with two says so once
		// rather than over two runs.
		"two unknown actions": {
			overrides: map[string]string{"snd": "ctrl+g", "quti": "x"},
			want:      []string{"snd", "quti"},
		},
		"a conflict the user created": {
			overrides: map[string]string{"send": "q"},
			want:      []string{`"q"`, "quit", "send"},
		},
		"a conflict between two remapped actions": {
			overrides: map[string]string{"send": "ctrl+g", "export": "ctrl+g"},
			want:      []string{"ctrl+g", "export", "send"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			km, err := keys.Default().Apply(tc.overrides)
			require.Error(t, err)
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want)
			}

			// Nothing is applied unless everything can be: a half-remapped
			// keyboard is worse than an unremapped one, because the half that
			// worked hides the half that did not.
			assert.Equal(t, keys.Default().Send.Keys(), km.Send.Keys())
		})
	}
}

// TestKeyMap_Apply_DefaultOverlapsAreAllowed pins the "at least one was
// remapped" rule. grpctui's own defaults share keys between actions that can
// never both be live — a letter means one thing in a modal and another in a
// panel — and refusing those would be refusing the built-in keymap.
func TestKeyMap_Apply_DefaultOverlapsAreAllowed(t *testing.T) {
	t.Parallel()

	// Remapping something entirely unrelated must not surface a clash that was
	// already there.
	_, err := keys.Default().Apply(map[string]string{"send": "ctrl+g"})
	require.NoError(t, err)
}

// TestKeyMap_Apply_RemappedIntoAnExistingBinding pins that the conflict check
// looks at the *result*, not only at the pair of overrides.
func TestKeyMap_Apply_RemappedIntoAnExistingBinding(t *testing.T) {
	t.Parallel()

	_, err := keys.Default().Apply(map[string]string{"export": "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traffic")
}

// TestKeyMap_Apply_HelpStaysTruthful is the reason the help key is rewritten
// along with the keys: the `?` bar has to document what the user presses.
func TestKeyMap_Apply_HelpStaysTruthful(t *testing.T) {
	t.Parallel()

	km, err := keys.Default().Apply(map[string]string{
		"send":  "ctrl+g",
		"quit":  "ctrl+q",
		"diff":  "",
		"up":    "ctrl+k,up",
		"down":  "ctrl+j,down",
		"retry": "R",
	})
	require.NoError(t, err)

	for _, binding := range km.ShortHelp() {
		if !binding.Enabled() {
			continue
		}
		help := binding.Help()
		require.NotEmpty(t, help.Desc, "a binding in the help bar with no description")
		require.NotEmpty(t, binding.Keys(), "%q is in the help bar but bound to nothing", help.Desc)
		assert.Contains(t, binding.Keys(), firstOf(help.Key),
			"the help bar says %q for %q", help.Key, help.Desc)
	}
}

// firstOf takes the first key out of a "a/b/c" help string.
func firstOf(help string) string {
	for i := range len(help) {
		if help[i] == '/' {
			return help[:i]
		}
	}
	return help
}

func press(k string) tea.KeyMsg {
	if len(k) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	return tea.KeyMsg{Type: keyType(k)}
}

// keyType maps the few named keys these tests press.
func keyType(k string) tea.KeyType {
	switch k {
	case "ctrl+g":
		return tea.KeyCtrlG
	case "ctrl+q":
		return tea.KeyCtrlQ
	case "ctrl+s":
		return tea.KeyCtrlS
	case "esc":
		return tea.KeyEsc
	case "enter":
		return tea.KeyEnter
	default:
		return tea.KeyNull
	}
}
