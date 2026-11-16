import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.core_release_version import main, notes, reviewed, select, validate_plan


class ReleaseVersionTest(unittest.TestCase):
    def plan(self, **overrides):
        return dict(previous="v0.2.3", bump="patch", version="v0.2.4", security=False, supersedes=[],
                    changes=[dict(kind="fix", summary="Repair retries.")], **overrides)

    def test_selection_and_no_release(self):
        self.assertEqual(select("v0.2.3", "patch", [dict(kind="fix", summary="Fix")]), "v0.2.4")
        for kind in ("docs", "test", "ci"):
            self.assertIsNone(select("v0.2.3", "patch", [dict(kind=kind, summary="Update")]))
        for kind in ("feature", "breaking"):
            changes = [dict(kind=kind, summary="Change")]
            with self.assertRaisesRegex(ValueError, "explicit minor"):
                select("v0.2.3", "patch", changes)
            self.assertEqual(select("v0.2.3", "minor", changes), "v0.3.0")
        for previous in ("v1.0.0", "v0.01.0", "v0.1.0-rc1", "bad"):
            with self.assertRaises(ValueError):
                select(previous, "patch", [dict(kind="fix", summary="Fix")])

    def test_plan_requires_exact_reviewed_notes_and_relationship(self):
        plan = self.plan()
        plan.update(security=True, supersedes=["v0.2.3"])
        validate_plan(plan, notes(plan), "v0.2.4")
        for changelog in (notes(plan) + "- Unreviewed change\n", notes(plan).replace("Repair", "Other")):
            with self.assertRaises(ValueError):
                validate_plan(plan, changelog, "v0.2.4")
        plan["supersedes"] = ["v0.2.4"]
        with self.assertRaises(ValueError):
            validate_plan(plan, notes(plan), "v0.2.4")

    def test_prepare_preserves_unreleased_and_older_notes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            changelog = root / "CHANGELOG.md"
            changelog.write_text("# Changelog\n\n## Unreleased\n\n- Pending\n\n## v0.2.3\n\n- Older\n")
            changes = root / "changes.json"
            changes.write_text(json.dumps(self.plan()["changes"]))
            with patch("scripts.core_release_version.Path", side_effect=lambda p: root / p), \
                 patch("sys.argv", ["release", "v0.2.3", str(changes)]):
                main()
            plan = json.loads((root / "release-plan.json").read_text())
            validate_plan(plan, changelog.read_text(), "v0.2.4")
            self.assertIn("## Unreleased\n\n- Pending", changelog.read_text())
            self.assertIn("## v0.2.3\n\n- Older", changelog.read_text())

    def test_publication_refuses_a_stale_source_snapshot(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            plan = self.plan()
            (root / 'release-inputs.json').write_text(json.dumps(dict(through_sha='a' * 40)))
            (root / 'release-plan.json').write_text(json.dumps(plan))
            (root / 'CHANGELOG.md').write_text(notes(plan))
            changed = 'CHANGELOG.md\nrelease-plan.json\nrelease-inputs.json\n'
            with patch('scripts.core_release_version.subprocess.run'), \
                 patch('scripts.core_release_version.subprocess.check_output', side_effect=['b' * 40 + '\n', changed]):
                self.assertEqual(reviewed(root, plan['version']), plan)
            for commits, files in [('b\nc\n', changed), ('b\n', changed + 'runtime.go\n')]:
                with patch('scripts.core_release_version.subprocess.run'), \
                     patch('scripts.core_release_version.subprocess.check_output', side_effect=[commits, files]):
                    with self.assertRaisesRegex(ValueError, 'stale'):
                        reviewed(root, plan['version'])

    def test_real_git_intervening_feature_cannot_ship_under_patch_plan(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            def git(*args):
                return subprocess.check_output(['git', '-C', str(root), *args], text=True).strip()
            git('init', '-q')
            git('config', 'commit.gpgsign', 'false')
            git('config', 'user.name', 'Synthetic')
            git('config', 'user.email', 'synthetic@example.test')
            (root / 'runtime.txt').write_text('compatible fix')
            (root / 'CHANGELOG.md').write_text('# Changelog\n\n## Unreleased\n')
            git('add', '.')
            git('commit', '-qm', 'patch snapshot')
            snapshot = git('rev-parse', 'HEAD')
            (root / 'runtime.txt').write_text('intervening feature')
            git('add', '.')
            git('commit', '-qm', 'feature before release plan merge')
            plan = self.plan()
            (root / 'release-plan.json').write_text(json.dumps(plan))
            (root / 'release-inputs.json').write_text(json.dumps(dict(through_sha=snapshot)))
            (root / 'CHANGELOG.md').write_text('# Changelog\n\n## Unreleased\n\n' + notes(plan))
            git('add', '.')
            git('commit', '-qm', 'stale patch plan')
            with self.assertRaisesRegex(ValueError, 'stale'):
                reviewed(root, plan['version'])
            # A refreshed plan binds the immediate prior source and passes the same guard.
            snapshot = git('rev-parse', 'HEAD')
            plan = dict(plan, bump='minor', version='v0.3.0')
            (root / 'release-plan.json').write_text(json.dumps(plan))
            (root / 'release-inputs.json').write_text(json.dumps(dict(through_sha=snapshot)))
            (root / 'CHANGELOG.md').write_text('# Changelog\n\n## Unreleased\n\n' + notes(plan))
            git('add', '.')
            git('commit', '-qm', 'reviewed refreshed minor plan')
            self.assertEqual(reviewed(root, plan['version']), plan)
