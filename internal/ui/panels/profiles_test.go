package panels_test

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/panels"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

func testProfiles() []grpcclient.Profile {
	return []grpcclient.Profile{
		{Name: "local", Target: "localhost:50051"},
		{
			Name:     "staging",
			Target:   "api.staging.example.com:443",
			Security: grpcclient.Security{TLS: true, CACert: "/etc/ssl/ca.pem"},
			Auth:     grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: "s3cr3t"},
			Metadata: grpcclient.Metadata{{Key: "x-tenant", Value: "acme"}},
		},
		{
			Name:     "prod",
			Target:   "api.example.com:443",
			Security: grpcclient.Security{TLS: true, ClientCert: "c.pem", ClientKey: "k.pem"},
			Auth:     grpcclient.Auth{Kind: grpcclient.AuthBasic, Username: "alice", Password: "hunter2"},
		},
	}
}

func newProfiles(t *testing.T) panels.Profiles {
	t.Helper()

	p := panels.NewProfiles(keys.Default(), styles.New())
	p.SetSize(80, 20)
	p.SetProfiles(testProfiles(), 0)
	p.Open()
	return p
}

func pressProfiles(t *testing.T, p panels.Profiles, keystrokes ...string) (panels.Profiles, tea.Cmd) {
	t.Helper()

	var cmd tea.Cmd
	for _, k := range keystrokes {
		p, cmd = p.Update(keyMsg(k))
	}
	return p, cmd
}

// selectedProfile runs the command a choice produced and narrows the message.
func selectedProfile(t *testing.T, cmd tea.Cmd) panels.ProfileSelectedMsg {
	t.Helper()

	require.NotNil(t, cmd, "choosing a profile must emit a message")
	msg, ok := cmd().(panels.ProfileSelectedMsg)
	require.True(t, ok, "expected a ProfileSelectedMsg")
	return msg
}

func TestProfiles_ListsEveryConnection(t *testing.T) {
	p := newProfiles(t)

	view := p.View()

	assert.Contains(t, view, "local")
	assert.Contains(t, view, "staging")
	assert.Contains(t, view, "prod")
	assert.Contains(t, view, "api.staging.example.com:443")
}

// A terminal is shared over a screen share more often than a config file is.
func TestProfiles_NeverRendersACredential(t *testing.T) {
	p := newProfiles(t)

	view := p.View()

	assert.NotContains(t, view, "s3cr3t")
	assert.NotContains(t, view, "hunter2")
	assert.Contains(t, view, "bearer")
	assert.Contains(t, view, "basic (alice)")
}

func TestProfiles_ShowsHowEachConnectionIsProtected(t *testing.T) {
	p := newProfiles(t)

	view := p.View()

	assert.Contains(t, view, "plaintext")
	assert.Contains(t, view, "TLS")
	assert.Contains(t, view, "mTLS")
	assert.Contains(t, view, "1 header")
}

func TestProfiles_ChoosingEmitsTheProfile(t *testing.T) {
	p := newProfiles(t)

	p, cmd := pressProfiles(t, p, "j", "enter")

	msg := selectedProfile(t, cmd)
	assert.Equal(t, 1, msg.Index)
	assert.Equal(t, "staging", msg.Profile.Name)
	assert.False(t, p.Opened(), "choosing closes the switcher")
}

// Picking the connection already in use is not a no-op: reconnecting is how a
// user recovers a connection the server dropped.
func TestProfiles_ChoosingTheActiveProfileStillConnects(t *testing.T) {
	p := newProfiles(t)

	_, cmd := pressProfiles(t, p, "enter")

	assert.Equal(t, 0, selectedProfile(t, cmd).Index)
}

func TestProfiles_EscapeClosesWithoutChoosing(t *testing.T) {
	p := newProfiles(t)

	p, cmd := pressProfiles(t, p, "j", "esc")

	assert.Nil(t, cmd)
	assert.False(t, p.Opened())
}

func TestProfiles_TheOpenKeyAlsoCloses(t *testing.T) {
	p := newProfiles(t)

	p, cmd := pressProfiles(t, p, "p")

	assert.Nil(t, cmd)
	assert.False(t, p.Opened())
}

// Reopening starts from the connected profile rather than from wherever the
// cursor was left, so the list always says where you are before where you were.
func TestProfiles_OpensOnTheActiveProfile(t *testing.T) {
	p := newProfiles(t)
	p.SetActive(2)

	p, _ = pressProfiles(t, p, "k", "esc")
	p.Open()

	_, cmd := pressProfiles(t, p, "enter")
	assert.Equal(t, 2, selectedProfile(t, cmd).Index)
}

func TestProfiles_CursorStopsAtTheEnds(t *testing.T) {
	p := newProfiles(t)

	p, _ = pressProfiles(t, p, "k", "k")
	_, cmd := pressProfiles(t, p, "enter")
	assert.Equal(t, 0, selectedProfile(t, cmd).Index)

	p.Open()
	p, _ = pressProfiles(t, p, "G", "j")
	_, cmd = pressProfiles(t, p, "enter")
	assert.Equal(t, 2, selectedProfile(t, cmd).Index)
}

func TestProfiles_IgnoresKeysWhileClosed(t *testing.T) {
	p := newProfiles(t)
	p.Close()

	p, cmd := pressProfiles(t, p, "j", "enter")

	assert.Nil(t, cmd)
	assert.False(t, p.Opened())
}

func TestProfiles_WithNothingToSwitchTo(t *testing.T) {
	p := panels.NewProfiles(keys.Default(), styles.New())
	p.SetSize(80, 20)

	p.Open()

	assert.False(t, p.Opened(), "there is nothing to choose between")
	assert.Equal(t, 0, p.Len())

	_, ok := p.Active()
	assert.False(t, ok)
}

func TestProfiles_Active(t *testing.T) {
	p := newProfiles(t)
	p.SetActive(1)

	active, ok := p.Active()

	require.True(t, ok)
	assert.Equal(t, "staging", active.Name)
}

func TestProfiles_ScrollsALongList(t *testing.T) {
	p := panels.NewProfiles(keys.Default(), styles.New())
	p.SetSize(80, 6)

	many := make([]grpcclient.Profile, 0, 12)
	for i := range 12 {
		many = append(many, grpcclient.Profile{
			Name:   string(rune('a'+i)) + "-env",
			Target: "host:443",
		})
	}
	p.SetProfiles(many, 0)
	p.Open()

	require.Contains(t, p.View(), "a-env")

	p, _ = pressProfiles(t, p, "G")

	view := p.View()
	assert.Contains(t, view, "l-env")
	assert.NotContains(t, view, "a-env")
}
