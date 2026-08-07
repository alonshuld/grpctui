package ui_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui"
	"github.com/alonshuld/grpctui/internal/ui/panels"
)

const (
	termWidth  = 100
	termHeight = 24
)

// fakeClient stands in for the transport layer, so the UI tests need no server,
// no network, and no TTY.
type fakeClient struct {
	target   string
	services []grpcclient.Service
	err      error
	calls    atomic.Int32

	// invoke decides what a call does. The default answers every call with an
	// empty response message.
	invoke      func(ctx context.Context, method grpcclient.Method, req proto.Message) (*grpcclient.UnaryResponse, error)
	invocations atomic.Int32

	// mu guards the headers each half of the transport layer was handed, which
	// the tea.Cmds behind discovery and invocation write from their own
	// goroutines.
	mu           sync.Mutex
	discoveryMD  grpcclient.Metadata
	invocationMD grpcclient.Metadata

	// closed counts Close calls, so a test can tell whether the model let go of
	// a connection it replaced.
	closed atomic.Int32
}

func (f *fakeClient) Target() string { return f.target }

func (f *fakeClient) ListServices(ctx context.Context, md grpcclient.Metadata) ([]grpcclient.Service, error) {
	f.calls.Add(1)
	f.record(&f.discoveryMD, md)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.services, nil
}

func (f *fakeClient) InvokeUnary(ctx context.Context, method grpcclient.Method, req proto.Message, md grpcclient.Metadata) (*grpcclient.UnaryResponse, error) {
	f.invocations.Add(1)
	f.record(&f.invocationMD, md)

	if f.invoke != nil {
		return f.invoke(ctx, method, req)
	}
	return &grpcclient.UnaryResponse{
		Message:  dynamicpb.NewMessage(method.Descriptor.Output()),
		Duration: 12 * time.Millisecond,
	}, nil
}

// Close makes the fake an io.Closer, which is how the root model releases a
// connection it opened.
func (f *fakeClient) Close() error {
	f.closed.Add(1)
	return nil
}

func (f *fakeClient) record(into *grpcclient.Metadata, md grpcclient.Metadata) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*into = md
}

// headers returns the metadata one half of the transport layer last received.
func (f *fakeClient) headers(from func(*fakeClient) *grpcclient.Metadata) grpcclient.Metadata {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *from(f)
}

func discoveryHeaders(f *fakeClient) *grpcclient.Metadata  { return &f.discoveryMD }
func invocationHeaders(f *fakeClient) *grpcclient.Metadata { return &f.invocationMD }

// healthyClient is a client that discovers the demo services and answers calls.
func healthyClient() *fakeClient {
	return &fakeClient{target: "localhost:50051", services: testServices()}
}

// goldenProfiles are the connections the headers and switcher goldens are shot
// against: one of each kind of protection, and a credential that must never
// appear in the picture.
func goldenProfiles() []grpcclient.Profile {
	return []grpcclient.Profile{
		{
			Name:   "local",
			Target: "localhost:50051",
			Metadata: grpcclient.Metadata{
				{Key: "x-tenant", Value: "acme"},
				{Key: "x-request-id", Value: "6f1c-42"},
			},
		},
		{
			Name:     "staging",
			Target:   "api.staging.example.com:443",
			Security: grpcclient.Security{TLS: true, CACert: "/etc/ssl/staging-ca.pem"},
			Auth:     grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: "s3cr3t"},
		},
		{
			Name:     "prod",
			Target:   "api.example.com:443",
			Security: grpcclient.Security{TLS: true, ClientCert: "client.pem", ClientKey: "client-key.pem"},
			Auth:     grpcclient.Auth{Kind: grpcclient.AuthBasic, Username: "alice", Password: "hunter2"},
		},
	}
}

// reply builds a response message for a method. Like the fixture it draws on,
// it panics on a name that is not in the descriptor: that is a mistake in the
// test rather than a failure of the code under test, and taking a *testing.T
// would drag one into every fake the golden runs install.
func reply(method grpcclient.Method, values map[string]any) proto.Message {
	md := method.Descriptor.Output()
	msg := dynamicpb.NewMessage(md)
	for name, v := range values {
		fd := md.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			panic(fmt.Sprintf("no field %q on %s", name, md.FullName()))
		}
		msg.Set(fd, protoreflect.ValueOf(v))
	}
	return msg
}

// requestValue reads a field off a request the fake client was handed.
func requestValue(t *testing.T, req proto.Message, name string) protoreflect.Value {
	t.Helper()

	m := req.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "no field %q on %s", name, m.Descriptor().FullName())
	return m.Get(fd)
}

func newModel(t *testing.T, client ui.Client, opts ...ui.Option) ui.Model {
	t.Helper()
	return ui.New(client, append([]ui.Option{ui.WithLogger(zaptest.NewLogger(t))}, opts...)...)
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

// apply feeds every message a command produced back into the model, skipping
// spinner ticks — which would otherwise loop forever.
func apply(t *testing.T, m ui.Model, cmd tea.Cmd) ui.Model {
	t.Helper()

	for _, msg := range drain(cmd) {
		if _, isTick := msg.(spinner.TickMsg); isTick {
			continue
		}
		m = asModel(t, mustUpdate(m, msg))
	}
	return m
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

// selectMethod walks the tree to a method and selects it, leaving focus on the
// request form.
func selectMethod(t *testing.T, m ui.Model, steps int) ui.Model {
	t.Helper()

	for range steps {
		m, _ = press(t, m, "j")
	}

	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd, "enter on a method must emit a selection")
	m = asModel(t, mustUpdate(m, cmd()))

	m, _ = press(t, m, "tab")
	return m
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

// typeText sends one key message per rune, as a terminal would.
func typeText(t *testing.T, m ui.Model, text string) ui.Model {
	t.Helper()

	for _, r := range text {
		m, _ = press(t, m, string(r))
	}
	return m
}

func TestModel_RendersNothingBeforeWindowSize(t *testing.T) {
	assert.Empty(t, newModel(t, healthyClient()).View())
}

func TestModel_ConnectingState(t *testing.T) {
	m := sized(t, newModel(t, &fakeClient{target: "localhost:50051"}))
	assert.Contains(t, m.View(), "Connecting to localhost:50051")
}

func TestModel_DiscoverySuccess(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))
	view := m.View()

	assert.Contains(t, view, "demo.v1.Echo")
	assert.Contains(t, view, "SayHello")
	assert.Contains(t, view, "2 services")
	assert.Contains(t, view, "3 methods")
	assert.Contains(t, view, "Select a method to build a request.")
}

func TestModel_SelectingAMethodBuildsTheRequestForm(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))

	// Down onto demo.v1.Echo.Echo, then select it. Enter emits the selection
	// as a command, which the root model consumes on the next Update.
	m, cmd := press(t, m, "j", "enter")
	require.NotNil(t, cmd)
	m = asModel(t, mustUpdate(m, cmd()))

	view := m.View()
	assert.Contains(t, view, "demo.v1.Echo.Echo")
	assert.Contains(t, view, "demo.v1.EchoRequest", "the form names the message it builds")
	assert.Contains(t, view, "demo.v1.EchoReply", "the response panel names what it expects")
	assert.Contains(t, view, "unary")
	assert.Contains(t, view, "message", "the request's fields are on screen")
}

func TestModel_RequestFormRendersEveryFieldKind(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))
	m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello

	view := m.View()
	for _, want := range []string{
		"name", "string",
		"times", "int32",
		"shout", "bool",
		"volume", "demo.v1.Volume",
		"aliases", "repeated string",
		"address", "demo.v1.Address",
		"greeting", "oneof",
	} {
		assert.Contains(t, view, want)
	}

	// The shapes that hold other rows are drawn as rows that open, not as
	// fields to type into.
	for _, want := range []string{"▸ volume", "▸ aliases", "▸ address", "▸ greeting"} {
		assert.Contains(t, view, want)
	}
}

func TestModel_ReflectionUnavailableGetsItsOwnScreen(t *testing.T) {
	err := fmt.Errorf("list services: %w: %w",
		grpcclient.ErrReflectionUnavailable,
		status.Error(codes.Unimplemented, "unknown service grpc.reflection.v1.ServerReflection"))

	m := settled(t, newModel(t, &fakeClient{target: "localhost:50051", err: err}))
	view := m.View()

	assert.Contains(t, view, "Server reflection unavailable")
	assert.Contains(t, view, "Enable reflection on the server")
	assert.Contains(t, view, "retry")
	assert.NotContains(t, view, "Connection failed")
}

func TestModel_TransportFailureGetsTheGenericScreen(t *testing.T) {
	err := fmt.Errorf("list services: %w",
		status.Error(codes.Unavailable, "connection refused"))

	m := settled(t, newModel(t, &fakeClient{target: "localhost:50051", err: err}))
	view := m.View()

	assert.Contains(t, view, "Connection failed")
	assert.Contains(t, view, "connection refused")
	assert.NotContains(t, view, "Server reflection unavailable")
}

func TestModel_RetryRerunsDiscovery(t *testing.T) {
	client := &fakeClient{
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
	m = apply(t, m, cmd)

	assert.Equal(t, int32(2), client.calls.Load())
	assert.Contains(t, m.View(), "2 services")
}

func TestModel_RetryIgnoredWhenNotFailed(t *testing.T) {
	client := healthyClient()
	m := settled(t, newModel(t, client))

	_, cmd := press(t, m, "r")

	assert.Nil(t, cmd)
	assert.Equal(t, int32(1), client.calls.Load())
}

func TestModel_TabCyclesFocusThroughEveryPanel(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))

	// The tree owns focus first; j moves its cursor onto the Echo method.
	m, _ = press(t, m, "j")
	require.Contains(t, m.View(), "❯    Echo")

	// With focus on the request form, j must no longer move the tree.
	m, _ = press(t, m, "tab", "j", "j")
	assert.Contains(t, m.View(), "❯    Echo",
		"tree cursor moved while the request panel had focus")

	// Headers, then response, then back round to the tree.
	m, _ = press(t, m, "tab", "j")
	assert.Contains(t, m.View(), "❯    Echo")

	m, _ = press(t, m, "tab", "j")
	assert.Contains(t, m.View(), "❯    Echo")

	m, _ = press(t, m, "tab", "j")
	assert.Contains(t, m.View(), "❯ ▾ demo.v1.Greeter")
}

func TestModel_ShiftTabCyclesBackwards(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))

	// One step back from the tree lands on the response panel, so the tree
	// keeps its cursor.
	m, _ = press(t, m, "j")
	m, _ = press(t, m, "shift+tab", "j", "j")

	assert.Contains(t, m.View(), "❯    Echo")
}

func TestModel_HelpToggle(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))

	assert.NotContains(t, m.View(), "page up", "full help is hidden by default")

	m, _ = press(t, m, "?")
	assert.Contains(t, m.View(), "page up", "? reveals the full help")

	m, _ = press(t, m, "?")
	assert.NotContains(t, m.View(), "page up")
}

func TestModel_QuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		t.Run(k, func(t *testing.T) {
			m := settled(t, newModel(t, healthyClient()))

			_, cmd := press(t, m, k)
			require.NotNil(t, cmd)
			assert.IsType(t, tea.QuitMsg{}, cmd())
		})
	}
}

func TestModel_DiscoveryRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m := sized(t, ui.New(healthyClient(),
		ui.WithLogger(zaptest.NewLogger(t)),
		ui.WithContext(ctx),
		ui.WithDiscoveryTimeout(time.Second),
	))

	m = apply(t, m, m.Init())

	assert.Contains(t, m.View(), "context canceled")
}

// --- the request/response cycle --------------------------------------------

func TestModel_SendInvokesTheMethodWithTheTypedValues(t *testing.T) {
	client := healthyClient()

	var got proto.Message
	var gotMethod grpcclient.Method
	client.invoke = func(_ context.Context, method grpcclient.Method, req proto.Message) (*grpcclient.UnaryResponse, error) {
		gotMethod, got = method, req
		return &grpcclient.UnaryResponse{
			Message:  reply(method, map[string]any{"greeting": "hello world", "count": int32(2)}),
			Duration: 12 * time.Millisecond,
		}, nil
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello

	// Fill in name, then step down to times and fill that in too.
	m, _ = press(t, m, "enter")
	m = typeText(t, m, "world")
	m, _ = press(t, m, "esc", "j", "enter")
	m = typeText(t, m, "2")
	m, _ = press(t, m, "esc")

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd, "ctrl+s must start a call")
	m = apply(t, m, cmd)

	require.Equal(t, int32(1), client.invocations.Load())
	assert.Equal(t, "demo.v1.Greeter.SayHello", gotMethod.FullName)
	require.NotNil(t, got)
	assert.Equal(t, "world", requestValue(t, got, "name").String())
	assert.EqualValues(t, 2, requestValue(t, got, "times").Int())

	view := m.View()
	assert.Contains(t, view, "OK")
	assert.Contains(t, view, `"greeting": "hello world"`, "the response is shown as JSON")
	assert.Contains(t, view, "12ms")
}

// A non-OK status is an answer, not a crash: it belongs in the response panel
// with its code and the server's own message.
func TestModel_SendShowsAFailedStatus(t *testing.T) {
	client := healthyClient()
	client.invoke = func(_ context.Context, method grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		return nil, fmt.Errorf("invoke %s: %w", method.FullName,
			status.Error(codes.NotFound, "no such greeting"))
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)
	m = apply(t, m, cmd)

	view := m.View()
	assert.Contains(t, view, "NotFound (5)")
	assert.Contains(t, view, "no such greeting")
	assert.NotContains(t, view, "rpc error: code =", "the raw status string is not for humans")
}

// A failure with no gRPC status never reached the wire, and saying "code 2" for
// it would be a lie.
func TestModel_SendShowsAFailureWithoutAStatus(t *testing.T) {
	client := healthyClient()
	client.invoke = func(_ context.Context, _ grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		return nil, errors.New("invoke: the request could not be encoded")
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	m = apply(t, m, cmd)

	view := m.View()
	assert.Contains(t, view, "Call failed")
	assert.Contains(t, view, "could not be encoded")
}

func TestModel_SendRejectsValuesThatDoNotFitTheirField(t *testing.T) {
	client := healthyClient()
	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	// Type letters into times, an int32.
	m, _ = press(t, m, "j", "enter")
	m = typeText(t, m, "lots")
	m, _ = press(t, m, "esc")

	m, cmd := press(t, m, "ctrl+s")

	assert.Nil(t, cmd, "a request that does not build must not be sent")
	assert.Zero(t, client.invocations.Load())
	assert.Contains(t, m.View(), "expected a whole number")
}

func TestModel_SendRefusesStreamingMethods(t *testing.T) {
	client := healthyClient()
	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 4) // demo.v1.Greeter.SayHelloStream

	m, cmd := press(t, m, "ctrl+s")

	assert.Nil(t, cmd)
	assert.Zero(t, client.invocations.Load())
	assert.Contains(t, m.View(), "Streaming methods are callable from v0.5.")
}

func TestModel_SendWithNoMethodSelected(t *testing.T) {
	client := healthyClient()
	m := settled(t, newModel(t, client))

	m, cmd := press(t, m, "ctrl+s")

	assert.Nil(t, cmd)
	assert.Zero(t, client.invocations.Load())
	assert.Contains(t, m.View(), "Select a method first.")
}

func TestModel_BoolFieldsToggleWithSpace(t *testing.T) {
	client := healthyClient()

	var got proto.Message
	client.invoke = func(_ context.Context, method grpcclient.Method, req proto.Message) (*grpcclient.UnaryResponse, error) {
		got = req
		return &grpcclient.UnaryResponse{Message: reply(method, nil)}, nil
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	// name, times, then shout.
	m, _ = press(t, m, "j", "j", " ")
	require.Contains(t, m.View(), "true")

	_, cmd := press(t, m, "ctrl+s")
	apply(t, m, cmd)

	require.NotNil(t, got)
	assert.True(t, requestValue(t, got, "shout").Bool())
}

// While a field is being edited every key is a character. A q that quits the
// program mid-word would make the form unusable.
func TestModel_EditingSwallowsCommandKeys(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "enter")
	m, cmd := press(t, m, "q")

	assert.Nil(t, cmd, "q must not quit while a field is being edited")
	assert.Contains(t, m.View(), "q", "q was typed into the field")

	// Escape ends the edit and hands the keys back.
	m, _ = press(t, m, "esc")
	_, cmd = press(t, m, "q")
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

// ctrl+c is the one key that always works, edit in progress or not.
func TestModel_ForceQuitWorksWhileEditing(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "enter")
	_, cmd := press(t, m, "ctrl+c")

	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestModel_EscapeCancelsAnInFlightCall(t *testing.T) {
	client := healthyClient()

	started := make(chan struct{})
	client.invoke = func(ctx context.Context, _ grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		close(started)
		<-ctx.Done()
		return nil, fmt.Errorf("invoke: %w", status.FromContextError(ctx.Err()).Err())
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)

	// Run the call in the background, as bubbletea would, and give up on it.
	results := make(chan tea.Msg, 4)
	go func() {
		for _, msg := range drain(cmd) {
			results <- msg
		}
		close(results)
	}()

	<-started
	m, _ = press(t, m, "esc")

	deadline := time.After(3 * time.Second)
	for msg := range results {
		select {
		case <-deadline:
			t.Fatal("the cancelled call never came back")
		default:
		}
		if _, isTick := msg.(spinner.TickMsg); isTick {
			continue
		}
		m = asModel(t, mustUpdate(m, msg))
	}

	assert.Contains(t, m.View(), "Canceled")
}

// A result for a call the user has replaced must not overwrite the newer one.
func TestModel_StaleResultsAreDropped(t *testing.T) {
	client := healthyClient()
	client.invoke = func(_ context.Context, method grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		return &grpcclient.UnaryResponse{
			Message:  reply(method, map[string]any{"greeting": "second"}),
			Duration: time.Millisecond,
		}, nil
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	// Start one call and hold on to its result.
	_, firstCmd := press(t, m, "ctrl+s")
	require.NotNil(t, firstCmd)
	firstResult := drain(firstCmd)

	// Start a second, and let it land.
	m, secondCmd := press(t, m, "ctrl+s")
	m = apply(t, m, secondCmd)
	require.Contains(t, m.View(), `"greeting": "second"`)

	// The first call's answer arrives late and must be ignored.
	for _, msg := range firstResult {
		if _, isTick := msg.(spinner.TickMsg); isTick {
			continue
		}
		m = asModel(t, mustUpdate(m, msg))
	}
	assert.Contains(t, m.View(), `"greeting": "second"`)
}

// The root model takes a send as a message as well as a keystroke, which is
// how v0.6 will re-send a request out of history.
func TestModel_SendRequestMsgStartsACall(t *testing.T) {
	client := healthyClient()
	method := testServices()[1].Methods[0] // demo.v1.Greeter.SayHello

	client.invoke = func(_ context.Context, m grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		return &grpcclient.UnaryResponse{
			Message:  reply(m, map[string]any{"greeting": "from a message"}),
			Duration: time.Millisecond,
		}, nil
	}

	m := settled(t, newModel(t, client))
	next, cmd := m.Update(panels.SendRequestMsg{
		Service: testServices()[1],
		Method:  method,
		Request: dynamicpb.NewMessage(method.InputDescriptor()),
	})
	require.NotNil(t, cmd)

	m = apply(t, asModel(t, next), cmd)

	assert.Equal(t, int32(1), client.invocations.Load())
	assert.Contains(t, m.View(), "from a message")
}

// ctrl+s during a call replaces it rather than being swallowed: the sequence
// number already exists so that the abandoned call's answer is dropped, and a
// keystroke that silently does nothing is worse than either alternative.
func TestModel_SendDuringACallReplacesIt(t *testing.T) {
	client := healthyClient()
	client.invoke = func(ctx context.Context, _ grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	m, first := press(t, m, "ctrl+s")
	require.NotNil(t, first)

	_, second := press(t, m, "ctrl+s")

	assert.NotNil(t, second, "the second ctrl+s was swallowed")
}

func TestModel_InFlightShowsALoadingState(t *testing.T) {
	client := healthyClient()
	client.invoke = func(ctx context.Context, _ grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)

	assert.Contains(t, m.View(), "Calling SayHello…")
}

// --- golden-file tests -----------------------------------------------------

// goldenStep is one beat of a golden run: some keys, then the text that says
// they have taken effect. Anything asynchronous — selecting a method, which
// arrives as a command's message, or a call — needs its own step, or the next
// keys race the state they depend on.
type goldenStep struct {
	keys  []string
	until string
}

func TestModel_Golden(t *testing.T) {
	const discovered = "2 services"

	selectSayHello := []goldenStep{
		{until: discovered},
		{keys: []string{"j", "j", "j", "enter"}, until: "demo.v1.HelloRequest"},
	}

	tests := map[string]struct {
		client func() ui.Client
		opts   []ui.Option
		steps  []goldenStep
	}{
		"browsing": {
			client: func() ui.Client { return healthyClient() },
			steps:  []goldenStep{{until: discovered}},
		},
		"method selected": {
			client: func() ui.Client { return healthyClient() },
			steps:  selectSayHello,
		},
		"request form focused": {
			client: func() ui.Client { return healthyClient() },
			steps: append(selectSayHello,
				goldenStep{keys: []string{"tab", "j", "j", "j", "enter"}, until: "○ VOLUME_LOUD"}),
		},
		// The v0.3 shapes on one screen: an item added to a repeated field, a
		// nested message opened and filled in, and a oneof variant picked.
		"nested request form": {
			client: func() ui.Client { return healthyClient() },
			steps: append(selectSayHello,
				goldenStep{keys: []string{"tab", "j", "j", "j", "j", "a"}, until: "[0]"},
				goldenStep{keys: []string{"enter", "a", "d", "a", "esc"}, until: "ada"},
				goldenStep{keys: []string{"j", "enter"}, until: "postcode"},
				goldenStep{keys: []string{"j", "enter", "O", "s", "l", "o", "esc"}, until: "Oslo"},
				goldenStep{keys: []string{"j", "j", "enter"}, until: "○ automatic"},
				goldenStep{keys: []string{"j", " "}, until: "● custom"},
			),
		},
		"editing a field": {
			client: func() ui.Client { return healthyClient() },
			steps: append(selectSayHello,
				goldenStep{keys: []string{"tab", "enter", "w", "o", "r", "l", "d"}, until: "world"}),
		},
		"response": {
			client: func() ui.Client {
				c := healthyClient()
				c.invoke = func(_ context.Context, method grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
					return &grpcclient.UnaryResponse{
						Message:  reply(method, map[string]any{"greeting": "hello, world", "count": int32(3)}),
						Duration: 12 * time.Millisecond,
					}, nil
				}
				return c
			},
			steps: append(selectSayHello, goldenStep{keys: []string{"ctrl+s"}, until: `"greeting"`}),
		},
		"failed call": {
			client: func() ui.Client {
				c := healthyClient()
				c.invoke = func(_ context.Context, method grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
					return nil, fmt.Errorf("invoke %s: %w", method.FullName,
						status.Error(codes.PermissionDenied, "caller is not allowed to greet"))
				}
				return c
			},
			steps: append(selectSayHello, goldenStep{keys: []string{"ctrl+s"}, until: "PermissionDenied"}),
		},
		// The headers that ride along with every call, and the switcher that
		// moves between the connections they belong to.
		"request headers": {
			client: func() ui.Client { return healthyClient() },
			opts:   []ui.Option{ui.WithProfiles(goldenProfiles(), 0)},
			steps: append(selectSayHello,
				goldenStep{keys: []string{"tab", "tab"}, until: "x-tenant"},
				goldenStep{keys: []string{"j", " "}, until: "○"},
				goldenStep{keys: []string{"k"}, until: "❯ ●"},
			),
		},
		"connection switcher": {
			client: func() ui.Client { return healthyClient() },
			opts:   []ui.Option{ui.WithProfiles(goldenProfiles(), 0)},
			steps:  []goldenStep{{until: discovered}, {keys: []string{"p"}, until: "Connections"}},
		},
		"full help": {
			client: func() ui.Client { return healthyClient() },
			steps:  []goldenStep{{until: discovered}, {keys: []string{"?"}, until: "page up"}},
		},
		"reflection unavailable": {
			client: func() ui.Client {
				return &fakeClient{
					target: "localhost:50051",
					err: fmt.Errorf("list services: %w: %w",
						grpcclient.ErrReflectionUnavailable,
						status.Error(codes.Unimplemented, "unknown service grpc.reflection.v1.ServerReflection")),
				}
			},
			steps: []goldenStep{{until: "Server reflection unavailable"}},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tm := teatest.NewTestModel(t,
				newModel(t, tt.client(), tt.opts...),
				teatest.WithInitialTermSize(termWidth, termHeight),
			)

			for _, step := range tt.steps {
				for _, k := range step.keys {
					tm.Send(keyMsg(k))
				}
				teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
					return strings.Contains(string(b), step.until)
				}, teatest.WithDuration(3*time.Second))
			}

			// ctrl+c, not q: a run that ends mid-edit would type the q into a
			// field and never quit.
			tm.Send(keyMsg("ctrl+c"))
			teatest.RequireEqualOutput(t, finalOutput(t, tm))
		})
	}
}

func finalOutput(t *testing.T, tm *teatest.TestModel) []byte {
	t.Helper()

	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	return []byte(asModel(t, tm.FinalModel(t)).View())
}

// A resize down to a sliver is a normal thing for a user to do, and a panic
// there takes the terminal with it. Every screen must survive degenerate
// dimensions.
func TestModel_SurvivesTinyTerminals(t *testing.T) {
	sizes := []struct{ w, h int }{
		{1, 1}, {2, 3}, {5, 2}, {10, 5}, {20, 4}, {0, 10}, {80, 1},
	}

	clients := map[string]func() ui.Client{
		"discovered": func() ui.Client { return healthyClient() },
		"failed": func() ui.Client {
			return &fakeClient{
				target: "localhost:50051",
				err: fmt.Errorf("list services: %w",
					status.Error(codes.Unavailable, "connection refused")),
			}
		},
	}

	for state, newClient := range clients {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s %dx%d", state, size.w, size.h), func(t *testing.T) {
				m := newModel(t, newClient(), ui.WithProfiles(goldenProfiles(), 0))

				next, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
				m = asModel(t, next)
				m = asModel(t, mustUpdate(m, discoveryResult(t, m)))

				assert.NotPanics(t, func() { _ = m.View() })
				assertFitsTerminal(t, m.View(), size.w, size.h)

				// And with a method selected, which is what fills the panels.
				m, cmd := press(t, m, "j", "enter")
				if cmd != nil {
					m = asModel(t, mustUpdate(m, cmd()))
				}
				assert.NotPanics(t, func() { _ = m.View() })
				assertFitsTerminal(t, m.View(), size.w, size.h)

				// The headers panel, which turns the right-hand column into
				// three boxes where there was barely room for two.
				m, _ = press(t, m, "tab", "tab")
				assert.NotPanics(t, func() { _ = m.View() })
				assertFitsTerminal(t, m.View(), size.w, size.h)

				// And the connection switcher, which is a box of its own.
				m, _ = press(t, m, "shift+tab", "shift+tab", "p")
				assert.NotPanics(t, func() { _ = m.View() })
				assertFitsTerminal(t, m.View(), size.w, size.h)
			})
		}
	}
}

// assertFitsTerminal pins the invariant that surviving a tiny terminal is not
// the same as fitting in one: a frame with more rows than the window scrolls
// the screen and strands the previous frame above it, and one with wider rows
// wraps them, which desynchronises every row below.
//
// The panel minimums cannot all be met below about 30x12, so on the sizes this
// runs at the layout necessarily asks for more room than exists. What must hold
// is that nothing it asks for reaches the terminal.
func assertFitsTerminal(t *testing.T, view string, width, height int) {
	t.Helper()

	if view == "" {
		return
	}

	lines := strings.Split(view, "\n")
	assert.LessOrEqual(t, len(lines), height, "the frame is taller than the terminal")
	for i, line := range lines {
		assert.LessOrEqual(t, lipgloss.Width(line), width,
			"line %d runs past the right edge: %q", i, line)
	}
}

// Selecting a method fills the request form but must not move the user into it:
// browsing means walking the list with j/k, and having focus jump on every
// enter would break that after the first press.
func TestModel_SelectingAMethodKeepsFocusOnTheTree(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))

	m, cmd := press(t, m, "j", "enter")
	require.NotNil(t, cmd)
	m = asModel(t, mustUpdate(m, cmd()))
	require.Contains(t, m.View(), "demo.v1.EchoRequest", "the request form did not fill")

	// j must still walk the tree.
	m, _ = press(t, m, "j")
	assert.Contains(t, m.View(), "❯ ▾ demo.v1.Greeter")
}

// Selecting another method while a call is out is the user moving on. The call
// belongs to the method they have left, so it is cancelled and its answer
// dropped — otherwise it lands in the panel under the new method's name, as if
// it were an answer to that, and esc has stopped being able to cancel it
// because the panel is no longer in its in-flight state.
func TestModel_SelectingAnotherMethodAbandonsTheCallInFlight(t *testing.T) {
	client := healthyClient()

	callCtx := make(chan context.Context, 1)
	release := make(chan struct{})
	client.invoke = func(ctx context.Context, method grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		callCtx <- ctx
		<-release
		return &grpcclient.UnaryResponse{
			Message:  reply(method, map[string]any{"greeting": "ANSWER-FOR-SAYHELLO"}),
			Duration: time.Millisecond,
		}, nil
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)

	results := make(chan tea.Msg, 4)
	go func() {
		defer close(results)
		for _, msg := range drain(cmd) {
			results <- msg
		}
	}()

	started := <-callCtx
	require.Contains(t, m.View(), "Calling SayHello…")

	// The user picks a different method. demo.v1.Echo is the first service.
	echo := testServices()[0]
	m = asModel(t, mustUpdate(m, panels.MethodSelectedMsg{Service: echo, Method: echo.Methods[0]}))

	require.Error(t, started.Err(), "the abandoned call was left running")

	// Its answer arrives anyway, as it would from a server that had already
	// begun replying.
	close(release)
	for msg := range results {
		if _, isTick := msg.(spinner.TickMsg); isTick {
			continue
		}
		m = asModel(t, mustUpdate(m, msg))
	}

	view := m.View()
	assert.Contains(t, view, "demo.v1.EchoRequest", "the form should have moved to the new method")
	assert.NotContains(t, view, "ANSWER-FOR-SAYHELLO",
		"the abandoned call's answer was drawn under the newly selected method")
	assert.Contains(t, view, "Press ctrl+s to send the request.",
		"the response panel should be waiting for a call to the new method")
}

// A response that JSON cannot represent is still a response. Reporting it as a
// failed call would be a lie about what the server did.
func TestModel_AResponseThatIsNotJSONIsStillASuccess(t *testing.T) {
	client := healthyClient()
	client.invoke = func(_ context.Context, _ grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		return &grpcclient.UnaryResponse{
			// An Any whose payload type this client has never seen, which is
			// what protojson refuses outright.
			Message: &anypb.Any{
				TypeUrl: "type.googleapis.com/some.server.only.Detail",
				Value:   []byte{0x0a, 0x03, 'a', 'b', 'c'},
			},
			Duration: 7 * time.Millisecond,
		}, nil
	}

	m := settled(t, newModel(t, client))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)
	m = apply(t, m, cmd)

	view := m.View()
	assert.Contains(t, view, "OK")
	assert.NotContains(t, view, "Call failed", "a successful call was reported as a failure")
	assert.Contains(t, view, "protobuf text", "the panel should say why the body is not JSON")
	assert.Contains(t, view, "some.server.only.Detail")
}

// The 60-second default is generous on purpose, but a service that legitimately
// takes longer — or a user who wants a shorter leash while debugging — needs it
// to be reachable rather than compiled in.
func TestModel_CallTimeoutIsConfigurable(t *testing.T) {
	client := healthyClient()
	client.invoke = func(ctx context.Context, _ grpcclient.Method, _ proto.Message) (*grpcclient.UnaryResponse, error) {
		<-ctx.Done()
		return nil, fmt.Errorf("invoke: %w", status.FromContextError(ctx.Err()).Err())
	}

	m := sized(t, ui.New(client,
		ui.WithLogger(zaptest.NewLogger(t)),
		ui.WithCallTimeout(time.Millisecond),
	))
	m = asModel(t, mustUpdate(m, discoveryResult(t, m)))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)
	m = apply(t, m, cmd)

	assert.Contains(t, m.View(), "DeadlineExceeded",
		"the call outlived the timeout the model was built with")
}
