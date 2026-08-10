# Chapter 12 — Testing Strategy

~43k lines total, roughly half of which are tests. The rule: **every feature
lands with tests in the same PR — a feature branch that adds behaviour and no
test does not merge.**

## 12.1 The choice of technique, per layer

The interesting thing is not that everything is tested; it is that each layer
uses a *different* technique, chosen for a stated reason.

| Package | Technique | Why that one |
|---|---|---|
| `grpcclient` | Real gRPC server over **bufconn** | The wire is what is under test |
| `proxy` | **Three** real stacks over bufconn | *A proxy that forwards correctly to a mock tells you only that the mock was called* |
| `protoschema` | Pure functions + **fuzzing** | Exhaustive and cheap; the round trip is a property |
| `ui` | **teatest** golden files + direct `Update` calls | A frame is a function of its messages |
| `runner` | A **hand-written fake** client | *What is under test is the replay, the ordering, the report and the exit code — `grpcclient` is where the wire is tested* |
| `config`, `requests` | **Temp dirs**, real files | The failure modes are filesystem failure modes |
| `vars`, `diff`, `format`, `export` | Pure unit tests | Leaf packages, no I/O |
| `protofiles` | Real `.proto` text in a temp dir | Compiler behaviour is what is under test |
| `cmd/grpctui` | The paths that return **before the TUI starts** | Everything after that is `internal/ui`'s job |

The `proxy` and `runner` entries are opposites, and knowing which side a test
sits on is the actual skill: use a real stack when the *integration* is the
thing; use a fake when the *logic around* the integration is the thing.

## 12.2 bufconn: real servers, no ports

```go
lis := bufconn.Listen(1024 * 1024)
srv := grpc.NewServer()
reflection.Register(srv)
go srv.Serve(lis)

client, _ := grpcclient.Dial("bufnet",
    grpcclient.WithGRPCDialOptions(grpc.WithContextDialer(...)))
```

`WithGRPCDialOptions` exists on the production `Dial` **for this** — the doc
comment says so: *"Tests use it to swap in a bufconn dialer."* An escape hatch
that is documented as a test seam is honest; one that pretends to be a general
feature is not.

Everything real: a real HTTP/2 handshake, real framing, real status codes, real
reflection — just over an in-memory pipe. No port, no flakiness from a port
already in use, no firewall, and fast enough to run in the fast path.

### `teststream_test.go` — a service with no generated code

Streaming needs a service with all three shapes, and there is no generated code
for one. So the test **serves a hand-built descriptor through
`grpc.UnknownServiceHandler`** and answers with `dynamicpb` messages — a real
server answering real streams.

This is a nice symmetry: the test server uses exactly the technique the
*production proxy* uses (`UnknownServiceHandler` + dynamic messages), for
exactly the same reason (no compile-time knowledge of the schema).

### `failures_test.go` — the branches nothing else reaches

Gathered *by that reason* rather than by subject. That is a deliberate
organisational choice: a file called `failures_test.go` is where you look when
coverage drops, whereas failure cases scattered across five files are invisible.

**Two branches are unreachable and are left alone deliberately, with reasons:**

1. `grpc.NewClient` resolves lazily, so **no target string fails a dial** — a
   malformed *option* is what does.
2. `attach`'s binary decode is unreachable because `ValidateHeader` checks the
   same thing first — *belt to that braces*.

Being able to say "these two lines are unreachable and here is why, and I chose
not to contort the code to reach them" is a better answer than 100% coverage.

## 12.3 teatest and golden files

```bash
go test ./internal/ui -update    # regenerate, then REVIEW THE DIFF
```

> Never regenerate blindly to make a test pass.

Golden files only work if the frame is deterministic. Four things make it so:

1. **The clock is injected** (`ui.WithClock`), so *a frame is a function of the
   messages that produced it*.
2. **JSON indentation is byte-stable**, because `MarshalJSON` re-indents with
   `encoding/json` rather than trusting protojson's deliberately-randomised
   whitespace (Chapter 5 §5.8).
3. **Discovery results are sorted**, and so is `vars.Set`.
4. **Colour is forced** via `withColour(t)`, because under `go test` stdout is
   not a terminal and lipgloss would render every style as bare text — *a test
   asserting that a theme reached the frame would assert nothing.*

The `withColour` helper *sets the profile and puts it back*, and **no test in
those packages runs in parallel**, which is what makes that safe. `paralleltest`
is explicitly disabled in `.golangci.yml` with that reason written down.

Two themes can also render identically on purpose — `dark` is `auto` with the
guessing removed — *so a test proving a switch happened uses a palette nothing
else uses.* That is a subtle test-design trap: asserting "the theme changed" by
comparing frames fails silently when the two themes agree.

### The two UI-test traps

**`apply` follows a command one hop**, which is all a unary call needs. But
v0.6's flows are *chains* — a keystroke yields a message whose handler yields
the write whose result closes the prompt — so they use `applyChain`, and *a test
that stops at the first hop asserts against a screen the user never sees.*

**Streaming cannot use the synchronous helpers at all**: an open stream's
receive does not return until the server speaks, so a test driving it that way
**hangs on the first one**. `stream_test.go`'s `harness` is a small runtime —
commands on their own goroutines, messages fed back — that *settles with the
receive still outstanding, which is exactly what an open stream is.*

That is the single most interesting test-infrastructure decision in the repo:
the harness has to model "quiescent but not finished", which the standard helper
cannot express.

### The 100-cell help budget

The expanded help bar has a hard 100-cell budget. Past it `bubbles/help`
**truncates the last column away, which silently undocuments a keybinding.**
`internal/ui/keys` has a test for it, and the fix is always to move a binding to
another column or shorten a description — never to let the bar grow.

The `FullHelp()` comments record this being hit *three separate times*, each
time resolved by folding a would-be sixth column into an existing one — *since a
column is only ever as wide as its widest entry*, adding a row is free and
adding a column is not.

## 12.4 Fuzzing

```bash
go test ./internal/protoschema -fuzz FuzzRoundTrip
```

`FuzzRoundTrip` fuzzes build → load → build. The instruction is specific: **run
it after changing how a value is parsed or formatted.**

That is the right target for fuzzing in this codebase, because the round trip is
a genuine *property* (`Build(Load(m)) == m`) over a large input space with lots
of edge cases: 32- vs 64-bit widths, signed/unsigned/fixed encodings, base64
bytes, enum names vs numbers, float formatting precision.

## 12.5 Scale tests: shape, not stopwatch

The v1.0 large-schema work, in `internal/ui/scale_test.go` and
`internal/ui/panels/scale_test.go`.

```go
const (
    bigRows = testschema.BigServices * (testschema.BigMethods + 1)  // 300 × 11 = 3,300
    panelHeight = 30
)
```

**Never assert a duration:**

> A shared CI runner makes that a coin toss, and it is the reason a bufconn
> timing assertion was deleted once already.

That is a real event in the history — commit `73c8c8f`, *"stop asserting a
bufconn call takes measurable time"*.

**Assert what the cost scales *with*:**

```go
// TestTree_RendersOnlyWhatFits: a frame costs a screenful, not a schema.
lines := strings.Split(tree.View(), "\n")
assert.Len(t, lines, panelHeight)   // 30, not 3,300
```

```go
// testing.AllocsPerRun at two sizes catches the same regression deterministically:
// four times the entries costing sixteen times the allocations is unambiguous
// where a stopwatch on a shared runner is not.
```

**Benchmarks measure; tests pin the shape.** Both are present, and knowing why
you need both is the point: the benchmark tells you the number today, the test
fails when the *complexity class* changes.

The fixture generator lives in `internal/testschema` and is shared between the
two packages, *because two copies would be one edit away from two suites
benchmarking different workloads under the same claim.*

## 12.6 Documentation-completeness tests

Three, all the same idea: **walk the source of truth, fail if the artefact does
not cover it.**

```go
// internal/config/docs_test.go, internal/requests/docs_test.go
//   walk the struct tags → assert docs/formats.md names every key
//   a nested block documented once elsewhere opts out with a `prefix.*` line,
//   which reads correctly to a person too

// cmd/grpctui/flags_test.go
//   walk the flag set → assert flagGroups files every flag

// internal/ui/keys/reference_test.go
//   check keys.Reference against the KeyMap struct IN BOTH DIRECTIONS
//   (nothing missing, nothing invented)
```

The `prefix.*` opt-out is a small piece of craft worth noticing: the escape
hatch is *itself* readable documentation, so it does not create a second thing
to remember.

## 12.7 Testing the constraints, not just the behaviour

The tests that pin the *invariants* rather than the features:

```go
// internal/requests — every test that writes a record asserts:
//   no header VALUE reached the file
//   no expanded variable reached the file
// "those are the constraints most likely to be broken by accident, and the
//  cheapest to pin"

// internal/export — every test asserts no credential and no expanded variable
//   reached the rendered command

// internal/runner — no header name OR value reached either report format

// internal/proxy — metadata reaches the upstream unchanged AND no credential
//   was added

// internal/diff — every line of both inputs appears exactly once on its own side
//   "or the view has quietly lost part of a response"

// internal/render — a note's line is asserted against the TEXT on that line
//   rather than a number counted by hand
```

These are the tests that would catch a regression a feature test would sail past.
Being able to point at "I have a test for the *rule*, not only for the code that
happens to follow it" is a strong signal.

## 12.8 Fixtures and fakes

**Hand-written fakes, no mocking framework.** `internal/runner/fake_test.go` is a
struct with the four methods and a script of canned responses.

The reasoning: *what is under test is behaviour, not call sequences.* A
generated mock's value is `AssertCalledWith`, and the questions here are "did
the run continue past the failure", "was the bidi call skipped", "did the JSON
report round-trip" — none of which are about call sequences.

**Shared fixtures live in `internal/testschema` and `internal/testdocs`** —
non-`_test.go` packages under `internal/`, so they can be imported across test
packages without being exported to users.

## 12.9 The hard constraints on the suite

> Tests must not require a network, a real gRPC endpoint, or a TTY. Anything
> slow or external goes behind `testing.Short()` and is skipped in the fast
> path.

And:

> Add a regression test with every bug fix, named for the behaviour it pins.

The naming rule matters: `TestTree_RendersOnlyWhatFits` says what is true, not
what was broken. A year later the name is still a specification.

## 12.10 Coverage: the numbers and the policy

- **`internal/grpcclient`: 98.9%** — the one package v1.0 named, and *a change
  that drops it wants a reason*.
- Coverage is uploaded per CI run.
- **A PR that drops total coverage needs a stated reason in the description.**

Not a hard gate, which is the right call: a hard coverage gate rewards testing
the easy lines. A stated reason keeps the conversation where it belongs.

## 12.11 The three-tier gate

```
pre-push hook       gofmt · go vet · go test              (a few seconds, local)
        ↓
CI on every PR      + golangci-lint · -race · 3 OS × 2 Go  (the authority)
        ↓
release on a tag    re-runs the full suite from scratch    (trusts nothing)
```

> The hook is the fast local mirror of CI — it deliberately omits `-race` and the
> lint pass to stay under a few seconds, so CI remains the authority. **Never
> bypass it with `--no-verify`; if the hook is wrong, fix the hook.**

Three tiers with an explicit statement of which one is authoritative, and an
explicit statement of what each deliberately omits, is a mature setup. The
release job **re-runs the full suite rather than trusting the branch run**,
which is the right paranoia for something published to an immutable module
proxy.
