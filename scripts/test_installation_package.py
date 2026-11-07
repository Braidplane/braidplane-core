import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

from scripts.installation.package import package
from scripts.test_installation_release import manifest


class InstallationPackageTest(unittest.TestCase):
    def test_deterministic_archive_checksums_and_checkout_independent_preparation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release = root / "release.json"
            release.write_text(json.dumps(manifest()))
            first, second = root / "first.tar.gz", root / "second.tar.gz"
            digest = package(release, first)
            self.assertEqual(digest, package(release, second))
            self.assertEqual(first.read_bytes(), second.read_bytes())
            original = first.read_bytes()
            with self.assertRaises(FileExistsError):
                package(release, first)
            self.assertEqual(first.read_bytes(), original)
            extracted = root / "extracted"
            with tarfile.open(first) as archive:
                for member in archive.getmembers():
                    self.assertTrue(member.isfile())
                    self.assertEqual(member.mtime, 0)
                    self.assertNotIn("..", Path(member.name).parts)
                    self.assertFalse(member.name.endswith((".env", ".key", ".go")))
                archive.extractall(extracted, filter="data")
            bundle = next(extracted.iterdir())
            self.assertEqual(json.loads((bundle / "first-result/input.json").read_text()), [{"value": 41}])
            self.assertEqual(json.loads((bundle / "first-result/expected.json").read_text()), [{"result": 42}])
            self.assertTrue((bundle / "first-result/first-result.pipeline.json").is_file())
            self.assertTrue((bundle / "first-result/write-operation.json").is_file())
            entries = (bundle / "SHA256SUMS").read_text().splitlines()
            for line in entries:
                expected, name = line.split("  ", 1)
                self.assertEqual(hashlib.sha256((bundle / name).read_bytes()).hexdigest(), expected)
            self.assertEqual(len(entries), len([p for p in bundle.rglob("*") if p.is_file()]) - 1)
            certs = root / "tls"
            certs.mkdir()
            for name in ("ca.pem", "database.pem", "database.key", "edge.pem", "edge.key"):
                (certs / name).write_text("synthetic fixture")
            target = root / "customer"
            result = subprocess.run([sys.executable, "-m", "scripts.installation.prepare", str(target),
                                     "release.json", "app.customer.test", "192.168.1.20", "mail.customer.test:587",
                                     "recovery@customer.test", str(certs)], cwd=bundle,
                                    env={"PATH": os.environ["PATH"], "PYTHONPATH": "", "PYTHONNOUSERSITE": "1"},
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((target / "runtime.env").is_file())
            self.assertTrue((target / "runtime-grants.sql").is_file())
            status = subprocess.run([sys.executable, "-m", "scripts.installation.lifecycle", "--help"],
                                    cwd=bundle, capture_output=True, text=True)
            self.assertEqual(status.returncode, 0, status.stderr)
            for module in ("backup", "restore_probe", "restore"):
                help_result = subprocess.run([sys.executable, "-m", "scripts.installation." + module, "--help"],
                                             cwd=bundle, env={"PATH": os.environ["PATH"], "PYTHONPATH": "", "PYTHONNOUSERSITE": "1"},
                                             capture_output=True, text=True)
                self.assertEqual(help_result.returncode, 0, help_result.stderr)


if __name__ == "__main__":
    unittest.main()
