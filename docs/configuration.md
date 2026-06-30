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
- `api_tokens[].id`: operator label for quota and logs.
- `api_tokens[].secret_hash`: `sha256:<lowercase-hex-sha256>` of the plaintext API token. Plaintext tokens must not be stored.
- `api_tokens[].enabled`: disabled tokens cannot create drop points.
- `api_tokens[].max_active_drop_points`: optional per-token active drop point quota.

Generate token material with:

```sh
drop-point token generate
```

The command prints the plaintext API token once and the matching config hash. Store the hash; deliver the plaintext token only to the receiver client.
