import json
import os
import subprocess
import unittest
from unittest.mock import patch

from scripts import run_bruno


class BrunoRunnerTests(unittest.TestCase):
    def run_fixture(self, seed, status=0):
        with patch.dict(os.environ, {"BRUNO_BASE_URL": "http://127.0.0.1:3001/api"}), \
                patch.object(run_bruno.subprocess, "run", side_effect=[
                    subprocess.CompletedProcess([], 0, seed, ""),
                    subprocess.CompletedProcess([], status),
                ]) as execute:
            result = run_bruno.main()
        return result, execute

    def test_propagates_failed_assertions_and_passes_fixture_identity(self):
        seed = "api_key_token=synthetic-token\norganization_slug=owned\nempty_organization_slug=other\nproject_slug=example\n"
        result, execute = self.run_fixture(seed, status=1)
        self.assertEqual(result, 1)
        args = execute.call_args.args[0]
        self.assertIn("apiKey=synthetic-token", args)
        self.assertIn("otherOrganization=other", args)
        self.assertEqual(execute.call_args.kwargs["cwd"], run_bruno.ROOT / "bruno")

    def test_missing_fixture_identity_prevents_execution(self):
        with self.assertRaises(KeyError):
            self.run_fixture("organization_slug=owned\n")

    def test_empty_fixture_identity_prevents_execution(self):
        with self.assertRaises(ValueError):
            self.run_fixture("api_key_token=\norganization_slug=owned\nempty_organization_slug=other\nproject_slug=example\n")

    def test_cli_pin_matches_toolchain(self):
        package = json.loads((run_bruno.ROOT / "bruno/package.json").read_text())
        versions = (run_bruno.ROOT / "ci/toolchain/versions.env").read_text().splitlines()
        self.assertIn("BRUNO_CLI_VERSION=" + package["devDependencies"]["@usebruno/cli"], versions)
