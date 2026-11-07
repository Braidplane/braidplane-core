# Apply spreadsheet overrides to contacts

S3 normalizes a selected XLSX worksheet into rows. A named-input join applies nonblank first-name overrides to PostgreSQL contacts, then upserts HubSpot contacts.

Use this when a small spreadsheet supplies explicit overrides while blank cells preserve the database value. The fixture changes one name to Alexandra and keeps Sam for the blank override.

## Set up and execute

Open the [canonical recipe](https://github.com/Braidplane/examples/blob/main/recipes/spreadsheet-contact-overrides/README.md) for provider prerequisites and copyable commands. It links the full spec, operations, logical binding template, synthetic fixtures and expected output. Follow its shared guide to validate the selected environment, deploy the exact version, start an explicit run and inspect that run's own step timeline.

Connector setup: [S3](../connectors/s3.md), [PostgreSQL](../connectors/postgresql.md) and [HubSpot](../connectors/hubspot.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Qualification and limits

An independent mock-provider read checked both contacts. HubSpot was a local protocol mock; no real account or outreach was exercised. The destination requires the unique external_customer_id property.

Use a dedicated test account without contact workflows. Contact writes do not authorize email or subscriptions. A partial batch can leave successful writes even when a run fails.

The canonical recipe includes independent destination checks and cleanup commands. Run them for your own resources; the checked-in receipt describes the dated qualification run, not your new execution. See [all examples and compatibility requirements](index.md).
