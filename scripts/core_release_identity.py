"""Reserve immutable release identities and verify publication before completion."""
import argparse
import json
import os
import re
import subprocess
from pathlib import Path

from scripts.core_release_version import reviewed
from scripts.validate_core_candidate import file_hash, validate_candidate
from scripts.verify_core_release import verify_release


def api(path, payload=None):
    command = ["gh", "api", path]
    paginated = payload is None and "/git/matching-refs/" in path
    if paginated:
        command += ["--paginate", "--slurp"]
    if payload is not None:
        command += ["--method", "POST", "--input", "-"]
    result = json.loads(subprocess.check_output(command, input=None if payload is None else json.dumps(payload).encode()))
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
    # Existing target is allowed only for exact-identity retries, checked by bind().
    if version in versions:
        return
    if previous == "v0.0.0":
        if versions:
            raise ValueError("initial release baseline requires no prior version reservations")
    elif previous not in versions:
        raise ValueError("previous version has no release reservation")
    line = previous.rsplit('.', 1)[0] + '.'
    if any(v.startswith(line) and int(v.rsplit('.', 1)[1]) > int(previous.rsplit('.', 1)[1]) for v in versions):
        raise ValueError("previous version is stale within its release line")


def identity(directory, approved, checkout, env):
    repository, source = env["GITHUB_REPOSITORY"], env["GITHUB_SHA"]
    record = validate_candidate(directory, approved, repository, source)
    release = record["release"]
    if env["GITHUB_WORKFLOW_SHA"] != source or env["GITHUB_WORKFLOW_REF"] != repository + "/.github/workflows/core-release.yml@refs/heads/main":
        raise ValueError("identity requires the exact reviewed release workflow")
    head = subprocess.check_output(["git", "-C", str(checkout), "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"])
    if head != source or dirty:
        raise ValueError("release identity requires clean exact source")
    plan = reviewed(checkout, release["version"])
    materials = {name: file_hash(checkout / name) for name in
                 ("release-plan.json", "CHANGELOG.md", "ci/toolchain/versions.env", ".github/workflows/core-release.yml")}
    return {"repository": repository, "source": source, "workflow_sha": source,
            "materials": materials, "plan": plan, "approval_sha256": approved, "record": record}


def execute(phase, directory, approved, checkout, output, env):
    if env.get("CORE_RELEASE_PUBLICATION_AUTHORIZED") != "yes":
        raise ValueError("explicit publication authority required")
    value = identity(directory, approved, checkout, env)
    repository, source = value["repository"], value["source"]
    release = value["record"]["release"]
    version, platform = release["version"], release["platform"].replace('/', '-')
    message = json.dumps(value, sort_keys=True, separators=(",", ":"))
    if phase == "reserve":
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
                        workflow=".github/workflows/core-release.yml", source_sha=source, signer_sha=source,
                        run_id=env["GITHUB_RUN_ID"], attempt=env["GITHUB_RUN_ATTEMPT"], version=version,
                        platform=release["platform"], migration_version=migration["version"], migration_digest=migration["digest"])
        verify_release(directory / "record.json", directory / "installation.tar.gz", expected,
                       output / "verification", Path(env["DOCKER_CONFIG"]))
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
