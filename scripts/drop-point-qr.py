#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "cryptography>=49.0.0",
#     "qrcode[pil]>=8.2",
# ]
# ///
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from urllib import error, parse, request

import qrcode

from drop_point_protocol import b64u_encode, generate_x25519_keypair

DEFAULT_TOKEN_ENV = "DROP_POINT_API_TOKEN"
DEFAULT_STATE_PATH = Path(".local/drop-point-mobile-test-state.json")
# Cloudflare Browser Integrity Check rejects Python urllib's default user
# agent before API requests reach DropPoint, so use a stable tool-specific
# value instead of the stdlib default.
USER_AGENT = "DropPointQR/1.0"


BLACK_BG = "\033[40m  "
WHITE_BG = "\033[47m  "
RESET = "\033[0m"


def main() -> int:
    parser = argparse.ArgumentParser(
        description=(
            "Create a public DropPoint sender link, save receiver state, "
            "and display the sender link as a QR code."
        )
    )
    parser.add_argument(
        "--base-url",
        default=os.environ.get("DROP_POINT_BASE_URL"),
        help="DropPoint public base URL (or set DROP_POINT_BASE_URL)",
    )
    parser.add_argument(
        "--api-token-env",
        default=DEFAULT_TOKEN_ENV,
        help=f"Environment variable containing the plaintext api_... bearer token (default: {DEFAULT_TOKEN_ENV})",
    )
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE_PATH, help="Private receiver state file to write")
    parser.add_argument("--client-name", default="python-mobile-qr", help="client_name sent to the receiver API")
    parser.add_argument("--ttl-seconds", type=int, default=600, help="Drop point TTL in seconds")
    parser.add_argument("--max-bytes", type=int, default=52_428_800, help="Maximum encrypted upload size")
    parser.add_argument(
        "--single-use",
        action=argparse.BooleanOptionalAction,
        default=True,
        help="Request a single-use drop point",
    )
    parser.add_argument("--png", type=Path, help="Also write the QR code as a PNG image")
    parser.add_argument("--no-terminal-qr", action="store_true", help="Do not print the QR code to the terminal")
    args = parser.parse_args()

    try:
        base_url = read_base_url(args.base_url)
        api_token = read_api_token(args.api_token_env)
        private_key_raw, public_key_raw = generate_x25519_keypair()
        created = create_drop_point(args, base_url, api_token)
        drop_link_with_fragment = append_sender_fragment(created["drop_link"], public_key_raw, created["expires_at"])
        state = receiver_state(base_url, created, private_key_raw, public_key_raw, drop_link_with_fragment)
        write_private_state(args.state, state)

        print(f"State written to: {args.state}")
        print(f"Drop name: {created['display_name']}")
        print("Share this sender link:")
        print(drop_link_with_fragment)
        print()

        if args.png:
            write_png(args.png, drop_link_with_fragment)
            print(f"QR PNG written to: {args.png}")
            print()

        if not args.no_terminal_qr:
            print("Scan this QR code with the mobile phone:")
            print_terminal_qr(drop_link_with_fragment)

        print("After uploading from the phone, receive with:")
        print(f"  ./scripts/drop-point-receiver.py pickup --state {args.state} --wait")
        print("The receiver helper removes recipient_private_key from the state file after pickup and close.")
        return 0
    except Exception as exc:  # noqa: BLE001 - CLI should report any failure clearly.
        print(f"qr setup error: {exc}", file=sys.stderr)
        return 1


def read_base_url(value: str | None) -> str:
    base_url = (value or "").strip().rstrip("/")
    if not base_url:
        raise ValueError("set DROP_POINT_BASE_URL or pass --base-url explicitly")
    return base_url


def read_api_token(env_name: str) -> str:
    token = os.environ.get(env_name, "").strip()
    if not token:
        raise ValueError(f"set {env_name} to the plaintext DropPoint API token")
    return token


def create_drop_point(args: argparse.Namespace, base_url: str, api_token: str) -> dict:
    request_body = json.dumps(
        {
            "client_name": args.client_name,
            "ttl_seconds": args.ttl_seconds,
            "max_bytes": args.max_bytes,
            "single_use": args.single_use,
        }
    ).encode("utf-8")
    return json_request(
        "POST",
        f"{base_url}/api/drop-points",
        token=api_token,
        body=request_body,
        headers={"Content-Type": "application/json"},
    )


def append_sender_fragment(drop_link: str, public_key_raw: bytes, expires_at: str) -> str:
    fragment = parse.urlencode({"v": "2", "pk": b64u_encode(public_key_raw), "exp": expires_at})
    return drop_link + "#" + fragment


def receiver_state(
    base_url: str,
    created: dict,
    private_key_raw: bytes,
    public_key_raw: bytes,
    drop_link_with_fragment: str,
) -> dict:
    return {
        "base_url": base_url.rstrip("/"),
        "drop_point_id": created["drop_point_id"],
        "display_name": created["display_name"],
        "pickup_token": created["pickup_token"],
        "recipient_private_key": b64u_encode(private_key_raw),
        "recipient_public_key": b64u_encode(public_key_raw),
        "drop_link": created["drop_link"],
        "drop_link_with_fragment": drop_link_with_fragment,
        "expires_at": created["expires_at"],
        "max_bytes": created["max_bytes"],
    }


def write_private_state(path: Path, state: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC
    fd = os.open(path, flags, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as file:
        json.dump(state, file, indent=2)
        file.write("\n")


def make_qr(data: str) -> qrcode.QRCode:
    qr = qrcode.QRCode(error_correction=qrcode.constants.ERROR_CORRECT_M, border=4)
    qr.add_data(data)
    qr.make(fit=True)
    return qr


def print_terminal_qr(data: str) -> None:
    matrix = make_qr(data).get_matrix()
    for row in matrix:
        print("".join(BLACK_BG if module else WHITE_BG for module in row) + RESET)
    print()


def write_png(path: Path, data: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    image = make_qr(data).make_image(fill_color="black", back_color="white")
    image.save(path)


def json_request(
    method: str,
    url: str,
    token: str,
    body: bytes | None = None,
    headers: dict[str, str] | None = None,
) -> dict:
    all_headers = {"User-Agent": USER_AGENT, "Accept": "application/json"}
    all_headers.update(headers or {})
    all_headers["Authorization"] = "Bearer " + token
    req = request.Request(url, data=body, headers=all_headers, method=method)
    try:
        with request.urlopen(req, timeout=30) as response:  # noqa: S310 - deployment URL is supplied by the operator.
            return json.loads(response.read().decode("utf-8"))
    except error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(detail)
            detail = json.dumps(parsed, indent=2)
        except json.JSONDecodeError:
            pass
        raise RuntimeError(f"HTTP {exc.code} from {url}: {detail}") from exc


if __name__ == "__main__":
    raise SystemExit(main())
