package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// loadConnections reads a config body and converts it, which is what every
// caller of this package actually does.
func loadConnections(t *testing.T, body string) []grpcclient.Profile {
	t.Helper()

	cfg, err := config.Load(writeConfig(t, body))
	require.NoError(t, err)

	profiles, problems := cfg.Connections()
	require.NoError(t, errors.Join(problems...))
	return profiles
}

func TestConfig_Connections_FullProfile(t *testing.T) {
	profiles := loadConnections(t, `
profiles:
  - name: staging
    target: api.staging.example.com:443
    tls:
      enabled: true
      ca_cert: /etc/ssl/staging-ca.pem
      client_cert: /etc/ssl/client.pem
      client_key: /etc/ssl/client-key.pem
      server_name: api.staging.internal
      insecure_skip_verify: true
    auth:
      type: bearer
      token: abc.def
    metadata:
      x-tenant: acme
      x-region: eu
`)

	require.Len(t, profiles, 1)
	assert.Equal(t, grpcclient.Profile{
		Name:   "staging",
		Target: "api.staging.example.com:443",
		Security: grpcclient.Security{
			TLS:                true,
			CACert:             "/etc/ssl/staging-ca.pem",
			ClientCert:         "/etc/ssl/client.pem",
			ClientKey:          "/etc/ssl/client-key.pem",
			ServerName:         "api.staging.internal",
			InsecureSkipVerify: true,
		},
		Auth: grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: "abc.def"},
		// Sorted by key, so that two runs of the same file send the same
		// headers in the same order.
		Metadata: grpcclient.Metadata{
			{Key: "x-region", Value: "eu"},
			{Key: "x-tenant", Value: "acme"},
		},
	}, profiles[0])
}

// A file that predates profiles keeps the connection it already had, and the
// top-level settings lead the list.
func TestConfig_Connections_TopLevelBecomesTheDefaultProfile(t *testing.T) {
	profiles := loadConnections(t, `
target: localhost:50051
metadata:
  x-tenant: acme
profiles:
  - name: staging
    target: staging:443
`)

	require.Len(t, profiles, 2)
	assert.Equal(t, "default", profiles[0].Name)
	assert.Equal(t, "localhost:50051", profiles[0].Target)
	assert.Equal(t, grpcclient.Metadata{{Key: "x-tenant", Value: "acme"}}, profiles[0].Metadata)
	assert.Equal(t, "staging", profiles[1].Name)
}

func TestConfig_Connections_NoConnectionConfigured(t *testing.T) {
	profiles := loadConnections(t, "# nothing here\n")

	assert.Empty(t, profiles)
}

// Top-level TLS or auth with no target is still a connection: the target may
// arrive on the command line.
func TestConfig_Connections_SettingsWithoutATarget(t *testing.T) {
	profiles := loadConnections(t, "tls:\n  enabled: true\n")

	require.Len(t, profiles, 1)
	assert.Empty(t, profiles[0].Target)
	assert.True(t, profiles[0].Security.TLS)
}

func TestConfig_Connections_ExpandsEnvironmentVariables(t *testing.T) {
	t.Setenv("GRPCTUI_TEST_TOKEN", "s3cr3t")
	t.Setenv("GRPCTUI_TEST_HOST", "api.example.com")

	profiles := loadConnections(t, `
profiles:
  - name: prod
    target: ${GRPCTUI_TEST_HOST}:443
    auth:
      type: bearer
      token: ${GRPCTUI_TEST_TOKEN}
    metadata:
      x-api-key: ${GRPCTUI_TEST_TOKEN}
`)

	require.Len(t, profiles, 1)
	assert.Equal(t, "api.example.com:443", profiles[0].Target)
	assert.Equal(t, "s3cr3t", profiles[0].Auth.Token)
	assert.Equal(t, grpcclient.Metadata{{Key: "x-api-key", Value: "s3cr3t"}}, profiles[0].Metadata)
}

// A token that quietly becomes "" produces an authentication failure that looks
// like anything but a config problem.
func TestConfig_Connections_MissingVariableIsAProblem(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, `
profiles:
  - name: prod
    target: a:1
    auth:
      type: bearer
      token: ${GRPCTUI_TEST_ABSENT}
`))
	require.NoError(t, err)

	profiles, problems := cfg.Connections()

	require.Len(t, problems, 1)
	require.Error(t, problems[0])
	assert.Contains(t, problems[0].Error(), "GRPCTUI_TEST_ABSENT")
	assert.Contains(t, problems[0].Error(), `profile "prod"`)

	// The profile is still listed, and what could not be expanded is left as it
	// was written: a connection nobody can dial is one the switcher still has to
	// be able to name.
	require.Len(t, profiles, 1)
	assert.Equal(t, "prod", profiles[0].Name)
	assert.Equal(t, "a:1", profiles[0].Target)
	assert.Equal(t, "${GRPCTUI_TEST_ABSENT}", profiles[0].Auth.Token)
}

// One unset variable must not make the whole file unusable: a production token
// nobody exported should stop you reaching production, not your own laptop.
func TestConfig_Connections_OneBadProfileLeavesTheRestUsable(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, `
profiles:
  - name: dev
    target: localhost:50051
  - name: prod
    target: api.example.com:443
    auth:
      type: bearer
      token: ${GRPCTUI_TEST_ABSENT}
`))
	require.NoError(t, err)

	profiles, problems := cfg.Connections()

	require.Len(t, profiles, 2)
	require.NoError(t, problems[0])
	require.Error(t, problems[1])
}

func TestConfig_Connections_MissingVariableInMetadata(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, "metadata:\n  x-api-key: ${GRPCTUI_TEST_ABSENT}\n"))
	require.NoError(t, err)

	_, problems := cfg.Connections()

	require.Len(t, problems, 1)
	require.Error(t, problems[0])
	assert.Contains(t, problems[0].Error(), "x-api-key")
	assert.Contains(t, problems[0].Error(), "GRPCTUI_TEST_ABSENT")
}

// Only the braced form is a reference, so a password written down verbatim
// survives the trip.
func TestConfig_Connections_LeavesBareDollarsAlone(t *testing.T) {
	t.Setenv("PASS", "should-not-be-used")

	profiles := loadConnections(t, `
profiles:
  - name: prod
    target: a:1
    auth:
      type: basic
      username: alice
      password: p$ssw0rd$PASS
`)

	require.Len(t, profiles, 1)
	assert.Equal(t, "p$ssw0rd$PASS", profiles[0].Auth.Password)
}

func TestConfig_Connections_ExpandsHomeInPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	profiles := loadConnections(t, `
profiles:
  - name: prod
    target: a:1
    tls:
      enabled: true
      ca_cert: ~/certs/ca.pem
      client_cert: ~/certs/client.pem
      client_key: ~/certs/client-key.pem
`)

	require.Len(t, profiles, 1)
	assert.Equal(t, filepath.Join(home, "certs", "ca.pem"), profiles[0].Security.CACert)
	assert.Equal(t, filepath.Join(home, "certs", "client.pem"), profiles[0].Security.ClientCert)
	assert.Equal(t, filepath.Join(home, "certs", "client-key.pem"), profiles[0].Security.ClientKey)
}

// A ~ that is not the whole first segment is part of the name, not a home
// directory: "~backup/ca.pem" is a directory somebody could genuinely have.
func TestConfig_Connections_LeavesOtherTildesAlone(t *testing.T) {
	profiles := loadConnections(t, `
profiles:
  - name: prod
    target: a:1
    tls:
      enabled: true
      ca_cert: ~backup/ca.pem
`)

	require.Len(t, profiles, 1)
	assert.Equal(t, "~backup/ca.pem", profiles[0].Security.CACert)
}

func TestLoad_RejectsBadProfileNames(t *testing.T) {
	tests := map[string]struct {
		body   string
		errMsg string
	}{
		"a profile with no name": {
			body:   "profiles:\n  - target: a:1\n",
			errMsg: "profile 1 has no name",
		},
		"two profiles with the same name": {
			body:   "profiles:\n  - name: dev\n    target: a:1\n  - name: dev\n    target: b:2\n",
			errMsg: `profile "dev" is defined twice`,
		},
		"a profile called default": {
			body:   "profiles:\n  - name: default\n    target: a:1\n",
			errMsg: "the name of the top-level settings",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tt.body))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestLoad_RejectsUnknownProfileKeys(t *testing.T) {
	_, err := config.Load(writeConfig(t, "profiles:\n  - name: dev\n    targt: a:1\n"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "targt")
}

func TestSelect(t *testing.T) {
	profiles := []grpcclient.Profile{
		{Name: "default", Target: "localhost:50051"},
		{Name: "staging", Target: "staging:443"},
	}

	tests := map[string]struct {
		name   string
		want   int
		errMsg string
	}{
		"no name takes the first": {want: 0},
		"by name":                 {name: "staging", want: 1},
		"a name that matches nothing": {
			name:   "prod",
			errMsg: `no profile named "prod": have "default", "staging"`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := config.Select(profiles, tt.name)

			if tt.errMsg != "" {
				require.Error(t, err)
				assert.Equal(t, tt.errMsg, err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSelect_NoProfiles(t *testing.T) {
	_, err := config.Select(nil, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no connection profiles")
}
