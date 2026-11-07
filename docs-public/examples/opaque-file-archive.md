# Archive a file without parsing its bytes

An HTTP file response passes directly to an S3 file sink. The pipeline has no row conversion or SQL transform.

Use this when the file itself is the artifact. The synthetic binary fixture is 31 bytes; its expected digest is checked after download from the destination.

## Set up and execute

Open the [canonical recipe](https://github.com/Braidplane/examples/blob/main/recipes/opaque-file-archive/README.md) for provider prerequisites and copyable commands. It links the full spec, operations, logical binding template, synthetic fixtures and expected output. Follow its shared guide to validate the selected environment, deploy the exact version, start an explicit run and inspect that run's own step timeline.

Connector setup: [HTTP](../connectors/http.md) and [S3](../connectors/s3.md). Keep connection credentials outside Git and use only the isolated resources you created for this exercise.

## Qualification and limits

Independent destination download matched the exact size and SHA-256 digest. Target validation without its logical binding also rejected execution with DATA_SOURCE_BINDING_NOT_FOUND.

The write overwrites its explicit object key. A new run is not retained-input replay or proof of exactly-once delivery.

The canonical recipe includes independent destination checks and cleanup commands. Run them for your own resources; the checked-in receipt describes the dated qualification run, not your new execution. See [all examples and compatibility requirements](index.md).
