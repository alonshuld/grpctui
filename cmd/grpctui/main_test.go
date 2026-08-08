package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui"
	"github.com/alonshuld/grpctui/internal/version"
)

// runWithin runs the CLI and fails if it does not come back.
//
// Only the paths that return before the TUI starts can be exercised here, and a
// case that slips through and reaches tea.Program does not fail — it blocks on
// a terminal that is not there until the whole test binary times out, ten
// minutes later, naming nothing. That is precisely what a config path with
// Unix-only semantics did on Windows. This turns it into an immediate failure
// against the case responsible.
func runWithin(t *testing.T, d time.Duration, args []string, stdout, stderr io.Writer) int {
	t.Helper()

	done := make(chan int, 1)
	go func() { done <- run(args, stdout, stderr) }()

	select {
	case code := <-done:
		return code
	case <-time.After(d):
		t.Fatalf("run(%q) did not return within %s: it reached the TUI", args, d)
		return 0
	}
}

// runTimeout is generous — these paths do no I/O worth the name, so anything
// approaching it means the call is never coming back.
const runTimeout = 30 * time.Second

// Only the paths that return before the TUI starts are exercised here: once
// tea.Program takes the terminal there is nothing meaningful to assert from a
// non-interactive test. Behaviour beyond this point is covered by
// internal/ui's teatest suite.
func TestRun(t *testing.T) {
	// The default config path is derived from the environment, and a real one
	// on the developer's machine would supply a target these cases assume is
	// missing. The state directory goes the same way: the default history file
	// lives there, and a run of the tests must not read the developer's own.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	brokenHistory := filepath.Join(t.TempDir(), "history.yaml")
	require.NoError(t, os.WriteFile(brokenHistory, []byte("requests: [oh dear\n"), 0o600))

	brokenCollections := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(brokenCollections, "team.yaml"), []byte("requests: [oh dear\n"), 0o600))

	tests := map[string]struct {
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		"version": {
			args:       []string{"--version"},
			wantCode:   exitOK,
			wantStdout: "grpctui " + version.Version(),
		},
		"version ignores a missing target": {
			args:     []string{"-version"},
			wantCode: exitOK,
		},
		// Help goes to stdout and exits 0, so that it can be piped into a pager
		// and so that `grpctui --help && …` does not stop there.
		"help": {
			args:       []string{"--help"},
			wantCode:   exitOK,
			wantStdout: "Usage:",
		},
		"-h": {
			args:       []string{"-h"},
			wantCode:   exitOK,
			wantStdout: "grpctui completion <shell>",
		},
		"no target": {
			args:       nil,
			wantCode:   exitUsage,
			wantStderr: "missing target address",
		},
		"too many targets": {
			args:       []string{"localhost:50051", "localhost:50052"},
			wantCode:   exitUsage,
			wantStderr: "expected one target address, got 2",
		},
		"unknown flag": {
			args:       []string{"--nope"},
			wantCode:   exitUsage,
			wantStderr: "flag provided but not defined",
		},
		"invalid log level": {
			args:       []string{"--log-level", "chatty", "localhost:50051"},
			wantCode:   exitError,
			wantStderr: `invalid log level "chatty"`,
		},
		// A named config file that is not there is an error, not a shrug.
		// Every case in this table must fail before start() is reached: one
		// that does not launches the real TUI and hangs until the test binary's
		// timeout, which is exactly how this case used to behave on Windows.
		"named config is missing": {
			args:       []string{"--config", filepath.Join(t.TempDir(), "absent.yaml"), "localhost:50051"},
			wantCode:   exitError,
			wantStderr: "open config",
		},
		// A history file that cannot be parsed stops startup rather than being
		// quietly overwritten by the first send of the session.
		"history is malformed": {
			args:       []string{"--history-file", brokenHistory, "localhost:50051"},
			wantCode:   exitError,
			wantStderr: "history.yaml",
		},
		// The same for a collection, which somebody hand-wrote and may well have
		// committed: skipping it looks exactly like a request that never saved.
		"a collection is malformed": {
			args:       []string{"--collections", brokenCollections, "localhost:50051"},
			wantCode:   exitError,
			wantStderr: "team.yaml",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := runWithin(t, runTimeout, tt.args, &stdout, &stderr)

			assert.Equal(t, tt.wantCode, code)
			if tt.wantStdout != "" {
				assert.Contains(t, stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" {
				assert.Contains(t, stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestParseFlags_Defaults(t *testing.T) {
	var stderr bytes.Buffer

	opts, err := parseFlags([]string{"localhost:50051"}, &stderr)

	require.NoError(t, err)
	assert.Equal(t, "localhost:50051", opts.target)
	assert.Equal(t, "error", opts.logLevel, "a normal run must be near-silent")
	assert.NotEmpty(t, opts.logFile)
	assert.Equal(t, ui.DefaultCallTimeout, opts.callTimeout)
	assert.Equal(t, requests.DefaultLimit, opts.historyLimit)
}

// Both halves of v0.6's storage can be turned off, and turning one off must not
// stop grpctui starting: history keeps working for the session, saving reports
// that there is nowhere to save to.
func TestLoadRequests_Disabled(t *testing.T) {
	var stderr bytes.Buffer

	opts, err := parseFlags([]string{
		"--history-file", "",
		"--collections", "",
		"localhost:50051",
	}, &stderr)
	require.NoError(t, err)

	saved, err := loadRequests(opts)
	require.NoError(t, err)
	assert.Equal(t, 0, saved.history.Len())
	assert.Equal(t, 0, saved.collections.Len())
}

func TestLoadRequests(t *testing.T) {
	dir := t.TempDir()
	historyFile := filepath.Join(dir, "history.yaml")
	collectionsDir := filepath.Join(dir, "collections")

	require.NoError(t, os.WriteFile(historyFile,
		[]byte("requests:\n  - method: a.B\n"), 0o600))
	require.NoError(t, os.Mkdir(collectionsDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(collectionsDir, "team.yaml"),
		[]byte("requests:\n  - name: one\n    method: a.B\n"), 0o600))

	var stderr bytes.Buffer
	opts, err := parseFlags([]string{
		"--history-file", historyFile,
		"--collections", collectionsDir,
		"localhost:50051",
	}, &stderr)
	require.NoError(t, err)

	saved, err := loadRequests(opts)
	require.NoError(t, err)
	assert.Equal(t, 1, saved.history.Len())
	assert.Equal(t, []string{"team"}, saved.collections.Names())
}

func TestParseFlags_Overrides(t *testing.T) {
	var stderr bytes.Buffer
	logFile := filepath.Join(t.TempDir(), "grpctui.log")

	opts, err := parseFlags([]string{
		"--log-file", logFile,
		"--log-level", "debug",
		"--call-timeout", "5s",
		"example.com:443",
	}, &stderr)

	require.NoError(t, err)
	assert.Equal(t, "example.com:443", opts.target)
	assert.Equal(t, logFile, opts.logFile)
	assert.Equal(t, "debug", opts.logLevel)
	assert.Equal(t, 5*time.Second, opts.callTimeout,
		"a service slower than the default needs this reachable without a rebuild")
}

// An empty --log-file disables logging rather than falling back to a console
// sink, which would corrupt the TUI's render.
func TestParseFlags_EmptyLogFileDisablesLogging(t *testing.T) {
	var stderr bytes.Buffer

	opts, err := parseFlags([]string{"--log-file", "", "localhost:50051"}, &stderr)

	require.NoError(t, err)
	assert.Empty(t, opts.logFile)
}

// writeConfig puts a config file in a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// The target may come from the config file, but an argument always wins: a
// config file is a default, not an override.
func TestRun_TargetPrecedence(t *testing.T) {
	cfg := writeConfig(t, "target: config.example:50051\n")

	t.Run("the config file supplies a missing target", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		// --log-level is invalid on purpose: it fails after the target has been
		// resolved but before the TUI takes the terminal, which is as far as a
		// non-interactive test can go.
		code := runWithin(t, runTimeout, []string{"--config", cfg, "--log-level", "chatty"}, &stdout, &stderr)

		assert.Equal(t, exitError, code)
		assert.NotContains(t, stderr.String(), "missing target address")
	})

	t.Run("an argument beats the config file", func(t *testing.T) {
		opts, err := parseFlags([]string{"--config", cfg, "argument.example:50051"}, &bytes.Buffer{})

		require.NoError(t, err)
		assert.Equal(t, "argument.example:50051", opts.target)
	})

	t.Run("no target anywhere is a usage error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		code := runWithin(t, runTimeout, []string{"--config", writeConfig(t, "# nothing here\n")}, &stdout, &stderr)

		assert.Equal(t, exitUsage, code)
		assert.Contains(t, stderr.String(), "missing target address")
		assert.Contains(t, stderr.String(), "Usage:")
	})
}
