import json
import os
from pathlib import Path
import tempfile
import tomllib
import unittest

from scripts.installation.prepare import prepare
from scripts.test_installation_release import manifest


class PreparationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.release = self.root / "release.json"
        self.release.write_text(json.dumps(manifest()))
        self.certs = self.root / "tls"
        self.certs.mkdir()
        for name in ("ca.pem", "database.pem", "database.key", "edge.pem", "edge.key"):
            (self.certs / name).write_text("synthetic test input\n")
        self.target = self.root / "bundle"

    def prepare(self, **overrides):
        args = dict(directory=self.target, manifest=self.release, hostname="app.customer.test",
                    bind_address="192.168.1.20", smtp_host="mail.customer.test:587",
                    sender="recovery@customer.test", certificates=self.certs)
        args.update(overrides)
        prepare(**args)

    def test_bundle_is_self_contained_and_preserves_keys(self):
        self.prepare()
        runtime = dict(line.split("=", 1) for line in (self.target / "runtime.env").read_text().splitlines())
        executor = dict(line.split("=", 1) for line in (self.target / "executor.env").read_text().splitlines())
        self.assertNotIn("ENCRYPTION_KEY_HEX", executor)
        self.assertEqual(runtime["EXECUTION_REQUEST_KEY_HEX"], executor["EXECUTION_REQUEST_KEY_HEX"])
        self.assertNotEqual(runtime["ENCRYPTION_KEY_HEX"], runtime["EXECUTION_REQUEST_KEY_HEX"])
        self.assertNotEqual(runtime["DB_PASSWORD"], executor["DB_PASSWORD"])
        self.assertEqual(runtime["DB_SSL_MODE"], "verify-full")
        self.assertEqual(os.stat(self.target / "runtime.env").st_mode & 0o777, 0o600)
        self.assertEqual(os.stat(self.target).st_mode & 0o777, 0o700)
        config = tomllib.loads((self.target / "config.toml").read_text())
        self.assertEqual(config["deployment"]["profile"], "self-hosted")
        self.assertFalse(config["logging"]["log_http_requests"])
        self.assertFalse(config["auth"]["self_service_registration_enabled"])
        self.assertTrue(config["session"]["cookie_secure"])
        for name in ("compose.json", "Caddyfile", "runtime-grants.sql", "execution-grants.sql", "release.json"):
            self.assertTrue((self.target / name).is_file(), name)
        before = (self.target / "runtime.env").read_bytes()
        with self.assertRaises(FileExistsError):
            self.prepare()
        self.assertEqual((self.target / "runtime.env").read_bytes(), before)

    def test_api_only_keeps_backend_roles_and_direct_tls_routing(self):
        self.prepare(api_only=True)
        compose = json.loads((self.target / "compose.json").read_text())
        self.assertNotIn("frontend", compose["services"])
        for service in ("api", "worker", "executor", "database", "bootstrap", "migrate", "grants"):
            self.assertIn(service, compose["services"])
        self.assertEqual(compose["services"]["edge"]["depends_on"], {"api": {"condition": "service_healthy"}})
        self.assertIn("reverse_proxy api:3001", (self.target / "Caddyfile").read_text())
        self.assertNotIn("frontend", (self.target / "Caddyfile").read_text())

    def test_untrusted_customer_values_create_nothing(self):
        for key, value in (("hostname", "app.test\nEVIL=yes"), ("hostname", "app.localhost"),
                           ("bind_address", "0.0.0.0;id"), ("smtp_host", 'mail.test:587"'),
                           ("smtp_host", "mail.test:99999"), ("sender", "x@test\nEVIL=yes")):
            with self.subTest(key=key):
                with self.assertRaises(ValueError):
                    self.prepare(**{key: value})
                self.assertFalse(self.target.exists())
        (self.certs / "edge.key").unlink()
        (self.certs / "edge.key").symlink_to(self.certs / "database.key")
        with self.assertRaises(ValueError):
            self.prepare()
        self.assertFalse(self.target.exists())


if __name__ == "__main__":
    unittest.main()
