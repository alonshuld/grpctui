# grpctui

A terminal UI for exploring, calling, and debugging gRPC services — `grpcui`,
but full-screen, keyboard-driven, and living in your terminal instead of a
browser tab.

Point it at a gRPC server with reflection enabled and it discovers the entire
API surface with zero configuration.

> **Status: v0.1 — Connect & Browse.** Reflection-based discovery and
> keyboard navigation work today. Sending requests lands in v0.2.

## Install

```bash
go install github.com/alonshuld/grpctui/cmd/grpctui@latest
```

Prebuilt binaries for Linux, macOS, and Windows are attached to each
[GitHub Release](https://github.com/alonshuld/grpctui/releases).

## Usage

```bash
grpctui localhost:50051
```

```
Flags:
  -log-file string    write logs to this file; empty disables logging
                      (default "$XDG_STATE_HOME/grpctui/grpctui.log")
  -log-level string   log level: debug, info, warn, error (default "error")
  -version            print the version and exit
```

The target must serve the [gRPC server reflection API][reflection]. In Go, that
is one line on the server:

```go
import "google.golang.org/grpc/reflection"

reflection.Register(srv)
```

v0.1 connects in plaintext only. TLS and mTLS arrive in v0.4.

[reflection]: https://github.com/grpc/grpc/blob/master/doc/server-reflection.md

## Keybindings

| Key | Action |
| --- | --- |
| `↑`/`k`, `↓`/`j` | Move the cursor |
| `ctrl+u`, `ctrl+d` | Page up / down |
| `g`, `G` | Jump to top / bottom |
| `→`/`l`, `←`/`h` | Expand / collapse a service |
| `enter` | Toggle a service, or select a method |
| `tab`, `shift+tab` | Switch panel |
| `r` | Retry after a failed connection |
| `?` | Toggle the full help |
| `q`, `ctrl+c` | Quit |

## Development

```bash
make hooks        # install the pre-push hook — do this first
make test         # go test ./...
make check        # everything CI enforces: fmt, vet, lint, build, race tests
make golden       # regenerate teatest golden files (review the diff!)
make run TARGET=localhost:50051
```

The codebase is four strictly one-directional layers — transport → domain → UI
→ main:

| Package | Responsibility |
| --- | --- |
| `internal/grpcclient` | Dial, reflection discovery, dynamic invoke |
| `internal/protoschema` | Descriptor → form-field tree (v0.2) |
| `internal/ui` | bubbletea models, panels, keymap, styles |
| `cmd/grpctui` | Flags, config, `tea.Program` bootstrap |

The UI never imports `google.golang.org/grpc`, and the transport layer never
imports `bubbletea`. That separation is what makes the reflection/invoke core
testable without a terminal.

Logging goes to a file and never to stdout or stderr — a TUI owns the terminal,
and any stray write corrupts the render.

## Licence

[MIT](LICENSE) © Alon Shuldiner
