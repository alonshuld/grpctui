package grpcclient_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// A real call over a real server is the only way to know the stats handler is
// actually installed and reached — a unit test of the handler would pass
// against a client that never registers it.
func TestInvokeUnaryMeasuresTheCall(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	resp, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""), nil)
	require.NoError(t, err)

	timing := resp.Timing
	require.True(t, timing.Measured, "no breakdown was collected")

	t.Run("the parts fit inside the whole", func(t *testing.T) {
		assert.Positive(t, timing.Total)
		assert.GreaterOrEqual(t, timing.Total, timing.FirstByte)
		assert.GreaterOrEqual(t, timing.FirstByte, timing.Connect)
		assert.GreaterOrEqual(t, timing.Connect, time.Duration(0))
	})

	t.Run("the whole is no longer than the wall clock around it", func(t *testing.T) {
		assert.LessOrEqual(t, timing.Total, resp.Duration)
	})

	t.Run("counts the bytes on the wire", func(t *testing.T) {
		// Both sides carry gRPC's five-byte frame header even for an empty
		// message, so neither can legitimately be zero.
		assert.GreaterOrEqual(t, timing.RequestBytes, 5)
		assert.GreaterOrEqual(t, timing.ResponseBytes, 5)
	})
}

// A failed call has no response to hang a breakdown off, and the caller sees
// the failure rather than a Timing. What must not happen is a panic or a
// half-filled struct escaping, which is what the collector's begin/end guard is
// for.
func TestInvokeUnaryTimingOnAFailedCall(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := c.InvokeUnary(ctx, method, checkRequest(t, method, ""), nil)
	require.Error(t, err)
	assert.Nil(t, resp)
}

// A call so fast the clock cannot see it is still a measured call. Deciding
// otherwise from the total alone is what a coarse timer turns into "this
// response has no timings", on exactly the responses that came back quickest.
func TestTimingMeasuredIsNotTheTotal(t *testing.T) {
	assert.False(t, grpcclient.Timing{}.Measured)
	assert.True(t, grpcclient.Timing{Measured: true}.Measured)
}
