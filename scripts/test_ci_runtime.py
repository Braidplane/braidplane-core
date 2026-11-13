import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class RuntimeSuiteTest(unittest.TestCase):
    def test_suites_are_separate_and_failure_still_cleans_up(self):
        for suite, failed in (("browser", ""), ("bruno", ""), ("bruno", "bruno-ci")):
            with self.subTest(suite=suite, failed=failed), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "scripts").mkdir()
                (root / "bin").mkdir()
                calls = root / "calls"
                commands = {
                    "docker": 'echo "docker $*" >> "$CALLS"; if [[ "$1" = run ]]; then echo fixture-id; fi',
                    "task": 'echo "task $*" >> "$CALLS"; if [[ "$1" = "$FAILED_TASK" ]]; then exit 42; fi',
                    "ssh-keygen": 'touch "$PWD/.worktree/ci-git-key"; echo synthetic-public-key > "$PWD/.worktree/ci-git-key.pub"',
                    "ssh-keyscan": 'echo synthetic-host-key',
                    "curl": 'test -f .worktree/community/keys.env',
                }
                for name, body in commands.items():
                    path = root / "bin" / name
                    path.write_text("#!/bin/bash\n" + body + "\n")
                    path.chmod(0o755)
                for name in ("go-toolchain.sh", "node-toolchain.sh"):
                    (root / "scripts" / name).write_text('#!/bin/bash\necho "toolchain $*" >> "$CALLS"\n')
                (root / "scripts/dev.py").write_text(
                    'from pathlib import Path\nimport time\n'
                    'p=Path(".worktree/community"); p.mkdir(parents=True)\n'
                    '(p/"keys.env").write_text("ENCRYPTION_KEY_HEX=synthetic\\nEXECUTION_REQUEST_KEY_HEX=synthetic\\n")\n'
                    'while True: time.sleep(1)\n')
                result = subprocess.run(["bash", str(ROOT / "scripts/ci-runtime.sh"), suite],
                    cwd=root, env=os.environ | {"CI": "true", "CI_GIT_IMAGE": "synthetic-fixture",
                        "CALLS": str(calls), "FAILED_TASK": failed,
                        "PATH": f'{root / "bin"}:{os.environ["PATH"]}'},
                    capture_output=True, text=True, timeout=15)
                self.assertEqual(result.returncode, 42 if failed else 0, result.stderr)
                commands = calls.read_text()
                self.assertIn("down --volumes --remove-orphans", commands)
                if suite == "browser":
                    self.assertIn("task test-e2e", commands)
                    self.assertNotIn("task bruno-ci", commands)
                    self.assertNotIn("mysql-connector-test", commands)
                else:
                    self.assertIn("task bruno-ci", commands)
                    self.assertIn("task test-bruno", commands)
                    self.assertNotIn("task test-e2e", commands)
                    self.assertIn("mysql-connector-test", commands)
