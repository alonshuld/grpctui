// Package protoschema turns protobuf descriptors into the generic field tree
// the UI renders, and turns a filled-in tree back into a wire message.
//
// It is the only package that knows about protobuf kinds. The UI walks a tree
// of [Node]s and never branches on a protoreflect.Kind — which is what let v0.3
// add nested messages, repeated fields, maps, oneof pickers and enum selects
// here rather than rewrite the form panel around each of them.
//
// Nothing here imports google.golang.org/grpc: a descriptor is a descriptor
// whether it arrived over reflection or from a .proto file (v0.9).
package protoschema

import (
	"fmt"
	"strconv"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Kind classifies a form row for the UI.
type Kind string

// The row kinds the UI knows how to render. The first group is editable leaves,
// the second is rows that hold other rows.
const (
	KindString Kind = "string"
	KindInt    Kind = "int"
	KindUint   Kind = "uint"
	KindFloat  Kind = "float"
	KindBool   Kind = "bool"
	KindBytes  Kind = "bytes"

	// KindEnum is a field whose value is picked from a list rather than typed:
	// expanding it reveals one [KindChoice] row per admissible value.
	KindEnum Kind = "enum"

	// KindChoice is one admissible value of the [KindEnum] row above it.
	KindChoice Kind = "choice"

	// KindMessage is a nested message: expanding it reveals its fields.
	KindMessage Kind = "message"

	// KindList is a repeated field, holding zero or more items the user adds and
	// removes. KindMap is the same thing for a map field, whose items are its
	// key/value entries.
	KindList Kind = "list"
	KindMap  Kind = "map"

	// KindOneof is a picker over the variants of a real oneof: expanding it
	// reveals the variants, of which at most one is ever sent.
	KindOneof Kind = "oneof"

	// KindUnsupported is a field grpctui has no editor for. There is no such
	// protobuf kind today; it is what an unrecognised one degrades to, so that a
	// descriptor from the future lists the field instead of dropping it.
	KindUnsupported Kind = "unsupported"
)

// EnumValue is one admissible value of an enum field.
type EnumValue struct {
	Name   string
	Number int32
}

// Resolver expands the variable references a form value may contain.
// internal/vars implements it; a form without one sends every value exactly as
// it was typed.
//
// It is an interface rather than an import so that this package keeps knowing
// about protobuf and nothing else. What a reference looks like is the
// resolver's business, which is why [Resolver.Refers] is part of the contract
// and not a pattern match here.
type Resolver interface {
	// Resolve returns text with every reference replaced. A reference nothing
	// binds is an error: a request that quietly loses a value fails somewhere
	// far from the mistake.
	Resolve(text string) (string, error)

	// Refers reports whether text contains a reference at all. A value that does
	// is not checked against its field's type until it has been resolved —
	// "{{count}}" is not a number yet, and saying so while it is being typed
	// would be a complaint about the wrong thing.
	Refers(text string) bool
}

// Form is the editable shape of one request message.
//
// A Form is a handle onto a mutable tree, so copying it shares the tree rather
// than duplicating it. That is deliberate: the bubbletea panel holding one is
// copied on every Update, and a form that forgot what had been typed into it
// each keystroke would be no form at all.
type Form struct {
	// Name is the fully-qualified message name, e.g. "demo.v1.HelloRequest".
	Name string

	root *Node
}

// Node is one row of a request form: a field, an item of a repeated field, or a
// variant to pick between.
//
// Children are materialised when a node is first expanded rather than when the
// form is built. A message that contains itself — a linked list, an expression
// tree — is perfectly legal protobuf, and building its form eagerly would not
// terminate.
type Node struct {
	name string
	kind Kind
	typ  string
	note string

	// fd is the field this row belongs to: its own for a field row, its parent's
	// for an item of a repeated field. It is nil only for the root and for
	// [KindChoice] rows, which describe a value rather than a field.
	fd protoreflect.FieldDescriptor

	// md is the message a [KindMessage] row's children come from.
	md protoreflect.MessageDescriptor

	// ev is the value a [KindChoice] row stands for.
	ev EnumValue

	parent   *Node
	children []*Node
	depth    int
	index    int

	// loaded records that children have been materialised, which for a message
	// row is not the same as having any.
	loaded   bool
	expanded bool

	// value is the text of a leaf row, and the chosen value's name for an enum.
	value string

	// touched records that the user has changed this row, which is how an
	// explicitly cleared field is told apart from one never visited. It is the
	// difference between sending an `optional string` as "" and leaving it unset;
	// see [Node.AcceptsEmpty].
	touched bool

	// present records that the user asked for an otherwise-empty message to be
	// sent, which is the only way to say so: a message with no fields set is
	// indistinguishable from one that was never filled in.
	present bool

	// active is the variant a [KindOneof] row will send, if any.
	active *Node

	// resolver expands the variable references in every value under this row.
	// Only the root ever carries one — see [Node.resolverFor] — so that
	// installing one is a single assignment and a tree cannot end up half
	// resolved.
	resolver Resolver
}

// NewForm derives the form for a request message. A nil descriptor yields the
// zero Form, whose [Form.Build] reports an error.
func NewForm(md protoreflect.MessageDescriptor) Form {
	if md == nil {
		return Form{}
	}

	root := &Node{
		name:  string(md.FullName()),
		kind:  KindMessage,
		typ:   string(md.FullName()),
		md:    md,
		depth: -1, // so that top-level fields sit at depth 0
	}
	root.ensureChildren()
	return Form{Name: string(md.FullName()), root: root}
}

// Rows returns the form's visible rows, depth-first, with the children of a
// collapsed row left out. It is what the panel renders and navigates.
func (f Form) Rows() []*Node {
	if f.root == nil {
		return nil
	}
	var rows []*Node
	f.root.appendRows(&rows)
	return rows
}

// Root returns the message row every other row hangs off. It is the form's
// handle on itself, and is nil for a form with no descriptor.
func (f Form) Root() *Node { return f.root }

// SetResolver installs the expansion applied to every value on its way to the
// wire. A nil resolver turns interpolation off, which is what a form built
// before the variables were known has.
//
// It is set on the tree rather than on the Form, so that it survives the Form
// being copied — which happens on every keystroke, since the panel holding one
// is a bubbletea value model — and so that a row can find it from anywhere in
// the tree without a second handle back to the form.
func (f Form) SetResolver(r Resolver) {
	if f.root != nil {
		f.root.resolver = r
	}
}

// resolverFor returns the expansion in force for this row.
func (n *Node) resolverFor() Resolver {
	for c := n; c != nil; c = c.parent {
		if c.resolver != nil {
			return c.resolver
		}
	}
	return nil
}

// Refers reports whether this row's value contains a variable reference, so
// that the panel can render it as the template it is rather than as a value.
func (n *Node) Refers() bool {
	r := n.resolverFor()
	return r != nil && r.Refers(n.value)
}

func (n *Node) appendRows(rows *[]*Node) {
	for _, c := range n.children {
		*rows = append(*rows, c)
		if c.expanded {
			c.appendRows(rows)
		}
	}
}

// Name is the field's protobuf name, "[2]" for an item of a repeated field, or
// the value's name for a choice.
func (n *Node) Name() string { return n.name }

// Kind says how the UI should render and validate the row.
func (n *Node) Kind() Kind { return n.kind }

// Type is the protobuf type, for display: "string", "repeated int64",
// "demo.v1.Colour", "= 2" for a choice.
func (n *Node) Type() string { return n.typ }

// Note explains a row the form has no editor for. It is empty for every other
// row.
func (n *Node) Note() string { return n.note }

// Depth is how far the row is indented: zero for a field of the request
// message itself.
func (n *Node) Depth() int { return n.depth }

// Path names the row uniquely within the form — "user.tags[1]", "choice.by_id"
// — which is how a build error finds its way back to the row that caused it.
func (n *Node) Path() string {
	if n.parent == nil {
		return ""
	}

	base := n.parent.Path()
	switch {
	case n.parent.isRepeated():
		return base + n.name // "[2]", which needs no separator
	case n.kind == KindChoice:
		return base + "=" + n.name
	case base == "":
		return n.name
	default:
		return base + "." + n.name
	}
}

// Editable reports whether the row's value is typed in. Bools are toggled and
// enums are picked from a list, so neither is editable in this sense.
func (n *Node) Editable() bool {
	switch n.kind {
	case KindString, KindInt, KindUint, KindFloat, KindBytes:
		return true
	default:
		return false
	}
}

// Expandable reports whether the row holds other rows.
func (n *Node) Expandable() bool {
	switch n.kind {
	case KindMessage, KindList, KindMap, KindOneof, KindEnum:
		return true
	default:
		return false
	}
}

// Expanded reports whether the row's children are showing.
func (n *Node) Expanded() bool { return n.expanded }

// SetExpanded shows or hides the row's children, materialising them the first
// time it is asked.
func (n *Node) SetExpanded(expanded bool) {
	if !n.Expandable() {
		return
	}
	if expanded {
		n.ensureChildren()
	}
	n.expanded = expanded
}

// Parent returns the row this one hangs off, or nil for a top-level field.
func (n *Node) Parent() *Node { return n.parent }

// Radio reports whether the row is one of a set the user picks between: a
// choice under an enum, or a variant under a oneof.
func (n *Node) Radio() bool { return n.kind == KindChoice || n.isVariant() }

// Selected reports whether a [Node.Radio] row is the one that will be sent.
func (n *Node) Selected() bool {
	switch {
	case n.kind == KindChoice:
		return n.parent != nil && n.parent.value == n.name
	case n.isVariant():
		return n.parent.active == n
	default:
		return false
	}
}

// Active returns the name of the variant a oneof row will send, if the user has
// picked one.
func (n *Node) Active() string {
	if n.kind != KindOneof || n.active == nil {
		return ""
	}
	return n.active.name
}

// Items reports how many items a repeated or map row holds.
func (n *Node) Items() int {
	if !n.isRepeated() {
		return 0
	}
	return len(n.children)
}

// Value is the text of a leaf row, or the chosen value's name for an enum.
func (n *Node) Value() string { return n.value }

// SetValue replaces a leaf row's value and marks it touched. Typing into a
// oneof variant also picks it, which is what the user meant by typing there.
func (n *Node) SetValue(value string) {
	n.value = value
	n.touched = true
	if n.isVariant() {
		n.parent.active = n
	}
}

// Touched reports whether the user has changed this row's value.
func (n *Node) Touched() bool { return n.touched }

// Filled reports whether anything under this row will be sent. It is what a
// collapsed message row shows instead of its contents.
func (n *Node) Filled() bool { return n.dirty() }

// Toggle flips whatever the row can change without typing: a bool, the picked
// variant of a oneof, the chosen value of an enum, or whether an empty nested
// message is sent at all.
func (n *Node) Toggle() {
	switch {
	case n.kind == KindChoice:
		n.parent.choose(n)

	case n.kind == KindBool:
		// A bool variant is picked and flipped by the same keystroke: activating
		// without flipping would make the first press look like it did nothing.
		if n.isVariant() {
			n.parent.active = n
		}
		n.SetValue(otherBool(n.value))

	case n.isVariant():
		n.parent.active = n

	case n.kind == KindMessage:
		n.present = !n.present
	}
}

// CanAdd reports whether [Node.AddItem] will do anything.
func (n *Node) CanAdd() bool { return n.isRepeated() }

// AddItem appends an item to a repeated or map row and returns it, expanding
// the row so the new item is visible. It returns nil for any other row.
func (n *Node) AddItem() *Node {
	if !n.isRepeated() {
		return nil
	}

	item := newItem(n, len(n.children))
	n.children = append(n.children, item)
	n.expanded = true
	return item
}

// CanRemove reports whether [Node.Remove] will do anything.
func (n *Node) CanRemove() bool { return n.parent != nil && n.parent.isRepeated() }

// Remove drops an item from the repeated or map row holding it, renumbering
// the items after it.
func (n *Node) Remove() bool {
	if !n.CanRemove() {
		return false
	}

	p := n.parent
	for i, c := range p.children {
		if c != n {
			continue
		}
		p.children = append(p.children[:i], p.children[i+1:]...)
		p.renumber()
		return true
	}
	return false
}

// HasPresence reports whether the field distinguishes "set to its zero value"
// from "not set" on the wire — proto3's `optional` keyword, a oneof member, or
// any proto2 optional field. For every other field the two are the same thing,
// which is why the form can leave a zero value out without changing what the
// server sees.
func (n *Node) HasPresence() bool { return n.fd != nil && n.fd.HasPresence() }

// AcceptsEmpty reports whether an empty value is a value rather than the
// absence of one, so that [Form.Build] sends it instead of skipping the field.
//
// That takes two things. The field must have presence, because without it an
// empty value and an unset one are indistinguishable anyway. And its type must
// have an empty form: a string or a bytes field can be explicitly empty, while
// an empty number box means the user typed nothing, since there is no such
// number to type.
//
// Bools and enums are absent from that list on purpose. Neither is typed into —
// a toggled-off bool holds an explicit "false" and a picked enum holds a value
// name — so neither ever reaches Build empty but touched.
func (n *Node) AcceptsEmpty() bool {
	return n.HasPresence() && (n.kind == KindString || n.kind == KindBytes)
}

// EnumValues lists the admissible values of an enum row, in declaration order.
func (n *Node) EnumValues() []EnumValue {
	if n.kind != KindEnum {
		return nil
	}
	out := make([]EnumValue, 0, len(n.children))
	for _, c := range n.children {
		out = append(out, c.ev)
	}
	return out
}

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

// choose records the value picked from an enum's list and folds the list away
// again, since the row now says what was chosen.
func (n *Node) choose(c *Node) {
	n.SetValue(c.name)
	n.expanded = false
}

// renumber relabels a repeated row's items after one is removed.
func (n *Node) renumber() {
	for i, c := range n.children {
		c.index = i
		c.name = itemName(i)
	}
}

// dirty reports whether the user has put anything into this row or anything
// under it — which for a nested message is the difference between sending it
// and leaving it out.
func (n *Node) dirty() bool {
	if n.touched || n.present || n.isActiveVariant() {
		return true
	}
	if n.kind == KindOneof && n.active != nil {
		return true
	}
	for _, c := range n.children {
		if c.dirty() {
			return true
		}
	}
	return false
}

func (n *Node) isRepeated() bool { return n.kind == KindList || n.kind == KindMap }

func (n *Node) isVariant() bool { return n.parent != nil && n.parent.kind == KindOneof }

func (n *Node) isActiveVariant() bool { return n.isVariant() && n.parent.active == n }

// childByName finds the row for a field of this message, for the callers that
// start from a descriptor rather than from a row. Variants of a oneof are not
// reachable this way, and neither caller needs them: a required field cannot be
// a oneof member, and a map entry has no oneofs.
func (n *Node) childByName(name protoreflect.Name) *Node {
	for _, c := range n.children {
		if c.name == string(name) {
			return c
		}
	}
	return nil
}

func itemName(i int) string { return "[" + strconv.Itoa(i) + "]" }

func otherBool(value string) string {
	if value == "true" {
		return "false"
	}
	return "true"
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
