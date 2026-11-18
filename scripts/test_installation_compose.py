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

    def test_proxy_network_only_contains_request_forwarders(self):
        proxy_members = {name for name, service in self.services.items() if "proxy" in service["networks"]}
        self.assertEqual(proxy_members, {"edge", "frontend", "api"})
        edge, frontend, api = (self.services[name] for name in ("edge", "frontend", "api"))
        self.assertEqual(set(edge["networks"]), {"proxy"})
        self.assertEqual(frontend["environment"]["FRONTEND_SERVER_TRUSTED_PROXIES"],
                         edge["networks"]["proxy"]["ipv4_address"])
        self.assertEqual(api["environment"]["SERVER_TRUSTED_PROXIES"],
                         frontend["networks"]["proxy"]["ipv4_address"])
        self.assertEqual(frontend["environment"]["BACKEND_API_URL"], "http://api-proxy:3001")
        self.assertIn("api-proxy", api["networks"]["proxy"]["aliases"])


if __name__ == "__main__":
    unittest.main()
