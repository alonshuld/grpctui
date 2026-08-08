package ui_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/teatest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/ui"
	"github.com/alonshuld/grpctui/internal/vars"
)

// testEnvironments are the variable sets the v0.7 tests switch between: two
// that point somewhere, and one that only carries values.
func testEnvironments() []vars.Environment {
	return []vars.Environment{
		{
			Name:   "dev",
			Target: "localhost:50051",
			Variables: []vars.Variable{
				{Name: "tenant", Value: "local"},
				{Name: "who", Value: "alice"},
			},
		},
		{
			Name:   "staging",
			Target: "api.staging.example.com:443",
			Variables: []vars.Variable{
				{Name: "tenant", Value: "acme"},
				{Name: "who", Value: "bob"},
			},
		},
		{Name: "values only", Variables: []vars.Variable{{Name: "who", Value: "carol"}}},
	}
}

// varsModel is a settled model with the test environments installed.
func varsModel(t *testing.T, client ui.Client, opts ...ui.Option) ui.Model {
	t.Helper()

	return settled(t, newModel(t, client, append([]ui.Option{
		ui.WithClock(goldenNow),
		ui.WithProfiles(goldenProfiles(), 0),
		ui.WithEnvironments(testEnvironments(), 0),
	}, opts...)...))
}

// fillName selects SayHello and types text into its `name` field.
func fillName(t *testing.T, m ui.Model, text string) ui.Model {
	t.Helper()

	m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello
	m, _ = press(t, m, "enter")
	m = typeText(t, m, text)
	m, _ = press(t, m, "esc")
	return m
}

// sentRequest sends the form and returns the message the transport was handed.
func sentRequest(t *testing.T, m ui.Model, client *fakeClient) (ui.Model, proto.Message) {
	t.Helper()

	var got proto.Message
	client.invoke = func(_ context.Context, method grpcclient.Method, req proto.Message) (*grpcclient.UnaryResponse, error) {
		got = req
		return &grpcclient.UnaryResponse{
			Message:  reply(method, map[string]any{"greeting": "hi", "count": int32(3)}),
			Duration: time.Millisecond,
		}, nil
	}

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd, "ctrl+s must start a call")
	m = applyChain(t, m, cmd)

	require.NotNil(t, got, "the call never reached the transport layer")
	return m, got
}

// --- interpolation ----------------------------------------------------------

func TestModel_SendResolvesReferencesInTheBody(t *testing.T) {
	client := healthyClient()

	m := fillName(t, varsModel(t, client), "hello {{who}}")
	_, req := sentRequest(t, m, client)

	assert.Equal(t, "hello alice", requestValue(t, req, "name").String())
}

func TestModel_SendResolvesReferencesInTheHeaders(t *testing.T) {
	client := healthyClient()

	m := varsModel(t, client)
	m = fillName(t, m, "world")

	// tab from the form to the headers, then edit the value of the first one.
	m, _ = press(t, m, "tab")
	m, _ = press(t, m, "l", "enter")
	m = clearField(t, m)
	m = typeText(t, m, "{{tenant}}")
	m, _ = press(t, m, "esc")

	sentRequest(t, m, client)

	sent := client.headers(invocationHeaders)
	require.NotEmpty(t, sent)
	assert.Equal(t, "local", sent[0].Value)
}

// An `authorization: Bearer {{token}}` that goes out literally comes back
// Unauthenticated, which is the most misleading answer the server could give.
func TestModel_SendRefusesAnUnboundHeaderReference(t *testing.T) {
	client := healthyClient()

	m := varsModel(t, client)
	m = fillName(t, m, "world")

	m, _ = press(t, m, "tab")
	m, _ = press(t, m, "l", "enter")
	m = clearField(t, m)
	m = typeText(t, m, "{{nobody_bound_this}}")
	m, _ = press(t, m, "esc")

	before := client.invocations.Load()
	m, cmd := press(t, m, "ctrl+s")
	assert.Nil(t, cmd, "the call must not go out")
	assert.Equal(t, before, client.invocations.Load())
	assert.Contains(t, m.View(), "nobody_bound_this")
}

// A reference nothing binds lands on the row that made it, not on the panel.
func TestModel_SendRefusesAnUnboundFieldReference(t *testing.T) {
	client := healthyClient()

	m := fillName(t, varsModel(t, client), "{{nobody_bound_this}}")

	before := client.invocations.Load()
	m, cmd := press(t, m, "ctrl+s")
	assert.Nil(t, cmd)
	assert.Equal(t, before, client.invocations.Load())
	assert.Contains(t, m.View(), "nobody_bound_this")
}

// A reference means a variable whether or not any environment is configured:
// with none, v is still how you bind one, and sending the braces as written
// would be a request nobody meant.
func TestModel_WithoutEnvironmentsAReferenceStillNeedsBinding(t *testing.T) {
	client := healthyClient()

	m := settled(t, newModel(t, client, ui.WithClock(goldenNow)))
	m = fillName(t, m, "{{who}}")

	m, cmd := press(t, m, "ctrl+s")
	assert.Nil(t, cmd)
	assert.Contains(t, m.View(), "{{who}}")

	m, _ = press(t, m, "v", "a")
	m = typeText(t, m, "who=alice")
	m, cmd = press(t, m, "enter")
	m = applyChain(t, m, cmd)
	m, _ = press(t, m, "esc")

	_, req := sentRequest(t, m, client)
	assert.Equal(t, "alice", requestValue(t, req, "name").String())
}

// --- environments -----------------------------------------------------------

func TestModel_SwitchEnvironmentChangesWhatARequestMeans(t *testing.T) {
	client := healthyClient()

	m := fillName(t, varsModel(t, client), "hello {{who}}")

	m, req := sentRequest(t, m, client)
	require.Equal(t, "hello alice", requestValue(t, req, "name").String())

	m = switchTo(t, m, 2) // "values only", which has no target
	_, req = sentRequest(t, m, client)
	assert.Equal(t, "hello carol", requestValue(t, req, "name").String())
}

// "staging" usually means both a different host and a different account id, and
// having to switch those separately is how a request meant for staging reaches
// production.
func TestModel_SwitchEnvironmentFollowsItsTarget(t *testing.T) {
	client := healthyClient()
	next := healthyClient()
	next.target = "api.staging.example.com:443"

	var dialled grpcclient.Profile
	m := varsModel(t, client, ui.WithDialer(ui.DialerFunc(
		func(p grpcclient.Profile) (ui.Client, error) {
			dialled = p
			return next, nil
		})))

	m, cmd := press(t, m, "e", "j", "enter")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)

	assert.Equal(t, "api.staging.example.com:443", dialled.Target)
	assert.Equal(t, "local", dialled.Name, "the profile decides how to connect, the environment where")
	assert.Contains(t, m.View(), "api.staging.example.com:443")
}

func TestModel_SwitchEnvironmentStaysPutWhenItNamesNoTarget(t *testing.T) {
	client := healthyClient()

	dials := 0
	m := varsModel(t, client, ui.WithDialer(ui.DialerFunc(
		func(grpcclient.Profile) (ui.Client, error) {
			dials++
			return client, nil
		})))

	m, cmd := press(t, m, "e", "G", "enter") // "values only"
	m = applyChain(t, m, cmd)

	assert.Zero(t, dials)
	assert.Contains(t, m.View(), "localhost:50051")
}

// Which environment a request means is worth seeing at a glance; what it binds
// is not something to put on a screen somebody might be sharing.
func TestModel_StatusBarNamesTheEnvironmentAndNotItsValues(t *testing.T) {
	view := varsModel(t, healthyClient()).View()

	assert.Contains(t, view, "dev")
	assert.Contains(t, view, "2 vars")
	assert.NotContains(t, view, "alice", "what a variable is bound to is not a status-bar segment")
}

// --- capture ----------------------------------------------------------------

func TestModel_CaptureFromAResponse(t *testing.T) {
	client := healthyClient()

	m := fillName(t, varsModel(t, client), "world")
	m, _ = sentRequest(t, m, client)

	m, cmd := press(t, m, "ctrl+p")
	require.Nil(t, cmd)

	m = typeText(t, m, "greeting=greeting")
	m, cmd = press(t, m, "enter")
	m = applyChain(t, m, cmd)

	// The captured value is bound, and marked as having come from a response.
	assert.Contains(t, m.View(), "greeting")
	assert.Contains(t, m.View(), "hi")
	assert.Contains(t, m.View(), "captured")

	// And the next request can refer to it.
	m, _ = press(t, m, "esc")
	m = retypeName(t, m, "{{greeting}} again")

	_, req := sentRequest(t, m, client)
	assert.Equal(t, "hi again", requestValue(t, req, "name").String())
}

// retypeName replaces what is in the `name` field of a form already on screen,
// with the cursor on that row.
func retypeName(t *testing.T, m ui.Model, text string) ui.Model {
	t.Helper()

	m, _ = press(t, m, "enter")
	m = clearField(t, m)
	m = typeText(t, m, text)
	m, _ = press(t, m, "esc")
	return m
}

func TestModel_CaptureRefusals(t *testing.T) {
	t.Run("with no response yet", func(t *testing.T) {
		m := varsModel(t, healthyClient())

		m, _ = press(t, m, "ctrl+p")
		assert.Contains(t, m.View(), "no response to capture from")
	})

	t.Run("a path the response does not have", func(t *testing.T) {
		client := healthyClient()

		m := fillName(t, varsModel(t, client), "world")
		m, _ = sentRequest(t, m, client)

		m, _ = press(t, m, "ctrl+p")
		m = typeText(t, m, "id=user.id")

		m, cmd := press(t, m, "enter")
		m = applyChain(t, m, cmd)

		assert.Contains(t, m.View(), `no "user"`)
		assert.Contains(t, m.View(), "Capture from the response",
			"the prompt stays up so the path can be corrected")
	})
}

// A capture belongs to the environment it was made in. Carrying it into
// production would be the most expensive thing this feature could do.
func TestModel_SwitchingEnvironmentDropsCaptures(t *testing.T) {
	client := healthyClient()

	m := fillName(t, varsModel(t, client), "world")
	m, _ = sentRequest(t, m, client)

	m, _ = press(t, m, "ctrl+p")
	m = typeText(t, m, "greeting=greeting")
	m, cmd := press(t, m, "enter")
	m = applyChain(t, m, cmd)
	m, _ = press(t, m, "esc")

	m = switchTo(t, m, 2)

	m, _ = press(t, m, "v")
	assert.NotContains(t, m.View(), "captured")
	assert.Contains(t, m.View(), "Variables · values only (1)")
}

// --- what a record keeps ----------------------------------------------------

// A history file sits in the state directory for weeks and a collection is
// meant to be committed, so what goes in either is the reference rather than
// what it expanded to — which is also the only way a captured token stays off
// the disk.
func TestModel_RecordsTheReferenceAndNotItsValue(t *testing.T) {
	client := healthyClient()
	history, path := historyIn(t)

	m := varsModel(t, client, ui.WithHistory(history))
	m = fillName(t, m, "hello {{who}}")

	// `times` is an int32, which is a reference protobuf's JSON mapping cannot
	// hold — so it lands in `values` beside a zero rather than in the body.
	m, _ = press(t, m, "j", "enter")
	m = typeText(t, m, "{{times}}")
	m, _ = press(t, m, "esc")

	m, _ = press(t, m, "v", "a")
	m = typeText(t, m, "times=3")
	m, cmd := press(t, m, "enter")
	m = applyChain(t, m, cmd)
	m, _ = press(t, m, "esc")

	m, req := sentRequest(t, m, client)
	require.Equal(t, "hello alice", requestValue(t, req, "name").String())
	require.EqualValues(t, 3, requestValue(t, req, "times").Int())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	assert.Contains(t, string(raw), "{{who}}")
	assert.Contains(t, string(raw), "{{times}}")
	assert.NotContains(t, string(raw), "alice", "the expansion must not reach the file")

	// And recalling it puts the references back on the rows, so the same entry
	// means something else in another environment.
	m = switchTo(t, m, 1) // staging, where who is bob
	m, _ = press(t, m, "v", "a")
	m = typeText(t, m, "times=9")
	m, cmd = press(t, m, "enter")
	m = applyChain(t, m, cmd)
	m, _ = press(t, m, "esc")

	m, cmd = press(t, m, "ctrl+r")
	require.Nil(t, cmd)
	m, cmd = press(t, m, "enter")
	m = applyChain(t, m, cmd)

	assert.Contains(t, m.View(), "{{who}}")

	_, req = sentRequest(t, m, client)
	assert.Equal(t, "hello bob", requestValue(t, req, "name").String())
	assert.EqualValues(t, 9, requestValue(t, req, "times").Int())
}

// --- binding by hand --------------------------------------------------------

func TestModel_BindAndUnbindAVariable(t *testing.T) {
	client := healthyClient()

	m := varsModel(t, client)

	m, _ = press(t, m, "v", "a")
	m = typeText(t, m, "who=dave")
	m, cmd := press(t, m, "enter")
	m = applyChain(t, m, cmd)
	m, _ = press(t, m, "esc")

	m = fillName(t, m, "{{who}}")
	m, req := sentRequest(t, m, client)
	assert.Equal(t, "dave", requestValue(t, req, "name").String())

	// Unbinding puts the reference back to being unresolvable, which refuses the
	// send rather than sending an empty name.
	m, _ = press(t, m, "v")
	m, cmd = press(t, m, "G", "d")
	m = applyChain(t, m, cmd)
	m, _ = press(t, m, "esc")

	before := client.invocations.Load()
	_, cmd = press(t, m, "ctrl+s")
	assert.Nil(t, cmd)
	assert.Equal(t, before, client.invocations.Load())
}

// --- goldens ----------------------------------------------------------------

func TestModel_VariablesGolden(t *testing.T) {
	tests := map[string]struct {
		// send fills the form in and calls, for the screens that need a response
		// to have happened.
		send bool
		keys []string
		want string
	}{
		"environment switcher": {keys: []string{"e"}, want: "Environments"},
		"variables":            {keys: []string{"v"}, want: "Variables · dev"},
		"capture prompt":       {send: true, keys: []string{"ctrl+p"}, want: "Capture from the response"},
		"a form full of references": {
			send: false,
			want: "{{who}}",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			client := healthyClient()
			m := varsModel(t, client)
			m = fillName(t, m, "hello {{who}}")

			if tt.send {
				m, _ = sentRequest(t, m, client)
			}
			m, _ = press(t, m, tt.keys...)

			require.Contains(t, m.View(), tt.want)
			teatest.RequireEqualOutput(t, []byte(m.View()))
		})
	}
}

// switchTo opens the environment switcher and picks the nth entry.
func switchTo(t *testing.T, m ui.Model, index int) ui.Model {
	t.Helper()

	m, _ = press(t, m, "e", "g")
	for range index {
		m, _ = press(t, m, "j")
	}

	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd, "choosing an environment must emit a message")
	return applyChain(t, m, cmd)
}

// clearField empties whichever text input is being edited. It presses more
// backspaces than the field can hold rather than counting what is in it.
func clearField(t *testing.T, m ui.Model) ui.Model {
	t.Helper()

	for range 64 {
		m, _ = press(t, m, "backspace")
	}
	return m
}
