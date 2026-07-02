# Deployment guide

DropPoint is deployment-neutral. It can run behind Cloudflare Tunnel, Caddy, another reverse proxy, a container ingress, direct TLS, or local development routing.

## Build

```sh
go build -o drop-point ./cmd/drop-point
```

## Container image

Build the included image:

```sh
docker build -t drop-point:local .
```

Run it with persistent storage and environment configuration:

```sh
docker run --rm \
  -p 8080:8080 \
  -v drop-point-data:/var/lib/drop-point \
  -e DROP_POINT_BASE_URL=https://drop.example.com \
  -e 'DROP_POINT_API_TOKENS_JSON=[{"id":"desktop-main","secret_hash":"sha256:<lowercase-hex-sha256>","enabled":true}]' \
  drop-point:local
```

The image listens on `:8080`, stores data under `/var/lib/drop-point`, and runs as a non-root user.

For the included Compose file, copy the template first so deployment-specific values and token hashes stay out of version control:

```sh
cp .env.example .env
docker compose up --build
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

Use `/var/lib/drop-point` as `data_dir`. The running relay performs expiry cleanup on its configured interval. You may also run:

```sh
drop-point cleanup expired --config /etc/drop-point/config.json
```

from a timer or cron as an operational backstop.

## Reverse proxy and tunnel requirements

- `/drop/:drop_token` and `/api/drops/:drop_token` metadata/upload routes must be reachable by sender browsers.
- Receiver APIs under `/api/drop-points` must be reachable by receiver clients.
- Sender browsers must see HTTPS or localhost. HTTP over a LAN IP is not a secure browser context.
- Request body limits and idle/upload timeouts must allow `max_bytes` plus multipart overhead for slow mobile senders.
- TLS may terminate outside DropPoint.
- `/health` is unauthenticated and low-information.
- Public deployments must enforce rate limits and connection caps at the ingress or TLS terminator. Apply them to invalid `Authorization` attempts, drop-token metadata/upload routes, and unauthenticated page/asset/health routes; leaked drop links can otherwise be used to force repeated large failed uploads during their TTL.
- Public TLS terminators should emit `Strict-Transport-Security: max-age=31536000; includeSubDomains` for the DropPoint origin after HTTPS is confirmed working. Consider HSTS preload only when every subdomain is permanently HTTPS-ready.

## Logging guidance

Application logs redact DropPoint token-bearing paths. Configure every proxy, CDN, and tunnel to redact or avoid logging:

- `/drop/:drop_token`
- `/api/drops/:drop_token`
- `Authorization` headers
- query strings and fragments

Do not log envelope contents, public key fragments, filenames, MIME types, manifest JSON, private keys, derived keys, or payload bytes.

## Browser compatibility

The sender page requires secure context WebCrypto and native X25519 support. Unsupported browsers show an explicit unsupported-browser error instead of uploading plaintext.
