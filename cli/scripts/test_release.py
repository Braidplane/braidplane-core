from pathlib import Path
import os
import subprocess
import sys
import tarfile
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("release.py")
COMMIT = "1" * 40


class ReleaseTest(unittest.TestCase):
    def setUp(self):
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        (self.source / "LICENSE").write_text("license\n")
        (self.source / "README.md").write_text("readme\n")
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.com")
        self.git("config", "user.name", "Test")
        self.git("config", "commit.gpgsign", "false")
        self.git("add", ".")
        self.git("commit", "-qm", "canonical cli tree")
        tree = self.git("rev-parse", "HEAD^{tree}")
        (self.source / "canonical-source.json").write_text(
            '{"canonical_commit":"' + COMMIT + '","cli_tree":"' + tree
            + '","release_version":"v1.2.3-rc.1"}\n'
        )
        self.git("add", "canonical-source.json")
        self.git("commit", "-qm", "public snapshot")
        self.git("tag", "v1.2.3-rc.1")
        tools = self.root / "bin"
        tools.mkdir()
        goroot = self.root / "goroot"
        goroot.mkdir()
        (goroot / "LICENSE").write_text("Go license\n")
        module = self.root / "module"
        module.mkdir()
        self.module = module
        (module / "pkg" / "internal").mkdir(parents=True)
        (module / "LICENSE.txt").write_text("Dependency license\n")
        fake_go = tools / "go"
        fake_go.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, pathlib, sys\n"
            f"goroot = {str(goroot)!r}\n"
            f"module = {str(module)!r}\n"
            "if sys.argv[1:3] == ['env', 'GOROOT']:\n"
            "    print(goroot)\n"
            "elif sys.argv[1] == 'list':\n"
            "    print(json.dumps({'ImportPath': 'example.test/dependency/pkg', "
            "'Dir': str(pathlib.Path(module) / 'pkg' / 'internal'), "
            "'Module': {'Path': 'example.test/dependency', 'Version': 'v1.0.0', 'Dir': module}}))\n"
            "else:\n"
            "    out = pathlib.Path(sys.argv[sys.argv.index('-o') + 1])\n"
            "    out.write_bytes((os.environ['GOOS'] + '/' + os.environ['GOARCH']).encode())\n"
        )
        fake_go.chmod(0o755)
        self.env["PATH"] = str(tools) + os.pathsep + self.env["PATH"]

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.run(
            ["git", "-C", self.source, *args], check=True, capture_output=True, text=True, env=self.env
        ).stdout.strip()

    def run_release(self, dist, tag="v1.2.3-rc.1"):
        return subprocess.run(
            [sys.executable, SCRIPT, "--source", self.source, "--dist", dist, "--tag", tag],
            env=self.env,
            capture_output=True,
            text=True,
        )

    def test_build_is_reproducible_for_all_targets(self):
        first, second = self.root / "first", self.root / "second"
        results = [self.run_release(first), self.run_release(second)]
        self.assertTrue(all(result.returncode == 0 for result in results), results)
        self.assertEqual(
            {path.name: path.read_bytes() for path in first.iterdir()},
            {path.name: path.read_bytes() for path in second.iterdir()},
        )
        self.assertEqual(len(list(first.glob("*.tar.gz"))), 4)
        self.assertEqual(len(list(first.glob("*.zip"))), 2)
        self.assertEqual(len((first / "checksums.txt").read_text().splitlines()), 6)
        archive = next(first.glob("*.tar.gz"))
        with tarfile.open(archive) as contents:
            self.assertIn("THIRD_PARTY_NOTICES.txt", contents.getnames())
            notices = contents.extractfile("THIRD_PARTY_NOTICES.txt").read().decode()
        self.assertIn("example.test/dependency v1.0.0", notices)
        self.assertNotIn(str(self.root), notices)

    def test_requires_matching_release_provenance(self):
        mismatch = self.run_release(self.root / "mismatch", "v1.2.4")
        self.assertNotEqual(mismatch.returncode, 0)
        self.assertIn("release_version does not match", mismatch.stderr)
        (self.source / "canonical-source.json").unlink()
        missing = self.run_release(self.root / "missing")
        self.assertNotEqual(missing.returncode, 0)
        self.assertIn("missing or invalid", missing.stderr)

    def test_includes_notices_in_selected_package_ancestry(self):
        (self.module / "pkg" / "LICENSE").write_text("Nested package license\n")
        (self.module / "pkg" / "notice_response.go").write_text("package protocol\n")
        sibling = self.module / "unselected"
        sibling.mkdir()
        (sibling / "LICENSE").write_text("Unselected sibling license\n")
        dist = self.root / "nested-notices"
        result = self.run_release(dist)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(next(dist.glob("*.tar.gz"))) as archive:
            notices = archive.extractfile("THIRD_PARTY_NOTICES.txt").read().decode()
        self.assertIn("--- pkg/LICENSE ---", notices)
        self.assertIn("Nested package license", notices)
        self.assertNotIn("Unselected sibling license", notices)
        self.assertNotIn("package protocol", notices)
        self.assertNotIn(str(self.root), notices)

    def test_rejects_package_directory_outside_its_module(self):
        fake_go = self.root / "bin" / "go"
        fake_go.write_text(fake_go.read_text().replace(
            "'Dir': str(pathlib.Path(module) / 'pkg' / 'internal')",
            "'Dir': str(pathlib.Path(module).parent)",
        ))
        result = self.run_release(self.root / "outside-module")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("outside its module", result.stderr)

    def test_includes_standard_library_package_ancestry(self):
        goroot = self.root / "goroot"
        package = goroot / "src" / "vendor" / "example"
        package.mkdir(parents=True)
        (package.parent / "LICENSE").write_text("Go vendored license\n")
        fake_go = self.root / "bin" / "go"
        fake_go.write_text(fake_go.read_text().replace(
            "elif sys.argv[1] == 'list':\n",
            "elif sys.argv[1] == 'list':\n"
            f"    print(json.dumps({{'Standard': True, 'Dir': {str(package)!r}}}))\n",
        ))
        dist = self.root / "stdlib-notices"
        result = self.run_release(dist)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(next(dist.glob("*.tar.gz"))) as archive:
            notices = archive.extractfile("THIRD_PARTY_NOTICES.txt").read().decode()
        self.assertIn("--- src/vendor/LICENSE ---", notices)
        self.assertIn("Go vendored license", notices)
        self.assertNotIn(str(self.root), notices)

    def test_preserves_supplementary_and_sidecar_legal_texts(self):
        documents = {
            "THIRD-PARTY-NOTICES": "Copied-code attribution and complete third-party terms\n",
            "PATENTS": "Additional patent rights grant\n",
            "AUTHORS": "Additional copyright holders\n",
            "pkg/file.go.license": "SPDX source-file attribution\n",
        }
        for name, content in documents.items():
            (self.module / name).write_text(content)
        (self.module / "checklicense.sh").write_text("not a legal document\n")
        dist = self.root / "supplementary-notices"
        result = self.run_release(dist)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(next(dist.glob("*.tar.gz"))) as archive:
            notices = archive.extractfile("THIRD_PARTY_NOTICES.txt").read().decode()
        for name, content in documents.items():
            self.assertIn(f"--- {name} ---", notices)
            self.assertIn(content, notices)
        self.assertNotIn("not a legal document", notices)

    def test_retains_source_header_attribution_without_implementation(self):
        path = self.module / "pkg/internal/copied.go"
        path.write_text("/* Copyright 2010 Additional Author\nPermission: synthetic fixture terms\n*/\npackage fixture\nvar implementation = 42\n")
        fake_go = self.root / "bin/go"
        fake_go.write_text(fake_go.read_text().replace(
            "'ImportPath': 'example.test/dependency/pkg',",
            "'ImportPath': 'example.test/dependency/pkg', 'GoFiles': ['copied.go'],",
        ))
        dist = self.root / "source-headers"
        result = self.run_release(dist)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(next(dist.glob("*.tar.gz"))) as archive:
            notices = archive.extractfile("THIRD_PARTY_NOTICES.txt").read().decode()
        self.assertIn("source header: pkg/internal/copied.go", notices)
        self.assertIn("Copyright 2010 Additional Author", notices)
        self.assertIn("Permission: synthetic fixture terms", notices)
        self.assertNotIn("var implementation", notices)
        self.assertNotIn("package fixture", notices)

    def test_refuses_source_header_path_outside_module(self):
        (self.root / "outside.go").write_text("// Copyright synthetic private fixture\npackage fixture\n")
        fake_go = self.root / "bin/go"
        fake_go.write_text(fake_go.read_text().replace(
            "'ImportPath': 'example.test/dependency/pkg',",
            "'ImportPath': 'example.test/dependency/pkg', 'GoFiles': ['../../../outside.go'],",
        ))
        result = self.run_release(self.root / "outside-source")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("source file is outside", result.stderr)
        self.assertNotIn("synthetic private fixture", result.stdout + result.stderr)

    def test_attribution_does_not_replace_missing_license_documents(self):
        (self.module / "LICENSE.txt").unlink()
        (self.module / "AUTHORS").write_text("Synthetic copyright holder\n")
        result = self.run_release(self.root / "authors-without-license")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("dependency has no LICENSE", result.stderr)

    def test_rejects_non_object_provenance(self):
        for value in ("null", "0", "[]", '[{}]'):
            with self.subTest(value=value):
                (self.source / "canonical-source.json").write_text(value)
                result = self.run_release(self.root / "invalid")
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("must contain release provenance", result.stderr)
                self.assertNotIn("Traceback", result.stderr)

    def test_rejects_local_dependency_replacement(self):
        fake_go = self.root / "bin" / "go"
        script = fake_go.read_text()
        script = script.replace("'Version': 'v1.0.0',", "'Version': 'v1.0.0', 'Replace': {'Path': '../local', 'Dir': module},")
        fake_go.write_text(script)
        result = self.run_release(self.root / "local-replacement")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("dependency module identity is incomplete", result.stderr)

    def test_refuses_existing_distribution_directory(self):
        dist = self.root / "existing"
        dist.mkdir()
        result = self.run_release(dist)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("already exists", result.stderr)

    def test_refuses_dependency_without_notice(self):
        (self.module / "LICENSE.txt").unlink()
        result = self.run_release(self.root / "missing-license")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("dependency has no LICENSE", result.stderr)

    def test_rejects_dirty_or_tree_mismatched_source(self):
        (self.source / "untracked.txt").write_text("changed\n")
        dirty = self.run_release(self.root / "dirty")
        self.assertNotEqual(dirty.returncode, 0)
        self.assertIn("tracked or untracked changes", dirty.stderr)
        (self.source / "untracked.txt").unlink()

        (self.source / "README.md").write_text("different\n")
        self.git("add", "README.md")
        self.git("commit", "-qm", "different public source")
        self.git("tag", "-f", "v1.2.3-rc.1")
        mismatch = self.run_release(self.root / "tree-mismatch")
        self.assertNotEqual(mismatch.returncode, 0)
        self.assertIn("does not match canonical cli_tree", mismatch.stderr)


if __name__ == "__main__":
    unittest.main()
