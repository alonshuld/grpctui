package keys_test

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/ui/keys"
)

// update regenerates docs/keybindings.md, the same way internal/ui regenerates
// its golden frames:
//
//	go test ./internal/ui/keys -update
var update = flag.Bool("update", false, "rewrite docs/keybindings.md from the keymap")

const docsPath = "../../../docs/keybindings.md"

// TestKeybindingDocs checks docs/keybindings.md against the keymap it is
// generated from.
//
// The page is generated rather than written because v1.0 promised a *complete*
// reference, and a hand-written table is complete exactly once. Regenerating it
// is one flag; noticing that it went stale, without this test, is nobody's job.
func TestKeybindingDocs(t *testing.T) {
	want := keybindingDocs()

	if *update {
		require.NoError(t, os.WriteFile(docsPath, []byte(want), 0o600))
		return
	}

	got, err := os.ReadFile(docsPath)
	require.NoError(t, err)
	assert.Equal(t, want, string(got),
		"docs/keybindings.md is out of date — regenerate it with: go test ./internal/ui/keys -update")
}

// keybindingDocs renders the page: the built-in keymap, since a committed
// document cannot depend on whoever's config file happened to be on the machine
// that generated it.
func keybindingDocs() string {
	var b strings.Builder

	b.WriteString(docsPreamble)
	for _, g := range keys.Default().Reference() {
		fmt.Fprintf(&b, "\n## %s\n\n| Key | Action | What it does |\n| --- | --- | --- |\n", g.Title)
		for _, a := range g.Actions {
			fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", a.String(), a.Name, a.Detail)
		}
	}
	b.WriteString(docsRemapping)

	return b.String()
}

const docsPreamble = `<!--
Generated from internal/ui/keys. Do not edit by hand: run

    go test ./internal/ui/keys -update
-->

# Keybindings

Every key grpctui binds, what the config file calls it, and what it does.
` + "`grpctui keys`" + ` prints the same table with your own remapping applied, which is
the one to trust if you have edited any of them.

The ` + "`?`" + ` bar inside grpctui is the short version — the keys worth learning
first. This is all of them.
`

const docsRemapping = `
## Remapping

The middle column is the action's name. Bind one to different keys under
` + "`keys:`" + ` in ` + "`~/.config/grpctui/config.yaml`" + `, as a comma-separated list.
An empty value unbinds the action, which is how you get a key back that grpctui
has claimed and you want for something else:

` + "```yaml" + `
keys:
  send: ctrl+enter
  requests: ctrl+r,f3
  traffic: ""
` + "```" + `

Keys are spelled the way bubbletea spells them: ` + "`enter`" + `, ` + "`esc`" + `,
` + "`tab`" + `, ` + "`space`" + `, ` + "`up`" + `, ` + "`pgdown`" + `, ` + "`ctrl+s`" + `,
` + "`shift+tab`" + `, or a bare character.

Two actions sharing a key is refused at startup when at least one of them was
remapped — grpctui's own defaults overlap in places where the two can never both
be live, and refusing those would be refusing the built-in keymap. What is worth
catching is binding ` + "`send`" + ` to ` + "`q`" + ` and losing ` + "`quit`" + ` without
being told.

Every override is checked before any is reported, and nothing is applied unless
everything can be: a half-remapped keyboard is worse than an unremapped one,
because the half that worked hides the half that did not.

See also [docs/formats.md](formats.md) for the rest of the config file.
`
