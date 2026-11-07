import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.qualify_core_images import BINARIES, qualify
from scripts.test_installation_release import manifest


class QualifyImagesTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        self.checkout, self.build, self.images = (root / name for name in ("checkout", "build", "images"))
        self.release = manifest()
        self.release["images"] = {name: f"registry.example.test/braidplane/{name}@sha256:" + "b" * 64
                                  for name in self.release["images"]}
        self.removed = []
        self.identity = {"version": 1, "digest": "a" * 64}
        self.file(self.checkout / "postgres/migrations/manifest.sha256", f"1 {'a' * 64}\n".encode())
        self.file(self.checkout / "postgres/migrations/1_init.up.sql", b"select 1;")
        self.file(self.checkout / "brand/icons/icon.svg", b"icon")
        self.file(self.build / "frontend-assets/style.css", b"css")
        for component, binaries in BINARIES.items():
            for name in binaries:
                self.file(self.build / "out" / name, name.encode())
                self.file(self.images / component / name, name.encode())
                (self.images / component / name).chmod(0o755)
        shutil.copytree(self.checkout / "postgres/migrations", self.images / "migrate/postgres/migrations")
        shutil.copytree(self.checkout / "brand/icons", self.images / "frontend/app/brand/icons")
        shutil.copytree(self.build / "frontend-assets", self.images / "frontend/app/frontend/assets")

    @staticmethod
    def file(path, data):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)

    def output(self, args, **kwargs):
        if args[0] == "git":
            return self.release["core_revision"] + "\n" if "rev-parse" in args else b""
        if args[1:3] == ["image", "inspect"]:
            return "linux/amd64\n"
        if args[1] == "create":
            return next(name for name, image in self.release["images"].items() if image == args[-1]) + "\n"
        if args[1] == "run":
            return json.dumps(self.identity).encode()
        raise AssertionError(args)

    def run_command(self, args, **kwargs):
        if args[1] == "cp":
            component, name = args[2].split(":", 1)
            source, target = self.images / component / name.lstrip("/"), Path(args[3])
            if source.is_dir():
                shutil.copytree(source, target)
            else:
                shutil.copy2(source, target)
        elif args[1] == "rm":
            self.removed.append(args[2])
        elif args[1] != "pull":
            raise AssertionError(args)
        return subprocess.CompletedProcess(args, 0)

    def execute(self):
        with patch("scripts.qualify_core_images.subprocess.check_output", side_effect=self.output), \
             patch("scripts.qualify_core_images.subprocess.run", side_effect=self.run_command):
            qualify(self.release, self.checkout, self.build)

    def test_exact_payloads_pass_and_containers_are_removed(self):
        self.execute()
        self.assertEqual(self.removed, list(BINARIES))

    def test_changed_binary_and_catalog_fail_closed(self):
        self.file(self.images / "backend/braidplane-server", b"changed")
        with self.assertRaisesRegex(ValueError, "binary differs"):
            self.execute()
        self.assertEqual(self.removed, ["backend"])
        self.file(self.images / "backend/braidplane-server", b"braidplane-server")
        self.identity["digest"] = "b" * 64
        with self.assertRaisesRegex(ValueError, "embedded migration catalog"):
            self.execute()
        self.assertEqual(self.removed[-1], "migrate")

    def test_nonexecutable_image_binary_fails(self):
        (self.images / "backend/braidplane-server").chmod(0o644)
        with self.assertRaisesRegex(ValueError, "binary differs"):
            self.execute()
        self.assertEqual(self.removed, ["backend"])

    def test_changed_migration_and_frontend_assets_fail(self):
        self.file(self.images / "frontend/app/frontend/assets/style.css", b"changed")
        with self.assertRaisesRegex(ValueError, "frontend assets"):
            self.execute()
        self.file(self.images / "frontend/app/frontend/assets/style.css", b"css")
        self.file(self.images / "migrate/postgres/migrations/1_init.up.sql", b"changed")
        with self.assertRaisesRegex(ValueError, "migrations differ"):
            self.execute()

    def test_wrong_revision_or_platform_stops_before_qualification(self):
        original = self.output

        def wrong_revision(args, **kwargs):
            if args[0] == "git" and "rev-parse" in args:
                return "c" * 40 + "\n"
            return original(args, **kwargs)

        with patch("scripts.qualify_core_images.subprocess.check_output", side_effect=wrong_revision), \
             patch("scripts.qualify_core_images.subprocess.run", side_effect=self.run_command):
            with self.assertRaisesRegex(ValueError, "clean and match"):
                qualify(self.release, self.checkout, self.build)
        self.assertEqual(self.removed, [])

        def wrong_platform(args, **kwargs):
            if args[0:3] == ["docker", "image", "inspect"]:
                return "linux/arm64\n"
            return original(args, **kwargs)

        with patch("scripts.qualify_core_images.subprocess.check_output", side_effect=wrong_platform), \
             patch("scripts.qualify_core_images.subprocess.run", side_effect=self.run_command):
            with self.assertRaisesRegex(ValueError, "platform differs"):
                qualify(self.release, self.checkout, self.build)
        self.assertEqual(self.removed, [])


if __name__ == "__main__":
    unittest.main()
