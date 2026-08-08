package grpcclient_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// TestListServices_FromSchema pins the .proto-file fallback: a client dialed
// with descriptors answers discovery from them and never asks the server.
//
// The target here is deliberately one that does not exist. That is the whole
// point of the feature's shape — the schema is what the *files* say the server
// offers, and reporting it as unavailable because the server is asleep would
// hide the schema the user supplied for precisely that situation.
func TestListServices_FromSchema(t *testing.T) {
	t.Parallel()

	sd := streamerFile().Services().Get(0)

	client, err := grpcclient.Dial("127.0.0.1:1", grpcclient.WithSchema(
		[]protoreflect.ServiceDescriptor{sd}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	services, err := client.ListServices(context.Background(), nil)
	require.NoError(t, err)

	require.Len(t, services, 1)
	assert.Equal(t, streamerService, services[0].Name)
	require.Len(t, services[0].Methods, 4)

	// The methods come back shaped exactly as reflection would have shaped
	// them, which is what lets every layer above stay unchanged.
	byName := make(map[string]grpcclient.Method, len(services[0].Methods))
	for _, m := range services[0].Methods {
		byName[m.Name] = m
	}

	assert.Equal(t, grpcclient.KindServerStreaming, byName["Ticks"].Kind())
	assert.Equal(t, grpcclient.KindClientStreaming, byName["Collect"].Kind())
	assert.Equal(t, grpcclient.KindBidiStreaming, byName["Chat"].Kind())
	assert.Equal(t, streamerService+".Ticks", byName["Ticks"].FullName)
	assert.NotNil(t, byName["Ticks"].InputDescriptor())
}

// TestListServices_FromSchema_IgnoresMetadata pins that discovery from files
// does no I/O at all: a header that a server would have rejected changes
// nothing, because no server is asked.
func TestListServices_FromSchema_IgnoresMetadata(t *testing.T) {
	t.Parallel()

	client, err := grpcclient.Dial("127.0.0.1:1", grpcclient.WithSchema(
		[]protoreflect.ServiceDescriptor{streamerFile().Services().Get(0)}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Even a cancelled context yields the schema, since nothing is dialled.
	services, err := client.ListServices(ctx, grpcclient.Metadata{{Key: "x-any", Value: "thing"}})
	require.NoError(t, err)
	assert.Len(t, services, 1)
}

// TestListServices_WithoutSchema pins that a client dialed without one still
// asks the server, which is grpctui's normal path.
func TestListServices_WithoutSchema(t *testing.T) {
	t.Parallel()

	client := startTestServer(t).client(t)

	services, err := client.ListServices(context.Background(), nil)
	require.NoError(t, err)
	assert.NotEmpty(t, services)
}
