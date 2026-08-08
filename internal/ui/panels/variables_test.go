package panels_test

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
	"github.com/alonshuld/grpctui/internal/vars"
)

func testBindings() vars.Set {
	return vars.NewSet([]vars.Variable{
		{Name: "tenant", Value: "acme"},
		{Name: "token", Value: "ey.token", Captured: true},
		{Name: "user_id", Value: "42"},
	})
}

func newVariables(t *testing.T) panels.Variables {
	t.Helper()

	v := panels.NewVariables(keys.Default(), styles.New())
	v.SetSize(80, 24)
	v.SetVariables(testBindings(), "staging")
	v.Open()
	return v
}

func pressVariables(t *testing.T, v panels.Variables, keystrokes ...string) (panels.Variables, tea.Cmd) {
	t.Helper()

	var cmd tea.Cmd
	for _, k := range keystrokes {
		v, cmd = v.Update(keyMsg(k))
	}
	return v, cmd
}

// typeIntoVariables sends a string one rune at a time, the way a user does.
func typeIntoVariables(t *testing.T, v panels.Variables, text string) panels.Variables {
	t.Helper()

	for _, r := range text {
		v, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return v
}

func TestVariables_Bind(t *testing.T) {
	v := newVariables(t)

	v, _ = pressVariables(t, v, "a")
	v = typeIntoVariables(t, v, "trace_id=abc")
	_, cmd := pressVariables(t, v, "enter")

	require.NotNil(t, cmd)
	msg, ok := cmd().(panels.VariableBoundMsg)
	require.True(t, ok)
	assert.Equal(t, "trace_id", msg.Name)
	assert.Equal(t, "abc", msg.Value)
}

// Correcting a value should be an edit, not retyping the name in front of it.
func TestVariables_EditSeedsTheWholeBinding(t *testing.T) {
	v := newVariables(t)

	v, _ = pressVariables(t, v, "enter")
	assert.Contains(t, v.View(), "tenant=acme")

	v = typeIntoVariables(t, v, "!")
	_, cmd := pressVariables(t, v, "enter")

	msg, ok := cmd().(panels.VariableBoundMsg)
	require.True(t, ok)
	assert.Equal(t, "acme!", msg.Value)
}

func TestVariables_Unbind(t *testing.T) {
	_, cmd := pressVariables(t, newVariables(t), "j", "d")

	require.NotNil(t, cmd)
	msg, ok := cmd().(panels.VariableUnboundMsg)
	require.True(t, ok)
	assert.Equal(t, "token", msg.Name)
}

func TestVariables_Capture(t *testing.T) {
	v := newVariables(t)

	v, _ = pressVariables(t, v, "ctrl+p")
	assert.Contains(t, v.View(), "Capture from the response")

	v = typeIntoVariables(t, v, "id=user.id")
	_, cmd := pressVariables(t, v, "enter")

	require.NotNil(t, cmd)
	msg, ok := cmd().(panels.VariableCapturedMsg)
	require.True(t, ok)
	assert.Equal(t, "id", msg.Name)
	assert.Equal(t, "user.id", msg.Path)
}

func TestVariables_RefusesWhatItCanJudge(t *testing.T) {
	tests := map[string]struct {
		open    string
		typed   string
		message string
	}{
		"no equals sign":                  {open: "a", typed: "trace_id", message: "name=value"},
		"a name no reference could spell": {open: "a", typed: "trace-id=abc", message: "trace-id"},
		"no name at all":                  {open: "a", typed: "=abc", message: "needs a name"},
		"a capture with no path":          {open: "ctrl+p", typed: "id=", message: "path"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			v := newVariables(t)
			v, _ = pressVariables(t, v, tt.open)
			v = typeIntoVariables(t, v, tt.typed)

			v, cmd := pressVariables(t, v, "enter")
			assert.Nil(t, cmd, "nothing should reach the root model")
			assert.Contains(t, v.View(), tt.message)
			assert.True(t, v.Opened(), "the prompt stays up so it can be corrected")
		})
	}
}

// Abandoning a half-typed binding should not also throw away the list you
// opened to look at.
func TestVariables_EscapeGoesBackToTheList(t *testing.T) {
	v := newVariables(t)

	v, _ = pressVariables(t, v, "a")
	v = typeIntoVariables(t, v, "half")
	v, _ = pressVariables(t, v, "esc")

	require.True(t, v.Opened())
	assert.Contains(t, v.View(), "Variables · staging (3)")

	v, _ = pressVariables(t, v, "esc")
	assert.False(t, v.Opened())
}

func TestVariables_View(t *testing.T) {
	view := newVariables(t).View()

	assert.Contains(t, view, "Variables · staging (3)")
	assert.Contains(t, view, "tenant")
	assert.Contains(t, view, "acme")
	assert.Contains(t, view, "captured", "a captured value does not survive switching environment")
}

func TestVariables_Empty(t *testing.T) {
	v := panels.NewVariables(keys.Default(), styles.New())
	v.SetSize(80, 24)
	v.Open()

	assert.Contains(t, v.View(), "Nothing bound")

	// Removing from an empty list asks for nothing rather than panicking.
	_, cmd := pressVariables(t, v, "d")
	assert.Nil(t, cmd)
}

func TestVariables_Notice(t *testing.T) {
	v := newVariables(t)
	v.SetNotice("user.email is not in this response", true)

	assert.Contains(t, v.View(), "not in this response")
}
