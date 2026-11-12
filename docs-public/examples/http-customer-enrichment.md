# Enrich API orders with customer segments

HTTP orders and PostgreSQL customers feed a named-input left join, then write JSON to S3. Unknown customer IDs remain in the output with the explicit unmatched segment.

Use this when a lookup should enrich records without dropping unmatched source records. The fixture contains one known and one unknown customer.

## Set up and execute

Open the [recipe](https://github.com/Braidplane/examples/blob/main/recipes/http-customer-enrichment/README.md) for setup commands, the pipeline definition, connection bindings and sample data. Validate your environment, deploy the pipeline and inspect the run's step timeline.

Connector setup: [HTTP](../connectors/http.md), [PostgreSQL](../connectors/postgresql.md) and [S3](../connectors/s3.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Check the result

Compare both exported rows in S3 with the expected output. The recipe also includes an invalid amount that produces transform_contract_mismatch at the enrich-orders transform, before the sink starts.

The object write overwrites its explicit key. Inspect the destination after an unknown write outcome before starting another run.

Use the recipe's destination checks to compare your output, then follow its cleanup commands. See [all examples and compatibility requirements](index.md).
