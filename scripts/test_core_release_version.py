import unittest

from scripts.core_release_version import notes, select, validate_plan, with_notes


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

    def test_candidate_notes_preserve_source_unreleased_and_older_releases(self):
        plan = self.plan()
        source = "# Changelog\n\n## Unreleased\n\n- Pending\n\n## v0.2.3\n\n- Older\n"
        candidate = with_notes(source, plan)
        validate_plan(plan, candidate, "v0.2.4")
        self.assertIn("## Unreleased\n\n- Pending", candidate)
        self.assertIn("## v0.2.3\n\n- Older", candidate)
