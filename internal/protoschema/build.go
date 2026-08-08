package protoschema

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// errRequired is what a proto2 `required` field that nobody filled in reports.
// proto3 has no such thing, so most requests can never produce it.
var errRequired = errors.New("this field is required")

// FieldError reports a form value that does not fit its field. Its message is
// shown to the user next to the offending row, so it is phrased for a human
// rather than lifted from strconv.
type FieldError struct {
	// Path locates the row within the form — "user.tags[1]" — and is what the
	// panel matches on to put the message back where it came from.
	Path string

	// Name is the protobuf field name, without the path leading to it.
	Name string

	// Value is the text the user typed.
	Value string

	Err error
}

func (e *FieldError) Error() string { return e.Path + ": " + e.Err.Error() }

func (e *FieldError) Unwrap() error { return e.Err }

// builder carries the state one walk over the form accumulates: what it could
// not use, and — for a template build — the references it kept out of the
// message.
type builder struct {
	// resolver expands the values on the way past. It is nil for a form that has
	// none, in which case every value is sent exactly as it was typed.
	resolver Resolver

	// template switches the walk from "what goes on the wire" to "what a record
	// keeps": a value carrying a reference is written as the reference rather
	// than as its expansion. See [Form.Template].
	template bool

	errs   []error
	values map[string]string
}

// refers reports whether a value is a reference this walk must not expand.
func (b *builder) refers(value string) bool {
	return b.template && b.resolver != nil && b.resolver.Refers(value)
}

// record keeps one row's reference text, for the fields whose protobuf type
// cannot hold it.
func (b *builder) record(path, value string) {
	if b.values == nil {
		b.values = make(map[string]string)
	}
	b.values[path] = value
}

func (b *builder) fail(err error) { b.errs = append(b.errs, err) }

// Build turns the filled-in form into a request message, expanding every
// variable reference on the way.
//
// A row the user never touched is left alone, and so is one holding an empty
// value — "" and "unset" are the same thing for an ordinary proto3 field, and
// treating them differently would put empty strings on the wire for every field
// the user skipped.
//
// The exceptions are the three ways a form says "this, explicitly": a field with
// presence that was filled in and then cleared, an item added to a repeated
// field, and the picked variant of a oneof. Each is sent even when what it holds
// is empty, because each took a deliberate keystroke to say.
//
// Every unusable value is reported, not just the first: a form that fails one
// field at a time is miserable to fill in. The returned error joins one
// [*FieldError] per bad row, which is also where a reference nothing binds
// lands — so the complaint arrives on the row that made it.
func (f Form) Build() (proto.Message, error) {
	msg, _, err := f.build(builder{resolver: f.resolverFor()})
	return msg, err
}

// Template turns the filled-in form into the request a record keeps: the same
// message, with every variable reference left as it was written instead of
// expanded.
//
// That is what makes a saved request portable, and it is the only way to keep
// grpctui's standing rule about credentials true here. A token captured out of
// a login response and referred to as {{token}} is exactly what chaining
// requests produces, and writing its expansion into a collection meant to be
// committed would be writing the token down.
//
// Most references need nothing beyond the message: a reference in a string
// field is a perfectly good string, and survives the round trip through
// protobuf's JSON mapping untouched. The rest cannot — "{{id}}" is not an int64
// — so those fields are written at their zero value and the text is returned
// alongside, keyed by [Node.Path], for [Form.LoadValues] to put back.
func (f Form) Template() (proto.Message, map[string]string, error) {
	return f.build(builder{resolver: f.resolverFor(), template: true})
}

func (f Form) build(b builder) (proto.Message, map[string]string, error) {
	if f.root == nil || f.root.md == nil {
		return nil, nil, errors.New("build request: form has no message descriptor")
	}

	msg := dynamicpb.NewMessage(f.root.md)
	f.root.build(msg, &b)
	b.errs = append(b.errs, f.root.missingRequired(msg)...)

	if len(b.errs) > 0 {
		return nil, nil, errors.Join(b.errs...)
	}
	return msg, b.values, nil
}

// resolverFor returns the expansion the form was given, if any.
func (f Form) resolverFor() Resolver {
	if f.root == nil {
		return nil
	}
	return f.root.resolver
}

// Validate reports what [Form.Build] would refuse to send, and nothing else. It
// is the same walk over the same tree — a second implementation would be a
// second set of rules to keep in step.
func (f Form) Validate() error {
	_, err := f.Build()
	return err
}

// Validate reports whether this row's own value is usable, for the panel to
// call as soon as an edit ends rather than waiting for a send.
//
// A value that is a variable reference is left alone: what it will be worth is
// not known until the request is built, and a form recalled under an
// environment that does not bind it is not a typo to complain about mid-edit.
// The send says so instead, where the answer is actually needed.
func (n *Node) Validate() error {
	if !n.Editable() || n.value == "" || n.Refers() {
		return nil
	}
	if _, err := n.parse(n.value); err != nil {
		return err
	}
	return nil
}

// build fills msg from n's children, reporting whether anything was set.
func (n *Node) build(msg protoreflect.Message, b *builder) bool {
	set := false
	for _, c := range n.children {
		if c.setInto(msg, b) {
			set = true
		}
	}
	return set
}

// setInto writes one row into the message holding it.
func (n *Node) setInto(msg protoreflect.Message, b *builder) bool {
	switch n.kind {
	case KindMessage:
		return n.setMessage(msg, b)
	case KindList:
		return n.setList(msg, b)
	case KindMap:
		return n.setMap(msg, b)
	case KindOneof:
		if n.active == nil {
			return false
		}
		return n.active.setInto(msg, b)
	case KindChoice, KindUnsupported:
		return false
	default:
		return n.setScalar(msg, b)
	}
}

// setScalar writes a leaf row — a string, a number, a bool, an enum.
func (n *Node) setScalar(msg protoreflect.Message, b *builder) bool {
	if n.value == "" {
		switch {
		case n.isActiveVariant():
			// Picking a variant is itself the value: an active `string by_name`
			// with nothing typed into it is a request for an empty by_name, not
			// for no oneof at all.
			msg.Set(n.fd, n.fd.Default())
			return true
		case !n.touched || !n.AcceptsEmpty():
			return false
		}
	}

	if b.refers(n.value) {
		msg.Set(n.fd, n.templateValue(b))
		return true
	}

	v, err := n.parse(n.value)
	if err != nil {
		b.fail(n.fieldError(err))
		return false
	}
	msg.Set(n.fd, v)
	return true
}

// templateValue is what a reference is written as in a template build: itself,
// where the field's type can hold it, and the type's zero value plus a recorded
// path where it cannot.
func (n *Node) templateValue(b *builder) protoreflect.Value {
	if n.kind == KindString {
		return protoreflect.ValueOfString(n.value)
	}
	b.record(n.Path(), n.value)
	return n.zero()
}

// setMessage writes a nested message, if there is anything in it to write.
//
// Required fields are checked only for a message that is actually sent: a
// proto2 message the user never opened is not an incomplete request, it is an
// absent optional field.
func (n *Node) setMessage(msg protoreflect.Message, b *builder) bool {
	if !n.dirty() {
		return false
	}

	sub := msg.NewField(n.fd)
	n.build(sub.Message(), b)
	b.errs = append(b.errs, n.missingRequired(sub.Message())...)
	msg.Set(n.fd, sub)
	return true
}

// setList writes a repeated field. An item that is on screen is sent, empty or
// not: adding one is how the user says a value belongs there.
func (n *Node) setList(msg protoreflect.Message, b *builder) bool {
	if len(n.children) == 0 {
		return false
	}

	list := msg.Mutable(n.fd).List()
	for _, item := range n.children {
		if item.kind == KindMessage {
			elem := list.NewElement()
			item.build(elem.Message(), b)
			b.errs = append(b.errs, item.missingRequired(elem.Message())...)
			list.Append(elem)
			continue
		}

		// An item is appended whatever it holds, reference or not: dropping one
		// would renumber every item after it, and a path recorded against
		// "tags[2]" would come back on the wrong row.
		if b.refers(item.value) {
			list.Append(item.templateValue(b))
			continue
		}

		v, err := item.parseOrZero()
		if err != nil {
			b.fail(item.fieldError(err))
			continue
		}
		list.Append(v)
	}
	return true
}

// setMap writes a map field. Every entry is a message of protobuf's generated
// entry type, so it is built like any other nested message and then split into
// the key and value the map wants.
func (n *Node) setMap(msg protoreflect.Message, b *builder) bool {
	if len(n.children) == 0 {
		return false
	}

	keyFd, valueFd := n.fd.MapKey(), n.fd.MapValue()
	mp := msg.Mutable(n.fd).Map()

	for _, entry := range n.children {
		built := dynamicpb.NewMessage(n.fd.Message())
		entry.build(built, b)

		// An entry whose value was left alone still belongs in the map — the user
		// added it — so the map's own empty value stands in for it.
		value := mp.NewValue()
		if built.Has(valueFd) {
			value = built.Get(valueFd)
		}
		mp.Set(built.Get(keyFd).MapKey(), value)
	}
	return true
}

// missingRequired reports the proto2 `required` fields of a message that is
// being sent but has not been filled in.
func (n *Node) missingRequired(msg protoreflect.Message) []error {
	if n.md == nil {
		return nil
	}

	var errs []error
	fields := n.md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.Cardinality() != protoreflect.Required || msg.Has(fd) {
			continue
		}

		path := n.Path() + "." + string(fd.Name())
		if child := n.childByName(fd.Name()); child != nil {
			path = child.Path()
		}
		errs = append(errs, &FieldError{Path: path, Name: string(fd.Name()), Err: errRequired})
	}
	return errs
}

func (n *Node) fieldError(err error) error {
	return &FieldError{Path: n.Path(), Name: n.name, Value: n.value, Err: err}
}

// parseOrZero parses an item of a repeated field, reading an empty box as the
// type's zero value rather than as an absence — an item that is there is there.
func (n *Node) parseOrZero() (protoreflect.Value, error) {
	if n.value == "" {
		return n.zero(), nil
	}
	return n.parse(n.value)
}

// zero is the value an empty item of this kind stands for.
func (n *Node) zero() protoreflect.Value {
	switch n.kind {
	case KindString:
		return protoreflect.ValueOfString("")
	case KindBool:
		return protoreflect.ValueOfBool(false)
	case KindBytes:
		return protoreflect.ValueOfBytes(nil)
	case KindEnum:
		return protoreflect.ValueOfEnum(0)
	case KindFloat:
		if n.bits() == 32 {
			return protoreflect.ValueOfFloat32(0)
		}
		return protoreflect.ValueOfFloat64(0)
	case KindUint:
		if n.bits() == 32 {
			return protoreflect.ValueOfUint32(0)
		}
		return protoreflect.ValueOfUint64(0)
	default:
		if n.bits() == 32 {
			return protoreflect.ValueOfInt32(0)
		}
		return protoreflect.ValueOfInt64(0)
	}
}

// parse converts one row's text into a protobuf value, expanding its variable
// references first.
//
// Resolution happens here rather than a layer up so that every kind gets it for
// free: {{user_id}} is as useful in an int64 field as in a string one, and a
// substitution that only worked on text would be the more surprising rule.
func (n *Node) parse(raw string) (protoreflect.Value, error) {
	raw, err := n.resolve(raw)
	if err != nil {
		return protoreflect.Value{}, err
	}

	switch n.kind {
	case KindString:
		return protoreflect.ValueOfString(raw), nil
	case KindBool:
		return parseBool(raw)
	case KindInt:
		return n.parseInt(raw)
	case KindUint:
		return n.parseUint(raw)
	case KindFloat:
		return n.parseFloat(raw)
	case KindBytes:
		return parseBytes(raw)
	case KindEnum:
		return n.parseEnum(raw)
	default:
		return protoreflect.Value{}, errors.New("this field cannot be filled in")
	}
}

// resolve expands the variable references in one row's text. A form with no
// resolver leaves it exactly as typed, which is what a run with no environments
// configured gets.
func (n *Node) resolve(raw string) (string, error) {
	r := n.resolverFor()
	if r == nil {
		return raw, nil
	}
	return r.Resolve(raw)
}

// format renders a protobuf value as the text [Node.parse] accepts back.
func (n *Node) format(v protoreflect.Value) string {
	switch n.kind {
	case KindBytes:
		return base64.StdEncoding.EncodeToString(v.Bytes())
	case KindEnum:
		if ev := n.fd.Enum().Values().ByNumber(v.Enum()); ev != nil {
			return string(ev.Name())
		}
		return strconv.FormatInt(int64(v.Enum()), 10)
	case KindFloat:
		return strconv.FormatFloat(v.Float(), 'g', -1, n.bits())
	default:
		// String, bool and both integer families all round-trip through the
		// protobuf value's own formatting.
		return v.String()
	}
}

func parseBool(raw string) (protoreflect.Value, error) {
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return protoreflect.Value{}, errors.New("expected true or false")
	}
	return protoreflect.ValueOfBool(b), nil
}

func parseBytes(raw string) (protoreflect.Value, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return protoreflect.Value{}, errors.New("expected base64")
	}
	return protoreflect.ValueOfBytes(b), nil
}

func (n *Node) parseInt(raw string) (protoreflect.Value, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, n.bits())
	if err != nil {
		return protoreflect.Value{}, n.numberError(err, "expected a whole number")
	}
	if n.bits() == 32 {
		//nolint:gosec // ParseInt with bitSize 32 already bounds v to int32.
		return protoreflect.ValueOfInt32(int32(v)), nil
	}
	return protoreflect.ValueOfInt64(v), nil
}

func (n *Node) parseUint(raw string) (protoreflect.Value, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, n.bits())
	if err != nil {
		return protoreflect.Value{}, n.numberError(err, "expected a whole number that is not negative")
	}
	if n.bits() == 32 {
		//nolint:gosec // ParseUint with bitSize 32 already bounds v to uint32.
		return protoreflect.ValueOfUint32(uint32(v)), nil
	}
	return protoreflect.ValueOfUint64(v), nil
}

func (n *Node) parseFloat(raw string) (protoreflect.Value, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), n.bits())
	if err != nil {
		return protoreflect.Value{}, n.numberError(err, "expected a number")
	}
	if n.bits() == 32 {
		return protoreflect.ValueOfFloat32(float32(v)), nil
	}
	return protoreflect.ValueOfFloat64(v), nil
}

// parseEnum accepts a value name ("STATUS_SERVING") or a number ("1"). The
// picker only ever produces a name, but a message loaded back into the form
// (v0.6's history) can carry a number this client's descriptor predates —
// proto3 enums are open, and dropping such a value would silently change the
// request.
func (n *Node) parseEnum(raw string) (protoreflect.Value, error) {
	name := strings.TrimSpace(raw)
	if ev := n.fd.Enum().Values().ByName(protoreflect.Name(name)); ev != nil {
		return protoreflect.ValueOfEnum(ev.Number()), nil
	}
	if v, err := strconv.ParseInt(name, 10, 32); err == nil {
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(v)), nil
	}

	values := n.EnumValues()
	names := make([]string, 0, len(values))
	for _, ev := range values {
		names = append(names, ev.Name)
	}
	return protoreflect.Value{}, fmt.Errorf("expected one of %s", strings.Join(names, ", "))
}

// numberError turns a strconv failure into something worth showing a user,
// keeping the range case distinct from the syntax case.
func (n *Node) numberError(err error, syntax string) error {
	if errors.Is(err, strconv.ErrRange) {
		return fmt.Errorf("out of range for %s", baseTypeName(n.fd))
	}
	return errors.New(syntax)
}

// bits reports the width of a numeric field, which decides both how strconv
// parses it and which protoreflect.ValueOf constructor stores it.
func (n *Node) bits() int {
	switch n.fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.FloatKind:
		return 32
	default:
		return 64
	}
}
