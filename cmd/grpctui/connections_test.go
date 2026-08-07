package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// resolve runs the flags and the config file through the same path the real
// startup does, stopping just before anything is dialled.
func resolve(t *testing.T, args ...string) (startup, error) {
	t.Helper()

	opts, err := parseFlags(args, &bytes.Buffer{})
	require.NoError(t, err)

	cfg, err := loadConfig(opts)
	require.NoError(t, err)

	return connections(cfg, opts)
}

// active is the profile a set of arguments would connect with.
func active(t *testing.T, args ...string) grpcclient.Profile {
	t.Helper()

	s, err := resolve(t, args...)
	require.NoError(t, err)
	return s.profile()
}

func TestConnections_NoConfigFile(t *testing.T) {
	p := active(t, "--config", "", "localhost:50051")

	assert.Equal(t, "localhost:50051", p.Target)
	assert.Equal(t, "plaintext", p.Security.Mode())
	assert.Equal(t, grpcclient.AuthNone, p.Auth.Kind)
}

func TestConnections_StartsOnTheNamedProfile(t *testing.T) {
	cfg := writeConfig(t, `
profiles:
  - name: dev
    target: localhost:50051
  - name: staging
    target: staging.example.com:443
    tls:
      enabled: true
`)

	assert.Equal(t, "localhost:50051", active(t, "--config", cfg).Target,
		"the first profile is the default")

	staging := active(t, "--config", cfg, "--profile", "staging")
	assert.Equal(t, "staging.example.com:443", staging.Target)
	assert.Equal(t, "TLS", staging.Security.Mode())
}

// Connecting to whatever came first is how a request meant for staging reaches
// production.
func TestConnections_UnknownProfileIsAnError(t *testing.T) {
	cfg := writeConfig(t, "profiles:\n  - name: dev\n    target: a:1\n")

	_, err := resolve(t, "--config", cfg, "--profile", "prod")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `no profile named "prod"`)
	assert.Contains(t, err.Error(), `"dev"`)
}

// The flags override the chosen profile rather than replacing it: a saved
// connection with verification off for one session, not a new connection that
// has lost its credentials.
func TestConnections_FlagsOverrideTheChosenProfile(t *testing.T) {
	cfg := writeConfig(t, `
profiles:
  - name: staging
    target: staging.example.com:443
    tls:
      enabled: true
      ca_cert: /etc/ssl/staging-ca.pem
    auth:
      type: bearer
      token: from-the-file
    metadata:
      x-tenant: acme
`)

	p := active(t, "--config", cfg, "--profile", "staging", "--insecure", "-H", "x-request-id: 42")

	assert.Equal(t, "staging.example.com:443", p.Target)
	assert.True(t, p.Security.InsecureSkipVerify)
	assert.Equal(t, "/etc/ssl/staging-ca.pem", p.Security.CACert, "the rest of the profile survives")
	assert.Equal(t, "from-the-file", p.Auth.Token)
	assert.Equal(t, grpcclient.Metadata{
		{Key: "x-tenant", Value: "acme"},
		{Key: "x-request-id", Value: "42"},
	}, p.Metadata)
}

func TestConnections_TargetArgumentOverridesTheProfile(t *testing.T) {
	cfg := writeConfig(t, "profiles:\n  - name: dev\n    target: a:1\n")

	p := active(t, "--config", cfg, "--profile", "dev", "b:2")

	assert.Equal(t, "b:2", p.Target)
}

// Requiring --tls beside a certificate would only be a way to get an error
// message.
func TestConnections_TLSFlagsImplyTLS(t *testing.T) {
	tests := map[string][]string{
		"cacert":     {"--cacert", "/etc/ssl/ca.pem"},
		"servername": {"--servername", "api.internal"},
		"insecure":   {"--insecure"},
		"mutual":     {"--cert", "c.pem", "--key", "k.pem"},
	}

	for name, flags := range tests {
		t.Run(name, func(t *testing.T) {
			p := active(t, append([]string{"--config", ""}, append(flags, "localhost:50051")...)...)

			assert.True(t, p.Security.TLS)
		})
	}
}

func TestConnections_TLSCanBeTurnedOffForOneSession(t *testing.T) {
	cfg := writeConfig(t, "profiles:\n  - name: dev\n    target: a:1\n    tls:\n      enabled: true\n")

	p := active(t, "--config", cfg, "--tls=false")

	assert.False(t, p.Security.TLS)
	assert.Equal(t, "plaintext", p.Security.Mode())
}

// Naming a CA while explicitly asking for plaintext is a contradiction rather
// than an omission, and connecting in the clear anyway is the wrong answer.
func TestConnections_ContradictoryTLSFlagsAreRefused(t *testing.T) {
	_, err := resolve(t, "--config", "", "--tls=false", "--cacert", "/etc/ssl/ca.pem", "a:1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS is not enabled")
}

func TestConnections_HalfAClientCertificateIsRefused(t *testing.T) {
	_, err := resolve(t, "--config", "", "--cert", "c.pem", "a:1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "both a client certificate and a key")
}

func TestConnections_RejectsAMalformedHeaderFlag(t *testing.T) {
	tests := map[string]struct {
		flag   string
		errMsg string
	}{
		"no colon":      {flag: "x-tenant acme", errMsg: "want `key: value`"},
		"reserved key":  {flag: "grpc-timeout: 1S", errMsg: "reserved"},
		"bad character": {flag: "x tenant: acme", errMsg: "invalid character"},
		"empty key":     {flag: ": acme", errMsg: "empty header key"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := resolve(t, "--config", "", "-H", tt.flag, "a:1")

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestConnections_RepeatableHeaderFlag(t *testing.T) {
	p := active(t, "--config", "",
		"-H", "x-a: 1",
		"-H", "authorization: Bearer abc",
		"localhost:50051")

	assert.Equal(t, grpcclient.Metadata{
		{Key: "x-a", Value: "1"},
		{Key: "authorization", Value: "Bearer abc"},
	}, p.Metadata)
}

// A profile the config file rejects must not reach a dial: the error belongs at
// startup, where it can be read.
func TestConnections_RejectsAnUnusableProfile(t *testing.T) {
	cfg := writeConfig(t, `
profiles:
  - name: dev
    target: a:1
    auth:
      type: bearer
`)

	_, err := resolve(t, "--config", cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a token")
}

func TestConnections_TopLevelSettingsBecomeTheDefaultProfile(t *testing.T) {
	cfg := writeConfig(t, `
target: localhost:50051
auth:
  type: basic
  username: alice
  password: hunter2
profiles:
  - name: staging
    target: staging.example.com:443
`)

	s, err := resolve(t, "--config", cfg)

	require.NoError(t, err)
	require.Len(t, s.profiles, 2)
	assert.Equal(t, 0, s.active)
	assert.Equal(t, "default", s.profiles[0].Name)
	assert.Equal(t, "basic (alice)", s.profiles[0].Auth.Describe())
}

func TestParseFlags_SecurityDefaults(t *testing.T) {
	opts, err := parseFlags([]string{"localhost:50051"}, &bytes.Buffer{})

	require.NoError(t, err)
	assert.False(t, opts.tls, "plaintext is what a local server almost always wants")
	assert.Empty(t, opts.profile)
	assert.Empty(t, opts.headers)
	assert.Empty(t, opts.given, "nothing was given, so nothing overrides a profile")
}

func TestSelect_IsWhatTheFlagUses(t *testing.T) {
	profiles := []grpcclient.Profile{{Name: "a"}, {Name: "b"}}

	i, err := config.Select(profiles, "b")

	require.NoError(t, err)
	assert.Equal(t, 1, i)
}

// A profile nobody asked for must not stop the run. A production token you do
// not have should stop you reaching production, not your own laptop.
func TestConnections_AnUnusableProfileIsSetAsideNotFatal(t *testing.T) {
	cfg := writeConfig(t, `
profiles:
  - name: dev
    target: localhost:50051
  - name: prod
    target: api.example.com:443
    auth:
      type: bearer
      token: ${GRPCTUI_TEST_ABSENT}
`)

	s, err := resolve(t, "--config", cfg)

	require.NoError(t, err)
	assert.Equal(t, "dev", s.profile().Name)
	assert.Len(t, s.profiles, 2, "the switcher can still name the one that failed")

	// Choosing it is when the reason is due.
	require.Error(t, s.problem("prod"))
	assert.Contains(t, s.problem("prod").Error(), "GRPCTUI_TEST_ABSENT")
	assert.NoError(t, s.problem("dev"))
}

// Asking for the broken one is a different matter: it has to work now.
func TestConnections_AnUnusableActiveProfileIsFatal(t *testing.T) {
	cfg := writeConfig(t, `
profiles:
  - name: dev
    target: localhost:50051
  - name: prod
    target: api.example.com:443
    auth:
      type: bearer
      token: ${GRPCTUI_TEST_ABSENT}
`)

	_, err := resolve(t, "--config", cfg, "--profile", "prod")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "GRPCTUI_TEST_ABSENT")
}

// The switcher's dialer is where a set-aside profile finally fails, which is
// the point at which the user has asked for it.
func TestStartup_DialerRefusesAnUnusableProfile(t *testing.T) {
	s := startup{
		profiles: []grpcclient.Profile{{Name: "dev", Target: "a:1"}, {Name: "prod", Target: "b:2"}},
		problems: []error{nil, errors.New("environment variable PROD_TOKEN is not set")},
	}

	_, err := s.dialer(zap.NewNop())(s.profiles[1])

	require.Error(t, err)
	assert.Contains(t, err.Error(), "PROD_TOKEN")
}
