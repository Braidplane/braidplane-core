#!/usr/bin/env python3
"""Balance the complete discovered Go E2E inventory using recorded durations."""
import json
from pathlib import Path
import sys


def partition(packages, count, seconds):
    shards, totals = [[] for _ in range(count)], [0.0] * count
    # shortcut: new packages use one second; refresh measurements when shard timings diverge.
    for package in sorted(packages, key=lambda name: (-seconds.get(name, 1.0), name)):
        index = min(range(count), key=lambda index: (totals[index], index))
        shards[index].append(package)
        totals[index] += seconds.get(package, 1.0)
    return [sorted(shard) for shard in shards]


if __name__ == "__main__":
    index, count = map(int, sys.argv[1:])
    if count < 1 or not 0 <= index < count:
        raise ValueError("invalid shard index/count")
    profile = Path(__file__).resolve().parents[1] / "ci/go-e2e-durations.json"
    seconds = json.loads(profile.read_text())["seconds"]
    packages = [line.strip() for line in sys.stdin if line.strip()]
    for package in partition(packages, count, seconds)[index]:
        print(package)
