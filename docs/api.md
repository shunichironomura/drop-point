# DropPoint API reference

All JSON fields use `snake_case`. Receiver APIs use bearer tokens. Sender drops use the drop token in the URL path. DropPoint APIs do not rely on cookies.

## Create drop point

```sh
curl -sS https://drop.example.com/api/drop-points \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"client_name":"desktop","ttl_seconds":600,"max_bytes":52428800,"single_use":true}'
```

Response:

```json
{
  "drop_point_id": "dp_...",
  "drop_link": "https://drop.example.com/drop/drop_...",
  "pickup_token": "pick_...",
  "expires_at": "2026-06-30T12:15:00Z",
  "max_bytes": 52428800
}
```

Append the receiver-generated public key fragment locally:

```text
#v=2&pk=<base64url(raw-32-byte-x25519-public-key)>
```

## Sender encrypted drop framing

The browser page submits:

```http
PUT /api/drops/:drop_token
Content-Type: multipart/form-data
```

Parts:

- `envelope`, `application/json`
- `payload`, `application/octet-stream`

The relay validates only the envelope shape and stores ciphertext. It does not decrypt metadata or payload.

## Poll status

```sh
curl -sS https://drop.example.com/api/drop-points/$DROP_POINT_ID/status \
  -H "Authorization: Bearer $PICKUP_TOKEN"
```

Response includes `status`, `encrypted_size`, `dropped_at`, `first_picked_up_at`, and `expires_at`.

## Pickup encrypted payload

```sh
curl -sS https://drop.example.com/api/drop-points/$DROP_POINT_ID/pickup \
  -H "Authorization: Bearer $PICKUP_TOKEN" \
  -o pickup.multipart
```

The response is `multipart/mixed` with the same logical `envelope` and `payload` parts. Pickup is repeatable and does not close or delete the drop point.

## Close drop point

```sh
curl -i -X DELETE https://drop.example.com/api/drop-points/$DROP_POINT_ID \
  -H "Authorization: Bearer $PICKUP_TOKEN"
```

Close marks the drop point closed and removes stored ciphertext if present. Retrying close is safe.

## Health

```sh
curl -sS https://drop.example.com/health
```

The health response is unauthenticated and intentionally low-information.
