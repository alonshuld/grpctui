// requests_view.go renders the browser. The frame's measurements are taken
// once per View and handed down: asking each row for a column width made a
// frame quadratic in the history, which is what layout exists to prevent.

package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// columns is the width of the two fixed columns of the list.
type columns struct {
	label  int
	source int
}

// measure sizes them against every entry rather than the visible ones, so that
// filtering narrows the list without moving the walls around it.
func measure(entries []entry) columns {
	c := columns{label: 1, source: len(historySource)}
	for _, e := range entries {
		c.label = max(c.label, len(e.request.Label()))
		c.source = max(c.source, len(e.source))
	}
	c.label = min(c.label, maxNameWidth)
	c.source = min(c.source, maxTypeWidth)
	return c
}

const (
	filterPrompt = "filter: "
	savePrompt   = "collection/name  "

	listHint = "enter load · ctrl+s load and send · / filter · esc close"
	saveHint = "enter save · esc cancel"

	// minBoxWidth keeps the modal from collapsing around a single short entry.
	minBoxWidth = 44
)

// View renders the browser as a box the root model centres on the screen.
//
// The box is given an explicit width rather than shrinking to its contents:
// otherwise every character typed into the filter resizes it as the list
// narrows, and a modal that jumps about under the cursor is unreadable.
func (r Requests) View() string {
	// The geometry is measured once and handed down, rather than recomputed by
	// every line that needs it. Each of the three widths is a pass over every
	// entry, and a browser holding a full history redrew itself in tens of
	// milliseconds when each of a screenful of rows asked for them again — which
	// is a modal that visibly lags behind the key you pressed.
	l := r.layout()

	// lipgloss counts padding inside Width, so the box is asked for the content
	// width plus it — otherwise the widest row is a cell or two too long and
	// wraps onto a line of its own.
	box := r.styles.Panel.Width(l.inner + r.styles.Panel.GetHorizontalPadding())

	if r.mode == requestsSave {
		return box.Render(strings.Join(r.saveLines(l), "\n"))
	}

	lines := []string{r.styles.PanelTitle.Render(r.title()), r.filterLine(l), ""}

	switch {
	case len(r.entries) == 0:
		lines = append(lines, r.styles.Muted.Render("Nothing sent or saved yet."))
	case len(r.visible) == 0:
		lines = append(lines, r.styles.Muted.Render("No request matches this filter."))
	default:
		visible := min(r.visibleRows(), len(r.visible)-r.offset)
		for i := r.offset; i < r.offset+visible; i++ {
			lines = append(lines, r.row(i, l))
		}
	}

	lines = append(lines, "", r.styles.Hint.Render(listHint))
	return box.Render(strings.Join(lines, "\n"))
}

// layout is the browser's geometry for one frame: the box's content width and
// the width of the two fixed columns inside it.
//
// It is a value computed once per View rather than three methods called
// wherever a width is wanted. That is not tidiness: each width is a pass over
// every entry, and a width asked for inside a loop over the entries is a
// quadratic frame.
type layout struct {
	inner  int
	label  int
	source int
}

// layout measures the browser against everything it holds.
//
// The two fixed columns were measured when the entries changed; what is left to
// do here is the description column, which is the one thing about a row that
// moves on its own — "3m ago" becomes "4m ago" with nothing having happened.
// Even that is counted rather than rendered: building a hundred descriptions to
// throw all but the widest away is three allocations per entry, on every
// keystroke, for a modal that draws two dozen rows.
func (r Requests) layout() layout {
	l := layout{
		inner:  max(minBoxWidth, lipgloss.Width(listHint)),
		label:  r.columns.label,
		source: r.columns.source,
	}

	now := r.now()
	describe := 0
	for _, e := range r.entries {
		describe = max(describe, describeWidth(e, now))
	}

	// The cursor gutter and one gap per column, the same on every row.
	const gaps = 2 + 2 + 2
	l.inner = min(max(l.inner, gaps+l.label+l.source+describe), r.available())

	return l
}

// saveLines renders the destination prompt.
func (r Requests) saveLines(l layout) []string {
	lines := []string{
		r.styles.PanelTitle.Render("Save request"),
		"",
		r.styles.Label.Render(savePrompt) + r.input.View(),
	}

	if names := r.collections.Names(); len(names) > 0 {
		lines = append(lines, "",
			styles.Truncate(r.styles.Muted.Render("collections: "+strings.Join(names, ", ")), l.inner))
	}

	if r.notice != "" {
		lines = append(lines, "", r.noticeLine(l))
	}
	return append(lines, "", r.styles.Hint.Render(saveHint))
}

// title counts what the list is showing, and what it is hiding when a filter is
// on: a browser that silently omits half your history is one you stop trusting.
func (r Requests) title() string {
	if r.query != "" && len(r.visible) != len(r.entries) {
		return fmt.Sprintf("Requests (%d of %d)", len(r.visible), len(r.entries))
	}
	return fmt.Sprintf("Requests (%d)", len(r.entries))
}

func (r Requests) filterLine(l layout) string {
	prompt := r.styles.Label.Render(filterPrompt)

	if r.mode == requestsFilter {
		return styles.Truncate(prompt+r.input.View(), l.inner)
	}
	if r.query == "" {
		return styles.Truncate(prompt+r.styles.FieldDisabled.Render("press / to search"), l.inner)
	}
	return styles.Truncate(prompt+r.styles.FieldValue.Render(r.query), l.inner)
}

// noticeLine reports the outcome of the last save. Only the save prompt ever
// shows one: a save that worked closes the browser, and one that did not keeps
// the prompt up so the name can be corrected — see [Requests.SetNotice].
func (r Requests) noticeLine(l layout) string {
	style := r.styles.Hint
	if r.failed {
		style = r.styles.FieldError
	}
	return styles.Truncate(style.Render(r.notice), l.inner)
}

func (r Requests) row(i int, l layout) string {
	e := r.entries[r.visible[i]]

	label := r.styles.Method.Render(padCell(e.request.Label(), l.label))
	source := r.styles.Service.Render(padCell(e.source, l.source))
	text := label + "  " + source + "  " + r.styles.Muted.Render(r.describe(e.request))

	if i != r.cursor {
		return styles.Truncate("  "+text, l.inner)
	}
	return r.styles.Cursor.Render(styles.Truncate("❯ "+text, l.inner))
}

// describe is the muted right-hand column: the method the request calls and how
// long ago it was sent.
func (r Requests) describe(req requests.Request) string {
	parts := []string{req.Method}
	if !req.SentAt.IsZero() {
		parts = append(parts, ago(r.now().Sub(req.SentAt)))
	}
	return strings.Join(parts, describeSeparator)
}

// describeSeparator joins the two halves of a description, and separatorWidth
// is how many cells it takes: the middle dot is two bytes and one cell, which
// is precisely the difference len() would get wrong.
const (
	describeSeparator = " · "
	separatorWidth    = 3
)

// describeWidth is how wide [Requests.describe] would be, without building it.
//
// It is arithmetic rather than a measurement because the widest description is
// wanted once per frame and every entry has to be asked. The two are pinned to
// each other by a test over a table of durations, which is the price of having
// written the width down twice.
func describeWidth(e entry, now time.Time) int {
	if e.request.SentAt.IsZero() {
		return e.methodWidth
	}
	return e.methodWidth + separatorWidth + agoWidth(now.Sub(e.request.SentAt))
}

// ago renders a duration the way a list wants it read: roughly, in one unit,
// and never in more precision than the reader can use.
func ago(d time.Duration) string {
	switch {
	case d < 0:
		// A request saved on another machine, or a clock that has been put back.
		// "in 3m" would be worse than saying nothing about when.
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%d %s ago", days, plural(days, "day"))
	}
}

// agoWidth is the width of [ago] without building the string. Every branch here
// mirrors one there, and TestAgoWidthMatchesAgo walks a table over both.
func agoWidth(d time.Duration) int {
	switch {
	case d < time.Minute:
		return len("just now")
	case d < time.Hour:
		return digits(int(d.Minutes())) + len("m ago")
	case d < 24*time.Hour:
		return digits(int(d.Hours())) + len("h ago")
	default:
		// len(plural(days, "day")) would be the obvious thing to write and would
		// allocate the string this exists to avoid building.
		days := int(d.Hours() / 24)
		width := digits(days) + len(" day ago")
		if days != 1 {
			width++
		}
		return width
	}
}

// digits is how many characters %d takes. Only non-negative numbers reach it:
// a negative duration is reported as "just now" before it gets here.
func digits(n int) int {
	width := 1
	for n >= 10 {
		n /= 10
		width++
	}
	return width
}

// available is the widest the box's contents may be on this screen.
//
// A terminal too narrow to hold a bordered box at all still has to yield a
// number the box can be built from, so the result has a floor. What comes out
// is then wider than the terminal — which the root model clamps, as it does for
// every other panel with a minimum size it cannot always be given.
func (r Requests) available() int {
	const chrome = 4 // the box's border and padding
	return max(r.width-chrome, minValueWidth)
}
