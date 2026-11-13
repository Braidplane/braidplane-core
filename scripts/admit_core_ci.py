#!/usr/bin/env python3
"""Admit one successful Core main CI run for an exact release revision."""
import argparse
import json
import re
import subprocess
from pathlib import Path


REQUIRED = {"verify", "go-e2e (0)", "go-e2e (1)", "go-e2e (2)", "go-e2e (3)",
            "docs", "browser", "bruno", "community-gate", "containers"}


def admit(repository, sha, run_id, attempt):
    if (not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository) or
            not re.fullmatch(r"[0-9a-f]{40}", sha) or
            not re.fullmatch(r"[1-9][0-9]*", run_id) or not re.fullmatch(r"[1-9][0-9]*", attempt)):
        raise ValueError("invalid CI run identity")
    endpoint = f"repos/{repository}/actions/runs/{run_id}"
    run = json.loads(subprocess.check_output(["gh", "api", endpoint]))
    if (run.get("id") != int(run_id) or run.get("run_attempt") != int(attempt) or
            run.get("path") != ".github/workflows/ci.yml" or run.get("event") != "push" or
            run.get("head_branch") != "main" or run.get("head_sha") != sha or
            (run.get("head_repository") or {}).get("full_name") != repository or
            run.get("status") != "completed" or run.get("conclusion") != "success"):
        raise ValueError("CI run is not the successful main push for the approved revision")
    jobs = json.loads(subprocess.check_output(["gh", "api", f"{endpoint}/attempts/{attempt}/jobs?per_page=100"]))
    entries = jobs.get("jobs")
    if jobs.get("total_count") != len(REQUIRED) or not isinstance(entries, list) or len(entries) != len(REQUIRED):
        raise ValueError("CI run does not contain the complete required job set")
    names = [job.get("name") for job in entries]
    if (set(names) != REQUIRED or len(set(names)) != len(names) or
            any(job.get("status") != "completed" or job.get("conclusion") != "success" or
                job.get("labels") != ["ubuntu-24.04"] or job.get("runner_group_name") != "GitHub Actions"
                for job in entries)):
        raise ValueError("required CI jobs must succeed on hosted runners")
    return {"repository": repository, "source_sha": sha, "run_id": int(run_id), "attempt": int(attempt),
            "workflow": ".github/workflows/ci.yml", "jobs": sorted(names), "url": run["html_url"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repository")
    parser.add_argument("source_sha")
    parser.add_argument("run_id")
    parser.add_argument("attempt")
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        result = admit(args.repository, args.source_sha, args.run_id, args.attempt)
        with args.output.open("x") as output:
            json.dump(result, output, indent=2, sort_keys=True)
            output.write("\n")
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"CI admission failed: {error}\n")


if __name__ == "__main__":
    main()
