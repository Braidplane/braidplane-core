#!/usr/bin/env python3
"""Verify one Core artifact attestation against independently supplied identities."""
import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path

from scripts.core_release_sbom import validate_sbom


PROVENANCE = "https://slsa.dev/provenance/v1"
SPDX = "https://spdx.dev/Document/v2.3"


def verify(subject, digest, repository, workflow, source_sha, signer_sha, run_id, attempt, predicate, version, platform):
    if (not re.fullmatch(r"[0-9a-f]{64}", digest) or
            not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository) or
            not re.fullmatch(r"\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml", workflow) or
            not re.fullmatch(r"[0-9a-f]{40}", source_sha) or not re.fullmatch(r"[0-9a-f]{40}", signer_sha) or
            not re.fullmatch(r"[1-9][0-9]*", run_id) or not re.fullmatch(r"[1-9][0-9]*", attempt) or predicate not in (PROVENANCE, SPDX) or
            not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version) or
            platform not in ("linux/amd64", "linux/arm64")):
        raise ValueError("invalid expected release identity")
    if subject.startswith("oci://"):
        if not re.fullmatch(r"oci://[a-z0-9][a-z0-9./:_-]*@sha256:" + digest, subject):
            raise ValueError("image subject must match the expected immutable digest")
    else:
        subject = str(Path(subject).resolve(strict=True))
        if hashlib.sha256(Path(subject).read_bytes()).hexdigest() != digest:
            raise ValueError("artifact bytes differ from the expected digest")
    identity = f"https://github.com/{repository}/{workflow}@refs/heads/main"
    output = subprocess.check_output([
        "gh", "attestation", "verify", subject, "--repo", repository, "--cert-identity", identity,
        "--cert-oidc-issuer", "https://token.actions.githubusercontent.com", "--source-ref", "refs/heads/main",
        "--source-digest", source_sha, "--signer-digest", signer_sha, "--deny-self-hosted-runners",
        "--predicate-type", predicate, "--format", "json",
    ])
    statements = json.loads(output)
    if not isinstance(statements, list):
        raise ValueError("verification did not return a statement list")
    for attestation in statements:
        try:
            result = attestation["verificationResult"]
            certificate = result["signature"]["certificate"]
            statement = result["statement"]
            invocation = certificate["runInvocationURI"]
            if (not result["verifiedTimestamps"] or certificate["sourceRepositoryVisibilityAtSigning"] != "public" or
                    invocation != f"https://github.com/{repository}/actions/runs/{run_id}/attempts/{attempt}" or
                    statement["predicateType"] != predicate or
                    not any(item.get("digest") == {"sha256": digest} for item in statement["subject"])):
                continue
            if predicate == SPDX:
                validate_sbom(statement["predicate"])
            else:
                claim = statement["predicate"]
                workflow_url = f"https://github.com/{repository}/{workflow}"
                definition = claim["buildDefinition"]
                details = claim["runDetails"]
                github = definition["internalParameters"]["github"]
                if (definition["buildType"] != "https://actions.github.io/buildtypes/workflow/v1" or
                        definition["externalParameters"] != {"workflow": {"repository": "https://github.com/" + repository,
                                                                          "path": workflow, "ref": "refs/heads/main"}} or
                        definition["internalParameters"]["release"] != {"version": version, "platform": platform} or
                        github["event_name"] != "workflow_dispatch" or github["runner_environment"] != "github-hosted" or
                        not re.fullmatch(r"[1-9][0-9]*", github["repository_id"]) or
                        not re.fullmatch(r"[1-9][0-9]*", github["repository_owner_id"]) or
                        definition["resolvedDependencies"] != [{"uri": f"git+https://github.com/{repository}@refs/heads/main",
                                                                "digest": {"gitCommit": source_sha}}] or
                        details["builder"]["id"] != workflow_url + "@refs/heads/main" or
                        details["metadata"]["invocationId"] != invocation):
                    continue
            return output
        except (KeyError, TypeError, ValueError, AttributeError):
            continue
    raise ValueError("no verified statement matches the expected artifact, public signer and run")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for argument in ("subject", "digest", "repository", "workflow", "source_sha", "signer_sha", "run_id", "attempt", "predicate", "version", "platform"):
        parser.add_argument(argument)
    parser.add_argument("output", type=Path, help="new verification result file")
    args = parser.parse_args()
    try:
        result = verify(args.subject, args.digest, args.repository, args.workflow, args.source_sha,
                        args.signer_sha, args.run_id, args.attempt, args.predicate, args.version, args.platform)
        with args.output.open("xb") as output:
            output.write(result)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"attestation verification failed: {error}\n")


if __name__ == "__main__":
    main()
