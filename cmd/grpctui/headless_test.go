package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/vars"
)

func TestPick(t *testing.T) {
	collections := loadedCollections(t, map[string]string{
		"smoke.yaml": `requests:
  - name: login
    method: demo.v1.Auth.Login
  - name: whoami
    method: demo.v1.Auth.WhoAmI
`,
		"empty.yaml": "requests: []\n",
	})

	t.Run("a whole collection, in file order", func(t *testing.T) {
		got, err := pick(collections, "smoke", "")
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "login", got[0].Name)
		assert.Equal(t, "whoami", got[1].Name)
	})

	t.Run("one request from it", func(t *testing.T) {
		got, err := pick(collections, "smoke", "whoami")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "whoami", got[0].Name)
	})
}

// TestPick_Errors pins that nothing to run is an error rather than a green run
// that checked nothing. "0 of 0 passed" in a CI log is the worst outcome there
// is.
func TestPick_Errors(t *testing.T) {
	collections := loadedCollections(t, map[string]string{
		"smoke.yaml": "requests:\n  - name: login\n    method: demo.v1.Auth.Login\n",
		"empty.yaml": "requests: []\n",
	})

	tests := map[string]struct {
		collection string
		request    string
		want       []string
	}{
		"no such collection": {
			collection: "nope",
			want:       []string{"nope", `"smoke"`},
		},
		"no such request": {
			collection: "smoke",
			request:    "logout",
			want:       []string{"logout", `"login"`},
		},
		"an empty collection": {
			collection: "empty",
			want:       []string{"empty", "is empty"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := pick(collections, tt.collection, tt.request)
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestHeaders pins that a profile's headers reach a headless call with their
// {{name}} references expanded. A profile whose authorization header is
// "Bearer {{token}}" is how a collection is made portable, and sending the
// reference verbatim would authenticate as nobody.
func TestHeaders(t *testing.T) {
	profile := grpcclient.Profile{Metadata: grpcclient.Metadata{
		{Key: "authorization", Value: "Bearer {{token}}"},
		{Key: "x-tenant", Value: "acme"},
	}}

	envs := environs{
		list: []vars.Environment{{
			Name:      "staging",
			Variables: []vars.Variable{{Name: "token", Value: "abc123"}},
		}},
		problems: []error{nil},
		active:   0,
	}

	got, err := headers(profile, envs)
	require.NoError(t, err)
	assert.Equal(t, grpcclient.Metadata{
		{Key: "authorization", Value: "Bearer abc123"},
		{Key: "x-tenant", Value: "acme"},
	}, got)
}

// TestHeaders_Unbound pins that a reference nothing binds refuses the run
// rather than sending an empty credential — the failure of which looks like
// anything but a missing variable.
func TestHeaders_Unbound(t *testing.T) {
	profile := grpcclient.Profile{Metadata: grpcclient.Metadata{
		{Key: "authorization", Value: "Bearer {{token}}"},
	}}

	_, err := headers(profile, environs{active: -1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token")
	assert.Contains(t, err.Error(), "authorization")
}

func TestHeaders_None(t *testing.T) {
	got, err := headers(grpcclient.Profile{}, environs{active: -1})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestQuoted(t *testing.T) {
	assert.Equal(t, "none", quoted(nil))
	assert.Equal(t, `"a"`, quoted([]string{"a"}))
	assert.Equal(t, `"a", "b"`, quoted([]string{"a", "b"}))
}

// TestRunCollection_Usage covers the paths that fail before anything is
// dialled. Anything past that needs a server, which internal/runner's tests
// provide with a fake.
func TestRunCollection_Usage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "grpctui", "collections"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "grpctui", "collections", "smoke.yaml"),
		[]byte("requests:\n  - name: login\n    method: demo.v1.Auth.Login\n"), 0o600))

	tests := map[string]struct {
		args     []string
		wantCode int
		wantErr  string
	}{
		"no collection": {
			args:     nil,
			wantCode: exitUsage,
			wantErr:  "run takes one collection, got 0",
		},
		"two collections": {
			args:     []string{"smoke", "other"},
			wantCode: exitUsage,
			wantErr:  "run takes one collection, got 2",
		},
		"a format that is not one": {
			args:     []string{"-format", "yaml", "smoke"},
			wantCode: exitUsage,
			wantErr:  "yaml",
		},
		"no target": {
			args:     []string{"smoke"},
			wantCode: exitError,
			wantErr:  missingTarget,
		},
		"a collection nothing names": {
			args:     []string{"-target", "localhost:50051", "nope"},
			wantCode: exitError,
			wantErr:  "no collection named",
		},
		"an unknown flag": {
			args:     []string{"-nope", "smoke"},
			wantCode: exitUsage,
			wantErr:  "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := runCollection(tt.args, &stdout, &stderr)
			assert.Equal(t, tt.wantCode, code)
			if tt.wantErr != "" {
				assert.Contains(t, stderr.String(), tt.wantErr)
			}
		})
	}
}

// TestRun_DispatchesSubcommands pins that the first bare word chooses the
// command, and that `run` and `completion` never fall through to the TUI.
func TestRun_DispatchesSubcommands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	t.Run("completion", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		code := runWithin(t, runTimeout, []string{"completion", "bash"}, &stdout, &stderr)
		assert.Equal(t, exitOK, code)
		assert.Contains(t, stdout.String(), "complete -F _grpctui grpctui")
	})

	t.Run("the hidden value helper", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		code := runWithin(t, runTimeout, []string{"__complete", "shells"}, &stdout, &stderr)
		assert.Equal(t, exitOK, code)
		assert.Equal(t, "bash\nzsh\nfish\n", stdout.String())
	})

	t.Run("run", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		// No config file in this temp home, so there is no such collection —
		// which is enough to prove the word reached runCollection rather than
		// being taken for a target address.
		code := runWithin(t, runTimeout,
			[]string{"run", "-target", "localhost:50051", "smoke"}, &stdout, &stderr)
		assert.Equal(t, exitError, code)
		assert.Contains(t, stderr.String(), "no collection named")
	})
}

// loadedCollections writes files into a temp directory and loads them.
func loadedCollections(t *testing.T, files map[string]string) requests.Collections {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}

	collections, err := requests.LoadCollections(dir)
	require.NoError(t, err)
	return collections
}
