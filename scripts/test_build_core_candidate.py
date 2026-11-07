import json
import tempfile
import unittest
from argparse import Namespace
from pathlib import Path
from unittest.mock import patch

from scripts.build_core_candidate import build
from scripts.test_core_release_provenance import context
from scripts.test_installation_release import manifest


class BuildCoreCandidateTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        release = manifest()
        self.args = Namespace(version=release["version"], platform=release["platform"], edge=release["images"]["edge"],
                              postgres=release["images"]["postgres"], checkout=Path(__file__).resolve().parents[1],
                              output=Path(self.tmp.name) / "candidate")
        self.env = context()
        self.event = {"repository": {"full_name": self.env["GITHUB_REPOSITORY"], "visibility": "private"}}

    def test_rejects_unreviewed_context_before_building(self):
        self.env["GITHUB_REF"] = "refs/heads/other"
        with patch("scripts.build_core_candidate.subprocess.run") as run:
            with self.assertRaisesRegex(ValueError, "reviewed main"):
                build(self.args, self.env, self.event)
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
        self.assertEqual(set(approval["images"]), {"backend", "frontend", "migrate", "cli"})
        self.assertEqual(approval["images"]["backend"]["manifest_digest"], "sha256:" + "c" * 64)


if __name__ == "__main__":
    unittest.main()
