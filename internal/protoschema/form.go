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
