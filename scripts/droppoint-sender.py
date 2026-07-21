#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "cryptography>=49.0.0",
# ]
# ///
from __future__ import annotations

import argparse
import json
import re
import sys
import uuid
from pathlib import Path
from urllib import error, parse, request

from drop_point_protocol import b64u_decode, encrypt_files

# Cloudflare Browser Integrity Check rejects Python urllib's default user
# agent before API requests reach DropPoint, so use a stable tool-specific
# value instead of the stdlib default.
USER_AGENT = "DropPointSender/1.0"
_CAPABILITY_RE = re.compile(r"(?:drop|pick|api)_[A-Za-z0-9_-]+")


def main() -> int:
    parser = argparse.ArgumentParser(description="Simulate a DropPoint sender: encrypt files locally and submit one drop.")
    parser.add_argument("drop_link", help="Full drop link including #v=2&pk=... fragment")
    parser.add_argument("files", nargs="+", type=Path, help="Files to encrypt and drop")
    args = parser.parse_args()

    try:
        drop_url, recipient_public_key = parse_drop_link(args.drop_link)
        file_paths = validate_files(args.files)
        envelope_json, encrypted_payload = encrypt_files(file_paths, recipient_public_key)
        body, content_type = multipart_body(envelope_json, encrypted_payload)
        response = http_request("PUT", drop_url, body=body, headers={"Content-Type": content_type})
        print(response.decode("utf-8"))
        print(f"Dropped {len(file_paths)} file(s), encrypted payload bytes={len(encrypted_payload)}")
        return 0
    except Exception as exc:  # noqa: BLE001 - CLI should report any failure clearly.
        print(f"sender error: {exc}", file=sys.stderr)
        return 1


def parse_drop_link(drop_link: str) -> tuple[str, bytes]:
    parsed = parse.urlparse(drop_link)
    if not parsed.scheme or not parsed.netloc:
        raise ValueError("drop link must include scheme and host")
    if not parsed.fragment:
        raise ValueError("drop link is missing #v=2&pk=... fragment")
    fragment = parse.parse_qs(parsed.fragment, keep_blank_values=False)
    if fragment.get("v", [None])[0] != "2":
        raise ValueError("drop link fragment must contain v=2")
    pk_values = fragment.get("pk")
    if not pk_values:
        raise ValueError("drop link fragment must contain pk")
    recipient_public_key = b64u_decode(pk_values[0])
    if len(recipient_public_key) != 32:
        raise ValueError("drop link public key must decode to 32 bytes")

    parts = [part for part in parsed.path.split("/") if part]
    if len(parts) < 2 or parts[-2] != "drop":
        raise ValueError("drop link path must end with /drop/<drop-token>")
    drop_token = parts[-1]
    drop_path = "/api/drops/" + parse.quote(drop_token, safe="")
    drop_url = parse.urlunparse((parsed.scheme, parsed.netloc, drop_path, "", "", ""))
    return drop_url, recipient_public_key


def validate_files(paths: list[Path]) -> list[Path]:
    out = []
    for path in paths:
        if not path.is_file():
            raise ValueError(f"not a file: {path}")
        out.append(path)
    return out


def multipart_body(envelope_json: bytes, encrypted_payload: bytes) -> tuple[bytes, str]:
    boundary = "droppoint-" + uuid.uuid4().hex
    chunks = [
        f"--{boundary}\r\n".encode(),
        b'Content-Disposition: form-data; name="envelope"\r\n',
        b"Content-Type: application/json\r\n\r\n",
        envelope_json,
        b"\r\n",
        f"--{boundary}\r\n".encode(),
        b'Content-Disposition: form-data; name="payload"\r\n',
        b"Content-Type: application/octet-stream\r\n\r\n",
        encrypted_payload,
        b"\r\n",
        f"--{boundary}--\r\n".encode(),
    ]
    return b"".join(chunks), f"multipart/form-data; boundary={boundary}"


def http_request(method: str, url: str, body: bytes | None = None, headers: dict[str, str] | None = None) -> bytes:
    all_headers = {"User-Agent": USER_AGENT}
    all_headers.update(headers or {})
    req = request.Request(url, data=body, headers=all_headers, method=method)
    try:
        with request.urlopen(req, timeout=30) as response:  # noqa: S310 - local/dev CLI target supplied by user.
            return response.read()
    except error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(detail)
            detail = json.dumps(parsed, indent=2)
        except json.JSONDecodeError:
            pass
        safe_url = redact_capability_url(url)
        safe_detail = _CAPABILITY_RE.sub(":capability", detail)
        raise RuntimeError(f"HTTP {exc.code} from {safe_url}: {safe_detail}") from exc


def redact_capability_url(url: str) -> str:
    parsed = parse.urlparse(url)
    parts = parsed.path.split("/")
    if len(parts) >= 4 and parts[-3:-1] == ["api", "drops"]:
        parts[-1] = ":drop_token"
    redacted_path = _CAPABILITY_RE.sub(":capability", "/".join(parts))
    redacted_query = _CAPABILITY_RE.sub(":capability", parsed.query)
    return parse.urlunparse((parsed.scheme, parsed.netloc, redacted_path, "", redacted_query, ""))


if __name__ == "__main__":
    raise SystemExit(main())
