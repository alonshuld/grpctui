package export_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/export"
	"github.com/alonshuld/grpctui/internal/grpcclient"
)

func TestGrpcurlPlaintext(t *testing.T) {
	got := export.Grpcurl(export.Command{
		Target: "localhost:50051",
		Method: "helloworld.Greeter.SayHello",
		Body:   "{\n  \"name\": \"world\"\n}",
	})

	assert.Equal(t, strings.Join([]string{
		"# grpctui does not copy credentials out, so header values below are placeholders.",
		"grpcurl \\",
		`  -plaintext \`,
		`  -d '{"name":"world"}' \`,
		"  localhost:50051 helloworld.Greeter/SayHello",
	}, "\n"), got)
}

func TestGrpcurlTLS(t *testing.T) {
	got := export.Grpcurl(export.Command{
		Target: "api.example.com:443",
		Method: "helloworld.Greeter.SayHello",
		Body:   "{}",
		Security: grpcclient.Security{
			TLS:                true,
			CACert:             "/etc/ssl/ca.pem",
			ClientCert:         "/etc/ssl/client.pem",
			ClientKey:          "/etc/ssl/client.key",
			ServerName:         "internal.example.com",
			InsecureSkipVerify: true,
		},
	})

	assert.NotContains(t, got, "-plaintext")
	assert.Contains(t, got, "-insecure")
	assert.Contains(t, got, "-cacert /etc/ssl/ca.pem")
	assert.Contains(t, got, "-cert /etc/ssl/client.pem")
	assert.Contains(t, got, "-key /etc/ssl/client.key")
	assert.Contains(t, got, "-servername internal.example.com")
}

// The whole point of the package: a rendered command must be safe to paste into
// a bug report. Nothing secret may survive the round trip.
func TestGrpcurlNeverExportsACredential(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiJ9.super-secret"
	const password = "hunter2"

	got := export.Grpcurl(export.Command{
		Target:  "localhost:50051",
		Method:  "helloworld.Greeter.SayHello",
		Body:    "{}",
		Headers: []string{"authorization", "x-tenant"},
		Auth:    grpcclient.Auth{Kind: grpcclient.AuthBearer, Token: token},
	})

	assert.NotContains(t, got, token)
	assert.NotContains(t, got, password)
	assert.Contains(t, got, "Bearer <token>")
	assert.Contains(t, got, "x-tenant: <x-tenant>")

	// The connection's own credential renders the authorization header, so the
	// panel's header of the same name must not render a second one.
	assert.Equal(t, 1, strings.Count(got, "authorization:"))
}

func TestGrpcurlBasicAuth(t *testing.T) {
	got := export.Grpcurl(export.Command{
		Target: "localhost:50051",
		Method: "helloworld.Greeter.SayHello",
		Auth:   grpcclient.Auth{Kind: grpcclient.AuthBasic, Username: "alice", Password: "hunter2"},
	})

	assert.Contains(t, got, "Basic <base64 of alice:password>")
	assert.NotContains(t, got, "hunter2")
}

func TestGrpcurlBasicAuthWithoutUsername(t *testing.T) {
	got := export.Grpcurl(export.Command{
		Target: "localhost:50051",
		Method: "helloworld.Greeter.SayHello",
		Auth:   grpcclient.Auth{Kind: grpcclient.AuthBasic, Password: "hunter2"},
	})
	assert.Contains(t, got, "Basic <base64 of <username>:password>")
}

// A {{variable}} is exported as written. Expanding one would put the token a
// login response returned straight into the command.
func TestGrpcurlKeepsVariableReferences(t *testing.T) {
	got := export.Grpcurl(export.Command{
		Target: "localhost:50051",
		Method: "helloworld.Greeter.SayHello",
		Body:   `{"name": "{{user}}"}`,
		Values: map[string]string{"id": "{{user_id}}", "account": "{{account_id}}"},
	})

	assert.Contains(t, got, `"name":"{{user}}"`)

	lines := strings.Split(got, "\n")
	require.GreaterOrEqual(t, len(lines), 4)
	assert.Equal(t, "#   account = {{account_id}}", lines[2], "sorted, so the output is stable")
	assert.Equal(t, "#   id = {{user_id}}", lines[3])
}

func TestGrpcurlEmptyBody(t *testing.T) {
	got := export.Grpcurl(export.Command{
		Target: "localhost:50051",
		Method: "helloworld.Greeter.SayHello",
	})
	assert.NotContains(t, got, "-d")
}

func TestGrpcurlQuoting(t *testing.T) {
	tests := []struct {
		name string
		cmd  export.Command
		want string
	}{
		{
			name: "a plain target is left bare",
			cmd:  export.Command{Target: "localhost:50051", Method: "a.B.C"},
			want: "localhost:50051 a.B/C",
		},
		{
			name: "a target with a space is quoted",
			cmd:  export.Command{Target: "local host:1", Method: "a.B.C"},
			want: "'local host:1' a.B/C",
		},
		{
			name: "a single quote in the body is escaped",
			cmd:  export.Command{Target: "h:1", Method: "a.B.C", Body: `{"name":"it's"}`},
			want: `-d '{"name":"it'\''s"}'`,
		},
		{
			name: "a method with no package keeps its name",
			cmd:  export.Command{Target: "h:1", Method: "Bare"},
			want: "h:1 Bare",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Contains(t, export.Grpcurl(tt.cmd), tt.want)
		})
	}
}

// Whitespace inside a string literal belongs to the value, not to the
// indentation, and squeezing it would change what the request says.
func TestGrpcurlKeepsWhitespaceInsideStrings(t *testing.T) {
	got := export.Grpcurl(export.Command{
		Target: "h:1",
		Method: "a.B.C",
		Body:   "{\n  \"greeting\": \"hello  world\",\n  \"escaped\": \"a\\\"b\"\n}",
	})
	assert.Contains(t, got, `{"greeting":"hello  world","escaped":"a\"b"}`)
}

func TestGrpcurlDisabledHeaderNamesAreNotRendered(t *testing.T) {
	got := export.Grpcurl(export.Command{
		Target:  "h:1",
		Method:  "a.B.C",
		Headers: []string{"", "  ", "X-Tenant"},
	})

	assert.Contains(t, got, "x-tenant: <x-tenant>", "names are lowercased, as they are on the wire")
	assert.Equal(t, 1, strings.Count(got, "-H "))
}
