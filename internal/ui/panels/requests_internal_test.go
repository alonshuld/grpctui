package panels

import (
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"

	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// TestAgoWidthMatchesAgo is the price of having written the width down twice.
//
// [agoWidth] exists so that measuring the description column does not build a
// string per entry per frame, and the moment the two disagree the browser draws
// a box the wrong size — a column short, or a gap nobody asked for. Every
// boundary [ago] switches on is here, and both sides of each.
func TestAgoWidthMatchesAgo(t *testing.T) {
	durations := []time.Duration{
		-time.Hour,
		0,
		time.Second,
		59 * time.Second,
		time.Minute,
		9 * time.Minute,
		10 * time.Minute,
		59 * time.Minute,
		time.Hour,
		9 * time.Hour,
		23 * time.Hour,
		24 * time.Hour,
		47 * time.Hour,
		9 * 24 * time.Hour,
		10 * 24 * time.Hour,
		99 * 24 * time.Hour,
		365 * 24 * time.Hour,
		4000 * 24 * time.Hour,
	}

	for _, d := range durations {
		text := ago(d)
		assert.Equal(t, lipgloss.Width(text), agoWidth(d), "the width of %q (%s)", text, d)
	}
}

// TestDescribeWidthMatchesDescribe pins the other half of the same arithmetic:
// the separator between a method and its timestamp is two bytes wider than it
// is on screen, which is exactly the mistake len() would make.
func TestDescribeWidthMatchesDescribe(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	tests := map[string]requests.Request{
		"sent an hour ago": {
			Name:   "login",
			Method: "auth.v1.AuthService.Login",
			SentAt: now.Add(-time.Hour),
		},
		"never sent": {
			Name:   "login",
			Method: "auth.v1.AuthService.Login",
		},
		"a method name with wide runes": {
			Name:   "login",
			Method: "auth.v1.認証.Login",
			SentAt: now.Add(-3 * time.Minute),
		},
	}

	r := NewRequests(keys.Default(), styles.New())
	r.SetClock(func() time.Time { return now })

	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			e := newEntry(req, historySource)

			assert.Equal(t, lipgloss.Width(r.describe(req)), describeWidth(e, now))
		})
	}
}
