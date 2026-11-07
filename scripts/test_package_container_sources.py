import hashlib
import json
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts import package_container_sources as sources


class ContainerSourcesTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.payload = self.root / "source.tar.xz"
        self.payload.write_bytes(b"synthetic upstream source archive")
        checksum = sources.digest(self.payload)
        self.dsc = self.root / "example_1.0-1.dsc"
        self.dsc.write_text(
            "Source: example\nVersion: 1.0-1\nChecksums-Sha256:\n"
            f" {checksum} {self.payload.stat().st_size} example_1.0.orig.tar.xz\n"
        )
        self.files = {sources.digest(path, "sha1"): path for path in (self.dsc, self.payload)}
        self.names = {self.dsc: self.dsc.name, self.payload: "example_1.0.orig.tar.xz"}
        for name in ("LICENSE", "NOTICE", "SOURCE_AVAILABILITY.txt"):
            (self.root / name).write_text("synthetic attribution\n")

    def metadata(self, path):
        if path.endswith("/srcfiles"):
            return {"result": [{"hash": sha1} for sha1 in self.files]}
        sha1 = path.split("/")[-2]
        return {"result": [{"name": self.names[self.files[sha1]]}]}

    def collect(self):
        with patch.object(sources, "metadata", side_effect=self.metadata), \
                patch.object(sources, "cached_file", side_effect=lambda cache, sha1: self.files[sha1]):
            return sources.collect_source(("example", "1.0-1"), self.root)

    def test_collects_every_source_descriptor_file_and_verifies_bytes(self):
        package, version, files = self.collect()
        self.assertEqual((package, version), ("example", "1.0-1"))
        self.assertEqual(set(files), {self.dsc.name, "example_1.0.orig.tar.xz"})
        self.payload.write_bytes(b"changed source")
        with self.assertRaisesRegex(ValueError, "descriptor checksum mismatch"):
            self.collect()

    def test_rejects_wrong_descriptor_identity_and_unsafe_filenames(self):
        for text in (
            self.dsc.read_text().replace("Version: 1.0-1", "Version: 2.0-1"),
            self.dsc.read_text().replace("example_1.0.orig.tar.xz", "../outside.tar.xz"),
        ):
            with self.subTest(text=text), self.assertRaises(ValueError):
                sources.source_checksums(text, "example", "1.0-1")

    def test_rejects_download_bytes_that_differ_from_snapshot_identity(self):
        with patch.object(sources, "urlopen", return_value=io.BytesIO(b"incorrect download")), \
                self.assertRaisesRegex(ValueError, "download checksum mismatch"):
            sources.cached_file(self.root, "0" * 40)
        self.assertFalse((self.root / ("0" * 40)).exists())

    def test_archive_covers_the_immutable_image_and_installed_source_graph(self):
        image_id = "sha256:" + "1" * 64
        source = self.collect()
        outputs = [json.dumps([{"Id": image_id, "Architecture": "arm64"}]),
                   "example-bin\t1.0-1\texample\t1.0-1\n"]
        with patch.object(sources.subprocess, "check_output", side_effect=outputs) as docker, \
                patch.object(sources, "collect_source", return_value=source) as collect:
            path = sources.package_image("example:local", self.root)
        collect.assert_called_once_with(("example", "1.0-1"), self.root / ".worktree/cache/debian-sources")
        self.assertIn(image_id, docker.call_args_list[1].args[0])
        self.assertIn("--read-only", docker.call_args_list[1].args[0])
        with tarfile.open(path) as archive:
            manifest = json.load(archive.extractfile("manifest.json"))
            self.assertEqual(manifest["image"], image_id)
            self.assertEqual(len(manifest["sources"]), 1)
            for name, record in manifest["sources"][0]["files"].items():
                data = archive.extractfile(name).read()
                self.assertEqual(hashlib.sha256(data).hexdigest(), record["sha256"])
                self.assertEqual(len(data), record["bytes"])
        receipt = json.loads(path.with_suffix(".json").read_text())
        self.assertEqual(receipt["sha256"], sources.digest(path))


if __name__ == "__main__":
    unittest.main()
