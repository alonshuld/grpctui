package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/logging"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/runner"
)

// missingTarget is the one startup failure that is a usage error rather than a
// configuration one, and the caller prints the help page beside it.
const missingTarget = "missing target address"

// runCollection replays a saved collection with no UI, which is what makes a
// collection a CI smoke test rather than only a convenience.
//
// It is the same startup as the interactive command up to the point the
// terminal would be taken over: the same config file, the same profile
// selection, the same environment, the same compiled schema. That is
// deliberate — a green run has to mean the requests the TUI would have sent
// actually work, and a shortcut here would be a shortcut in what CI is
// checking.
func runCollection(args []string, stdout, stderr io.Writer) int {
	var opts options

	fs := newFlagSet("grpctui run", &opts, stderr)

	names, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	opts.record(fs)

	if opts.showVersion {
		printVersion(stdout)
		return exitOK
	}

	if len(names) != 1 {
		fs.Usage()
		_, _ = fmt.Fprintf(stderr, "grpctui: run takes one collection, got %d\n", len(names))
		return exitUsage
	}
	collection, request := splitTarget(names[0])

	format, err := runner.ParseFormat(opts.format)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitUsage
	}

	session, err := prepare(opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	defer session.close()

	entries, err := pick(session.saved.collections, collection, request)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}

	// SIGINT ends the run the way it ends an interactive session: the call in
	// flight is cancelled and reported as the failure it is, rather than the
	// process vanishing mid-request.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := grpcclient.DialProfile(session.startup.profile(),
		dialOptions(session.startup.schema, session.logger)...)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	defer func() { _ = client.Close() }()

	headers, err := headers(session.startup.profile(), session.envs)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}

	report, err := runner.Run(ctx, client, stdout, runner.Options{
		Requests:  entries,
		Variables: session.envs.environment().Set(),
		Metadata:  headers,
		Timeout:   opts.callTimeout,
		Format:    format,
		Logger:    session.logger,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}

	// A skipped request does not fail the run — see [runner.Report.Failed] —
	// so the exit code follows Failed alone.
	if report.Failed > 0 {
		return exitError
	}
	return exitOK
}

// splitTarget reads what to run: a whole collection, or one request inside it.
//
// It is deliberately not [requests.SplitName], which reads the same syntax the
// other way round — there a bare word is a *request* in the default collection,
// because that is what somebody typing into the save prompt means. Here a bare
// word is the collection, because `grpctui run smoke` plainly means "run
// smoke", and quietly looking for a request called smoke inside a collection
// the user never mentioned would be the least helpful reading available.
func splitTarget(input string) (collection, request string) {
	input = strings.TrimSpace(input)
	if before, after, ok := strings.Cut(input, "/"); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	return input, ""
}

// headers are the metadata a headless call carries: the profile's own, with
// every {{name}} in a value expanded against the active environment.
//
// The expansion matters here more than anywhere. A profile whose authorization
// header is "Bearer {{token}}" is exactly how a collection is made portable
// between environments, and a runner that sent the reference verbatim would
// authenticate as nobody.
func headers(profile grpcclient.Profile, envs environs) (grpcclient.Metadata, error) {
	bindings := envs.environment().Set()

	out := make(grpcclient.Metadata, 0, len(profile.Metadata))
	for _, header := range profile.Metadata {
		value, err := bindings.Resolve(header.Value)
		if err != nil {
			return nil, fmt.Errorf("header %q: %w", header.Key, err)
		}
		out = append(out, grpcclient.Header{Key: header.Key, Value: value})
	}
	return out, nil
}

// pick chooses the requests to run: a whole collection, or one request from it.
//
// A collection or request that is not there is an error naming what was, rather
// than an empty run reporting success. "0 of 0 passed" in a CI log is the worst
// possible outcome: it is green, and it checked nothing.
func pick(collections requests.Collections, name, request string) ([]requests.Request, error) {
	var found *requests.Collection
	for _, c := range collections.All() {
		if c.Name == name {
			found = &c
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no collection named %q: have %s", name, quoted(collections.Names()))
	}

	if request == "" {
		if len(found.Requests) == 0 {
			return nil, fmt.Errorf("collection %q is empty", name)
		}
		return found.Requests, nil
	}

	names := make([]string, 0, len(found.Requests))
	for _, r := range found.Requests {
		if r.Name == request {
			return []requests.Request{r}, nil
		}
		names = append(names, r.Name)
	}
	return nil, fmt.Errorf("collection %q has no request %q: have %s", name, request, quoted(names))
}

func quoted(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strconv.Quote(n))
	}
	return strings.Join(out, ", ")
}

// session is everything both commands need before they diverge: the config
// file read, the profiles and environments resolved, the schema compiled, the
// saved requests loaded and the logger open.
type session struct {
	startup startup
	envs    environs
	saved   saved
	look    appearance
	logger  *zap.Logger

	closeLog func() error
}

func (s session) close() {
	if s.closeLog != nil {
		_ = s.closeLog()
	}
}

// prepare does the startup both commands share.
//
// The order matters and is the order of dependency: the config file decides the
// environments, the environments decide the target, the target completes the
// profile, and the schema is compiled last because it is the slowest and there
// is no point compiling it for a run that has already failed.
func prepare(opts options) (session, error) {
	cfg, err := loadConfig(opts)
	if err != nil {
		return session{}, err
	}

	var s session
	if s.envs, err = environments(cfg, opts); err != nil {
		return session{}, err
	}
	if s.startup, err = connections(cfg, opts, s.envs.target()); err != nil {
		return session{}, err
	}
	if s.startup.profile().Target == "" {
		return session{}, errors.New(missingTarget)
	}
	if s.saved, err = loadRequests(opts); err != nil {
		return session{}, err
	}
	if s.look, err = look(cfg, opts); err != nil {
		return session{}, err
	}

	logger, closeLog, err := logging.New(logging.Config{File: opts.logFile, Level: opts.logLevel})
	if err != nil {
		return session{}, err
	}
	s.logger, s.closeLog = logger, closeLog

	if s.startup.schema, err = compileSchema(context.Background(), cfg, opts); err != nil {
		s.close()
		return session{}, err
	}
	return s, nil
}
