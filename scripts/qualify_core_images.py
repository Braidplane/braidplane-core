#!/usr/bin/env python3
"""Compare pulled Core image payloads with outputs from the admitted build."""
import argparse
import hashlib
import json
import subprocess
import sys
import tempfile
from pathlib import Path

from scripts.installation.release import read_manifest


BINARIES = {
    "backend": ("braidplane-server", "braidplane-admin", "braidplane-healthcheck"),
    "frontend": ("braidplane-frontend", "braidplane-healthcheck"),
    "migrate": ("braidplane-migrate",),
    "cli": ("braidplane",),
}


def hashes(root):
    if root.is_symlink() or not root.is_dir():
        raise ValueError(f"missing or symbolic directory: {root}")
    files = {}
    for path in root.rglob("*"):
        if path.is_symlink() or not (path.is_dir() or path.is_file()):
            raise ValueError(f"unsupported release payload: {path}")
        if path.is_file():
            files[path.relative_to(root).as_posix()] = hashlib.sha256(path.read_bytes()).hexdigest()
    if not files:
        raise ValueError(f"empty release payload: {root}")
    return files


def qualify(manifest, checkout, build):
    head = subprocess.check_output(["git", "-C", str(checkout), "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"])
    if head != manifest["core_revision"] or dirty:
        raise ValueError("checkout must be clean and match the admitted revision")
    expected_catalog = (checkout / "postgres/migrations/manifest.sha256").read_text().split()
    if len(expected_catalog) != 2:
        raise ValueError("invalid source migration manifest")

    for component, binaries in BINARIES.items():
        image = manifest["images"][component]
        platform = manifest["platform"]
        subprocess.run(["docker", "pull", "--platform", platform, image], check=True)
        image_platform = subprocess.check_output(
            ["docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image], text=True).strip()
        if image_platform != platform:
            raise ValueError(f"{component} image platform differs from manifest")
        container = subprocess.check_output(["docker", "create", "--platform", platform, "--network=none", image], text=True).strip()
        if not container:
            raise ValueError("docker create returned no container")
        try:
            with tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                for name in binaries:
                    target = root / name
                    subprocess.run(["docker", "cp", f"{container}:/{name}", str(target)], check=True)
                    if (not target.is_file() or target.is_symlink() or not target.stat().st_mode & 0o111 or
                            target.read_bytes() != (build / "out" / name).read_bytes()):
                        raise ValueError(f"{component} binary differs from admitted build: {name}")
                if component == "frontend":
                    for source, expected, name in (
                        ("/app/frontend/assets", build / "frontend-assets", "assets"),
                        ("/app/brand/icons", checkout / "brand/icons", "icons"),
                    ):
                        target = root / name
                        subprocess.run(["docker", "cp", f"{container}:{source}", str(target)], check=True)
                        if hashes(target) != hashes(expected):
                            raise ValueError(f"published frontend {name} differs from admitted build")
                if component == "migrate":
                    target = root / "migrations"
                    subprocess.run(["docker", "cp", f"{container}:/postgres/migrations", str(target)], check=True)
                    if hashes(target) != hashes(checkout / "postgres/migrations"):
                        raise ValueError("published migrations differ from admitted source")
                    output = subprocess.check_output([
                        "docker", "run", "--rm", "--platform", platform, "--network=none", "--read-only",
                        "--cap-drop=ALL", "--security-opt=no-new-privileges", image, "catalog"])
                    identity = json.loads(output)
                    if identity != {"version": int(expected_catalog[0]), "digest": expected_catalog[1]}:
                        raise ValueError("embedded migration catalog differs from admitted source")
        finally:
            subprocess.run(["docker", "rm", container], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("checkout", type=Path)
    parser.add_argument("build", type=Path)
    args = parser.parse_args()
    try:
        qualify(read_manifest(args.manifest), args.checkout.resolve(), args.build.resolve())
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"image qualification failed: {error}\n")


if __name__ == "__main__":
    main()
