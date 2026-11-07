import copy
import hashlib
import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path

from scripts.qualify_core_images import BINARIES
from scripts.qualify_core_images import hashes
from scripts.test_installation_release import manifest
from scripts.test_verify_core_oci import fixture
from scripts.validate_core_candidate import file_hash, validate_candidate


class ValidateCoreCandidateTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "oci").mkdir()
        (self.root / "build").mkdir()
        (self.root / "build" / "baseline").write_bytes(b"synthetic build evidence")
        self.release = manifest()
        images = {}
        for name in BINARIES:
            path = self.root / "oci" / f"{name}.tar"
            digest = fixture(path)
            self.release["images"][name] = f"ghcr.io/example/core-{name}@{digest}"
            images[name] = {"manifest_digest": digest, "oci_archive_sha256": file_hash(path)}
        data = json.dumps(self.release).encode()
        (self.root / "release.json").write_bytes(data)
        with tarfile.open(self.root / "installation.tar.gz", "w:gz") as archive:
            prefix = f"braidplane-{self.release['version']}-linux-amd64"
            info = tarfile.TarInfo(prefix + "/release.json")
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))
        record = {"release": self.release, "migration": {"version": 1, "digest": "d" * 64},
                  "archive_sha256": file_hash(self.root / "installation.tar.gz")}
        (self.root / "record.json").write_text(json.dumps(record))
        self.approval = {"core_revision": self.release["core_revision"], "version": self.release["version"],
                         "platform": self.release["platform"], "record_sha256": file_hash(self.root / "record.json"),
                         "baseline_sha256": hashlib.sha256(json.dumps(hashes(self.root / "build"), sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
                         "images": images}
        (self.root / "approval.json").write_text(json.dumps(self.approval))
        self.approved = file_hash(self.root / "approval.json")

    def verify(self):
        return validate_candidate(self.root, self.approved, "Example/core", "a" * 40)

    def test_validates_the_full_candidate(self):
        self.assertEqual(self.verify()["release"], self.release)

    def test_rejects_any_changed_candidate_bytes(self):
        for name in ("record.json", "release.json", "installation.tar.gz", "oci/backend.tar", "build/baseline"):
            path = self.root / name
            original = path.read_bytes()
            path.write_bytes(original + b"tampered")
            with self.subTest(name=name), self.assertRaises(ValueError):
                self.verify()
            path.write_bytes(original)

    def test_rejects_valid_but_different_manifest(self):
        changed = copy.deepcopy(self.release)
        changed["images"]["backend"] = "ghcr.io/example/core-backend@sha256:" + "f" * 64
        (self.root / "release.json").write_text(json.dumps(changed))
        with self.assertRaisesRegex(ValueError, "manifest differs"):
            self.verify()

    def test_rejects_unapproved_document_and_wrong_image_destination(self):
        with self.assertRaises(ValueError):
            validate_candidate(self.root, "0" * 64, "Example/core", "a" * 40)
        bad = copy.deepcopy(self.approval)
        bad["images"]["backend"]["manifest_digest"] = "sha256:" + "f" * 64
        (self.root / "approval.json").write_text(json.dumps(bad))
        self.approved = file_hash(self.root / "approval.json")
        with self.assertRaisesRegex(ValueError, "destination"):
            self.verify()


if __name__ == "__main__":
    unittest.main()
