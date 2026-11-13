import importlib.util
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('coverage', Path(__file__).with_name('bruno-coverage.py'))
coverage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(coverage)


class BrunoCoverageTest(unittest.TestCase):
    def test_ci_defers_only_existing_live_lifecycle(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / 'collection'
            live = root / 'github-pipelines/lifecycle.bru'
            live.parent.mkdir(parents=True)
            live.touch()
            reports = Path(temp) / 'reports'
            reports.mkdir()
            self.assertEqual(coverage.check(root, reports), 1)
            self.assertEqual(coverage.check(root, reports, ci=True), 0)
            (live.parent / 'new.bru').touch()
            self.assertEqual(coverage.check(root, reports, ci=True), 1)

    def test_full_suite_requires_live_setup_before_starting_local_mutations(self):
        environment = {key: value for key, value in os.environ.items()
                       if not key.startswith('BRAIDPLANE_GITHUB_E2E_')}
        result = subprocess.run(['bash', str(Path(__file__).with_name('run-bruno-all.sh'))],
                                env=environment, capture_output=True, text=True, check=False)
        self.assertEqual(result.returncode, 1)
        self.assertIn('bruno-all requires BRAIDPLANE_GITHUB_E2E_CONFIG', result.stderr)

    def test_actual_results_reject_missing_new_and_skipped_requests(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / 'collection'
            root.mkdir()
            reports = Path(temp) / 'reports'
            reports.mkdir()
            (root / 'folder.bru').touch()
            (root / 'collection.bru').touch()
            (root / 'environments').mkdir()
            (root / 'environments/Worktree.bru').touch()
            first = root / 'first.bru'
            first.touch()
            report = reports / 'run.json'
            result = {'suitename': str(first.with_suffix('')), 'response': {'status': 200}, 'status': 'pass'}
            report.write_text(json.dumps([{'results': [result]}]))
            self.assertEqual(coverage.check(root, reports), 0)
            second = root / 'new.bru'
            second.touch()
            self.assertEqual(coverage.check(root, reports), 1)
            result2 = {'suitename': str(second.with_suffix('')), 'response': {'status': 'skipped'}, 'skipped': True}
            report.write_text(json.dumps([{'results': [result, result2]}]))
            self.assertEqual(coverage.check(root, reports), 1)
            # A skipped duplicate must fail even when the same file passed elsewhere.
            report.write_text(json.dumps([{'results': [result, result2, dict(result2, skipped=False, status='pass', response={'status': 200})]}]))
            self.assertEqual(coverage.check(root, reports), 1)
            result2.update(response={'status': 201}, skipped=False, status='pass')
            report.write_text(json.dumps([{'results': [result, result2]}]))
            self.assertEqual(coverage.check(root, reports), 0)


if __name__ == '__main__':
    unittest.main()
