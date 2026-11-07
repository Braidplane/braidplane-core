import pathlib
import os
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("worktree-add.sh").resolve()


class WorktreeAddTest(unittest.TestCase):
    def test_create_reuse_and_reject_invalid_name(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory) / "repo"
            repo.mkdir()

            def git(*args):
                return subprocess.run(["git", *args], cwd=repo, check=True, capture_output=True)

            git("init")
            git("-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
                "commit", "--allow-empty", "-m", "initial")
            (repo / "scripts").mkdir()
            shutil.copy(SCRIPT, repo / "scripts")
            shutil.copy(SCRIPT.parent.parent / "Taskfile.yml", repo)

            def create(name):
                return subprocess.run(["bash", str(SCRIPT), name], cwd=repo, capture_output=True)

            result = subprocess.run(["task", "worktree-create", "name=feature"],
                                    cwd=repo, capture_output=True,
                                    env=os.environ | {"WORKTREE_NAME": "other"})
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertTrue((repo.parent / "feature" / ".git").is_file())
            self.assertFalse((repo.parent / "other").exists())
            result = subprocess.run(["task", "worktree-create", "name=$(touch injected)"],
                                    cwd=repo, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((repo / "injected").exists())
            self.assertNotEqual(create("feature").returncode, 0)
            git("worktree", "remove", str(repo.parent / "feature"))
            self.assertEqual(create("feature").returncode, 0)
            self.assertNotEqual(create("../escape").returncode, 0)
            self.assertFalse((repo.parent.parent / "escape").exists())


if __name__ == "__main__":
    unittest.main()
