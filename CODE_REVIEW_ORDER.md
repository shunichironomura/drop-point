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

11. `internal/store/migrate.go`
    - Idempotent SQLite schema migration for `drop_points`.

12. `internal/store/store.go`
    - SQLite opening, runtime PRAGMA configuration, and database handle ownership.

13. `internal/store/store_test.go`
    - SQLite initialization and schema tests.

14. `internal/httpapi/health.go`
    - Low-information `/health` handler.

15. `internal/httpapi/middleware.go`
    - Request logging, token-path redaction, and panic recovery middleware.

16. `internal/httpapi/router.go`
    - HTTP route assembly.

17. `internal/httpapi/router_test.go`
    - Health, method rejection, redaction, and recovery tests.

18. `internal/server/server.go`
    - Imperative shell wiring config, data directory, SQLite, and HTTP server.

19. `internal/server/server_test.go`
    - Server initialization and health routing tests.

20. `cmd/drop-point/main.go`
    - `drop-point` CLI entrypoint and default `serve` command.

21. `CODE_REVIEW_ORDER.md`
    - This review-order index. Update it whenever repository files change.
