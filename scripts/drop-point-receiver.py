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
import os
import sys
import time
from email import policy
from email.parser import BytesParser
from pathlib import Path
from urllib import error, parse, request

from drop_point_protocol import b64u_encode, decrypt_bundle, generate_x25519_keypair

# Cloudflare Browser Integrity Check rejects Python urllib's default user
# agent before API requests reach DropPoint, so use a stable tool-specific
# value instead of the stdlib default.
USER_AGENT = "DropPointReceiver/1.0"


def main() -> int:
    parser = argparse.ArgumentParser(description="Simulate a DropPoint receiver client.")
    subcommands = parser.add_subparsers(dest="command", required=True)

    create = subcommands.add_parser("create", help="Create a drop point and save receiver state")
    create.add_argument("--base-url", default="http://127.0.0.1:8080", help="DropPoint base URL")
    create.add_argument("--api-token", required=True, help="Plaintext api_... bearer token")
    create.add_argument("--state", type=Path, default=Path(".local/drop-point-receiver-state.json"), help="State file to write")
    create.add_argument("--client-name", default="python-local")
    create.add_argument("--ttl-seconds", type=int, default=600)
    create.add_argument("--max-bytes", type=int, default=52_428_800)

    status = subcommands.add_parser("status", help="Print receiver-visible drop point status")
    status.add_argument("--state", type=Path, default=Path(".local/drop-point-receiver-state.json"))

    pickup = subcommands.add_parser("pickup", help="Pick up, decrypt, and write files")
    pickup.add_argument("--state", type=Path, default=Path(".local/drop-point-receiver-state.json"))
    pickup.add_argument("--out-dir", type=Path, default=Path(".local/drop-point-pickup"))
    pickup.add_argument("--wait", action="store_true", help="Poll until ready before pickup")
    pickup.add_argument("--timeout", type=float, default=120.0)
    pickup.add_argument("--interval", type=float, default=1.0)
    pickup.add_argument("--close", action=argparse.BooleanOptionalAction, default=True, help="Close after successful local write")
    pickup.add_argument("--overwrite", action="store_true", help="Overwrite output files if they already exist")

    args = parser.parse_args()
    try:
        match args.command:
            case "create":
                create_drop_point(args)
            case "status":
                print(json.dumps(get_status(load_state(args.state)), indent=2))
            case "pickup":
                pickup_drop(args)
        return 0
    except Exception as exc:  # noqa: BLE001 - CLI should report any failure clearly.
        print(f"receiver error: {exc}", file=sys.stderr)
        return 1


def create_drop_point(args: argparse.Namespace) -> None:
    private_key_raw, public_key_raw = generate_x25519_keypair()
    base_url = args.base_url.rstrip("/")
    request_body = json.dumps(
        {
            "client_name": args.client_name,
            "ttl_seconds": args.ttl_seconds,
            "max_bytes": args.max_bytes,
            "single_use": True,
        }
    ).encode("utf-8")
    created = json_request(
        "POST",
        f"{base_url}/api/drop-points",
        token=args.api_token,
        body=request_body,
        headers={"Content-Type": "application/json"},
    )
    fragment = parse.urlencode({"v": "2", "pk": b64u_encode(public_key_raw), "exp": created["expires_at"]})
    drop_link_with_fragment = created["drop_link"] + "#" + fragment
    state = {
        "base_url": base_url,
        "drop_point_id": created["drop_point_id"],
        "pickup_token": created["pickup_token"],
        "recipient_private_key": b64u_encode(private_key_raw),
        "recipient_public_key": b64u_encode(public_key_raw),
        "drop_link": created["drop_link"],
        "drop_link_with_fragment": drop_link_with_fragment,
        "expires_at": created["expires_at"],
        "max_bytes": created["max_bytes"],
    }
    write_private_state(args.state, state)
    print(f"State written to: {args.state}")
    print("Share this sender link:")
    print(drop_link_with_fragment)


def pickup_drop(args: argparse.Namespace) -> None:
    state = load_state(args.state)
    if args.wait:
        wait_until_ready(state, args.timeout, args.interval)
    envelope_json, encrypted_payload = pickup_ciphertext(state)
    files = decrypt_bundle(b64u_decode_from_state(state, "recipient_private_key"), envelope_json, encrypted_payload)
    args.out_dir.mkdir(parents=True, exist_ok=True)
    for recovered in files:
        target = args.out_dir / recovered.name
        mode = "wb" if args.overwrite else "xb"
        with target.open(mode) as file:
            file.write(recovered.data)
        print(f"wrote {target} ({len(recovered.data)} bytes, {recovered.mime_type})")
    if args.close:
        close_drop_point(state)
        print("closed remote drop point")


def wait_until_ready(state: dict, timeout: float, interval: float) -> None:
    deadline = time.monotonic() + timeout
    while True:
        status = get_status(state)
        print(f"status={status['status']}")
        if status["status"] == "ready":
            return
        if status["status"] in {"closed", "expired", "failed"}:
            raise RuntimeError(f"drop point is terminal: {status['status']}")
        if time.monotonic() >= deadline:
            raise TimeoutError("timed out waiting for drop point to become ready")
        time.sleep(interval)


def get_status(state: dict) -> dict:
    return json_request("GET", api_url(state, f"/api/drop-points/{state['drop_point_id']}/status"), token=state["pickup_token"])


def pickup_ciphertext(state: dict) -> tuple[bytes, bytes]:
    content_type, body = raw_request(
        "GET",
        api_url(state, f"/api/drop-points/{state['drop_point_id']}/pickup"),
        token=state["pickup_token"],
    )
    return parse_pickup_multipart(content_type, body)


def close_drop_point(state: dict) -> None:
    raw_request("DELETE", api_url(state, f"/api/drop-points/{state['drop_point_id']}"), token=state["pickup_token"])


def parse_pickup_multipart(content_type: str, body: bytes) -> tuple[bytes, bytes]:
    message = BytesParser(policy=policy.default).parsebytes(
        b"Content-Type: " + content_type.encode("utf-8") + b"\r\nMIME-Version: 1.0\r\n\r\n" + body
    )
    if not message.is_multipart():
        raise ValueError("pickup response is not multipart")
    parts: dict[str, bytes] = {}
    for part in message.iter_parts():
        name = part.get_param("name", header="content-disposition")
        if name:
            payload = part.get_payload(decode=True)
            parts[name] = payload if payload is not None else b""
    if set(parts) != {"envelope", "payload"}:
        raise ValueError(f"pickup response parts = {sorted(parts)}, want envelope and payload")
    return parts["envelope"], parts["payload"]


def json_request(method: str, url: str, token: str | None = None, body: bytes | None = None, headers: dict[str, str] | None = None) -> dict:
    _content_type, response_body = raw_request(method, url, token=token, body=body, headers=headers)
    return json.loads(response_body.decode("utf-8")) if response_body else {}


def raw_request(
    method: str,
    url: str,
    token: str | None = None,
    body: bytes | None = None,
    headers: dict[str, str] | None = None,
) -> tuple[str, bytes]:
    all_headers = {"User-Agent": USER_AGENT, "Accept": "application/json"}
    all_headers.update(headers or {})
    if token:
        all_headers["Authorization"] = "Bearer " + token
    req = request.Request(url, data=body, headers=all_headers, method=method)
    try:
        with request.urlopen(req, timeout=30) as response:  # noqa: S310 - local/dev CLI target supplied by user.
            return response.headers.get("Content-Type", ""), response.read()
    except error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {exc.code} from {url}: {detail}") from exc


def api_url(state: dict, path: str) -> str:
    parsed = parse.urlparse(state["base_url"])
    return parse.urlunparse((parsed.scheme, parsed.netloc, path, "", "", ""))


def write_private_state(path: Path, state: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC
    fd = os.open(path, flags, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as file:
        json.dump(state, file, indent=2)
        file.write("\n")


def load_state(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def b64u_decode_from_state(state: dict, key: str) -> bytes:
    from drop_point_protocol import b64u_decode

    value = state.get(key)
    if not isinstance(value, str):
        raise ValueError(f"state is missing {key}")
    return b64u_decode(value)


if __name__ == "__main__":
    raise SystemExit(main())
