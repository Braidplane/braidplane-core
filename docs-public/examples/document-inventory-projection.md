# Project document inventory into reporting rows

MongoDB returns a JSON document array. An explicit row contract and SQL projection produce inventory rows for a MySQL reporting table.

Use this when document data needs a narrow relational projection. The fixture excludes negative stock and preserves zero stock.

## Set up and execute

Open the [canonical recipe](https://github.com/Braidplane/examples/blob/main/recipes/document-inventory-projection/README.md) for provider prerequisites and copyable commands. It links the full spec, operations, logical binding template, synthetic fixtures and expected output. Follow its shared guide to validate the selected environment, deploy the exact version, start an explicit run and inspect that run's own step timeline.

Connector setup: [MongoDB](../connectors/mongodb.md) and [MySQL](../connectors/mysql.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Qualification and limits

Independent MySQL readback matched both expected rows. MongoDB and MySQL were local containers; the record retains the executed spec hash and all successful step events.

The target uses a stable SKU key for upsert. Remove only the isolated fixture collection and reporting table during cleanup.

The canonical recipe includes independent destination checks and cleanup commands. Run them for your own resources; the checked-in receipt describes the dated qualification run, not your new execution. See [all examples and compatibility requirements](index.md).
