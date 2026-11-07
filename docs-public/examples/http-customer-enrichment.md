# Enrich API orders with customer segments

HTTP orders and PostgreSQL customers feed a named-input left join, then write JSON to S3. Unknown customer IDs remain in the output with the explicit unmatched segment.

Use this when a lookup should enrich records without dropping unmatched source records. The fixture contains one known and one unknown customer.

## Set up and execute

Open the [canonical recipe](https://github.com/Braidplane/examples/blob/main/recipes/http-customer-enrichment/README.md) for provider prerequisites and copyable commands. It links the full spec, operations, logical binding template, synthetic fixtures and expected output. Follow its shared guide to validate the selected environment, deploy the exact version, start an explicit run and inspect that run's own step timeline.

Connector setup: [HTTP](../connectors/http.md), [PostgreSQL](../connectors/postgresql.md) and [S3](../connectors/s3.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Qualification and limits

The comparison checked both exported rows independently in S3-compatible storage. A deliberately invalid amount failed at the enrich-orders transform boundary with transform_contract_mismatch; the sink did not start. This does not prove source-boundary contract enforcement.

The object write overwrites its explicit key. Inspect the destination after an unknown write outcome before starting another run.

The canonical recipe includes independent destination checks and cleanup commands. Run them for your own resources; the checked-in receipt describes the dated qualification run, not your new execution. See [all examples and compatibility requirements](index.md).
