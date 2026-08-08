package ui_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// stripANSI removes the styling, so an assertion about what is on screen reads
// the cells a terminal would draw rather than the escapes that colour them.
func stripANSI(s string) string { return ansi.Strip(s) }

// withColour makes lipgloss emit escape codes for the duration of one test.
//
// Under `go test` stdout is not a terminal, so lipgloss detects no colour
// support and renders every style as bare text — which would leave a test
// asserting that a theme reached the frame asserting nothing at all. No test in
// this package runs in parallel, so setting the profile and putting it back is
// safe.
func withColour(t *testing.T) {
	t.Helper()

	before := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(before) })
}

// magenta is a theme nothing else uses, recognisable in a frame by its escape
// codes alone.
func magenta() styles.Theme {
	return styles.Theme{Name: "magenta", Description: "for a test", Palette: styles.Palette{
		Primary:   lipgloss.Color("#ff00ff"),
		Secondary: lipgloss.Color("#00ff00"),
		Muted:     lipgloss.Color("#ff8800"),
		Border:    lipgloss.Color("#0000ff"),
		Error:     lipgloss.Color("#ff0000"),
		Success:   lipgloss.Color("#00ffff"),
		Text:      lipgloss.Color("#ffffff"),
		Inverted:  lipgloss.Color("#000000"),
	}}
}

// TestModel_ThemeSwitcher pins the feature end to end: T opens the switcher,
// moving the cursor previews a theme, and the frame behind it is redrawn in it.
func TestModel_ThemeSwitcher(t *testing.T) {
	withColour(t)

	themes := append(styles.BuiltinThemes(), magenta())
	m := settled(t, newModel(t,
		&fakeClient{target: "localhost:50051", services: testServices()},
		ui.WithThemes(themes, 0)))

	before := m.View()

	m, _ = press(t, m, "T")
	require.Contains(t, stripANSI(m.View()), "Themes", "T did not open the switcher")
	require.Contains(t, stripANSI(m.View()), "magenta", "the config file's theme is not listed")

	// Moving the cursor previews rather than waiting for enter: a theme is
	// judged by looking at it, and a chooser that made you commit first would
	// be one you had to open four times.
	//
	// Three steps down, onto magenta. The built-in dark theme would not do: it
	// is the auto theme with the guessing taken out, so on a terminal reporting
	// a dark background the two render identically — which is exactly what they
	// are meant to do, and useless as evidence that anything changed.
	m, _ = press(t, m, "down", "down")
	m, cmd := press(t, m, "down")
	require.NotNil(t, cmd, "moving the cursor should have asked for a theme")

	m = apply(t, m, cmd)
	m, _ = press(t, m, "esc")

	after := m.View()
	assert.NotEqual(t, before, after, "the frame kept its old colours")
	assert.Contains(t, stripANSI(after), "localhost:50051", "the frame lost its content")
}

// TestModel_ThemeSelectedMsg is the same switch driven by the message alone,
// which is what proves every panel — not only the one on top — was handed the
// new styles.
func TestModel_ThemeSelectedMsg(t *testing.T) {
	withColour(t)

	m := settled(t, newModel(t,
		&fakeClient{target: "localhost:50051", services: testServices()},
		ui.WithThemes(styles.BuiltinThemes(), 0)))

	before := m.View()
	m = asModel(t, mustUpdate(m, panels.ThemeSelectedMsg{Index: 0, Theme: magenta()}))
	after := m.View()

	require.NotEqual(t, before, after)
	assert.Contains(t, after, "255;0;255", "the new primary colour is not in the frame")

	// The tree, the form and the response are all on screen at once, and a
	// panel left behind would be visible in the frame.
	for _, title := range []string{"Services", "Request", "Response"} {
		assert.Contains(t, stripANSI(after), title)
	}
}

// TestModel_ThemeDefault pins that a model built without WithThemes still
// offers the built-ins, so T is never a key that does nothing.
func TestModel_ThemeDefault(t *testing.T) {
	m := settled(t, newModel(t, &fakeClient{target: "localhost:50051", services: testServices()}))

	m, _ = press(t, m, "T")
	view := stripANSI(m.View())

	assert.Contains(t, view, "Themes")
	for _, theme := range styles.BuiltinThemes() {
		assert.Contains(t, view, theme.Name)
	}
}

// TestModel_KeyMap pins that a remapped key reaches the panels.
func TestModel_KeyMap(t *testing.T) {
	km, err := keys.Default().Apply(map[string]string{"themes": "ctrl+t"})
	require.NoError(t, err)

	m := settled(t, newModel(t,
		&fakeClient{target: "localhost:50051", services: testServices()},
		ui.WithKeyMap(km)))

	// The old key no longer opens the switcher…
	m, _ = press(t, m, "T")
	assert.NotContains(t, stripANSI(m.View()), "Themes")

	// …and the new one does.
	m, _ = press(t, m, "ctrl+t")
	assert.Contains(t, stripANSI(m.View()), "Themes")
}

// TestModel_Renderers pins the glosses end to end: the registry is installed on
// the model, the call command computes the notes off the Update goroutine, and
// the response panel draws them beside the values they describe.
func TestModel_Renderers(t *testing.T) {
	registry, err := render.New(render.Builtins()...)
	require.NoError(t, err)

	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	m := sendGloss(t, glossModel(t, now,
		ui.WithRenderers(registry),
		ui.WithClock(func() time.Time { return now })))

	view := stripANSI(m.View())
	require.Contains(t, view, "3 minutes ago")

	// Beside the value, never instead of it: the response panel shows what the
	// server sent, and a gloss is commentary on it.
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(line, "3 minutes ago") {
			assert.Contains(t, line, "2026-08-08T11:57")
			assert.Less(t, strings.Index(line, "2026-08-08"), strings.Index(line, "3 minutes ago"))
			return
		}
	}
	t.Fatal("the gloss is on no line")
}

// TestModel_NoRenderers pins the default: without a registry the response panel
// shows exactly the JSON the server sent, which is what every version before
// v0.9 did.
func TestModel_NoRenderers(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	m := sendGloss(t, glossModel(t, now, ui.WithClock(func() time.Time { return now })))

	view := stripANSI(m.View())
	assert.Contains(t, view, "2026-08-08T11:57")
	assert.NotContains(t, view, "ago")
}

// glossModel builds a settled model over a one-method service whose reply
// carries a timestamp three minutes before now.
//
// It brings its own fixture rather than adding a timestamp to the shared demo
// file, which every golden in this package renders: a field added there would
// churn a dozen approved frames to serve one test.
func glossModel(t *testing.T, now time.Time, opts ...ui.Option) ui.Model {
	t.Helper()

	client := &fakeClient{target: "localhost:50051", services: glossServices()}
	client.invoke = func(context.Context, grpcclient.Method, proto.Message) (*grpcclient.UnaryResponse, error) {
		md := glossFile().Messages().ByName("Pong")
		msg := dynamicpb.NewMessage(md)
		msg.Set(md.Fields().ByName("created_at"),
			protoreflect.ValueOfMessage(timestamppb.New(now.Add(-3*time.Minute)).ProtoReflect()))
		return &grpcclient.UnaryResponse{Message: msg, Duration: time.Millisecond}, nil
	}

	return settled(t, newModel(t, client, opts...))
}

// sendGloss selects the fixture's one method and calls it.
func sendGloss(t *testing.T, m ui.Model) ui.Model {
	t.Helper()

	// One service, one method: a single j lands on it.
	m = selectMethod(t, m, 1)

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd, "ctrl+s must start a call")
	return apply(t, m, cmd)
}

// glossFile is a service whose reply carries a well-known type, so that a
// built-in renderer has something to gloss:
//
//	syntax = "proto3";
//	package gloss.v1;
//
//	import "google/protobuf/timestamp.proto";
//
//	message Ping {}
//	message Pong { google.protobuf.Timestamp created_at = 1; }
//
//	service Gloss { rpc Ping(Ping) returns (Pong); }
var glossFile = sync.OnceValue(func() protoreflect.FileDescriptor {
	message := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL

	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:       proto.String("gloss/v1/gloss.proto"),
		Package:    proto.String("gloss.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Ping")},
			{Name: proto.String("Pong"), Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("created_at"),
				Number:   proto.Int32(1),
				Type:     &message,
				Label:    &optional,
				TypeName: proto.String(".google.protobuf.Timestamp"),
				JsonName: proto.String("createdAt"),
			}}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("Gloss"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Ping"),
				InputType:  proto.String(".gloss.v1.Ping"),
				OutputType: proto.String(".gloss.v1.Pong"),
			}},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		panic(fmt.Sprintf("build the gloss descriptors: %v", err))
	}
	return fd
})

func glossServices() []grpcclient.Service {
	return []grpcclient.Service{serviceFrom(glossFile().Services().Get(0))}
}
