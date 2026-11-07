# Local HTTP timestamp ingestion

This synthetic recipe runs a paginated HTTP source, an idempotent HTTP target,
the real query service/HTTP connector, and a PostgreSQL checkpoint store. It
uses disposable local fixtures, requires Docker, and contacts no customer API.
The test owns and removes its containers. No running Braidplane app is required.

From the repository root:

```bash
bash scripts/go-toolchain.sh test ./service/queriessvc/e2e \
  -run '^TestQueryServiceE2E/HTTPWatermarkCommittedAndBlockedCheckpoints$' \
  -count=1 -v
```

The fixture's [`http_watermark_test.go`](../../../service/queriessvc/e2e/http_watermark_test.go)
is the executable example. It creates the source and target operations through
services and runs this sequence:

First, an empty initial read persists a null checkpoint. The next run must still
omit the lower filter, including when lookback is configured.

1. With no timestamp boundary, omit `updated_since`. Drain two pages containing equal
   timestamps. Upsert both IDs into the synthetic target and commit their maximum
   timestamp, `2026-05-24T10:00:00Z`.
2. Apply a five-minute lookback. Both pages receive the same lower bound,
   `2026-05-24T09:55:00Z`. Read newer data but fail the dependent target write.
   Read back the blocked checkpoint and the still-committed `10:00` boundary.
3. Retry from that committed boundary, again sending `09:55` on both pages.
   Upsert by record ID, so overlap creates no duplicate records. Read back the
   committed `11:00` checkpoint and assert the target contains exactly two IDs.
4. Return a late-arriving record timestamped `10:58`. The `10:55` filter includes
   it, excludes the old `10:00` record, and upserts the late ID. Retain the `11:00`
   checkpoint, with exactly three target IDs.
5. Read an empty result from `10:55` after lookback. Retain the `11:00` checkpoint.

The page token `page-2` controls traversal only. The durable checkpoint is a
`{"value":"..."}` timestamp envelope. The target acknowledgement requires HTTP
200 and `{"ok":true}`; a completed request with an unsuccessful acknowledgement
cannot advance source progress.

For focused saved/inline, upper-bound, invalid-record and cancellation checks:

```bash
bash scripts/go-toolchain.sh test ./service/queriessvc \
  -run 'TestHTTPWatermark' -count=1 -v
```

To adapt the source in the UI builder, select **Watermark**, use
`items[].updated_at` as its field, and set **Lookback window** to `5m`. Add the
same field to its JSON object output contract. API/CLI definitions use the
[`query.incremental` example](../../data-sources/HTTP.md#timestamp-watermarks).
The source operation may be saved or inline. Its `updated_since` and
`updated_until` bindings use `runtime.incremental.lower_bound.value` and
`runtime.incremental.upper_bound.value`. Use bootstrap/backfill operations to
set initial or bounded runtime state rather than embedding checkpoint values in
the pipeline definition. Seed and bound envelopes must contain RFC3339 strings
with a timezone and no more than nine fractional digits, for example
`{"value":"2026-05-24T10:00:00.123456789Z"}`. Numeric, null and malformed bounds
are rejected at admission.

The synthetic target upserts by ID. For a real target, implement upsert or
record-level deduplication and bind `runtime.sink.idempotency_key` if its API
supports request idempotency. Merely declaring a replay class does not make an
arbitrary write safe. Verify provider filter inclusivity, timestamp precision,
ordering, snapshot behavior, and late-arrival bounds before adopting the recipe.
This fixture proves local behavior; hosted and real-provider acceptance remain
separate work.
