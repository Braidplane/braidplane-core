import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent


class BrunoEnvironmentTest(unittest.TestCase):
    def test_shell_ports_override_defaults_without_loading_retained_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".env").write_text("exit 99\n")
            (root / ".worktree-env").write_text("exit 98\n")
            env = os.environ | {"SERVER_PORT": "43001", "DB_PORT": "45432",
                                "POSTGRES_CONNECTOR_PORT": "45433",
                                "MAILPIT_WEB_PORT": "48025"}
            subprocess.run(["bash", str(ROOT / "scripts/gen-bruno-env.sh"), directory],
                           env=env, check=True, capture_output=True, text=True)
            collections = root / "api-schema/bruno"
            files = list(collections.glob("*/environments/Worktree.bru"))
            self.assertEqual(len(files), 5)
            for file in files:
                text = file.read_text()
                self.assertIn("baseUrl: http://127.0.0.1:43001/api", text)
                self.assertIn("mailpitUrl: http://localhost:48025", text)
            source = (collections / "data-sources-verification/environments/Worktree.bru").read_text()
            tenant = (collections / "multi-tenant-isolation/environments/Worktree.bru").read_text()
            self.assertIn("postgresPort: 45433", source)
            self.assertIn("postgresPort: 45432", tenant)
