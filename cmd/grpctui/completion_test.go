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

			// Every script has to be able to offer every subcommand, and to call
			// back into the hidden helper for the names only grpctui knows. bash
			// and fish ask for the subcommand list rather than baking it in,
			// which is why adding one needs no change to them; zsh describes them
			// inline so that it can put a sentence beside each.
			for _, cmd := range []string{cmdRun, cmdKeys, cmdCompletion} {
				assert.True(t,
					strings.Contains(script, cmd) || strings.Contains(script, cmdComplete+" commands"),
					"%s completion cannot offer %s", shell, cmd)
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
