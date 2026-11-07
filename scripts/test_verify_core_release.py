import copy
import hashlib
import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.test_installation_release import manifest
from scripts.core_release_record import record
from scripts.verify_core_release import verify_release


class VerifyCoreReleaseTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.record_path, self.archive, self.output = (self.root / name for name in ("record.json", "bundle.tar.gz", "evidence"))
        self.release = manifest()
        self.write_archive(self.release)
        self.record = {"release": self.release, "migration": {"version": 1, "digest": "d" * 64},
                       "archive_sha256": hashlib.sha256(self.archive.read_bytes()).hexdigest()}
        self.record_path.write_text(json.dumps(self.record))
        self.expected = {"record_sha256": hashlib.sha256(self.record_path.read_bytes()).hexdigest(),
                         "repository": "Example/core", "workflow": ".github/workflows/release.yml",
                         "source_sha": "a" * 40, "signer_sha": "a" * 40, "run_id": "123", "attempt": "1",
                         "version": self.release["version"], "platform": self.release["platform"],
                         "migration_version": 1, "migration_digest": "d" * 64}

    def write_archive(self, release):
        prefix = f"braidplane-{self.release['version']}-{self.release['platform'].replace('/', '-')}"
        data = json.dumps(release).encode()
        with tarfile.open(self.archive, "w:gz") as archive:
            info = tarfile.TarInfo(prefix + "/release.json")
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))

    def test_complete_set_is_required_for_receipt(self):
        with patch("scripts.verify_core_release.verify", return_value=b"verified") as check, \
             patch("scripts.verify_core_release.require_image_manifest"):
            verify_release(self.record_path, self.archive, self.expected, self.output, self.root)
        self.assertEqual(check.call_count, 10)
        self.assertTrue(all(call.args[7] == "1" for call in check.call_args_list))
        receipt = json.loads((self.output / "verification.json").read_text())
        self.assertEqual(len(receipt["verified"]), 10)
        self.assertEqual(receipt["migration"], self.record["migration"])

    def test_record_is_derived_from_the_clean_candidate(self):
        outputs = ["a" * 40 + "\n", b"", b"1 " + b"d" * 64 + b"\n"]
        with patch("scripts.core_release_record.subprocess.check_output", side_effect=outputs):
            self.assertEqual(record(self.release, self.archive, self.root), self.record)
        with patch("scripts.core_release_record.subprocess.check_output", side_effect=["a" * 40 + "\n", b" M source\n"]):
            with self.assertRaisesRegex(ValueError, "clean and match"):
                record(self.release, self.archive, self.root)

    def test_failed_attestation_leaves_no_receipt(self):
        with patch("scripts.verify_core_release.verify", side_effect=[b"ok", b"ok", ValueError("bad signature")]), \
             patch("scripts.verify_core_release.require_image_manifest"):
            with self.assertRaises(ValueError):
                verify_release(self.record_path, self.archive, self.expected, self.output, self.root)
        self.assertFalse((self.output / "verification.json").exists())

    def test_index_manifest_leaves_no_receipt(self):
        with patch("scripts.verify_core_release.verify", return_value=b"ok"), \
             patch("scripts.verify_core_release.require_image_manifest", side_effect=ValueError("image index")):
            with self.assertRaisesRegex(ValueError, "image index"):
                verify_release(self.record_path, self.archive, self.expected, self.output, self.root)
        self.assertFalse((self.output / "verification.json").exists())

    def test_unapproved_record_or_migration_is_rejected(self):
        for field, value in (("record_sha256", "0" * 64), ("migration_digest", "0" * 64)):
            expected = copy.deepcopy(self.expected)
            expected[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                verify_release(self.record_path, self.archive, expected, self.output, self.root)
            self.assertFalse(self.output.exists())

    def test_archive_manifest_must_match_record(self):
        other = copy.deepcopy(self.release)
        other["version"] = "v1.0.0"
        self.write_archive(other)
        with patch("scripts.verify_core_release.verify", return_value=b"verified"):
            with self.assertRaisesRegex(ValueError, "manifest differs"):
                verify_release(self.record_path, self.archive, self.expected, self.output, self.root)
        self.assertFalse((self.output / "verification.json").exists())


if __name__ == "__main__":
    unittest.main()
