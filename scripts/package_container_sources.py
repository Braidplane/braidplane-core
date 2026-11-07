"""Collect exact Debian source packages for a locally built backend image."""

import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
from urllib.parse import quote
from urllib.error import URLError
from urllib.request import urlopen

from cli.scripts.release import add_tar_file


ROOT = Path(__file__).resolve().parent.parent
SNAPSHOT = "https://snapshot.debian.org"
SAFE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9.+~:_-]*")
PACKAGE = re.compile(r"[a-z0-9][a-z0-9+.-]*")
SHA1 = re.compile(r"[0-9a-f]{40}")


def digest(path, algorithm="sha256"):
    result = hashlib.new(algorithm)
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            result.update(chunk)
    return result.hexdigest()


def metadata(path):
    try:
        with urlopen(SNAPSHOT + path, timeout=30) as response:
            return json.load(response)
    except (URLError, TimeoutError) as error:
        raise RuntimeError(f"Debian source metadata unavailable: {path}") from error


def source_checksums(text, package, version):
    if not re.search(r"^Source: " + re.escape(package) + r"$", text, re.M):
        raise ValueError("source descriptor package differs from installed package")
    if not re.search(r"^Version: " + re.escape(version) + r"$", text, re.M):
        raise ValueError("source descriptor version differs from installed package")
    section = re.search(r"^Checksums-Sha256:\n((?:[ \t].*\n)+)", text, re.M)
    if not section:
        raise ValueError("source descriptor has no SHA-256 file inventory")
    result = {}
    for line in section[1].splitlines():
        checksum, size, name = line.split()
        if not re.fullmatch(r"[0-9a-f]{64}", checksum) or not SAFE_NAME.fullmatch(name) or name in result:
            raise ValueError("unsafe or duplicate source descriptor filename")
        result[name] = (checksum, int(size))
    return result


def cached_file(cache, sha1):
    if not SHA1.fullmatch(sha1):
        raise ValueError("invalid Debian snapshot file identity")
    path = cache / sha1
    if path.is_file() and digest(path, "sha1") == sha1:
        return path
    with tempfile.NamedTemporaryFile(dir=cache, delete=False) as output:
        temporary = Path(output.name)
        try:
            with urlopen(SNAPSHOT + "/file/" + sha1, timeout=60) as response:
                for chunk in iter(lambda: response.read(1024 * 1024), b""):
                    output.write(chunk)
            output.flush()
            if digest(temporary, "sha1") != sha1:
                raise ValueError("Debian snapshot download checksum mismatch")
            temporary.replace(path)
        finally:
            temporary.unlink(missing_ok=True)
    return path


def collect_source(identity, cache):
    package, version = identity
    if not PACKAGE.fullmatch(package) or not SAFE_NAME.fullmatch(version):
        raise ValueError("invalid installed source package identity")
    records = metadata(f"/mr/package/{quote(package, safe='')}/{quote(version, safe='')}/srcfiles")["result"]
    files = {}
    for record in records:
        sha1 = record["hash"]
        if not SHA1.fullmatch(sha1):
            raise ValueError("invalid Debian snapshot file identity")
        for item in metadata(f"/mr/file/{sha1}/info")["result"]:
            name = item["name"]
            if not SAFE_NAME.fullmatch(name):
                raise ValueError("unsafe Debian source filename")
            if name in files and files[name] != sha1:
                raise ValueError("conflicting Debian source files")
            files[name] = sha1
    descriptors = [name for name in files if name.endswith(".dsc")]
    if len(descriptors) != 1:
        raise ValueError(f"expected one source descriptor: {package} {version}")
    descriptor = descriptors[0]
    dsc = cached_file(cache, files[descriptor])
    checksums = source_checksums(dsc.read_text(), package, version)
    selected = {descriptor: {"path": dsc, "sha256": digest(dsc), "bytes": dsc.stat().st_size}}
    for name, (checksum, size) in checksums.items():
        if name not in files:
            raise ValueError(f"source descriptor file unavailable: {name}")
        path = cached_file(cache, files[name])
        if path.stat().st_size != size or digest(path) != checksum:
            raise ValueError(f"source descriptor checksum mismatch: {name}")
        selected[name] = {"path": path, "sha256": checksum, "bytes": size}
    print(f"Verified {package} {version}: {len(selected)} source files", flush=True)
    return package, version, selected


def package_image(image, root=ROOT):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9/_.:@-]*", image):
        raise ValueError("invalid local image reference")
    details = json.loads(subprocess.check_output(["docker", "image", "inspect", image]))[0]
    image_id = details["Id"]
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", image_id):
        raise ValueError("image does not have an immutable SHA-256 identity")
    destination = root / "out/container-sources"
    archive_path = destination / ("backend-" + image_id.removeprefix("sha256:") + ".tar")
    if archive_path.exists():
        raise ValueError(f"source archive already exists: {archive_path}")
    output = subprocess.check_output([
        "docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
        "--security-opt", "no-new-privileges", "--entrypoint", "dpkg-query", image_id,
        "-W", "-f=${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n",
    ], text=True)
    rows = [line.split("\t") for line in output.splitlines()]
    if not rows or any(len(row) != 4 or not all(row) for row in rows):
        raise ValueError("image has no complete Debian package/source inventory")
    identities = sorted({(row[2], row[3]) for row in rows})
    cache = root / ".worktree/cache/debian-sources"
    cache.mkdir(parents=True, exist_ok=True)
    with ThreadPoolExecutor(max_workers=4) as pool:
        sources = list(pool.map(lambda identity: collect_source(identity, cache), identities))
    manifest = {
        "image": image_id, "architecture": details["Architecture"], "packages": sorted(rows), "sources": [],
        "scope": "Debian source packages for the installed package database; not final rights clearance, a signed-source verification, or a complete reconstruction of locally modified/extra image files.",
    }
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=destination, delete=False) as temporary:
        temporary_path = Path(temporary.name)
    try:
        with tarfile.open(temporary_path, "w", format=tarfile.GNU_FORMAT) as archive:
            for name, version, files in sources:
                receipt = {"package": name, "version": version, "files": {}}
                for filename, record in sorted(files.items()):
                    member = f"sources/{name}/{version}/{filename}"
                    add_tar_file(archive, member, record["path"].read_bytes(), 0o644)
                    receipt["files"][member] = {"sha256": record["sha256"], "bytes": record["bytes"]}
                manifest["sources"].append(receipt)
            add_tar_file(archive, "manifest.json", (json.dumps(manifest, indent=2) + "\n").encode(), 0o644)
            readme = (
                "Debian backend source companion\n\nImage: " + image_id + "\n\n"
                "manifest.json maps the installed binary packages to exact source versions.\n"
                "sources/ contains their original Debian source descriptors and source files.\n"
                "Keep original package licenses and notices; Braidplane's Apache license\n"
                "covers its packing metadata/instructions, not these third-party sources.\n\n"
                "The copied SOURCE_AVAILABILITY.txt describes the complete image distribution.\n"
                "Its Go source ZIPs are in that image's own license directory, not this Debian\n"
                "companion. Supply this companion with the matching image and recipient\n"
                "source instructions. Local preparation is not publication or rights clearance.\n"
            )
            add_tar_file(archive, "README.txt", readme.encode(), 0o644)
            for filename in ("LICENSE", "NOTICE", "SOURCE_AVAILABILITY.txt"):
                add_tar_file(archive, filename, (root / filename).read_bytes(), 0o644)
        temporary_path.replace(archive_path)
    finally:
        temporary_path.unlink(missing_ok=True)
    receipt = {"archive": archive_path.name, "sha256": digest(archive_path), "bytes": archive_path.stat().st_size,
               "image": image_id, "binary_packages": len(rows), "source_packages": len(sources)}
    archive_path.with_suffix(".json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps(receipt, indent=2))
    return archive_path


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    package_image(parser.parse_args().image)
