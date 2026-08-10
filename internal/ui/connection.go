// connection.go holds the connection lifecycle: the first reflection sweep,
// switching profile, and closing a client down.

package ui

import (
	"context"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/ui/panels"
)

// discover runs one reflection sweep off the Update goroutine.
//
// It carries the metadata panel's headers: discovery is an RPC like any other,
// so a server that gates its API behind a header gates its schema behind the
// same one.
func (m Model) discover() tea.Cmd {
	client, parent, timeout := m.client, m.ctx, m.discoveryTimeout

	// A reference nothing binds is left as written here rather than refusing the
	// sweep. Discovery is not something the user asked for at this moment, and a
	// connection that will not even list its services because a variable is
	// missing is a worse answer than a server that says Unauthenticated.
	md, err := m.headers()
	if err != nil {
		m.logger.Debug("discovering with unresolved headers", zap.Error(err))
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()

		services, err := client.ListServices(ctx, md)
		if err != nil {
			return discoveryFailedMsg{err: err}
		}
		return servicesDiscoveredMsg{services: services}
	}
}

// connect opens a connection for a profile off the Update goroutine. Dialling
// does no I/O of its own, but reading a CA bundle and a client key does, and
// Update is not the place for it.
func (m Model) connect(msg panels.ProfileSelectedMsg) tea.Cmd {
	dialer, seq := m.dialer, m.connSeq
	return func() tea.Msg {
		client, err := dialer.Dial(msg.Profile)
		return clientConnectedMsg{
			seq:     seq,
			index:   msg.Index,
			profile: msg.Profile,
			client:  client,
			err:     err,
		}
	}
}

// startConnect opens the connection a profile describes, tagged with a sequence
// number so that a dial the user has moved on from cannot install its client.
func (m *Model) startConnect(msg panels.ProfileSelectedMsg) tea.Cmd {
	if m.dialer == nil {
		m.logger.Warn("cannot switch connection: no dialer configured")
		return nil
	}

	m.retireCall()
	m.connSeq++
	m.state = stateConnecting
	m.err = nil
	m.pendingTarget = msg.Profile.Target

	m.logger.Info("connecting",
		zap.String("profile", msg.Profile.Label()),
		zap.String("target", msg.Profile.Target),
		zap.String("security", msg.Profile.Security.Mode()),
		zap.String("auth", msg.Profile.Auth.Describe()),
	)
	return tea.Batch(m.spinner.Tick, m.connect(msg))
}

// finishConnect installs a new client, or leaves the old connection alone and
// says why the new one could not be opened.
//
// A failed dial deliberately keeps the previous client: the user still has a
// working connection, and taking it away because a second one could not be
// opened would turn a typo in a certificate path into a lost session.
func (m Model) finishConnect(msg clientConnectedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.connSeq {
		m.logger.Debug("dropping stale connection", zap.Int("seq", msg.seq))
		if closer, ok := msg.client.(io.Closer); ok && msg.err == nil {
			_ = closer.Close()
		}
		return m, nil
	}

	m.pendingTarget = ""

	if msg.err != nil {
		m.state = stateFailed
		m.err = fmt.Errorf("connect to %s: %w", msg.profile.Label(), msg.err)
		m.logger.Error("connection failed",
			zap.String("profile", msg.profile.Label()),
			zap.Error(msg.err),
		)
		return m, nil
	}

	m.closeClient()
	m.client = msg.client
	m.ownsClient = true
	m.profiles.SetActive(msg.index)

	// The headers belong to the connection, so switching replaces them rather
	// than carrying the previous profile's `authorization` across to a server
	// that never issued it.
	m.metadata.SetHeaders(msg.profile.Metadata)

	m.services = nil
	m.tree.SetServices(nil)
	m.request.Clear()
	m.response.Clear()
	m.state = stateConnecting

	// History spans connections — it is a record of what you did, not of one
	// server — but the walk through it does not: the form has just been emptied,
	// so `[` starts again from the newest entry.
	m.historyAt = noHistory
	m.layout()

	return m, tea.Batch(m.spinner.Tick, m.discover())
}

// Close releases the connection the model holds, if the model opened it, along
// with any stream still running on it. The client [New] was given belongs to
// whoever passed it in.
//
// bubbletea has no teardown hook, so this is called on the final model that
// Run returns.
func (m Model) Close() error {
	if m.stream != nil {
		_ = m.stream.Close()
	}
	if !m.ownsClient {
		return nil
	}
	if closer, ok := m.client.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// closeClient drops the connection being replaced.
func (m *Model) closeClient() {
	if !m.ownsClient {
		return
	}
	if closer, ok := m.client.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			m.logger.Debug("closing the previous connection", zap.Error(err))
		}
	}
	m.ownsClient = false
}
