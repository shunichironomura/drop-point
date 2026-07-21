# DropPoint Specification

Date: 2026-07-08
Status: Draft

DropPoint is a temporary encrypted file handoff service. A receiver creates a short-lived drop point and shares a drop link. A sender opens the link, encrypts one or more files locally, and drops a single encrypted bundle. The relay stores ciphertext only. The receiver later picks up the payload and decrypts it locally.

DropPoint is not a system of record. Drop points are short-lived, single-use handoff points that are explicitly closed by the receiver or expired by time-to-live policy.

This document is a product, API, data, and protocol specification.

## 1. Normative language

The terms **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY** are used as described in RFC 2119.

## 2. Scope

DropPoint provides:

- Authenticated receiver-initiated creation of temporary drop points.
- Sender-facing drop links suitable for QR-code sharing.
- A sender-facing drop page that encrypts selected files in the browser.
- A relay API that stores and returns ciphertext and encrypted metadata only.
- Separate sender-side drop capability and receiver-side pickup capability.
- Expiring, single-use drop points.
- Receiver-side pickup and explicit close semantics.

DropPoint does not provide:

- Permanent file storage or a system of record.
- Server-side plaintext processing.
- Anonymous drop point creation.
- Public pickup URLs.
- Sender identity proof beyond possession of the drop token.
- Multiple independently stored payloads per drop point.
- Resumable or chunked cryptographic payloads.
- A full account system or admin dashboard.
- Zero-trust confidentiality against an operator that can serve malicious sender-side JavaScript.
- The older RSA-OAEP-based encryption design described in earlier local notes.

## 3. Terminology

| Term | Meaning |
| --- | --- |
| DropPoint | Product/service name. |
| `droppoint` | Operator CLI/binary name for running the relay. |
| drop point | One temporary handoff session created by a receiver. |
| display name | Human-readable random label for a drop point, such as `calm-otter`, used only as a UX check. |
| receiver | Client that creates the drop point, owns the private key, and picks up the payload. |
| sender / uploader | Person or client that opens the drop link and drops encrypted files. |
| relay | DropPoint HTTP service that stores ciphertext and operational metadata. |
| drop link | URL shared with the sender. |
| drop token | Sender-side capability token in the drop link path. |
| pickup token | Receiver-side capability token used to poll, pick up, and close. |
| drop page | Sender-facing page served at the drop link. |
| drop | Sender-side encrypted upload action. |
| pickup | Receiver-side encrypted payload retrieval action. |
| close | Receiver-driven cancellation/deletion action. |
| bundle | Ordered logical set of one or more files before encryption. |
| payload | Binary AES-GCM ciphertext stream for the concatenated bundle bytes. |
| envelope | JSON crypto metadata required for receiver-side decryption. |

Protocol terms such as `payload`, `envelope`, `ciphertext`, `nonce`, `AAD`, `public_key`, and `private_key` keep their cryptographic meanings and are not replaced by metaphorical names.

## 4. Core lifecycle

1. The receiver authenticates to the relay and creates a drop point.
2. The receiver locally generates a per-drop-point X25519 key pair.
3. The relay returns a drop point ID, display name, drop link without fragment, pickup token, expiry time, and max payload size.
4. The receiver appends the encryption public key fragment to the drop link and displays or shares it alongside the display name, commonly as a QR code.
5. The sender opens the drop link.
6. The drop page reads the receiver public key from `location.hash`, fetches sender-safe metadata including the server-bound display name, encrypts a bundle locally, and submits an envelope plus encrypted payload to the relay.
7. The relay stores the envelope and encrypted payload and marks the drop point `ready` only after durable storage succeeds.
8. The receiver polls status, picks up the envelope and encrypted payload, decrypts locally, validates the decrypted bundle, stores plaintext in its own system if desired, and then explicitly closes the drop point.
9. The relay deletes stored ciphertext when the drop point is closed or expires.

A pickup MUST NOT automatically close or delete a drop point. Pickup is repeatable until the drop point is closed or expires.

## 5. Drop point state model

Drop points use these status values:

| Status | Meaning | Terminal |
| --- | --- | --- |
| `open` | Drop point exists and can accept exactly one drop. | No |
| `receiving` | The relay is receiving a drop stream. | No |
| `ready` | A durable encrypted payload is available for pickup. | No |
| `closed` | Receiver explicitly closed the drop point. | Yes |
| `expired` | TTL elapsed and the drop point is no longer usable. | Yes |
| `failed` | Terminal internal failure requiring cleanup or operator attention. | Yes |

Required transition rules:

- `open` MAY transition to `receiving` when a valid drop starts.
- `receiving` MUST transition to `ready` only after the envelope and payload are durably stored.
- A failed or partial drop MUST NOT consume the single-use slot; the relay MUST delete attempt artifacts and return the drop point to `open` unless it has expired or failed terminally.
- Durable storage failures, including disk-full or write-failure paths, MUST NOT mark a drop point `ready` and MUST NOT consume the single-use slot unless it has expired or failed terminally.
- The relay MUST durably record when a `receiving` attempt begins. Startup reconciliation MUST recover every interrupted `receiving` attempt before serving requests, and periodic reconciliation MUST recover attempts older than the configured HTTP operation bound plus a conservative grace period.
- `failed` is reserved for unrecoverable internal inconsistency or corruption. Malformed requests, interrupted uploads, and transient storage failures MUST remain recoverable and MUST NOT ordinarily transition to `failed`.
- `open`, `receiving`, and `ready` MAY transition to `closed` by receiver request.
- Any non-terminal status whose expiry time has elapsed MUST be treated as expired and MUST NOT accept new drops or pickups.
- Pickup records `first_picked_up_at` and optionally `last_picked_up_at`; pickup MUST NOT be modeled as a terminal status.

## 6. Authentication and capabilities

### 6.1 API token

Creating a drop point requires an enabled API bearer token stored in the relay database:

```http
Authorization: Bearer <api-token>
```

API tokens are long random secrets. Stored API token material MUST be hashed at rest. The token ID is a label for quota checks, logs, and attribution; it is not a user identity by itself. The default local implementation hashes the presented token with SHA-256 and performs a direct SQLite lookup by `secret_hash` in `api_tokens`.

### 6.2 Drop token

The drop token is embedded in the drop link path. Possession authorizes the sender to submit exactly one encrypted drop before expiry. It MUST NOT authorize status polling, pickup, or close.

Because the drop token appears in URL paths, it may be exposed by application, proxy, tunnel, CDN, or browser history logs unless those layers are configured carefully. A leaked drop token within the TTL can let an attacker pre-empt the legitimate sender by dropping junk first; it does not authorize pickup or decryption. Operators MUST redact token-bearing paths and SHOULD keep TTLs short.

### 6.3 Pickup token

The pickup token is returned only to the receiver at drop point creation. Possession authorizes status polling, pickup, and close for that drop point. It MUST NOT authorize dropping a payload.

Receiver endpoint authorization failures SHOULD avoid revealing whether the drop point ID, pickup token, expiry state, or status caused the rejection.

### 6.4 Token format

Capability tokens and public IDs SHOULD use type prefixes for operator readability:

| Value | Prefix |
| --- | --- |
| Drop point ID | `dp_` |
| Drop token | `drop_` |
| Pickup token | `pick_` |
| API token | `api_` |

Capability token secret material MUST carry at least 256 bits of entropy and SHOULD be encoded as base64url without padding. Raw capability tokens MUST NOT be logged and MUST NOT be stored at rest.

### 6.5 Cookie-free authentication posture

Core DropPoint APIs MUST NOT rely on cookies or other ambient browser credentials for authorization. Receiver APIs use bearer tokens, and sender APIs use short-lived capability URLs. This keeps the core relay largely outside classic cookie-based CSRF assumptions; future browser-facing administrative surfaces, if any, MUST define their own CSRF protections separately.

## 7. HTTP API

All JSON request and response fields use `snake_case`.

### 7.1 Create drop point

```http
POST /api/drop-points
Authorization: Bearer <api-token>
Content-Type: application/json
```

Request:

```json
{
  "client_name": "generic-client",
  "ttl_seconds": 600,
  "max_bytes": 52428800,
  "single_use": true
}
```

Rules:

- `ttl_seconds` MUST be positive and MUST NOT exceed the configured maximum TTL.
- `max_bytes` MUST be positive and MUST NOT exceed the configured maximum encrypted payload size.
- DropPoint is always single-use. If `single_use` is present, it MUST be `true`.
- The authenticated API token's active-drop-point quota MUST be enforced.
- The relay MUST generate a human-readable `display_name` for the drop point using an adjective-noun style such as `calm-otter`.
- The display name is not a secret, is not an authentication factor, and MUST NOT replace capability-token checks.

Response:

```json
{
  "drop_point_id": "dp_...",
  "display_name": "calm-otter",
  "drop_link": "https://drop.example.com/drop/drop_...",
  "pickup_token": "pick_...",
  "expires_at": "2026-06-30T12:15:00Z",
  "max_bytes": 52428800
}
```

The response `drop_link` does not contain the public-key fragment. The receiver appends the fragment locally before displaying or sharing the link.

### 7.2 Serve drop page

```http
GET /drop/:drop_token
```

The response is the sender-facing HTML/JS/CSS page. The page MUST:

- Read the receiver public key from `location.hash`.
- Fetch sender-safe metadata from `GET /api/drops/:drop_token` and display the returned server-bound display name before upload.
- Require the encryption fragment format defined in Section 10.9.
- Detect `window.isSecureContext` and `crypto.subtle` support.
- Show a clear error if the page is not running in a secure context.
- Show a clear error for a missing or malformed public key.
- Show selected image files with local thumbnail previews next to the file name and size, without sending plaintext preview bytes to the relay or third parties.
- Use no third-party scripts.
- Use a strict Content Security Policy.
- Set defensive sender-facing security headers such as `X-Content-Type-Options: nosniff`, `Referrer-Policy`, and a restrictive `Permissions-Policy`.

The page SHOULD avoid exposing useful details about unknown or expired drop tokens.

### 7.3 Get sender drop metadata

```http
GET /api/drops/:drop_token
```

Response:

```json
{
  "display_name": "calm-otter",
  "expires_at": "2026-06-30T12:15:00Z",
  "max_bytes": 52428800
}
```

Rules:

- The drop token is the only sender-side authorization requirement.
- The endpoint MUST expose only sender-safe metadata needed by the static drop page.
- The returned `display_name` is authoritative for the drop token and is the value the sender page displays.
- The endpoint MUST NOT expose pickup tokens, drop point IDs, encrypted size, pickup timestamps, storage paths, or receiver-only status details.
- Unknown, expired, closed, failed, ready, or otherwise unavailable drop points SHOULD avoid exposing their display names.

### 7.4 Submit encrypted drop

```http
PUT /api/drops/:drop_token
Content-Type: multipart/form-data; boundary=...
```

The request body has exactly these crypto parts:

| Part | Content-Type | Meaning |
| --- | --- | --- |
| `envelope` | `application/json` | Encryption envelope JSON. |
| `payload` | `application/octet-stream` | Raw encrypted payload bytes, `ciphertext || tag`. |

Rules:

- The drop token is the only sender-side authorization requirement.
- The relay MUST reject drops for closed, expired, ready, failed, or unknown drop points.
- Concurrent drop attempts for the same drop point MUST result in at most one committed drop.
- `max_bytes` applies to the encrypted `payload` part.
- A committed drop stores one envelope and one encrypted payload.
- The relay MUST NOT decrypt or require plaintext metadata.
- Before committing a drop, the relay MUST validate the relay-visible envelope shape, including:
  - `protocol_version` is integer `2`;
  - `key_agreement` is `x25519-hkdf-sha256-aesgcm-raw32`;
  - base64url fields are non-empty, unpadded, and decodable;
  - `sender_ephemeral_public_key` decodes to 32 bytes;
  - `metadata_nonce` and `payload_nonce` decode to 12 bytes;
  - `encrypted_metadata` is present and long enough to contain an AES-GCM tag.

Response:

```json
{ "status": "ready" }
```

Error status rules:

- malformed envelope or multipart input: `400 Bad Request`;
- encrypted payload or request-size violation: `413 Request Entity Too Large`;
- known storage capacity exhaustion, including disk full: `507 Insufficient Storage`;
- known transient storage unavailability: `503 Service Unavailable`;
- other durable-storage or internal finalization failures: `500 Internal Server Error`.

The relay MUST log structured underlying storage and finalization failures without capability tokens, capability-bearing paths, envelope contents, or plaintext metadata.

### 7.5 Poll drop point status

```http
GET /api/drop-points/:drop_point_id/status
Authorization: Bearer <pickup-token>
```

Response:

```json
{
  "status": "ready",
  "display_name": "calm-otter",
  "encrypted_size": 2849123,
  "dropped_at": "2026-06-30T12:03:12Z",
  "first_picked_up_at": null,
  "expires_at": "2026-06-30T12:15:00Z"
}
```

The pickup token MUST authorize only its own drop point.

### 7.6 Pick up encrypted payload

```http
GET /api/drop-points/:drop_point_id/pickup
Authorization: Bearer <pickup-token>
```

The response is `multipart/mixed` containing:

| Part | Content-Type | Meaning |
| --- | --- | --- |
| `envelope` | `application/json` | Stored envelope JSON. |
| `payload` | `application/octet-stream` | Stored encrypted payload bytes. |

Rules:

- Pickup is allowed only for `ready` drop points that have not expired.
- Pickup is idempotent and repeatable until close or expiry.
- Successful pickup records `first_picked_up_at` if it was not already set.
- Pickup MUST NOT delete payload files and MUST NOT close the drop point.

### 7.7 Close drop point

```http
DELETE /api/drop-points/:drop_point_id
Authorization: Bearer <pickup-token>
```

Close deletes stored envelope and payload data if present and marks the drop point `closed`. Close SHOULD be idempotent enough for receiver cleanup flows and MUST tolerate already-deleted envelope or payload files.

### 7.8 Health check

```http
GET /health
```

The health response is unauthenticated and MUST NOT expose drop point counts, token material, file paths, or other sensitive operational details.

## 8. Logical relay data model

A drop point record contains at least:

| Field | Meaning |
| --- | --- |
| `id` | Public drop point ID. |
| `api_token_id` | Label of the API token that created the drop point. |
| `client_name` | Optional receiver-provided client label. |
| `display_name` | Relay-generated human-readable drop point label. |
| `drop_token_hash` | Hash of sender-side capability token. |
| `pickup_token_hash` | Hash of receiver-side capability token. |
| `status` | One of the status values in Section 5. |
| `payload_path` | Storage location for encrypted payload, if present. |
| `envelope_path` | Storage location for envelope JSON, if present. |
| `encrypted_size` | Encrypted payload byte length. |
| `created_at` | Drop point creation timestamp. |
| `dropped_at` | Durable drop completion timestamp. |
| `receiving_started_at` | Internal start timestamp for recovery of interrupted receiving attempts. |
| `first_picked_up_at` | First successful pickup timestamp. |
| `closed_at` | Explicit close timestamp. |
| `expires_at` | TTL expiry timestamp. |
| `max_bytes` | Max encrypted payload size for this drop point. |

The default local implementation uses SQLite for relay metadata and API token hashes. It MUST enable WAL journal mode, foreign-key enforcement, and a busy timeout, and it SHOULD apply built-in schema migrations automatically before serving requests, running cleanup, or running token-management CLI commands.

The default SQLite schema for the local implementation is:

```sql
CREATE TABLE drop_points (
  id TEXT PRIMARY KEY,
  api_token_id TEXT NOT NULL,
  client_name TEXT,
  display_name TEXT NOT NULL DEFAULT '',
  drop_token_hash TEXT NOT NULL UNIQUE,
  pickup_token_hash TEXT NOT NULL,
  status TEXT NOT NULL,
  payload_path TEXT,
  envelope_path TEXT,
  encrypted_size INTEGER,
  created_at TEXT NOT NULL,
  dropped_at TEXT,
  receiving_started_at TEXT,
  first_picked_up_at TEXT,
  closed_at TEXT,
  expires_at TEXT NOT NULL,
  max_bytes INTEGER NOT NULL
);

CREATE INDEX idx_drop_points_status_expires_at
  ON drop_points (status, expires_at);

CREATE INDEX idx_drop_points_api_token_status
  ON drop_points (api_token_id, status);

CREATE TABLE api_tokens (
  id TEXT PRIMARY KEY,
  secret_hash TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  max_active_drop_points INTEGER,
  created_at TEXT NOT NULL,
  disabled_at TEXT
);

```

The default local storage layout is:

```text
/var/lib/droppoint/
├── relay.db  # drop point metadata and API token hashes
└── drop-points/
    └── <drop-point-id>/
        ├── payload.bin
        └── envelope.json
```

`payload.bin` and `envelope.json` are protocol/storage terms and SHOULD keep these names.

The local implementation MUST create the configured data directory and `drop-points/` subdirectory with owner-only permissions. SQLite database files and stored blob files SHOULD also use restrictive owner-only permissions.

Filesystem blob writes MUST be atomic from the repository point of view: write temporary files, flush file contents where supported, rename into place only after successful writes, and flush the containing directory where supported. Creating a per-drop-point directory MUST be followed by flushing the parent `drop-points/` directory before metadata can commit `ready`; removing a per-drop-point directory MUST be followed by flushing that parent before file pointers are cleared. Close and expiry cleanup MUST be safe to repeat when blob directories or files are already gone. Terminal rows are the durable cleanup queue: every cleanup run MUST reconcile all `closed`, `expired`, and `failed` rows with receiver-owned blob storage, including terminal rows whose file pointers were already cleared. Cleanup MUST also remove receiver-owned drop-point blob directories for which no metadata row exists. A deletion or pointer-clear failure MUST leave work retryable by a later cleanup run.

## 9. Configuration surface

DropPoint configuration contains:

```json
{
  "listen_addr": "127.0.0.1:8080",
  "base_url": "https://drop.example.com",
  "data_dir": "/var/lib/droppoint",
  "default_ttl_seconds": 600,
  "max_ttl_seconds": 900,
  "default_max_bytes": 52428800,
  "max_bytes": 52428800,
  "default_max_active_drop_points": 3,
  "read_timeout_seconds": 600,
  "write_timeout_seconds": 600,
  "cleanup_interval_seconds": 60,
  "terminal_retention_seconds": 2592000
}
```

The reference `droppoint` single-binary implementation provides this operator command surface:

```text
droppoint serve --config ./config.json
droppoint --config ./config.json
droppoint token add --id desktop-main [--max-active 3] [--config ./config.json]
droppoint token list [--config ./config.json]
droppoint token disable --id desktop-main [--config ./config.json]
droppoint token remove --id desktop-main [--config ./config.json]
droppoint token generate
droppoint cleanup expired --config ./config.json
```

`serve` starts the HTTP relay and runs lifecycle reconciliation at startup and periodically. Running `droppoint` without an explicit subcommand MAY default to `serve`. `token add` manages SQLite token rows and prints the newly generated plaintext token exactly once. `token generate` MAY remain available as a standalone token/hash utility but MUST NOT be required for normal token management. `cleanup expired` marks elapsed non-terminal drop points expired, reconciles every terminal row and orphan blob directory idempotently, and purges terminal metadata rows older than the configured retention window after their blob pointers have been cleared.

Configuration rules:

- API tokens MUST be managed in SQLite via the token CLI.
- `base_url` MUST include scheme and host and MUST NOT include query or fragment components.
- Sender-facing drop pages MUST be served to browsers over HTTPS, except for browser-recognized local secure contexts such as `localhost`.
- LAN-IP-over-HTTP is not supported for browser encryption because WebCrypto requires a secure context.
- The canonical system data directory is `/var/lib/droppoint`; local development configurations MAY use a project-local data directory.
- The shipped default encrypted payload limit is 52428800 bytes, and the shipped maximum encrypted payload limit is 52428800 bytes.
- `read_timeout_seconds`, `write_timeout_seconds`, and `cleanup_interval_seconds` MUST be positive. Defaults SHOULD allow slow mobile uploads up to the configured payload limit.
- `terminal_retention_seconds` MUST be positive. The local implementation MUST purge terminal SQLite rows older than this retention window after any ciphertext pointers for those rows have been cleared.
- Plaintext API tokens MUST NOT be stored.
- `api_tokens.secret_hash` rows use `sha256:<lowercase-hex-sha256>` for high-entropy random API tokens.
- `token add` MUST generate a high-entropy plaintext API token, store only the hash, and print the plaintext token exactly once.
- `token disable` MUST make an API token immediately invalid for new drop point creation without restarting the relay.
- `token add`, `token disable`, and `token remove` SHOULD write structured audit log events without plaintext token material.

## 10. Encryption protocol

DropPoint supports a single encryption protocol: X25519 ECDH, HKDF-SHA256, and AES-256-GCM using the exact wire values below.

Earlier local notes call this design "v2" because it followed an RSA-OAEP draft. DropPoint does not implement the RSA-OAEP design. This specification treats the X25519 design as the only supported DropPoint encryption protocol. The fixed wire identifier remains `protocol_version: 2` to match the detailed protocol definition; this does not imply support for an earlier DropPoint encryption protocol.

### 10.1 Encoding rules

- Byte concatenation is written as `||`.
- X25519 public keys, private keys, and shared secrets are raw 32-byte RFC 7748 values.
- X25519 keys MUST NOT be encoded as SPKI, DER, or PEM in this protocol.
- Binary values in JSON or URL fragments use base64url without padding.
- HKDF `info` labels and AAD field labels are exact ASCII byte strings with no trailing newline, NUL, or padding.
- AES-GCM ciphertext fields are stored as `ciphertext || tag`, where the tag is the final 16 bytes.

### 10.2 Fixed primitives

| Purpose | Algorithm | Parameters |
| --- | --- | --- |
| Key agreement | X25519 | Raw 32-byte public keys. |
| Key derivation | HKDF-SHA256 | 32-byte output per derived key. |
| Payload encryption | AES-256-GCM | 12-byte nonce, 16-byte tag. |
| Metadata encryption | AES-256-GCM | 12-byte nonce, 16-byte tag. |

Implementations MUST NOT accept alternative algorithms, nonce sizes, tag sizes, or key encodings for this protocol.

### 10.3 Receiver key pair

For each drop point, the receiver generates:

```text
recipient_private_key  = 32 raw bytes, kept locally
recipient_public_key   = 32 raw bytes, sent to sender in URL fragment
```

The receiver private key MUST NOT be sent to the relay. The receiver MUST delete the private key when the drop point is completed, closed, or expired.

### 10.4 Sender ephemeral key pair

For each drop, the sender generates a fresh X25519 ephemeral key pair:

```text
sender_ephemeral_private_key = 32 raw bytes, sender-local only
sender_ephemeral_public_key  = 32 raw bytes, stored in envelope
```

The sender MUST NOT reuse an ephemeral key pair across drops.

### 10.5 X25519 shared secret

Sender:

```text
shared_secret = X25519(sender_ephemeral_private_key, recipient_public_key)
```

Receiver:

```text
shared_secret = X25519(recipient_private_key, sender_ephemeral_public_key)
```

If the shared secret is 32 zero bytes, implementations MUST reject the operation. The shared secret MUST NOT be used directly as an AES key.

### 10.6 HKDF-SHA256 key derivation

Both derived keys use the same salt:

```text
salt = sender_ephemeral_public_key || recipient_public_key
```

The salt is exactly 64 bytes.

The exact HKDF info labels are:

```text
INFO_METADATA = "DropPoint/protocol/v2 key=metadata"
INFO_PAYLOAD  = "DropPoint/protocol/v2 key=payload"
```

Keys are derived as:

```text
metadata_key = HKDF-SHA256(IKM = shared_secret, salt = salt, info = INFO_METADATA, L = 32)
payload_key  = HKDF-SHA256(IKM = shared_secret, salt = salt, info = INFO_PAYLOAD,  L = 32)
```

### 10.7 AAD

Each AES-GCM operation uses AAD bound to the protocol identifier and field purpose:

```text
AAD = protocol_version (1 byte, 0x02)
   || field_label
```

Concrete values:

```text
AAD_METADATA = 0x02 || "metadata"
AAD_PAYLOAD  = 0x02 || "payload"
```

### 10.8 Nonces

- `metadata_nonce` is 12 random bytes from a CSPRNG.
- `payload_nonce` is 12 random bytes from a CSPRNG.
- Nonces are generated independently.
- Fixed or deterministic nonces MUST NOT be used.

Metadata and payload use separate derived AES keys, so equality between the two nonce byte strings is not catastrophic, but implementations still MUST generate them independently.

### 10.9 Drop link fragment

The receiver appends this fragment to the drop link:

```text
#v=2&pk=<base64url(recipient_public_key, 32 raw bytes)>&exp=<urlencoded RFC3339 expires_at>
```

The `exp` value is optional for backward compatibility. Current relay-served sender pages fetch the authoritative expiry and display name from `GET /api/drops/:drop_token`, but clients SHOULD continue to include `exp` for compatibility with older pages.

Example full drop link:

```text
https://drop.example.com/drop/drop_...#v=2&pk=<base64url-raw-x25519-public-key>&exp=2026-06-30T12%3A15%3A00Z
```

Rules:

- `v=2` identifies the fixed wire protocol in this section.
- `pk` is the raw 32-byte X25519 receiver public key encoded as base64url without padding.
- `exp`, when present, is the drop point `expires_at` timestamp encoded with `encodeURIComponent` / URL encoding.
- The display name is intentionally not authoritative when supplied in a URL; the sender page uses the server-bound metadata endpoint instead.
- The fragment is not sent to the relay by normal HTTP requests.
- The drop page MUST reject missing, non-base64url, or non-32-byte `pk` values.
- The drop page MUST reject an invalid `exp` value when it is present.
- The drop page MUST feature-detect X25519 support in WebCrypto and show an explicit unsupported-environment error if unavailable.

Native WebCrypto X25519 support is required for the relay-served drop page. It reached all major browser engines by 2025, with Chrome stable support in Chrome 137. Operators serving unmanaged or old sender browsers SHOULD document that unsupported browsers will see the explicit unsupported-environment error.

### 10.10 Encryption procedure

The sender:

1. Decodes `recipient_public_key` from the URL fragment and verifies it is 32 bytes.
2. Generates a fresh sender ephemeral X25519 key pair.
3. Computes the X25519 shared secret and rejects all-zero output.
4. Derives `metadata_key` and `payload_key` using Section 10.6.
5. Builds the bundle manifest and concatenated `payload_plaintext` using Section 11.
6. Encrypts the payload:

   ```text
   encrypted_payload = AES-256-GCM(payload_key, payload_nonce, AAD_PAYLOAD, payload_plaintext)
   ```

7. Encrypts the manifest:

   ```text
   encrypted_metadata = AES-256-GCM(metadata_key, metadata_nonce, AAD_METADATA, manifest_json_bytes)
   ```

8. Builds the envelope and submits the envelope plus encrypted payload.
9. Discards sender ephemeral private key and derived keys.

### 10.11 Decryption procedure

The receiver:

1. Fetches the envelope and encrypted payload from the relay.
2. Parses and validates envelope field lengths.
3. Computes the X25519 shared secret and rejects all-zero output.
4. Reconstructs the salt as `sender_ephemeral_public_key || recipient_public_key`.
5. Derives `metadata_key` and `payload_key` using Section 10.6.
6. Decrypts and authenticates `encrypted_metadata` using `AAD_METADATA`.
7. Decrypts and authenticates `encrypted_payload` using `AAD_PAYLOAD`.
8. Verifies the manifest and splits payload bytes using Section 11.
9. Sanitizes filenames and treats MIME types as untrusted using Section 12.
10. Deletes the receiver private key when no longer needed.

Any authentication failure MUST reject the drop.

### 10.12 Envelope JSON

The envelope is JSON:

```json
{
  "protocol_version": 2,
  "key_agreement": "x25519-hkdf-sha256-aesgcm-raw32",
  "sender_ephemeral_public_key": "<base64url, 32 raw bytes>",
  "metadata_nonce": "<base64url, 12 bytes>",
  "payload_nonce": "<base64url, 12 bytes>",
  "encrypted_metadata": "<base64url, ciphertext||tag>"
}
```

Rules:

- `protocol_version` MUST be integer `2` and is authoritative.
- Transport headers MUST NOT override the envelope `protocol_version`.
- `key_agreement` MUST be `x25519-hkdf-sha256-aesgcm-raw32`.
- JSON field order has no meaning.
- Canonical JSON is not required.
- Receivers MUST reject unsupported `protocol_version` values.

### 10.13 Multipart framing

Drop requests use `multipart/form-data` with:

- `envelope` (`application/json`): envelope JSON.
- `payload` (`application/octet-stream`): raw encrypted payload bytes.

Pickup responses use `multipart/mixed` with the same two logical parts.

Cryptographic material MUST NOT be carried in custom `X-*` headers, except that a protocol-version header MAY be used as a non-authoritative routing hint.

### 10.14 Payload size and buffering constraints

This protocol version encrypts each bundle as one AES-GCM message. Sender implementations generally need the selected bundle bytes, manifest, and resulting ciphertext available as whole messages while encrypting. Receiver implementations MUST NOT use decrypted payload bytes as trusted plaintext until AES-GCM authentication has succeeded for the whole payload. The relay may stream ciphertext for storage and pickup, but cryptographic clients should treat configured `max_bytes` as a memory and authentication boundary as well as a network/storage boundary.

Chunked or framed AEAD payloads are outside this protocol version and require a future incompatible protocol version.

## 11. Bundle manifest

Encrypted metadata is always a bundle manifest, even for a single file.

Manifest plaintext JSON:

```json
{
  "protocol_version": 2,
  "files": [
    { "name": "scan-01.jpg", "type": "image/jpeg", "size": 482113 },
    { "name": "scan-02.jpg", "type": "image/jpeg", "size": 511044 }
  ],
  "created_at": "2026-06-30T12:00:00Z"
}
```

Rules:

- `files` is an ordered array with one entry per file.
- Each file entry has `name`, `type`, and `size`.
- `size` is the original plaintext byte length and MUST be a non-negative integer.
- `created_at` is the sender-side bundle creation time in ISO 8601 format.
- `payload_plaintext` is the concatenation of file bytes in `files` order.
- The receiver MUST verify that the sum of all `size` values equals `len(payload_plaintext)`.
- If the size sum does not match, the receiver MUST reject the bundle.

The manifest is encrypted and authenticated as metadata; filenames, MIME types, and per-file sizes are not visible to the relay except for ciphertext length leakage.

## 12. Receiver validation obligations

Decrypted manifest fields are sender-controlled. Receivers MUST NOT trust filenames, MIME types, or timestamps.

For each manifest entry, the receiver MUST:

- Sanitize the filename to a safe base name.
- Remove or reject path separators, NUL bytes, and control characters.
- Reject empty filenames, absolute paths, and platform-reserved names.
- Reject or disambiguate duplicate filenames within the bundle.
- Never let a filename choose the destination directory.
- Treat MIME type as advisory and prefer content-based type detection where relevant.
- Verify the manifest size sum before writing recovered files.

Receivers SHOULD store files by content hash or another receiver-controlled safe name and keep the original filename only as display metadata.

## 13. Security model

DropPoint protects against:

- Network observers.
- Passive or honest-but-curious relay operators.
- Accidental exposure of relay storage.
- Stolen relay backups containing only ciphertext and token hashes.
- Server-side or third-party ciphertext modification, detected by AES-GCM authentication.
- Metadata/payload substitution, detected by separate keys and AAD.
- Re-targeting ciphertext to another receiver key, prevented by HKDF salt binding to both public keys.
- Protocol downgrade within this wire protocol, detected by HKDF labels, AAD, and envelope validation.
- X25519 low-order point abuse, by rejecting all-zero shared secrets.

DropPoint does not protect against:

- A malicious relay operator that serves modified sender-side JavaScript.
- A compromised sender browser or device.
- A compromised receiver device.
- Someone else scanning or copying the drop link and dropping junk first.
- Sender impersonation; possession of the drop token is the sender capability.
- File size and timing leakage through operational metadata.
- Decryption of saved ciphertext if the receiver private key is later stolen before it is deleted.
- Quantum adversaries.

When the relay serves the drop page JavaScript, the relay origin is part of the trusted computing base for active attacks. DropPoint MUST NOT be described as zero-trust E2EE against the relay operator unless the sender uses independently verified client code outside the relay-served page.

## 14. Operational metadata and privacy

The relay may store and expose only limited plaintext operational metadata:

- Drop point ID.
- API token ID label.
- Client name label.
- Display name.
- Status.
- Encrypted payload size.
- Created, dropped, picked-up, closed, and expiry timestamps.
- Storage object paths.

The relay MUST NOT store plaintext filenames, MIME types, bundle manifests, file contents, private keys, or derived encryption keys.

## 15. Logging and metrics

Logs MUST NOT include:

- Raw API tokens, drop tokens, or pickup tokens.
- Public key fragments.
- Private keys or derived keys.
- Envelope contents.
- Plaintext filenames, MIME types, or manifest data.
- Payload bytes.

HTTP access logs MUST redact token-bearing paths such as `/drop/:drop_token` and `/api/drops/:drop_token`.

Recommended structured log events include:

```json
{ "event": "drop_point.created", "drop_point_id": "dp_...", "api_token_id": "desktop-main", "expires_at": "2026-06-30T12:15:00Z" }
```

```json
{ "event": "drop.completed", "drop_point_id": "dp_...", "encrypted_size": 2849123, "dropped_at": "2026-06-30T12:03:12Z" }
```

```json
{ "event": "payload.picked_up", "drop_point_id": "dp_...", "first_pickup": true }
```

Metrics SHOULD avoid high-cardinality or sensitive labels such as drop point IDs, token prefixes, IP addresses, filenames, MIME types, and storage paths.

Recommended metric names include:

- `droppoint_drop_points_created_total`
- `droppoint_drop_points_open`
- `droppoint_drop_points_expired_total`
- `droppoint_drop_points_closed_total`
- `droppoint_drops_completed_total`
- `droppoint_drops_failed_total`
- `droppoint_pickups_total`
- `droppoint_payload_bytes_total`
- `droppoint_cleanup_runs_total`
- `droppoint_cleanup_deleted_payloads_total`

Recommended metric labels are `api_token_id`, `status`, `result`, and `reason`. Metrics MUST NOT use high-cardinality labels such as `drop_point_id`, token prefixes, IP address, filename, MIME type, or storage path.

## 16. Sender-facing page requirements

The drop page MUST:

- Run in a secure browser context.
- Read the drop token from the path and the public key from the fragment.
- Accept one or more files and build one encrypted bundle.
- Append newly chosen or dragged files to the current selection until the sender drops or removes files.
- Let the sender remove individual files from the current selection before dropping.
- Use the protocol in Section 10.
- Submit a single envelope and a single encrypted payload.
- Display clear states for missing key, unsupported browser, encrypting, dropping, success, expiry, and failure.
- Use no third-party scripts.
- Avoid leaking plaintext metadata into URLs, logs, local storage, analytics, or error reporting.

Recommended English UI copy:

| UI element | Copy |
| --- | --- |
| Page title | `Drop files` |
| Intro | `This drop point accepts one encrypted file bundle.` |
| File picker | `Choose files` |
| Submit | `Drop encrypted files` |
| In progress | `Encrypting and dropping files...` |
| Success | `Files dropped successfully.` |
| Expired | `This drop point has expired.` |
| Missing key | `This drop link is missing its public key.` |
| Insecure context | `This page must be opened over HTTPS or localhost to encrypt files.` |

## 17. Deployment requirements

DropPoint is deployment-neutral. It may run behind a reverse proxy, tunnel, CDN, or direct TLS termination as long as these externally visible requirements hold:

- `/drop/:drop_token` is reachable by sender browsers.
- `/api/drops/:drop_token` is reachable by sender browsers for metadata lookup and encrypted upload.
- Sender browsers see an HTTPS origin or another browser-recognized secure context.
- Receiver APIs are reachable by receiver clients.
- Request body size limits at the application and every edge layer are compatible with configured `max_bytes` plus multipart overhead.
- Operators redact token-bearing paths at proxies, tunnels, CDNs, and application logs.
- Browser CORS policy is appropriate for same-origin drop page use and does not treat CORS as an authorization mechanism for receiver APIs.

## 18. Generic client integration boundary

DropPoint is generic and client-agnostic. Client applications decide how to store decrypted files, how to represent imported attachments, and when to close a drop point.

Client integrations MUST append durable local records only after:

1. Pickup succeeds.
2. Decryption and authentication succeed.
3. Manifest validation succeeds.
4. Filename/MIME sanitization succeeds.
5. Plaintext is durably stored in the client-controlled system.

Only after those steps SHOULD the receiver close the remote drop point.
