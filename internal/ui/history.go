// history.go holds the record side of a send: writing history, saving into a
// collection, and recalling an entry back into the form.

package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui/panels"
)

// record adds one sent request to history and returns the command that writes
// history out.
//
// The entry carries header *names* and no values, and the request as it was
// *typed* rather than as it was sent. A history file sits in the state
// directory for weeks and a collection is meant to be committed, so the
// standing rule that a credential never reaches a surface outliving the call
// applies to both — and since v0.7 the most likely credential in a request body
// is a {{token}} captured out of a login response.
func (m *Model) record(req panels.SendRequestMsg, md grpcclient.Metadata) tea.Cmd {
	entry, ok := m.entry(req, md)
	if !ok {
		return nil
	}

	m.history.Add(entry)
	m.browser.SetHistory(m.history)

	// The form now holds the newest entry, so [ should step to the one before it
	// rather than back to what was just sent.
	m.historyAt = 0

	return saveHistory(m.history)
}

// entry builds the record of one request, for history and for a collection
// alike — they hold the same thing for different reasons, so there is one place
// that decides what it holds.
//
// The body comes from the request's template, which is the same message with
// its {{variable}} references left as they were written. A template that could
// not be built falls back to the request that went out: a call worth making is
// worth recording imperfectly rather than not at all.
func (m Model) entry(req panels.SendRequestMsg, md grpcclient.Metadata) (requests.Request, bool) {
	message, values := req.Template, req.Values
	if message == nil {
		message = req.Request
	}

	body, err := protoschema.EncodeBody(message)
	if err != nil {
		// The request itself is fine — it built from the form. Only recording it
		// failed, and dropping the entry beats refusing the call.
		m.logger.Warn("could not record the request",
			zap.String("method", req.Method.FullName),
			zap.Error(err),
		)
		return requests.Request{}, false
	}

	entry := requests.Request{
		Method:  req.Method.FullName,
		Kind:    string(req.Method.Kind()),
		Target:  m.client.Target(),
		Headers: md.Keys(),
		Body:    body,
		Values:  values,
		SentAt:  m.now().UTC(),
	}
	if profile, ok := m.profiles.Active(); ok {
		entry.Profile = profile.Label()
	}
	return entry, true
}

// saveHistory writes history out off the Update goroutine.
func saveHistory(h requests.History) tea.Cmd {
	return func() tea.Msg { return historySavedMsg{err: h.Save()} }
}

// loadRequest fills the form in from the browser's choice, and sends it when
// that is what was asked for.
//
// The browser is a different way in from the history walk, so it puts the walk
// back to the start: `[` after picking something out of the list means "the
// last thing I sent" rather than one step from wherever the walk had reached
// before the list was opened. A collection entry has no place in that sequence
// at all until it is sent.
func (m Model) loadRequest(msg panels.LoadRequestMsg) (tea.Model, tea.Cmd) {
	if m.state != stateReady || !m.load(msg.Request) {
		return m, nil
	}
	m.historyAt = noHistory

	if !msg.Send {
		return m, nil
	}

	req, ok := m.request.Submit()
	if !ok {
		m.layout()
		return m, nil
	}
	return m, m.dispatch(req)
}

// openBrowser shows the saved-request list.
func (m *Model) openBrowser() {
	m.browser.Open()
	m.layout()
}

// startSave asks where to put the request in the form.
//
// The request is built first, and a form that does not build stops here with
// its error rows showing rather than behind a modal that covers them. That also
// means the prompt only ever opens for something that can actually be saved.
func (m *Model) startSave() {
	if m.state != stateReady {
		return
	}
	if _, ok := m.request.Submit(); !ok {
		m.layout()
		return
	}

	method, ok := m.request.Method()
	if !ok {
		return
	}

	m.browser.OpenSave(m.lastCollection + "/" + shortName(method.FullName))
	m.layout()
}

// stepHistory walks the sent requests and loads the one it lands on. A positive
// delta goes back in time, which is the direction `[` points.
func (m *Model) stepHistory(delta int) tea.Cmd {
	if m.state != stateReady || m.history.Len() == 0 {
		return nil
	}

	// From a form the user built themselves, `[` lands on the newest entry and
	// `]` does nothing: there is nothing newer than what is on screen.
	next := m.historyAt + delta
	if m.historyAt == noHistory {
		if delta < 0 {
			return nil
		}
		next = 0
	}
	if next < 0 || next >= m.history.Len() {
		return nil
	}

	entry, ok := m.history.At(next)
	if !ok {
		return nil
	}
	if !m.load(entry) {
		return nil
	}

	m.historyAt = next
	return nil
}

// load fills the tree, the form and the notice line in from a saved request,
// and reports whether it could.
//
// Header values are deliberately not restored: the record never held any. What
// the record does hold — the names — is said on the notice line when the live
// connection is not already sending them, so that a call that came back
// Unauthenticated has an explanation rather than a mystery.
func (m *Model) load(entry requests.Request) bool {
	svc, method, ok := m.tree.SelectMethod(entry.Method)
	if !ok {
		m.request.SetNotice(entry.Method + " is not on this connection.")
		m.layout()
		m.logger.Debug("recalled a method this connection does not have",
			zap.String("method", entry.Method))
		return false
	}

	msg, err := protoschema.DecodeBody(method.InputDescriptor(), entry.Body)
	if err != nil {
		m.request.SetNotice(err.Error())
		m.layout()
		m.logger.Warn("could not rebuild a saved request",
			zap.String("method", entry.Method), zap.Error(err))
		return false
	}

	m.retireCall()
	m.request.SetMethod(svc, method)
	m.request.Load(msg)

	// The references the body could not carry go back on the rows they were
	// typed into, which is what makes a recalled request a template again rather
	// than the zeroes its body had to hold. A path the method no longer has is
	// worth saying out loud: the alternative is a request quietly missing the
	// field you thought you had set.
	notice := m.missingHeaders(entry)
	if err := m.request.LoadValues(entry.Values); err != nil {
		notice = err.Error()
		m.logger.Warn("could not restore a saved request's variables",
			zap.String("method", entry.Method), zap.Error(err))
	}

	m.response.SetMethod(method)
	m.request.SetNotice(notice)
	m.layout()

	m.logger.Debug("recalled a request", zap.String("method", entry.Method))
	return true
}

// missingHeaders names the headers a recalled request went out with that the
// connection is not sending now, or "" when there are none.
func (m Model) missingHeaders(entry requests.Request) string {
	if len(entry.Headers) == 0 {
		return ""
	}

	have := make(map[string]bool, len(entry.Headers))
	for _, key := range m.metadata.Headers().Keys() {
		have[key] = true
	}

	var missing []string
	for _, key := range entry.Headers {
		if !have[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "This was sent with " + strings.Join(missing, ", ") + "."
}

// saveRequest writes the form's request into a collection.
func (m *Model) saveRequest(msg panels.SaveRequestMsg) tea.Cmd {
	req, ok := m.request.Submit()
	if !ok {
		// Between opening the prompt and answering it the modal owned the
		// keyboard, so the form cannot have changed — but a build that fails
		// twice is still a save that must not silently not happen.
		m.browser.SetNotice("The request no longer builds.", true)
		m.layout()
		return nil
	}

	// The headers are recorded by name, so an unresolved reference in one is no
	// reason to refuse a save: what goes in the file is the same either way.
	md, _ := m.headers()

	entry, ok := m.entry(req, md)
	if !ok {
		m.browser.SetNotice("The request could not be written down.", true)
		m.layout()
		return nil
	}
	entry.Name = msg.Name

	m.logger.Info("saving a request",
		zap.String("collection", msg.Collection),
		zap.String("name", msg.Name),
		zap.String("method", entry.Method),
	)
	return saveCollection(m.browser.Collections(), msg.Collection, entry)
}

// saveCollection writes one collection out off the Update goroutine, handing
// back the updated set so the model can install it.
func saveCollection(c requests.Collections, collection string, entry requests.Request) tea.Cmd {
	return func() tea.Msg {
		err := c.Save(collection, entry)
		return requestSavedMsg{
			collections: c,
			collection:  collection,
			name:        entry.Name,
			err:         err,
		}
	}
}

// finishSave reports the outcome of a save, leaving the prompt open on a
// failure so the name can be corrected rather than retyped.
func (m Model) finishSave(msg requestSavedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.browser.SetNotice(msg.err.Error(), true)
		m.layout()
		m.logger.Error("could not save the request", zap.Error(msg.err))
		return m, nil
	}

	m.browser.SetCollections(msg.collections)
	m.browser.SetNotice("", false)
	m.lastCollection = msg.collection
	m.layout()
	return m, nil
}

// shortName is a method's own name, without its package and service — the half
// of it worth suggesting as the name of a saved request.
func shortName(fullName string) string {
	if i := strings.LastIndex(fullName, "."); i >= 0 {
		return fullName[i+1:]
	}
	return fullName
}
