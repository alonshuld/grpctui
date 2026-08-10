# Chapter 11 — Concurrency, Contexts and Errors

## 11.1 The concurrency inventory

A useful thing to be able to state: **where are the goroutines?** In grpctui
there are exactly five sources, and four of them are owned by a framework.

| Source | Owner | Count |
|---|---|---|
| `tea.Cmd` execution | bubbletea runtime | one per in-flight command |
| The terminal input reader | bubbletea runtime | 1 |
| gRPC transport internals | grpc-go | opaque |
| The proxy's request pump | `proxy.handle` | one per proxied call |
| The proxy server's handlers | grpc-go | one per proxied call |

**grpctui itself spawns exactly two goroutines**, both in `main`/`proxy`:

```go
go func() { sendErr <- p.pumpRequests(...) }()     // proxy.handle
go func() { if err := watcher.Serve(); err != nil { logger.Error(...) } }()  // main
```

Everything else is a `tea.Cmd`, which means the *runtime* owns the goroutine and
the result comes back as a message. That is the single biggest reason the UI has
no data races: **there is no shared mutable state between the command and the
model, because commands capture values and return messages.**

## 11.2 The mutex inventory

Three, and each guards a distinct concurrency shape.

**`clientStream`'s three (Chapter 4 §4.4)** — `sendMu`, `recvMu`, `mu`. Split
because a single mutex would make `Send` and `Recv` mutually exclusive, which
deadlocks a bidi call.

**`callTiming.mu`** — because gRPC calls a stats handler *from whichever
goroutine the event happened on*, and the invoking goroutine reads the result
when the call returns.

**`proxy.stopped sync.Once`** — makes `Stop` idempotent and safe from any
goroutine; guarantees the event channel is closed exactly once however many
callers ask.

Plus two atomics in the proxy (`dropped`, `calls`) — counters read from the UI
goroutine and written from handler goroutines, where a mutex would be overkill.

## 11.3 Context propagation

**Every transport call takes a `context.Context` as its first parameter.** That
was designed in from v0.1 *because v0.5 requires the UI to cancel an in-flight
RPC* — retrofitting cancellation into a transport layer is a rewrite; designing
for it is one parameter.

The chain:

```
signal.NotifyContext(context.Background(), os.Interrupt)   // main
  └─ ui.WithContext(ctx) → Model.ctx
       ├─ context.WithTimeout(m.ctx, discoveryTimeout)     // 10s, per sweep
       ├─ context.WithTimeout(m.ctx, callTimeout)          // 60s, per unary call
       └─ context.WithCancel(m.ctx)                        // per stream — NO timeout
```

Four deliberate asymmetries:

| Operation | Bound | Why |
|---|---|---|
| Discovery | 10s | a sweep either answers or the target is wrong |
| Unary call | 60s, generous | *the user can give up sooner with `esc`, and a tool for debugging misbehaving services should not be the thing that decides a slow server has failed* |
| Stream | **none** | *watching a server-streaming method for an hour is the feature* |
| Headless stream | the run's timeout | *a smoke test that waits forever is a broken build rather than a failed one* |

The same operation has opposite bounds in the two front-ends, and both are
right — because in the TUI a human is watching and can press `esc`, and in CI
nobody is.

### `Dial` takes no context

Because `grpc.NewClient` is lazy and does no I/O. Accepting one would imply a
bound that does not exist. The doc comment tells callers what to do instead:
*follow `Dial` with `ListServices`, which does take a context.*

### Cancellation must always run

```go
ctx, cancel := context.WithCancel(ctx)
// ...
cs, err := c.conn.NewStream(ctx, desc, path)
if err != nil {
    cancel()            // on the error path
    return nil, ...
}
return &clientStream{cancel: cancel, ...}   // Close() calls it
```

> Every stream owns a cancel func, and it must be called however the stream ends
> — grpc-go leaks the call's resources otherwise. `Stream.Close` is that call,
> and the UI runs it on every path out.

`Stream.Close` is documented as safe to call more than once and safe on a
finished stream, *which is why the UI can call it unconditionally when the user
moves on.* Making cleanup idempotent is what lets callers stop reasoning about
it.

And a stream the user has already moved on from is closed rather than left to
the GC:

```go
if msg.seq != m.callSeq {
    if msg.stream != nil { _ = msg.stream.Close() }
    // an abandoned gRPC stream holds its call open until something cancels it
}
```

### Ownership of the client

```go
dialer     Dialer
ownsClient bool   // did this model open the connection, or was it handed one?
```

`New` is given a client that belongs to the *caller*. A client from
`Model.startConnect` belongs to the *model*. `Close()` respects the difference,
and `main` closes both:

```go
final, runErr := program.Run()
if m, ok := final.(ui.Model); ok { _ = m.Close() }   // whatever the FINAL model holds
_ = client.Close()                                   // the one main created
```

> The model owns the connection from here: switching profile replaces it, so the
> one to close at the end is whichever the *final* model holds.

bubbletea has no teardown hook, so `Run()`'s return value is the teardown hook.

## 11.4 Error handling patterns

### Wrapping with `%w`, always

```go
return nil, fmt.Errorf("invoke %s: %w", method.FullName, err)
```

Every error carries what was being attempted. `errorlint` is enabled in
`.golangci.yml`, which catches `%v` where `%w` was meant and `err == target`
where `errors.Is` was meant.

### Double-wrapping (Go 1.20+)

```go
return fmt.Errorf("%s: %w: %w", op, ErrReflectionUnavailable, err)
```

Wraps *both* a sentinel and the original status, so `errors.Is(err,
ErrReflectionUnavailable)` and `status.FromError(err)` both work on the same
value. The UI uses the first to show a dedicated screen with a specific fix.

### Sentinels for the failures with a specific fix

```go
grpcclient.ErrReflectionUnavailable  // → "enable reflection, or restart with --proto"
grpcclient.ErrNotUnary               // → programming error, caught in tests
grpcclient.ErrNotStreaming
grpcclient.ErrSendClosed             // → "you already ended the request stream"
grpcclient.ErrReservedHeader         // → "grpc- is reserved by the protocol"
requests.ErrNoCollectionsDir         // → "the feature is switched off, nothing failed"
format.ErrUnsupported                // → "wrong format version, not malformed YAML"
```

The test for whether something deserves a sentinel: **does a caller need to
behave differently, and is there a specific thing the user should do?**
`ErrNoCollectionsDir` is the clearest example — *it is a distinct error because
the UI reports it differently from a failed write: nothing went wrong, the
feature is simply switched off.*

### Typed errors when the caller needs the details

```go
type FieldError struct{ Path, Name, Value string; Err error }   // panel matches on Path
type UnsetError struct{ Names []string }                        // pluralises
type UnsupportedError struct{ Path string; Have, Known Version }// two directions of advice
```

Each carries the *fields the consumer acts on*, not just a message. `FieldError.Path`
is what puts the complaint back on the row that caused it.

### `errors.Join` — report everything at once

Used in nine places. The pattern is always the same:

```go
var errs []error
for _, thing := range things {
    if err := check(thing); err != nil { errs = append(errs, err); continue }
    ...
}
return errors.Join(errs...)
```

And the justification is always the same shape: *"a form that fails one field at
a time is miserable to fill in"*, *"a call refused for two bad headers should
say so twice"*, *"a file with three bad colours in it says so once rather than
over three runs"*, *"reporting them one send at a time is three sends"*.

**Always paired with sorting**, when iterating a map, *so that a file with two
bad names in it reports them the same way twice: Go's map iteration order is
deliberately not stable.*

### Validate-all-then-apply

```go
// keys.Apply
if err := errors.Join(errs...); err != nil { return k, err }  // ← original, unmodified
if err := out.conflicts(remapped); err != nil { return k, err }
return out, nil
```

> Nothing is applied unless everything can be: a half-remapped keyboard is worse
> than an unremapped one, because the half that worked hides the half that did
> not.

The same shape appears in `styles.Catalog` and `render.New`.

### Degrade, don't fail, when the failure is incidental

Several places deliberately swallow an error, each with a stated reason:

```go
wire, _ := protoschema.Wire(resp.Message)
// a message that will not re-encode simply has no raw view — the call itself
// succeeded, and reporting it as failed over a rendering would be absurd

case historySavedMsg:
    if msg.err != nil { m.logger.Warn("could not write history", ...) }
    // History is a convenience, not the call. A state directory that cannot be
    // written to is worth a line in the log and nothing on screen.

body, format, err := protoschema.MarshalRequest(req)
if err != nil {
    body = "(the request could not be rendered: " + err.Error() + ")"
    // The message is going out regardless — it is valid protobuf, or the form
    // would not have built it. Only the rendering failed.
}
```

The rule that emerges: **an error in the thing the user asked for is reported;
an error in the bookkeeping around it is logged.** Being able to state that as a
policy, rather than defending each `_` individually, is the strong answer.

`nilerr` and `errcheck` (with `check-type-assertions: true`) are both enabled, so
every one of these is a deliberate, reviewed choice rather than an oversight.

### Errors are written for the person reading them

```go
return errors.New("expected a whole number that is not negative")   // not "invalid syntax"
return fmt.Errorf("expected one of %s", strings.Join(names, ", "))  // lists the enum values
return fmt.Errorf("no profile named %q: have %s", name, ...)        // lists what there was
return fmt.Errorf("version must be a whole number like %s, got %s", Current, describe(node))
```

> `yaml`'s message for that names a Go type, which tells a person editing a
> config file nothing they can act on.

The pattern: **say what was wrong, and say what was allowed.** Nearly every
"not found" error in the codebase lists the valid options.

## 11.5 Cancellation-as-a-feature: the two kinds of giving up

Worth restating because it is a genuinely subtle distinction:

```go
func (m *Model) abortCall()  // esc: cancel, let the failure show as "Canceled"
func (m *Model) retireCall() // moved on: cancel AND bump seq, so nothing shows
```

> That is the difference from `abortCall`, which the user presses `esc` for and
> which is meant to put "Canceled" on screen. Here there is nothing left to put
> it under.

`retireCall` is called from four places: selecting another method, recalling
from history, loading from the proxy, and switching connection. Each is "the
user moved on".

## 11.6 What a race detector run would catch, and why it does not

`-race` is on in CI **always**, across three operating systems and two Go
versions — not just before a release tag. The things it would catch, and why
they are absent:

- **Model access from a command goroutine** — prevented by the capture
  discipline (§6.3). Every command captures values before returning the closure.
- **Concurrent `Send`/`Recv` on a gRPC stream** — prevented by `sendMu`/`recvMu`,
  and by the UI's send queue allowing one in flight at a time.
- **The stats handler racing the invoker** — prevented by `callTiming.mu`.
- **A history slice mutated under a command that captured it** — prevented by
  `History.Add` allocating rather than appending in place.
- **The proxy's event channel closed while a handler emits** — prevented by
  `grpc.WaitForHandlers(true)` plus `sync.Once`.

That last one is the one that *would only show up under load*, which is exactly
why it is worth having a written reason next to it.
