// traffic.go holds passive mode's side of the model: watching the proxy's
// event channel, logging what goes past, and loading one of those calls into
// the form.

package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/proxy"
	"github.com/alonshuld/grpctui/internal/ui/panels"
)

// watchTraffic reads one event off the proxy. Each one issues the next, which
// is the same chain of commands a stream is drained by and for the same reason:
// Update must never block on something that may not happen for an hour.
func watchTraffic(events <-chan proxy.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		return trafficEventMsg{event: event, ok: ok}
	}
}

// recordTraffic folds one proxy event into the traffic log and asks for the
// next.
//
// A closed channel ends the chain: the proxy has stopped, and what it saw stays
// on screen.
func (m Model) recordTraffic(msg trafficEventMsg) (tea.Model, tea.Cmd) {
	if !msg.ok {
		m.logger.Debug("the proxy stopped reporting")
		return m, nil
	}

	m.traffic.Record(msg.event)
	if m.dropped != nil {
		m.traffic.SetLost(m.dropped())
	}
	if m.traffic.Opened() {
		// Only when it is on screen: the log grows on every message a busy
		// service carries, and re-measuring the whole frame for a panel nobody
		// is looking at is work for nothing.
		m.layout()
	}
	return m, watchTraffic(m.events)
}

// replayTraffic fills the form in from a call the proxy watched go past.
//
// This is what makes passive mode more than a log: the request arrives as bytes
// with no schema attached, and the descriptor reflection already discovered is
// what turns it back into a form you can edit and send yourself.
func (m Model) replayTraffic(msg panels.TrafficReplayMsg) (tea.Model, tea.Cmd) {
	if m.state != stateReady {
		return m, nil
	}

	svc, method, ok := m.tree.SelectMethod(msg.Method)
	if !ok {
		m.request.SetNotice(msg.Method + " is not on this connection.")
		m.layout()
		m.logger.Debug("a proxied method is not on this connection",
			zap.String("method", msg.Method))
		return m, nil
	}

	decoded, err := protoschema.DecodeWire(method.InputDescriptor(), msg.Wire)
	if err != nil {
		m.request.SetNotice(err.Error())
		m.layout()
		m.logger.Warn("could not decode a proxied request",
			zap.String("method", msg.Method), zap.Error(err))
		return m, nil
	}

	m.retireCall()
	m.request.SetMethod(svc, method)
	m.request.Load(decoded)
	m.response.SetMethod(method)
	m.request.SetNotice("Loaded from the proxy. Headers are the ones on this connection.")

	// The form now holds something that came from outside the history walk, so
	// the walk starts over — the same thing recalling from the browser does.
	m.historyAt = noHistory
	m.layout()

	m.logger.Debug("loaded a proxied request", zap.String("method", msg.Method))
	return m, nil
}
