// connections.go resolves the connection profiles a session starts with, and
// applies the command line's overrides on top of the chosen one.

package main

import (
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protofiles"
	"github.com/alonshuld/grpctui/internal/ui"
)

// startup is everything the flags and the config file decide before anything is
// dialled: the connections available, which one to open, and why any of the
// others could not be.
type startup struct {
	profiles []grpcclient.Profile

	// problems runs parallel to profiles: problems[i] is why profiles[i] cannot
	// be dialled, or nil. A profile naming an environment variable nobody
	// exported is listed and refused when chosen, rather than taking the whole
	// file down with it at startup — a production token you do not have should
	// stop you reaching production, not your own laptop.
	problems []error

	active int

	// schema is the .proto-file fallback, empty unless -proto was given. It is
	// a property of the session rather than of one profile: the files describe
	// an API, and switching to another connection serving the same API must not
	// mean losing them.
	schema protofiles.Schema
}

// connections assembles the connection profiles and says which one to start on.
//
// The flags override the chosen profile rather than replacing it: `grpctui
// --profile staging --insecure` is a saved connection with verification turned
// off for one session, not a new connection that has lost its credentials.
//
// envTarget is where the starting environment points, which sits between the
// profile and the command line: a profile says how to reach a server, an
// environment says which server, and a target typed on the command line is the
// most specific thing the user could have said.
func connections(cfg config.Config, opts options, envTarget string) (startup, error) {
	var s startup
	s.profiles, s.problems = cfg.Connections()

	// A run with no config file at all is grpctui's whole pitch, and it still
	// has to have somewhere to connect to.
	if len(s.profiles) == 0 {
		s.profiles = []grpcclient.Profile{{}}
		s.problems = []error{nil}
	}

	active, err := config.Select(s.profiles, opts.profile)
	if err != nil {
		return startup{}, err
	}
	s.active = active

	// The connection actually being opened has to work now, so its problem is
	// raised here rather than deferred to a dial nobody asked for yet.
	if err := s.problems[active]; err != nil {
		return startup{}, err
	}

	if envTarget != "" {
		s.profiles[active].Target = envTarget
	}
	if s.profiles[active], err = applyFlags(s.profiles[active], opts); err != nil {
		return startup{}, err
	}

	// A missing target is left for the caller to report: it is a usage error
	// with a usage message, not a profile that is wrong about something.
	if s.profiles[active].Target != "" {
		if err := s.profiles[active].Validate(); err != nil {
			return startup{}, err
		}
	}
	return s, nil
}

// profile is the connection to open first.
func (s startup) profile() grpcclient.Profile { return s.profiles[s.active] }

// problem reports why a profile cannot be dialled, matched by name. Names are
// unique — the config loader refuses a file where they are not — so the name
// identifies a profile without comparing whole structs.
func (s startup) problem(name string) error {
	for i, p := range s.profiles {
		if p.Name == name {
			return s.problems[i]
		}
	}
	return nil
}

// applyFlags lays the command line over a profile.
func applyFlags(p grpcclient.Profile, opts options) (grpcclient.Profile, error) {
	if opts.target != "" {
		p.Target = opts.target
	}

	// Naming a certificate, a CA or a server name is asking for TLS; requiring
	// --tls beside them would only be a way to get an error message. Passing
	// --tls=false with them is still refused, by [grpcclient.Security.Validate],
	// because there it is a contradiction rather than an omission.
	for _, f := range []struct {
		name string
		set  func()
	}{
		{flagCACert, func() { p.Security.CACert = opts.caCert }},
		{flagCert, func() { p.Security.ClientCert = opts.clientCert }},
		{flagKey, func() { p.Security.ClientKey = opts.clientKey }},
		{flagServerName, func() { p.Security.ServerName = opts.serverName }},
		{flagInsecure, func() { p.Security.InsecureSkipVerify = opts.insecure }},
	} {
		if opts.given[f.name] {
			f.set()
			p.Security.TLS = true
		}
	}
	if opts.given[flagTLS] {
		p.Security.TLS = opts.tls
	}

	headers, err := parseHeaders(opts.headers)
	if err != nil {
		return grpcclient.Profile{}, err
	}
	p.Metadata = append(p.Metadata.Clone(), headers...)

	return p, nil
}

// parseHeaders reads the -H flags, in grpcurl's `key: value` form.
func parseHeaders(values []string) (grpcclient.Metadata, error) {
	if len(values) == 0 {
		return nil, nil
	}

	md := make(grpcclient.Metadata, 0, len(values))
	for _, v := range values {
		key, value, ok := strings.Cut(v, ":")
		if !ok {
			return nil, fmt.Errorf("header %q: want `key: value`", v)
		}

		header := grpcclient.Header{
			Key:   strings.TrimSpace(key),
			Value: strings.TrimSpace(value),
		}
		if err := grpcclient.ValidateHeader(header); err != nil {
			return nil, fmt.Errorf("header %q: %w", v, err)
		}
		md = append(md, header)
	}
	return md, nil
}

// dialer opens connections for the UI's profile switcher.
//
// A profile the config file could not finish reading — an unset environment
// variable in a token, say — fails here, with the reason it was set aside at
// startup. That is the point at which the user has asked for it, and the point
// at which the answer is useful.
func (s startup) dialer(logger *zap.Logger) ui.DialerFunc {
	return func(profile grpcclient.Profile) (ui.Client, error) {
		if err := s.problem(profile.Name); err != nil {
			return nil, err
		}
		return grpcclient.DialProfile(profile, dialOptions(s.schema, logger)...)
	}
}
