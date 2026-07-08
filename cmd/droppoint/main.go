package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shunichironomura/droppoint/internal/blobstore"
	"github.com/shunichironomura/droppoint/internal/cleanup"
	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/server"
	"github.com/shunichironomura/droppoint/internal/store"
	"github.com/shunichironomura/droppoint/internal/token"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			printUsage(stdout)
			return 0
		case "token":
			return runToken(args[1:], stdout, stderr)
		case "cleanup":
			return runCleanup(args[1:], stdout, stderr)
		case "serve":
			args = args[1:]
		}
	}

	flags := flag.NewFlagSet("droppoint serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to JSON configuration file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", flags.Args())
		printUsage(stderr)
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(stderr, "", log.LstdFlags|log.LUTC)
	srv, err := server.New(ctx, cfg, logger)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "startup error: %v\n", err)
		return 1
	}
	defer srv.Close()

	logger.Printf("starting droppoint listen_addr=%s data_dir=%s", cfg.ListenAddr, cfg.DataDir)
	if err := srv.ListenAndServe(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "server error: %v\n", err)
		return 1
	}
	return 0
}

func runToken(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "generate" {
		_, _ = fmt.Fprintln(stderr, "usage: droppoint token generate")
		return 2
	}
	plaintext, err := token.GenerateAPIToken()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "token generation error: %v\n", err)
		return 1
	}
	hash := token.HashSecret(plaintext)
	_, _ = fmt.Fprintf(stdout, "api_token: %s\nsecret_hash: %s\nconfig_entry: {\"id\":\"desktop-main\",\"secret_hash\":\"%s\",\"enabled\":true}\n", plaintext, hash, hash)
	return 0
}

func runCleanup(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "expired" {
		_, _ = fmt.Fprintln(stderr, "usage: droppoint cleanup expired [--config path]")
		return 2
	}
	flags := flag.NewFlagSet("droppoint cleanup expired", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to JSON configuration file")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return 1
	}
	if err := config.EnsureDataDir(cfg.DataDir); err != nil {
		_, _ = fmt.Fprintf(stderr, "data directory error: %v\n", err)
		return 1
	}
	db, err := store.Open(context.Background(), cfg.DataDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "database error: %v\n", err)
		return 1
	}
	defer db.Close()
	result, err := (cleanup.Service{Repository: store.NewRepository(db.SQLDB()), BlobStore: blobstore.New(cfg.DataDir), TerminalRetention: time.Duration(cfg.TerminalRetentionSeconds) * time.Second}).Expire(context.Background())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cleanup error: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "expired_drop_points=%d deleted_payloads=%d purged_rows=%d\n", result.ExpiredDropPoints, result.DeletedPayloads, result.PurgedRows)
	return 0
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  droppoint serve [--config path]
  droppoint [--config path]
  droppoint token generate
  droppoint cleanup expired [--config path]

Defaults:
  listen_addr: 127.0.0.1:8080
  data_dir: .data/droppoint

`)
}
