package protoschema

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

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

// LoadValues puts a saved request's variable references back onto the rows they
// were typed into: the other half of [Form.Template], and what makes a recalled
// request a template again rather than the zeroes its body had to carry.
//
// It runs after [Form.Load], on the tree that message built, because the paths
// it carries only name rows the message has already created. A path naming a
// row this method does not have is reported rather than skipped — a collection
// is hand-edited and outlives schema changes, and silently dropping the value
// you thought you had set is the failure this whole feature exists to avoid.
func (f Form) LoadValues(values map[string]string) error {
	if f.root == nil || len(values) == 0 {
		return nil
	}

	// Sorted, so that a file with two bad paths in it reports them the same way
	// twice: Go's map iteration order is deliberately not stable.
	paths := make([]string, 0, len(values))
	for path := range values {
		paths = append(paths, path)
	}
	slices.Sort(paths)

	var errs []error
	for _, path := range paths {
		node, err := f.root.nodeAt(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		node.SetValue(values[path])
	}
	return errors.Join(errs...)
}

// nodeAt finds the row a [Node.Path] names, materialising children and growing
// repeated rows as it descends.
//
// Growing rather than refusing is deliberate: a hand-written collection may
// carry values for a list whose items its body does not spell out, and the
// alternative to adding them is refusing a file that says exactly what it
// means.
func (n *Node) nodeAt(path string) (*Node, error) {
	at := n
	for _, seg := range splitPath(path) {
		name, indices, err := parseSegment(seg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}

		at.ensureChildren()
		child := at.childNamed(name)
		if child == nil {
			return nil, fmt.Errorf("%s: this request has no field %q", path, name)
		}

		for _, i := range indices {
			if !child.isRepeated() {
				return nil, fmt.Errorf("%s: %s is not a repeated field", path, name)
			}
			for len(child.children) <= i {
				child.AddItem()
			}
			child = child.children[i]
		}
		at = child
	}

	if at == n {
		return nil, fmt.Errorf("%q names no field", path)
	}
	return at, nil
}

// splitPath cuts a path into its dotted segments, leaving the "[i]" suffixes
// attached: "user.tags[1].name" becomes ["user", "tags[1]", "name"].
func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	return strings.Split(path, ".")
}

// parseSegment reads one segment: a field name and however many list indices
// follow it.
func parseSegment(seg string) (name string, indices []int, err error) {
	name, rest, found := strings.Cut(seg, "[")
	if name == "" {
		return "", nil, fmt.Errorf("%q names no field", seg)
	}
	if !found {
		return name, nil, nil
	}

	for part := range strings.SplitSeq(rest, "[") {
		digits, ok := strings.CutSuffix(part, "]")
		if !ok {
			return "", nil, fmt.Errorf("%q is not an item index", seg)
		}
		i, convErr := strconv.Atoi(digits)
		if convErr != nil || i < 0 {
			return "", nil, fmt.Errorf("%q is not an item index", seg)
		}
		indices = append(indices, i)
	}
	return name, indices, nil
}

// childNamed finds a child row by name. Unlike [Node.childByName] it takes a
// plain string, because a path is text rather than a descriptor.
func (n *Node) childNamed(name string) *Node {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
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
