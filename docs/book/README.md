# grpctui: Notes from Building It

This is the book I wanted to exist while I was writing grpctui: not an API
reference and not a tutorial, but a record of the *thinking* — the decisions,
the ones I got wrong first, and the reasons that only became obvious three
versions later.

Almost every design choice in this codebase has a reason, and most of those
reasons are already in the tree as doc comments. What was missing was the thread
connecting them: why the layering rule made the streaming work possible, why a
constraint I wrote down for v0.4 kept coming back in v0.6, v0.7, v0.8 and v0.9,
why a lazily-materialised form tree is a correctness requirement rather than an
optimisation. This book is that thread.

## How to read it

Chapters 1–3 are the framing: what I was trying to build, how I shaped the code,
and what I chose to build it out of. Chapters 4–9 walk the layers bottom-up,
following a request from the socket to the screen and back. Chapters 10–13 are
the concerns that cut across everything — the credentials rule, concurrency,
testing, and getting the thing released. Chapter 14 answers the questions I get
asked about it, and the ones I had to answer for myself.

If you only have an hour, read Chapter 2 (the architecture), Chapter 4 (the gRPC
transport), Chapter 6 (the TUI runtime), and Chapter 14.

## Contents

| # | Chapter | What it covers |
|---|---------|----------------|
| 1 | [The Problem and the Product](01-problem-and-product.md) | The gap in the gRPC tooling landscape, the design principles I set, and the roadmap I held myself to |
| 2 | [Architecture: Four Layers and One Rule](02-architecture.md) | transport → domain → UI → main, the one rule, the four seams, and the patterns I used |
| 3 | [The Dependency Stack](03-dependencies.md) | Every direct dependency, what it beat, and what I deliberately left out |
| 4 | [The Transport Layer: `internal/grpcclient`](04-transport.md) | Dial, reflection, dynamic invoke, streaming, TLS/mTLS, and the stats-handler timing trick |
| 5 | [The Schema Layer: `internal/protoschema`](05-schema.md) | Descriptor → form tree, presence semantics, the template round trip, raw wire decoding |
| 6 | [The TUI Runtime: `internal/ui`](06-tui-runtime.md) | The Elm architecture in Go, the command discipline, sequence numbers, the streaming chain |
| 7 | [State, Config and File Formats](07-state-and-config.md) | Config loading, profiles, environments, history, collections, and the compatibility promise |
| 8 | [The Debugging Half](08-debugging.md) | The passive proxy, the raw wire view, diffing, latency breakdown, grpcurl export, renderers |
| 9 | [Headless Mode and the CLI](09-cli-and-headless.md) | `run`, `keys`, `completion`, flag parsing, exit codes, CI smoke tests |
| 10 | [The Credentials Rule](10-credentials-rule.md) | The one constraint I set in v0.4, and how every version after it had to keep it true |
| 11 | [Concurrency, Contexts and Errors](11-concurrency-and-errors.md) | Where the goroutines are, how cancellation propagates, how errors are shaped |
| 12 | [Testing Strategy](12-testing.md) | bufconn, teatest, golden files, fuzzing, scale tests, and what I chose not to test |
| 13 | [Build, CI and Release Engineering](13-release-engineering.md) | Merge gates, the matrix, GoReleaser, semver policy, version embedding |
| 14 | [The Questions I Get Asked](14-questions.md) | Sixty-odd questions about the design, answered the way I would answer them out loud |

## The thirty-second version

grpctui is a terminal UI for gRPC: point it at `host:port`, it uses **server
reflection** to discover the whole API, generates a **request form from the
protobuf descriptor**, invokes the method **dynamically** (no generated stubs),
and renders the answer. It handles all four call shapes, TLS/mTLS, headers,
saved collections, `{{variable}}` interpolation across environments, a raw-bytes
view, response diffing, per-call latency breakdown, a passive man-in-the-middle
proxy, and a headless CI mode.

Stack: **Go 1.25**, **bubbletea** (Elm-architecture TUI) + **bubbles** +
**lipgloss**, **google.golang.org/grpc**, **google.golang.org/protobuf**
(`dynamicpb`, `protojson`, `protowire`), **jhump/protoreflect/v2** (reflection
client), **bufbuild/protocompile** (`.proto` fallback), **zap** (file-only
structured logging), **yaml.v3**, **testify** + **teatest** + **bufconn** for
tests, **GoReleaser** for distribution.

Architecture: four strictly one-directional layers. The UI never imports gRPC;
the transport never imports bubbletea. That one rule is what makes the
reflection/invoke core testable without a terminal (98.9% covered) and the UI
testable without a server — and almost everything else in this book follows from
it.
