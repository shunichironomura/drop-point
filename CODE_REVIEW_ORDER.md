# Code Review File Order

Date: 2026-06-30

This document lists repository files in dependency-first review order. Local
scratch files under `.local/`, generated runtime data under `.data/`, VCS
metadata under `.git/`, and build outputs (the `drop-point` binary) are
intentionally excluded from the codebase review order.

## Topological library-consumer order

### Foundations and dependency declarations

1. `SPEC.md`
   - Product, API, lifecycle, data, and encryption protocol specification.

2. `PLAN.md`
   - Phased implementation plan derived from the design notes and `SPEC.md`.

3. `go.mod`
   - Go module path and dependency declarations.

4. `go.sum`
   - Dependency checksums for the module graph.

5. `.gitignore`
   - Excludes the built binary, the default runtime data directory (`.data/`),
     and local scratch (`.local/`).

### Internal libraries (functional/imperative-shell building blocks)

6. `internal/config/config.go`
   - Relay configuration types, built-in defaults, JSON loading, and validation.

7. `internal/config/config_test.go`
   - Tests for defaults, file loading/override, unknown-field rejection, and
     validation rules.

8. `internal/storage/storage.go`
   - Data directory initialization and the SQLite connection lifecycle
     (WAL, foreign keys, busy timeout).

9. `internal/storage/migrate.go`
   - Ordered, versioned schema migrations, including the `drop_points` table.

10. `internal/storage/storage_test.go`
    - Tests for data dir permissions, connection pragmas, migration
      idempotency, and schema constraints.

11. `internal/server/server.go`
    - HTTP router and the unauthenticated, low-information `/health` handler.

12. `internal/server/middleware.go`
    - Request logging, panic recovery, and token-path redaction.

13. `internal/server/server_test.go`
    - Tests for the health endpoint and route/method handling.

14. `internal/server/middleware_test.go`
    - Tests for path redaction, log redaction, and panic recovery.

### Executable entrypoint (imperative shell)

15. `cmd/drop-point/main.go`
    - The `drop-point` binary: loads config, opens storage, serves HTTP, and
      shuts down gracefully.

### Operator and agent documentation

16. `CODE_REVIEW_ORDER.md`
    - This review-order index. Update it whenever repository files change.

17. `AGENTS.md`
    - Agent instructions, including the reminder to maintain this review-order
      document.

18. `CLAUDE.md`
    - Symlink to `AGENTS.md` for Claude-compatible agent instruction discovery.
