# grpctui

A terminal UI for exploring, calling, and debugging gRPC services — `grpcui`,
but full-screen, keyboard-driven, and living in your terminal instead of a
browser tab.

Point it at a gRPC server with reflection enabled and it discovers the entire
API surface with zero configuration.

> **Status: v0.2 — First Request/Response Cycle.** Discover a server, fill in
> a request form generated from the method's input message, send a unary call,
> and read the decoded response. Nested and repeated fields land in v0.3;
> metadata and TLS in v0.4; streaming in v0.5.

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

Pick a method with `j`/`k` and `enter`, `tab` into the request form, `enter` to
edit a field and `esc` when you are done with it, then `ctrl+s` to send. The
response arrives as JSON in the panel below the form; a non-OK call shows its
gRPC status code and the server's message in the same place. A response JSON
cannot represent — one carrying a `google.protobuf.Any` whose payload type the
server never described — is shown in protobuf's text format instead, labelled
as such, rather than reported as a failed call.

```
Flags:
  -config string      read settings from this file; empty skips it
                      (default "$XDG_CONFIG_HOME/grpctui/config.yaml")
  -log-file string    write logs to this file; empty disables logging
                      (default "$XDG_STATE_HOME/grpctui/grpctui.log")
  -log-level string   log level: debug, info, warn, error (default "error")
  -call-timeout d     give up on a single call after this long (default 1m0s)
  -version            print the version and exit
```

The target must serve the [gRPC server reflection API][reflection]. In Go, that
is one line on the server:

```go
import "google.golang.org/grpc/reflection"

reflection.Register(srv)
```

grpctui connects in plaintext only. TLS and mTLS arrive in v0.4.

[reflection]: https://github.com/grpc/grpc/blob/master/doc/server-reflection.md

## Configuration

Everything works with no configuration at all. If you point grpctui at the same
server every day, put its address in
`$XDG_CONFIG_HOME/grpctui/config.yaml` (`~/.config/grpctui/config.yaml`):

```yaml
# The address to connect to when none is given on the command line.
target: localhost:50051
```

A target argument always beats the file, so `grpctui other.example:443` still
does what it says.

Nothing about the file is guessed at. The default path may be absent — that is
the zero-config case — but a path you name with `--config` has to exist, and an
unknown key is an error. A typo is never silently ignored, whether it is in the
filename or inside the file.

## Request fields

The request form is generated from the method's input message. v0.2 fills in
scalars — `string`, the integer and floating-point families, `bool`, `bytes`
(base64) and enums, which take either a value name (`STATUS_SERVING`) or its
number.

Fields with a shape the form cannot edit yet — nested messages, repeated
fields, maps and `oneof` members — are still listed, marked with the version
that brings them (v0.3).

A field you never touch is left unset rather than sent as an explicit default.
Clearing one you have typed into is different: for a field with explicit
presence (proto3's `optional`, or any proto2 field) that sends an explicit
empty value, which is otherwise impossible to express. The same goes for
`space` on a `bool` — toggling it off sends an explicit `false`, where never
toggling it sends nothing at all.

Those fields say which state they are in: `unset` for one that will not be
sent, `""` for one that will be sent empty.

## Keybindings

| Key | Action |
| --- | --- |
| `↑`/`k`, `↓`/`j` | Move the cursor |
| `ctrl+u`, `ctrl+d` | Page up / down |
| `g`, `G` | Jump to top / bottom |
| `→`/`l`, `←`/`h` | Expand / collapse a service |
| `L`/`⇧→`, `H`/`⇧←` | Scroll the response left / right |
| `enter` | Toggle a service, select a method, or edit a field |
| `space` | Toggle a `bool` field |
| `ctrl+s` | Send the request |
| `esc` | Stop editing a field, or cancel a call in flight |
| `tab`, `shift+tab` | Switch panel |
| `r` | Retry after a failed connection |
| `?` | Toggle the full help |
| `q` | Quit |
| `ctrl+c` | Quit, even mid-edit |

While a field is being edited every key is a character — `q` types a `q`. Only
`ctrl+c`, `ctrl+s` and the panel switches keep their meaning.

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
| `internal/protoschema` | Descriptor → form-field tree; values → wire message |
| `internal/config` | `~/.config/grpctui/config.yaml` |
| `internal/ui` | bubbletea models, panels, keymap, styles |
| `cmd/grpctui` | Flags, config, `tea.Program` bootstrap |

The UI never imports `google.golang.org/grpc`, and the transport layer never
imports `bubbletea`. That separation is what makes the reflection/invoke core
testable without a terminal.

Logging goes to a file and never to stdout or stderr — a TUI owns the terminal,
and any stray write corrupts the render.

## Licence

[MIT](LICENSE) © Alon Shuldiner
