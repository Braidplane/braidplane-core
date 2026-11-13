import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]


class GoTestGate(unittest.TestCase):
    def test_package_discovery_must_succeed_and_not_be_empty(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("scripts", "ci/toolchain", "bin"):
                (root / name).mkdir(parents=True)
            for name in ("Taskfile.yml", "scripts/go-toolchain.sh", "scripts/go-test-packages.sh",
                         "scripts/cache-dir.sh", "scripts/require-go-tests.py", "scripts/shard-go-tests.py",
                         "ci/toolchain/versions.env", "ci/go-e2e-durations.json"):
                shutil.copyfile(ROOT / name, root / name)
            (root / "scripts/node-toolchain.sh").write_text("#!/bin/bash\nexit 0\n")
            go = root / "bin/go"
            go.write_text(r"""#!/bin/bash
case "$1 ${2:-}" in
  'env GOVERSION') echo go1.26.10 ;;
  'env GOOS') echo linux ;;
  'env GOARCH') echo amd64 ;;
  tool*) exit 0 ;;
  list*)
    case "$TEST_MODE" in fail) exit 42 ;; empty) exit 0 ;; *) printf '%s\n' $TEST_PACKAGES ;; esac ;;
  test*)
    printf '%s\n' "$@" > "$TEST_MARKER"
    for package in $TEST_PACKAGES; do
      if [[ "$TEST_MODE" = skipped ]]; then
        printf '{"Action":"skip","Test":"TestFixture","Package":"%s"}\n' "$package"
      elif [[ "$TEST_MODE" != no_tests ]]; then
        printf '{"Action":"pass","Test":"TestFixture","Package":"%s"}\n' "$package"
      fi
      printf '{"Action":"pass","Package":"%s"}\n' "$package"
      if [[ "$TEST_MODE" = missing ]]; then break; fi
    done
    if [[ "$TEST_MODE" = failed ]]; then exit 42; fi ;;
  *) exit 99 ;;
esac
""")
            go.chmod(0o755)
            marker = root / "test-invoked"
            env = os.environ | {"PATH": f"{root / 'bin'}:{os.environ['PATH']}",
                                "XDG_CACHE_HOME": str(root / "cache"), "TEST_MARKER": str(marker), "CI": "true"}
            for task in ("test-go", "test-go-e2e"):
                packages = ["example/one", "example/two", "example/new-package"]
                if task == "test-go-e2e":
                    packages = [package + "/e2e" for package in packages]
                modes = ["fail", "empty", "skipped", "failed", "no_tests", "ok"]
                if task == "test-go-e2e":
                    modes.append("missing")
                for mode in modes:
                    with self.subTest(task=task, mode=mode):
                        marker.unlink(missing_ok=True)
                        result = subprocess.run(["task", task], cwd=root,
                                                env=env | {"TEST_MODE": mode, "TEST_PACKAGES": " ".join(packages)},
                                                capture_output=True, text=True)
                        if mode == "ok":
                            self.assertEqual(result.returncode, 0, result.stderr)
                            for package in packages:
                                self.assertIn(package + "\n", marker.read_text())
                            if task == "test-go-e2e":
                                self.assertNotIn("-short", marker.read_text())
                                self.assertNotIn("-run", marker.read_text())
                        else:
                            self.assertNotEqual(result.returncode, 0, result.stderr)
                            self.assertEqual(marker.exists(), mode not in ("fail", "empty"))

    def test_e2e_shards_are_deterministic_disjoint_and_exhaustive(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "scripts").mkdir()
            shutil.copyfile(ROOT / "scripts/go-test-packages.sh", root / "scripts/go-test-packages.sh")
            shutil.copyfile(ROOT / "scripts/shard-go-tests.py", root / "scripts/shard-go-tests.py")
            (root / "ci").mkdir()
            (root / "ci/go-e2e-durations.json").write_text('{"seconds": {}}')
            (root / "scripts/go-toolchain.sh").write_text(
                '#!/bin/bash\nprintf "%s\\n" $TEST_PACKAGES\n')
            packages = [f"example/package-{index:02}/e2e" for index in range(17)]
            packages.append("example/nested/e2e/fixture")
            def discover(index, count, discovered):
                return subprocess.run(["bash", "scripts/go-test-packages.sh", "e2e"], cwd=root,
                                      env=os.environ | {"GO_E2E_SHARD_INDEX": str(index),
                                                        "GO_E2E_SHARD_COUNT": str(count),
                                                        "TEST_PACKAGES": " ".join(discovered)},
                                      capture_output=True, text=True)
            shards = []
            for index in range(4):
                result = discover(index, 4, packages[::-1] + ["example/unit"])
                self.assertEqual(result.returncode, 0, result.stderr)
                selected = result.stdout.splitlines()
                self.assertEqual(selected, sorted(packages)[index::4])
                self.assertEqual(result.stdout, discover(index, 4, packages).stdout)
                shards.extend(selected)
            self.assertEqual(sorted(shards), sorted(packages))
            self.assertEqual(len(shards), len(set(shards)))
            for index, count in ((-1, 4), (4, 4), (0, 0), ("bad", 4), (0, "bad")):
                result = discover(index, count, packages)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")

    def test_container_build_failure_stops_the_task(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shutil.copyfile(ROOT / "Taskfile.yml", root / "Taskfile.yml")
            docker = root / "docker"
            docker.write_text('#!/bin/bash\necho "$3" >> "$CALLS"\nif [[ "$3" = "$FAIL_TARGET" ]]; then exit 42; fi\n')
            docker.chmod(0o755)
            calls = root / "calls"
            env = os.environ | {"PATH": f"{root}:{os.environ['PATH']}", "CALLS": str(calls)}
            targets = ["backend", "frontend", "migrate", "cli"]
            for failed in targets + [""]:
                with self.subTest(failed=failed):
                    calls.unlink(missing_ok=True)
                    result = subprocess.run(["task", "containers"], cwd=root,
                                            env=env | {"FAIL_TARGET": failed}, capture_output=True, text=True)
                    expected = targets[:targets.index(failed) + 1] if failed else targets
                    self.assertEqual(calls.read_text().splitlines(), expected)
                    self.assertEqual(result.returncode == 0, not failed, result.stderr)


if __name__ == "__main__":
    unittest.main()
