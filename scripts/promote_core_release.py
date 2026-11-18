"""Promote a completed RC to a stable release without rebuilding artifacts."""
import argparse
import json
import os
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path

from scripts.core_release_identity import api, bind
from scripts.core_release_version import notes, validate_plan
from scripts.publish_core_release import ASSETS, publish_release, release_tag
from scripts.validate_core_candidate import file_hash
from scripts.verify_core_release import check_archive_manifest, read_json, verify_release


def promote(candidate, platform, checkout, output, env):
    if not re.fullmatch(r"v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*", candidate):
        raise ValueError("promotion requires a pre-v1 RC version")
    if platform not in ("linux/amd64", "linux/arm64"):
        raise ValueError("unsupported promotion platform")
    repository, main = env["GITHUB_REPOSITORY"], env["GITHUB_SHA"]
    if (env.get("CORE_RELEASE_PUBLICATION_AUTHORIZED") != "yes" or env["GITHUB_EVENT_NAME"] != "workflow_dispatch" or
            env["GITHUB_REF"] != "refs/heads/main" or env["GITHUB_WORKFLOW_SHA"] != main or
            env["GITHUB_WORKFLOW_REF"] != repository + "/.github/workflows/core-release-promote.yml@refs/heads/main" or
            env["RUNNER_ENVIRONMENT"] != "github-hosted"):
        raise ValueError("promotion requires the trusted manual main workflow")
    checkout = checkout.resolve(strict=True)
    if checkout != Path(__file__).resolve().parents[1]:
        raise ValueError("promotion tooling must run from its main checkout")
    def git(*args):
        return subprocess.check_output(["git", "-C", str(checkout), *args], text=True).strip()
    if git("rev-parse", "HEAD") != main or git("status", "--porcelain=v1", "--untracked-files=all"):
        raise ValueError("promotion requires clean exact workflow source")
    version = candidate.split("-rc.")[0]
    suffix = platform.replace('/', '-')
    ref = f"core-published/{candidate}/{suffix}"
    obj = api(f"repos/{repository}/git/ref/tags/{ref}")["object"]
    if obj["type"] != "tag":
        raise ValueError("RC must have a completed annotated identity")
    tag = api(f"repos/{repository}/git/tags/{obj['sha']}")
    value = json.loads(tag["message"])
    record, source = value["record"], value["source"]
    if tag["object"]["type"] != "commit" or tag["object"]["sha"] != source:
        raise ValueError("RC tag source differs")
    if (value["repository"] != repository or record["release"]["version"] != candidate or
            record["release"]["platform"] != platform or record["release"]["core_revision"] != source):
        raise ValueError("RC identity differs from requested promotion")
    subprocess.run(["git", "-C", str(checkout), "merge-base", "--is-ancestor", source, main], check=True)
    completed = api(f"repos/{repository}/git/matching-refs/tags/core-published/")
    versions = [m.group(1) for r in completed if (m := re.fullmatch(
        r"refs/tags/core-published/(v0\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-rc\.[1-9][0-9]*)?)/" + suffix, r["ref"]))]
    stable = [v for v in versions if "-rc." not in v]
    latest = max(stable, key=lambda v: tuple(map(int, v[1:].split('.'))), default="v0.0.0")
    if latest not in (value["plan"]["previous"], version):
        raise ValueError("stable baseline advanced; select an RC from the current cycle")
    history = {sha: index for index, sha in enumerate(git("rev-list", "--first-parent", main).splitlines())}
    candidates = []
    for item in completed:
        match = re.fullmatch(r"refs/tags/core-published/(v0\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)-rc\.[1-9][0-9]*)/" + suffix, item["ref"])
        if not match:
            continue
        rc_version = match.group(1)
        rc_obj = item["object"]
        if rc_obj["type"] != "tag":
            raise ValueError("RC must have a completed annotated identity")
        rc_tag = tag if rc_version == candidate and rc_obj["sha"] == obj["sha"] else api(f"repos/{repository}/git/tags/{rc_obj['sha']}")
        rc_value = json.loads(rc_tag["message"])
        rc_release = rc_value["record"]["release"]
        if (rc_tag["object"]["type"] != "commit" or rc_tag["object"]["sha"] != rc_value["source"] or
                rc_value["repository"] != repository or rc_release["version"] != rc_version or
                rc_release["platform"] != platform or rc_release["core_revision"] != rc_value["source"]):
            raise ValueError("completed RC identity differs from its ref")
        if rc_value["plan"]["previous"] != value["plan"]["previous"]:
            continue
        if rc_value["source"] not in history:
            raise ValueError("completed RC source is not on main first-parent history")
        rc_run = rc_value["run_id"]
        if not isinstance(rc_run, str) or not re.fullmatch(r"[1-9][0-9]*", rc_run):
            raise ValueError("invalid RC signing run")
        date = datetime.fromisoformat(rc_tag["tagger"]["date"].replace("Z", "+00:00"))
        if date.tzinfo is None:
            raise ValueError("RC tagger date must include a timezone")
        candidates.append((-history[rc_value["source"]], date.astimezone(timezone.utc), int(rc_run), rc_version))
    if not candidates or max(candidates)[3] != candidate:
        raise ValueError("select the latest completed RC so no candidate changes are omitted")
    run_id = value["run_id"]
    if not re.fullmatch(r"[1-9][0-9]*", run_id):
        raise ValueError("invalid RC signing run")
    run = api(f"repos/{repository}/actions/runs/{run_id}")
    if (run["status"] != "completed" or run["conclusion"] != "success" or run["head_sha"] != value["workflow_sha"] or
            run["event"] != "workflow_dispatch" or run["path"] != ".github/workflows/core-release.yml" or
            run["head_branch"] != "main"):
        raise ValueError("RC publication run must have succeeded on exact main source")
    output = output.resolve()
    if output.is_relative_to(checkout):
        raise ValueError("promotion output must be outside the checkout")
    output.mkdir(parents=False, exist_ok=False)
    for name in (*ASSETS, "identity.json"):
        subprocess.run(["gh", "release", "download", release_tag(candidate, platform), "--repo", repository,
                        "--pattern", name, "--dir", str(output)], check=True)
    approval = read_json(output / "approval.json")
    if (file_hash(output / "approval.json") != value["approval_sha256"] or
            read_json(output / "record.json") != record or read_json(output / "identity.json") != value or
            read_json(output / "release.json") != record["release"]):
        raise ValueError("downloaded RC assets differ from immutable identity")
    for name in ("release-plan.json", "release-inputs.json", "CHANGELOG.md"):
        if file_hash(output / name) != value["materials"][name] or file_hash(output / name) != approval["metadata_sha256"][name]:
            raise ValueError("RC notes or input receipt differs from immutable identity")
    if read_json(output / "release-plan.json") != value["plan"] or read_json(output / "release-inputs.json") != value["inputs"]:
        raise ValueError("RC plan or inputs differ")
    validate_plan(value["plan"], (output / "CHANGELOG.md").read_text(), candidate)
    check_archive_manifest(output / "installation.tar.gz", record["release"],
                           {name: (output / name).read_bytes() for name in approval["metadata_sha256"]})
    migration = record["migration"]
    expected = dict(record_sha256=approval["record_sha256"], repository=repository, workflow=".github/workflows/core-release.yml",
                    source_sha=source, signer_sha=value["workflow_sha"], run_id=run_id, attempt=str(run["run_attempt"]), version=candidate,
                    platform=platform, migration_version=migration["version"], migration_digest=migration["digest"])
    verify_release(output / "record.json", output / "installation.tar.gz", expected, output / "verification", Path(env["DOCKER_CONFIG"]))
    promoted = dict(value, promotion=dict(version=version, candidate=candidate))
    (output / "promotion.json").write_text(json.dumps(promoted, indent=2, sort_keys=True) + "\n")
    bind(repository, version, source, json.dumps(dict(source=source, version=version, candidate=candidate), sort_keys=True))
    if platform == "linux/arm64":
        bind(repository, release_tag(version, platform), source,
             json.dumps(dict(source=source, version=version, candidate=candidate), sort_keys=True))
    body = notes(dict(value["plan"], version=version)) + f"\nPromoted from `{candidate}`. Artifacts retain their signed RC identity and unchanged digests.\n"
    publish_release(repository, release_tag(version, platform), source, body, [output / name for name in (*ASSETS, "identity.json", "promotion.json")], False)
    bind(repository, f"core-published/{version}/{suffix}", source, json.dumps(promoted, sort_keys=True, separators=(",", ":")))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("candidate")
    parser.add_argument("platform")
    parser.add_argument("checkout", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        promote(args.candidate, args.platform, args.checkout, args.output, os.environ)
    except (OSError, ValueError, TypeError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"stable promotion failed: {error}\n")


if __name__ == "__main__":
    main()
