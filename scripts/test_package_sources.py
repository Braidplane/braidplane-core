import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts import package_sources


class PackageSourcesTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.content = b"synthetic ZIP bytes"
        self.zip = self.root / "module.zip"
        self.zip.write_bytes(self.content)
        checksum = hashlib.sha256(self.content).hexdigest()
        self.manifest = self.root / "MPL_SOURCES.sha256"
        self.manifest.write_text(f"{checksum}  example.test/module/@v/v1.2.3.zip\n")

    def run_package(self, version="v1.2.3"):
        outputs = [f"example.test/module {version}\n", json.dumps({"Zip": str(self.zip)})]
        with patch.object(package_sources, "go_stdout", side_effect=outputs), \
                patch.object(package_sources.subprocess, "run") as verify:
            package_sources.package(self.root)
        self.assertEqual(verify.call_args.args[0][-2:], ["mod", "verify"])

    def test_copies_exact_source_bytes(self):
        self.run_package()
        output = self.root / "out/licenses/sources/example.test/module/@v/v1.2.3.zip"
        self.assertEqual(output.read_bytes(), self.content)

    def test_refuses_changed_source_bytes_and_dependency_versions(self):
        self.zip.write_bytes(b"different source")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.run_package()
        with self.assertRaisesRegex(ValueError, "version differs"):
            self.run_package("v2.0.0")
        self.assertFalse((self.root / "out").exists())

    def test_refuses_unsafe_and_duplicate_manifest_paths(self):
        valid = self.manifest.read_text()
        for text in (valid.replace("example.test/module", "../module"), valid * 2):
            with self.subTest(text=text):
                self.manifest.write_text(text)
                with self.assertRaisesRegex(ValueError, "unsafe or duplicate"):
                    package_sources.source_entries(self.root)

    def test_mpl_notice_changes_require_matching_sources(self):
        matching = b"header\nexample.test/module v1.2.3\n==========================\nMozilla Public License Version 2.0\n"
        package_sources.verify_mpl_inventory(self.root, matching)
        for changed in (matching.replace(b"v1.2.3", b"v1.2.4"), matching.replace(b"Mozilla Public License", b"MIT")):
            with self.subTest(changed=changed):
                with self.assertRaisesRegex(ValueError, "does not match notice inventory"):
                    package_sources.verify_mpl_inventory(self.root, changed)

    def test_refuses_dependency_replacement(self):
        with self.assertRaisesRegex(ValueError, "replaced dependencies"):
            self.run_package("v1.2.3 replaced")

    def test_empty_manifest_needs_no_source_downloads(self):
        self.manifest.write_text("")
        with patch.object(package_sources, "go_stdout") as download:
            package_sources.package(self.root)
        download.assert_not_called()


if __name__ == "__main__":
    unittest.main()
