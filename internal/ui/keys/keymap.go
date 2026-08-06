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

	Select   key.Binding
	Expand   key.Binding
	Collapse key.Binding
	Toggle   key.Binding

	Send   key.Binding
	Cancel key.Binding

	NextPanel key.Binding
	PrevPanel key.Binding

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
		Send: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "send"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
		NextPanel: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next panel"),
		),
		PrevPanel: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "prev panel"),
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
		{k.Select, k.Expand, k.Collapse, k.Toggle},
		{k.Send, k.Cancel},
		{k.NextPanel, k.PrevPanel},
		{k.Retry, k.Help, k.Quit},
	}
}
