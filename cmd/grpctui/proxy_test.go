package main

import (
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

func TestParseFlags_Proxy(t *testing.T) {
	opts, err := parseFlags([]string{"-proxy", "127.0.0.1:8080", "localhost:50051"}, io.Discard)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:8080", opts.proxy)
	assert.Equal(t, "localhost:50051", opts.target)
}

func TestParseFlags_NoProxyByDefault(t *testing.T) {
	opts, err := parseFlags([]string{"localhost:50051"}, io.Discard)
	require.NoError(t, err)
	assert.Empty(t, opts.proxy, "passive mode is opt-in")
}

// The proxy has to bind before the TUI takes the terminal, so that a port
// already in use is an error the user can read rather than a line in a log file
// behind a full-screen UI.
func TestStartProxy(t *testing.T) {
	profile := grpcclient.Profile{Target: "localhost:50051"}

	watcher, stop, err := startProxy("127.0.0.1:0", profile, zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(stop)

	assert.NotEmpty(t, watcher.Addr())
	assert.NotEqual(t, "127.0.0.1:0", watcher.Addr(), "the bound port is reported, not the one asked for")

	// Something is actually listening there.
	conn, err := net.Dial("tcp", watcher.Addr())
	require.NoError(t, err)
	_ = conn.Close()
}

func TestStartProxyReportsAPortItCannotHave(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })

	_, _, err = startProxy(taken.Addr().String(),
		grpcclient.Profile{Target: "localhost:50051"}, zaptest.NewLogger(t))
	assert.Error(t, err)
}
