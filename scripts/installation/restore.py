"""Restore a trusted backup to a new isolated installation under a recovery hold."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import tarfile
import time

from .lifecycle import read_inputs
from .restore_probe import read_configuration


def restore(backup, destination):
    backup = Path(backup).resolve(strict=True)
    receipt, files = read_configuration(backup)
    destination = Path(destination).absolute()
    if destination == backup or backup in destination.parents:
        raise ValueError("restore destination must be outside the backup directory")
    # Never adopt an existing directory, project identity, container or volume.
    destination.mkdir(mode=0o700, exist_ok=False)
    started = time.monotonic()
    def private_file(name, content):
        descriptor = os.open(destination / name, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(descriptor, "wb") as output:
            output.write(content)
    private_file(".restore-pending.json", json.dumps({"backup": str(backup), "state": "incomplete"}).encode())
    (destination / "certificates").mkdir(mode=0o700)
    for name, content in files.items():
        private_file(name, content)
        if name.endswith(".pem") or name in ("config.toml", "executor.toml"):
            (destination / name).chmod(0o644)
    manifest, values = read_inputs(destination)
    if manifest != receipt["release"] or json.loads(files[".installed-release.json"]) != manifest:
        raise ValueError("restored release identities differ")
    project = "braidplane-" + secrets.token_hex(6)
    env_path = destination / "release.env"
    env_path.write_text("".join(f"{key}={project if key == 'BRAIDPLANE_PROJECT_NAME' else value}\n"
                                for key, value in values.items()))
    # Keep only the database and grant client; no restored app can execute work.
    # Customer volume overrides must never redirect recovery into existing storage.
    topology = json.loads((Path(__file__).parent / "compose.json").read_text())
    topology["services"] = {name: topology["services"][name] for name in ("database", "grants")}
    topology["services"]["grants"].pop("depends_on", None)
    # Recovered connection overrides must not send restore writes to the source database.
    topology["services"]["grants"]["environment"] = {
        "DB_HOST": "database", "DB_PORT": "5432", "DB_DATABASE": "braidplane",
        "DB_USER": "braidplane_migrator", "DB_SSL_MODE": "verify-full",
        "DB_SSL_ROOT_CERT": "/etc/braidplane/ca.pem",
    }
    clear_client_overrides = "unset PGHOSTADDR PGSERVICE PGSERVICEFILE; "
    topology["services"]["grants"]["command"][0] = clear_client_overrides + topology["services"]["grants"]["command"][0]
    topology["networks"] = {"private": {"internal": True}}
    for service in topology["services"].values():
        service.pop("ports", None)
    (destination / "compose.json").write_text(json.dumps(topology, indent=2) + "\n")
    command = ["docker", "compose", "--project-name", project, "--env-file", str(env_path),
               "-f", str(destination / "compose.json")]
    environment = {key: value for key, value in os.environ.items()
                   if key not in values and not key.startswith("COMPOSE_")}
    def compose(*args, **kwargs):
        return subprocess.run([*command, *args], cwd=destination, env=environment,
                              check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)
    compose("config", "--quiet")
    compose("up", "--detach", "--no-deps", "--wait", "--wait-timeout", "90", "database")
    export = clear_client_overrides + "export PGHOST=$DB_HOST PGPORT=$DB_PORT PGUSER=$DB_USER PGPASSWORD=$DB_PASSWORD PGDATABASE=$DB_DATABASE PGSSLMODE=$DB_SSL_MODE PGSSLROOTCERT=$DB_SSL_ROOT_CERT; "
    with (backup / "database.dump").open("rb") as source:
        compose("run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "grants", "-ec",
                export + "exec pg_restore --dbname=braidplane --exit-on-error --single-transaction --no-owner --no-acl", stdin=source)
    compose("run", "--rm", "--no-deps", "-T", "grants")
    check = compose("run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "grants", "-ec",
                    export + "exec psql -At -v ON_ERROR_STOP=1 -c 'SELECT count(*) = 1 AND bool_and(NOT dirty) FROM public.core_schema_migrations'")
    if check.stdout.strip() != b"t":
        raise ValueError("restored migration ledger must contain exactly one clean version")
    result = {"backup": str(backup), "project": project, "release": manifest,
              "state": "isolated-awaiting-reconciliation", "elapsed_seconds": round(time.monotonic() - started, 3)}
    (destination / ".restore-pending.json").write_text(json.dumps(result, indent=2) + "\n")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("backup", type=Path)
    parser.add_argument("destination", type=Path)
    args = parser.parse_args()
    try:
        result = restore(args.backup, args.destination)
    except (ValueError, OSError, tarfile.TarError, subprocess.CalledProcessError):
        parser.exit(1, "restore failed; preserve the destination and recovery hold; inspect its isolated database; diagnostics withheld to protect secrets\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
