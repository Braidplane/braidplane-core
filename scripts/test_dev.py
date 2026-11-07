import importlib.util
import io
import os
from contextlib import redirect_stdout
from http.client import RemoteDisconnected
from unittest import mock
from pathlib import Path
import stat
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("dev", Path(__file__).with_name("dev.py"))
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)


class LocalKeysTests(unittest.TestCase):
    def test_restarts_retain_both_keys(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "keys.env"
            created = dev.load_keys(path, False)
            self.assertEqual(0o600, stat.S_IMODE(path.stat().st_mode))
            self.assertEqual(created, dev.load_keys(path, True))
            self.assertNotEqual(created["ENCRYPTION_KEY_HEX"], created["EXECUTION_REQUEST_KEY_HEX"])

    def test_missing_keys_with_existing_database_are_never_replaced(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "keys.env"
            with self.assertRaisesRegex(ValueError, "restore the original keys"):
                dev.load_keys(path, True)
            self.assertFalse(path.exists())

    def test_invalid_exposed_or_symlink_keys_are_preserved_and_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "keys.env"
            path.write_text("incomplete fixture\n")
            with self.assertRaisesRegex(ValueError, "restore it"):
                dev.load_keys(path, False)
            self.assertEqual("incomplete fixture\n", path.read_text())
            path.unlink()
            dev.load_keys(path, False)
            path.chmod(0o644)
            with self.assertRaisesRegex(ValueError, "0600"):
                dev.load_keys(path, True)
            link = Path(directory) / "linked.env"
            link.symlink_to(path)
            with self.assertRaisesRegex(ValueError, "symbolic link"):
                dev.load_keys(link, False)


class LocalStartupTests(unittest.TestCase):
    def test_response_disconnect_retries_and_cleanup_stops_only_started_processes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            certificates = root / "docker/mailpit"
            certificates.mkdir(parents=True)
            for name in ("mail.example.com-cert.pem", "mail.example.com-key.pem"):
                (certificates / name).touch()
            processes = [mock.Mock() for _ in range(3)]
            for process in processes:
                process.poll.return_value = None
            response = mock.MagicMock()
            response.__enter__.return_value.status = 200
            previous = Path.cwd()
            output = io.StringIO()
            try:
                with (
                    mock.patch.object(dev, "ROOT", root),
                    mock.patch.dict(os.environ, {}, clear=True),
                    mock.patch.object(dev, "load_keys", return_value={}),
                    mock.patch.object(dev.subprocess, "check_output", return_value=""),
                    mock.patch.object(dev.subprocess, "run"),
                    mock.patch.object(dev.subprocess, "Popen", side_effect=processes) as launch,
                    mock.patch.object(dev.signal, "signal"),
                    mock.patch.object(dev.urllib.request, "urlopen", side_effect=[RemoteDisconnected(), response, response, response]) as requests,
                    mock.patch.object(dev.time, "sleep", side_effect=[None, KeyboardInterrupt()]),
                    redirect_stdout(output),
                    self.assertRaises(KeyboardInterrupt),
                ):
                    dev.main()
            finally:
                os.chdir(previous)
            self.assertEqual(4, requests.call_count)
            for call in launch.call_args_list:
                self.assertEqual("true", call.kwargs["env"]["SELF_SERVICE_REGISTRATION_ENABLED"])
            self.assertIn("Application:", output.getvalue())
            for process in processes:
                process.terminate.assert_called_once_with()
                process.wait.assert_called_once_with(timeout=40)
