# Chapter 10 — The Credentials Rule

If you take one thing into an interview from this codebase, take this chapter.
It is a single security invariant, stated once, enforced on eleven different
surfaces, and re-established every time a version added a new one.

## 10.1 The rule

> A bearer token, a basic-auth password and a header value never reach a surface
> that outlives the call. Only their **kind** and header **names** do.

"A surface that outlives the call" is the operative phrase, and it is what makes
the rule mechanical rather than a matter of taste. The surfaces:

| Surface | Added in | Lives for | What it may carry |
|---|---|---|---|
| The log file | v0.1 | weeks | header *names*, sizes, auth *kind* |
| The status bar | v0.1 | a screen share | security *mode*, auth *kind*, variable *count* |
| The connection switcher | v0.4 | a screen share | profile label, `Auth.Describe()` |
| The history file | v0.6 | weeks | header *names* |
| A collection file | v0.6 | forever, in git | header *names*, `{{refs}}` unexpanded |
| The variables panel | v0.7 | a screen share | names; values are the point, so it is modal |
| A request body | v0.7 | in a collection | `{{token}}`, never its expansion |
| An exported grpcurl command | v0.8 | pasted anywhere | placeholders and the credential's *kind* |
| The traffic log | v0.8 | a session | payloads (somebody else's), no metadata logged |
| A CI text report | v0.9 | forever, in a build log | **not even header names** |
| A CI JSON report | v0.9 | forever, machine-parsed | **not even header names** |

The CI reports are strictest, and the reason is that a build log is the most
durable surface in the whole system: it outlives the session, the machine, and
often the person who ran it.

## 10.2 How each surface enforces it

### The log

```go
c.logger.Info("call succeeded",
    zap.String("target", c.target),
    zap.String("method", method.FullName),
    zap.Duration("took", took),
    zap.Int("request_bytes",  proto.Size(req)),   // size, not content
    zap.Int("response_bytes", proto.Size(resp)),
    zap.Strings("headers", md.Keys()),            // NAMES
)
```

`Metadata.Keys()` exists precisely for this:

> It exists for everything that records a call without being the call: the log,
> and since v0.6 the history and collection files. The names of the headers on a
> call are useful there, and the values — bearer tokens, API keys — must never
> be.

At dial time, the *kind* only:

```go
cfg.logger.Info("created client",
    zap.String("security", cfg.security.Mode()),   // "mTLS (unverified)"
    zap.String("auth",     cfg.auth.Describe()),   // "bearer" / "basic (alice)"
)
```

Every stream log line records `zap.Int("bytes", proto.Size(req))` and never the
message: *a request message is whatever the user typed into the form, and this
log outlives the session.*

And the proxy's own log:

> No payload and no metadata is logged either way — a proxied body holds whatever
> the observed application put in it.

### The status bar

```go
segments = append(segments, profile.Security.Mode())
if profile.Auth.Kind != grpcclient.AuthNone {
    segments = append(segments, profile.Auth.Describe())
}
if env, ok := m.environments.Active(); ok {
    segment := env.Name
    if n := m.bindings().Len(); n > 0 { segment += fmt.Sprintf(" %d vars", n) }
}
```

> It never renders a credential, only its kind. **A terminal is shared over a
> screen share more often than a config file is.**

That sentence is the threat model in one line, and it appears twice in the
codebase. The environment segment names the environment and *counts* its
variables without rendering one — *since v0.7 a variable can hold a token
captured out of a login response.*

### `Auth.Describe()` — the type-level guard

```go
func (a Auth) Describe() string {
    switch a.Kind {
    case AuthBearer: return "bearer"
    case AuthBasic:
        if a.Username != "" { return "basic (" + a.Username + ")" }
        return "basic"
    default: return "none"
    }
}
```

There is **no `String()` on `Auth`**, deliberately. If there were, `%v` on a
`Profile` would print the token. Every display path goes through `Describe()`,
which structurally cannot return a secret. Making the safe path the *only* path
is better than making it the default.

### The record files

```go
entry := requests.Request{
    Method:  req.Method.FullName,
    Headers: md.Keys(),        // names
    Body:    body,             // from req.Template — {{refs}} intact
    Values:  values,           // the refs a message could not hold
    ...
}
```

`Request.Headers` is `[]string`, not `map[string]string`. **The type itself
cannot hold a value.** That is the strongest form of enforcement available: not
a rule to follow but a shape that makes the mistake unrepresentable.

The body is `req.Template`, not `req.Request` — Chapter 5 §5.5. The whole
`Template`/`LoadValues` round trip exists for this.

The tests assert it directly:

> Every test that writes a record also asserts no header *value* reached the
> file, and since v0.7 that no expanded variable did either; those are the
> constraints most likely to be broken by accident, and the cheapest to pin.

### Recall restores shape, not credentials

```go
// Header values are deliberately not restored: the record never held any. What
// the record does hold — the names — is said on the notice line when the live
// connection is not already sending them, so that a call that came back
// Unauthenticated has an explanation rather than a mystery.
```

This is the part that turns a security constraint into a *usability* feature:
the tool cannot restore the header, so it tells you which one is missing.
`missingHeaders()` diffs the record's names against the live connection's and
says *"This was sent with authorization, x-tenant-id."*

### The export

The most outward-facing surface, so the strictest of the interactive ones:

```
# grpctui does not copy credentials out, so header values below are placeholders.
grpcurl \
  -plaintext \
  -H 'authorization: Bearer <token>' \
  -H 'x-tenant-id: <x-tenant-id>' \
  -d '{"id":"{{user_id}}"}' \
  localhost:50051 demo.v1.Users/Get
```

- Header names with `<name>` placeholders.
- The credential's kind *in the shape the header takes on the wire*, so the
  reader knows whether to write a token or a base64 pair.
- `{{refs}}` unexpanded.
- A preamble saying so, *so that a command pasted somewhere else is not mistaken
  for one that will run as it stands.*

An `authorization` header the connection is already filling in is rendered
**once**, from the credential, rather than twice.

### The CI report

```go
// Logger receives the same detail the TUI's does. Header names only, never
// their values.
```

…and the reports themselves carry neither. `internal/runner`'s tests assert
that no header name or value reached either format.

### The flag value

```go
func (a *assignmentList) String() string { /* names only */ }
```

Because `flag` calls `String()` when printing defaults and in some error paths,
and `-V token=hunter2` would otherwise land in a usage message.

## 10.3 Where credentials *are* allowed to live

Being able to say where the secret actually is, and why that is acceptable,
matters as much as the list of places it is not.

1. **In the process, in `grpcclient.Auth`**, attached to the connection via
   `PerRPCCredentials`. It must be there — it is going on the wire.
2. **In a `vars.Set`**, if captured out of a login response. Runtime only, never
   persisted, and replaced wholesale on environment switch.
3. **In the environment**, reached through `${VAR}` in the config file. This is
   the *recommended* place, and the whole reason `${VAR}` exists:

   > Secrets — bearer tokens, basic-auth passwords, API-key headers — belong in
   > the environment rather than in the file, so any value may be written as
   > `${VAR}`.

4. **In a config file the user chose to write it in.** grpctui does not stop
   this; it is the user's file. But it never *copies* it anywhere.

## 10.4 The two deliberate relaxations

An honest security story includes what you chose *not* to enforce.

**1. Credentials over plaintext are allowed.**

```go
func (perRPCAuth) RequireTransportSecurity() bool { return false }
```

gRPC's default is to refuse. grpctui deviates because *it exists to talk to
local servers and port-forwards, where plaintext plus a token is the normal
shape of a staging environment.* Refusing would mean refusing the most common
setup there is.

Compensating controls: a `logger.Warn` at dial, and the status bar showing the
connection is in the clear.

**2. `InsecureSkipVerify` is available.**

```go
// #nosec G402 -- InsecureSkipVerify is the user's own explicit toggle for
// self-signed environments; it is off unless asked for and the status bar
// says so while it is on.
```

Compensating control: `Security.Mode()` returns `"TLS (unverified)"` or `"mTLS
(unverified)"`, which the status bar shows continuously — you cannot forget it
is on.

Both are the same shape of answer: **name the safe default, explain the specific
use case that motivates the deviation, and describe the compensating control.**
That is how a security trade-off should be argued.

## 10.5 The adjacent rule: response bodies

The credentials rule has a sibling that is easy to miss:

```go
// responses is the last body each method answered with... It is memory only and
// never written down — a response body holds whatever the server chose to put
// in it, and grpctui's standing rule about surfaces that outlive a call applies
// to answers as much as to credentials.
```

The diff cache is never persisted. Neither is the traffic log. A response from
a `GetUser` call is somebody's personal data, and a debugging tool that
accumulated those on disk would be a liability.

## 10.6 How to talk about this in an interview

The strongest framing is the **progression**, because it shows the constraint
being *maintained* rather than merely *stated*:

> v0.4 introduced credentials, and the rule was: they never reach the log or the
> status bar. v0.6 added files that outlive the session, so `Request.Headers`
> became `[]string` — a type that cannot hold a value. v0.7 added variable
> expansion, which meant a request *body* could now contain a token, so records
> keep the `{{reference}}` and never its expansion — that is why
> `Form.Template` exists at all. v0.8 added an exported command, the most
> outward-facing surface there is, so that exports placeholders and names the
> credential's kind. v0.9 added a CI log, which is the most *durable* surface
> there is, so that withholds even header names. Each version added a surface,
> and each one had to re-establish the same invariant on it.

Then the closer:

> The enforcement is mostly *structural* rather than procedural. `Request.Headers`
> is `[]string`. `Auth` has a `Describe()` and no `String()`. `Metadata.Keys()`
> exists so that "record a call" and "make a call" use different accessors. Where
> that was not possible, there is a test — every test that writes a record asserts
> no value reached the file.
