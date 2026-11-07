"""Verify a versioned backup receipt without extracting secrets or restoring data."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path

from .release import validate

FILES = ("database.dump", "configuration.tar")


def fingerprint(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError("backup payload must be a regular file")
    with path.open("rb") as source:
        digest = hashlib.file_digest(source, "sha256").hexdigest()
    size = path.stat().st_size
    if not size:
        raise ValueError("backup payload must not be empty")
    return {"sha256": digest, "bytes": size}


def seal(directory, release, stopped_at):
    receipt = {"format_version": 1, "release": validate(release),
               "writers_stopped_at": stopped_at,
               "completed_at": datetime.now(timezone.utc).isoformat(),
               "files": {name: fingerprint(directory / name) for name in FILES}}
    descriptor = os.open(directory / "backup.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w") as output:
        json.dump(receipt, output, indent=2, sort_keys=True)
        output.write("\n")
    verify(directory)


def verify(directory):
    directory = Path(directory)
    receipt = json.loads((directory / "backup.json").read_text())
    if not isinstance(receipt, dict) or set(receipt) != {
            "format_version", "release", "writers_stopped_at", "completed_at", "files"}:
        raise ValueError("invalid backup receipt")
    if type(receipt["format_version"]) is not int or receipt["format_version"] != 1:
        raise ValueError("unsupported backup format")
    validate(receipt["release"])
    times = []
    for name in ("writers_stopped_at", "completed_at"):
        value = receipt[name]
        if not isinstance(value, str):
            raise ValueError("invalid backup timestamp")
        timestamp = datetime.fromisoformat(value)
        if timestamp.tzinfo is None:
            raise ValueError("backup timestamp requires a timezone")
        times.append(timestamp)
    if times[1] < times[0]:
        raise ValueError("backup completion precedes stopped writers")
    if not isinstance(receipt["files"], dict) or set(receipt["files"]) != set(FILES):
        raise ValueError("backup payload inventory mismatch")
    for name in FILES:
        if fingerprint(directory / name) != receipt["files"][name]:
            raise ValueError("backup payload integrity mismatch: " + name)
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    try:
        verify(args.directory)
    except (ValueError, OSError) as error:
        parser.exit(1, f"backup verification failed: {error}\n")
    print("Backup receipt and payload checksums verified; restore remains unqualified.")


if __name__ == "__main__":
    main()
