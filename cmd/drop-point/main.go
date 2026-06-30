// Command drop-point runs the DropPoint relay HTTP service.
//
// DropPoint is a temporary encrypted file handoff relay: a receiver creates a
// short-lived drop point, a sender drops an encrypted bundle, and the receiver
// later picks it up. The relay stores ciphertext only. See SPEC.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shunichironomura/drop-point/internal/config"
	"github.com/shunichironomura/drop-point/internal/server"
	"github.com/shunichironomura/drop-point/internal/storage"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "drop-point: %v\n", err)
		os.Exit(1)
	}
}

// run wires the service together and blocks until the server stops. It is
// separated from main so the startup path can be exercised without os.Exit.
func run(args []string, logw io.Writer) error {
	fs := flag.NewFlagSet("drop-point", flag.ContinueOnError)
	fs.SetOutput(logw)
	configPath := fs.String("config", "", "path to JSON configuration file (defaults are used when empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(logw, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	store, err := storage.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.New(logger),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("starting",
			slog.String("listen_addr", cfg.ListenAddr),
			slog.String("data_dir", cfg.DataDir),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}
