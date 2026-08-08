package panels_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/testschema"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// v1.0 promised the tree and the search stay responsive against a service with
// a large schema. These are the measurements behind that claim, and the
// assertions that keep it true.
//
// The number that matters is not how long a keystroke takes but what it scales
// with. A tree that renders every row it holds is fine at ten services and
// unusable at three hundred, and the difference does not show up in a test that
// asserts a duration — it shows up in one that asserts the work done per
// keystroke does not depend on how much there is. Both are here: the benchmarks
// measure, and the tests pin the shape.

// The schema comes from internal/testschema, which internal/ui measures the
// whole frame against — two copies of the generator would be one edit away from
// two suites benchmarking different workloads under the same claim.
const (
	bigRows      = testschema.BigServices * (testschema.BigMethods + 1)
	panelWidth   = 40
	panelHeight  = 30
	historyDepth = 200
)

func bigTree(tb testing.TB) panels.Tree {
	tb.Helper()

	tree := panels.NewTree(keys.Default(), styles.New())
	tree.SetSize(panelWidth, panelHeight)
	tree.SetServices(testschema.Big())
	tree.Focus()
	return tree
}

// TestTree_RendersOnlyWhatFits is the invariant that makes the tree scale: a
// frame costs a screenful, not a schema. Three hundred services expanded is
// 3,300 rows, and rendering all of them to show thirty would be the whole
// difference between a tree that moves and one that does not.
func TestTree_RendersOnlyWhatFits(t *testing.T) {
	tree := bigTree(t)
	require.Equal(t, bigRows, tree.Len(), "every service starts expanded")

	lines := strings.Split(tree.View(), "\n")

	assert.Len(t, lines, panelHeight)
	for _, line := range lines {
		assert.LessOrEqual(t, len(line), panelWidth*4,
			"a row is truncated to the panel, allowing for styling escapes")
	}
}

// TestTree_ScrollsThroughAWholeBigSchema walks the cursor from the top of a
// 3,300-row tree to the bottom a page at a time, which is the interaction most
// likely to go quadratic — each step reads the rows, and a step that rebuilt
// them would turn a scroll into 3,300 rebuilds.
func TestTree_ScrollsThroughAWholeBigSchema(t *testing.T) {
	tree := bigTree(t)

	// A page is a screenful less one row of overlap, so the count is derived
	// rather than guessed — and rounded up, since the last page is a short one.
	page := panelHeight - 1
	for range (bigRows+page-1)/page + 1 {
		tree, _ = tree.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	}

	svc, method, ok := tree.Selection()
	require.True(t, ok, "the cursor has to land on a row, not past the end")
	assert.Equal(t, testschema.ServiceName(testschema.BigServices-1), svc.Name)
	assert.Equal(t, testschema.MethodName(testschema.BigMethods-1), method.Name)
	assert.NotEmpty(t, tree.View())
}

// TestTree_SelectMethodFindsTheLastOne pins that recalling a request out of
// history reaches a method at the far end of a large schema — the path that
// scans, and the one where an off-by-one leaves the tree pointing at the wrong
// row while the form shows the right one.
func TestTree_SelectMethodFindsTheLastOne(t *testing.T) {
	tree := bigTree(t)
	want := testschema.ServiceName(testschema.BigServices-1) + "." + testschema.MethodName(testschema.BigMethods-1)

	_, method, ok := tree.SelectMethod(want)

	require.True(t, ok)
	assert.Equal(t, want, method.FullName)

	svc, selected, ok := tree.Selection()
	require.True(t, ok, "the cursor moves to what was selected")
	assert.Equal(t, want, selected.FullName)
	assert.Equal(t, testschema.ServiceName(testschema.BigServices-1), svc.Name)
}

func BenchmarkTree_SetServices(b *testing.B) {
	services := testschema.Big()
	tree := panels.NewTree(keys.Default(), styles.New())
	tree.SetSize(panelWidth, panelHeight)

	for b.Loop() {
		tree.SetServices(services)
	}
}

// BenchmarkTree_View is the one that decides whether the tree feels alive:
// bubbletea redraws the whole frame on every keystroke, so this runs once per
// key the user presses.
func BenchmarkTree_View(b *testing.B) {
	tree := bigTree(b)

	for b.Loop() {
		_ = tree.View()
	}
}

func BenchmarkTree_Down(b *testing.B) {
	tree := bigTree(b)
	down := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}

	for b.Loop() {
		tree, _ = tree.Update(down)
	}
}

// BenchmarkTree_Collapse measures the keystroke that does rebuild the row list,
// which is the expensive one by design — and still has to be a single pass.
func BenchmarkTree_Collapse(b *testing.B) {
	tree := bigTree(b)
	collapse := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}}
	expand := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}}

	for b.Loop() {
		tree, _ = tree.Update(collapse)
		tree, _ = tree.Update(expand)
	}
}

func BenchmarkTree_SelectMethod(b *testing.B) {
	tree := bigTree(b)
	last := testschema.ServiceName(testschema.BigServices-1) + "." + testschema.MethodName(testschema.BigMethods-1)

	for b.Loop() {
		tree.SelectMethod(last)
	}
}

// bigHistory is a full history — the cap, so the largest one that can exist —
// of requests with bodies worth searching.
func bigHistory(tb testing.TB) requests.History {
	tb.Helper()

	h, err := requests.LoadHistory("", historyDepth)
	require.NoError(tb, err)

	for i := range historyDepth {
		h.Add(requests.Request{
			Method: testschema.ServiceName(i%testschema.BigServices) + "." + testschema.MethodName(i%testschema.BigMethods),
			Body: map[string]any{
				"tenant":  fmt.Sprintf("tenant-%03d", i),
				"user":    map[string]any{"id": i, "email": fmt.Sprintf("user%d@example.com", i)},
				"scopes":  []any{"read", "write"},
				"comment": strings.Repeat("some text worth searching ", 8),
			},
			SentAt: time.Now(),
		})
	}
	return h
}

func bigRequests(tb testing.TB) panels.Requests {
	tb.Helper()

	r := panels.NewRequests(keys.Default(), styles.New())
	r.SetSize(80, panelHeight)
	r.SetHistory(bigHistory(tb))
	r.Open()
	return r
}

// TestRequests_FilterAcrossAFullHistory pins that searching a full history
// narrows it rather than emptying it — the assertion that would catch a
// haystack quietly losing the body it is built from.
func TestRequests_FilterAcrossAFullHistory(t *testing.T) {
	r := bigRequests(t)
	require.Equal(t, historyDepth, r.Len())

	r, _ = typeQuery(t, r, "tenant-042")

	assert.Contains(t, r.View(), "Method",
		"a query matching one entry still renders a list with that entry in it")
}

// TestRequests_FrameCostDoesNotGrowWithTheHistory is the regression test for
// the browser's worst performance bug: measuring the box's width asked every
// row for the two column widths, and each of those was a pass over every entry
// — so a frame was quadratic in the history, and a full one took tens of
// milliseconds to draw on every keystroke.
//
// It counts *clock reads*, and that is not a curiosity. Time is the obvious
// thing to measure and useless: a shared CI runner makes a duration a coin
// toss. Allocations are the next thing to reach for and were worse than
// useless here — the quadratic work was arithmetic over widths and allocated
// nothing, so an allocation ratio sat comfortably under its threshold while the
// frame took 43 times as long. What the passes over the entries did do, every
// single time, was ask what time it is, since a row's description says how long
// ago it was sent. Counting that is exact, deterministic, and impossible to
// satisfy without actually walking the entries.
//
// A frame reads the clock once to measure the description column, and once per
// row it draws. Neither depends on how much history there is, so the count is
// the same at 50 entries and at 800 — and any pass over the entries that builds
// a description puts the history's own size into it.
func TestRequests_FrameCostDoesNotGrowWithTheHistory(t *testing.T) {
	const small, large = 50, 800

	frame := func(entries int) (clockReads int, allocs float64) {
		r := requestsWith(t, entries)
		allocs = testing.AllocsPerRun(20, func() { _ = r.View() })

		r.SetClock(func() time.Time {
			clockReads++
			return time.Now()
		})
		_ = r.View()
		return clockReads, allocs
	}

	baseReads, baseAllocs := frame(small)
	require.Positive(t, baseReads, "a frame reads the clock, or this measures nothing")
	require.Positive(t, baseAllocs)

	grownReads, grownAllocs := frame(large)

	assert.Equal(t, baseReads, grownReads,
		"%d times the history read the clock %d times instead of %d: the frame walks the entries",
		large/small, grownReads, baseReads)

	// The same statement in the other currency, which catches a frame that grows
	// by building strings rather than by reading the clock. It is a bound rather
	// than a ratio: nothing about drawing two dozen rows should cost more because
	// there are more rows out of sight.
	assert.Less(t, grownAllocs, baseAllocs*1.1,
		"%d times the history cost %.0f allocations per frame instead of %.0f",
		large/small, grownAllocs, baseAllocs)
}

// requestsWith builds a browser holding n history entries.
func requestsWith(tb testing.TB, n int) panels.Requests {
	tb.Helper()

	h, err := requests.LoadHistory("", n)
	require.NoError(tb, err)
	for i := range n {
		h.Add(requests.Request{
			Method: testschema.ServiceName(i%testschema.BigServices) + "." + testschema.MethodName(i%testschema.BigMethods),
			Body:   map[string]any{"tenant": fmt.Sprintf("tenant-%03d", i)},
			SentAt: time.Now(),
		})
	}

	r := panels.NewRequests(keys.Default(), styles.New())
	r.SetSize(80, panelHeight)
	r.SetHistory(h)
	r.Open()
	return r
}

// BenchmarkRequests_Filter is what runs on every keystroke typed into the
// search box, over the largest history there can be.
func BenchmarkRequests_Filter(b *testing.B) {
	r := bigRequests(b)
	slash := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	r, _ = r.Update(slash)

	letters := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'t'}},
		{Type: tea.KeyRunes, Runes: []rune{'e'}},
		{Type: tea.KeyRunes, Runes: []rune{'n'}},
		{Type: tea.KeyRunes, Runes: []rune{'a'}},
	}

	for b.Loop() {
		for _, letter := range letters {
			r, _ = r.Update(letter)
		}
		for range letters {
			r, _ = r.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		}
	}
}

// BenchmarkRequests_SetHistory is paid on every send: the browser rebuilds its
// haystacks when the history it shows changes.
func BenchmarkRequests_SetHistory(b *testing.B) {
	history := bigHistory(b)
	r := panels.NewRequests(keys.Default(), styles.New())
	r.SetSize(80, panelHeight)

	for b.Loop() {
		r.SetHistory(history)
	}
}

func BenchmarkRequests_View(b *testing.B) {
	r := bigRequests(b)

	for b.Loop() {
		_ = r.View()
	}
}

// typeQuery opens the filter and types a query into it.
func typeQuery(t *testing.T, r panels.Requests, query string) (panels.Requests, tea.Cmd) {
	t.Helper()

	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	var cmd tea.Cmd
	for _, ch := range query {
		r, cmd = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	return r, cmd
}
