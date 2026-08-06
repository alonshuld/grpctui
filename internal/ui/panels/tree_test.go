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

func testServices() []grpcclient.Service {
	return []grpcclient.Service{
		{
			Name: "demo.v1.Greeter",
			Methods: []grpcclient.Method{
				{
					Name: "SayHello", FullName: "demo.v1.Greeter.SayHello",
					InputType: "demo.v1.HelloRequest", OutputType: "demo.v1.HelloReply",
				},
				{
					Name: "SayHelloStream", FullName: "demo.v1.Greeter.SayHelloStream",
					InputType: "demo.v1.HelloRequest", OutputType: "demo.v1.HelloReply",
					ServerStreaming: true,
				},
			},
		},
		{
			Name: "demo.v1.Echo",
			Methods: []grpcclient.Method{
				{
					Name: "Echo", FullName: "demo.v1.Echo.Echo",
					InputType: "demo.v1.EchoRequest", OutputType: "demo.v1.EchoReply",
				},
			},
		},
	}
}

func newTree(t *testing.T) panels.Tree {
	t.Helper()
	tree := panels.NewTree(keys.Default(), styles.New())
	tree.SetSize(40, 20)
	tree.Focus()
	tree.SetServices(testServices())
	return tree
}

func press(t *testing.T, tree panels.Tree, keystrokes ...string) (panels.Tree, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keystrokes {
		tree, cmd = tree.Update(keyMsg(k))
	}
	return tree, cmd
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

func TestTree_ExpandsAllServicesOnLoad(t *testing.T) {
	tree := newTree(t)

	// 2 services + 3 methods.
	assert.Equal(t, 5, tree.Len())
	view := tree.View()
	assert.Contains(t, view, "demo.v1.Greeter")
	assert.Contains(t, view, "SayHello")
	assert.Contains(t, view, "Echo")
}

func TestTree_Empty(t *testing.T) {
	tree := panels.NewTree(keys.Default(), styles.New())
	tree.SetSize(40, 20)

	assert.Equal(t, 0, tree.Len())
	assert.Contains(t, tree.View(), "No services discovered")

	_, _, ok := tree.Selection()
	assert.False(t, ok)
}

func TestTree_Navigation(t *testing.T) {
	tests := map[string]struct {
		keystrokes []string
		wantMethod string
		wantOK     bool
	}{
		"cursor starts on a service row": {
			keystrokes: nil,
			wantOK:     false,
		},
		"down reaches the first method": {
			keystrokes: []string{"j"},
			wantMethod: "demo.v1.Greeter.SayHello",
			wantOK:     true,
		},
		"arrow keys work too": {
			keystrokes: []string{"down", "down"},
			wantMethod: "demo.v1.Greeter.SayHelloStream",
			wantOK:     true,
		},
		"up from the top is a no-op": {
			keystrokes: []string{"k", "k", "k"},
			wantOK:     false,
		},
		"down past the end clamps to the last row": {
			keystrokes: []string{"j", "j", "j", "j", "j", "j", "j"},
			wantMethod: "demo.v1.Echo.Echo",
			wantOK:     true,
		},
		"G jumps to the bottom": {
			keystrokes: []string{"G"},
			wantMethod: "demo.v1.Echo.Echo",
			wantOK:     true,
		},
		"g returns to the top": {
			keystrokes: []string{"G", "g"},
			wantOK:     false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tree, _ := press(t, newTree(t), tt.keystrokes...)

			_, method, ok := tree.Selection()
			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantMethod, method.FullName)
			}
		})
	}
}

func TestTree_CollapseAndExpand(t *testing.T) {
	tree := newTree(t)

	tree, _ = press(t, tree, "h")
	assert.Equal(t, 3, tree.Len(), "collapsing Greeter hides its 2 methods")
	assert.NotContains(t, tree.View(), "SayHello")

	tree, _ = press(t, tree, "l")
	assert.Equal(t, 5, tree.Len())
	assert.Contains(t, tree.View(), "SayHello")
}

func TestTree_CollapseFromMethodJumpsToParent(t *testing.T) {
	tree := newTree(t)

	tree, _ = press(t, tree, "j", "j") // second method of Greeter
	tree, _ = press(t, tree, "h")

	// Still fully expanded; the cursor moved to the service row instead.
	assert.Equal(t, 5, tree.Len())
	_, _, ok := tree.Selection()
	assert.False(t, ok, "cursor should be on the service row")

	svc, ok := tree.CursorService()
	require.True(t, ok)
	assert.Equal(t, "demo.v1.Greeter", svc.Name)
}

func TestTree_EnterOnServiceToggles(t *testing.T) {
	tree := newTree(t)

	tree, cmd := press(t, tree, "enter")
	assert.Nil(t, cmd, "toggling a service emits no message")
	assert.Equal(t, 3, tree.Len())

	tree, cmd = press(t, tree, "enter")
	assert.Nil(t, cmd)
	assert.Equal(t, 5, tree.Len())
}

func TestTree_EnterOnMethodEmitsSelection(t *testing.T) {
	tree := newTree(t)

	_, cmd := press(t, tree, "j", "enter")
	require.NotNil(t, cmd)

	msg, ok := cmd().(panels.MethodSelectedMsg)
	require.True(t, ok, "expected a MethodSelectedMsg")
	assert.Equal(t, "demo.v1.Greeter", msg.Service.Name)
	assert.Equal(t, "demo.v1.Greeter.SayHello", msg.Method.FullName)
}

func TestTree_IgnoresKeysWhenBlurred(t *testing.T) {
	tree := newTree(t)
	tree.Blur()

	tree, _ = press(t, tree, "j", "j")

	_, _, ok := tree.Selection()
	assert.False(t, ok, "a blurred panel must not move its cursor")
	assert.False(t, tree.Focused())
}

func TestTree_ScrollsToKeepCursorVisible(t *testing.T) {
	tree := newTree(t)
	tree.SetSize(40, 2) // only two rows fit

	tree, _ = press(t, tree, "G")

	lines := strings.Split(tree.View(), "\n")
	assert.Len(t, lines, 2)
	assert.Contains(t, tree.View(), "Echo")
	assert.NotContains(t, tree.View(), "SayHello")
}

func TestTree_TagsStreamingMethods(t *testing.T) {
	tree := newTree(t)

	view := tree.View()
	assert.Contains(t, view, "SayHelloStream «stream")
	assert.NotContains(t, view, "SayHello «stream\n", "unary methods carry no tag")
}

func TestTree_TruncatesLongRows(t *testing.T) {
	tree := newTree(t)
	tree.SetSize(12, 20)

	for line := range strings.SplitSeq(tree.View(), "\n") {
		assert.LessOrEqual(t, len([]rune(line)), 12, "line overflows the panel: %q", line)
	}
	assert.Contains(t, tree.View(), "…")
}
