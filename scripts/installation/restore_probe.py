"""Rehearse a trusted backup in disposable PostgreSQL with no network or application workers."""
import argparse
import json
from pathlib import Path
import subprocess
import tarfile
import time
import uuid

from .backup import verify
from .upgrade import SNAPSHOT


def read_configuration(directory):
    directory = Path(directory)
    receipt = verify(directory)
    # Never extract paths, links or device files from a recovery archive.
    expected = set(SNAPSHOT) | {"certificates/" + name for name in
                               ("ca.pem", "database.pem", "database.key", "edge.pem", "edge.key")}
    with tarfile.open(directory / "configuration.tar") as archive:
        members = archive.getmembers()
        if len(members) != len(expected) or {item.name for item in members} != expected or any(
                not item.isfile() for item in members):
            raise ValueError("configuration archive inventory mismatch")
        release = json.load(archive.extractfile("release.json"))
        if release != receipt["release"]:
            raise ValueError("configuration release differs from backup receipt")
        files = {item.name: archive.extractfile(item).read() for item in members}
    return receipt, files


def probe(directory):
    directory = Path(directory)
    receipt, sql = read_configuration(directory)
    release = receipt["release"]
    container = "braidplane-restore-" + uuid.uuid4().hex
    def docker(*args, **kwargs):
        return subprocess.run(["docker", *args], check=True, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, **kwargs)
    def psql(content):
        return docker("exec", "-i", container, "psql", "-U", "postgres", "-d", "braidplane",
                      "-v", "ON_ERROR_STOP=1", "-v", "runtime_role=braidplane_runtime",
                      "-v", "migrator_role=braidplane_migrator", "-v", "executor_role=braidplane_executor",
                      "-At", input=content)
    started = time.monotonic()
    created = False
    try:
        docker("run", "--detach", "--name", container, "--network", "none",
               "--platform", release["platform"], "-e", "POSTGRES_HOST_AUTH_METHOD=trust",
               "-e", "POSTGRES_DB=braidplane", release["images"]["postgres"])
        created = True
        for _ in range(120):
            try:
                docker("exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "postgres")
                break
            except subprocess.CalledProcessError:
                time.sleep(0.5)
        else:
            raise ValueError("isolated recovery database did not become ready")
        psql(sql["init.sql"])
        with (directory / "database.dump").open("rb") as source:
            docker("exec", "-i", container, "pg_restore", "-U", "postgres", "-d", "braidplane",
                   "--exit-on-error", "--single-transaction", "--no-owner", "--no-acl",
                   "--role=braidplane_migrator", stdin=source)
        for name in ("runtime-grants.sql", "execution-grants.sql"):
            psql(b"SET ROLE braidplane_migrator;\n" + sql[name])
        ledger = psql(b"SELECT version FROM core_schema_migrations WHERE NOT dirty;\n").stdout.decode().strip()
        count = psql(b"SELECT count(*) = 1 AND bool_and(NOT dirty) FROM core_schema_migrations;\n").stdout.strip()
        if count != b"t":
            raise ValueError("restored migration ledger must contain exactly one clean version")
        return {"release": release, "schema_version": ledger,
                "elapsed_seconds": round(time.monotonic() - started, 3),
                "scope": "isolated database restore and grants only; credential and application acceptance remain required"}
    finally:
        if created:
            docker("rm", "--force", "--volumes", container)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    try:
        result = probe(args.directory)
    except (ValueError, OSError, tarfile.TarError, subprocess.CalledProcessError):
        parser.exit(1, "isolated restore failed; no application workers were started; database diagnostics withheld to protect secrets\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
