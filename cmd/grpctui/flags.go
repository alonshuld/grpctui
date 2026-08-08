package main

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/logging"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/runner"
	"github.com/alonshuld/grpctui/internal/ui"
)

// This file holds grpctui's command line: the flags, how they are grouped for
// `--help`, and the two subcommands.
//
// The grouping is the point. Twenty-odd flags printed alphabetically is a list
// nobody reads to the end of; the same flags under six headings is a page
// somebody can find the one they want in. flagGroups is checked by a test
// against the flag set itself, so a flag added and not filed lands in "Other"
// and fails rather than quietly disappearing off the bottom.

// The subcommands. Everything else on the command line is a target address.
const (
	// cmdRun replays a saved collection with no UI at all — the CI smoke test.
	cmdRun = "run"

	// cmdCompletion prints a shell completion script.
	cmdCompletion = "completion"

	// cmdKeys prints the complete keybinding reference — the user's own, with
	// whatever their config file remaps applied, which is why it is a command
	// and not only a page in docs/.
	cmdKeys = "keys"

	// cmdComplete is the hidden helper those scripts call back into for the
	// names only grpctui knows: profiles, environments, themes, collections. It
	// is hidden because it is an implementation detail of the scripts and its
	// output format is not a promise to anybody else.
	cmdComplete = "__complete"
)

// The flag names that more than one place needs to agree on: the help page
// groups them, the completion scripts offer them, and applyFlags asks whether
// each was given. Only those appear here — a name used once is clearer spelled
// where it is used.
const (
	flagConfig      = "config"
	flagProfile     = "profile"
	flagEnv         = "env"
	flagTheme       = "theme"
	flagCACert      = "cacert"
	flagCert        = "cert"
	flagKey         = "key"
	flagServerName  = "servername"
	flagInsecure    = "insecure"
	flagTLS         = "tls"
	flagProto       = "proto"
	flagImportPath  = "import-path"
	flagCollections = "collections"
	flagHistoryFile = "history-file"
	flagLogFile     = "log-file"
)

// registerFlags declares every flag on fs. Both the interactive command and
// `run` take the same ones — a headless replay needs the same connection, the
// same environment and the same schema an interactive session does — so there
// is one declaration of each rather than two that could drift.
func registerFlags(fs *flag.FlagSet, opts *options) {
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
	fs.Var(&opts.protoFiles, "proto",
		"discover from this .proto `file` instead of server reflection; repeatable")
	fs.Var(&opts.importPaths, "import-path",
		"resolve -proto imports against this `directory`, like protoc's -I; repeatable")
	fs.StringVar(&opts.theme, "theme", "",
		"draw in this `theme`: auto, dark, light, or one the config file names")
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
	fs.StringVar(&opts.proxy, "proxy", "",
		"accept gRPC traffic on this `address` and forward it to the target, logging what goes past")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")
}

// registerRunFlags adds the flags only a headless replay has.
//
// -target exists here and not on the interactive command because that one takes
// the address as its argument, and `run` has already spent its argument on the
// collection. Without it the only way to say where a run goes would be the
// config file, which is exactly the wrong place for it in CI.
func registerRunFlags(fs *flag.FlagSet, opts *options) {
	fs.StringVar(&opts.target, "target", "",
		"the `host:port` to call; overrides the profile and the environment")
	fs.StringVar(&opts.format, "format", string(runner.FormatText),
		"report the run as `text` or json")
}

// flagGroup is one heading of the help page and the flags filed under it, in
// the order they are printed. Within a group the order is the order somebody
// meets them, not alphabetical: -profile before the TLS material it usually
// replaces, -proto before the import paths it needs.
type flagGroup struct {
	title string
	flags []string
}

var flagGroups = []flagGroup{
	{"Connecting", []string{flagConfig, "target", flagProfile, flagEnv, "V", "H", "call-timeout"}},
	{"Transport security", []string{flagTLS, flagCACert, flagCert, flagKey, flagServerName, flagInsecure}},
	{"Schema", []string{flagProto, flagImportPath}},
	{"Saved requests", []string{flagCollections, flagHistoryFile, "history-limit"}},
	{"Appearance", []string{flagTheme}},
	{"Diagnostics", []string{"proxy", flagLogFile, "log-level", "version"}},
	{"Reporting", []string{"format"}},
}

// usageText is the prose above the flags: what the three forms of the command
// are, and the two things about the target that are not obvious from them.
const usageText = `grpctui — a terminal UI for exploring, calling and debugging gRPC services

Usage:
  grpctui [flags] <host:port>        explore a target interactively
  grpctui run [flags] <collection>   replay a saved collection, no UI
  grpctui keys                       print the keybinding reference, yours included
  grpctui completion <shell>         print a completion script for bash, zsh or fish

The target may also come from the config file — its ` + "`target`" + ` key, the profile
named by -profile, or the environment named by -env — in which case it can be
left off the command line entirely.

A collection may name one request within it: ` + "`smoke/login`" + ` runs that request
alone, ` + "`smoke`" + ` runs all of them in the order the file lists them. The exit code
is 0 when every request that ran succeeded.
`

// examples close the help page. They are the five invocations that cover what
// people actually do, in the order they meet them.
const examples = `Examples:
  grpctui localhost:50051
  grpctui -tls -H 'authorization: Bearer ${TOKEN}' api.example.com:443
  grpctui -proto api/v1/greeter.proto -import-path api localhost:50051
  grpctui -profile staging -env staging
  grpctui run smoke -target localhost:50051 -format json
  grpctui keys
  grpctui completion zsh > ~/.zsh/completions/_grpctui
`

// printUsage writes the help page.
//
// It renders the flags itself rather than calling [flag.FlagSet.PrintDefaults]
// because that prints one flat alphabetical list, which is the thing this is
// trying not to be. Everything else about a line — the back-quoted argument
// name, the default — follows flag's own conventions, since a Go user already
// knows how to read those.
func printUsage(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprint(out, usageText)

	filed := make(map[string]bool)
	for _, group := range flagGroups {
		var lines []string
		for _, name := range group.flags {
			if f := fs.Lookup(name); f != nil {
				filed[name] = true
				lines = append(lines, flagLine(f))
			}
		}
		if len(lines) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(out, "\n%s\n%s", group.title, strings.Join(lines, ""))
	}

	// Anything not filed still gets printed. A flag missing from the help is a
	// worse bug than one under the wrong heading, and flags_test.go fails on
	// exactly this so it never reaches a release.
	var orphans []string
	fs.VisitAll(func(f *flag.Flag) {
		if !filed[f.Name] {
			orphans = append(orphans, flagLine(f))
		}
	})
	if len(orphans) > 0 {
		_, _ = fmt.Fprintf(out, "\nOther\n%s", strings.Join(orphans, ""))
	}

	_, _ = fmt.Fprintf(out, "\n%s", examples)
}

// flagLine renders one flag: its name, the argument it takes, its description
// and its default.
func flagLine(f *flag.Flag) string {
	arg, usage := flag.UnquoteUsage(f)

	name := "  -" + f.Name
	if arg != "" {
		name += " " + arg
	}

	// The description starts in a fixed column when the name fits, and on the
	// next line when it does not — which beats a ragged right-hand edge that
	// moves every time a flag is renamed.
	const column = 24
	if len(name) < column {
		name += strings.Repeat(" ", column-len(name))
	} else {
		name += "\n" + strings.Repeat(" ", column)
	}

	line := name + usage
	if def := defaultOf(f); def != "" {
		line += " (default " + def + ")"
	}
	return line + "\n"
}

// defaultOf renders a flag's default, or "" for the ones not worth saying.
//
// A false boolean and an empty string are the absence of the flag, and printing
// "(default false)" against every switch is noise. A path default is worth
// showing: knowing where history is written is most of why somebody looks up
// -history-file.
func defaultOf(f *flag.Flag) string {
	switch f.DefValue {
	case "", "false", "0", "[]":
		return ""
	default:
		return f.DefValue
	}
}

// parseArgs parses args, allowing flags to follow the bare words rather than
// only precede them, and returns the bare words in order.
//
// Go's flag package stops at the first non-flag argument, so `grpctui run smoke
// -format json` would otherwise treat `-format` and `json` as two more
// collections. That is the shape everybody types — the subject first, the
// options after — and refusing it is the sort of thing that makes a tool feel
// broken rather than strict.
//
// It works by parsing repeatedly: each pass consumes the flags it can, the bare
// word that stopped it is set aside, and what follows is parsed again.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var bare []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return bare, nil
		}
		bare = append(bare, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// wantsHelp reports whether the command line is asking for the help page.
//
// It is checked before parsing so that help goes to stdout and exits 0. The
// flag package's own -h writes to the flag set's output, which has to be stderr
// for real errors — and help printed to stderr is help that cannot be piped
// into a pager.
func wantsHelp(args []string) bool {
	return slices.ContainsFunc(args, func(arg string) bool {
		return slices.Contains(helpFlags, arg)
	})
}

// helpFlags are the spellings of "show me the help page".
var helpFlags = []string{"-h", "-help", "-" + flagHelp}

// flagHelp is the long form, which the completion scripts offer too.
const flagHelp = "-help"
