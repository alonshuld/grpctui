package keys

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

// This file turns [KeyMap] into a reference somebody can read outside the
// program: `grpctui keys` prints it, and docs/keybindings.md is generated from
// it.
//
// It exists because the `?` bar cannot be the complete reference and should not
// try to be. That bar has a hard width budget — past about 100 cells
// bubbles/help truncates a column away — so it shows the keys worth learning
// first, in five columns, and stops. A page has no such budget, and v1.0
// promised a complete one.
//
// The grouping is the same idea as cmd/grpctui's flagGroups: a table of field
// names, checked by a test against the struct itself, so a binding added and
// not filed fails a test rather than quietly missing from the reference.

// Action is one keybinding as a reference page shows it: what a config file
// calls it, what it is bound to now, and what it does.
type Action struct {
	// Name is the config-file action name — "history-prev" — which is what
	// makes the reference double as the list of what can be remapped.
	Name string

	// Keys are the keys currently bound, in bubbletea's spelling. It is empty
	// for an action the user has unbound.
	Keys []string

	// Desc is the binding's own description, the same text the help bar shows.
	// It is written for a bar with a hundred cells to spend on five columns, so
	// it is a label — "diff", "capture" — rather than a sentence.
	Desc string

	// Detail is the sentence the reference page shows instead.
	//
	// It is separate from Desc because the two are written to different budgets
	// and a reference that reads "diff  diff" teaches nobody anything. Every
	// action has one, and reference_test.go fails if a new binding does not.
	Detail string
}

// Bound reports whether the action has any key at all. An unbound one is still
// listed: knowing a key was deliberately taken away is worth a line.
func (a Action) Bound() bool { return len(a.Keys) > 0 }

// String renders the keys as a config file would spell them, comma-separated,
// or "unbound".
func (a Action) String() string {
	if !a.Bound() {
		return "unbound"
	}

	spelled := make([]string, 0, len(a.Keys))
	for _, k := range a.Keys {
		spelled = append(spelled, spell(k))
	}
	return strings.Join(spelled, ", ")
}

// spell renders a key the way a reader can act on it. bubbletea calls the space
// bar " ", which on a printed line looks like a missing binding.
func spell(k string) string {
	if strings.TrimSpace(k) == "" {
		return "space"
	}
	return k
}

// Group is one heading of the reference and the actions filed under it.
type Group struct {
	Title   string
	Actions []Action
}

// referenceGroups is every binding, filed under the heading it belongs to, in
// the order a reader meets them rather than alphabetically.
//
// Fields are named as they are spelled in [KeyMap]. reference_test.go checks
// this list against the struct in both directions: a field missing from here,
// and a name here that is no longer a field.
var referenceGroups = []struct {
	title  string
	fields []string
}{
	{"Moving about", []string{
		"Up", "Down", "PageUp", "PageDown", "Top", "Bottom",
		"ScrollLeft", "ScrollRight", "NextPanel", "PrevPanel",
	}},
	{"Filling in a request", []string{
		"Select", "Expand", "Collapse", "Toggle", "Add", "Remove",
	}},
	{"Sending", []string{
		"Send", "EndStream", "Cancel", "Retry",
	}},
	{"Reading a response", []string{
		"RawView", "Diff", "Capture", "Export",
	}},
	{"Saved requests", []string{
		"HistoryPrev", "HistoryNext", "Requests", "Save", "Filter",
	}},
	{"The session", []string{
		"Profiles", "Environments", "Variables", "Traffic", "Themes",
		"Help", "Quit", "ForceQuit",
	}},
}

// details is the sentence the reference page prints for each action, keyed by
// the config-file action name.
//
// They are here rather than on the bindings themselves because a binding's own
// description has to fit the `?` bar, where five columns share a hundred cells.
// A page has room to say what a key is for; the bar has room to remind you.
var details = map[string]string{
	"up":           "move the cursor up a row",
	"down":         "move the cursor down a row",
	"page-up":      "scroll up a screenful",
	"page-down":    "scroll down a screenful",
	"top":          "jump to the first row",
	"bottom":       "jump to the last row",
	"scroll-left":  "pan left, for a line wider than the panel",
	"scroll-right": "pan right, for a line wider than the panel",
	"next-panel":   "move focus to the next panel",
	"prev-panel":   "move focus to the previous panel",

	"select":   "choose a method, or start editing a field",
	"expand":   "open a nested message or a list",
	"collapse": "close it again",
	"toggle":   "flip a bool, or pick the oneof variant under the cursor",
	"add":      "append an item to a repeated field or a map",
	"remove":   "delete the item under the cursor",

	"send":       "put the request on the wire",
	"end-stream": "close the sending half and wait for the answer",
	"cancel":     "abandon the call, or leave the field being edited",
	"retry":      "send the last request again",

	"raw-view": "swap the response between decoded JSON and its bytes",
	"diff":     "compare this response with the last from the same method",
	"capture":  "bind a value out of the response to a variable",
	"export":   "render the request as a grpcurl command",

	"history-prev": "step back through the requests already sent",
	"history-next": "step forward again",
	"requests":     "browse history and collections, searchable",
	"save":         "put the request in the form into a collection",
	"filter":       "type a query in the request browser",

	"profiles":     "switch connection profile",
	"environments": "switch environment, changing what {{name}} means",
	"variables":    "list what the environment binds, and edit it",
	"traffic":      "show what the passive proxy has seen (needs --proxy)",
	"themes":       "switch palette, previewed as you move",
	"help":         "expand the help bar",
	"quit":         "leave grpctui",
	"force-quit":   "leave, even from inside a field being edited",
}

// Reference returns every binding in the keymap, grouped for a reference page.
//
// It reports the keymap it is called on rather than the built-in one, so
// `grpctui keys` run against a config file that remaps `send` prints the key
// that config file actually gives you. A reference that told you about
// somebody else's keyboard would be worse than none.
func (k KeyMap) Reference() []Group {
	value := reflect.ValueOf(k)
	byName := fieldsByName()

	groups := make([]Group, 0, len(referenceGroups))
	for _, g := range referenceGroups {
		actions := make([]Action, 0, len(g.fields))
		for _, field := range g.fields {
			f, ok := byName[field]
			if !ok {
				continue
			}
			binding, ok := value.FieldByIndex(f.Index).Interface().(key.Binding)
			if !ok {
				continue
			}
			name := ActionName(field)
			actions = append(actions, Action{
				Name:   name,
				Keys:   slices.Clone(binding.Keys()),
				Desc:   binding.Help().Desc,
				Detail: details[name],
			})
		}
		groups = append(groups, Group{Title: g.title, Actions: actions})
	}
	return groups
}

// Reference renders the reference as plain text, which is what `grpctui keys`
// prints.
//
// It is written here rather than in the command because the command is not the
// only reader: docs/keybindings.md is generated from the same function, and two
// renderings of one table would drift.
func Reference(k KeyMap) string {
	groups := k.Reference()

	// One pair of column widths for the whole page rather than one per group, so
	// the columns line up down the page instead of stepping in and out.
	var keyWidth, nameWidth int
	for _, g := range groups {
		for _, a := range g.Actions {
			keyWidth = max(keyWidth, len(a.String()))
			nameWidth = max(nameWidth, len(a.Name))
		}
	}

	var b strings.Builder
	for i, g := range groups {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s\n", g.Title)
		for _, a := range g.Actions {
			fmt.Fprintf(&b, "  %-*s  %-*s  %s\n", keyWidth, a.String(), nameWidth, a.Name, a.Detail)
		}
	}
	return b.String()
}

// fieldsByName indexes [KeyMap]'s bindings by their Go field name, which is how
// referenceGroups spells them.
func fieldsByName() map[string]reflect.StructField {
	fields := reflect.VisibleFields(reflect.TypeFor[KeyMap]())
	out := make(map[string]reflect.StructField, len(fields))
	for _, f := range fields {
		if f.Type == reflect.TypeFor[key.Binding]() {
			out[f.Name] = f
		}
	}
	return out
}
