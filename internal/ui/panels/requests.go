package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// LoadRequestMsg asks the root model to fill the form in from a saved request.
// Send is set when the user asked for it to go out immediately, which is what
// makes re-running yesterday's call one keystroke rather than two.
type LoadRequestMsg struct {
	Request requests.Request
	Send    bool
}

// SaveRequestMsg asks the root model to write whatever is in the form into a
// collection. The panel does not build the request itself — it has no
// descriptor and no form — so it only carries where the result should go.
type SaveRequestMsg struct {
	Collection string
	Name       string
}

// historySource is what the source column says for a request that was sent
// rather than saved.
const historySource = "history"

// requestsMode is what the browser is doing.
type requestsMode int

const (
	requestsClosed requestsMode = iota

	// requestsList is the browser proper: a list with a cursor.
	requestsList

	// requestsFilter is the same list with the query being typed. Every key is
	// a character while it lasts, which is why it is a mode and not a flag.
	requestsFilter

	// requestsSave is the prompt for where to put the form's request. It shares
	// the modal because it is the same subject — the saved requests — seen from
	// the writing end.
	requestsSave
)

// Requests is the saved-request browser: everything already sent and everything
// kept, in one searchable list.
//
// History and collections are shown together rather than in two panels because
// the question a user actually has is "where is that call I made", and whether
// they happened to name it afterwards is not how they remember it. The source
// column says which is which, and is itself searchable.
//
// It is modal, like the connection switcher: while it is open it owns the
// keyboard, because picking a request out of a list is a choice to finish
// rather than a place to tab out of.
type Requests struct {
	keys   keys.KeyMap
	styles styles.Styles

	history     requests.History
	collections requests.Collections

	// entries is history and collections flattened into one list, newest sends
	// first. It is rebuilt when either source changes, never per keystroke: the
	// search text is built with it, and folding a hundred bodies to lowercase on
	// every character typed is exactly the cost this avoids.
	entries []entry

	// visible indexes entries, holding the ones the current query matches.
	visible []int

	// input is the query while filtering and the destination while saving. One
	// input serves both because they never happen at once.
	input textinput.Model

	// query is the filter as it stands, kept when the user leaves filter mode so
	// that a list they narrowed stays narrowed.
	query string

	// notice reports the outcome of the last save, or why there was none.
	notice string
	failed bool

	mode   requestsMode
	cursor int
	offset int

	// now is the clock the relative timestamps read. Tests replace it, so that a
	// golden file of the browser is a function of its entries.
	now func() time.Time

	width  int
	height int
}

// entry is one row: a request, where it came from, and the text a query is
// matched against.
type entry struct {
	request  requests.Request
	source   string
	haystack string
}

// NewRequests builds an empty browser.
func NewRequests(km keys.KeyMap, st styles.Styles) Requests {
	input := textinput.New()
	input.Prompt = ""
	input.TextStyle = st.FieldValue
	input.PlaceholderStyle = st.FieldDisabled

	// A blinking cursor would make every frame time-dependent, which costs a
	// redraw a second for no information and makes golden files racy.
	input.Cursor.SetMode(cursor.CursorStatic)

	return Requests{keys: km, styles: st, input: input, now: time.Now}
}

// SetClock replaces the clock the relative timestamps read.
func (r *Requests) SetClock(now func() time.Time) {
	if now != nil {
		r.now = now
	}
}

// SetHistory replaces the sent requests.
func (r *Requests) SetHistory(h requests.History) {
	r.history = h
	r.rebuild()
}

// SetCollections replaces the saved requests.
func (r *Requests) SetCollections(c requests.Collections) {
	r.collections = c
	r.rebuild()
}

// Collections returns the collections the browser is showing, so the root model
// can hand them to whatever writes one.
func (r Requests) Collections() requests.Collections { return r.collections }

// Len reports how many requests there are in total, filter or no filter.
func (r Requests) Len() int { return len(r.entries) }

// Open shows the browser. It keeps whatever filter was last applied: coming
// back to a list you narrowed and finding it wide again is the kind of small
// betrayal that stops people using a feature.
func (r *Requests) Open() {
	r.mode = requestsList
	r.notice, r.failed = "", false
	r.moveTo(0)
}

// OpenSave shows the prompt for saving the form's request, seeded with a
// suggested `collection/name`.
func (r *Requests) OpenSave(suggested string) {
	r.mode = requestsSave
	r.notice, r.failed = "", false
	r.input.SetValue(suggested)
	r.input.Focus()
	r.input.CursorEnd()
}

// Close hides the browser.
func (r *Requests) Close() {
	r.mode = requestsClosed
	r.input.Blur()
}

// Opened reports whether the browser is on screen. While it is, it owns every
// key but ctrl+c.
func (r Requests) Opened() bool { return r.mode != requestsClosed }

// SetNotice reports the outcome of a save on the browser's status line, and
// leaves the browser open on a failure so the name can be corrected.
func (r *Requests) SetNotice(text string, failed bool) {
	r.notice, r.failed = text, failed
	if failed {
		r.mode = requestsSave
		r.input.Focus()
		r.input.CursorEnd()
		return
	}
	r.Close()
}

// SetSize sets the area the browser may draw into.
func (r *Requests) SetSize(width, height int) {
	r.width = width
	r.height = height
	r.resizeInput()
	r.clampOffset()
}

// resizeInput fits the text input to whatever is left of the box after the
// prompt beside it.
//
// bubbles/textinput pads its value to Width and then leaves room for the cursor
// past the end, so a view of Width occupies Width+1 cells; without the
// subtraction the input alone pushes the box past the screen, which is exactly
// what the save prompt did before it had a width of its own.
func (r *Requests) resizeInput() {
	prompt := filterPrompt
	if r.mode == requestsSave {
		prompt = savePrompt
	}
	r.input.Width = max(r.layout().inner-lipgloss.Width(prompt)-1, minValueWidth)
}

// Update handles the browser's keys while it is open.
func (r Requests) Update(msg tea.Msg) (Requests, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !r.Opened() {
		return r, nil
	}

	switch r.mode {
	case requestsFilter:
		return r.updateFilter(keyMsg)
	case requestsSave:
		return r.updateSave(keyMsg)
	default:
		return r.updateList(keyMsg)
	}
}

func (r Requests) updateList(msg tea.KeyMsg) (Requests, tea.Cmd) {
	switch {
	case key.Matches(msg, r.keys.Up):
		r.moveTo(r.cursor - 1)
	case key.Matches(msg, r.keys.Down):
		r.moveTo(r.cursor + 1)
	case key.Matches(msg, r.keys.PageUp):
		r.moveTo(r.cursor - r.visibleRows())
	case key.Matches(msg, r.keys.PageDown):
		r.moveTo(r.cursor + r.visibleRows())
	case key.Matches(msg, r.keys.Top):
		r.moveTo(0)
	case key.Matches(msg, r.keys.Bottom):
		r.moveTo(len(r.visible) - 1)

	case key.Matches(msg, r.keys.Filter):
		r.mode = requestsFilter
		r.input.SetValue(r.query)
		r.input.Focus()
		r.input.CursorEnd()

	case key.Matches(msg, r.keys.Cancel):
		// esc clears a filter before it closes the browser, so that narrowing a
		// list and changing your mind does not also throw the list away.
		if r.query != "" {
			r.setQuery("")
			return r, nil
		}
		r.Close()

	case key.Matches(msg, r.keys.Requests):
		r.Close()

	case key.Matches(msg, r.keys.Select):
		return r, r.choose(false)

	case key.Matches(msg, r.keys.Send):
		return r, r.choose(true)
	}
	return r, nil
}

// updateFilter routes keys to the query input. The list narrows as it is typed;
// enter settles on it and esc abandons it, restoring what it was before.
func (r Requests) updateFilter(msg tea.KeyMsg) (Requests, tea.Cmd) {
	switch {
	case key.Matches(msg, r.keys.Select):
		r.mode = requestsList
		r.input.Blur()
		return r, nil

	case key.Matches(msg, r.keys.Cancel):
		r.mode = requestsList
		r.input.Blur()
		r.setQuery("")
		return r, nil
	}

	var cmd tea.Cmd
	r.input, cmd = r.input.Update(msg)
	r.setQuery(r.input.Value())
	return r, cmd
}

// updateSave routes keys to the destination input.
func (r Requests) updateSave(msg tea.KeyMsg) (Requests, tea.Cmd) {
	switch {
	case key.Matches(msg, r.keys.Select):
		collection, name := requests.SplitName(r.input.Value())
		saved := SaveRequestMsg{Collection: collection, Name: name}
		return r, func() tea.Msg { return saved }

	case key.Matches(msg, r.keys.Cancel):
		r.Close()
		return r, nil
	}

	var cmd tea.Cmd
	r.input, cmd = r.input.Update(msg)
	return r, cmd
}

// choose closes the browser and asks for the request under the cursor.
func (r *Requests) choose(send bool) tea.Cmd {
	if r.cursor < 0 || r.cursor >= len(r.visible) {
		return nil
	}
	chosen := LoadRequestMsg{Request: r.entries[r.visible[r.cursor]].request, Send: send}

	r.Close()
	return func() tea.Msg { return chosen }
}

// setQuery re-filters the list, putting the cursor back at the top: after
// narrowing, the match you want is far more often the first one than wherever
// the cursor happened to be.
func (r *Requests) setQuery(query string) {
	r.query = query
	r.filter()
	r.moveTo(0)
}

// rebuild flattens history and collections into one list and re-applies the
// filter.
//
// History leads, newest first, because the overwhelmingly common reason to open
// this is the call you just made. Collections follow in name order, each in the
// order its file lists them — a collection is a document, and reordering
// somebody's document to suit a list is not the browser's business.
func (r *Requests) rebuild() {
	entries := make([]entry, 0, r.history.Len())
	for _, req := range r.history.Entries() {
		entries = append(entries, newEntry(req, historySource))
	}
	for _, col := range r.collections.All() {
		for _, req := range col.Requests {
			entries = append(entries, newEntry(req, col.Name))
		}
	}

	r.entries = entries
	r.filter()
	r.moveTo(r.cursor)
}

func newEntry(req requests.Request, source string) entry {
	// The source is part of the haystack, so that "team" narrows to a collection
	// and "history" to the sent list without either needing a key of its own.
	return entry{
		request:  req,
		source:   source,
		haystack: req.Search() + "\n" + strings.ToLower(source),
	}
}

func (r *Requests) filter() {
	matcher := requests.NewMatcher(r.query)

	visible := make([]int, 0, len(r.entries))
	for i, e := range r.entries {
		if matcher.Match(e.haystack) {
			visible = append(visible, i)
		}
	}
	r.visible = visible
}

func (r *Requests) moveTo(i int) {
	r.cursor = clampIndex(i, len(r.visible))
	r.clampOffset()
}

func (r *Requests) clampOffset() {
	r.offset = clampWindow(r.cursor, r.offset, r.visibleRows(), len(r.visible))
}

// visibleRows is how many requests fit, after the title, the filter line and
// the help line.
func (r Requests) visibleRows() int {
	const chrome = 6
	if r.height <= 0 {
		return max(len(r.visible), 1)
	}
	return max(r.height-chrome, 1)
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
// The width comes from every entry rather than the visible ones, so that
// filtering narrows the list without moving the walls around it.
func (r Requests) layout() layout {
	l := layout{
		inner:  max(minBoxWidth, lipgloss.Width(listHint)),
		label:  1,
		source: len(historySource),
	}

	// One pass, not three. The two column widths are the same on every row, so
	// the widest row is the widest description plus them — no second walk needed
	// once the maximum description is known.
	describe := 0
	for _, e := range r.entries {
		l.label = max(l.label, len(e.request.Label()))
		l.source = max(l.source, len(e.source))
		describe = max(describe, lipgloss.Width(r.describe(e.request)))
	}
	l.label = min(l.label, maxNameWidth)
	l.source = min(l.source, maxTypeWidth)

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
	return strings.Join(parts, " · ")
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
