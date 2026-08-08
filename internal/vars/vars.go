// Package vars holds grpctui's variables and the environments that supply
// them.
//
// A variable is a name bound to a piece of text. Anywhere a request is written
// — a form field, a header value — that text can be referred to as
// {{name}} instead of being typed out, and the reference is expanded on the way
// to the wire and nowhere else. That is what makes a saved request portable:
// the same collection entry calls dev and prod, and what differs between them
// is which [Environment] is switched on.
//
// # Two syntaxes, deliberately
//
// internal/config already expands ${VAR} from the process environment, and this
// is not that. ${VAR} is resolved once, at load, and is how a secret reaches a
// config file without being written in it; {{name}} is resolved at send, from a
// set the user can switch and add to while grpctui is running. Sharing one
// syntax would mean a config file that could not say which of the two it meant.
//
// # What is never written down
//
// A [Set] is runtime state. It is never persisted, and neither is a value
// captured out of a response — which is very often a token, since a login call
// followed by a call that carries its answer is the whole point of chaining
// requests. A saved request records the *reference* rather than its expansion;
// see internal/requests.
package vars

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Variable is one name bound to a piece of text.
type Variable struct {
	Name  string
	Value string

	// Captured records that the value came out of a response rather than out of
	// the config file. It is display only — the two behave identically — but a
	// user looking at a list wants to know which of them will survive switching
	// environment and which will not.
	Captured bool
}

// Environment is a named variable set and, optionally, the address it points
// at.
//
// The target is what makes an environment more than a bag of strings: "staging"
// usually means both a different host and a different account id, and having to
// switch those separately is how a request meant for staging reaches
// production.
type Environment struct {
	// Name identifies the environment in the switcher and to --env. It is
	// required: an unnamed environment cannot be switched to.
	Name string

	// Target is the address to dial while this environment is active. An empty
	// target leaves the connection where it is, which is what an environment
	// that only carries values wants.
	Target string

	// Variables are the environment's own bindings, ordered by name.
	Variables []Variable
}

// Label names the environment for a list.
func (e Environment) Label() string { return e.Name }

// Set returns the environment's variables as the set that resolves them.
func (e Environment) Set() Set { return NewSet(e.Variables) }

// Set is the variables in force: an environment's own, plus whatever has been
// captured or typed since it was switched on.
//
// It is a value, copied freely — the UI's root model is a bubbletea value model
// and holds one directly — so every mutation returns a new Set rather than
// writing through a copy somebody else is still holding. The zero Set resolves
// nothing and is perfectly usable: a run with no environments configured has
// one, and a request with no references in it does not notice.
type Set struct {
	// list is kept sorted by name, so that a list of variables and a golden file
	// of one read the same on every run.
	list []Variable
}

// NewSet builds a set from its variables, keeping the last of any duplicates —
// the same rule a later assignment follows everywhere else.
func NewSet(vs []Variable) Set {
	var s Set
	for _, v := range vs {
		s = s.With(v)
	}
	return s
}

// All returns the variables, ordered by name. The slice is the set's own and
// must not be modified.
func (s Set) All() []Variable { return s.list }

// Len reports how many variables are bound.
func (s Set) Len() int { return len(s.list) }

// Names lists the bound names, ordered.
func (s Set) Names() []string {
	names := make([]string, 0, len(s.list))
	for _, v := range s.list {
		names = append(names, v.Name)
	}
	return names
}

// Lookup returns the value bound to name.
func (s Set) Lookup(name string) (string, bool) {
	if i := s.indexOf(name); i >= 0 {
		return s.list[i].Value, true
	}
	return "", false
}

// With returns a copy of the set with v bound, replacing any existing binding
// of the same name.
func (s Set) With(v Variable) Set {
	v.Name = strings.TrimSpace(v.Name)

	if i := s.indexOf(v.Name); i >= 0 {
		list := slices.Clone(s.list)
		list[i] = v
		return Set{list: list}
	}

	list := make([]Variable, 0, len(s.list)+1)
	list = append(list, s.list...)
	list = append(list, v)
	slices.SortStableFunc(list, func(a, b Variable) int { return strings.Compare(a.Name, b.Name) })
	return Set{list: list}
}

// Without returns a copy of the set with name unbound.
func (s Set) Without(name string) Set {
	i := s.indexOf(strings.TrimSpace(name))
	if i < 0 {
		return s
	}

	list := make([]Variable, 0, len(s.list)-1)
	list = append(list, s.list[:i]...)
	list = append(list, s.list[i+1:]...)
	return Set{list: list}
}

func (s Set) indexOf(name string) int {
	return slices.IndexFunc(s.list, func(v Variable) bool { return v.Name == name })
}

// Refers reports whether text contains a {{name}} reference at all.
//
// It is what lets a half-typed request be left alone: "{{count}}" in an int64
// field is not a number yet, and complaining that it is not one — while it is
// being typed, or in a form recalled under an environment that does not bind it
// — would be complaining about the wrong thing.
func (s Set) Refers(text string) bool { return Refers(text) }

// Refers reports whether text contains a {{name}} reference.
func Refers(text string) bool { return refPattern.MatchString(text) }

// References lists the names text refers to, in the order they appear, without
// repeats.
func References(text string) []string {
	var names []string
	for _, match := range refPattern.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(names, match[1]) {
			names = append(names, match[1])
		}
	}
	return names
}

// Resolve replaces every {{name}} in text with the value bound to it.
//
// A reference to a name nothing binds is left as written and reported, exactly
// as internal/config treats a missing ${VAR}: what comes back is something to
// show, never something to send. A request that quietly loses a field's value
// fails somewhere far from the mistake, which is the failure this refuses to
// produce.
func (s Set) Resolve(text string) (string, error) {
	if !Refers(text) {
		return text, nil
	}

	var missing []string
	out := refPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := refPattern.FindStringSubmatch(match)[1]
		value, ok := s.Lookup(name)
		if !ok {
			if !slices.Contains(missing, name) {
				missing = append(missing, name)
			}
			return match
		}
		return value
	})

	if len(missing) > 0 {
		return out, &UnsetError{Names: missing}
	}
	return out, nil
}

// UnsetError reports references nothing bound. It names every one of them
// rather than the first: a form filled in from another environment can refer to
// three variables that are not here, and reporting them one send at a time is
// three sends.
type UnsetError struct {
	Names []string
}

func (e *UnsetError) Error() string {
	if len(e.Names) == 1 {
		return "{{" + e.Names[0] + "}} is not set in this environment"
	}
	return "{{" + strings.Join(e.Names, "}}, {{") + "}} are not set in this environment"
}

// ValidateName checks a variable name.
//
// The rule is the reference syntax's own: a name that {{...}} cannot spell is a
// variable nothing can refer to, so binding one is a quiet way to lose a value
// rather than a harmless oddity.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("a variable needs a name")
	case !namePattern.MatchString(name):
		return fmt.Errorf("%q is not a variable name: use letters, digits and _, starting with a letter or _", name)
	}
	return nil
}

// SplitAssignment reads the "name=value" form the -V flag and the variables
// panel accept. The value is taken verbatim after the first "=", so a value may
// contain one; only the name is trimmed, since trailing space in a token is the
// kind of thing that costs an afternoon.
func SplitAssignment(input string) (name, value string, ok bool) {
	name, value, ok = strings.Cut(input, "=")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(name), value, true
}

// refPattern matches a {{name}} reference, with or without space inside the
// braces.
//
// Only the doubled-brace form is a reference. A lone "{" is left alone, so that
// a request body carrying JSON, a Go template or a regex survives being typed
// into a field — which matters more here than matching any particular
// templating language exactly.
var refPattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// namePattern is what a variable may be called: the same names a reference can
// spell.
var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
