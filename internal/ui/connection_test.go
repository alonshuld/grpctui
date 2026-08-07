package ui_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui"
	"github.com/alonshuld/grpctui/internal/ui/panels"
)

// fakeDialer stands in for the transport layer's Dial, handing out one client
// per profile so a test can tell which connection the model ended up on.
type fakeDialer struct {
	mu      sync.Mutex
	clients map[string]*fakeClient
	err     error
	dialed  []string
}

func (d *fakeDialer) Dial(profile grpcclient.Profile) (ui.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.dialed = append(d.dialed, profile.Name)
	if d.err != nil {
		return nil, d.err
	}

	if d.clients == nil {
		d.clients = make(map[string]*fakeClient)
	}
	if c, ok := d.clients[profile.Name]; ok {
		return c, nil
	}

	c := &fakeClient{target: profile.Target, services: testServices()}
	d.clients[profile.Name] = c
	return c, nil
}

func (d *fakeDialer) client(name string) *fakeClient {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clients[name]
}

func (d *fakeDialer) targets() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dialed...)
}

// connectionProfiles are two saved connections whose differences — transport
// security, credentials, headers — are the ones the UI has to keep straight.
func connectionProfiles() []grpcclient.Profile {
	return []grpcclient.Profile{
		{Name: "local", Target: "localhost:50051"},
		{
			Name:     "staging",
			Target:   "api.staging.example.com:443",
			Security: grpcclient.Security{TLS: true},
			Auth:     grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: "s3cr3t"},
			Metadata: grpcclient.Metadata{{Key: "x-tenant", Value: "acme"}},
		},
	}
}

// connected builds a model that has discovered its services, with profiles and
// a dialer wired up.
func connected(t *testing.T, client ui.Client, d *fakeDialer, profiles []grpcclient.Profile) ui.Model {
	t.Helper()

	m := ui.New(client,
		ui.WithLogger(zaptest.NewLogger(t)),
		ui.WithDialer(d),
		ui.WithProfiles(profiles, 0),
	)
	return settled(t, m)
}

// settleAll feeds back every message a command produces, and every message the
// commands those updates return produce, until the model is quiet. Switching
// connection is a chain of them: dial, then discover.
func settleAll(t *testing.T, m ui.Model, cmd tea.Cmd) ui.Model {
	t.Helper()

	for depth := 0; cmd != nil && depth < 10; depth++ {
		msgs := drain(cmd)
		cmd = nil

		for _, msg := range msgs {
			if _, isTick := msg.(spinner.TickMsg); isTick {
				continue
			}
			next, nextCmd := m.Update(msg)
			m = asModel(t, next)
			cmd = tea.Batch(cmd, nextCmd)
		}
	}
	return m
}

// addHeader types one header into the metadata panel, from wherever focus is.
func addHeader(t *testing.T, m ui.Model, key, value string) ui.Model {
	t.Helper()

	// tab twice from the tree: request, then headers.
	m, _ = press(t, m, "tab", "tab", "a")
	m, _ = press(t, m, strings.Split(key, "")...)
	m, _ = press(t, m, "enter", "l", "enter")
	m, _ = press(t, m, strings.Split(value, "")...)
	m, _ = press(t, m, "enter")
	return m
}

// Discovery is an RPC too: a server that gates its API behind a header gates
// its schema behind the same one.
func TestModel_HeadersReachDiscovery(t *testing.T) {
	client := healthyClient()
	m := ui.New(client,
		ui.WithLogger(zaptest.NewLogger(t)),
		ui.WithProfiles([]grpcclient.Profile{{
			Name:     "staging",
			Target:   "staging:443",
			Metadata: grpcclient.Metadata{{Key: "x-api-key", Value: "sesame"}},
		}}, 0),
	)

	m = settled(t, m)

	assert.Equal(t,
		grpcclient.Metadata{{Key: "x-api-key", Value: "sesame"}},
		client.headers(discoveryHeaders))
	assert.Contains(t, m.View(), "x-api-key")
}

func TestModel_HeadersReachTheCall(t *testing.T) {
	client := healthyClient()
	m := sized(t, ui.New(client,
		ui.WithLogger(zaptest.NewLogger(t)),
		ui.WithProfiles([]grpcclient.Profile{{
			Name:   "staging",
			Target: "staging:443",
			Metadata: grpcclient.Metadata{
				{Key: "x-tenant", Value: "acme"},
				{Key: "x-parked", Value: "nope", Disabled: true},
			},
		}}, 0),
	))
	m = asModel(t, mustUpdate(m, discoveryResult(t, m)))

	m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello
	m, cmd := press(t, m, "ctrl+s")
	m = apply(t, m, cmd)

	sent := client.headers(invocationHeaders)
	assert.Equal(t, grpcclient.Metadata{
		{Key: "x-tenant", Value: "acme"},
		{Key: "x-parked", Value: "nope", Disabled: true},
	}, sent, "the transport layer decides what a disabled header means")
	assert.Equal(t, int32(1), client.invocations.Load())
	assert.Contains(t, m.View(), "OK", "the call went through")
}

func TestModel_HeadersTypedIntoThePanelAreSent(t *testing.T) {
	client := healthyClient()
	d := &fakeDialer{}
	m := connected(t, client, d, connectionProfiles())

	m = addHeader(t, m, "x-tenant", "acme")

	// Back round the tab cycle to the tree, then out to a method.
	m, _ = press(t, m, "tab", "tab")
	m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello

	m, cmd := press(t, m, "ctrl+s")
	m = apply(t, m, cmd)

	assert.Equal(t, grpcclient.Metadata{{Key: "x-tenant", Value: "acme"}},
		client.headers(invocationHeaders))
	assert.Contains(t, m.View(), "OK", "the call went through")
}

// A malformed header is refused before the call, with the cursor put on the row
// that caused it — a panel capped at eight lines can have it scrolled away.
func TestModel_RefusesToSendAMalformedHeader(t *testing.T) {
	client := healthyClient()
	m := sized(t, ui.New(client,
		ui.WithLogger(zaptest.NewLogger(t)),
		ui.WithProfiles([]grpcclient.Profile{{
			Name:     "local",
			Target:   "localhost:50051",
			Metadata: grpcclient.Metadata{{Key: "x-ok", Value: "1"}, {Key: "grpc-timeout", Value: "1S"}},
		}}, 0),
	))
	m = asModel(t, mustUpdate(m, discoveryResult(t, m)))
	m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello

	m, cmd := press(t, m, "ctrl+s")
	m = apply(t, m, cmd)

	assert.Zero(t, client.invocations.Load(), "a call went out with an unsendable header")

	view := m.View()
	assert.Contains(t, view, "⚠")
	assert.Contains(t, view, "reserved")
	assert.Contains(t, view, "❯ ● grpc-timeout", "the cursor is on the offending header")
}

func TestModel_ProfileSwitcherOpensAndCloses(t *testing.T) {
	m := connected(t, healthyClient(), &fakeDialer{}, connectionProfiles())

	m, _ = press(t, m, "p")
	view := m.View()
	assert.Contains(t, view, "Connections")
	assert.Contains(t, view, "staging")
	assert.NotContains(t, view, "Services", "the switcher covers the body")

	m, _ = press(t, m, "esc")
	assert.Contains(t, m.View(), "Services")
}

// While the switcher is open it owns the keyboard: half-applying tab to the
// panels behind it would be a way to lose your place.
func TestModel_ProfileSwitcherIsModal(t *testing.T) {
	m := connected(t, healthyClient(), &fakeDialer{}, connectionProfiles())

	m, _ = press(t, m, "p", "tab", "tab")

	assert.Contains(t, m.View(), "Connections")
}

func TestModel_SwitchingProfileReconnectsAndRediscovers(t *testing.T) {
	first := healthyClient()
	d := &fakeDialer{}
	m := connected(t, first, d, connectionProfiles())

	m, cmd := press(t, m, "p", "j", "enter")
	m = settleAll(t, m, cmd)

	assert.Equal(t, []string{"staging"}, d.targets())

	staging := d.client("staging")
	require.NotNil(t, staging)
	assert.Equal(t, int32(1), staging.calls.Load(), "the new connection is rediscovered")

	view := m.View()
	assert.Contains(t, view, "api.staging.example.com:443")
	assert.Contains(t, view, "staging", "the status bar names the connection")
	assert.Contains(t, view, "TLS")
	assert.Contains(t, view, "bearer")
	assert.NotContains(t, view, "s3cr3t", "the token itself is never on screen")
}

// Headers belong to the connection, so switching replaces them rather than
// carrying an `authorization` across to a server that never issued it.
func TestModel_SwitchingProfileReplacesTheHeaders(t *testing.T) {
	d := &fakeDialer{}
	m := connected(t, healthyClient(), d, connectionProfiles())

	m = addHeader(t, m, "x-local", "1")
	require.Contains(t, m.View(), "x-local")

	m, cmd := press(t, m, "p", "j", "enter")
	m = settleAll(t, m, cmd)

	view := m.View()
	assert.NotContains(t, view, "x-local")
	assert.Contains(t, view, "x-tenant")

	assert.Equal(t, grpcclient.Metadata{{Key: "x-tenant", Value: "acme"}},
		d.client("staging").headers(discoveryHeaders))
}

// The connection being replaced is released; the one the model was handed
// belongs to whoever passed it in.
func TestModel_SwitchingProfileClosesTheConnectionItOpened(t *testing.T) {
	first := healthyClient()
	d := &fakeDialer{}
	m := connected(t, first, d, connectionProfiles())

	m, cmd := press(t, m, "p", "j", "enter")
	m = settleAll(t, m, cmd)
	assert.Zero(t, first.closed.Load(), "the model closed a connection it does not own")

	// Back to the first profile: the staging connection was the model's own, so
	// this time it is closed.
	m, cmd = press(t, m, "p", "k", "enter")
	m = settleAll(t, m, cmd)
	assert.Equal(t, int32(1), d.client("staging").closed.Load())

	require.NoError(t, m.Close())
	assert.Equal(t, int32(1), d.client("local").closed.Load(), "the final connection is released")
}

func TestModel_CloseLeavesABorrowedConnectionAlone(t *testing.T) {
	client := healthyClient()
	m := connected(t, client, &fakeDialer{}, connectionProfiles())

	require.NoError(t, m.Close())

	assert.Zero(t, client.closed.Load())
}

// A failed dial keeps the connection the user already had: taking it away
// because a second one could not be opened would turn a typo in a certificate
// path into a lost session.
func TestModel_AFailedSwitchKeepsTheOldConnection(t *testing.T) {
	client := healthyClient()
	d := &fakeDialer{err: errors.New("load client certificate: no such file")}
	m := connected(t, client, d, connectionProfiles())

	m, cmd := press(t, m, "p", "j", "enter")
	m = settleAll(t, m, cmd)

	view := m.View()
	assert.Contains(t, view, "load client certificate")
	assert.Contains(t, view, "staging", "the error names the connection that failed")

	// r retries against the connection that is still there.
	before := client.calls.Load()
	m, cmd = press(t, m, "r")
	m = apply(t, m, cmd)

	assert.Greater(t, client.calls.Load(), before)
	assert.Contains(t, m.View(), "Services")
}

func TestModel_WithoutADialerTheSwitcherCannotConnect(t *testing.T) {
	client := healthyClient()
	m := settled(t, ui.New(client,
		ui.WithLogger(zaptest.NewLogger(t)),
		ui.WithProfiles(connectionProfiles(), 0),
	))

	m, cmd := press(t, m, "p", "j", "enter")
	m = settleAll(t, m, cmd)

	// Nothing happened, and in particular nothing crashed: the model is still
	// on the connection it started with.
	assert.Contains(t, m.View(), "localhost:50051")
	assert.Contains(t, m.View(), "Services")
}

// selection drains a command and returns the profile choice it carried.
func selection(t *testing.T, cmd tea.Cmd) panels.ProfileSelectedMsg {
	t.Helper()

	for _, msg := range drain(cmd) {
		if selected, ok := msg.(panels.ProfileSelectedMsg); ok {
			return selected
		}
	}
	t.Fatal("no profile selection in the command")
	return panels.ProfileSelectedMsg{}
}

// Two quick choices leave two dials in flight, and only the later one's client
// may be kept — the earlier one arrives to find the user somewhere else.
func TestModel_AStaleConnectionIsDropped(t *testing.T) {
	d := &fakeDialer{}
	m := connected(t, healthyClient(), d, connectionProfiles())

	m, cmd := press(t, m, "p", "j", "enter")
	toStaging := selection(t, cmd)
	m, cmd = press(t, m, "p", "enter")
	toLocal := selection(t, cmd)

	// Both choices reach the model before either dial comes back.
	next, dialStaging := m.Update(toStaging)
	m = asModel(t, next)
	next, dialLocal := m.Update(toLocal)
	m = asModel(t, next)

	m = settleAll(t, m, dialLocal)
	m = settleAll(t, m, dialStaging)

	assert.Contains(t, m.View(), "localhost:50051")
	assert.Equal(t, int32(1), d.client("staging").closed.Load(),
		"the late connection is closed rather than installed")
}

func TestModel_StatusBarDescribesTheConnection(t *testing.T) {
	m := connected(t, healthyClient(), &fakeDialer{}, connectionProfiles())

	view := m.View()

	assert.Contains(t, view, "localhost:50051")
	assert.Contains(t, view, "local", "the profile is named when there is more than one")
	assert.Contains(t, view, "plaintext")
	assert.Contains(t, view, "2 services")
}

// An empty headers panel is three rows of border saying "nothing here", taken
// from the panel the user is actually reading.
func TestModel_TheHeadersPanelAppearsWhenItIsNeeded(t *testing.T) {
	m := connected(t, healthyClient(), &fakeDialer{}, connectionProfiles())

	assert.NotContains(t, m.View(), "Headers")

	// Tab reaches it, and it appears.
	m, _ = press(t, m, "tab", "tab")
	assert.Contains(t, m.View(), "Headers")

	// It stays once it holds something.
	m, _ = press(t, m, "a")
	m, _ = press(t, m, "x", "enter")
	m, _ = press(t, m, "tab")
	assert.Contains(t, m.View(), "Headers (1)")
}
