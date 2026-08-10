# grpctui

A terminal UI for exploring, calling, and debugging gRPC services — `grpcui`,
but full-screen, keyboard-driven, and living in your terminal instead of a
browser tab.

Point it at a gRPC server with reflection enabled and it discovers the entire
API surface with zero configuration.

> **Status: v1.0 — Stable.** Discover a server, fill in a request form
> generated from the method's input message — nested messages, repeated fields,
> maps, `oneof` variants and enums included — send the headers a real service
> wants alongside it, over TLS or mTLS, switch between saved connections without
> restarting, and drive all three streaming shapes. Every call you make is
> remembered, and the ones worth keeping go into named collections you can
> commit. Write `{{variable}}` anywhere in a request, switch environments to
> change what it means, and capture a value out of one response to use in the
> next. When an answer surprises you, read the bytes it arrived as, diff it
> against the last one, or see where its time went — and put a passive proxy in
> front of somebody else's client to watch what it sends. Themes, a `.proto`
> fallback for servers without reflection, remappable keys, shell completions
> and a headless mode for CI round it off.
>
> The config file and the collection format are
> [documented and versioned](docs/formats.md) from here on: within a format
> version, a file that works today keeps working.

## Install

```bash
go install github.com/alonshuld/grpctui/cmd/grpctui@latest
```

Prebuilt binaries for Linux, macOS, and Windows are attached to each
[GitHub Release](https://github.com/alonshuld/grpctui/releases) — a single
static binary with no runtime dependencies, so unpacking one onto `$PATH` is the
second route.

The third is Docker — `alonshuld/grpctui`, `linux/amd64` and `linux/arm64`,
tagged with each version and `latest`:

```bash
docker run -it --rm --network host \
  -v ~/.config/grpctui:/config/grpctui \
  -v ~/.local/state/grpctui:/state/grpctui \
  alonshuld/grpctui localhost:50051
```

Three parts of that are load-bearing. `-it` gives the TUI a terminal, without
which it has nothing to draw on. `--network host` is what makes `localhost` mean
your machine rather than the container — on Docker Desktop, drop it and use
`host.docker.internal:50051` instead. The two mounts are what make config,
history and collections outlive the container; skip them and the container is a
scratch session, which for a one-off call against a remote target is often what
you want.

The image runs as root by default, so anything it writes into those mounts is
root-owned on the host. `--user "$(id -u):$(id -g)"` fixes that and still works:
the config and state paths are pinned with `XDG_CONFIG_HOME` and
`XDG_STATE_HOME` rather than derived from `$HOME`, precisely so that a container
running as an arbitrary uid still finds its files.

The image is a full Alpine userland with `vim`, `nano` and `less` in it rather
than a scratch or distroless base, so `docker run -it --entrypoint sh` gets you
a shell to edit a config in. Bear in mind that editing a config that is not
mounted only lasts as long as the container.

Headless replay works the same way and is the case a container is genuinely
better at, since it needs no terminal and no mounts beyond the collection:

```bash
docker run --rm --network host \
  -v "$PWD/smoke.yaml:/config/grpctui/collections/smoke.yaml" \
  alonshuld/grpctui run smoke -target localhost:50051
```

Then `grpctui <host:port>`, and press `?` for the keys.

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

There are four forms of the command:

```
grpctui [flags] <host:port>        explore a target interactively
grpctui run [flags] <collection>   replay a saved collection, no UI
grpctui keys                       print the keybinding reference, yours included
grpctui completion <shell>         print a completion script
```

`grpctui --help` prints every flag, grouped by what it is for. The ones you
reach for first:

```
  -profile string     connection profile to start on
  -env string         environment to start in
  -V name=value       variable, referred to as {{name}} in a request; repeatable
  -H key: value       request header; repeatable
  -tls                connect over TLS
  -cacert string      verify the server against this PEM bundle
  -cert / -key        PEM client certificate and key (mutual TLS)
  -insecure           accept any certificate the server offers
  -proto file         discover from this .proto instead of reflection; repeatable
  -import-path dir    resolve -proto imports against this directory; repeatable
  -theme name         auto, dark, light, or one your config file names
  -proxy address      accept gRPC traffic here and forward it, logging what passes
  -config string      read settings from this file; empty skips it
                      (default "$XDG_CONFIG_HOME/grpctui/config.yaml")
  -version            print the version and exit
```

By default the target must serve the [gRPC server reflection API][reflection].
In Go, that is one line on the server:

```go
import "google.golang.org/grpc/reflection"

reflection.Register(srv)
```

If it does not — production often does not — point grpctui at the `.proto`
files instead:

```bash
grpctui -proto api/v1/greeter.proto -import-path api localhost:50051
```

Both spellings work: a path on disk, or protoc's `-I` plus a name relative to
it. Well-known imports (`google/protobuf/timestamp.proto` and friends) resolve
without a copy on disk. Everything else behaves identically — the request form,
the raw-wire view and the invoker cannot tell where a descriptor came from. The
one difference is that the schema comes up even when the server is down, since
nothing is asked of it until you send.

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

If the servers you work with do not serve reflection, name the `.proto` files
once and stop passing `-proto`. The files describe an API rather than a
connection, so they sit at the top level and serve every profile:

```yaml
proto:
  files:
    - api/v1/greeter.proto
  import_paths:
    - api
```

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
with a header the current connection is not sending, the form says which. The
same rule is why a field filled in from a variable is written down as
`{{who}}` rather than as what it expanded to — see
[Variables and environments](#variables-and-environments).

A collection outlives the schema it was written against. A body naming a field
that has since been renamed is reported rather than skipped — a request quietly
sent without the field you thought you had set is the failure this whole feature
exists to prevent. So is a history or collection file that will not parse: it
stops startup instead of being silently replaced by the next send.

## Variables and environments

A collection is only portable if it is not hardcoded. Write `{{name}}` anywhere
in a request — a field of any type, a header value — and it is expanded on the
way to the wire and nowhere else:

```
name       string   hello {{who}}
times      int32    {{count}}
```

`e` opens the environment switcher. An environment is a named set of variables
and, optionally, the address they belong to: switching to `staging` changes both
what `{{tenant}}` means and which server hears about it, because having to
switch those separately is how a request meant for staging reaches production.

```yaml
environment: dev        # which one to start in; the first, if omitted

environments:
  - name: dev
    target: localhost:50051
    variables:
      tenant: local
      user_id: "1"

  - name: staging
    target: api.staging.example.com:443
    variables:
      tenant: acme
      user_id: "42"
```

Environments are a different axis from profiles. A profile says **how** to
connect — TLS, credentials, the headers that go with them — and an environment
says **what a request means**. Switching environment keeps the active profile
and only changes where it points, so one set of credentials serves every
environment that shares them.

`v` lists what is bound right now: `a` adds a binding, `enter` edits one, `d`
drops it. Nothing here is written to disk. `-V name=value` binds one from the
command line, for every environment, for the session.

**Chaining requests.** `ctrl+p` captures a value out of the last response into a
variable — the id a create call returned, the token a login call issued — using
the same path the request form shows:

```
name=path       user.id
                items[0].sku
                accessToken
```

The next request refers to it as `{{user_id}}`. It works from inside a
half-typed field, which is exactly when you want it.

A reference nothing binds refuses the send and says which name is missing, on
the row that used it. An `authorization: Bearer {{token}}` sent literally comes
back `Unauthenticated`, which is the most misleading answer a server can give.

**References survive being saved; their values never are.** A history entry and
a collection record `{{who}}`, not what it expanded to — so the same saved
request calls dev and prod, and a captured token stays out of every file. There
are two syntaxes here and they are deliberately distinct: `${VAR}` is the
process environment, read once when the config file loads, and `{{name}}` is a
grpctui variable, resolved when a request is sent.

## Debugging a response

Three keys change what the response panel shows, and the choice survives the
next call — if you pressed `D` to watch a field change across three sends, that
is what you meant.

- `w` — the raw wire: a field-by-field listing of the protobuf bytes as a
  decoder *without* a schema sees them, and a hex dump underneath. This is the
  view for when the decoded one is wrong: a field the server set that your
  descriptor does not have, a string that is not the string you expected.
- `D` — a diff against the previous response from the same method. "The bug is
  back" and "the bug moved" look nothing alike here.
- `X` — the request as an equivalent `grpcurl` command, for sharing outside
  grpctui. Header *names* are exported with placeholder values, the credential
  is named by kind rather than by value, and `{{variable}}` references are left
  unexpanded — a command meant to be pasted elsewhere is the last place a token
  should appear.

The status line under a response carries the latency breakdown gRPC itself
measured: how long connecting took, how long the server thought, how long the
answer took to arrive. A wall clock says a call took 800ms; this says whether
that was the service being slow or the service being far away.

Well-known values are glossed in place — a `google.protobuf.Timestamp` gets
`← 3 minutes ago` beside it, a `Duration` gets `← 1h30m0s`. The gloss never
replaces the value: the body stays exactly what the server sent, because the
case that matters most is the server whose timestamps are wrong. Switch one off
in the config file if you disagree:

```yaml
renderers:
  timestamp: false
```

## Watching somebody else's traffic

`--proxy` puts grpctui between an existing client and the server:

```bash
grpctui --proxy localhost:50052 localhost:50051
```

Point the application at `localhost:50052` and `t` shows every call that goes
past, with its status and timing. `enter` on one loads it into the request form,
so a call you did not write is a call you can now replay and edit. The proxy
adds nothing to the wire — no credential of its own, no rewritten metadata —
because a wire-watching tool that changed the wire would be worse than useless.

## Themes

`T` opens the theme switcher. Moving the cursor previews each one immediately,
because a terminal palette is judged by looking at it; `enter` keeps what you
are looking at, `esc` closes.

Three themes are built in. `auto` is the default and follows your terminal's
background; `dark` and `light` pin the half `auto` would have guessed, which is
the fix when a terminal behind tmux or ssh answers wrongly and half the status
bar goes invisible. `--theme dark` gets you through a session without editing
anything.

A theme is eight colours, and the config file can name its own:

```yaml
theme: midnight

themes:
  - name: midnight
    base: dark            # start from a built-in; omit for the default
    colors:
      primary: "#ff5fd7"
      error: "196"        # an ANSI index
      border: "#ccc/#333" # light/dark, following the terminal
```

The roles are `primary`, `secondary`, `muted`, `border`, `error`, `success`,
`text` and `inverted`; anything you leave out comes from the base. Taking a
built-in's name replaces it rather than adding a duplicate to the switcher, so
you can adjust one colour of `dark` without restating the other seven. A colour
that will not parse is an error at startup, not a theme that half works.

## Remapping keys

Every binding is addressable by name:

```yaml
keys:
  send: ctrl+g
  history-prev: "<"
  history-next: ">"
  traffic: ""        # unbind, and have the key back
```

Values are comma-separated keys in bubbletea's spelling (`ctrl+s`, `shift+tab`,
`enter`, `esc`, or a bare character). The `?` help bar documents whatever you
bound, so it stays truthful for free. Binding an action onto a key another one
already holds is refused at startup with both names — otherwise remapping
`send` to `q` would quietly cost you `quit`.

`grpctui __complete actions` lists every name.

## Running a collection in CI

`grpctui run` replays a saved collection with no UI at all, and exits non-zero
if anything failed:

```bash
grpctui run smoke -target api.staging.example.com:443 -env staging
```

```
✓ login      demo.v1.Auth.Login    41ms
✓ whoami     demo.v1.Auth.WhoAmI   12ms
✗ get-order  demo.v1.Orders.Get    NotFound: no such order

2 of 3 passed in 71ms against api.staging.example.com:443
```

Name one request to run it alone: `grpctui run smoke/login`. Add `-format json`
for something that parses the log.

Requests run in the order the file lists them, which is what lets a login come
before the call that uses its token, and every one is attempted — a smoke test
that stopped at the first problem would tell you about one thing when it could
have told you about four. What goes out is what the TUI would have sent: the
same form, the same `{{variable}}` resolution, the same transport. A failing
request prints its body; a passing one does not, because a CI log with a JSON
document per request is one nobody scrolls through.

Client-streaming and bidi calls are *skipped* rather than failed — a saved
request holds one message and those shapes are defined by a sequence — and a
skipped request does not fail the run. A server-streaming call is drained until
the stream ends or `-call-timeout` does.

No header name or value ever reaches either report.

## Shell completions

```bash
grpctui completion bash > /etc/bash_completion.d/grpctui
grpctui completion zsh  > ~/.zsh/completions/_grpctui
grpctui completion fish > ~/.config/fish/completions/grpctui.fish
```

Flags complete everywhere, and `-profile`, `-env` and `-theme` complete with
the names *your* config file defines — as does the collection argument to
`run`.

## Keybindings

The keys worth learning first are below. The complete reference — every binding,
grouped, with the name each one goes by in the config file — is
[docs/keybindings.md](docs/keybindings.md), generated from the keymap itself so
it cannot fall behind it.

If you have remapped anything, read your own instead:

```bash
grpctui keys
```

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
| `e` | Open the environment switcher |
| `v` | Open the variables list |
| `ctrl+p` | Capture a value from the last response into a variable |
| `w` | Toggle the raw wire view of the response |
| `D` | Toggle the diff against the previous response |
| `X` | Show the request as a `grpcurl` command |
| `t` | Open the traffic log (with `--proxy`) |
| `T` | Open the theme switcher |
| `[`, `]` | Step back / forward through the requests already sent |
| `ctrl+r` | Open the saved-request browser |
| `S` | Save the current request into a collection |
| `/` | Filter, in the browser |
| `r` | Retry after a failed connection |
| `?` | Toggle the full help |
| `q` | Quit |
| `ctrl+c` | Quit, even mid-edit |

While a field is being edited every key is a character — `q` types a `q`. Only
`ctrl+c`, `ctrl+s`, `ctrl+e`, `ctrl+r`, `ctrl+p` and the panel switches keep
their meaning.

## Demos

[docs/demos](docs/demos) holds three [VHS](https://github.com/charmbracelet/vhs)
tapes, one per workflow worth seeing before you try it:

| Tape | What it records |
| --- | --- |
| `unary.tape` | Discovery with nothing configured, a generated form, a response |
| `streaming.tape` | A server-streaming call arriving live, and `esc` ending the watch |
| `collections.tape` | Saving a request, finding it in the browser, replaying it |

`make demos` records all three as GIFs, against a real server rather than a
script — so a demo that goes stale is regenerated rather than restaged. The
recordings are not committed: they are megabytes that go out of date every
release, and the tape they come from is four lines to read and always current.
See [docs/demos/README.md](docs/demos/README.md) for what recording them needs.

## Documentation

| Page | What is in it |
| --- | --- |
| [docs/formats.md](docs/formats.md) | The config, collection and history file formats, and the compatibility promise |
| [docs/keybindings.md](docs/keybindings.md) | Every keybinding and the name it is remapped by |
| [docs/demos](docs/demos) | The tapes the demo recordings are made from, and how to record them |

## Development

```bash
make hooks        # install the pre-push hook — do this first
make test         # go test ./...
make check        # everything CI enforces: fmt, vet, lint, build, race tests
make golden       # regenerate teatest golden files (review the diff!)
make docs         # regenerate docs/keybindings.md from the keymap
make demos        # re-record the README GIFs (needs vhs)
make run TARGET=localhost:50051
```

The codebase is four strictly one-directional layers — transport → domain → UI
→ main:

| Package | Responsibility |
| --- | --- |
| `internal/grpcclient` | Dial, TLS/mTLS, credentials, reflection discovery, dynamic invoke |
| `internal/protoschema` | Descriptor → form-field tree; values → wire message |
| `internal/protofiles` | `.proto` source → descriptors, when the target has no reflection |
| `internal/proxy` | Passive mode: a schema-free proxy that reports what goes past |
| `internal/config` | `~/.config/grpctui/config.yaml`, profiles, environments, themes, keys |
| `internal/requests` | Sent-request history and saved collections on disk |
| `internal/format` | The `version:` on every file grpctui reads, and what an unknown one means |
| `internal/vars` | Variables, environments, `{{name}}` interpolation |
| `internal/diff` | Line diff, for comparing one response against the previous |
| `internal/export` | A request rendered as an equivalent `grpcurl` command |
| `internal/render` | Response renderers: a gloss beside a well-known type |
| `internal/runner` | Headless replay of a collection, for CI |
| `internal/ui` | bubbletea models, panels, keymap, styles, themes |
| `cmd/grpctui` | Subcommands, flags, config, `tea.Program` bootstrap |

The UI never imports `google.golang.org/grpc`, and the transport layer never
imports `bubbletea`. That separation is what makes the reflection/invoke core
testable without a terminal.

Logging goes to a file and never to stdout or stderr — a TUI owns the terminal,
and any stray write corrupts the render.

## Licence

[MIT](LICENSE) © Alon Shuldiner
