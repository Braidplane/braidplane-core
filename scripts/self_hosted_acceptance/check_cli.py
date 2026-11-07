"""Verify public CLI authentication with customer trust, and rejection without it."""
import argparse
import json
from pathlib import Path
import subprocess


def check(directory):
    base = ["docker", "compose", "-f", str(directory / "compose.yml"), "run", "--rm"]
    trusted = subprocess.run(base + ["cli"], capture_output=True, check=True)
    assert json.loads(trusted.stdout)["slug"] == "customer-owner"
    untrusted = subprocess.run(
        base + ["-e", "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt", "cli"],
        capture_output=True, check=False,
    )
    assert untrusted.returncode != 0, "CLI unexpectedly accepted an untrusted customer certificate"
    assert b"certificate" in untrusted.stderr.lower(), "failure must be certificate validation"
    print("PASS: authenticated public CLI with customer CA; untrusted certificate rejected")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    check(parser.parse_args().directory)
