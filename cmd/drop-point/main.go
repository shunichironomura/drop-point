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

	"github.com/shunichironomura/drop-point/internal/config"
	"github.com/shunichironomura/drop-point/internal/server"
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
		case "serve":
			args = args[1:]
		}
	}

	flags := flag.NewFlagSet("drop-point serve", flag.ContinueOnError)
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

	logger.Printf("starting drop-point listen_addr=%s data_dir=%s", cfg.ListenAddr, cfg.DataDir)
	if err := srv.ListenAndServe(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "server error: %v\n", err)
		return 1
	}
	return 0
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  drop-point serve [--config path]
  drop-point [--config path]

Defaults:
  listen_addr: 127.0.0.1:8080
  data_dir: .data/drop-point

`)
}
