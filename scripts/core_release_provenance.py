#!/usr/bin/env python3
"""Prepare the Core release provenance predicate in its trusted Actions context."""
import argparse
import json
import os
import re
from pathlib import Path

from scripts.installation.release import read_manifest


def predicate(release, env, event):
    repository = env["GITHUB_REPOSITORY"]
    workflow_ref = env["GITHUB_WORKFLOW_REF"]
    prefix = repository + "/"
    suffix = "@refs/heads/main"
    if (not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository) or
            not workflow_ref.startswith(prefix) or not workflow_ref.endswith(suffix)):
        raise ValueError("invalid release workflow identity")
    workflow = workflow_ref[len(prefix):-len(suffix)]
    sha = env["GITHUB_WORKFLOW_SHA"]
    run_id, attempt = env["GITHUB_RUN_ID"], env["GITHUB_RUN_ATTEMPT"]
    repository_id, owner_id = env["GITHUB_REPOSITORY_ID"], env["GITHUB_REPOSITORY_OWNER_ID"]
    if (not re.fullmatch(r"\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml", workflow) or
            not re.fullmatch(r"[0-9a-f]{40}", sha) or env["GITHUB_SHA"] != sha or
            release["core_revision"] != sha or env["GITHUB_REF"] != "refs/heads/main" or
            env["GITHUB_EVENT_NAME"] != "workflow_dispatch" or
            env["RUNNER_ENVIRONMENT"] != "github-hosted" or
            not re.fullmatch(r"[1-9][0-9]*", repository_id) or not re.fullmatch(r"[1-9][0-9]*", owner_id) or
            not re.fullmatch(r"[1-9][0-9]*", run_id) or not re.fullmatch(r"[1-9][0-9]*", attempt) or
            event["repository"]["full_name"] != repository or event["repository"]["visibility"] != "public"):
        raise ValueError("release requires the reviewed public main workflow and source revision")
    url = f"https://github.com/{repository}/{workflow}"
    return {
        "buildDefinition": {
            "buildType": "https://actions.github.io/buildtypes/workflow/v1",
            "externalParameters": {"workflow": {"repository": "https://github.com/" + repository,
                                                "path": workflow, "ref": "refs/heads/main"}},
            "internalParameters": {"github": {"event_name": "workflow_dispatch", "repository_id": repository_id,
                                              "repository_owner_id": owner_id, "runner_environment": "github-hosted"},
                                   "release": {"version": release["version"], "platform": release["platform"]}},
            "resolvedDependencies": [{"uri": f"git+https://github.com/{repository}@refs/heads/main",
                                      "digest": {"gitCommit": sha}}],
        },
        "runDetails": {"builder": {"id": url + "@refs/heads/main"},
                       "metadata": {"invocationId": f"https://github.com/{repository}/actions/runs/{run_id}/attempts/{attempt}"}},
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        release = read_manifest(args.manifest)
        event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
        data = json.dumps(predicate(release, os.environ, event), indent=2) + "\n"
        with args.output.open("x") as output:
            output.write(data)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(1, f"provenance preparation failed: {error}\n")


if __name__ == "__main__":
    main()
