package render

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// The built-in renderer names, as a config file writes them.
const (
	// RendererTimestamp glosses a google.protobuf.Timestamp with how long ago
	// it was. An RFC 3339 string is exact and unreadable: "was that before or
	// after the deploy" is the question actually being asked of it, and no
	// amount of staring at a Z-suffixed string answers it.
	RendererTimestamp = "timestamp"

	// RendererDuration glosses a google.protobuf.Duration, which protobuf's
	// JSON mapping renders as a number of seconds with an "s" on the end —
	// so a timeout of an hour and a half reads as "5400s".
	RendererDuration = "duration"
)

// Builtins returns the renderers grpctui ships with, in the order they are
// listed to the user.
func Builtins() []Renderer {
	return []Renderer{timestampRenderer{}, durationRenderer{}}
}

// BuiltinNames lists the built-in renderers, for an error message that says
// what was allowed rather than only what was wrong.
func BuiltinNames() []string {
	names := make([]string, 0, len(Builtins()))
	for _, r := range Builtins() {
		names = append(names, r.Name())
	}
	return names
}

// Enabled returns the built-in renderers a config file leaves switched on.
//
// The default is on: a renderer that has to be discovered and enabled before it
// does anything is a renderer nobody ever sees. `enabled` therefore only ever
// takes one away, and a name in it that is not a renderer is an error rather
// than a silently ignored line — the failure mode of a config file typo is that
// nothing happens, which is indistinguishable from the feature not working.
func Enabled(enabled map[string]bool) ([]Renderer, error) {
	names := make([]string, 0, len(enabled))
	for name := range enabled {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		if !slices.Contains(BuiltinNames(), name) {
			return nil, fmt.Errorf("renderers: %q is not a renderer: have %s",
				name, strings.Join(BuiltinNames(), ", "))
		}
	}

	var out []Renderer
	for _, r := range Builtins() {
		if on, set := enabled[r.Name()]; !set || on {
			out = append(out, r)
		}
	}
	return out, nil
}

// timestampRenderer glosses a google.protobuf.Timestamp with how long ago it
// was.
type timestampRenderer struct{}

func (timestampRenderer) Name() string { return RendererTimestamp }

func (timestampRenderer) Types() []string { return []string{"google.protobuf.Timestamp"} }

func (timestampRenderer) Render(msg protoreflect.Message, now time.Time) (string, bool) {
	seconds, ok := field(msg, "seconds")
	if !ok {
		return "", false
	}
	nanos, _ := field(msg, "nanos")

	at := time.Unix(seconds, nanos).UTC()

	// The zero Timestamp is 1970, and describing it as "56 years ago" is worse
	// than useless: it is almost always an unset field that something wrote out
	// anyway, and saying so is the useful gloss.
	if seconds == 0 && nanos == 0 {
		return "the epoch — an unset timestamp, most likely", true
	}
	return relative(at, now), true
}

// relative describes how far a moment is from now, in the coarsest unit that
// still says something. Precision here is false comfort: nobody reading a
// response needs "3 hours, 14 minutes and 9 seconds ago".
func relative(at, now time.Time) string {
	d := now.Sub(at)
	if d < 0 {
		return "in " + coarse(-d)
	}
	return coarse(d) + " ago"
}

// coarse renders a positive duration as one unit.
func coarse(d time.Duration) string {
	switch {
	case d < time.Second:
		return "under a second"
	case d < time.Minute:
		return plural(int(d.Seconds()), "second")
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	case d < 365*24*time.Hour:
		return plural(int(d.Hours()/24), "day")
	default:
		return plural(int(d.Hours()/24/365), "year")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// durationRenderer glosses a google.protobuf.Duration with Go's own spelling of
// it, which is the one every developer reading a terminal already knows.
type durationRenderer struct{}

func (durationRenderer) Name() string { return RendererDuration }

func (durationRenderer) Types() []string { return []string{"google.protobuf.Duration"} }

func (durationRenderer) Render(msg protoreflect.Message, now time.Time) (string, bool) {
	_ = now

	seconds, ok := field(msg, "seconds")
	if !ok {
		return "", false
	}
	nanos, _ := field(msg, "nanos")

	// A duration long enough to overflow a time.Duration is 292 years, which is
	// a corrupt field rather than a value worth glossing.
	if seconds > math.MaxInt64/int64(time.Second) || seconds < math.MinInt64/int64(time.Second) {
		return "", false
	}
	return (time.Duration(seconds)*time.Second + time.Duration(nanos)).String(), true
}

// field reads a named int-like field off a well-known type.
//
// It goes through the descriptor rather than casting to *timestamppb.Timestamp
// because the message is a dynamicpb one built from whatever descriptors this
// session discovered: the type is google.protobuf.Timestamp by name, and is not
// the generated Go type. Reading the two fields it is defined to have is what
// works either way.
func field(msg protoreflect.Message, name string) (int64, bool) {
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return 0, false
	}

	switch fd.Kind() {
	case protoreflect.Int64Kind, protoreflect.Sfixed64Kind, protoreflect.Sint64Kind:
		return msg.Get(fd).Int(), true
	case protoreflect.Int32Kind, protoreflect.Sfixed32Kind, protoreflect.Sint32Kind:
		return msg.Get(fd).Int(), true
	default:
		return 0, false
	}
}
