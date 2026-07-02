# DropPoint configuration reference

Example:

```json
{
  "listen_addr": "127.0.0.1:8080",
  "base_url": "https://drop.example.com",
  "data_dir": "/var/lib/drop-point",
  "default_ttl_seconds": 600,
  "max_ttl_seconds": 900,
  "default_max_bytes": 52428800,
  "max_bytes": 52428800,
  "default_max_active_drop_points": 3,
  "read_timeout_seconds": 600,
  "write_timeout_seconds": 600,
  "cleanup_interval_seconds": 60,
  "terminal_retention_seconds": 2592000,
  "api_tokens": [
    {
      "id": "desktop-main",
      "secret_hash": "sha256:<lowercase-hex-sha256>",
      "enabled": true,
      "max_active_drop_points": 3
    }
  ]
}
```

Fields:

- `listen_addr`: local address for the HTTP server.
- `base_url`: externally visible origin, with scheme and host, no query or fragment.
- `data_dir`: directory for `relay.db` and ciphertext directories. Use `/var/lib/drop-point` for system installs.
- `default_ttl_seconds` / `max_ttl_seconds`: receiver-requested TTL default and upper bound.
- `default_max_bytes` / `max_bytes`: encrypted payload size default and upper bound. The shipped maximum is `52428800` bytes.
- `default_max_active_drop_points`: quota used when an API token has no override.
- `read_timeout_seconds` / `write_timeout_seconds`: HTTP body/response timeouts. Defaults are long enough for slow mobile uploads up to the shipped payload limit.
- `cleanup_interval_seconds`: how often the running relay expires old drop points, deletes expired ciphertext directories, and purges old terminal metadata rows.
- `terminal_retention_seconds`: retention window for closed/expired/failed SQLite rows after ciphertext pointers have been cleared. The default is 30 days.
- `api_tokens[].id`: operator label for quota and logs.
- `api_tokens[].secret_hash`: `sha256:<lowercase-hex-sha256>` of the plaintext API token. Plaintext tokens must not be stored.
- `api_tokens[].enabled`: disabled tokens cannot create drop points.
- `api_tokens[].max_active_drop_points`: optional per-token active drop point quota.

## Environment overrides

`DROP_POINT_*` environment variables override the JSON file and defaults. They use the same validation rules as the JSON fields.

| Environment variable | Overrides |
| --- | --- |
| `DROP_POINT_LISTEN_ADDR` | `listen_addr` |
| `DROP_POINT_BASE_URL` | `base_url` |
| `DROP_POINT_DATA_DIR` | `data_dir` |
| `DROP_POINT_DEFAULT_TTL_SECONDS` | `default_ttl_seconds` |
| `DROP_POINT_MAX_TTL_SECONDS` | `max_ttl_seconds` |
| `DROP_POINT_DEFAULT_MAX_BYTES` | `default_max_bytes` |
| `DROP_POINT_MAX_BYTES` | `max_bytes` |
| `DROP_POINT_DEFAULT_MAX_ACTIVE_DROP_POINTS` | `default_max_active_drop_points` |
| `DROP_POINT_READ_TIMEOUT_SECONDS` | `read_timeout_seconds` |
| `DROP_POINT_WRITE_TIMEOUT_SECONDS` | `write_timeout_seconds` |
| `DROP_POINT_CLEANUP_INTERVAL_SECONDS` | `cleanup_interval_seconds` |
| `DROP_POINT_TERMINAL_RETENTION_SECONDS` | `terminal_retention_seconds` |
| `DROP_POINT_API_TOKENS_JSON` | the full `api_tokens` array as JSON |

Example:

```sh
DROP_POINT_BASE_URL=https://drop.example.com \
DROP_POINT_DATA_DIR=/var/lib/drop-point \
DROP_POINT_API_TOKENS_JSON='[{"id":"desktop-main","secret_hash":"sha256:<lowercase-hex-sha256>","enabled":true}]' \
drop-point serve --config /etc/drop-point/config.json
```

Generate token material with:

```sh
drop-point token generate
```

The command prints the plaintext API token once and the matching config hash. Store the hash; deliver the plaintext token only to the receiver client.
