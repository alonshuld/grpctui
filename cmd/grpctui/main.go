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
	"github.com/alonshuld/grpctui/internal/logging"
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

	envs, err := environments(cfg, opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}

	startup, err := connections(cfg, opts, envs.target())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	if startup.profile().Target == "" {
		opts.usage()
		_, _ = fmt.Fprintf(stderr, "grpctui: missing target address\n")
		return exitUsage
	}

	saved, err := loadRequests(opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}

	logger, closeLog, err := logging.New(logging.Config{File: opts.logFile, Level: opts.logLevel})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	defer func() { _ = closeLog() }()

	if err := start(opts, startup, envs, saved, logger, stdout); err != nil {
		logger.Error("exiting with error", zap.Error(err))
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	return exitOK
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
		{"cacert", func() { p.Security.CACert = opts.caCert }},
		{"cert", func() { p.Security.ClientCert = opts.clientCert }},
		{"key", func() { p.Security.ClientKey = opts.clientKey }},
		{"servername", func() { p.Security.ServerName = opts.serverName }},
		{"insecure", func() { p.Security.InsecureSkipVerify = opts.insecure }},
	} {
		if opts.given[f.name] {
			f.set()
			p.Security.TLS = true
		}
	}
	if opts.given["tls"] {
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
func start(opts options, startup startup, envs environs, saved saved, logger *zap.Logger, out io.Writer) error {
	// SIGINT/SIGTERM cancel in-flight RPCs; bubbletea handles ctrl+c itself,
	// so this covers signals sent from elsewhere.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := grpcclient.DialProfile(startup.profile(), grpcclient.WithLogger(logger))
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

	model := ui.New(client,
		ui.WithLogger(logger),
		ui.WithContext(ctx),
		ui.WithCallTimeout(opts.callTimeout),
		ui.WithDialer(startup.dialer(logger)),
		ui.WithProfiles(startup.profiles, startup.active),
		ui.WithEnvironments(envs.list, envs.active),
		ui.WithHistory(saved.history),
		ui.WithCollections(saved.collections),
	)
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
		return grpcclient.DialProfile(profile, grpcclient.WithLogger(logger))
	}
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var opts options

	fs := flag.NewFlagSet("grpctui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.configFile, "config", config.DefaultPath(),
		"read settings from this file; empty skips it")
	fs.StringVar(&opts.profile, "profile", "",
		"connection profile to start on; defaults to the first configured")
	fs.StringVar(&opts.env, "env", "",
		"environment to start in; defaults to the first configured")
	fs.Var(&opts.variables, "V",
		"variable as `name=value`, referred to as {{name}} in a request; repeatable")
	fs.BoolVar(&opts.tls, "tls", false,
		"connect over TLS; implied by -cacert, -cert, -key, -servername and -insecure")
	fs.StringVar(&opts.caCert, "cacert", "",
		"verify the server against this PEM bundle instead of the system trust store")
	fs.StringVar(&opts.clientCert, "cert", "",
		"PEM client certificate to present to the server (mutual TLS)")
	fs.StringVar(&opts.clientKey, "key", "",
		"PEM key for -cert")
	fs.StringVar(&opts.serverName, "servername", "",
		"name to check the server's certificate against, if not the address dialled")
	fs.BoolVar(&opts.insecure, "insecure", false,
		"accept any certificate the server offers; the connection is no longer authenticated")
	fs.Var(&opts.headers, "H",
		"request header as `key: value`; repeatable")
	fs.StringVar(&opts.logFile, "log-file", logging.DefaultFile(),
		"write logs to this file; empty disables logging")
	fs.StringVar(&opts.logLevel, "log-level", "error",
		"log level: debug, info, warn, error")
	fs.DurationVar(&opts.callTimeout, "call-timeout", ui.DefaultCallTimeout,
		"give up on a single call after this long")
	fs.StringVar(&opts.historyFile, "history-file", requests.DefaultHistoryFile(),
		"record sent requests in this file; empty keeps history for the session only")
	fs.IntVar(&opts.historyLimit, "history-limit", requests.DefaultLimit,
		"how many sent requests to keep")
	fs.StringVar(&opts.collectionsDir, "collections", requests.DefaultCollectionsDir(),
		"read and write saved request collections in this `directory`; empty disables saving")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "grpctui — a terminal UI for gRPC\n\n")
		_, _ = fmt.Fprintf(stderr, "Usage:\n  grpctui [flags] <host:port>\n\n"+
			"The target may also come from the config file — its `target` key, the\n"+
			"profile named by -profile, or the environment named by -env — in which\n"+
			"case it can be left off the command line.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	opts.usage = fs.Usage

	if err := fs.Parse(args); err != nil {
		return opts, err
	}

	// Visit reports only the flags actually given, which is the difference
	// between "the user chose this config file" and "this is where one would
	// live" — and, for the security flags, between an instruction and a default.
	opts.given = make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		opts.given[f.Name] = true
	})
	opts.configNamed = opts.given["config"]

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
