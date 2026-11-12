import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts.installation.upgrade import APPS, SNAPSHOT, backup_installation, upgrade, version_order
from scripts.installation.backup import verify
from scripts.test_installation_release import manifest


class UpgradeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.install = self.root / "install"
        self.install.mkdir()
        for name in SNAPSHOT:
            (self.install / name).write_text("retained original " + name)
        (self.install / "compose.json").write_text(json.dumps({"services": {name: {} for name in APPS}}))
        (self.install / "certificates").mkdir()
        for name in ("ca.pem", "database.pem", "database.key", "edge.pem", "edge.key"):
            (self.install / "certificates" / name).write_text("synthetic " + name)
        self.current = manifest()
        self.candidate = manifest()
        self.candidate["version"] = "v0.1.0-alpha.2"
        self.release = self.root / "next.json"
        self.release.write_text(json.dumps(self.candidate))
        self.bundle = self.root / "bundle"
        (self.bundle / "scripts/installation").mkdir(parents=True)
        (self.bundle / "release.json").write_text(json.dumps(self.candidate))
        (self.bundle / "scripts/self-hosted-runtime-grants.sql").write_text("candidate runtime grants")
        (self.bundle / "scripts/installation/execution-grants.sql").write_text("candidate execution grants")
        self.enterContext(patch("scripts.installation.upgrade.BUNDLE", self.bundle))
        self.backup = self.root / "backup"
        self.values = {"BRAIDPLANE_HOST": "app.customer.test", "BRAIDPLANE_BIND_ADDRESS": "192.168.1.20", "BRAIDPLANE_PROJECT_NAME": "braidplane-123456abcdef"}
        self.calls = []
        def compose(*args, **kwargs):
            self.calls.append(args)
            if args == ("run", "--rm", "--no-deps", "-T", "migrate"):
                self.assertTrue((self.backup / "configuration.tar").exists())
                self.assertEqual(verify(self.backup)["release"], self.current)
                self.assertEqual((self.install / "execution-grants.sql").read_text(), "retained original execution-grants.sql")
            if args[0] == "run" and "grants" in args:
                self.assertEqual(kwargs["input"], "candidate runtime grants\ncandidate execution grants")
                self.assertEqual((self.install / "execution-grants.sql").read_text(), "candidate execution grants")
                self.assertEqual((self.install / "runtime-grants.sql").read_text(), "candidate runtime grants")
        self.compose = compose
        def run(*args, **kwargs):
            if hasattr(kwargs.get("stdout"), "write"):
                kwargs["stdout"].write(b"synthetic database dump")
            return subprocess.CompletedProcess([], 0, stdout=b"1024\n")
        self.run = self.enterContext(patch("scripts.installation.upgrade.subprocess.run", side_effect=run))

    def upgrade(self):
        upgrade(self.install, self.current, self.values, self.release, self.backup, self.compose,
                ["docker", "compose"], {})

    def test_versions_are_forward_only(self):
        ordered = ["v0.1.0-alpha.2", "v0.1.0-alpha.10", "v0.1.0-beta", "v0.1.0", "v0.2.0"]
        self.assertEqual(sorted(ordered, key=version_order), ordered)
        self.candidate["version"] = "v0.0.9"
        self.release.write_text(json.dumps(self.candidate))
        (self.bundle / "release.json").write_text(json.dumps(self.candidate))
        with self.assertRaisesRegex(ValueError, "newer release"):
            self.upgrade()
        self.assertFalse(self.backup.exists())


    def test_standalone_backup_stops_apps_without_migrating_or_restarting(self):
        original = (self.install / "release.json").read_bytes()
        backup_installation(self.install, self.current, self.backup, self.compose, ["docker", "compose"], {})
        self.assertEqual(self.calls, [("stop", "--timeout", "45", *APPS)])
        self.assertEqual(verify(self.backup)["release"], self.current)
        self.assertEqual((self.install / "release.json").read_bytes(), original)
        self.assertFalse((self.backup / "candidate.env").exists())
        self.assertFalse((self.install / ".upgrade-in-progress.json").exists())

    def test_standalone_backup_failure_does_not_restart(self):
        self.run.side_effect = [subprocess.CompletedProcess([], 0, stdout=b"1024\n"), subprocess.CalledProcessError(1, ["pg_dump"])]
        with self.assertRaisesRegex(ValueError, "restart was not attempted"):
            backup_installation(self.install, self.current, self.backup, self.compose, ["docker", "compose"], {})
        self.assertEqual(self.calls, [("stop", "--timeout", "45", *APPS)])
        self.assertFalse((self.backup / "backup.json").exists())

    def test_low_backup_space_rejects_before_application_stop_or_migration(self):
        with patch("scripts.installation.upgrade.shutil.disk_usage") as usage:
            usage.return_value.free = 0
            with self.assertRaisesRegex(ValueError, "insufficient backup filesystem space"):
                self.upgrade()
        self.assertFalse(any(call[0] in ("stop", "up") or call[-1] == "migrate" for call in self.calls))
        self.assertFalse((self.install / ".upgrade-in-progress.json").exists())
        self.assertFalse((self.backup / "database.dump").exists())

    def test_invalid_database_size_rejects_standalone_backup_before_stop(self):
        self.run.return_value = subprocess.CompletedProcess([], 0, stdout=b"not a size")
        self.run.side_effect = None
        with self.assertRaisesRegex(ValueError, "capacity query failed"):
            backup_installation(self.install, self.current, self.backup, self.compose, ["docker", "compose"], {})
        self.assertEqual(self.calls, [])

    def test_success_backs_up_before_migration_and_retains_project(self):
        self.upgrade()
        self.assertEqual(self.calls[1], ("pull",))
        self.assertEqual(self.calls[2][0], "stop")
        self.assertEqual(json.loads((self.install / "release.json").read_text()), self.candidate)
        self.assertFalse((self.install / ".upgrade-in-progress.json").exists())
        self.assertEqual((self.backup / "database.dump").stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.backup.stat().st_mode & 0o777, 0o700)
        with tarfile.open(self.backup / "configuration.tar") as archive:
            self.assertEqual(archive.extractfile("runtime.env").read(), b"retained original runtime.env")
            self.assertEqual(archive.extractfile("execution-grants.sql").read(), b"retained original execution-grants.sql")
        self.assertIn("BRAIDPLANE_PROJECT_NAME=braidplane-123456abcdef", (self.install / "release.env").read_text())

    def test_api_only_upgrade_never_targets_frontend(self):
        (self.install / "compose.json").write_text(json.dumps({"services": {name: {} for name in APPS if name != "frontend"}}))
        self.upgrade()
        for call in self.calls:
            if call[0] in ("up", "stop"):
                self.assertNotIn("frontend", call)
                self.assertIn("api", call)

    def test_backup_receipt_rejects_corruption_missing_files_and_unknown_format(self):
        self.upgrade()
        self.assertEqual((self.backup / "backup.json").stat().st_mode & 0o777, 0o600)
        dump = self.backup / "database.dump"
        original = dump.read_bytes()
        dump.write_bytes(b"corrupted")
        with self.assertRaisesRegex(ValueError, "integrity mismatch"):
            verify(self.backup)
        dump.unlink()
        with self.assertRaisesRegex(ValueError, "regular file"):
            verify(self.backup)
        dump.symlink_to(self.backup / "configuration.tar")
        with self.assertRaisesRegex(ValueError, "regular file"):
            verify(self.backup)
        dump.unlink()
        dump.write_bytes(original)
        receipt_path = self.backup / "backup.json"
        receipt = json.loads(receipt_path.read_text())
        receipt["format_version"] = 2
        receipt_path.write_text(json.dumps(receipt))
        with self.assertRaisesRegex(ValueError, "unsupported backup format"):
            verify(self.backup)

    def test_receipt_failure_prevents_migration_and_restart(self):
        with patch("scripts.installation.upgrade.seal", side_effect=OSError("disk full")):
            with self.assertRaisesRegex(ValueError, "no automatic rollback"):
                self.upgrade()
        self.assertTrue((self.install / ".upgrade-in-progress.json").exists())
        self.assertFalse(any(call[0] == "up" or call[-1] == "migrate" for call in self.calls))

    def test_dump_failure_keeps_journal_original_identity_and_requests_stop(self):
        self.run.side_effect = [subprocess.CompletedProcess([], 0, stdout=b"1024\n"), subprocess.CalledProcessError(1, ["pg_dump"])]
        with self.assertRaisesRegex(ValueError, "no automatic rollback"):
            self.upgrade()
        self.assertTrue((self.install / ".upgrade-in-progress.json").is_file())
        self.assertEqual((self.install / "release.json").read_text(), "retained original release.json")
        self.assertEqual(self.calls[-1][0], "stop")
        self.assertFalse(any(call[0] == "up" for call in self.calls))

    def test_grant_failure_keeps_backup_and_stops_application(self):
        original_compose = self.compose
        def compose(*args, **kwargs):
            original_compose(*args, **kwargs)
            if args[0] == "run" and "grants" in args:
                raise ValueError("grant failed")
        self.compose = compose
        with self.assertRaisesRegex(ValueError, "no automatic rollback"):
            self.upgrade()
        self.assertTrue((self.install / ".upgrade-in-progress.json").is_file())
        with tarfile.open(self.backup / "configuration.tar") as archive:
            self.assertEqual(archive.extractfile("execution-grants.sql").read(), b"retained original execution-grants.sql")
        self.assertEqual(self.calls[-1][0], "stop")
        self.assertFalse(any(call[0] == "up" for call in self.calls))

    def test_missing_snapshot_input_rejected_before_downtime(self):
        (self.install / "runtime.env").unlink()
        with self.assertRaisesRegex(ValueError, "regular files"):
            self.upgrade()
        self.assertEqual(self.calls, [])
        self.run.assert_not_called()
        self.assertFalse(self.backup.exists())
        self.assertFalse((self.install / ".upgrade-in-progress.json").exists())

    def test_mismatched_bundle_rejected_before_downtime(self):
        (self.bundle / "release.json").write_text(json.dumps(self.current))
        with self.assertRaisesRegex(ValueError, "bundle matching"):
            self.upgrade()
        self.assertEqual(self.calls, [])
        self.assertFalse(self.backup.exists())

    def test_pull_failure_does_not_stop_application(self):
        def compose(*args, **kwargs):
            if args[0] == "pull":
                raise ValueError("unavailable image")
        self.compose = compose
        with self.assertRaisesRegex(ValueError, "unavailable image"):
            self.upgrade()
        self.assertFalse((self.install / ".upgrade-in-progress.json").exists())
        self.run.assert_not_called()

    def test_postgres_change_and_existing_backup_are_rejected(self):
        self.candidate["images"]["postgres"] = "other@sha256:" + "c" * 64
        self.release.write_text(json.dumps(self.candidate))
        (self.bundle / "release.json").write_text(json.dumps(self.candidate))
        with self.assertRaisesRegex(ValueError, "PostgreSQL image"):
            self.upgrade()
        self.assertFalse(self.backup.exists())
        self.candidate["images"]["postgres"] = self.current["images"]["postgres"]
        self.release.write_text(json.dumps(self.candidate))
        (self.bundle / "release.json").write_text(json.dumps(self.candidate))
        self.backup.mkdir()
        with self.assertRaises(FileExistsError):
            self.upgrade()
        self.assertEqual(self.calls, [])


if __name__ == "__main__":
    unittest.main()
