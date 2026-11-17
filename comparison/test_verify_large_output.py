"""Hermetic checks using only synthetic destination objects."""
import copy
import hashlib
import io
import json
import subprocess
import sys
from pathlib import Path
import tempfile
import unittest

import ijson

from verify_large_output import sql_records, verify


class LargeOracleTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path = Path(self.temp.name) / "output.json"
        self.rows = [
            {"order_id": 1, "amount_cents": 1, "payload": "c4ca4238a0b923820dcc509a6f75849b"},
            {"order_id": 2, "amount_cents": 2, "payload": "c81e728d9d4c2f636f067f89cc14862c"},
        ]
        self.addCleanup(self.temp.cleanup)

    def test_array_and_envelope_order_normalization(self):
        self.path.write_text(json.dumps(list(reversed(self.rows))))
        self.assertEqual(verify([self.path], "json", 2, 32)["records"], 2)
        self.path.write_text("\n".join(json.dumps({"_airbyte_data": row, "_airbyte_ab_id": "synthetic"})
                                       for row in self.rows))
        self.assertEqual(verify([self.path], "airbyte-jsonl", 2, 32)["records"], 2)

    def test_rejects_missing_duplicate_wrong_type_payload_and_extra_fields(self):
        invalid = [self.rows[:1], [self.rows[0], self.rows[0]],
                   [self.rows[0] | {"amount_cents": True}, self.rows[1]],
                   [self.rows[0] | {"order_id": "1"}, self.rows[1]],
                   [self.rows[0] | {"payload": "wrong"}, self.rows[1]],
                   [self.rows[0] | {"extra": 1}, self.rows[1]]]
        for rows in invalid:
            with self.subTest(rows=rows):
                self.path.write_text(json.dumps(rows))
                with self.assertRaises(ValueError):
                    verify([self.path], "json", 2, 32)
        for malformed in ["[", "{}", json.dumps(self.rows)[:-1]]:
            self.path.write_text(malformed)
            with self.assertRaises((ValueError, ijson.JSONError)):
                verify([self.path], "json", 2, 32)

    def envelope(self):
        return {"column_names": ["order_id", "amount_cents", "payload"],
                "rows": [list(row.values()) for row in self.rows], "row_count": 2}

    def test_sql_mapping_member_order_and_original_hash(self):
        envelope = self.envelope()
        envelope["column_names"] = ["payload", "amount_cents", "order_id"]
        envelope["rows"] = [list(reversed(row)) for row in reversed(envelope["rows"])]
        # SQL member order has no meaning; rows may precede their column metadata.
        raw = json.dumps({key: envelope[key] for key in ("rows", "row_count", "column_names")}).encode() + b" \n"
        self.path.write_bytes(raw)
        result = verify([self.path], "sql-envelope", 2, 32)
        self.assertEqual(result["records"], 2)
        self.assertEqual(result["objects"], [{"file": self.path.name, "bytes": len(raw),
                                             "sha256": hashlib.sha256(raw).hexdigest()}])
        self.assertEqual(self.path.read_bytes(), raw)

    def test_actual_core_object_envelope(self):
        envelope = self.envelope() | {"rows": list(reversed(self.rows))}
        raw = json.dumps(envelope)
        self.path.write_text(raw)
        result = verify([self.path], "sql-object-envelope", 2, 32)
        self.assertEqual(result["records"], 2)
        self.assertEqual(result["objects"][0]["sha256"], hashlib.sha256(raw.encode()).hexdigest())
        invalid = [raw.replace('"order_id": 1', '"order_id": 1, "order_id": 1'),
                   raw[:-1], raw + " {}"]
        for rows in ([self.rows[0]], [self.rows[0], self.rows[0]],
                     [self.rows[0] | {"extra": 1}, self.rows[1]],
                     [self.rows[0] | {"amount_cents": True}, self.rows[1]],
                     [self.rows[0] | {"payload": "changed"}, self.rows[1]],
                     self.envelope()["rows"]):
            invalid.append(json.dumps(envelope | {"rows": rows}))
        for value in invalid:
            with self.subTest(value=value):
                self.path.write_text(value)
                with self.assertRaises((ValueError, ijson.JSONError)):
                    verify([self.path], "sql-object-envelope", 2, 32)

    def test_sql_invalid_structure(self):
        invalid = []
        for key in self.envelope():
            value = self.envelope()
            del value[key]
            invalid.append(value)
        for columns in (None, {}, [], ["order_id", "payload"],
                        ["order_id", "amount_cents", "payload", "extra"],
                        ["order_id", "order_id", "payload"], [1, "amount_cents", "payload"]):
            invalid.append(self.envelope() | {"column_names": columns})
        for count in (-1, True, "2", 2.0, None, 1, 3):
            invalid.append(self.envelope() | {"row_count": count})
        for rows in (None, {}, [1], [{}], [[1, 1]], [[1, 1, "x", 4]]):
            invalid.append(self.envelope() | {"rows": rows})
        invalid.append(self.envelope() | {"extra": 1})
        for value in invalid:
            with self.subTest(value=value):
                self.path.write_text(json.dumps(value))
                with self.assertRaises((ValueError, ijson.JSONError)):
                    verify([self.path], "sql-envelope", 2, 32)

    def test_all_formats_reject_changed_records(self):
        mutations = [{"order_id": True}, {"order_id": "1"}, {"order_id": 1.0},
                     {"order_id": 0}, {"order_id": 3}, {"amount_cents": True},
                     {"amount_cents": 1.0}, {"amount_cents": "1"}, {"amount_cents": 9},
                     {"payload": None}, {"payload": 1}, {"payload": "wrong"}, {"extra": 1}]
        datasets = [[self.rows[0] | mutation, self.rows[1]] for mutation in mutations]
        datasets += [self.rows[:1], [self.rows[0], self.rows[0]], [], self.rows + [self.rows[0]]]
        missing = copy.deepcopy(self.rows)
        del missing[0]["payload"]
        datasets.append(missing)
        for encoding in ("json", "sql-envelope", "airbyte-jsonl"):
            for rows in datasets:
                with self.subTest(encoding=encoding, rows=rows):
                    self.write_rows(encoding, rows)
                    with self.assertRaises((ValueError, ijson.JSONError)):
                        verify([self.path], encoding, 2, 32)

    def write_rows(self, encoding, rows):
        if encoding == "sql-envelope":
            value = self.envelope() | {"rows": [list(row.values()) for row in rows], "row_count": len(rows)}
            raw = json.dumps(value)
        elif encoding == "airbyte-jsonl":
            raw = "\n".join(json.dumps({"_airbyte_data": row}) for row in rows)
        else:
            raw = json.dumps(rows)
        self.path.write_text(raw)

    def test_json_syntax_duplicates_truncation_and_trailing_data(self):
        for encoding in ("json", "sql-envelope", "airbyte-jsonl"):
            self.write_rows(encoding, self.rows)
            valid = self.path.read_text()
            invalid = [valid[:-1], valid + "x", valid + " {}", valid + " []"]
            invalid += [valid.replace('"order_id": 1', '"order_id": 1, "order_id": 1')]
            if encoding == "sql-envelope":
                invalid[-1] = valid.replace('"row_count": 2', '"row_count": 2, "row_count": 2')
                invalid += [valid.replace('"rows": [', '"rows": [,', 1)]
            for raw in invalid:
                with self.subTest(encoding=encoding, raw=raw):
                    self.path.write_text(raw)
                    with self.assertRaises((ValueError, ijson.JSONError)):
                        verify([self.path], encoding, 2, 32)

    def test_multiple_objects_and_cross_object_duplicates(self):
        other = self.path.with_name("part2.json")
        for encoding in ("json", "sql-envelope", "airbyte-jsonl"):
            self.write_rows(encoding, self.rows[:1])
            first = self.path.read_bytes()
            self.write_rows(encoding, self.rows[1:])
            other.write_bytes(self.path.read_bytes())
            self.path.write_bytes(first)
            self.assertEqual(verify([self.path, other], encoding, 2, 32)["records"], 2)
            other.write_bytes(first)
            with self.assertRaises(ValueError):
                verify([self.path, other], encoding, 2, 32)

    def test_incremental_bounded_reads(self):
        class BoundedInput(io.BytesIO):
            def read(self, size=-1):
                if not 0 <= size <= 65536:
                    raise AssertionError("Unbounded read")
                return super().read(min(size, 127))

            def readinto(self, buffer):
                data = self.read(len(buffer))
                buffer[:len(data)] = data
                return len(data)

        envelope = self.envelope()
        envelope["rows"] *= 3000
        envelope["row_count"] = 6000
        for object_rows in (False, True):
            if object_rows:
                envelope["rows"] = self.rows * 3000
            raw = json.dumps(envelope).encode()
            stream = BoundedInput(raw)
            rows = sql_records(stream, object_rows=object_rows)
            self.assertEqual(next(rows)["order_id"], 1)
            self.assertLess(stream.tell(), len(raw))
            self.assertEqual(1 + sum(1 for _ in rows), 6000)

    def test_cli_failure_is_nonzero(self):
        self.path.write_text("{}")
        result = subprocess.run([sys.executable, str(Path(__file__).with_name("verify_large_output.py")),
                                 "--encoding", "sql-envelope", "--rows", "2", "--payload-bytes", "32",
                                 str(self.path)], capture_output=True, text=True, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Missing SQL envelope member", result.stderr)
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
