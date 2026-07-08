package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/shunichironomura/droppoint/internal/blobstore"
	"github.com/shunichironomura/droppoint/internal/cleanup"
	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/httpapi"
	"github.com/shunichironomura/droppoint/internal/logutil"
	"github.com/shunichironomura/droppoint/internal/store"
)

const (
	readHeaderTimeout       = 10 * time.Second
	idleTimeout             = 2 * time.Minute
	shutdownTimeout         = 10 * time.Second
	cleanupOperationTimeout = 30 * time.Second
)

// Server is the imperative shell that wires configuration, storage, and HTTP.
type Server struct {
	Config     config.Config
	Store      *store.DB
	Repository *store.Repository
	BlobStore  *blobstore.Store
	HTTPServer *http.Server
	logger     *log.Logger
}

// New validates cfg, initializes local durable state, and builds the HTTP
// server without binding a network listener.
func New(ctx context.Context, cfg config.Config, logger *log.Logger) (*Server, error) {
	logger = logutil.DefaultLogger(logger)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	if err := config.EnsureDataDir(cfg.DataDir); err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return nil, err
	}

	repository := store.NewRepository(db.SQLDB())
	blobStore := blobstore.New(cfg.DataDir)
	handler := httpapi.NewRouterWithDependencies(httpapi.Dependencies{Config: cfg, Repository: repository, BlobStore: blobStore, Logger: logger})
	return &Server{
		Config:     cfg,
		Store:      db,
		Repository: repository,
		BlobStore:  blobStore,
		HTTPServer: &http.Server{
			Addr:              cfg.ListenAddr,
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       secondsDuration(cfg.ReadTimeoutSeconds),
			WriteTimeout:      secondsDuration(cfg.WriteTimeoutSeconds),
			IdleTimeout:       idleTimeout,
		},
		logger: logger,
	}, nil
}

// Handler returns the configured HTTP handler for tests and embedding.
func (s *Server) Handler() http.Handler {
	if s == nil || s.HTTPServer == nil {
		return http.NotFoundHandler()
	}
	return s.HTTPServer.Handler
}

// ListenAndServe runs the HTTP server until it fails or ctx is canceled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if s == nil || s.HTTPServer == nil {
		return fmt.Errorf("server is not initialized")
	}

	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	cleanupDone := s.startCleanupLoop(cleanupCtx)
	defer func() {
		stopCleanup()
		<-cleanupDone
	}()

	errCh := make(chan error, 1)
	go func() {
		err := s.HTTPServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := s.HTTPServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown http server: %w", err)
		}
		return <-errCh
	}
}

func (s *Server) startCleanupLoop(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		service := cleanup.Service{
			Repository:        s.Repository,
			BlobStore:         s.BlobStore,
			TerminalRetention: secondsDuration(s.Config.TerminalRetentionSeconds),
		}
		run := func() {
			runCtx, cancel := context.WithTimeout(ctx, cleanupOperationTimeout)
			result, err := service.Expire(runCtx)
			cancel()
			if err != nil {
				s.logger.Printf("cleanup error: %v", err)
				return
			}
			if result.ExpiredDropPoints != 0 || result.DeletedPayloads != 0 || result.PurgedRows != 0 {
				s.logger.Printf("cleanup expired_drop_points=%d deleted_payloads=%d purged_rows=%d", result.ExpiredDropPoints, result.DeletedPayloads, result.PurgedRows)
			}
		}

		run()
		ticker := time.NewTicker(secondsDuration(s.Config.CleanupIntervalSeconds))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return done
}

func secondsDuration(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

// Close releases local resources. It does not shut down an active listener; use
// ListenAndServe's context cancellation for graceful HTTP shutdown.
func (s *Server) Close() error {
	if s == nil || s.Store == nil {
		return nil
	}
	return s.Store.Close()
}
