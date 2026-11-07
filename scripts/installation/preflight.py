"""Check customer TLS and Docker runtime without starting an installation."""
import argparse
import json

from .release import read_manifest
from pathlib import Path
import subprocess


def openssl(*arguments):
    try:
        return subprocess.run(["openssl", *arguments], check=True, capture_output=True, timeout=15).stdout
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        raise ValueError("TLS validation failed; check certificate trust, hostname, expiry and key pairing") from error


def check_tls(directory, hostname):
    directory = Path(directory) / "certificates"
    for name in ("ca.pem", "database.pem", "database.key", "edge.pem", "edge.key"):
        path = directory / name
        if path.is_symlink() or not path.is_file() or not path.stat().st_size:
            raise ValueError(f"TLS input must be a nonempty regular file: {name}")
        if name.endswith(".key") and path.stat().st_mode & 0o077:
            raise ValueError(f"private key must be accessible only to its owner: {name}")
    for name, expected_host in (("database", "database"), ("edge", hostname)):
        certificate = str(directory / (name + ".pem"))
        key = str(directory / (name + ".key"))
        openssl("verify", "-x509_strict", "-CAfile", str(directory / "ca.pem"), "-purpose", "sslserver",
                "-verify_hostname", expected_host, certificate)
        openssl("x509", "-in", certificate, "-checkend", "86400", "-noout")
        public_certificate = openssl("x509", "-in", certificate, "-pubkey", "-noout")
        public_key = openssl("pkey", "-in", key, "-passin", "pass:", "-pubout")
        if public_certificate != public_key:
            raise ValueError(f"certificate and private key do not match: {name}")


def check_runtime(manifest, environment=None):
    def docker(*args):
        try:
            return subprocess.run(["docker", *args], check=True, capture_output=True,
                                  text=True, timeout=15, env=environment).stdout
        except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
            raise ValueError("Docker runtime preflight failed; verify Docker/Compose installation, daemon access and context") from error
    try:
        info = json.loads(docker("info", "--format", "{{json .}}"))
    except json.JSONDecodeError as error:
        raise ValueError("Docker info returned invalid JSON") from error
    if not isinstance(info, dict) or info.get("OSType") != "linux":
        raise ValueError("installation requires a Linux Docker daemon")
    architecture = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64", "amd64": "amd64"}.get(info.get("Architecture"))
    if not architecture or manifest["platform"] != "linux/" + architecture:
        raise ValueError("release platform does not match the Docker daemon; obtain the native release manifest")
    compose = docker("compose", "version", "--short").strip()
    flags = set(docker("compose", "up", "--help").split())
    if not {"--wait", "--wait-timeout"}.issubset(flags):
        raise ValueError("Docker Compose lacks required --wait/--wait-timeout support; install a compatible Compose version")
    return {"engine": info.get("ServerVersion"), "compose": compose, "platform": manifest["platform"],
            "daemon_cpus": info.get("NCPU"), "daemon_memory_bytes": info.get("MemTotal")}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("hostname", help="customer application DNS name")
    args = parser.parse_args()
    try:
        check_tls(args.directory, args.hostname)
        runtime = check_runtime(read_manifest(args.directory / "release.json"))
    except (ValueError, OSError) as error:
        parser.exit(1, f"Installation preflight failed: {error}\n")
    print("TLS inputs passed trust, hostname, key pairing and one-day validity checks.")
    print(json.dumps(runtime, sort_keys=True))


if __name__ == "__main__":
    main()
