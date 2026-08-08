package protoschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/vars"
)

// resolverFor builds the expansion a form is given, from name/value pairs.
func resolverFor(pairs ...string) protoschema.Resolver {
	set := vars.Set{}
	for i := 0; i+1 < len(pairs); i += 2 {
		set = set.With(vars.Variable{Name: pairs[i], Value: pairs[i+1]})
	}
	return set
}

// resolvedForm is a form over the test message with an expansion installed.
func resolvedForm(t *testing.T, pairs ...string) protoschema.Form {
	t.Helper()

	form := testForm(t)
	form.SetResolver(resolverFor(pairs...))
	return form
}

func TestForm_BuildResolvesReferences(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		typed string
		want  any
	}{
		{name: "string", path: "text", typed: "{{who}}", want: "alice"},
		{name: "string around a reference", path: "text", typed: "hi {{who}}!", want: "hi alice!"},
		{name: "int32", path: "count", typed: "{{n}}", want: int32(7)},
		{name: "int64", path: "total", typed: "{{n}}", want: int64(7)},
		{name: "uint32", path: "port", typed: "{{n}}", want: uint32(7)},
		{name: "float", path: "ratio", typed: "{{ratio}}", want: float32(1.5)},
		{name: "enum", path: "colour", typed: "{{colour}}", want: 1},
		{name: "int built from two references", path: "count", typed: "{{n}}{{n}}", want: int32(77)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form := resolvedForm(t,
				"who", "alice", "n", "7", "ratio", "1.5", "colour", "COLOUR_RED")
			row(t, form, tt.path).SetValue(tt.typed)

			msg, err := form.Build()
			require.NoError(t, err)
			assert.EqualValues(t, tt.want, value(t, msg, tt.path))
		})
	}
}

func TestForm_BuildResolvesEverywhere(t *testing.T) {
	form := resolvedForm(t, "who", "alice", "n", "7", "tenant", "acme")

	open(t, form, "nested")
	row(t, form, "nested.note").SetValue("{{who}}")

	tags := row(t, form, "tags")
	tags.AddItem().SetValue("{{who}}")

	labels := row(t, form, "labels")
	entry := labels.AddItem()
	entry.SetExpanded(true)
	row(t, form, "labels[0].key").SetValue("{{tenant}}")
	row(t, form, "labels[0].value").SetValue("{{n}}")

	choice := open(t, form, "choice")
	row(t, form, "choice.by_id").SetValue("{{n}}")
	require.Equal(t, "by_id", choice.Active())

	msg, err := form.Build()
	require.NoError(t, err)

	m := msg.ProtoReflect()
	fields := m.Descriptor().Fields()

	nested := m.Get(fields.ByName("nested")).Message()
	assert.Equal(t, "alice", nested.Get(nested.Descriptor().Fields().ByName("note")).String())

	list := m.Get(fields.ByName("tags")).List()
	require.Equal(t, 1, list.Len())
	assert.Equal(t, "alice", list.Get(0).String())

	labelMap := m.Get(fields.ByName("labels")).Map()
	assert.Equal(t, "7", labelMap.Get(protoreflect.ValueOfString("acme").MapKey()).String())

	assert.EqualValues(t, 7, value(t, msg, "by_id"))
}

// A reference nothing binds refuses the send and says so on the row that made
// it, rather than putting an empty value on the wire.
func TestForm_BuildReportsUnsetReferences(t *testing.T) {
	form := resolvedForm(t, "who", "alice")
	row(t, form, "text").SetValue("{{who}}")
	row(t, form, "count").SetValue("{{missing}}")

	_, err := form.Build()
	require.Error(t, err)

	var fieldErr *protoschema.FieldError
	require.ErrorAs(t, err, &fieldErr)
	assert.Equal(t, "count", fieldErr.Path)
	assert.Contains(t, err.Error(), "{{missing}}")

	var unset *vars.UnsetError
	assert.ErrorAs(t, err, &unset, "the reason should survive as far as the row")
}

// Without a resolver a reference is ordinary text, which is what a run with no
// environments configured must see.
func TestForm_BuildWithoutAResolverSendsTheTextAsTyped(t *testing.T) {
	form := testForm(t)
	row(t, form, "text").SetValue("{{who}}")

	msg, err := form.Build()
	require.NoError(t, err)
	assert.Equal(t, "{{who}}", value(t, msg, "text"))
}

// A reference is not checked against its field's type until it is resolved: the
// row is being typed into, or the environment that binds it is not switched on
// yet.
func TestNode_ValidateLeavesReferencesAlone(t *testing.T) {
	form := resolvedForm(t, "n", "7")

	count := row(t, form, "count")
	count.SetValue("{{whatever}}")
	require.NoError(t, count.Validate())

	count.SetValue("not a number")
	assert.Error(t, count.Validate())
}

func TestForm_Template(t *testing.T) {
	form := resolvedForm(t, "who", "alice", "n", "7")

	row(t, form, "text").SetValue("{{who}}")
	row(t, form, "count").SetValue("{{n}}")
	row(t, form, "total").SetValue("12")

	msg, values, err := form.Template()
	require.NoError(t, err)

	t.Run("a reference in a string field is the string", func(t *testing.T) {
		assert.Equal(t, "{{who}}", value(t, msg, "text"))
		assert.NotContains(t, values, "text", "a string needs no value of its own")
	})

	t.Run("a reference elsewhere is recorded beside a zero", func(t *testing.T) {
		assert.EqualValues(t, 0, value(t, msg, "count"))
		assert.Equal(t, map[string]string{"count": "{{n}}"}, values)
	})

	t.Run("an ordinary value is itself", func(t *testing.T) {
		assert.EqualValues(t, 12, value(t, msg, "total"))
	})

	t.Run("nothing is expanded", func(t *testing.T) {
		rendered, _, err := protoschema.MarshalRequest(msg)
		require.NoError(t, err)
		assert.NotContains(t, rendered, "alice")
	})
}

// Dropping a list item that held a reference would renumber the ones after it,
// and a path recorded against tags[2] would come back on the wrong row.
func TestForm_TemplateKeepsListIndices(t *testing.T) {
	form := protoschema.NewForm(listsDescriptor(t))
	form.SetResolver(resolverFor("n", "7"))

	ints := row(t, form, "ints")
	ints.AddItem().SetValue("1")
	ints.AddItem().SetValue("{{n}}")
	ints.AddItem().SetValue("3")

	msg, values, err := form.Template()
	require.NoError(t, err)

	list := msg.ProtoReflect().Get(
		msg.ProtoReflect().Descriptor().Fields().ByName("ints")).List()
	require.Equal(t, 3, list.Len())
	assert.EqualValues(t, 1, list.Get(0).Int())
	assert.EqualValues(t, 0, list.Get(1).Int(), "the reference stands at its zero")
	assert.EqualValues(t, 3, list.Get(2).Int())

	assert.Equal(t, map[string]string{"ints[1]": "{{n}}"}, values)
}

// The round trip a saved request makes: template out, message and values back
// in, and the form holds what was typed rather than what it was worth.
func TestForm_TemplateRoundTrip(t *testing.T) {
	sent := resolvedForm(t, "who", "alice", "n", "7", "tenant", "acme")

	row(t, sent, "text").SetValue("{{who}}")
	row(t, sent, "count").SetValue("{{n}}")
	row(t, sent, "total").SetValue("99")
	open(t, sent, "nested")
	row(t, sent, "nested.depth").SetValue("{{n}}")

	tags := row(t, sent, "tags")
	tags.AddItem().SetValue("first")
	tags.AddItem().SetValue("{{who}}")

	msg, values, err := sent.Template()
	require.NoError(t, err)

	// A different environment, to prove the recalled form holds the reference
	// rather than the value it had when it was saved.
	recalled := resolvedForm(t, "who", "bob", "n", "1")
	recalled.Load(msg)
	require.NoError(t, recalled.LoadValues(values))

	assert.Equal(t, "{{who}}", row(t, recalled, "text").Value())
	assert.Equal(t, "{{n}}", row(t, recalled, "count").Value())
	assert.Equal(t, "99", row(t, recalled, "total").Value())
	assert.Equal(t, "{{who}}", row(t, recalled, "tags[1]").Value())

	open(t, recalled, "nested")
	assert.Equal(t, "{{n}}", row(t, recalled, "nested.depth").Value())

	built, err := recalled.Build()
	require.NoError(t, err)
	assert.Equal(t, "bob", value(t, built, "text"))
	assert.EqualValues(t, 1, value(t, built, "count"))
}

func TestForm_LoadValues(t *testing.T) {
	t.Run("grows a repeated row the body did not spell out", func(t *testing.T) {
		form := resolvedForm(t, "who", "alice")
		require.NoError(t, form.LoadValues(map[string]string{"tags[2]": "{{who}}"}))

		tags := row(t, form, "tags")
		assert.Equal(t, 3, tags.Items())
		assert.Equal(t, "{{who}}", row(t, form, "tags[2]").Value())
	})

	t.Run("reaches into a nested message", func(t *testing.T) {
		form := resolvedForm(t)
		require.NoError(t, form.LoadValues(map[string]string{"nested.depth": "{{n}}"}))

		open(t, form, "nested")
		assert.Equal(t, "{{n}}", row(t, form, "nested.depth").Value())
	})

	t.Run("reaches into a map entry", func(t *testing.T) {
		form := resolvedForm(t)
		require.NoError(t, form.LoadValues(map[string]string{"labels[0].key": "{{tenant}}"}))

		open(t, form, "labels")
		row(t, form, "labels[0]").SetExpanded(true)
		assert.Equal(t, "{{tenant}}", row(t, form, "labels[0].key").Value())
	})

	t.Run("picks the oneof variant it lands on", func(t *testing.T) {
		form := resolvedForm(t)
		require.NoError(t, form.LoadValues(map[string]string{"choice.by_id": "{{n}}"}))

		assert.Equal(t, "by_id", open(t, form, "choice").Active())
	})

	t.Run("nothing to do", func(t *testing.T) {
		form := resolvedForm(t)
		assert.NoError(t, form.LoadValues(nil))
	})

	t.Run("reports a path this method does not have", func(t *testing.T) {
		form := resolvedForm(t)
		err := form.LoadValues(map[string]string{"gone": "{{x}}", "also_gone": "{{y}}"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), `no field "gone"`)
		assert.Contains(t, err.Error(), `no field "also_gone"`)
	})

	t.Run("reports a malformed path", func(t *testing.T) {
		for _, path := range []string{"", "tags[", "tags[x]", "tags[-1]", "text[0]", ".text"} {
			assert.Error(t, resolvedForm(t).LoadValues(map[string]string{path: "{{x}}"}), path)
		}
	})
}
