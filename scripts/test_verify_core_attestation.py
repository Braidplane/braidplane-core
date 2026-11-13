import copy
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.test_core_release_sbom import spdx
from scripts.verify_core_attestation import PROVENANCE, SPDX, verify


DIGEST = "a" * 64
REPOSITORY = "Example/core"
SUBJECT = "oci://ghcr.io/example/core@sha256:" + DIGEST
ARGS = (SUBJECT, DIGEST, REPOSITORY, ".github/workflows/release.yml", "b" * 40, "c" * 40, "123", "1", SPDX, "v1.0.0", "linux/amd64")


def response():
    return [{"verificationResult": {"verifiedTimestamps": [{"timestamp": "synthetic"}],
            "signature": {"certificate": {"sourceRepositoryVisibilityAtSigning": "public",
                          "runInvocationURI": "https://github.com/Example/core/actions/runs/123/attempts/1"}},
            "statement": {"predicateType": SPDX, "subject": [{"digest": {"sha256": DIGEST}}],
                          "predicate": spdx()}}}]


class VerifyAttestationTest(unittest.TestCase):
    def test_verified_subject_and_identity(self):
        raw = json.dumps(response()).encode()
        with patch("scripts.verify_core_attestation.subprocess.check_output", return_value=raw) as gh:
            self.assertEqual(verify(*ARGS), raw)
            command = gh.call_args.args[0]
            self.assertIn("--deny-self-hosted-runners", command)
            self.assertEqual(command[command.index("--source-digest") + 1], "b" * 40)
            self.assertEqual(command[command.index("--signer-digest") + 1], "c" * 40)
            self.assertEqual(command[command.index("--cert-identity") + 1],
                             "https://github.com/Example/core/.github/workflows/release.yml@refs/heads/main")

    def test_wrong_certificate_subject_or_sbom_fails(self):
        for mutation in ("visibility", "run", "attempt", "subject", "timestamp", "sbom"):
            value = copy.deepcopy(response())
            result = value[0]["verificationResult"]
            if mutation == "visibility":
                result["signature"]["certificate"]["sourceRepositoryVisibilityAtSigning"] = "private"
            elif mutation == "run":
                result["signature"]["certificate"]["runInvocationURI"] = "https://github.com/Example/core/actions/runs/124/attempts/1"
            elif mutation == "attempt":
                result["signature"]["certificate"]["runInvocationURI"] = "https://github.com/Example/core/actions/runs/123/attempts/2"
            elif mutation == "subject":
                result["statement"]["subject"][0]["digest"]["sha256"] = "d" * 64
            elif mutation == "timestamp":
                result["verifiedTimestamps"] = []
            else:
                result["statement"]["predicate"]["packages"] = []
            with self.subTest(mutation=mutation), patch("scripts.verify_core_attestation.subprocess.check_output", return_value=json.dumps(value).encode()):
                with self.assertRaises(ValueError):
                    verify(*ARGS)

    def test_rejects_mismatched_input_before_gh(self):
        with patch("scripts.verify_core_attestation.subprocess.check_output") as gh:
            with self.assertRaises(ValueError):
                verify(SUBJECT, "d" * 64, *ARGS[2:])
            with self.assertRaises(ValueError):
                verify(*ARGS[:3], "../release.yml", *ARGS[4:])
            gh.assert_not_called()

    def test_provenance_requires_mapping(self):
        value = response()
        statement = value[0]["verificationResult"]["statement"]
        statement["predicateType"] = PROVENANCE
        statement["predicate"] = "not a predicate"
        with patch("scripts.verify_core_attestation.subprocess.check_output", return_value=json.dumps(value).encode()):
            with self.assertRaises(ValueError):
                verify(*ARGS[:8], PROVENANCE, *ARGS[9:])

    def test_provenance_binds_build_source_and_invocation(self):
        value = response()
        statement = value[0]["verificationResult"]["statement"]
        statement["predicateType"] = PROVENANCE
        url = "https://github.com/Example/core/.github/workflows/release.yml"
        statement["predicate"] = {
            "buildDefinition": {"buildType": "https://actions.github.io/buildtypes/workflow/v1",
                                "externalParameters": {"workflow": {"repository": "https://github.com/" + REPOSITORY,
                                                                     "path": ".github/workflows/release.yml", "ref": "refs/heads/main"}},
                                "internalParameters": {"github": {"event_name": "workflow_dispatch", "repository_id": "456",
                                                                  "repository_owner_id": "789", "runner_environment": "github-hosted"},
                                                       "release": {"version": "v1.0.0", "platform": "linux/amd64"}},
                                "resolvedDependencies": [{"uri": "git+https://github.com/Example/core@refs/heads/main",
                                                          "digest": {"gitCommit": "b" * 40}}]},
            "runDetails": {"builder": {"id": url + "@refs/heads/main"},
                           "metadata": {"invocationId": "https://github.com/Example/core/actions/runs/123/attempts/1"}}}
        with patch("scripts.verify_core_attestation.subprocess.check_output", return_value=json.dumps(value).encode()):
            verify(*ARGS[:8], PROVENANCE, *ARGS[9:])
        for field, bad in (("version", "v2.0.0"), ("platform", "linux/arm64")):
            changed = copy.deepcopy(value)
            changed[0]["verificationResult"]["statement"]["predicate"]["buildDefinition"]["internalParameters"]["release"][field] = bad
            with self.subTest(field=field), patch("scripts.verify_core_attestation.subprocess.check_output", return_value=json.dumps(changed).encode()):
                with self.assertRaises(ValueError):
                    verify(*ARGS[:8], PROVENANCE, *ARGS[9:])
        for path in (("buildDefinition", "buildType"), ("buildDefinition", "resolvedDependencies"),
                     ("buildDefinition", "externalParameters"), ("buildDefinition", "internalParameters"),
                     ("runDetails", "builder"), ("runDetails", "metadata")):
            changed = copy.deepcopy(value)
            claim = changed[0]["verificationResult"]["statement"]["predicate"]
            claim[path[0]][path[1]] = {}
            with self.subTest(path=path), patch("scripts.verify_core_attestation.subprocess.check_output", return_value=json.dumps(changed).encode()):
                with self.assertRaises(ValueError):
                    verify(*ARGS[:8], PROVENANCE, *ARGS[9:])
        for field, bad in (("event_name", "pull_request"), ("runner_environment", "self-hosted"),
                           ("repository_id", "0"), ("repository_owner_id", "invalid")):
            changed = copy.deepcopy(value)
            changed[0]["verificationResult"]["statement"]["predicate"]["buildDefinition"]["internalParameters"]["github"][field] = bad
            with self.subTest(field=field), patch("scripts.verify_core_attestation.subprocess.check_output", return_value=json.dumps(changed).encode()):
                with self.assertRaises(ValueError):
                    verify(*ARGS[:8], PROVENANCE, *ARGS[9:])

    def test_file_bytes_must_match_before_calling_gh(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "artifact"
            path.write_bytes(b"synthetic")
            digest = hashlib.sha256(b"synthetic").hexdigest()
            value = response()
            value[0]["verificationResult"]["statement"]["subject"][0]["digest"]["sha256"] = digest
            with patch("scripts.verify_core_attestation.subprocess.check_output", return_value=json.dumps(value).encode()) as gh:
                verify(str(path), digest, *ARGS[2:])
                self.assertEqual(gh.call_args.args[0][3], str(path.resolve()))
                path.write_bytes(b"altered")
                with self.assertRaisesRegex(ValueError, "artifact bytes"):
                    verify(str(path), digest, *ARGS[2:])
                self.assertEqual(gh.call_count, 1)


if __name__ == "__main__":
    unittest.main()
