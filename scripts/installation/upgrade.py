"""Image-set upgrade with a stopped-write backup and explicit failure journal."""
import json
from datetime import datetime, timezone
import os
import shutil
from pathlib import Path
import subprocess
import tarfile

from .release import compose_inputs, read_manifest
from .backup import seal

APPS = ("edge", "frontend", "api", "worker", "executor")
BUNDLE = Path(__file__).resolve().parent.parent.parent
SNAPSHOT = ("release.json", "release.env", ".installed-release.json", "database.env", "migrator.env",
            "runtime.env", "executor.env", "init.sql", "config.toml", "executor.toml", "compose.json",
            "Caddyfile", "runtime-grants.sql", "execution-grants.sql")


def version_order(version):
    base, _, prerelease = version[1:].partition("-")
    identifiers = tuple((0, int(part)) if part.isdigit() else (1, part) for part in prerelease.split("."))
    return tuple(int(part) for part in base.split(".")), (0, identifiers) if prerelease else (1,)


def upgrade(directory, current, values, release, backup_directory, compose, command, environment):
    candidate = read_manifest(release)
    if candidate != read_manifest(BUNDLE / "release.json"):
        raise ValueError("upgrade must use the tools bundle matching the candidate release")
    grants = {
        "runtime-grants.sql": (BUNDLE / "scripts/self-hosted-runtime-grants.sql").read_text(),
        "execution-grants.sql": (BUNDLE / "scripts/installation/execution-grants.sql").read_text(),
    }
    if candidate["platform"] != current["platform"] or candidate["images"]["postgres"] != current["images"]["postgres"]:
        raise ValueError("upgrade cannot change platform or PostgreSQL image; use the separate database upgrade procedure")
    if version_order(candidate["version"]) <= version_order(current["version"]):
        raise ValueError("upgrade requires a newer release version; downgrades need separate recovery qualification")
    files, apps, backup = prepare_backup(directory, backup_directory)
    def private_file(path, content):
        descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(descriptor, "w") as output:
            output.write(content)
    next_env = backup / "candidate.env"
    private_file(next_env, compose_inputs(candidate) + "".join(f"{key}={values[key]}\n" for key in
                 ("BRAIDPLANE_HOST", "BRAIDPLANE_BIND_ADDRESS", "BRAIDPLANE_PROJECT_NAME")))
    private_file(backup / "candidate.json", json.dumps(candidate, indent=2) + "\n")
    compose("config", "--quiet", release_env=next_env)
    compose("pull", release_env=next_env)
    check_backup_space(directory, files, backup, command, environment)
    journal = directory / ".upgrade-in-progress.json"
    private_file(journal, json.dumps({"previous": current, "candidate": candidate, "backup": str(backup)}) + "\n")
    try:
        compose("stop", "--timeout", "45", *apps)
        stopped_at = datetime.now(timezone.utc).isoformat()
        write_backup(directory, current, files, backup, stopped_at, command, environment)
        compose("run", "--rm", "--no-deps", "-T", "migrate", release_env=next_env)
        for name, content in grants.items():
            staged = directory / (name + ".next")
            private_file(staged, content)
            os.replace(staged, directory / name)
        # Read the candidate SQL from stdin, avoiding stale single-file mounts.
        apply_grants = "export PGHOST=$DB_HOST PGPORT=$DB_PORT PGUSER=$DB_USER PGPASSWORD=$DB_PASSWORD PGDATABASE=$DB_DATABASE PGSSLMODE=$DB_SSL_MODE PGSSLROOTCERT=$DB_SSL_ROOT_CERT; exec psql -v ON_ERROR_STOP=1 -v runtime_role=braidplane_runtime -v migrator_role=braidplane_migrator -v executor_role=braidplane_executor -f -"
        compose("run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "grants", "-ec", apply_grants,
                release_env=next_env, input="\n".join(grants.values()))
        compose("up", "--detach", "--no-deps", "--wait", "--wait-timeout", "180", *apps, release_env=next_env)
        for name, content in (("release.json", json.dumps(candidate, indent=2) + "\n"),
                              ("release.env", next_env.read_text()),
                              (".installed-release.json", json.dumps(candidate, sort_keys=True) + "\n")):
            staged = directory / (name + ".next")
            private_file(staged, content)
            os.replace(staged, directory / name)
        journal.unlink()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        try:
            compose("stop", "--timeout", "45", *apps, release_env=next_env)
        except (ValueError, OSError):
            pass
        raise ValueError("upgrade failed; application stop was requested; preserve the upgrade journal and backup for recovery; no automatic rollback was attempted") from error


def prepare_backup(directory, backup_directory):
    files = [directory / name for name in SNAPSHOT]
    files += [directory / "certificates" / name for name in ("ca.pem", "database.pem", "database.key", "edge.pem", "edge.key")]
    for path in files:
        if path.is_symlink() or not path.is_file():
            raise ValueError("backup inputs must be regular files")
    services = json.loads((directory / "compose.json").read_text())["services"]
    apps = tuple(name for name in APPS if name in services)
    backup = Path(backup_directory).resolve()
    if backup == directory or directory in backup.parents:
        raise ValueError("backup directory must be outside the installation directory")
    backup.mkdir(mode=0o700, exist_ok=False)
    return files, apps, backup


def write_backup(directory, current, files, backup, stopped_at, command, environment):
    # Both dump and bundled credentials remain protected even after a partial failure.
    dump = backup / "database.dump"
    descriptor = os.open(dump, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    export = "export PGHOST=$DB_HOST PGPORT=$DB_PORT PGUSER=$DB_USER PGPASSWORD=$DB_PASSWORD PGDATABASE=$DB_DATABASE PGSSLMODE=$DB_SSL_MODE PGSSLROOTCERT=$DB_SSL_ROOT_CERT; exec pg_dump --format=custom --no-owner --no-acl"
    with os.fdopen(descriptor, "wb") as output:
        subprocess.run([*command, "run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "grants", "-ec", export],
                       cwd=directory, env=environment, check=True, stdout=output, stderr=subprocess.PIPE)
    with dump.open("rb") as source:
        subprocess.run([*command, "run", "--rm", "--no-deps", "-T", "--entrypoint", "pg_restore", "grants", "--list"],
                       cwd=directory, env=environment, check=True, stdin=source, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    descriptor = os.open(backup / "configuration.tar", os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, "wb") as raw:
        with tarfile.open(fileobj=raw, mode="w") as archive:
            for path in files:
                archive.add(path, arcname=str(path.relative_to(directory)), recursive=False)
    seal(backup, current, stopped_at)


def check_backup_space(directory, files, backup, command, environment):
    export = "export PGHOST=$DB_HOST PGPORT=$DB_PORT PGUSER=$DB_USER PGPASSWORD=$DB_PASSWORD PGDATABASE=$DB_DATABASE PGSSLMODE=$DB_SSL_MODE PGSSLROOTCERT=$DB_SSL_ROOT_CERT; exec psql -At -v ON_ERROR_STOP=1 -c 'SELECT pg_database_size(current_database())'"
    try:
        result = subprocess.run([*command, "run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "grants", "-ec", export],
                                cwd=directory, env=environment, check=True, capture_output=True)
        size = int(result.stdout.strip())
        if size <= 0:
            raise ValueError("database size must be positive")
    except (ValueError, subprocess.CalledProcessError) as error:
        raise ValueError("backup capacity query failed; application stop was not requested") from error
    # This is an early capacity check, not a guarantee against growth or concurrent disk use.
    required = size + sum(path.stat().st_size for path in files)
    if shutil.disk_usage(backup).free < required:
        raise ValueError("insufficient backup filesystem space for measured database and configuration sizes; application stop was not requested")


def backup_installation(directory, current, backup_directory, compose, command, environment):
    files, apps, backup = prepare_backup(directory, backup_directory)
    check_backup_space(directory, files, backup, command, environment)
    try:
        compose("stop", "--timeout", "45", *apps)
        stopped_at = datetime.now(timezone.utc).isoformat()
        write_backup(directory, current, files, backup, stopped_at, command, environment)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        raise ValueError("backup failed; application restart was not attempted; preserve partial backup and inspect installation status") from error
