package grpcclient_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// tlsServerName is the name the test server's certificate is issued for. The
// bufconn target is not a real host, so every TLS test overrides the name the
// certificate is checked against — which is the same override a user needs for
// an IP address or a port-forward.
const tlsServerName = "localhost"

func TestSecurity_Mode(t *testing.T) {
	tests := []struct {
		name     string
		security grpcclient.Security
		want     string
	}{
		{
			name: "zero value is plaintext",
			want: "plaintext",
		},
		{
			name:     "tls",
			security: grpcclient.Security{TLS: true},
			want:     "TLS",
		},
		{
			name:     "tls with a client certificate is mutual",
			security: grpcclient.Security{TLS: true, ClientCert: "c.pem", ClientKey: "k.pem"},
			want:     "mTLS",
		},
		{
			name:     "skipping verification is called out",
			security: grpcclient.Security{TLS: true, InsecureSkipVerify: true},
			want:     "TLS (unverified)",
		},
		{
			name:     "mutual tls without verification",
			security: grpcclient.Security{TLS: true, ClientCert: "c.pem", ClientKey: "k.pem", InsecureSkipVerify: true},
			want:     "mTLS (unverified)",
		},
		{
			name:     "tls settings without tls are still plaintext",
			security: grpcclient.Security{CACert: "ca.pem"},
			want:     "plaintext",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.security.Mode())
		})
	}
}

func TestSecurity_Validate(t *testing.T) {
	tests := []struct {
		name     string
		security grpcclient.Security
		errMsg   string
	}{
		{
			name: "plaintext",
		},
		{
			name:     "tls with the system trust store",
			security: grpcclient.Security{TLS: true},
		},
		{
			name:     "mutual tls with both halves",
			security: grpcclient.Security{TLS: true, ClientCert: "c.pem", ClientKey: "k.pem"},
		},
		{
			name:     "client certificate without a key",
			security: grpcclient.Security{TLS: true, ClientCert: "c.pem"},
			errMsg:   "both a client certificate and a key",
		},
		{
			name:     "key without a certificate",
			security: grpcclient.Security{TLS: true, ClientKey: "k.pem"},
			errMsg:   "both a client certificate and a key",
		},
		{
			name:     "a CA but no TLS",
			security: grpcclient.Security{CACert: "ca.pem"},
			errMsg:   "TLS is not enabled",
		},
		{
			name:     "skip-verify but no TLS",
			security: grpcclient.Security{InsecureSkipVerify: true},
			errMsg:   "TLS is not enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.security.Validate()

			if tt.errMsg == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

// tlsServer starts a server speaking TLS, returning it with the CA that signed
// its certificate.
func tlsServer(t *testing.T, mutual bool) (*testServer, *certAuthority) {
	t.Helper()

	ca := newCertAuthority(t)
	certPath, keyPath := ca.issue(t, tlsServerName, true)

	var clientCAs *x509.CertPool
	if mutual {
		clientCAs = x509.NewCertPool()
		require.True(t, clientCAs.AppendCertsFromPEM(ca.certPEM))
	}

	return startTestServer(t, withServerTLS(t, certPath, keyPath, clientCAs)), ca
}

func TestDial_TLS_WithCustomCA(t *testing.T) {
	ts, ca := tlsServer(t, false)

	c := ts.client(t, grpcclient.WithSecurity(grpcclient.Security{
		TLS:        true,
		CACert:     ca.caFile(),
		ServerName: tlsServerName,
	}))

	services, err := c.ListServices(context.Background(), nil)

	require.NoError(t, err)
	assert.NotEmpty(t, services)
}

// The system trust store does not know the test CA, so a connection that
// trusts only it must fail — which is what proves the CA above was doing the
// work.
func TestDial_TLS_WithoutTheCAFails(t *testing.T) {
	ts, _ := tlsServer(t, false)

	c := ts.client(t, grpcclient.WithSecurity(grpcclient.Security{
		TLS:        true,
		ServerName: tlsServerName,
	}))

	_, err := c.ListServices(context.Background(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate")
}

func TestDial_TLS_InsecureSkipVerify(t *testing.T) {
	ts, _ := tlsServer(t, false)

	c := ts.client(t, grpcclient.WithSecurity(grpcclient.Security{
		TLS:                true,
		InsecureSkipVerify: true,
	}))

	services, err := c.ListServices(context.Background(), nil)

	require.NoError(t, err)
	assert.NotEmpty(t, services)
}

// A certificate issued for one name and checked against another is the whole
// point of ServerName, so the failure it prevents is worth pinning too.
func TestDial_TLS_WrongServerName(t *testing.T) {
	ts, ca := tlsServer(t, false)

	c := ts.client(t, grpcclient.WithSecurity(grpcclient.Security{
		TLS:        true,
		CACert:     ca.caFile(),
		ServerName: "not-the-server",
	}))

	_, err := c.ListServices(context.Background(), nil)

	require.Error(t, err)
}

func TestDial_MutualTLS(t *testing.T) {
	ts, ca := tlsServer(t, true)
	clientCert, clientKey := ca.issue(t, "client", false)

	c := ts.client(t, grpcclient.WithSecurity(grpcclient.Security{
		TLS:        true,
		CACert:     ca.caFile(),
		ClientCert: clientCert,
		ClientKey:  clientKey,
		ServerName: tlsServerName,
	}))

	services, err := c.ListServices(context.Background(), nil)

	require.NoError(t, err)
	assert.NotEmpty(t, services)
}

func TestDial_MutualTLS_WithoutAClientCertificateFails(t *testing.T) {
	ts, ca := tlsServer(t, true)

	c := ts.client(t, grpcclient.WithSecurity(grpcclient.Security{
		TLS:        true,
		CACert:     ca.caFile(),
		ServerName: tlsServerName,
	}))

	_, err := c.ListServices(context.Background(), nil)

	require.Error(t, err)
}

// Bad TLS material fails at Dial rather than on the first call: nothing about
// a CA file that does not exist improves by waiting for an RPC.
func TestDial_TLS_RejectsUnreadableMaterial(t *testing.T) {
	ca := newCertAuthority(t)
	certPath, keyPath := ca.issue(t, tlsServerName, true)
	otherCert, _ := newCertAuthority(t).issue(t, "other", true)

	empty := filepath.Join(t.TempDir(), "empty.pem")
	require.NoError(t, os.WriteFile(empty, []byte("not a certificate"), 0o600))

	tests := []struct {
		name     string
		security grpcclient.Security
		errMsg   string
	}{
		{
			name:     "missing CA file",
			security: grpcclient.Security{TLS: true, CACert: filepath.Join(t.TempDir(), "absent.pem")},
			errMsg:   "read CA certificate",
		},
		{
			name:     "CA file holding no PEM",
			security: grpcclient.Security{TLS: true, CACert: empty},
			errMsg:   "no PEM certificates found",
		},
		{
			name:     "certificate that does not match its key",
			security: grpcclient.Security{TLS: true, ClientCert: otherCert, ClientKey: keyPath},
			errMsg:   "load client certificate",
		},
		{
			name:     "half a client certificate",
			security: grpcclient.Security{TLS: true, ClientCert: certPath},
			errMsg:   "both a client certificate and a key",
		},
	}

	ts := startTestServer(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ts.dial(t, grpcclient.WithSecurity(tt.security))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestAuth_Describe(t *testing.T) {
	tests := []struct {
		name string
		auth grpcclient.Auth
		want string
	}{
		{name: "none", want: "none"},
		{
			name: "bearer never shows the token",
			auth: grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: "super-secret"},
			want: "bearer",
		},
		{
			name: "basic names the user",
			auth: grpcclient.Auth{Kind: grpcclient.AuthBasic, Username: "alice", Password: "hunter2"},
			want: "basic (alice)",
		},
		{
			name: "basic without a user",
			auth: grpcclient.Auth{Kind: grpcclient.AuthBasic},
			want: "basic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.auth.Describe()

			assert.Equal(t, tt.want, got)
			if tt.auth.Token != "" {
				assert.NotContains(t, got, tt.auth.Token)
			}
			if tt.auth.Password != "" {
				assert.NotContains(t, got, tt.auth.Password)
			}
		})
	}
}

func TestAuth_Validate(t *testing.T) {
	tests := []struct {
		name   string
		auth   grpcclient.Auth
		errMsg string
	}{
		{name: "none"},
		{name: "bearer", auth: grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: "t"}},
		{name: "basic", auth: grpcclient.Auth{Kind: grpcclient.AuthBasic, Username: "alice"}},
		{
			name:   "bearer without a token",
			auth:   grpcclient.Auth{Kind: grpcclient.AuthBearer},
			errMsg: "needs a token",
		},
		{
			name:   "basic without a username",
			auth:   grpcclient.Auth{Kind: grpcclient.AuthBasic, Password: "p"},
			errMsg: "needs a username",
		},
		{
			name:   "unknown kind",
			auth:   grpcclient.Auth{Kind: "oauth"},
			errMsg: `unknown auth type "oauth"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.auth.Validate()

			if tt.errMsg == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

// Auth is per-connection credentials rather than a header on the call, so it
// has to reach reflection as well as the RPC the user asked for — a server
// that guards its API guards its schema too.
func TestDial_Auth_SendsCredentialsOnEveryRPC(t *testing.T) {
	tests := []struct {
		name string
		auth grpcclient.Auth
		want string
	}{
		{
			name: "bearer",
			auth: grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: "abc.def"},
			want: "Bearer abc.def",
		},
		{
			name: "bearer with the scheme already on it",
			auth: grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: "Bearer abc.def"},
			want: "Bearer abc.def",
		},
		{
			name: "basic",
			auth: grpcclient.Auth{Kind: grpcclient.AuthBasic, Username: "alice", Password: "hunter2"},
			want: "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:hunter2")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &headerRecorder{}
			ts := startTestServer(t, withHeaderCapture(rec))
			c := ts.client(t, grpcclient.WithAuth(tt.auth))

			method := healthMethod(t, c, "Check")
			_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""), nil)
			require.NoError(t, err)

			// Twice over: once for the reflection stream healthMethod opened,
			// once for the call itself.
			assert.Equal(t, []string{tt.want, tt.want}, rec.values("authorization"))
		})
	}
}

func TestDial_Auth_NoneSendsNothing(t *testing.T) {
	rec := &headerRecorder{}
	ts := startTestServer(t, withHeaderCapture(rec))
	c := ts.client(t)

	_, err := c.ListServices(context.Background(), nil)
	require.NoError(t, err)

	assert.Empty(t, rec.values("authorization"))
}

// A header typed into the metadata panel and a configured credential both end
// up as `authorization`; the call's own header must not lose to the connection's.
func TestDial_Auth_MetadataOverridesNothing(t *testing.T) {
	rec := &headerRecorder{}
	ts := startTestServer(t, withHeaderCapture(rec))
	c := ts.client(t, grpcclient.WithAuth(grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: "from-profile"}))

	method := healthMethod(t, c, "Check")
	md := grpcclient.Metadata{{Key: "authorization", Value: "Bearer from-panel"}}

	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""), md)
	require.NoError(t, err)

	assert.Contains(t, rec.values("authorization"), "Bearer from-panel")
}

func TestClient_Security(t *testing.T) {
	ts := startTestServer(t)

	c := ts.client(t, grpcclient.WithSecurity(grpcclient.Security{TLS: true, InsecureSkipVerify: true}))

	assert.Equal(t, "TLS (unverified)", c.Security().Mode())
	assert.Equal(t, bufTarget, c.Target())
}

func TestProfile_Label(t *testing.T) {
	assert.Equal(t, "staging", grpcclient.Profile{Name: "staging", Target: "a:1"}.Label())
	assert.Equal(t, "a:1", grpcclient.Profile{Target: "a:1"}.Label())
}

func TestProfile_Validate(t *testing.T) {
	tests := []struct {
		name    string
		profile grpcclient.Profile
		errMsg  string
	}{
		{
			name:    "plain target",
			profile: grpcclient.Profile{Target: "localhost:50051"},
		},
		{
			name:   "no target",
			errMsg: "no target address",
		},
		{
			name:    "bad security",
			profile: grpcclient.Profile{Target: "a:1", Security: grpcclient.Security{TLS: true, ClientKey: "k.pem"}},
			errMsg:  "both a client certificate and a key",
		},
		{
			name:    "bad auth",
			profile: grpcclient.Profile{Target: "a:1", Auth: grpcclient.Auth{Kind: grpcclient.AuthBearer}},
			errMsg:  "needs a token",
		},
		{
			name: "bad metadata",
			profile: grpcclient.Profile{
				Target:   "a:1",
				Metadata: grpcclient.Metadata{{Key: "grpc-timeout", Value: "1S"}},
			},
			errMsg: "reserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.profile.Validate()

			if tt.errMsg == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestDialProfile(t *testing.T) {
	ts := startTestServer(t)

	c, err := grpcclient.DialProfile(
		grpcclient.Profile{Name: "local", Target: bufTarget},
		grpcclient.WithGRPCDialOptions(ts.dialer()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	services, err := c.ListServices(context.Background(), nil)

	require.NoError(t, err)
	assert.NotEmpty(t, services)
}

func TestDialProfile_RejectsAnInvalidProfile(t *testing.T) {
	_, err := grpcclient.DialProfile(grpcclient.Profile{
		Name:   "staging",
		Target: "a:1",
		Auth:   grpcclient.Auth{Kind: grpcclient.AuthBearer},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `profile "staging"`)
	assert.Contains(t, err.Error(), "needs a token")
}
