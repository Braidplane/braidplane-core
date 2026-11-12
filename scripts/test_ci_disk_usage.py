import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch

from scripts.ci_disk_usage import measure, report, sample


class DiskUsage(unittest.TestCase):
    def test_preserves_lowest_available_space(self):
        state = {}
        for free in (80, 40, 70):
            state = measure(state, SimpleNamespace(f_bavail=free, f_frsize=1024, f_blocks=100))
        self.assertEqual(state, {"samples": 3, "total_bytes": 102400,
                                 "minimum_available_bytes": 40960, "last_available_bytes": 71680})

    def test_report_stops_sampler_and_keeps_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory) / "disk"
            with patch("scripts.ci_disk_usage.time.sleep", side_effect=lambda _: base.with_suffix(".stop").touch()):
                sample(base)
            with patch("builtins.print") as output:
                report(base)
            state = json.loads(base.with_suffix(".json").read_text())
            self.assertEqual(state["samples"], 1)
            self.assertTrue(base.with_suffix(".stop").exists())
            self.assertIn("minimum_available_bytes", output.call_args.args[0])
