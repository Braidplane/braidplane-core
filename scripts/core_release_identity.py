"""Reserve immutable release identities and verify publication before completion."""
import argparse
import json
import os
import re
import subprocess
from pathlib import Path

from scripts.admit_core_ci import require_main
from scripts.verify_core_release import read_json
from scripts.validate_core_candidate import file_hash, validate_candidate
from scripts.verify_core_release import verify_release


def api(path, payload=None, method="POST"):
    command = ["gh", "api", path]
    paginated = payload is None and "/git/matching-refs/" in path
    if paginated:
        command += ["--paginate", "--slurp"]
    if payload is not None:
        command += ["--method", method, "--input", "-"]
    raw = subprocess.check_output(command, input=None if payload is None else json.dumps(payload).encode())
    result = json.loads(raw) if raw.strip() else None
    return [item for page in result for item in page] if paginated else result


def bind(repository, ref, source, message):
    """Create only; on races/retries read back the existing exact identity."""
    # Listing an exact namespace avoids treating an authentication failure as a missing ref.
    matches = api(f"repos/{repository}/git/matching-refs/tags/{ref}")
    exact = [item for item in matches if item["ref"] == "refs/tags/" + ref]
    if not exact:
        tag = api(f"repos/{repository}/git/tags", {"tag": ref, "message": message, "object": source, "type": "commit"})
        try:
            api(f"repos/{repository}/git/refs", {"ref": "refs/tags/" + ref, "sha": tag["sha"]})
        except subprocess.CalledProcessError:
            # The create may have raced or succeeded before the response was lost.
            pass
        exact = [api(f"repos/{repository}/git/ref/tags/{ref}")]
    obj = exact[0]["object"]
    if obj["type"] != "tag":
        raise ValueError("release identity must be an annotated tag")
    tag = api(f"repos/{repository}/git/tags/{obj['sha']}")
    if tag["object"]["sha"] != source or tag["object"]["type"] != "commit":
        raise ValueError("immutable release source differs")
    if tag["tag"] != ref or tag["message"].rstrip("\n") != message.rstrip("\n"):
        raise ValueError("immutable release identity differs; never replace the tag")


def baseline(repository, plan):
    refs = api(f"repos/{repository}/git/matching-refs/tags/v")
    versions = {item["ref"].removeprefix("refs/tags/") for item in refs
                if re.fullmatch(r"refs/tags/v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", item["ref"])}
    previous, version = plan["previous"], plan["version"]
    if not re.fullmatch(r"v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*", version):
        raise ValueError("automatic publication requires a pre-v1 RC")
    reserved = {item["ref"].removeprefix("refs/tags/") for item in refs}
    # Existing target is allowed only for exact-identity retries, checked by bind().
    if version in reserved:
        return
    if previous == "v0.0.0":
        if versions:
            raise ValueError("initial release baseline requires no prior version reservations")
    elif previous not in versions:
        raise ValueError("previous version has no release reservation")
    if any(tuple(map(int, v[1:].split('.'))) > tuple(map(int, previous[1:].split('.'))) for v in versions):
        raise ValueError("previous stable version is stale")


def identity(directory, approved, checkout, env):
    repository, source = env["GITHUB_REPOSITORY"], env.get("RELEASE_SOURCE", env["GITHUB_SHA"])
    record = validate_candidate(directory, approved, repository, source)
    if "RELEASE_SOURCE" in env:
        receipt = Path(env["RUNNER_TEMP"]) / "core-release-ci-admission.json"
        if file_hash(receipt) != read_json(directory / "approval.json")["ci_admission_sha256"]:
            raise ValueError("CI admission differs from the approved candidate")
    if env["GITHUB_WORKFLOW_SHA"] != env["GITHUB_SHA"] or env["GITHUB_WORKFLOW_REF"] != repository + "/.github/workflows/core-release.yml@refs/heads/main":
        raise ValueError("identity requires the exact reviewed release workflow")
    head = subprocess.check_output(["git", "-C", str(checkout), "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"])
    if head != source or dirty:
        raise ValueError("release identity requires clean exact source")
    plan = read_json(directory / "release-plan.json")
    materials = {name: file_hash(checkout / name) for name in
                 ("ci/core-release.json", "ci/toolchain/versions.env")}
    materials[".github/workflows/core-release.yml"] = file_hash(Path(__file__).resolve().parents[1] / ".github/workflows/core-release.yml")
    materials.update({name: file_hash(directory / name) for name in ("release-plan.json", "release-inputs.json", "CHANGELOG.md")})
    return {"repository": repository, "source": source, "workflow_sha": env["GITHUB_WORKFLOW_SHA"],
            "run_id": env["GITHUB_RUN_ID"],
            "materials": materials, "plan": plan, "inputs": read_json(directory / "release-inputs.json"),
            "approval_sha256": approved, "record": record}


def check_publication_order(repository, source, version, platform, checkout):
    refs = api(f"repos/{repository}/git/matching-refs/tags/core-published/")
    for ref in refs:
        if not re.fullmatch(r"refs/tags/core-published/v0\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?/linux-(?:amd64|arm64)", ref["ref"]):
            continue
        if ref["object"]["type"] != "tag":
            raise ValueError("published release cursor must be an annotated identity tag")
        tag = api(f"repos/{repository}/git/tags/{ref['object']['sha']}")
        published = tag["object"]["sha"]
        if tag["object"]["type"] != "commit" or not re.fullmatch(r"[0-9a-f]{40}", published):
            raise ValueError("invalid completed release source")
        if published == source:
            parts = ref["ref"].split("/")
            if parts[-1] == platform and parts[-2] != version:
                raise ValueError("source already published for this platform")
            continue
        result = subprocess.run(["git", "-C", str(checkout), "merge-base", "--is-ancestor", published, source])
        if result.returncode != 0:
            raise ValueError("completed release source is outside candidate history")


def execute(phase, directory, approved, checkout, output, env):
    if env.get("CORE_RELEASE_PUBLICATION_AUTHORIZED") != "yes":
        raise ValueError("explicit publication authority required")
    value = identity(directory, approved, checkout, env)
    repository, source = value["repository"], value["source"]
    release = value["record"]["release"]
    version, platform = release["version"], release["platform"].replace('/', '-')
    message = json.dumps(value, sort_keys=True, separators=(",", ":"))
    if phase == "reserve":
        check_publication_order(repository, source, version, platform, checkout)
        baseline(repository, value["plan"])
        bind(repository, version, source, json.dumps({"source": source, "version": version}, sort_keys=True))
        bind(repository, f"core-candidate/{version}/{platform}", source, message)
        output.mkdir(exist_ok=True)
        (output / "identity.json").write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    else:
        # Recheck the reservation before checking any signatures or publishing completion.
        bind(repository, f"core-candidate/{version}/{platform}", source, message)
        migration = value["record"]["migration"]
        expected = dict(record_sha256=file_hash(directory / "record.json"), repository=repository,
                        workflow=".github/workflows/core-release.yml", source_sha=source, signer_sha=value["workflow_sha"],
                        run_id=env["GITHUB_RUN_ID"], attempt=env["GITHUB_RUN_ATTEMPT"], version=version,
                        platform=release["platform"], migration_version=migration["version"], migration_digest=migration["digest"])
        verify_release(directory / "record.json", directory / "installation.tar.gz", expected,
                       output / "verification", Path(env["DOCKER_CONFIG"]))
        if "RELEASE_SOURCE" in env:
            require_main(repository, source, checkout)
        bind(repository, f"core-published/{version}/{platform}", source, message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("reserve", "complete"))
    parser.add_argument("candidate", type=Path)
    parser.add_argument("approval_sha256")
    parser.add_argument("checkout", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        execute(args.phase, args.candidate, args.approval_sha256, args.checkout, args.output, os.environ)
    except (OSError, ValueError, TypeError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"release identity failed: {error}\n")


if __name__ == "__main__":
    main()
