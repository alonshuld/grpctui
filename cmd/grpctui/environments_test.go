package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeEnvConfig puts a config file in a temp dir and returns its path.
func writeEnvConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// resolveEnvs runs the flags and the config file through the same path startup
// does, stopping before anything is dialled.
func resolveEnvs(t *testing.T, args ...string) (environs, error) {
	t.Helper()

	opts, err := parseFlags(args, &bytes.Buffer{})
	require.NoError(t, err)

	cfg, err := loadConfig(opts)
	require.NoError(t, err)

	return environments(cfg, opts)
}

const envConfig = `
environments:
  - name: dev
    target: localhost:50051
    variables:
      user_id: "1"
  - name: prod
    target: prod.example.com:443
    variables:
      user_id: "9"
`

func TestEnvironmentsFromTheConfigFile(t *testing.T) {
	path := writeEnvConfig(t, envConfig)

	t.Run("the first by default", func(t *testing.T) {
		e, err := resolveEnvs(t, "--config", path)
		require.NoError(t, err)
		assert.Equal(t, "dev", e.environment().Name)
	})

	t.Run("named by -env", func(t *testing.T) {
		e, err := resolveEnvs(t, "--config", path, "--env", "prod")
		require.NoError(t, err)
		assert.Equal(t, "prod", e.environment().Name)
	})

	t.Run("a name that matches nothing is refused", func(t *testing.T) {
		_, err := resolveEnvs(t, "--config", path, "--env", "staging")
		assert.Error(t, err)
	})

	t.Run("none configured binds nothing", func(t *testing.T) {
		e, err := resolveEnvs(t, "--config", "")
		require.NoError(t, err)
		assert.Empty(t, e.environment().Name)
		assert.Negative(t, e.active)
	})
}

// An environment's target sits between the profile and the command line: the
// profile says how to reach a server, the environment says which one, and a
// target typed on the command line is the most specific thing the user said.
func TestEnvironmentTargetPrecedence(t *testing.T) {
	path := writeEnvConfig(t, `
target: from-the-file:50051
`+envConfig)

	assert.Equal(t, "localhost:50051", active(t, "--config", path).Target)
	assert.Equal(t, "prod.example.com:443", active(t, "--config", path, "--env", "prod").Target)
	assert.Equal(t, "typed:50051", active(t, "--config", path, "--env", "prod", "typed:50051").Target)

	t.Run("an environment with no target leaves the connection alone", func(t *testing.T) {
		path := writeEnvConfig(t, `
target: from-the-file:50051
environments:
  - name: values only
    variables:
      user_id: "1"
`)
		assert.Equal(t, "from-the-file:50051", active(t, "--config", path).Target)
	})
}

func TestVariableFlag(t *testing.T) {
	path := writeEnvConfig(t, envConfig)

	t.Run("overrides what the environment binds", func(t *testing.T) {
		e, err := resolveEnvs(t, "--config", path, "-V", "user_id=99")
		require.NoError(t, err)

		value, ok := e.environment().Set().Lookup("user_id")
		require.True(t, ok)
		assert.Equal(t, "99", value)
	})

	// It is an override the user typed for this session, so switching
	// environment must not be a way to lose it.
	t.Run("applies to every environment", func(t *testing.T) {
		e, err := resolveEnvs(t, "--config", path, "-V", "trace_id=abc")
		require.NoError(t, err)

		for _, env := range e.list {
			value, ok := env.Set().Lookup("trace_id")
			require.True(t, ok, env.Name)
			assert.Equal(t, "abc", value)
		}
	})

	t.Run("with no environments configured it becomes one", func(t *testing.T) {
		e, err := resolveEnvs(t, "--config", "", "-V", "user_id=99", "-V", "tenant=acme")
		require.NoError(t, err)

		assert.Equal(t, commandLineEnvironment, e.environment().Name)
		assert.Equal(t, []string{"tenant", "user_id"}, e.environment().Set().Names())
	})

	t.Run("a value may contain an =", func(t *testing.T) {
		e, err := resolveEnvs(t, "--config", "", "-V", "url=https://x/?a=1")
		require.NoError(t, err)

		value, _ := e.environment().Set().Lookup("url")
		assert.Equal(t, "https://x/?a=1", value)
	})

	// A mistyped name is otherwise silent: the reference that would have used it
	// is refused at the row, long after the flag is out of sight.
	t.Run("a malformed assignment is refused as it is given", func(t *testing.T) {
		for _, arg := range []string{"user_id", "user-id=1", "=1"} {
			_, err := parseFlags([]string{"-V", arg, "localhost:50051"}, &bytes.Buffer{})
			assert.Error(t, err, arg)
		}
	})
}

// The flag's String is used in usage output and error messages, and a -V is as
// likely to carry a token as a -H is.
func TestVariableFlagPrintsNamesOnly(t *testing.T) {
	list := assignmentList{"token=secret-value", "user_id=1"}
	assert.Equal(t, "token, user_id", list.String())
}

// A ${VAR} nobody exported in the environment being started in is fatal, and in
// any other one is not: it should stop you using production, not stop grpctui.
func TestEnvironmentProblemsAreRaisedWhenChosen(t *testing.T) {
	path := writeEnvConfig(t, `
environments:
  - name: dev
    variables:
      tenant: local
  - name: prod
    variables:
      tenant: ${GRPCTUI_TEST_NOT_SET}
`)

	e, err := resolveEnvs(t, "--config", path)
	require.NoError(t, err)
	assert.Equal(t, "dev", e.environment().Name)
	require.Error(t, e.problems[1])

	_, err = resolveEnvs(t, "--config", path, "--env", "prod")
	assert.Error(t, err)
}
