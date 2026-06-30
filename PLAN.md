# DropPoint Phased Implementation Plan

Date: 2026-06-30
Status: Draft

This plan turns the DropPoint specification into implementation phases. It is based on the design notes in `.local/` and on `SPEC.md`.

Key decisions carried into this plan:

- DropPoint is a generic temporary encrypted file handoff relay, not a Procnote-specific service.
- The relay stores ciphertext only and is not a system of record.
- The implementation target is the Go single-binary service with SQLite metadata and local filesystem payload storage.
- The service is deployment-neutral: it can run behind Cloudflare Tunnel, Caddy, another reverse proxy, direct TLS, or local development routing.
- The encryption protocol is the X25519/HKDF/AES-256-GCM protocol whose envelope uses `protocol_version: 2`.
- The older RSA-OAEP protocol described in earlier notes is not implemented.
- Drop points are single-use at the drop point level; one drop can contain a multi-file encrypted bundle.
- Pickup is repeatable until explicit close or expiry; pickup never auto-deletes.
- Keep a functional core and imperative shell: domain rules, token handling, envelope validation, and status transitions should be pure and easy to test; HTTP, SQLite, filesystem, clocks, and logging remain at the shell.

## Implementation structure and naming targets

Use this initial Go layout unless implementation experience justifies a simpler equivalent:

```text
cmd/drop-point/
internal/config/
internal/httpapi/
internal/droppoint/
internal/store/
internal/blobstore/
internal/token/
internal/cryptoenv/
internal/cleanup/
web/drop-page/
```

- `internal/droppoint` contains domain logic and status transition rules.
- `internal/httpapi` contains route handlers and request/response DTOs.
- `internal/store` contains SQLite persistence.
- `internal/blobstore` contains filesystem payload/envelope writes.
- `internal/cryptoenv` contains envelope schema validation, protocol reference helpers, and test-vector helpers; production relay request handling must not decrypt user payloads.
- `web/drop-page` contains static sender-facing HTML, CSS, and JavaScript.

The canonical operator binary is `drop-point`. Target command shape:

```text
drop-point serve --config ./config.json
drop-point token generate
drop-point migrate
drop-point cleanup expired
drop-point inspect drop-point <dp_...>
```

The initial implementation may run `serve` by default when no subcommand is provided, but documentation should converge on explicit subcommands as they become available.

Canonical HTTP handler names:

```go
HandleCreateDropPoint
HandleServeDropPage
HandleSubmitDrop
HandleGetDropPointStatus
HandlePickupPayload
HandleCloseDropPoint
HandleHealth
```

The domain service boundary should stay close to:

```go
type DropPointService interface {
    CreateDropPoint(ctx context.Context, req CreateDropPointRequest) (*CreateDropPointResponse, error)
    GetDropPointStatus(ctx context.Context, id string, pickupToken string) (*DropPointStatusResponse, error)
    BeginDrop(ctx context.Context, dropToken string) (*DropPoint, error)
    CommitDrop(ctx context.Context, dropToken string, result CommitDropResult) error
    AbortDrop(ctx context.Context, dropToken string, cause error) error
    PickupPayload(ctx context.Context, id string, pickupToken string) (*PickupPayloadResult, error)
    CloseDropPoint(ctx context.Context, id string, pickupToken string) error
    ExpireDropPoints(ctx context.Context, now time.Time) error
}
```

Canonical SQLite repository method names:

```text
CreateDropPoint
FindDropPointByID
FindOpenDropPointByDropTokenHash
AuthorizePickupToken
BeginReceivingDrop
CommitReceivedDrop
ResetReceivingDrop
MarkFirstPickedUp
CloseDropPoint
ExpireDropPoints
CountActiveDropPointsByAPITokenID
DeleteDropPointFiles
```

Canonical domain error names:

```text
ErrDropPointNotFound
ErrDropPointExpired
ErrDropPointClosed
ErrDropPointNotOpen
ErrDropAlreadyExists
ErrDropTokenInvalid
ErrPickupTokenInvalid
ErrPayloadTooLarge
ErrEnvelopeInvalid
```

## Phase 0: service skeleton and development foundation

Status: Completed on 2026-07-01.

### Goal

Create the minimal runnable Go service foundation that later phases build on. This phase is explicit because the current repository has no implementation baseline.

### Deliverables

- Initialize the Go module and `cmd/drop-point` entrypoint, producing the `drop-point` CLI/binary.
- Add JSON config loading and validation.
- Add SQLite initialization and schema migration for `drop_points` using the schema in `SPEC.md`.
- Add data directory initialization with restrictive permissions; use `/var/lib/drop-point` as the canonical system data directory in examples and packaged configurations.
- Add an HTTP router with unauthenticated, low-information `/health`.
- Add request logging and panic recovery.
- Add basic tests for config, storage, and server packages.
- Update `CODE_REVIEW_ORDER.md` as implementation files are created.

### Acceptance criteria

- `go test ./...` passes.
- The `drop-point` binary starts with defaults on `127.0.0.1:8080`.
- The configured data directory is created with restrictive permissions.
- SQLite uses WAL, foreign keys, and a busy timeout.
- `/health` returns a successful low-information response.

## Phase 1: domain, tokens, and persistence core

Status: Completed on 2026-07-01.

### Goal

Build the reusable functional core for drop point lifecycle rules, token generation/hashing, and SQLite persistence before adding HTTP behavior.

### Deliverables

- Add a domain package for drop point entities and status transitions.
- Add typed request/result structures for creating, dropping, picking up, closing, and expiring drop points.
- Add error types for domain failures, using explicit errors rather than `None`/zero-value ambiguity.
- Add a token package that can:
  - generate `dp_`, `drop_`, `pick_`, and `api_` identifiers/tokens;
  - use at least 256 bits of CSPRNG entropy for capability tokens;
  - encode tokens as base64url without padding;
  - hash token secrets for storage;
  - compare token hashes in constant time where direct comparison is needed.
- Add SQLite repository methods for:
  - creating a drop point;
  - finding a drop point by ID;
  - finding an open drop point by drop token hash;
  - authorizing a pickup token for a specific drop point;
  - beginning, committing, and aborting a receiving state;
  - recording first pickup;
  - closing and expiring drop points;
  - counting active drop points per API token ID.
- Keep raw token values out of database rows and logs.

### Acceptance criteria

- Token generation tests prove prefix, entropy length, base64url encoding, uniqueness shape, and hash format.
- Domain tests cover all allowed and rejected status transitions.
- Store tests cover create, lookup, token mismatch, quota count, close, expiry, and pickup timestamp behavior.
- Cross-drop-point authorization tests prove a pickup token for one drop point cannot access another.
- Failed/aborted receiving state returns the drop point to `open` without consuming the single-use slot.

## Phase 2: authenticated drop point creation

Status: Completed on 2026-07-01.

### Goal

Expose receiver-initiated drop point creation through the HTTP API.

### Deliverables

- Add bearer-token authentication for `POST /api/drop-points`.
- Add `drop-point token generate` to create a high-entropy plaintext API token and print the matching `sha256:<lowercase-hex-sha256>` config entry once.
- Map configured API token hashes to `api_token_id` labels.
- Validate `ttl_seconds`, `max_bytes`, and `single_use` against configuration.
- Enforce `max_active_drop_points` per API token, using the token-specific override when present and the default otherwise.
- Create drop point records with status `open`.
- Return:
  - `drop_point_id`;
  - fragment-free `drop_link`;
  - `pickup_token`;
  - `expires_at`;
  - `max_bytes`.
- Redact authorization headers and token-bearing values from logs.

### Acceptance criteria

- Missing, malformed, disabled, or invalid API tokens cannot create drop points.
- Valid API tokens can create drop points within quota.
- Stored rows contain token hashes only.
- Returned `drop_link` uses `base_url` and `/drop/<drop_token>` and contains no URL fragment.
- Requests exceeding TTL, size, or active-drop-point quotas are rejected with clear JSON errors.

## Phase 3: receiver status and close APIs

Status: Completed on 2026-07-01.

### Goal

Expose the receiver-side lifecycle endpoints that are needed before sender uploads are enabled.

### Deliverables

- Add pickup-token authorization for receiver endpoints.
- Add `GET /api/drop-points/:drop_point_id/status`.
- Add `DELETE /api/drop-points/:drop_point_id`.
- Apply expiry checks to receiver endpoints.
- Delete payload/envelope files on close when present.
- Make close safe to retry.
- Ensure the drop token cannot authorize receiver endpoints.

### Acceptance criteria

- A pickup token can poll and close only its own drop point.
- Unknown drop point IDs and wrong pickup tokens do not reveal sensitive details.
- Expired drop points cannot be picked up and are reported consistently.
- Closing before any drop prevents later drops.
- Repeated close requests do not corrupt state or fail destructively.

## Phase 4: blob storage and encrypted drop endpoint

Status: Completed on 2026-07-01.

### Goal

Accept a single encrypted drop and store its envelope and payload durably without decrypting either.

### Deliverables

- Add a filesystem blob store rooted under `drop-points/<drop-point-id>/`.
- Add atomic write behavior: write temporary files, fsync where appropriate, and rename into place only after success.
- Add `PUT /api/drops/:drop_token`.
- Parse `multipart/form-data` with exactly:
  - `envelope` (`application/json`);
  - `payload` (`application/octet-stream`).
- Validate envelope shape enough for relay hygiene:
  - `protocol_version` is `2`;
  - `key_agreement` is `x25519-hkdf-sha256-aesgcm-raw32`;
  - base64url fields decode;
  - public key length is 32 bytes;
  - nonce lengths are 12 bytes;
  - encrypted metadata is present.
- Do not decrypt metadata or payload.
- Enforce max encrypted payload size while streaming.
- Enforce one committed drop per drop point with a transaction.
- Reset `receiving` to `open` after failed or partial writes unless expiry/failure state applies.
- Mark status `ready` only after durable envelope and payload writes succeed.

### Acceptance criteria

- A valid drop token can submit exactly one encrypted payload before expiry.
- Unknown, expired, closed, ready, or failed drop points reject drops.
- A second drop for the same drop point is rejected.
- Concurrent drop attempts commit at most one payload.
- Oversized payloads are rejected and do not leave the drop point consumed.
- Interrupted or malformed multipart uploads do not mark the drop point `ready`.
- Stored payload bytes are exactly the submitted ciphertext bytes.
- Stored envelope JSON is not logged.

## Phase 5: pickup endpoint and expiry cleanup

Status: Completed on 2026-07-01.

### Goal

Let receivers retrieve ciphertext reliably and clean up closed or expired drop points.

### Deliverables

- Add `GET /api/drop-points/:drop_point_id/pickup`.
- Return `multipart/mixed` containing the stored `envelope` JSON and streamed encrypted `payload`.
- Record `first_picked_up_at` after a successful pickup response if it is not already set.
- Keep pickup idempotent and repeatable.
- Add an expiry cleanup service that:
  - marks expired non-terminal drop points as `expired`;
  - deletes payload/envelope directories for expired drop points;
  - can be run repeatedly safely.
- Ensure manual close and expiry cleanup do not panic if files are already gone.

### Acceptance criteria

- A pickup token can retrieve ready ciphertext.
- Pickup before a drop is rejected cleanly.
- Pickup is repeatable and does not auto-delete.
- Drop tokens cannot pick up.
- Closed and expired drop points cannot be picked up.
- Cleanup deletes expired payload directories and is idempotent.
- Service restart preserves open and ready drop points.

## Phase 6: protocol reference package and test vectors

Status: Completed on 2026-07-01.

### Goal

Provide a reference implementation of the encryption protocol outside the relay path, so browser and non-browser clients can interoperate with confidence.

The relay itself still does not decrypt user payloads in production request handling.

### Deliverables

- Add a protocol/reference package or tools for:
  - X25519 key generation using raw 32-byte keys;
  - X25519 shared-secret computation with all-zero rejection;
  - HKDF-SHA256 derivation with the exact salt and info strings;
  - AES-256-GCM metadata and payload encryption/decryption;
  - AAD construction for metadata and payload;
  - envelope build/parse/validation;
  - bundle manifest build/parse;
  - payload splitting by manifest sizes;
  - filename and MIME sanitization helpers for receivers.
- Generate deterministic positive test vectors with fixed recipient private key, sender ephemeral private key, nonces, manifests, and payload bytes.
- Include intermediate values in positive test-vector documentation: `shared_secret`, `metadata_key`, `payload_key`, `encrypted_metadata`, and `encrypted_payload`.
- Document language-specific protocol implementation pointers for Go, Node.js/TypeScript, Python, Swift, Rust, and browser WebCrypto, including raw 32-byte X25519 public keys, HKDF salt/info byte strings, and AES-GCM `ciphertext || tag` handling.
- Generate negative test vectors for:
  - tampered payload ciphertext/tag;
  - tampered metadata ciphertext/tag;
  - tampered nonce;
  - tampered sender ephemeral public key;
  - wrong recipient key;
  - protocol version downgrade/change;
  - all-zero or low-order X25519 input;
  - manifest size-sum mismatch;
  - hostile filenames and MIME types;
  - duplicate filenames in a bundle.
- Include both single-file and multi-file bundle vectors.

### Acceptance criteria

- Reference encryptor to reference decryptor round-trips single-file and multi-file bundles.
- Every negative test vector is rejected for the expected reason.
- Hostile filenames cannot escape receiver-controlled directories.
- Manifest size sum must equal decrypted payload length.
- Test vector documentation contains enough fixed inputs and outputs for independent client implementations.

## Phase 7: sender-facing drop page with browser encryption

Status: Completed on 2026-07-01.

### Goal

Implement the mobile-friendly sender page that reads the fragment key, encrypts locally with WebCrypto, and submits the encrypted drop.

### Deliverables

- Add `GET /drop/:drop_token` serving static HTML/CSS/JS.
- Use canonical DropPoint UI language:
  - `Drop files`;
  - `Choose files`;
  - `Drop encrypted files`;
  - `Ready for pickup` where receiver-facing copy applies.
- Add secure-context detection for `window.isSecureContext` and `crypto.subtle`.
- Feature-detect WebCrypto X25519 support and show an explicit unsupported-browser error when unavailable.
- Parse fragment format `#v=2&pk=<base64url(raw-32-byte-x25519-public-key)>`.
- Build a bundle manifest for one or more files.
- Concatenate file bytes in manifest order.
- Generate sender ephemeral X25519 key pair and AES-GCM nonces.
- Derive metadata and payload keys with the protocol HKDF values.
- Encrypt payload and metadata using the exact AAD values.
- Build envelope JSON and submit `multipart/form-data` to `PUT /api/drops/:drop_token`.
- Show clear states for missing key, unsupported context, selecting files, encrypting, dropping, success, expired, and failure.
- Use a strict CSP and no third-party scripts.

### Acceptance criteria

- The browser never submits plaintext file contents, plaintext filenames, MIME types, or manifest JSON to the relay.
- Missing or malformed fragment keys are rejected before file selection/drop.
- Non-secure contexts show a clear error rather than failing during encryption.
- Browser-produced drops decrypt with the reference decryptor.
- Multi-file selection produces one payload and one encrypted manifest.
- Network failures and expired drop points are reported clearly.

## Phase 8: end-to-end HTTP integration and hardening

### Goal

Validate the complete service behavior under realistic failure, concurrency, and security conditions.

### Deliverables

- Add full integration tests for:
  - create → drop → status → pickup → close;
  - create → close → attempted drop;
  - expired drop point cannot accept a drop;
  - failed drop then retry;
  - concurrent drop race;
  - oversized drop;
  - wrong pickup token;
  - drop token used against receiver APIs;
  - pickup token used against drop endpoint;
  - repeated pickup;
  - cleanup of expired ready and open drop points.
- Add log redaction checks for:
  - authorization headers;
  - drop tokens in URL paths;
  - pickup tokens;
  - envelope contents;
  - public key fragments.
- Add security headers for the drop page.
- Add CORS policy appropriate for same-origin drop page use and non-browser receiver clients.
- Add body size limits at HTTP handler boundaries.
- Add defensive behavior for disk-full/write-failure paths.

### Acceptance criteria

- `go test ./...` covers the domain, store, HTTP handlers, blob store, and protocol reference code.
- Integration tests prove the relay never needs plaintext to complete create/drop/pickup/close.
- Token-bearing paths are redacted in application logs.
- Disk write failures do not corrupt drop point state or falsely mark drops ready.
- Cleanup and close remain safe during missing-file and in-flight edge cases.

## Phase 9: operator documentation and deployment packaging

### Goal

Make the service understandable and safely deployable without tying it to one hosting provider.

### Deliverables

- Update README with:
  - product overview;
  - security model and active-JavaScript TCB caveat;
  - local development flow;
  - API examples;
  - sender secure-context requirement;
  - WebCrypto X25519 browser support expectations and the unsupported-browser error path;
  - token generation and configuration guidance;
  - data directory and cleanup behavior.
- Add a configuration reference.
- Add an API reference with curl examples for receiver flows and encrypted drop framing.
- Add protocol/test-vector documentation for independent clients.
- Add production packaging guidance, such as:
  - binary build command;
  - systemd unit example or container example;
  - reverse proxy/tunnel requirements;
  - request body size guidance;
  - log redaction guidance at proxy/CDN/tunnel layers.
- Document supported deployment properties:
  - sender-facing page must be HTTPS or localhost;
  - receiver APIs must be reachable by receiver clients;
  - TLS termination may be external;
  - `/health` is unauthenticated and low-information.

### Acceptance criteria

- A new operator can configure and run DropPoint from the docs.
- Docs do not market DropPoint as zero-trust E2EE against a relay operator that can modify served JavaScript.
- API examples use DropPoint vocabulary: drop point, drop link, drop token, pickup token, drop, pickup, close.
- Documentation states that the RSA-OAEP legacy design is not implemented.

## Phase 10: optional Procnote/client integration boundary

### Goal

Keep DropPoint generic while documenting how a client such as Procnote should integrate safely.

### Deliverables

- Document the client-side receiver flow:
  - create drop point;
  - locally generate recipient X25519 key pair;
  - append `#v=2&pk=...` fragment;
  - show QR/drop link;
  - poll status;
  - pickup ciphertext;
  - decrypt locally;
  - validate and sanitize manifest entries;
  - durably store plaintext in the client system;
  - close the drop point.
- Document that client-specific event models and storage records are outside the relay.
- For Procnote-like attachment clients, document that the durable local attachment event is appended only after successful local storage.

### Acceptance criteria

- Client guidance is generic and does not add Procnote-specific API requirements to the relay.
- The receiver ordering prevents remote deletion before local durable storage succeeds.

## Cross-phase testing checklist

Maintain these tests as features land:

- Config validation.
- Token generation, hashing, and constant-time comparison paths.
- Domain status transition rules.
- API token auth and quota enforcement.
- Pickup-token authorization scoped to one drop point.
- Drop-token authorization scoped to sender drop only.
- SQLite transaction behavior for one-drop races.
- Atomic blob writes and cleanup idempotency.
- Multipart drop and pickup framing.
- Protocol reference positive and negative vectors.
- Browser encryption to reference decryptor interoperability.
- Reference encryptor to reference decryptor interoperability.
- Hostile filename/MIME sanitization.
- Secure-context error handling.
- Log redaction.
- Service restart preservation of open/ready drop points.

## Hardening checklist before wider use

- Strict CSP on the drop page.
- No third-party scripts on the drop page.
- Security headers on sender-facing responses.
- Request body limits at application and edge layers.
- Token and path redaction in all logs.
- No envelope contents, public key fragments, filenames, MIME types, or manifest plaintext in logs.
- Secure data directory permissions.
- Cleanup metrics and basic operational alerts.
- Backpressure and clear errors for disk-full conditions.
- Rate limiting by API token and source IP where practical.
- Clear documentation that a malicious relay origin can compromise browser-delivered encryption code.

## Deferred work

These items are intentionally outside the initial implementation plan:

- Anonymous drop point creation.
- Public pickup URLs.
- Multiple independently stored payloads under one drop point.
- Resumable or chunked encrypted payloads.
- Server-side per-file payload storage.
- WebSocket/SSE notifications.
- CAPTCHA, Turnstile, or proof-of-work in the normal path.
- Full account management.
- Admin dashboard.
- Cloud-provider-specific storage backends.
- True zero-trust sender code integrity against the relay operator.
