package demoapi_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/alonshuld/grpctui/internal/demoapi"
	"github.com/alonshuld/grpctui/internal/grpcclient"
)

const bufSize = 1024 * 1024

// dial starts the demo API on an in-memory listener with reflection on, and
// returns a grpctui client pointed at it. The client is the real one on
// purpose: what this fixture has to be is a server grpctui can discover and
// call, so the test discovers and calls it exactly as the TUI would.
func dial(t *testing.T) *grpcclient.Client {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	require.NoError(t, demoapi.Register(srv))
	reflection.Register(srv)

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// passthrough:// keeps grpc from resolving "bufnet" as a DNS name, which is
	// what grpc.NewClient does by default and what the context dialer is
	// standing in for.
	client, err := grpcclient.Dial("passthrough:///bufnet", grpcclient.WithGRPCDialOptions(
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// method finds one method of the discovered schema by its fully-qualified name.
func method(t *testing.T, client *grpcclient.Client, full string) grpcclient.Method {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	services, err := client.ListServices(ctx, nil)
	require.NoError(t, err)

	for _, svc := range services {
		for _, m := range svc.Methods {
			if m.FullName == full {
				return m
			}
		}
	}

	t.Fatalf("no method %q in the discovered schema", full)
	return grpcclient.Method{}
}

// field reads a named field off a response message, so an assertion can name
// the field rather than walk the reflection API.
func field(t *testing.T, msg protoreflect.ProtoMessage, name string) protoreflect.Value {
	t.Helper()

	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "no field %q on %s", name, m.Descriptor().FullName())

	return m.Get(fd)
}

func TestRegister_reflectionReportsEveryShape(t *testing.T) {
	client := dial(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	services, err := client.ListServices(ctx, nil)
	require.NoError(t, err)

	shapes := map[string][2]bool{}
	for _, svc := range services {
		for _, m := range svc.Methods {
			shapes[m.FullName] = [2]bool{m.ClientStreaming, m.ServerStreaming}
		}
	}

	// All four call shapes, so the tapes have one of each to record and the
	// tree has something to tag «stream».
	want := map[string][2]bool{
		"demo.v1.UserService.GetUser":        {false, false},
		"demo.v1.UserService.ListUsers":      {false, true},
		"demo.v1.OrderService.ImportOrders":  {true, false},
		"demo.v1.OrderService.TrackShipment": {true, true},
		"demo.v1.InventoryService.GetStock":  {false, false},
		"demo.v1.OrderService.WatchOrders":   {false, true},
		"demo.v1.UserService.CreateUser":     {false, false},
		"demo.v1.OrderService.GetOrder":      {false, false},
	}
	for full, shape := range want {
		assert.Equal(t, shape, shapes[full], "shape of %s", full)
	}
}

func TestRegister_unaryAnswersItsCannedReply(t *testing.T) {
	client := dial(t)
	m := method(t, client, "demo.v1.UserService.GetUser")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req := dynamicpb.NewMessage(m.Descriptor.Input())
	resp, err := client.InvokeUnary(ctx, m, req, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.Message)

	assert.Equal(t, "usr_8f21c4", field(t, resp.Message, "id").String())
	assert.Equal(t, "Ada Lovelace", field(t, resp.Message, "display_name").String())

	// The nested message and the map are what make this worth recording: a
	// reply that was one string field would show the response panel nothing.
	address := field(t, resp.Message, "address").Message()
	assert.Equal(t, "London", address.Get(address.Descriptor().Fields().ByName("city")).String())
	assert.Equal(t, 2, field(t, resp.Message, "labels").Map().Len())
}

func TestRegister_timestampsAreRecent(t *testing.T) {
	client := dial(t)
	m := method(t, client, "demo.v1.UserService.CreateUser")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.InvokeUnary(ctx, m, dynamicpb.NewMessage(m.Descriptor.Input()), nil)
	require.NoError(t, err)

	created := field(t, resp.Message, "created_at").Message()
	seconds := created.Get(created.Descriptor().Fields().ByName("seconds")).Int()

	// A canned timestamp written into the source would render as "2 years ago"
	// soon enough, which is a poor showing for a renderer whose job is that
	// gloss — so the replies carry a token that is expanded when they are sent.
	assert.WithinDuration(t, time.Now(), time.Unix(seconds, 0), time.Minute)
}

func TestRegister_serverStreamingSendsEveryReply(t *testing.T) {
	client := dial(t)
	m := method(t, client, "demo.v1.UserService.ListUsers")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.InvokeStream(ctx, m, nil)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	require.NoError(t, stream.Send(dynamicpb.NewMessage(m.Descriptor.Input())))
	require.NoError(t, stream.CloseSend())

	var got []string
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		got = append(got, field(t, msg, "id").String())
	}

	assert.Equal(t, []string{"usr_8f21c4", "usr_1b90de", "usr_c40a77"}, got)
}

func TestRegister_aWatchDoesNotEndOnItsOwn(t *testing.T) {
	client := dial(t)
	m := method(t, client, "demo.v1.OrderService.WatchOrders")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.InvokeStream(ctx, m, nil)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	require.NoError(t, stream.Send(dynamicpb.NewMessage(m.Descriptor.Input())))
	require.NoError(t, stream.CloseSend())

	// One more than there are canned replies: a watch wraps round rather than
	// finishing, which is what makes it worth pressing esc at.
	for range len(demoapiWatchReplies) + 1 {
		msg, err := stream.Recv()
		require.NoError(t, err)
		require.NotNil(t, msg)
	}
}

// demoapiWatchReplies is how many replies WatchOrders cycles through. It is
// written out rather than read from the package, which does not export its
// canned data — and a test that read it could not tell a wrap from a stop.
var demoapiWatchReplies = [4]struct{}{}

func TestRegister_clientStreamingCountsWhatArrived(t *testing.T) {
	client := dial(t)
	m := method(t, client, "demo.v1.OrderService.ImportOrders")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.InvokeStream(ctx, m, nil)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	const sent = 3
	for range sent {
		require.NoError(t, stream.Send(dynamicpb.NewMessage(m.Descriptor.Input())))
	}
	require.NoError(t, stream.CloseSend())

	msg, err := stream.Recv()
	require.NoError(t, err)

	// The summary counts the messages that actually arrived: a client-streaming
	// demo whose answer ignored the call would not show that the sends worked.
	assert.Equal(t, int32(sent), int32(field(t, msg, "accepted").Int()))
}

func TestRegister_bidiAnswersEachMessage(t *testing.T) {
	client := dial(t)
	m := method(t, client, "demo.v1.OrderService.TrackShipment")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.InvokeStream(ctx, m, nil)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	var statuses []string
	for range 2 {
		require.NoError(t, stream.Send(dynamicpb.NewMessage(m.Descriptor.Input())))

		msg, err := stream.Recv()
		require.NoError(t, err)
		statuses = append(statuses, field(t, msg, "status").String())
	}

	assert.Equal(t, []string{"in transit", "out for delivery"}, statuses)
}

func TestServices_namesEveryRegisteredService(t *testing.T) {
	services, err := demoapi.Services()
	require.NoError(t, err)

	assert.Equal(t, []string{
		"demo.v1.UserService",
		"demo.v1.OrderService",
		"demo.v1.InventoryService",
	}, services)
}
