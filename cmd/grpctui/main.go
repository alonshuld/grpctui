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
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protofiles"
	"github.com/alonshuld/grpctui/internal/proxy"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui"
	"github.com/alonshuld/grpctui/internal/vars"
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

	// profile names the connection profile to start on, from the config file's
	// `profiles` list.
	profile string

	// env names the environment to start in, from the config file's
	// `environments` list, and variables are the `name=value` bindings the
	// command line adds on top of it.
	env       string
	variables assignmentList

	// The transport security flags. Each overrides the chosen profile's
	// setting, so that a saved connection can be reached over a different CA or
	// with verification off for one session without editing the file.
	tls        bool
	caCert     string
	clientCert string
	clientKey  string
	serverName string
	insecure   bool

	// headers are added to the profile's own, in `key: value` form.
	headers headerList

	// protoFiles are .proto sources to discover from instead of asking the
	// target, and importPaths the directories their imports resolve against.
	// Both are repeatable, and empty means discovery by reflection.
	protoFiles  pathList
	importPaths pathList

	// theme names the palette to draw in, from the built-in ones or the config
	// file's `themes` list.
	theme string

	// format is how a headless run reports itself: text or json. It is unused
	// by the interactive command, which reports itself by being on screen.
	format string

	logFile     string
	logLevel    string
	callTimeout time.Duration
	showVersion bool

	// historyFile and collectionsDir are where sent and saved requests live.
	// Either may be empty, which turns that half off — history keeps working for
	// the session, saving reports that there is nowhere to save to.
	historyFile    string
	collectionsDir string

	// historyLimit caps how many sent requests are kept.
	historyLimit int

	// proxy is the address to accept proxied gRPC traffic on, or empty for no
	// passive mode. Everything it receives is forwarded to the target and shown
	// in the traffic panel.
	proxy string

	// given records which flags were actually passed, which is the difference
	// between "the user asked for plaintext" and "the user said nothing about
	// transport security".
	given map[string]bool

	// usage prints the flag set's help. The target may come from the config
	// file, so whether one is missing is only known after the config has been
	// read — by which point the flag set is out of scope.
	usage func()
}

// headerList collects a repeatable -H flag.
type headerList []string

// String implements [flag.Value].
func (h *headerList) String() string { return strings.Join(*h, ", ") }

// Set implements [flag.Value].
func (h *headerList) Set(value string) error {
	*h = append(*h, value)
	return nil
}

// pathList collects a repeatable path flag: -proto and -import-path.
type pathList []string

// String implements [flag.Value].
func (p *pathList) String() string { return strings.Join(*p, ", ") }

// Set implements [flag.Value].
func (p *pathList) Set(value string) error {
	if value == "" {
		return errors.New("want a path, got an empty one")
	}
	*p = append(*p, value)
	return nil
}

// assignmentList collects a repeatable -V flag.
//
// It is separate from [headerList] only so that -V's names can be checked as
// they are given: a mistyped variable name is silent otherwise, since a
// reference to a name nothing binds is refused at the row rather than at
// startup, and by then the flag is long out of sight.
type assignmentList []string

// String implements [flag.Value]. Only the names are printed: a -V is as likely
// to carry a token as a -H is, and this ends up in usage output and error
// messages.
func (a *assignmentList) String() string {
	names := make([]string, 0, len(*a))
	for _, v := range *a {
		name, _, _ := vars.SplitAssignment(v)
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// Set implements [flag.Value].
func (a *assignmentList) Set(value string) error {
	name, _, ok := vars.SplitAssignment(value)
	if !ok {
		return fmt.Errorf("want `name=value`, got %q", value)
	}
	if err := vars.ValidateName(name); err != nil {
		return err
	}

	*a = append(*a, value)
	return nil
}

func run(args []string, stdout, stderr io.Writer) int {
	// Help is answered before anything is parsed, so that it reaches stdout and
	// exits 0 — a help page on stderr cannot be piped into a pager, and one that
	// exits 2 breaks `grpctui --help && …`.
	if wantsHelp(args) {
		var opts options
		printUsage(stdout, newFlagSet(commandName(args), &opts, stdout))
		return exitOK
	}

	if len(args) > 0 {
		switch args[0] {
		case cmdRun:
			return runCollection(args[1:], stdout, stderr)
		case cmdCompletion:
			return completion(args[1:], stdout, stderr)
		case cmdKeys:
			return printKeys(args[1:], stdout, stderr)
		case cmdComplete:
			return completeValues(args[1:], stdout)
		}
	}

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

	session, err := prepare(opts)
	if err != nil {
		// A missing target is the one startup failure that is a usage error:
		// there is nothing wrong with the configuration, the user simply has not
		// said where to connect. Everything else is a problem with something
		// they wrote down, and the help page would only be in the way of it.
		code := exitError
		if err.Error() == missingTarget {
			opts.usage()
			code = exitUsage
		}
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return code
	}
	defer session.close()

	if err := start(opts, session, stdout); err != nil {
		session.logger.Error("exiting with error", zap.Error(err))
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	return exitOK
}

// commandName is what the help page is being asked for: `grpctui run` has a
// flag the interactive command does not, and printing it under the wrong
// heading — or not at all — is the sort of thing help pages are blamed for.
func commandName(args []string) string {
	if len(args) > 0 && args[0] == cmdRun {
		return "grpctui " + cmdRun
	}
	return "grpctui"
}

// startup is everything the flags and the config file decide before anything is
// dialled: the connections available, which one to open, and why any of the
// others could not be.
type startup struct {
	profiles []grpcclient.Profile

	// problems runs parallel to profiles: problems[i] is why profiles[i] cannot
	// be dialled, or nil. A profile naming an environment variable nobody
	// exported is listed and refused when chosen, rather than taking the whole
	// file down with it at startup — a production token you do not have should
	// stop you reaching production, not your own laptop.
	problems []error

	active int

	// schema is the .proto-file fallback, empty unless -proto was given. It is
	// a property of the session rather than of one profile: the files describe
	// an API, and switching to another connection serving the same API must not
	// mean losing them.
	schema protofiles.Schema
}

// environs is everything the flags and the config file decide about variables:
// the environments available, which one to start in, and why any of the others
// cannot be used.
type environs struct {
	list []vars.Environment

	// problems runs parallel to list, exactly as [startup.problems] does: an
	// environment naming a ${VAR} nobody exported is listed and refused when it
	// is chosen, rather than taking the whole file down at startup.
	problems []error

	// active indexes list, or is -1 when no environments are configured — which
	// is the ordinary case, and resolves nothing.
	active int
}

// environments assembles the variable sets and says which one to start in.
//
// A -V binding is laid over *every* environment rather than only the active
// one. It is an override the user typed for this session, and having it vanish
// on switching environment would make it a surprise rather than an override.
func environments(cfg config.Config, opts options) (environs, error) {
	var e environs
	e.list, e.problems = cfg.Environments()

	active, err := config.SelectEnvironment(e.list, opts.env)
	if err != nil {
		return environs{}, err
	}
	e.active = active

	// The environment actually being used has to work now, so its problem is
	// raised here rather than deferred to a switch nobody has asked for yet.
	if active >= 0 {
		if err := e.problems[active]; err != nil {
			return environs{}, err
		}
	}

	overrides, err := parseAssignments(opts.variables)
	if err != nil {
		return environs{}, err
	}
	if len(overrides) == 0 {
		return e, nil
	}

	// With no environments configured at all, the command line's bindings are
	// still worth having, so they become one.
	if len(e.list) == 0 {
		e.list = []vars.Environment{{Name: commandLineEnvironment}}
		e.problems = []error{nil}
		e.active = 0
	}
	for i := range e.list {
		e.list[i].Variables = vars.NewSet(append(
			slices.Clone(e.list[i].Variables), overrides...)).All()
	}
	return e, nil
}

// commandLineEnvironment is what the -V bindings are called when there is no
// environment in the config file for them to sit in.
const commandLineEnvironment = "command line"

// environment is the variable set to start with, or the zero one when none are
// configured.
func (e environs) environment() vars.Environment {
	if e.active < 0 || e.active >= len(e.list) {
		return vars.Environment{}
	}
	return e.list[e.active]
}

// target is the address the starting environment points at, if it names one.
func (e environs) target() string { return e.environment().Target }

// parseAssignments reads the -V flags. Their form is already checked as they
// are given — see [assignmentList.Set] — so this only splits them.
func parseAssignments(values []string) ([]vars.Variable, error) {
	out := make([]vars.Variable, 0, len(values))
	for _, v := range values {
		name, value, ok := vars.SplitAssignment(v)
		if !ok {
			return nil, fmt.Errorf("variable %q: want `name=value`", v)
		}
		out = append(out, vars.Variable{Name: name, Value: value})
	}
	return out, nil
}

// connections assembles the connection profiles and says which one to start on.
//
// The flags override the chosen profile rather than replacing it: `grpctui
// --profile staging --insecure` is a saved connection with verification turned
// off for one session, not a new connection that has lost its credentials.
//
// envTarget is where the starting environment points, which sits between the
// profile and the command line: a profile says how to reach a server, an
// environment says which server, and a target typed on the command line is the
// most specific thing the user could have said.
func connections(cfg config.Config, opts options, envTarget string) (startup, error) {
	var s startup
	s.profiles, s.problems = cfg.Connections()

	// A run with no config file at all is grpctui's whole pitch, and it still
	// has to have somewhere to connect to.
	if len(s.profiles) == 0 {
		s.profiles = []grpcclient.Profile{{}}
		s.problems = []error{nil}
	}

	active, err := config.Select(s.profiles, opts.profile)
	if err != nil {
		return startup{}, err
	}
	s.active = active

	// The connection actually being opened has to work now, so its problem is
	// raised here rather than deferred to a dial nobody asked for yet.
	if err := s.problems[active]; err != nil {
		return startup{}, err
	}

	if envTarget != "" {
		s.profiles[active].Target = envTarget
	}
	if s.profiles[active], err = applyFlags(s.profiles[active], opts); err != nil {
		return startup{}, err
	}

	// A missing target is left for the caller to report: it is a usage error
	// with a usage message, not a profile that is wrong about something.
	if s.profiles[active].Target != "" {
		if err := s.profiles[active].Validate(); err != nil {
			return startup{}, err
		}
	}
	return s, nil
}

// profile is the connection to open first.
func (s startup) profile() grpcclient.Profile { return s.profiles[s.active] }

// problem reports why a profile cannot be dialled, matched by name. Names are
// unique — the config loader refuses a file where they are not — so the name
// identifies a profile without comparing whole structs.
func (s startup) problem(name string) error {
	for i, p := range s.profiles {
		if p.Name == name {
			return s.problems[i]
		}
	}
	return nil
}

// applyFlags lays the command line over a profile.
func applyFlags(p grpcclient.Profile, opts options) (grpcclient.Profile, error) {
	if opts.target != "" {
		p.Target = opts.target
	}

	// Naming a certificate, a CA or a server name is asking for TLS; requiring
	// --tls beside them would only be a way to get an error message. Passing
	// --tls=false with them is still refused, by [grpcclient.Security.Validate],
	// because there it is a contradiction rather than an omission.
	for _, f := range []struct {
		name string
		set  func()
	}{
		{flagCACert, func() { p.Security.CACert = opts.caCert }},
		{flagCert, func() { p.Security.ClientCert = opts.clientCert }},
		{flagKey, func() { p.Security.ClientKey = opts.clientKey }},
		{flagServerName, func() { p.Security.ServerName = opts.serverName }},
		{flagInsecure, func() { p.Security.InsecureSkipVerify = opts.insecure }},
	} {
		if opts.given[f.name] {
			f.set()
			p.Security.TLS = true
		}
	}
	if opts.given[flagTLS] {
		p.Security.TLS = opts.tls
	}

	headers, err := parseHeaders(opts.headers)
	if err != nil {
		return grpcclient.Profile{}, err
	}
	p.Metadata = append(p.Metadata.Clone(), headers...)

	return p, nil
}

// parseHeaders reads the -H flags, in grpcurl's `key: value` form.
func parseHeaders(values []string) (grpcclient.Metadata, error) {
	if len(values) == 0 {
		return nil, nil
	}

	md := make(grpcclient.Metadata, 0, len(values))
	for _, v := range values {
		key, value, ok := strings.Cut(v, ":")
		if !ok {
			return nil, fmt.Errorf("header %q: want `key: value`", v)
		}

		header := grpcclient.Header{
			Key:   strings.TrimSpace(key),
			Value: strings.TrimSpace(value),
		}
		if err := grpcclient.ValidateHeader(header); err != nil {
			return nil, fmt.Errorf("header %q: %w", v, err)
		}
		md = append(md, header)
	}
	return md, nil
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

// startProxy binds the passive proxy and starts serving, returning it and the
// way to shut it down.
//
// The connection it forwards over carries the profile's *security* and none of
// its credentials. A passive proxy exists to show what an application is
// sending, and one that quietly added grpctui's own bearer token would be
// showing the user something the application never sent — which is the one
// thing a wire-watching tool may not do.
//
// It is pinned to the target grpctui started on. Switching connection or
// environment in the UI moves the calls the user makes; where the proxy
// forwards to is decided once, because an application pointed at it has no way
// to know that its destination changed underneath it.
func startProxy(listen string, p grpcclient.Profile, logger *zap.Logger) (*proxy.Proxy, func(), error) {
	upstream, err := grpcclient.DialProfile(grpcclient.Profile{
		Target:   p.Target,
		Security: p.Security,
	}, grpcclient.WithLogger(logger))
	if err != nil {
		return nil, nil, fmt.Errorf("proxy upstream: %w", err)
	}

	watcher := proxy.New(listen, upstream.Conn(), proxy.WithLogger(logger))
	if err := watcher.Listen(); err != nil {
		_ = upstream.Close()
		return nil, nil, err
	}

	// Serving runs for the life of the program. Its error is logged rather than
	// returned: by the time it happens the TUI owns the terminal, and a proxy
	// that has stopped is not a reason to take the session down — the traffic
	// panel simply stops filling.
	go func() {
		if err := watcher.Serve(); err != nil {
			logger.Error("the proxy stopped", zap.Error(err))
		}
	}()

	logger.Info("proxying",
		zap.String("listen", watcher.Addr()),
		zap.String("upstream", p.Target),
		zap.String("security", p.Security.Mode()),
	)

	return watcher, func() {
		watcher.Stop()
		_ = upstream.Close()
	}, nil
}

// dialer opens connections for the UI's profile switcher.
//
// A profile the config file could not finish reading — an unset environment
// variable in a token, say — fails here, with the reason it was set aside at
// startup. That is the point at which the user has asked for it, and the point
// at which the answer is useful.
func (s startup) dialer(logger *zap.Logger) ui.DialerFunc {
	return func(profile grpcclient.Profile) (ui.Client, error) {
		if err := s.problem(profile.Name); err != nil {
			return nil, err
		}
		return grpcclient.DialProfile(profile, dialOptions(s.schema, logger)...)
	}
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var opts options

	fs := newFlagSet("grpctui", &opts, stderr)

	targets, err := parseArgs(fs, args)
	if err != nil {
		return opts, err
	}
	opts.record(fs)

	// A missing target is not decided here: it may still come from the config
	// file, which is loaded once the flags — including --config — are known.
	if len(targets) > 1 {
		fs.Usage()
		return opts, fmt.Errorf("expected one target address, got %d", len(targets))
	}
	if len(targets) == 1 {
		opts.target = targets[0]
	}

	return opts, nil
}

// newFlagSet builds a flag set with grpctui's flags on it and its help page
// attached.
func newFlagSet(name string, opts *options, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerFlags(fs, opts)
	if name == "grpctui "+cmdRun {
		registerRunFlags(fs, opts)
	}

	fs.Usage = func() { printUsage(stderr, fs) }
	opts.usage = fs.Usage
	return fs
}

// record notes which flags were actually given, which is the difference between
// "the user chose this config file" and "this is where one would live" — and,
// for the security flags, between an instruction and a default.
func (o *options) record(fs *flag.FlagSet) {
	o.given = make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		o.given[f.Name] = true
	})
	o.configNamed = o.given[flagConfig]
}
