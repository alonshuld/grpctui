// update.go holds the message loop: Update and the key handling it dispatches
// to. Blocking work never happens here — everything slow leaves as a tea.Cmd.

package ui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/vars"
)

// Update routes messages to the panels and handles global keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		m.measureHelp()
		m.layout()
		return m, nil

	case tea.KeyMsg:
		if cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}

	case servicesDiscoveredMsg:
		m.state = stateReady
		m.err = nil
		m.services = msg.services
		m.tree.SetServices(msg.services)
		m.request.Clear()
		m.response.Clear()
		m.layout()
		m.logger.Info("services discovered",
			zap.String("target", m.client.Target()),
			zap.Int("services", len(msg.services)),
		)
		return m, nil

	case discoveryFailedMsg:
		m.state = stateFailed
		m.err = msg.err
		m.logger.Error("discovery failed",
			zap.String("target", m.client.Target()),
			zap.Error(msg.err),
		)
		return m, nil

	case panels.MethodSelectedMsg:
		// A call still in flight was made against the method the user has just
		// left, so it is cancelled and its sequence number retired. Without
		// that, its answer arrives after the panels have moved on and is drawn
		// under the new method's name as if it belonged to it — and, since the
		// panel is no longer in its in-flight state, esc has stopped being able
		// to cancel it.
		m.retireCall()

		// Focus deliberately stays on the tree: selecting builds the request
		// form but does not move the user into it, so j/k keep walking the
		// method list. Tab is how you enter the form — the same way lazygit and
		// k9s behave.
		// The form now holds something the user built rather than something
		// recalled, so the history walk starts over: `[` from here means "the
		// last thing I sent", not "one before wherever I had walked to".
		m.historyAt = noHistory

		m.request.SetMethod(msg.Service, msg.Method)
		m.response.SetMethod(msg.Method)
		m.layout()
		m.logger.Debug("method selected", zap.String("method", msg.Method.FullName))
		return m, nil

	case panels.SendRequestMsg:
		return m, m.dispatch(msg)

	case panels.LoadRequestMsg:
		return m.loadRequest(msg)

	case panels.SaveRequestMsg:
		return m, m.saveRequest(msg)

	case requestSavedMsg:
		return m.finishSave(msg)

	case historySavedMsg:
		if msg.err != nil {
			// History is a convenience, not the call. A state directory that
			// cannot be written to is worth a line in the log and nothing on
			// screen: the request went out either way.
			m.logger.Warn("could not write history", zap.Error(msg.err))
		}
		return m, nil

	case callFinishedMsg:
		return m.finishCall(msg)

	case streamOpenedMsg:
		return m.streamOpened(msg)

	case streamSentMsg:
		return m.streamSent(msg)

	case streamRecvMsg:
		return m.streamReceived(msg)

	// The choices a modal makes about the session rather than about the request
	// in front of you: which connection, which environment, which theme, and
	// what the variables are bound to. They are handled together in
	// [Model.updateSession] because they are one subject, and because keeping
	// them here would leave this switch too long to read.
	case panels.ProfileSelectedMsg, panels.EnvironmentSelectedMsg, panels.ThemeSelectedMsg,
		panels.VariableBoundMsg, panels.VariableUnboundMsg, panels.VariableCapturedMsg:
		return m.updateSession(msg)

	case panels.TrafficReplayMsg:
		return m.replayTraffic(msg)

	case trafficEventMsg:
		return m.recordTraffic(msg)

	case clientConnectedMsg:
		return m.finishConnect(msg)

	case spinner.TickMsg:
		return m.tick(msg)
	}

	return m.updatePanels(msg)
}

// updateSession handles the messages the session-wide modals emit: switching
// connection, environment or theme, and binding or unbinding a variable.
func (m Model) updateSession(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case panels.ProfileSelectedMsg:
		return m, m.startConnect(msg)

	case panels.EnvironmentSelectedMsg:
		return m, m.switchEnvironment(msg)

	case panels.ThemeSelectedMsg:
		m.switchTheme(msg)
		return m, nil

	case panels.VariableBoundMsg:
		m.bind(vars.Variable{Name: msg.Name, Value: msg.Value})
		m.variables.Open()
		m.layout()
		m.logger.Debug("bound a variable", zap.String("name", msg.Name))
		return m, nil

	case panels.VariableUnboundMsg:
		env, _ := m.environments.Active()
		m.variables.SetVariables(m.bindings().Without(msg.Name), env.Name)
		m.request.SetResolver(m.bindings())
		m.layout()
		return m, nil

	case panels.VariableCapturedMsg:
		return m.capture(msg)
	}
	return m, nil
}

// handleKey handles the keys the root model owns. It reports whether the key
// was consumed, so panel keys fall through untouched.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	// ctrl+c comes before even the switcher: there must always be a way out.
	if key.Matches(msg, m.keys.ForceQuit) {
		return tea.Quit, true
	}

	if cmd, handled := m.handleModalKey(msg); handled {
		return cmd, true
	}

	// These work everywhere, including inside a text field being edited: send
	// and the panel switches, because filling in the last field and firing the
	// call is the whole workflow.
	switch {
	case key.Matches(msg, m.keys.Send):
		if m.state != stateReady {
			return nil, true
		}
		return m.send(), true

	case key.Matches(msg, m.keys.EndStream):
		return m.endSending(), true

	case key.Matches(msg, m.keys.Requests):
		// ctrl+r rather than a letter precisely so that it reaches here from
		// inside a half-typed field: recalling the request you meant is the
		// answer to "I am typing this out again".
		m.openBrowser()
		return nil, true

	case key.Matches(msg, m.keys.Capture):
		// ctrl+p for the same reason: reading an id out of the response and
		// putting it in the field you are already typing into is the ordinary
		// way a chain of requests gets built.
		m.startCapture()
		return nil, true

	case key.Matches(msg, m.keys.NextPanel):
		return nil, m.movePanel(1)

	case key.Matches(msg, m.keys.PrevPanel):
		return nil, m.movePanel(-1)
	}

	// While a field is being edited every remaining key is a character, not a
	// command — q types a q, and p types a p.
	if m.editing() {
		return nil, false
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit, true

	case key.Matches(msg, m.keys.Profiles):
		m.profiles.Open()
		return nil, true

	case key.Matches(msg, m.keys.Environments):
		m.environments.Open()
		return nil, true

	case key.Matches(msg, m.keys.Variables):
		m.variables.Open()
		m.layout()
		return nil, true

	case key.Matches(msg, m.keys.HistoryPrev):
		return m.stepHistory(1), true

	case key.Matches(msg, m.keys.HistoryNext):
		return m.stepHistory(-1), true

	case key.Matches(msg, m.keys.Save):
		m.startSave()
		return nil, true

	case key.Matches(msg, m.keys.Export):
		m.startExport()
		return nil, true

	case key.Matches(msg, m.keys.Traffic):
		m.traffic.Open()
		m.layout()
		return nil, true

	case key.Matches(msg, m.keys.Themes):
		m.themes.Open()
		m.layout()
		return nil, true

	// The two response renderings are toggled from anywhere rather than only
	// with the response panel focused. Reaching for the raw bytes is something
	// you do while looking at a form that produced a surprising answer, and
	// making it a two-key job would put it in the way of its own purpose.
	case key.Matches(msg, m.keys.RawView):
		m.response.ToggleRaw()
		return nil, true

	case key.Matches(msg, m.keys.Diff):
		m.response.ToggleDiff()
		return nil, true

	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.measureHelp()
		m.layout()
		return nil, true

	case key.Matches(msg, m.keys.Retry) && m.state == stateFailed:
		m.state = stateConnecting
		m.err = nil
		return tea.Batch(m.spinner.Tick, m.discover()), true

	case key.Matches(msg, m.keys.Cancel) && m.response.Active():
		m.abortCall()
		return nil, true
	}
	return nil, false
}

// handleModalKey gives the key to whichever modal is open, and reports whether
// one was.
//
// A modal owns every remaining key while it is up, including the panel switches
// — each is a choice to finish, not a place to tab out of. That is why this runs
// before the sends and the switches below: inside the browser ctrl+s means
// "load and send this one", and inside the variables panel ctrl+p means
// "capture into this prompt".
func (m *Model) handleModalKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	var cmd tea.Cmd

	switch {
	case m.profiles.Opened():
		m.profiles, cmd = m.profiles.Update(msg)
		// The two switchers have no rows of their own to reflow, so they are the
		// two that do not relayout.
		return cmd, true

	case m.environments.Opened():
		m.environments, cmd = m.environments.Update(msg)
		return cmd, true

	case m.themes.Opened():
		m.themes, cmd = m.themes.Update(msg)
		return cmd, true

	case m.browser.Opened():
		m.browser, cmd = m.browser.Update(msg)

	case m.variables.Opened():
		m.variables, cmd = m.variables.Update(msg)

	case m.traffic.Opened():
		m.traffic, cmd = m.traffic.Update(msg)

	case m.export.Opened():
		m.export, cmd = m.export.Update(msg)

	default:
		return nil, false
	}

	m.layout()
	return cmd, true
}

// editing reports whether a text field somewhere is being typed into.
func (m Model) editing() bool { return m.request.Editing() || m.metadata.Editing() }

// tick feeds a spinner tick to whichever spinner it belongs to. Each
// spinner.Model ignores ticks that are not its own, so both can be fed
// unconditionally.
func (m Model) tick(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// A stream's elapsed time advances on the tick rather than on its messages:
	// a watch that has gone quiet is still running, and a clock frozen beside a
	// turning spinner would read as a hung UI.
	if m.response.Streaming() {
		m.response.SetElapsed(m.elapsed())
	}

	if m.state == stateConnecting {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	var cmd tea.Cmd
	m.response, cmd = m.response.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m Model) updatePanels(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.state != stateReady {
		return m, nil
	}

	var cmds []tea.Cmd
	var cmd tea.Cmd

	m.tree, cmd = m.tree.Update(msg)
	cmds = append(cmds, cmd)

	m.request, cmd = m.request.Update(msg)
	cmds = append(cmds, cmd)

	m.metadata, cmd = m.metadata.Update(msg)
	cmds = append(cmds, cmd)

	m.response, cmd = m.response.Update(msg)
	cmds = append(cmds, cmd)

	// Editing a field changes how tall the form wants to be only when it grows
	// a hint row, but recomputing is cheap and getting it wrong leaves the
	// panels overlapping.
	m.layout()

	return m, tea.Batch(cmds...)
}

// movePanel cycles focus. It reports the key as consumed even before discovery
// lands, so tab never leaks through to a panel that is not on screen.
func (m *Model) movePanel(delta int) bool {
	if m.state != stateReady {
		return true
	}
	m.focus = focus((int(m.focus) + delta + focusCount) % focusCount)
	m.syncFocus()
	return true
}

func (m *Model) syncFocus() {
	m.tree.Blur()
	m.request.Blur()
	m.metadata.Blur()
	m.response.Blur()

	switch m.focus {
	case focusRequest:
		m.request.Focus()
	case focusMetadata:
		m.metadata.Focus()
	case focusResponse:
		m.response.Focus()
	default:
		m.tree.Focus()
	}
}
