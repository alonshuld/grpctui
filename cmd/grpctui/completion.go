package main

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// The shells a completion script can be printed for.
const (
	shellBash = "bash"
	shellZsh  = "zsh"
	shellFish = "fish"
)

// shells lists them in the order the error message names them.
var shells = []string{shellBash, shellZsh, shellFish}

// completion prints a completion script.
//
// The scripts complete flag names statically — they are baked in below from the
// same flag set the help page is built from — and call back into `grpctui
// __complete` for the things only grpctui knows: which profiles, environments,
// themes and collections this user's config file defines. That callback is why
// completing -profile offers "staging" rather than nothing, and it is the only
// part that has to read anything.
func completion(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintf(stderr, "grpctui: completion takes one shell: %s\n", strings.Join(shells, ", "))
		return exitUsage
	}

	var script string
	switch args[0] {
	case shellBash:
		script = bashScript()
	case shellZsh:
		script = zshScript()
	case shellFish:
		script = fishScript()
	default:
		_, _ = fmt.Fprintf(stderr, "grpctui: no completion for %q: have %s\n", args[0], strings.Join(shells, ", "))
		return exitUsage
	}

	if _, err := io.WriteString(stdout, script); err != nil {
		_, _ = fmt.Fprintf(stderr, "grpctui: %v\n", err)
		return exitError
	}
	return exitOK
}

// completeValues is the hidden callback the scripts use, printing one candidate
// per line.
//
// A failure prints nothing and still exits 0. This runs inside the user's shell
// every time they press tab: an unreadable config file is worth an error when
// they start grpctui, and worth silence when they are halfway through typing a
// command.
func completeValues(args []string, stdout io.Writer) int {
	if len(args) != 1 {
		return exitOK
	}

	for _, value := range candidates(args[0]) {
		_, _ = fmt.Fprintln(stdout, value)
	}
	return exitOK
}

// The kinds of value a completion script can ask for. The first five are
// answered from the binary itself; the rest need the user's config file.
const (
	kindShells       = "shells"
	kindCommands     = "commands"
	kindRenderers    = "renderers"
	kindActions      = "actions"
	kindColors       = "colors"
	kindProfiles     = "profiles"
	kindEnvironments = "environments"
	kindThemes       = "themes"
	kindCollections  = "collections"
)

// candidates lists the completions for one kind of value.
func candidates(what string) []string {
	switch what {
	case kindShells:
		return shells
	case kindCommands:
		return []string{cmdRun, cmdKeys, cmdCompletion}
	case kindRenderers:
		return render.BuiltinNames()
	case kindActions:
		return keys.Names()
	case kindColors:
		return styles.RoleNames()
	default:
		return configured(what)
	}
}

// configured answers the kinds that come out of the config file.
//
// It reads the file at its default path. A completion that honoured -config
// would mean parsing the half-typed command line to find it, and the payoff —
// completing profile names for a config file the user is in the middle of
// naming — is not worth that.
func configured(what string) []string {
	var opts options
	registerFlags(flag.NewFlagSet("grpctui", flag.ContinueOnError), &opts)

	cfg, err := loadConfig(opts)
	if err != nil {
		return nil
	}

	switch what {
	case kindProfiles:
		profiles, _ := cfg.Connections()
		names := make([]string, 0, len(profiles))
		for _, p := range profiles {
			names = append(names, p.Label())
		}
		return names

	case kindEnvironments:
		envs, _ := cfg.Environments()
		names := make([]string, 0, len(envs))
		for _, e := range envs {
			names = append(names, e.Name)
		}
		return names

	case kindThemes:
		themes, err := styles.Catalog(specs(cfg.Themes))
		if err != nil {
			// A theme that will not parse is still a theme the user meant to
			// have; falling back to the built-ins keeps tab working while they
			// fix it.
			themes = styles.BuiltinThemes()
		}
		names := make([]string, 0, len(themes))
		for _, t := range themes {
			names = append(names, t.Name)
		}
		return names

	case kindCollections:
		saved, err := loadRequests(opts)
		if err != nil {
			return nil
		}
		return saved.collections.Names()

	default:
		return nil
	}
}

// valueCompletions says which flags take a value grpctui can complete, and
// which kind. A flag that is not here completes as a file path or not at all,
// which is what the shells do by default.
var valueCompletions = map[string]string{
	flagProfile: kindProfiles,
	flagEnv:     kindEnvironments,
	flagTheme:   kindThemes,
}

// pathFlags take a filename, and dirFlags a directory. Telling the shell which
// is which is most of what makes completion feel native: offering every file in
// the tree where only a directory is legal is worse than offering nothing.
var pathFlags = []string{
	flagConfig, flagCACert, flagCert, flagKey, flagProto, flagHistoryFile, flagLogFile,
}

var dirFlags = []string{flagImportPath, flagCollections}

// flagNames lists every flag, sorted, as the scripts spell them.
func flagNames() []string {
	var opts options
	fs := flag.NewFlagSet("grpctui", flag.ContinueOnError)
	registerFlags(fs, &opts)
	registerRunFlags(fs, &opts)

	var names []string
	fs.VisitAll(func(f *flag.Flag) { names = append(names, "-"+f.Name) })
	slices.Sort(names)
	return names
}

func bashScript() string {
	return fmt.Sprintf(`# grpctui completion for bash. Source it, or drop it in
# /etc/bash_completion.d — or, for one shell:
#
#     source <(grpctui completion bash)

_grpctui() {
    local cur prev
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    case "${prev#-}" in
        %s)
            COMPREPLY=($(compgen -W "$(grpctui __complete "$(_grpctui_kind "${prev#-}")" 2>/dev/null)" -- "$cur"))
            return ;;
        %s)
            COMPREPLY=($(compgen -f -- "$cur")); return ;;
        %s)
            COMPREPLY=($(compgen -d -- "$cur")); return ;;
    esac

    if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "%s" -- "$cur"))
        return
    fi

    # The first bare word is either a subcommand or the target address, which
    # nothing can complete: an address is not a name grpctui knows.
    if [[ "$COMP_CWORD" -eq 1 ]]; then
        COMPREPLY=($(compgen -W "$(grpctui __complete commands 2>/dev/null)" -- "$cur"))
        return
    fi
    if [[ "${COMP_WORDS[1]}" == "%s" ]]; then
        COMPREPLY=($(compgen -W "$(grpctui __complete collections 2>/dev/null)" -- "$cur"))
    elif [[ "${COMP_WORDS[1]}" == "%s" ]]; then
        COMPREPLY=($(compgen -W "$(grpctui __complete shells 2>/dev/null)" -- "$cur"))
    fi
}

_grpctui_kind() {
    case "$1" in
%s
    esac
}

complete -F _grpctui grpctui
`,
		strings.Join(namesOf(valueCompletions), "|"),
		strings.Join(pathFlags, "|"),
		strings.Join(dirFlags, "|"),
		strings.Join(flagNames(), " "),
		cmdRun,
		cmdCompletion,
		bashKindCases(),
	)
}

// namesOf lists the flags that complete as a name, sorted.
func namesOf(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// bashKindCases maps a flag to the kind of name it takes, as a bash case body.
func bashKindCases() string {
	var b strings.Builder
	for _, name := range namesOf(valueCompletions) {
		fmt.Fprintf(&b, "        %s) echo %s ;;\n", name, valueCompletions[name])
	}
	return strings.TrimRight(b.String(), "\n")
}

func zshScript() string {
	var args strings.Builder
	for _, name := range namesOf(valueCompletions) {
		fmt.Fprintf(&args, "        '-%s[]:%s:_grpctui_values %s' \\\n",
			name, name, valueCompletions[name])
	}
	for _, name := range pathFlags {
		fmt.Fprintf(&args, "        '-%s[]:file:_files' \\\n", name)
	}
	for _, name := range dirFlags {
		fmt.Fprintf(&args, "        '-%s[]:directory:_files -/' \\\n", name)
	}

	var plain []string
	for _, name := range flagNames() {
		bare := strings.TrimPrefix(name, "-")
		if slices.Contains(namesOf(valueCompletions), bare) ||
			slices.Contains(pathFlags, bare) || slices.Contains(dirFlags, bare) {
			continue
		}
		plain = append(plain, "'"+name+"[]'")
	}

	return fmt.Sprintf(`#compdef grpctui
# grpctui completion for zsh. Put it on your $fpath as _grpctui:
#
#     grpctui completion zsh > ~/.zsh/completions/_grpctui

_grpctui_values() {
    local -a values
    values=(${(f)"$(grpctui __complete $1 2>/dev/null)"})
    compadd -a values
}

_grpctui() {
    local -a subcommands
    subcommands=(
        '%s:replay a saved collection with no UI'
        '%s:print the keybinding reference'
        '%s:print a completion script'
    )

    if (( CURRENT == 2 )) && [[ $words[2] != -* ]]; then
        _describe 'command' subcommands
        return
    fi

    case $words[2] in
        %s) if (( CURRENT == 3 )); then _grpctui_values collections; return; fi ;;
        %s) if (( CURRENT == 3 )); then _grpctui_values shells; return; fi ;;
    esac

    _arguments -s \
%s        %s
}

_grpctui "$@"
`,
		cmdRun,
		cmdKeys,
		cmdCompletion,
		cmdRun,
		cmdCompletion,
		args.String(),
		strings.Join(plain, " \\\n        "),
	)
}

func fishScript() string {
	var b strings.Builder
	fmt.Fprintf(&b, `# grpctui completion for fish:
#
#     grpctui completion fish > ~/.config/fish/completions/grpctui.fish

complete -c grpctui -f
complete -c grpctui -n __fish_use_subcommand -a '(grpctui __complete commands)'
complete -c grpctui -n '__fish_seen_subcommand_from %s' -a '(grpctui __complete collections)'
complete -c grpctui -n '__fish_seen_subcommand_from %s' -a '(grpctui __complete shells)'
`, cmdRun, cmdCompletion)

	for _, name := range namesOf(valueCompletions) {
		fmt.Fprintf(&b, "complete -c grpctui -o %s -x -a '(grpctui __complete %s)'\n",
			name, valueCompletions[name])
	}
	for _, name := range pathFlags {
		fmt.Fprintf(&b, "complete -c grpctui -o %s -r -F\n", name)
	}
	for _, name := range dirFlags {
		fmt.Fprintf(&b, "complete -c grpctui -o %s -x -a '(__fish_complete_directories)'\n", name)
	}
	for _, name := range flagNames() {
		bare := strings.TrimPrefix(name, "-")
		if slices.Contains(namesOf(valueCompletions), bare) ||
			slices.Contains(pathFlags, bare) || slices.Contains(dirFlags, bare) {
			continue
		}
		fmt.Fprintf(&b, "complete -c grpctui -o %s\n", bare)
	}
	return b.String()
}
