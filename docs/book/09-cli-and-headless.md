# Chapter 9 — Headless Mode and the CLI

## 9.1 Four forms of one command

```
grpctui [flags] <host:port>        explore a target interactively
grpctui run [flags] <collection>   replay a saved collection, no UI
grpctui keys                       print the keybinding reference, yours included
grpctui completion bash|zsh|fish   print a completion script
grpctui __complete <what>          hidden: the callback those scripts use
```

**They share one `registerFlags`.** The reasoning:

> A headless replay needs the same connection, environment and schema an
> interactive session does — and `keys` needs `-config` to mean what it means
> everywhere else, since the reference it prints is the *user's*, with their
> remapping applied.

`grpctui keys` **reports a config file it cannot build a keymap from rather than
falling back to the built-in one**: *a keymap that will not load is exactly what
somebody runs it to find out about.*

## 9.2 `splitCommand` — parsing that survives habit

Go's `flag` package stops at the first non-flag argument. That produces two
problems, and both were real bugs.

**Problem 1: the subcommand is not always `args[0]`.**

```go
func splitCommand(args []string) (string, []string)
```

> `grpctui run smoke` is the form the dispatch was first written for, and
> `grpctui -config ./ci.yaml keys` is the form somebody types after a day of
> putting `-config` first. Both name a subcommand; only the first has it in
> `args[0]`, and **reading only `args[0]` made the second one dial a host called
> "keys" and take over the terminal.**

The scan steps over flag values rather than examining them, *so `-profile run`
names a profile and not a subcommand*, and honours `--` (everything after it is
an argument by definition). The first bare word decides everything — *an address
genuinely spelled "keys" is not worth breaking `grpctui keys` for.*

**Problem 2: flags may follow the bare word.**

```go
// parseArgs parses repeatedly so flags may follow the bare word
```

`grpctui run smoke -format json` is what everyone types. Go's `flag` would stop
at `smoke` and leave `-format json` unparsed. `parseArgs` loops: parse, collect
the bare word, parse the rest, repeat.

## 9.3 Help, exit codes and stdout discipline

```go
const (
    exitOK    = 0
    exitError = 1
    exitUsage = 2
)
```

```go
if wantsHelp(args) {
    printUsage(stdout, newFlagSet(commandName(args), &opts, stdout))
    return exitOK
}
```

**Help is answered before anything is parsed**, and goes to **stdout** with exit
**0**:

> A help page on stderr cannot be piped into a pager, and one that exits 2
> breaks `grpctui --help && …`.

`commandName(args)` picks the right page, because *`grpctui run` has a flag the
interactive command does not, and printing it under the wrong heading — or not
at all — is the sort of thing help pages are blamed for.*

**Exactly one startup failure is a *usage* error:**

```go
code := exitError
if err.Error() == missingTarget {
    opts.usage()
    code = exitUsage
}
```

> A missing target is the one startup failure that is a usage error: there is
> nothing wrong with the configuration, the user simply has not said where to
> connect. Everything else is a problem with something they wrote down, and the
> help page would only be in the way of it.

That distinction — *the user has not told me something* versus *the user told me
something wrong* — is the rule I settled on for choosing between exit 2 and exit
1, and it decides every case in the program.

`options.usage` is stored as a field for a reason worth noting: *the target may
come from the config file, so whether one is missing is only known after the
config has been read — by which point the flag set is out of scope.*

## 9.4 `flagGroups` — a help page that cannot rot

```go
var flagGroups = []flagGroup{
    {"Connecting",         []string{flagConfig, "target", flagProfile, flagEnv, "V", "H", "call-timeout"}},
    {"Transport security", []string{flagTLS, flagCACert, flagCert, flagKey, flagServerName, flagInsecure}},
    {"Schema",             []string{flagProto, flagImportPath}},
    {"Saved requests",     []string{flagCollections, flagHistoryFile, "history-limit"}},
    {"Appearance",         []string{flagTheme}},
    {"Diagnostics",        []string{"proxy", flagLogFile, "log-level", "version"}},
    {"Reporting",          []string{"format"}},
}
```

> Twenty-odd flags printed alphabetically is a list nobody reads to the end of;
> the same flags under six headings is a page somebody can find the one they
> want in.

**`flagGroups` is checked by a test against the flag set itself**, so a flag
added and not filed lands in "Other" and **fails** rather than quietly
disappearing off the bottom of the page.

This is the third instance of the same idea in the codebase — documentation
completeness enforced by a test that walks the source of truth:

| Source of truth | Generated / checked artefact | Test |
|---|---|---|
| the `flag.FlagSet` | grouped `--help` | `flags_test.go` |
| `Config` / `Request` struct tags | `docs/formats.md` | `docs_test.go` ×2 |
| `keys.KeyMap` struct | `docs/keybindings.md` | `internal/ui/keys/docs_test.go` |

## 9.5 Custom `flag.Value` types

```go
type headerList []string      // repeatable -H
type pathList   []string      // repeatable -proto / -import-path
type assignmentList []string  // repeatable -V
```

`assignmentList` is separate from `headerList` for two reasons, and both are
good:

**It validates on `Set`:**

```go
func (a *assignmentList) Set(value string) error {
    name, _, ok := vars.SplitAssignment(value)
    if !ok { return fmt.Errorf("want `name=value`, got %q", value) }
    if err := vars.ValidateName(name); err != nil { return err }
    ...
}
```

> A mistyped variable name is silent otherwise, since a reference to a name
> nothing binds is refused at the row rather than at startup, and by then the
> flag is long out of sight.

**Its `String()` prints names only:**

```go
func (a *assignmentList) String() string {
    // names only: a -V is as likely to carry a token as a -H is, and this ends
    // up in usage output and error messages.
}
```

The credentials rule reaching into a `flag.Value` implementation. `flag` calls
`String()` when printing defaults and in some error paths; a `-V token=hunter2`
would otherwise appear in a usage message.

## 9.6 Shell completions

Three hand-written scripts (bash, zsh, fish) plus a hidden callback:

> The scripts complete flag names **statically** — baked in from the same flag
> set the help page is built from — and call back into `grpctui __complete` for
> the things only grpctui knows: which profiles, environments, themes and
> collections *this user's config file* defines.

`__complete` is hidden *because it is an implementation detail of the scripts
and its output format is not a promise to anybody else.*

**The completion callback stays silent on a broken config file** rather than
printing an error into somebody's half-typed command line — and there is a test
for exactly that. That is a genuinely thoughtful failure mode: a completion
handler that writes to stderr mid-tab-completion produces a garbled prompt.

Why not cobra's generated completions: the static half is a table this project
already has, and the dynamic half — reading the user's own config — is
grpctui-specific either way. Cobra would replace the first half and leave the
second.

## 9.7 `internal/runner` — the CI smoke test

```go
func Run(ctx context.Context, client Client, out io.Writer, opts Options) (Report, error)
```

**The thesis:**

> What it runs is exactly what the TUI would have sent. A request is rebuilt
> through the same `protoschema.Form` the request panel uses, resolved against
> the same `vars.Set`, and invoked through the same transport interface — which
> is the point. **A second, simpler path that happened to send something
> slightly different would make a green CI run mean nothing.**

```go
func build(method, request, bindings) (proto.Message, error) {
    saved, _ := protoschema.DecodeBody(md, request.Body)
    form := protoschema.NewForm(md)
    form.SetResolver(bindings)
    form.Load(saved)
    form.LoadValues(request.Values)   // put the {{refs}} back
    return form.Build()               // expand them
}
```

> It goes through `protoschema.Form` rather than straight from `DecodeBody`
> because a saved request is a **template**: the body carries what protobuf's
> JSON mapping could hold, and `Values` carries the `{{name}}` references it
> could not.

`main` reinforces this at the level above:

> It is the same startup as the interactive command up to the point the terminal
> would be taken over: the same config file, the same profile selection, the
> same environment, the same compiled schema. A shortcut here would be a
> shortcut in what CI is checking.

### Four policy decisions

**1. Every request is attempted, including the ones after a failure.**

> A smoke test that stops at the first problem tells you about one thing when it
> could have told you about four, and the run is over in seconds either way.

**2. Client-streaming and bidi calls are *skipped*, not failed.**

```go
if method.ClientStreaming {
    result.Skipped = true
    result.Error = fmt.Sprintf("%s calls need a queue of messages, "+
        "which a saved request does not hold", method.Kind())
    return result
}
```

> A collection entry holds one message, and those shapes are defined by a
> *sequence*. Sending that single message and calling it a smoke test would be
> inventing a meaning the file never had.

And a skipped request does **not** fail the run: *a collection holding a
bidi-streaming call is not a broken deployment, and failing CI over it would
teach people to stop putting streaming calls in collections.* That is a
behaviour-design argument, not a correctness one, and it is the better kind.

**3. A server-streaming call is drained, bounded by the timeout.**

```go
// A watch stream never ends on its own, so the timeout is what ends it — and a
// timeout reached that way is reported as the failure it is. There is no honest
// alternative: a runner that decided a watch had gone on long enough and called
// it a pass would pass on a server sending nothing at all.
```

The request half is closed immediately after the one message, *because a
server-streaming call takes exactly one message and a server waiting for a
second one would hang.*

**4. `error` means the *run* failed; a `NotFound` is a *result*.**

```go
// The returned error is a failure of the run — discovery that did not answer, a
// report that could not be written — and never a request that came back
// NotFound. A call that reached the server and was refused is a result, which
// is what Report.Failed counts.
```

The exit code comes from `Report.Failed`, not from the returned error. Getting
this boundary right is what makes the tool composable in a pipeline.

### The report

```go
type Millis time.Duration
func (m Millis) MarshalJSON() ([]byte, error)
```

> A bare `time.Duration` marshals as its nanosecond count, which under a key
> called `took_ms` would be wrong by a factor of a million — and wrong in the
> direction that makes a fast run look slow to whatever is reading the log.

Fractions are kept (`0.342`, not `0`), and `UnmarshalJSON` exists *so that a
report round-trips*.

**Text output prints the body of failures only:**

> The run is a smoke test, and a log with eleven JSON documents in it is one
> nobody scrolls through; the body of the one that broke is the thing worth
> having, and `--format json` is there for everything else.

**The status glyph is a word as well as a colour** (`✓ ✗ –`) *because a CI log is
not a terminal.*

**A failed call is reported by its status *message* alone:**

```go
func reason(err error) string {
    if st, ok := grpcclient.StatusOf(err); ok && st.Message != "" { return st.Message }
    return err.Error()
}
```

> The line already carries the code and grpc-go's error text repeats it: `"rpc
> error: code = NotFound desc = unknown service"` beside a column that says
> NotFound is three quarters noise.

### And the credentials rule, again

> `grpctui run` resolves `{{token}}` for real, because a headless call has to
> authenticate, but **neither its text report nor its JSON one ever prints a
> header name or value** — `internal/runner`'s tests assert exactly that.

A CI log is *the most durable surface in the whole program*: it outlives the
session, the machine, and often the person who ran it. So this is the one place
where even header *names* are withheld, which is stricter than anywhere else.

### Testing it

> A fake client rather than bufconn: what is under test is the replay, the
> ordering, the report and the exit code, and `internal/grpcclient` is where the
> wire is tested.

The complement to the proxy's "bufconn, because a fake would tell you nothing".
Knowing which side of that line a given test sits on is the skill.

## 9.8 `grpctui keys` and the generated reference

The keymap grew a second view for v1.0:

- **`ShortHelp` / `FullHelp`** — the `?` bar, with a hard **100-cell budget**.
  Past it `bubbles/help` truncates the last column away, which *silently
  undocuments a keybinding*. `internal/ui/keys` has a test for it — the fix is
  to move a binding to another column or shorten a description, never to let the
  bar grow.
- **`keys.Reference`** — the complete listing, grouped by `referenceGroups` and
  checked against the struct **in both directions** (nothing missing, nothing
  invented).

It is a **method**, not a package function, *so `grpctui keys` prints the user's
own bindings rather than the built-in ones.*

Each action's page description lives in a separate `details` map rather than on
the binding, *because the bar's text is a label and the page's is a sentence*.

`docs/keybindings.md` is generated from it (`make docs`, i.e. `go test
./internal/ui/keys -update`), *which is the only way a "complete reference"
stays complete.*

## 9.9 Remapping — `keys.Apply`

```go
func (k KeyMap) Apply(overrides map[string]string) (KeyMap, error)
```

Action names are **derived from the struct's field names** by reflection —
`HistoryPrev` → `"history-prev"` — rather than listed in a table:

> A table would be a second place to remember, and the failure it invites —
> adding a binding and forgetting to make it remappable — is silent. The cost is
> that renaming a field renames a config key, which is a real compatibility
> surface; `keymap_test.go` pins the whole list so that such a rename fails a
> test rather than a user's config file.

I went back and forth on this one. A table is explicit and a reflected name is
not; but the table is a second place to remember, and forgetting it fails
silently. The reflection wins on the failure mode, and the test buys back the
explicitness.

**Nothing is applied unless everything can be:**

> A half-remapped keyboard is worse than an unremapped one, because the half
> that worked hides the half that did not.

**An empty value unbinds** — *the only way to get a key back that grpctui has
claimed and the user wants for something else* — and an unbound binding is
`SetEnabled(false)`, which also keeps it out of the help bar.

**Conflict detection is scoped to what the user changed:**

```go
if len(names) < 2 || !slices.ContainsFunc(names, func(n string) bool { return remapped[n] }) {
    continue
}
```

> grpctui's own defaults overlap in places where the two actions can never both
> be live — the same letter means one thing in a modal and another in a panel —
> and refusing those would be refusing the built-in keymap. What is worth
> catching is the case the user created: **binding send to `q`, and losing quit
> without being told.**

A naive "no two actions may share a key" check would have been wrong, and
noticing that is the interesting part.
