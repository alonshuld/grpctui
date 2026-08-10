// layout.go turns a terminal size into panel sizes. The numbers are measured
// once per resize and handed down, never asked for per row.

package ui

import (
	"github.com/charmbracelet/lipgloss"
)

// innerSize reports the content area left inside a panel of the given outer
// size, after its border, its padding, and the one-line title.
func innerSize(s lipgloss.Style, width, height int) (w, h int) {
	w = width - s.GetHorizontalBorderSize() - s.GetHorizontalPadding()
	h = height - s.GetVerticalBorderSize() - s.GetVerticalPadding() - 1
	return max(w, 0), max(h, 0)
}

// layoutSizes is the geometry of one frame: a service tree down the left, and
// down the right the request form, the headers, and the response.
type layoutSizes struct {
	treeW     int
	rightW    int
	bodyH     int
	requestH  int
	metadataH int
	responseH int
}

// layout recomputes panel sizes; called whenever anything that affects the
// available space changes — including the form growing a row, which steals
// height from the response.
func (m *Model) layout() {
	l := m.computeLayout()

	w, h := innerSize(m.styles.Panel, l.treeW, l.bodyH)
	m.tree.SetSize(w, h)

	w, h = innerSize(m.styles.Panel, l.rightW, l.requestH)
	m.request.SetSize(w, h)

	w, h = innerSize(m.styles.Panel, l.rightW, l.metadataH)
	m.metadata.SetSize(w, h)

	w, h = innerSize(m.styles.Panel, l.rightW, l.responseH)
	m.response.SetSize(w, h)

	m.profiles.SetSize(m.width, m.height)
	m.browser.SetSize(m.width, m.height)
	m.environments.SetSize(m.width, m.height)
	m.variables.SetSize(m.width, m.height)
	m.traffic.SetSize(m.width, m.height)
	m.export.SetSize(m.width, m.height)
	m.themes.SetSize(m.width, m.height)
}

func (m Model) computeLayout() layoutSizes {
	const (
		minTreeWidth   = 24
		maxTreeWidth   = 48
		minPanelHeight = 3
	)

	l := layoutSizes{}
	l.treeW = min(max(m.width*2/5, minTreeWidth), maxTreeWidth)
	l.treeW = min(l.treeW, m.width)
	l.rightW = m.width - l.treeW

	// The status bar takes one row; the help bar takes however many it needs.
	l.bodyH = max(m.height-1-m.helpHeight(), minPanelHeight)

	// Each of the top two panels gets the height it asks for and the response
	// takes the rest: a three-field request with one header should not reserve
	// half the screen between them.
	chrome := m.styles.Panel.GetVerticalBorderSize() + m.styles.Panel.GetVerticalPadding() + 1

	metadataWant := 0
	if m.metadata.Visible() {
		metadataWant = m.metadata.ContentHeight() + chrome
	}

	l.requestH, l.metadataH, l.responseH = splitColumn(l.bodyH,
		m.request.ContentHeight()+chrome, metadataWant, minPanelHeight)
	return l
}

// helpHeight reports how many rows the help bar occupies, from the cached
// measurement. It falls back to measuring so that a Model nobody has sized —
// one built straight from [New] in a test — still lays out correctly.
func (m Model) helpHeight() int {
	if m.helpH > 0 {
		return m.helpH
	}
	return lipgloss.Height(m.help.View(m.keys))
}

// measureHelp re-measures the help bar. Only two things change its height: the
// terminal width, and `?`.
func (m *Model) measureHelp() { m.helpH = lipgloss.Height(m.help.View(m.keys)) }

// splitHeight divides a column in two, giving the top panel the height it wants
// within what is left after the bottom one's minimum. A column too short to
// satisfy both minimums is halved instead, because a panel of zero rows renders
// as a broken box rather than as nothing.
func splitHeight(total, want, minEach int) (top, bottom int) {
	if total < 2*minEach {
		top = total / 2
		return top, total - top
	}
	top = min(max(want, minEach), total-minEach)
	return top, total - top
}

// splitColumn divides the right-hand column three ways: the form and the
// headers each get what they ask for, and the response takes the rest.
//
// The headers are settled first and against the *whole* column, so that a form
// tall enough to fill the screen cannot squeeze them out — a header list is
// small, bounded, and the thing you are most likely to be changing when a call
// keeps coming back Unauthenticated.
func splitColumn(total, wantTop, wantMiddle, minEach int) (top, middle, bottom int) {
	// A middle panel that wants nothing is not on screen, and the column is the
	// two-panel one it has always been.
	if wantMiddle <= 0 {
		top, bottom = splitHeight(total, wantTop, minEach)
		return top, 0, bottom
	}

	if total < 3*minEach {
		// Too short for three panels at their minimum. Thirds keep all three
		// visible, which beats one of them rendering as a broken box.
		top = total / 3
		middle = total / 3
		return top, middle, total - top - middle
	}

	middle = min(max(wantMiddle, minEach), total-2*minEach)
	top, bottom = splitHeight(total-middle, wantTop, minEach)
	return top, middle, bottom
}
