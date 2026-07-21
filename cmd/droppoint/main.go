package main

import (
	"context"
	"errors"
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
	if len(args) == 0 {
		printTokenUsage(stderr)
		return 2
	}
	switch args[0] {
	case "generate":
		return runTokenGenerate(args[1:], stdout, stderr)
	case "add":
		return runTokenAdd(args[1:], stdout, stderr)
	case "list":
		return runTokenList(args[1:], stdout, stderr)
	case "disable":
		return runTokenDisable(args[1:], stdout, stderr)
	case "remove":
		return runTokenRemove(args[1:], stdout, stderr)
	default:
		printTokenUsage(stderr)
		return 2
	}
}

func runTokenGenerate(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: droppoint token generate")
		return 2
	}
	plaintext, err := token.GenerateAPIToken()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "token generation error: %v\n", err)
		return 1
	}
	hash := token.HashSecret(plaintext)
	_, _ = fmt.Fprintf(stdout, "api_token: %s\nsecret_hash: %s\n", plaintext, hash)
	return 0
}

func runTokenAdd(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("droppoint token add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to JSON configuration file")
	id := flags.String("id", "", "operator API token label")
	maxActive := flags.Int("max-active", 0, "optional per-token active drop point quota")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if *id == "" {
		_, _ = fmt.Fprintln(stderr, "token add requires --id")
		return 2
	}
	var maxActiveOverride *int
	if flagWasSet(flags, "max-active") {
		if *maxActive <= 0 {
			_, _ = fmt.Fprintln(stderr, "--max-active must be positive")
			return 2
		}
		maxActiveOverride = maxActive
	}

	ctx := context.Background()
	logger := log.New(stderr, "", log.LstdFlags|log.LUTC)
	db, repo, ok := openTokenRepository(ctx, *configPath, stderr)
	if !ok {
		return 1
	}
	defer db.Close()

	plaintext, err := token.GenerateAPIToken()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "token generation error: %v\n", err)
		return 1
	}
	if err := repo.AddAPIToken(ctx, store.AddAPITokenParams{ID: *id, SecretHash: token.HashSecret(plaintext), MaxActiveDropPoints: maxActiveOverride, CreatedAt: time.Now().UTC()}); err != nil {
		if store.IsUniqueConstraint(err) {
			_, _ = fmt.Fprintf(stderr, "api token %q already exists\n", *id)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "token add error: %v\n", err)
		return 1
	}
	logger.Printf("event=api_token.added api_token_id=%q max_active_drop_points=%s", *id, formatOptionalInt(maxActiveOverride))
	_, _ = fmt.Fprintf(stdout, "api_token: %s\nid: %s\n", plaintext, *id)
	return 0
}

func runTokenList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("droppoint token list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to JSON configuration file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}

	ctx := context.Background()
	db, repo, ok := openTokenRepository(ctx, *configPath, stderr)
	if !ok {
		return 1
	}
	defer db.Close()

	tokens, err := repo.ListAPITokens(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "token list error: %v\n", err)
		return 1
	}
	for _, apiToken := range tokens {
		_, _ = fmt.Fprintf(stdout, "id=%s enabled=%t max_active_drop_points=%s created_at=%s disabled_at=%s\n",
			apiToken.ID,
			apiToken.Enabled,
			formatOptionalInt(apiToken.MaxActiveDropPoints),
			formatOptionalTime(apiToken.CreatedAt),
			formatOptionalTimePtr(apiToken.DisabledAt),
		)
	}
	return 0
}

func runTokenDisable(args []string, stdout io.Writer, stderr io.Writer) int {
	_ = stdout
	flags := flag.NewFlagSet("droppoint token disable", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to JSON configuration file")
	id := flags.String("id", "", "operator API token label")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if *id == "" {
		_, _ = fmt.Fprintln(stderr, "token disable requires --id")
		return 2
	}

	ctx := context.Background()
	logger := log.New(stderr, "", log.LstdFlags|log.LUTC)
	db, repo, ok := openTokenRepository(ctx, *configPath, stderr)
	if !ok {
		return 1
	}
	defer db.Close()

	if err := repo.DisableAPIToken(ctx, *id, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrAPITokenNotFound) {
			_, _ = fmt.Fprintf(stderr, "api token %q not found\n", *id)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "token disable error: %v\n", err)
		return 1
	}
	logger.Printf("event=api_token.disabled api_token_id=%q", *id)
	return 0
}

func runTokenRemove(args []string, stdout io.Writer, stderr io.Writer) int {
	_ = stdout
	flags := flag.NewFlagSet("droppoint token remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to JSON configuration file")
	id := flags.String("id", "", "operator API token label")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if *id == "" {
		_, _ = fmt.Fprintln(stderr, "token remove requires --id")
		return 2
	}

	ctx := context.Background()
	logger := log.New(stderr, "", log.LstdFlags|log.LUTC)
	db, repo, ok := openTokenRepository(ctx, *configPath, stderr)
	if !ok {
		return 1
	}
	defer db.Close()

	if err := repo.RemoveAPIToken(ctx, *id); err != nil {
		if errors.Is(err, store.ErrAPITokenNotFound) {
			_, _ = fmt.Fprintf(stderr, "api token %q not found\n", *id)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "token remove error: %v\n", err)
		return 1
	}
	logger.Printf("event=api_token.removed api_token_id=%q", *id)
	return 0
}

func openTokenRepository(ctx context.Context, configPath string, stderr io.Writer) (*store.DB, *store.Repository, bool) {
	cfg, err := config.Load(configPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return nil, nil, false
	}
	if err := config.EnsureDataDir(cfg.DataDir); err != nil {
		_, _ = fmt.Fprintf(stderr, "data directory error: %v\n", err)
		return nil, nil, false
	}
	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "database error: %v\n", err)
		return nil, nil, false
	}
	repo := store.NewRepository(db.SQLDB())
	return db, repo, true
}

func flagWasSet(flags *flag.FlagSet, name string) bool {
	found := false
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			found = true
		}
	})
	return found
}

func formatOptionalInt(value *int) string {
	if value == nil {
		return "default"
	}
	return fmt.Sprintf("%d", *value)
}

func formatOptionalTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTimePtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatOptionalTime(*value)
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
	_, _ = fmt.Fprintf(stdout, "expired_drop_points=%d deleted_payloads=%d deleted_orphans=%d purged_rows=%d\n", result.ExpiredDropPoints, result.DeletedPayloads, result.DeletedOrphans, result.PurgedRows)
	return 0
}

func printTokenUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  droppoint token add --id label [--max-active n] [--config path]
  droppoint token list [--config path]
  droppoint token disable --id label [--config path]
  droppoint token remove --id label [--config path]
  droppoint token generate

`)
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  droppoint serve [--config path]
  droppoint [--config path]
  droppoint token add --id label [--max-active n] [--config path]
  droppoint token list [--config path]
  droppoint token disable --id label [--config path]
  droppoint token remove --id label [--config path]
  droppoint token generate
  droppoint cleanup expired [--config path]

Defaults:
  listen_addr: 127.0.0.1:8080
  data_dir: .data/droppoint

`)
}
