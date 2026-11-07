import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile
import unittest


class MailpitFixtureTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="braidplane-mailpit-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / "scripts").mkdir()
        source = Path(__file__).with_name("generate-email-cert.sh")
        self.script = self.root / "scripts/generate-email-cert.sh"
        shutil.copy2(source, self.script)
        self.output = self.root / "docker/mailpit"

    def run_generator(self):
        return subprocess.run(["bash", str(self.script)], capture_output=True, text=True)

    def test_generates_a_valid_pair_and_preserves_existing_identity(self):
        result = self.run_generator()
        self.assertEqual(0, result.returncode, result.stderr)
        key = self.output / "mail.example.com-key.pem"
        cert = self.output / "mail.example.com-cert.pem"
        self.assertEqual(0o600, stat.S_IMODE(key.stat().st_mode))
        subprocess.run(["openssl", "verify", "-CAfile", str(cert), str(cert)], check=True, capture_output=True)
        public_cert = subprocess.check_output(["openssl", "x509", "-in", str(cert), "-pubkey", "-noout"])
        public_key = subprocess.check_output(["openssl", "pkey", "-in", str(key), "-pubout"], stderr=subprocess.PIPE)
        self.assertTrue(public_cert == public_key)
        before = key.read_bytes(), cert.read_bytes()
        result = self.run_generator()
        self.assertNotEqual(0, result.returncode)
        self.assertIn("Refusing to replace", result.stderr)
        self.assertTrue(before == (key.read_bytes(), cert.read_bytes()))
        self.assertFalse((self.output / ".tls-lock").exists())

    def test_refuses_partial_or_symbolic_identity(self):
        self.output.mkdir(parents=True)
        key = self.output / "mail.example.com-key.pem"
        key.write_text("retained fixture")
        self.assertNotEqual(0, self.run_generator().returncode)
        self.assertEqual("retained fixture", key.read_text())
        key.unlink()
        key.symlink_to(self.root / "absent")
        self.assertNotEqual(0, self.run_generator().returncode)
        self.assertTrue(key.is_symlink())
        self.assertFalse(key.exists())

    def test_openssl_failure_is_visible_and_releases_the_lock(self):
        executable = self.root / "bin/openssl"
        executable.parent.mkdir()
        executable.write_text("#!/bin/sh\necho fixture-openssl-error >&2\nexit 19\n")
        executable.chmod(0o700)
        result = subprocess.run(["bash", str(self.script)], capture_output=True, text=True,
                                env=os.environ | {"PATH": str(executable.parent) + os.pathsep + os.environ["PATH"]})
        self.assertNotEqual(0, result.returncode)
        self.assertIn("fixture-openssl-error", result.stderr)
        self.assertFalse((self.output / ".tls-lock").exists())
        self.assertFalse((self.output / "mail.example.com-key.pem").exists())

    def test_existing_lock_prevents_generation(self):
        lock = self.output / ".tls-lock"
        lock.mkdir(parents=True)
        self.assertNotEqual(0, self.run_generator().returncode)
        self.assertTrue(lock.is_dir())
        self.assertFalse((self.output / "mail.example.com-key.pem").exists())


if __name__ == "__main__":
    unittest.main()
