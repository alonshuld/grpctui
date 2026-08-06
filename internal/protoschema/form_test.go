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

func field(t *testing.T, form protoschema.Form, name string) protoschema.Field {
	t.Helper()
	for _, f := range form.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no field %q in form %s", name, form.Name)
	return protoschema.Field{}
}

func TestNewForm(t *testing.T) {
	form := testForm(t)

	assert.Equal(t, testMessage, form.Name)

	t.Run("keeps declaration order", func(t *testing.T) {
		names := make([]string, len(form.Fields))
		for i, f := range form.Fields {
			names[i] = f.Name
		}
		assert.Equal(t, []string{
			"text", "flag", "count", "total", "port", "size", "delta", "offset",
			"ratio", "weight", "payload", "colour", "nested", "tags", "labels",
			"by_name", "by_id", "note", "retry_count",
		}, names)
	})

	t.Run("classifies every scalar kind", func(t *testing.T) {
		tests := map[string]struct {
			kind     protoschema.Kind
			typeName string
		}{
			"text":    {protoschema.KindString, "string"},
			"flag":    {protoschema.KindBool, "bool"},
			"count":   {protoschema.KindInt, "int32"},
			"total":   {protoschema.KindInt, "int64"},
			"port":    {protoschema.KindUint, "uint32"},
			"size":    {protoschema.KindUint, "uint64"},
			"delta":   {protoschema.KindInt, "sint32"},
			"offset":  {protoschema.KindUint, "fixed64"},
			"ratio":   {protoschema.KindFloat, "float"},
			"weight":  {protoschema.KindFloat, "double"},
			"payload": {protoschema.KindBytes, "bytes"},
			"colour":  {protoschema.KindEnum, testPackage + ".Colour"},
		}

		for name, want := range tests {
			t.Run(name, func(t *testing.T) {
				f := field(t, form, name)
				assert.Equal(t, want.kind, f.Kind)
				assert.Equal(t, want.typeName, f.Type)
				assert.True(t, f.Editable())
				assert.Empty(t, f.Note)
			})
		}
	})

	t.Run("lists enum values in declaration order", func(t *testing.T) {
		assert.Equal(t, []protoschema.EnumValue{
			{Name: "COLOUR_UNSPECIFIED", Number: 0},
			{Name: "COLOUR_RED", Number: 1},
			{Name: "COLOUR_BLUE", Number: 2},
		}, field(t, form, "colour").Enum)
	})

	// The shapes v0.3 brings are still listed, so the form describes the whole
	// message rather than silently hiding the parts it cannot fill in.
	t.Run("reports what it cannot edit yet", func(t *testing.T) {
		tests := map[string]struct {
			typeName string
			note     string
		}{
			"nested":  {testPackage + ".Nested", "nested messages arrive in v0.3"},
			"tags":    {"repeated string", "repeated fields arrive in v0.3"},
			"labels":  {"map<string, string>", "map fields arrive in v0.3"},
			"by_name": {"string", "oneof fields arrive in v0.3"},
			"by_id":   {"int32", "oneof fields arrive in v0.3"},
		}

		for name, want := range tests {
			t.Run(name, func(t *testing.T) {
				f := field(t, form, name)
				assert.Equal(t, protoschema.KindUnsupported, f.Kind)
				assert.Equal(t, want.typeName, f.Type)
				assert.Equal(t, want.note, f.Note)
				assert.False(t, f.Editable())
			})
		}
	})

	// proto3's `optional` compiles to a oneof of one, which is an ordinary
	// editable field and must not be mistaken for a variant to pick between.
	t.Run("treats a proto3 optional field as editable", func(t *testing.T) {
		f := field(t, form, "note")
		assert.Equal(t, protoschema.KindString, f.Kind)
		assert.True(t, f.Editable())
	})

	t.Run("counts editable fields", func(t *testing.T) {
		assert.Equal(t, 14, form.EditableCount())
	})
}

func TestNewForm_NilDescriptor(t *testing.T) {
	form := protoschema.NewForm(nil)

	assert.Empty(t, form.Name)
	assert.Empty(t, form.Fields)
	assert.Zero(t, form.EditableCount())

	_, err := form.Build(map[string]string{"text": "hello"})
	require.Error(t, err, "a form with no descriptor cannot build a request")
}
