package protoschema

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Load fills the form in from an existing message: the inverse of [Form.Build],
// and what makes the pair testable as a round trip.
//
// Only fields the message actually carries are loaded. A proto3 scalar sitting
// at its default is indistinguishable from an unset one on the wire, so leaving
// it out of the form is what keeps Build(Load(m)) equal to m rather than
// littering the request with explicit zeroes.
//
// v0.6 reloads a request out of history this way; v0.7 will seed one from a
// previous response.
func (f Form) Load(msg proto.Message) {
	if f.root == nil || msg == nil {
		return
	}
	f.root.loadFrom(msg.ProtoReflect())
}

// loadFrom fills n's children from the message they describe.
func (n *Node) loadFrom(m protoreflect.Message) {
	n.ensureChildren()

	for _, c := range n.children {
		if c.kind != KindOneof {
			c.loadValue(m)
			continue
		}
		// At most one variant is ever set, and finding it is what says which one
		// the picker should show as active.
		for _, variant := range c.children {
			if m.Has(variant.fd) {
				c.active = variant
				variant.loadValue(m)
			}
		}
	}
}

// loadValue fills one row from the field it stands for.
func (n *Node) loadValue(m protoreflect.Message) {
	if n.fd == nil || !m.Has(n.fd) {
		return
	}
	v := m.Get(n.fd)

	switch n.kind {
	case KindList:
		list := v.List()
		for i := range list.Len() {
			n.AddItem().loadElement(list.Get(i))
		}
	case KindMap:
		v.Map().Range(func(k protoreflect.MapKey, value protoreflect.Value) bool {
			n.AddItem().loadEntry(k, value)
			return true
		})
	case KindMessage:
		n.present = true
		n.loadFrom(v.Message())
	case KindChoice, KindUnsupported:
	default:
		n.SetValue(n.format(v))
	}
}

// loadElement fills one item of a repeated field.
func (n *Node) loadElement(v protoreflect.Value) {
	if n.kind == KindMessage {
		n.loadFrom(v.Message())
		return
	}
	n.SetValue(n.format(v))
}

// loadEntry fills one entry of a map, whose key and value are the two fields of
// protobuf's generated entry message.
func (n *Node) loadEntry(k protoreflect.MapKey, value protoreflect.Value) {
	n.ensureChildren()

	if key := n.childByName("key"); key != nil {
		key.SetValue(key.format(k.Value()))
	}
	if val := n.childByName("value"); val != nil {
		val.loadElement(value)
	}
}
