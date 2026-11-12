# Archive a file without parsing its bytes

An HTTP file response passes directly to an S3 file sink. The pipeline has no row conversion or SQL transform.

Use this when the file itself is the artifact. The synthetic binary fixture is 31 bytes; its expected digest is checked after download from the destination.

## Set up and execute

Open the [recipe](https://github.com/Braidplane/examples/blob/main/recipes/opaque-file-archive/README.md) for setup commands, the pipeline definition, connection bindings and sample data. Validate your environment, deploy the pipeline and inspect the run's step timeline.

Connector setup: [HTTP](../connectors/http.md) and [S3](../connectors/s3.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Check the result

Download the destination object and compare its size and SHA-256 digest with the fixture. Configure the logical binding before validation; a missing binding returns DATA_SOURCE_BINDING_NOT_FOUND.

The write overwrites its explicit object key. Starting another run reads the source again and can replace the output object.

Use the recipe's destination checks to compare your output, then follow its cleanup commands. See [all examples and compatibility requirements](index.md).
