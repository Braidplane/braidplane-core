import tempfile
import unittest
from unittest.mock import patch

from sample_host import measure


class HostMeasurementTest(unittest.TestCase):
    def test_one_filesystem_and_failed_workload(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch("sample_host.used_memory", return_value=1024):
                result = measure([directory, directory + "/."], ["sh", "-c", "exit 7"])
        self.assertEqual(result["exit_code"], 7)
        self.assertEqual(result["peak_used_ram_bytes"], 1024)
        self.assertEqual(len(result["filesystem_free_bytes"]), 1)
        fs = result["filesystem_free_bytes"][0]
        self.assertEqual(fs["peak_additional_bytes"], fs["before"] - fs["minimum"])
