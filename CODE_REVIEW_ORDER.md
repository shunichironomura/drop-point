# Code Review File Order

Date: 2026-07-02

This document lists repository files in dependency-first review order. Local scratch files under `.local/`, generated runtime data under `.data/`, VCS metadata under `.git/`, and build outputs are intentionally excluded from the codebase review order.

## Topological library-consumer order

1. `SPEC.md`
   - Product, API, lifecycle, data, encryption protocol, and reference implementation specification.

2. `AGENTS.md`
   - Agent instructions, including the authoritative-spec and review-order maintenance guidance.

3. `CLAUDE.md`
   - Symlink to `AGENTS.md` for Claude-compatible agent instruction discovery.

4. `.gitignore`
   - Local Python bytecode, virtual-environment, and deployment `.env` ignore rules.

5. `.dockerignore`
   - Docker build context exclusions for VCS metadata, runtime data, and local scratch files.

6. `.env.example`
   - Docker Compose environment template for host binding, service configuration, and token-hash JSON.

7. `renovate.json5`
   - Renovate dependency-update policy, including GitHub Actions digest pinning, weekly lock-file maintenance, and CI labeling.

8. `go.mod`
   - Go module declaration and direct dependency list.

9. `go.sum`
   - Go dependency checksums.

10. `.github/workflows/ci.yml`
    - GitHub Actions CI workflow for formatting, build, vet, race tests, Staticcheck, and govulncheck.

11. `config.example.json`
    - Example operator configuration using the canonical `/var/lib/droppoint` data directory.

12. `internal/config/config.go`
    - JSON configuration types, defaults, loading, and validation.

13. `internal/config/datadir.go`
    - Data directory creation and restrictive permission enforcement.

14. `internal/config/config_test.go`
    - Config loading, validation, and data-directory tests.

15. `internal/logutil/logutil.go`
    - Shared discard-default logger helper for optional logging dependencies.

16. `internal/token/token.go`
    - High-entropy token generation, hashing, and constant-time hash comparison helpers.

17. `internal/token/token_test.go`
    - Token prefix, entropy, base64url, uniqueness-shape, and hash-format tests.

18. `internal/dropname/dropname.go`
    - Human-readable non-secret drop point display-name generation and validation.

19. `internal/dropname/dropname_test.go`
    - Drop display-name generation and validation tests.

20. `internal/droppoint/droppoint.go`
    - Functional drop point domain entity, display name, lifecycle statuses, errors, request/result types, and pure transition rules.

21. `internal/droppoint/droppoint_test.go`
    - Domain lifecycle transition acceptance and rejection tests.

22. `internal/store/migrate.go`
    - Idempotent SQLite schema migration for `drop_points`.

23. `internal/store/store.go`
    - SQLite opening, runtime PRAGMA configuration, and database handle ownership.

24. `internal/store/repository.go`
    - SQLite drop point repository methods for lifecycle, token authorization, quota counting, display-name persistence, and file-pointer cleanup.

25. `internal/store/store_test.go`
    - SQLite initialization, schema, and migration tests.

26. `internal/store/repository_test.go`
    - Repository create, lookup, token mismatch, quota, close, expiry, pickup timestamp, and receiving-reset tests.

27. `internal/store/lifecycle_parity_test.go`
    - Parity tests keeping SQLite lifecycle mutations aligned with the pure drop point state machine.

28. `internal/blobstore/blobstore.go`
    - Filesystem encrypted payload/envelope storage with atomic writes, exact-byte reads, fsync, rename, and idempotent deletion.

29. `internal/blobstore/blobstore_test.go`
    - Blob storage exact-byte write/read, oversize, and idempotent deletion tests.

30. `internal/cleanup/cleanup.go`
    - Expiry cleanup service that marks expired drop points and deletes payload directories idempotently.

31. `internal/cleanup/cleanup_test.go`
    - Cleanup tests for expired ready/open drop points and repeated runs.

32. `internal/cryptoenv/envelope.go`
    - Relay-side protocol envelope schema validation and base64url helpers.

33. `internal/cryptoenv/manifest.go`
    - Bundle manifest building, parsing, validation, payload splitting, filename sanitization, and MIME sanitization.

34. `internal/cryptoenv/reference.go`
    - X25519/HKDF/AES-GCM reference encrypt/decrypt implementation outside the relay path.

35. `internal/cryptoenv/vectors.go`
    - Deterministic positive protocol test-vector generation.

36. `internal/cryptoenv/envelope_test.go`
    - Envelope shape, algorithm, length, padding, and unknown-field validation tests.

37. `internal/cryptoenv/reference_test.go`
    - Reference round-trip, negative vector rejection, manifest validation, and sanitization tests.

38. `docs/protocol-reference.md`
    - Protocol implementation pointers and deterministic positive/negative test-vector documentation.

39. `web/drop-page/index.html`
    - Sender-facing mobile-friendly drop page shell and canonical UI copy.

40. `web/drop-page/styles.css`
    - Sender-facing drop page styles.

41. `web/drop-page/app.js`
    - Browser WebCrypto X25519/HKDF/AES-GCM bundle encryption, sender metadata lookup, and multipart encrypted drop submission.

42. `web/drop-page/assets.go`
    - Embedded static asset filesystem for the drop page.

43. `internal/httpapi/health.go`
    - Low-information `/health` handler.

44. `internal/httpapi/responses.go`
    - Shared JSON response and error helpers for API handlers.

45. `internal/httpapi/auth.go`
    - Bearer API token parsing and configured token-hash authentication.

46. `internal/httpapi/create.go`
    - Authenticated drop point creation handler, request validation, display-name generation, quota enforcement, and drop-link construction.

47. `internal/httpapi/receiver.go`
    - Pickup-token authorization, receiver status, close API handlers, and blob-store interface.

48. `internal/httpapi/drop_metadata.go`
    - Sender drop-token metadata handler for server-bound display names and upload limits.

49. `internal/httpapi/drop.go`
    - Encrypted multipart drop endpoint, envelope validation, streaming size enforcement, and ready-state commit handling.

50. `internal/httpapi/pickup.go`
    - Multipart encrypted pickup endpoint and first-pickup timestamp recording.

51. `internal/httpapi/drop_page.go`
    - Sender-facing drop page and same-origin asset HTTP handlers with strict security headers.

52. `internal/httpapi/cors.go`
    - Same-origin CORS/preflight policy for browser requests while preserving non-browser bearer-token clients.

53. `internal/httpapi/middleware.go`
    - Request logging, token-path redaction, and panic recovery middleware.

54. `internal/httpapi/router.go`
    - HTTP route assembly and dependency injection.

55. `internal/httpapi/router_test.go`
    - Health, method rejection, redaction, and recovery tests.

56. `internal/httpapi/create_test.go`
    - Authenticated create API tests for valid, invalid, disabled, quota, and limit cases.

57. `internal/httpapi/receiver_test.go`
    - Receiver status and close API tests for pickup-token scoping, expiry reporting, retry safety, and file-pointer cleanup.

58. `internal/httpapi/drop_metadata_test.go`
    - Sender metadata API tests for server-bound display names and unavailable drops.

59. `internal/httpapi/drop_test.go`
    - Drop endpoint tests for valid encrypted storage, second-drop rejection, oversize reset, malformed reset, authorization scoping, and concurrency.

60. `internal/httpapi/pickup_test.go`
    - Pickup tests for ready retrieval, repeatability, first-pickup timestamps, and rejection cases.

61. `internal/httpapi/drop_page_test.go`
    - Drop page security header, copy, asset, metadata lookup, and token-redaction tests.

62. `internal/httpapi/integration_test.go`
    - End-to-end create/drop/status/pickup/close, failure, concurrency, cleanup, CORS, redaction, and disk-write failure tests.

63. `internal/server/server.go`
    - Imperative shell wiring config, data directory, SQLite repository, blob store, and HTTP server.

64. `internal/server/server_test.go`
    - Server initialization and health routing tests.

65. `cmd/droppoint/main.go`
    - `droppoint` CLI entrypoint, default `serve` command, API token generation command, and cleanup command.

66. `cmd/droppoint/main_test.go`
    - CLI token generation command tests.

67. `Dockerfile`
    - Multi-stage non-root container image for the DropPoint relay.

68. `compose.yaml`
    - Docker Compose service definition for building and running the relay with persistent storage and ignored `.env` configuration.

69. `scripts/drop_point_protocol.py`
    - Shared Python DropPoint protocol helpers for local receiver/sender simulations.

70. `scripts/droppoint-receiver.py`
    - Python receiver simulation for create, status, pickup, decrypt, and close.

71. `scripts/droppoint-qr.py`
    - Python public-endpoint mobile test helper for creating a drop point, saving receiver state, and rendering the sender link as a QR code.

72. `scripts/droppoint-sender.py`
    - Python sender simulation for fragment parsing, browser-equivalent encryption, and encrypted drop upload.

73. `docs/configuration.md`
    - Operator configuration reference and token hash guidance.

74. `docs/api.md`
    - Receiver API, sender metadata, and encrypted drop framing reference with curl examples.

75. `docs/deployment.md`
    - Build, systemd, reverse-proxy/tunnel, secure-context, request-size, and log-redaction guidance.

76. `docs/client-integration.md`
    - Generic receiver/client integration boundary and durable local storage ordering guidance.

77. `docs/local-testing.md`
    - Local testing workflow using the Python receiver and sender simulation scripts.

78. `README.md`
    - Product overview, local development flow, security model, and operator entry points.

79. `CODE_REVIEW_ORDER.md`
    - This review-order index. Update it whenever repository files change.
