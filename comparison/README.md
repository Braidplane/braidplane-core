# Comparison verification

Run the following commands from the repository root unless stated otherwise.

For the synthetic comparison dataset, use
[`verify_large_output.py`](verify_large_output.py) on the
original downloaded destination objects. It requires Python 3.12 or newer and
the pinned streaming parser:

```sh
cd comparison
python3 -m venv ../.worktree/comparison-venv
../.worktree/comparison-venv/bin/python -m pip install -r requirements.txt
../.worktree/comparison-venv/bin/python verify_large_output.py \
  --encoding sql-object-envelope --rows 500 --payload-bytes 1024 destination.json
```

At the current Core revision, SQL file rows are objects. Use the explicit
`sql-object-envelope` mode for that output. The `sql-envelope` mode preserves
verification of positional-row envelopes from earlier comparison fixtures.
For example, the positional envelope has this shape (the abbreviated payload below is illustrative):

```json
{"column_names":["order_id","amount_cents","payload"],"rows":[[1,1,"..."]],"row_count":1}
```

Column order determines each positional value's field. Object rows must instead
have exactly the named columns, with no duplicate JSON members. Member and record order
may vary. The verifier requires exactly these three unique column names, matching
row widths, and a nonnegative integer `row_count` matching each object's parsed
rows. It rejects missing or duplicate members, unexpected members, malformed or
truncated JSON, and trailing content. A metadata scan and a second streaming row
pass allow `rows` to precede `column_names` without buffering or rewriting the
result. Memory holds parser buffers, one row and a one-bit-per-expected-record
bitmap; it does not grow with total payload bytes. Inputs must be seekable local
files and must stay unchanged during verification.

The independent formula in [`datasets.sql`](datasets.sql)
requires exactly `order_id`, `amount_cents` and `payload`: integer IDs from 1
through the selected row count, integer amounts equal to `order_id % 100000`,
and a string repeating the lowercase MD5 hex digest of the decimal ID to the
selected payload length. Values are never coerced; extra, missing, duplicate,
out-of-range or changed records fail. Multiple object paths select one snapshot;
duplicate detection and completeness span all of them. Each SQL object's
`row_count` describes only that object.

The frozen workloads remain moderate (500 rows × 1,024 payload bytes), near-limit
(1,000 × 8,192) and heavy (1,000,000 × 1,024). Set `--rows` and `--payload-bytes`
accordingly. The sanitized SQL definition preserves their formula and sizes;
it creates a fresh synthetic schema and is not run by verifier tests.

Existing modes are `--encoding json` for one array of business row objects per
file and `--encoding airbyte-jsonl` for one `_airbyte_data` object per nonblank
line. Both apply the same exact business-field checks. A successful invocation
prints a JSON verdict with the SHA-256 and byte size of each original file;
verification errors exit nonzero. No intermediate row-array file is created.

Run the hermetic synthetic regression checks from `comparison/`:

```sh
../.worktree/comparison-venv/bin/python -m unittest test_verify_large_output
```

Comparison tooling has its own dependency and explicit test command; the standard
Core `task test` does not run these tests. These checks start no services and load
no SQL fixtures. They establish verifier behavior, not transfer throughput, capacity
or comparison readiness. Comparison execution and dataset loading need their
own authorization.

For disposable million-row execution, fault boundaries and measurement scope,
see [large SQL qualification](../doc/LARGE_SQL_RECOVERY_QUALIFICATION.md).
