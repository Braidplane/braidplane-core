import copy
import json
import unittest
from unittest.mock import patch

from scripts.admit_core_ci import REQUIRED, admit


def fixture():
    run = {"id": 123, "run_attempt": 1, "path": ".github/workflows/ci.yml", "event": "push",
           "head_branch": "main", "head_sha": "a" * 40, "head_repository": {"full_name": "Example/core"},
           "status": "completed", "conclusion": "success", "html_url": "https://github.com/Example/core/actions/runs/123"}
    jobs = {"total_count": len(REQUIRED), "jobs": [{"name": name, "status": "completed", "conclusion": "success",
             "labels": ["ubuntu-24.04"], "runner_group_name": "GitHub Actions"} for name in sorted(REQUIRED)]}
    return run, jobs


class AdmitCoreCITest(unittest.TestCase):
    def execute(self, run, jobs):
        with patch("scripts.admit_core_ci.subprocess.check_output", side_effect=[json.dumps(run).encode(), json.dumps(jobs).encode()]):
            return admit("Example/core", "a" * 40, "123", "1")

    def test_exact_successful_run_is_admitted(self):
        receipt = self.execute(*fixture())
        self.assertEqual(receipt["jobs"], sorted(REQUIRED))
        self.assertEqual(receipt["source_sha"], "a" * 40)

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
