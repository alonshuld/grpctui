// Package logging constructs the single *zap.Logger that the rest of grpctui
// passes down through constructors.
//
// A TUI owns the terminal: anything written to stdout or stderr while
// bubbletea holds the screen corrupts the render. Every logger built here
// therefore writes to a file and nothing else — including zap's own internal
// error output, which defaults to stderr and must be redirected too.
package logging

import (
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config selects where and how verbosely grpctui logs.
type Config struct {
	// File is the log file path. An empty path disables logging entirely and
	// yields a no-op logger — never a console fallback.
	File string

	// Level is a zapcore level name ("debug", "info", "warn", "error", ...).
	// An empty level means [zapcore.ErrorLevel], so a normal run writes almost
	// nothing.
	Level string
}

// CloseFunc flushes and releases the logger's file sink.
type CloseFunc func() error

// New builds a logger from cfg. It returns a no-op logger and a no-op
// [CloseFunc] when cfg.File is empty, so callers never need to nil-check
// either result.
func New(cfg Config) (*zap.Logger, CloseFunc, error) {
	if cfg.File == "" {
		return zap.NewNop(), func() error { return nil }, nil
	}

	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, nil, err
	}

	f, err := openLogFile(cfg.File)
	if err != nil {
		return nil, nil, err
	}

	sink := zapcore.Lock(f)
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encCfg), sink, level)

	// ErrorOutput is set explicitly: zap defaults it to stderr, which would
	// scribble over the rendered UI the first time an encoder failed.
	logger := zap.New(core,
		zap.ErrorOutput(sink),
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)

	closeFn := func() error {
		// Sync on a regular file can report EINVAL on some platforms; the
		// close below is what actually matters.
		_ = logger.Sync()
		return f.Close()
	}
	return logger, closeFn, nil
}

// DefaultFile reports the default log file path,
// $XDG_STATE_HOME/grpctui/grpctui.log, falling back to
// ~/.local/state/grpctui/grpctui.log. It returns an empty string when neither
// the state dir nor the home dir can be determined, which disables logging
// rather than failing startup.
func DefaultFile() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "grpctui", "grpctui.log")
}

func parseLevel(name string) (zapcore.Level, error) {
	if name == "" {
		return zapcore.ErrorLevel, nil
	}
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(name)); err != nil {
		return 0, fmt.Errorf("invalid log level %q: %w", name, err)
	}
	return level, nil
}

func openLogFile(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		// 0700/0600: the log may name targets and file paths, so it is the
		// user's business and nobody else's.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create log directory %q: %w", dir, err)
		}
	}
	// #nosec G304 -- the path is the log file the user asked for, via
	// --log-file or the XDG default; opening it is the whole point.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return f, nil
}
