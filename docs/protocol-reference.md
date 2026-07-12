# DropPoint protocol reference and test vectors

DropPoint implements only protocol version `2`: X25519, HKDF-SHA256, and AES-256-GCM with raw 32-byte X25519 keys.

Implementation pointers:

- Go: use `crypto/ecdh.X25519`, `crypto/hkdf`, and `crypto/cipher.NewGCM`; use `PrivateKey.Bytes()` and `PublicKey.Bytes()` for raw 32-byte keys.
- Node.js / TypeScript: use `crypto.subtle` or `node:crypto` X25519 where raw keys are available; HKDF salt is `sender_ephemeral_public_key || recipient_public_key`; AES-GCM ciphertext fields are `ciphertext || tag`.
- Python: use `cryptography.hazmat.primitives.asymmetric.x25519`, HKDF-SHA256, and AESGCM; serialize public keys as `Encoding.Raw` and `PublicFormat.Raw`.
- Swift: use CryptoKit Curve25519 key agreement, HKDF-SHA256, and AES.GCM; convert sealed boxes to combined `ciphertext || tag` when building JSON fields.
- Rust: use `x25519-dalek`, `hkdf`, and an AES-GCM crate; reject all-zero shared secrets and avoid accepting DER/PEM key encodings.
- Browser WebCrypto: require secure context and feature-detect `X25519`; import/export raw keys and use HKDF with exact `info` byte strings.

Exact protocol byte strings:

- `INFO_METADATA = "DropPoint/protocol/v2 key=metadata"`
- `INFO_PAYLOAD = "DropPoint/protocol/v2 key=payload"`
- `AAD_METADATA = 0x02 || "metadata"`
- `AAD_PAYLOAD = 0x02 || "payload"`

Receivers must reject malformed envelopes, noncanonical base64url, duplicate/unknown or wrongly typed JSON fields, invalid RFC3339 manifest timestamps, AES-GCM authentication failures, all-zero/low-order X25519 inputs, manifest size-sum mismatch, hostile or noncanonical filenames, unsafe MIME types, and normalization/lowercase comparison-key collisions. Bundles contain at most 1000 manifest entries; canonical names are NFC and at most 240 UTF-8 bytes, and MIME values are at most 255 UTF-8 bytes. `testdata/filename-policy.json` and `testdata/protocol-parsing-policy.json` are the normative low-maintenance cross-language parsing fixtures.

## Cross-language coverage status

The existing low-maintenance Go CI validates the protocol implementation, deterministic vectors, negative cases, manifest bounds, and both normative `testdata/` parsing-policy fixtures. The browser and Python implementations consume the same documented policy and can be checked locally with `node --check web/drop-page/app.js` and `ruff check scripts`.

Executable browser/Python vector suites, browser E2E automation, new JavaScript package metadata/lockfiles, browser downloads, and a dedicated Python test/type environment are intentionally deferred while DropPoint is unreleased and has no supported client ecosystem. This is an accepted coverage risk, not a claim that substring/source checks provide behavioral coverage. Add those suites when supported clients or regression history justify their maintenance cost.

## Positive deterministic vectors

```json
[
  {
    "name": "single-file",
    "recipient_private_key": "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA",
    "recipient_public_key": "B6N8vBQgk8i3VdwbEOhstCY3StFqqFPtC9_AsrhtHHw",
    "sender_ephemeral_private_key": "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVpbXF1eX2A",
    "sender_ephemeral_public_key": "ZLEBsdC-WocEvQePmJUAH8A-jp-VIvGI3RKNmEbUhGY",
    "metadata_nonce": "gYKDhIWGh4iJiouM",
    "payload_nonce": "oaKjpKWmp6ipqqus",
    "shared_secret": "JsLBf9uCFhyyGtFuchMVNVtk0XYxGbEL_JYlMNx8wWM",
    "metadata_key": "s40FQaIwem52dtMHtlJRS37fGsEW9GE6izhF_HtPdlY",
    "payload_key": "-fkaloOyTMleEvd80ejsplPXwz-pmN7L54aChKlIDMg",
    "manifest_json": "{\"protocol_version\":2,\"files\":[{\"name\":\"scan-01.txt\",\"type\":\"text/plain\",\"size\":17}],\"created_at\":\"2026-06-30T12:00:00Z\"}",
    "envelope_json": "{\"protocol_version\":2,\"key_agreement\":\"x25519-hkdf-sha256-aesgcm-raw32\",\"sender_ephemeral_public_key\":\"ZLEBsdC-WocEvQePmJUAH8A-jp-VIvGI3RKNmEbUhGY\",\"metadata_nonce\":\"gYKDhIWGh4iJiouM\",\"payload_nonce\":\"oaKjpKWmp6ipqqus\",\"encrypted_metadata\":\"RXCd3ShA60Tza36-2nebwQVpV_NcAFlqtswR1p3V2_CXK9RVNjBXH2SER4pzbkLgtZj8Il4yGrid_PJ1BQatt8XhCygqbzWI5SCXUm-dZwSHv_bZSg6mhLJX6EDE8Uuhr0CYIabnfbDEU1swi_mQ6FshM7aLdi-XQzleiuSNyKclXXGJ-5WbPQI\"}",
    "encrypted_metadata": "RXCd3ShA60Tza36-2nebwQVpV_NcAFlqtswR1p3V2_CXK9RVNjBXH2SER4pzbkLgtZj8Il4yGrid_PJ1BQatt8XhCygqbzWI5SCXUm-dZwSHv_bZSg6mhLJX6EDE8Uuhr0CYIabnfbDEU1swi_mQ6FshM7aLdi-XQzleiuSNyKclXXGJ-5WbPQI",
    "encrypted_payload": "95kEDw2nrrpQAuknRO8NY2vBLOEvOd2Qjbzwu0aRORaf",
    "payload_plaintext": "aGVsbG8gZHJvcCBwb2ludAo"
  },
  {
    "name": "multi-file",
    "recipient_private_key": "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA",
    "recipient_public_key": "B6N8vBQgk8i3VdwbEOhstCY3StFqqFPtC9_AsrhtHHw",
    "sender_ephemeral_private_key": "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVpbXF1eX2A",
    "sender_ephemeral_public_key": "ZLEBsdC-WocEvQePmJUAH8A-jp-VIvGI3RKNmEbUhGY",
    "metadata_nonce": "gYKDhIWGh4iJiouM",
    "payload_nonce": "oaKjpKWmp6ipqqus",
    "shared_secret": "JsLBf9uCFhyyGtFuchMVNVtk0XYxGbEL_JYlMNx8wWM",
    "metadata_key": "s40FQaIwem52dtMHtlJRS37fGsEW9GE6izhF_HtPdlY",
    "payload_key": "-fkaloOyTMleEvd80ejsplPXwz-pmN7L54aChKlIDMg",
    "manifest_json": "{\"protocol_version\":2,\"files\":[{\"name\":\"scan-01.txt\",\"type\":\"text/plain\",\"size\":11},{\"name\":\"scan-02.bin\",\"type\":\"application/octet-stream\",\"size\":6}],\"created_at\":\"2026-06-30T12:00:00Z\"}",
    "envelope_json": "{\"protocol_version\":2,\"key_agreement\":\"x25519-hkdf-sha256-aesgcm-raw32\",\"sender_ephemeral_public_key\":\"ZLEBsdC-WocEvQePmJUAH8A-jp-VIvGI3RKNmEbUhGY\",\"metadata_nonce\":\"gYKDhIWGh4iJiouM\",\"payload_nonce\":\"oaKjpKWmp6ipqqus\",\"encrypted_metadata\":\"RXCd3ShA60Tza36-2nebwQVpV_NcAFlqtswR1p3V2_CXK9RVNjBXH2SER4pzbkLgtZj8Il4yGrid_PJ1BQatt8XhCygqbzWI5SCXUm-dZwSHufaoHQ6rl7pTvh-C3Um041eKIbi3IvPWSVR3wt3E-FszYvzLKhzWXzju4waBbCw4w5KQ2kSyUuMok_Q0WYrUvLSv3oi0BWOb-FUUmM6TzYaCR7s8CeQtq0ntqYg_Bv97Iaw0ytpAW2Uf9UFwpZwMqJ-0i9h37a3-TdU\"}",
    "encrypted_metadata": "RXCd3ShA60Tza36-2nebwQVpV_NcAFlqtswR1p3V2_CXK9RVNjBXH2SER4pzbkLgtZj8Il4yGrid_PJ1BQatt8XhCygqbzWI5SCXUm-dZwSHufaoHQ6rl7pTvh-C3Um041eKIbi3IvPWSVR3wt3E-FszYvzLKhzWXzju4waBbCw4w5KQ2kSyUuMok_Q0WYrUvLSv3oi0BWOb-FUUmM6TzYaCR7s8CeQtq0ntqYg_Bv97Iaw0ytpAW2Uf9UFwpZwMqJ-0i9h37a3-TdU",
    "encrypted_payload": "-ZUaEBanrKFTF8NXKoRgE2SbeAwJTwpaM2YK_eooj2sl",
    "payload_plaintext": "Zmlyc3QgZmlsZQoAAQIDBAU"
  }
]
```

## Negative vectors covered by tests

The `internal/cryptoenv` test suite builds a positive bundle and verifies rejection for:

- tampered payload ciphertext/tag;
- tampered metadata ciphertext/tag;
- tampered payload nonce;
- tampered sender ephemeral public key;
- wrong recipient key;
- protocol version change;
- all-zero/low-order X25519 public input;
- manifest size-sum mismatch;
- hostile/noncanonical filenames and unsafe MIME types;
- normalization or lowercase comparison-key collisions in a bundle;
- manifest entry and filename/MIME length violations.
