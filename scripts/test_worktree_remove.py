import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("worktree-remove.sh").resolve()


class WorktreeRemoveTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.repo = pathlib.Path(self.directory.name) / "repo"
        self.repo.mkdir()
        self.git("init")
        (self.repo / "tracked").write_text("original\n")
        (self.repo / ".gitignore").write_text("ignored/\n.worktree/\n")
        self.git("add", ".")
        self.git("-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                 "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
                 "commit", "-m", "initial")
        (self.repo / "scripts").mkdir()
        shutil.copy(SCRIPT, self.repo / "scripts")
        shutil.copy(SCRIPT.parent.parent / "Taskfile.yml", self.repo)
        self.target = self.repo.parent / "feature"
        self.git("worktree", "add", "-b", "feature", str(self.target))

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.repo, check=True, capture_output=True)

    def remove(self, name="feature", cwd=None):
        return subprocess.run(["bash", str(SCRIPT), name], cwd=cwd or self.repo,
                              capture_output=True)

    def test_task_removes_only_named_worktree_and_retains_branch(self):
        (self.target / "ignored").mkdir()
        (self.target / "ignored" / "build-output").touch()
        other = self.repo.parent / "other"
        self.git("worktree", "add", "-b", "other", str(other))
        result = subprocess.run(["task", "worktree-remove", "name=feature"],
                                cwd=self.repo, capture_output=True,
                                env=os.environ | {"WORKTREE_NAME": "other"})
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertFalse(self.target.exists())
        self.assertNotIn(str(self.target), self.git("worktree", "list", "--porcelain").stdout.decode())
        self.git("show-ref", "--verify", "refs/heads/feature")
        self.assertTrue((other / "tracked").is_file())
        self.assertTrue((self.repo / "tracked").is_file())

    def test_refuses_dirty_locked_and_retained_runtime_state(self):
        for dirty in ("tracked", "untracked"):
            with self.subTest(dirty=dirty):
                (self.target / dirty).write_text("preserve me\n")
                self.assertNotEqual(self.remove().returncode, 0)
                self.assertEqual((self.target / dirty).read_text(), "preserve me\n")
                if dirty == "tracked":
                    (self.target / dirty).write_text("original\n")
                else:
                    (self.target / dirty).unlink()
        self.git("worktree", "lock", str(self.target))
        self.assertNotEqual(self.remove().returncode, 0)
        self.git("worktree", "unlock", str(self.target))
        retained = self.target / ".worktree" / "community"
        retained.mkdir(parents=True)
        (retained / "keys.env").write_text("synthetic retained keys\n")
        self.assertNotEqual(self.remove().returncode, 0)
        self.assertTrue((retained / "keys.env").is_file())

    def test_refuses_detached_head_until_commit_is_preserved_on_branch(self):
        subprocess.run(["git", "checkout", "--detach"], cwd=self.target,
                       check=True, capture_output=True)
        subprocess.run(["git", "-c", "user.name=Test", "-c",
                        "user.email=test@example.invalid", "-c", "commit.gpgsign=false",
                        "commit", "--allow-empty", "-m", "detached work"],
                       cwd=self.target, check=True, capture_output=True)
        result = self.remove()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"detached", result.stderr)
        self.assertTrue(self.target.exists())
        subprocess.run(["git", "switch", "-c", "preserved"], cwd=self.target,
                       check=True, capture_output=True)
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=self.target,
                              check=True, capture_output=True).stdout.strip()
        result = self.remove()
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertEqual(self.git("rev-parse", "refs/heads/preserved").stdout.strip(), head)

    def test_refuses_staged_changes_and_removal_from_linked_checkout(self):
        (self.target / "tracked").write_text("staged work\n")
        subprocess.run(["git", "add", "tracked"], cwd=self.target,
                       check=True, capture_output=True)
        self.assertNotEqual(self.remove().returncode, 0)
        self.assertEqual((self.target / "tracked").read_text(), "staged work\n")
        other = self.repo.parent / "other"
        self.git("worktree", "add", "-b", "other", str(other))
        result = self.remove("other", cwd=self.target)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"main worktree", result.stderr)
        self.assertTrue(other.exists())

    def test_refuses_paths_current_primary_unregistered_and_symlinks(self):
        for name in ("../feature", str(self.target), "--force", "missing", "repo"):
            with self.subTest(name=name):
                self.assertNotEqual(self.remove(name).returncode, 0)
        self.assertNotEqual(self.remove(cwd=self.target).returncode, 0)
        self.assertNotEqual(self.remove("repo", cwd=self.target).returncode, 0)
        unregistered = self.repo.parent / "unregistered"
        unregistered.mkdir()
        (unregistered / "keep").touch()
        self.assertNotEqual(self.remove("unregistered").returncode, 0)
        self.assertTrue((unregistered / "keep").is_file())
        link = self.repo.parent / "linked"
        link.symlink_to(self.target, target_is_directory=True)
        self.assertNotEqual(self.remove("linked").returncode, 0)
        result = subprocess.run(["task", "worktree-remove", "name=$(touch injected)"],
                                cwd=self.repo, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.repo / "injected").exists())
        self.assertTrue((self.target / "tracked").is_file())


if __name__ == "__main__":
    unittest.main()
