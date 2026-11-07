import json
import subprocess
import unittest
from unittest.mock import patch

from scripts.installation.preflight import check_runtime
from scripts.test_installation_release import manifest


class RuntimePreflightTest(unittest.TestCase):
    def run_check(self, info=None, flags="--wait --wait-timeout"):
        if info is None:
            info = {"OSType": "linux", "Architecture": "x86_64", "ServerVersion": "fixture", "NCPU": 2, "MemTotal": 1024}
        results = [json.dumps(info), "fixture-compose\n", flags]
        with patch("scripts.installation.preflight.subprocess.run", side_effect=[subprocess.CompletedProcess([], 0, stdout=value) for value in results]):
            return check_runtime(manifest())

    def test_native_daemon_and_compose_features(self):
        receipt = self.run_check()
        self.assertEqual(receipt["platform"], "linux/amd64")
        self.assertEqual(receipt["compose"], "fixture-compose")
        self.assertEqual(receipt["daemon_cpus"], 2)

    def test_wrong_platform_or_missing_features_fail_before_start(self):
        for info in ({"OSType": "windows"}, {"OSType": "linux", "Architecture": "aarch64"}, {}):
            with self.subTest(info=info), self.assertRaises(ValueError):
                self.run_check(info)
        with self.assertRaisesRegex(ValueError, "lacks required"):
            self.run_check(flags="--detach --wait")

    def test_missing_or_unavailable_docker_has_actionable_error(self):
        with patch("scripts.installation.preflight.subprocess.run", side_effect=FileNotFoundError("docker")):
            with self.assertRaisesRegex(ValueError, "daemon access and context"):
                check_runtime(manifest())
        with patch("scripts.installation.preflight.subprocess.run", return_value=subprocess.CompletedProcess([], 0, stdout="broken")):
            with self.assertRaisesRegex(ValueError, "invalid JSON"):
                check_runtime(manifest())


if __name__ == "__main__":
    unittest.main()
