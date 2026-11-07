import fcntl
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from scripts.installation.lifecycle import operate
from scripts.installation.release import compose_inputs
from scripts.test_installation_release import manifest


class LifecycleTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.release = manifest()
        (self.root / "release.json").write_text(json.dumps(self.release))
        (self.root / "release.env").write_text(compose_inputs(self.release) + "BRAIDPLANE_HOST=app.customer.test\nBRAIDPLANE_BIND_ADDRESS=192.168.1.20\nBRAIDPLANE_PROJECT_NAME=braidplane-123456abcdef\n")
        self.run = self.enterContext(patch("scripts.installation.lifecycle.subprocess.run", return_value=subprocess.CompletedProcess([], 0, stdout="synthetic status")))
        self.runtime = self.enterContext(patch("scripts.installation.lifecycle.check_runtime"))
        self.tls = self.enterContext(patch("scripts.installation.lifecycle.check_tls"))

    def commands(self):
        return [call.args[0][call.args[0].index("-f") + 2:] for call in self.run.call_args_list]

    def test_install_and_restart_preserve_identity_and_ignore_ambient_overrides(self):
        with patch.dict(os.environ, {"BACKEND_IMAGE": "evil:latest", "COMPOSE_FILE": "/untrusted", "BRAIDPLANE_HOST": "evil.test"}):
            operate(self.root, "install")
        self.assertEqual(self.commands(), [["config", "--quiet"], ["pull"], ["up", "--detach", "--wait", "--wait-timeout", "180"]])
        environment = self.run.call_args.kwargs["env"]
        for key in ("BACKEND_IMAGE", "COMPOSE_FILE", "BRAIDPLANE_HOST"):
            self.assertNotIn(key, environment)
        expected = (self.root / ".installed-release.json").read_bytes()
        operate(self.root, "start")
        self.assertEqual((self.root / ".installed-release.json").read_bytes(), expected)

    def test_stop_needs_no_tls_or_pull_and_preserves_data(self):
        marker = self.root / "retained-key"
        marker.write_text("synthetic")
        operate(self.root, "stop")
        self.assertEqual(self.commands(), [["stop", "--timeout", "45"]])
        self.tls.assert_not_called()
        self.runtime.assert_not_called()
        self.assertEqual(marker.read_text(), "synthetic")

    def test_backup_requires_destination_and_installed_identity(self):
        with self.assertRaisesRegex(ValueError, "requires --backup-directory"):
            operate(self.root, "backup")
        with self.assertRaisesRegex(ValueError, "existing installation identity"):
            operate(self.root, "backup", backup_directory=self.root.parent / "backup")
        self.run.assert_not_called()

    def test_backup_dispatch_preserves_lock_and_does_not_pull_or_start(self):
        (self.root / ".installed-release.json").write_text(json.dumps(self.release))
        def check_lock(*args):
            with (self.root / ".installation.lock").open() as lock:
                with self.assertRaises(BlockingIOError):
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with patch("scripts.installation.upgrade.backup_installation", side_effect=check_lock) as backup:
            operate(self.root, "backup", backup_directory=self.root.parent / "backup")
            backup.assert_called_once()
            with (self.root / ".installation.lock").open() as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.assertEqual(self.commands(), [["config", "--quiet"]])

    def test_lock_and_identity_reject_concurrent_or_changed_release(self):
        with (self.root / ".installation.lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(ValueError, "another installation"):
                operate(self.root, "install")
        self.run.assert_not_called()
        (self.root / ".installed-release.json").write_text('{}')
        with self.assertRaisesRegex(ValueError, "release changed"):
            operate(self.root, "install")
        self.assertEqual(self.commands(), [["config", "--quiet"]])

    def test_failed_start_retains_identity_and_does_not_reset(self):
        self.run.side_effect = [subprocess.CompletedProcess([], 0, stdout=""), subprocess.CompletedProcess([], 0, stdout=""), subprocess.CalledProcessError(1, ["docker"])]
        with self.assertRaisesRegex(ValueError, "data and keys were preserved"):
            operate(self.root, "install")
        self.assertTrue((self.root / ".installed-release.json").exists())
        self.assertEqual(len(self.commands()), 3)
        self.assertFalse(any("down" in command for command in self.commands()))

    def test_incomplete_upgrade_blocks_start_but_allows_status(self):
        (self.root / ".upgrade-in-progress.json").write_text(json.dumps({"previous": self.release, "candidate": self.release}))
        with self.assertRaisesRegex(ValueError, "upgrade is incomplete"):
            operate(self.root, "start")
        self.run.assert_not_called()
        operate(self.root, "status")
        self.assertEqual(self.commands(), [["ps", "--all"]])

    def test_restore_hold_blocks_all_mutations_but_allows_stop_and_status(self):
        (self.root / ".restore-pending.json").write_text('{}')
        for action in ("install", "start", "upgrade", "backup"):
            with self.assertRaisesRegex(ValueError, "held for reconciliation"):
                operate(self.root, action)
        self.run.assert_not_called()
        operate(self.root, "status")
        operate(self.root, "stop")
        self.assertEqual(self.commands(), [["ps", "--all"], ["stop", "--timeout", "45"]])

    def test_status_and_stop_survive_interrupted_release_metadata_commit(self):
        candidate = dict(self.release, version="v0.1.0-alpha.2")
        (self.root / "release.json").write_text(json.dumps(candidate))
        (self.root / ".upgrade-in-progress.json").write_text(json.dumps({"previous": self.release, "candidate": candidate}))
        operate(self.root, "status")
        operate(self.root, "stop")
        self.assertEqual(self.commands(), [["ps", "--all"], ["stop", "--timeout", "45"]])
        with self.assertRaisesRegex(ValueError, "upgrade is incomplete"):
            operate(self.root, "start")
        self.tls.assert_not_called()

    def test_malformed_upgrade_journal_has_actionable_error(self):
        (self.root / ".upgrade-in-progress.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "journal is incomplete"):
            operate(self.root, "stop")
        self.run.assert_not_called()

    def test_mismatched_release_inputs_start_nothing(self):
        with (self.root / "release.env").open("a") as output:
            output.write("BACKEND_IMAGE=evil:latest\n")
        with self.assertRaisesRegex(ValueError, "duplicate"):
            operate(self.root, "install")
        self.run.assert_not_called()
        self.tls.assert_not_called()


if __name__ == "__main__":
    unittest.main()
