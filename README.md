# grpctui

A terminal UI for exploring, calling, and debugging gRPC services — `grpcui`,
but full-screen, keyboard-driven, and living in your terminal instead of a
browser tab.

Point it at a gRPC server with reflection enabled and it discovers the entire
API surface with zero configuration.

> **Status: v0.3 — Real Request Forms.** Discover a server, fill in a request
> form generated from the method's input message — nested messages, repeated
> fields, maps, `oneof` variants and enums included — send a unary call, and
> read the highlighted response. Metadata and TLS land in v0.4; streaming in
> v0.5.

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
response arrives as highlighted JSON in the panel below the form; a non-OK call
shows its gRPC status code and the server's message in the same place. A response JSON
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

The request form is generated from the method's input message, and it is a tree
rather than a list: a row either holds a value you type in or holds other rows
you open with `enter` (or `→`/`l`) and fold away again with `←`/`h`.

| Shape | How you fill it in |
| --- | --- |
| `string`, integers, floats, `bytes` (base64) | `enter` to edit, `esc` when done |
| `bool` | `space` toggles it |
| enum | open it and pick a value with `enter` or `space` |
| nested message | open it and fill in its fields |
| repeated field, map | `a` adds an item, `d` removes the one under the cursor |
| `oneof` | open it and pick a variant with `space`; typing into one picks it too |

`a` works from anywhere inside a list, not just on the list's own row, so a
dozen items are a dozen keystrokes rather than a walk back up each time.

Values are checked as each edit ends, so a typo is reported on the row where it
was made rather than when the call goes out. Sending checks the whole form and
reports every bad row at once — including proto2 `required` fields nobody
filled in.

A field you never touch is left unset rather than sent as an explicit default.
Clearing one you have typed into is different: for a field with explicit
presence (proto3's `optional`, or any proto2 field) that sends an explicit
empty value, which is otherwise impossible to express. The same goes for
`space` on a `bool` — toggling it off sends an explicit `false`, where never
toggling it sends nothing at all.

Those fields say which state they are in: `unset` for one that will not be
sent, `""` for one that will be sent empty.

The same problem turns up a level higher: an empty nested message is
indistinguishable from one nobody opened, so `space` on a message row sends it
anyway. An item you add to a repeated field is always sent, empty or not — you
added it on purpose.

## Keybindings

| Key | Action |
| --- | --- |
| `↑`/`k`, `↓`/`j` | Move the cursor |
| `ctrl+u`, `ctrl+d` | Page up / down |
| `g`, `G` | Jump to top / bottom |
| `→`/`l`, `←`/`h` | Expand / collapse a service or a request field |
| `L`/`⇧→`, `H`/`⇧←` | Scroll the response left / right |
| `enter` | Toggle a service, select a method, edit a field, or open a field that holds others |
| `space` | Toggle a `bool`, pick an enum value or a `oneof` variant, send an empty message |
| `a`, `d` | Add / remove an item of a repeated or map field |
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
