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
