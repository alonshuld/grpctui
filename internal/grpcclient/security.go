package grpcclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Security describes how a connection to a target is protected.
//
// The zero value is plaintext, which is what grpctui has done since v0.1 and
// what a local server almost always wants. Everything beyond it — the system
// trust store, a private CA, a client certificate — is opt-in.
type Security struct {
	// TLS turns the connection into a TLS one. Without it every other field
	// here is ignored, so that a half-filled profile fails loudly at validation
	// rather than quietly connecting in the clear.
	TLS bool

	// CACert is the path to a PEM bundle used to verify the server. Empty means
	// the host's trust store.
	CACert string

	// ClientCert and ClientKey are a PEM certificate/key pair presented to the
	// server: mutual TLS. Both are needed or neither.
	ClientCert string
	ClientKey  string

	// ServerName overrides the name checked against the server's certificate,
	// for the case where the address dialled is not the name issued — an IP, a
	// tunnel, a port-forward.
	ServerName string

	// InsecureSkipVerify accepts any certificate the server offers. It is a
	// debugging tool for self-signed staging environments and it removes the
	// guarantee that the connection is to who it says: the status bar says so
	// while it is on.
	InsecureSkipVerify bool
}

// Mode names the protection in force, for the status bar.
func (s Security) Mode() string {
	if !s.TLS {
		return "plaintext"
	}

	mode := "TLS"
	if s.ClientCert != "" {
		mode = "mTLS"
	}
	if s.InsecureSkipVerify {
		mode += " (unverified)"
	}
	return mode
}

// Validate reports a combination of settings that cannot be dialled, before a
// connection is attempted with it.
func (s Security) Validate() error {
	if !s.TLS {
		// A profile carrying TLS material but not TLS itself is a mistake worth
		// naming: the alternative is connecting in the clear while the config
		// file talks about certificates.
		switch {
		case s.CACert != "", s.ClientCert != "", s.ClientKey != "", s.ServerName != "", s.InsecureSkipVerify:
			return errors.New("TLS settings are configured but TLS is not enabled")
		}
		return nil
	}

	if (s.ClientCert == "") != (s.ClientKey == "") {
		return errors.New("mutual TLS needs both a client certificate and a key")
	}
	return nil
}

// credentials builds the transport credentials this Security describes.
func (s Security) credentials() (credentials.TransportCredentials, error) {
	if !s.TLS {
		return insecure.NewCredentials(), nil
	}

	// #nosec G402 -- InsecureSkipVerify is the user's own explicit toggle for
	// self-signed environments; it is off unless asked for and the status bar
	// says so while it is on.
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         s.ServerName,
		InsecureSkipVerify: s.InsecureSkipVerify,
	}

	if s.CACert != "" {
		pool, err := certPool(s.CACert)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}

	if s.ClientCert != "" || s.ClientKey != "" {
		if s.ClientCert == "" || s.ClientKey == "" {
			return nil, errors.New("mutual TLS needs both a client certificate and a key")
		}
		cert, err := tls.LoadX509KeyPair(s.ClientCert, s.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return credentials.NewTLS(cfg), nil
}

// certPool reads a PEM bundle into a pool holding only those roots.
//
// The system store is deliberately not merged in: naming a CA is how a user
// says "this server is signed by this authority and no other", and quietly
// accepting every public root as well would turn a pinned connection into an
// ordinary one.
func certPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path) // #nosec G304 -- the path is the CA the user named.
	if err != nil {
		return nil, fmt.Errorf("read CA certificate: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("read CA certificate %q: no PEM certificates found", path)
	}
	return pool, nil
}

// AuthKind is the kind of credential attached to every call on a connection.
type AuthKind string

// The supported auth kinds. Anything more exotic — a signed JWT, a custom
// scheme — is a header, which the metadata panel can already send.
const (
	AuthNone   AuthKind = ""
	AuthBearer AuthKind = "bearer"
	AuthBasic  AuthKind = "basic"
)

// Auth is a per-connection credential. It becomes an `authorization` header on
// every RPC the connection carries — including reflection, which a protected
// server is as likely to guard as anything else.
type Auth struct {
	Kind AuthKind

	// Token is the bearer token, with or without its "Bearer " prefix.
	Token string

	// Username and Password are the basic-auth pair.
	Username string
	Password string
}

// Describe names the credential without revealing it, for the status bar and
// the profile picker.
func (a Auth) Describe() string {
	switch a.Kind {
	case AuthBearer:
		return "bearer"
	case AuthBasic:
		if a.Username != "" {
			return "basic (" + a.Username + ")"
		}
		return "basic"
	default:
		return "none"
	}
}

// Validate reports an auth preset that cannot be sent.
func (a Auth) Validate() error {
	switch a.Kind {
	case AuthNone:
		return nil
	case AuthBearer:
		if a.Token == "" {
			return errors.New("bearer auth needs a token")
		}
		return nil
	case AuthBasic:
		if a.Username == "" {
			return errors.New("basic auth needs a username")
		}
		return nil
	default:
		return fmt.Errorf("unknown auth type %q: want one of bearer, basic", a.Kind)
	}
}

// header renders the credential as the header it becomes on the wire. ok is
// false when there is no credential to send.
func (a Auth) header() (Header, bool) {
	switch a.Kind {
	case AuthBearer:
		if a.Token == "" {
			return Header{}, false
		}
		// A token pasted from a browser's dev tools often arrives with the
		// scheme already on it; prefixing it again produces "Bearer Bearer …",
		// which servers reject with a 401 that says nothing useful.
		value := a.Token
		if !strings.HasPrefix(strings.ToLower(value), "bearer ") {
			value = "Bearer " + value
		}
		return Header{Key: "authorization", Value: value}, true

	case AuthBasic:
		if a.Username == "" && a.Password == "" {
			return Header{}, false
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Password))
		return Header{Key: "authorization", Value: "Basic " + encoded}, true

	default:
		return Header{}, false
	}
}

// perRPCAuth attaches a fixed header to every call on the connection.
//
// It is gRPC's [credentials.PerRPCCredentials] rather than a header in the
// metadata panel so that it survives every RPC the client makes — reflection
// included — without each caller having to remember it.
type perRPCAuth struct {
	key   string
	value string
}

// GetRequestMetadata implements [credentials.PerRPCCredentials].
func (a perRPCAuth) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{a.key: a.value}, nil
}

// RequireTransportSecurity implements [credentials.PerRPCCredentials].
//
// It reports false, which lets a token travel over a plaintext connection.
// gRPC's own default is to refuse, and for a library that is right; for a
// debugging tool aimed at local and port-forwarded servers it would mean
// refusing the most common setup there is. The client logs a warning instead,
// and the status bar shows the connection is in the clear.
func (perRPCAuth) RequireTransportSecurity() bool { return false }

// Profile is a named way to reach a target: where it is, how the connection is
// protected, who we are on it, and what headers ride along.
//
// It is the unit the profile switcher cycles through, and the shape the config
// file's `profiles` list is read into.
type Profile struct {
	// Name identifies the profile in the switcher. It is optional: a profile
	// assembled from command-line flags has none.
	Name string

	// Target is the address to dial, "host:port".
	Target string

	Security Security
	Auth     Auth

	// Metadata seeds the metadata panel when the profile becomes active.
	Metadata Metadata
}

// Label names the profile for a list, falling back to the target for a profile
// that was never named.
func (p Profile) Label() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Target
}

// Validate reports a profile that cannot be dialled.
func (p Profile) Validate() error {
	if p.Target == "" {
		return errors.New("profile has no target address")
	}
	if err := p.Security.Validate(); err != nil {
		return err
	}
	if err := p.Auth.Validate(); err != nil {
		return err
	}
	return p.Metadata.Validate()
}
