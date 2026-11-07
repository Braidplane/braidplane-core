#!/usr/bin/env python3
"""Create an installation manifest from Core image build metadata."""
import argparse
import json
import re
from pathlib import Path

from scripts.installation.release import validate
from scripts.qualify_core_images import BINARIES


def candidate(repository, revision, version, platform, edge, postgres, metadata):
    if not re.fullmatch(r"[a-z0-9]+(?:[._-][a-z0-9]+)*/[a-z0-9]+(?:[._-][a-z0-9]+)*", repository):
        raise ValueError("invalid Core repository")
    if not isinstance(metadata, dict) or set(metadata) != set(BINARIES):
        raise ValueError("build metadata must contain all four Core images")
    images = {}
    for component in BINARIES:
        digest = metadata[component].get("containerimage.digest") if isinstance(metadata[component], dict) else None
        if not isinstance(digest, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            raise ValueError(f"missing image digest for {component}")
        images[component] = f"ghcr.io/{repository}-{component}@{digest}"
    images.update(edge=edge, postgres=postgres)
    return validate({"version": version, "core_revision": revision, "platform": platform, "images": images})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repository", help="lowercase GitHub owner/repository")
    parser.add_argument("revision")
    parser.add_argument("version")
    parser.add_argument("platform")
    parser.add_argument("edge")
    parser.add_argument("postgres")
    parser.add_argument("metadata", type=Path, help="directory with a Buildx JSON file per Core image")
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        metadata = {name: json.loads((args.metadata / f"{name}.json").read_text()) for name in BINARIES}
        release = candidate(args.repository, args.revision, args.version, args.platform, args.edge, args.postgres, metadata)
        with args.output.open("x") as output:
            json.dump(release, output, indent=2, sort_keys=True)
            output.write("\n")
    except (OSError, ValueError, TypeError) as error:
        parser.exit(1, f"candidate preparation failed: {error}\n")


if __name__ == "__main__":
    main()
