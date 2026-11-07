"""Build a deterministic, source-independent installation tools archive."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import tarfile
import tempfile

from cli.scripts.release import add_tar_file
from .release import read_manifest

SOURCE = Path(__file__).resolve().parent
ROOT = SOURCE.parent.parent
TOOLS = ("release.py", "prepare.py", "preflight.py", "lifecycle.py", "upgrade.py", "backup.py", "restore_probe.py", "restore.py", "compose.json", "Caddyfile", "execution-grants.sql")


def package(manifest_path, output):
    manifest = read_manifest(manifest_path)
    prefix = f"braidplane-{manifest['version']}-{manifest['platform'].replace('/', '-')}"
    files = {f"scripts/installation/{name}": (SOURCE / name).read_bytes() for name in TOOLS}
    files.update({
        "scripts/__init__.py": b"", "scripts/installation/__init__.py": b"",
        "scripts/self-hosted-runtime-grants.sql": (SOURCE.parent / "self-hosted-runtime-grants.sql").read_bytes(),
        "release.json": (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode(),
        "INSTALL.md": (SOURCE / "INSTALL.md").read_bytes(),
        "RECOVERY.md": (SOURCE / "RECOVERY.md").read_bytes(),
        "LICENSE": (ROOT / "LICENSE").read_bytes(), "NOTICE": (ROOT / "NOTICE").read_bytes(),
    })
    for name in ("first-result.pipeline.json", "input.json", "expected.json", "write-operation.json"):
        files[f"first-result/{name}"] = (SOURCE / "first-result" / name).read_bytes()
    files["SHA256SUMS"] = "".join(f"{hashlib.sha256(data).hexdigest()}  {name}\n" for name, data in sorted(files.items())).encode()
    output = Path(output)
    # Build beside the destination, then publish without replacing an existing artifact.
    with tempfile.TemporaryDirectory(prefix=".braidplane-package-", dir=output.parent) as temporary:
        archive_path = Path(temporary) / "bundle.tar.gz"
        with archive_path.open("wb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.GNU_FORMAT) as archive:
                    for name, data in sorted(files.items()):
                        add_tar_file(archive, prefix + "/" + name, data, 0o644)
        os.link(archive_path, output)
    return hashlib.sha256(output.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        digest = package(args.manifest, args.output)
    except (ValueError, OSError) as error:
        parser.exit(1, f"bundle packaging failed: {error}\n")
    print(f"{digest}  {args.output.name}")


if __name__ == "__main__":
    main()
