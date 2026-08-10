// export.go turns the request on screen into an equivalent grpcurl command.

package ui

import (
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/export"
	"github.com/alonshuld/grpctui/internal/protoschema"
)

// startExport renders the request in the form as a grpcurl command.
//
// The request is built first, and a form that does not build stops here with
// its error rows showing rather than behind a modal that covers them — the same
// order [Model.startSave] uses, for the same reason.
//
// What is exported is the *template*: the request as it was typed, with its
// {{variable}} references intact and its header values replaced by
// placeholders. A command that ran as it stands would be one carrying a bearer
// token into whatever the user pastes it into.
func (m *Model) startExport() {
	if m.state != stateReady {
		return
	}

	method, ok := m.request.Method()
	if !ok {
		m.export.OpenNotice("Select a method first.")
		m.layout()
		return
	}

	req, ok := m.request.Submit()
	if !ok {
		m.layout()
		return
	}

	message := req.Template
	if message == nil {
		message = req.Request
	}

	body, _, err := protoschema.MarshalRequest(message)
	if err != nil {
		m.export.OpenNotice("The request could not be rendered: " + err.Error())
		m.layout()
		m.logger.Warn("could not export the request",
			zap.String("method", method.FullName), zap.Error(err))
		return
	}

	command := export.Command{
		Target: m.client.Target(),
		Method: method.FullName,
		Body:   body,
		Values: req.Values,
		// Names only. Resolving them would mean expanding the references, and
		// the values are exactly what must not leave.
		Headers: m.metadata.Headers().Keys(),
	}
	if profile, ok := m.profiles.Active(); ok {
		command.Security, command.Auth = profile.Security, profile.Auth
	}

	m.export.Open(export.Grpcurl(command))
	m.layout()
	m.logger.Debug("exported a request", zap.String("method", method.FullName))
}
