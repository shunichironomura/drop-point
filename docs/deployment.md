# Deployment guide

DropPoint is deployment-neutral. It can run behind Cloudflare Tunnel, Caddy, another reverse proxy, a container ingress, direct TLS, or local development routing.

## Build

```sh
go build -o drop-point ./cmd/drop-point
```

## systemd example

```ini
[Unit]
Description=DropPoint relay
After=network-online.target
Wants=network-online.target

[Service]
User=drop-point
Group=drop-point
ExecStart=/usr/local/bin/drop-point serve --config /etc/drop-point/config.json
Restart=on-failure
StateDirectory=drop-point
ReadWritePaths=/var/lib/drop-point
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

Use `/var/lib/drop-point` as `data_dir` and run:

```sh
drop-point cleanup expired --config /etc/drop-point/config.json
```

from a timer or cron.

## Reverse proxy and tunnel requirements

- `/drop/:drop_token` and `/api/drops/:drop_token` must be reachable by sender browsers.
- Receiver APIs under `/api/drop-points` must be reachable by receiver clients.
- Sender browsers must see HTTPS or localhost. HTTP over a LAN IP is not a secure browser context.
- Request body limits must allow `max_bytes` plus multipart overhead.
- TLS may terminate outside DropPoint.
- `/health` is unauthenticated and low-information.

## Logging guidance

Application logs redact DropPoint token-bearing paths. Configure every proxy, CDN, and tunnel to redact or avoid logging:

- `/drop/:drop_token`
- `/api/drops/:drop_token`
- `Authorization` headers
- query strings and fragments

Do not log envelope contents, public key fragments, filenames, MIME types, manifest JSON, private keys, derived keys, or payload bytes.

## Browser compatibility

The sender page requires secure context WebCrypto and native X25519 support. Unsupported browsers show an explicit unsupported-browser error instead of uploading plaintext.
