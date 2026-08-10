// proxy.go starts passive mode's listener, for a session watching somebody
// else's traffic.

package main

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/proxy"
)

// startProxy binds the passive proxy and starts serving, returning it and the
// way to shut it down.
//
// The connection it forwards over carries the profile's *security* and none of
// its credentials. A passive proxy exists to show what an application is
// sending, and one that quietly added grpctui's own bearer token would be
// showing the user something the application never sent — which is the one
// thing a wire-watching tool may not do.
//
// It is pinned to the target grpctui started on. Switching connection or
// environment in the UI moves the calls the user makes; where the proxy
// forwards to is decided once, because an application pointed at it has no way
// to know that its destination changed underneath it.
func startProxy(listen string, p grpcclient.Profile, logger *zap.Logger) (*proxy.Proxy, func(), error) {
	upstream, err := grpcclient.DialProfile(grpcclient.Profile{
		Target:   p.Target,
		Security: p.Security,
	}, grpcclient.WithLogger(logger))
	if err != nil {
		return nil, nil, fmt.Errorf("proxy upstream: %w", err)
	}

	watcher := proxy.New(listen, upstream.Conn(), proxy.WithLogger(logger))
	if err := watcher.Listen(); err != nil {
		_ = upstream.Close()
		return nil, nil, err
	}

	// Serving runs for the life of the program. Its error is logged rather than
	// returned: by the time it happens the TUI owns the terminal, and a proxy
	// that has stopped is not a reason to take the session down — the traffic
	// panel simply stops filling.
	go func() {
		if err := watcher.Serve(); err != nil {
			logger.Error("the proxy stopped", zap.Error(err))
		}
	}()

	logger.Info("proxying",
		zap.String("listen", watcher.Addr()),
		zap.String("upstream", p.Target),
		zap.String("security", p.Security.Mode()),
	)

	return watcher, func() {
		watcher.Stop()
		_ = upstream.Close()
	}, nil
}
