// Command grpctui is a terminal UI for exploring, calling, and debugging gRPC
// services.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"go.uber.org/zap"

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

func run(args []string, stdout, stderr io.Writer) int {
	// Help is answered before anything is parsed, so that it reaches stdout and
	// exits 0 — a help page on stderr cannot be piped into a pager, and one that
	// exits 2 breaks `grpctui --help && …`.
	if wantsHelp(args) {
		var opts options
		printUsage(stdout, newFlagSet(commandName(args), &opts, stdout))
		return exitOK
	}

	// The subcommand is looked for anywhere on the line rather than only in
	// args[0], so that the flags may come first: see [splitCommand].
	switch name, rest := splitCommand(args); name {
	case cmdRun:
		return runCollection(rest, stdout, stderr)
	case cmdCompletion:
		return completion(rest, stdout, stderr)
	case cmdKeys:
		return printKeys(rest, stdout, stderr)
	case cmdComplete:
		return completeValues(rest, stdout)
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
		printVersion(stdout)
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
	if name, _ := splitCommand(args); name == cmdRun {
		return "grpctui " + cmdRun
	}
	return "grpctui"
}

// printVersion answers -version, wherever on the command line it was given.
//
// Every form of the command takes the flag, because every form of the command
// registers it — and a `grpctui keys -version` that printed a keybinding table
// would be answering a question nobody asked.
func printVersion(stdout io.Writer) {
	_, _ = fmt.Fprintf(stdout, "grpctui %s\n", version.Version())
}
