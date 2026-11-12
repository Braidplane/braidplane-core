"""Operate one prepared installation without removing containers, volumes or keys."""
import argparse
import fcntl
import ipaddress
import json
import os
from pathlib import Path
import re
import subprocess

from .preflight import check_runtime, check_tls
from .release import compose_inputs, read_manifest, validate


def read_inputs(directory, allow_upgrade_recovery=False):
    manifest = read_manifest(directory / "release.json")
    values = {}
    for line in (directory / "release.env").read_text().splitlines():
        key, separator, value = line.partition("=")
        if not separator or key in values:
            raise ValueError("release.env contains an invalid or duplicate setting")
        values[key] = value
    if allow_upgrade_recovery and (directory / ".upgrade-in-progress.json").exists():
        journal = json.loads((directory / ".upgrade-in-progress.json").read_text())
        if not isinstance(journal, dict) or not {"previous", "candidate"}.issubset(journal):
            raise ValueError("upgrade journal is incomplete; preserve it and the backup for recovery")
        # A crash can leave release.json and release.env at different commit stages.
        for name in ("previous", "candidate"):
            recorded = validate(journal[name])
            expected = dict(line.split("=", 1) for line in compose_inputs(recorded).splitlines())
            if all(values.get(key) == value for key, value in expected.items()):
                manifest = recorded
                break
    expected = dict(line.split("=", 1) for line in compose_inputs(manifest).splitlines())
    customer = {"BRAIDPLANE_HOST", "BRAIDPLANE_BIND_ADDRESS", "BRAIDPLANE_PROJECT_NAME"}
    if set(values) != set(expected) | customer or any(values[key] != value for key, value in expected.items()):
        raise ValueError("release.env must match release.json and contain only the customer host, bind address and project name")
    if not re.fullmatch(r"[a-z0-9][a-z0-9.-]*", values["BRAIDPLANE_HOST"]):
        raise ValueError("invalid customer hostname")
    if not re.fullmatch(r"braidplane-[a-f0-9]{12}", values["BRAIDPLANE_PROJECT_NAME"]):
        raise ValueError("invalid installation project identity")
    if ipaddress.ip_address(values["BRAIDPLANE_BIND_ADDRESS"]).version != 4:
        raise ValueError("bind address must be IPv4")
    return manifest, values


def operate(directory, action, release=None, backup_directory=None):
    if action not in ("install", "start", "stop", "status", "upgrade", "backup"):
        raise ValueError("unknown installation operation")
    directory = Path(directory).resolve(strict=True)
    descriptor = os.open(directory / ".installation.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError("another installation operation is running; retry after it finishes") from error
        if action not in ("stop", "status") and (directory / ".restore-pending.json").exists():
            raise ValueError("restored installation is held for reconciliation; normal startup is prohibited")
        if action not in ("stop", "status") and (directory / ".upgrade-in-progress.json").exists():
            raise ValueError("an upgrade is incomplete; preserve its journal and backup and follow recovery guidance")
        manifest, values = read_inputs(directory, allow_upgrade_recovery=action in ("stop", "status"))
        # Ambient Compose/image variables must not override the reviewed release.env.
        environment = {key: value for key, value in os.environ.items()
                       if key not in values and not key.startswith("COMPOSE_")}
        command = ["docker", "compose", "--project-name", values["BRAIDPLANE_PROJECT_NAME"],
                   "--env-file", str(directory / "release.env"), "-f", str(directory / "compose.json")]
        def compose(*arguments, release_env=None, input=None):
            selected = list(command)
            if release_env is not None:
                selected[selected.index("--env-file") + 1] = str(release_env)
            try:
                return subprocess.run([*selected, *arguments], cwd=directory, env=environment,
                                      check=True, capture_output=True, text=True, input=input).stdout
            except subprocess.CalledProcessError as error:
                raise ValueError(f"Compose {arguments[0]} failed; inspect this project's status and logs; data and keys were preserved") from error
        if action == "stop":
            compose("stop", "--timeout", "45")
            return
        if action == "status":
            return compose("ps", "--all")
        if action in ("upgrade") and (release is None or backup_directory is None):
            raise ValueError("upgrade requires --release and --backup-directory")
        if action == "backup" and backup_directory is None:
            raise ValueError("backup requires --backup-directory")
        if action in ("upgrade", "backup") and not (directory / ".installed-release.json").exists():
            raise ValueError("backup and upgrade require an existing installation identity")
        check_tls(directory, values["BRAIDPLANE_HOST"])
        check_runtime(manifest, environment)
        compose("config", "--quiet")
        identity = directory / ".installed-release.json"
        if identity.exists():
            if json.loads(identity.read_text()) != manifest:
                raise ValueError("release changed; use the separately qualified upgrade procedure")
        else:
            # Pin even a failed first attempt so a retry cannot silently change its release.
            with identity.open("x") as output:
                json.dump(manifest, output, sort_keys=True)
                output.write("\n")
        if action == "backup":
            from .upgrade import backup_installation
            return backup_installation(directory, manifest, backup_directory, compose, command, environment)
        if action in ("upgrade"):
            from .upgrade import upgrade
            return upgrade(directory, manifest, values, release, backup_directory, compose, command, environment)
        compose("pull")
        compose("up", "--detach", "--wait", "--wait-timeout", "180")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "start", "stop", "status", "upgrade", "backup"))
    parser.add_argument("directory", type=Path)
    parser.add_argument("--release", type=Path)
    parser.add_argument("--backup-directory", type=Path)
    args = parser.parse_args()
    try:
        result = operate(args.directory, args.action, args.release, args.backup_directory)
    except (ValueError, OSError) as error:
        parser.exit(1, f"installation operation failed: {error}\n")
    print(result or f"Installation {args.action} completed.", end="\n")


if __name__ == "__main__":
    main()
