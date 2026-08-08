# File formats

grpctui reads and writes three files. This page is the specification for all
three, and the compatibility promise that comes with them.

| File | Where | Written by |
| --- | --- | --- |
| Config | `~/.config/grpctui/config.yaml` | you |
| Collections | `~/.config/grpctui/collections/*.yaml` | you and grpctui |
| History | `~/.local/state/grpctui/history.yaml` | grpctui |

All three are YAML, and all three respect `$XDG_CONFIG_HOME` and
`$XDG_STATE_HOME` where those are set. The config file's path is overridable
with `-config`, the collections directory with `-collections`, and the history
file with `-history-file` — an empty value for either of the last two switches
that half off.

## The compatibility promise

Every one of these files carries a `version:` key naming the format it is
written in. The current format version is **1**.

Within a major format version:

- **A file keeps working.** A config file or collection that loads today loads
  in every later grpctui with the same format version.
- **New keys are optional.** A release may add a key; leaving it out gets you
  the behaviour you have now.
- **An existing key does not change meaning, change type, or disappear.**

Breaking any of that means the format version goes up, and this is what each
side does about it:

- **No `version:` key at all** reads as the current format. Every file written
  before v1.0 keeps working untouched, and nobody has to add the key by hand.
- **An older version this grpctui still reads** is read as that version.
- **A newer version** is refused, naming both numbers and telling you to
  upgrade. This is the entire point of the key: without it a file from a later
  release fails with `field retries not found`, and you go hunting for a typo
  you did not make.

```
grpctui: collection "/home/you/.config/grpctui/collections/smoke.yaml" is
written in format version 2 and this grpctui reads up to 1: upgrade grpctui
```

Unknown keys are otherwise an error. A silently ignored `methd:` is how a
request goes out without the header you thought you had written.

## What is never written down

A bearer token, a basic-auth password and a header value never reach a
collection or the history — only header *names* do. A request is recorded as it
was typed, `{{variable}}` references and all, never as what those expanded to.
Both files outlive the call by weeks, and a collection is meant to be committed.

The config file is the one place a credential may be written, and it should not
be: any value in it may be written as `${VAR}` and is replaced from the process
environment at load. A missing variable is an error rather than an empty
string — a bearer token that quietly becomes `""` produces an authentication
failure that looks like anything but a config problem.

---

## Config

`~/.config/grpctui/config.yaml`. Every key is optional; grpctui runs with no
config file at all.

```yaml
version: 1

# The connection used when no profile is chosen. A `host:port` given on the
# command line always wins over this.
target: localhost:50051

tls:
  enabled: true
  ca_cert: ~/certs/internal-ca.pem      # verify the server against this CA
  client_cert: ~/certs/client.pem       # mTLS: the certificate to present
  client_key: ~/certs/client-key.pem    # mTLS: its private key
  server_name: api.internal             # override the name verified in the cert
  insecure_skip_verify: false           # accept any certificate at all

auth:
  type: bearer                          # bearer | basic | none
  token: ${API_TOKEN}
  # type: basic
  # username: svc-reader
  # password: ${API_PASSWORD}

# Headers sent with every call on this connection, including reflection.
metadata:
  x-request-source: grpctui

# The profile to start on. Empty starts on the first one.
profile: staging

# Saved connections, cycled through with `p`. Each takes the same target, tls,
# auth and metadata keys as the top level. When both are present, the top-level
# settings become the first profile in the list.
profiles:
  - name: staging
    target: api.staging.example.com:443
    tls:
      enabled: true
    auth:
      type: bearer
      token: ${STAGING_TOKEN}
    metadata:
      x-tenant: acme

# The environment to start in. Empty starts in the first one.
environment: staging

# Variable sets that {{name}} references resolve against. Switching environment
# *replaces* the bindings rather than merging them: a value captured while
# pointed at staging is staging's.
environments:
  - name: staging
    target: api.staging.example.com:443  # overridden by a profile's own target
    variables:
      user_id: "42"
      tenant: acme

# The theme to draw in: the built-in `auto`, `dark` or `light`, or one named
# below.
theme: nord

# Palettes offered alongside the built-in ones. `base` is the theme this one
# starts from, so a theme that adjusts one colour is three lines. Taking a
# built-in's name replaces it rather than adding a second entry under that
# label.
themes:
  - name: nord
    base: dark
    colors:
      accent: "#88c0d0"
      error: "#bf616a"

# Keybinding overrides, from an action name to a comma-separated list of keys.
# An action nobody names keeps its built-in keys; an empty value unbinds it.
# `grpctui keys` prints every action name and what it is currently bound to.
keys:
  send: ctrl+enter
  requests: ctrl+r,f3
  traffic: ""

# .proto sources to discover from instead of asking the target for reflection.
# The files describe an API rather than a connection, so this is top-level: one
# schema serves the dev, staging and production profiles of one service.
proto:
  files:
    - api/v1/greeter.proto
  import_paths:
    - api

# Response renderers, by name. Every renderer is on unless it appears here set
# to false.
renderers:
  timestamp: false
```

### Config keys

| Key | Type | Meaning |
| --- | --- | --- |
| `version` | int | Format version. Absent means the current one. |
| `target` | string | Default `host:port`. |
| `tls.enabled` | bool | Connect over TLS rather than plaintext. |
| `tls.ca_cert` | path | Verify the server against this CA instead of the system store. |
| `tls.client_cert` | path | Client certificate, for mTLS. |
| `tls.client_key` | path | Its private key. |
| `tls.server_name` | string | The name verified in the server's certificate. |
| `tls.insecure_skip_verify` | bool | Accept any certificate. For a broken staging box, not for production. |
| `auth.type` | string | `bearer`, `basic` or `none`. |
| `auth.token` | string | The bearer token. Write it as `${VAR}`. |
| `auth.username` / `auth.password` | string | Basic-auth credentials. Write the password as `${VAR}`. |
| `metadata` | map | Headers sent with every call on the connection. |
| `profile` | string | Which profile to start on. |
| `environment` | string | Which environment to start in. |
| `theme` | string | The palette to draw in. |
| `keys` | map | Keybinding overrides, action name to keys. `grpctui keys` lists the names. |
| `proto.files` | list of paths | `.proto` sources to discover from. |
| `proto.import_paths` | list of paths | Directories an `import` resolves against — protoc's `-I`. |
| `renderers` | map | Response renderers, name to on/off. |

A leading `~` is expanded in every path, and `${VAR}` in every value.

### Profiles

A profile is a saved connection. It takes the same four connection keys as the
top level, under a name.

| Key | Type | Meaning |
| --- | --- | --- |
| `profiles[].name` | string | Required, and unique. This is what `-profile` and the switcher name. |
| `profiles[].target` | string | The `host:port` to dial. |
| `profiles[].tls.*` | — | As `tls` above: `enabled`, `ca_cert`, `client_cert`, `client_key`, `server_name`, `insecure_skip_verify`. |
| `profiles[].auth.*` | — | As `auth` above: `type`, `token`, `username`, `password`. |
| `profiles[].metadata` | map | Headers sent on this connection. |

When both a top-level `target` and a `profiles` list are present, the top-level
settings become the first profile.

### Environments

An environment is what a `{{name}}` reference means. Switching one *replaces*
the bindings rather than merging them: a value captured while pointed at staging
is staging's, and carrying it into production is the most expensive thing this
feature could do.

| Key | Type | Meaning |
| --- | --- | --- |
| `environments[].name` | string | Required, and unique. What `-env` and the switcher name. |
| `environments[].target` | string | The address this environment implies, used when no profile or argument gives one. |
| `environments[].variables` | map | The bindings, name to value. A name must be one `{{...}}` could spell. |

### Themes

| Key | Type | Meaning |
| --- | --- | --- |
| `themes[].name` | string | Required. Taking a built-in's name (`auto`, `dark`, `light`) replaces it rather than adding a second entry under that label — which is how you adjust one colour of a built-in. |
| `themes[].base` | string | The theme this one starts from. Empty starts from the default. |
| `themes[].colors` | map | The colours to override, as hex or an ANSI number. |

---

## Collections

`~/.config/grpctui/collections/<name>.yaml`, one file per collection. The
collection's name is its filename without the extension. Both `.yaml` and
`.yml` are read; grpctui writes `.yaml`.

This is the file worth committing beside a project: `grpctui run <name>` replays
it in CI, and the request browser recalls from the same file.

```yaml
version: 1
requests:
  - name: login
    method: auth.v1.AuthService.Login
    kind: unary
    target: api.staging.example.com:443
    profile: staging
    headers:
      - authorization
      - x-tenant
    body:
      username: svc-reader
      scopes:
        - read
        - write
    values:
      tenant_id: "{{tenant}}"
    sent_at: 2026-08-08T09:14:02Z
```

| Key | Type | Meaning |
| --- | --- | --- |
| `version` | int | Format version. Absent means the current one. |
| `requests` | list | The saved requests, in the order they are shown and replayed. |
| `requests[].name` | string | Required in a collection. Saving over a name replaces that entry. |
| `requests[].method` | string | Fully-qualified method, `package.Service.Method`. |
| `requests[].kind` | string | `unary`, `server`, `client` or `bidi`. Display only — a replay uses the live descriptor. |
| `requests[].target` | string | Where it went. Context, not an instruction: a replay uses the open connection. |
| `requests[].profile` | string | Which profile it went out on. Context, as above. |
| `requests[].headers` | list of strings | Header **names** only. Never values. |
| `requests[].body` | mapping | The request message under protobuf's JSON mapping, written as nested keys. |
| `requests[].values` | map | `{{variable}}` references a protobuf field cannot hold, keyed by field path — `user.id`, `tags[1]`. |
| `requests[].sent_at` | timestamp | RFC 3339. |

Most `{{name}}` references need no `values` entry: a reference in a string field
is a perfectly good string and rides along in `body`. `values` holds only the
ones protobuf's JSON mapping would reject — a `{{id}}` in an `int64` field —
and those fields sit at their zero value in `body` until the reference is put
back.

grpctui only ever adds, replaces or removes a whole entry, and only in the one
file being saved to. A directory full of a team's collections is not reformatted
because somebody saved a request.

---

## History

`~/.local/state/grpctui/history.yaml`. Machine-managed: every send appends,
the oldest fall off at `-history-limit` (200 by default), and nobody is expected
to open it.

```yaml
version: 1
requests:
  - method: helloworld.Greeter.SayHello
    kind: unary
    target: localhost:50051
    body:
      name: world
    sent_at: 2026-08-08T09:14:02Z
```

Entries are newest first and take the same keys as a collection's, minus
`name` — the timestamp identifies an entry here. It is written atomically: a
process killed mid-write leaves the previous file intact rather than a truncated
one.

Deleting the file is a supported way to clear history, and `-history-file=`
turns persistence off for a session without losing it in memory.
