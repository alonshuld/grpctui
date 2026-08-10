# Chapter 5 — The Schema Layer: `internal/protoschema`

The only package that knows about protobuf *kinds*. The UI walks a tree of
`[]*Node` and never branches on a `protoreflect.Kind`. That single fact is what
let v0.3 add nested messages, repeated fields, maps, `oneof` pickers and enum
selects **here** rather than rewriting the form panel around each of them.

It imports no gRPC: a descriptor is a descriptor whether it arrived over
reflection or from a `.proto` file.

## 5.1 The form tree

```go
type Form struct {
    Name string   // "demo.v1.HelloRequest"
    root *Node
}

type Node struct {
    name, typ, note string
    kind            Kind
    fd  protoreflect.FieldDescriptor
    md  protoreflect.MessageDescriptor
    ev  EnumValue
    parent   *Node
    children []*Node
    depth, index int
    loaded, expanded bool
    value    string
    touched, present bool
    active   *Node     // for a oneof: the picked variant
    resolver Resolver  // only the root ever carries one
}
```

`Kind` is the UI-facing classification: `KindString`, `KindInt`, `KindUint`,
`KindFloat`, `KindBool`, `KindBytes`, `KindEnum`, `KindChoice`, `KindMessage`,
`KindList`, `KindMap`, `KindOneof`, `KindUnsupported`.

`KindUnsupported` has **no protobuf kind that maps to it today**. It is what an
unrecognised one degrades to, *so that a descriptor from the future lists the
field instead of dropping it*. Forward-compatibility as a default branch.

### Lazy children — and why it is a correctness requirement

```go
func (n *Node) SetExpanded(expanded bool) {
    if !n.Expandable() { return }
    if expanded { n.ensureChildren() }
    n.expanded = expanded
}
```

Children are materialised when a node is **first expanded**, not when the form
is built. This is not an optimisation:

> A message that contains itself — a linked list, an expression tree — is
> perfectly legal protobuf, and building its form eagerly would not terminate.

That is the single best "why lazy?" answer in the codebase. It is a correctness
requirement disguised as a performance decision.

### `Form` is a handle, not a value

```go
// A Form is a handle onto a mutable tree, so copying it shares the tree rather
// than duplicating it.
```

This is the deliberate exception to the value-model rule from Chapter 2. The
bubbletea panel holding a `Form` is copied on every `Update`, and *a form that
forgot what had been typed into it each keystroke would be no form at all*.
Similarly, `SetResolver` sets the resolver **on the tree, not on the Form**, so
it survives the copy and any row can find it from anywhere in the tree without a
second handle back.

```go
func (n *Node) resolverFor() Resolver {
    for c := n; c != nil; c = c.parent {
        if c.resolver != nil { return c.resolver }
    }
    return nil
}
```

Only the root ever carries one, *so that installing one is a single assignment
and a tree cannot end up half resolved*.

## 5.2 The three flavours of "empty"

This is the subtlest part of the package, and the part I rewrote most often. It
is where protobuf semantics and UI semantics meet, and they do not agree.

Protobuf's problem: for an ordinary proto3 scalar, *"set to the zero value"* and
*"not set"* are indistinguishable on the wire. So a form must decide what an
empty text box means. It uses three flags:

```go
touched bool  // the user changed this row
present bool  // the user asked for an otherwise-empty message to be sent
active *Node  // the oneof variant that will be sent
```

And two predicates:

```go
func (n *Node) HasPresence() bool {
    return n.fd != nil && n.fd.HasPresence()
}
// proto3 `optional`, a oneof member, or any proto2 optional field

func (n *Node) AcceptsEmpty() bool {
    return n.HasPresence() && (n.kind == KindString || n.kind == KindBytes)
}
```

`AcceptsEmpty` takes **two** things:

1. The field must have presence, because without it an empty value and an unset
   one are indistinguishable anyway.
2. Its type must *have* an empty form. A string or bytes field can be explicitly
   empty; an empty number box means the user typed nothing, since there is no
   such number to type.

Bools and enums are absent from that list on purpose: neither is typed into — a
toggled-off bool holds an explicit `"false"` and a picked enum holds a value
name — so neither ever reaches `Build` empty-but-touched.

`Build`'s rule, stated in the doc comment:

> A row the user never touched is left alone, and so is one holding an empty
> value. The exceptions are the three ways a form says "this, explicitly": a
> field with presence that was filled in and then cleared, an item added to a
> repeated field, and the picked variant of a oneof. Each is sent even when what
> it holds is empty, because each took a deliberate keystroke to say.

## 5.3 `oneof` — real versus synthetic

```go
func realOneof(fd protoreflect.FieldDescriptor) protoreflect.OneofDescriptor {
    od := fd.ContainingOneof()
    if od == nil || od.IsSynthetic() { return nil }
    return od
}
```

**proto3's `optional` keyword is implemented as a synthetic oneof wrapping a
single field.** If you did not filter those out, every `optional string name`
would render as a picker with one choice in it. This is a protobuf internals
detail that bites everyone who writes descriptor-walking code once, and the fix
is one predicate.

Every member of a real oneof hangs off one picker row, placed where the *first*
of them was declared.

## 5.4 Maps as messages

```go
case parent.kind == KindMap:
    n.kind, n.md, n.typ = KindMessage, fd.Message(), ""
```

A map entry is *an ordinary message row over protobuf's generated entry type*,
whose two fields are `key` and `value`. So the key and the value are edited by
the same code as any other pair of fields — no map-specific editor exists. The
entry type's own name (`…LabelsEntry`) is blanked because it says nothing the
map row above has not already said.

Writing them back:

```go
func (n *Node) setMap(msg protoreflect.Message, b *builder) bool {
    keyFd, valueFd := n.fd.MapKey(), n.fd.MapValue()
    mp := msg.Mutable(n.fd).Map()
    for _, entry := range n.children {
        built := dynamicpb.NewMessage(n.fd.Message())
        entry.build(built, b)
        value := mp.NewValue()
        if built.Has(valueFd) { value = built.Get(valueFd) }
        mp.Set(built.Get(keyFd).MapKey(), value)
    }
}
```

An entry whose value was left alone still belongs in the map — *the user added
it* — so the map's own empty value stands in.

## 5.5 Build, Template, Validate: one walk, three modes

```go
func (f Form) Build() (proto.Message, error)
func (f Form) Template() (proto.Message, map[string]string, error)
func (f Form) Validate() error   // == Build, discarding the message
```

All three go through `f.build(builder{...})`. `Validate` is *deliberately* the
same walk: "a second implementation would be a second set of rules to keep in
step."

The `builder` carries the mode:

```go
type builder struct {
    resolver Resolver
    template bool                // don't expand references
    errs     []error
    values   map[string]string   // path → reference text
}
```

### Why `Template` exists

A saved request must be recorded **as it was typed**, references and all. Two
reasons, and the second is the important one:

1. It is what makes a collection portable between environments.
2. **It is the only way to keep the credentials rule true here.** A token
   captured out of a login response and referred to as `{{token}}` is exactly
   what chaining requests produces, and writing its expansion into a collection
   meant to be committed would be writing the token down.

The mechanism:

```go
func (n *Node) templateValue(b *builder) protoreflect.Value {
    if n.kind == KindString {
        return protoreflect.ValueOfString(n.value)  // "{{id}}" is a fine string
    }
    b.record(n.Path(), n.value)   // "{{id}}" is not an int64
    return n.zero()
}
```

Most references need nothing beyond the message — a reference in a string field
is a perfectly good string and survives protobuf's JSON mapping untouched. The
rest cannot, so those fields are written at their **zero value** and the text is
returned alongside, keyed by `Node.Path()`, for `Form.LoadValues` to put back.

That pair — `Template` writes the map out, `LoadValues` puts it back — is the
round trip that makes a recalled request a *template* again rather than the
zeroes its body had to carry.

### Every error, not the first

```go
if len(b.errs) > 0 { return nil, nil, errors.Join(b.errs...) }
```

Wrapped in `*FieldError`:

```go
type FieldError struct {
    Path, Name, Value string
    Err error
}
```

`Path` is what the panel matches on to put the message back on the offending
row. *A form that fails one field at a time is miserable to fill in.*

And the error text is written for a human, not lifted from strconv:

```go
func (n *Node) numberError(err error, syntax string) error {
    if errors.Is(err, strconv.ErrRange) {
        return fmt.Errorf("out of range for %s", baseTypeName(n.fd))
    }
    return errors.New(syntax)  // "expected a whole number"
}
```

## 5.6 Where variable expansion happens, and why it is *here*

```go
func (n *Node) parse(raw string) (protoreflect.Value, error) {
    raw, err := n.resolve(raw)   // {{name}} → value, first
    if err != nil { return protoreflect.Value{}, err }
    switch n.kind { ... }
}
```

Resolution happens at `Node.parse` — *before* type parsing — so **every kind
gets it for free**: `{{user_id}}` is as useful in an `int64` field as in a
`string` one. A substitution done one layer up, on the rendered JSON, would only
have worked on text.

The complement is `Node.Validate`:

```go
func (n *Node) Validate() error {
    if !n.Editable() || n.value == "" || n.Refers() { return nil }
    ...
}
```

A value that *is* a reference is left alone mid-edit. What it will be worth is
not known until the request is built, and *a form recalled under an environment
that does not bind it is not a typo to complain about mid-edit*. The send says
so instead, where the answer is actually needed.

## 5.7 Load — the inverse, and what makes the round trip testable

```go
func (f Form) Load(msg proto.Message)
```

Only fields the message **actually carries** are loaded (`m.Has(fd)`). A proto3
scalar at its default is indistinguishable from an unset one on the wire, so
leaving it out of the form is what keeps `Build(Load(m)) == m` rather than
littering the request with explicit zeroes.

That property is fuzz-tested: `FuzzRoundTrip` in `internal/protoschema` fuzzes
build → load → build. The instruction is to run it after changing how a value is
parsed or formatted.

### `LoadValues` and path resolution

```go
func (f Form) LoadValues(values map[string]string) error
```

Paths are sorted before processing, *so that a file with two bad paths in it
reports them the same way twice — Go's map iteration order is deliberately not
stable.* (This "sort before iterating a map so errors are deterministic" pattern
appears four times in the codebase: here, in `keys.Apply`, in `styles.Spec.Theme`,
and in `export.references`.)

`nodeAt` **grows repeated rows as it descends**:

```go
for len(child.children) <= i { child.AddItem() }
```

*Growing rather than refusing is deliberate: a hand-written collection may carry
values for a list whose items its body does not spell out, and the alternative
to adding them is refusing a file that says exactly what it means.*

But a path naming a field the method does **not** have is reported, not skipped:
*a collection is hand-edited and outlives schema changes, and silently dropping
the value you thought you had set is the failure this whole feature exists to
avoid.*

## 5.8 The JSON boundary

```go
func Marshal(msg proto.Message) (string, Format, error)        // response
func MarshalJSON(msg proto.Message) (string, error)            // strict
func MarshalRequest(msg proto.Message) (string, Format, error) // no unpopulated
```

### The text-format fallback

`Marshal` prefers JSON and falls back to protobuf's **text format**. One thing
makes JSON impossible in practice: **a `google.protobuf.Any` whose payload type
this client has never seen.** protojson refuses to render such a message at all;
prototext degrades to printing the Any's type URL and raw bytes.

The call *succeeded* either way, so failing here would report a perfectly good
answer as a failed call. `Any` is common enough (`google.rpc.Status` details,
grpc-gateway) to be worth the fallback. The returned `Format` lets the panel say
"· protobuf text (unknown Any type)" rather than leaving the user wondering why
the body does not look like JSON.

### Stable indentation

```go
compact, _ := protojson.MarshalOptions{EmitUnpopulated: unpopulated}.Marshal(msg)
json.Indent(&out, compact, "", jsonIndent)
```

protojson **randomises its whitespace on purpose** to discourage anyone treating
its output as stable. Marshalling compact and re-indenting with `encoding/json`
gives byte-stable output — which is what makes golden files possible — while
keeping protobuf's JSON *mapping* (field-name casing, 64-bit ints as strings,
well-known types) intact.

### `EncodeBody` / `DecodeBody`

The other direction of the same job: a message to and from the generic `any` a
saved request carries.

```go
func EncodeBody(msg proto.Message) (any, error)
func DecodeBody(md protoreflect.MessageDescriptor, body any) (proto.Message, error)
```

They live here rather than in `internal/requests` **so that storage stays a
storage layer with no protobuf in it, and so that the JSON mapping is decided in
exactly one package.**

`EncodeBody` goes protojson → `encoding/json.Unmarshal` into `any`, rather than
using protojson's output directly, because *the point is a Go value, and this is
the only decoder that agrees with protojson about how 64-bit integers and
well-known types are written.*

`DecodeBody` has to `normalizeBody` first: **YAML decodes a mapping into
`map[string]any` when every key is a string and into `map[any]any` when one is
not**, and `encoding/json` can only marshal the first. Normalising here means a
hand-written collection with an integer map key gets protobuf's complaint about
the field rather than json's complaint about the type.

## 5.9 The raw wire view

Reading protobuf bytes **without a descriptor** is protobuf knowledge and
nothing else, so it lives here.

```go
func Wire(msg proto.Message) ([]byte, error)          // re-encode
func WireFields(raw []byte) ([]WireField, error)      // schema-free parse
func Hexdump(raw []byte, perLine int) string
func DecodeWire(desc, raw []byte) (proto.Message, error)
```

### `Wire` is a re-encoding, and says so

gRPC hands its stats handlers the **decoded message**, not the frame it came in.
There is nothing to capture. So `Wire` produces the same message under the same
encoding, with `Deterministic: true` (so two calls returning the same message
produce the same bytes and a diff of two raw views shows only what really
changed). The panel labels it *"as re-encoded"* rather than pretending
otherwise. A server that wrote its fields out of order, or used a non-minimal
varint, will differ here in layout while meaning exactly the same thing.

Saying so — in a doc comment *and* on screen — was the only option I could live
with. A view that silently claimed to be a packet capture would be worse than no
view at all, in a tool whose whole pitch is reading the wire.

### `WireFields` is deliberately schema-free

```go
number, typ, tagLen := protowire.ConsumeTag(raw)
value, valueLen := consumeValue(raw[tagLen:], typ)
```

> The point of a raw view is the case where the decoded one is wrong — an
> unknown field, a field the client's descriptor says is a string and the server
> wrote as an int — and consulting the descriptor to render it would hide
> exactly those cases.

A truncated or malformed buffer **stops the walk and returns what was read so
far along with the error**: *a body that goes wrong halfway is exactly the body
somebody opened the raw view to look at, and the fields before the damage are
the useful part.*

`renderBytes` shows a length-delimited payload as the thing it most likely is:
valid printable UTF-8 as a quoted string, everything else by size — *because
guessing at a nested message means re-parsing bytes that may not be one*. Groups
(proto2's deprecated nested encoding) are consumed and reported by size: rare
enough not to be worth a recursive walk, common enough in old schemas to be
worth not choking on.

`Hexdump` is written by hand rather than taken from `encoding/hex.Dump` because
*the column widths are ours*: `hex.Dump` hard-codes sixteen bytes a line and a
trailing newline, and the panel needs neither fixed — the response panel picks
16, 8 or 4 bytes per line based on available width.

### `DecodeWire` — the other direction

Exists for exactly one caller: **a request the passive proxy watched go past**,
which arrives as bytes because the proxy has no schema, and becomes a filled-in
form once the UI has found the method in what reflection discovered. That single
function is what makes passive mode more than a log.

## 5.10 `LookupJSON` — reading a value back out

```go
func LookupJSON(body, path string) (string, error)
```

What a chained request captures with. It reads from **the JSON the response panel
is already showing**, rather than from the message it came from, so *nothing has
to be kept alive between the call and the user deciding, several keystrokes
later, that they want the id out of it.*

The path syntax is **the same one `Node.Path()` writes** — `user.id`,
`items[0].name`. That is deliberate: *a user who has read one has learnt the
other.*

A path landing on an object or a list is an error, because a variable is text
and there is no useful text to make of a subtree. And numbers are formatted with
`strconv.FormatFloat(v, 'f', -1, 64)` because `encoding/json` decodes every
number to a `float64`, and `"1e+06"` is not something to paste into an int64
field.
