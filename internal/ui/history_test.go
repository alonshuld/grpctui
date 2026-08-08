package ui_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/requests"
	"github.com/alonshuld/grpctui/internal/ui"
)

// goldenNow is the clock every v0.6 test reads, so that "5m ago" is decided by
// the entries a test wrote rather than by when it ran.
func goldenNow() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }

// historyIn returns a history persisting to a file in a temp directory, along
// with the path, so a test can assert on what actually reached the disk.
func historyIn(t *testing.T) (requests.History, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "history.yaml")
	h, err := requests.LoadHistory(path, 0)
	require.NoError(t, err)
	return h, path
}

// sayHello is a request as history records one, against the demo fixture.
func sayHello(name string, sentAt time.Time) requests.Request {
	return requests.Request{
		Method: "demo.v1.Greeter.SayHello",
		Kind:   string(grpcclient.KindUnary),
		Target: "localhost:50051",
		Body:   map[string]any{"name": name},
		SentAt: sentAt,
	}
}

// seeded returns a history holding the given requests, oldest first — the order
// they would have been sent in.
func seeded(reqs ...requests.Request) requests.History {
	var h requests.History
	for _, r := range reqs {
		h.Add(r)
	}
	return h
}

// applyChain feeds a command's messages back into the model, and the commands
// those produce after them, until the chain runs dry.
//
// apply stops after one hop, which is all a unary call needs: one command, one
// message. v0.6's flows are chains — a keystroke yields a SaveRequestMsg, whose
// handler yields the write, whose result closes the prompt — and stopping at
// the first hop would assert against a screen the user never sees.
func applyChain(t *testing.T, m ui.Model, cmd tea.Cmd) ui.Model {
	t.Helper()

	// A generous bound rather than none: a bug that makes a handler re-issue its
	// own command would otherwise hang the suite instead of naming itself.
	const maxHops = 16

	for hop := 0; cmd != nil; hop++ {
		require.Less(t, hop, maxHops, "the command chain did not settle")

		var next []tea.Cmd
		for _, msg := range drain(cmd) {
			if _, isTick := msg.(spinner.TickMsg); isTick {
				continue
			}
			model, c := m.Update(msg)
			m = asModel(t, model)
			next = append(next, c)
		}
		cmd = tea.Batch(next...)
	}
	return m
}

// clearPrompt empties whichever text input the browser is showing. It presses
// more backspaces than the field can hold rather than counting the suggestion's
// length, so a test says what it means without depending on it.
func clearPrompt(t *testing.T, m ui.Model) ui.Model {
	t.Helper()

	for range 64 {
		m, _ = press(t, m, "backspace")
	}
	return m
}

// --- recording --------------------------------------------------------------

func TestModel_SendRecordsTheRequest(t *testing.T) {
	client := healthyClient()
	history, path := historyIn(t)

	m := settled(t, newModel(t, client,
		ui.WithHistory(history),
		ui.WithClock(goldenNow),
		ui.WithProfiles(goldenProfiles(), 0),
	))
	m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello

	m, _ = press(t, m, "enter")
	m = typeText(t, m, "world")
	m, _ = press(t, m, "esc")

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)
	require.Contains(t, m.View(), "OK", "the call itself must have gone through")

	reloaded, err := requests.LoadHistory(path, 0)
	require.NoError(t, err)
	require.Equal(t, 1, reloaded.Len(), "the send must have reached the file")

	entry, ok := reloaded.At(0)
	require.True(t, ok)
	assert.Equal(t, "demo.v1.Greeter.SayHello", entry.Method)
	assert.Equal(t, string(grpcclient.KindUnary), entry.Kind)
	assert.Equal(t, "localhost:50051", entry.Target)
	assert.Equal(t, map[string]any{"name": "world"}, entry.Body)
	assert.Equal(t, goldenNow(), entry.SentAt.UTC())

	t.Run("records header names and no values", func(t *testing.T) {
		assert.Equal(t, []string{"x-tenant", "x-request-id"}, entry.Headers)

		body, err := os.ReadFile(path) // #nosec G304 -- a path the test made.
		require.NoError(t, err)
		for _, value := range []string{"acme", "6f1c-42"} {
			assert.NotContains(t, string(body), value,
				"a header value must never reach a file that outlives the call")
		}
	})
}

// History is a convenience, not the call: a state directory nobody can write to
// must not stop a request going out.
func TestModel_SendSurvivesAnUnwritableHistory(t *testing.T) {
	client := healthyClient()

	// A path whose parent is a file, which no platform will let us create a
	// directory under.
	blocked := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocked, nil, 0o600))

	m := settled(t, newModel(t, client,
		ui.WithHistory(requests.History{Path: filepath.Join(blocked, "history.yaml")}),
	))
	m = selectMethod(t, m, 3)

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)

	assert.Equal(t, int32(1), client.invocations.Load(), "the call still went out")
	assert.NotContains(t, m.View(), "history", "a failed history write says nothing on screen")
}

// A streaming call is a sent request too, and each message fed to a
// client-streaming one is another.
func TestModel_StreamSendsAreRecorded(t *testing.T) {
	client := healthyClient()
	history, path := historyIn(t)

	m := settled(t, newModel(t, client, ui.WithHistory(history), ui.WithClock(goldenNow)))
	m = selectMethod(t, m, 5) // demo.v1.Greeter.CollectHellos, client-streaming

	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)
	m = apply(t, m, cmd)

	// A second message on the same open stream.
	m, _ = press(t, m, "enter")
	m = typeText(t, m, "again")
	m, _ = press(t, m, "esc")
	m, cmd = press(t, m, "ctrl+s")
	require.NotNil(t, cmd)
	_ = apply(t, m, cmd)

	reloaded, err := requests.LoadHistory(path, 0)
	require.NoError(t, err)
	require.Equal(t, 2, reloaded.Len())

	newest, _ := reloaded.At(0)
	assert.Equal(t, map[string]any{"name": "again"}, newest.Body)
	assert.Equal(t, string(grpcclient.KindClientStreaming), newest.Kind)
}

// --- cycling ----------------------------------------------------------------

func TestModel_HistoryKeysWalkTheSentRequests(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(
			sayHello("first", goldenNow().Add(-time.Hour)),
			sayHello("second", goldenNow().Add(-time.Minute)),
		)),
	))

	m, _ = press(t, m, "[")
	assert.Contains(t, m.View(), "second", "[ lands on the newest entry")
	assert.Contains(t, m.View(), "demo.v1.Greeter.SayHello", "and on its method")

	m, _ = press(t, m, "[")
	assert.Contains(t, m.View(), "first")

	m, _ = press(t, m, "[")
	assert.Contains(t, m.View(), "first", "there is nothing older to step to")

	m, _ = press(t, m, "]")
	assert.Contains(t, m.View(), "second")
}

func TestModel_HistoryKeysDoNothingWithNoHistory(t *testing.T) {
	m := settled(t, newModel(t, healthyClient()))
	before := m.View()

	m, cmd := press(t, m, "[")
	assert.Nil(t, cmd)
	assert.Equal(t, before, m.View())

	m, cmd = press(t, m, "]")
	assert.Nil(t, cmd)
	assert.Equal(t, before, m.View())
}

// Stepping forward from a form the user built themselves has nowhere to go:
// there is nothing newer than what is on screen.
func TestModel_HistoryForwardFromAFreshForm(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(),
		ui.WithHistory(seeded(sayHello("first", goldenNow()))),
		ui.WithClock(goldenNow),
	))
	m = selectMethod(t, m, 1) // demo.v1.Echo.Echo

	m, _ = press(t, m, "]")
	assert.Contains(t, m.View(), "demo.v1.Echo.Echo", "the form is left alone")
}

// A history entry recalled and re-sent must not push the one before it out of
// reach, so the cursor sits on the newest entry after a send rather than
// wherever it had walked to.
func TestModel_SendResetsTheHistoryCursor(t *testing.T) {
	client := healthyClient()

	m := settled(t, newModel(t, client,
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(
			sayHello("first", goldenNow().Add(-time.Hour)),
			sayHello("second", goldenNow().Add(-time.Minute)),
		)),
	))

	m, _ = press(t, m, "[", "[")
	require.Contains(t, m.View(), "first")

	m, cmd := press(t, m, "ctrl+s")
	m = apply(t, m, cmd)

	m, _ = press(t, m, "[")
	assert.Contains(t, m.View(), "second",
		"[ after a send steps to the entry before the one just sent")
}

// Picking a method by hand means the form holds something built rather than
// something recalled, so the walk starts over: `[` means "the last thing I
// sent", not "one before wherever I had got to".
func TestModel_SelectingAMethodResetsTheHistoryCursor(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(
			sayHello("first", goldenNow().Add(-time.Hour)),
			sayHello("second", goldenNow().Add(-time.Minute)),
		)),
	))

	m, _ = press(t, m, "[", "[")
	require.Contains(t, m.View(), "first")

	m = selectMethod(t, m, 1) // demo.v1.Echo.Echo, by hand

	m, _ = press(t, m, "[")
	assert.Contains(t, m.View(), "second")
}

// The browser is a different way in from the walk, so picking out of it puts
// the walk back to the start too.
func TestModel_BrowsingResetsTheHistoryCursor(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(
			sayHello("first", goldenNow().Add(-time.Hour)),
			sayHello("second", goldenNow().Add(-time.Minute)),
		)),
	))

	m, _ = press(t, m, "[", "[")
	require.Contains(t, m.View(), "first")

	// Pick the older entry out of the list rather than by walking to it.
	m, _ = press(t, m, "ctrl+r", "j")
	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)
	require.Contains(t, m.View(), "first")

	m, _ = press(t, m, "[")
	assert.Contains(t, m.View(), "second")
}

// A request recorded against another server is not a request this one can make.
func TestModel_RecallingAMethodTheConnectionLacks(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(requests.Request{
			Method: "other.v1.Thing.Do",
			Body:   map[string]any{"x": 1},
			SentAt: goldenNow(),
		})),
	))

	m, _ = press(t, m, "[")
	assert.Contains(t, m.View(), "other.v1.Thing.Do is not on this connection.")
}

// A collection outlives the schema. A body naming a field that has since gone
// says so rather than sending a request missing it.
func TestModel_RecallingABodyThatNoLongerFits(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(requests.Request{
			Method: "demo.v1.Greeter.SayHello",
			Body:   map[string]any{"nickname": "world"},
			SentAt: goldenNow(),
		})),
	))

	m, _ = press(t, m, "[")
	assert.Contains(t, m.View(), "decode request body")
}

// The record holds header names and no values, so a recalled request cannot
// restore them — but it can say which ones the connection is not sending, which
// is the difference between an explained Unauthenticated and a mystery.
func TestModel_RecallNamesTheHeadersItCannotRestore(t *testing.T) {
	// The connection sends x-tenant and x-request-id; the record also names an
	// authorization header, which the connection has no credential for.
	entry := sayHello("world", goldenNow())
	entry.Headers = []string{"x-tenant", "authorization"}

	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithProfiles(goldenProfiles(), 0),
		ui.WithHistory(seeded(entry)),
	))

	m, _ = press(t, m, "[")

	assert.Contains(t, m.View(), "This was sent with authorization.",
		"only the headers the connection is not already sending are named")
}

// --- the browser ------------------------------------------------------------

func TestModel_BrowserListsHistoryAndCollections(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(sayHello("world", goldenNow().Add(-5*time.Minute)))),
		ui.WithCollections(collectionsWith(t, "team", requests.Request{
			Name:   "greet-alice",
			Method: "demo.v1.Greeter.SayHello",
			Body:   map[string]any{"name": "alice"},
		})),
	))

	m, _ = press(t, m, "ctrl+r")

	view := m.View()
	assert.Contains(t, view, "Requests (2)")
	assert.Contains(t, view, "history")
	assert.Contains(t, view, "greet-alice")
	assert.Contains(t, view, "team")
	assert.Contains(t, view, "5m ago")
}

// The browser is modal: while it is open the panels behind it see nothing.
func TestModel_BrowserOwnsTheKeyboard(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(), ui.WithClock(goldenNow)))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "ctrl+r")
	require.Contains(t, m.View(), "Requests")

	m, _ = press(t, m, "tab", "q")
	assert.Contains(t, m.View(), "Requests", "tab and q are the browser's, not the panels'")

	m, _ = press(t, m, "esc")
	assert.NotContains(t, m.View(), "Nothing sent or saved yet.")
}

func TestModel_BrowserFiltersOnMethodAndFieldContent(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(
			requests.Request{
				Method: "demo.v1.Echo.Echo",
				Body:   map[string]any{"message": "ping"},
				SentAt: goldenNow(),
			},
			sayHello("alice", goldenNow()),
		)),
	))

	m, _ = press(t, m, "ctrl+r")
	require.Contains(t, m.View(), "Requests (2)")

	m, _ = press(t, m, "/")
	m = typeText(t, m, "alice")

	view := m.View()
	assert.Contains(t, view, "Requests (1 of 2)", "the browser says what it is hiding")
	assert.Contains(t, view, "SayHello")
	assert.NotContains(t, view, "demo.v1.Echo.Echo")

	t.Run("esc clears the filter rather than closing the browser", func(t *testing.T) {
		m, _ := press(t, m, "enter")
		m, _ = press(t, m, "esc")

		view := m.View()
		assert.Contains(t, view, "Requests (2)")
		assert.Contains(t, view, "demo.v1.Echo.Echo")
	})
}

func TestModel_BrowserLoadsARequest(t *testing.T) {
	client := healthyClient()

	m := settled(t, newModel(t, client,
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(sayHello("world", goldenNow()))),
	))

	m, _ = press(t, m, "ctrl+r")
	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd, "enter must ask for the request under the cursor")
	m = applyChain(t, m, cmd)

	view := m.View()
	assert.NotContains(t, view, "Requests (", "the browser closes on a choice")
	assert.Contains(t, view, "demo.v1.Greeter.SayHello")
	assert.Contains(t, view, "world")
	assert.Equal(t, int32(0), client.invocations.Load(), "enter loads; it does not send")
}

func TestModel_BrowserLoadsAndSends(t *testing.T) {
	client := healthyClient()

	var got proto.Message
	client.invoke = func(_ context.Context, method grpcclient.Method, req proto.Message) (*grpcclient.UnaryResponse, error) {
		got = req
		return &grpcclient.UnaryResponse{
			Message:  reply(method, map[string]any{"greeting": "hello, world"}),
			Duration: 3 * time.Millisecond,
		}, nil
	}

	m := settled(t, newModel(t, client,
		ui.WithClock(goldenNow),
		ui.WithHistory(seeded(sayHello("world", goldenNow()))),
	))

	m, _ = press(t, m, "ctrl+r")
	m, cmd := press(t, m, "ctrl+s")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)

	require.Equal(t, int32(1), client.invocations.Load())
	require.NotNil(t, got)
	assert.Equal(t, "world", requestValue(t, got, "name").String())
	assert.Contains(t, m.View(), `"greeting": "hello, world"`)
}

// ctrl+r has to reach the model from inside a half-typed field: recalling the
// request you meant is the answer to "I am typing this out again".
func TestModel_BrowserOpensWhileEditing(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(), ui.WithClock(goldenNow)))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "enter")
	m = typeText(t, m, "wor")

	m, _ = press(t, m, "ctrl+r")
	assert.Contains(t, m.View(), "Nothing sent or saved yet.")
}

// --- saving -----------------------------------------------------------------

func TestModel_SaveWritesACollection(t *testing.T) {
	dir := t.TempDir()
	collections, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithProfiles(goldenProfiles(), 0),
		ui.WithCollections(collections),
	))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "enter")
	m = typeText(t, m, "world")
	m, _ = press(t, m, "esc")

	m, _ = press(t, m, "S")
	require.Contains(t, m.View(), "Save request")
	require.Contains(t, m.View(), "default/SayHello", "the prompt suggests a name")

	// Replace the suggestion with a collection and name of our own.
	m = clearPrompt(t, m)
	m = typeText(t, m, "team/greet-world")

	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)

	assert.NotContains(t, m.View(), "Save request", "a successful save closes the prompt")

	saved, err := requests.LoadCollections(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"team"}, saved.Names())

	entries := saved.All()[0].Requests
	require.Len(t, entries, 1)
	assert.Equal(t, "greet-world", entries[0].Name)
	assert.Equal(t, "demo.v1.Greeter.SayHello", entries[0].Method)
	assert.Equal(t, map[string]any{"name": "world"}, entries[0].Body)

	// A collection is meant to be committed, so what it records about the
	// connection is header names and nothing else.
	t.Run("no credential reaches the file", func(t *testing.T) {
		body, err := os.ReadFile(filepath.Join(dir, "team.yaml")) // #nosec G304 -- a path the test made.
		require.NoError(t, err)

		assert.Contains(t, string(body), "x-tenant")
		for _, value := range []string{"acme", "6f1c-42"} {
			assert.NotContains(t, string(body), value)
		}
	})

	t.Run("the next prompt suggests the collection just used", func(t *testing.T) {
		m, _ := press(t, m, "S")
		assert.Contains(t, m.View(), "team/SayHello")
	})
}

// A saved request loaded back and saved under another name is how you duplicate
// one without retyping the form.
func TestModel_SaveDuplicatesUnderANewName(t *testing.T) {
	dir := t.TempDir()
	collections, err := requests.LoadCollections(dir)
	require.NoError(t, err)
	require.NoError(t, collections.Save("team", requests.Request{
		Name:   "greet-alice",
		Method: "demo.v1.Greeter.SayHello",
		Body:   map[string]any{"name": "alice"},
	}))

	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithCollections(collections),
	))

	m, _ = press(t, m, "ctrl+r")
	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)
	require.Contains(t, m.View(), "alice")

	m, _ = press(t, m, "S")
	m = clearPrompt(t, m)
	m = typeText(t, m, "team/greet-alice-again")

	m, cmd = press(t, m, "enter")
	require.NotNil(t, cmd)
	_ = applyChain(t, m, cmd)

	saved, err := requests.LoadCollections(dir)
	require.NoError(t, err)
	require.Len(t, saved.All()[0].Requests, 2, "the original is still there")
}

// The prompt only opens for a request that can actually be saved: a form with
// errors shows them rather than hiding them behind a modal.
func TestModel_SaveRefusesAFormThatDoesNotBuild(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(), ui.WithClock(goldenNow)))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "j", "enter") // times, an int32
	m = typeText(t, m, "many")
	m, _ = press(t, m, "esc")

	m, _ = press(t, m, "S")

	view := m.View()
	assert.NotContains(t, view, "Save request")
	assert.Contains(t, view, "⚠")
}

func TestModel_SaveWithNowhereToSaveSaysSo(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(), ui.WithClock(goldenNow)))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "S")
	require.Contains(t, m.View(), "Save request")

	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)

	assert.Contains(t, m.View(), "no collections directory")
	assert.Contains(t, m.View(), "Save request", "a failed save keeps the prompt open")
}

func TestModel_SaveRejectsANameThatIsAPath(t *testing.T) {
	dir := t.TempDir()
	collections, err := requests.LoadCollections(dir)
	require.NoError(t, err)

	m := settled(t, newModel(t, healthyClient(),
		ui.WithClock(goldenNow),
		ui.WithCollections(collections),
	))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "S")
	m = clearPrompt(t, m)
	m = typeText(t, m, "..")

	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	m = applyChain(t, m, cmd)

	assert.Contains(t, m.View(), "Save request")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestModel_SaveCanBeAbandoned(t *testing.T) {
	m := settled(t, newModel(t, healthyClient(), ui.WithClock(goldenNow)))
	m = selectMethod(t, m, 3)

	m, _ = press(t, m, "S")
	require.Contains(t, m.View(), "Save request")

	m, _ = press(t, m, "esc")
	assert.NotContains(t, m.View(), "Save request")
}

// --- goldens ----------------------------------------------------------------

func TestModel_RequestBrowserGolden(t *testing.T) {
	history := seeded(
		requests.Request{
			Method: "demo.v1.Echo.Echo",
			Kind:   string(grpcclient.KindUnary),
			Target: "localhost:50051",
			Body:   map[string]any{"message": "ping"},
			SentAt: goldenNow().Add(-3 * time.Hour),
		},
		func() requests.Request {
			r := sayHello("world", goldenNow().Add(-5*time.Minute))
			r.Headers = []string{"authorization", "x-tenant"}
			return r
		}(),
	)

	collections := collectionsWith(t, "team",
		requests.Request{
			Name:   "greet-alice",
			Method: "demo.v1.Greeter.SayHello",
			Kind:   string(grpcclient.KindUnary),
			Body:   map[string]any{"name": "alice"},
		},
		requests.Request{
			Name:   "watch-hellos",
			Method: "demo.v1.Greeter.SayHelloStream",
			Kind:   string(grpcclient.KindServerStreaming),
			Body:   map[string]any{"name": "bob"},
		},
	)

	opts := []ui.Option{
		ui.WithClock(goldenNow),
		ui.WithHistory(history),
		ui.WithCollections(collections),
	}

	tests := map[string]struct {
		// pick selects a method first, for the screens that need one.
		pick bool
		keys []string
		want string
	}{
		"request browser":          {keys: []string{"ctrl+r"}, want: "greet-alice"},
		"request browser filtered": {keys: []string{"ctrl+r", "/", "a", "l", "i"}, want: "1 of 4"},
		"save prompt":              {pick: true, keys: []string{"S"}, want: "Save request"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			m := settled(t, newModel(t, healthyClient(), opts...))
			if tt.pick {
				m = selectMethod(t, m, 3) // demo.v1.Greeter.SayHello
			}

			m, _ = press(t, m, tt.keys...)
			require.Contains(t, m.View(), tt.want)

			teatest.RequireEqualOutput(t, []byte(m.View()))
		})
	}
}

// collectionsWith builds a collections set on disk and loads it back, which is
// the only way to get one with a directory it can be saved into.
func collectionsWith(t *testing.T, name string, reqs ...requests.Request) requests.Collections {
	t.Helper()

	c, err := requests.LoadCollections(t.TempDir())
	require.NoError(t, err)
	for _, r := range reqs {
		require.NoError(t, c.Save(name, r))
	}
	return c
}
