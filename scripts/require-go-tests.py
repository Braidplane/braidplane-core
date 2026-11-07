#!/usr/bin/env python3
"""Print Go test output and reject an empty or skipped required suite."""
import argparse
import json
import sys


def check(lines, packages=()):
    passed = set()
    skipped = []
    tested_packages = set()
    completed_packages = set()
    failed = set()
    for line in lines:
        event = json.loads(line)
        sys.stdout.write(event.get("Output", ""))
        package = event.get("Package")
        if event["Action"] == "fail":
            failed.add(package or event.get("Test", "unknown"))
        if event["Action"] == "pass" and not event.get("Test"):
            completed_packages.add(package)
        if event.get("Test"):
            if event["Action"] == "pass":
                passed.add((package, event["Test"]))
                tested_packages.add(package)
            elif event["Action"] == "skip":
                skipped.append(event["Test"])
    missing_packages = set(packages) - (tested_packages & completed_packages)
    if not passed or skipped or failed or missing_packages:
        raise ValueError(f"Required Go suite: {len(passed)} passed, skipped: {skipped}, "
                         f"failed: {sorted(failed)}, "
                         f"missing packages: {sorted(missing_packages)}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--packages", nargs="+", default=[])
    args = parser.parse_args()
    try:
        check(sys.stdin, args.packages)
    except (ValueError, KeyError) as error:
        sys.exit(str(error))
