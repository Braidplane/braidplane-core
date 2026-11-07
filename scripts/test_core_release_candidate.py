import copy
import unittest

from scripts.core_release_candidate import candidate
from scripts.qualify_core_images import BINARIES
from scripts.test_installation_release import manifest


class CoreReleaseCandidateTest(unittest.TestCase):
    def setUp(self):
        self.release = manifest()
        self.metadata = {name: {"containerimage.digest": "sha256:" + "a" * 64} for name in BINARIES}
        self.args = ("example/core", self.release["core_revision"], self.release["version"],
                     self.release["platform"], self.release["images"]["edge"], self.release["images"]["postgres"])

    def test_candidate_uses_build_digests_and_third_party_inputs(self):
        result = candidate(*self.args, self.metadata)
        self.assertEqual(result["images"]["backend"], "ghcr.io/example/core-backend@sha256:" + "a" * 64)
        self.assertEqual(result["images"]["edge"], self.release["images"]["edge"])
        self.assertEqual(result["core_revision"], self.release["core_revision"])

    def test_invalid_or_incomplete_metadata_fails(self):
        for field, value in (("backend", {}), ("frontend", {"containerimage.digest": "sha256:bad"}),
                             ("cli", None)):
            metadata = copy.deepcopy(self.metadata)
            metadata[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                candidate(*self.args, metadata)
        metadata = copy.deepcopy(self.metadata)
        del metadata["migrate"]
        with self.assertRaises(ValueError):
            candidate(*self.args, metadata)
        with self.assertRaises(ValueError):
            candidate("example/../other", *self.args[1:], self.metadata)


if __name__ == "__main__":
    unittest.main()
