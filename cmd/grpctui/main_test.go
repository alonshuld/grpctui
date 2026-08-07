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
	// missing.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

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
		"help": {
			args:       []string{"--help"},
			wantCode:   exitOK,
			wantStderr: "Usage:",
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
}

func TestParseFlags_Overrides(t *testing.T) {
	var stderr bytes.Buffer
	logFile := filepath.Join(t.TempDir(), "grpctui.log")

	opts, err := parseFlags([]string{
		"--log-file", logFile,
		"--log-level", "debug",
		"example.com:443",
	}, &stderr)

	require.NoError(t, err)
	assert.Equal(t, "example.com:443", opts.target)
	assert.Equal(t, logFile, opts.logFile)
	assert.Equal(t, "debug", opts.logLevel)
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
