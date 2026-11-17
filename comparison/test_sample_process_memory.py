import unittest

from sample_process_memory import process_tree


class ProcessMemoryTest(unittest.TestCase):
    def test_includes_only_root_and_transitive_children(self):
        rows = [("3", "2", "30"), ("1", "0", "10"), ("2", "1", "20"),
                ("4", "0", "1000")]
        self.assertEqual(process_tree(rows, 1), {1: 10, 2: 20, 3: 30})
        self.assertEqual(process_tree(rows, 2), {2: 20, 3: 30})
        self.assertIsNone(process_tree(rows, 99))
