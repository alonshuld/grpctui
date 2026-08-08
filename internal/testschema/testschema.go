// Package testschema builds the large service list the scale tests measure
// against.
//
// v1.0 promised grpctui stays responsive against a big API surface, and two
// suites check it from different heights: internal/ui/panels measures the tree
// and the request browser, internal/ui measures the whole frame bubbletea
// redraws on every keystroke. They have to be measuring the same thing, and two
// copies of the generator that must stay identical — with nothing tying them
// together — is one edit away from two suites quietly benchmarking different
// workloads while both cite the same promise.
package testschema

import (
	"fmt"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// The size v1.0 was validated against: 300 services of 10 methods, which is
// 3,300 rows once every service is expanded, and larger than any real API
// surface the author has met.
const (
	BigServices = 300
	BigMethods  = 10
)

// Big builds that schema. Every third method is server-streaming, so the rows
// that render a stream marker are exercised rather than only the plain ones.
func Big() []grpcclient.Service {
	services := make([]grpcclient.Service, 0, BigServices)
	for s := range BigServices {
		name := ServiceName(s)

		methods := make([]grpcclient.Method, 0, BigMethods)
		for m := range BigMethods {
			method := MethodName(m)
			methods = append(methods, grpcclient.Method{
				Name:            method,
				FullName:        name + "." + method,
				InputType:       name + ".Request",
				OutputType:      name + ".Reply",
				ServerStreaming: m%3 == 0,
			})
		}
		services = append(services, grpcclient.Service{Name: name, Methods: methods})
	}
	return services
}

// ServiceName is how the nth service is spelled, so that a test looking for the
// last row does not write the format out a second time.
func ServiceName(n int) string { return fmt.Sprintf("big.v1.Service%03d", n) }

// MethodName is how the nth method of any of them is spelled.
func MethodName(n int) string { return fmt.Sprintf("Method%02d", n) }
