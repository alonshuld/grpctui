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
	_ "google.golang.org/protobuf/types/known/structpb" // registers google.protobuf.Value

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// The panel's fixtures are real, already-registered messages, invented services
// aside: descriptor.proto and struct.proto between them cover every shape a
// request form has to render, and resolving against the global registry means
// there is no descriptor literal here to keep in step with anything.
const (
	formMethodName  = "grpctui.paneltest.v1.Describer.Describe"
	formRequestName = "google.protobuf.FieldDescriptorProto"

	descriptorProto = "google/protobuf/descriptor.proto"
	structProto     = "google/protobuf/struct.proto"

	formService = "grpctui.paneltest.v1.Describer"
)

// methodFor invents a unary method taking the named request message.
func methodFor(t *testing.T, dependency, request string) grpcclient.Method {
	t.Helper()

	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:       proto.String("grpctui/paneltest/v1/describer.proto"),
		Package:    proto.String("grpctui.paneltest.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{dependency},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("Describer"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Describe"),
				InputType:  proto.String("." + request),
				OutputType: proto.String("." + request),
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

func formMethod(t *testing.T) grpcclient.Method {
	t.Helper()
	return methodFor(t, descriptorProto, formRequestName)
}

// Row order in FieldDescriptorProto's form, which the tests below navigate by.
// It is declaration order — name, number, label, type, type_name, extendee,
// default_value, oneof_index, json_name, options, proto3_optional — not field
// number order.
const (
	fieldNumber   = 1  // int32
	fieldTypeName = 4  // string
	fieldOptions  = 9  // message
	fieldProto3   = 10 // bool, with presence
)

// down moves the cursor n rows.
func down(t *testing.T, form panels.Form, n int) panels.Form {
	t.Helper()

	for range n {
		form, _ = pressForm(t, form, "j")
	}
	return form
}

// downTo walks the cursor onto the named row, so that a test navigating a real
// descriptor does not also pin where in descriptor.proto a field is declared.
func downTo(t *testing.T, form panels.Form, name string) panels.Form {
	t.Helper()

	for range 100 {
		if cursorLabel(form.View()) == name {
			return form
		}
		next, _ := pressForm(t, form, "j")
		if next.View() == form.View() {
			break
		}
		form = next
	}

	t.Fatalf("no row %q in:\n%s", name, form.View())
	return form
}

// cursorLabel is the name of the row under the cursor, with the glyphs saying
// what kind of row it is stripped off.
func cursorLabel(view string) string {
	for line := range strings.SplitSeq(view, "\n") {
		if !strings.HasPrefix(line, "❯ ") {
			continue
		}
		for word := range strings.FieldsSeq(strings.TrimPrefix(line, "❯ ")) {
			if !strings.ContainsAny(word, "▾▸●○") {
				return word
			}
		}
	}
	return ""
}

// formHeight is tall enough to show every row of the test message at once, so
// that an assertion about the view is never really an assertion about scrolling.
const formHeight = 20

func newForm(t *testing.T) panels.Form {
	t.Helper()
	return formFor(t, formMethod(t))
}

func formFor(t *testing.T, method grpcclient.Method) panels.Form {
	t.Helper()

	form := panels.NewForm(keys.Default(), styles.New())
	form.SetSize(72, formHeight)
	form.Focus()
	form.SetMethod(grpcclient.Service{Name: formService}, method)
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

// submit sends the form, failing the test if it will not go.
func submit(t *testing.T, form panels.Form) panels.SendRequestMsg {
	t.Helper()

	msg, ok := form.Submit()
	require.True(t, ok, "the form refused to build a request:\n%s", form.View())
	return msg
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

func TestForm_SubmitBuildsARequest(t *testing.T) {
	form := newForm(t)

	form, _ = pressForm(t, form, "enter")
	form, _ = pressForm(t, form, "f", "o", "o")
	form, _ = pressForm(t, form, "esc")

	msg := submit(t, form)

	assert.Equal(t, formMethodName, msg.Method.FullName)
	require.NotNil(t, msg.Request)
	assert.Equal(t, "foo", stringField(t, msg.Request, "name"))
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

// A typo is reported where it was made. Waiting for the send would mean typing
// out the rest of the form before finding out the first field was wrong.
func TestForm_ChecksAFieldAsTheEditEnds(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldNumber)

	form, _ = pressForm(t, form, "enter", "l", "o", "t", "s", "esc")
	assert.Contains(t, form.View(), "expected a whole number")

	form, _ = pressForm(t, form, "enter", "backspace", "backspace", "backspace", "backspace", "7", "esc")
	assert.NotContains(t, form.View(), "expected a whole number", "fixing the value must clear the complaint")

	msg := submit(t, form)
	assert.Equal(t, int32(7), int32Field(t, msg.Request, "number"))
}

func TestForm_SubmitRefusesStreamingMethods(t *testing.T) {
	method := formMethod(t)
	method.ServerStreaming = true

	form := formFor(t, method)

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

	form.SetMethod(grpcclient.Service{Name: formService}, formMethod(t))
	assert.False(t, form.Editing())

	msg := submit(t, form)
	assert.False(t, fieldIsSet(t, msg.Request, "name"), "the old value came along")
}

func TestForm_ContentHeightGrowsWithTheMessage(t *testing.T) {
	empty := panels.NewForm(keys.Default(), styles.New())
	empty.SetSize(60, 12)

	assert.Less(t, empty.ContentHeight(), newForm(t).ContentHeight())
}

// Expanding a nested message adds rows to the panel, which has to ask the root
// model for more of the column — or the fields it just revealed are drawn over
// the response.
func TestForm_ContentHeightGrowsWhenARowIsExpanded(t *testing.T) {
	form := newForm(t)
	before := form.ContentHeight()

	form = down(t, form, fieldOptions)
	form, _ = pressForm(t, form, "enter")

	assert.Greater(t, form.ContentHeight(), before)
}

func TestForm_SurvivesDegenerateSizes(t *testing.T) {
	for _, size := range []struct{ w, h int }{{0, 0}, {1, 1}, {3, 2}, {80, 0}, {-4, -4}} {
		form := newForm(t)
		form.SetSize(size.w, size.h)

		assert.NotPanics(t, func() { _ = form.View() })
	}
}

// A page jump moves by the field rows on screen, not by the panel's height. The
// two are not the same number: the header takes three lines before the first
// field, so paging by the height steps over fields the user never saw.
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
// presence — which every field of descriptor.proto has — false is a value the
// caller can only send by saying so.
func TestForm_BoolTogglesBetweenTrueAndFalse(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldProto3)

	form, _ = pressForm(t, form, " ")
	assert.True(t, boolField(t, submit(t, form).Request, "proto3_optional"))

	form, _ = pressForm(t, form, " ")
	msg := submit(t, form)
	assert.False(t, boolField(t, msg.Request, "proto3_optional"))
	assert.True(t, fieldIsSet(t, msg.Request, "proto3_optional"),
		"a toggled-off bool must be sent as an explicit false, not dropped")
}

func TestForm_AnUntouchedBoolIsNotSent(t *testing.T) {
	form := newForm(t)

	assert.False(t, fieldIsSet(t, submit(t, form).Request, "proto3_optional"),
		"a bool nobody toggled must not be sent at all")
}

// Typing a value and then deleting it is how a user says "explicitly empty",
// and it has to be distinguishable from never having visited the field.
func TestForm_ClearingAFieldSendsAnExplicitEmptyValue(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldTypeName)

	form, _ = pressForm(t, form, "enter", "x", "backspace", "esc")

	msg := submit(t, form)
	assert.True(t, fieldIsSet(t, msg.Request, "type_name"),
		`a field the user cleared must be sent as ""`)
	assert.Empty(t, stringField(t, msg.Request, "type_name"))
}

func TestForm_AnUntouchedFieldIsNotSent(t *testing.T) {
	form := newForm(t)

	assert.False(t, fieldIsSet(t, submit(t, form).Request, "type_name"),
		"a field nobody visited must not be sent")
}

// Opening a field and leaving it without typing is not the same as clearing it.
func TestForm_EnteringAFieldWithoutTypingLeavesItUnset(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldTypeName)

	form, _ = pressForm(t, form, "enter", "esc")

	assert.False(t, fieldIsSet(t, submit(t, form).Request, "type_name"))
}

// An enum is picked from the list it expands to, not typed in: nothing else on
// screen says what a legal value looks like.
func TestForm_EnumIsPickedFromItsValues(t *testing.T) {
	form := newForm(t)
	form = downTo(t, form, "label")

	form, _ = pressForm(t, form, "enter")
	require.False(t, form.Editing(), "an enum is not typed into")
	require.Contains(t, form.View(), "○ LABEL_OPTIONAL", "expanding an enum shows what it accepts")

	// Down onto the first value, and pick it.
	form, _ = pressForm(t, form, "j", "enter")

	view := form.View()
	assert.NotContains(t, view, "○ LABEL_", "picking a value folds the list away again")
	assert.Contains(t, view, "❯ ▸ label", "the cursor comes back to the field that was picked for")
	assert.Contains(t, view, "LABEL_OPTIONAL", "the row shows what was picked")

	msg := submit(t, form)
	assert.Equal(t, protoreflect.EnumNumber(1), enumField(t, msg.Request, "label"))
}

// A nested message is a row that opens, not a row that is typed into.
func TestForm_NestedMessageExpands(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldOptions)

	form, _ = pressForm(t, form, "enter")

	require.False(t, form.Editing())
	view := form.View()
	assert.Contains(t, view, "▾ options")
	assert.Contains(t, view, "deprecated", "the message's own fields are the rows underneath it")

	// And h folds it back up.
	form, _ = pressForm(t, form, "h")
	assert.NotContains(t, form.View(), "deprecated")
}

func TestForm_CollapseJumpsToTheParentRow(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldOptions)
	form, _ = pressForm(t, form, "l", "j") // open options, step onto its first field

	form, _ = pressForm(t, form, "h")

	assert.Contains(t, form.View(), "❯ ▾ options",
		"collapsing from a field inside a message goes to the message")
}

// A message with nothing in it is not sent by default — there is nothing to
// distinguish it from one nobody opened — so saying "send it anyway" is a
// keystroke of its own.
func TestForm_EmptyNestedMessageIsSentOnlyWhenAskedFor(t *testing.T) {
	form := newForm(t)
	form = down(t, form, fieldOptions)

	assert.False(t, fieldIsSet(t, submit(t, form).Request, "options"))

	form, _ = pressForm(t, form, " ")
	assert.True(t, fieldIsSet(t, submit(t, form).Request, "options"))
}

func TestForm_NestedValuesAreSent(t *testing.T) {
	form := newForm(t)
	form = downTo(t, form, "options")

	// A value inside the message brings the message with it: nothing had to say
	// "send options" for its own sake.
	form, _ = pressForm(t, form, "l")
	form = downTo(t, form, "deprecated")
	form, _ = pressForm(t, form, " ")

	msg := submit(t, form)
	require.True(t, fieldIsSet(t, msg.Request, "options"))

	m, fd := requestField(t, msg.Request, "options")
	options := m.Get(fd).Message()
	assert.True(t, options.Get(options.Descriptor().Fields().ByName("deprecated")).Bool())
}

// FileDescriptorProto is the fixture with repeated fields in it: `dependency`
// is a repeated string, which is the shape a user fills in by hand most often.
func listForm(t *testing.T) panels.Form {
	t.Helper()
	return formFor(t, methodFor(t, descriptorProto, "google.protobuf.FileDescriptorProto"))
}

func TestForm_RepeatedFieldAddsAndRemovesItems(t *testing.T) {
	form := listForm(t)
	form = downTo(t, form, "dependency")
	require.Contains(t, form.View(), "press a to add an item")

	form, _ = pressForm(t, form, "a")
	form, _ = pressForm(t, form, "enter", "o", "n", "e", "esc")

	// `a` from inside the list adds a sibling, so a list is filled in without
	// ever going back to the row above it.
	form, _ = pressForm(t, form, "a")
	form, _ = pressForm(t, form, "enter", "t", "w", "o", "esc")
	form, _ = pressForm(t, form, "a")
	form, _ = pressForm(t, form, "enter", "s", "i", "x", "esc")

	view := form.View()
	assert.Contains(t, view, "[0]")
	assert.Contains(t, view, "[2]")
	assert.Contains(t, view, "3 items")

	assert.Equal(t, []string{"one", "two", "six"}, stringList(t, submit(t, form).Request, "dependency"))

	// And d takes the item under the cursor back out again, renumbering the rest.
	form, _ = pressForm(t, form, "k", "d")

	assert.Equal(t, []string{"one", "six"}, stringList(t, submit(t, form).Request, "dependency"))
	assert.Contains(t, form.View(), "2 items")
	assert.Equal(t, "[1]", cursorLabel(form.View()),
		"the cursor stays on the row the removed item left, where the next one now is")
	assert.Contains(t, form.View(), "six")
}

func TestForm_RepeatedMessagesAreFilledInLikeAnyOther(t *testing.T) {
	form := listForm(t)
	form = downTo(t, form, "message_type")

	form, _ = pressForm(t, form, "a")
	require.Contains(t, form.View(), "▾ [0]", "a new message item opens, or its fields are out of reach")

	form = downTo(t, form, "name")
	form, _ = pressForm(t, form, "enter", "M", "s", "g", "esc")

	msg := submit(t, form)
	m, fd := requestField(t, msg.Request, "message_type")
	list := m.Get(fd).List()
	require.Equal(t, 1, list.Len())

	item := list.Get(0).Message()
	assert.Equal(t, "Msg", item.Get(item.Descriptor().Fields().ByName("name")).String())
}

// google.protobuf.Value is one oneof and nothing else, which is exactly the
// shape this needs.
func TestForm_OneofPicksOneVariant(t *testing.T) {
	form := formFor(t, methodFor(t, structProto, "google.protobuf.Value"))

	require.Contains(t, form.View(), "▸ kind")
	form, _ = pressForm(t, form, "enter")

	view := form.View()
	assert.Contains(t, view, "○ number_value", "every variant is offered")
	assert.Contains(t, view, "○ string_value")

	// Typing into a variant picks it: that is what typing there meant.
	form, _ = pressForm(t, form, "j", "j", "j") // null_value, number_value, string_value
	form, _ = pressForm(t, form, "enter", "h", "i", "esc")

	assert.Contains(t, form.View(), "● string_value")

	msg := submit(t, form)
	assert.Equal(t, "hi", stringField(t, msg.Request, "string_value"))
	assert.False(t, fieldIsSet(t, msg.Request, "number_value"))
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

func int32Field(t *testing.T, msg proto.Message, name string) int32 {
	t.Helper()
	m, fd := requestField(t, msg, name)
	return int32(m.Get(fd).Int())
}

func enumField(t *testing.T, msg proto.Message, name string) protoreflect.EnumNumber {
	t.Helper()
	m, fd := requestField(t, msg, name)
	return m.Get(fd).Enum()
}

func stringList(t *testing.T, msg proto.Message, name string) []string {
	t.Helper()

	m, fd := requestField(t, msg, name)
	list := m.Get(fd).List()

	out := make([]string, 0, list.Len())
	for i := range list.Len() {
		out = append(out, list.Get(i).String())
	}
	return out
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
		toggled := down(t, form, fieldProto3)
		toggled, _ = pressForm(t, toggled, " ", " ")

		assert.Contains(t, toggled.View(), "false")
	})

	t.Run("a cleared field is not the same as an untouched one", func(t *testing.T) {
		cleared := down(t, form, fieldTypeName)
		cleared, _ = pressForm(t, cleared, "enter", "x", "backspace", "esc")

		assert.Contains(t, cleared.View(), `""`,
			"a field cleared by hand will be sent as an empty value and must say so")
	})
}
