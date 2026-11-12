#!/usr/bin/env python3
"""Sample available runner disk space until the job requests a report."""
import json
import os
from pathlib import Path
import sys
import time


def measure(previous, usage):
    available = usage.f_bavail * usage.f_frsize
    return {"samples": previous.get("samples", 0) + 1,
            "total_bytes": usage.f_blocks * usage.f_frsize,
            "minimum_available_bytes": min(previous.get("minimum_available_bytes", available), available),
            "last_available_bytes": available}


def sample(base):
    state = {}
    while not base.with_suffix(".stop").exists():
        state = measure(state, os.statvfs("/"))
        temporary = base.with_suffix(".tmp")
        temporary.write_text(json.dumps(state))
        temporary.replace(base.with_suffix(".json"))
        time.sleep(5)


def report(base):
    base.with_suffix(".stop").touch()
    state = json.loads(base.with_suffix(".json").read_text())
    print("Runner disk (5-second samples): " + json.dumps(state, sort_keys=True))


if __name__ == "__main__":
    command, path = sys.argv[1:]
    {"sample": sample, "report": report}[command](Path(path))
