import copy
import json
import unittest
from unittest.mock import patch

from scripts.core_release_provenance import predicate
from scripts.test_installation_release import manifest
from scripts.test_verify_core_attestation import DIGEST, PROVENANCE, REPOSITORY, SUBJECT, response
from scripts.verify_core_attestation import verify


def context():
    sha = "a" * 40
    return {"GITHUB_REPOSITORY": REPOSITORY, "GITHUB_WORKFLOW_REF": REPOSITORY + "/.github/workflows/release.yml@refs/heads/main",
            "GITHUB_WORKFLOW_SHA": sha, "GITHUB_SHA": sha, "GITHUB_REF": "refs/heads/main",
            "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1",
            "GITHUB_REPOSITORY_ID": "456", "GITHUB_REPOSITORY_OWNER_ID": "789", "RUNNER_ENVIRONMENT": "github-hosted"}


class CoreReleaseProvenanceTest(unittest.TestCase):
    def test_generated_claim_satisfies_verification_contract(self):
        claim = predicate(manifest(), context(), {"repository": {"full_name": REPOSITORY, "visibility": "public"}})
        definition = claim["buildDefinition"]
        self.assertEqual(definition["buildType"], "https://actions.github.io/buildtypes/workflow/v1")
        self.assertEqual(definition["externalParameters"], {"workflow": {"repository": "https://github.com/" + REPOSITORY,
                         "path": ".github/workflows/release.yml", "ref": "refs/heads/main"}})
        self.assertEqual(definition["internalParameters"]["github"], {"event_name": "workflow_dispatch",
                         "repository_id": "456", "repository_owner_id": "789", "runner_environment": "github-hosted"})
        value = response()
        statement = value[0]["verificationResult"]["statement"]
        statement["predicateType"], statement["predicate"] = PROVENANCE, claim
        with patch("scripts.verify_core_attestation.subprocess.check_output", return_value=json.dumps(value).encode()):
            verify(SUBJECT, DIGEST, REPOSITORY, ".github/workflows/release.yml", "a" * 40, "a" * 40,
                   "123", "1", PROVENANCE, manifest()["version"], manifest()["platform"])

    def test_rejects_untrusted_context(self):
        event = {"repository": {"full_name": REPOSITORY, "visibility": "public"}}
        for key, bad in (("GITHUB_REF", "refs/heads/branch"), ("GITHUB_SHA", "b" * 40),
                         ("GITHUB_WORKFLOW_SHA", "b" * 40), ("GITHUB_EVENT_NAME", "pull_request"),
                         ("GITHUB_RUN_ID", "0"), ("GITHUB_WORKFLOW_REF", "evil"),
                         ("GITHUB_REPOSITORY_ID", "0"), ("GITHUB_REPOSITORY_OWNER_ID", "invalid"),
                         ("RUNNER_ENVIRONMENT", "self-hosted")):
            env = context()
            env[key] = bad
            with self.subTest(key=key), self.assertRaises(ValueError):
                predicate(manifest(), env, event)
        private = copy.deepcopy(event)
        private["repository"]["visibility"] = "private"
        with self.assertRaises(ValueError):
            predicate(manifest(), context(), private)


if __name__ == "__main__":
    unittest.main()
