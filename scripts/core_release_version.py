"""Prepare and validate a reviewed pre-v1 release plan and changelog."""
import argparse
import json
import re
import subprocess
from pathlib import Path

from scripts.verify_core_release import read_json


def select(previous, bump, changes):
    match = re.fullmatch(r"v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", previous)
    if not match or bump not in ("patch", "minor"):
        raise ValueError("require a stable pre-v1 previous version and patch or minor selection")
    if not changes or any(not isinstance(c, dict) or set(c) != {"kind", "summary"} or
                          c["kind"] not in ("fix", "performance", "feature", "breaking", "docs", "test", "ci") or
                          not isinstance(c["summary"], str) or not c["summary"].strip() or any(char in c["summary"] for char in "\r\n")
                          for c in changes):
        raise ValueError("require classified changes with single-line summaries")
    eligible = [c for c in changes if c["kind"] not in ("docs", "test", "ci")]
    if not eligible:
        return None
    if bump == "patch" and any(c["kind"] in ("feature", "breaking") for c in eligible):
        raise ValueError("features and breaking changes require explicit minor selection before v1")
    minor, patch = map(int, match.groups())
    return f"v0.{minor + 1}.0" if bump == "minor" else f"v0.{minor}.{patch + 1}"


def validate_plan(plan, changelog, version):
    if not isinstance(plan, dict) or set(plan) != {"previous", "bump", "changes", "version", "security", "supersedes"}:
        raise ValueError("invalid reviewed release plan")
    selected = select(plan["previous"], plan["bump"], plan["changes"])
    if selected is None or selected != version or plan["version"] != version:
        raise ValueError("release version differs from reviewed selection")
    if type(plan["security"]) is not bool or not isinstance(plan["supersedes"], list):
        raise ValueError("invalid release relationship")
    for old in plan["supersedes"]:
        if not isinstance(old, str) or not re.fullmatch(r"v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", old):
            raise ValueError("invalid superseded version")
        if tuple(map(int, old[1:].split('.'))) >= tuple(map(int, version[1:].split('.'))):
            raise ValueError("superseded version must be older")
    section = notes(plan)
    actual = changelog.split(f"## {version}\n", 1)[-1].split("\n## ", 1)[0]
    if changelog.count(f"## {version}\n") != 1 or actual.strip() != section.split("\n", 1)[1].strip():
        raise ValueError("changelog differs from reviewed release plan")
    return plan


def notes(plan):
    lines = [f"## {plan['version']}", ""]
    if plan["security"]:
        lines += ["Expedited security release.", ""]
    if plan["supersedes"]:
        lines += ["Supersedes: " + ", ".join(plan["supersedes"]) + ".", ""]
    return "\n".join(lines + [f"- {c['summary']}" for c in plan["changes"]]) + "\n"


def reviewed(checkout, version):
    inputs = checkout / "release-inputs.json"
    if inputs.exists():
        through = read_json(inputs)["through_sha"]
        if not isinstance(through, str) or not re.fullmatch(r"[0-9a-f]{40}", through):
            raise ValueError("invalid release source snapshot")
        subprocess.run(["git", "-C", str(checkout), "merge-base", "--is-ancestor", through, "HEAD"], check=True)
        commits = subprocess.check_output(["git", "-C", str(checkout), "rev-list", "--first-parent", f"{through}..HEAD"], text=True).splitlines()
        changed = subprocess.check_output(["git", "-C", str(checkout), "diff", "--name-only", through, "HEAD"], text=True).splitlines()
        if len(commits) != 1 or set(changed) != {"CHANGELOG.md", "release-plan.json", "release-inputs.json"}:
            raise ValueError("release plan is stale: refresh and review it against current main before building")
    return validate_plan(read_json(checkout / "release-plan.json"), (checkout / "CHANGELOG.md").read_text(), version)


def with_notes(changelog, plan):
    version = plan["version"]
    if f"## {version}\n" in changelog or changelog.count("## Unreleased\n") != 1:
        raise ValueError("changelog needs one Unreleased heading and no existing selected version")
    before, _, pending = changelog.partition("## Unreleased\n")
    unreleased, next_heading, older = pending.partition("\n## ")
    updated = before + "## Unreleased\n" + unreleased.rstrip() + "\n\n" + notes(plan)
    if next_heading:
        updated += "\n## " + older
    validate_plan(plan, updated, version)
    return updated


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("previous")
    parser.add_argument("changes", type=Path, help="JSON array of kind/summary objects")
    parser.add_argument("--bump", choices=("patch", "minor"), default="patch")
    parser.add_argument("--security", action="store_true")
    parser.add_argument("--supersedes", action="append", default=[])
    args = parser.parse_args()
    try:
        changes = read_json(args.changes)
        version = select(args.previous, args.bump, changes)
        if version is None:
            print("No release: documentation, tests and CI only.")
            return
        plan = dict(previous=args.previous, bump=args.bump, changes=changes, version=version,
                    security=args.security, supersedes=args.supersedes)
        path = Path("CHANGELOG.md")
        changelog = path.read_text()
        updated = with_notes(changelog, plan)
        Path("release-plan.json").write_text(json.dumps(plan, indent=2) + "\n")
        path.write_text(updated)
        print(version)
    except (OSError, ValueError, TypeError, KeyError) as error:
        parser.exit(1, f"release selection failed: {error}\n")


if __name__ == "__main__":
    main()
