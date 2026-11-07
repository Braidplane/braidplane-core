#!/usr/bin/env python3
"""Verify the Core-produced artifacts named by one approved release record."""
import argparse
import hashlib
import json
import re
import subprocess
import tarfile
from pathlib import Path

from scripts.core_release_sbom import require_image_manifest
from scripts.installation.release import validate
from scripts.qualify_core_images import BINARIES
from scripts.verify_core_attestation import PROVENANCE, SPDX, verify


def unique_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate key: {key}")
        result[key] = value
    return result


def read_json(path):
    return json.loads(path.read_bytes(), object_pairs_hook=unique_keys)


def check_archive_manifest(archive, release):
    prefix = f"braidplane-{release['version']}-{release['platform'].replace('/', '-')}"
    with tarfile.open(archive, "r:gz") as bundle:
        members = [member for member in bundle if member.name == prefix + "/release.json"]
        if len(members) != 1 or not members[0].isfile() or members[0].size > 65536:
            raise ValueError("installation archive has no unique regular release manifest")
        contents = bundle.extractfile(members[0]).read()
        if json.loads(contents, object_pairs_hook=unique_keys) != release:
            raise ValueError("installation archive manifest differs from the signed release record")


def verify_release(record_path, archive, expected, output, docker_config):
    keys = {"record_sha256", "repository", "workflow", "source_sha", "signer_sha", "run_id", "attempt",
            "version", "platform", "migration_version", "migration_digest"}
    if (not isinstance(expected, dict) or set(expected) != keys or
            type(expected["migration_version"]) is not int or expected["migration_version"] <= 0):
        raise ValueError("incomplete independent release expectations")
    raw = record_path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != expected["record_sha256"]:
        raise ValueError("release record differs from the approved digest")
    record = json.loads(raw, object_pairs_hook=unique_keys)
    if not isinstance(record, dict) or set(record) != {"release", "migration", "archive_sha256"}:
        raise ValueError("invalid release record")
    release = validate(record["release"])
    migration = record["migration"]
    if (release["core_revision"] != expected["source_sha"] or release["version"] != expected["version"] or
            release["platform"] != expected["platform"] or not isinstance(migration, dict) or
            set(migration) != {"version", "digest"} or type(migration["version"]) is not int or
            migration["version"] <= 0 or migration["version"] != expected["migration_version"] or
            not isinstance(migration["digest"], str) or not re.fullmatch(r"[0-9a-f]{64}", migration["digest"]) or
            migration["digest"] != expected["migration_digest"] or
            not isinstance(record["archive_sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", record["archive_sha256"])):
        raise ValueError("record differs from approved source, release or migration identities")
    output.mkdir(parents=False, exist_ok=False)
    results = []
    def check(name, subject, digest, kind):
        result = verify(subject, digest, expected["repository"], expected["workflow"], expected["source_sha"],
                        expected["signer_sha"], expected["run_id"], expected["attempt"], kind, expected["version"], expected["platform"])
        filename = name + ".verified.json"
        (output / filename).write_bytes(result)
        results.append({"file": filename, "sha256": hashlib.sha256(result).hexdigest()})

    check("record", str(record_path), expected["record_sha256"], PROVENANCE)
    check("archive", str(archive), record["archive_sha256"], PROVENANCE)
    check_archive_manifest(archive, release)
    for component in BINARIES:
        image = release["images"][component]
        require_image_manifest(image, docker_config.resolve(strict=True), release["platform"])
        digest = image.rsplit("@sha256:", 1)[1]
        for name, kind in (("provenance", PROVENANCE), ("sbom", SPDX)):
            check(f"{component}.{name}", "oci://" + image, digest, kind)
    receipt = {"expectations": expected, "release": release, "migration": migration,
               "archive_sha256": record["archive_sha256"], "verified": results}
    (output / "verification.json").write_text(json.dumps(receipt, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("record", type=Path)
    parser.add_argument("archive", type=Path)
    parser.add_argument("expectations", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("docker_config", type=Path)
    args = parser.parse_args()
    try:
        verify_release(args.record, args.archive, read_json(args.expectations), args.output, args.docker_config)
    except (OSError, ValueError, TypeError, KeyError, tarfile.TarError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"release verification failed: {error}\n")


if __name__ == "__main__":
    main()
