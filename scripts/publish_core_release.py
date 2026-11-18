"""Publish candidate assets without replacing an existing release or asset."""
import argparse
import json
import os
import re
import subprocess
from pathlib import Path

from scripts.core_release_identity import api, bind
from scripts.core_release_version import notes
from scripts.validate_core_candidate import file_hash
from scripts.verify_core_release import read_json


ASSETS = ("record.json", "installation.tar.gz", "release.json", "release-plan.json",
          "release-inputs.json", "CHANGELOG.md", "approval.json")


def release_tag(version, platform):
    if platform not in ("linux/amd64", "linux/arm64"):
        raise ValueError("unsupported release platform")
    return version if platform == "linux/amd64" else version + "-linux-arm64"


def publish_release(repository, version, source, body, assets, prerelease):
    releases = json.loads(subprocess.check_output(["gh", "api", f"repos/{repository}/releases?per_page=100", "--paginate", "--slurp"]))
    matches = [r for page in releases for r in page if r["tag_name"] == version]
    release = matches[0] if matches else api(f"repos/{repository}/releases", dict(
        tag_name=version, target_commitish=source, name=version, body=body, draft=True, prerelease=prerelease))
    if release["body"] != body or release["prerelease"] != prerelease:
        raise ValueError("existing release metadata differs; never replace it")
    asset_endpoint = f"repos/{repository}/releases/{release['id']}/assets?per_page=100"
    expected = {path.name: "sha256:" + file_hash(path) for path in assets}
    existing = {asset["name"]: asset.get("digest") for asset in api(asset_endpoint)}
    missing = []
    for path in assets:
        if path.name in existing:
            if existing[path.name] != expected[path.name]:
                raise ValueError("existing release asset differs; never replace it")
        elif release["draft"]:
            missing.append(path)
        else:
            raise ValueError("published release is missing a required asset")
    for path in missing:
        subprocess.run(["gh", "release", "upload", version, str(path), "--repo", repository], check=True)
    if release["draft"]:
        if missing:
            uploaded = {asset["name"]: asset.get("digest") for asset in api(asset_endpoint)}
            if any(uploaded.get(name) != digest for name, digest in expected.items()):
                raise ValueError("uploaded release asset differs from the approved digest")
        api(f"repos/{repository}/releases/{release['id']}", dict(draft=False, make_latest="false" if prerelease else "true"), method="PATCH")


def publish_candidate(directory, identity_dir, env):
    value = read_json(identity_dir / "identity.json")
    release = value["record"]["release"]
    version = release["version"]
    if (not re.fullmatch(r"v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*", version) or
            value["source"] != env.get("RELEASE_SOURCE", env["GITHUB_SHA"]) or value["repository"] != env["GITHUB_REPOSITORY"]):
        raise ValueError("prerelease requires this run's verified v0 RC")
    # The completion tag exists only after all ten subjects passed verification.
    ref = f"core-published/{version}/{release['platform'].replace('/', '-')}"
    tag_ref = api(f"repos/{value['repository']}/git/ref/tags/{ref}")
    tag = api(f"repos/{value['repository']}/git/tags/{tag_ref['object']['sha']}")
    if json.loads(tag["message"]) != value:
        raise ValueError("RC completion identity differs")
    if release["platform"] == "linux/arm64":
        bind(value["repository"], release_tag(version, release["platform"]), value["source"],
             json.dumps(dict(source=value["source"], version=version), sort_keys=True))
    publish_release(value["repository"], release_tag(version, release["platform"]), value["source"], notes(value["plan"]),
                    [directory / name for name in ASSETS] + [identity_dir / "identity.json"], True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("candidate", type=Path)
    parser.add_argument("identity", type=Path)
    args = parser.parse_args()
    try:
        publish_candidate(args.candidate, args.identity, os.environ)
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"RC release publication failed: {error}\n")


if __name__ == "__main__":
    main()
