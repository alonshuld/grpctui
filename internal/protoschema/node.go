// node.go is the field tree's API: what the UI asks a node about itself, and
// the edits it makes to one. Nothing here reads a descriptor — a node knows
// its own kind by the time the UI sees it.

package protoschema

import (
	"strconv"

	"google.golang.org/protobuf/reflect/protoreflect"
)

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
