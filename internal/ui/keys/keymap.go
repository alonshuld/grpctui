// Package keys holds grpctui's single keymap.
//
// Every keybinding in the application is declared here exactly once. Panels
// receive the [KeyMap] and match against its bindings; none of them compares a
// key string directly. That is what lets the `?` help bar stay truthful for
// free, and what makes config-driven remapping (v0.9) a change to this package
// rather than a hunt through every panel.
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
func Default() KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+u"),
			key.WithHelp("ctrl+u", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+d"),
			key.WithHelp("ctrl+d", "page down"),
		),
		Top: key.NewBinding(
			key.WithKeys("home", "g"),
			key.WithHelp("g", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("G", "bottom"),
		),
		ScrollLeft: key.NewBinding(
			key.WithKeys("shift+left", "H"),
			key.WithHelp("H", "scroll left"),
		),
		ScrollRight: key.NewBinding(
			key.WithKeys("shift+right", "L"),
			key.WithHelp("L", "scroll right"),
		),
		Select: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "select/edit"),
		),
		Expand: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→/l", "expand"),
		),
		Collapse: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←/h", "collapse"),
		),
		Toggle: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle"),
		),
		Add: key.NewBinding(
			key.WithKeys("a", "+"),
			key.WithHelp("a", "add item"),
		),
		Remove: key.NewBinding(
			key.WithKeys("d", "-"),
			key.WithHelp("d", "remove item"),
		),
		Send: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "send"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
		EndStream: key.NewBinding(
			// ctrl+e rather than a plain letter: closing the request stream has
			// to work from inside the form, where a letter is a character being
			// typed into a field.
			key.WithKeys("ctrl+e"),
			key.WithHelp("ctrl+e", "end sending"),
		),
		NextPanel: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next panel"),
		),
		PrevPanel: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "prev panel"),
		),
		Profiles: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "connections"),
		),
		Retry: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "retry"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "quit"),
		),
		ForceQuit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
	}
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
		{k.Select, k.Expand, k.Collapse, k.Toggle, k.ScrollLeft, k.ScrollRight},
		// Add and Remove edit the request the way send and cancel run it, and
		// sharing a column with them keeps the bar at five columns: a sixth pushes
		// it past 100 cells, where the last one is truncated away entirely.
		{k.Add, k.Remove, k.Send, k.EndStream, k.Cancel},
		{k.NextPanel, k.PrevPanel, k.Profiles},
		{k.Retry, k.Help, k.Quit},
	}
}
