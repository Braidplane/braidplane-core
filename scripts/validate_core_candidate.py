"""Validate all candidate bytes against an independently approved digest."""
import hashlib
import json
import re
from pathlib import Path

from scripts.installation.release import validate
from scripts.qualify_core_images import BINARIES, hashes
from scripts.verify_core_oci import verify_archive
from scripts.verify_core_release import check_archive_manifest, read_json


def file_hash(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError(f"candidate file is missing or symbolic: {path.name}")
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def validate_candidate(directory, approved_sha256, repository, source_sha):
    if (not re.fullmatch(r"[0-9a-f]{64}", approved_sha256) or
            not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository) or
            not re.fullmatch(r"[0-9a-f]{40}", source_sha)):
        raise ValueError("invalid independent approval identity")
    if directory.is_symlink() or not directory.is_dir() or (directory / "oci").is_symlink():
        raise ValueError("candidate directory must not be symbolic")
    approval_path = directory / "approval.json"
    if file_hash(approval_path) != approved_sha256:
        raise ValueError("approval document differs from the approved digest")
    approval = read_json(approval_path)
    if not isinstance(approval, dict) or set(approval) != {"core_revision", "version", "platform", "record_sha256", "baseline_sha256", "images"}:
        raise ValueError("invalid candidate approval document")
    if approval["core_revision"] != source_sha:
        raise ValueError("candidate source differs from the approved revision")
    baseline = hashlib.sha256(json.dumps(hashes(directory / "build"), sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    if baseline != approval["baseline_sha256"]:
        raise ValueError("candidate build evidence differs from the approved document")
    record_path = directory / "record.json"
    if file_hash(record_path) != approval["record_sha256"]:
        raise ValueError("candidate record differs from the approved document")
    record = read_json(record_path)
    if not isinstance(record, dict) or set(record) != {"release", "migration", "archive_sha256"}:
        raise ValueError("invalid candidate record")
    migration = record["migration"]
    if (not isinstance(migration, dict) or set(migration) != {"version", "digest"} or
            type(migration["version"]) is not int or migration["version"] <= 0 or
            not isinstance(migration["digest"], str) or not re.fullmatch(r"[0-9a-f]{64}", migration["digest"])):
        raise ValueError("invalid candidate migration identity")
    release = validate(record["release"])
    manifest_path = directory / "release.json"
    if manifest_path.is_symlink() or not manifest_path.is_file() or read_json(manifest_path) != release:
        raise ValueError("candidate manifest differs from the approved record")
    if (release["core_revision"] != source_sha or release["version"] != approval["version"] or
            release["platform"] != approval["platform"]):
        raise ValueError("candidate release identity differs from the approved document")
    archive = directory / "installation.tar.gz"
    if file_hash(archive) != record["archive_sha256"]:
        raise ValueError("installation archive differs from the candidate record")
    check_archive_manifest(archive, release)
    images = approval["images"]
    if not isinstance(images, dict) or set(images) != set(BINARIES):
        raise ValueError("candidate image set is incomplete")
    for name in BINARIES:
        image = images[name]
        if not isinstance(image, dict) or set(image) != {"manifest_digest", "oci_archive_sha256"}:
            raise ValueError(f"invalid {name} candidate identity")
        digest = image["manifest_digest"]
        if release["images"][name] != f"ghcr.io/{repository.lower()}-{name}@{digest}":
            raise ValueError(f"{name} image destination differs from the approved repository")
        archive_path = directory / "oci" / f"{name}.tar"
        if file_hash(archive_path) != image["oci_archive_sha256"]:
            raise ValueError(f"{name} OCI archive differs from the approved document")
        verify_archive(archive_path, digest, release["platform"])
    return record
