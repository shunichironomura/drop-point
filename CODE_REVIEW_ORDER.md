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

5. `.gitignore`
   - Local Python bytecode and virtual-environment ignore rules for helper scripts.

6. `go.mod`
   - Go module declaration and direct dependency list.

7. `go.sum`
   - Go dependency checksums.

8. `config.example.json`
   - Example operator configuration using the canonical `/var/lib/drop-point` data directory.

9. `internal/config/config.go`
   - JSON configuration types, defaults, loading, and validation.

10. `internal/config/datadir.go`
   - Data directory creation and restrictive permission enforcement.

11. `internal/config/config_test.go`
    - Config loading, validation, and data-directory tests.

12. `internal/token/token.go`
    - High-entropy token generation, hashing, and constant-time hash comparison helpers.

13. `internal/token/token_test.go`
    - Token prefix, entropy, base64url, uniqueness-shape, and hash-format tests.

14. `internal/droppoint/droppoint.go`
    - Functional drop point domain entity, lifecycle statuses, errors, request/result types, and pure transition rules.

15. `internal/droppoint/droppoint_test.go`
    - Domain lifecycle transition acceptance and rejection tests.

16. `internal/store/migrate.go`
    - Idempotent SQLite schema migration for `drop_points`.

17. `internal/store/store.go`
    - SQLite opening, runtime PRAGMA configuration, and database handle ownership.

18. `internal/store/repository.go`
    - SQLite drop point repository methods for lifecycle, token authorization, quota counting, and file-pointer cleanup.

19. `internal/store/store_test.go`
    - SQLite initialization and schema tests.

20. `internal/store/repository_test.go`
    - Repository create, lookup, token mismatch, quota, close, expiry, pickup timestamp, and receiving-reset tests.

21. `internal/blobstore/blobstore.go`
    - Filesystem encrypted payload/envelope storage with atomic writes, exact-byte reads, fsync, rename, and idempotent deletion.

22. `internal/blobstore/blobstore_test.go`
    - Blob storage exact-byte write/read, oversize, and idempotent deletion tests.

23. `internal/cleanup/cleanup.go`
    - Expiry cleanup service that marks expired drop points and deletes payload directories idempotently.

24. `internal/cleanup/cleanup_test.go`
    - Cleanup tests for expired ready/open drop points and repeated runs.

25. `internal/cryptoenv/envelope.go`
    - Relay-side protocol envelope schema validation and base64url helpers.

26. `internal/cryptoenv/manifest.go`
    - Bundle manifest building, parsing, validation, payload splitting, filename sanitization, and MIME sanitization.

27. `internal/cryptoenv/reference.go`
    - X25519/HKDF/AES-GCM reference encrypt/decrypt implementation outside the relay path.

28. `internal/cryptoenv/vectors.go`
    - Deterministic positive protocol test-vector generation.

29. `internal/cryptoenv/envelope_test.go`
    - Envelope shape, algorithm, length, padding, and unknown-field validation tests.

30. `internal/cryptoenv/reference_test.go`
    - Reference round-trip, negative vector rejection, manifest validation, and sanitization tests.

31. `docs/protocol-reference.md`
    - Protocol implementation pointers and deterministic positive/negative test-vector documentation.

32. `web/drop-page/index.html`
    - Sender-facing mobile-friendly drop page shell and canonical UI copy.

33. `web/drop-page/styles.css`
    - Sender-facing drop page styles.

34. `web/drop-page/app.js`
    - Browser WebCrypto X25519/HKDF/AES-GCM bundle encryption and multipart encrypted drop submission.

35. `web/drop-page/assets.go`
    - Embedded static asset filesystem for the drop page.

36. `internal/httpapi/health.go`
    - Low-information `/health` handler.

37. `internal/httpapi/responses.go`
    - Shared JSON response and error helpers for API handlers.

38. `internal/httpapi/auth.go`
    - Bearer API token parsing and configured token-hash authentication.

39. `internal/httpapi/create.go`
    - Authenticated drop point creation handler, request validation, quota enforcement, and drop-link construction.

40. `internal/httpapi/receiver.go`
    - Pickup-token authorization, receiver status, close API handlers, and blob-store interface.

41. `internal/httpapi/drop.go`
    - Encrypted multipart drop endpoint, envelope validation, streaming size enforcement, and ready-state commit handling.

42. `internal/httpapi/pickup.go`
    - Multipart encrypted pickup endpoint and first-pickup timestamp recording.

43. `internal/httpapi/drop_page.go`
    - Sender-facing drop page and same-origin asset HTTP handlers with strict security headers.

44. `internal/httpapi/cors.go`
    - Same-origin CORS/preflight policy for browser requests while preserving non-browser bearer-token clients.

45. `internal/httpapi/middleware.go`
    - Request logging, token-path redaction, and panic recovery middleware.

46. `internal/httpapi/router.go`
    - HTTP route assembly and dependency injection.

47. `internal/httpapi/router_test.go`
    - Health, method rejection, redaction, and recovery tests.

48. `internal/httpapi/create_test.go`
    - Authenticated create API tests for valid, invalid, disabled, quota, and limit cases.

49. `internal/httpapi/receiver_test.go`
    - Receiver status and close API tests for pickup-token scoping, expiry reporting, retry safety, and file-pointer cleanup.

50. `internal/httpapi/drop_test.go`
    - Drop endpoint tests for valid encrypted storage, second-drop rejection, oversize reset, malformed reset, authorization scoping, and concurrency.

51. `internal/httpapi/pickup_test.go`
    - Pickup tests for ready retrieval, repeatability, first-pickup timestamps, and rejection cases.

52. `internal/httpapi/drop_page_test.go`
    - Drop page security header, copy, asset, and token-redaction tests.

53. `internal/httpapi/integration_test.go`
    - End-to-end create/drop/status/pickup/close, failure, concurrency, cleanup, CORS, redaction, and disk-write failure tests.

54. `internal/server/server.go`
    - Imperative shell wiring config, data directory, SQLite repository, blob store, and HTTP server.

55. `internal/server/server_test.go`
    - Server initialization and health routing tests.

56. `cmd/drop-point/main.go`
    - `drop-point` CLI entrypoint, default `serve` command, API token generation command, and cleanup command.

57. `cmd/drop-point/main_test.go`
    - CLI token generation command tests.

58. `scripts/drop_point_protocol.py`
    - Shared Python DropPoint protocol helpers for local receiver/sender simulations.

59. `scripts/drop-point-receiver.py`
    - Python receiver simulation for create, status, pickup, decrypt, and close.

60. `scripts/drop-point-sender.py`
    - Python sender simulation for fragment parsing, browser-equivalent encryption, and encrypted drop upload.

61. `docs/configuration.md`
    - Operator configuration reference and token hash guidance.

62. `docs/api.md`
    - Receiver API and encrypted drop framing reference with curl examples.

63. `docs/deployment.md`
    - Build, systemd, reverse-proxy/tunnel, secure-context, request-size, and log-redaction guidance.

64. `docs/client-integration.md`
    - Generic receiver/client integration boundary and durable local storage ordering guidance.

65. `docs/local-testing.md`
    - Local testing workflow using the Python receiver and sender simulation scripts.

66. `README.md`
    - Product overview, local development flow, security model, and operator entry points.

67. `CODE_REVIEW_ORDER.md`
    - This review-order index. Update it whenever repository files change.
