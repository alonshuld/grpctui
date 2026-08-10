# Chapter 13 — Build, CI and Release Engineering

## 13.1 CI as a merge gate

```
gofmt -l . (must be empty) → go vet ./... → go mod tidy -diff
  → golangci-lint run → go build ./... → go test -race -coverprofile ./...
```

`main` is protected: **no direct pushes, no force-pushes, PRs only, green CI
required.**

### Two jobs, two shapes

**`static`** runs once on Linux: formatting, vet, module tidiness, lint. These
are platform-independent, so running them three times would be waste.

**`test`** runs a matrix: `{ubuntu, macos, windows} × {go 1.25, go 1.26}` — three
operating systems and the two most recent Go minors, six legs, `fail-fast:
false` so one failure does not hide the others.

**`-race` is on for every run, not just before a release tag.**

### Three details worth knowing

**`shell: bash` is set as a default, and there is a reason:**

> Windows runners default to PowerShell, which mangles `-flag=value` into
> separate arguments — `-coverprofile=coverage.out` reached go as a stray `.out`
> package pattern.

A real cross-platform CI bug with a one-line fix, and the fix is *documented at
the point of the fix*.

**`go mod tidy -diff` is its own step:**

> go.mod drift is invisible to every other gate here: `go get` adds a
> requirement without revising its `// indirect` marker, and the build, the
> linters and the whole test matrix are all perfectly happy with a stale one. **A
> released go.mod is cached immutably, so it is worth catching before a tag
> rather than after.**

That last clause is the argument. The module proxy caches `go.mod` forever; a
stale one cannot be fixed by editing, only by a new tag.

**The trigger avoids double runs:**

```yaml
on:
  push:         { branches: [main] }   # guards main itself
  pull_request: { branches: [main] }   # covers branches
```

> A branch with an open PR would otherwise run the whole matrix twice, once per
> trigger.

Plus `concurrency: cancel-in-progress`, so a force-push cancels the superseded
run.

**golangci-lint is pinned, not `latest`:**

> CI and `make check` must agree on what a lint failure is, and a silent upstream
> bump should not turn a green PR red.

## 13.2 The lint configuration as a design document

`.golangci.yml` enables ~45 linters across seven categories, and — more
interestingly — **disables sixteen with a written reason each.** That file is
worth reading as a statement of intent.

**Enabled and load-bearing:**

| Linter | What it protects |
|---|---|
| `errorlint` | `%w` vs `%v`, `errors.Is` vs `==` |
| `nilerr` | returning nil after checking a non-nil error |
| `forcetypeassert` | unchecked `x.(T)` |
| `containedctx` | a `context.Context` in a struct — flags the one deliberate case |
| `fatcontext` | a context created in a loop |
| `errcheck` with `check-type-assertions: true` | every ignored error is deliberate |
| `gosec` | the `#nosec` comments each carry a justification |
| `exhaustive` with `default-signifies-exhaustive` | a new `Kind` breaks the switches that must handle it |
| `modernize`, `intrange`, `exptostd` | keeps the code on current Go idiom (`for i := range n`, `slices`/`maps`) |
| `nolintlint` with `require-explanation` + `require-specific` | **a bare `//nolint` fails the lint** |

That last one is the meta-rule: every suppression in the repo names its linter
*and* gives a reason, because the config makes anything else an error.

**Disabled, in two groups:**

*Not applicable* — `sqlclosecheck`, `rowserrcheck`, `bodyclose`, `noctx`,
`sloglint`, `zerologlint`, `spancheck`, `protogetter`. Listed explicitly *so the
reason survives a config refresh* rather than being silently absent.

*Deliberate project choices* — `lll`, `wrapcheck`, `err113`, `mnd`,
`exhaustruct`, `varnamelen`, `gochecknoglobals`, `testpackage`. Plus
`paralleltest`, disabled because *the UI tests swap `os.Stdout` and share
lipgloss's global renderer, so they cannot run in parallel.*

**Two targeted exclusions:**

```yaml
- path: _test\.go
  linters: [dupl, funlen, goconst, gosec]
  # table-driven tests across packages look alike by construction
- path: internal/ui/model\.go
  linters: [gocyclo]
  # the bubbletea Update/View switch is a dispatch table; splitting it to
  # satisfy a complexity budget makes it harder to read, not easier
```

The second is what I settled on after trying both alternatives — raising
`gocyclo` globally, and splitting `Update` into artificial helpers. Neither was
better. This is scoped to one file, and it states *why the metric is wrong
here* rather than that the metric is inconvenient.

## 13.3 Versioning and release policy

**Releases are manually tagged, automatically published. CI never creates tags.**

```bash
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

Tags are `vMAJOR.MINOR.PATCH`, always `v`-prefixed (**Go modules require it**),
always annotated, and **never moved or deleted once pushed**:

> The module proxy caches them immutably, so a bad release is fixed by tagging
> the next patch, never by force-pushing the tag.

Pre-1.0, breaking changes bumped the *minor* version — which is semver's own
rule for 0.x. The roadmap versions map directly onto real tags.

### The `workflow_dispatch` escape hatch

```yaml
workflow_dispatch:
  inputs:
    tag: { description: "Existing tag to release, e.g. v0.2.0", required: true }
```

> Publishing a tag whose push never reached a runner would otherwise be
> impossible: tags are immutable, so it cannot be re-pushed to fire the trigger
> again, and there is no failed run to re-run. This is the way back — it releases
> a tag that already exists and **never creates one**.

And the subtlety that makes it work:

> Dispatch it from the **default branch**, not from the tag: GitHub reads the
> workflow definition from the ref it is dispatched against, and a tag cut before
> this trigger existed does not carry it. The `tag` input is what decides which
> commit is actually built.

`GORELEASER_CURRENT_TAG` is set from the input so *a dispatched release cannot
quietly build the wrong one*.

## 13.4 GoReleaser

```yaml
builds:
  - env: [CGO_ENABLED=0]
    goos:   [linux, darwin, windows]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X github.com/alonshuld/grpctui/internal/version.version={{ .Version }}
```

Six binaries. `CGO_ENABLED=0` is what makes them genuinely static (design
principle 4). `-s -w` strips the symbol table and DWARF info.

### Version embedding — the direction that matters

```go
func Version() string {
    if info, ok := debug.ReadBuildInfo(); ok {
        if v := info.Main.Version; v != "" && v != "(devel)" { return v }
    }
    return version   // the ldflags value, as a FALLBACK
}
```

> **ldflags are not applied when a user runs `go install`.** A `go install
> github.com/alonshuld/grpctui/cmd/grpctui@v0.1.0` build carries an accurate
> module version and no ldflags at all — reading them the other way round makes
> every such build report "dev".

Build info **wins**; ldflags fill in for builds that have neither (a local `go
build`, which reports `(devel)` and falls through to `"dev"`). I had this the
wrong way round first, and it is invisible until somebody installs from source
and reports the version as "dev".

### Homebrew: a cask, not a formula

```yaml
homebrew_casks:
  - name: grpctui
    repository: { owner: alonshuld, name: homebrew-tap, token: "{{ .Env.HOMEBREW_TAP_TOKEN }}" }
```

> It is a cask rather than a formula: **a formula for a pre-built binary is
> deprecated upstream**, and building from source is what `go install` is already
> for.

**The token problem, and the graceful degradation:**

> The tap is a *different repository*, which the release job's `GITHUB_TOKEN`
> cannot write to — that token is scoped to this one. `HOMEBREW_TAP_TOKEN` is a
> PAT with `contents:write` on the tap; **when it is absent the cask is built and
> not pushed, so a release still happens and the binaries still land.**

```yaml
env:
  - HOMEBREW_TAP_TOKEN={{ if index .Env "HOMEBREW_TAP_TOKEN" }}...{{ end }}
skip_upload: '{{ if .Env.HOMEBREW_TAP_TOKEN }}auto{{ else }}true{{ end }}'
```

The empty default is declared because **GoReleaser fails a template that reads
an environment variable which is not there at all**, which would turn "no tap
token" into "no release". Degrading one artefact rather than failing the whole
release is the right shape, and the release notes say which happened.

**The macOS quarantine hook:**

```ruby
system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", ...]
```

> The binaries are not notarised, so macOS quarantines them and the first run is
> a dialog rather than a TUI. Stripping the attribute is the documented
> workaround where signing is not on the table; **it is deliberately the only
> thing the cask does to the machine.**

An honest answer to "why not notarise": it needs a paid Apple Developer account
and a signing pipeline. The trade-off is stated rather than hidden.

**`skip_upload: auto`** also keeps a prerelease from becoming what `brew install
grpctui` gives you.

### Changelog from conventional commits

```yaml
changelog:
  use: github
  groups:
    - { title: Features,    regexp: '^feat(\(.+\))?!?:', order: 0 }
    - { title: Fixes,       regexp: '^fix(\(.+\))?!?:',  order: 1 }
    - { title: Performance, regexp: '^perf(\(.+\))?!?:', order: 2 }
    - { title: Other,       order: 99 }
  filters:
    exclude: ['^docs:', '^test:', '^chore:', '^ci:']
```

The regexes handle the `!` breaking-change marker, which is why the commit
convention (`<type>(<scope>): <description>`, `!` + `BREAKING CHANGE:` footer)
is enforced on PR titles: **the changelog generator keys off both.**

Scope is the package or panel touched — `grpcclient`, `protoschema`, `ui/form`,
`config`, `ci` — which makes the generated changelog navigable.

## 13.5 The distribution matrix

| Route | Mechanism | Version source |
|---|---|---|
| `go install .../cmd/grpctui@latest` | `proxy.golang.org` picks up the tag | `debug.ReadBuildInfo` |
| GitHub Release binaries | GoReleaser, 6 platform/arch pairs | ldflags |
| `brew install alonshuld/tap/grpctui` | cask in a separate tap repo | ldflags |

Three routes, one build, no private registry. `proxy.golang.org` picks up the
tag on first fetch — nothing has to be published to it.

## 13.6 The Makefile

`make check` is the local mirror of the full CI gate:

```make
check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	$(GO) vet ./...
	golangci-lint run
	$(GO) build ./...
	$(GO) test -race ./...
```

Plus the generation targets, each of which regenerates a committed artefact:

```make
golden:  go test ./internal/ui -update        # teatest frames
docs:    go test ./internal/ui/keys -update   # docs/keybindings.md
demos:   vhs docs/demos/*.tape                # README GIFs
hooks:   cp scripts/hooks/pre-push .git/hooks/
snapshot: goreleaser release --snapshot --clean
```

**`make demos` uses `vhs`** (charm's terminal recorder), and the `.gitignore`
records the policy: **the `.tape` is committed, the GIF is not** — *a GIF is
megabytes that go stale every release.* Recording a demo from a script rather
than by hand means the demo can be re-recorded after a UI change instead of
silently becoming a lie.

`make help` is a self-documenting target that greps `## ` comments out of the
Makefile itself.

## 13.7 What a reviewer should notice

Three things about this setup that generalise beyond grpctui:

1. **Every gate states what it deliberately omits.** The pre-push hook omits
   `-race` and lint *to stay under a few seconds*, and says so, and says CI is
   the authority. That is a designed trade-off rather than an accident.

2. **Every workaround carries the bug it fixes.** `shell: bash` carries the
   PowerShell argument-mangling story. The `HOMEBREW_TAP_TOKEN` empty default
   carries "GoReleaser fails a template that reads a missing env var". A
   workaround without its reason gets deleted by the next person.

3. **Immutability is respected everywhere it exists.** Tags are never moved
   because the proxy caches them. `go.mod` is checked before a tag because a
   released one is cached forever. A bad release is fixed by the next patch. That
   is the correct mental model for anything published to a content-addressed
   store, and it is the source of several otherwise-odd-looking decisions.
