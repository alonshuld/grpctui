package config

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/alonshuld/grpctui/internal/vars"
)

// Environment is one named set of variables, and where they point.
//
// It is a separate axis from [Profile] on purpose. A profile says *how* to
// connect — transport security, credentials, the headers that go with them — and
// an environment says *what the request means*: which account id, which tenant,
// which host. Switching to staging is usually both, which is why an environment
// may name a target of its own; keeping them one thing would mean a second copy
// of every profile per environment.
type Environment struct {
	// Name identifies the environment in the switcher and to --env. It is
	// required: an unnamed environment cannot be switched to.
	Name string `yaml:"name"`

	// Target is the address to dial while this environment is active. It is
	// optional — an environment that only carries values leaves the connection
	// where it is.
	Target string `yaml:"target"`

	// Variables are the bindings {{name}} references resolve against. Like
	// everywhere else in this file, a value may be written as ${VAR} and comes
	// from the process environment.
	Variables map[string]string `yaml:"variables"`
}

// Environments converts the file's environments into the sets the UI switches
// between, in the order they were written.
//
// problems runs parallel to them, exactly as it does for [Config.Connections]
// and for the same reason: one unset environment variable in the production
// environment should stop you *using* production, not stop grpctui starting.
func (c Config) Environments() (envs []vars.Environment, problems []error) {
	for _, e := range c.Envs {
		env, err := e.Environment()
		if err != nil {
			err = fmt.Errorf("environment %q: %w", e.Name, err)
		}
		envs = append(envs, env)
		problems = append(problems, err)
	}
	return envs, problems
}

// Environment converts one environment into its runtime form, expanding ${VAR}
// references as it goes.
//
// Every value is expanded before anything is reported, so a file missing three
// variables says so once. The environment comes back either way, with whatever
// could not be expanded left as it was written: an environment nobody can use
// is still one the switcher has to be able to name.
func (e Environment) Environment() (vars.Environment, error) {
	var errs []error

	out := vars.Environment{Name: e.Name}

	target, err := expand(e.Target)
	if err != nil {
		errs = append(errs, err)
	}
	out.Target = target

	// Sorted, so that a file and a session agree on the order — and so that a
	// golden file of the variables panel is decided by the file rather than by
	// Go's map iteration.
	names := make([]string, 0, len(e.Variables))
	for name := range e.Variables {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		if err := vars.ValidateName(name); err != nil {
			errs = append(errs, err)
			continue
		}

		value, err := expand(e.Variables[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("variable %q: %w", name, err))
		}
		out.Variables = append(out.Variables, vars.Variable{Name: name, Value: value})
	}

	return out, errors.Join(errs...)
}

// SelectEnvironment finds the environment to start in by name, or the first one
// when no name was given. It reports -1 when there are none, which is not an
// error: environments are optional, and a run without them resolves nothing and
// notices nothing.
//
// A name that matches nothing is an error listing what there was, for the same
// reason [Select] refuses one: starting in whatever environment came first is
// how a request meant for staging carries production's account id.
func SelectEnvironment(envs []vars.Environment, name string) (int, error) {
	if len(envs) == 0 {
		if name != "" {
			return -1, fmt.Errorf("no environment named %q: none are configured", name)
		}
		return -1, nil
	}
	if name == "" {
		return 0, nil
	}

	for i, e := range envs {
		if e.Name == name {
			return i, nil
		}
	}

	names := make([]string, 0, len(envs))
	for _, e := range envs {
		names = append(names, strconv.Quote(e.Name))
	}
	return -1, fmt.Errorf("no environment named %q: have %s", name, strings.Join(names, ", "))
}

// validateEnvironmentNames checks that every environment has a name and that no
// two share one, for the same reason profiles are checked: a duplicate makes
// --env ambiguous, and finding that out on the third connection of the day is
// worse than finding it out at startup.
func (c Config) validateEnvironmentNames() error {
	seen := make(map[string]bool, len(c.Envs))
	for i, e := range c.Envs {
		switch {
		case e.Name == "":
			return fmt.Errorf("environment %d has no name", i+1)
		case seen[e.Name]:
			return fmt.Errorf("environment %q is defined twice", e.Name)
		}
		seen[e.Name] = true
	}
	return nil
}
