package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/proxy"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// TrafficReplayMsg asks the root model to load a call the proxy saw into the
// request form.
//
// It carries the method by name and the request by bytes, because the panel has
// neither a descriptor nor any way to decode one. Turning those into a filled-in
// form is the root model's job, which is where the discovered schema lives.
type TrafficReplayMsg struct {
	// Method is the fully-qualified name with dots — "package.Service.Method" —
	// converted from the slashed path the wire uses, because that is the
	// spelling every other part of grpctui uses.
	Method string

	// Wire is the first request message of the call, as it went past.
	Wire []byte
}

// TrafficCall is one call the proxy has seen, folded up from its events.
type TrafficCall struct {
	// ID is the proxy's call number, which is what the events are matched by.
	ID int

	// Method is the path as it appeared on the wire, "/package.Service/Method".
	Method string

	// Peer is where the call came from.
	Peer string

	// At is when the call started.
	At time.Time

	// Sent and Received count the messages each way.
	Sent     int
	Received int

	// Request is the first message the client sent, kept so the call can be
	// loaded into the form. Later ones are counted and dropped: a replay starts
	// from the first message whatever shape the call was.
	Request []byte

	// Done says the call has finished, and Status how — with HasStatus false for
	// one that ended cleanly.
	Done      bool
	Status    grpcclient.CallStatus
	HasStatus bool
}

// ShortMethod is the method without its package and service, for a column too
// narrow to spend thirty cells on a prefix every row shares.
func (c TrafficCall) ShortMethod() string {
	name := strings.TrimPrefix(c.Method, "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// FullName is the method as the rest of grpctui spells it: dots throughout,
// rather than the slash the wire puts before the method.
func (c TrafficCall) FullName() string {
	return strings.ReplaceAll(strings.TrimPrefix(c.Method, "/"), "/", ".")
}

// maxTrafficCalls is how many calls the panel keeps. A proxy in front of a busy
// service sees thousands, and the tail is what anybody looks at — the same
// bound, and the same reasoning, as the stream log's.
const maxTrafficCalls = 200

// Traffic is the log of calls the passive proxy has seen.
//
// It is a modal chooser like the request browser, and for the same reason:
// picking a call out of it fills the form in, which is the whole point of
// watching somebody else's traffic. What it does not do is decode anything —
// the messages are bytes here and become a request only once the root model has
// a descriptor to read them against.
type Traffic struct {
	keys   keys.KeyMap
	styles styles.Styles

	// listen is where the proxy is listening, shown in the title so that a panel
	// with nothing in it says where to point the client rather than looking
	// broken.
	listen string

	// enabled is false when grpctui was started without --proxy, which is the
	// ordinary case. The key then explains itself instead of opening an empty
	// list that will never fill.
	enabled bool

	calls []TrafficCall

	// at maps a proxy call number to its index in calls, so folding an event in
	// is a lookup rather than a scan of two hundred rows per message.
	at map[int]int

	// dropped counts the calls that fell off the front, and lost the events the
	// proxy could not hand over. Both are said out loud: a log quietly missing
	// entries is worse than one that admits to it.
	dropped int
	lost    int

	opened bool
	cursor int
	offset int

	// now is the clock, so a list of "5m ago" is reproducible in a golden file.
	now func() time.Time

	width  int
	height int
}

// NewTraffic builds an empty traffic panel.
func NewTraffic(km keys.KeyMap, st styles.Styles) Traffic {
	return Traffic{keys: km, styles: st, at: make(map[int]int), now: time.Now}
}

// SetClock replaces the clock the panel reads.
func (t *Traffic) SetClock(now func() time.Time) {
	if now != nil {
		t.now = now
	}
}

// Enable says the proxy is running, and where.
func (t *Traffic) Enable(listen string) {
	t.enabled = true
	t.listen = listen
}

// Enabled reports whether a proxy is running at all.
func (t Traffic) Enabled() bool { return t.enabled }

// SetLost records how many events the proxy had to throw away.
func (t *Traffic) SetLost(n int) { t.lost = n }

// Len is how many calls the panel is holding.
func (t Traffic) Len() int { return len(t.calls) }

// Calls returns the log, oldest first.
func (t Traffic) Calls() []TrafficCall { return t.calls }

// Record folds one proxy event into the log.
//
// Events for a call that has already fallen off the front are ignored rather
// than resurrecting it: the row is gone, and a stream that outlives two hundred
// other calls would otherwise keep coming back at the bottom of the list.
func (t *Traffic) Record(event proxy.Event) {
	if event.Kind == proxy.Started {
		t.start(event)
		return
	}

	i, ok := t.at[event.Call]
	if !ok {
		return
	}

	call := &t.calls[i]
	switch event.Kind {
	case proxy.Started:
		// Handled above, before the lookup: a second Started for the same call
		// number cannot happen, since the proxy counts them itself.

	case proxy.Sent:
		call.Sent++
		if call.Request == nil {
			call.Request = event.Wire
		}
	case proxy.Received:
		call.Received++
	case proxy.Ended:
		call.Done = true
		call.Status = event.Status
		call.HasStatus = event.HasStatus
	}
}

// start adds a new call, dropping the oldest once the log is full.
func (t *Traffic) start(event proxy.Event) {
	// The cursor follows the tail only while it is already there, so that a user
	// reading a call from ten minutes ago is not dragged away from it by traffic
	// arriving underneath.
	following := t.cursor >= len(t.calls)-1

	t.calls = append(t.calls, TrafficCall{
		ID:     event.Call,
		Method: event.Method,
		Peer:   event.Peer,
		At:     event.At,
	})
	t.at[event.Call] = len(t.calls) - 1

	if len(t.calls) > maxTrafficCalls {
		drop := len(t.calls) - maxTrafficCalls
		for _, gone := range t.calls[:drop] {
			delete(t.at, gone.ID)
		}
		t.calls = t.calls[drop:]
		t.dropped += drop

		for i := range t.calls {
			t.at[t.calls[i].ID] = i
		}
		t.cursor -= drop
	}

	if following {
		t.cursor = len(t.calls) - 1
	}
	t.moveTo(t.cursor)
}

// Open shows the log.
func (t *Traffic) Open() {
	t.opened = true
	t.moveTo(t.cursor)
}

// Close hides the panel.
func (t *Traffic) Close() { t.opened = false }

// Opened reports whether the panel is on screen.
func (t Traffic) Opened() bool { return t.opened }

// SetSize sets the area the panel may draw into.
func (t *Traffic) SetSize(width, height int) {
	t.width = width
	t.height = height
	t.clampOffset()
}

// Update handles the panel's keys while it is open.
func (t Traffic) Update(msg tea.Msg) (Traffic, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !t.opened {
		return t, nil
	}

	if cursor, moved := moveCursor(keyMsg, t.keys, t.cursor, len(t.calls)); moved {
		t.moveTo(cursor)
		return t, nil
	}

	switch {
	case key.Matches(keyMsg, t.keys.PageUp):
		t.moveTo(t.cursor - t.visibleRows())
	case key.Matches(keyMsg, t.keys.PageDown):
		t.moveTo(t.cursor + t.visibleRows())

	case key.Matches(keyMsg, t.keys.Select):
		return t, t.replay()

	case key.Matches(keyMsg, t.keys.Cancel), key.Matches(keyMsg, t.keys.Traffic):
		t.Close()
	}
	return t, nil
}

// replay asks the root model to load the call under the cursor into the form.
func (t *Traffic) replay() tea.Cmd {
	call, ok := t.current()
	if !ok || call.Request == nil {
		return nil
	}

	t.Close()
	msg := TrafficReplayMsg{Method: call.FullName(), Wire: call.Request}
	return func() tea.Msg { return msg }
}

func (t Traffic) current() (TrafficCall, bool) {
	if t.cursor < 0 || t.cursor >= len(t.calls) {
		return TrafficCall{}, false
	}
	return t.calls[t.cursor], true
}

func (t *Traffic) moveTo(i int) {
	t.cursor = clampIndex(i, len(t.calls))
	t.clampOffset()
}

func (t *Traffic) clampOffset() {
	t.offset = clampWindow(t.cursor, t.offset, t.visibleRows(), len(t.calls))
}

// visibleRows is how many calls fit, after the title, the blank line and the
// hint.
func (t Traffic) visibleRows() int {
	const chrome = 5
	if t.height <= 0 {
		return max(len(t.calls), 1)
	}
	return max(t.height-chrome, 1)
}

const (
	trafficHint = "enter load into the form · esc close"
	closeHint   = "esc close"
)

// emptyMessage is what the panel says instead of a list, or "" when there is a
// list to show. It is a method rather than two literals inside View so that
// [Traffic.inner] can size the box around it — a message wider than the box is
// one the border wraps into nonsense.
func (t Traffic) emptyMessage() string {
	switch {
	case !t.enabled:
		return "Not proxying. Start grpctui with --proxy <addr> to watch traffic."
	case len(t.calls) == 0:
		return "Nothing yet. Point a client at " + t.listen + "."
	default:
		return ""
	}
}

// hint is the key line: there is nothing to load from an empty list.
func (t Traffic) hint() string {
	if t.emptyMessage() != "" {
		return closeHint
	}
	return trafficHint
}

// View renders the panel as a box the root model centres on the screen.
func (t Traffic) View() string {
	box := t.styles.Panel.Width(t.inner() + t.styles.Panel.GetHorizontalPadding())

	lines := []string{t.styles.PanelTitle.Render(t.title()), ""}

	switch {
	case t.emptyMessage() != "":
		lines = append(lines, t.styles.Muted.Render(t.emptyMessage()))
	default:
		if t.dropped > 0 {
			lines = append(lines, t.styles.Muted.Render(
				fmt.Sprintf("… %d earlier %s dropped", t.dropped, plural(t.dropped, "call"))))
		}
		visible := min(t.visibleRows(), len(t.calls)-t.offset)
		for i := t.offset; i < t.offset+visible; i++ {
			lines = append(lines, t.row(i))
		}
	}

	if t.lost > 0 {
		lines = append(lines, "", t.styles.Hint.Render(
			fmt.Sprintf("%d %s were dropped while the panel was busy",
				t.lost, plural(t.lost, "event"))))
	}
	return box.Render(strings.Join(append(lines, "", t.styles.Hint.Render(t.hint())), "\n"))
}

func (t Traffic) title() string {
	if !t.enabled {
		return "Traffic"
	}
	return fmt.Sprintf("Traffic · %s (%d)", t.listen, len(t.calls))
}

// row renders one call: when, what, from where, how much it carried and how it
// ended.
func (t Traffic) row(i int) string {
	call := t.calls[i]

	text := strings.Join([]string{
		padCell(ago(t.now().Sub(call.At)), trafficAgeWidth),
		padCell(call.ShortMethod(), t.methodWidth()),
		t.styles.Muted.Render(fmt.Sprintf("→%d ←%d", call.Sent, call.Received)),
		t.outcome(call),
	}, "  ")

	if i != t.cursor {
		return styles.Truncate("  "+text, t.inner())
	}
	return t.styles.Cursor.Render(styles.Truncate("❯ "+text, t.inner()))
}

// outcome says how a call ended, or that it has not.
func (t Traffic) outcome(call TrafficCall) string {
	switch {
	case !call.Done:
		return t.styles.StreamReceived.Render("open")
	case call.HasStatus:
		return t.styles.StatusError.Render(call.Status.Name)
	default:
		return t.styles.StatusOK.Render("OK")
	}
}

const (
	// trafficAgeWidth fits "59m ago", which is the widest relative time the
	// browser's renderer produces before it falls back to a date.
	trafficAgeWidth = 8

	maxTrafficMethodWidth = 28
)

func (t Traffic) methodWidth() int {
	w := 0
	for _, call := range t.calls {
		w = max(w, lipgloss.Width(call.ShortMethod()))
	}
	return min(max(w, 1), maxTrafficMethodWidth)
}

// inner is the box's content width: enough for the hint line and the widest
// row, bounded by the screen.
func (t Traffic) inner() int {
	want := max(minBoxWidth, lipgloss.Width(t.hint()), lipgloss.Width(t.emptyMessage()))
	if t.enabled {
		want = max(want, lipgloss.Width(t.title()))
	}
	for _, call := range t.calls {
		const gaps = 2 + 3*2 // the cursor gutter and the gaps between the columns
		want = max(want, gaps+trafficAgeWidth+t.methodWidth()+
			lipgloss.Width(fmt.Sprintf("→%d ←%d", call.Sent, call.Received))+
			lipgloss.Width(call.Status.Name))
	}
	return min(want, t.available())
}

func (t Traffic) available() int {
	const chrome = 4 // the box's border and padding
	return max(t.width-chrome, minValueWidth)
}
