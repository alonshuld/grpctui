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

func testEnvironments() []vars.Environment {
	return []vars.Environment{
		{
			Name:      "dev",
			Target:    "localhost:50051",
			Variables: []vars.Variable{{Name: "user_id", Value: "1"}},
		},
		{
			Name:   "staging",
			Target: "api.staging.example.com:443",
			Variables: []vars.Variable{
				{Name: "tenant", Value: "acme"},
				{Name: "user_id", Value: "42"},
			},
		},
		{Name: "values only", Variables: []vars.Variable{{Name: "user_id", Value: "7"}}},
	}
}

func newEnvironments(t *testing.T) panels.Environments {
	t.Helper()

	e := panels.NewEnvironments(keys.Default(), styles.New())
	e.SetSize(80, 20)
	e.SetEnvironments(testEnvironments(), 0)
	e.Open()
	return e
}

func pressEnvironments(t *testing.T, e panels.Environments, keystrokes ...string) (panels.Environments, tea.Cmd) {
	t.Helper()

	var cmd tea.Cmd
	for _, k := range keystrokes {
		e, cmd = e.Update(keyMsg(k))
	}
	return e, cmd
}

func selectedEnvironment(t *testing.T, cmd tea.Cmd) panels.EnvironmentSelectedMsg {
	t.Helper()

	require.NotNil(t, cmd, "choosing an environment must emit a message")
	msg, ok := cmd().(panels.EnvironmentSelectedMsg)
	require.True(t, ok, "expected an EnvironmentSelectedMsg")
	return msg
}

func TestEnvironments_Choose(t *testing.T) {
	e := newEnvironments(t)

	e, cmd := pressEnvironments(t, e, "j", "enter")
	msg := selectedEnvironment(t, cmd)

	assert.Equal(t, 1, msg.Index)
	assert.Equal(t, "staging", msg.Environment.Name)
	assert.False(t, e.Opened(), "choosing closes the switcher")
}

// Picking the one already active is how a set of variables captured over an
// afternoon is put back to what the config file says.
func TestEnvironments_ChoosingTheActiveOneStillAsks(t *testing.T) {
	_, cmd := pressEnvironments(t, newEnvironments(t), "enter")

	msg := selectedEnvironment(t, cmd)
	assert.Equal(t, 0, msg.Index)
}

func TestEnvironments_Navigation(t *testing.T) {
	e := newEnvironments(t)

	e, _ = pressEnvironments(t, e, "G")
	_, cmd := pressEnvironments(t, e, "enter")
	assert.Equal(t, 2, selectedEnvironment(t, cmd).Index)

	e, _ = pressEnvironments(t, e, "g")
	_, cmd = pressEnvironments(t, e, "enter")
	assert.Equal(t, 0, selectedEnvironment(t, cmd).Index)

	// The cursor stops at the ends rather than wrapping.
	e, _ = pressEnvironments(t, e, "k", "k")
	_, cmd = pressEnvironments(t, e, "enter")
	assert.Equal(t, 0, selectedEnvironment(t, cmd).Index)
}

func TestEnvironments_CloseWithoutChoosing(t *testing.T) {
	for _, k := range []string{"esc", "e"} {
		t.Run(k, func(t *testing.T) {
			e, cmd := pressEnvironments(t, newEnvironments(t), "j", k)
			assert.False(t, e.Opened())
			assert.Nil(t, cmd)
		})
	}
}

func TestEnvironments_Empty(t *testing.T) {
	e := panels.NewEnvironments(keys.Default(), styles.New())
	e.SetSize(80, 20)
	e.Open()

	assert.False(t, e.Opened(), "there is nothing to switch between")
	assert.Equal(t, 0, e.Len())

	_, ok := e.Active()
	assert.False(t, ok)
}

// A negative index is what a config file with no environments yields, and it
// must not be read as "the first one".
func TestEnvironments_NoActiveEnvironment(t *testing.T) {
	e := panels.NewEnvironments(keys.Default(), styles.New())
	e.SetEnvironments(testEnvironments(), -1)

	_, ok := e.Active()
	assert.False(t, ok)
}

// Which environment you are in is worth seeing; what {{token}} expands to is
// not something to put on a screen somebody might be sharing.
func TestEnvironments_ViewShowsCountsAndNotValues(t *testing.T) {
	view := newEnvironments(t).View()

	assert.Contains(t, view, "staging")
	assert.Contains(t, view, "api.staging.example.com:443")
	assert.Contains(t, view, "2 variables")
	assert.Contains(t, view, "1 variable")
	assert.NotContains(t, view, "acme")
	assert.NotContains(t, view, "user_id")
}

func TestEnvironments_ViewMarksTheActiveOne(t *testing.T) {
	e := panels.NewEnvironments(keys.Default(), styles.New())
	e.SetSize(80, 20)
	e.SetEnvironments(testEnvironments(), 1)
	e.Open()

	active, ok := e.Active()
	require.True(t, ok)
	assert.Equal(t, "staging", active.Name)
	assert.Contains(t, e.View(), "Environments")
}
