# Chapter 3 — The Dependency Stack

Every direct dependency, why it is there, what was considered instead, and what
it costs. This chapter is the one most likely to be quizzed line-by-line.

## 3.1 The manifest

```go
module github.com/alonshuld/grpctui
go 1.25.0

require (
    github.com/bufbuild/protocompile        v0.14.1
    github.com/charmbracelet/bubbles        v1.0.0
    github.com/charmbracelet/bubbletea      v1.3.10
    github.com/charmbracelet/lipgloss       v1.1.0
    github.com/charmbracelet/x/ansi         v0.11.6
    github.com/charmbracelet/x/exp/teatest  v0.0.0-...
    github.com/jhump/protoreflect/v2        v2.0.0-beta.2
    github.com/muesli/termenv               v0.16.0
    github.com/stretchr/testify             v1.11.1
    go.uber.org/zap                         v1.28.0
    google.golang.org/grpc                  v1.83.0
    google.golang.org/protobuf              v1.36.11
    gopkg.in/yaml.v3                        v3.0.1
)
```

Thirteen direct dependencies for a 43k-line project including tests. That number
is itself a design statement: no CLI framework, no DI container, no HTTP client,
no config library, no test-mock generator.

## 3.2 The TUI stack: bubbletea + bubbles + lipgloss

### `charmbracelet/bubbletea` — the framework

**What it is:** an implementation of **The Elm Architecture** for Go. Your
program is a `Model` with three methods:

```go
Init()  tea.Cmd                      // what to start doing
Update(tea.Msg) (tea.Model, tea.Cmd) // fold a message into new state
View()  string                       // render state to a string
```

The runtime owns the terminal, reads input, runs `Cmd`s on their own goroutines,
feeds their returned `Msg`s back into `Update`, and diffs the `View` string
against the last frame.

**Why it was chosen over the alternatives:**

| Alternative | Why not |
|---|---|
| `rivo/tview` | Widget/callback model. State lives inside widgets, mutated from callbacks. Testing a screen means driving widgets; there is no "the frame is a pure function of the messages" property, which is exactly what golden-file testing needs. |
| `gdamore/tcell` (raw) | This is what tview and bubbletea are both built on. Using it directly means writing your own event loop, layout, and component model — months of work for no product value. |
| `gizak/termui` | Dashboard-oriented, weak input handling. |
| Writing a web UI (what `grpcui` did) | Fails design principle 2 outright. |

**The decisive property** is that `Update` is a pure fold and `View` is a pure
function. Combined with an injected clock (`WithClock`), *a frame is a function
of the messages that produced it* — which is what makes a golden file of a
streaming session reproducible. Without that, `internal/ui` would be
effectively untestable and the layering rule would have bought nothing.

**What it costs:** you must never block inside `Update`. Every RPC, file write
and dial becomes a `tea.Cmd`, which means every async result needs a message
type and a handler — `internal/ui/msgs.go` exists solely for that. It also means
you cannot thread a `context.Context` through `Update`, which is why the root
model holds one as a field with an explicit `//nolint:containedctx` and a
justification. Being able to name that as a *known cost with a documented
workaround* is the mark of having thought about it.

### `charmbracelet/bubbles` — the components

Used: `key` (bindings), `help` (the `?` bar), `spinner`, `viewport`,
`textinput`, `cursor`.

The interesting one is **`bubbles/key`**. It gives a `key.Binding` type carrying
both the keys and the help text, and `key.Matches(msg, binding)`. That single
type is what makes the whole keymap architecture work: because a binding carries
its own help text, the `?` bar reads from the same struct the panels match
against, and the two can never disagree.

**`bubbles/viewport`** ships its own default keymap (u/d/b/f, h/l, arrows). Left
as-is, scroll bindings would sit *outside* the single keymap: absent from `?`,
unreachable by remapping, and — for h/l — bound to something other than the
expand/collapse they mean everywhere else. So `viewportKeys()` maps grpctui's
keymap onto the viewport's, and bindings with no grpctui equivalent are left
**disabled** rather than silently keeping defaults. This is a good example of
"adopting a library without adopting its opinions".

### `charmbracelet/lipgloss` — the styling

CSS-like styles over strings: colours, borders, padding, alignment, and
`JoinHorizontal`/`JoinVertical` for composition.

Two facts about lipgloss shape the layout code and are worth knowing:

1. **It composes boxes; it does not overlay them.** There is no z-index. That is
   why every modal in grpctui *covers* the body rather than floating over it —
   `lipgloss.Place(width, height, Center, Center, content)`. A half-drawn panel
   behind a chooser reads as a rendering bug rather than as depth.
2. **`Width`/`Height` count padding inside but add the border outside.** Hence
   `innerSize()`, which subtracts border, padding, and the one-line title to get
   the real content area. And `MaxHeight(0)` means "no maximum", so a panel with
   no room for a body must be given an *empty string* rather than told to
   truncate — otherwise the body runs straight through the border below it.
3. **`lipgloss.AdaptiveColor{Light, Dark}`** is what the `auto` theme is built
   from, and `styles.fix()` collapses every adaptive colour onto one side to
   produce the `dark` and `light` themes from the same palette.

### `muesli/termenv` and `charmbracelet/x/ansi`

Direct dependencies for two narrow jobs: forcing a colour profile in tests
(`withColour(t)`, since under `go test` stdout is not a terminal and lipgloss
would render every style as bare text — a test asserting a theme reached the
frame would assert nothing), and ANSI-aware string truncation/width measurement
in `internal/ui/styles`. Measuring a styled string with `len()` counts escape
bytes; `ansi` counts cells.

## 3.3 The gRPC and protobuf stack

### `google.golang.org/grpc`

Non-negotiable — it is the reference implementation. What matters is *which
parts* are used, because they are not the parts a normal service uses:

| API | Used for |
|---|---|
| `grpc.NewClient` | Dialling. Note: **lazy**, does no I/O, which is why `Dial` takes no context. |
| `ClientConnInterface.Invoke` | Dynamic unary call by method path string |
| `ClientConn.NewStream` + `grpc.StreamDesc` | Dynamic streaming, shape built from the descriptor |
| `credentials`, `credentials/insecure` | TLS / mTLS / plaintext |
| `credentials.PerRPCCredentials` | Attaching auth to *every* RPC including reflection |
| `metadata.AppendToOutgoingContext` | Request headers |
| `stats.Handler` | The latency breakdown (§4.7) |
| `grpc.UnknownServiceHandler`, `ForceCodec`, `ForceServerCodec`, `WaitForHandlers` | The schema-free proxy (§8.1) |
| `test/bufconn` | Every transport test |

The four proxy options are the exotic ones. `grpc.UnknownServiceHandler` catches
every method the server has no registration for — which, for a proxy with no
schema, is all of them.

### `google.golang.org/protobuf` (the v2 runtime)

The heart of the tool. Four sub-packages carry the weight:

- **`types/dynamicpb`** — construct and decode a message from a
  `MessageDescriptor` at runtime, with no generated Go type. This is the single
  library capability the entire product rests on. Without it, calling an
  arbitrary method would require code generation at runtime.
- **`reflect/protoreflect`** — the descriptor and value model. `Node` in
  `protoschema` is essentially a UI-shaped projection of
  `protoreflect.FieldDescriptor`.
- **`encoding/protojson`** — protobuf's canonical JSON mapping. Field-name
  casing, 64-bit ints as strings, well-known types.
- **`encoding/protowire`** — low-level tag/varint/length-delimited parsing, used
  by `WireFields` to decode bytes **without a descriptor**.

Two non-obvious usages worth being able to explain:

**protojson's whitespace is deliberately randomised.** The amount of space after
a key varies between builds of the same program, to discourage anyone treating
its output as stable. So `MarshalJSON` marshals *compact* and re-indents with
`encoding/json`, which gives byte-stable output (golden-file testable) while
keeping protobuf's JSON *mapping* intact.

**`EmitUnpopulated` is asymmetric.** Responses render with unpopulated fields
(`MarshalJSON`); requests render without them (`MarshalRequest`). The reasoning:
a response's defaults are information — the server *chose* to leave them empty.
A request's defaults are every field of the form the user did not fill in, and a
stream log spending seven lines on them per sent message is a log nobody reads.

### `jhump/protoreflect/v2`

Used for exactly one thing: `grpcreflect.NewClientAuto(ctx, conn)` — the client
for the **gRPC Server Reflection protocol**.

**Why not implement it directly?** The protocol is a bidirectional stream
(`ServerReflectionInfo`) over which you ask for file descriptors by symbol, then
walk their transitive imports and link them into a `FileDescriptor` set. It has
two incompatible versions (`v1alpha` and `v1`) that servers implement
inconsistently — `NewClientAuto` is named for handling exactly that. Implementing
it yourself means reimplementing descriptor linking and version negotiation, and
getting the caching wrong.

**Why `jhump` specifically:** it is the library `grpcurl` itself is built on, so
it is battle-tested against the same universe of servers grpctui will meet. v2 is
the rewrite that sits on the protobuf v2 runtime and returns
`protoreflect.Descriptor` values directly, which is what lets the descriptors
flow straight into `protoschema` with no conversion.

**Its one API constraint, and how the code accommodates it:** `grpcreflect` binds
a context *and one reflection stream* per client, so a `grpcreflect.Client` is
constructed **per `ListServices` call** rather than cached on `grpcclient.Client`,
with `defer rc.Reset()` to tear the stream down.

**Risk to acknowledge:** it is pinned at `v2.0.0-beta.2`. A pre-release
dependency in a v1.0 product is a real, statable risk; the mitigation is that
the surface used is one constructor and two methods, so a swap back to v1 or a
hand-rolled client is bounded work.

### `bufbuild/protocompile`

The `.proto`-file fallback: compiles `.proto` source into the descriptors
reflection would otherwise have produced.

**Why not `protoc`?** It is an external binary. Requiring it violates design
principle 4 (single static binary, no runtime dependencies).

**Why not `jhump/protoparse`?** `protocompile` is its successor, written by the
same author under the Buf umbrella, with a proper `context`-bounded `Compile`
and a resolver model.

**The killer feature:** `protocompile.WithStandardImports` resolves
`google/protobuf/timestamp.proto` and friends out of the protobuf runtime this
binary already links. Requiring the user to find a copy of `descriptor.proto` to
call a method taking a `Timestamp` would be a poor first experience of the
fallback path.

**The subtle bug it forced a fix for:** protocompile resolves every file against
the import paths and nothing else. A file given as `./api/v1/greeter.proto` with
no `-I` would not be found — which is exactly what someone reaching for this flag
types first. So `plan()` makes each on-disk file contribute *its own directory*
as an import path and renames the file to the part below it. The renaming
matters beyond convenience: **a file compiled under two different names is two
files to the linker, and it refuses the resulting duplicate symbols.** Deriving
both halves from one resolution is what keeps `--proto a/b.proto --proto
a/c.proto` working when they import each other.

## 3.4 Logging: `go.uber.org/zap`

**Why zap over `log/slog`:** the project predates a settled slog handler
ecosystem and wanted a zero-allocation structured logger with a mature
`zaptest/observer` for asserting on log records in tests. slog would be a
defensible choice today; the honest interview answer is "zap for the observer
and the maturity, and I would consider slog now that the ecosystem has caught
up".

**The non-negotiable constraint:** a TUI owns the terminal, so *the logger must
never write to stdout or stderr*. Anything written while bubbletea holds the
screen corrupts the render and is very hard to diagnose. Therefore:

- File sink only, default `~/.local/state/grpctui/grpctui.log`.
- No log file configured ⇒ `zap.NewNop()`, **never** a console fallback.
- `zap.ErrorOutput(sink)` is set explicitly, because zap defaults its *internal*
  error output to stderr and would scribble over the UI the first time an
  encoder failed. This is the kind of detail that separates "I used zap" from "I
  understand zap".
- Default level is `error`, so a normal run writes almost nothing.
- `*zap.Logger` is passed explicitly through constructors. **No package-level
  global, no `zap.L()`** — for the same reason `render.Registry` is a value: a
  global a test can mutate makes failures non-reproducible.

## 3.5 Configuration and storage: `gopkg.in/yaml.v3`

**Why YAML and not JSON or TOML:** collections are meant to be *committed and
diffed*. A request body written as nested YAML keys produces a readable diff; the
same body as an escaped JSON string in a field produces a one-line diff nobody
can review.

**Why not `viper`:** viper brings automatic env binding, multiple format
support, remote KV, and file watching. None of those are wanted, and one is
actively harmful — viper is case-insensitive and silently lowercases keys, which
would break the strict-unknown-key behaviour that is central here. The manual
loader is ~100 lines and does exactly one thing.

**The two decisions that matter:**

`dec.KnownFields(true)` — **unknown keys are rejected.** Silently ignoring a
typo'd setting is the worst possible behaviour for a config file: your `tls:`
block does nothing and you cannot tell.

That strictness then created the problem `internal/format` exists to solve. A
file from a *later* grpctui is made of keys this one has never heard of, so the
strict decode fails on the key rather than the version — sending the reader
hunting for a typo they did not make. Hence `format.Check` is a **separate pass
over the same bytes, before the real decode**, reading the version out of a
`yaml.Node`. Chapter 7 covers this in full; it is one of the best "I anticipated
a second-order consequence" stories in the codebase.

## 3.6 Testing: testify, teatest, bufconn

- **`stretchr/testify`** — `require` for fatal preconditions, `assert` for
  independent checks. Deliberately *not* `testify/mock`: the fakes in this
  codebase are hand-written structs (`internal/runner/fake_test.go`,
  `internal/ui`'s stub client), because a generated mock asserts on calls where
  what is under test is behaviour.
- **`charmbracelet/x/exp/teatest`** — drives a `tea.Program` in a test and
  captures the final frame for golden-file comparison. This is what makes
  "review the diff, never regenerate blindly" a meaningful workflow.
- **`google.golang.org/grpc/test/bufconn`** — an in-memory `net.Listener`. Every
  transport test runs a **real gRPC server** over it. `internal/proxy`'s tests
  run *three* real stacks with no port between them: a health server, the proxy,
  and a client.

The bufconn choice is worth defending explicitly: *there is nothing useful to
test with a fake here — a proxy that forwards correctly to a mock tells you only
that the mock was called.*

## 3.7 What is deliberately absent

| Not used | Why |
|---|---|
| `spf13/cobra` | Four subcommands and one shared flag set. Cobra's value is a deep command tree with generated completion; here it would be 200 lines of framework to replace 100 lines of `flag`. The completions are hand-written per shell anyway, because they call back into a hidden `__complete` subcommand for names only grpctui knows. |
| `spf13/viper` | See §3.5. |
| A DI container (`wire`, `fx`, `dig`) | The dependency graph is a straight line assembled once in `main`. A container solves a problem this program does not have. |
| `golang.org/x/exp` generics helpers | Go 1.25's `slices`/`maps` stdlib covers everything used. |
| A mocking framework | See §3.6. |
| An ORM / database | State is three YAML files. |
| `sync/errgroup` | The only multi-goroutine site is the proxy's two pumps, which needs asymmetric error handling (the response half is authoritative), not `errgroup`'s first-error semantics. |

Being able to list what you *did not* add, with a reason each, is generally a
stronger signal than listing what you did.
