"""Check customer topology invariants without a daemon or customer credentials."""
import json
from pathlib import Path
import unittest

COMPOSE = Path(__file__).parent / "installation" / "compose.json"


class InstallationComposeTest(unittest.TestCase):
    def setUp(self):
        self.compose = json.loads(COMPOSE.read_text())
        self.services = self.compose["services"]

    def test_roles_and_migration_order(self):
        for role in ("api", "worker", "executor"):
            service = self.services[role]
            self.assertEqual(service["command"], [role])
            self.assertEqual(service["environment"]["EXECUTION_BACKEND"], "poller")
            self.assertEqual(service["environment"]["DEPLOYMENT_PROFILE"], "self-hosted")
            self.assertEqual(service["depends_on"]["grants"]["condition"], "service_completed_successfully")
        self.assertEqual(self.services["grants"]["depends_on"]["migrate"]["condition"], "service_completed_successfully")
        self.assertEqual(self.services["migrate"]["depends_on"]["database"]["condition"], "service_healthy")
        self.assertEqual(self.services["executor"]["env_file"], ["executor.env"])
        self.assertNotIn("./config.toml:", " ".join(self.services["executor"]["volumes"]))

    def test_persistent_storage_and_exposure(self):
        self.assertIn("pg_isready -h 127.0.0.1", self.services["database"]["healthcheck"]["test"][1])
        self.assertIn("database:/var/lib/postgresql", self.services["database"]["volumes"])
        for name, service in self.services.items():
            with self.subTest(service=name):
                self.assertNotIn("build", service)
                self.assertIn(":?release image required}", service["image"])
                self.assertNotIn("privileged", service)
                self.assertNotIn("network_mode", service)
                if name != "edge":
                    self.assertNotIn("ports", service)
                self.assertEqual(service["logging"]["options"]["max-size"], "10m")
        for name in ("api", "worker", "executor", "frontend", "cli"):
            self.assertTrue(self.services[name]["read_only"])
            self.assertEqual(self.services[name]["cap_drop"], ["ALL"])
        self.assertEqual(self.services["cli"]["profiles"], ["tools"])
        self.assertEqual(self.services["bootstrap"]["profiles"], ["tools"])
        self.assertEqual(self.services["bootstrap"]["env_file"], ["runtime.env", "migrator.env"])
        self.assertNotIn("healthcheck", self.services["bootstrap"])
        self.assertEqual(self.services["bootstrap"]["entrypoint"], ["/braidplane-admin"])
        self.assertEqual(self.services["bootstrap"]["command"], ["bootstrap"])
        for name in ("api", "worker", "executor", "frontend"):
            probe = self.services[name]["healthcheck"]
            self.assertEqual(probe["test"], ["CMD", "/braidplane-healthcheck", "3002" if name == "frontend" else "3001"])
            self.assertEqual(probe["timeout"], "3s")
        self.assertEqual(self.services["edge"]["depends_on"]["frontend"]["condition"], "service_healthy")


if __name__ == "__main__":
    unittest.main()
