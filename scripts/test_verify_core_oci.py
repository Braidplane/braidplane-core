import hashlib
import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path

from scripts.verify_core_oci import verify_archive


def descriptor(data, media_type):
    return {"mediaType": media_type, "digest": "sha256:" + hashlib.sha256(data).hexdigest(), "size": len(data)}


def fixture(path, *, corrupt=False, architecture="amd64", duplicate=False):
    config = json.dumps({"os": "linux", "architecture": architecture}).encode()
    layer = b"synthetic layer"
    config_descriptor = descriptor(config, "application/vnd.oci.image.config.v1+json")
    layer_descriptor = descriptor(layer, "application/vnd.oci.image.layer.v1.tar+gzip")
    image = json.dumps({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
                        "config": config_descriptor, "layers": [layer_descriptor]}).encode()
    image_descriptor = descriptor(image, "application/vnd.oci.image.manifest.v1+json")
    index = json.dumps({"schemaVersion": 2, "manifests": [image_descriptor]}).encode()
    with tarfile.open(path, "w") as archive:
        files = [("oci-layout", b'{"imageLayoutVersion":"1.0.0"}'), ("index.json", index),
                 ("blobs/sha256/" + image_descriptor["digest"][7:], image),
                 ("blobs/sha256/" + config_descriptor["digest"][7:], config),
                 ("blobs/sha256/" + layer_descriptor["digest"][7:], b"x" * len(layer) if corrupt else layer)]
        if duplicate:
            files.append(files[0])
        for name, data in files:
            info = tarfile.TarInfo(name)
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))
    return image_descriptor["digest"]


class VerifyCoreOCITest(unittest.TestCase):
    def test_accepts_complete_matching_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "image.tar"
            digest = fixture(path)
            verify_archive(path, digest, "linux/amd64")

    def test_rejects_wrong_digest_corrupted_blob_platform_and_duplicates(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "image.tar"
            for options in ({"corrupt": True}, {"architecture": "arm64"}, {"duplicate": True}):
                digest = fixture(path, **options)
                with self.subTest(options=options), self.assertRaises(ValueError):
                    verify_archive(path, digest, "linux/amd64")
            digest = fixture(path)
            with self.assertRaises(ValueError):
                verify_archive(path, "sha256:" + "0" * 64, "linux/amd64")


if __name__ == "__main__":
    unittest.main()
