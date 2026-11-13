"""Seed synthetic API identities and run Bruno against an owned local runtime."""

import os
from pathlib import Path
import subprocess


ROOT = Path(__file__).resolve().parent.parent


def main():
    base_url = os.environ["BRUNO_BASE_URL"]
    seed = subprocess.run(
        ["bash", "scripts/go-toolchain.sh", "run", "./cmd/fake", "--core-only"],
        cwd=ROOT, check=True, stdout=subprocess.PIPE, text=True,
    )
    fields = dict(line.split("=", 1) for line in seed.stdout.splitlines() if "=" in line)
    variables = {
        "baseUrl": base_url,
        "apiKey": fields["api_key_token"],
        "organization": fields["organization_slug"],
        "otherOrganization": fields["empty_organization_slug"],
        "project": fields["project_slug"],
    }
    args = ["bash", str(ROOT / "scripts/node-toolchain.sh"), "node",
            str(ROOT / "bruno/node_modules/@usebruno/cli/bin/bru.js"), "run"]
    for key, value in variables.items():
        if not value:
            raise ValueError(f"Missing Bruno fixture variable: {key}")
        args.extend(["--env-var", f"{key}={value}"])
    return subprocess.run(args, cwd=ROOT / "bruno", check=False).returncode


if __name__ == "__main__":
    raise SystemExit(main())
