"""Copy the pinned MPL module source ZIPs into the local executable bundle."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

from cli.scripts.release import go_stdout

ROOT = Path(__file__).resolve().parent.parent
GO = ("bash", "scripts/go-toolchain.sh")
ENTRY = re.compile(r"([0-9a-f]{64})  ([a-z0-9./_-]+)/@v/(v[A-Za-z0-9.+-]+)\.zip")


def source_entries(root):
    entries = []
    seen = set()
    for line in (root / "MPL_SOURCES.sha256").read_text().splitlines():
        match = ENTRY.fullmatch(line)
        if not match:
            raise ValueError("invalid MPL source manifest entry")
        checksum, module, version = match.groups()
        if any(part in ("", ".", "..") for part in module.split("/")) or module in seen:
            raise ValueError("unsafe or duplicate MPL source module")
        seen.add(module)
        entries.append((checksum, f"{module}/@v/{version}.zip", module, version))
    return entries


def verify_mpl_inventory(root, notices):
    sections = re.split(rb"\n(\S+) (v\S+)\n=+\n", notices)
    discovered = {
        (sections[index].decode(), sections[index + 1].decode())
        for index in range(1, len(sections), 3)
        if b"Mozilla Public License" in sections[index + 2]
    }
    pinned = {(module, version) for _, _, module, version in source_entries(root)}
    if discovered != pinned:
        raise ValueError(f"MPL source manifest does not match notice inventory: {sorted(discovered ^ pinned)}")


def package(root=ROOT):
    env = dict(os.environ, GOWORK="off")
    entries = source_entries(root)
    if not entries:
        return
    subprocess.run([*GO, "mod", "verify"], cwd=root, env=env, check=True)
    modules = [module for _, _, module, _ in entries]
    output = go_stdout(root, env, GO, "list", "-m", "-f", "{{.Path}} {{.Version}}{{if .Replace}} replaced{{end}}", *modules)
    if any(len(line.split()) != 2 for line in output.splitlines()):
        raise ValueError("replaced dependencies require a separately reviewed source package")
    selected = dict(line.split() for line in output.splitlines())
    sources = []
    for checksum, relative, module, version in entries:
        if selected.get(module) != version:
            raise ValueError(f"MPL source version differs from selected module: {module}")
        metadata = json.loads(go_stdout(root, env, GO, "mod", "download", "-json", f"{module}@{version}"))
        if metadata.get("Error"):
            raise ValueError(metadata["Error"])
        content = Path(metadata["Zip"]).read_bytes()
        if hashlib.sha256(content).hexdigest() != checksum:
            raise ValueError(f"MPL source ZIP checksum mismatch: {module} {version}")
        sources.append((relative, content))
    destination = root / "out/licenses/sources"
    for relative, content in sources:
        target = destination / relative
        if target.is_symlink() or not target.resolve().is_relative_to((root / "out").resolve()):
            raise ValueError("source output escapes the executable bundle")
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content)


if __name__ == "__main__":
    package()
