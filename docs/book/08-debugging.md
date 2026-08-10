# Chapter 8 — The Debugging Half

v0.8's thesis: *this is the point where it stops being "grpcui in a terminal" and
starts being a genuine debugging tool.* Five features, each answering a question
the decoded response cannot.

| Feature | The question it answers |
|---|---|
| Raw wire view | "What did the server *actually* put on the wire?" |
| Diff | "Is the bug back, or did it move?" |
| Latency breakdown | "Is the service slow, or is it far away?" |
| Passive proxy | "What is the application actually sending?" |
| grpcurl export | "Can you show me the call you made?" |

## 8.1 The passive proxy — `internal/proxy`

A gRPC server that forwards everything it receives to a real one and reports
what went past. `mitmproxy` for gRPC: point the application at grpctui instead
of at the service, and every call it makes is logged.

### Forwarding without a schema

The problem: **a proxy that decoded messages would need a descriptor for every
method it might see, which is exactly what it does not have at the moment a call
arrives.**

The solution is three gRPC server options:

```go
p.server = grpc.NewServer(
    grpc.ForceServerCodec(rawCodec{}),   // decode nothing
    grpc.UnknownServiceHandler(p.handle),// catch every method
    grpc.WaitForHandlers(true),          // see §8.1.4
)
```

**`rawCodec`** satisfies gRPC's insistence on a codec by doing nothing:

```go
type frame struct{ payload []byte }

func (rawCodec) Marshal(v any) ([]byte, error)  { return v.(*frame).payload, nil }
func (rawCodec) Unmarshal(data []byte, v any) error {
    f := v.(*frame)
    f.payload = make([]byte, len(data))   // COPY
    copy(f.payload, data)
    return nil
}
func (rawCodec) Name() string { return "proto" }
```

Three details:

1. **The copy is mandatory.** *gRPC hands over a buffer it may reuse or return
   to a pool once the call returns, and both the forwarded copy and the event the
   UI reads outlive that moment.* Without it the UI would show a message that
   changes underneath it — a heisenbug of the worst kind.
2. **`Name()` returns `"proto"`, not something of its own.** The name is the
   content-subtype on the wire, and a real client sends
   `application/grpc+proto`; *a codec calling itself anything else would not be
   selected for that call.*
3. **It is installed per connection** via `ForceCodec`/`ForceServerCodec`,
   **never** via `encoding.RegisterCodec` — *registering it would replace the
   real protobuf codec process-wide, and grpctui's own client is in the same
   process.* That is a genuinely nasty bug avoided by one sentence of thought.

### Every call is a bidi stream

```go
desc := &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}
client, err := p.upstream.NewStream(outgoing, desc, method, grpc.ForceCodec(rawCodec{}))
```

> Unary, server-streaming, client-streaming and bidi all reduce to the same
> shape once nobody is counting messages, which is why one handler covers all
> four.

### Two pumps, one goroutine each

```go
sendErr := make(chan error, 1)                      // buffered — see below
go func() { sendErr <- p.pumpRequests(...) }()      // client → server
err = p.pumpResponses(...)                          // server → client, on this goroutine
if err == nil {
    if e := <-sendErr; e != nil && !errors.Is(e, io.EOF) { err = e }
}
```

> A single loop would deadlock a bidirectional call, where the server may not
> answer until it has heard several messages and the client may not send more
> until it has heard one.

**The channel is buffered with capacity 1** *so that a request pump abandoned by
a failing response half can send its result and exit rather than leaking for the
life of the process.* This is the classic goroutine-leak fix and it is called
out explicitly.

**The response half is authoritative:** if it finished cleanly, *whatever the
request half made of the call is the last word on it — an error there is the
reason a short call was short.* If it failed, its error wins. This asymmetry is
why `errgroup` (first error wins) would have been the wrong tool.

### `Stop` and the close-once problem

```go
func (p *Proxy) Stop() {
    p.stopped.Do(func() {
        if p.server != nil { p.server.Stop() }
        close(p.events)
    })
}
```

> The channel is closed here rather than where `Serve` returns because `Serve`
> returns as soon as the listener closes, which is **before** the calls already
> in flight have finished. Closing it then would leave a handler emitting into a
> closed channel — a panic, and one that would only show up under load.
> `grpc.WaitForHandlers` makes `Stop` wait for them instead.

`sync.Once` makes `Stop` idempotent and safe from any goroutine — which matters
because `Serve` calls it via `defer` *and* `main`'s cleanup calls it.

### Backpressure: drop, never block

```go
func (p *Proxy) emit(event Event) {
    select {
    case p.events <- event:
    default:
        p.dropped.Add(1)
    }
}
```

> Blocking here would make the proxy's own latency a function of how fast the UI
> redraws, which would change the timings the user is trying to read.

**The one thing a passive proxy may never do is slow down the traffic it is
watching.** A 256-event buffer, an `atomic.Int64` of what was dropped, and the
panel says how many were lost. I chose lossy over lossless deliberately: an
unbounded queue would have made the tool distort the thing it is measuring.

Messages are also truncated at `MaxPayload = 64 << 10` for the *log* — *the call
itself is forwarded whole regardless; only what the log keeps is cut* — because
a proxy watching a service that streams megabyte frames must not grow without
bound just because somebody left the panel open.

### What it does not do

```go
// It adds no credential of its own. The connection it forwards over is dialled
// without grpctui's auth precisely so that the server sees what the observed
// client sent and nothing else.
outgoing = metadata.NewOutgoingContext(ctx, md.Copy())   // verbatim
```

In `main`:

```go
upstream, err := grpcclient.DialProfile(grpcclient.Profile{
    Target:   p.Target,
    Security: p.Security,   // security only — no Auth, no Metadata
}, ...)
```

> A passive proxy exists to show what an application is sending, and one that
> quietly added grpctui's own bearer token would be showing the user something
> the application never sent — which is the one thing a wire-watching tool may
> not do.

It is also **pinned to the target grpctui started on**: switching connection or
environment in the UI moves the calls *the user* makes, but *an application
pointed at the proxy has no way to know that its destination changed underneath
it.*

### The payoff: replay

```go
func (m Model) replayTraffic(msg panels.TrafficReplayMsg) (tea.Model, tea.Cmd) {
    svc, method, ok := m.tree.SelectMethod(msg.Method)
    decoded, err := protoschema.DecodeWire(method.InputDescriptor(), msg.Wire)
    m.request.SetMethod(svc, method)
    m.request.Load(decoded)
    m.request.SetNotice("Loaded from the proxy. Headers are the ones on this connection.")
}
```

> This is what makes passive mode more than a log: the request arrives as bytes
> with no schema attached, and the descriptor reflection already discovered is
> what turns it back into a form you can edit and send yourself.

Two layers that know nothing about each other — a schema-free proxy and a
reflection-driven form — meet at exactly one function call.

### Testing it

Three real gRPC stacks and no port between them: a real health server, a real
proxy in front of it, and a real client talking to the proxy, all over
`bufconn` (`WithListener` exists for exactly this).

> There is nothing useful to test with a fake here — a proxy that forwards
> correctly to a mock tells you only that the mock was called.

Covered: all four call shapes, the status of a failed call surviving, metadata
reaching the upstream **unchanged *and* no credential being added**, interleaved
call numbering, and events being dropped rather than blocking.

## 8.2 The diff — `internal/diff`

A leaf package: nothing of grpctui's, nothing about protobuf, gRPC or the
terminal.

```go
func Lines(before, after string) []Line
func Unified(before, after string, context int) ([]Line, []Skip)
```

**Line-grain, not character-grain.** A changed line is a `Delete` followed by an
`Insert`: *pairing the two into a "modified" line would need a second,
character-level diff, and a JSON body's changed line is usually rewritten
wholesale anyway.*

### The algorithm and its two guards

Classic **longest common subsequence** dynamic programming, with two protections
that turn an O(n·m) textbook algorithm into something safe for a response body:

**1. Peel the common prefix and suffix first.**

```go
for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] { prefix++ }
for suffix < ... && before[len(before)-1-suffix] == after[len(after)-1-suffix] { suffix++ }
```

> Two responses from the same method usually differ in a field or two out of
> dozens, so this is **not an optimisation at the margin: it is what keeps the
> table small enough to build at all in the ordinary case.**

**2. Cap the table.**

```go
const maxCells = 4 << 20
case len(before)*len(after) > maxCells:
    return append(all(Delete, before), all(Insert, after)...)
```

> Past this the quadratic table costs more memory than the answer is worth, so
> the two sides are reported as wholly replaced instead. That is a true diff,
> just a coarse one, and it keeps a pathological pair of bodies from taking the
> process down.

Degrading gracefully rather than either hanging or erroring is the right answer
for a debugging tool, and knowing where to put the cliff is the interesting part.

**3. One allocation for the table.**

```go
table := make([][]int, len(before)+1)
cells := make([]int, (len(before)+1)*(len(after)+1))
for i := range table { table[i], cells = cells[:len(after)+1], cells[len(after)+1:] }
```

A single backing slice re-sliced into rows — one allocation instead of n+1, and
contiguous memory for cache locality.

**4. Deterministic tie-breaking.**

```go
case table[i+1][j] >= table[i][j+1]:   // >= not >
    lines = append(lines, Line{Op: Delete, ...})
```

> Where the two branches are equally good it deletes before inserting, so that a
> changed line reads old-then-new — the order a reader expects, and the order a
> unified diff has always used.

### The invariant worth pinning

There is one property worth asserting directly, ahead of any particular case:

> Every line of both inputs appears exactly once on its own side, or the view
> has quietly lost part of a response.

That is the test to write for any diff implementation, and it catches the entire
class of off-by-one bugs that a "does it look right" test does not.

### `Unified` and honest elision

`Skip{At, Lines}` reports the runs that were collapsed rather than dropping them
silently: *a panel that says "42 unchanged lines" is honest about what it is not
showing.* And a diff with no changes at all is returned **whole** rather than
collapsed to nothing, because *"these two responses are identical" is better
said by the panel than by an empty box.*

## 8.3 The response panel's three views

```go
const (
    viewDecoded responseView = iota
    viewRaw
    viewDiff
)
```

**One panel with a `view` field, not three panels.** They are three renderings of
*one* result: the body, the bytes it was encoded from, and what changed since
last time.

**The chosen view survives the next call:**

> Somebody watching a field change across three sends has said, by pressing `D`,
> what they want to see — putting them back in the decoded body on every send
> would make the feature unusable for the thing it is for.

The viewport goes back to the top on a *switch*, though, *because the three
renderings have nothing to do with each other line for line, so carrying an
offset across would land the user in the middle of something they have not
read.*

And `viewNote()` puts "· raw bytes" / "· diff vs previous" on the status line,
*because without it a raw or diff view is a panel showing something that is not
the response, with nothing to say so — and the two are easy to leave switched on
and then be confused by several calls later.*

### The raw view's two halves

```go
func (r Response) rawView() string {
    // 1. field listing, schema-free
    // 2. hexdump underneath
}
```

> Both halves earn their place. The field listing answers "what did the server
> actually put on the wire", which is the question that survives a descriptor
> disagreeing with reality; the hex dump answers "what exactly", which is the
> one you fall back to when the listing itself looks wrong.

The hexdump adapts its width — 16, 8 or 4 bytes per line — *halving down from
sixteen so that the columns stay a power of two and a byte offset can still be
read off the row.*

## 8.4 The latency breakdown, on screen

The mechanism is Chapter 4 §4.7. What the panel does with it:

```go
return formatDuration(r.duration) + "  · " + strings.Join([]string{
    "connect "    + formatDuration(r.timing.Connect),
    "first byte " + formatDuration(r.timing.FirstByte),
    "total "      + formatDuration(r.timing.Total),
    protoschema.ByteCount(r.timing.RequestBytes)  + " out",
    protoschema.ByteCount(r.timing.ResponseBytes) + " in",
}, " · ")
```

> **Total first, breakdown after:** the parts are what you read when the total
> surprises you, so they are the half a narrow panel may truncate away.

Bytes sit on the same status line as latency *because "slow" and "large" are the
two answers to the same question.*

`formatDuration` picks resolution by magnitude — seconds to the millisecond,
milliseconds to 100µs, below that to the microsecond — because *a column of "0s"
would say nothing about a run where every request was fast.*

## 8.5 grpcurl export — `internal/export`

```go
func Grpcurl(c Command) string
```

Why grpcurl: *it is the tool everyone already has, and its flags map almost
one-for-one onto a `grpcclient.Profile`.*

**This is the most outward-facing surface in the program — it is written to be
copied somewhere else** — so the credentials rule is at its strictest:

```go
const preamble = "# grpctui does not copy credentials out, so header values below are placeholders."
```

- Header **names** only, with `<name>` placeholders for values.
- The credential's **kind**, in the shape the header actually takes on the wire:
  `Bearer <token>`, `Basic <base64 of alice:password>` — *so that somebody
  filling the command in knows whether to write a token or a base64 pair.*
- `{{variable}}` references exported **as written**: *the value behind one is
  very often a token captured out of a login response.*
- References the JSON body could not carry (the int64 case) are listed as
  comments, because *grpcurl has nowhere to put them: the field is at its zero
  value in the body, and saying so is the only honest thing to do with it.*

Three implementation details worth noting:

**Multi-line with backslash continuations.** *A request with three headers on
one line is a line nobody can read, and the point of exporting one is that a
human looks at it.*

**`compact()` is a hand-written squeeze, not `json.Compact`:**

> It drops the whitespace between tokens, which JSON does not care about, and
> leaves everything inside a string literal exactly as it is. It is a squeeze
> rather than a re-encode through `encoding/json` **because the body may hold
> `{{variable}}` references in places JSON would refuse to parse.**

**Shell quoting is single-quote-and-escape:**

```go
func quote(value string) string {
    return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
```

*Single quotes are the only shell quoting that needs no thought about what is
inside them — except for a single quote, which has to be closed, escaped and
reopened.* And `quoteIfNeeded` leaves an ordinary `host:port` unquoted so the
command stays readable.

**The flag polarity flip:** grpcurl defaults to TLS and takes `-plaintext` to
turn it off, which is the *opposite* of grpctui's default. So the plaintext case
is the one that needs a flag.

## 8.6 Response renderers — `internal/render`

The extension point, and the clearest statement of design principle 3.

```go
type Renderer interface {
    Name() string                   // for the config file to switch it off
    Types() []string                // "google.protobuf.Timestamp"
    Render(msg protoreflect.Message, now time.Time) (string, bool)
}
```

**Annotate, never rewrite.** `"2026-08-08T09:14:02Z"` stays exactly what the
server sent; "3 minutes ago" appears beside it, dimmed, behind a `←` marker.

> That is not a stylistic choice: grpctui's whole pitch is reading the wire
> rather than a prettified account of it, and a response panel that silently
> replaced a value with an interpretation of it would be lying about the one
> thing the tool exists to show. **It would also be unusable for the case that
> matters most — the server whose timestamps are wrong.**

Returning `false` is the right answer for a value the renderer cannot make sense
of: *a gloss that says something wrong is worse than no gloss.*

Renderers are **on by default** and switched off by name in the config
(`renderers: {timestamp: false}`), because *one that had to be discovered and
enabled before it did anything is one nobody would ever see.*

### The hard part: joining a message to its rendered text

```go
func (r Registry) Annotate(msg proto.Message, body string, now time.Time) []Annotation
```

> The two are walked separately and joined by **field path**, rather than the
> JSON being parsed back into a message, **because the message is where the
> *types* are: JSON has forgotten that a string was a `Timestamp`, which is the
> one thing a renderer needs to know.**

So: walk the *message* by descriptor to collect `path → gloss`; walk the *JSON
text* to collect `path → line number`; join.

The JSON walk uses `json.Decoder.Token()` and `InputOffset()` rather than
splitting on newlines:

> It walks tokens rather than the lines themselves because a path is a fact
> about the structure and not about the indentation: the body is indented by
> `MarshalJSON` and looks predictable today, but a walk that depended on that
> would break the day anything about the layout changed.

The `walker` maintains `path []string` and `inObject []bool` in parallel — the
latter says whether each level is an object (so the next token names a field) or
a list (where it occupies a position). Line numbers come from counting newlines
before the decoder's offset, which works *because no JSON value spans a line — a
string's newlines are escaped.*

Two correct-by-design behaviours:

- **A renderer takes precedence over descending.** *A type somebody has claimed
  is a type they have said how to show, and glossing its innards as well would
  be annotating the same value twice.*
- **A body that is not JSON yields no annotations, not an error.** That is the
  text-format fallback, and *there is nothing wrong in that case; there is
  simply nothing to attach a gloss to.*

Duplicate type claims are an **error** at registry construction, not a silent
precedence rule: *two renderers competing for `google.protobuf.Timestamp` is a
configuration mistake, and which one wins is not something a user should have to
discover by looking at the output.*

### Testing the invariant that matters

> The invariant that matters is that a gloss lands on the line of the field it
> describes, so the tests assert a note's line against the **text on that line**
> rather than a number counted by hand.

That is a genuinely better test design: a hand-counted line number breaks when
you add a field to the fixture; a text assertion does not.
