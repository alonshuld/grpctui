// Command demoserver serves the made-up API in internal/demoapi, plus the gRPC
// health service, with reflection switched on.
//
// It is what the tapes in docs/demos record against, and it is worth running
// for its own sake: it is the fastest way to see what grpctui does without
// pointing it at something of yours.
//
//	go run ./cmd/demoserver &
//	go run ./cmd/grpctui localhost:50051
//
// It is not part of the release. GoReleaser builds ./cmd/grpctui and nothing
// else, and this server answers every call with a canned reply — it is a
// fixture, not a service.
package main

import (
	"errors"
	"flag"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/alonshuld/grpctui/internal/demoapi"
)

// flapEvery is how often the health service changes what it reports. A
// server-streaming call needs something to say after its first message, and
// grpc.health.v1.Health.Watch is the one streaming method every server has.
const flapEvery = 6 * time.Second

func main() {
	os.Exit(cli())
}

// cli is main with a return value, so that the logger still gets flushed on the
// way out: os.Exit does not run deferred functions.
func cli() int {
	addr := flag.String("addr", "localhost:50051", "address to listen on")
	flag.Parse()

	logger, err := zap.NewDevelopment()
	if err != nil {
		// The logger is what failed, so there is nothing else to report it
		// through. This server owns its terminal — unlike grpctui itself, which
		// may never write to one.
		_, _ = os.Stderr.WriteString("build logger: " + err.Error() + "\n")
		return 1
	}
	defer func() { _ = logger.Sync() }()

	if err := run(*addr, logger); err != nil {
		logger.Error("demo server stopped", zap.Error(err))
		return 1
	}
	return 0
}

func run(addr string, logger *zap.Logger) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	srv := grpc.NewServer()
	if err := demoapi.Register(srv); err != nil {
		return err
	}

	checker := health.NewServer()
	checker.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	checker.SetServingStatus("grpc.health.v1.Health", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, checker)
	reflection.Register(srv)

	done := make(chan struct{})
	defer close(done)
	go flap(checker, done)

	go stopOnSignal(srv, logger)

	services, err := demoapi.Services()
	if err != nil {
		return err
	}
	logger.Info("serving", zap.String("addr", lis.Addr().String()), zap.Strings("services", services))

	if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}

// flap dips the health status briefly and restores it, on a duty cycle chosen
// so that a unary Check almost always reads SERVING while a Watch still sees
// the status change within a few seconds.
func flap(checker *health.Server, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-time.After(flapEvery):
		}

		checker.SetServingStatus("grpc.health.v1.Health", healthpb.HealthCheckResponse_NOT_SERVING)

		select {
		case <-done:
			return
		case <-time.After(flapEvery / 4):
		}

		checker.SetServingStatus("grpc.health.v1.Health", healthpb.HealthCheckResponse_SERVING)
	}
}

func stopOnSignal(srv *grpc.Server, logger *zap.Logger) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	sig := <-signals
	logger.Info("stopping", zap.String("signal", sig.String()))
	srv.GracefulStop()
}
