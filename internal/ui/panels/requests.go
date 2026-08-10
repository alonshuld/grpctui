package panels

import (
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

	// columns is the width of the two fixed columns, measured when the entries
	// change rather than when a frame is drawn. Neither depends on the clock, the
	// filter or the terminal, so measuring them per keystroke was a pass over the
	// whole history for an answer that had not moved.
	columns columns

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

	// methodWidth is the width of the fixed half of the row's description. The
	// other half is how long ago the request was sent, which is the only thing
	// about a row that changes without the entries themselves changing.
	methodWidth int
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
	r.columns = measure(entries)
	r.filter()
	r.moveTo(r.cursor)
}

func newEntry(req requests.Request, source string) entry {
	// The source is part of the haystack, so that "team" narrows to a collection
	// and "history" to the sent list without either needing a key of its own.
	return entry{
		request:     req,
		source:      source,
		haystack:    req.Search() + "\n" + strings.ToLower(source),
		methodWidth: lipgloss.Width(req.Method),
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
