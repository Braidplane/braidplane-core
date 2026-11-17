"""Independently verify original destination objects against datasets.sql."""
import argparse
import hashlib
import json
from pathlib import Path

import ijson

FIELDS = {"order_id", "amount_cents", "payload"}


class UniqueObject(dict):
    def __setitem__(self, key, value):
        if key in self:
            raise ValueError("Duplicate JSON member: " + key)
        super().__setitem__(key, value)


def unique_object(pairs):
    result = UniqueObject()
    for key, value in pairs:
        result[key] = value
    return result


def invalid_constant(value):
    raise ValueError("Invalid JSON constant: " + value)


def sql_metadata(stream):
    """Scan metadata without building rows; JSON members may appear in any order."""
    events = iter(ijson.parse(stream))
    if next(events, None) != ("", "start_map", None):
        raise ValueError("Expected one SQL envelope")
    members = set()
    columns = None
    count = None
    for prefix, event, value in events:
        if (prefix, event) == ("", "end_map"):
            break
        if prefix != "" or event != "map_key" or value not in {"column_names", "rows", "row_count"}:
            raise ValueError("Unexpected SQL envelope member")
        if value in members:
            raise ValueError("Duplicate SQL envelope member")
        members.add(value)
        member = value
        first = next(events)
        if member == "row_count":
            count = first[2]
            if first[1] != "number" or type(count) is not int or count < 0:
                raise ValueError("Expected a nonnegative integer row_count")
        else:
            if first != (member, "start_array", None):
                raise ValueError("Expected an array for " + member)
            if member == "column_names":
                columns = []
                for entry in events:
                    if entry == (member, "end_array", None):
                        break
                    if entry[1] != "string" or entry[2] not in FIELDS or entry[2] in columns:
                        raise ValueError("Invalid or duplicate SQL column")
                    columns.append(entry[2])
                if set(columns) != FIELDS:
                    raise ValueError("SQL columns differ from the frozen dataset")
            else:
                depth = 1
                row_keys = set()
                for prefix, event, value in events:
                    if prefix == "rows.item" and event == "start_map":
                        row_keys = set()
                    if prefix == "rows.item" and event == "map_key":
                        if value in row_keys:
                            raise ValueError("Duplicate SQL row member: " + value)
                        row_keys.add(value)
                    depth += (event in {"start_array", "start_map"}) - (event in {"end_array", "end_map"})
                    if depth == 0:
                        break
    else:
        raise ValueError("Incomplete SQL envelope")
    if members != {"column_names", "rows", "row_count"}:
        raise ValueError("Missing SQL envelope member")
    if next(events, None) is not None:
        raise ValueError("Trailing SQL envelope data")
    return columns, count


def sql_records(stream, *, object_rows=False):
    columns, expected_count = sql_metadata(stream)
    # A metadata pass permits rows before column_names without buffering the result.
    stream.seek(0)
    count = 0
    for values in ijson.items(stream, "rows.item", map_type=UniqueObject):
        if object_rows:
            if not isinstance(values, dict) or set(values) != set(columns):
                raise ValueError("SQL row fields differ from column_names")
            row = values
        else:
            if not isinstance(values, list) or len(values) != len(columns):
                raise ValueError("SQL row width differs from column_names")
            row = dict(zip(columns, values))
        count += 1
        yield row
    if count != expected_count:
        raise ValueError("SQL row_count differs from parsed rows")


def records(path, encoding):
    with path.open("rb") as stream:
        if encoding == "json":
            events = ijson.parse(stream)
            if next(events, None) != ("", "start_array", None):
                raise ValueError("Expected one JSON array")
            yield from ijson.items(events, "item", map_type=UniqueObject)
        elif encoding in {"sql-envelope", "sql-object-envelope"}:
            yield from sql_records(stream, object_rows=encoding == "sql-object-envelope")
        elif encoding == "airbyte-jsonl":
            for line in stream:
                if line.strip():
                    envelope = json.loads(line, object_pairs_hook=unique_object,
                                          parse_constant=invalid_constant)
                    if not isinstance(envelope, dict) or "_airbyte_data" not in envelope:
                        raise ValueError("Expected an Airbyte record envelope")
                    yield envelope["_airbyte_data"]
        else:
            raise ValueError("Unknown encoding")


def verify(paths, encoding, rows, payload_bytes):
    if rows < 1 or payload_bytes < 32 or payload_bytes % 32:
        raise ValueError("Invalid frozen dataset parameters")
    seen = bytearray((rows + 7) // 8)
    count = 0
    objects = []
    for path in paths:
        digest = hashlib.sha256()
        with path.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
        objects.append({"file": path.name, "bytes": path.stat().st_size,
                        "sha256": digest.hexdigest()})
        for row in records(path, encoding):
            if not isinstance(row, dict) or set(row) != FIELDS:
                raise ValueError("Business field set differs from the frozen dataset")
            order_id = row["order_id"]
            if type(order_id) is not int or not 1 <= order_id <= rows:
                raise ValueError("Invalid order_id or type")
            index, bit = divmod(order_id - 1, 8)
            if seen[index] & (1 << bit):
                raise ValueError("Duplicate order_id in the selected snapshot")
            # MD5 defines synthetic fixture bytes; it is not an integrity hash.
            expected_payload = hashlib.md5(str(order_id).encode()).hexdigest() * (payload_bytes // 32)
            if (type(row["amount_cents"]) is not int or row["amount_cents"] != order_id % 100000
                    or row["payload"] != expected_payload):
                raise ValueError("Record value or type differs from the frozen formula")
            seen[index] |= 1 << bit
            count += 1
    if count != rows:
        raise ValueError("Missing records in the selected snapshot")
    return {"verdict": "passed", "records": count, "payload_bytes_per_row": payload_bytes,
            "objects": objects, "oracle": "datasets.sql independent per-record formula"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--encoding", choices=["json", "airbyte-jsonl", "sql-envelope", "sql-object-envelope"], required=True)
    parser.add_argument("--rows", type=int, required=True)
    parser.add_argument("--payload-bytes", type=int, required=True)
    parser.add_argument("objects", type=Path, nargs="+")
    args = parser.parse_args()
    try:
        result = verify(args.objects, args.encoding, args.rows, args.payload_bytes)
    except (OSError, ValueError, ijson.JSONError) as error:
        parser.exit(1, f"Verification failed: {error}\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
