# Reconcile settlements with the ledger

S3 CSV settlements and PostgreSQL ledger rows produce matched payments/refunds, amount mismatches and unmatched records in both directions. Two explicit transform branches append to BigQuery.

Use this when both sides of a reconciliation must remain visible. The five expected rows total 1,800 settled cents, 1,550 ledger cents and a 250-cent difference.

## Set up and execute

Open the [canonical recipe](https://github.com/Braidplane/examples/blob/main/recipes/settlement-ledger-reconciliation/README.md) for provider prerequisites and copyable commands. It links the full spec, operations, logical binding template, synthetic fixtures and expected output. Follow its shared guide to validate the selected environment, deploy the exact version, start an explicit run and inspect that run's own step timeline.

Connector setup: [S3](../connectors/s3.md), [PostgreSQL](../connectors/postgresql.md) and [BigQuery](../connectors/bigquery.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Qualification and limits

Independent readback checked all five rows and the exact cent totals. BigQuery was a local protocol mock, so Google Cloud account setup and acceptance remain unverified.

The two append sinks are independent and are not atomic. A new run can duplicate rows; inspect both destinations after a partial write.

The canonical recipe includes independent destination checks and cleanup commands. Run them for your own resources; the checked-in receipt describes the dated qualification run, not your new execution. See [all examples and compatibility requirements](index.md).
