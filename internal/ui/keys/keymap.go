// Package keys holds grpctui's single keymap.
//
// Every keybinding in the application is declared here exactly once. Panels
// receive the [KeyMap] and match against its bindings; none of them compares a
// key string directly. That is what lets the `?` help bar stay truthful for
// free, and what made config-driven remapping a change to this package alone
// rather than a hunt through every panel — see remap.go, which addresses these
// bindings by name.
//
// It is a leaf package so that both internal/ui and internal/ui/panels can
// import it without a cycle.
package keys

import "github.com/charmbracelet/bubbles/key"

// KeyMap is the complete set of grpctui keybindings.
type KeyMap struct {
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Top      key.Binding
	Bottom   key.Binding

	// ScrollLeft and ScrollRight pan a panel whose content is wider than it is —
	// a JSON response holding a URL or a stack trace, in practice. Without them
	// the viewport truncates such a line and there is no keystroke that reveals
	// the rest. They are deliberately not h/l, which mean expand/collapse
	// everywhere else in grpctui.
	ScrollLeft  key.Binding
	ScrollRight key.Binding

	Select   key.Binding
	Expand   key.Binding
	Collapse key.Binding
	Toggle   key.Binding

	// Add and Remove grow and shrink a repeated field in the request form. They
	// are plain letters rather than modified keys because filling in a list of a
	// dozen items is a keystroke-per-item job.
	Add    key.Binding
	Remove key.Binding

	Send   key.Binding
	Cancel key.Binding

	// EndStream closes the sending half of an open stream, telling the server
	// no more request messages are coming. It is separate from Cancel because
	// the two are opposites: this one finishes the call politely and waits for
	// the answer, Cancel throws it away.
	EndStream key.Binding

	NextPanel key.Binding
	PrevPanel key.Binding

	// Profiles opens the connection switcher. It is a modal chooser rather than
	// a panel, so it has a key of its own rather than a place in the tab cycle:
	// switching connection is a thing you do occasionally, not something to
	// step past on the way to the response.
	Profiles key.Binding

	// HistoryPrev and HistoryNext step through the requests already sent,
	// filling the form in from each. `[` goes back in time and `]` forward,
	// which is the direction they point.
	HistoryPrev key.Binding
	HistoryNext key.Binding

	// Requests opens the saved-request browser: history and collections in one
	// searchable list. It is ctrl+r rather than a letter so that it works from
	// inside a field being edited, where recalling a previous request is exactly
	// what you want instead of typing the whole thing again — and because that
	// is the shell's key for the same idea.
	Requests key.Binding

	// Save puts the request currently in the form into a collection. It is a
	// capital S so that it cannot be confused with ctrl+s, which sends: the two
	// keys are one shift apart in the fingers and a world apart in effect.
	Save key.Binding

	// Environments opens the environment switcher and Variables the list of
	// what the active one binds. They sit beside Profiles because they are the
	// same sort of thing — a modal choice about the session rather than about
	// the request — and for the same reason neither is in the tab cycle.
	Environments key.Binding
	Variables    key.Binding

	// Capture takes a value out of the last response and binds it to a variable,
	// which is how one call's answer becomes the next call's argument. It is
	// ctrl+p rather than a letter because reaching for it while a field is being
	// typed into is the ordinary case: you read the id in the response, and the
	// cursor is already in the field that wants it.
	Capture key.Binding

	// Filter starts typing a query in the request browser. `/` is what every
	// pager, editor and TUI in the neighbourhood uses.
	Filter key.Binding

	// RawView swaps the response panel between the decoded body and the
	// protobuf bytes it came from, and Diff between the body and what changed
	// since the last call to the same method. They are two renderings of one
	// answer rather than two panels, so they are toggles rather than places to
	// tab to.
	RawView key.Binding
	Diff    key.Binding

	// Export renders the request in the form as a grpcurl command, for sharing
	// outside grpctui. It is a capital X for the reason Save is a capital S: the
	// lowercase letters in this range are already scroll and expand keys.
	Export key.Binding

	// Themes opens the theme switcher. It is a capital T for the reason Save is
	// a capital S: the lowercase letters around it are already taken, and t in
	// particular is the traffic log.
	Themes key.Binding

	// Traffic opens the log of calls the passive proxy has seen. Like the other
	// modals it has a key of its own rather than a place in the tab cycle —
	// and unlike them it does nothing at all unless grpctui was started with
	// --proxy.
	Traffic key.Binding

	Retry key.Binding
	Help  key.Binding
	Quit  key.Binding

	// ForceQuit is the one binding that works everywhere, including while a
	// text field is being edited — where q is a character, not a command. It is
	// deliberately absent from the help views: Quit already documents "quit",
	// and ctrl+c needs no teaching.
	ForceQuit key.Binding
}

// Default returns the built-in keybindings.
//
// They are declared in three groups — moving about, editing and sending a
// request, and the session-wide modals — purely so that the table stays
// readable. There is still exactly one definition of every binding.
func Default() KeyMap {
	var km KeyMap
	km.navigation()
	km.request()
	km.session()
	return km
}

// navigation binds the keys that move a cursor or a view.
func (k *KeyMap) navigation() {
	k.Up = key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)
	k.Down = key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)
	k.PageUp = key.NewBinding(
		key.WithKeys("pgup", "ctrl+u"),
		key.WithHelp("ctrl+u", "page up"),
	)
	k.PageDown = key.NewBinding(
		key.WithKeys("pgdown", "ctrl+d"),
		key.WithHelp("ctrl+d", "page down"),
	)
	k.Top = key.NewBinding(
		key.WithKeys("home", "g"),
		key.WithHelp("g", "top"),
	)
	k.Bottom = key.NewBinding(
		key.WithKeys("end", "G"),
		key.WithHelp("G", "bottom"),
	)
	k.ScrollLeft = key.NewBinding(
		key.WithKeys("shift+left", "H"),
		key.WithHelp("H", "scroll left"),
	)
	k.ScrollRight = key.NewBinding(
		key.WithKeys("shift+right", "L"),
		key.WithHelp("L", "scroll right"),
	)
	k.NextPanel = key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "next panel"),
	)
	k.PrevPanel = key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "prev panel"),
	)
}

// request binds the keys that fill a request in and put it on the wire.
func (k *KeyMap) request() {
	k.Select = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select/edit"),
	)
	k.Expand = key.NewBinding(
		key.WithKeys("right", "l"),
		key.WithHelp("→/l", "expand"),
	)
	k.Collapse = key.NewBinding(
		key.WithKeys("left", "h"),
		key.WithHelp("←/h", "collapse"),
	)
	k.Toggle = key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "toggle"),
	)
	k.Add = key.NewBinding(
		key.WithKeys("a", "+"),
		key.WithHelp("a", "add item"),
	)
	k.Remove = key.NewBinding(
		key.WithKeys("d", "-"),
		key.WithHelp("d", "remove item"),
	)
	k.Send = key.NewBinding(
		key.WithKeys("ctrl+s"),
		key.WithHelp("ctrl+s", "send"),
	)
	k.Cancel = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	)
	k.EndStream = key.NewBinding(
		// ctrl+e rather than a plain letter: closing the request stream has to
		// work from inside the form, where a letter is a character being typed
		// into a field.
		key.WithKeys("ctrl+e"),
		key.WithHelp("ctrl+e", "end sending"),
	)
	k.HistoryPrev = key.NewBinding(
		key.WithKeys("["),
		key.WithHelp("[", "older request"),
	)
	k.HistoryNext = key.NewBinding(
		key.WithKeys("]"),
		key.WithHelp("]", "newer request"),
	)
}

// session binds the keys that open a modal or end the program: the things that
// are about the session rather than about the request in front of you.
func (k *KeyMap) session() {
	k.Profiles = key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "connections"),
	)
	k.Environments = key.NewBinding(
		key.WithKeys("e"),
		key.WithHelp("e", "environments"),
	)
	k.Variables = key.NewBinding(
		key.WithKeys("v"),
		key.WithHelp("v", "variables"),
	)
	k.Capture = key.NewBinding(
		key.WithKeys("ctrl+p"),
		key.WithHelp("ctrl+p", "capture"),
	)
	k.Requests = key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r", "requests"),
	)
	k.Save = key.NewBinding(
		key.WithKeys("S"),
		key.WithHelp("S", "save"),
	)
	k.Filter = key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter"),
	)
	k.RawView = key.NewBinding(
		key.WithKeys("w"),
		key.WithHelp("w", "raw bytes"),
	)
	k.Diff = key.NewBinding(
		key.WithKeys("D"),
		key.WithHelp("D", "diff"),
	)
	k.Export = key.NewBinding(
		key.WithKeys("X"),
		key.WithHelp("X", "grpcurl"),
	)
	k.Themes = key.NewBinding(
		key.WithKeys("T"),
		key.WithHelp("T", "theme"),
	)
	k.Traffic = key.NewBinding(
		key.WithKeys("t"),
		key.WithHelp("t", "traffic"),
	)
	k.Retry = key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "retry"),
	)
	k.Help = key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	)
	k.Quit = key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	)
	k.ForceQuit = key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	)
}

// ShortHelp implements help.KeyMap: the single-line help bar.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Select, k.Send, k.NextPanel, k.Help, k.Quit}
}

// FullHelp implements help.KeyMap: the expanded help view, one column per
// group.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		// Horizontal scrolling shares a column with expand/collapse rather than
		// taking one of its own: a sixth column pushes the bar past 100 cells,
		// where it gets truncated and the last one disappears entirely.
		// The response panel's two alternative renderings sit with the other keys
		// that change what a panel shows rather than what it holds.
		{k.Select, k.Expand, k.Collapse, k.Toggle, k.ScrollLeft, k.ScrollRight, k.RawView, k.Diff},
		// Add and Remove edit the request the way send and cancel run it, and
		// sharing a column with them keeps the bar at five columns: a sixth pushes
		// it past 100 cells, where the last one is truncated away entirely.
		// Recalling and keeping a request sit with sending one: they are the same
		// subject, and a sixth column would push the bar past 100 cells, where
		// the last of them is truncated away entirely. Capturing a value out of a
		// response is the same subject again, and — like everything else here —
		// costs a row rather than a column, since a column is only ever as wide
		// as its widest entry.
		{k.Add, k.Remove, k.Send, k.EndStream, k.Cancel, k.Requests, k.Save, k.Capture, k.Export},
		{k.NextPanel, k.PrevPanel, k.Profiles, k.HistoryPrev, k.HistoryNext, k.Environments, k.Variables, k.Traffic, k.Themes},
		{k.Retry, k.Help, k.Quit},
	}
}
