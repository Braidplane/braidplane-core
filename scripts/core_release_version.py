"""Select pre-v1 versions and bind their generated release notes to candidates."""
import re


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
    if (selected is None or not isinstance(version, str) or
            not re.fullmatch(re.escape(selected) + r"(?:-rc\.[1-9][0-9]*)?", version) or plan["version"] != version):
        raise ValueError("release version differs from reviewed selection")
    if type(plan["security"]) is not bool or not isinstance(plan["supersedes"], list):
        raise ValueError("invalid release relationship")
    for old in plan["supersedes"]:
        if not isinstance(old, str) or not re.fullmatch(r"v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", old):
            raise ValueError("invalid superseded version")
        if tuple(map(int, old[1:].split('.'))) >= tuple(map(int, selected[1:].split('.'))):
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
    for change in plan["changes"]:
        marker = "**Breaking change:** " if change["kind"] == "breaking" else ""
        lines.append(f"- {marker}{change['summary']}")
    return "\n".join(lines) + "\n"


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
