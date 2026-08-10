// variables.go holds the {{name}} half of the session: the live bindings, the
// environment switch, and capturing a value out of a response into a variable.

package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/vars"
)

// bindings are the variables in force, which the variables panel owns.
func (m Model) bindings() vars.Set { return m.variables.Set() }

// installEnvironment points the variables panel and the request form at the
// active environment's bindings.
//
// Switching environment replaces the bindings rather than merging them: a value
// captured while pointed at staging is staging's, and carrying it into
// production would be the single most expensive thing this feature could do.
func (m *Model) installEnvironment() {
	env, _ := m.environments.Active()
	m.variables.SetVariables(env.Set(), env.Name)
	m.request.SetResolver(m.bindings())
}

// bind adds or replaces one variable for the rest of the session. Nothing is
// written to disk: a binding captured out of a response is very often a token,
// and grpctui's standing rule is that such a value never reaches a surface that
// outlives the call.
func (m *Model) bind(v vars.Variable) {
	env, _ := m.environments.Active()
	m.variables.SetVariables(m.bindings().With(v), env.Name)
	m.request.SetResolver(m.bindings())
}

// startCapture asks which value of the last response to bind, and to what.
//
// It refuses when there is nothing to read from rather than opening a prompt
// over an empty response: a prompt that can only fail is a worse answer than
// the reason it would have failed for.
func (m *Model) startCapture() {
	_, format, ok := m.responseBody()
	switch {
	case !ok:
		m.variables.Open()
		m.variables.SetNotice("There is no response to capture from yet.", true)
	case format != protoschema.FormatJSON:
		m.variables.Open()
		m.variables.SetNotice("This response is protobuf text, so there is no path to read.", true)
	default:
		m.variables.OpenCapture("")
	}
	m.layout()
}

// responseBody is the message a capture would read from.
func (m Model) responseBody() (string, protoschema.Format, bool) {
	return m.response.LastMessage()
}

// capture reads one value out of the last response and binds it.
//
// The lookup happens here rather than in the panel because only the root model
// has the response, and because a path that names nothing is a thing to report
// on the prompt — with the prompt still up, so it can be corrected — rather
// than a thing to guess at.
func (m Model) capture(msg panels.VariableCapturedMsg) (tea.Model, tea.Cmd) {
	body, _, ok := m.responseBody()
	if !ok {
		m.variables.SetNotice("There is no response to capture from yet.", true)
		m.layout()
		return m, nil
	}

	value, err := protoschema.LookupJSON(body, msg.Path)
	if err != nil {
		m.variables.SetNotice(err.Error(), true)
		m.layout()
		m.logger.Debug("could not capture from the response",
			zap.String("name", msg.Name), zap.Error(err))
		return m, nil
	}

	m.bind(vars.Variable{Name: msg.Name, Value: value, Captured: true})
	m.variables.Open()
	m.layout()

	// The name and where it came from, never what it is worth: a captured value
	// is a bearer token as often as not.
	m.logger.Info("captured a variable",
		zap.String("name", msg.Name), zap.String("path", msg.Path))
	return m, nil
}

// switchEnvironment installs another environment's bindings, and reconnects
// when it points somewhere else.
//
// Reconnecting is the point of an environment having a target at all: "staging"
// usually means both a different host and a different account id, and having to
// switch those separately is how a request meant for staging reaches
// production. The connection keeps the active profile's security and
// credentials — how to connect is the profile's business, where to connect is
// the environment's.
func (m *Model) switchEnvironment(msg panels.EnvironmentSelectedMsg) tea.Cmd {
	m.environments.SetActive(msg.Index)
	m.installEnvironment()
	m.layout()

	m.logger.Info("switched environment",
		zap.String("environment", msg.Environment.Name),
		zap.Strings("variables", m.bindings().Names()),
	)

	if msg.Environment.Target == "" || msg.Environment.Target == m.client.Target() {
		return nil
	}

	profile, ok := m.profiles.Active()
	if !ok {
		m.logger.Warn("cannot follow the environment's target: no profile is active",
			zap.String("target", msg.Environment.Target))
		return nil
	}

	profile.Target = msg.Environment.Target
	return m.startConnect(panels.ProfileSelectedMsg{
		Index:   m.profiles.ActiveIndex(),
		Profile: profile,
	})
}
