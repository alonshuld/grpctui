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
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/config"
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
	target     string
	configFile string

	// configNamed records whether --config was given rather than defaulted. A
	// config file the user named has to exist; the default one does not.
	configNamed bool

	logFile     string
	logLevel    string
	callTimeout time.Duration
	showVersion bool

	// usage prints the flag set's help. The target may come from the config
	// file, so whether one is missing is only known after the config has been
	// read — by which point the flag set is out of scope.
	usage func()
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

	cfg, err := loadConfig(opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	if opts.target == "" {
		opts.target = cfg.Target
	}
	if opts.target == "" {
		opts.usage()
		_, _ = fmt.Fprintf(stderr, "grpctui: missing target address\n")
		return exitUsage
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

// loadConfig reads the config file, holding a path the user named to a higher
// standard than the default one: the default may be absent, a named one may
// not.
func loadConfig(opts options) (config.Config, error) {
	if opts.configNamed {
		return config.LoadFile(opts.configFile)
	}
	return config.Load(opts.configFile)
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

	model := ui.New(client,
		ui.WithLogger(logger),
		ui.WithContext(ctx),
		ui.WithCallTimeout(opts.callTimeout),
	)
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
	fs.StringVar(&opts.configFile, "config", config.DefaultPath(),
		"read settings from this file; empty skips it")
	fs.StringVar(&opts.logFile, "log-file", logging.DefaultFile(),
		"write logs to this file; empty disables logging")
	fs.StringVar(&opts.logLevel, "log-level", "error",
		"log level: debug, info, warn, error")
	fs.DurationVar(&opts.callTimeout, "call-timeout", ui.DefaultCallTimeout,
		"give up on a single call after this long")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "grpctui — a terminal UI for gRPC\n\n")
		_, _ = fmt.Fprintf(stderr, "Usage:\n  grpctui [flags] <host:port>\n\n"+
			"The target may also come from the config file's `target` key,\n"+
			"in which case it can be left off the command line.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	opts.usage = fs.Usage

	if err := fs.Parse(args); err != nil {
		return opts, err
	}

	// Visit reports only the flags actually given, which is the difference
	// between "the user chose this config file" and "this is where one would
	// live".
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			opts.configNamed = true
		}
	})

	// A missing target is not decided here: it may still come from the config
	// file, which is loaded once the flags — including --config — are known.
	if fs.NArg() > 1 {
		fs.Usage()
		return opts, fmt.Errorf("expected one target address, got %d", fs.NArg())
	}
	if fs.NArg() == 1 {
		opts.target = fs.Arg(0)
	}

	return opts, nil
}
