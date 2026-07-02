# Codebase Review Report

Date: 2026-07-02
Scope: full repository at commit `1789548` — Go relay (`cmd/`, `internal/`), sender web page (`web/drop-page/`), Python client scripts (`scripts/`), deployment files, and `SPEC.md` conformance.

This report lists possible bugs, code smells, and potential security vulnerabilities found during a manual review. Findings are ordered by severity within each section. Line numbers refer to the commit above.

## Summary

The codebase is in good shape overall: capability tokens carry 256 bits of CSPRNG entropy and are stored hashed with constant-time comparison, blob writes are atomic with fsync and rename, the drop page uses a strict CSP with no third-party scripts, SQL uses bound parameters throughout, request bodies are size-capped, and log paths are token-redacted. Test coverage is broad, including lifecycle-parity and negative-vector tests.

The most significant findings are: (1) an aborted upload can leave a drop point stuck in `receiving` until TTL expiry because cleanup reuses the canceled request context, violating the spec's single-use-slot rule; (2) the running relay never deletes expired ciphertext on its own — deletion depends entirely on an operator cron job; and (3) the blob store's ID validation accepts `..`, making `DeleteDropPoint("..")` equivalent to deleting the entire data directory (not attacker-reachable today, but a one-bug-away hazard).

## Bugs

### B1 (high): Aborted drop consumes the single-use slot until TTL expiry

`internal/httpapi/drop.go:50-55` — the deferred failure cleanup runs repository calls on `r.Context()`:

```go
defer func() {
    if !committed {
        _ = deps.BlobStore.DeleteDropPoint(r.Context(), dp.ID)
        _ = deps.Repository.ResetReceivingDrop(r.Context(), dp.ID, deps.Now().UTC())
    }
}()
```

When a sender disconnects mid-upload (dropped Wi-Fi, closed tab, read timeout), `net/http` cancels the request context. `ResetReceivingDrop` then fails immediately with `context.Canceled`, the error is discarded, and the row stays in `receiving`. Every subsequent drop attempt gets `409 drop_already_exists` until the TTL expires. This violates SPEC §5: "A failed or partial drop MUST NOT consume the single-use slot; the drop point MUST return to `open`." Interrupted mobile uploads are the primary use case, so this is reachable in normal operation.

The same problem affects the commit path: if the client disconnects after the payload is fully received but before `CommitReceivedDrop` (`drop.go:73`) runs, the canceled context fails the commit *and* the cleanup, leaving the row stuck in `receiving` with its blobs deleted.

**Fix:** run the deferred cleanup (and arguably the commit) on `context.WithoutCancel(r.Context())` or a fresh short-timeout background context.

### B2 (medium): The running relay never deletes expired ciphertext

SPEC §4 step 9: "The relay deletes stored ciphertext when the drop point is closed or expires." In the implementation, expiry only flips row status lazily on access (`internal/store/repository.go:125-129`, `158-161`); blob deletion for expired drop points happens only when an operator runs `drop-point cleanup expired` (documented as a cron job in `docs/deployment.md`). `internal/server/server.go` starts no background cleanup loop. Two consequences:

- If the cron job is missing or broken, encrypted payloads persist on disk indefinitely past their TTL — a data-retention gap in a product whose core promise is short-lived storage.
- The receiver cannot compensate: `HandleCloseDropPoint` (`internal/httpapi/receiver.go:57-59`) returns `410` for an expired drop point *before* the blob-deletion branch, so a diligent receiver closing an expired drop still leaves its ciphertext on disk.

**Fix:** run the existing `cleanup.Service` on a ticker inside `Server.ListenAndServe`, and/or delete blobs on the expired-close path before returning `410`.

### B3 (medium): `blobstore.validateID` accepts `..` and `.` — `RemoveAll` on the data directory

`internal/blobstore/blobstore.go:202-207`:

```go
func validateID(id string) error {
    if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
        return fmt.Errorf("invalid drop point id %q", id)
    }
    return nil
}
```

`filepath.Base("..") == ".."` and `..` contains no separators, so `validateID("..")` passes. `dropDir("..")` resolves to the data directory itself, so `DeleteDropPoint("..")` executes `os.RemoveAll(dataDir)` — deleting `relay.db` and every stored payload. `validateID(".")` similarly resolves to the whole `drop-points/` tree. This is not attacker-reachable through the HTTP API today (IDs are server-generated and every handler path requires a matching DB row first), but it converts any future bug that lets a crafted ID reach the blob store into total data destruction.

**Fix:** reject `.` and `..` explicitly (and ideally require the `dp_` prefix / base64url alphabet).

### B4 (medium): Drop page can build bundles the reference receiver is guaranteed to reject

`web/drop-page/app.js` appends picker/drag files to the selection without deduplication (`app.js:35`, `79-90`), and `buildEncryptedBundle` copies names into the manifest as-is. Both reference receivers reject duplicate filenames case-insensitively (`internal/cryptoenv/manifest.go:85-88`, `scripts/drop_point_protocol.py:178-181`). Selecting the same file twice — or two different files that share a name, e.g. `photo.jpg` from two folders, which is common on mobile — produces a drop that uploads successfully and then fails at decrypt time on the receiver, permanently consuming the single-use slot with an unusable payload.

**Fix:** deduplicate or disambiguate names in the sender page before encrypting (SPEC §12 puts disambiguation on receivers, but the sender page should not manufacture guaranteed-invalid bundles).

### B5 (low): `max_bytes` is fetched by the drop page but never enforced client-side

`app.js` stores `state.maxBytes` (`app.js:57`, validated at `276-281`) and never reads it again. A sender selecting an oversized bundle reads every file into memory, encrypts it, uploads the whole ciphertext, and only then gets a server rejection — reported as the generic "Network failure or drop point rejected the encrypted files" because `dropSelectedFiles` (`app.js:229-237`) has no handling for `413`. A pre-encryption size check plus a specific over-limit message would fail fast and match SPEC §10.14's guidance to treat `max_bytes` as a memory boundary.

### B6 (low): Body-limit overflow returns 400 instead of 413

`internal/httpapi/drop.go:57` wraps the body in `http.MaxBytesReader`, but `writeMultipartDropError` (`drop.go:155-164`) has no case for `*http.MaxBytesError`, so a request exceeding the overall cap maps to `400 drop_multipart_invalid` rather than `413`. (Payload-part overflow is correctly detected as `ErrPayloadTooLarge` → 413 via the blob store; this case covers oversized envelopes/framing.)

### B7 (low): Success message on the drop page is later replaced by an expiry error

After a successful drop, `dropSelectedFiles` never calls `stopExpiryCountdown()`. When the TTL later elapses in the open tab, `renderExpiryCountdown` (`app.js:325-334`) calls `showError('This drop point has expired.')`, replacing the "Files dropped successfully" confirmation with an error — confusing for a sender who left the tab open.

### B8 (low): Close race can mark an expired drop point `closed`

`Repository.CloseDropPoint` (`internal/store/repository.go:252-275`) does a read-check then an unconditional `UPDATE ... WHERE id = ?` with no status guard. Between the read and the write, another request (e.g. the cleanup job) can mark the row `expired`; the update then overwrites a terminal `expired` status with `closed`, violating the state machine in SPEC §5. Same shape exists in the close handler (`receiver.go:65-74`): blob deletion, file-pointer clearing, and the status transition are three non-atomic steps, so a crash in between can leave a `ready` row with NULL paths (pickup then returns 500). Low impact, but a `WHERE status IN (...)` guard on the final UPDATE is cheap.

### B9 (low): Python `_split_payload` accepts booleans as sizes

`scripts/drop_point_protocol.py:184` — `isinstance(size, int)` is `True` for `bool` (a JSON `true` size passes as `1`). Harmless in practice, but `isinstance(size, bool)` should be excluded for strictness parity with the Go validator, which rejects it as a JSON type error.

## Potential security vulnerabilities

### S1 (medium): Missing `Permissions-Policy` header on the drop page

SPEC §7.2 requires "defensive sender-facing security headers such as `X-Content-Type-Options: nosniff`, `Referrer-Policy`, and a restrictive `Permissions-Policy`." `setDropPageSecurityHeaders` (`internal/httpapi/drop_page.go:47-52`) sets CSP, nosniff, Referrer-Policy, and COOP, but no `Permissions-Policy` — a direct spec deviation. Adding e.g. `Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=()` closes it.

### S2 (low): Expired/closed rows are retained in SQLite forever

Nothing ever deletes rows from `drop_points`; `cleanup expired` only flips status and removes blobs. Token hashes, display names, client names, and full timestamp trails for every drop point ever created accumulate indefinitely. This conflicts with the product's minimal-retention posture (SPEC §14 limits what may be stored but the intent is short-lived handoffs) and grows the table without bound. Consider a retention window that purges terminal rows.

### S3 (low): Receiver helper scripts never delete the private key

SPEC §10.3: "The receiver MUST delete the private key when the drop point is completed, closed, or expired." `scripts/drop-point-receiver.py` writes `recipient_private_key` into the state file and leaves it there after a successful pickup-and-close (`pickup_drop`, lines 106-122). `drop-point-qr.py` behaves the same. These are dev/test helpers, but they model the client contract; deleting or overwriting the state file after close would match the spec.

### S4 (informational): No rate limiting on any endpoint

Token guessing is infeasible (256-bit secrets), and quota bounds drop-point creation per valid token, but there is no throttle on repeated invalid `Authorization` attempts against `POST /api/drop-points` or on drop-token probing. Given SHA-256-per-candidate verification cost is trivial, this is only a nuisance/DoS-hygiene concern; a reverse-proxy rate limit is a reasonable operator mitigation and could be mentioned in `docs/deployment.md`.

### S5 (informational): Committed `.env` normalizes secret-adjacent config in VCS

`.env` is checked in and is where `DROP_POINT_API_TOKENS_JSON` lives in the compose flow. Today it contains only placeholders and the comments correctly forbid plaintext tokens (hashes of 256-bit random tokens are safe to commit), but users following the flow may commit their real deployment `.env` including host-binding decisions. A `.env.example` + gitignored `.env` pattern is the safer convention.

## Code smells

1. **Dead-ish repository API.** `Repository.CreateDropPoint` and `CountActiveDropPointsByAPITokenID` (`internal/store/repository.go:52`, `315`) are used only by tests; production uses `CreateDropPointWithinQuota`. The count query also duplicates the quota predicate embedded in `insertDropPointWithinQuotaSQL` — two places to keep in sync.
2. **Mutable package-level AAD slices.** `cryptoenv.AADMetadata` / `AADPayload` (`internal/cryptoenv/reference.go:22-25`) are exported `[]byte` vars; any accidental mutation would silently break the protocol. Prefer functions or copies.
3. **Fixed 2-minute read/write timeouts.** `internal/server/server.go:19-24` hard-codes `ReadTimeout`/`WriteTimeout` at 2 minutes while `max_bytes` defaults to 50 MiB. A legitimate sender on a slow mobile uplink (< ~3.5 Mbps) cannot complete an upload; the values are not configurable.
4. **`statusRecorder` hides optional interfaces.** `internal/httpapi/middleware.go:68-96` wraps the `ResponseWriter` without implementing `Unwrap()`/`http.Flusher`, so `http.ResponseController` flush/deadline operations degrade silently for streamed pickups if ever needed.
5. **Raw exception text shown to senders.** `init().catch((error) => showError(error.message || ...))` (`app.js:33`) surfaces raw browser exception messages (e.g. `atob` `InvalidCharacterError` from a malformed fragment) instead of the spec's UI copy.
6. **HEAD returns 405 on GET endpoints.** `getOnly` (`internal/httpapi/router.go:57-65`) deliberately rejects HEAD on `/drop/:token` and API GETs. Harmless, but link-preview bots and some monitors probe with HEAD; the asymmetry with `/health` (which allows HEAD) reads as accidental.
7. **Time parsing is broader than time writing.** Writes use the fixed-width `sqliteTimeFormat` so TEXT comparison on `expires_at` stays chronological (`internal/store/repository.go:16-18`), but `parseTime` accepts any RFC3339Nano. Fine today; a trap if any other writer (manual SQL, future migration) inserts a differently-formatted timestamp, since ordering comparisons would silently misbehave.
8. **`store.Open` passes a raw path as the DSN.** `sql.Open("sqlite", databasePath)` (`internal/store/store.go:32`) — a data directory containing `?` would be parsed as DSN parameters by the modernc driver. Purely theoretical for operator-chosen paths; `file:`-URI quoting would remove the edge.

## Positive observations

- Constant-time hash comparison for all capability tokens; raw tokens never persisted or logged; token-bearing paths redacted in access logs, matching SPEC §15.
- Atomic blob installation (temp file → fsync → rename → dir sync) with idempotent deletes matches SPEC §8.
- Single-statement quota-checked insert avoids a check-then-insert race; `BeginReceivingDrop` claims the single-use slot atomically with a status+expiry guard.
- Envelope validation enforces exact field lengths, unpadded base64url, unknown-field rejection, and single-JSON-value bodies on every input path (relay, reference Go, Python, JS behaviors are consistent).
- Drop page: strict CSP, no third-party scripts, `credentials: 'omit'`, secure-context and X25519 feature detection, thumbnails via `blob:` URLs only.
- CI runs gofmt, vet, race tests, staticcheck, and govulncheck with pinned action digests; the container image is distroless non-root.
