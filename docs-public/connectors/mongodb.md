# MongoDB

Read documents with find, count, and aggregation operations, or write documents
to a collection. Nested fields remain structured values; inferred schemas are
samples rather than a guarantee that every document has the same shape.

Use the [shared connection workflow](connect-a-system.md) to create this data
source in your workspace. You can read this guide without an account or
provider resources. Running examples requires provider resources appropriate for your evaluation.

## Connection settings

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `host` | yes | string | — | Hostname or IP of the MongoDB server |
| `port` | yes | integer | 27017 | 1–65535 |
| `database` | yes | string | — | Database name |
| `auth_database` | no | string | `admin` | Authentication database |
| `connect_timeout` | no | integer | 5 | Connection timeout in seconds (1–300) |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

## Authentication and permissions

| Field | Required | Notes |
|---|:---:|---|
| `username` | yes | MongoDB user |
| `password` | yes | MongoDB password |

Credentials belong in the connection, never in an operation statement. They are
encrypted at rest and are not returned in API responses. Hosted/default
deployments block loopback and private network endpoints; your laptop’s
`localhost` is not the hosted worker’s network.

Use read-only provider permissions and `read_only: true` for a source. A
destination requires a separate write-capable account scoped to its collection.

## Test and discover

Test the connection and refresh metadata to sample collections and infer BSON
field types. Unreadable collections or documents can be omitted. Mixed field
types and nested documents are preserved; refresh after a schema change.

## Read example

Given a `customers` document with `email: "alex@example.com"` and `name: "Alex"`,
define a text `email` parameter with that address. The result contains the
matching email and name, with `_id` excluded.

```json
{
  "type": "mongodb",
  "collection": "customers",
  "operation": "find",
  "filter": {
    "email": "{{email}}"
  },
  "projection": {
    "_id": 0,
    "email": 1,
    "name": 1
  }
}
```

## Write example

On a writable connection, define text parameters `email` and `name`. Running
this direct operation inserts one document. Repeating an insert can create
duplicates; it is not an upsert.

```json
{
  "type": "mongodb",
  "collection": "customers",
  "operation": "insert_one",
  "document": {
    "email": "{{email}}",
    "name": "{{name}}"
  }
}
```

## Operation reference

| Field | Meaning |
|---|---|
| `type` | `mongodb` |
| `collection` | Target collection |
| `operation` | `find`, `aggregate`, `count_documents`, `insert_one`, `update_one`, `update_many`, `delete_one`, or `delete_many` in the public statement contract |
| `filter` | Document filter; use a bounded filter for updates/deletes |
| `sort` | Field names mapped to `1` (ascending) or `-1` (descending) |
| `projection` | Field names mapped to inclusion/exclusion flags |
| `document` | Write document; ignored by reads |

Parameters use `{{name}}`. Structured object/array placeholders keep their native
shape where the operation supports them; do not assemble JSON by string
concatenation. See the [API schema](../api/index.md) for the public statement
contract before choosing aggregation or bulk-write forms.

## Pipeline behavior

Use `operation_type: mongodb`. Find/aggregate pipeline output is a JSON document
array; row-based consumers normalize it to rows. Count results are metadata.
The public statement exposes no replace or upsert option. Sink operations bind the runtime input
through a supported structured field such as `document`. Match a single-document
operation to one object; do not pass a row array to `insert_one`.

Watermark sources must filter with the previous checkpoint and return the
configured watermark field. Incremental bounds can be referenced inside filters
as `{{runtime.incremental.lower_bound.value}}` and
`{{runtime.incremental.upper_bound.value}}`. Handle the initial empty checkpoint.

## Limitations and troubleshooting

- No public pagination or automatic change stream/CDC.
- Do not rely on the API’s `limit_value` field to bound current MongoDB reads;
  the connector does not apply that field. Use selective filters and the
  deployment’s execution limits.
- JSON-native transform execution is not generally available. Validate the
  source representation and downstream row contract before using SQL transforms.
- Empty discovery: check collection permissions and sample contents.
- Authentication failure: verify `auth_database` as well as the data database.
- Write rejection: check access mode, provider permissions, and object/array
  shape. Destructive operations need an explicit filter and writable access.

See [operation and pipeline basics](connect-a-system.md#use-the-examples) for
where to use these statements, parameters, environment bindings, and replay
safety. Return to the [connector directory](index.md).
