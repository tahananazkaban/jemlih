// Command server runs the fleet service as a long-running HTTP server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tahananazkaban/jemlih/pkg/awsx"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/services/fleet/internal/app"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local server and exit, for container health checks")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	addr := env.String("HTTP_ADDR", ":8080")

	if *healthcheck {
		os.Exit(probe(addr))
	}
	if err := run(addr); err != nil {
		slog.Error("fleet stopped", "err", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	awsCfg, err := awsx.Load(ctx)
	if err != nil {
		return err
	}
	handler, err := app.New(app.ConfigFromEnv(awsCfg))
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	slog.Info("fleet listening", "addr", addr)

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// probe exits 0 when /healthz answers 200; distroless images have no curl.
func probe(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
