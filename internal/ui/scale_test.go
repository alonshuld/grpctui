package ui_test

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui"
)

// v1.0 promised grpctui stays responsive against a service with a large schema.
// The panels are measured in internal/ui/panels; this is the whole frame, which
// is what bubbletea redraws on every single keystroke.
//
// 300 services of 10 methods is larger than any real API surface the author has
// met, and 3,300 rows once the tree expands them all.
const (
	bigServices = 300
	bigMethods  = 10
)

func bigSchema() []grpcclient.Service {
	services := make([]grpcclient.Service, 0, bigServices)
	for s := range bigServices {
		name := fmt.Sprintf("big.v1.Service%03d", s)

		methods := make([]grpcclient.Method, 0, bigMethods)
		for m := range bigMethods {
			method := fmt.Sprintf("Method%02d", m)
			methods = append(methods, grpcclient.Method{
				Name:            method,
				FullName:        name + "." + method,
				InputType:       name + ".Request",
				OutputType:      name + ".Reply",
				ServerStreaming: m%3 == 0,
			})
		}
		services = append(services, grpcclient.Service{Name: name, Methods: methods})
	}
	return services
}

// bigModel is a model that has discovered a large schema and is showing it.
//
// It logs to nowhere rather than to the test, unlike everything else here: a
// benchmark that rebuilds the model per iteration writes one "services
// discovered" line per iteration, and thousands of them bury the numbers the
// benchmark exists to print.
func bigModel(tb testing.TB) ui.Model {
	tb.Helper()

	client := healthyClient()
	client.services = bigSchema()
	return settled(tb, ui.New(client, ui.WithLogger(zap.NewNop())))
}

// TestModel_LargeSchemaIsUsable is the smoke test behind the benchmarks: a
// schema this size is discovered, drawn, and navigable, rather than merely
// fast to fail at.
func TestModel_LargeSchemaIsUsable(t *testing.T) {
	m := bigModel(t)

	frame := m.View()
	require.NotEmpty(t, frame)
	assert.Contains(t, frame, "big.v1.Service000")

	// A frame is a screenful whatever the schema holds — the tree renders what
	// fits, not what it has.
	assert.LessOrEqual(t, len(splitLines(frame)), termHeight+1)

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
	client.services = bigSchema()

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

func splitLines(frame string) []string {
	var lines []string
	start := 0
	for i, r := range frame {
		if r == '\n' {
			lines = append(lines, frame[start:i])
			start = i + 1
		}
	}
	return append(lines, frame[start:])
}
