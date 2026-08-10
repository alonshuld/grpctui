// options.go holds the functional options New takes. Each one sets a field
// of Model; nothing here reads one back.

package ui

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/proxy"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
	"github.com/alonshuld/grpctui/internal/vars"
)

// Option configures a [Model].
type Option func(*Model)

// WithLogger attaches a logger.
func WithLogger(logger *zap.Logger) Option {
	return func(m *Model) {
		if logger != nil {
			m.logger = logger
		}
	}
}

// WithContext sets the parent context for RPCs the UI issues. Cancelling it
// aborts in-flight work.
func WithContext(ctx context.Context) Option {
	return func(m *Model) {
		if ctx != nil {
			m.ctx = ctx
		}
	}
}

// WithDiscoveryTimeout overrides [DefaultDiscoveryTimeout].
func WithDiscoveryTimeout(d time.Duration) Option {
	return func(m *Model) {
		if d > 0 {
			m.discoveryTimeout = d
		}
	}
}

// WithCallTimeout overrides [DefaultCallTimeout].
func WithCallTimeout(d time.Duration) Option {
	return func(m *Model) {
		if d > 0 {
			m.callTimeout = d
		}
	}
}

// WithClock replaces the clock the UI reads for a stream's elapsed time. A test
// that freezes it gets a stream panel whose every line is decided by the
// messages it was fed.
func WithClock(now func() time.Time) Option {
	return func(m *Model) {
		if now != nil {
			m.now = now
		}
	}
}

// WithDialer supplies the way to open a connection for a profile. Without one
// the profile switcher can list connections but not move between them, which is
// what a UI test with a hand-made client gets.
func WithDialer(d Dialer) Option {
	return func(m *Model) {
		if d != nil {
			m.dialer = d
		}
	}
}

// WithProfiles supplies the saved connections and says which one the client
// passed to [New] belongs to. The active profile's headers seed the metadata
// panel.
func WithProfiles(profiles []grpcclient.Profile, active int) Option {
	return func(m *Model) {
		m.profiles.SetProfiles(profiles, active)
		if p, ok := m.profiles.Active(); ok {
			m.metadata.SetHeaders(p.Metadata)
		}
	}
}

// WithEnvironments supplies the named variable sets and says which one to start
// in. A negative index — which is what a config file with no `environments` key
// yields — starts with nothing bound, and every {{variable}} reference is then
// text like any other.
func WithEnvironments(envs []vars.Environment, active int) Option {
	return func(m *Model) {
		m.environments.SetEnvironments(envs, active)
		m.installEnvironment()
	}
}

// WithHistory supplies the requests already sent. Without one the model keeps
// a session-only history: [ and ] still walk it, it is simply not written down.
func WithHistory(h requests.History) Option {
	return func(m *Model) { m.history = h }
}

// WithTraffic points the traffic panel at a running passive proxy: the events
// it reports, where it is listening, and a way to ask how many events it had to
// drop. Without it the traffic key explains that grpctui is not proxying.
func WithTraffic(events <-chan proxy.Event, listen string, dropped func() int) Option {
	return func(m *Model) {
		if events == nil {
			return
		}
		m.events = events
		m.dropped = dropped
		m.traffic.Enable(listen)
	}
}

// WithCollections supplies the saved requests. Without one the browser shows
// history alone and saving reports that there is nowhere to save to.
func WithCollections(c requests.Collections) Option {
	return func(m *Model) { m.browser.SetCollections(c) }
}

// WithRenderers sets the response renderers. Without it nothing is glossed,
// which is a response panel showing exactly the JSON the server sent — the
// behaviour every version before v0.9 had.
func WithRenderers(r render.Registry) Option {
	return func(m *Model) { m.renderers = r }
}

// WithThemes sets the palettes the switcher offers and which one to start in.
// active indexes themes; an out-of-range index falls back to the first, which
// is the built-in default.
func WithThemes(themes []styles.Theme, active int) Option {
	return func(m *Model) {
		if len(themes) == 0 {
			return
		}
		m.themes.SetThemes(themes, active)
		if theme, ok := m.themes.Active(); ok {
			m.applyTheme(theme)
		}
	}
}

// WithKeyMap replaces the keybindings, which is how the config file's
// remappings reach the UI.
//
// Every panel is handed the same map, so a remapped key works everywhere it
// worked before and the help bar documents it without being told — which is the
// whole reason internal/ui/keys exists as one struct.
func WithKeyMap(km keys.KeyMap) Option {
	return func(m *Model) {
		m.keys = km
		m.applyKeys(km)
	}
}
