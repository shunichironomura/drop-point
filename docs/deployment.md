# Deployment guide

The `droppoint` binary serves plain HTTP only. Public/browser deployments must put it behind an external TLS terminator such as Cloudflare Tunnel, Caddy, another reverse proxy, or a container ingress. Localhost HTTP remains suitable for local secure-context development.

## Build

```sh
go build -o droppoint ./cmd/droppoint
```

## Container image

Build the included image:

```sh
docker build -t droppoint:local .
```

Run it with persistent storage and environment configuration:

```sh
docker run --rm \
  -p 8080:8080 \
  -v droppoint-data:/var/lib/droppoint \
  -e DROPPOINT_BASE_URL=https://drop.example.com \
  droppoint:local
```

Create receiver API tokens in the same volume with a one-off container:

```sh
docker run --rm \
  -v droppoint-data:/var/lib/droppoint \
  -e DROPPOINT_BASE_URL=https://drop.example.com \
  droppoint:local token add --id desktop-main
```

The image listens on `:8080`, stores data under `/var/lib/droppoint`, and runs as a non-root user.

For the included Compose file, copy the template first so deployment-specific values stay out of version control:

```sh
cp .env.example .env
docker compose up --build

# In another terminal, add a receiver token to the Compose volume.
docker compose run --rm droppoint token add --id desktop-main
```

## systemd example

```ini
[Unit]
Description=DropPoint relay
After=network-online.target
Wants=network-online.target

[Service]
User=droppoint
Group=droppoint
ExecStart=/usr/local/bin/droppoint serve --config /etc/droppoint/config.json
Restart=on-failure
StateDirectory=droppoint
ReadWritePaths=/var/lib/droppoint
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

Use `/var/lib/droppoint` as `data_dir`. Manage tokens without restarting the service:

```sh
sudo -u droppoint droppoint token add --id desktop-main --config /etc/droppoint/config.json
sudo -u droppoint droppoint token list --config /etc/droppoint/config.json
```

The unreleased relay accepts only its current versioned SQLite schema. It intentionally rejects unversioned legacy relay tables rather than creating active rows with invalid empty display names; recreate pre-release development data when the schema changes.

The running relay performs expiry cleanup on its configured interval. You may also run:

```sh
droppoint cleanup expired --config /etc/droppoint/config.json
```

from a timer or cron as an operational backstop.

## Reverse proxy and tunnel requirements

- `/drop/:drop_token` and `/api/drops/:drop_token` metadata/upload routes must be reachable by sender browsers.
- Receiver APIs under `/api/drop-points` must be reachable by receiver clients.
- Sender browsers must see HTTPS or localhost. HTTP over a LAN IP is not a secure browser context.
- Request body limits and idle/upload timeouts must allow `max_bytes` plus multipart overhead for slow mobile senders.
- TLS must terminate outside DropPoint for public/browser deployments; the binary has no certificate/key or built-in TLS configuration.
- `/health` is unauthenticated and low-information.
- Public deployments must enforce rate limits and connection caps at the ingress or TLS terminator. Apply them to invalid `Authorization` attempts, drop-token metadata/upload routes, and unauthenticated page/asset/health routes; leaked drop links can otherwise be used to force repeated large failed uploads during their TTL.
- Public TLS terminators should emit `Strict-Transport-Security: max-age=31536000; includeSubDomains` for the DropPoint origin after HTTPS is confirmed working. Consider HSTS preload only when every subdomain is permanently HTTPS-ready.

## Logging guidance

Application logs prefer matched route templates and redact capability-shaped values even when path delimiters are URL-encoded or malformed. Panic logs omit arbitrary recovered values, and the shared Python client HTTP boundary redacts capability URLs and response details on HTTP errors. Configure every proxy, CDN, and tunnel to redact or avoid logging:

- `/drop/:drop_token`
- `/api/drops/:drop_token`
- `Authorization` headers
- query strings and fragments

Do not log envelope contents, public key fragments, filenames, MIME types, manifest JSON, private keys, derived keys, or payload bytes.

## Browser compatibility

The sender page requires secure context WebCrypto and native X25519 support. Unsupported browsers show an explicit unsupported-browser error instead of uploading plaintext.
