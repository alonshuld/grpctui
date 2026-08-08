package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlagGroups_CoverEveryFlag is the guard the help page's grouping needs. A
// flag added and not filed still prints — under "Other" — and this fails, which
// is how it gets a heading before anyone sees it.
func TestFlagGroups_CoverEveryFlag(t *testing.T) {
	var opts options
	fs := flag.NewFlagSet("grpctui", flag.ContinueOnError)
	registerFlags(fs, &opts)
	registerRunFlags(fs, &opts)

	filed := make(map[string]bool)
	for _, group := range flagGroups {
		for _, name := range group.flags {
			require.False(t, filed[name], "%q is in two groups", name)
			require.NotNil(t, fs.Lookup(name), "group %q files %q, which is not a flag", group.title, name)
			filed[name] = true
		}
	}

	fs.VisitAll(func(f *flag.Flag) {
		assert.True(t, filed[f.Name], "-%s is in no group; add it to flagGroups", f.Name)
	})

	var out bytes.Buffer
	printUsage(&out, fs)
	assert.NotContains(t, out.String(), "\nOther\n")
}

// TestPrintUsage pins that the help page names every flag and every subcommand.
// A flag missing from it is a flag nobody finds.
func TestPrintUsage(t *testing.T) {
	var opts options
	fs := flag.NewFlagSet("grpctui", flag.ContinueOnError)
	registerFlags(fs, &opts)

	var out bytes.Buffer
	printUsage(&out, fs)
	page := out.String()

	fs.VisitAll(func(f *flag.Flag) {
		assert.Contains(t, page, "  -"+f.Name, "-%s is not on the help page", f.Name)
	})

	for _, want := range []string{
		"Usage:", cmdRun, cmdKeys, cmdCompletion, "Examples:",
	} {
		assert.Contains(t, page, want)
	}

	// The hidden completion helper stays hidden: it is an implementation detail
	// of the generated scripts.
	assert.NotContains(t, page, cmdComplete)

	for _, group := range flagGroups {
		if group.title == "Reporting" {
			// Only `grpctui run` has that flag, so its heading is absent here.
			assert.NotContains(t, page, "\n"+group.title+"\n")
			continue
		}
		assert.Contains(t, page, "\n"+group.title+"\n")
	}
}

// TestPrintUsage_RunHasFormat pins that `grpctui run --help` documents the flag
// only it has.
func TestPrintUsage_RunHasFormat(t *testing.T) {
	var opts options
	fs := newFlagSet("grpctui run", &opts, &bytes.Buffer{})

	var out bytes.Buffer
	printUsage(&out, fs)

	assert.Contains(t, out.String(), "-format")
	assert.Contains(t, out.String(), "\nReporting\n")
}

func TestFlagLine(t *testing.T) {
	var opts options
	fs := flag.NewFlagSet("grpctui", flag.ContinueOnError)
	registerFlags(fs, &opts)

	tests := map[string]struct {
		flag string
		want []string
	}{
		"a flag taking a named argument": {
			flag: "proto",
			want: []string{"-proto file", "server reflection"},
		},
		// A false boolean is the absence of the flag, and "(default false)"
		// against every switch is noise.
		"a boolean has no default printed": {
			flag: "tls",
			want: []string{"-tls"},
		},
		"a path default is worth showing": {
			flag: "log-level",
			want: []string{"-log-level string", "(default error)"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			line := flagLine(fs.Lookup(tt.flag))
			for _, want := range tt.want {
				assert.Contains(t, line, want)
			}
			assert.True(t, strings.HasSuffix(line, "\n"))
		})
	}

	assert.NotContains(t, flagLine(fs.Lookup("tls")), "default")
}

func TestWantsHelp(t *testing.T) {
	for _, args := range [][]string{
		{"-h"},
		{"-help"},
		{"--help"},
		{"run", "smoke", "--help"},
		{"localhost:50051", "-h"},
	} {
		assert.True(t, wantsHelp(args), "%q should ask for help", args)
	}

	for _, args := range [][]string{
		nil, {"localhost:50051"}, {"-tls"}, {"run", "smoke"},
	} {
		assert.False(t, wantsHelp(args), "%q should not ask for help", args)
	}
}

func TestCommandName(t *testing.T) {
	assert.Equal(t, "grpctui", commandName(nil))
	assert.Equal(t, "grpctui", commandName([]string{"localhost:50051"}))
	assert.Equal(t, "grpctui run", commandName([]string{"run", "smoke"}))
	assert.Equal(t, "grpctui run", commandName([]string{"-config", "ci.yaml", "run", "smoke"}),
		"the help page for `run` is the one wanted whether or not the flags came first")
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCmd  string
		wantRest []string
	}{
		{
			name: "nothing at all",
		},
		{
			name: "a target address is not a command",
			args: []string{"localhost:50051"},
		},
		{
			name:     "the command first",
			args:     []string{"run", "smoke"},
			wantCmd:  "run",
			wantRest: []string{"smoke"},
		},
		{
			// The whole point: this used to dial a host named "keys".
			name:     "a flag before the command",
			args:     []string{"-config", "ci.yaml", "keys"},
			wantCmd:  "keys",
			wantRest: []string{"-config", "ci.yaml"},
		},
		{
			name:     "a flag joined to its value",
			args:     []string{"-config=ci.yaml", "keys"},
			wantCmd:  "keys",
			wantRest: []string{"-config=ci.yaml"},
		},
		{
			name:     "a boolean flag does not swallow the command",
			args:     []string{"-tls", "run", "smoke"},
			wantCmd:  "run",
			wantRest: []string{"-tls", "smoke"},
		},
		{
			name:     "flags on both sides of the command",
			args:     []string{"-target", "localhost:50051", "run", "smoke", "-format", "json"},
			wantCmd:  "run",
			wantRest: []string{"-target", "localhost:50051", "smoke", "-format", "json"},
		},
		{
			// A profile may be called anything, including "run".
			name: "a flag's value is not a command",
			args: []string{"-profile", "run"},
		},
		{
			name: "the hidden callback",
			args: []string{"__complete", "profiles"},

			wantCmd:  "__complete",
			wantRest: []string{"profiles"},
		},
		{
			name: "a command after the target is the target's business",
			args: []string{"localhost:50051", "keys"},
		},
		{
			name: "nothing after -- is a command",
			args: []string{"--", "keys"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, rest := splitCommand(tt.args)

			assert.Equal(t, tt.wantCmd, cmd)
			if tt.wantCmd == "" {
				assert.Equal(t, tt.args, rest, "an unrecognised line is handed back untouched")
				return
			}
			assert.Equal(t, tt.wantRest, rest)
		})
	}
}
