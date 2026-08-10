# grpctui: The Complete Engineering Book

A full walk through the design of `grpctui` — every layer, every dependency
choice, every pattern, and the reasoning behind each. Written so that by the end
you can answer a senior-level interview question about any part of it, including
"why did you do it that way and what would you do differently".

## How to read this

Chapters 1–3 are the framing: what the product is, how the code is shaped, and
what it is built out of. Chapters 4–9 walk the layers bottom-up, following a
request from the socket to the screen and back. Chapters 10–13 are the
cross-cutting concerns — security, testing, release engineering — that an
interviewer is at least as likely to probe as the feature code. Chapter 14 is a
drill: the questions, and crisp answers.

If you only have an hour, read Chapter 2 (architecture), Chapter 4 (the gRPC
transport), Chapter 6 (the TUI runtime), and Chapter 14 (the drill).

## Contents

| # | Chapter | What it covers |
|---|---------|----------------|
| 1 | [The Problem and the Product](01-problem-and-product.md) | The gap in the gRPC tooling landscape, the design principles, the version roadmap as an engineering discipline |
| 2 | [Architecture: Four Layers and One Rule](02-architecture.md) | transport → domain → UI → main, the dependency rule, package-by-package map, why the seams sit where they do |
| 3 | [The Dependency Stack](03-dependencies.md) | Every direct dependency, what it does, what was rejected, and the cost of each choice |
| 4 | [The Transport Layer: `internal/grpcclient`](04-transport.md) | Dial, reflection, dynamic invoke, streaming, TLS/mTLS, metadata, the stats-handler timing trick |
| 5 | [The Schema Layer: `internal/protoschema`](05-schema.md) | Descriptor → form tree, build/validate, JSON mapping, raw wire decoding, path lookup |
| 6 | [The TUI Runtime: `internal/ui`](06-tui-runtime.md) | The Elm architecture in Go, the root model, commands, focus, layout arithmetic, the streaming command chain |
| 7 | [State, Config and File Formats](07-state-and-config.md) | Config loading, profiles, environments, history, collections, the `version:` compatibility promise |
| 8 | [The Debugging Half](08-debugging.md) | Passive proxy, raw wire view, diff, latency breakdown, grpcurl export, response renderers |
| 9 | [Headless Mode and the CLI](09-cli-and-headless.md) | `run`, `keys`, `completion`, flag parsing, exit codes, CI smoke tests |
| 10 | [The Credentials Rule](10-credentials-rule.md) | The single standing constraint, and how it is enforced on every surface |
| 11 | [Concurrency, Contexts and Errors](11-concurrency-and-errors.md) | Goroutine discipline, cancellation, sequence numbers, error wrapping, status propagation |
| 12 | [Testing Strategy](12-testing.md) | bufconn, teatest, golden files, fuzzing, scale tests, what is deliberately not tested |
| 13 | [Build, CI and Release Engineering](13-release-engineering.md) | Merge gates, the matrix, GoReleaser, semver policy, version embedding |
| 14 | [Interview Drill](14-interview-drill.md) | 60+ questions with answers, ranked by how likely they are to come up |

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
(`dynamicpb`, `protojson`, `protowire`), **jhump/protoreflect/v2**
(reflection client), **bufbuild/protocompile** (`.proto` fallback), **zap**
(file-only structured logging), **yaml.v3**, **testify** + **teatest** +
**bufconn** for tests, **GoReleaser** for distribution.

Architecture: four strictly one-directional layers. The UI never imports gRPC;
the transport never imports bubbletea. That one rule is what makes the
reflection/invoke core testable without a terminal (98.9% covered) and the UI
testable without a server.
