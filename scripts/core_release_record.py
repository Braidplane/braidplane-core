#!/usr/bin/env python3
"""Prepare a candidate Core artifact and migration identity record."""
import argparse
import hashlib
import json
import re
import subprocess
import tarfile
from pathlib import Path

from scripts.installation.release import read_manifest
from scripts.verify_core_release import check_archive_manifest


def record(release, archive, checkout):
    sha = subprocess.check_output(["git", "-C", str(checkout), "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"])
    if sha != release["core_revision"] or dirty:
        raise ValueError("checkout must be clean and match the candidate revision")
    catalog = subprocess.check_output(["git", "-C", str(checkout), "show", f"{sha}:postgres/migrations/manifest.sha256"])
    match = re.fullmatch(rb"([1-9][0-9]*) ([0-9a-f]{64})\n", catalog)
    if not match:
        raise ValueError("invalid source migration manifest")
    check_archive_manifest(archive, release)
    return {"release": release, "migration": {"version": int(match[1]), "digest": match[2].decode("ascii")},
            "archive_sha256": hashlib.sha256(archive.read_bytes()).hexdigest()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("archive", type=Path)
    parser.add_argument("checkout", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        value = record(read_manifest(args.manifest), args.archive, args.checkout)
        with args.output.open("x") as output:
            json.dump(value, output, indent=2, sort_keys=True)
            output.write("\n")
    except (OSError, ValueError, tarfile.TarError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"release record preparation failed: {error}\n")


if __name__ == "__main__":
    main()
