# Chapter 4 — The Transport Layer: `internal/grpcclient`

The highest-value package in the repository. 98.9% covered, and the one v1.0
named explicitly. Everything here exposes plain Go types and `error`s: no
`tea.Msg`, no lipgloss, no terminal.

## 4.1 Dialling

```go
func Dial(target string, opts ...DialOption) (*Client, error)
```

**It takes no `context.Context`, and that is the most interesting thing about
it.** The doc comment explains why: `grpc.NewClient` connects *lazily*. Nothing
goes over the network at dial time, so a target that is down surfaces as a
`codes.Unavailable` status on the first call rather than as a dial error.
Accepting a context would imply an I/O bound that does not exist.

**The exception, and it is deliberate:** bad TLS material — a CA file that does
not exist, a client certificate that does not match its key — fails *here*,
because nothing about it will improve by waiting. `Security.credentials()` does
real file I/O and real key parsing.

Three dial options are always installed:

```go
dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
dialOpts = append(dialOpts, grpc.WithStatsHandler(statsHandler{}))
if header, ok := cfg.auth.header(); ok {
    dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(perRPCAuth{...}))
}
```

The stats handler is installed **once, here**, because gRPC takes stats handlers
as *dial* options, not call options. Section 4.7 explains how one handler then
serves every concurrent call.

### The client struct

```go
type Client struct {
    conn     grpc.ClientConnInterface  // narrow interface, not *grpc.ClientConn
    closer   func() error
    target   string
    security Security
    logger   *zap.Logger
    schema   []protoreflect.ServiceDescriptor // .proto fallback
}
```

`conn` is the narrow `grpc.ClientConnInterface` (just `Invoke` and `NewStream`),
not `*grpc.ClientConn`. `Conn()` exposes it for exactly one caller —
`internal/proxy`, which forwards streams this package knows nothing about — and
the doc comment is explicit that it is *a seam for passing a connection along,
not an invitation to reach past the transport layer*.

## 4.2 Discovery by reflection

```go
func (c *Client) ListServices(ctx context.Context, md Metadata) ([]Service, error)
```

The gRPC Server Reflection protocol is itself an RPC: a bidi stream on
`grpc.reflection.v1.ServerReflection/ServerReflectionInfo` over which the client
asks for file descriptors by symbol name and the server replies with serialised
`FileDescriptorProto`s, which the client links.

The flow:

```go
rc := grpcreflect.NewClientAuto(ctx, c.conn)   // per call — see §3.3
defer rc.Reset()

names, err := rc.ListServices()
slices.Sort(names)                              // stable tree ordering

resolver := rc.AsResolver()
for _, name := range names {
    desc, _ := resolver.FindDescriptorByName(name)
    sd := desc.(protoreflect.ServiceDescriptor)
    services = append(services, serviceFromDescriptor(sd))
}
```

Three decisions worth defending:

**Metadata rides along on the reflection stream.** Discovery is an RPC like any
other, so *a server that gates its API behind a header gates reflection behind
the same one*. The doc comment notes that "a target that answers grpcurl but not
grpctui is nearly always this" — a diagnostic insight baked into the code.

**The results are sorted.** Reflection returns services in whatever order the
server registered them. Sorting means the tree looks the same on every connect,
and it means the `.proto` fallback (which also sorts) produces an identical
tree.

**`Unimplemented` is mapped to a sentinel:**

```go
func discoveryError(op string, err error) error {
    if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
        return fmt.Errorf("%s: %w: %w", op, ErrReflectionUnavailable, err)
    }
    return fmt.Errorf("%s: %w", op, err)
}
```

Note the **double `%w`** (Go 1.20+): the error wraps *both* the sentinel and the
original status, so `errors.Is(err, ErrReflectionUnavailable)` and
`status.FromError(err)` both still work on the same value. The UI uses the first
to show a dedicated screen — "enable reflection, or restart with `--proto`" —
because that failure has a specific, actionable fix.

### The `.proto` fallback path

```go
if len(c.schema) > 0 {
    return c.listFromSchema(start), nil
}
```

`WithSchema` is a **dial option**, not an argument to `ListServices`, because
*where a schema comes from is a property of the connection*: switching profile
re-dials, and the files come with it.

`listFromSchema` does **no I/O at all**, and the doc comment defends that as the
one behavioural difference the layers above can observe: a target that is down
still shows its full API and only says so when a call is made. *That is the
right way round — the schema is what the files say the server offers, and
reporting it as unavailable because the server is asleep would hide the schema
the user supplied for exactly that situation.*

### The domain types

```go
type Method struct {
    Name, FullName, InputType, OutputType string
    ClientStreaming, ServerStreaming      bool
    Descriptor protoreflect.MethodDescriptor
}
func (m Method) Kind() Kind  // unary | server-streaming | client-streaming | bidi-streaming
```

`Kind()` collapses the two booleans into one enum so that every consumer
switches on a single value. The four constants are `KindUnary`,
`KindServerStreaming`, `KindClientStreaming`, `KindBidiStreaming`.

## 4.3 Dynamic unary invocation

```go
path := methodPath(method.Descriptor)               // "/pkg.Service/Method"
resp := dynamicpb.NewMessage(method.Descriptor.Output())
err = c.conn.Invoke(ctx, path, req, resp)
```

Three lines carry the whole "no code generation" claim:

1. The **method path** is built from the descriptor: `"/" + parent.FullName() +
   "/" + name`. That string is literally what goes on the wire as the HTTP/2
   `:path` pseudo-header.
2. **`dynamicpb.NewMessage(descriptor)`** produces a `proto.Message` whose shape
   is decided at runtime.
3. **`ClientConnInterface.Invoke`** is grpc-go's generic call primitive — the
   same one generated stubs call underneath. Generated code adds type safety and
   nothing else.

### Carrying a gRPC status across the layer boundary

```go
type CallStatus struct {
    Code    uint32  // 5
    Name    string  // "NotFound"
    Message string  // the server's own message
}

func StatusOf(err error) (CallStatus, bool) {
    var carrier interface{ GRPCStatus() *status.Status }
    if !errors.As(err, &carrier) { return CallStatus{}, false }
    ...
}
```

The subtlety is in *how the status is extracted*. It is read off the wrapped
error **directly via `errors.As` on the `GRPCStatus()` interface**, not through
`status.FromError`. Why: `status.FromError` on a *wrapped* status rewrites the
message to the full error string. That is right for a log line and wrong for a
panel that already says which method was called — what belongs on screen is the
server's own message, not `"invoke pkg.Svc.M: rpc error: code = NotFound desc =
..."`.

`ok == false` means the error never reached the wire (a request that could not
be built), which the UI shows as a plain failure rather than as a status. That
distinction is visible to the user and is why both cases exist.

## 4.4 Streaming

```go
type Stream interface {
    Method() Method
    Send(req proto.Message) error
    CloseSend() error
    Recv() (proto.Message, error)
    Close() error
}
```

Opening one builds a `grpc.StreamDesc` from the descriptor:

```go
desc := &grpc.StreamDesc{
    StreamName:    string(method.Descriptor.Name()),
    ServerStreams: method.ServerStreaming,
    ClientStreams: method.ClientStreaming,
}
cs, err := c.conn.NewStream(ctx, desc, methodPath(method.Descriptor))
```

That is the whole "all four shapes from one code path" trick: the shape is two
booleans read off the descriptor.

### No timeout, on purpose

```go
// It deliberately carries no timeout of its own; a server-streaming watch that
// runs for an hour is the feature, not a hung call.
ctx, cancel := context.WithCancel(ctx)
```

A unary call gets `DefaultCallTimeout` (60s). A stream gets none. `esc` ends
one, and nothing else does. The asymmetry looks like an oversight until you try
the alternative: a timeout on a watch stream means the tool deciding when a
watch has gone on long enough, which is not a decision it is in any position to
make.

### Locking discipline

```go
type clientStream struct {
    sendMu sync.Mutex   // serialises senders
    recvMu sync.Mutex   // serialises receivers
    mu     sync.Mutex   // guards shared bookkeeping
    sendClosed, closed  bool
    sent, received      int
}
```

**Three mutexes, not one, and each has a distinct job.** grpc-go's contract is
"one sender and one receiver at a time, and they may run concurrently". So:

- `sendMu` serialises `Send`/`CloseSend` against each other.
- `recvMu` serialises `Recv` against itself.
- `mu` guards the counters and flags *both halves* touch.

A single mutex would make `Send` and `Recv` mutually exclusive, which would
deadlock a bidi call: the server may not answer until it has heard several
messages, and the client may not send more until it has heard one.

### Preserving grpc-go's EOF contract

```go
if err := s.cs.SendMsg(req); err != nil {
    if errors.Is(err, io.EOF) {
        return io.EOF   // unwrapped, deliberately
    }
    return fmt.Errorf("stream %s: %w", s.method.FullName, err)
}
```

`io.EOF` from `SendMsg` means the call is already over and `Recv` holds the
reason. It is returned **unwrapped** so `errors.Is` finds it and the caller goes
looking in the right place, rather than showing "EOF" as a failure. The UI's
handler acts on exactly this:

```go
case errors.Is(msg.err, io.EOF):
    // the reason belongs to the receiving half, which is about to report it
    m.sendQueue, m.closeAfterQueue = nil, false
    return m, nil
```

### `finished` runs once

```go
func (s *clientStream) finished(err error) {
    s.mu.Lock()
    if s.closed { s.mu.Unlock(); return }
    s.closed = true
    ...
}
```

A stream ends exactly once, but `Recv` may well be called again afterwards. One
log entry per extra call would be noise.

## 4.5 Transport security

```go
type Security struct {
    TLS                bool
    CACert             string   // PEM bundle path
    ClientCert, ClientKey string // mTLS pair
    ServerName         string   // for IP / port-forward / tunnel
    InsecureSkipVerify bool
}
```

**Zero value is plaintext**, which is what a local server almost always wants.
Everything beyond it is opt-in. `Validate()` refuses two contradictions:

- TLS material configured but `TLS: false` — *the alternative is connecting in
  the clear while the config file talks about certificates.*
- A client cert without a key, or vice versa.

**The CA pool does not merge the system store:**

```go
pool := x509.NewCertPool()
pool.AppendCertsFromPEM(pem)
cfg.RootCAs = pool
```

The reasoning is stated in the code: *naming a CA is how a user says "this
server is signed by this authority and no other", and quietly accepting every
public root as well would turn a pinned connection into an ordinary one.* One
line of implementation, a paragraph of justification — which is the usual ratio
for the security decisions in this codebase.

`MinVersion: tls.VersionTLS12` is set explicitly. `InsecureSkipVerify` carries a
`#nosec G402` with a written justification and *the status bar says so while it
is on*.

## 4.6 Auth and metadata

### Per-RPC credentials, not a header

```go
type perRPCAuth struct{ key, value string }
func (a perRPCAuth) GetRequestMetadata(context.Context, ...string) (map[string]string, error)
func (perRPCAuth) RequireTransportSecurity() bool { return false }
```

Auth is attached with `grpc.WithPerRPCCredentials` rather than added to the
metadata panel *so that it survives every RPC the client makes — reflection
included — without each caller having to remember it.* A protected server is as
likely to guard reflection as anything else.

**`RequireTransportSecurity() returns false`, which is a deliberate deviation
from gRPC's default.** gRPC refuses to send per-RPC credentials over an insecure
connection, and *for a library that is right*. For a debugging tool aimed at
local servers and `kubectl port-forward`, it would mean refusing the most common
setup there is. The mitigations: a `logger.Warn` at dial time, and the status
bar showing the connection is in the clear. This is a good example of "I know
what the safe default is, here is why I deviated, and here is the compensating
control."

### The bearer-prefix detail

```go
value := a.Token
if !strings.HasPrefix(strings.ToLower(value), "bearer ") {
    value = "Bearer " + value
}
```

*A token pasted from a browser's dev tools often arrives with the scheme already
on it; prefixing it again produces "Bearer Bearer …", which servers reject with
a 401 that says nothing useful.*

### Metadata validation

```go
func ValidateHeader(h Header) error
```

Enforces gRPC's own rules: non-empty key, no HTTP/2 pseudo-header (`:path`), no
`grpc-` prefix (reserved by the protocol), only token characters
(`[a-zA-Z0-9-_.]`), and — for a `-bin` suffixed key — a value that is valid
base64.

The `-bin` handling is the subtle part. gRPC base64-encodes a binary header
value on the wire, so *what the user types is the encoded form and what we hand
to the metadata package is the decoded bytes* — otherwise it is encoded twice
and the server sees gibberish. And `decodeBinary` tries **all four base64
alphabets** (std/raw × standard/URL), because what the user pasted came from
somewhere else and which of the four spellings it uses is not something they
should have to know.

`Metadata.Validate()` uses `errors.Join` to report *every* bad header at once: *a
call refused for two bad headers should say so twice.*

**Metadata is passed explicitly to `ListServices` and `InvokeUnary` rather than
baked into the client**, because the metadata panel edits it between calls and
*reconnecting to change a header would be absurd*.

## 4.7 The latency breakdown: one stats handler, many calls

This is the most technically interesting piece of the transport layer.

**The problem.** A wall-clock duration says a call took 800ms. It does not say
whether that was connecting, waiting, or receiving — the difference between "the
service is slow" and "the service is far away".

**The constraint.** gRPC takes `stats.Handler` as a **dial** option. One handler
serves every concurrent call on the connection. So how does a handler know which
call an event belongs to, without a registry keyed on something?

**The solution: hang the collector off the call's own context.**

```go
type timingKey struct{}   // private type — nothing outside can collide

func withTiming(ctx context.Context) (context.Context, *callTiming) {
    t := &callTiming{}
    return context.WithValue(ctx, timingKey{}, t), t
}

// in InvokeUnary:
ctx, collector := withTiming(ctx)
err = c.conn.Invoke(ctx, path, req, resp)
timing := collector.result()
```

```go
func (h statsHandler) HandleRPC(ctx context.Context, rpc stats.RPCStats) {
    t := timingFrom(ctx)
    if t == nil { return }   // a call that did not ask — e.g. reflection
    switch s := rpc.(type) {
    case *stats.Begin:      t.begin = s.BeginTime
    case *stats.OutHeader:  t.outHeader = h.clock()
    case *stats.OutPayload: t.requestBytes += s.WireLength
    case *stats.InHeader:   t.inHeader = h.clock()
    case *stats.InPayload:  t.responseBytes += s.WireLength
                            if t.inHeader.IsZero() { t.inHeader = s.RecvTime }
    case *stats.End:        t.end = s.EndTime
    }
}
```

Five details worth knowing:

1. **`timingKey struct{}` is a private type.** Nothing outside the package can
   put a value on the same key — the standard fix for context-key collisions.
2. **`callTiming` has a mutex.** gRPC calls a stats handler from *whichever
   goroutine the event happened on* — the one that wrote the headers, the one
   reading the transport — and the invoking goroutine reads the result when the
   call returns.
3. **Header events carry no timestamp of their own**, unlike the payload ones,
   so they are stamped as they are handled. gRPC calls a handler synchronously
   on the event's goroutine, so that is the event's time to within a scheduling
   hop. The clock is injectable (`now func() time.Time`) so a test can assert on
   a breakdown rather than on "non-negative".
4. **`InPayload` backfills `inHeader`.** A server that sends no headers before
   its first message — or a trailers-only response — leaves `InHeader` unseen,
   and the first byte is then the payload's.
5. **`HandleConn` is deliberately a no-op.** Connection events arrive on the
   *connection's* context, not any call's, so there is nothing to attribute them
   to.

### `Measured bool` — the field that is not redundant

```go
type Timing struct {
    Measured   bool
    Connect    time.Duration
    FirstByte  time.Duration
    Total      time.Duration
    RequestBytes, ResponseBytes int
}
```

Every consumer checks `Timing.Measured` rather than testing `Total != 0`. The
reason: **a coarse clock — Windows' is ~15ms — genuinely returns a zero total
for a fast call over a loopback transport.** Reading that as "no breakdown
available" would hide the timings of exactly the quickest responses. This is a
cross-platform correctness detail that only shows up if you actually ran the
Windows leg of the CI matrix.

`Connect` being near zero on a warm connection is *not a measurement error* —
grpctui has usually already run a reflection sweep over the same connection, so
the honest answer for the second call onwards is that connecting cost nothing.

## 4.8 `Profile` — and why it lives here

```go
type Profile struct {
    Name     string
    Target   string
    Security Security
    Auth     Auth
    Metadata Metadata
}
```

`Profile` lives in `internal/grpcclient` rather than `internal/config` **because
every field of it is a transport concern.** `config` maps YAML onto it; the UI
switches between them. Putting it in `config` would force the transport layer to
depend on the config layer to dial, inverting the dependency.

`DialProfile` is `Dial` plus the profile's security and credentials — and
notably *not* its metadata, because *headers ride on the call, so that editing
them does not mean reconnecting*.
