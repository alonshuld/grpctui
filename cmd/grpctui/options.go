// options.go holds everything a command line turns into: the options struct
// the flags write to, and the three repeatable flag types that collect a list.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/alonshuld/grpctui/internal/vars"
)

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
