package ui_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/testschema"
	"github.com/alonshuld/grpctui/internal/ui"
)

// v1.0 promised grpctui stays responsive against a service with a large schema.
// The panels are measured in internal/ui/panels; this is the whole frame, which
// is what bubbletea redraws on every single keystroke. Both suites build the
// schema from internal/testschema, so both are measuring the same thing.

// bigModel is a model that has discovered a large schema and is showing it.
//
// It logs to nowhere rather than to the test, unlike everything else here: a
// benchmark that rebuilds the model per iteration writes one "services
// discovered" line per iteration, and thousands of them bury the numbers the
// benchmark exists to print.
func bigModel(tb testing.TB) ui.Model {
	tb.Helper()

	client := healthyClient()
	client.services = testschema.Big()
	return settled(tb, ui.New(client, ui.WithLogger(zap.NewNop())))
}

// TestModel_LargeSchemaIsUsable is the smoke test behind the benchmarks: a
// schema this size is discovered, drawn, and navigable, rather than merely
// fast to fail at.
func TestModel_LargeSchemaIsUsable(t *testing.T) {
	m := bigModel(t)

	frame := m.View()
	require.NotEmpty(t, frame)
	assert.Contains(t, frame, testschema.ServiceName(0))

	// A frame is a screenful whatever the schema holds — the tree renders what
	// fits, not what it has.
	assert.LessOrEqual(t, len(strings.Split(frame, "\n")), termHeight+1)

	// And the far end is reachable: selecting the last method of the last
	// service builds its form like any other.
	m = asModel(t, keyPress(m, tea.KeyMsg{Type: tea.KeyEnd}))
	assert.NotEmpty(t, m.View())
}

// BenchmarkModel_View is the cost of one redraw with a large schema on screen.
// bubbletea pays it for every keystroke, so it is the number that decides
// whether the tool feels alive or sticky.
func BenchmarkModel_View(b *testing.B) {
	m := bigModel(b)

	for b.Loop() {
		_ = m.View()
	}
}

// BenchmarkModel_KeyThenView is a whole keystroke: handle it, then draw the
// frame it produced.
func BenchmarkModel_KeyThenView(b *testing.B) {
	m := bigModel(b)
	down := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}

	for b.Loop() {
		next, _ := m.Update(down)
		m, _ = next.(ui.Model)
		_ = m.View()
	}
}

// BenchmarkModel_Discovery is the one-off: turning what reflection reported
// into the tree, on connect and on every profile switch.
func BenchmarkModel_Discovery(b *testing.B) {
	client := healthyClient()
	client.services = testschema.Big()

	base := sized(b, ui.New(client, ui.WithLogger(zap.NewNop())))
	msg := discoveryResult(b, base)

	for b.Loop() {
		next, _ := base.Update(msg)
		_ = next
	}
}

func keyPress(m ui.Model, key tea.KeyMsg) tea.Model {
	next, _ := m.Update(key)
	return next
}
