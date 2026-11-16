"""Prepare one reviewable release PR from merged PR labels, without publication."""
import argparse
import base64
import hashlib
import json
import os
import re
import subprocess
from pathlib import Path

from scripts.core_release_identity import api
from scripts.core_release_version import notes, select, validate_plan, with_notes
from scripts.verify_core_release import read_json

BRANCH_PREFIX = "automation/core-release/"
LABELS = {"release:patch": "fix", "release:minor": "feature", "release:breaking": "breaking", "release:none": "docs"}
FILES = {"CHANGELOG.md", "release-plan.json", "release-inputs.json"}


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
        if pull["head"]["ref"].startswith(BRANCH_PREFIX) and pull["head"]["repo"]["full_name"] == repository:
            files = pages(f"repos/{repository}/pulls/{pull['number']}/files?per_page=100")
            if {f["filename"] for f in files} != FILES:
                raise ValueError("generated release PR contains unexpected files")
            continue
        pulls[pull["number"]] = pull
    return list(pulls.values())


def ensure_ci(repository, branch, sha):
    runs = api(f"repos/{repository}/actions/workflows/ci.yml/runs?head_sha={sha}&event=workflow_dispatch")
    if not any(run["head_sha"] == sha for run in runs["workflow_runs"]):
        api(f"repos/{repository}/actions/workflows/ci.yml/dispatches", {"ref": branch})


def contents(repository, sha, name):
    value = api(f"repos/{repository}/contents/{name}?ref={sha}")
    if value["encoding"] != "base64":
        raise ValueError("unexpected release metadata encoding")
    return base64.b64decode(value["content"]).decode()


def serialized(plan, inputs, changelog):
    return {"release-plan.json": json.dumps(plan, indent=2) + "\n",
            "release-inputs.json": json.dumps(inputs, indent=2) + "\n", "CHANGELOG.md": changelog}


def commit_message(plan, files):
    digest = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()
    return f"chore: prepare Core {plan['version']}\n\nRelease-plan-sha256: {digest}"


def check_branch(repository, checkout, sha):
    files = {name: contents(repository, sha, name) for name in FILES}
    plan, inputs = json.loads(files["release-plan.json"]), json.loads(files["release-inputs.json"])
    validate_plan(plan, files["CHANGELOG.md"], plan["version"])
    through = inputs["through_sha"]
    if not isinstance(through, str) or not re.fullmatch(r"[0-9a-f]{40}", through):
        raise ValueError("invalid release branch source snapshot")
    subprocess.run(["git", "-C", str(checkout), "merge-base", "--is-ancestor", through, "HEAD"], check=True)
    commit = api(f"repos/{repository}/git/commits/{sha}")
    compare = api(f"repos/{repository}/compare/{through}...{sha}")
    if commit["message"] != commit_message(plan, files) or {f["filename"] for f in compare["files"]} != FILES:
        raise ValueError("release branch has reviewer edits; preserve and reconcile them before refreshing")


def without_plan(changelog, plan):
    validate_plan(plan, changelog, plan["version"])
    section = notes(plan)
    return changelog.replace(section, "", 1)


def publish_pr(repository, branch, sha, version, existing=None):
    pull = existing or api(f"repos/{repository}/pulls", dict(base="main", head=branch,
        title=f"chore: release Core {version}",
        body="Prepare the reviewed Core release plan and changelog.\n\n"
             "Review `release-inputs.json` for the included source snapshot and PR classifications. "
             "New main merges refresh this PR until its plan is merged; refreshed versions and notes need review again. "
             "The bot refuses to overwrite reviewer edits. A stale merged plan cannot be published.\n\n"
             "Merge does not publish artifacts. Use the separately approved Core release workflow after main CI passes.\n\n"
             "Generated from merged PR metadata by the trusted main release-preparation workflow."))
    title = f"chore: release Core {version}"
    if existing and existing["title"] != title:
        api(f"repos/{repository}/pulls/{pull['number']}", {"title": title}, method="PATCH")
    api(f"repos/{repository}/issues/{pull['number']}/labels", {"labels": ["release:none"]})
    ensure_ci(repository, branch, sha)
    return pull


def create_pr(repository, source, plan, inputs, changelog, existing=None):
    files = serialized(plan, inputs, changelog)
    fingerprint = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()[:12]
    branch = existing["head"]["ref"] if existing else BRANCH_PREFIX + source[:12] + "-" + fingerprint
    parent = api(f"repos/{repository}/git/commits/{source}")
    tree = api(f"repos/{repository}/git/trees", {"base_tree": parent["tree"]["sha"], "tree": [
        dict(path=name, mode="100644", type="blob", content=value) for name, value in files.items()]})
    parents = [source]
    if existing:
        head = existing["head"]["sha"]
        commit = api(f"repos/{repository}/git/commits/{head}")
        if commit["tree"]["sha"] == tree["sha"]:
            return publish_pr(repository, branch, head, plan["version"], existing)
        # Including the old head permits a fast-forward, and refuses concurrent reviewer commits.
        parents = [head, source]
    if not existing:
        refs = api(f"repos/{repository}/git/matching-refs/heads/{branch}")
        exact = [ref for ref in refs if ref["ref"] == "refs/heads/" + branch]
        if exact:
            commit = api(f"repos/{repository}/git/commits/{exact[0]['object']['sha']}")
            if commit["tree"]["sha"] != tree["sha"]:
                raise ValueError("release branch has reviewer edits; preserve them before retrying")
            previous = pages(f"repos/{repository}/pulls?state=all&head={repository.split('/')[0]}:{branch}&per_page=100")
            if previous:
                raise ValueError("existing release PR requires operator reconciliation")
            return publish_pr(repository, branch, commit["sha"], plan["version"])
    commit = api(f"repos/{repository}/git/commits", dict(message=commit_message(plan, files),
                                                       tree=tree["sha"], parents=parents))
    if existing:
        api(f"repos/{repository}/git/refs/heads/{branch}", dict(sha=commit["sha"], force=False), method="PATCH")
    else:
        api(f"repos/{repository}/git/refs", dict(ref="refs/heads/" + branch, sha=commit["sha"]))
    return publish_pr(repository, branch, commit["sha"], plan["version"], existing)


def prepare(checkout, env):
    repository, source = env["GITHUB_REPOSITORY"], env["GITHUB_SHA"]
    if (env["GITHUB_REF"] != "refs/heads/main" or env["GITHUB_EVENT_NAME"] not in ("push", "workflow_dispatch") or
            not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository) or not re.fullmatch(r"[0-9a-f]{40}", source) or
            env["GITHUB_WORKFLOW_SHA"] != source or env["RUNNER_ENVIRONMENT"] != "github-hosted" or
            env["GITHUB_WORKFLOW_REF"] != repository + "/.github/workflows/core-release-plan.yml@refs/heads/main"):
        raise ValueError("release preparation requires reviewed main source on a hosted runner")
    if git(checkout, "rev-parse", "HEAD") != source or git(checkout, "status", "--porcelain=v1", "--untracked-files=all"):
        raise ValueError("release preparation requires clean exact source")
    # Stale queued push jobs defer to the current main job, never branch from an older main.
    if api(f"repos/{repository}/git/ref/heads/main")["object"]["sha"] != source:
        print("Main advanced; defer to its preparation run.")
        return
    open_pulls = pages(f"repos/{repository}/pulls?state=open&base=main&per_page=100")
    pending = [p for p in open_pulls if p["head"]["ref"].startswith(BRANCH_PREFIX) and
               p["head"]["repo"]["full_name"] == repository]
    existing = pending[0] if len(pending) == 1 else None
    if len(pending) > 1:
        raise ValueError("multiple release PRs require operator reconciliation")
    if existing:
        check_branch(repository, checkout, existing["head"]["sha"])
    ledger = checkout / "release-inputs.json"
    previous_inputs = read_json(ledger) if ledger.exists() else None
    start = previous_inputs["through_sha"] if previous_inputs else read_json(checkout / "ci/core-release-start.json")["source"]
    if not isinstance(start, str) or not re.fullmatch(r"[0-9a-f]{40}", start):
        raise ValueError("invalid release input cursor")
    refs = api(f"repos/{repository}/git/matching-refs/tags/v")
    versions = [ref["ref"].removeprefix("refs/tags/") for ref in refs
                if re.fullmatch(r"refs/tags/v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", ref["ref"])]
    changelog = (checkout / "CHANGELOG.md").read_text()
    replacing = None
    if (checkout / "release-plan.json").exists():
        current = read_json(checkout / "release-plan.json")
        validate_plan(current, changelog, current["version"])
        if current["version"] not in versions:
            if previous_inputs is None:
                raise ValueError("manual release plan awaits reservation; reconcile it before automatic preparation")
            # Reconcile a stale merged plan rather than ship changes absent from its reviewed input set.
            start = previous_inputs["since_sha"]
            replacing = current
            changelog = without_plan(changelog, current)
    previous = max(versions, key=lambda v: tuple(map(int, v[1:].split('.')))) if versions else "v0.0.0"
    pulls = collect(repository, checkout, start, source)
    plan = release_plan(previous, pulls)
    if plan is None:
        print("No release: no eligible merged changes.")
        return
    if replacing and plan == replacing and previous_inputs["through_sha"] == git(checkout, "rev-parse", "HEAD^"):
        print("Merged release plan awaits its authorized artifact reservation.")
        return
    inputs = dict(since_sha=start, through_sha=source, pull_requests=[dict(number=p["number"], title=p["title"], merge_sha=p["merge_commit_sha"],
                    labels=sorted(label["name"] for label in p["labels"] if label["name"].startswith("release:"))) for p in pulls])
    pull = create_pr(repository, source, plan, inputs, with_notes(changelog, plan), existing)
    print(pull["html_url"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("checkout", type=Path)
    args = parser.parse_args()
    try:
        prepare(args.checkout, os.environ)
    except (OSError, ValueError, TypeError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"release PR preparation failed: {error}\n")


if __name__ == "__main__":
    main()
