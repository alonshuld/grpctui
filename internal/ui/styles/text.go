package styles

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Clamp trims a rendered frame to at most width columns and height rows,
// dropping the lines that do not fit and cutting whatever runs past the right
// edge of the ones that do.
//
// It exists because lipgloss cannot do it in one style: MaxHeight(0) is read as
// "no maximum", and rendering a multi-line block through a style pads every
// line out to the width of the widest one, which fills a frame with trailing
// whitespace. Lines already inside the width are returned untouched for the
// same reason.
func Clamp(s string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = lipgloss.NewStyle().MaxWidth(width).Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

// Truncate cuts a rendered string to width, accounting for ANSI sequences, and
// marks the cut with an ellipsis. A width of zero or less leaves s alone: a
// panel that has not been sized yet must not render as a row of ellipses.
func Truncate(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(s) + "…"
}

// Wrap hard-wraps text to width on whitespace.
//
// It lives here, next to [Truncate], because both the root model and the panels
// need it and a second copy is a fix applied to one of them.
func Wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}
