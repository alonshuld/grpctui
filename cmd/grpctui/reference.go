package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/alonshuld/grpctui/internal/ui/keys"
)

// printKeys answers `grpctui keys`: the complete keybinding reference, on
// stdout, exiting 0.
//
// It is a command rather than only a page in docs/ because the reference that
// matters is the one for *this* keyboard. A config file may rebind anything,
// and a printed table naming keys the user has replaced would be worse than no
// table at all — so this loads their config and prints what it actually gives
// them.
//
// It takes the connection flags like everything else, of which only -config
// changes the answer. Accepting the rest costs nothing and means `grpctui
// -config ./ci.yaml keys` works the way somebody would expect after typing the
// same prefix all day.
func printKeys(args []string, stdout, stderr io.Writer) int {
	var opts options
	fs := newFlagSet("grpctui "+cmdKeys, &opts, stderr)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	opts.record(fs)

	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "grpctui: keys takes no arguments, got %q\n", fs.Arg(0))
		return exitUsage
	}

	cfg, err := loadConfig(opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}

	km, err := keys.Default().Apply(cfg.Keys)
	if err != nil {
		// A keymap the config file cannot produce is exactly what somebody runs
		// this command to find out about, so it is reported rather than fallen
		// back from: printing the built-in keys under a config that does not
		// load would answer a question nobody asked.
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}

	_, _ = fmt.Fprint(stdout, keysHeader)
	_, _ = fmt.Fprint(stdout, keys.Reference(km))
	_, _ = fmt.Fprint(stdout, keysFooter)
	return exitOK
}

// keysHeader and keysFooter frame the table: what the columns are, and what to
// do with the middle one.
const keysHeader = `grpctui keybindings — the keys, what the config file calls them, what they do

`

const keysFooter = `
The middle column is the action's name in the config file. Remap one by naming
it under ` + "`keys:`" + ` in ~/.config/grpctui/config.yaml, as a comma-separated list
of keys — an empty value unbinds it:

  keys:
    send: ctrl+enter
    requests: ctrl+r,f3
    traffic: ""

Conflicts are refused at startup, so a remap that would cost you a key you still
need fails loudly rather than quietly.
`
