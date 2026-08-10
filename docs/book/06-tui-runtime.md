# Chapter 6 — The TUI Runtime: `internal/ui`

## 6.1 The Elm architecture, in Go

```go
type Model interface {
    Init() tea.Cmd
    Update(tea.Msg) (tea.Model, tea.Cmd)
    View() string
}
```

The contract, and every consequence in this chapter follows from it:

- **`Update` is a pure fold.** It takes a message and returns new state. It must
  never block.
- **`View` is a pure function of state.** No side effects, no I/O.
- **`tea.Cmd` is `func() tea.Msg`.** The runtime runs it on its own goroutine
  and feeds the returned message back into `Update`. This is the *only* way to
  do anything asynchronous.
- **`tea.Batch(cmds...)`** runs several commands concurrently; their messages
  arrive in whatever order they finish.

Everything blocking in grpctui — reflection sweeps, RPCs, stream receives, file
writes, dials — is a `tea.Cmd`.

## 6.2 The root model

`Model` in `internal/ui/model.go` (2,534 lines) owns focus, the panel layout,
and all cross-panel state. Its fields fall into seven groups:

```go
type Model struct {
    // 1. Collaborators
    client Client; dialer Dialer; logger *zap.Logger
    ctx    context.Context   // see below
    renderers render.Registry

    // 2. Chrome
    keys keys.KeyMap; styles styles.Styles; help help.Model; spinner spinner.Model

    // 3. The four tab-cycle panels
    tree panels.Tree; request panels.Form
    metadata panels.Metadata; response panels.Response

    // 4. The seven modals
    profiles, browser, environments, variables, export, traffic, themes

    // 5. Session state
    history requests.History; responses map[string]string
    historyAt int; lastCollection string
    services []grpcclient.Service; state state; focus focus; err error

    // 6. In-flight call bookkeeping
    callSeq, connSeq int
    cancelCall context.CancelFunc
    stream grpcclient.Stream; streamStart time.Time
    sendQueue []proto.Message; sending, closeAfterQueue bool

    // 7. Geometry and clock
    width, height, helpH int
    now func() time.Time
}
```

### The contained context

```go
//nolint:containedctx // bubbletea's Update/Cmd signatures leave no alternative.
ctx context.Context
```

Storing a `context.Context` in a struct is a documented Go anti-pattern, and the
linter (`containedctx`) is enabled and flags it. The suppression carries a
specific reason: **`Update(tea.Msg)` takes a message and nothing else, so there
is no parameter to thread a context through.** Holding it here is what lets a
`tea.Cmd` inherit cancellation from the process (`signal.NotifyContext` in
`main`).

`nolintlint` is configured with `require-explanation: true` and
`require-specific: true`, so a bare `//nolint` would itself fail the lint. Every
suppression in the repo names its linter and gives a reason.

## 6.3 The tea.Cmd closure discipline

This is the rule that makes the concurrency safe, and it is worth stating
precisely because it is the thing an interviewer can push hardest on.

**A `tea.Cmd` runs on its own goroutine. Reading the model from inside one is a
data race.** So every command *captures values before it is returned*:

```go
func (m Model) discover() tea.Cmd {
    client, parent, timeout := m.client, m.ctx, m.discoveryTimeout  // captured
    md, err := m.headers()                                          // computed now
    return func() tea.Msg {
        ctx, cancel := context.WithTimeout(parent, timeout)
        defer cancel()
        services, err := client.ListServices(ctx, md)
        ...
    }
}
```

The same discipline appears in `annotator()`:

```go
func (m Model) annotator() func(proto.Message, string) []render.Annotation {
    registry, now := m.renderers, m.now   // both values
    if registry.Empty() { return nil }
    return func(msg proto.Message, body string) []render.Annotation {
        return registry.Annotate(msg, body, now())
    }
}
```

> It is a closure rather than the registry itself because the commands run off
> the Update goroutine, where reading the model would be a race. Capturing the
> registry and the clock — **both values** — is what lets a command annotate a
> response without touching anything the UI still owns.

This is where the value-model discipline from Chapter 2 pays off. `render.Registry`,
`vars.Set`, `requests.History` and `grpcclient.Metadata` are all values
precisely so that capturing them is safe.

## 6.4 Message flow: a unary call end to end

```
 user presses ctrl+s
   ↓
 Model.handleKey  →  m.send()
   ↓  metadata.Validate() — refuse here so the complaint lands on the row
   ↓  request.Submit() → panels.SendRequestMsg{Method, Request, Template, Values}
   ↓
 Model.dispatch(req)                          ← THE ONE DOOR
   ├─ m.headers()      resolve {{vars}} in header values; refuse if unbound
   ├─ m.record(req,md) history entry + saveHistory() cmd
   └─ m.startCall(req, md)
        ├─ abortCall(); dropStream(); callSeq++
        ├─ ctx, cancel := context.WithTimeout(m.ctx, m.callTimeout)
        ├─ response.SetInFlight(method) → spinner tick cmd
        └─ tea.Batch(tick, invoke(ctx, cancel, client, req, md, callSeq, gloss))
                                    ↓ (own goroutine)
                              client.InvokeUnary(...)
                              protoschema.Marshal(resp.Message)   ← decode here,
                              protoschema.Wire(resp.Message)         not in Update
                              annotate(gloss, ...)
                                    ↓
                              callFinishedMsg{seq, body, format, wire, notes, timing}
   ↓
 Model.finishCall
   ├─ if msg.seq != m.callSeq → drop (stale)
   ├─ response.SetSuccess(Result{Body, Wire, Previous: m.responses[method], ...})
   └─ m.remember(method, body)   ← for the next diff
```

Four things in that flow are worth being able to justify:

**Rendering happens in the command, not in `Update`.** `Marshal`, `Wire` and
`annotate` all run off the Update goroutine *so that the panel receives text and
never a protobuf message*. This keeps per-response work — which is O(response
size) — out of the render loop.

**A message that will not re-encode simply has no raw view.** `wire, _ :=
protoschema.Wire(...)` deliberately discards the error: *the call itself
succeeded, and reporting it as failed over a rendering would be absurd.*

**`dispatch` is the single door.** Sends arrive from three places — the form, a
recalled history entry, the browser's "load and send". The history entry is
written here rather than in each of them, *so there is one place recording
happens and one place a reference nobody bound refuses a call, rather than three
of each*.

**The previous body is read before the new one replaces it.** That single
ordering *is* the diff view.

## 6.5 Sequence numbers: the stale-result problem

Three counters, one pattern:

```go
callSeq int  // unary calls and streams share it — only one call is ever in flight
connSeq int  // dials
```

Every command carries the sequence it was issued at; every handler starts with:

```go
if msg.seq != m.callSeq {
    m.logger.Debug("dropping stale call result", zap.Int("seq", msg.seq))
    return m, nil
}
```

Two distinct operations bump it, and the difference matters:

```go
func (m *Model) abortCall()  { m.cancelCall(); m.cancelCall = nil }  // esc
func (m *Model) retireCall() { abortCall(); dropStream(); m.callSeq++ }
```

- **`abortCall`** — the user pressed `esc`. The cancellation surfaces as an
  ordinary failed call, *so the panel needs no separate "cancelled" state*, and
  "Canceled" appears on screen.
- **`retireCall`** — the user moved on (selected another method, recalled a
  request, switched connection). The sequence number is bumped so the answer
  already on its way is **dropped rather than shown**. *Here there is nothing
  left to put it under.*

The comment on `MethodSelectedMsg` spells out the bug this prevents:

> Without that, its answer arrives after the panels have moved on and is drawn
> under the new method's name as if it belonged to it — and, since the panel is
> no longer in its in-flight state, `esc` has stopped being able to cancel it.

Two bugs in one: wrong data displayed, *and* an uncancellable call.

The stale-connection handler additionally closes what it drops:

```go
if msg.seq != m.connSeq {
    if closer, ok := msg.client.(io.Closer); ok && msg.err == nil {
        _ = closer.Close()   // two quick presses leave two dials in flight
    }
}
```

## 6.6 Streaming: a chain of commands, not a loop

The hardest part of the UI, and the best interview material in the package.

**The problem.** A `Recv` on an open stream blocks until the server speaks —
which may be an hour. A loop reading a stream cannot live in `Update`, and a
goroutine writing into the model would be a race.

**The solution: each receive issues the next.**

```go
func receive(stream grpcclient.Stream, seq int, at func() time.Duration,
             gloss func(proto.Message, string) []render.Annotation) tea.Cmd {
    return func() tea.Msg {
        msg, err := stream.Recv()          // blocks — on its own goroutine
        switch {
        case errors.Is(err, io.EOF):
            return streamRecvMsg{seq: seq, done: true, at: at()}
        case err != nil:
            st, has := grpcclient.StatusOf(err)
            return streamRecvMsg{seq: seq, err: err, status: st, hasStatus: has, at: at()}
        }
        body, format, _ := protoschema.Marshal(msg)
        return streamRecvMsg{seq: seq, body: body, format: format,
                             notes: annotate(gloss, msg, body), at: at()}
    }
}

func (m Model) streamReceived(msg streamRecvMsg) (tea.Model, tea.Cmd) {
    if msg.seq != m.callSeq { return m, nil }
    if msg.done || msg.err != nil { return m.finishStream(msg) }
    m.response.AppendReceived(msg.body, msg.format, msg.at, msg.notes)
    m.layout()
    return m, receive(m.stream, m.callSeq, m.elapsed, m.annotator())  // ← next
}
```

`Update` is never blocked on a server that has gone quiet. The exact same
pattern drains the proxy's event channel (`watchTraffic`).

### The send queue

```go
sendQueue       []proto.Message
sending         bool
closeAfterQueue bool
```

**gRPC allows exactly one sender per stream, and the order of a client-streaming
call's messages is part of what it means.** So sends go through a queue with one
in flight at a time:

```go
func (m *Model) nextSend() tea.Cmd {
    if m.sending || m.stream == nil { return nil }
    if len(m.sendQueue) > 0 {
        req := m.sendQueue[0]; m.sendQueue = m.sendQueue[1:]
        m.sending = true
        return send(m.stream, req, m.callSeq, m.elapsed)
    }
    if m.closeAfterQueue {
        m.closeAfterQueue = false; m.sending = true
        return closeSending(m.stream, m.callSeq, m.elapsed)
    }
    return nil
}
```

`closeAfterQueue` exists because *closing straight away would throw away
messages the user has already sent*. `ctrl+e` sets the flag; the close happens
once the queue drains.

### One keystroke, two meanings

```go
func (m *Model) sendOnStream(req, md) tea.Cmd {
    if m.stream == nil || !req.Method.ClientStreaming {
        return m.startStream(req, md)
    }
    m.sendQueue = append(m.sendQueue, req.Request)
    return m.nextSend()
}
```

`ctrl+s` means "send what the form says". On a client-streaming call that is
something you do repeatedly. On a server-streaming call there is only ever one
request message, so a second `ctrl+s` starts a *new* stream — the same way a
second `ctrl+s` replaces a unary call in flight.

The deliberate non-case: a client-streaming stream whose sending half the user
has **already closed** is not restarted. *They ended the request stream and are
waiting for the answer to it; throwing that away and starting again is the one
thing they cannot have meant.*

### The elapsed clock is a function, not a value

```go
return tea.Batch(tick, openStream(ctx, m.client, req.Method, md, m.callSeq))
// ...
send(m.stream, req, m.callSeq, m.elapsed)   // m.elapsed is func() time.Duration
```

Passed as a function *so that each command timestamps itself when it actually
happens rather than when it was created.*

And elapsed time advances **on the spinner tick**, not on messages: *a watch
that has gone quiet is still running, and a clock frozen beside a turning
spinner would read as a hung UI.*

## 6.7 Focus, modals and key routing

```go
const (
    focusTree focus = iota
    focusRequest
    focusMetadata
    focusResponse
    focusCount = int(focusResponse) + 1
)
```

`handleKey` is a strict priority ladder, and the order encodes real decisions:

```go
1. ForceQuit (ctrl+c)      — before even the modals: there must always be a way out
2. handleModalKey          — a modal owns every remaining key while it is up
3. Send / EndStream / Requests / Capture / NextPanel / PrevPanel
                           — work everywhere, INCLUDING inside a text field
4. if m.editing() → fall through   — q types a q, p types a p
5. everything else         — quit, modals, history walk, save, export, raw, diff, help
```

**Why group 3 works while editing.** `ctrl+s` sends, because *filling in the
last field and firing the call is the whole workflow*. `ctrl+r` opens the
browser rather than a letter *precisely so that it reaches here from inside a
half-typed field: recalling the request you meant is the answer to "I am typing
this out again"* — and it is the shell's key for the same idea. `ctrl+p`
captures, because *reading an id out of the response and putting it in the field
you are already typing into is the ordinary way a chain of requests gets built.*

**Why modals come before sends.** *Inside the browser `ctrl+s` means "load and
send this one", and inside the variables panel `ctrl+p` means "capture into this
prompt".* Context wins.

**Why the modals are not tab stops.** They are choices to *finish*, not places
to tab out of — and each is about the session rather than about the request in
front of you.

**Why `w` (raw) and `D` (diff) are global rather than response-panel-only:**
*reaching for the raw bytes is something you do while looking at a form that
produced a surprising answer, and making it a two-key job would put it in the
way of its own purpose.*

## 6.8 Layout arithmetic

```go
type layoutSizes struct {
    treeW, rightW, bodyH, requestH, metadataH, responseH int
}
```

```
┌─ Services ─────┬─ Request ────────────────┐
│                │                          │  requestH = what the form asks for
│  tree          ├─ Headers (2) ────────────┤  metadataH = what the panel asks for
│  treeW =       │                          │
│  clamp(w*2/5,  ├─ Response ───────────────┤  responseH = the rest
│    24, 48)     │                          │
└────────────────┴──────────────────────────┘
  status bar                                    1 row
  help bar                                      helpH rows (measured)
```

**The headers panel is settled first, against the *whole* column:**

```go
middle = min(max(wantMiddle, minEach), total-2*minEach)
top, bottom = splitHeight(total-middle, wantTop, minEach)
```

> so that a form tall enough to fill the screen cannot squeeze them out — a
> header list is small, bounded, and the thing you are most likely to be
> changing when a call keeps coming back `Unauthenticated`.

**Degenerate windows are handled, not assumed away.** A column too short for
three panels at their minimum is divided into thirds, *because a panel of zero
rows renders as a broken box rather than as nothing*. And `View` ends with:

```go
return styles.Clamp(screen, m.width, m.height)
```

> Panels have minimum heights that a 20×4 terminal cannot satisfy at all, so on
> a small enough window the layout arithmetic necessarily asks for more rows
> than exist; a frame larger than the terminal scrolls the screen and strands
> the previous one above it.

**The help bar is measured, not computed:**

```go
func (m *Model) measureHelp() { m.helpH = lipgloss.Height(m.help.View(m.keys)) }
```

Cached, because measuring means *rendering the whole bar*, and `computeLayout`
runs twice per keystroke purely for that number. Only two things change it: the
terminal width, and `?`. (And a theme switch — a bold key style is wider than a
plain one — which is why `switchTheme` re-measures.)

## 6.9 Theming and remapping: the same shape twice

```go
func (m *Model) applyTheme(theme styles.Theme) {
    m.styles = styles.NewTheme(theme)
    m.tree.SetStyles(st); m.request.SetStyles(st); ... // eleven panels, written out
}
func (m *Model) applyKeys(km keys.KeyMap) {
    m.tree.SetKeys(km); m.request.SetKeys(km); ...     // the same eleven
}
```

**Why a written-out list rather than a loop over an interface:**

> The panels are concrete types held **by value**: a `[]interface{
> SetStyles(...) }` would hold copies, and the copies are what would change
> colour.

This is a real Go gotcha with a real consequence. The mitigation is
`internal/ui/panels/settings.go`, which gathers the setters, plus a test.

The response panel is the one with real work to do on a theme switch: a body is
highlighted **once when it arrives**, precisely so it is not re-highlighted on
every resize. So a theme change has to rebuild every such rendering from the
plain text it came from — which is why both `body` and `rendered` are stored.

Themes preview **on cursor movement** rather than on enter: *a palette is judged
by looking at it.*

## 10 The bounded caches

Two, both memory-only and both justified:

```go
const maxDiffMethods = 24
responses map[string]string   // last body per method, for the diff view
```

> A long session against a large schema would otherwise keep a body per method
> for the life of the process. An evicted method simply reports its next
> response as the first one.

Eviction is *arbitrary* (map order), and the comment defends that: *every entry
is equally a convenience, and choosing properly would mean keeping an access
order for a cache of two dozen strings.* Knowing when **not** to build an LRU is
a signal.

```go
const maxStreamEntries = 500   // in the response panel
```

> A watch stream is unbounded by design — that is the point of watching one — so
> the log has to be bounded somewhere, or a session left open overnight ends as
> an out-of-memory.

Dropping the *oldest* keeps the tail, which is what a user looks at, and the
panel **says so** ("… 37 earlier messages dropped") rather than quietly losing
them. The `sent`/`received` counters count every message including dropped ones:
*the counts describe the call, not the window onto it.*

Neither cache is ever written to disk. A response body holds whatever the server
chose to put in it, and the credentials rule applies to answers as much as to
credentials.

## 6.11 Scale: what the frame costs

v1.0's rule, learned from a real regression:

> **Measure the frame once and hand the numbers down.**

The request browser did the opposite — its box width asked *every row* for the
two column widths, and each of those was a pass over every entry. A frame was
**quadratic in the history**, and a full one took 35ms to draw, on every
keystroke.

The fix is `Requests.layout()`: one value, computed once per `View`, passed to
everything that needs a width. The two fixed columns are measured when the
*entries* change, not when a frame is drawn, because neither depends on the
clock, the filter or the terminal. Only the description column is recomputed per
frame — and even that is *counted* rather than rendered, because building a
hundred descriptions to throw all but the widest away is three allocations per
entry per keystroke.

The tree's invariant is the complementary one: **a frame costs a screenful, not
a schema.**

```go
visible := t.height
for i := t.offset; i < t.offset+visible; i++ { lines = append(lines, t.renderRow(i)) }
```

300 services × 10 methods = 3,300 rows; the view renders 30.

**Both are pinned by tests that assert what the cost scales *with*, never a
duration:**

```go
// testing.AllocsPerRun at two sizes catches the same regression deterministically.
```

> Never assert a duration; a shared CI runner makes that a coin toss, and it is
> the reason a bufconn timing assertion was deleted once already.

That last sentence is a genuine war story and a good thing to have ready: the
project *did* once assert that a bufconn call took measurable time, it *did*
flake, and commit `73c8c8f` removed it.
