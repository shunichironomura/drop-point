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

8. `internal/domain/domain.go`
   - Functional core: the drop point entity, typed create/commit parameters
     with validation, and the explicit domain error set.

9. `internal/domain/status.go`
   - The drop point status values and the `CanTransition` state machine
     (SPEC §5).

10. `internal/domain/domain_test.go`
    - Tests for entity helpers and create/commit parameter validation.

11. `internal/domain/status_test.go`
    - Exhaustive tests for allowed and rejected status transitions.

12. `internal/token/token.go`
    - Prefixed identifier and capability-token generation, at-rest hashing, and
      constant-time hash comparison (SPEC §6).

13. `internal/token/token_test.go`
    - Tests for token prefixes, entropy length, base64url encoding, uniqueness,
      and hash format.

14. `internal/storage/storage.go`
    - Data directory initialization and the SQLite connection lifecycle
      (WAL, foreign keys, busy timeout).

15. `internal/storage/migrate.go`
    - Ordered, versioned schema migrations, including the `drop_points` table.

16. `internal/storage/droppoints.go`
    - Drop point repository methods on `Store`: create, lookup, pickup
      authorization, receiving/commit/abort, pickup timestamping, close,
      expiry, and active-quota counting.

17. `internal/storage/storage_test.go`
    - Tests for data dir permissions, connection pragmas, migration
      idempotency, and schema constraints.

18. `internal/storage/droppoints_test.go`
    - Tests for create/lookup, cross-drop-point pickup authorization, the
      single-use slot and one-drop race, expiry, and pickup timestamps.

19. `internal/server/server.go`
    - HTTP router and the unauthenticated, low-information `/health` handler.

20. `internal/server/middleware.go`
    - Request logging, panic recovery, and token-path redaction.

21. `internal/server/server_test.go`
    - Tests for the health endpoint and route/method handling.

22. `internal/server/middleware_test.go`
    - Tests for path redaction, log redaction, and panic recovery.

### Executable entrypoint (imperative shell)

23. `cmd/drop-point/main.go`
    - The `drop-point` binary: loads config, opens storage, serves HTTP, and
      shuts down gracefully.

### Operator and agent documentation

24. `CODE_REVIEW_ORDER.md`
    - This review-order index. Update it whenever repository files change.

25. `AGENTS.md`
    - Agent instructions, including the reminder to maintain this review-order
      document.

26. `CLAUDE.md`
    - Symlink to `AGENTS.md` for Claude-compatible agent instruction discovery.
