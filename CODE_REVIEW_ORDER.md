# Code Review File Order

Date: 2026-07-01

This document lists repository files in dependency-first review order. Local scratch files under `.local/`, generated runtime data under `.data/`, VCS metadata under `.git/`, and build outputs are intentionally excluded from the codebase review order.

## Topological library-consumer order

1. `SPEC.md`
   - Product, API, lifecycle, data, and encryption protocol specification.

2. `PLAN.md`
   - Phased implementation plan derived from the design notes and `SPEC.md`.

3. `AGENTS.md`
   - Agent instructions, including the reminder to maintain this review-order document.

4. `CLAUDE.md`
   - Symlink to `AGENTS.md` for Claude-compatible agent instruction discovery.

5. `go.mod`
   - Go module declaration and direct dependency list.

6. `go.sum`
   - Go dependency checksums.

7. `config.example.json`
   - Example operator configuration using the canonical `/var/lib/drop-point` data directory.

8. `internal/config/config.go`
   - JSON configuration types, defaults, loading, and validation.

9. `internal/config/datadir.go`
   - Data directory creation and restrictive permission enforcement.

10. `internal/config/config_test.go`
    - Config loading, validation, and data-directory tests.

11. `internal/token/token.go`
    - High-entropy token generation, hashing, and constant-time hash comparison helpers.

12. `internal/token/token_test.go`
    - Token prefix, entropy, base64url, uniqueness-shape, and hash-format tests.

13. `internal/droppoint/droppoint.go`
    - Functional drop point domain entity, lifecycle statuses, errors, request/result types, and pure transition rules.

14. `internal/droppoint/droppoint_test.go`
    - Domain lifecycle transition acceptance and rejection tests.

15. `internal/store/migrate.go`
    - Idempotent SQLite schema migration for `drop_points`.

16. `internal/store/store.go`
    - SQLite opening, runtime PRAGMA configuration, and database handle ownership.

17. `internal/store/repository.go`
    - SQLite drop point repository methods for lifecycle, token authorization, quota counting, and file-pointer cleanup.

18. `internal/store/store_test.go`
    - SQLite initialization and schema tests.

19. `internal/store/repository_test.go`
    - Repository create, lookup, token mismatch, quota, close, expiry, pickup timestamp, and receiving-reset tests.

20. `internal/httpapi/health.go`
    - Low-information `/health` handler.

21. `internal/httpapi/middleware.go`
    - Request logging, token-path redaction, and panic recovery middleware.

22. `internal/httpapi/router.go`
    - HTTP route assembly.

23. `internal/httpapi/router_test.go`
    - Health, method rejection, redaction, and recovery tests.

24. `internal/server/server.go`
    - Imperative shell wiring config, data directory, SQLite, and HTTP server.

25. `internal/server/server_test.go`
    - Server initialization and health routing tests.

26. `cmd/drop-point/main.go`
    - `drop-point` CLI entrypoint and default `serve` command.

27. `CODE_REVIEW_ORDER.md`
    - This review-order index. Update it whenever repository files change.
