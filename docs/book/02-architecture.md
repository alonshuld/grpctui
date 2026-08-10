# Chapter 2 — Architecture: Four Layers and One Rule

## 2.1 The rule

```
transport  →  domain  →  UI  →  main
```

Strictly one-directional. Stated as two prohibitions that are easy to check and
easy to defend:

> **The UI must never import `google.golang.org/grpc`.**
> **The transport layer must never import `bubbletea`.**

This is the single most important structural rule in the repository, and the
reason to be able to state it crisply is that it explains almost every other
design decision downstream.

**What it buys, concretely:**

1. `internal/grpcclient` is testable against a real gRPC server over `bufconn`
   with no terminal at all. It sits at **98.9% coverage** — the highest-value
   tests in the repo — precisely because nothing in it needs a TTY.
2. `internal/ui` is testable with a hand-written fake client and no server at
   all. Golden-file tests of full frames are reproducible because the model's
   only inputs are messages.
3. A gRPC status can cross the boundary without the UI knowing what a
   `status.Status` is — see `grpcclient.CallStatus`, a flattened struct of
   `Code uint32`, `Name string`, `Message string`. The response panel renders a
   code distinctly from a transport failure without importing grpc.

**How it is enforced:** by discipline, not by a linter rule — which is the
weakest part of the arrangement, and I know it. It could be enforced
mechanically with `depguard` in `.golangci.yml`, and on a team it should be. The
reason it has held here is that the boundary types
(`CallStatus`, `Timing`, `proxy.Event`) are designed so that importing grpc from
the UI would not even be convenient.

## 2.2 The package map

```
cmd/grpctui/            main; subcommands, flags, config load, tea.Program bootstrap
internal/grpcclient/    dial, reflection, dynamic invoke (unary + streaming), timing
internal/proxy/         passive mode: schema-free gRPC proxy
internal/protofiles/    .proto source → descriptors (no-reflection fallback)
internal/protoschema/   descriptor → form tree; message build/decode; raw wire
internal/config/        ~/.config/grpctui/config.yaml; profiles, envs, themes, keys
internal/requests/      history and collections on disk
internal/vars/          variables, environments, {{name}} interpolation
internal/format/        the version: on every file, and what an unknown one means
internal/diff/          line diff
internal/export/        a request as a grpcurl command
internal/render/        response renderers (annotate a well-known type)
internal/runner/        headless replay of a collection, for CI
internal/logging/       the one *zap.Logger constructor
internal/version/       build version from debug.ReadBuildInfo, ldflags fallback
internal/ui/            bubbletea models, keymap, styles, themes
internal/ui/panels/     one file per panel and per modal
internal/ui/keys/       the single keymap + remapping + generated reference
internal/ui/styles/     palette, themes, JSON highlighting, text helpers
```

Two packages are *transport* even though only one is obviously so:
`internal/grpcclient` and `internal/proxy`. The test is not "does it talk to a
server" but "does it import `google.golang.org/grpc` and expose plain Go types".
Both do.

Three packages are **leaves** — they import nothing of grpctui's own:
`internal/vars`, `internal/diff`, `internal/format`, and `internal/protofiles`
(which imports protobuf and nothing of ours). Leaf-ness is load-bearing:
`internal/protoschema` and `internal/config` both depend on `internal/vars`, and
`internal/config` and `internal/requests` both depend on `internal/format`,
neither of which may import the other. Leaf packages are how you break a
would-be cycle without inventing an interface.

## 2.3 Where the seams are, and why each one is there

The architecture is defined by four deliberate seams. Each is an interface or a
plain-data boundary, and each was placed for a specific reason.

### Seam 1: `ui.Client` — the UI's view of the transport

```go
type Discoverer interface {
    Target() string
    ListServices(ctx context.Context, md grpcclient.Metadata) ([]grpcclient.Service, error)
}
type Invoker interface {
    InvokeUnary(ctx, method, req proto.Message, md) (*grpcclient.UnaryResponse, error)
}
type Streamer interface {
    InvokeStream(ctx, method, md) (grpcclient.Stream, error)
}
type Client interface { Discoverer; Invoker; Streamer }
```

**Why an interface here rather than `*grpcclient.Client`:** it is what lets the
UI tests run with no server. Note the Go idiom on display — *the interface is
declared by the consumer, not the producer*. `grpcclient` does not define
`Client`; `ui` does, describing exactly the slice it needs. `internal/runner`
declares its own, near-identical one for the same reason.

**Why three interfaces composed into one:** `Invoker` and `Streamer` are split
because they hand back different things — a unary call returns its answer, a
stream returns something to drive. Splitting them means a consumer that only
needs one is not forced to fake the other.

### Seam 2: `grpcclient.Stream` — an interface where everything else is a struct

Everything else in `internal/grpcclient` is a concrete struct returning plain
values. `Stream` is the exception, and the doc comment says why: *a stream
backed by a real connection cannot be conjured in a test without a server*. It
is the seam `internal/ui` fakes.

Its contract preserves grpc-go's rather than papering over it:
- `Recv` returns a **bare** `io.EOF` for a stream that finished cleanly (so
  `errors.Is` finds it) and keeps the gRPC status on anything else.
- `Send` returns `io.EOF` when the call has already failed, and the *reason*
  belongs to `Recv`. That is grpc-go's own contract. Preserving it means the UI
  can distinguish "the stream ended" from "sending failed for reason X", which a
  wrapper that invented its own error would have destroyed.

### Seam 3: `protoschema.Resolver` — an interface instead of an import

```go
type Resolver interface {
    Resolve(text string) (string, error)
    Refers(text string) bool
}
```

`internal/protoschema` knows about protobuf and nothing else. Variable expansion
happens at `Node.parse` — so `{{user_id}}` works in an `int64` field as well as
a `string` one, which a substitution done a layer up could not manage. But *what
a reference looks like* stays `internal/vars`'s business, which is why `Refers`
is part of the contract rather than a regex in this package.

This is a textbook dependency-inversion: the low-level package defines the
abstraction it needs, and the higher-level package (`vars`) satisfies it
incidentally. `vars.Set` implements `Resolver` without importing `protoschema`.

### Seam 4: `render.Renderer` — the extension point that is not a plugin

```go
type Renderer interface {
    Name() string
    Types() []string
    Render(msg protoreflect.Message, now time.Time) (string, bool)
}
```

The roadmap asked for "plugin/extension points for custom response renderers".
The implementation is deliberately **not a plugin process**, and the reasoning
is the whole argument: *loading foreign code into a terminal client
that holds bearer tokens is a bad trade, and Go has no stable plugin story on
the platforms grpctui ships to.* What is offered instead is the seam — an
interface, a registry that is a value, and a few lines to add one. The built-ins
are ordinary renderers registered the same way.

## 2.4 Design patterns in use, and why each one

### Functional options

Used for `grpcclient.Dial`, `ui.New`, `proxy.New`, `logging.New`.

```go
func Dial(target string, opts ...DialOption) (*Client, error)
```

**Why:** the constructors have 3–15 optional parameters that grew across ten
versions. A config struct would have been the alternative; options win here
because (a) the zero value of each option is meaningful and the caller never has
to spell it, (b) an option can validate (`WithLogger` ignores nil), and (c)
adding one is backward-compatible at the call site — which mattered, since
`ui.New` grew from two options to eleven.

**The cost, and the honest answer to "why not a struct":** options are harder to
introspect, cannot be constructed declaratively from config without a builder,
and add a small allocation per call. For a constructor called once per process
that is free. For a hot path it would not be.

### Value models (the bubbletea convention)

`Model`, every panel, `History`, `Collections`, `vars.Set`, `render.Registry`
are all **values, copied freely**. Every mutation allocates rather than writing
through a copy someone else holds:

```go
func (s Set) With(v Variable) Set { /* clones, returns a new Set */ }
func (h *History) Add(r Request)  { /* allocates a new slice */ }
```

**Why:** bubbletea's `Update(tea.Msg) (tea.Model, tea.Cmd)` returns a model by
value. A panel is copied on *every keystroke*. If `History.Add` appended in
place, a `tea.Cmd` still holding a pre-send copy would observe the send — a data
race in the general case and a correctness bug in every case.

**The one exception, and it is deliberate:** `protoschema.Form` is a handle onto
a *mutable* tree. Copying a `Form` shares the tree rather than duplicating it,
because the panel holding one is copied on every keystroke and "a form that
forgot what had been typed into it each keystroke would be no form at all".
The exception is more instructive than the rule: the rule exists to prevent a
race, and where there is no race there is no reason to pay for it.

### The command pattern (Elm architecture)

Blocking work never runs inside `Update`. RPCs, file writes and dials are all
`tea.Cmd`s — functions returning a `tea.Msg` — run on their own goroutine by the
runtime. Chapter 6 covers this in depth.

### Sequence-number generation counters

`callSeq` and `connSeq` on the root model. A result arriving after the user has
moved on carries a stale sequence number and is dropped instead of overwriting
newer state. This is the standard fix for the "stale async response" problem and
it appears three times: unary calls, streams, and dials.

### Strategy, via a registry that is a value

`render.Registry` maps a fully-qualified protobuf type name to a `Renderer`. The
zero registry annotates nothing. It is a value rather than a package-level
singleton *for the reason `internal/logging` refuses a global logger*: a
registry a test could mutate out from under another test makes failures
non-reproducible.

### Adapter

`ui.DialerFunc` adapts a function to the `Dialer` interface, exactly as
`http.HandlerFunc` does. `proxy.rawCodec` adapts "do nothing" to
`encoding.Codec`.

### Template method / one door

`Model.dispatch` is the single door every send goes through — from the form,
from a recalled history entry, from the browser. That is where the history entry
is written and where headers are resolved, *so there is one place recording
happens and one place a reference nobody bound refuses a call, rather than three
of each*. This is the strongest anti-duplication argument in the codebase.

## 2.5 Why not clean architecture / hexagonal / DDD?

Worth addressing head-on, because the layering invites the comparison.

The four layers *are* a ports-and-adapters arrangement — `ui.Client` is a port,
`grpcclient.Client` is the adapter, `Stream` is a port with a real and a fake
adapter. What is deliberately absent is the ceremony: no `domain/entity`
package, no repository interfaces for things that are files, no use-case
structs. grpctui has one bounded context and one process. The layering exists to
buy **testability** (a terminal-free transport, a server-free UI) and nothing
else; every abstraction that does not buy testability or break a cycle was left
out.

The concrete evidence that this was a choice and not an oversight:
`internal/requests` deliberately does **not** import protobuf. A body is stored
as `any` — the generic structure protobuf's JSON mapping decodes to — and
`internal/protoschema` owns the conversion in both directions. That keeps
storage a storage layer rather than a second schema layer. That is a DDD-shaped
instinct applied where it pays, and skipped where it would not.
