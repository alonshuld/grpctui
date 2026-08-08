package ui

import (
	"time"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/requests"
)

// historySavedMsg reports whether history reached the disk. Writing it is
// ordinary file I/O, which Update must not do, and it is a convenience rather
// than part of the call: a failure is logged and never shown.
type historySavedMsg struct {
	err error
}

// requestSavedMsg carries the outcome of writing a request into a collection,
// along with the updated set — [requests.Collections.Save] both writes the file
// and records the entry, and the write happens in a command, so the result has
// to travel back to the model.
type requestSavedMsg struct {
	collections requests.Collections

	// collection and name say where it went, for the prompt's next suggestion.
	collection string
	name       string

	err error
}

// servicesDiscoveredMsg carries the result of a successful reflection sweep.
type servicesDiscoveredMsg struct {
	services []grpcclient.Service
}

// discoveryFailedMsg carries a failed reflection sweep. The error keeps its
// gRPC status, so the error screen can distinguish "reflection is off" from
// "nothing is listening".
type discoveryFailedMsg struct {
	err error
}

// clientConnectedMsg carries the outcome of opening a connection for a profile.
// Dialling is a tea.Cmd like any other blocking work: it reads a CA bundle and
// a client key off disk, which Update must not do.
type clientConnectedMsg struct {
	// seq identifies the attempt, so that a dial the user has moved on from
	// cannot install its client over a newer one.
	seq int

	// index and profile say which connection this is, for the switcher's active
	// marker and the metadata panel's headers.
	index   int
	profile grpcclient.Profile

	// client is the new connection, and err the reason there is none. On a
	// failure the model keeps the connection it already had.
	client Client
	err    error
}

// streamOpenedMsg carries the outcome of opening a streaming call. Opening one
// puts a request on the wire, so like every other RPC it happens in a tea.Cmd
// and comes back as a message.
type streamOpenedMsg struct {
	// seq identifies the stream, from the same counter unary calls use: a stream
	// and a call are both "the call in flight", and only one of them exists at a
	// time.
	seq int

	stream grpcclient.Stream
	err    error
}

// streamSentMsg reports one request message having gone out — or the sending
// half having been closed, when closedSend is set.
type streamSentMsg struct {
	seq int

	// body is the message as it was rendered for the log, and format how. They
	// are empty for a close, which puts a note in the log rather than a message.
	body   string
	format protoschema.Format

	closedSend bool
	err        error

	// at is how far into the stream this happened.
	at time.Duration
}

// streamRecvMsg carries one response message off a stream, or the end of it.
type streamRecvMsg struct {
	seq int

	body   string
	format protoschema.Format

	// done marks the clean end of the stream: the server finished sending and
	// the call succeeded.
	done bool

	// err is how the stream failed, with its gRPC status when it had one.
	err       error
	status    grpcclient.CallStatus
	hasStatus bool

	at time.Duration
}

// callFinishedMsg carries the outcome of one unary call. The response is
// already rendered as JSON: decoding happens in the command, not in Update.
type callFinishedMsg struct {
	// seq identifies the call. A message whose seq is not the model's current
	// one belongs to a call the user has moved on from and is dropped.
	seq int

	// body is the rendered response, set only on success, and format says how it
	// was rendered — JSON unless the message left no other option.
	body   string
	format protoschema.Format

	// err is the call's failure, if any. status and hasStatus carry its gRPC
	// status, when it had one — a request that never reached the wire does not.
	err       error
	status    grpcclient.CallStatus
	hasStatus bool

	duration time.Duration
}
