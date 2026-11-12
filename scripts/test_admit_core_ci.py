import copy
import json
import re
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.admit_core_ci import REQUIRED, admit


def fixture():
    run = {"id": 123, "run_attempt": 1, "path": ".github/workflows/ci.yml", "event": "push",
           "head_branch": "main", "head_sha": "a" * 40, "head_repository": {"full_name": "Example/core"},
           "status": "completed", "conclusion": "success", "html_url": "https://github.com/Example/core/actions/runs/123"}
    names = {"verify", "docs", "browser", "community-gate",
             "go-e2e (0)", "go-e2e (1)", "go-e2e (2)", "go-e2e (3)",
             "containers (backend)", "containers (frontend)", "containers (migrate)", "containers (cli)"}
    jobs = {"total_count": len(names), "jobs": [{"name": name, "status": "completed", "conclusion": "success",
             "labels": ["ubuntu-24.04"], "runner_group_name": "GitHub Actions"} for name in sorted(names)]}
    return run, jobs


class AdmitCoreCITest(unittest.TestCase):
    def execute(self, run, jobs):
        with patch("scripts.admit_core_ci.subprocess.check_output", side_effect=[json.dumps(run).encode(), json.dumps(jobs).encode()]):
            return admit("Example/core", "a" * 40, "123", "1")

    def test_exact_successful_run_is_admitted(self):
        run, jobs = fixture()
        receipt = self.execute(run, jobs)
        self.assertEqual(receipt["jobs"], sorted(job["name"] for job in jobs["jobs"]))
        self.assertEqual(receipt["source_sha"], "a" * 40)

    def test_release_requirements_track_workflow_shards(self):
        workflow = Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml"
        matrices = re.findall(r"^\s+shard:\s*(\[[^\n]+\])", workflow.read_text(), re.MULTILINE)
        self.assertEqual(len(matrices), 1)
        shards = json.loads(matrices[0])
        self.assertEqual({name for name in REQUIRED if name.startswith("go-e2e")},
                         {f"go-e2e ({shard})" for shard in shards})

    def test_each_e2e_shard_must_succeed(self):
        run, jobs = fixture()
        for shard in range(4):
            name = f"go-e2e ({shard})"
            for condition in ("missing", "failure", "skipped"):
                changed = copy.deepcopy(jobs)
                if condition == "missing":
                    changed["jobs"] = [job for job in changed["jobs"] if job["name"] != name]
                    changed["total_count"] = len(changed["jobs"])
                else:
                    next(job for job in changed["jobs"] if job["name"] == name)["conclusion"] = condition
                with self.subTest(shard=shard, condition=condition), self.assertRaises(ValueError):
                    self.execute(run, changed)

    def test_legacy_aggregate_cannot_replace_e2e_shards(self):
        run, jobs = fixture()
        legacy = copy.deepcopy(jobs)
        aggregate = copy.deepcopy(next(job for job in legacy["jobs"] if job["name"] == "go-e2e (0)"))
        aggregate["name"] = "go-e2e"
        legacy["jobs"] = [job for job in legacy["jobs"] if not job["name"].startswith("go-e2e")]
        legacy["jobs"].append(aggregate)
        legacy["total_count"] = len(legacy["jobs"])
        with self.assertRaises(ValueError):
            self.execute(run, legacy)

    def test_wrong_source_event_attempt_or_status_fails(self):
        run, jobs = fixture()
        for key, value in (("head_sha", "b" * 40), ("event", "pull_request"), ("run_attempt", 2),
                           ("path", ".github/workflows/other.yml"), ("conclusion", "failure")):
            changed = copy.deepcopy(run)
            changed[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.execute(changed, jobs)

    def test_missing_skipped_duplicate_or_private_runner_fails(self):
        run, jobs = fixture()
        for change in ("missing", "skipped", "duplicate", "runner"):
            changed = copy.deepcopy(jobs)
            if change == "missing":
                changed["jobs"].pop()
            elif change == "skipped":
                changed["jobs"][0]["conclusion"] = "skipped"
            elif change == "duplicate":
                changed["jobs"][0]["name"] = changed["jobs"][1]["name"]
            else:
                changed["jobs"][0]["runner_group_name"] = "Private runners"
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.execute(run, changed)


if __name__ == "__main__":
    unittest.main()
