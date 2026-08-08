package render_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/render"
)

// clock is the fixed "now" every relative gloss is measured against, so that a
// test asserting "3 minutes ago" still says it in a year.
var clock = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// registry builds one over the built-ins.
func registry(t *testing.T) render.Registry {
	t.Helper()

	r, err := render.New(render.Builtins()...)
	require.NoError(t, err)
	return r
}

// annotate renders msg the way the response panel does and glosses it, which is
// the contract [render.Registry.Annotate] is written against: the body must be
// the JSON rendering of the message it is given.
func annotate(t *testing.T, r render.Registry, msg proto.Message) (string, []render.Annotation) {
	t.Helper()

	body, err := protoschema.MarshalJSON(msg)
	require.NoError(t, err)
	return body, r.Annotate(msg, body, clock)
}

// lineOf returns the 1-based line of body holding want.
//
// Asserting a note's line against the *text* on that line — rather than against
// a number counted by hand — is what keeps these tests from breaking every time
// the JSON layout shifts by a row.
func lineOf(t *testing.T, body, want string) int {
	t.Helper()

	at := 0
	for i, line := range strings.Split(body, "\n") {
		if strings.Contains(line, want) {
			require.Zero(t, at, "%q appears on more than one line of\n%s", want, body)
			at = i + 1
		}
	}
	require.NotZero(t, at, "%q is on no line of\n%s", want, body)
	return at
}

func TestRegistry_Annotate_Timestamp(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		at   time.Time
		want string
	}{
		"minutes ago":   {at: clock.Add(-3 * time.Minute), want: "3 minutes ago"},
		"one hour ago":  {at: clock.Add(-time.Hour), want: "1 hour ago"},
		"days ago":      {at: clock.Add(-50 * time.Hour), want: "2 days ago"},
		"years ago":     {at: clock.AddDate(-3, 0, 0), want: "3 years ago"},
		"in the future": {at: clock.Add(90 * time.Second), want: "in 1 minute"},
		"just now":      {at: clock, want: "under a second ago"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			msg := message(t, "Item")
			set(t, msg, "at", timestamppb.New(tc.at))

			body, notes := annotate(t, registry(t), msg)
			require.Len(t, notes, 1)
			assert.Equal(t, tc.want, notes[0].Text)
			assert.Equal(t, lineOf(t, body, `"at"`), notes[0].Line)
		})
	}
}

// TestRegistry_Annotate_Epoch pins the one timestamp worth describing rather
// than dating: a zero value that something wrote out anyway.
func TestRegistry_Annotate_Epoch(t *testing.T) {
	t.Parallel()

	msg := message(t, "Item")
	set(t, msg, "at", &timestamppb.Timestamp{})

	_, notes := annotate(t, registry(t), msg)
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0].Text, "unset timestamp")
}

func TestRegistry_Annotate_Duration(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		d    time.Duration
		want string
	}{
		"an hour and a half": {d: 90 * time.Minute, want: "1h30m0s"},
		"sub-second":         {d: 250 * time.Millisecond, want: "250ms"},
		"zero":               {d: 0, want: "0s"},
		"negative":           {d: -30 * time.Second, want: "-30s"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			msg := message(t, "Item")
			set(t, msg, "ttl", durationpb.New(tc.d))

			body, notes := annotate(t, registry(t), msg)
			require.Len(t, notes, 1)
			assert.Equal(t, tc.want, notes[0].Text)
			assert.Equal(t, lineOf(t, body, `"ttl"`), notes[0].Line)
		})
	}
}

// TestRegistry_Annotate_Nested walks a message with glossable fields at several
// depths, in a list and in a map — which is the whole reason the glosses are
// found by descriptor rather than by field name.
func TestRegistry_Annotate_Nested(t *testing.T) {
	t.Parallel()

	inner := message(t, "Inner")
	set(t, inner, "updated_at", timestamppb.New(clock.Add(-time.Hour)))

	first := message(t, "Inner")
	set(t, first, "updated_at", timestamppb.New(clock.Add(-2*time.Hour)))
	second := message(t, "Inner")
	set(t, second, "updated_at", timestamppb.New(clock.Add(-3*time.Hour)))

	keyed := message(t, "Inner")
	set(t, keyed, "updated_at", timestamppb.New(clock.Add(-4*time.Hour)))

	msg := message(t, "Item")
	set(t, msg, "created_at", timestamppb.New(clock.Add(-3*time.Minute)))
	set(t, msg, "ttl", durationpb.New(time.Minute))
	set(t, msg, "inner", inner)
	appendItem(t, msg, "items", first)
	appendItem(t, msg, "items", second)
	putEntry(t, msg, "by_key", "one", keyed)

	body, notes := annotate(t, registry(t), msg)

	byLine := make(map[int]string, len(notes))
	for _, n := range notes {
		byLine[n.Line] = n.Text
	}

	// One for created_at, one for ttl, one inside inner, one per list item, one
	// inside the map entry: six fields, six glosses, each on its own line.
	require.Len(t, notes, 6)
	assert.Equal(t, "3 minutes ago", byLine[lineOf(t, body, `"createdAt"`)])
	assert.Equal(t, "1m0s", byLine[lineOf(t, body, `"ttl"`)])

	texts := make([]string, 0, len(notes))
	for _, n := range notes {
		texts = append(texts, n.Text)
	}
	assert.ElementsMatch(t,
		[]string{"3 minutes ago", "1m0s", "1 hour ago", "2 hours ago", "3 hours ago", "4 hours ago"},
		texts)

	// The notes come back in line order, which is what lets a caller walk them
	// alongside the body.
	for i := 1; i < len(notes); i++ {
		assert.LessOrEqual(t, notes[i-1].Line, notes[i].Line)
	}
}

// TestRegistry_Annotate_LineNumbersAreDistinct is the invariant that matters
// most: every gloss lands on the line of the field it describes. A walk that
// drifted by one would put "3 minutes ago" beside an unrelated value, which is
// worse than saying nothing.
func TestRegistry_Annotate_LineNumbersAreDistinct(t *testing.T) {
	t.Parallel()

	msg := message(t, "Item")
	set(t, msg, "at", timestamppb.New(clock.Add(-time.Minute)))
	set(t, msg, "created_at", timestamppb.New(clock.Add(-time.Hour)))
	set(t, msg, "ttl", durationpb.New(time.Second))

	body, notes := annotate(t, registry(t), msg)
	require.Len(t, notes, 3)

	seen := make(map[int]bool, len(notes))
	lines := strings.Split(body, "\n")
	for _, n := range notes {
		require.False(t, seen[n.Line], "two glosses on line %d", n.Line)
		seen[n.Line] = true
		require.GreaterOrEqual(t, n.Line, 1)
		require.LessOrEqual(t, n.Line, len(lines))
	}

	assert.Contains(t, lines[notes[0].Line-1], `"at"`)
	assert.Contains(t, lines[byText(notes, "1 hour ago")-1], `"createdAt"`)
	assert.Contains(t, lines[byText(notes, "1s")-1], `"ttl"`)
}

func byText(notes []render.Annotation, text string) int {
	for _, n := range notes {
		if n.Text == text {
			return n.Line
		}
	}
	return 0
}

func TestRegistry_Annotate_NothingToSay(t *testing.T) {
	t.Parallel()

	r := registry(t)

	t.Run("a message with no glossable field", func(t *testing.T) {
		t.Parallel()

		msg := message(t, "Item")
		msg.Set(msg.Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("ada"))

		_, notes := annotate(t, r, msg)
		assert.Empty(t, notes)
	})

	t.Run("an empty registry", func(t *testing.T) {
		t.Parallel()

		msg := message(t, "Item")
		set(t, msg, "at", timestamppb.New(clock))

		var empty render.Registry
		assert.True(t, empty.Empty())
		assert.Nil(t, empty.Annotate(msg, "{}", clock))
	})

	t.Run("a nil message", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, r.Annotate(nil, "{}", clock))
	})

	t.Run("an empty body", func(t *testing.T) {
		t.Parallel()

		msg := message(t, "Item")
		set(t, msg, "at", timestamppb.New(clock))
		assert.Nil(t, r.Annotate(msg, "", clock))
	})

	t.Run("a body that is not JSON", func(t *testing.T) {
		t.Parallel()

		// protobuf's text format, which protoschema.Marshal falls back to. There
		// is nothing wrong here — there is simply nothing to attach a gloss to.
		msg := message(t, "Item")
		set(t, msg, "at", timestamppb.New(clock))
		assert.Empty(t, r.Annotate(msg, "at: {seconds: 1}", clock))
	})
}

func TestNew_DuplicateTypes(t *testing.T) {
	t.Parallel()

	_, err := render.New(render.Builtins()[0], render.Builtins()[0])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "google.protobuf.Timestamp")
}

func TestNew_Names(t *testing.T) {
	t.Parallel()

	r := registry(t)
	assert.Equal(t, []string{render.RendererTimestamp, render.RendererDuration}, r.Names())
	assert.False(t, r.Empty())
}

func TestEnabled(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		enabled map[string]bool
		want    []string
	}{
		"nothing configured leaves every renderer on": {
			enabled: nil,
			want:    []string{render.RendererTimestamp, render.RendererDuration},
		},
		"one switched off": {
			enabled: map[string]bool{render.RendererTimestamp: false},
			want:    []string{render.RendererDuration},
		},
		"one switched on explicitly changes nothing": {
			enabled: map[string]bool{render.RendererTimestamp: true},
			want:    []string{render.RendererTimestamp, render.RendererDuration},
		},
		"all switched off": {
			enabled: map[string]bool{
				render.RendererTimestamp: false,
				render.RendererDuration:  false,
			},
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			enabled, err := render.Enabled(tc.enabled)
			require.NoError(t, err)

			var names []string
			for _, r := range enabled {
				names = append(names, r.Name())
			}
			assert.Equal(t, tc.want, names)
		})
	}
}

// TestEnabled_UnknownName pins that a typo is an error. The failure mode of a
// silently ignored line is that nothing happens, which is indistinguishable
// from the feature not working.
func TestEnabled_UnknownName(t *testing.T) {
	t.Parallel()

	_, err := render.Enabled(map[string]bool{"clock": false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clock")
	assert.Contains(t, err.Error(), render.RendererTimestamp)
}

// TestRenderer_Types pins the type names, which are the contract between a
// renderer and every response it will ever see.
func TestRenderer_Types(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{render.RendererTimestamp, render.RendererDuration}, render.BuiltinNames())

	var types []string
	for _, r := range render.Builtins() {
		types = append(types, r.Types()...)
	}
	assert.Equal(t, []string{"google.protobuf.Timestamp", "google.protobuf.Duration"}, types)
}

// structRenderer is a renderer written by a test, which is the extension point
// the whole package exists to offer.
type structRenderer struct{}

func (structRenderer) Name() string    { return "struct" }
func (structRenderer) Types() []string { return []string{"google.protobuf.Struct"} }

func (structRenderer) Render(msg protoreflect.Message, _ time.Time) (string, bool) {
	fields := msg.Descriptor().Fields().ByName("fields")
	if fields == nil {
		return "", false
	}
	return fmt.Sprintf("a struct with %d fields", msg.Get(fields).Map().Len()), true
}

// TestRegistry_CustomRenderer pins that registering a renderer is all it takes,
// and that a claimed type is glossed rather than descended into.
func TestRegistry_CustomRenderer(t *testing.T) {
	t.Parallel()

	r, err := render.New(structRenderer{})
	require.NoError(t, err)

	payload, err := structpb.NewStruct(map[string]any{"name": "ada", "id": 7.0})
	require.NoError(t, err)

	msg := message(t, "Item")
	// A timestamp too, to prove a registry without the timestamp renderer leaves
	// one entirely alone.
	set(t, msg, "at", timestamppb.New(clock))
	set(t, msg, "payload", payload)

	body, notes := annotate(t, r, msg)
	require.Len(t, notes, 1)
	assert.Equal(t, "a struct with 2 fields", notes[0].Text)
	assert.Equal(t, lineOf(t, body, `"payload"`), notes[0].Line)
}
