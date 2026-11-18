"""Select release metadata from merged labels and dispatch candidate preparation."""
import argparse
import json
import os
import re
import subprocess
from pathlib import Path

from scripts.admit_core_ci import admit, require_main
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


def release_metadata(checkout, repository, source, platform=None, *, read_source=None):
    if read_source is None:
        read_source = lambda name: (checkout / name).read_text()
    refs = api(f"repos/{repository}/git/matching-refs/tags/v")
    versions = [ref["ref"].removeprefix("refs/tags/") for ref in refs]
    completed = api(f"repos/{repository}/git/matching-refs/tags/core-published/")
    pattern = r"refs/tags/core-published/(v0\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-rc\.[1-9][0-9]*)?)/linux-(?:amd64|arm64)"
    completed = [(ref, match.group(1)) for ref in completed if (match := re.fullmatch(pattern, ref["ref"]))]
    start = json.loads(read_source("ci/core-release-start.json"))["source"]
    previous = "v0.0.0"

    def identity(ref, version):
        if ref["object"]["type"] != "tag":
            raise ValueError("published release cursor must be an annotated identity tag")
        tag = api(f"repos/{repository}/git/tags/{ref['object']['sha']}")
        value = json.loads(tag["message"])
        source_sha = value["source"]
        release_version = value["record"]["release"]["version"]
        promotion = value.get("promotion")
        valid_version = (release_version == version if promotion is None else
                         promotion == {"version": version, "candidate": release_version} and
                         re.fullmatch(re.escape(version) + r"-rc\.[1-9][0-9]*", release_version))
        if (tag["object"]["type"] != "commit" or tag["object"]["sha"] != source_sha or value["repository"] != repository or
                value["record"]["release"]["core_revision"] != source_sha or not valid_version):
            raise ValueError("published release identity differs from its source")
        return value

    stable = [(ref, version) for ref, version in completed if "-rc." not in version]
    if stable:
        ref, previous = max(stable, key=lambda item: tuple(map(int, item[1][1:].split('.'))))
        start = identity(ref, previous)["source"]
    if not isinstance(start, str) or not re.fullmatch(r"[0-9a-f]{40}", start):
        raise ValueError("invalid published release source cursor")
    pulls = collect(repository, checkout, start, source)
    plan = release_plan(previous, pulls)
    if plan is None:
        return None
    inputs = dict(since_sha=start, through_sha=source, pull_requests=[dict(number=p["number"], title=p["title"], merge_sha=p["merge_commit_sha"],
                    labels=sorted(label["name"] for label in p["labels"] if label["name"].startswith("release:"))) for p in pulls])
    target = plan["version"]
    candidates = [(ref, version) for ref, version in completed if re.fullmatch(re.escape(target) + r"-rc\.[1-9][0-9]*", version)]
    if candidates:
        history = {sha: index for index, sha in enumerate(git(checkout, "rev-list", "--first-parent", source).splitlines())}
        ranked = []
        for ref, version in candidates:
            value = identity(ref, version)
            prior_source = value["source"]
            if prior_source not in history:
                try:
                    subprocess.run(["git", "-C", str(checkout), "merge-base", "--is-ancestor", source, prior_source], check=True)
                except subprocess.CalledProcessError as error:
                    raise ValueError("completed RC source is outside candidate history") from error
                return None
            ranked.append((-history[prior_source], int(version.split('-rc.')[1]), value))
        prior_value = max(ranked, key=lambda item: item[:2])[2]
        prior = prior_value["inputs"]
        eligible = lambda items: sorted((item for item in items if "release:none" not in item["labels"]), key=lambda item: item["number"])
        if prior["since_sha"] == start and eligible(prior["pull_requests"]) == eligible(inputs["pull_requests"]):
            if platform is None or any(value["record"]["release"].get("platform") == platform
                                       and value["source"] == prior_value["source"]
                                       and value["record"]["release"]["version"] == prior_value["record"]["release"]["version"]
                                       for _, _, value in ranked):
                return None
            if prior_value["source"] != source:
                raise ValueError("missing platform requires the exact sibling candidate source")
            plan = prior_value["plan"]
            return {"release-plan.json": json.dumps(plan, indent=2) + "\n",
                    "release-inputs.json": json.dumps(prior, indent=2) + "\n",
                    "CHANGELOG.md": with_notes(read_source("CHANGELOG.md"), plan)}
    numbers = [int(match.group(1)) for version in versions
               if (match := re.fullmatch(re.escape(target) + r"-rc\.([1-9][0-9]*)", version))]
    plan["version"] = f"{target}-rc.{max(numbers, default=0) + 1}"
    completed_versions = {version for _, version in completed}
    for ref in refs:
        version = ref["ref"].removeprefix("refs/tags/")
        if version in completed_versions or not re.fullmatch(re.escape(target) + r"-rc\.[1-9][0-9]*", version) or "object" not in ref:
            continue
        tag = api(f"repos/{repository}/git/tags/{ref['object']['sha']}")
        if (tag["object"]["type"] == "commit" and tag["object"]["sha"] == source and
                json.loads(tag["message"]) == dict(source=source, version=version)):
            plan["version"] = version
    return {"release-plan.json": json.dumps(plan, indent=2) + "\n",
            "release-inputs.json": json.dumps(inputs, indent=2) + "\n",
            "CHANGELOG.md": with_notes(read_source("CHANGELOG.md"), plan)}


def prepare(checkout, env, event):
    repository, workflow_sha = env["GITHUB_REPOSITORY"], env["GITHUB_SHA"]
    run = event["workflow_run"]
    source = run["head_sha"]
    if (env["GITHUB_EVENT_NAME"] != "workflow_run" or env["GITHUB_REF"] != "refs/heads/main" or
            env["GITHUB_WORKFLOW_SHA"] != workflow_sha or env["RUNNER_ENVIRONMENT"] != "github-hosted" or
            env["GITHUB_WORKFLOW_REF"] != repository + "/.github/workflows/core-release-plan.yml@refs/heads/main"):
        raise ValueError("automatic preparation requires trusted main workflow code")
    admit(repository, source, str(run["id"]), str(run["run_attempt"]))
    require_main(repository, source, checkout)
    if git(checkout, "rev-parse", "HEAD") != workflow_sha or git(checkout, "status", "--porcelain=v1", "--untracked-files=all"):
        raise ValueError("automatic preparation requires clean workflow source")

    def read_source(name):
        return subprocess.check_output(["git", "-C", str(checkout), "show", f"{source}:{name}"]).decode()

    config = json.loads(read_source("ci/core-release.json"))
    if (set(config) != {"platform", "platforms", "edge", "postgres"} or
            config["platforms"] != ["linux/amd64", "linux/arm64"] or config["platform"] not in config["platforms"]):
        raise ValueError("invalid reviewed release configuration")
    placeholders = {name: {"containerimage.digest": "sha256:" + "0" * 64} for name in BINARIES}
    candidate(repository.lower(), source, "v0.0.1", config["platform"], config["edge"], config["postgres"], placeholders)
    metadata = {platform: release_metadata(checkout, repository, source, platform, read_source=read_source) for platform in config["platforms"]}
    if all(value is None for value in metadata.values()):
        print("No release: no eligible merged changes.")
        return

    endpoint = f"repos/{repository}/actions/workflows/core-release.yml/runs?per_page=100"
    raw = subprocess.check_output(["gh", "api", endpoint, "--paginate", "--slurp"])
    titles = {item["display_title"] for page in json.loads(raw) for item in page["workflow_runs"]
              if item["event"] == "workflow_dispatch" and item["head_branch"] == "main"}
    inputs = {key: config[key] for key in ("platform", "edge", "postgres")}
    inputs.update(source_sha=source, ci_run_id=str(run["id"]), ci_attempt=str(run["run_attempt"]))
    for platform in config["platforms"]:
        if metadata[platform] is None:
            continue
        if f"Core release candidate ({platform}, {source})" in titles:
            print(f"Candidate run already exists for {platform}; inspect or retry that run.")
            continue
        api(f"repos/{repository}/actions/workflows/core-release.yml/dispatches",
            {"ref": "main", "inputs": dict(inputs, platform=platform)})
        plan = json.loads(metadata[platform]["release-plan.json"])
        print(f"Dispatched {plan['version']} candidate for {source} on {platform}; publication follows the automatic v0 policy.")


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
