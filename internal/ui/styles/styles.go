// Package styles holds grpctui's lipgloss styles.
//
// Like internal/ui/keys it is a leaf package, so the root model and the panels
// share one definition of how things look. Themes (v0.9) become alternative
// constructors here.
package styles

import "github.com/charmbracelet/lipgloss"

// Palette is the set of colours a theme provides.
type Palette struct {
	Primary   lipgloss.TerminalColor
	Secondary lipgloss.TerminalColor
	Muted     lipgloss.TerminalColor
	Border    lipgloss.TerminalColor
	Error     lipgloss.TerminalColor
	Success   lipgloss.TerminalColor
	Text      lipgloss.TerminalColor
	Inverted  lipgloss.TerminalColor
}

// DefaultPalette adapts to light and dark terminals.
func DefaultPalette() Palette {
	return Palette{
		Primary:   lipgloss.AdaptiveColor{Light: "#7D56F4", Dark: "#B79BFF"},
		Secondary: lipgloss.AdaptiveColor{Light: "#0F7B6C", Dark: "#4FD6BE"},
		Muted:     lipgloss.AdaptiveColor{Light: "#6C6C6C", Dark: "#8A8A8A"},
		Border:    lipgloss.AdaptiveColor{Light: "#C0C0C0", Dark: "#4A4A4A"},
		Error:     lipgloss.AdaptiveColor{Light: "#B00020", Dark: "#FF6B6B"},
		Success:   lipgloss.AdaptiveColor{Light: "#1B7F3B", Dark: "#6FD48A"},
		Text:      lipgloss.AdaptiveColor{Light: "#1A1A1A", Dark: "#E4E4E4"},
		Inverted:  lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1A1A1A"},
	}
}

// Styles is the full set of styles used by the UI.
type Styles struct {
	Palette Palette

	Panel        lipgloss.Style
	PanelFocused lipgloss.Style
	PanelTitle   lipgloss.Style

	Service         lipgloss.Style
	Method          lipgloss.Style
	Cursor          lipgloss.Style
	CursorUnfocused lipgloss.Style
	Marker          lipgloss.Style
	StreamTag       lipgloss.Style

	Label  lipgloss.Style
	Value  lipgloss.Style
	Muted  lipgloss.Style
	Status lipgloss.Style

	FieldName     lipgloss.Style
	FieldType     lipgloss.Style
	FieldValue    lipgloss.Style
	FieldDisabled lipgloss.Style
	FieldError    lipgloss.Style

	StatusOK    lipgloss.Style
	StatusError lipgloss.Style

	// The two directions a stream message travels. They differ in colour, and
	// the log puts an arrow beside them so the distinction survives a
	// monochrome terminal.
	StreamSent     lipgloss.Style
	StreamReceived lipgloss.Style

	// The response body's syntax highlighting. Keys carry the structure of a
	// message, so they get the accent colour; the values beside them are told
	// apart by type rather than fought for attention.
	JSONKey     lipgloss.Style
	JSONString  lipgloss.Style
	JSONNumber  lipgloss.Style
	JSONLiteral lipgloss.Style
	JSONPunct   lipgloss.Style

	ErrorTitle lipgloss.Style
	ErrorBody  lipgloss.Style
	Hint       lipgloss.Style
}

// New builds the default styles.
func New() Styles {
	p := DefaultPalette()

	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(p.Border).
		Padding(0, 1)

	// The focused panel changes border *shape*, not just colour: focus has to
	// be legible on a monochrome terminal too.
	focused := panel.
		Border(lipgloss.ThickBorder()).
		BorderForeground(p.Primary)

	return Styles{
		Palette:      p,
		Panel:        panel,
		PanelFocused: focused,
		PanelTitle:   lipgloss.NewStyle().Bold(true).Foreground(p.Primary),

		Service:         lipgloss.NewStyle().Bold(true).Foreground(p.Text),
		Method:          lipgloss.NewStyle().Foreground(p.Text),
		Cursor:          lipgloss.NewStyle().Bold(true).Foreground(p.Inverted).Background(p.Primary),
		CursorUnfocused: lipgloss.NewStyle().Foreground(p.Primary),
		Marker:          lipgloss.NewStyle().Foreground(p.Muted),
		StreamTag:       lipgloss.NewStyle().Foreground(p.Secondary),

		Label:  lipgloss.NewStyle().Foreground(p.Muted),
		Value:  lipgloss.NewStyle().Foreground(p.Text),
		Muted:  lipgloss.NewStyle().Foreground(p.Muted),
		Status: lipgloss.NewStyle().Foreground(p.Muted).Padding(0, 1),

		FieldName:     lipgloss.NewStyle().Foreground(p.Text),
		FieldType:     lipgloss.NewStyle().Foreground(p.Muted),
		FieldValue:    lipgloss.NewStyle().Foreground(p.Secondary),
		FieldDisabled: lipgloss.NewStyle().Foreground(p.Muted).Italic(true),
		FieldError:    lipgloss.NewStyle().Foreground(p.Error),

		StatusOK:    lipgloss.NewStyle().Bold(true).Foreground(p.Success),
		StatusError: lipgloss.NewStyle().Bold(true).Foreground(p.Error),

		StreamSent:     lipgloss.NewStyle().Bold(true).Foreground(p.Primary),
		StreamReceived: lipgloss.NewStyle().Bold(true).Foreground(p.Secondary),

		JSONKey:     lipgloss.NewStyle().Foreground(p.Primary),
		JSONString:  lipgloss.NewStyle().Foreground(p.Secondary),
		JSONNumber:  lipgloss.NewStyle().Foreground(p.Text),
		JSONLiteral: lipgloss.NewStyle().Bold(true).Foreground(p.Text),
		JSONPunct:   lipgloss.NewStyle().Foreground(p.Muted),

		ErrorTitle: lipgloss.NewStyle().Bold(true).Foreground(p.Error),
		ErrorBody:  lipgloss.NewStyle().Foreground(p.Text),
		Hint:       lipgloss.NewStyle().Foreground(p.Muted).Italic(true),
	}
}
