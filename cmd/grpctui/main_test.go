package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/version"
)

// Only the paths that return before the TUI starts are exercised here: once
// tea.Program takes the terminal there is nothing meaningful to assert from a
// non-interactive test. Behaviour beyond this point is covered by
// internal/ui's teatest suite.
func TestRun(t *testing.T) {
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
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := run(tt.args, &stdout, &stderr)

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
