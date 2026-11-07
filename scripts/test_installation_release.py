import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from scripts.installation.release import IMAGES, compose_inputs, read_manifest, validate


def manifest():
    return {
        "version": "v0.1.0-alpha.1", "core_revision": "a" * 40,
        "platform": "linux/amd64",
        "images": {name: f"registry.example.test/braidplane/{name}@sha256:" + "b" * 64 for name in IMAGES},
    }


class ReleaseTest(unittest.TestCase):
    def test_compose_inputs_are_exact_and_inert(self):
        value = manifest()
        expected = "BRAIDPLANE_VERSION=v0.1.0-alpha.1\nBRAIDPLANE_PLATFORM=linux/amd64\n"
        expected += "".join(f"{name.upper()}_IMAGE={value['images'][name]}\n" for name in IMAGES)
        self.assertEqual(compose_inputs(value), expected)
        value["platform"] = "linux/arm64"
        validate(value)

    def test_untrusted_inputs_fail_closed(self):
        changes = [
            ("version", "v1.0.0\nEVIL=yes"), ("core_revision", "main"),
            ("platform", "linux/386"), ("extra", "unknown"),
            ("images", {}), ("images", []), ("version", None),
        ]
        for key, value in changes:
            with self.subTest(key=key, value=value):
                candidate = manifest()
                candidate[key] = value
                with self.assertRaises(ValueError):
                    validate(candidate)
        for reference in ["repo:latest", "repo@sha256:abc", "repo:tag@sha256:" + "a" * 64,
                          "$(id)@sha256:" + "a" * 64, "repo\nEVIL=yes", None]:
            with self.subTest(reference=reference):
                candidate = copy.deepcopy(manifest())
                candidate["images"]["backend"] = reference
                with self.assertRaises(ValueError):
                    validate(candidate)

    def test_duplicate_keys_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release.json"
            path.write_text('{"version":"v1.0.0","version":"v2.0.0"}')
            with self.assertRaisesRegex(ValueError, "duplicate"):
                read_manifest(path)

    def test_cli_preserves_existing_output(self):
        with tempfile.TemporaryDirectory() as directory:
            source, target = [Path(directory) / name for name in ("release.json", "release.env")]
            source.write_text(json.dumps(manifest()))
            command = [sys.executable, "-m", "scripts.installation.release", str(source), str(target)]
            first = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(first.returncode, 0, first.stderr)
            expected = target.read_bytes()
            second = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(second.returncode, 1)
            self.assertEqual(target.read_bytes(), expected)


if __name__ == "__main__":
    unittest.main()
