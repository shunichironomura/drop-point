# DropPoint configuration reference

Example:

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

Fields:

- `listen_addr`: local address for the HTTP server.
- `base_url`: externally visible HTTP(S) root origin. It must contain a scheme and host and no user info, non-root path prefix, query, or fragment. External path-prefix hosting is not supported.
- `data_dir`: directory for `relay.db` and ciphertext directories. Use `/var/lib/droppoint` for system installs.
- `default_ttl_seconds` / `max_ttl_seconds`: receiver-requested TTL default and upper bound. Every configured duration is limited to 31536000 seconds (365 days) before conversion to `time.Duration`.
- `default_max_bytes` / `max_bytes`: encrypted payload size default and upper bound. The shipped maximum is `52428800` bytes; the implementation safety ceiling is 1099511627776 bytes (1 TiB) so request framing arithmetic remains bounded.
- `default_max_active_drop_points`: quota used when an API token has no per-token override in SQLite.
- `read_timeout_seconds` / `write_timeout_seconds`: HTTP body/response timeouts. Defaults are long enough for slow mobile uploads up to the shipped payload limit.
- `cleanup_interval_seconds`: how often the running relay expires old drop points, deletes expired ciphertext directories, and purges old terminal metadata rows.
- `terminal_retention_seconds`: retention window for closed/expired/failed SQLite rows after ciphertext pointers have been cleared. The default is 30 days.

## API token management

Receiver API token hashes are stored in the SQLite `api_tokens` table. Manage them with:

```sh
droppoint token add --id desktop-main --config /etc/droppoint/config.json
# prints the plaintext API token once

droppoint token list --config /etc/droppoint/config.json
droppoint token disable --id desktop-main --config /etc/droppoint/config.json
droppoint token remove --id desktop-main --config /etc/droppoint/config.json
```

Use `--max-active n` with `token add` to override `default_max_active_drop_points` for one token.

DropPoint stores only token hashes; the plaintext token is shown only when `token add` creates it. CLI output failures return non-zero. If the one-time `token add` output cannot be written, the command removes the just-created row so an unusable token is not left behind.

## Environment overrides

`DROPPOINT_*` environment variables override the JSON file and defaults. They use the same validation rules as the JSON fields.

| Environment variable | Overrides |
| --- | --- |
| `DROPPOINT_LISTEN_ADDR` | `listen_addr` |
| `DROPPOINT_BASE_URL` | `base_url` |
| `DROPPOINT_DATA_DIR` | `data_dir` |
| `DROPPOINT_DEFAULT_TTL_SECONDS` | `default_ttl_seconds` |
| `DROPPOINT_MAX_TTL_SECONDS` | `max_ttl_seconds` |
| `DROPPOINT_DEFAULT_MAX_BYTES` | `default_max_bytes` |
| `DROPPOINT_MAX_BYTES` | `max_bytes` |
| `DROPPOINT_DEFAULT_MAX_ACTIVE_DROP_POINTS` | `default_max_active_drop_points` |
| `DROPPOINT_READ_TIMEOUT_SECONDS` | `read_timeout_seconds` |
| `DROPPOINT_WRITE_TIMEOUT_SECONDS` | `write_timeout_seconds` |
| `DROPPOINT_CLEANUP_INTERVAL_SECONDS` | `cleanup_interval_seconds` |
| `DROPPOINT_TERMINAL_RETENTION_SECONDS` | `terminal_retention_seconds` |

Example:

```sh
DROPPOINT_BASE_URL=https://drop.example.com \
DROPPOINT_DATA_DIR=/var/lib/droppoint \
droppoint serve --config /etc/droppoint/config.json
```
