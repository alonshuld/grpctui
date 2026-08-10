# Chapter 7 — State, Config and File Formats

Three files, three packages, one versioning scheme.

| File | Package | Written by | Read by | Meant to be |
|---|---|---|---|---|
| `~/.config/grpctui/config.yaml` | `internal/config` | the user, never grpctui | startup | hand-edited |
| `~/.local/state/grpctui/history.yaml` | `internal/requests` | every send | startup, `[`/`]` | never opened |
| `~/.config/grpctui/collections/*.yaml` | `internal/requests` | `S` | startup, `ctrl+r`, `grpctui run` | **committed** |

That last column drives everything else in this chapter.

## 7.1 XDG paths, and failing soft

```go
func DefaultPath() string {            // config
    dir := os.Getenv("XDG_CONFIG_HOME")
    if dir == "" {
        home, err := os.UserHomeDir()
        if err != nil { return "" }    // ← empty, not an error
        dir = filepath.Join(home, ".config")
    }
    return filepath.Join(dir, "grpctui", "config.yaml")
}
```

An undeterminable directory returns `""`, which every caller treats as "this
feature is off" rather than as a startup failure. *grpctui's whole pitch is that
it needs no configuration* (principle 1). The same shape appears for the state
dir (history, logs) and the collections dir.

Collections live under **config**, not state, *because they are the same kind of
thing as `config.yaml`: written by a person, meant to be kept*. History lives
under state because it is machine-managed.

## 7.2 Two syntaxes, deliberately distinct

This is one of the sharper design decisions in the project and a very good
interview answer.

| Syntax | Resolved | From | Purpose |
|---|---|---|---|
| `${VAR}` | once, at **load** | the process environment | get a secret *into* the config file without writing it there |
| `{{name}}` | at **send** | a `vars.Set` the user switches and adds to while running | make a saved request portable across environments |

> Sharing one syntax would mean a config file that could not say which of the
> two it meant.

They also differ in a way that matters for the credentials rule: `${VAR}` is
expanded *before* anything is recorded, so its value can end up in a live
`Auth`. `{{name}}` is expanded *at the wire*, and a saved request keeps the
reference — never the expansion.

Both refuse an unresolvable reference rather than substituting empty:

```go
// config.expand
if len(missing) > 0 {
    return out, fmt.Errorf("environment variable %s is not set", ...)
}
// vars.Set.Resolve
if len(missing) > 0 { return out, &UnsetError{Names: missing} }
```

> A bearer token that quietly becomes the empty string produces an
> authentication failure that looks like anything but a config problem.

And both report **every** missing name at once. `UnsetError` even pluralises:
*"a form filled in from another environment can refer to three variables that
are not here, and reporting them one send at a time is three sends."*

The `${VAR}` regex is deliberately **only the braced form**:

```go
var varPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
```

> A bare `$` is left alone, so that a password of `"p$ssw0rd"` survives being
> written down — which is more valuable here than matching the shell exactly.

The same instinct governs `{{name}}`: only the doubled brace counts, *so that a
request body carrying JSON, a Go template or a regex survives being typed into a
field*.

## 7.3 Config loading: strict, and forgiving in exactly two places

```go
func Load(path string) (Config, error)      // missing file → zero Config, no error
func LoadFile(path string) (Config, error)  // every failure is an error
```

The distinction is **not cosmetic**, and the doc comment records the bug that
proved it:

> "Missing is fine" applied to an explicit path is also what let `--config`
> accept a path that could not exist and carry on into the TUI — a shrug on one
> platform and an error on another, depending on whether the OS distinguishes
> `ENOTDIR` from `ENOENT`.

The default path is a *suggestion*; a path the user named by hand is a
*requirement*.

```go
dec.KnownFields(true)   // unknown keys are rejected
```

Plus one more forgiving case: an *empty* file decodes to `io.EOF`, which means
"nothing configured", not "broken".

### Validation at load, not at use

```go
cfg.validateNames()             // every profile named, no duplicates
cfg.validateEnvironmentNames()
cfg.validateThemeNames()
```

> A duplicate profile name makes `--profile` ambiguous, and finding that out on
> the third connection of the day is worse than finding it out at startup.

### Partial failure is carried, not returned

```go
func (c Config) Connections() (profiles []grpcclient.Profile, problems []error)
```

`problems[i]` is why `profiles[i]` cannot be dialled, or nil. A profile that
cannot be converted is **listed rather than dropped**:

> One unset environment variable would otherwise make the whole file unusable —
> a production token nobody exported would stop you reaching your own laptop —
> where what it should stop is connecting to production.

The problem is then raised at exactly two points: at startup for the profile
actually being opened, and in `startup.dialer` when the user *asks* for one of
the others. *That is the point at which the user has asked for it, and the point
at which the answer is useful.*

This "collect problems in a parallel slice, surface on use" pattern is used
identically for environments (`environs.problems`).

### The precedence ladder

```
command-line target  >  environment's target  >  profile's target  >  config target
command-line flags   >  the chosen profile's settings
-V bindings          >  the environment's own variables
```

Stated in `connections()`:

> The flags override the chosen profile rather than replacing it: `grpctui
> --profile staging --insecure` is a saved connection with verification turned
> off for one session, not a new connection that has lost its credentials.

And a nice piece of intent inference:

```go
for _, f := range []struct{ name string; set func() }{
    {flagCACert, ...}, {flagCert, ...}, {flagKey, ...},
    {flagServerName, ...}, {flagInsecure, ...},
} {
    if opts.given[f.name] { f.set(); p.Security.TLS = true }
}
```

> Naming a certificate, a CA or a server name **is** asking for TLS; requiring
> `--tls` beside them would only be a way to get an error message. Passing
> `--tls=false` with them is still refused, by `Security.Validate`, because
> there it is a contradiction rather than an omission.

The `opts.given` map — built from `fs.Visit` — is what distinguishes *"the user
asked for plaintext"* from *"the user said nothing about transport security"*.
Go's `flag` package gives you the zero value for both; only `Visit` tells them
apart.

## 7.4 History and collections: the same record, two reasons

```go
type Request struct {
    Name    string            `yaml:"name,omitempty"`     // collections only
    Method  string            `yaml:"method"`
    Kind    string            `yaml:"kind,omitempty"`     // display only
    Target  string            `yaml:"target,omitempty"`   // context, not instruction
    Profile string            `yaml:"profile,omitempty"`
    Headers []string          `yaml:"headers,omitempty"`  // NAMES ONLY
    Body    any               `yaml:"body,omitempty"`     // nested YAML keys
    Values  map[string]string `yaml:"values,omitempty"`   // {{refs}} by path
    SentAt  time.Time         `yaml:"sent_at,omitempty"`
}
```

`Body any` is the choice that keeps `internal/requests` free of protobuf. It is
the generic structure protobuf's JSON mapping decodes to, and it is written as
**ordinary nested YAML keys** rather than an escaped JSON string — *a body you
can read in a diff is most of what makes a collection worth committing*.

`Kind` is recorded **for display only**: *what a replay actually does is decided
by the live descriptor, not by this.* Same for `Target` and `Profile` — *context
for the user reading a list, not instructions*.

### `History` is a working set, not an audit log

```go
const DefaultLimit = 200
```

> Past a couple of hundred entries nobody scrolls, and the file is rewritten on
> every send.

Consecutive identical sends **collapse**:

```go
if len(h.entries) > 0 && sameRequest(h.entries[0], r) { /* replace entry 0 */ }
```

> Pressing `ctrl+s` twice to retry a call that timed out should not push the
> request before it out of reach. Only the *immediately preceding* entry is
> compared — the same call made again after three others is a genuinely separate
> event.

`sameRequest` uses `reflect.DeepEqual` on the body, and the comment defends it:
*a body is a tree of maps, slices and scalars that a hand-edited collection can
put anything into — including a mapping with non-string keys, which YAML allows
and which `==` would panic on — and this runs once per send, where reflection
costs nothing worth avoiding.* Knowing *when* reflection is acceptable is as
useful as knowing it is slow.

### Atomic writes

```go
tmp, _ := os.CreateTemp(dir, ".*.tmp")
tmp.Chmod(0o600)
tmp.Write(body)
tmp.Close()
os.Rename(tmp.Name(), path)
```

> A process killed mid-write leaves the previous file intact rather than a
> truncated one. History is rewritten on every send; a crash during the tenth
> call of the day must not cost the first nine.

Temp file in the **same directory**, because `os.Rename` is only atomic within a
filesystem. `defer os.Remove(tmp.Name())` is a no-op once the rename succeeds and
the cleanup that matters when anything before it did not.

Permissions are `0700`/`0600`: *a history file records which services someone
has been calling, and a collection can be the shape of an internal API, so
neither is world-readable.*

### Collection saves rewrite one file

```go
// Only the one file is rewritten. The others are not grpctui's to touch, and a
// directory full of a team's collections should not be reformatted because
// somebody saved a request.
```

An entry whose name is taken is **replaced in place** — *saving over a request
you have just edited is the ordinary case, and two entries with one name would
make the second unreachable. Saving under a new name is therefore how you
duplicate one.*

And `ValidateName` refuses path separators outright rather than sanitising:

```go
case strings.ContainsAny(name, `/\`):
    return errors.New("a name may not contain a path")
```

> `"prod/../../.ssh/config"` silently becoming `"config"` is worse than being
> told no.

**Both** separators on **every** platform, *because a collection written on
Linux is read on Windows, where a backslash would suddenly be a directory.*

### Search

```go
func (r Request) Search() string   // lowercased name + method + flattened body + values
type Matcher struct{ terms []string }
```

Field *content* is searchable *because that is how you find a call again — you
remember the account id you passed, not that it was the fourth SayHello of the
afternoon.* Built **once** when entries change, never per keystroke: *folding a
hundred bodies to lowercase on every character typed is exactly the cost this
avoids.*

The `Values` map is folded in too, *so the one thing a user remembers about the
call — that it was the one using `{{tenant}}` — is not the one thing they could
not search for.*

Matching is AND-over-whitespace-separated-terms: `"sayhello alice"` finds the
SayHello that carried alice, *without caring which came first*.

## 7.5 `internal/format` — the compatibility promise

v1.0 promised the file formats keep working. This package is that promise in
code.

```go
const Current Version = 1
const First   Version = 1
type Version int   // 0 means "absent", not "invalid"
```

**One number shared by all three files**, not one per file:

> They are versioned together because they are released together: three numbers
> to reason about would be three chances to get a compatibility claim wrong, and
> there is no world in which a user has the collection format from one release
> and the config format from another.

### The four rules

1. **Absent version means `Current`.** Every file written before v1.0 keeps
   working untouched, and a user who never types the key never has to know it
   exists — which is most of them, since grpctui writes it for them (on save for
   a collection, on every send for history; never for config, which grpctui does
   not write).
2. A version this binary knows is read.
3. A **newer** version is refused, naming both numbers: *"upgrade grpctui"*.
4. A version below `First`, or not a number, is refused as malformed: *"which is
   not a version"*.

`UnsupportedError` carries both numbers *because the two directions need
different advice: a file from the future wants a newer grpctui, and a file with
a nonsense version in it wants an editor.*

### The non-obvious part: *when* the check runs

This is the best detail in the package and worth memorising.

Both readers decode with `KnownFields(true)`. **A file from a later grpctui is
made of keys this one has never heard of.** So the strict decode would fail on
the *key* rather than the *version*:

> Left to the strict pass, a config file written by the next release fails with
> `"field renderers not found"`, and the reader goes looking for a typo they did
> not make.

Hence `format.Check` is **a pass of its own over the same bytes, before the real
decode**, reading the version out of a `yaml.Node`:

```go
func Check(body []byte, path string) error {
    version, err := read(body)
    if err != nil { return fmt.Errorf("%s: %w", path, err) }
    return version.Check(path)
}
```

And a document it cannot parse at all yields **no error** — deliberately
swallowed with `//nolint:nilerr` and a reason:

> The caller's own decode is about to fail on this document with a message about
> the whole file, which is the better one. Reporting it here would report it
> twice, in the wrong words.

### Three spellings of zero

`UnmarshalYAML` distinguishes cases YAML itself conflates:

| Written | Read as | Why |
|---|---|---|
| (key absent) | absent → `Current` | every pre-v1.0 file |
| `version:` (nothing after) | **absent** → `Current` | *a line somebody started and did not finish. Refusing it would fail exactly the compatibility case this package exists to protect, over a zero the user never typed.* |
| `version: 0` | **error** | a written-down zero is a claim, and `Check` cannot tell it from an absent key |
| `version: 1.5` | error | *yaml decodes 1.5 into an int by truncating it, and a version silently read as 1 is worse than one refused* |
| `version: one` | error naming the key's purpose | yaml's own message names a Go type, which tells a person editing a config file nothing they can act on |

### The docs test

`docs/formats.md` is the promise in the user's language. Both `internal/config`
and `internal/requests` carry a `docs_test.go` that **walks the struct tags and
fails when the page does not name a key**. A nested block documented once
elsewhere opts out with a `prefix.*` line, *which reads correctly to a person
too*.

That is the same trick `flagGroups` uses (Chapter 9), for the same reason: the
only way a "complete reference" stays complete is if incompleteness fails a
test.

## 7.6 `internal/vars` — the runtime half

```go
type Set struct{ list []Variable }   // kept sorted by name
func (s Set) With(v Variable) Set    // returns a NEW Set
func (s Set) Without(name string) Set
func (s Set) Resolve(text string) (string, error)
func (s Set) Refers(text string) bool   // satisfies protoschema.Resolver
```

A leaf package: nothing of grpctui's is imported by it, *which is what lets
`internal/protoschema` and `internal/config` both depend on it without a cycle.*

Sorted *so that a list of variables and a golden file of one read the same on
every run*. A value, *so switching environment or capturing a value allocates
rather than mutating a set some in-flight command still holds*.

**It is never persisted.** The canonical capture is a login response's token.

### Switching environment replaces, never merges

```go
func (m *Model) installEnvironment() {
    env, _ := m.environments.Active()
    m.variables.SetVariables(env.Set(), env.Name)
    m.request.SetResolver(m.bindings())
}
```

> A value captured while pointed at staging is staging's, and carrying it into
> production is the single most expensive thing this feature could do.

An `Environment` carries a `Target` as well as variables, *because "staging"
usually means both a different host and a different account id, and having to
switch those separately is how a request meant for staging reaches production.*
Switching environment therefore re-dials when the target differs — keeping the
active profile's security and credentials, because **how** to connect is the
profile's business and **where** is the environment's.

### `ValidateName`

```go
var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
```

> The rule is the reference syntax's own: a name that `{{...}}` cannot spell is
> a variable nothing can refer to, so binding one is a quiet way to lose a value
> rather than a harmless oddity.

### `SplitAssignment`

```go
name, value, ok = strings.Cut(input, "=")
return strings.TrimSpace(name), value, true   // value NOT trimmed
```

> The value is taken verbatim after the first `=`, so a value may contain one;
> only the name is trimmed, since trailing space in a token is the kind of thing
> that costs an afternoon.
