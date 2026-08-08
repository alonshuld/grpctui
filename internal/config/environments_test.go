package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/vars"
)

const environmentsFile = `
environment: staging
environments:
  - name: dev
    target: localhost:50051
    variables:
      user_id: "1"
      tenant: local
  - name: staging
    target: staging.example.com:443
    variables:
      user_id: "42"
      tenant: acme
  - name: values only
    variables:
      user_id: "7"
`

func loadEnvironments(t *testing.T, body string) config.Config {
	t.Helper()

	cfg, err := config.Load(writeConfig(t, body))
	require.NoError(t, err)
	return cfg
}

func TestEnvironments(t *testing.T) {
	cfg := loadEnvironments(t, environmentsFile)

	envs, problems := cfg.Environments()
	require.Len(t, envs, 3)
	require.Equal(t, []error{nil, nil, nil}, problems)

	assert.Equal(t, "dev", envs[0].Name)
	assert.Equal(t, "localhost:50051", envs[0].Target)

	// Sorted by name, so that a file and a session agree on the order whatever
	// Go's map iteration does that run.
	assert.Equal(t, []string{"tenant", "user_id"}, envs[1].Set().Names())

	value, ok := envs[1].Set().Lookup("user_id")
	require.True(t, ok)
	assert.Equal(t, "42", value)

	t.Run("an environment may carry values and no target", func(t *testing.T) {
		assert.Empty(t, envs[2].Target)
		assert.Equal(t, 1, envs[2].Set().Len())
	})

	t.Run("the file says which to start in", func(t *testing.T) {
		i, err := config.SelectEnvironment(envs, cfg.Env)
		require.NoError(t, err)
		assert.Equal(t, "staging", envs[i].Name)
	})
}

func TestEnvironmentsExpandProcessVariables(t *testing.T) {
	t.Setenv("GRPCTUI_TEST_TENANT", "acme")

	cfg := loadEnvironments(t, `
environments:
  - name: prod
    target: ${GRPCTUI_TEST_TENANT}.example.com:443
    variables:
      tenant: ${GRPCTUI_TEST_TENANT}
`)

	envs, problems := cfg.Environments()
	require.Len(t, envs, 1)
	require.NoError(t, problems[0])

	assert.Equal(t, "acme.example.com:443", envs[0].Target)

	value, ok := envs[0].Set().Lookup("tenant")
	require.True(t, ok)
	assert.Equal(t, "acme", value)
}

// One unset process variable in production should stop you using production,
// not stop grpctui starting.
func TestEnvironmentsCarryTheirProblems(t *testing.T) {
	cfg := loadEnvironments(t, `
environments:
  - name: dev
    variables:
      tenant: local
  - name: prod
    variables:
      tenant: ${GRPCTUI_TEST_NOT_SET}
`)

	envs, problems := cfg.Environments()
	require.Len(t, envs, 2)

	require.NoError(t, problems[0])
	require.Error(t, problems[1])
	assert.Contains(t, problems[1].Error(), `environment "prod"`)
	assert.Contains(t, problems[1].Error(), "GRPCTUI_TEST_NOT_SET")

	assert.Equal(t, "prod", envs[1].Name, "a broken environment is still listed")
}

// A {{name}} reference cannot spell a name with a dash in it, so binding one is
// a quiet way to lose a value.
func TestEnvironmentsRefuseUnreferenceableNames(t *testing.T) {
	cfg := loadEnvironments(t, `
environments:
  - name: dev
    variables:
      user-id: "1"
`)

	_, problems := cfg.Environments()
	require.Error(t, problems[0])
	assert.Contains(t, problems[0].Error(), "user-id")
}

func TestLoadRefusesBadEnvironmentNames(t *testing.T) {
	tests := map[string]string{
		"unnamed": `
environments:
  - target: localhost:50051
`,
		"defined twice": `
environments:
  - name: dev
  - name: dev
`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, body))
			assert.Error(t, err)
		})
	}
}

func TestSelectEnvironment(t *testing.T) {
	envs := []vars.Environment{{Name: "dev"}, {Name: "prod"}}

	t.Run("by name", func(t *testing.T) {
		i, err := config.SelectEnvironment(envs, "prod")
		require.NoError(t, err)
		assert.Equal(t, 1, i)
	})

	t.Run("the first when none is named", func(t *testing.T) {
		i, err := config.SelectEnvironment(envs, "")
		require.NoError(t, err)
		assert.Equal(t, 0, i)
	})

	// Starting in whatever came first is how a request meant for staging
	// carries production's account id.
	t.Run("a name that matches nothing is an error listing what there was", func(t *testing.T) {
		_, err := config.SelectEnvironment(envs, "staging")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"dev", "prod"`)
	})

	t.Run("none configured is not an error", func(t *testing.T) {
		i, err := config.SelectEnvironment(nil, "")
		require.NoError(t, err)
		assert.Negative(t, i)
	})

	t.Run("but naming one that cannot exist is", func(t *testing.T) {
		_, err := config.SelectEnvironment(nil, "dev")
		assert.Error(t, err)
	})
}

// The two syntaxes are distinct on purpose: ${VAR} is the process environment
// at load, {{name}} is a grpctui variable at send.
func TestEnvironmentsLeaveReferencesAlone(t *testing.T) {
	cfg := loadEnvironments(t, `
environments:
  - name: dev
    variables:
      greeting: "hello {{tenant}}"
`)

	envs, problems := cfg.Environments()
	require.NoError(t, problems[0])

	value, ok := envs[0].Set().Lookup("greeting")
	require.True(t, ok)
	assert.Equal(t, "hello {{tenant}}", value)
}
