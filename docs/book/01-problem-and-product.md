# Chapter 1 — The Problem and the Product

## 1.1 The gap

Working with gRPC in 2026 means choosing between four kinds of tool, and each
covers a slice of the job:

| Tool | What it gives you | What it costs |
|------|-------------------|---------------|
| `grpcurl` | Scriptable, reflection-aware, single binary | You hand-write JSON payloads. No exploration, no state, no memory of what you did. |
| `grpcui` | Reflection-driven form generation | Lives in a browser tab, visually stuck in 2018, no keyboard workflow, not scriptable. |
| Postman / Insomnia / Kreya | Collections, environments, polish | Heavyweight GUI, often paid, often cloud-coupled, not in the terminal where the rest of your work is. |
| Wireshark / mitmproxy | Wire truth | Protocol-generic; knows nothing about your `.proto`, so a gRPC frame is bytes with an HTTP/2 wrapper. |

Nobody had built the `lazygit` of gRPC: a full-screen, keyboard-driven,
terminal-native client that is as good at **debugging a misbehaving service** as
it is at calling a healthy one. That is the gap grpctui fills.

The analogy is deliberate and it is the one I kept coming back to:
`lazygit` did not add a feature `git` lacked; it made the workflow you already
had *fast* by making state visible and every action one keystroke away. grpctui
does the same thing to `grpcurl`.

## 1.2 The four design principles

Every design decision in the codebase traces back to one of these four. When you
are asked "why is X like that", the answer is usually "because of principle N".

**1. Zero config to start.** Point it at an address; reflection does the rest.
`.proto` files are *optional*, never required. This is why reflection is the
primary discovery path and `internal/protofiles` is explicitly documented as
"the fallback path, not the preferred one". It is also why a missing config file
is not an error (`config.Load` returns the zero `Config`), why a missing history
file is not an error, and why an undeterminable state directory disables
persistence rather than failing startup.

**2. Full-screen, panel-based, keyboard-first.** No mouse for any core
workflow. This is why every keybinding lives in one `keys.KeyMap` struct, why
panels never compare a key string directly, and why the modal panels (profiles,
requests, variables, themes, traffic, export) own the keyboard while open
instead of being tab stops.

**3. Read the wire, not just the docs.** The tool must be useful for the
*misbehaving* service, not only the healthy one. This is the most
consequence-heavy principle in the codebase:

- `WireFields` is deliberately **schema-free** — the point of a raw view is the
  case where the decoded view is wrong, so consulting the descriptor to render
  it would hide exactly the bug you opened it for.
- `internal/render` **annotates, never rewrites** — `"2026-08-08T09:14:02Z"`
  stays exactly what the server sent and "3 minutes ago" appears beside it,
  because a panel that replaced a value with an interpretation would be useless
  against the server whose timestamps are wrong.
- The passive proxy **adds nothing to the wire**: no credential of its own, no
  rewritten metadata. A wire-watching tool that changed the wire would be worse
  than useless.
- The raw view is labelled "as re-encoded" rather than pretending to be a
  capture, because gRPC hands its stats handlers the decoded message and not the
  frame.

**4. Stay a single static binary.** No runtime dependencies. `CGO_ENABLED=0` in
the release build. This is why the `.proto` fallback resolves well-known imports
out of the linked protobuf runtime rather than asking the user to find a copy of
`timestamp.proto` on disk, and why the "plugin" extension point for renderers is
an *interface* rather than a loadable plugin process.

## 1.3 The version roadmap as engineering discipline

Before writing any code I laid out a v0.1 → v1.0 roadmap in which **each version
is independently shippable** — a real release somebody could use, not a
checkpoint. That constraint is the thing that kept the codebase coherent, and it
is worth saying why.

| Version | Theme | The thing it proved |
|---------|-------|---------------------|
| v0.1 | Connect & browse | Reflection discovery works inside a TUI shell |
| v0.2 | First request/response | Dynamic invoke of a unary call with a generated form |
| v0.3 | Real request forms | Nested messages, repeated, maps, `oneof`, enums, validation |
| v0.4 | Metadata & auth | TLS/mTLS, bearer/basic, headers, connection profiles |
| v0.5 | Streaming | All three streaming shapes, live |
| v0.6 | History & collections | Requests become a repeatable workflow |
| v0.7 | Variables & environments | Collections become portable; chained requests |
| v0.8 | Debugging & observability | Raw wire, diff, latency breakdown, proxy, grpcurl export |
| v0.9 | Polish & extensibility | Themes, `.proto` fallback, remapping, completions, headless, renderers |
| v1.0 | Stable | Versioned formats, 98.9% transport coverage, Homebrew, generated key reference, large-schema benchmarks |

Two properties of that ordering did most of the work:

**It is ordered by architectural risk, not by user visibility.** v0.3 (the field
tree) comes before v0.4 (auth) because the field tree is the thing that decides
whether the whole schema layer's abstraction holds. If a `oneof` picker had
forced a protobuf `switch` into the form panel, the layer boundary would have
been wrong and everything after it would have inherited the mistake.

**Each version's constraints were designed forward, not retrofitted.** The
clearest example: `context.Context` is threaded through every transport call
from v0.1, *because* v0.5 needs the UI to cancel an in-flight RPC. Retrofitting
cancellation into a transport layer is a rewrite; designing for it is one
parameter.

## 1.4 What "done" looks like

The acceptance criteria I set for v1.0 at the outset — someone should be able
to:

1. Point at any gRPC service with reflection — zero setup
2. Explore its full API surface visually, no `.proto` files needed
3. Build and fire complex nested requests without hand-writing JSON
4. Watch streaming RPCs live
5. Save and replay request collections across dev/staging/prod
6. Debug a misbehaving call — headers, raw bytes, latency, diffs — without
   switching to Wireshark

All six shipped. The roadmap is closed; work from here is maintenance, bug
fixes, or a new milestone.

## 1.5 The one standing constraint

Cutting across every version is a single rule, which I wrote down as a
*standing constraint, not a v0.4 detail*:

> A bearer token, a basic-auth password and a header value never reach the log
> file, the status bar, the connection switcher, a history file, a collection
> file, an exported command, or a CI log — only their *kind* and header *names*
> do.

Chapter 10 is entirely about how that is enforced, and it is the part of the
project I am most pleased with — not because the rule is clever, but because it
had to be re-established on *every new surface*, and each version added one.
v0.6 added files that outlive the session. v0.7 made it possible for a request
*body* to hold a token. v0.8 added an exported command written to be pasted
elsewhere. v0.9 added a CI log. Writing the rule down once in v0.4 was the easy
part; keeping it true five versions later is what made it a property of the
system rather than a feature of one release.
