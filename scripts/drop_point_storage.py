from __future__ import annotations

import errno
import hashlib
import json
import os
import re
import shutil
import stat
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable, Protocol

from drop_point_protocol import filename_collision_key

_RECEIPT_NAME = ".droppoint-receipt.json"
_DROP_POINT_ID_RE = re.compile(r"^dp_[A-Za-z0-9_-]+$")


class RecoveredFile(Protocol):
    name: str
    mime_type: str
    data: bytes


@dataclass(frozen=True)
class BundleInstall:
    path: Path
    identity: str
    already_installed: bool
    files: tuple[str, ...]


def encrypted_bundle_identity(envelope_json: bytes, encrypted_payload: bytes) -> str:
    digest = hashlib.sha256()
    digest.update(b"DropPoint installed bundle v1\x00")
    for value in (envelope_json, encrypted_payload):
        digest.update(len(value).to_bytes(8, "big"))
        digest.update(value)
    return digest.hexdigest()


def atomic_write_private_json(path: Path, value: object) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    temporary_path = Path(temporary_name)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as file:
            json.dump(value, file, indent=2)
            file.write("\n")
            file.flush()
            os.fsync(file.fileno())
        os.replace(temporary_path, path)
        os.chmod(path, 0o600)
        fsync_directory(path.parent)
    except BaseException:
        try:
            os.close(fd)
        except OSError:
            pass
        temporary_path.unlink(missing_ok=True)
        raise


def install_bundle(
    out_dir: Path,
    drop_point_id: str,
    identity: str,
    recovered_files: Iterable[RecoveredFile],
) -> BundleInstall:
    if not _DROP_POINT_ID_RE.fullmatch(drop_point_id):
        raise ValueError("invalid drop_point_id in receiver state")
    if not re.fullmatch(r"[0-9a-f]{64}", identity):
        raise ValueError("invalid installed bundle identity")

    files = tuple(recovered_files)
    receipt = _build_receipt(drop_point_id, identity, files)
    out_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    final_path = out_dir / f"bundle-{drop_point_id}"
    if final_path.exists():
        _verify_receipt_and_files(final_path, receipt)
        return BundleInstall(final_path, identity, True, tuple(entry["name"] for entry in receipt["files"]))

    staging_path = Path(tempfile.mkdtemp(prefix=f".{final_path.name}.", suffix=".tmp", dir=out_dir))
    try:
        os.chmod(staging_path, 0o700)
        for recovered, entry in zip(files, receipt["files"], strict=True):
            _write_new_private_file(staging_path / recovered.name, recovered.data)
            if entry["name"] == _RECEIPT_NAME:
                raise ValueError(f"filename {_RECEIPT_NAME!r} is reserved by the receiver")
        _write_new_private_file(
            staging_path / _RECEIPT_NAME,
            (json.dumps(receipt, indent=2, sort_keys=True) + "\n").encode("utf-8"),
        )
        fsync_directory(staging_path)
        try:
            os.rename(staging_path, final_path)
        except OSError as exc:
            if exc.errno not in {errno.EEXIST, errno.ENOTEMPTY}:
                raise
            _verify_receipt_and_files(final_path, receipt)
            shutil.rmtree(staging_path)
            return BundleInstall(final_path, identity, True, tuple(entry["name"] for entry in receipt["files"]))
        fsync_directory(out_dir)
        return BundleInstall(final_path, identity, False, tuple(entry["name"] for entry in receipt["files"]))
    except BaseException:
        if staging_path.exists():
            shutil.rmtree(staging_path)
        raise


def verify_installed_bundle(path: Path, drop_point_id: str, identity: str) -> BundleInstall:
    receipt = _load_receipt(path)
    if receipt.get("drop_point_id") != drop_point_id or receipt.get("bundle_sha256") != identity:
        raise RuntimeError(f"installed bundle receipt does not match receiver state: {path}")
    _verify_receipt_and_files(path, receipt)
    files = receipt.get("files")
    assert isinstance(files, list)
    return BundleInstall(path, identity, True, tuple(entry["name"] for entry in files))


def fsync_directory(path: Path) -> None:
    flags = os.O_RDONLY
    if hasattr(os, "O_DIRECTORY"):
        flags |= os.O_DIRECTORY
    fd = os.open(path, flags)
    try:
        try:
            os.fsync(fd)
        except OSError as exc:
            if exc.errno not in {errno.EINVAL, errno.ENOTSUP}:
                raise
    finally:
        os.close(fd)


def _build_receipt(drop_point_id: str, identity: str, files: tuple[RecoveredFile, ...]) -> dict:
    entries = []
    seen: set[str] = set()
    for recovered in files:
        name = recovered.name
        if not isinstance(name, str) or not name or Path(name).name != name or "/" in name or "\\" in name:
            raise ValueError(f"receiver refused unsafe recovered filename: {name!r}")
        if name == _RECEIPT_NAME:
            raise ValueError(f"filename {_RECEIPT_NAME!r} is reserved by the receiver")
        collision_key = filename_collision_key(name)
        if collision_key in seen:
            raise ValueError(f"receiver refused duplicate recovered filename: {name!r}")
        seen.add(collision_key)
        if not isinstance(recovered.mime_type, str):
            raise ValueError(f"receiver refused invalid recovered MIME type for {name!r}")
        entries.append(
            {
                "name": name,
                "mime_type": recovered.mime_type,
                "size": len(recovered.data),
                "sha256": hashlib.sha256(recovered.data).hexdigest(),
            }
        )
    return {
        "receipt_version": 1,
        "drop_point_id": drop_point_id,
        "bundle_sha256": identity,
        "files": entries,
    }


def _write_new_private_file(path: Path, data: bytes) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    fd = os.open(path, flags, 0o600)
    try:
        view = memoryview(data)
        while view:
            written = os.write(fd, view)
            if written <= 0:
                raise OSError("short write while installing recovered bundle")
            view = view[written:]
        os.fsync(fd)
    finally:
        os.close(fd)


def _load_receipt(path: Path) -> dict:
    receipt_path = path / _RECEIPT_NAME
    try:
        value = json.loads(receipt_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise RuntimeError(f"existing bundle has no valid durable receipt: {path}") from exc
    if not isinstance(value, dict):
        raise RuntimeError(f"existing bundle has an invalid durable receipt: {path}")
    _validate_receipt(value, path)
    return value


def _validate_receipt(receipt: dict, path: Path) -> None:
    if receipt.get("receipt_version") != 1:
        raise RuntimeError(f"existing bundle has an unsupported durable receipt: {path}")
    drop_point_id = receipt.get("drop_point_id")
    identity = receipt.get("bundle_sha256")
    files = receipt.get("files")
    if not isinstance(drop_point_id, str) or not _DROP_POINT_ID_RE.fullmatch(drop_point_id):
        raise RuntimeError(f"existing bundle receipt has an invalid drop point ID: {path}")
    if not isinstance(identity, str) or not re.fullmatch(r"[0-9a-f]{64}", identity):
        raise RuntimeError(f"existing bundle receipt has an invalid identity: {path}")
    if not isinstance(files, list):
        raise RuntimeError(f"existing bundle receipt has an invalid file list: {path}")
    seen: set[str] = set()
    for entry in files:
        if not isinstance(entry, dict):
            raise RuntimeError(f"existing bundle receipt has an invalid file entry: {path}")
        name = entry.get("name")
        mime_type = entry.get("mime_type")
        size = entry.get("size")
        expected_hash = entry.get("sha256")
        if (
            not isinstance(name, str)
            or not name
            or Path(name).name != name
            or name == _RECEIPT_NAME
            or filename_collision_key(name) in seen
            or not isinstance(mime_type, str)
            or isinstance(size, bool)
            or not isinstance(size, int)
            or size < 0
            or not isinstance(expected_hash, str)
            or not re.fullmatch(r"[0-9a-f]{64}", expected_hash)
        ):
            raise RuntimeError(f"existing bundle receipt has an invalid file entry: {path}")
        seen.add(filename_collision_key(name))


def _verify_receipt_and_files(path: Path, expected_receipt: dict) -> None:
    try:
        directory_stat = path.lstat()
    except OSError as exc:
        raise RuntimeError(f"installed bundle directory is missing: {path}") from exc
    if not stat.S_ISDIR(directory_stat.st_mode) or directory_stat.st_mode & 0o077:
        raise RuntimeError(f"installed bundle directory is not owner-only: {path}")
    actual_receipt = _load_receipt(path)
    if actual_receipt != expected_receipt:
        raise RuntimeError(f"refusing to overwrite a different existing bundle: {path}")
    files = actual_receipt.get("files")
    if not isinstance(files, list):
        raise RuntimeError(f"existing bundle receipt has an invalid file list: {path}")
    expected_names = {_RECEIPT_NAME}
    for entry in files:
        if not isinstance(entry, dict):
            raise RuntimeError(f"existing bundle receipt has an invalid file entry: {path}")
        name = entry.get("name")
        size = entry.get("size")
        expected_hash = entry.get("sha256")
        if not isinstance(name, str) or not isinstance(size, int) or not isinstance(expected_hash, str):
            raise RuntimeError(f"existing bundle receipt has an invalid file entry: {path}")
        expected_names.add(name)
        file_path = path / name
        try:
            file_stat = file_path.lstat()
        except OSError as exc:
            raise RuntimeError(f"installed bundle file is missing: {file_path}") from exc
        if not stat.S_ISREG(file_stat.st_mode) or file_stat.st_size != size:
            raise RuntimeError(f"installed bundle file does not match its receipt: {file_path}")
        if file_stat.st_mode & 0o077:
            raise RuntimeError(f"installed bundle file is not owner-only: {file_path}")
        digest = _hash_file(file_path)
        if digest != expected_hash:
            raise RuntimeError(f"installed bundle file does not match its receipt: {file_path}")
    receipt_stat = (path / _RECEIPT_NAME).lstat()
    if not stat.S_ISREG(receipt_stat.st_mode) or receipt_stat.st_mode & 0o077:
        raise RuntimeError(f"installed bundle receipt is not an owner-only file: {path}")
    actual_names = {entry.name for entry in path.iterdir()}
    if actual_names != expected_names:
        raise RuntimeError(f"installed bundle contains files not covered by its receipt: {path}")


def _hash_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()
