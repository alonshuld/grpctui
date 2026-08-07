package panels_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// formMethod is a method whose request message is FieldDescriptorProto: a real,
// already-registered message that happens to be exactly what this panel needs —
// a dozen fields covering strings, an int32, two enums, a bool and a nested
// message. Only the service around it has to be invented, and it resolves
// against the global registry, so there is no fixture to keep in step with
// anything.
const (
	formMethodName  = "grpctui.paneltest.v1.Describer.Describe"
	formRequestName = "google.protobuf.FieldDescriptorProto"
)

func formMethod(t *testing.T) grpcclient.Method {
	t.Helper()

	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:       proto.String("grpctui/paneltest/v1/describer.proto"),
		Package:    proto.String("grpctui.paneltest.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("Describer"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Describe"),
				InputType:  proto.String("." + formRequestName),
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

// Row order in FieldDescriptorProto's form, which the tests below navigate by.
// It is declaration order — name, number, label, type, type_name, extendee,
// default_value, oneof_index, json_name, options, proto3_optional — not field
// number order.
const (
	fieldNumber  = 1 // int32
	fieldOptions = 9 // message: not editable until v0.3
)

// down moves the cursor n rows.
func down(t *testing.T, form panels.Form, n int) panels.Form {
	t.Helper()

	for range n {
		form, _ = pressForm(t, form, "j")
	}
	return form
}

// formHeight is tall enough to show every row of the test message at once, so
// that an assertion about the view is never really an assertion about scrolling.
const formHeight = 20

func newForm(t *testing.T) panels.Form {
	t.Helper()

	form := panels.NewForm(keys.Default(), styles.New())
	form.SetSize(60, formHeight)
	form.Focus()
	form.SetMethod(grpcclient.Service{Name: "grpctui.paneltest.v1.Describer"}, formMethod(t))
	return form
}

func pressForm(t *testing.T, form panels.Form, keystrokes ...string) (panels.Form, tea.Cmd) {
	t.Helper()

	var cmd tea.Cmd
	for _, k := range keystrokes {
		form, cmd = form.Update(keyMsg(k))
	}
	return form, cmd
}

func TestForm_EmptyUntilAMethodIsSelected(t *testing.T) {
	form := panels.NewForm(keys.Default(), styles.New())
	form.SetSize(60, 12)

	assert.Contains(t, form.View(), "Select a method to build a request.")

	_, ok := form.Submit()
	assert.False(t, ok)
	assert.Contains(t, form.View(), "Select a method first.")
}

func TestForm_ListsEveryFieldOfTheRequest(t *testing.T) {
	form := newForm(t)
	view := form.View()

	assert.Contains(t, view, formMethodName)
	assert.Contains(t, view, formRequestName)
	for _, name := range []string{"name", "number", "label", "type", "json_name"} {
		assert.Contains(t, view, name)
	}
}

func TestForm_EditingAField(t *testing.T) {
	form := newForm(t)
	require.False(t, form.Editing())

	form, _ = pressForm(t, form, "enter")
	require.True(t, form.Editing(), "enter must start an edit")

	// Every key is now a character, including the ones that are commands
	// elsewhere.
	form, _ = pressForm(t, form, "j", "k", "q")
	assert.Contains(t, form.View(), "jkq")

	form, _ = pressForm(t, form, "backspace")
	assert.Contains(t, form.View(), "jk")

	form, _ = pressForm(t, form, "esc")
	assert.False(t, form.Editing(), "esc must end the edit")
	assert.Contains(t, form.View(), "jk", "the value survives leaving edit mode")
}

func TestForm_EnterEndsAnEditToo(t *testing.T) {
	form := newForm(t)

	form, _ = pressForm(t, form, "enter", "x", "enter")

	assert.False(t, form.Editing())
	assert.Contains(t, form.View(), "x")
}

// Blurring the panel while a field is being edited would otherwise leave the
// form in a mode the user cannot see and cannot escape.
func TestForm_BlurEndsAnEdit(t *testing.T) {
	form := newForm(t)
	form, _ = pressForm(t, form, "enter")
	require.True(t, form.Editing())

	form.Blur()

	assert.False(t, form.Editing())
	assert.False(t, form.Focused())
}

func TestForm_IgnoresKeysWhenBlurred(t *testing.T) {
	form := newForm(t)
	form.Blur()

	form, cmd := pressForm(t, form, "enter", "j")

	assert.Nil(t, cmd)
	assert.False(t, form.Editing())
}

func TestForm_CursorStaysInsideTheFieldList(t *testing.T) {
	form := newForm(t)
	first := form.View()

	// Up from the top row goes nowhere.
	form, _ = pressForm(t, form, "k", "k")
	assert.Equal(t, first, form.View())

	// And the bottom is a wall too, not a wrap.
	form, _ = pressForm(t, form, "G")
	bottom := form.View()
	form, _ = pressForm(t, form, "j", "j")
	assert.Equal(t, bottom, form.View())
}

// A message with more fields than the panel has rows must scroll, keeping the
// cursor on screen rather than walking off the bottom.
func TestForm_ScrollsToKeepTheCursorVisible(t *testing.T) {
	form := newForm(t)
	form.SetSize(60, 6)

	form, _ = pressForm(t, form, "G")
	view := form.View()

	assert.LessOrEqual(t, len(strings.Split(view, "\n")), 6, "the panel drew past its height")
	assert.Contains(t, view, "❯", "the cursor scrolled out of view")
}

func TestForm_UnsupportedFieldsCannotBeEdited(t *testing.T) {
	form := newForm(t)

	// options is a nested message: listed, explained, and not editable. The
	// explanation is truncated to the column, so only its start is asserted.
	require.Contains(t, form.View(), "nested messages ar")

	form = down(t, form, fieldOptions)
	form, _ = pressForm(t, form, "enter")

	assert.False(t, form.Editing(), "a field with no editor must not enter edit mode")
}

func TestForm_SubmitBuildsARequest(t *testing.T) {
	form := newForm(t)

	form, _ = pressForm(t, form, "enter")
	form, _ = pressForm(t, form, "f", "o", "o")
	form, _ = pressForm(t, form, "esc")

	msg, ok := form.Submit()

	require.True(t, ok)
	assert.Equal(t, formMethodName, msg.Method.FullName)
	require.NotNil(t, msg.Request)

	m := msg.Request.ProtoReflect()
	nameField := m.Descriptor().Fields().ByName("name")
	require.NotNil(t, nameField)
	assert.Equal(t, "foo", m.Get(nameField).String())
}

func TestForm_SubmitReportsBadValuesAgainstTheirField(t *testing.T) {
	form := newForm(t)

	form = down(t, form, fieldNumber)
	form, _ = pressForm(t, form, "enter")
	form, _ = pressForm(t, form, "l", "o", "t", "s")
	form, _ = pressForm(t, form, "esc")

	_, ok := form.Submit()

	require.False(t, ok)
	view := form.View()
	assert.Contains(t, view, "expected a whole number")
	assert.Contains(t, view, "⚠")
}

func TestForm_SubmitRefusesStreamingMethods(t *testing.T) {
	form := panels.NewForm(keys.Default(), styles.New())
	form.SetSize(60, formHeight)

	method := formMethod(t)
	method.ServerStreaming = true
	form.SetMethod(grpcclient.Service{Name: "grpctui.paneltest.v1.Describer"}, method)

	_, ok := form.Submit()

	assert.False(t, ok)
	assert.Contains(t, form.View(), "v0.5")
}

func TestForm_ClearForgetsTheMethod(t *testing.T) {
	form := newForm(t)
	require.Contains(t, form.View(), formMethodName)

	form.Clear()

	assert.Contains(t, form.View(), "Select a method to build a request.")
	_, ok := form.Method()
	assert.False(t, ok)
}

// Selecting a different method must not carry the previous one's values over:
// they belong to a different message.
func TestForm_SetMethodResetsTheValues(t *testing.T) {
	form := newForm(t)
	form, _ = pressForm(t, form, "enter", "x", "esc")
	require.Contains(t, form.View(), "x")

	form.SetMethod(grpcclient.Service{Name: "grpctui.paneltest.v1.Describer"}, formMethod(t))
	assert.False(t, form.Editing())

	msg, ok := form.Submit()
	require.True(t, ok)

	m := msg.Request.ProtoReflect()
	assert.False(t, m.Has(m.Descriptor().Fields().ByName("name")), "the old value came along")
}

func TestForm_ContentHeightGrowsWithTheMessage(t *testing.T) {
	empty := panels.NewForm(keys.Default(), styles.New())
	empty.SetSize(60, 12)

	assert.Less(t, empty.ContentHeight(), newForm(t).ContentHeight())
}

func TestForm_SurvivesDegenerateSizes(t *testing.T) {
	for _, size := range []struct{ w, h int }{{0, 0}, {1, 1}, {3, 2}, {80, 0}, {-4, -4}} {
		form := newForm(t)
		form.SetSize(size.w, size.h)

		assert.NotPanics(t, func() { _ = form.View() })
	}
}

// Row order again, for the tests below: the bool is proto3_optional, the last
// field of FieldDescriptorProto — and, being a proto2 field, one with explicit
// presence, so false and unset are genuinely different answers.
const (
	fieldTypeName       = 4  // string
	fieldProto3Optional = 10 // bool, with presence
)

// A page jump moves by the field rows on screen, not by the panel's height. The
// two are not the same number: the header takes three lines before the first
// field, and every hint takes another, so paging by the height steps over
// fields the user never saw.
func TestForm_PagesByVisibleFieldRows(t *testing.T) {
	form := newForm(t)
	form.SetSize(60, 8) // header (3 lines) + 5 field rows

	form, _ = pressForm(t, form, "ctrl+d")

	view := form.View()
	assert.Contains(t, view, "❯ type_name", "a page down should land on the last field that was visible")
	assert.NotContains(t, view, "❯ oneof_index", "a page down overshot by the header")

	form, _ = pressForm(t, form, "ctrl+u")
	assert.Contains(t, form.View(), "❯ name", "a page up should come back to the top")
}

func TestForm_PagingStopsAtTheEnds(t *testing.T) {
	form := newForm(t)
	form.SetSize(60, 8)

	form, _ = pressForm(t, form, "ctrl+u", "ctrl+u")
	assert.Contains(t, form.View(), "❯ name")

	for range 10 {
		form, _ = pressForm(t, form, "ctrl+d")
	}
	assert.Contains(t, form.View(), "❯ proto3_optional")
}

// Space cycles a bool between an explicit true and an explicit false. The
// second is not the same as never having touched the field: for a bool with
// presence — which every proto2 field has — false is a value the caller can
// only send by saying so.
func TestForm_BoolTogglesBetweenTrueAndFalse(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldProto3Optional)

	form, _ = pressForm(t, form, " ")
	msg, ok := form.Submit()
	require.True(t, ok)
	assert.True(t, boolField(t, msg.Request, "proto3_optional"))

	form, _ = pressForm(t, form, " ")
	msg, ok = form.Submit()
	require.True(t, ok)
	assert.False(t, boolField(t, msg.Request, "proto3_optional"))
	assert.True(t, fieldIsSet(t, msg.Request, "proto3_optional"),
		"a toggled-off bool must be sent as an explicit false, not dropped")
}

func TestForm_AnUntouchedBoolIsNotSent(t *testing.T) {
	form := newForm(t)

	msg, ok := form.Submit()

	require.True(t, ok)
	assert.False(t, fieldIsSet(t, msg.Request, "proto3_optional"),
		"a bool nobody toggled must not be sent at all")
}

// Typing a value and then deleting it is how a user says "explicitly empty",
// and it has to be distinguishable from never having visited the field.
func TestForm_ClearingAFieldSendsAnExplicitEmptyValue(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldTypeName)

	form, _ = pressForm(t, form, "enter", "x", "backspace", "esc")

	msg, ok := form.Submit()
	require.True(t, ok)
	assert.True(t, fieldIsSet(t, msg.Request, "type_name"),
		`a field the user cleared must be sent as ""`)
	assert.Empty(t, stringField(t, msg.Request, "type_name"))
}

func TestForm_AnUntouchedFieldIsNotSent(t *testing.T) {
	form := newForm(t)

	msg, ok := form.Submit()

	require.True(t, ok)
	assert.False(t, fieldIsSet(t, msg.Request, "type_name"),
		"a field nobody visited must not be sent")
}

// Opening a field and leaving it without typing is not the same as clearing it.
func TestForm_EnteringAFieldWithoutTypingLeavesItUnset(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldTypeName)

	form, _ = pressForm(t, form, "enter", "esc")

	msg, ok := form.Submit()
	require.True(t, ok)
	assert.False(t, fieldIsSet(t, msg.Request, "type_name"))
}

func requestField(t *testing.T, msg proto.Message, name string) (protoreflect.Message, protoreflect.FieldDescriptor) {
	t.Helper()

	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "no field %q on %s", name, m.Descriptor().FullName())
	return m, fd
}

func fieldIsSet(t *testing.T, msg proto.Message, name string) bool {
	t.Helper()
	m, fd := requestField(t, msg, name)
	return m.Has(fd)
}

func boolField(t *testing.T, msg proto.Message, name string) bool {
	t.Helper()
	m, fd := requestField(t, msg, name)
	return m.Get(fd).Bool()
}

func stringField(t *testing.T, msg proto.Message, name string) string {
	t.Helper()
	m, fd := requestField(t, msg, name)
	return m.Get(fd).String()
}

// A field with presence has a state an ordinary field does not — filled in with
// nothing — and one it does not share with its own zero value. The row has to
// distinguish them, or the user cannot tell what will be sent.
func TestForm_MarksPresenceFieldsThatAreUnsetOrEmpty(t *testing.T) {
	form := newForm(t)

	t.Run("an untouched bool with presence is not false", func(t *testing.T) {
		assert.Contains(t, form.View(), "unset")
	})

	t.Run("a toggled bool shows its value", func(t *testing.T) {
		toggled := down(t, form, fieldProto3Optional)
		toggled, _ = pressForm(t, toggled, " ", " ")

		view := toggled.View()
		assert.Contains(t, view, "false")
	})

	t.Run("a cleared field is not the same as an untouched one", func(t *testing.T) {
		cleared := down(t, form, fieldTypeName)
		cleared, _ = pressForm(t, cleared, "enter", "x", "backspace", "esc")

		assert.Contains(t, cleared.View(), `""`,
			"a field cleared by hand will be sent as an empty value and must say so")
	})
}
