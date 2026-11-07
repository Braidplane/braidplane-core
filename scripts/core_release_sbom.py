#!/usr/bin/env python3
"""Generate SPDX inventories for the four immutable Core image references."""
import argparse
import hashlib
import json
import os
import subprocess
from pathlib import Path

from scripts.installation.release import read_manifest
from scripts.qualify_core_images import BINARIES


SYFT = "docker.io/anchore/syft@sha256:4cc92b64ca74ffca4c94d4c8fcc01f217731375300be14d0b6eeb2e82eaa110a"
IMAGE_MANIFESTS = {"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"}
IMAGE_CONFIGS = {"application/vnd.oci.image.config.v1+json", "application/vnd.docker.container.image.v1+json"}


def require_image_manifest(image, config, platform):
    env = dict(os.environ, DOCKER_CONFIG=str(config))
    raw = subprocess.check_output(["docker", "buildx", "imagetools", "inspect", "--raw", image], env=env)
    if hashlib.sha256(raw).hexdigest() != image.rsplit("@sha256:", 1)[1]:
        raise ValueError("registry manifest bytes do not match the requested image digest")
    document = json.loads(raw)
    if (not isinstance(document, dict) or document.get("schemaVersion") != 2 or
            document.get("mediaType") not in IMAGE_MANIFESTS or not isinstance(document.get("config"), dict) or
            document["config"].get("mediaType") not in IMAGE_CONFIGS or
            not isinstance(document.get("layers"), list)):
        raise ValueError("image digest must identify a single-platform image manifest, not an index")
    image_config = json.loads(subprocess.check_output(
        ["docker", "buildx", "imagetools", "inspect", "--format", "{{json .Image}}", image], env=env))
    if (not isinstance(image_config, dict) or
            f"{image_config.get('os')}/{image_config.get('architecture')}" != platform):
        raise ValueError("registry image platform differs from the release manifest")


def validate_sbom(sbom):
    if not isinstance(sbom, dict):
        raise ValueError("SBOM must be an SPDX document")
    packages = sbom.get("packages")
    relationships = sbom.get("relationships")
    if not isinstance(packages, list) or not isinstance(relationships, list):
        raise ValueError("SBOM must contain packages and relationships")
    ids = {package.get("SPDXID") for package in packages
           if isinstance(package, dict) and isinstance(package.get("SPDXID"), str) and package["SPDXID"]
           and isinstance(package.get("name"), str) and package["name"]}
    described = {item.get("relatedSpdxElement") for item in relationships
                 if isinstance(item, dict) and item.get("spdxElementId") == "SPDXRef-DOCUMENT"
                 and item.get("relationshipType") == "DESCRIBES" and isinstance(item.get("relatedSpdxElement"), str)}
    if (sbom.get("spdxVersion") != "SPDX-2.3" or sbom.get("SPDXID") != "SPDXRef-DOCUMENT" or
            not isinstance(sbom.get("documentNamespace"), str) or not sbom["documentNamespace"] or not ids or
            not (described & ids) or
            not any(isinstance(item, dict) and item.get("spdxElementId") in described and
                    item.get("relationshipType") == "CONTAINS" and
                    isinstance(item.get("relatedSpdxElement"), str) and item["relatedSpdxElement"] in ids - described
                    for item in relationships)):
        raise ValueError("SBOM must be a nonempty SPDX 2.3 inventory describing an image")


def generate(manifest, output, docker_config):
    config = docker_config.resolve(strict=True)
    if not config.is_dir():
        raise ValueError("Docker configuration must be a directory")
    output.mkdir(parents=False, exist_ok=False)
    output = output.resolve()
    for component in BINARIES:
        image = manifest["images"][component]
        require_image_manifest(image, config, manifest["platform"])
        target = output / f"{component}.spdx.json"
        subprocess.run([
            "docker", "run", "--rm", "--user", f"{os.getuid()}:{os.getgid()}", "--read-only",
            "--cap-drop=ALL", "--security-opt=no-new-privileges", "--tmpfs", "/tmp:rw,noexec,nosuid,mode=1777",
            "--env", "HOME=/tmp", "--env", "DOCKER_CONFIG=/config", "--env", "SYFT_CHECK_FOR_APP_UPDATE=false",
            "--volume", f"{config}:/config:ro", "--volume", f"{output}:/out",
            SYFT, "scan", f"registry:{image}", "--platform", manifest["platform"],
            "-o", f"spdx-json=/out/{target.name}",
        ], check=True)
        validate_sbom(json.loads(target.read_text()))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("output", type=Path, help="new directory for inventories")
    parser.add_argument("docker_config", type=Path, help="isolated registry configuration directory")
    args = parser.parse_args()
    try:
        generate(read_manifest(args.manifest), args.output, args.docker_config)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"SBOM generation failed: {error}\n")


if __name__ == "__main__":
    main()
