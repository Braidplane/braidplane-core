import json
import subprocess
import tempfile
from pathlib import Path
from unittest.mock import patch

from scripts.publish_core_candidate import ORAS_IMAGE, publish
from scripts.test_core_release_provenance import context
from scripts.test_validate_core_candidate import ValidateCoreCandidateTest


class PublishCoreCandidateTest(ValidateCoreCandidateTest):
    def setUp(self):
        super().setUp()
        self.publication_tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.publication_tmp.cleanup)
        self.output = Path(self.publication_tmp.name) / "publication"
        self.checkout = Path(__file__).resolve().parents[1]
        self.env = dict(context(), CORE_RELEASE_PUBLICATION_AUTHORIZED="yes", GITHUB_ACTOR="example", GITHUB_TOKEN="synthetic")
        self.event = {"repository": {"full_name": "Example/core", "visibility": "public"}}

    def test_publish_uses_approved_digests_without_tags(self):
        calls = []

        def run(command, **kwargs):
            calls.append((command, kwargs))

        git = ["a" * 40 + "\n", b"", b"1 " + b"d" * 64 + b"\n"]
        with patch("scripts.publish_core_candidate.subprocess.check_output", side_effect=git), \
             patch("scripts.publish_core_candidate.admit", return_value={"synthetic": True}), \
             patch("scripts.publish_core_candidate.subprocess.run", side_effect=run), \
             patch("scripts.publish_core_candidate.require_image_manifest") as inspect, \
             patch("scripts.publish_core_candidate.qualify"), patch("scripts.publish_core_candidate.generate"):
            publish(self.root, self.approved, self.checkout, self.output, "123", "1", self.env, self.event)
        copies = [call for call in calls if ORAS_IMAGE in call[0]]
        self.assertEqual(len(copies), 4)
        self.assertEqual(inspect.call_count, 4)
        for command, _ in copies:
            self.assertIn("--from-oci-layout", command)
            self.assertTrue(command[-2].startswith("/candidate/oci/"))
            self.assertIn("@sha256:", command[-2])
            self.assertNotIn("@", command[-1])
            self.assertNotIn(":", command[-1])
        entries = [json.loads(line) for line in (self.output / "publication.jsonl").read_text().splitlines()]
        self.assertEqual([entry["status"] for entry in entries], ["attempted", "verified"] * 4)
        self.assertEqual(json.loads((self.output / "provenance.json").read_text())["buildDefinition"]["externalParameters"]["commit"], "a" * 40)
        self.assertFalse(Path(calls[0][1]["env"]["DOCKER_CONFIG"]).exists())

    def test_bad_approval_stops_before_registry_actions(self):
        with patch("scripts.publish_core_candidate.subprocess.check_output", side_effect=["a" * 40 + "\n", b""]), \
             patch("scripts.publish_core_candidate.subprocess.run") as run:
            with self.assertRaisesRegex(ValueError, "approved digest"):
                publish(self.root, "0" * 64, self.checkout, self.output, "123", "1", self.env, self.event)
            run.assert_not_called()
            self.assertFalse(self.output.exists())

    def test_changed_signing_manifest_stops_before_registry_actions(self):
        self.release["images"]["backend"] = "ghcr.io/example/core-backend@sha256:" + "f" * 64
        (self.root / "release.json").write_text(json.dumps(self.release))
        with patch("scripts.publish_core_candidate.subprocess.check_output", side_effect=["a" * 40 + "\n", b""]), \
             patch("scripts.publish_core_candidate.subprocess.run") as run:
            with self.assertRaisesRegex(ValueError, "manifest differs"):
                publish(self.root, self.approved, self.checkout, self.output, "123", "1", self.env, self.event)
            run.assert_not_called()
            self.assertFalse(self.output.exists())

    def test_failed_copy_retains_completed_images_and_cleans_credentials(self):
        calls = []

        def run(command, **kwargs):
            calls.append((command, kwargs))
            if ORAS_IMAGE in command and len([call for call in calls if ORAS_IMAGE in call[0]]) == 2:
                raise subprocess.CalledProcessError(1, command)

        git = ["a" * 40 + "\n", b"", b"1 " + b"d" * 64 + b"\n"]
        with patch("scripts.publish_core_candidate.subprocess.check_output", side_effect=git), \
             patch("scripts.publish_core_candidate.admit", return_value={}), \
             patch("scripts.publish_core_candidate.subprocess.run", side_effect=run), \
             patch("scripts.publish_core_candidate.require_image_manifest"):
            with self.assertRaises(subprocess.CalledProcessError):
                publish(self.root, self.approved, self.checkout, self.output, "123", "1", self.env, self.event)
        entries = [json.loads(line) for line in (self.output / "publication.jsonl").read_text().splitlines()]
        self.assertEqual([entry["status"] for entry in entries], ["attempted", "verified", "attempted"])
        self.assertFalse(Path(calls[0][1]["env"]["DOCKER_CONFIG"]).exists())

    def test_no_authorization_stops_before_any_work(self):
        self.env.pop("CORE_RELEASE_PUBLICATION_AUTHORIZED")
        with patch("scripts.publish_core_candidate.subprocess.check_output") as check:
            with self.assertRaisesRegex(ValueError, "authorization"):
                publish(self.root, self.approved, self.checkout, self.output, "123", "1", self.env, self.event)
            check.assert_not_called()
