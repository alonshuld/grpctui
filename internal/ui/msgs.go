package ui

import "github.com/alonshuld/grpctui/internal/grpcclient"

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
