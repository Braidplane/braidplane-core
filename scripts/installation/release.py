"""Validate a single-host release manifest and emit inert Compose image inputs."""
import argparse
import json
import re
from pathlib import Path

IMAGES = ("backend", "frontend", "migrate", "cli", "edge", "postgres")
# Restrict references to registry/repository names plus an immutable digest.
IMAGE = re.compile(r"(?:[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]+)?/)?"
                   r"[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}")
VERSION = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?")


def validate(manifest):
    if not isinstance(manifest, dict) or set(manifest) != {"version", "core_revision", "platform", "images"}:
        raise ValueError("release must contain version, core_revision, platform and images only")
    if not isinstance(manifest["version"], str) or not VERSION.fullmatch(manifest["version"]):
        raise ValueError("version must be a v-prefixed release version")
    if not isinstance(manifest["core_revision"], str) or not re.fullmatch(r"[0-9a-f]{40}", manifest["core_revision"]):
        raise ValueError("core_revision must be a full lowercase Git commit ID")
    if manifest["platform"] not in ("linux/amd64", "linux/arm64"):
        raise ValueError("platform must be linux/amd64 or linux/arm64")
    images = manifest["images"]
    if not isinstance(images, dict) or set(images) != set(IMAGES):
        raise ValueError("images must contain exactly: " + ", ".join(IMAGES))
    for name, reference in images.items():
        if not isinstance(reference, str) or not IMAGE.fullmatch(reference):
            raise ValueError(f"{name} must be a repository@sha256 digest without a mutable tag")
    return manifest


def read_manifest(path):
    def unique_keys(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError(f"duplicate manifest key: {key}")
            result[key] = value
        return result
    return validate(json.loads(Path(path).read_text(), object_pairs_hook=unique_keys))


def compose_inputs(manifest):
    validate(manifest)
    return "".join([
        f"BRAIDPLANE_VERSION={manifest['version']}\n",
        f"BRAIDPLANE_PLATFORM={manifest['platform']}\n",
        *(f"{name.upper()}_IMAGE={manifest['images'][name]}\n" for name in IMAGES),
    ])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("output", type=Path, help="new Compose env file; existing files are preserved")
    args = parser.parse_args()
    try:
        content = compose_inputs(read_manifest(args.manifest))
        with args.output.open("x") as output:
            output.write(content)
    except (ValueError, OSError) as error:
        parser.exit(1, f"release validation failed: {error}\n")


if __name__ == "__main__":
    main()
