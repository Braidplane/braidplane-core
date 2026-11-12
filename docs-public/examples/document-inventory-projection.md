# Project document inventory into reporting rows

MongoDB returns a JSON document array. An explicit row contract and SQL projection produce inventory rows for a MySQL reporting table.

Use this when document data needs a narrow relational projection. The fixture excludes negative stock and preserves zero stock.

## Set up and execute

Open the [recipe](https://github.com/Braidplane/examples/blob/main/recipes/document-inventory-projection/README.md) for setup commands, the pipeline definition, connection bindings and sample data. Validate your environment, deploy the pipeline and inspect the run's step timeline.

Connector setup: [MongoDB](../connectors/mongodb.md) and [MySQL](../connectors/mysql.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Check the result

Query the MySQL reporting table and compare both rows with the expected output. Check the executed version and step results in the run timeline.

The target uses a stable SKU key for upsert. Remove only the isolated fixture collection and reporting table during cleanup.

Use the recipe's destination checks to compare your output, then follow its cleanup commands. See [all examples and compatibility requirements](index.md).
