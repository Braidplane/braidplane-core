# Reconcile settlements with the ledger

S3 CSV settlements and PostgreSQL ledger rows produce matched payments/refunds, amount mismatches and unmatched records in both directions. Two explicit transform branches append to BigQuery.

Use this when both sides of a reconciliation must remain visible. The five expected rows total 1,800 settled cents, 1,550 ledger cents and a 250-cent difference.

## Set up and execute

Open the [recipe](https://github.com/Braidplane/examples/blob/main/recipes/settlement-ledger-reconciliation/README.md) for setup commands, the pipeline definition, connection bindings and sample data. Validate your environment, deploy the pipeline and inspect the run's step timeline.

Connector setup: [S3](../connectors/s3.md), [PostgreSQL](../connectors/postgresql.md) and [BigQuery](../connectors/bigquery.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Check the result

Query the destination and compare all five rows and their cent totals with the expected output.

The two append sinks are independent and are not atomic. A new run can duplicate rows; inspect both destinations after a partial write.

Use the recipe's destination checks to compare your output, then follow its cleanup commands. See [all examples and compatibility requirements](index.md).
