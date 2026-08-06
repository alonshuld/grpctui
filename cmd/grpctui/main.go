// Command grpctui is a terminal UI for exploring, calling, and debugging gRPC
// services.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/logging"
	"github.com/alonshuld/grpctui/internal/ui"
	"github.com/alonshuld/grpctui/internal/version"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	target      string
	logFile     string
	logLevel    string
	showVersion bool
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitUsage
	}

	if opts.showVersion {
		_, _ = fmt.Fprintf(stdout, "grpctui %s\n", version.Version())
		return exitOK
	}

	logger, closeLog, err := logging.New(logging.Config{File: opts.logFile, Level: opts.logLevel})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	defer func() { _ = closeLog() }()

	if err := start(opts, logger, stdout); err != nil {
		logger.Error("exiting with error", zap.Error(err))
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	return exitOK
}

// start runs the TUI. Everything it writes goes to out, which is the terminal
// bubbletea takes over; the logger writes to a file only.
func start(opts options, logger *zap.Logger, out io.Writer) error {
	// SIGINT/SIGTERM cancel in-flight RPCs; bubbletea handles ctrl+c itself,
	// so this covers signals sent from elsewhere.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := grpcclient.Dial(opts.target, grpcclient.WithLogger(logger))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	logger.Info("starting grpctui",
		zap.String("target", opts.target),
		zap.String("version", version.Version()),
	)

	model := ui.New(client, ui.WithLogger(logger), ui.WithContext(ctx))
	program := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithOutput(out),
		tea.WithAltScreen(),
	)

	if _, err := program.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return fmt.Errorf("run ui: %w", err)
	}
	return nil
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var opts options

	fs := flag.NewFlagSet("grpctui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.logFile, "log-file", logging.DefaultFile(),
		"write logs to this file; empty disables logging")
	fs.StringVar(&opts.logLevel, "log-level", "error",
		"log level: debug, info, warn, error")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "grpctui — a terminal UI for gRPC\n\n")
		_, _ = fmt.Fprintf(stderr, "Usage:\n  grpctui [flags] <host:port>\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return opts, err
	}

	if opts.showVersion {
		return opts, nil
	}

	switch fs.NArg() {
	case 0:
		fs.Usage()
		return opts, errors.New("missing target address")
	case 1:
		opts.target = fs.Arg(0)
	default:
		fs.Usage()
		return opts, fmt.Errorf("expected one target address, got %d", fs.NArg())
	}

	return opts, nil
}
