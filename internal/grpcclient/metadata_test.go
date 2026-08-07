package grpcclient_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

func TestValidateHeader(t *testing.T) {
	tests := []struct {
		name   string
		header grpcclient.Header
		errMsg string
	}{
		{
			name:   "plain header",
			header: grpcclient.Header{Key: "x-tenant", Value: "acme"},
		},
		{
			name:   "authorization",
			header: grpcclient.Header{Key: "authorization", Value: "Bearer abc.def"},
		},
		{
			name:   "dots and underscores",
			header: grpcclient.Header{Key: "x.api_key-1", Value: "v"},
		},
		{
			name:   "empty value is allowed",
			header: grpcclient.Header{Key: "x-empty"},
		},
		{
			name:   "binary header with base64",
			header: grpcclient.Header{Key: "x-trace-bin", Value: base64.StdEncoding.EncodeToString([]byte{0x00, 0xff})},
		},
		{
			name:   "binary header unpadded",
			header: grpcclient.Header{Key: "x-trace-bin", Value: base64.RawStdEncoding.EncodeToString([]byte{0x00, 0xff})},
		},
		{
			name:   "empty key",
			header: grpcclient.Header{Value: "v"},
			errMsg: "empty header key",
		},
		{
			name:   "pseudo-header",
			header: grpcclient.Header{Key: ":authority", Value: "elsewhere"},
			errMsg: "pseudo-header",
		},
		{
			name:   "grpc-prefixed key is reserved",
			header: grpcclient.Header{Key: "grpc-timeout", Value: "1S"},
			errMsg: "reserved",
		},
		{
			name:   "grpc prefix is caught whatever its case",
			header: grpcclient.Header{Key: "GRPC-Timeout", Value: "1S"},
			errMsg: "reserved",
		},
		{
			name:   "space in key",
			header: grpcclient.Header{Key: "x tenant", Value: "acme"},
			errMsg: "invalid character",
		},
		{
			name:   "colon in key",
			header: grpcclient.Header{Key: "x-tenant:", Value: "acme"},
			errMsg: "invalid character",
		},
		{
			name:   "binary header with a value that is not base64",
			header: grpcclient.Header{Key: "x-trace-bin", Value: "not base64!"},
			errMsg: "must be base64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := grpcclient.ValidateHeader(tt.header)

			if tt.errMsg == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestValidateHeader_ReservedIsMatchable(t *testing.T) {
	err := grpcclient.ValidateHeader(grpcclient.Header{Key: "grpc-status", Value: "0"})

	require.ErrorIs(t, err, grpcclient.ErrReservedHeader)
}

func TestMetadata_Validate_ReportsEveryProblem(t *testing.T) {
	md := grpcclient.Metadata{
		{Key: "x-good", Value: "1"},
		{Key: "grpc-bad", Value: "2"},
		{Key: "also bad", Value: "3"},
	}

	err := md.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "grpc-bad")
	assert.Contains(t, err.Error(), "also bad")
}

func TestMetadata_Validate_SkipsDisabled(t *testing.T) {
	md := grpcclient.Metadata{{Key: "grpc-bad", Value: "2", Disabled: true}}

	assert.NoError(t, md.Validate())
}

func TestMetadata_Enabled(t *testing.T) {
	md := grpcclient.Metadata{
		{Key: "a", Value: "1"},
		{Key: "b", Value: "2", Disabled: true},
		{Key: "c", Value: "3"},
	}

	assert.Equal(t, grpcclient.Metadata{{Key: "a", Value: "1"}, {Key: "c", Value: "3"}}, md.Enabled())
}

func TestMetadata_Keys_LowercasesAndSkipsDisabled(t *testing.T) {
	md := grpcclient.Metadata{
		{Key: "X-Tenant", Value: "acme"},
		{Key: "authorization", Value: "Bearer secret", Disabled: true},
	}

	assert.Equal(t, []string{"x-tenant"}, md.Keys())
}

func TestMetadata_Clone_IsIndependent(t *testing.T) {
	md := grpcclient.Metadata{{Key: "a", Value: "1"}}

	clone := md.Clone()
	clone[0].Value = "2"

	assert.Equal(t, "1", md[0].Value)
	assert.Nil(t, grpcclient.Metadata(nil).Clone())
}

// The headers a call is given have to arrive at the server; the rest of this
// file only proves grpctui thinks they are well-formed.
func TestClient_InvokeUnary_SendsMetadata(t *testing.T) {
	rec := &headerRecorder{}
	ts := startTestServer(t, withHeaderCapture(rec))
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	md := grpcclient.Metadata{
		{Key: "X-Tenant", Value: "acme"},
		{Key: "x-request-id", Value: "abc-123"},
		{Key: "x-parked", Value: "nope", Disabled: true},
	}

	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""), md)
	require.NoError(t, err)

	assert.Equal(t, []string{"acme"}, rec.values("x-tenant"), "key should be lowercased on the wire")
	assert.Equal(t, []string{"abc-123"}, rec.values("x-request-id"))
	assert.Empty(t, rec.values("x-parked"), "a disabled header must not be sent")
}

func TestClient_InvokeUnary_SendsRepeatedKeys(t *testing.T) {
	rec := &headerRecorder{}
	ts := startTestServer(t, withHeaderCapture(rec))
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	md := grpcclient.Metadata{
		{Key: "x-scope", Value: "read"},
		{Key: "x-scope", Value: "write"},
	}

	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""), md)
	require.NoError(t, err)

	assert.Equal(t, []string{"read", "write"}, rec.values("x-scope"))
}

// A "-bin" value is typed as base64 and travels as bytes; sending it verbatim
// would have gRPC encode it a second time and the server see the base64 text.
func TestClient_InvokeUnary_DecodesBinaryHeaders(t *testing.T) {
	rec := &headerRecorder{}
	ts := startTestServer(t, withHeaderCapture(rec))
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	raw := []byte{0x00, 0x01, 0xfe, 0xff}
	md := grpcclient.Metadata{{
		Key:   "x-trace-bin",
		Value: base64.StdEncoding.EncodeToString(raw),
	}}

	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""), md)
	require.NoError(t, err)

	assert.Equal(t, []string{string(raw)}, rec.values("x-trace-bin"))
}

func TestClient_InvokeUnary_RejectsBadMetadata(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)
	method := healthMethod(t, c, "Check")

	md := grpcclient.Metadata{{Key: "grpc-timeout", Value: "1S"}}

	_, err := c.InvokeUnary(context.Background(), method, checkRequest(t, method, ""), md)

	require.ErrorIs(t, err, grpcclient.ErrReservedHeader)
}

// Discovery is an RPC too, and a server that gates its API behind a header
// gates reflection behind the same one.
func TestClient_ListServices_SendsMetadata(t *testing.T) {
	rec := &headerRecorder{}
	ts := startTestServer(t, withHeaderCapture(rec))
	c := ts.client(t)

	_, err := c.ListServices(context.Background(), grpcclient.Metadata{{Key: "x-api-key", Value: "sesame"}})
	require.NoError(t, err)

	assert.Equal(t, []string{"sesame"}, rec.values("x-api-key"))
}

func TestClient_ListServices_RejectsBadMetadata(t *testing.T) {
	ts := startTestServer(t)
	c := ts.client(t)

	_, err := c.ListServices(context.Background(), grpcclient.Metadata{{Key: "bad key", Value: "v"}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid character")
}
