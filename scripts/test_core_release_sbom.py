import copy
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.core_release_sbom import BINARIES, SYFT, generate, require_image_manifest, validate_sbom
from scripts.test_installation_release import manifest


def spdx():
    return {"spdxVersion": "SPDX-2.3", "SPDXID": "SPDXRef-DOCUMENT", "documentNamespace": "https://example.test/sbom",
            "packages": [{"SPDXID": "SPDXRef-image", "name": "synthetic-image"},
                         {"SPDXID": "SPDXRef-package", "name": "synthetic-package"}],
            "relationships": [{"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES",
                               "relatedSpdxElement": "SPDXRef-image"},
                              {"spdxElementId": "SPDXRef-image", "relationshipType": "CONTAINS",
                               "relatedSpdxElement": "SPDXRef-package"}]}


def image_manifest():
    return json.dumps({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
                       "config": {"mediaType": "application/vnd.oci.image.config.v1+json"}, "layers": []}).encode()


def release_manifest(raw):
    value = manifest()
    digest = hashlib.sha256(raw).hexdigest()
    value["images"] = {name: reference.split("@sha256:")[0] + "@sha256:" + digest
                       for name, reference in value["images"].items()}
    return value


class CoreReleaseSBOMTest(unittest.TestCase):
    def test_rejects_empty_or_unrelated_inventory(self):
        validate_sbom(spdx())
        for key, value in (("packages", []), ("relationships", []), ("documentNamespace", ""),
                           ("spdxVersion", "SPDX-2.2")):
            document = copy.deepcopy(spdx())
            document[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                validate_sbom(document)
        root_only = spdx()
        root_only["packages"].pop()
        root_only["relationships"].pop()
        with self.assertRaises(ValueError):
            validate_sbom(root_only)

    def test_scans_each_exact_image_with_isolated_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            config, output = root / "config", root / "sboms"
            config.mkdir()
            commands = []
            release = release_manifest(image_manifest())

            def scan(args, check):
                commands.append(args)
                name = args[-1].split("/out/")[1]
                (output / name).write_text(json.dumps(spdx()))

            outputs = [item for _ in BINARIES for item in (image_manifest(), b'{"os":"linux","architecture":"amd64"}')]
            with patch("scripts.core_release_sbom.subprocess.run", side_effect=scan), \
                 patch("scripts.core_release_sbom.subprocess.check_output", side_effect=outputs) as inspect:
                generate(release, output, config)
                self.assertEqual(len(commands), len(BINARIES))
                self.assertEqual(inspect.call_count, 2 * len(BINARIES))
                self.assertEqual(inspect.call_args.kwargs["env"]["DOCKER_CONFIG"], str(config.resolve()))
                for command, component in zip(commands, BINARIES):
                    self.assertIn(SYFT, command)
                    self.assertIn("registry:" + release["images"][component], command)
                    self.assertEqual(command[command.index("--platform") + 1], "linux/amd64")
                    self.assertIn(f"{config.resolve()}:/config:ro", command)
                    self.assertIn("--read-only", command)
                with self.assertRaises(FileExistsError):
                    generate(release, output, config)

    def test_invalid_scanner_output_stops(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            config, output = root / "config", root / "sboms"
            config.mkdir()

            def scan(args, check):
                (output / args[-1].split("/out/")[1]).write_text("{}")

            with patch("scripts.core_release_sbom.subprocess.run", side_effect=scan) as run, \
                 patch("scripts.core_release_sbom.subprocess.check_output", side_effect=[image_manifest(), b'{"os":"linux","architecture":"amd64"}']):
                with self.assertRaises(ValueError):
                    generate(release_manifest(image_manifest()), output, config)
                self.assertEqual(run.call_count, 1)

    def test_rejects_index_and_mismatched_registry_bytes(self):
        raw = json.dumps({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
                          "manifests": []}).encode()
        with patch("scripts.core_release_sbom.subprocess.check_output", return_value=raw):
            with self.assertRaisesRegex(ValueError, "single-platform"):
                require_image_manifest(release_manifest(raw)["images"]["backend"], Path("/tmp"), "linux/amd64")
            with self.assertRaisesRegex(ValueError, "do not match"):
                require_image_manifest(release_manifest(image_manifest())["images"]["backend"], Path("/tmp"), "linux/amd64")

    def test_rejects_wrong_platform(self):
        image = release_manifest(image_manifest())["images"]["backend"]
        with patch("scripts.core_release_sbom.subprocess.check_output", side_effect=[image_manifest(), b'{"os":"linux","architecture":"arm64"}']):
            with self.assertRaisesRegex(ValueError, "platform differs"):
                require_image_manifest(image, Path("/tmp"), "linux/amd64")


if __name__ == "__main__":
    unittest.main()
