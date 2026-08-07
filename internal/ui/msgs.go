package ui

import (
	"time"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
)

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
