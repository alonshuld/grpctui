// start.go loads what a session needs off disk and hands it to bubbletea.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui"
	"github.com/alonshuld/grpctui/internal/version"
)

// loadConfig reads the config file, holding a path the user named to a higher
// standard than the default one: the default may be absent, a named one may
// not.
func loadConfig(opts options) (config.Config, error) {
	if opts.configNamed {
		return config.LoadFile(opts.configFile)
	}
	return config.Load(opts.configFile)
}

// saved is the requests already sent and the ones kept on purpose.
type saved struct {
	history     requests.History
	collections requests.Collections
}

// loadRequests reads history and collections.
//
// A file that cannot be parsed stops startup rather than being replaced or
// skipped. It is the rule internal/config already follows, and it matters more
// here: the alternative to an error is grpctui writing a fresh history over the
// broken one on the very next send, or a collection someone committed silently
// not being in the list. Either is a way to lose work, and `--history-file=`
// gets past it in one flag.
func loadRequests(opts options) (saved, error) {
	history, err := requests.LoadHistory(opts.historyFile, opts.historyLimit)
	if err != nil {
		return saved{}, err
	}

	collections, err := requests.LoadCollections(opts.collectionsDir)
	if err != nil {
		return saved{}, err
	}
	return saved{history: history, collections: collections}, nil
}

// start runs the TUI. Everything it writes goes to out, which is the terminal
// bubbletea takes over; the logger writes to a file only.
func start(opts options, s session, out io.Writer) error {
	startup, envs, saved, look, logger := s.startup, s.envs, s.saved, s.look, s.logger

	// SIGINT/SIGTERM cancel in-flight RPCs; bubbletea handles ctrl+c itself,
	// so this covers signals sent from elsewhere.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := grpcclient.DialProfile(startup.profile(), dialOptions(startup.schema, logger)...)
	if err != nil {
		return err
	}

	// Variable names, never their values: this log outlives the session, and a
	// value captured out of a login response is a token.
	logger.Info("starting grpctui",
		zap.String("target", startup.profile().Target),
		zap.String("profile", startup.profile().Label()),
		zap.String("environment", envs.environment().Name),
		zap.Strings("variables", envs.environment().Set().Names()),
		zap.String("version", version.Version()),
	)

	uiOpts := []ui.Option{
		ui.WithLogger(logger),
		ui.WithContext(ctx),
		ui.WithCallTimeout(opts.callTimeout),
		ui.WithDialer(startup.dialer(logger)),
		ui.WithProfiles(startup.profiles, startup.active),
		ui.WithEnvironments(envs.list, envs.active),
		ui.WithHistory(saved.history),
		ui.WithCollections(saved.collections),
		ui.WithThemes(look.palettes.list, look.palettes.active),
		ui.WithKeyMap(look.keys),
		ui.WithRenderers(look.renderers),
	}

	if opts.proxy != "" {
		watcher, stopProxy, err := startProxy(opts.proxy, startup.profile(), logger)
		if err != nil {
			return err
		}
		defer stopProxy()
		uiOpts = append(uiOpts, ui.WithTraffic(watcher.Events(), watcher.Addr(), watcher.Dropped))
	}

	model := ui.New(client, uiOpts...)
	program := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithOutput(out),
		tea.WithAltScreen(),
	)

	// The model owns the connection from here: switching profile replaces it,
	// so the one to close at the end is whichever the *final* model holds.
	final, runErr := program.Run()
	if m, ok := final.(ui.Model); ok {
		_ = m.Close()
	}
	_ = client.Close()

	if runErr != nil && !errors.Is(runErr, tea.ErrProgramKilled) {
		return fmt.Errorf("run ui: %w", runErr)
	}
	return nil
}
