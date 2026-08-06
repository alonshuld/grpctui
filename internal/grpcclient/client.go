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
	"google.golang.org/grpc/credentials/insecure"
)

// Client is a connection to a single gRPC target. It is safe for concurrent
// use; the underlying [grpc.ClientConn] multiplexes RPCs over one HTTP/2
// connection, so a Client is created once and reused for every call.
type Client struct {
	conn   grpc.ClientConnInterface
	closer func() error
	target string
	logger *zap.Logger
}

// DialOption configures [Dial].
type DialOption func(*dialConfig)

type dialConfig struct {
	logger    *zap.Logger
	grpcOpts  []grpc.DialOption
	plaintext bool
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
// bufconn dialer; TLS credentials arrive this way in v0.4.
func WithGRPCDialOptions(opts ...grpc.DialOption) DialOption {
	return func(cfg *dialConfig) {
		cfg.grpcOpts = append(cfg.grpcOpts, opts...)
	}
}

// Dial creates a client for target ("host:port"). v0.1 is plaintext only.
//
// It takes no context because it does no I/O: gRPC connects lazily, so a
// target that is down surfaces as a [codes.Unavailable] status on the first
// call rather than as a dial error. Callers that need to know the target is
// reachable should follow Dial with [Client.ListServices], which does take a
// context.
func Dial(target string, opts ...DialOption) (*Client, error) {
	if target == "" {
		return nil, errors.New("dial: empty target address")
	}

	cfg := dialConfig{logger: zap.NewNop(), plaintext: true}
	for _, opt := range opts {
		opt(&cfg)
	}

	dialOpts := make([]grpc.DialOption, 0, len(cfg.grpcOpts)+1)
	if cfg.plaintext {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	dialOpts = append(dialOpts, cfg.grpcOpts...)

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", target, err)
	}

	cfg.logger.Info("created client", zap.String("target", target), zap.Bool("plaintext", cfg.plaintext))

	return &Client{
		conn:   conn,
		closer: conn.Close,
		target: target,
		logger: cfg.logger,
	}, nil
}

// Target returns the address this client was dialed with.
func (c *Client) Target() string { return c.target }

// Close releases the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.closer == nil {
		return nil
	}
	c.logger.Debug("closing client", zap.String("target", c.target))
	return c.closer()
}
