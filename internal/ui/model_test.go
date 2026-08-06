package ui_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui"
)

const (
	termWidth  = 100
	termHeight = 24
)

// fakeDiscoverer stands in for the transport layer, so the UI tests need no
// server, no network, and no TTY.
type fakeDiscoverer struct {
	target   string
	services []grpcclient.Service
	err      error
	calls    atomic.Int32
}

func (f *fakeDiscoverer) Target() string { return f.target }

func (f *fakeDiscoverer) ListServices(ctx context.Context) ([]grpcclient.Service, error) {
	f.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.services, nil
}

func testServices() []grpcclient.Service {
	return []grpcclient.Service{
		{
			Name: "demo.v1.Echo",
			Methods: []grpcclient.Method{
				{
					Name: "Echo", FullName: "demo.v1.Echo.Echo",
					InputType: "demo.v1.EchoRequest", OutputType: "demo.v1.EchoReply",
				},
			},
		},
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
	}
}

func newModel(t *testing.T, client ui.Discoverer) ui.Model {
	t.Helper()
	return ui.New(client, ui.WithLogger(zaptest.NewLogger(t)))
}

// asModel narrows the tea.Model that Update returns back to ui.Model.
func asModel(t *testing.T, m tea.Model) ui.Model {
	t.Helper()
	model, ok := m.(ui.Model)
	require.True(t, ok, "expected a ui.Model, got %T", m)
	return model
}

// sized returns a model that has already received its window size.
func sized(t *testing.T, m ui.Model) ui.Model {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: termWidth, Height: termHeight})
	return asModel(t, next)
}

// settled runs Init's discovery command and feeds the resulting message back,
// leaving the model in whatever state discovery produced.
func settled(t *testing.T, m ui.Model) ui.Model {
	t.Helper()

	m = sized(t, m)
	msg := discoveryResult(t, m)
	next, _ := m.Update(msg)
	return asModel(t, next)
}

// discoveryResult drains Init's batch and returns the discovery message,
// skipping the spinner tick.
func discoveryResult(t *testing.T, m ui.Model) tea.Msg {
	t.Helper()

	cmd := m.Init()
	require.NotNil(t, cmd)

	msgs := drain(cmd)
	for _, msg := range msgs {
		if _, isTick := msg.(spinner.TickMsg); !isTick {
			return msg
		}
	}
	t.Fatalf("no discovery message in %v", msgs)
	return nil
}

// drain executes a command, flattening tea.BatchMsg into its results.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, drain(c)...)
		}
		return out
	case nil:
		return nil
	default:
		return []tea.Msg{msg}
	}
}

// mustUpdate applies one message and discards the resulting command.
func mustUpdate(m ui.Model, msg tea.Msg) tea.Model {
	next, _ := m.Update(msg)
	return next
}

func press(t *testing.T, m ui.Model, keystrokes ...string) (ui.Model, tea.Cmd) {
	t.Helper()

	var cmd tea.Cmd
	for _, k := range keystrokes {
		var next tea.Model
		next, cmd = m.Update(keyMsg(k))
		m = asModel(t, next)
	}
	return m, cmd
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

func TestModel_RendersNothingBeforeWindowSize(t *testing.T) {
	m := newModel(t, &fakeDiscoverer{target: "localhost:50051", services: testServices()})
	assert.Empty(t, m.View())
}

func TestModel_ConnectingState(t *testing.T) {
	m := sized(t, newModel(t, &fakeDiscoverer{target: "localhost:50051"}))
	assert.Contains(t, m.View(), "Connecting to localhost:50051")
}

func TestModel_DiscoverySuccess(t *testing.T) {
	m := settled(t, newModel(t, &fakeDiscoverer{
		target:   "localhost:50051",
		services: testServices(),
	}))
	view := m.View()

	assert.Contains(t, view, "demo.v1.Echo")
	assert.Contains(t, view, "SayHello")
	assert.Contains(t, view, "2 services")
	assert.Contains(t, view, "3 methods")
	assert.Contains(t, view, "Select a method to see its signature.")
}

func TestModel_SelectingAMethodFillsTheDetailPanel(t *testing.T) {
	m := settled(t, newModel(t, &fakeDiscoverer{
		target:   "localhost:50051",
		services: testServices(),
	}))

	// Down onto demo.v1.Echo.Echo, then select it. Enter emits the selection
	// as a command, which the root model consumes on the next Update.
	m, cmd := press(t, m, "j", "enter")
	require.NotNil(t, cmd)
	m = asModel(t, mustUpdate(m, cmd()))

	view := m.View()
	assert.Contains(t, view, "demo.v1.EchoRequest")
	assert.Contains(t, view, "demo.v1.EchoReply")
	assert.Contains(t, view, "unary")
}

func TestModel_ReflectionUnavailableGetsItsOwnScreen(t *testing.T) {
	err := fmt.Errorf("list services: %w: %w",
		grpcclient.ErrReflectionUnavailable,
		status.Error(codes.Unimplemented, "unknown service grpc.reflection.v1.ServerReflection"))

	m := settled(t, newModel(t, &fakeDiscoverer{target: "localhost:50051", err: err}))
	view := m.View()

	assert.Contains(t, view, "Server reflection unavailable")
	assert.Contains(t, view, "Enable reflection on the server")
	assert.Contains(t, view, "retry")
	assert.NotContains(t, view, "Connection failed")
}

func TestModel_TransportFailureGetsTheGenericScreen(t *testing.T) {
	err := fmt.Errorf("list services: %w",
		status.Error(codes.Unavailable, "connection refused"))

	m := settled(t, newModel(t, &fakeDiscoverer{target: "localhost:50051", err: err}))
	view := m.View()

	assert.Contains(t, view, "Connection failed")
	assert.Contains(t, view, "connection refused")
	assert.NotContains(t, view, "Server reflection unavailable")
}

func TestModel_RetryRerunsDiscovery(t *testing.T) {
	client := &fakeDiscoverer{
		target: "localhost:50051",
		err:    status.Error(codes.Unavailable, "connection refused"),
	}
	m := settled(t, newModel(t, client))
	require.Equal(t, int32(1), client.calls.Load())

	// The server comes back, and the user hits retry.
	client.err = nil
	client.services = testServices()

	m, cmd := press(t, m, "r")
	require.NotNil(t, cmd)
	for _, msg := range drain(cmd) {
		m = asModel(t, mustUpdate(m, msg))
	}

	assert.Equal(t, int32(2), client.calls.Load())
	assert.Contains(t, m.View(), "2 services")
}

func TestModel_RetryIgnoredWhenNotFailed(t *testing.T) {
	client := &fakeDiscoverer{target: "localhost:50051", services: testServices()}
	m := settled(t, newModel(t, client))

	_, cmd := press(t, m, "r")

	assert.Nil(t, cmd)
	assert.Equal(t, int32(1), client.calls.Load())
}

func TestModel_TabTogglesFocus(t *testing.T) {
	m := settled(t, newModel(t, &fakeDiscoverer{
		target:   "localhost:50051",
		services: testServices(),
	}))

	// The tree owns focus first; j moves its cursor onto the Echo method.
	m, _ = press(t, m, "j")
	require.Contains(t, m.View(), "❯    Echo")

	// With focus on the detail panel, j must no longer move the tree.
	m, _ = press(t, m, "tab", "j", "j")
	assert.Contains(t, m.View(), "❯    Echo",
		"tree cursor moved while the detail panel had focus")

	// Tabbing back restores tree navigation.
	m, _ = press(t, m, "tab", "j")
	assert.Contains(t, m.View(), "❯ ▾ demo.v1.Greeter")
}

func TestModel_HelpToggle(t *testing.T) {
	m := settled(t, newModel(t, &fakeDiscoverer{
		target:   "localhost:50051",
		services: testServices(),
	}))

	assert.NotContains(t, m.View(), "page up", "full help is hidden by default")

	m, _ = press(t, m, "?")
	assert.Contains(t, m.View(), "page up", "? reveals the full help")

	m, _ = press(t, m, "?")
	assert.NotContains(t, m.View(), "page up")
}

func TestModel_QuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		t.Run(k, func(t *testing.T) {
			m := settled(t, newModel(t, &fakeDiscoverer{
				target:   "localhost:50051",
				services: testServices(),
			}))

			_, cmd := press(t, m, k)
			require.NotNil(t, cmd)
			assert.IsType(t, tea.QuitMsg{}, cmd())
		})
	}
}

func TestModel_DiscoveryRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &fakeDiscoverer{target: "localhost:50051", services: testServices()}
	m := sized(t, ui.New(client,
		ui.WithLogger(zaptest.NewLogger(t)),
		ui.WithContext(ctx),
		ui.WithDiscoveryTimeout(time.Second),
	))

	for _, msg := range drain(m.Init()) {
		m = asModel(t, mustUpdate(m, msg))
	}

	assert.Contains(t, m.View(), "context canceled")
}

// --- golden-file tests -----------------------------------------------------

func TestModel_Golden(t *testing.T) {
	healthy := func() ui.Discoverer {
		return &fakeDiscoverer{target: "localhost:50051", services: testServices()}
	}

	tests := map[string]struct {
		client ui.Discoverer
		// until is text that only appears once discovery has landed. Waiting
		// on a positive marker — rather than on the absence of the spinner —
		// is what keeps these runs from racing the first frame.
		until string
		keys  []string
	}{
		"browsing": {
			client: healthy(),
			until:  "2 services",
		},
		"method selected": {
			client: healthy(),
			until:  "2 services",
			keys:   []string{"j", "j", "j", "enter"},
		},
		"full help": {
			client: healthy(),
			until:  "2 services",
			keys:   []string{"?"},
		},
		"reflection unavailable": {
			client: &fakeDiscoverer{
				target: "localhost:50051",
				err: fmt.Errorf("list services: %w: %w",
					grpcclient.ErrReflectionUnavailable,
					status.Error(codes.Unimplemented, "unknown service grpc.reflection.v1.ServerReflection")),
			},
			until: "Server reflection unavailable",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tm := teatest.NewTestModel(t,
				newModel(t, tt.client),
				teatest.WithInitialTermSize(termWidth, termHeight),
			)

			teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
				return strings.Contains(string(b), tt.until)
			}, teatest.WithDuration(3*time.Second))

			for _, k := range tt.keys {
				tm.Send(keyMsg(k))
			}

			tm.Send(keyMsg("q"))
			teatest.RequireEqualOutput(t, finalOutput(t, tm))
		})
	}
}

func finalOutput(t *testing.T, tm *teatest.TestModel) []byte {
	t.Helper()

	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	return []byte(asModel(t, tm.FinalModel(t)).View())
}
