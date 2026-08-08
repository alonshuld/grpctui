package panels_test

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// panelNow is the clock the browser reads in these tests, so that "5m ago" is
// a function of the entries rather than of when the suite ran.
func panelNow() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }

func testHistory() requests.History {
	var h requests.History
	h.Add(requests.Request{
		Method: "demo.v1.Echo.Echo",
		Body:   map[string]any{"message": "ping"},
		SentAt: panelNow().Add(-3 * time.Hour),
	})
	h.Add(requests.Request{
		Method: "demo.v1.Greeter.SayHello",
		Body:   map[string]any{"name": "alice"},
		SentAt: panelNow().Add(-5 * time.Minute),
	})
	return h
}

func testCollections(t *testing.T) requests.Collections {
	t.Helper()

	c, err := requests.LoadCollections(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, c.Save("team", requests.Request{
		Name:   "greet-bob",
		Method: "demo.v1.Greeter.SayHello",
		Body:   map[string]any{"name": "bob"},
	}))
	return c
}

func newRequests(t *testing.T) panels.Requests {
	t.Helper()

	r := panels.NewRequests(keys.Default(), styles.New())
	r.SetClock(panelNow)
	r.SetHistory(testHistory())
	r.SetCollections(testCollections(t))
	r.SetSize(90, 20)
	r.Open()
	return r
}

func pressKey(t *testing.T, r panels.Requests, keystrokes ...string) (panels.Requests, tea.Cmd) {
	t.Helper()

	var cmd tea.Cmd
	for _, k := range keystrokes {
		r, cmd = r.Update(panelKey(k))
	}
	return r, cmd
}

func panelKey(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+r":
		return tea.KeyMsg{Type: tea.KeyCtrlR}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

func typeInto(t *testing.T, r panels.Requests, text string) panels.Requests {
	t.Helper()

	for _, ch := range text {
		r, _ = pressKey(t, r, string(ch))
	}
	return r
}

func TestRequests_ClosedIgnoresKeys(t *testing.T) {
	r := panels.NewRequests(keys.Default(), styles.New())
	r.SetHistory(testHistory())

	_, cmd := pressKey(t, r, "enter")
	assert.Nil(t, cmd, "a closed browser must not answer a key the panels below own")
}

func TestRequests_ListsHistoryThenCollections(t *testing.T) {
	r := newRequests(t)

	view := r.View()
	assert.Equal(t, 3, r.Len())
	assert.Contains(t, view, "Requests (3)")

	// History first and newest first, then collections.
	order := []string{"SayHello", "Echo", "greet-bob"}
	at := -1
	for _, want := range order {
		i := strings.Index(view, want)
		require.NotEqual(t, -1, i, "%q is not on screen:\n%s", want, view)
		assert.Greater(t, i, at, "%q is out of order", want)
		at = i
	}
}

func TestRequests_RendersRelativeTimes(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:  "just now",
		5 * time.Minute:   "5m ago",
		3 * time.Hour:     "3h ago",
		25 * time.Hour:    "1 day ago",
		72 * time.Hour:    "3 days ago",
		-10 * time.Minute: "just now", // a clock put back, or another machine's
	}

	for age, want := range cases {
		t.Run(want, func(t *testing.T) {
			r := panels.NewRequests(keys.Default(), styles.New())
			r.SetClock(panelNow)

			var h requests.History
			h.Add(requests.Request{Method: "a.B", SentAt: panelNow().Add(-age)})
			r.SetHistory(h)
			r.SetSize(90, 20)
			r.Open()

			assert.Contains(t, r.View(), want)
		})
	}
}

// A collection entry has no timestamp, and inventing one — or rendering the
// zero time as "20263 days ago" — would be worse than saying nothing.
func TestRequests_ASavedRequestHasNoAge(t *testing.T) {
	r := newRequests(t)
	assert.NotContains(t, r.View(), "days ago")
}

func TestRequests_Filter(t *testing.T) {
	r := newRequests(t)

	r, _ = pressKey(t, r, "/")
	r = typeInto(t, r, "bob")

	view := r.View()
	assert.Contains(t, view, "Requests (1 of 3)")
	assert.Contains(t, view, "greet-bob")
	assert.NotContains(t, view, "Echo")

	t.Run("matches field content, not only names", func(t *testing.T) {
		r := newRequests(t)
		r, _ = pressKey(t, r, "/")
		r = typeInto(t, r, "ping")

		assert.Contains(t, r.View(), "Echo")
		assert.NotContains(t, r.View(), "greet-bob")
	})

	t.Run("matches the source column", func(t *testing.T) {
		r := newRequests(t)
		r, _ = pressKey(t, r, "/")
		r = typeInto(t, r, "team")

		assert.Contains(t, r.View(), "Requests (1 of 3)")
	})

	t.Run("every term must match", func(t *testing.T) {
		r := newRequests(t)
		r, _ = pressKey(t, r, "/")
		r = typeInto(t, r, "sayhello ping")

		assert.Contains(t, r.View(), "No request matches this filter.")
	})
}

// esc inside the filter abandons the query; esc in the list clears an applied
// one before it closes the browser.
func TestRequests_EscapeUnwindsOneStepAtATime(t *testing.T) {
	r := newRequests(t)

	r, _ = pressKey(t, r, "/")
	r = typeInto(t, r, "bob")
	r, _ = pressKey(t, r, "esc")

	require.True(t, r.Opened())
	assert.Contains(t, r.View(), "Requests (3)", "esc while typing abandons the query")

	r, _ = pressKey(t, r, "/")
	r = typeInto(t, r, "bob")
	r, _ = pressKey(t, r, "enter")
	require.Contains(t, r.View(), "Requests (1 of 3)", "enter settles on the query")

	r, _ = pressKey(t, r, "esc")
	require.True(t, r.Opened(), "the first esc clears the filter")
	assert.Contains(t, r.View(), "Requests (3)")

	r, _ = pressKey(t, r, "esc")
	assert.False(t, r.Opened(), "the second closes the browser")
}

// A filter narrows the list under the cursor, so the cursor goes back to the
// top: after narrowing, the match you want is far more often the first one.
func TestRequests_FilterResetsTheCursor(t *testing.T) {
	r := newRequests(t)

	r, _ = pressKey(t, r, "j", "j")
	r, _ = pressKey(t, r, "/")
	r = typeInto(t, r, "e")
	r, _ = pressKey(t, r, "enter")

	assert.Equal(t, "demo.v1.Greeter.SayHello", chosen(t, r).Method)
}

func TestRequests_EnterLoadsAndCtrlSSends(t *testing.T) {
	r := newRequests(t)

	after, cmd := pressKey(t, r, "enter")
	require.NotNil(t, cmd)
	loaded, ok := cmd().(panels.LoadRequestMsg)
	require.True(t, ok)
	assert.False(t, loaded.Send, "enter loads a request; it does not fire one")
	assert.False(t, after.Opened(), "choosing closes the browser")

	after, cmd = pressKey(t, r, "ctrl+s")
	require.NotNil(t, cmd)
	loaded, ok = cmd().(panels.LoadRequestMsg)
	require.True(t, ok)
	assert.True(t, loaded.Send)
	assert.False(t, after.Opened())
}

func TestRequests_PagingAndClosing(t *testing.T) {
	r := newRequests(t)

	r, _ = pressKey(t, r, "ctrl+d")
	assert.Equal(t, "greet-bob", chosen(t, r).Name, "a page down past the end lands on the last row")

	r = newRequests(t)
	r, _ = pressKey(t, r, "G", "ctrl+u")
	assert.Equal(t, "demo.v1.Greeter.SayHello", chosen(t, r).Method)

	r = newRequests(t)
	r, _ = pressKey(t, r, "G", "g")
	assert.Equal(t, "demo.v1.Greeter.SayHello", chosen(t, r).Method)

	// ctrl+r closes the browser as well as opening it, the way p does for the
	// connection switcher.
	r = newRequests(t)
	r, cmd := pressKey(t, r, "ctrl+r")
	assert.Nil(t, cmd)
	assert.False(t, r.Opened())
}

func TestRequests_MovementStaysInBounds(t *testing.T) {
	r := newRequests(t)

	r, _ = pressKey(t, r, "k", "k", "k")
	assert.Equal(t, "demo.v1.Greeter.SayHello", chosen(t, r).Method,
		"k off the top stays on the first row")

	r = newRequests(t)
	r, _ = pressKey(t, r, "G")
	assert.Equal(t, "greet-bob", chosen(t, r).Name, "G lands on the last row")
}

func TestRequests_EmptyBrowser(t *testing.T) {
	r := panels.NewRequests(keys.Default(), styles.New())
	r.SetSize(90, 20)
	r.Open()

	assert.Contains(t, r.View(), "Nothing sent or saved yet.")

	_, cmd := pressKey(t, r, "enter")
	assert.Nil(t, cmd, "there is nothing to choose")
}

func TestRequests_SavePrompt(t *testing.T) {
	r := newRequests(t)
	r.OpenSave("team/greet")
	r.SetSize(90, 20)

	view := r.View()
	assert.Contains(t, view, "Save request")
	assert.Contains(t, view, "team/greet")
	assert.Contains(t, view, "collections: team", "the prompt offers what is already there")

	save := saved(t, r)
	assert.Equal(t, "team", save.Collection)
	assert.Equal(t, "greet", save.Name)
}

func TestRequests_SavePromptDefaultsTheCollection(t *testing.T) {
	r := newRequests(t)
	r.OpenSave("")
	r.SetSize(90, 20)

	r = typeInto(t, r, "greet")

	save := saved(t, r)
	assert.Equal(t, requests.DefaultCollection, save.Collection)
	assert.Equal(t, "greet", save.Name)
}

func TestRequests_SavePromptCanBeAbandoned(t *testing.T) {
	r := newRequests(t)
	r.OpenSave("team/greet")

	r, cmd := pressKey(t, r, "esc")
	assert.Nil(t, cmd)
	assert.False(t, r.Opened())
}

// A failed save keeps the prompt up with the reason on it, so the name can be
// corrected rather than retyped from the start.
func TestRequests_SetNotice(t *testing.T) {
	r := newRequests(t)
	r.OpenSave("team/greet")
	r.SetNotice("a name may not contain a path", true)

	require.True(t, r.Opened())
	assert.Contains(t, r.View(), "a name may not contain a path")

	r.SetNotice("", false)
	assert.False(t, r.Opened(), "a save that worked closes the prompt")
}

// The modal must not resize as the list narrows under the cursor, and must
// never draw wider than the screen it was given.
func TestRequests_BoxWidthIsStable(t *testing.T) {
	r := newRequests(t)
	wide := lipgloss.Width(r.View())

	r, _ = pressKey(t, r, "/")
	r = typeInto(t, r, "bob")
	assert.Equal(t, wide, lipgloss.Width(r.View()), "filtering must not move the walls")

	r.OpenSave("team/greet")
	r.SetSize(90, 20)
	assert.Equal(t, wide, lipgloss.Width(r.View()), "nor must switching to the save prompt")

	// Below about 16 cells there is no bordered box to draw and the root model
	// clamps whatever comes out — see TestModel_SurvivesTinyTerminals. Above it,
	// the browser is responsible for its own width, and a save prompt seeded
	// with a long name is what used to run past the edge.
	t.Run("a narrow terminal", func(t *testing.T) {
		for _, width := range []int{16, 30, 60, 90} {
			r := newRequests(t)
			r.SetSize(width, 20)

			assert.LessOrEqual(t, lipgloss.Width(r.View()), width,
				"the browser is wider than a %d-cell terminal", width)

			r.OpenSave("team/a-rather-long-request-name")
			r.SetSize(width, 20)
			assert.LessOrEqual(t, lipgloss.Width(r.View()), width,
				"the save prompt is wider than a %d-cell terminal", width)
		}
	})
}

// chosen presses enter and reads the request the browser asked for.
func chosen(t *testing.T, r panels.Requests) requests.Request {
	t.Helper()

	_, cmd := pressKey(t, r, "enter")
	require.NotNil(t, cmd, "enter must ask for the request under the cursor")

	loaded, ok := cmd().(panels.LoadRequestMsg)
	require.True(t, ok, "want a LoadRequestMsg")
	return loaded.Request
}

// saved presses enter on the save prompt and reads where it asked to go.
func saved(t *testing.T, r panels.Requests) panels.SaveRequestMsg {
	t.Helper()

	_, cmd := pressKey(t, r, "enter")
	require.NotNil(t, cmd, "enter must ask for the request to be saved")

	msg, ok := cmd().(panels.SaveRequestMsg)
	require.True(t, ok, "want a SaveRequestMsg")
	return msg
}
