package keys

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
)

// This file makes [KeyMap] addressable by name, which is what a config file
// needs to remap a binding.
//
// The names are derived from the struct's fields rather than listed in a table
// beside it: a table would be a second place to remember, and the failure it
// invites — adding a binding and forgetting to make it remappable — is silent.
// The cost is that renaming a field renames a config key, which is a real
// compatibility surface; keymap_test.go pins the whole list so that such a
// rename fails a test rather than a user's config file.

// Names lists every remappable action, sorted, for an error message that says
// what was allowed rather than only what was wrong.
func Names() []string {
	fields := reflect.VisibleFields(reflect.TypeFor[KeyMap]())
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		if f.Type == reflect.TypeFor[key.Binding]() {
			names = append(names, ActionName(f.Name))
		}
	}
	slices.Sort(names)
	return names
}

// ActionName converts a [KeyMap] field name into the name a config file uses:
// HistoryPrev becomes "history-prev".
func ActionName(field string) string {
	var b strings.Builder
	for i, r := range field {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('-')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Apply returns k with the named actions rebound.
//
// Each value is a comma-separated list of keys in bubbletea's spelling —
// "ctrl+s", "shift+tab", "enter", "esc", or a bare character. An empty value
// unbinds the action entirely, which is the only way to get a key back that
// grpctui has claimed and the user wants for something else.
//
// Every override is checked before any is reported, so a file with three
// mistakes in it says so once rather than over three runs. Nothing is applied
// unless everything can be: a half-remapped keyboard is worse than an
// unremapped one, because the half that worked hides the half that did not.
func (k KeyMap) Apply(overrides map[string]string) (KeyMap, error) {
	if len(overrides) == 0 {
		return k, nil
	}

	// Sorted, so that a file with two bad names in it reports them the same way
	// twice: Go's map iteration order is deliberately not stable.
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	slices.Sort(names)

	out := k
	value := reflect.ValueOf(&out).Elem()
	byName := fieldsByActionName()

	var errs []error
	remapped := make(map[string]bool, len(overrides))
	for _, name := range names {
		field, ok := byName[name]
		if !ok {
			errs = append(errs, fmt.Errorf("keys: %q is not an action: have %s",
				name, strings.Join(Names(), ", ")))
			continue
		}

		current, ok := value.FieldByIndex(field.Index).Interface().(key.Binding)
		if !ok {
			continue
		}

		binding, err := rebind(current, overrides[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("keys: %s: %w", name, err))
			continue
		}
		value.FieldByIndex(field.Index).Set(reflect.ValueOf(binding))
		remapped[name] = true
	}

	if err := errors.Join(errs...); err != nil {
		return k, err
	}
	if err := out.conflicts(remapped); err != nil {
		return k, err
	}
	return out, nil
}

// fieldsByActionName indexes [KeyMap]'s bindings by the name a config file uses.
func fieldsByActionName() map[string]reflect.StructField {
	fields := reflect.VisibleFields(reflect.TypeFor[KeyMap]())
	out := make(map[string]reflect.StructField, len(fields))
	for _, f := range fields {
		if f.Type == reflect.TypeFor[key.Binding]() {
			out[ActionName(f.Name)] = f
		}
	}
	return out
}

// rebind replaces a binding's keys, keeping the description it already had.
//
// The help *key* is replaced along with the keys themselves, so the `?` bar
// documents what the user actually has to press. Keeping the description is the
// other half of that: "send" is still what the action does, whichever key does
// it.
func rebind(from key.Binding, spec string) (key.Binding, error) {
	desc := from.Help().Desc

	fields := strings.Split(spec, ",")
	pressed := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			pressed = append(pressed, f)
		}
	}

	if len(pressed) == 0 {
		if strings.TrimSpace(spec) != "" {
			return key.Binding{}, fmt.Errorf("%q names no key", spec)
		}
		// Deliberately unbound: a binding with no keys matches nothing, and
		// disabling it keeps it out of the help bar as well.
		unbound := key.NewBinding(key.WithHelp("", desc))
		unbound.SetEnabled(false)
		return unbound, nil
	}

	return key.NewBinding(
		key.WithKeys(pressed...),
		key.WithHelp(strings.Join(pressed, "/"), desc),
	), nil
}

// conflicts reports two actions sharing a key, when at least one of them was
// remapped.
//
// The "at least one" is what keeps this honest. grpctui's own defaults overlap
// in places where the two actions can never both be live — the same letter
// means one thing in a modal and another in a panel — and refusing those would
// be refusing the built-in keymap. What is worth catching is the case the user
// created: binding send to q, and losing quit without being told.
func (k KeyMap) conflicts(remapped map[string]bool) error {
	holders := make(map[string][]string)

	fields := reflect.VisibleFields(reflect.TypeFor[KeyMap]())
	value := reflect.ValueOf(k)
	for _, f := range fields {
		if f.Type != reflect.TypeFor[key.Binding]() {
			continue
		}
		binding, ok := value.FieldByIndex(f.Index).Interface().(key.Binding)
		if !ok {
			continue
		}

		name := ActionName(f.Name)
		for _, pressed := range binding.Keys() {
			holders[pressed] = append(holders[pressed], name)
		}
	}

	pressedKeys := make([]string, 0, len(holders))
	for pressed := range holders {
		pressedKeys = append(pressedKeys, pressed)
	}
	slices.Sort(pressedKeys)

	var errs []error
	for _, pressed := range pressedKeys {
		names := holders[pressed]
		if len(names) < 2 || !slices.ContainsFunc(names, func(n string) bool { return remapped[n] }) {
			continue
		}
		slices.Sort(names)
		errs = append(errs, fmt.Errorf("keys: %q is bound to %s at once", pressed, strings.Join(names, " and ")))
	}
	return errors.Join(errs...)
}
