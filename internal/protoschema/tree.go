// tree.go builds the nodes from descriptors. Children are materialised when a
// node is first expanded, not when the form is built: a message that contains
// itself is legal protobuf, and an eager walk would not terminate.

package protoschema

import (
	"fmt"
	"strconv"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// ensureChildren materialises a node's children the first time they are needed.
func (n *Node) ensureChildren() {
	if n.loaded {
		return
	}
	n.loaded = true

	if n.md == nil {
		return
	}
	fields := n.md.Fields()
	seen := make(map[protoreflect.FullName]bool)
	for i := range fields.Len() {
		fd := fields.Get(i)

		od := realOneof(fd)
		if od == nil {
			n.children = append(n.children, newField(n, fd))
			continue
		}
		// Every member of a oneof hangs off one picker row, placed where the
		// first of them was declared.
		if !seen[od.FullName()] {
			seen[od.FullName()] = true
			n.children = append(n.children, newOneof(n, od))
		}
	}
}

// newField builds the row for one field of a message.
func newField(parent *Node, fd protoreflect.FieldDescriptor) *Node {
	n := &Node{
		name:   string(fd.Name()),
		typ:    typeName(fd),
		fd:     fd,
		parent: parent,
		depth:  parent.depth + 1,
	}

	switch {
	case fd.IsMap():
		n.kind = KindMap
		n.loaded = true
	case fd.IsList():
		n.kind = KindList
		n.loaded = true
	case isMessage(fd):
		n.kind = KindMessage
		n.md = fd.Message()
	default:
		n.setLeafKind(fd)
	}
	return n
}

// newItem builds the row for one item of a repeated or map field. A map entry
// is an ordinary message row over protobuf's generated entry type, so its key
// and value are edited by the same code as any other pair of fields.
func newItem(parent *Node, i int) *Node {
	fd := parent.fd
	n := &Node{
		name:   itemName(i),
		typ:    baseTypeName(fd),
		fd:     fd,
		parent: parent,
		depth:  parent.depth + 1,
		index:  i,
	}

	switch {
	case parent.kind == KindMap:
		// The entry type's name ("…​.LabelsEntry") says nothing the map row above
		// has not already said.
		n.kind, n.md, n.typ = KindMessage, fd.Message(), ""
	case isMessage(fd):
		n.kind, n.md = KindMessage, fd.Message()
	default:
		n.setLeafKind(fd)
	}

	// An item the user just added is opened for them: it was added to be filled
	// in, and a collapsed one would hide every field it has.
	if n.kind == KindMessage {
		n.SetExpanded(true)
	}
	return n
}

// newOneof builds the picker row for a real oneof, with one row per variant.
func newOneof(parent *Node, od protoreflect.OneofDescriptor) *Node {
	n := &Node{
		name:   string(od.Name()),
		kind:   KindOneof,
		typ:    "oneof",
		parent: parent,
		depth:  parent.depth + 1,
		loaded: true,
	}

	fields := od.Fields()
	for i := range fields.Len() {
		n.children = append(n.children, newField(n, fields.Get(i)))
	}
	return n
}

// setLeafKind classifies a row that holds a value rather than other rows,
// giving an enum its choices.
func (n *Node) setLeafKind(fd protoreflect.FieldDescriptor) {
	n.kind = kindOf(fd.Kind())
	n.loaded = true

	if n.kind == KindUnsupported {
		n.note = "grpctui has no editor for this field's type"
		return
	}
	if n.kind != KindEnum {
		return
	}

	values := fd.Enum().Values()
	for i := range values.Len() {
		v := values.Get(i)
		n.children = append(n.children, &Node{
			name:   string(v.Name()),
			kind:   KindChoice,
			typ:    "= " + strconv.FormatInt(int64(v.Number()), 10),
			ev:     EnumValue{Name: string(v.Name()), Number: int32(v.Number())},
			parent: n,
			depth:  n.depth + 1,
			loaded: true,
		})
	}
}

// realOneof returns the oneof a field is a variant of. proto3's `optional`
// keyword also produces a oneof — a synthetic one, wrapping a single field —
// and those are ordinary fields, not variants to pick between.
func realOneof(fd protoreflect.FieldDescriptor) protoreflect.OneofDescriptor {
	od := fd.ContainingOneof()
	if od == nil || od.IsSynthetic() {
		return nil
	}
	return od
}

func isMessage(fd protoreflect.FieldDescriptor) bool {
	return fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind
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
