package grpcclient

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/stats"
)

// These tests are inside the package on purpose. The collector's arithmetic is
// worth pinning exactly, and doing it through a real call would mean comparing
// numbers that are a few microseconds of scheduling noise apart — an assertion
// that passes or fails by coin toss. Reaching the handler directly, with a
// clock that does not move on its own, makes every number in the breakdown a
// decision rather than a measurement.

// stepClock returns the given times in order, then repeats the last. gRPC
// stamps the two header events as it handles them, and those are the only
// clock reads a call makes.
func stepClock(base time.Time, steps ...time.Duration) func() time.Time {
	var i int
	return func() time.Time {
		d := steps[min(i, len(steps)-1)]
		i++
		return base.Add(d)
	}
}

func TestStatsHandlerBreaksDownACall(t *testing.T) {
	base := time.Now()
	h := statsHandler{now: stepClock(base, 2*time.Millisecond, 18*time.Millisecond)}

	ctx, collector := withTiming(context.Background())
	h.HandleRPC(ctx, &stats.Begin{BeginTime: base})
	h.HandleRPC(ctx, &stats.OutHeader{})
	h.HandleRPC(ctx, &stats.OutPayload{WireLength: 12})
	h.HandleRPC(ctx, &stats.InHeader{})
	h.HandleRPC(ctx, &stats.InPayload{WireLength: 34, RecvTime: base.Add(20 * time.Millisecond)})
	h.HandleRPC(ctx, &stats.End{BeginTime: base, EndTime: base.Add(24 * time.Millisecond)})

	got := collector.result()
	assert.Equal(t, 2*time.Millisecond, got.Connect, "the gap before the request headers went out")
	assert.Equal(t, 18*time.Millisecond, got.FirstByte, "to the response headers")
	assert.Equal(t, 24*time.Millisecond, got.Total)
	assert.Equal(t, 12, got.RequestBytes)
	assert.Equal(t, 34, got.ResponseBytes)
}

// A warm connection costs nothing to reach, which is what the breakdown exists
// to distinguish from a server that is slow to answer.
func TestStatsHandlerOnAWarmConnection(t *testing.T) {
	base := time.Now()
	h := statsHandler{now: stepClock(base, 0, 30*time.Millisecond)}

	ctx, collector := withTiming(context.Background())
	h.HandleRPC(ctx, &stats.Begin{BeginTime: base})
	h.HandleRPC(ctx, &stats.OutHeader{})
	h.HandleRPC(ctx, &stats.InHeader{})
	h.HandleRPC(ctx, &stats.End{BeginTime: base, EndTime: base.Add(31 * time.Millisecond)})

	got := collector.result()
	assert.Zero(t, got.Connect)
	assert.Equal(t, 30*time.Millisecond, got.FirstByte, "all of it was the server thinking")
}

// A trailers-only response — or a server that sends no headers of its own
// before its first message — leaves InHeader unseen, and the first byte is then
// the payload's.
func TestStatsHandlerFirstByteWithoutHeaders(t *testing.T) {
	base := time.Now()
	h := statsHandler{now: stepClock(base, time.Millisecond)}

	ctx, collector := withTiming(context.Background())
	h.HandleRPC(ctx, &stats.Begin{BeginTime: base})
	h.HandleRPC(ctx, &stats.OutHeader{})
	h.HandleRPC(ctx, &stats.InPayload{WireLength: 5, RecvTime: base.Add(9 * time.Millisecond)})
	h.HandleRPC(ctx, &stats.End{BeginTime: base, EndTime: base.Add(10 * time.Millisecond)})

	assert.Equal(t, 9*time.Millisecond, collector.result().FirstByte)
}

// A call the clock could not separate is still a measured call. Windows' timer
// is coarse enough to return the same instant for both ends of a call over a
// loopback transport, and deciding "measured" from the total would report those
// — the fastest calls there are — as having no timings at all.
func TestStatsHandlerZeroLengthCall(t *testing.T) {
	base := time.Now()
	h := statsHandler{now: stepClock(base, 0)}

	ctx, collector := withTiming(context.Background())
	h.HandleRPC(ctx, &stats.Begin{BeginTime: base})
	h.HandleRPC(ctx, &stats.OutHeader{})
	h.HandleRPC(ctx, &stats.InHeader{})
	h.HandleRPC(ctx, &stats.End{BeginTime: base, EndTime: base})

	got := collector.result()
	assert.True(t, got.Measured)
	assert.Zero(t, got.Total)
}

// A call that never reached the wire has no breakdown to give, and a
// half-filled struct must not escape as one.
func TestStatsHandlerIncompleteCall(t *testing.T) {
	base := time.Now()
	h := statsHandler{now: stepClock(base, time.Millisecond)}

	ctx, collector := withTiming(context.Background())
	h.HandleRPC(ctx, &stats.Begin{BeginTime: base})
	h.HandleRPC(ctx, &stats.OutHeader{})

	got := collector.result()
	require.False(t, got.Measured)
	assert.Equal(t, Timing{}, got, "nothing partial leaks out")
}

// Reflection and everything else that does not ask for a breakdown shares the
// handler, so a call with no collector on its context has to be a no-op rather
// than a panic.
func TestStatsHandlerIgnoresUntaggedCalls(t *testing.T) {
	h := statsHandler{}
	assert.NotPanics(t, func() {
		h.HandleRPC(context.Background(), &stats.Begin{BeginTime: time.Now()})
	})
}

func TestCallTimingResultOnNil(t *testing.T) {
	var collector *callTiming
	assert.Equal(t, Timing{}, collector.result())
}
