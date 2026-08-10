// environments.go resolves the {{name}} environments a session starts with —
// the ones in the config file, and the one the command line spells out.

package main

import (
	"fmt"
	"slices"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/vars"
)

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
