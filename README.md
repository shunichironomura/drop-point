# DropPoint

DropPoint is a temporary encrypted file handoff relay. A receiver creates a short-lived drop point, shares a drop link, and later picks up one encrypted bundle. The relay stores ciphertext only and is not a system of record.

DropPoint is a Go single-binary service with SQLite metadata and local filesystem ciphertext storage.

## Security model

DropPoint protects against passive relay storage exposure and network observers by requiring senders to encrypt in the browser before upload. The relay never needs plaintext file bytes, filenames, MIME types, manifests, private keys, or derived keys.

The relay origin is still part of the active JavaScript trusted computing base. A malicious operator that serves modified JavaScript can compromise browser-delivered encryption. Do not describe this deployment as zero-trust E2EE against the relay operator unless senders use independently verified client code.

The legacy RSA-OAEP design from early notes is not implemented. The only supported protocol is X25519/HKDF-SHA256/AES-256-GCM with `protocol_version: 2`.

## Local development

```sh
go test ./...
go run ./cmd/drop-point serve --config config.example.json
```

Generate an API token and put only the printed `secret_hash` in configuration:

```sh
go run ./cmd/drop-point token generate
```

For local browser encryption, use `http://localhost` or HTTPS. LAN-IP-over-HTTP is not a secure browser context and WebCrypto may be unavailable.

## Basic receiver flow

1. Create a drop point with a configured API token.
2. Generate a recipient X25519 key pair locally.
3. Show the returned human-readable drop name to the receiver.
4. Append `#v=2&pk=<base64url(raw-32-byte-public-key)>` to the returned drop link.
5. Share the full drop link with the sender and ask them to compare the drop name shown on the page.
6. Poll status until `ready`.
7. Pick up the encrypted envelope and payload.
8. Decrypt locally, validate the manifest, durably store plaintext if desired, then close the drop point.

See `docs/local-testing.md` for a Python receiver/sender simulation, `docs/api.md` for curl examples, `docs/protocol-reference.md` for protocol vectors, and `docs/client-integration.md` for generic receiver/client integration guidance.

## Operations

- Default local data directory: `.data/drop-point`.
- Canonical system data directory: `/var/lib/drop-point`.
- SQLite database: `relay.db`.
- Ciphertext blobs: `drop-points/<drop-point-id>/envelope.json` and `payload.bin`.
- Cleanup command: `drop-point cleanup expired --config ./config.json`.

Sender-facing pages must be served over HTTPS or localhost. Request body limits at proxies/tunnels must allow configured `max_bytes` plus multipart overhead. Redact token-bearing paths such as `/drop/:drop_token` and `/api/drops/:drop_token` at every logging layer.
