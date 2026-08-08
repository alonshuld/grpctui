# grpctui

A terminal UI for exploring, calling, and debugging gRPC services — `grpcui`,
but full-screen, keyboard-driven, and living in your terminal instead of a
browser tab.

Point it at a gRPC server with reflection enabled and it discovers the entire
API surface with zero configuration.

> **Status: v0.6 — History & Collections.** Discover a server, fill in a request
> form generated from the method's input message — nested messages, repeated
> fields, maps, `oneof` variants and enums included — send the headers a real
> service wants alongside it, over TLS or mTLS, switch between saved connections
> without restarting, and drive all three streaming shapes. Every call you make
> is remembered, and the ones worth keeping go into named collections you can
> commit. Variables and environments land in v0.7.

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
  -profile string     connection profile to start on
  -tls                connect over TLS
  -cacert string      verify the server against this PEM bundle
  -cert string        PEM client certificate to present (mutual TLS)
  -key string         PEM key for -cert
  -servername string  name to check the certificate against
  -insecure           accept any certificate the server offers
  -H key: value       request header; repeatable
  -config string      read settings from this file; empty skips it
                      (default "$XDG_CONFIG_HOME/grpctui/config.yaml")
  -log-file string    write logs to this file; empty disables logging
                      (default "$XDG_STATE_HOME/grpctui/grpctui.log")
  -log-level string   log level: debug, info, warn, error (default "error")
  -call-timeout d     give up on a single call after this long (default 1m0s)
  -history-file str   record sent requests here; empty keeps them for the session
                      (default "$XDG_STATE_HOME/grpctui/history.yaml")
  -history-limit n    how many sent requests to keep (default 200)
  -collections dir    read and write saved collections here; empty disables saving
                      (default "$XDG_CONFIG_HOME/grpctui/collections")
  -version            print the version and exit
```

The target must serve the [gRPC server reflection API][reflection]. In Go, that
is one line on the server:

```go
import "google.golang.org/grpc/reflection"

reflection.Register(srv)
```

[reflection]: https://github.com/grpc/grpc/blob/master/doc/server-reflection.md

## Headers, TLS and auth

Connections are plaintext unless you say otherwise, which is what a local
server almost always wants. Everything beyond that is opt-in:

```bash
grpctui -tls api.example.com:443                          # system trust store
grpctui -cacert ./ca.pem api.internal:443                 # a private CA
grpctui -cert ./client.pem -key ./client-key.pem api:443  # mutual TLS
grpctui -insecure staging.internal:443                    # accept anything
grpctui -H 'authorization: Bearer abc' localhost:50051    # one-off headers
```

Naming a certificate, a CA or a server name implies `-tls`; you never have to
pass both. `-insecure` turns off certificate *and* hostname verification, so
the connection is encrypted but no longer authenticated — the status bar says
`TLS (unverified)` for as long as it is on.

The **Headers** panel holds the metadata sent with every RPC, including
reflection: a server that gates its API behind a header gates its schema behind
the same one. `tab` into it, `a` adds a header, `h`/`l` move between the name
and the value, `enter` edits, and `space` parks a header without deleting it —
so you can find out whether the `authorization` header was the problem without
retyping the token afterwards. It appears when it has something to show.

Bad headers are caught where they are typed: `grpc-`-prefixed names are
reserved by the protocol, a `-bin` value has to be base64, and a call is
refused rather than sent with a header the server would reject.

## Connections

`p` opens the connection switcher: the saved `host:port` + TLS + auth
combinations from your config file. `enter` connects, which re-runs discovery
and swaps in that connection's headers. Picking the one you are already on
reconnects, which is how you recover a connection the server dropped.

Credentials are never rendered — the switcher and the status bar say `bearer`
or `basic (alice)`, never the token — and the log file records header *names*
and message sizes, never their contents.

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

Once you have more than one server, name them. Each profile is a whole
connection — where it is, how it is protected, who you are on it, and what
headers ride along — and `p` moves between them without restarting:

```yaml
profile: dev            # which one to start on; the first, if omitted

profiles:
  - name: dev
    target: localhost:50051
    metadata:
      x-tenant: acme

  - name: staging
    target: api.staging.example.com:443
    tls:
      enabled: true
      ca_cert: ~/certs/staging-ca.pem
      server_name: api.staging.internal   # if not the address dialled
    auth:
      type: bearer
      token: ${STAGING_TOKEN}

  - name: prod
    target: api.example.com:443
    tls:
      enabled: true
      client_cert: ~/certs/client.pem     # mutual TLS
      client_key: ~/certs/client-key.pem
    auth:
      type: basic
      username: alice
      password: ${PROD_PASSWORD}
```

**Secrets belong in the environment, not in the file.** Any value may be written
as `${VAR}` and is replaced at load time; a variable that is not set is an error
rather than an empty token, because an empty token fails in a way that looks
like anything but a config problem. Only the braced form is a reference, so a
password containing a literal `$` survives being written down.

`--profile` picks the connection to start on, and the other flags override that
one profile for the session: `grpctui --profile staging --insecure` is your
saved staging connection with verification off for one run, not a new connection
that has lost its credentials.

Nothing about the file is guessed at. The default path may be absent — that is
the zero-config case — but a path you name with `--config` has to exist, and an
unknown key is an error. A typo is never silently ignored, whether it is in the
filename or inside the file. Profiles must be named, and two with the same name
is an error rather than a coin toss.

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

## History and collections

Every request you send is recorded. `[` steps back through them and `]` forward,
filling the form in from each — the tree moves to the method too, so the whole
screen agrees about what you are looking at. `ctrl+s` sends whatever is on
screen, so re-running yesterday's call is two keystrokes.

`ctrl+r` opens the browser: history and the saved collections in one list.
It works from inside a half-typed field, which is when you most often want it.

| Key | In the browser |
| --- | --- |
| `enter` | Load the request into the form |
| `ctrl+s` | Load it and send it |
| `/` | Filter; `enter` keeps the query, `esc` abandons it |
| `esc` | Clear the filter, or close the browser |

The filter matches method names, saved names, the source column, and **field
content** — which is how you actually find a call again, since you remember the
account id you passed rather than that it was the fourth `SayHello` of the
afternoon. Every whitespace-separated term has to match, in any order.

`S` saves whatever is in the form into a collection, under
`collection/name` — a bare name goes to `default`. Saving over a name replaces
that entry, so saving under a new one is how you duplicate a request you have
just edited. A form with errors shows them instead of opening the prompt.

History lives in `$XDG_STATE_HOME/grpctui/history.yaml` and is capped; nobody is
meant to open it. Collections are yours:

```yaml
# ~/.config/grpctui/collections/team.yaml
requests:
  - name: greet-alice
    method: demo.v1.Greeter.SayHello
    kind: unary
    headers:
      - x-tenant
    body:
      name: alice
      times: 2
```

The body is ordinary nested YAML rather than an escaped JSON blob, so a
collection diffs cleanly and belongs in a repository beside the service it
calls. grpctui only ever adds or replaces a whole entry, and only in the one
file you saved to — a comment you wrote above a request survives being edited
from the TUI.

**A record carries header names and never header values.** A history file sits
in your state directory for weeks and a collection is meant to be committed, so
neither is somewhere a bearer token may end up. Recalling a request therefore
restores its shape and leaves the credentials to the connection; if it went out
with a header the current connection is not sending, the form says which.

A collection outlives the schema it was written against. A body naming a field
that has since been renamed is reported rather than skipped — a request quietly
sent without the field you thought you had set is the failure this whole feature
exists to prevent. So is a history or collection file that will not parse: it
stops startup instead of being silently replaced by the next send.

## Keybindings

| Key | Action |
| --- | --- |
| `↑`/`k`, `↓`/`j` | Move the cursor |
| `ctrl+u`, `ctrl+d` | Page up / down |
| `g`, `G` | Jump to top / bottom |
| `→`/`l`, `←`/`h` | Expand / collapse a service or a request field |
| `L`/`⇧→`, `H`/`⇧←` | Scroll the response left / right |
| `enter` | Toggle a service, select a method, edit a field, or open a field that holds others |
| `space` | Toggle a `bool`, pick an enum value or a `oneof` variant, send an empty message, park a header |
| `a`, `d` | Add / remove an item of a repeated or map field, or a header |
| `ctrl+s` | Send the request, or load and send the one under the cursor in the browser |
| `ctrl+e` | Close the sending half of a stream |
| `esc` | Stop editing a field, cancel a call in flight, or close a modal |
| `tab`, `shift+tab` | Switch panel |
| `p` | Open the connection switcher |
| `[`, `]` | Step back / forward through the requests already sent |
| `ctrl+r` | Open the saved-request browser |
| `S` | Save the current request into a collection |
| `/` | Filter, in the browser |
| `r` | Retry after a failed connection |
| `?` | Toggle the full help |
| `q` | Quit |
| `ctrl+c` | Quit, even mid-edit |

While a field is being edited every key is a character — `q` types a `q`. Only
`ctrl+c`, `ctrl+s`, `ctrl+e`, `ctrl+r` and the panel switches keep their
meaning.

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
| `internal/grpcclient` | Dial, TLS/mTLS, credentials, reflection discovery, dynamic invoke |
| `internal/protoschema` | Descriptor → form-field tree; values → wire message |
| `internal/config` | `~/.config/grpctui/config.yaml`, connection profiles |
| `internal/requests` | Sent-request history and saved collections on disk |
| `internal/ui` | bubbletea models, panels, keymap, styles |
| `cmd/grpctui` | Flags, config, `tea.Program` bootstrap |

The UI never imports `google.golang.org/grpc`, and the transport layer never
imports `bubbletea`. That separation is what makes the reflection/invoke core
testable without a terminal.

Logging goes to a file and never to stdout or stderr — a TUI owns the terminal,
and any stray write corrupts the render.

## Licence

[MIT](LICENSE) © Alon Shuldiner
