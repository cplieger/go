// Command ssetest serves an internal fixture.Fixture for the TypeScript integration
// suites. It binds -addr (default 127.0.0.1:0), prints "LISTEN <url>" on
// stdout once bound, and serves until SIGINT or SIGTERM.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/go/sse/v2/internal/fixture"
)

const shutdownGrace = 5 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ssetest:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:0", "listen address; port 0 picks a free port")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *addr)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fx := fixture.NewFixture(sse.WithLogger(logger))
	srv := httpserver.New(fx.Handler())

	fmt.Fprintf(os.Stdout, "LISTEN http://%s\n", ln.Addr())
	return httpserver.Run(ctx, srv, ln, nil,
		httpserver.WithShutdownGrace(shutdownGrace),
		httpserver.WithPreDrain(func(ctx context.Context) { _ = fx.Shutdown(ctx) }),
	)
}
