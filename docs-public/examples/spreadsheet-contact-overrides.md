# Apply spreadsheet overrides to contacts

S3 normalizes a selected XLSX worksheet into rows. A named-input join applies nonblank first-name overrides to PostgreSQL contacts, then upserts HubSpot contacts.

Use this when a small spreadsheet supplies explicit overrides while blank cells preserve the database value. The fixture changes one name to Alexandra and keeps Sam for the blank override.

## Set up and execute

Open the [recipe](https://github.com/Braidplane/examples/blob/main/recipes/spreadsheet-contact-overrides/README.md) for setup commands, the pipeline definition, connection bindings and sample data. Validate your environment, deploy the pipeline and inspect the run's step timeline.

Connector setup: [S3](../connectors/s3.md), [PostgreSQL](../connectors/postgresql.md) and [HubSpot](../connectors/hubspot.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Check the result

Read both destination contacts and compare their names with the expected output. Create the unique external_customer_id property before running the upsert.

Use a dedicated test account without contact workflows. Contact writes do not authorize email or subscriptions. A partial batch can leave successful writes even when a run fails.

Use the recipe's destination checks to compare your output, then follow its cleanup commands. See [all examples and compatibility requirements](index.md).
