#!/usr/bin/env python3
"""Build Core release candidates as local OCI archives without publishing."""
import argparse
import hashlib
import json
import os
import subprocess
import tempfile
import uuid
from pathlib import Path

from scripts.core_release_candidate import candidate
from scripts.core_release_record import record
from scripts.installation.package import package
from scripts.qualify_core_images import BINARIES, hashes
from scripts.verify_core_oci import verify_archive

BUILDKIT_IMAGE = "docker.io/moby/buildkit@sha256:93da6a53287490dd3b35952f73a109654ed9f260f2e6434db925125fa3ac6b2a"


def build(args, env, event):
    repository = env["GITHUB_REPOSITORY"]
    revision = env["GITHUB_SHA"]
    placeholders = {name: {"containerimage.digest": "sha256:" + "0" * 64} for name in BINARIES}
    candidate(repository.lower(), revision, args.version, args.platform, args.edge, args.postgres, placeholders)
    if (env["GITHUB_REF"] != "refs/heads/main" or env["GITHUB_EVENT_NAME"] != "workflow_dispatch" or
            env["GITHUB_WORKFLOW_SHA"] != revision or event["repository"]["full_name"] != repository):
        raise ValueError("candidate requires the reviewed main workflow and source revision")
    checkout = args.checkout.resolve(strict=True)
    if checkout != Path(__file__).resolve().parents[1]:
        raise ValueError("release tooling must run from the candidate checkout")
    output = args.output.resolve()
    if output.is_relative_to(checkout):
        raise ValueError("release output must be outside the checkout")
    head = subprocess.check_output(["git", "-C", str(checkout), "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"])
    if head != revision or dirty:
        raise ValueError("checkout must be clean and match the approved revision")
    output.mkdir(parents=False, exist_ok=False)
    metadata_dir = output / "metadata"
    metadata_dir.mkdir()
    images_dir = output / "oci"
    images_dir.mkdir()
    builder = "core-release-" + uuid.uuid4().hex
    with tempfile.TemporaryDirectory(prefix="core-release-docker-") as docker_dir:
        docker_env = dict(env, DOCKER_CONFIG=docker_dir)
        created = False
        try:
            subprocess.run(["docker", "buildx", "create", "--name", builder, "--driver", "docker-container",
                            "--driver-opt", "image=" + BUILDKIT_IMAGE], env=docker_env, check=True)
            created = True
            base = ["docker", "buildx", "build", "--builder", builder, "--no-cache", "--platform", args.platform]
            subprocess.run([*base, "--target", "release-evidence", "--output", f"type=local,dest={output / 'build'}", str(checkout)],
                           env=docker_env, check=True)
            for name in BINARIES:
                subprocess.run([*base, "--target", name, "--provenance=false", "--sbom=false",
                                "--label", f"org.opencontainers.image.source=https://github.com/{repository}",
                                "--metadata-file", str(metadata_dir / f"{name}.json"), "--output",
                                f"type=oci,dest={images_dir / (name + '.tar')}", str(checkout)],
                               env=docker_env, check=True)
            metadata = {name: json.loads((metadata_dir / f"{name}.json").read_text()) for name in BINARIES}
            release = candidate(repository.lower(), revision, args.version, args.platform, args.edge, args.postgres, metadata)
            for name in BINARIES:
                verify_archive(images_dir / f"{name}.tar", metadata[name]["containerimage.digest"], args.platform)
            manifest = output / "release.json"
            manifest.write_text(json.dumps(release, indent=2, sort_keys=True) + "\n")
            archive = output / "installation.tar.gz"
            package(manifest, archive)
            record_path = output / "record.json"
            record_path.write_text(json.dumps(record(release, archive, checkout), indent=2, sort_keys=True) + "\n")
            images = {}
            for name in BINARIES:
                with (images_dir / f"{name}.tar").open("rb") as image:
                    images[name] = {"manifest_digest": metadata[name]["containerimage.digest"],
                                    "oci_archive_sha256": hashlib.file_digest(image, "sha256").hexdigest()}
            baseline = hashlib.sha256(json.dumps(hashes(output / "build"), sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            approval = {"core_revision": revision, "version": args.version, "platform": args.platform,
                        "baseline_sha256": baseline,
                        "record_sha256": hashlib.sha256(record_path.read_bytes()).hexdigest(), "images": images}
            (output / "approval.json").write_text(json.dumps(approval, indent=2, sort_keys=True) + "\n")
        finally:
            if created:
                subprocess.run(["docker", "buildx", "rm", builder], env=docker_env, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("version", "platform", "edge", "postgres"):
        parser.add_argument(name)
    parser.add_argument("checkout", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
        build(args, os.environ, event)
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Core candidate build failed: {error}\n")


if __name__ == "__main__":
    main()
