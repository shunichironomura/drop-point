# DropPoint API reference

All JSON fields use `snake_case`. Receiver APIs use bearer tokens. Sender drops use the drop token in the URL path. DropPoint APIs do not rely on cookies.

## Create drop point

```sh
curl -sS https://drop.example.com/api/drop-points \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"client_name":"desktop","ttl_seconds":600,"max_bytes":52428800}'
```

The request body must be one non-null JSON object with `Content-Type: application/json` (media-type parameters are accepted). Omitted `ttl_seconds` and `max_bytes` use configured defaults; explicit zero or `null` is invalid. Optional `client_name`, when present, must be a non-null, non-blank string of at most 128 UTF-8 bytes without Unicode control or format characters.

Response:

```json
{
  "drop_point_id": "dp_...",
  "display_name": "calm-otter",
  "drop_link": "https://drop.example.com/drop/drop_...",
  "pickup_token": "pick_...",
  "expires_at": "2026-06-30T12:15:00Z",
  "max_bytes": 52428800,
  "max_pending_submissions": 10,
  "max_pending_bytes": 524288000
}
```

Display `display_name` to the receiver so the sender can compare it with the name shown on the drop page. Append the receiver-generated public key and returned expiry timestamp locally:

```text
#v=2&pk=<base64url(raw-32-byte-x25519-public-key)>&exp=<urlencoded expires_at>
```

The `exp` fragment parameter is optional for compatibility. Current sender pages fetch the authoritative display name and expiry from the relay using the drop token.

## Sender metadata

The static sender page fetches server-bound, sender-safe metadata before enabling uploads:

```sh
curl -sS https://drop.example.com/api/drops/$DROP_TOKEN
```

Response:

```json
{
  "display_name": "calm-otter",
  "expires_at": "2026-06-30T12:15:00Z",
  "max_bytes": 52428800,
  "max_pending_submissions": 10,
  "max_pending_bytes": 524288000
}
```

This endpoint is authorized only by the drop token and does not expose receiver-only pickup state.

## Sender encrypted drop framing

The browser page submits:

```http
PUT /api/drops/:drop_token/submissions/:submission_id
Content-Type: multipart/form-data
```

The sender generates a fresh `submission_id` for each bundle. It must use the `sub_` prefix followed by 128 to 256 bits of CSPRNG entropy encoded as unpadded base64url.

The request contains exactly two ordered parts:

1. `envelope`, `application/json`, at most 1048576 bytes (1 MiB);
2. `payload`, `application/octet-stream`, at most the drop point's `max_bytes`.

Reordered, missing, duplicated, or additional parts are rejected. The total request allowance is `max_bytes` plus the 1 MiB envelope cap plus 65536 bytes (64 KiB) reserved for multipart framing overhead; `max_bytes` itself applies only to payload bytes.

The relay validates only the envelope shape and stores ciphertext. It does not decrypt metadata or payload. A committed submission is immutable. Retrying its ID returns the existing status without replacing ciphertext. Failed or interrupted attempts are cleaned up without consuming the ID or failing the reusable parent session; startup and periodic reconciliation recover interrupted receiving attempts.

Submission errors use these status classes:

- `400` for malformed envelope, multipart, or uploader-read input;
- `413` for encrypted payload/request-size violations;
- `429` when the pending submission count or byte queue is full;
- `507` for known storage-capacity exhaustion such as disk full;
- `503` for known transient storage unavailability;
- `500` for other durable-storage or internal finalization failures.

## Poll status

```sh
curl -sS https://drop.example.com/api/drop-points/$DROP_POINT_ID/status \
  -H "Authorization: Bearer $PICKUP_TOKEN"
```

Response:

```json
{
  "status": "open",
  "display_name": "calm-otter",
  "expires_at": "2026-06-30T12:15:00Z",
  "pending_submissions": 2,
  "pending_bytes": 5698246,
  "max_pending_submissions": 10,
  "max_pending_bytes": 524288000
}
```

The parent remains `open` while submissions are queued. `pending_submissions` counts both receiving and ready children; `pending_bytes` counts ready encrypted payload bytes.

## List ready submissions

```sh
curl -sS https://drop.example.com/api/drop-points/$DROP_POINT_ID/submissions \
  -H "Authorization: Bearer $PICKUP_TOKEN"
```

Response:

```json
{
  "submissions": [
    {
      "submission_id": "sub_...",
      "encrypted_size": 2849123,
      "dropped_at": "2026-06-30T12:03:12Z",
      "first_picked_up_at": null
    }
  ]
}
```

Only ready children are listed, ordered by `dropped_at` and then `submission_id`.

## Pickup encrypted submission

```sh
curl -sS https://drop.example.com/api/drop-points/$DROP_POINT_ID/submissions/$SUBMISSION_ID/pickup \
  -H "Authorization: Bearer $PICKUP_TOKEN" \
  -o pickup.multipart
```

The response is `multipart/mixed` with the same logical `envelope` and `payload` parts. Pickup is repeatable and does not acknowledge the submission or close the drop point. The relay records a successful pickup only after it writes the full GET multipart response without a response-writer error; HEAD and partial/failed writes do not count. Timestamp finalization is detached from request cancellation and survives a concurrent close or expiry.

## Acknowledge submission

After decrypting, validating, and durably installing the complete bundle, acknowledge that child:

```sh
curl -i -X DELETE https://drop.example.com/api/drop-points/$DROP_POINT_ID/submissions/$SUBMISSION_ID \
  -H "Authorization: Bearer $PICKUP_TOKEN"
```

Acknowledgement first records terminal child state and then deletes that child's ciphertext. It is idempotent and frees its queue capacity. The parent stays open for more submissions.

## Close drop point

```sh
curl -i -X DELETE https://drop.example.com/api/drop-points/$DROP_POINT_ID \
  -H "Authorization: Bearer $PICKUP_TOKEN"
```

Close marks the drop point closed and removes all remaining child ciphertext. Retrying close is safe. Do this only when the reusable session should stop accepting new submissions.

## Health

```sh
curl -sS https://drop.example.com/health
```

The health response is unauthenticated and intentionally low-information.
