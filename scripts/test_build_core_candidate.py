import json
import tempfile
import unittest
from argparse import Namespace
from pathlib import Path
from unittest.mock import patch

from scripts.build_core_candidate import build
from scripts.test_core_release_provenance import context
from scripts.test_installation_release import manifest
from scripts.core_release_version import notes
from scripts.prepare_core_release import release_plan


class BuildCoreCandidateTest(unittest.TestCase):
    platform = 'linux/amd64'

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        release = manifest()
        self.args = Namespace(version=release["version"], platform=self.platform, edge=release["images"]["edge"],
                              postgres=release["images"]["postgres"], checkout=Path(__file__).resolve().parents[1],
                              output=Path(self.tmp.name) / "candidate")
        config = json.loads((self.args.checkout / "ci/core-release.json").read_text())
        self.args.edge, self.args.postgres = config["edge"], config["postgres"]
        self.args.version = "v0.1.0"
        plan = release_plan("v0.0.0", [dict(number=1, title="Synthetic feature", labels=[dict(name="release:minor")])])
        metadata = {"release-plan.json": json.dumps(plan), "release-inputs.json": json.dumps(dict(through_sha="a" * 40)), "CHANGELOG.md": notes(plan)}
        mock_review = patch("scripts.build_core_candidate.release_metadata", return_value=metadata)
        mock_review.start()
        self.addCleanup(mock_review.stop)
        mock_dependencies = patch("scripts.build_core_candidate.require_platform_image")
        mock_dependencies.start()
        self.addCleanup(mock_dependencies.stop)
        self.env = context()
        self.event = {"repository": {"full_name": self.env["GITHUB_REPOSITORY"], "visibility": "private"}}

    def test_rejects_unreviewed_context_before_building(self):
        self.env["GITHUB_REF"] = "refs/heads/other"
        with patch("scripts.build_core_candidate.subprocess.run") as run:
            with self.assertRaisesRegex(ValueError, "reviewed main"):
                build(self.args, self.env, self.event)
            run.assert_not_called()
            self.assertFalse(self.args.output.exists())

    def test_missing_native_dependency_stops_before_builder_creation(self):
        with patch("scripts.build_core_candidate.subprocess.check_output", side_effect=["a" * 40 + "\n", b""]), \
             patch("scripts.build_core_candidate.require_platform_image", side_effect=ValueError("missing native image")), \
             patch("scripts.build_core_candidate.subprocess.run") as run:
            with self.assertRaisesRegex(ValueError, "missing native image"):
                build(self.args, self.env, self.event)
            run.assert_not_called()

    def test_missing_candidate_metadata_stops_before_building(self):
        with patch("scripts.build_core_candidate.subprocess.check_output", side_effect=["a" * 40 + "\n", b""]), \
             patch("scripts.build_core_candidate.release_metadata", side_effect=ValueError("missing plan")), \
             patch("scripts.build_core_candidate.subprocess.run") as run:
            with self.assertRaisesRegex(ValueError, "missing plan"):
                build(self.args, self.env, self.event)
            run.assert_not_called()
            self.assertFalse(self.args.output.exists())

    def test_already_published_changes_skip_without_building(self):
        self.args.version = ""
        with patch("scripts.build_core_candidate.subprocess.check_output", side_effect=["a" * 40 + "\n", b""]), \
             patch("scripts.build_core_candidate.release_metadata", return_value=None), \
             patch("scripts.build_core_candidate.subprocess.run") as run:
            self.assertFalse(build(self.args, self.env, self.event))
            run.assert_not_called()
            self.assertFalse(self.args.output.exists())

    def test_failed_build_removes_builder_and_temporary_config(self):
        calls = []

        def run(command, **kwargs):
            calls.append((command, kwargs))
            if "--target" in command:
                raise ValueError("synthetic build failure")

        with patch("scripts.build_core_candidate.subprocess.check_output", side_effect=["a" * 40 + "\n", b""]), \
             patch("scripts.build_core_candidate.subprocess.run", side_effect=run):
            with self.assertRaisesRegex(ValueError, "synthetic build failure"):
                build(self.args, self.env, self.event)
        self.assertEqual(calls[-1][0][:3], ["docker", "buildx", "rm"])
        self.assertFalse(Path(calls[0][1]["env"]["DOCKER_CONFIG"]).exists())

    def test_failed_builder_creation_does_not_remove_an_existing_builder(self):
        with patch("scripts.build_core_candidate.subprocess.check_output", side_effect=["a" * 40 + "\n", b""]), \
             patch("scripts.build_core_candidate.subprocess.run", side_effect=ValueError("create failed")) as run:
            with self.assertRaisesRegex(ValueError, "create failed"):
                build(self.args, self.env, self.event)
            self.assertEqual(run.call_count, 1)

    def test_exports_local_archives_without_registry_writes(self):
        self.args.version = ""
        calls = []

        def run(command, **kwargs):
            calls.append(command)
            if "release-evidence" in command:
                baseline = self.args.output / "build" / "out"
                baseline.mkdir(parents=True)
                (baseline / "binary").write_bytes(b"synthetic baseline")
            if "--metadata-file" in command:
                Path(command[command.index("--metadata-file") + 1]).write_text(
                    json.dumps({"containerimage.digest": "sha256:" + "c" * 64}))
                Path(command[command.index("--output") + 1].removeprefix("type=oci,dest=")).write_bytes(b"synthetic OCI")

        with patch("scripts.build_core_candidate.subprocess.check_output", side_effect=["a" * 40 + "\n", b""]), \
             patch("scripts.build_core_candidate.subprocess.run", side_effect=run), \
             patch("scripts.build_core_candidate.verify_archive") as verify, \
             patch("scripts.build_core_candidate.package"), patch("scripts.build_core_candidate.record", return_value={}):
            build(self.args, self.env, self.event)
        images = [cmd for cmd in calls if "--metadata-file" in cmd]
        self.assertEqual(len(images), 4)
        self.assertEqual(verify.call_count, 4)
        for cmd in images:
            self.assertIn("--no-cache", cmd)
            self.assertEqual(cmd[cmd.index("--label") + 1],
                             f"org.opencontainers.image.source=https://github.com/{self.env['GITHUB_REPOSITORY']}")
            self.assertTrue(cmd[cmd.index("--output") + 1].startswith("type=oci,dest="))
            self.assertNotIn("--push", cmd)
        self.assertFalse(any("login" in cmd for cmd in calls))
        self.assertTrue((self.args.output / "record.json").is_file())
        approval = json.loads((self.args.output / "approval.json").read_text())
        self.assertEqual(approval["version"], "v0.1.0")
        self.assertEqual(set(approval["images"]), {"backend", "frontend", "migrate", "cli"})
        self.assertEqual(approval["images"]["backend"]["manifest_digest"], "sha256:" + "c" * 64)


class BuildArm64CoreCandidateTest(BuildCoreCandidateTest):
    platform = 'linux/arm64'


if __name__ == "__main__":
    unittest.main()
