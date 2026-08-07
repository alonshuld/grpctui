package panels_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

func newMetadata(t *testing.T) panels.Metadata {
	t.Helper()

	md := panels.NewMetadata(keys.Default(), styles.New())
	md.SetSize(60, 10)
	md.Focus()
	return md
}

func pressMetadata(t *testing.T, md panels.Metadata, keystrokes ...string) (panels.Metadata, tea.Cmd) {
	t.Helper()

	var cmd tea.Cmd
	for _, k := range keystrokes {
		md, cmd = md.Update(keyMsg(k))
	}
	return md, cmd
}

// typeMetadata sends one keystroke per character, which is what a text input
// actually receives.
func typeMetadata(t *testing.T, md panels.Metadata, text string) panels.Metadata {
	t.Helper()

	for _, r := range text {
		md, _ = md.Update(keyMsg(string(r)))
	}
	return md
}

func TestMetadata_EmptyPanelSaysHowToStart(t *testing.T) {
	md := newMetadata(t)

	assert.Contains(t, md.View(), "Press a to add one")
	assert.Empty(t, md.Headers())
	assert.Equal(t, 0, md.Enabled())
}

// Adding a header opens it for typing: needing enter after a would be two
// keystrokes for one intention.
func TestMetadata_AddOpensTheNewHeaderForTyping(t *testing.T) {
	md := newMetadata(t)

	md, _ = pressMetadata(t, md, "a")
	require.True(t, md.Editing())

	md = typeMetadata(t, md, "x-tenant")
	md, _ = pressMetadata(t, md, "enter")

	require.False(t, md.Editing())
	assert.Equal(t, grpcclient.Metadata{{Key: "x-tenant"}}, md.Headers())
}

func TestMetadata_TypesAKeyAndAValue(t *testing.T) {
	md := newMetadata(t)

	md, _ = pressMetadata(t, md, "a")
	md = typeMetadata(t, md, "authorization")
	md, _ = pressMetadata(t, md, "enter", "l", "enter")
	md = typeMetadata(t, md, "Bearer abc")
	md, _ = pressMetadata(t, md, "enter")

	assert.Equal(t, grpcclient.Metadata{{Key: "authorization", Value: "Bearer abc"}}, md.Headers())
	assert.Contains(t, md.View(), "Bearer abc")
}

// h and l move between a row's two cells, which is what those keys mean
// everywhere else in grpctui.
func TestMetadata_ColumnsAreReachableWithHAndL(t *testing.T) {
	md := newMetadata(t)
	md.SetHeaders(grpcclient.Metadata{{Key: "x-tenant", Value: "acme"}})

	// The cursor starts on the key.
	md, _ = pressMetadata(t, md, "enter")
	md = typeMetadata(t, md, "!")
	md, _ = pressMetadata(t, md, "enter")
	require.Equal(t, "x-tenant!", md.Headers()[0].Key)

	md, _ = pressMetadata(t, md, "l", "enter")
	md = typeMetadata(t, md, "!")
	md, _ = pressMetadata(t, md, "enter")
	require.Equal(t, "acme!", md.Headers()[0].Value)

	// And back, so a mistyped key is one h away.
	md, _ = pressMetadata(t, md, "h", "enter", "backspace", "enter")
	assert.Equal(t, "x-tenant", md.Headers()[0].Key)
}

func TestMetadata_RemoveDeletesTheHeaderUnderTheCursor(t *testing.T) {
	md := newMetadata(t)
	md.SetHeaders(grpcclient.Metadata{
		{Key: "a", Value: "1"},
		{Key: "b", Value: "2"},
		{Key: "c", Value: "3"},
	})

	md, _ = pressMetadata(t, md, "j", "d")

	assert.Equal(t, grpcclient.Metadata{{Key: "a", Value: "1"}, {Key: "c", Value: "3"}}, md.Headers())

	// The cursor stays put, so the next one slides under it and clearing
	// several is one keystroke each.
	md, _ = pressMetadata(t, md, "d")
	assert.Equal(t, grpcclient.Metadata{{Key: "a", Value: "1"}}, md.Headers())
}

func TestMetadata_RemoveOnAnEmptyPanelDoesNothing(t *testing.T) {
	md := newMetadata(t)

	md, _ = pressMetadata(t, md, "d", "d")

	assert.Empty(t, md.Headers())
}

// Parking a header beats deleting it: retyping a bearer token to find out
// whether it was the problem is exactly what this tool exists to avoid.
func TestMetadata_ToggleParksAHeaderWithoutDeletingIt(t *testing.T) {
	md := newMetadata(t)
	md.SetHeaders(grpcclient.Metadata{
		{Key: "authorization", Value: "Bearer abc"},
		{Key: "x-tenant", Value: "acme"},
	})

	md, _ = pressMetadata(t, md, " ")

	require.Len(t, md.Headers(), 2)
	assert.True(t, md.Headers()[0].Disabled)
	assert.Equal(t, "Bearer abc", md.Headers()[0].Value, "the value is kept")
	assert.Equal(t, 1, md.Enabled())
	assert.Equal(t, 2, md.Len())

	md, _ = pressMetadata(t, md, " ")
	assert.False(t, md.Headers()[0].Disabled)
	assert.Equal(t, 2, md.Enabled())
}

func TestMetadata_ShowsWhatIsWrongWithAHeader(t *testing.T) {
	md := newMetadata(t)
	md.SetHeaders(grpcclient.Metadata{{Key: "grpc-timeout", Value: "1S"}})

	assert.Contains(t, md.View(), "⚠")
	assert.Contains(t, md.View(), "reserved")
	require.Error(t, md.Validate())
}

// A row being typed into and a row that has only just been added are both
// half-finished rather than wrong.
func TestMetadata_DoesNotComplainPrematurely(t *testing.T) {
	md := newMetadata(t)

	md, _ = pressMetadata(t, md, "a")
	assert.NotContains(t, md.View(), "⚠", "a header being typed is not yet wrong")

	md, _ = pressMetadata(t, md, "enter")
	assert.NotContains(t, md.View(), "⚠", "an untouched new row is not yet wrong")
}

func TestMetadata_DisabledHeadersAreNotValidated(t *testing.T) {
	md := newMetadata(t)
	md.SetHeaders(grpcclient.Metadata{{Key: "grpc-timeout", Value: "1S", Disabled: true}})

	assert.NotContains(t, md.View(), "⚠")
	assert.NoError(t, md.Validate())
}

func TestMetadata_FocusFirstInvalid(t *testing.T) {
	md := newMetadata(t)
	md.SetHeaders(grpcclient.Metadata{
		{Key: "x-ok", Value: "1"},
		{Key: "bad key", Value: "2"},
	})

	require.True(t, md.FocusFirstInvalid())

	// The cursor is on the offending row, which is what makes its ⚠ reachable
	// in a panel that scrolls.
	md, _ = pressMetadata(t, md, "d")
	assert.Equal(t, grpcclient.Metadata{{Key: "x-ok", Value: "1"}}, md.Headers())

	assert.False(t, md.FocusFirstInvalid(), "nothing left to complain about")
}

// Headers belong to the connection, so switching profile replaces them rather
// than carrying an `authorization` across to a server that never issued it.
func TestMetadata_SetHeadersReplacesEverything(t *testing.T) {
	md := newMetadata(t)
	md.SetHeaders(grpcclient.Metadata{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}})

	md, _ = pressMetadata(t, md, "j", "enter")
	md.SetHeaders(grpcclient.Metadata{{Key: "c", Value: "3"}})

	assert.Equal(t, grpcclient.Metadata{{Key: "c", Value: "3"}}, md.Headers())
	assert.False(t, md.Editing(), "the edit in progress belonged to the old connection")
}

// A call in flight must not see a header the user edited after sending it.
func TestMetadata_HeadersAreACopy(t *testing.T) {
	md := newMetadata(t)
	md.SetHeaders(grpcclient.Metadata{{Key: "a", Value: "1"}})

	taken := md.Headers()
	md, _ = pressMetadata(t, md, "l", "enter")
	md = typeMetadata(t, md, "2")

	assert.Equal(t, "1", taken[0].Value)
	assert.Equal(t, "12", md.Headers()[0].Value)
}

// While a cell is being typed into, every key is a character — including the
// ones that would otherwise add, remove and toggle rows.
func TestMetadata_EditingSwallowsCommandKeys(t *testing.T) {
	md := newMetadata(t)
	md.SetHeaders(grpcclient.Metadata{{Key: "x", Value: "v"}})

	md, _ = pressMetadata(t, md, "enter")
	md = typeMetadata(t, md, "ad")

	require.Len(t, md.Headers(), 1, "a must not have added a row")
	assert.Equal(t, "xad", md.Headers()[0].Key)
}

func TestMetadata_EscapeEndsTheEdit(t *testing.T) {
	md := newMetadata(t)

	md, _ = pressMetadata(t, md, "a")
	md = typeMetadata(t, md, "x-tenant")
	md, _ = pressMetadata(t, md, "esc")

	assert.False(t, md.Editing())
	assert.Equal(t, "x-tenant", md.Headers()[0].Key, "what was typed is kept")
}

func TestMetadata_BlurEndsTheEdit(t *testing.T) {
	md := newMetadata(t)

	md, _ = pressMetadata(t, md, "a")
	require.True(t, md.Editing())

	md.Blur()

	assert.False(t, md.Editing())
	assert.False(t, md.Focused())
}

func TestMetadata_IgnoresKeysWhenUnfocused(t *testing.T) {
	md := newMetadata(t)
	md.Blur()

	md, _ = pressMetadata(t, md, "a", "a", "a")

	assert.Empty(t, md.Headers())
}

// An empty headers panel is three rows of border saying "nothing here", taken
// from the panel the user is actually reading.
func TestMetadata_VisibleOnlyWhenItHasSomethingToShow(t *testing.T) {
	md := newMetadata(t)
	assert.True(t, md.Visible(), "focused, so it is where tab has just arrived")

	md.Blur()
	assert.False(t, md.Visible())

	md.SetHeaders(grpcclient.Metadata{{Key: "a", Value: "1"}})
	assert.True(t, md.Visible(), "it has a header to show")
}

// The panel is capped, so a long list scrolls rather than swallowing the
// response.
func TestMetadata_ContentHeightIsCapped(t *testing.T) {
	md := newMetadata(t)

	many := make(grpcclient.Metadata, 0, 20)
	for i := range 20 {
		many = append(many, grpcclient.Header{Key: string(rune('a'+i)) + "-key", Value: "v"})
	}
	md.SetHeaders(many)

	assert.LessOrEqual(t, md.ContentHeight(), 8)
}

func TestMetadata_ScrollsToKeepTheCursorVisible(t *testing.T) {
	md := newMetadata(t)
	md.SetSize(60, 3)

	md.SetHeaders(grpcclient.Metadata{
		{Key: "first", Value: "1"},
		{Key: "second", Value: "2"},
		{Key: "third", Value: "3"},
		{Key: "fourth", Value: "4"},
	})

	require.Contains(t, md.View(), "first")

	md, _ = pressMetadata(t, md, "G")

	view := md.View()
	assert.Contains(t, view, "fourth")
	assert.NotContains(t, view, "first")
	assert.LessOrEqual(t, strings.Count(view, "\n")+1, 3)
}
