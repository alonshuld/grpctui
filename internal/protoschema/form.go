// Package protoschema turns protobuf descriptors into the generic field tree
// the UI renders, and turns filled-in form values back into a wire message.
//
// It is the only package that knows about protobuf kinds. The UI walks a
// []Field and never branches on a protoreflect.Kind — which is what makes v0.3
// (nested messages, repeated fields, oneof, enum pickers) additive here rather
// than a rewrite of the form panel. Fields grpctui cannot edit yet are reported
// as [KindUnsupported] rather than dropped, so the form stays an honest
// description of the message.
//
// Nothing here imports google.golang.org/grpc: a descriptor is a descriptor
// whether it arrived over reflection or from a .proto file (v0.9).
package protoschema

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Kind classifies a form field for the UI.
type Kind string

// The field kinds the UI knows how to render.
const (
	KindString Kind = "string"
	KindInt    Kind = "int"
	KindUint   Kind = "uint"
	KindFloat  Kind = "float"
	KindBool   Kind = "bool"
	KindBytes  Kind = "bytes"
	KindEnum   Kind = "enum"

	// KindUnsupported is a field v0.2 cannot fill in — a nested message, a
	// repeated field, a map, or a oneof member. It is still listed in the form,
	// carrying a [Field.Note] explaining when it arrives.
	KindUnsupported Kind = "unsupported"
)

// EnumValue is one admissible value of a [KindEnum] field.
type EnumValue struct {
	Name   string
	Number int32
}

// Field is one row of a request form.
type Field struct {
	// Name is the protobuf field name, e.g. "user_id".
	Name string

	// Kind says how the UI should render and validate the field.
	Kind Kind

	// Type is the protobuf type, for display: "string", "int32",
	// "demo.v1.Colour", "repeated int64".
	Type string

	// Enum lists the admissible values of a [KindEnum] field, in declaration
	// order. It is nil for every other kind.
	Enum []EnumValue

	// Note explains why a [KindUnsupported] field cannot be filled in. It is
	// empty for supported fields.
	Note string

	desc protoreflect.FieldDescriptor
}

// Editable reports whether the user can put a value into this field.
func (f Field) Editable() bool { return f.Kind != KindUnsupported }

// Form is the editable shape of one request message.
type Form struct {
	// Name is the fully-qualified message name, e.g. "demo.v1.HelloRequest".
	Name string

	// Fields are the message's fields in declaration order.
	Fields []Field

	desc protoreflect.MessageDescriptor
}

// NewForm derives the form for a request message. A nil descriptor yields the
// zero Form, whose [Form.Build] reports an error.
func NewForm(md protoreflect.MessageDescriptor) Form {
	if md == nil {
		return Form{}
	}

	fds := md.Fields()
	form := Form{
		Name:   string(md.FullName()),
		Fields: make([]Field, 0, fds.Len()),
		desc:   md,
	}
	for i := range fds.Len() {
		form.Fields = append(form.Fields, newField(fds.Get(i)))
	}
	return form
}

// EditableCount reports how many of the form's fields the user can fill in.
func (f Form) EditableCount() int {
	n := 0
	for _, field := range f.Fields {
		if field.Editable() {
			n++
		}
	}
	return n
}

func newField(fd protoreflect.FieldDescriptor) Field {
	field := Field{
		Name: string(fd.Name()),
		Type: typeName(fd),
		desc: fd,
	}

	if note, ok := unsupported(fd); ok {
		field.Kind = KindUnsupported
		field.Note = note
		return field
	}

	field.Kind = kindOf(fd.Kind())
	if field.Kind == KindEnum {
		field.Enum = enumValues(fd.Enum())
	}
	return field
}

// unsupported reports the shapes v0.2 has no editor for, along with the
// milestone that brings one.
func unsupported(fd protoreflect.FieldDescriptor) (string, bool) {
	switch {
	case inOneof(fd):
		return "oneof fields arrive in v0.3", true
	case fd.IsMap():
		return "map fields arrive in v0.3", true
	case fd.IsList():
		return "repeated fields arrive in v0.3", true
	case fd.Kind() == protoreflect.MessageKind, fd.Kind() == protoreflect.GroupKind:
		return "nested messages arrive in v0.3", true
	default:
		return "", false
	}
}

// inOneof reports whether fd is a member of a real oneof. proto3's `optional`
// keyword also produces a oneof — a synthetic one, wrapping a single field —
// and those are ordinary editable fields, not variants to pick between.
func inOneof(fd protoreflect.FieldDescriptor) bool {
	od := fd.ContainingOneof()
	return od != nil && !od.IsSynthetic()
}

func kindOf(k protoreflect.Kind) Kind {
	switch k {
	case protoreflect.StringKind:
		return KindString
	case protoreflect.BoolKind:
		return KindBool
	case protoreflect.BytesKind:
		return KindBytes
	case protoreflect.EnumKind:
		return KindEnum
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return KindInt
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return KindUint
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return KindFloat
	default:
		return KindUnsupported
	}
}

func enumValues(ed protoreflect.EnumDescriptor) []EnumValue {
	if ed == nil {
		return nil
	}
	vals := ed.Values()
	out := make([]EnumValue, 0, vals.Len())
	for i := range vals.Len() {
		v := vals.Get(i)
		out = append(out, EnumValue{Name: string(v.Name()), Number: int32(v.Number())})
	}
	return out
}

func typeName(fd protoreflect.FieldDescriptor) string {
	switch {
	case fd.IsMap():
		return fmt.Sprintf("map<%s, %s>", baseTypeName(fd.MapKey()), baseTypeName(fd.MapValue()))
	case fd.IsList():
		return "repeated " + baseTypeName(fd)
	default:
		return baseTypeName(fd)
	}
}

func baseTypeName(fd protoreflect.FieldDescriptor) string {
	switch fd.Kind() {
	case protoreflect.EnumKind:
		return string(fd.Enum().FullName())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return string(fd.Message().FullName())
	default:
		return fd.Kind().String()
	}
}
