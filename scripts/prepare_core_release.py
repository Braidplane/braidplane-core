"""Select release metadata from merged labels and dispatch candidate preparation."""
import argparse
import json
import os
import re
import subprocess
from pathlib import Path

from scripts.admit_core_ci import admit
from scripts.core_release_candidate import candidate
from scripts.core_release_identity import api
from scripts.qualify_core_images import BINARIES
from scripts.core_release_version import select, with_notes
from scripts.verify_core_release import read_json

LABELS = {"release:patch": "fix", "release:minor": "feature", "release:breaking": "breaking", "release:none": "docs"}


def pages(path):
    raw = subprocess.check_output(["gh", "api", path, "--paginate", "--slurp"])
    return [item for page in json.loads(raw) for item in page]


def git(checkout, *args):
    return subprocess.check_output(["git", "-C", str(checkout), *args], text=True).strip()


def classified(pulls):
    changes, security = [], False
    for pull in pulls:
        labels = {label["name"] for label in pull["labels"]}
        unknown = {label for label in labels if label.startswith("release:")} - LABELS.keys() - {"release:security"}
        if unknown:
            raise ValueError(f"unsupported release labels on PR #{pull['number']}: {sorted(unknown)}")
        selected = labels & LABELS.keys()
        if len(selected) != 1:
            raise ValueError(f"PR #{pull['number']} requires exactly one release:patch/minor/breaking/none label")
        label = selected.pop()
        if label == "release:none":
            if "release:security" in labels:
                raise ValueError("a security release cannot be classified release:none")
            continue
        title = pull["title"]
        if not isinstance(title, str) or not title.strip() or any(char in title for char in "\r\n"):
            raise ValueError("release notes require single-line PR titles")
        changes.append(dict(kind=LABELS[label], summary=f"{title} (#{pull['number']})"))
        security |= "release:security" in labels
    return changes, security


def release_plan(previous, pulls):
    changes, security = classified(pulls)
    if not changes:
        return None
    bump = "minor" if any(change["kind"] in ("feature", "breaking") for change in changes) else "patch"
    version = select(previous, bump, changes)
    return dict(previous=previous, bump=bump, version=version, changes=changes,
                security=security, supersedes=[previous] if security and previous != "v0.0.0" else [])


def collect(repository, checkout, start, source):
    subprocess.run(["git", "-C", str(checkout), "merge-base", "--is-ancestor", start, source], check=True)
    commits = git(checkout, "rev-list", "--first-parent", "--reverse", f"{start}..{source}").splitlines()
    pulls = {}
    for sha in commits:
        associated = pages(f"repos/{repository}/commits/{sha}/pulls?per_page=100")
        merged = [p for p in associated if p.get("merged_at") and p["base"]["ref"] == "main" and
                  p["base"]["repo"]["full_name"] == repository and p.get("merge_commit_sha") in commits]
        if len(merged) != 1:
            raise ValueError(f"commit {sha} needs one merged main PR; direct commits require operator reconciliation")
        pull = merged[0]
        pulls[pull["number"]] = pull
    return list(pulls.values())


def release_metadata(checkout, repository, source):
    versions = api(f"repos/{repository}/git/matching-refs/tags/v")
    versions = [ref["ref"].removeprefix("refs/tags/") for ref in versions
                if re.fullmatch(r"refs/tags/v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", ref["ref"])]
    previous = max(versions, key=lambda v: tuple(map(int, v[1:].split('.')))) if versions else "v0.0.0"
    completed = api(f"repos/{repository}/git/matching-refs/tags/core-published/")
    completed = [ref for ref in completed if re.fullmatch(r"refs/tags/core-published/v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)/linux-(amd64|arm64)", ref["ref"])]
    start = read_json(checkout / "ci/core-release-start.json")["source"]
    published_version = "v0.0.0"
    if completed:
        latest = max(completed, key=lambda ref: tuple(map(int, ref["ref"].split('/')[-2][1:].split('.'))))
        if latest["object"]["type"] != "tag":
            raise ValueError("published release cursor must be an annotated identity tag")
        tag = api(f"repos/{repository}/git/tags/{latest['object']['sha']}")
        identity = json.loads(tag["message"])
        start, published_version = identity["source"], identity["record"]["release"]["version"]
        if (tag["object"]["type"] != "commit" or tag["object"]["sha"] != start or identity["repository"] != repository or
                identity["record"]["release"]["core_revision"] != start or published_version != latest["ref"].split('/')[-2]):
            raise ValueError("published release identity differs from its source")
    if not isinstance(start, str) or not re.fullmatch(r"[0-9a-f]{40}", start):
        raise ValueError("invalid published release source cursor")
    pulls = collect(repository, checkout, start, source)
    plan = release_plan(previous, pulls)
    if plan is None:
        return None
    if previous != published_version and previous != "v0.0.0":
        plan["supersedes"] = sorted(set(plan["supersedes"] + [previous]))
    inputs = dict(since_sha=start, through_sha=source, pull_requests=[dict(number=p["number"], title=p["title"], merge_sha=p["merge_commit_sha"],
                    labels=sorted(label["name"] for label in p["labels"] if label["name"].startswith("release:"))) for p in pulls])
    return {"release-plan.json": json.dumps(plan, indent=2) + "\n",
            "release-inputs.json": json.dumps(inputs, indent=2) + "\n",
            "CHANGELOG.md": with_notes((checkout / "CHANGELOG.md").read_text(), plan)}


def prepare(checkout, env, event):
    repository, source = env["GITHUB_REPOSITORY"], env["GITHUB_SHA"]
    run = event["workflow_run"]
    if (env["GITHUB_EVENT_NAME"] != "workflow_run" or env["GITHUB_REF"] != "refs/heads/main" or
            env["GITHUB_WORKFLOW_SHA"] != source or env["RUNNER_ENVIRONMENT"] != "github-hosted" or
            env["GITHUB_WORKFLOW_REF"] != repository + "/.github/workflows/core-release-plan.yml@refs/heads/main"):
        raise ValueError("automatic preparation requires trusted main workflow code")
    if run["head_sha"] != source or api(f"repos/{repository}/git/ref/heads/main")["object"]["sha"] != source:
        print("Main advanced; wait for successful CI on current main.")
        return
    admit(repository, source, str(run["id"]), str(run["run_attempt"]))
    if git(checkout, "rev-parse", "HEAD") != source or git(checkout, "status", "--porcelain=v1", "--untracked-files=all"):
        raise ValueError("automatic preparation requires clean exact source")
    config = read_json(checkout / "ci/core-release.json")
    if set(config) != {"platform", "edge", "postgres"}:
        raise ValueError("invalid reviewed release configuration")
    placeholders = {name: {"containerimage.digest": "sha256:" + "0" * 64} for name in BINARIES}
    candidate(repository.lower(), source, "v0.0.1", config["platform"], config["edge"], config["postgres"], placeholders)
    metadata = release_metadata(checkout, repository, source)
    if metadata is None:
        print("No release: no eligible merged changes.")
        return
    runs = api(f"repos/{repository}/actions/workflows/core-release.yml/runs?head_sha={source}&event=workflow_dispatch")
    if any(item["head_sha"] == source for item in runs["workflow_runs"]):
        print("Candidate run already exists for this source; inspect or retry that run.")
        return
    plan = json.loads(metadata["release-plan.json"])
    inputs = dict(config, ci_run_id=str(run["id"]), ci_attempt=str(run["run_attempt"]))
    api(f"repos/{repository}/actions/workflows/core-release.yml/dispatches", {"ref": "main", "inputs": inputs})
    print(f"Dispatched {plan['version']} candidate for {source}; publication still requires artifact approval.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("checkout", type=Path)
    args = parser.parse_args()
    try:
        prepare(args.checkout, os.environ, read_json(Path(os.environ["GITHUB_EVENT_PATH"])))
    except (OSError, ValueError, TypeError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"automatic release preparation failed: {error}\n")


if __name__ == "__main__":
    main()
