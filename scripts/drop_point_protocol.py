from __future__ import annotations

import base64
import json
import mimetypes
import os
import re
import unicodedata
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

PROTOCOL_VERSION = 2
KEY_AGREEMENT = "x25519-hkdf-sha256-aesgcm-raw32"
INFO_METADATA = b"DropPoint/protocol/v2 key=metadata"
INFO_PAYLOAD = b"DropPoint/protocol/v2 key=payload"
AAD_METADATA = bytes([PROTOCOL_VERSION]) + b"metadata"
AAD_PAYLOAD = bytes([PROTOCOL_VERSION]) + b"payload"
MAX_MANIFEST_FILES = 1000
MAX_FILENAME_UTF8_BYTES = 240
MAX_FILENAME_EXTENSION_BYTES = 32
MAX_MIME_TYPE_UTF8_BYTES = 255
RESERVED_RECEIPT_NAME = ".droppoint-receipt.json"
WINDOWS_RESERVED_NAMES = {
    "CON",
    "PRN",
    "AUX",
    "NUL",
    *(f"COM{i}" for i in range(1, 10)),
    "COM¹",
    "COM²",
    "COM³",
    *(f"LPT{i}" for i in range(1, 10)),
    "LPT¹",
    "LPT²",
    "LPT³",
}


@dataclass(frozen=True)
class DecryptedFile:
    name: str
    mime_type: str
    data: bytes


def b64u_encode(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def b64u_decode(value: str) -> bytes:
    if not value or "=" in value:
        raise ValueError("base64url value must be non-empty and unpadded")
    padding = "=" * (-len(value) % 4)
    return base64.urlsafe_b64decode((value + padding).encode("ascii"))


def generate_x25519_keypair() -> tuple[bytes, bytes]:
    private_key = X25519PrivateKey.generate()
    return private_key_to_raw(private_key), public_key_to_raw(private_key.public_key())


def private_key_to_raw(private_key: X25519PrivateKey) -> bytes:
    return private_key.private_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PrivateFormat.Raw,
        encryption_algorithm=serialization.NoEncryption(),
    )


def public_key_to_raw(public_key: X25519PublicKey) -> bytes:
    return public_key.public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )


def public_from_private_raw(private_key_raw: bytes) -> bytes:
    private_key = X25519PrivateKey.from_private_bytes(private_key_raw)
    return public_key_to_raw(private_key.public_key())


def encrypt_files(file_paths: Iterable[Path], recipient_public_key_raw: bytes) -> tuple[bytes, bytes]:
    if len(recipient_public_key_raw) != 32:
        raise ValueError("recipient public key must be 32 raw bytes")

    paths = list(file_paths)
    if not paths or len(paths) > MAX_MANIFEST_FILES:
        raise ValueError(f"bundle must contain between 1 and {MAX_MANIFEST_FILES} files")
    canonical_names = canonicalize_filenames([path.name for path in paths])
    files = []
    payload_parts = []
    for path, canonical_name in zip(paths, canonical_names, strict=True):
        data = path.read_bytes()
        mime_type = sanitize_mime_type(mimetypes.guess_type(path.name)[0] or "application/octet-stream")
        files.append({"name": canonical_name, "type": mime_type, "size": len(data)})
        payload_parts.append(data)

    manifest = {
        "protocol_version": PROTOCOL_VERSION,
        "files": files,
        "created_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
    }
    manifest_json = json.dumps(manifest, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    payload_plaintext = b"".join(payload_parts)

    sender_private_key = X25519PrivateKey.generate()
    sender_public_key_raw = public_key_to_raw(sender_private_key.public_key())
    recipient_public_key = X25519PublicKey.from_public_bytes(recipient_public_key_raw)
    shared_secret = sender_private_key.exchange(recipient_public_key)
    _reject_all_zero(shared_secret)
    metadata_key, payload_key = _derive_keys(shared_secret, sender_public_key_raw, recipient_public_key_raw)

    metadata_nonce = os.urandom(12)
    payload_nonce = os.urandom(12)
    encrypted_metadata = AESGCM(metadata_key).encrypt(metadata_nonce, manifest_json, AAD_METADATA)
    encrypted_payload = AESGCM(payload_key).encrypt(payload_nonce, payload_plaintext, AAD_PAYLOAD)

    envelope = {
        "protocol_version": PROTOCOL_VERSION,
        "key_agreement": KEY_AGREEMENT,
        "sender_ephemeral_public_key": b64u_encode(sender_public_key_raw),
        "metadata_nonce": b64u_encode(metadata_nonce),
        "payload_nonce": b64u_encode(payload_nonce),
        "encrypted_metadata": b64u_encode(encrypted_metadata),
    }
    envelope_json = json.dumps(envelope, separators=(",", ":")).encode("utf-8")
    return envelope_json, encrypted_payload


def decrypt_bundle(recipient_private_key_raw: bytes, envelope_json: bytes, encrypted_payload: bytes) -> list[DecryptedFile]:
    recipient_private_key = X25519PrivateKey.from_private_bytes(recipient_private_key_raw)
    recipient_public_key_raw = public_key_to_raw(recipient_private_key.public_key())

    envelope = json.loads(envelope_json.decode("utf-8"))
    if envelope.get("protocol_version") != PROTOCOL_VERSION:
        raise ValueError("unsupported envelope protocol_version")
    if envelope.get("key_agreement") != KEY_AGREEMENT:
        raise ValueError("unsupported envelope key_agreement")

    sender_public_key_raw = _field_b64(envelope, "sender_ephemeral_public_key", 32)
    metadata_nonce = _field_b64(envelope, "metadata_nonce", 12)
    payload_nonce = _field_b64(envelope, "payload_nonce", 12)
    encrypted_metadata = _field_b64(envelope, "encrypted_metadata", None)
    if len(encrypted_metadata) < 16:
        raise ValueError("encrypted_metadata is too short")

    shared_secret = recipient_private_key.exchange(X25519PublicKey.from_public_bytes(sender_public_key_raw))
    _reject_all_zero(shared_secret)
    metadata_key, payload_key = _derive_keys(shared_secret, sender_public_key_raw, recipient_public_key_raw)

    manifest_json = AESGCM(metadata_key).decrypt(metadata_nonce, encrypted_metadata, AAD_METADATA)
    payload_plaintext = AESGCM(payload_key).decrypt(payload_nonce, encrypted_payload, AAD_PAYLOAD)
    manifest = json.loads(manifest_json.decode("utf-8"))
    return _split_payload(manifest, payload_plaintext)


def _field_b64(envelope: dict, field: str, length: int | None) -> bytes:
    value = envelope.get(field)
    if not isinstance(value, str):
        raise ValueError(f"missing envelope field {field}")
    decoded = b64u_decode(value)
    if length is not None and len(decoded) != length:
        raise ValueError(f"{field} decoded length = {len(decoded)}, want {length}")
    return decoded


def _derive_keys(shared_secret: bytes, sender_public_key_raw: bytes, recipient_public_key_raw: bytes) -> tuple[bytes, bytes]:
    salt = sender_public_key_raw + recipient_public_key_raw
    metadata_key = HKDF(algorithm=hashes.SHA256(), length=32, salt=salt, info=INFO_METADATA).derive(shared_secret)
    payload_key = HKDF(algorithm=hashes.SHA256(), length=32, salt=salt, info=INFO_PAYLOAD).derive(shared_secret)
    return metadata_key, payload_key


def _reject_all_zero(shared_secret: bytes) -> None:
    if shared_secret == b"\x00" * 32:
        raise ValueError("all-zero X25519 shared secret")


def _split_payload(manifest: dict, payload_plaintext: bytes) -> list[DecryptedFile]:
    if manifest.get("protocol_version") != PROTOCOL_VERSION:
        raise ValueError("unsupported manifest protocol_version")
    files = manifest.get("files")
    if not isinstance(files, list) or not files or len(files) > MAX_MANIFEST_FILES:
        raise ValueError(f"manifest must contain between 1 and {MAX_MANIFEST_FILES} files")

    remaining = len(payload_plaintext)
    seen_names: set[str] = set()
    out: list[DecryptedFile] = []
    for entry in files:
        if not isinstance(entry, dict):
            raise ValueError("manifest file entry must be an object")
        raw_name = entry.get("name")
        raw_type = entry.get("type")
        if not isinstance(raw_name, str) or not isinstance(raw_type, str):
            raise ValueError("manifest file name and type must be strings")
        name = sanitize_filename(raw_name)
        folded = filename_collision_key(name)
        if folded in seen_names:
            raise ValueError(f"duplicate filename in bundle: {name}")
        seen_names.add(folded)
        mime_type = sanitize_mime_type(raw_type)
        size = entry.get("size")
        if isinstance(size, bool) or not isinstance(size, int) or size < 0:
            raise ValueError(f"invalid size for {name}")
        if size > remaining:
            raise ValueError(f"size for {name} exceeds the remaining payload")
        remaining -= size
        out.append(DecryptedFile(name=name, mime_type=mime_type, data=b""))

    if remaining != 0:
        raise ValueError(f"manifest sizes leave {remaining} unclaimed payload bytes")

    offset = 0
    recovered: list[DecryptedFile] = []
    for template, entry in zip(out, files, strict=True):
        size = int(entry["size"])
        recovered.append(DecryptedFile(template.name, template.mime_type, payload_plaintext[offset : offset + size]))
        offset += size
    return recovered


def canonicalize_filenames(names: Iterable[str]) -> list[str]:
    values = list(names)
    if len(values) > MAX_MANIFEST_FILES:
        raise ValueError(f"bundle contains more than {MAX_MANIFEST_FILES} files")
    used: set[str] = set()
    out: list[str] = []
    for name in values:
        base = _canonicalize_filename(name)
        candidate = base
        suffix = 2
        while filename_collision_key(candidate) in used:
            candidate = _fit_filename(base, f" ({suffix})")
            suffix += 1
        used.add(filename_collision_key(candidate))
        out.append(candidate)
    return out


def sanitize_filename(name: str) -> str:
    try:
        encoded = name.encode("utf-8")
    except UnicodeEncodeError as exc:
        raise ValueError("filename must be valid UTF-8") from exc
    if not name:
        raise ValueError("filename must not be empty")
    if len(encoded) > MAX_FILENAME_UTF8_BYTES:
        raise ValueError(f"filename exceeds {MAX_FILENAME_UTF8_BYTES} UTF-8 bytes")
    if unicodedata.normalize("NFC", name) != name:
        raise ValueError("filename must use Unicode NFC")
    if name.rstrip(" .") != name:
        raise ValueError("filename must not end in a space or dot")
    if _filename_blank(name) or name in {".", ".."}:
        raise ValueError(f"reserved filename: {name!r}")
    if any(_forbidden_filename_character(ch) for ch in name):
        raise ValueError(f"unsafe filename: {name!r}")
    if _reserved_windows_name(name) or name.lower() == RESERVED_RECEIPT_NAME:
        raise ValueError(f"platform-reserved filename: {name!r}")
    return name


def _canonicalize_filename(name: str) -> str:
    if not isinstance(name, str):
        raise ValueError("filename must be a string")
    try:
        normalized = unicodedata.normalize("NFC", name)
        normalized.encode("utf-8")
    except UnicodeError as exc:
        raise ValueError("filename must be valid UTF-8") from exc
    candidate = "".join("_" if _forbidden_filename_character(ch) else ch for ch in normalized).rstrip(" .")
    if _filename_blank(candidate) or candidate in {".", ".."}:
        candidate = "file"
    if _reserved_windows_name(candidate) or candidate.lower() == RESERVED_RECEIPT_NAME:
        candidate = "_" + candidate
    candidate = _fit_filename(candidate, "")
    return sanitize_filename(candidate)


def _fit_filename(name: str, suffix: str) -> str:
    stem = name
    extension = ""
    dot = name.rfind(".")
    if dot > 0:
        possible_extension = name[dot:]
        if len(possible_extension.encode("utf-8")) <= MAX_FILENAME_EXTENSION_BYTES:
            stem = name[:dot]
            extension = possible_extension
    budget = MAX_FILENAME_UTF8_BYTES - len(suffix.encode("utf-8")) - len(extension.encode("utf-8"))
    if budget < 1:
        extension = ""
        budget = MAX_FILENAME_UTF8_BYTES - len(suffix.encode("utf-8"))
    stem = _truncate_utf8(stem, budget).rstrip(" .")
    if _filename_blank(stem) or stem in {".", ".."}:
        stem = "file"
    return stem + suffix + extension


def _truncate_utf8(value: str, max_bytes: int) -> str:
    out: list[str] = []
    length = 0
    for character in value:
        encoded_length = len(character.encode("utf-8"))
        if length + encoded_length > max_bytes:
            break
        out.append(character)
        length += encoded_length
    return "".join(out)


def _forbidden_filename_character(character: str) -> bool:
    return character in '/\\\x00<>:"|?*' or unicodedata.category(character) in {"Cc", "Cf"}


def _filename_blank(name: str) -> bool:
    return not name or all(character.isspace() for character in name)


def filename_collision_key(name: str) -> str:
    return unicodedata.normalize("NFC", name).lower()


def _reserved_windows_name(name: str) -> bool:
    return name.split(".", 1)[0].upper() in WINDOWS_RESERVED_NAMES


_MIME_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*$")


def sanitize_mime_type(value: str) -> str:
    if not value:
        return "application/octet-stream"
    try:
        encoded = value.encode("utf-8")
    except UnicodeEncodeError as exc:
        raise ValueError("MIME type must be valid UTF-8") from exc
    if len(encoded) > MAX_MIME_TYPE_UTF8_BYTES:
        raise ValueError(f"MIME type exceeds {MAX_MIME_TYPE_UTF8_BYTES} UTF-8 bytes")
    lowered = value.lower()
    if not _MIME_RE.match(lowered):
        raise ValueError(f"unsafe MIME type: {value!r}")
    return lowered
