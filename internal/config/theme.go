package config

import "fmt"

// Theme is one palette the file defines, added to grpctui's built-in ones.
//
// Its colours are deliberately *not* ${VAR}-expanded, unlike every other value
// in this file. That syntax exists so a secret can reach the config without
// being written in it, and a colour is not a secret; expanding one would only
// add a way for a theme to fail to load on a machine where some variable is
// unset.
type Theme struct {
	// Name identifies the theme in the switcher and to --theme. It is required:
	// an unnamed theme cannot be switched to. Taking a built-in name — "dark",
	// say — replaces that theme rather than adding a second one under the same
	// label, which is how a file adjusts one colour of a built-in without
	// restating the other seven.
	Name string `yaml:"name"`

	// Base names the theme this one starts from, so that a variation need only
	// list what differs. Empty starts from the built-in default.
	Base string `yaml:"base"`

	// Colors maps a colour role — "primary", "border", "error" — to a colour, in
	// any of three forms: `#7D56F4`, an ANSI palette index `0`-`255`, or a
	// `light/dark` pair that follows the terminal's background.
	//
	// The role names live in internal/ui/styles, which is also where a name that
	// is not one is refused. Holding them as a map rather than as eight fields is
	// what keeps this package out of the UI layer: a colour is a string here and
	// becomes a colour exactly once, where colours are understood.
	Colors map[string]string `yaml:"colors"`
}

// validateThemeNames checks that every theme has a name and that no two share
// one, for the same reason profiles and environments are checked: a duplicate
// makes --theme ambiguous, and one of the two would silently never be reachable.
func (c Config) validateThemeNames() error {
	seen := make(map[string]bool, len(c.Themes))
	for i, t := range c.Themes {
		switch {
		case t.Name == "":
			return fmt.Errorf("theme %d has no name", i+1)
		case seen[t.Name]:
			return fmt.Errorf("theme %q is defined twice", t.Name)
		}
		seen[t.Name] = true
	}
	return nil
}
