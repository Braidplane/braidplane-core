#!/usr/bin/env python3
"""Publish only the approved Core OCI images by digest."""
import argparse
import json
import os
import subprocess
import tempfile
from pathlib import Path

from scripts.admit_core_ci import admit
from scripts.core_release_provenance import predicate
from scripts.core_release_sbom import generate, require_image_manifest
from scripts.qualify_core_images import BINARIES, qualify
from scripts.validate_core_candidate import validate_candidate


ORAS_IMAGE = "ghcr.io/oras-project/oras@sha256:9cfb578be975b762fd898b9d004243d8003aeb807a3e15b2ed212cdd1326c9eb"


def publish(directory, approved_digest, checkout, output, ci_run_id, ci_attempt, env, event):
    if env.get("CORE_RELEASE_PUBLICATION_AUTHORIZED") != "yes":
        raise ValueError("explicit Core publication authorization is required")
    repository, sha = env["GITHUB_REPOSITORY"], env["GITHUB_SHA"]
    checkout = checkout.resolve(strict=True)
    if checkout != Path(__file__).resolve().parents[1]:
        raise ValueError("release tooling must run from the candidate checkout")
    head = subprocess.check_output(["git", "-C", str(checkout), "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"])
    if head != sha or dirty:
        raise ValueError("checkout must be clean and match the approved revision")
    directory = directory.absolute()
    record = validate_candidate(directory, approved_digest, repository, sha)
    directory = directory.resolve(strict=True)
    provenance = predicate(record["release"], env, event)
    source_catalog = subprocess.check_output(["git", "-C", str(checkout), "show", f"{sha}:postgres/migrations/manifest.sha256"])
    expected_catalog = f"{record['migration']['version']} {record['migration']['digest']}\n".encode()
    if source_catalog != expected_catalog:
        raise ValueError("candidate migration identity differs from the approved source")
    ci = admit(repository, sha, ci_run_id, ci_attempt)
    output = output.resolve()
    if output.is_relative_to(checkout) or output.is_relative_to(directory):
        raise ValueError("publication evidence must be outside the checkout and candidate directory")
    output.mkdir(parents=False, exist_ok=False)
    (output / "ci-admission.json").write_text(json.dumps(ci, indent=2) + "\n")
    (output / "provenance.json").write_text(json.dumps(provenance, indent=2) + "\n")
    with tempfile.TemporaryDirectory(prefix="core-release-publisher-") as docker_dir:
        docker_env = dict(env, DOCKER_CONFIG=docker_dir)
        subprocess.run(["docker", "login", "ghcr.io", "--username", env["GITHUB_ACTOR"], "--password-stdin"],
                       input=env["GITHUB_TOKEN"].encode(), env=docker_env, check=True)
        with (output / "publication.jsonl").open("x") as journal:
            for name in BINARIES:
                image = record["release"]["images"][name]
                destination, digest = image.split("@")
                journal.write(json.dumps({"component": name, "image": image, "status": "attempted"}) + "\n")
                journal.flush()
                os.fsync(journal.fileno())
                subprocess.run([
                    "docker", "run", "--rm", "--user", f"{os.getuid()}:{os.getgid()}", "--read-only",
                    "--cap-drop=ALL", "--security-opt=no-new-privileges", "--tmpfs", "/tmp:rw,noexec,nosuid,mode=1777",
                    "--env", "HOME=/tmp", "--env", "DOCKER_CONFIG=/config", "--volume", f"{docker_dir}:/config:ro",
                    "--volume", f"{directory.resolve()}:/candidate:ro", ORAS_IMAGE, "cp", "--from-oci-layout",
                    f"/candidate/oci/{name}.tar@{digest}", destination,
                ], env=docker_env, check=True)
                require_image_manifest(image, Path(docker_dir), record["release"]["platform"])
                journal.write(json.dumps({"component": name, "image": image, "status": "verified"}) + "\n")
                journal.flush()
                os.fsync(journal.fileno())
        old_config = os.environ.get("DOCKER_CONFIG")
        os.environ["DOCKER_CONFIG"] = docker_dir
        try:
            qualify(record["release"], checkout, directory / "build")
            generate(record["release"], output / "sboms", Path(docker_dir))
        finally:
            if old_config is None:
                os.environ.pop("DOCKER_CONFIG", None)
            else:
                os.environ["DOCKER_CONFIG"] = old_config


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("candidate", type=Path)
    parser.add_argument("approved_sha256")
    parser.add_argument("checkout", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("ci_run_id")
    parser.add_argument("ci_attempt")
    args = parser.parse_args()
    try:
        event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
        publish(args.candidate, args.approved_sha256, args.checkout, args.output,
                args.ci_run_id, args.ci_attempt, os.environ, event)
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Core candidate publication failed: {error}\n")


if __name__ == "__main__":
    main()
