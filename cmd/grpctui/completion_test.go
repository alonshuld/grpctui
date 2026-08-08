package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

func TestCompletion(t *testing.T) {
	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			require.Equal(t, exitOK, completion([]string{shell}, &stdout, &stderr))
			assert.Empty(t, stderr.String())

			script := stdout.String()
			assert.NotEmpty(t, script)

			// Every script has to be able to offer every subcommand, and each does
			// it one of two ways: bash and fish ask the hidden helper for the
			// list, which is why adding a subcommand needs no change to them, and
			// zsh describes them inline so it can put a sentence beside each.
			// Accepting either for every shell would assert nothing about the two
			// that always contain the helper call.
			if shell == shellZsh {
				for _, cmd := range candidates(kindCommands) {
					assert.Contains(t, script, "'"+cmd+":", "zsh completion does not describe %s", cmd)
				}
			} else {
				assert.Contains(t, script, cmdComplete+" "+kindCommands,
					"%s completion does not ask for the subcommand list", shell)
			}

			// The subcommands that take an argument of their own need a branch
			// naming them, whichever way the list is offered.
			for cmd, branch := range argumentBranches(shell) {
				assert.Contains(t, script, branch, "%s completion has no branch for %s", shell, cmd)
			}
			assert.Contains(t, script, cmdComplete)

			// And every flag has to be offerable, or completing one silently
			// stops working the day it is added. Each shell spells a flag its own
			// way: bash and zsh keep the leading dash, fish takes the bare name
			// after -o.
			for _, name := range flagNames() {
				want := name
				if shell == shellFish {
					want = "-o " + strings.TrimPrefix(name, "-")
				}
				assert.Contains(t, script, want, "%s completion does not offer %s", shell, name)
			}
		})
	}
}

// argumentBranches is what each script must say to complete the argument of a
// subcommand that takes one: a collection for `run`, a shell for `completion`.
func argumentBranches(shell string) map[string]string {
	switch shell {
	case shellBash:
		return map[string]string{cmdRun: `== "` + cmdRun + `"`, cmdCompletion: `== "` + cmdCompletion + `"`}
	case shellZsh:
		return map[string]string{cmdRun: "\n        " + cmdRun + ")", cmdCompletion: "\n        " + cmdCompletion + ")"}
	default:
		return map[string]string{
			cmdRun:        "__fish_seen_subcommand_from " + cmdRun,
			cmdCompletion: "__fish_seen_subcommand_from " + cmdCompletion,
		}
	}
}

// TestCompletion_RunOnlyFlagsAreOfferedOnlyAfterRun pins the other half of
// offering a flag: a completion is a promise the command line will parse.
// -format and -target are declared by `run` and by nothing else, so a script
// that offers them after `keys` completes a line that fails with "flag provided
// but not defined" — which looks like grpctui's bug, not the script's.
func TestCompletion_RunOnlyFlagsAreOfferedOnlyAfterRun(t *testing.T) {
	// The line each script puts its run-only flags on, and nowhere else. Every
	// one of them is reached only once the subcommand is known to be `run`.
	guards := map[string]string{
		shellBash: `flags="$flags`,
		shellZsh:  "extra=(",
		shellFish: "__fish_seen_subcommand_from " + cmdRun,
	}

	require.NotEmpty(t, runOnlyFlagNames(), "the test means nothing if no flag is run-only")

	for shell, guard := range guards {
		t.Run(shell, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			require.Equal(t, exitOK, completion([]string{shell}, &stdout, &stderr))

			for _, name := range runOnlyFlagNames() {
				bare := strings.TrimPrefix(name, "-")
				for line := range strings.SplitSeq(stdout.String(), "\n") {
					if !strings.Contains(line, name) && !strings.Contains(line, "-o "+bare) {
						continue
					}
					assert.Contains(t, line, guard,
						"%s offers %s outside the branch that knows the subcommand is %s", shell, name, cmdRun)
				}
			}
		})
	}
}

func TestCompletion_Errors(t *testing.T) {
	tests := map[string][]string{
		"no shell":          {},
		"two shells":        {"bash", "zsh"},
		"a shell we do not": {"tcsh"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			assert.Equal(t, exitUsage, completion(args, &stdout, &stderr))
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), "bash")
		})
	}
}

func TestCompleteValues(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "grpctui", "collections"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "grpctui", "config.yaml"), []byte(`
profiles:
  - name: staging
    target: staging.example.com:443
  - name: production
    target: api.example.com:443
environments:
  - name: dev
    variables: {tenant: acme}
  - name: prod
themes:
  - name: midnight
    base: dark
`), 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "grpctui", "collections", "smoke.yaml"),
		[]byte("requests:\n  - name: login\n    method: demo.v1.Auth.Login\n"), 0o600))

	tests := map[string][]string{
		"profiles":     {"staging", "production"},
		"environments": {"dev", "prod"},
		"themes":       {styles.ThemeAuto, styles.ThemeDark, styles.ThemeLight, "midnight"},
		"collections":  {"smoke"},
		"shells":       shells,
		"commands":     {cmdRun, cmdKeys, cmdCompletion},
		"renderers":    render.BuiltinNames(),
		"actions":      keys.Names(),
		"colors":       styles.RoleNames(),
	}

	for what, want := range tests {
		t.Run(what, func(t *testing.T) {
			var stdout bytes.Buffer

			require.Equal(t, exitOK, completeValues([]string{what}, &stdout))
			assert.Equal(t, want, lines(stdout.String()))
		})
	}
}

// TestCompleteValues_StaysQuiet pins that the callback never prints an error.
// It runs inside the user's shell on every tab: a broken config file is worth
// an error when they start grpctui, and worth silence while they are halfway
// through typing a command.
func TestCompleteValues_StaysQuiet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "grpctui"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "grpctui", "config.yaml"), []byte("profiles: [oh dear\n"), 0o600))

	for _, what := range []string{"profiles", "environments", "themes", "collections", "nonsense", ""} {
		t.Run(what, func(t *testing.T) {
			var stdout bytes.Buffer

			assert.Equal(t, exitOK, completeValues([]string{what}, &stdout))
			assert.Empty(t, stdout.String())
		})
	}

	// Nothing at all to complete is not an error either.
	var stdout bytes.Buffer
	assert.Equal(t, exitOK, completeValues(nil, &stdout))
	assert.Empty(t, stdout.String())
}

// TestCompleteValues_BrokenThemeFallsBack pins that a theme that will not parse
// still leaves tab working on the built-ins while the user fixes it.
func TestCompleteValues_BrokenThemeFallsBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "grpctui"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "grpctui", "config.yaml"),
		[]byte("themes:\n  - name: broken\n    colors: {primary: beige}\n"), 0o600))

	var stdout bytes.Buffer
	require.Equal(t, exitOK, completeValues([]string{"themes"}, &stdout))
	assert.Equal(t, []string{styles.ThemeAuto, styles.ThemeDark, styles.ThemeLight}, lines(stdout.String()))
}

// TestValueCompletions_NameRealFlags pins that the tables the scripts are built
// from name flags that exist. A stale entry produces a script that completes
// nothing, silently.
func TestValueCompletions_NameRealFlags(t *testing.T) {
	known := make(map[string]bool)
	for _, name := range flagNames() {
		known[strings.TrimPrefix(name, "-")] = true
	}

	// The kinds a flag may complete as. A kind not in this list is one
	// [candidates] answers nothing for, which produces a script that silently
	// completes nothing.
	kinds := []string{"profiles", "environments", "themes", "collections", "shells", "commands", "renderers", "actions", "colors"}

	for name, kind := range valueCompletions {
		assert.True(t, known[name], "-%s completes as %q but is not a flag", name, kind)
		assert.Contains(t, kinds, kind, "-%s completes as %q, which nothing answers", name, kind)
	}
	for _, name := range pathFlags {
		assert.True(t, known[name], "-%s takes a path but is not a flag", name)
	}
	for _, name := range dirFlags {
		assert.True(t, known[name], "-%s takes a directory but is not a flag", name)
	}
}

func lines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
