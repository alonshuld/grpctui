# Chapter 14 — The Questions I Get Asked

Everything in the previous thirteen chapters, restated as answers. Some of these
are questions people have actually put to me about grpctui; the rest are ones I
had to answer for myself before the code could be written. They are grouped by
subject, and each answer is compressed to roughly what I would say out loud.

---

## The first five minutes

**Q. Give me the elevator pitch.**

A terminal UI for gRPC. Point it at `host:port`; it uses gRPC server reflection
to discover the whole API, generates a request form from the protobuf
descriptor, invokes the method dynamically with no generated stubs, and renders
the answer. It handles all four call shapes, TLS/mTLS, headers, saved
collections with `{{variable}}` interpolation across environments, a raw-bytes
view, response diffing, latency breakdown, a passive MITM proxy, and a headless
CI mode. Go, bubbletea, `dynamicpb`, single static binary.

**Q. Walk me through the architecture.**

Four strictly one-directional layers: transport → domain → UI → main. One rule:
the UI never imports `google.golang.org/grpc`, and the transport never imports
bubbletea. That buys two things — the transport is testable against a real gRPC
server over bufconn with no terminal (it's at 98.9%), and the UI is testable
with a fake client and no server. Everything crossing the boundary is a plain Go
type; a gRPC status becomes a flattened `CallStatus{Code, Name, Message}` so the
response panel can render a code distinctly from a transport failure without
importing grpc.

**Q. How do you call a method you have no generated code for?**

Three pieces. Reflection gives you a `protoreflect.MethodDescriptor`.
`dynamicpb.NewMessage(descriptor)` gives you a `proto.Message` whose shape is
decided at runtime. And `ClientConnInterface.Invoke(ctx, "/pkg.Service/Method",
req, resp)` is grpc-go's generic call primitive — the same one generated stubs
call underneath. Generated code adds type safety and nothing else. For streaming
it's `NewStream` with a `grpc.StreamDesc` whose two booleans come straight off
the descriptor, which is how one code path covers all four shapes.

**Q. Why bubbletea and not tview?**

bubbletea is the Elm architecture: `Update` is a pure fold over messages, `View`
is a pure function of state. With an injected clock, a frame becomes a function
of the messages that produced it — which is what makes golden-file testing of a
whole streaming session work. tview is widget-and-callback: state lives inside
widgets, mutated from callbacks, and there is no equivalent property. Given the
layering rule was bought specifically to make the UI testable, tview would have
thrown away the payoff.

**Q. What's the hardest thing you solved?**

Streaming in an event loop that must never block. `Recv` on an open stream
blocks until the server speaks, which might be an hour. A loop can't live in
`Update` and a goroutine writing into the model would be a race. The answer is a
chain of commands: each receive is a `tea.Cmd` that blocks on its own goroutine,
returns a message, and the handler issues the *next* receive. `Update` is never
blocked. Sends go through a queue with one in flight, because gRPC allows a
single sender and message order is part of what a client-streaming call means.
And each command captures values from the model before it's returned, never
reads it from inside — that capture discipline is why there are no races.

**Q. Tell me about a bug you found and fixed.**

The request browser's frame was quadratic in the history. Its box width asked
every row for two column widths, and each of those was a pass over every entry —
so drawing a full frame took 35ms, on every keystroke. The fix was `layout()`:
measure the frame once per `View` and hand the numbers down. The two fixed
columns are now measured when the *entries* change, since they don't depend on
the clock or the filter. And it's pinned by `testing.AllocsPerRun` at two sizes
rather than by a duration — four times the entries costing sixteen times the
allocations is unambiguous, where a stopwatch on a shared CI runner is a coin
toss.

**Q. What would you do differently?**

Three things. First, the layering rule is enforced by review; I'd add a
`depguard` rule to `.golangci.yml` so it's mechanical. Second,
`jhump/protoreflect/v2` is pinned at a beta in a v1.0 product — the surface used
is one constructor and two methods, so it's bounded, but it's a real risk I'd
want off the critical path. Third, `internal/ui/model.go` is 2,500 lines. It's
cohesive and the `Update` switch is a dispatch table, but the streaming
state machine — `sendQueue`, `sending`, `closeAfterQueue`, `stream`,
`streamStart` — is really its own object and would be easier to test in
isolation.

---

## Design decisions

**Q. Why does `Dial` not take a context?**

Because `grpc.NewClient` connects lazily and does no I/O. Accepting a context
would imply a bound that doesn't exist. A target that's down surfaces as
`codes.Unavailable` on the first call. The exception is TLS material — a missing
CA file or a mismatched cert/key pair fails at dial, because nothing about it
improves by waiting.

**Q. Why is `Stream` an interface when everything else in that package is a
struct?**

It's the one thing the UI can't fake otherwise — a stream backed by a real
connection can't be conjured in a test without a server. Everything else in
`grpcclient` returns plain values you can construct directly.

**Q. Why does a unary call have a timeout and a stream doesn't?**

Watching a server-streaming method for an hour is the feature, not a hung call.
A timeout would mean the tool deciding when a watch has gone on long enough.
`esc` ends one and nothing else does. In the headless runner it's the opposite —
there the timeout is the *only* thing that can end a watch, and a smoke test
that waits forever is a broken build rather than a failed one.

**Q. Why are form children materialised lazily?**

Not performance — correctness. A message that contains itself is legal protobuf:
a linked list, an expression tree. An eager walk would not terminate.

**Q. `{{variable}}` expansion happens where, and why there?**

At `Node.parse`, in the schema layer, *before* type parsing. That way every kind
gets it for free: `{{user_id}}` works in an int64 field as well as a string one.
A substitution done one layer up, on the rendered JSON, would only have worked
on text. The expansion arrives as a `Resolver` interface rather than an import,
so `protoschema` keeps knowing about protobuf and nothing else — what a
reference *looks like* stays `internal/vars`'s business, which is why `Refers`
is part of the contract instead of a regex.

**Q. Why two variable syntaxes?**

`${VAR}` is the process environment, resolved once at load — that's how a secret
reaches the config file without being written in it. `{{name}}` is a grpctui
variable resolved at send, from a set the user switches and adds to while
running. One syntax for both would be a file that couldn't say which it meant —
and they have opposite security properties: `${VAR}` is expanded before anything
is recorded, `{{name}}` is expanded at the wire and a saved request keeps the
reference.

**Q. Why is a proxy event dropped rather than queued?**

The one thing a passive proxy may never do is slow down the traffic it's
watching. Blocking on a full channel would make the proxy's latency a function
of how fast the UI redraws, which changes the timings the user is trying to
read. So: 256-event buffer, non-blocking send, an atomic counter of what was
dropped, and the panel says how many were lost. Lossy, deliberately, and honest
about it.

**Q. Why does the raw wire view not use the descriptor?**

The point of a raw view is the case where the *decoded* one is wrong — an
unknown field, or a field the client's descriptor says is a string and the
server wrote as an int. Consulting the descriptor to render it would hide
exactly those cases.

**Q. Why do renderers annotate instead of rewrite?**

The pitch is reading the wire, not a prettified account of it. A panel that
replaced `"2026-08-08T09:14:02Z"` with "3 minutes ago" would be unusable for the
case that matters most — the server whose timestamps are wrong. So the value
stays exactly what the server sent and the gloss goes beside it, dimmed, behind
a marker.

**Q. Why is the renderer extension point not a real plugin system?**

Loading foreign code into a client that holds bearer tokens is a bad trade, and
Go has no stable plugin story on the platforms this ships to. What's offered is
the seam — an interface, a registry that's a value, a few lines to add one. The
built-ins are ordinary renderers registered the same way.

**Q. Why is `Profile` in the transport package and not in config?**

Every field of it is a transport concern — target, security, auth, metadata.
`config` maps YAML onto it and the UI switches between them. Putting it in
config would force the transport layer to depend on config to dial, inverting
the dependency.

**Q. Why is the diff its own package?**

It's a leaf — nothing of grpctui's is imported by it, and it knows nothing about
protobuf, gRPC or the terminal. That's what makes it exhaustively testable for
free. It's also narrower than a general diff library on purpose: the output is a
flat slice of lines with an op attached, ready to be styled and handed to a
viewport, not a patch anyone could apply.

---

## Go questions

**Q. Functional options: why, and what do they cost?**

Constructors with 3–15 optional parameters that grew across ten versions.
Options win over a config struct because the zero value of each is meaningful
and never has to be spelled, an option can validate, and adding one is
backward-compatible at the call site — which mattered, since `ui.New` went from
two options to eleven. The cost is that they're harder to introspect and can't
be built declaratively from config without a builder. For a constructor called
once per process that's free.

**Q. You have a `context.Context` in a struct. Defend it.**

`Update(tea.Msg) (tea.Model, tea.Cmd)` takes a message and nothing else — there
is no parameter to thread a context through. Holding it on the model is what
lets a `tea.Cmd` inherit cancellation from the process. It carries an explicit
`//nolint:containedctx` with that reason, and `nolintlint` is configured with
`require-explanation`, so a bare suppression would itself fail the lint.

**Q. Why three mutexes on one stream?**

grpc-go's contract is one sender and one receiver at a time, and they may run
concurrently. `sendMu` serialises senders, `recvMu` serialises receivers, `mu`
guards the counters both halves touch. A single mutex would make `Send` and
`Recv` mutually exclusive, which deadlocks a bidi call: the server may not
answer until it's heard several messages and the client may not send more until
it's heard one.

**Q. How does one stats handler serve every concurrent call?**

The handler is a dial option — gRPC doesn't take them per call. So each call
hangs its own collector off its own context, under a private `struct{}` key, and
the handler reads it back with `timingFrom(ctx)`. No registry, no map, no
locking on a shared structure. A call that doesn't ask — reflection, say — gets
a nil and is ignored at the cost of a nil check.

**Q. Why does `Timing` have a `Measured bool` instead of testing `Total != 0`?**

Because a coarse clock — Windows' is about 15ms — genuinely returns a zero total
for a fast call over a loopback transport. Reading that as "no breakdown
available" would hide the timings of exactly the quickest responses. That one
surfaced from the Windows leg of the CI matrix.

**Q. Why are all these types values rather than pointers?**

bubbletea returns a model by value, and a panel is copied on every keystroke. If
`History.Add` appended in place, a `tea.Cmd` still holding a pre-send copy would
observe the send. So every mutation allocates. The one deliberate exception is
`protoschema.Form`, which is a handle onto a mutable tree — a form that forgot
what had been typed into it each keystroke would be no form at all.

**Q. Why is `applyTheme` a written-out list of eleven calls instead of a loop?**

The panels are concrete types held **by value**. A `[]interface{ SetStyles(...) }`
would hold copies, and the copies are what would change colour. Painful Go
gotcha, real consequence. The mitigation is a settings file that gathers the
setters, plus a test.

**Q. Where do you use `errors.Join`, and why?**

Nine places, always the same shape: collect, continue, join. A form that fails
one field at a time is miserable to fill in; a call refused for two bad headers
should say so twice; a config file missing three environment variables should say
so once rather than over three runs. It's always paired with sorting when
iterating a map, so two runs report the same problems in the same order — Go's
map iteration order is deliberately not stable.

**Q. Show me a place you deliberately ignore an error.**

`wire, _ := protoschema.Wire(resp.Message)`. A message that won't re-encode
simply has no raw view; the call itself succeeded, and reporting it as failed
over a rendering would be absurd. The general policy: an error in the thing the
user asked for is reported, an error in the bookkeeping around it is logged.
`errcheck` with `check-type-assertions` and `nilerr` are both on, so every one of
those is a reviewed choice.

**Q. Why `errors.As` on the `GRPCStatus()` interface instead of
`status.FromError`?**

`status.FromError` on a *wrapped* status rewrites the message to the full error
string. That's right for a log line and wrong for a panel that already says which
method was called — what belongs on screen is the server's own message, not
`"invoke pkg.Svc.M: rpc error: code = NotFound desc = ..."`.

**Q. What's the double `%w` for?**

`fmt.Errorf("%s: %w: %w", op, ErrReflectionUnavailable, err)` — Go 1.20+. Wraps
both a sentinel and the original status, so `errors.Is(err,
ErrReflectionUnavailable)` and `status.FromError(err)` both work on the same
value. The UI uses the first to show a dedicated screen with a specific fix.

**Q. Where did you use `reflect`, and why is it acceptable there?**

Two places. `keys.Apply` derives config-file action names from `KeyMap`'s field
names, so a new binding is remappable without a second table to remember — the
cost is that renaming a field renames a config key, which a test pins.
And `sameRequest` compares two decoded bodies with `reflect.DeepEqual`, because a
body is a tree of maps and slices a hand-edited collection can put anything into
— including a mapping with non-string keys, which YAML allows and `==` would
panic on — and it runs once per send.

---

## Protocol and protobuf

**Q. What is gRPC server reflection, mechanically?**

A service the server exposes at `grpc.reflection.v1.ServerReflection`. You open a
bidi stream, ask for file descriptors by symbol name, and the server returns
serialised `FileDescriptorProto`s which you link, following transitive imports.
There are two incompatible versions, `v1alpha` and `v1`, that servers implement
inconsistently — `grpcreflect.NewClientAuto` exists to negotiate exactly that.
I use `jhump/protoreflect/v2` because reimplementing descriptor linking and
version negotiation is not the interesting part of this problem, and because
it's what grpcurl is built on, so it's tested against the same universe of
servers.

**Q. What's a synthetic oneof and why do you care?**

proto3's `optional` keyword is implemented as a oneof wrapping a single field.
If you don't filter those out with `od.IsSynthetic()`, every `optional string
name` renders as a picker with one choice in it. It's a protobuf-internals
detail that bites everyone who writes descriptor-walking code once.

**Q. How do you tell "set to empty" from "not set"?**

For an ordinary proto3 scalar you can't — they're identical on the wire, which
is why the form leaves an untouched empty field out entirely. Where you *can* is
`fd.HasPresence()`: proto3 `optional`, a oneof member, or any proto2 optional
field. And even then the field's type has to *have* an empty form — a string or
bytes can be explicitly empty; an empty number box means the user typed nothing,
because there's no such number to type. So `AcceptsEmpty()` is presence AND
(string OR bytes). Bools and enums are excluded because neither is typed into.

**Q. How does a map field work in the form?**

A map entry is an ordinary message over protobuf's generated entry type, whose
two fields are `key` and `value`. So they're edited by exactly the same code as
any other pair of fields — there's no map-specific editor at all. Writing them
back builds each entry as a `dynamicpb` message and then splits it into the key
and value the map wants.

**Q. Why can't you always render a response as JSON?**

A `google.protobuf.Any` whose payload type the client has never seen. protojson
refuses to render such a message at all; prototext degrades to printing the
type URL and raw bytes. The call succeeded either way, so failing there would
report a perfectly good answer as a failed call — and `Any` is common enough
(`google.rpc.Status` details, grpc-gateway) to be worth the fallback. The
returned `Format` lets the panel say "protobuf text (unknown Any type)" instead
of leaving the user wondering.

**Q. Why re-indent protojson's output?**

protojson randomises its whitespace on purpose — the space after a key varies
between builds of the same program, to stop anyone treating it as stable. So I
marshal compact and re-indent with `encoding/json`, which is byte-stable (hence
golden files) while keeping protobuf's JSON *mapping* — field-name casing,
64-bit ints as strings, well-known types — intact.

**Q. Why `EmitUnpopulated` for responses but not requests?**

A response's defaults are information — the server *chose* to leave them empty.
A request's defaults are every field of the form the user didn't fill in, and a
stream log spending seven lines on them per sent message is a log nobody reads.

**Q. How does the proxy forward a method it has never seen?**

A raw codec on both halves that hands bytes straight through,
`grpc.UnknownServiceHandler` to catch every method, and each call pumped as a
bidi stream in two directions. Unary, server-streaming, client-streaming and
bidi all reduce to the same shape once nobody is counting messages. The codec is
installed per-connection via `ForceCodec`, never via `encoding.RegisterCodec` —
registering it would replace the real protobuf codec process-wide, and grpctui's
own client is in the same process.

**Q. Why does the raw view say "as re-encoded"?**

Because gRPC hands its stats handlers the decoded message, not the frame. There
is nothing to capture. What's shown is the same message under the same encoding,
marshalled deterministically. A server that wrote fields out of order or used a
non-minimal varint will differ in layout while meaning the same thing — so the
panel says so rather than pretending to be a packet capture.

---

## Security, testing and process

**Q. Walk me through your credentials handling.**

One rule: a token, a password or a header value never reaches a surface that
outlives the call — only its kind and header names do. What makes it interesting
is that every version added a surface and each had to re-establish it. v0.6
added files, so `Request.Headers` is `[]string` — a type that *cannot* hold a
value. v0.7 added variable expansion, so a request body could now contain a
token; that's why `Form.Template` exists, keeping the `{{reference}}` and never
its expansion. v0.8 added an exported grpcurl command, the most outward-facing
surface there is, so that exports placeholders. v0.9 added a CI log, the most
*durable* surface there is, so that withholds even header names. Enforcement is
mostly structural: `Auth` has a `Describe()` and no `String()`, so `%v` can't
leak it; `Metadata.Keys()` exists so "record a call" and "make a call" use
different accessors. Where structure wasn't possible there's a test — every test
that writes a record asserts no value reached the file.

**Q. You send bearer tokens over plaintext. Isn't that wrong?**

gRPC's default is to refuse, and for a library that's right. This is a debugging
tool aimed at local servers and `kubectl port-forward`, where plaintext plus a
token is the normal shape of a staging environment — refusing would mean
refusing the most common setup there is. So `RequireTransportSecurity` returns
false, and the compensating controls are a warning in the log at dial time and a
status bar that says the connection is in the clear. Same shape of argument for
`InsecureSkipVerify`: `Security.Mode()` returns "TLS (unverified)" and it's on
screen continuously, so you can't forget it's on.

**Q. Why doesn't a custom CA merge the system trust store?**

Naming a CA is how a user says "this server is signed by this authority and no
other". Quietly accepting every public root as well would turn a pinned
connection into an ordinary one.

**Q. How do you test a TUI?**

Two techniques. `teatest` drives a real `tea.Program` and captures the final
frame for golden-file comparison — which only works because the frame is
deterministic: injected clock, byte-stable JSON, sorted discovery, and a forced
colour profile (under `go test` stdout isn't a terminal, so lipgloss would
render every style as bare text and a theme assertion would assert nothing). And
direct `Update` calls with synthetic `tea.KeyMsg`s for state transitions.
Streaming needs a third thing: an open stream's receive doesn't return until the
server speaks, so a test driving it synchronously hangs on the first one. There's
a small harness that runs commands on their own goroutines and settles with the
receive still outstanding — which is exactly what an open stream is.

**Q. Why bufconn for the proxy but a fake for the runner?**

Because of what's under test. For the proxy, the *integration* is the thing — a
proxy that forwards correctly to a mock tells you only that the mock was called.
So it's three real gRPC stacks with no port between them. For the runner, what's
under test is the replay, the ordering, the report and the exit code;
`grpcclient` is where the wire is tested, and duplicating that would be slower
tests measuring the same thing.

**Q. Why don't your performance tests assert durations?**

A shared CI runner makes that a coin toss — and it's not hypothetical, a bufconn
timing assertion had to be deleted for exactly that. So I assert what the cost
scales *with*: the tree renders a screenful rather than a schema, and
`testing.AllocsPerRun` at two input sizes catches a complexity regression
deterministically. Four times the entries costing sixteen times the allocations
is unambiguous. The benchmarks are still there to give a number; the tests pin
the shape.

**Q. How do you keep documentation from rotting?**

Three tests that walk the source of truth and fail when the artefact doesn't
cover it. `flagGroups` is checked against the flag set, so an unfiled flag fails
rather than disappearing off the bottom of the help page. `docs/formats.md` is
checked against the struct tags of `Config` and `Request`. And
`docs/keybindings.md` is *generated* from the keymap. The only way a "complete
reference" stays complete is if incompleteness fails a test.

**Q. Why did you version the file formats, and why one number for three files?**

Because v1.0 promises a collection you commit today still works next year. One
number, because they're released together — three numbers would be three chances
to get a compatibility claim wrong, and nobody has the collection format from
one release and the config format from another. The non-obvious part is *when*
the check runs: the readers use `KnownFields(true)`, and a file from a later
grpctui is made of keys this one has never heard of, so the strict decode would
fail on the *key* rather than the *version* — `"field renderers not found"` sends
the reader hunting for a typo they didn't make. So the version check is a
separate pass over the same bytes, before the real decode, reading it out of a
`yaml.Node`. An absent version means current, which is what keeps every file
written before v1.0 working.

**Q. Why is version read from build info before ldflags?**

Because ldflags aren't applied when a user runs `go install`. A `go install
...@v0.1.0` build carries an accurate module version and no ldflags at all —
reading them the other way round makes every such build report "dev". Build info
wins; ldflags fill in for the GoReleaser binaries, and a local `go build` falls
through both to "dev".

**Q. Why are tags never moved?**

The module proxy caches them immutably. A bad release is fixed by tagging the
next patch, never by force-pushing. That mental model is also why `go mod tidy
-diff` is its own CI step — a released `go.mod` is cached forever, and drift is
invisible to the build, the linters and the whole test matrix.

---

## The five I answer most often

If the whole project had to reduce to five sentences, these are the five.

1. **The layering rule, and what it bought.** The UI never imports grpc and the
   transport never imports bubbletea — which is why the transport sits at 98.9%
   without a terminal and the UI is testable without a server.

2. **Dynamic invocation.** A descriptor from reflection,
   `dynamicpb.NewMessage`, and `ClientConnInterface.Invoke` with the method
   path. Generated stubs are that, plus type safety.

3. **The streaming command chain.** Each receive is a command that blocks on its
   own goroutine and issues the next one from its handler, so `Update` is never
   blocked on a server that has gone quiet.

4. **The credentials rule as a progression.** Every version added a surface that
   outlives the call, and each one had to re-establish the same invariant —
   enforced structurally wherever I could manage it, so `Headers []string` is a
   type that cannot hold a value.

5. **Scale is tested by shape, not by stopwatch.** A frame costs a screenful
   rather than a schema, and `AllocsPerRun` at two sizes pins that
   deterministically where a duration on a shared runner is a coin toss.
