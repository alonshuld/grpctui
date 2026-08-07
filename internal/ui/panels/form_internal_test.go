package panels

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// The form caches its rendered rows, which turns every missed invalidation into
// a panel showing something that is no longer true — a stale cursor, a field
// error that has been fixed, a value that is not what was typed. The cache is
// only safe if every mutation refreshes it, so this walks the mutations and
// checks the cache against a fresh build after each one.
//
// It lives in the package rather than beside the other form tests because the
// cache is exactly the kind of thing a black-box test cannot see: a stale cache
// and a correct one render identically right up until they do not.
func TestForm_RenderCacheNeverGoesStale(t *testing.T) {
	method := internalTestMethod(t)
	service := grpcclient.Service{Name: "grpctui.paneltest.v1.Describer"}

	steps := []struct {
		name string
		do   func(f *Form)
	}{
		{"fresh from the constructor", func(_ *Form) {}},
		{"sized", func(f *Form) { f.SetSize(60, 20) }},
		{"focused", func(f *Form) { f.Focus() }},
		{"method selected", func(f *Form) { f.SetMethod(service, method) }},
		{"cursor moved down", func(f *Form) { *f = pressInternal(*f, "j") }},
		{"cursor moved to the bottom", func(f *Form) { *f = pressInternal(*f, "G") }},
		{"cursor moved to the top", func(f *Form) { *f = pressInternal(*f, "g") }},
		{"paged down", func(f *Form) { *f = pressInternal(*f, "ctrl+d") }},
		{"paged up", func(f *Form) { *f = pressInternal(*f, "ctrl+u") }},
		{"editing started", func(f *Form) { *f = pressInternal(*f, "enter") }},
		{"a character typed", func(f *Form) { *f = pressInternal(*f, "x") }},
		{"a character deleted", func(f *Form) { *f = pressInternal(*f, "backspace") }},
		{"editing ended", func(f *Form) { f.StopEditing() }},
		{"submitted", func(f *Form) { f.Submit() }},
		{"resized smaller", func(f *Form) { f.SetSize(30, 6) }},
		// Wide enough that the value column is on screen at all: at 30 cells the
		// name and type columns use up the row, and a toggle would change
		// nothing visible — which would make the check below vacuous.
		{"resized wider", func(f *Form) { f.SetSize(90, 10) }},
		{"a row expanded", func(f *Form) {
			f.moveTo(9) // options, a nested message
			f.expand()
		}},
		{"a row collapsed", func(f *Form) { f.collapse() }},
		{"an item added", func(f *Form) {
			f.moveTo(9)
			f.expand()
			*f = pressInternal(*f, "j", "j", "j", "j") // options ▸ uninterpreted_option
			f.addItem()
		}},
		{"an item removed", func(f *Form) { f.removeItem() }},
		{"bool toggled", func(f *Form) {
			f.moveTo(len(f.rows) - 1)
			f.toggle()
		}},
		{"bool toggled back", func(f *Form) { f.toggle() }},
		{"a bad value submitted", func(f *Form) {
			f.moveTo(1) // number, an int32
			*f = pressInternal(*f, "enter", "l", "o", "t", "s")
			f.StopEditing()
			f.Submit()
		}},
		{"blurred", func(f *Form) { f.Blur() }},
		{"cleared", func(f *Form) { f.Clear() }},
	}

	form := NewForm(keys.Default(), styles.New())
	for _, step := range steps {
		step.do(&form)

		t.Run(step.name, func(t *testing.T) {
			assert.Equal(t, form.build(), form.render(),
				"the cached rows are not what a fresh render produces")
		})
	}
}

func pressInternal(f Form, keystrokes ...string) Form {
	for _, k := range keystrokes {
		f, _ = f.Update(internalKeyMsg(k))
	}
	return f
}

func internalKeyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

// internalTestMethod mirrors the fixture the black-box form tests use: a
// service invented around google.protobuf.FieldDescriptorProto, which already
// has the mix of kinds a form panel needs.
func internalTestMethod(t *testing.T) grpcclient.Method {
	t.Helper()

	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:       proto.String("grpctui/paneltest/v1/internal.proto"),
		Package:    proto.String("grpctui.paneltest.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("Describer"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Describe"),
				InputType:  proto.String(".google.protobuf.FieldDescriptorProto"),
				OutputType: proto.String(".google.protobuf.DescriptorProto"),
			}},
		}},
	}, protoregistry.GlobalFiles)
	require.NoError(t, err)

	md := file.Services().Get(0).Methods().Get(0)
	return grpcclient.Method{
		Name:       string(md.Name()),
		FullName:   string(md.FullName()),
		InputType:  string(md.Input().FullName()),
		OutputType: string(md.Output().FullName()),
		Descriptor: md,
	}
}
