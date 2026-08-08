// Package grpcclient is grpctui's transport layer: it dials a target, asks it
// what it serves over server reflection, and (from v0.2) invokes methods
// dynamically.
//
// It exposes plain Go types and errors. Nothing here knows about bubbletea,
// lipgloss, or the terminal, which is what makes the reflection/invoke core
// testable without a TTY.
package grpcclient

import (
	"errors"
	"fmt"

	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// Client is a connection to a single gRPC target. It is safe for concurrent
// use; the underlying [grpc.ClientConn] multiplexes RPCs over one HTTP/2
// connection, so a Client is created once and reused for every call.
type Client struct {
	conn     grpc.ClientConnInterface
	closer   func() error
	target   string
	security Security
	logger   *zap.Logger
}

// DialOption configures [Dial].
type DialOption func(*dialConfig)

type dialConfig struct {
	logger   *zap.Logger
	grpcOpts []grpc.DialOption
	security Security
	auth     Auth
}

// WithLogger attaches a logger to the client. Without it the client logs
// nothing.
func WithLogger(logger *zap.Logger) DialOption {
	return func(cfg *dialConfig) {
		if logger != nil {
			cfg.logger = logger
		}
	}
}

// WithGRPCDialOptions appends raw gRPC dial options. Tests use it to swap in a
// bufconn dialer.
func WithGRPCDialOptions(opts ...grpc.DialOption) DialOption {
	return func(cfg *dialConfig) {
		cfg.grpcOpts = append(cfg.grpcOpts, opts...)
	}
}

// WithSecurity chooses how the connection is protected. Without it the
// connection is plaintext.
func WithSecurity(s Security) DialOption {
	return func(cfg *dialConfig) { cfg.security = s }
}

// WithAuth attaches a credential to every RPC the connection carries,
// reflection included.
func WithAuth(a Auth) DialOption {
	return func(cfg *dialConfig) { cfg.auth = a }
}

// Dial creates a client for target ("host:port"), plaintext unless
// [WithSecurity] says otherwise.
//
// It takes no context because it does no I/O: gRPC connects lazily, so a
// target that is down surfaces as a [codes.Unavailable] status on the first
// call rather than as a dial error. Callers that need to know the target is
// reachable should follow Dial with [Client.ListServices], which does take a
// context.
//
// Bad TLS material is the exception: a CA file that does not exist, or a client
// certificate that does not match its key, fails here rather than on the first
// call, because nothing about it will improve by waiting.
func Dial(target string, opts ...DialOption) (*Client, error) {
	if target == "" {
		return nil, errors.New("dial: empty target address")
	}

	cfg := dialConfig{logger: zap.NewNop()}
	for _, opt := range opts {
		opt(&cfg)
	}

	creds, err := cfg.security.credentials()
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", target, err)
	}

	dialOpts := make([]grpc.DialOption, 0, len(cfg.grpcOpts)+3)
	dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))

	// The stats handler is installed once, here, and measures only the calls
	// that ask for it — see [withTiming]. Installing it per call is not an
	// option: gRPC takes stats handlers as dial options, not call options.
	dialOpts = append(dialOpts, grpc.WithStatsHandler(statsHandler{}))

	if header, ok := cfg.auth.header(); ok {
		if !cfg.security.TLS {
			// Not an error: grpctui exists to talk to local servers and
			// port-forwards, where plaintext plus a token is the normal shape of
			// a staging environment. It is worth a line in the log all the same.
			cfg.logger.Warn("sending credentials over a plaintext connection",
				zap.String("target", target),
				zap.String("auth", string(cfg.auth.Kind)),
			)
		}
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(perRPCAuth{
			key:   header.Key,
			value: header.Value,
		}))
	}

	dialOpts = append(dialOpts, cfg.grpcOpts...)

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", target, err)
	}

	// The credential itself is never logged — only which kind it is.
	cfg.logger.Info("created client",
		zap.String("target", target),
		zap.String("security", cfg.security.Mode()),
		zap.String("auth", cfg.auth.Describe()),
	)

	return &Client{
		conn:     conn,
		closer:   conn.Close,
		target:   target,
		security: cfg.security,
		logger:   cfg.logger,
	}, nil
}

// DialProfile creates a client for a connection profile, which is [Dial] plus
// the profile's security and credentials. The profile's metadata is not applied
// here: headers ride on the call, so that editing them does not mean
// reconnecting.
func DialProfile(p Profile, opts ...DialOption) (*Client, error) {
	if err := p.Validate(); err != nil {
		if p.Name != "" {
			return nil, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		return nil, err
	}

	opts = append([]DialOption{WithSecurity(p.Security), WithAuth(p.Auth)}, opts...)
	return Dial(p.Target, opts...)
}

// Target returns the address this client was dialed with.
func (c *Client) Target() string { return c.target }

// Security reports how this connection is protected.
func (c *Client) Security() Security { return c.security }

// Conn exposes the underlying connection, for the one caller that needs to open
// streams this package knows nothing about: internal/proxy, which forwards
// whatever an outside client sends without decoding it.
//
// It is deliberately the narrow [grpc.ClientConnInterface] rather than a
// *grpc.ClientConn. Everything grpctui's own layers do goes through the methods
// on Client; this is a seam for passing a connection along, not an invitation
// to reach past the transport layer.
func (c *Client) Conn() grpc.ClientConnInterface { return c.conn }

// Close releases the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.closer == nil {
		return nil
	}
	c.logger.Debug("closing client", zap.String("target", c.target))
	return c.closer()
}
