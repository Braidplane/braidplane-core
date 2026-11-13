import importlib.util
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("sharding", ROOT / "scripts/shard-go-tests.py")
sharding = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sharding)


class GoShardingTest(unittest.TestCase):
    def test_measured_heavy_packages_are_separated_and_new_packages_are_kept(self):
        seconds = {"pipeline": 150, "query": 90, "worker": 25}
        packages = ["new", "pipeline", "query", "worker"]
        shards = sharding.partition(packages, 4, seconds)
        self.assertEqual(shards, sharding.partition(packages[::-1], 4, seconds))
        self.assertEqual(sorted(p for shard in shards for p in shard), sorted(packages))
        self.assertEqual(sum(bool(set(shard) & set(seconds)) for shard in shards), 3)

    def test_baseline_is_balanced_without_omitting_any_discovered_package(self):
        seconds = json.loads((ROOT / "ci/go-e2e-durations.json").read_text())["seconds"]
        packages = sorted(seconds)
        shards = sharding.partition(packages, 4, seconds)
        self.assertEqual(sorted(p for shard in shards for p in shard), packages)
        totals = [sum(seconds[p] for p in shard) for shard in shards]
        old = [sum(seconds[p] for p in packages[index::4]) for index in range(4)]
        self.assertLess(max(totals), max(old) * 0.65)
