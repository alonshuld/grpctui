package logging_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/logging"
)

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var records []map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var rec map[string]any
		require.NoError(t, dec.Decode(&rec))
		records = append(records, rec)
	}
	return records
}

func TestNew_WritesJSONToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grpctui.log")

	logger, closeFn, err := logging.New(logging.Config{File: path, Level: "info"})
	require.NoError(t, err)

	logger.Info("dialed target", zap.String("addr", "localhost:50051"))
	require.NoError(t, closeFn())

	records := readRecords(t, path)
	require.Len(t, records, 1)
	assert.Equal(t, "dialed target", records[0]["msg"])
	assert.Equal(t, "localhost:50051", records[0]["addr"])
	assert.Equal(t, "info", records[0]["level"])
}

func TestNew_CreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state", "grpctui.log")

	logger, closeFn, err := logging.New(logging.Config{File: path, Level: "info"})
	require.NoError(t, err)
	logger.Info("hello")
	require.NoError(t, closeFn())

	assert.FileExists(t, path)
}

func TestNew_LevelFiltering(t *testing.T) {
	tests := map[string]struct {
		level     string
		wantCount int
	}{
		"default level is error": {level: "", wantCount: 1},
		"explicit error":         {level: "error", wantCount: 1},
		"warn admits warn+error": {level: "warn", wantCount: 2},
		"debug admits all":       {level: "debug", wantCount: 4},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "grpctui.log")

			logger, closeFn, err := logging.New(logging.Config{File: path, Level: tt.level})
			require.NoError(t, err)

			logger.Debug("debug")
			logger.Info("info")
			logger.Warn("warn")
			logger.Error("error")
			require.NoError(t, closeFn())

			assert.Len(t, readRecords(t, path), tt.wantCount)
		})
	}
}

func TestNew_AppendsAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grpctui.log")

	for range 2 {
		logger, closeFn, err := logging.New(logging.Config{File: path, Level: "info"})
		require.NoError(t, err)
		logger.Info("run")
		require.NoError(t, closeFn())
	}

	assert.Len(t, readRecords(t, path), 2)
}

// A TUI owns the terminal: a logger that writes anywhere but the file sink
// corrupts the render. This pins that no configuration produces console output.
func TestNew_NeverWritesToStdio(t *testing.T) {
	tests := map[string]logging.Config{
		"file sink":  {File: filepath.Join(t.TempDir(), "grpctui.log"), Level: "debug"},
		"no logging": {},
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			restore := captureStdio(t)

			logger, closeFn, err := logging.New(cfg)
			require.NoError(t, err)
			logger.Debug("debug")
			logger.Error("error", zap.String("k", "v"))
			require.NoError(t, closeFn())

			stdout, stderr := restore()
			assert.Empty(t, stdout, "logger wrote to stdout")
			assert.Empty(t, stderr, "logger wrote to stderr")
		})
	}
}

// captureStdio redirects os.Stdout and os.Stderr to temp files, returning a
// function that restores them and reports what was written.
func captureStdio(t *testing.T) func() (stdout, stderr string) {
	t.Helper()

	dir := t.TempDir()
	newFile := func(name string) *os.File {
		f, err := os.Create(filepath.Join(dir, name))
		require.NoError(t, err)
		return f
	}

	origOut, origErr := os.Stdout, os.Stderr
	outFile, errFile := newFile("stdout"), newFile("stderr")
	os.Stdout, os.Stderr = outFile, errFile

	return func() (string, string) {
		os.Stdout, os.Stderr = origOut, origErr
		require.NoError(t, outFile.Close())
		require.NoError(t, errFile.Close())

		out, err := os.ReadFile(outFile.Name())
		require.NoError(t, err)
		errOut, err := os.ReadFile(errFile.Name())
		require.NoError(t, err)
		return string(out), string(errOut)
	}
}

func TestNew_NoFileYieldsNopLogger(t *testing.T) {
	logger, closeFn, err := logging.New(logging.Config{})
	require.NoError(t, err)
	require.NotNil(t, logger)
	require.NotNil(t, closeFn)

	// A Nop logger has no enabled levels, so nothing is ever encoded.
	assert.False(t, logger.Core().Enabled(zap.ErrorLevel))
	assert.NoError(t, closeFn())
}

func TestNew_InvalidLevel(t *testing.T) {
	_, _, err := logging.New(logging.Config{
		File:  filepath.Join(t.TempDir(), "grpctui.log"),
		Level: "chatty",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid log level "chatty"`)
}

func TestNew_UnopenableFile(t *testing.T) {
	// A path whose parent is an existing file cannot be created.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	_, _, err := logging.New(logging.Config{File: filepath.Join(blocker, "grpctui.log")})
	require.Error(t, err)
}

func TestDefaultFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join("custom", "state"))
	assert.Equal(t, filepath.Join("custom", "state", "grpctui", "grpctui.log"), logging.DefaultFile())

	t.Setenv("XDG_STATE_HOME", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".local", "state", "grpctui", "grpctui.log"), logging.DefaultFile())
}
