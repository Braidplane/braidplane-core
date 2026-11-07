# Pinned resource scopes and dynamic selectors

New compiled format 3 may explicitly map an exact ODCS contract revision/object
to a logical data source and saved operation through
the compilation API, CLI or deployment page. The referenced object must be pinned
on that operation's applicable input/output port. Imported ODCS servers never
configure connections, credentials or grants. The mapping is part of the
immutable operation content and compiled digest, separate from the authored
pipeline hash. Compilation and reload reject unmatched, duplicate, unsupported
or incomplete mappings.

## Select a scope during compilation

The retained-version compilation request accepts optional `resource_scopes`
alongside `projection`. Use `pipeline version compile ... --resource-scopes-file
scopes.json` or **Resource scope selection** on the deployment page. The file
and UI field contain a JSON array, not the enclosing API request. The selection
is bounded to 1 MiB and 128 mappings; unknown fields are rejected.

```json
[
  {
    "step": "fetch",
    "role": "query",
    "scope": {
      "policy_id": "s3/relative-key/v1",
      "data_source": "orders-source",
      "operation": "read-orders",
      "contract": "orders",
      "revision": "v1",
      "object_id": "row",
      "representation": "rows",
      "s3": {
        "endpoint": "https://storage.example",
        "region": "example-region",
        "bucket": "example-bucket",
        "prefix": "imports",
        "dynamic_key": true,
        "force_path_style": false
      }
    }
  }
]
```

`role` is `query` or `sink`; `data_source` is the authored logical reference,
not the environment's concrete source. The selected saved operation must match
that step and role. The exact revision/object must already be pinned on its
applicable port in this project. `latest`, unmatched or duplicate selections,
unsupported connectors and invalid policy values fail before a plan is stored.
Select the compilation environment to locate saved operations through existing
bindings. Credentials and grants cannot be supplied in the selection.

Retained plan inspection reports the policy, logical source, saved operation,
contract revision/object and representation. It omits endpoint, region, bucket,
prefix and selector settings. Deployment and attempts retain the selected plan
and operation digests, preserving the full policy internally. Turning off
composition admission prevents new compilation while authorized historical
inspection remains available.

The portable `s3/relative-key/v1` policy supports `rows` from explicitly selected
CSV or JSON operations. It fixes a concrete endpoint URL, addressing mode,
region, bucket, normalized configured prefix, operation, parsing and
overwrite policy. Only an explicitly declared `dynamic_key` may select another
relative key under that prefix. A new partition does not require redeployment.
Content, content type and object metadata remain ordinary execution values.
Scoped plans require an endpoint URL even for AWS, so ambient SDK endpoint
settings cannot select another host.
The connector catalog advertises these roles and reports other connectors as
unsupported for confinement. Auto format, file/blob and spreadsheet mappings
are currently unsupported for this policy; their ordinary execution support
does not imply a fixed-target claim. SQL parameters retain value binding and
never become interpolated identifiers or SQL text.

The S3 adapter decodes a private execution copy, applies existing typed
parameter substitutions and sink overrides, then validates the final selector
before object I/O. Current endpoint, addressing mode, region, bucket and normalized configured
prefix must match the captured scope. Bucket changes, undeclared key changes,
absolute paths, empty/dot/traversal segments, encoded paths, backslashes, control
characters, URLs and unresolved tokens fail closed. Percent escapes are rejected
as literal input, including nested encodings; this policy does not decode or
normalize them into permitted paths. Scoped requests follow no redirects.
Runtime changes to parsing or overwrite fields also fail before dispatch.

Scope errors contain no resolved values. Scoped results expose existing typed
operation/format/count metadata and payload rows, while omitting target paths,
resource names and file metadata. Provider error chains are replaced with a
safe scoped execution failure because they may contain paths, URLs or sensitive
hashes. Fixed, sanitized S3 provider explanations and HTTP statuses are retained;
typed row/byte limit failures retain only their safe sentinel. Hashing an
input-derived key does not make it safe evidence. Execution
requests remain inside the existing encrypted worker channel; no resolved key
or hash is added as provenance to logs, timelines or Proof.

Format 1 remains inspectable. Format 2 remains executable with its original
saved-operation semantics. Neither can acquire format 3 scope claims. A format 3
operation without a resource mapping makes no confinement claim. Historical
unscoped S3 bucket overrides retain their original meaning.

Snapshot execution and scoped worker requests are available without a rollout
control or live provider probe. [Deployment resolutions](DEPLOYMENT_RESOLUTIONS.md)
bind exact templates and scopes to an environment, check current authority, and
select each attempt atomically. This scope policy does not grant access.
