package protoschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/protoschema"
)

func testForm(t *testing.T) protoschema.Form {
	t.Helper()
	return protoschema.NewForm(scalarsDescriptor(t))
}

// row finds a visible row by path. A row that is not on screen is not findable,
// exactly as it is not reachable in the panel: reaching a nested field means
// expanding what holds it first.
func row(t *testing.T, form protoschema.Form, path string) *protoschema.Node {
	t.Helper()

	for _, n := range form.Rows() {
		if n.Path() == path {
			return n
		}
	}
	t.Fatalf("no row %q in %s; visible rows are %v", path, form.Name, paths(form))
	return nil
}

// open expands a row and returns it.
func open(t *testing.T, form protoschema.Form, path string) *protoschema.Node {
	t.Helper()

	n := row(t, form, path)
	n.SetExpanded(true)
	return n
}

func paths(form protoschema.Form) []string {
	rows := form.Rows()
	out := make([]string, 0, len(rows))
	for _, n := range rows {
		out = append(out, n.Path())
	}
	return out
}

func TestNewForm(t *testing.T) {
	form := testForm(t)

	assert.Equal(t, testMessage, form.Name)

	// Declaration order, with every variant of a oneof folded into the one row
	// that picks between them.
	t.Run("lists every field in declaration order", func(t *testing.T) {
		assert.Equal(t, []string{
			"text", "flag", "count", "total", "port", "size", "delta", "offset",
			"ratio", "weight", "payload", "colour", "nested", "tags", "labels",
			"choice", "note", "retry_count", "verbose", "notes", "tree",
		}, paths(form))
	})

	t.Run("classifies every scalar kind", func(t *testing.T) {
		tests := map[string]struct {
			kind     protoschema.Kind
			typeName string
		}{
			"text":    {protoschema.KindString, "string"},
			"count":   {protoschema.KindInt, "int32"},
			"total":   {protoschema.KindInt, "int64"},
			"port":    {protoschema.KindUint, "uint32"},
			"size":    {protoschema.KindUint, "uint64"},
			"delta":   {protoschema.KindInt, "sint32"},
			"offset":  {protoschema.KindUint, "fixed64"},
			"ratio":   {protoschema.KindFloat, "float"},
			"weight":  {protoschema.KindFloat, "double"},
			"payload": {protoschema.KindBytes, "bytes"},
		}

		for name, want := range tests {
			t.Run(name, func(t *testing.T) {
				n := row(t, form, name)
				assert.Equal(t, want.kind, n.Kind())
				assert.Equal(t, want.typeName, n.Type())
				assert.True(t, n.Editable())
				assert.False(t, n.Expandable())
				assert.Empty(t, n.Note())
			})
		}
	})

	t.Run("classifies the shapes that hold other rows", func(t *testing.T) {
		tests := map[string]struct {
			kind     protoschema.Kind
			typeName string
		}{
			"colour": {protoschema.KindEnum, testPackage + ".Colour"},
			"nested": {protoschema.KindMessage, testPackage + ".Nested"},
			"tags":   {protoschema.KindList, "repeated string"},
			"labels": {protoschema.KindMap, "map<string, string>"},
			"choice": {protoschema.KindOneof, "oneof"},
			"notes":  {protoschema.KindList, "repeated " + testPackage + ".Nested"},
		}

		for name, want := range tests {
			t.Run(name, func(t *testing.T) {
				n := row(t, form, name)
				assert.Equal(t, want.kind, n.Kind())
				assert.Equal(t, want.typeName, n.Type())
				assert.True(t, n.Expandable())
				assert.False(t, n.Editable(), "a row that holds other rows is not typed into")
			})
		}
	})

	// Neither a bool nor an enum is typed into: one is toggled, the other picked
	// from the list it expands to.
	t.Run("bools and enums are not typed into", func(t *testing.T) {
		flag := row(t, form, "flag")
		assert.Equal(t, protoschema.KindBool, flag.Kind())
		assert.Equal(t, "bool", flag.Type())
		assert.False(t, flag.Editable(), "a bool is toggled")
		assert.False(t, flag.Expandable())

		assert.False(t, row(t, form, "colour").Editable(), "an enum is picked from the list it expands to")
	})

	t.Run("a proto3 optional field is an ordinary field", func(t *testing.T) {
		n := row(t, form, "note")
		assert.Equal(t, protoschema.KindString, n.Kind())
		assert.True(t, n.Editable())
		assert.True(t, n.HasPresence())
		assert.True(t, n.AcceptsEmpty())
	})
}

func TestNewForm_NilDescriptor(t *testing.T) {
	form := protoschema.NewForm(nil)

	assert.Empty(t, form.Name)
	assert.Empty(t, form.Rows())
	assert.Nil(t, form.Root())

	_, err := form.Build()
	require.Error(t, err, "a form with no descriptor cannot build a request")
}

// A message that contains itself is legal protobuf and common enough — an
// expression tree, a linked list — that a form which expanded eagerly would not
// survive its first encounter with one.
func TestNewForm_RecursiveMessage(t *testing.T) {
	form := protoschema.NewForm(treeDescriptor(t))

	require.Equal(t, []string{"label", "children"}, paths(form))

	item := row(t, form, "children").AddItem()
	require.NotNil(t, item)
	assert.Equal(t, []string{"label", "children", "children[0]", "children[0].label", "children[0].children"}, paths(form))

	// And one level further, to show the recursion is bounded by keystrokes
	// rather than by the descriptor.
	row(t, form, "children[0].children").AddItem()
	assert.Contains(t, paths(form), "children[0].children[0].label")
}

func TestNode_NestedMessagesExpandOnDemand(t *testing.T) {
	form := testForm(t)

	require.NotContains(t, paths(form), "nested.note", "a collapsed message must not show its fields")

	nested := open(t, form, "nested")
	assert.Contains(t, paths(form), "nested.note")
	assert.Contains(t, paths(form), "nested.depth")

	// The panel indents by depth and folds by parent, so both have to survive
	// the trip down.
	note := row(t, form, "nested.note")
	assert.Equal(t, "note", note.Name())
	assert.Equal(t, 0, nested.Depth())
	assert.Equal(t, 1, note.Depth())
	assert.Same(t, nested, note.Parent())

	row(t, form, "nested").SetExpanded(false)
	assert.NotContains(t, paths(form), "nested.note")
}

func TestNode_EnumExpandsToItsValues(t *testing.T) {
	form := testForm(t)
	colour := row(t, form, "colour")

	assert.Equal(t, []protoschema.EnumValue{
		{Name: "COLOUR_UNSPECIFIED", Number: 0},
		{Name: "COLOUR_RED", Number: 1},
		{Name: "COLOUR_BLUE", Number: 2},
	}, colour.EnumValues())

	open(t, form, "colour")
	blue := row(t, form, "colour=COLOUR_BLUE")
	assert.True(t, blue.Radio())
	assert.False(t, blue.Selected())
	assert.Equal(t, "= 2", blue.Type(), "a choice shows the number it stands for")

	blue.Toggle()

	assert.True(t, blue.Selected())
	assert.Equal(t, "COLOUR_BLUE", colour.Value())
	assert.False(t, colour.Expanded(), "picking a value folds the list away again")
}

func TestNode_OneofPicksOneVariant(t *testing.T) {
	form := testForm(t)
	choice := open(t, form, "choice")

	assert.Equal(t, []string{"choice.by_name", "choice.by_id", "choice.by_nested"},
		paths(form)[16:19], "every variant hangs off the picker")
	assert.Empty(t, choice.Active())

	byID := row(t, form, "choice.by_id")
	require.True(t, byID.Radio())
	byID.Toggle()

	assert.Equal(t, "by_id", choice.Active())
	assert.True(t, byID.Selected())

	// Typing into another variant picks it: that is what typing there meant.
	row(t, form, "choice.by_name").SetValue("ada")
	assert.Equal(t, "by_name", choice.Active())
	assert.False(t, byID.Selected())
}

func TestNode_AddAndRemoveItems(t *testing.T) {
	form := testForm(t)
	tags := row(t, form, "tags")

	require.Zero(t, tags.Items())
	require.False(t, tags.CanRemove())
	require.True(t, tags.CanAdd())

	for _, value := range []string{"a", "b", "c"} {
		tags.AddItem().SetValue(value)
	}

	assert.Equal(t, 3, tags.Items())
	assert.True(t, tags.Expanded(), "adding an item opens the list it went into")
	assert.Equal(t, []string{"tags[0]", "tags[1]", "tags[2]"}, paths(form)[14:17])

	middle := row(t, form, "tags[1]")
	require.True(t, middle.CanRemove())
	require.True(t, middle.Remove())

	assert.Equal(t, 2, tags.Items())
	assert.Equal(t, []string{"a", "c"}, []string{
		row(t, form, "tags[0]").Value(),
		row(t, form, "tags[1]").Value(),
	}, "the items after the hole are renumbered, not left with gaps")

	assert.False(t, row(t, form, "text").Remove(), "an ordinary field is not an item")
}

// A map entry is protobuf's own key/value message, so it is edited with the
// same two rows as any other pair of fields.
func TestNode_MapEntriesHoldAKeyAndAValue(t *testing.T) {
	form := testForm(t)

	entry := row(t, form, "labels").AddItem()
	require.NotNil(t, entry)
	assert.True(t, entry.Expanded(), "a new entry opens, or its key is out of reach")

	assert.Contains(t, paths(form), "labels[0].key")
	assert.Contains(t, paths(form), "labels[0].value")
}

func TestNode_ToggleBool(t *testing.T) {
	form := testForm(t)
	flag := row(t, form, "flag")

	require.Empty(t, flag.Value())
	require.False(t, flag.Touched())

	flag.Toggle()
	assert.Equal(t, "true", flag.Value())
	assert.True(t, flag.Touched())

	flag.Toggle()
	assert.Equal(t, "false", flag.Value(), "toggling twice is not the same as never toggling")
	assert.True(t, flag.Touched())
}

// A nested message with nothing in it is indistinguishable from one nobody
// filled in, so saying "send it anyway" takes a keystroke of its own.
func TestNode_ToggleMessagePresence(t *testing.T) {
	form := testForm(t)
	nested := row(t, form, "nested")

	require.False(t, nested.Filled())

	nested.Toggle()
	assert.True(t, nested.Filled())

	nested.Toggle()
	assert.False(t, nested.Filled())
}

func TestNode_FilledFollowsWhatIsUnderneath(t *testing.T) {
	form := testForm(t)
	nested := row(t, form, "nested")

	require.False(t, nested.Filled())

	open(t, form, "nested")
	row(t, form, "nested.note").SetValue("hello")

	assert.True(t, nested.Filled(), "a message holding a value is filled in")
}
