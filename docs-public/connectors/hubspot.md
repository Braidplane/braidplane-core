# HubSpot

[Proof](../getting-started/proof.md#demonstrated-alpha-connector-scope) can use
explicitly authorized read-only account and contact-property metadata for a
pipeline assessment. This does not read contact records, verify a successful
write, or authorize pipeline execution.

Read contacts, companies, deals, and discovered custom CRM objects, or upsert
records using stable unique keys. HubSpot exposes CRM operations and property
metadata instead of requiring you to assemble raw HTTP requests.

Use the [shared connection workflow](connect-a-system.md) to create this data
source in your workspace. You can read this guide without an account or
provider resources. Running examples requires provider resources appropriate for your evaluation.

## Connect and authenticate

1. Choose HubSpot in your organization’s data sources and name the connection.
2. Choose read-only access for a source or **Write-enabled upsert destination**
   for a sink. HubSpot connections default to read-only.
3. Authorize the Braidplane integration in the intended HubSpot account. OAuth
   creation stays inactive until authorization completes successfully.
4. Test the connection and refresh the CRM schema.

A HubSpot private app token is also supported through the `private_app_token`
credential field. Use one credential mode for the connection; an expired or
revoked OAuth grant does not silently fall back to a stored static token.
Credentials are encrypted and are not returned in API responses.

| Connection field | Required | Meaning |
|---|---|---|
| `timeout_seconds` | No | Request timeout, default 5 seconds, range 1–300 |

### Permissions

Reads require the corresponding `crm.objects.<object>.read` scope; upserts need
both read and write access. Custom objects use `crm.objects.custom.read` and
`crm.objects.custom.write`. Optional permissions may be absent from an otherwise
valid grant. Changing a saved access mode does not elevate provider access:
save the writable mode, then use **Reconnect HubSpot** to request the required
scopes. Token refresh does not add scopes.

## Test and discover

The connection test checks account access. Refresh discovers supported standard
objects and account-specific properties, plus active custom-object schemas.
Use a custom object’s immutable `objectTypeId`, not its display label. Removed
or inaccessible custom objects fail explicitly; saved operations are not
redirected to another object with a similar label.

## Read example

Define a text `email` parameter with a synthetic contact address. Search returns
matching contacts with `hs_object_id`, email, and first name, plus pagination
metadata. It does not create a contact.

```json
{
  "type": "hubspot",
  "object_type": "contacts",
  "operation": "search",
  "properties": [
    "email",
    "firstname"
  ],
  "filter_groups": [
    {
      "filters": [
        {
          "property_name": "email",
          "operator": "EQ",
          "value": "{{email}}"
        }
      ]
    }
  ],
  "limit": 10
}
```

## Write example

First create `external_customer_id` as a unique HubSpot contact property and
grant access to it. Save this operation for a writable sink with
`idempotency_class: idempotent_with_key`. With upstream
`[{"customer_id":"demo-1","email":"alex@example.com","first_name":"Alex"}]`,
HubSpot creates or updates the record for that stable customer ID. The mapping
is from input field to HubSpot property.

```json
{
  "type": "hubspot",
  "object_type": "contacts",
  "operation": "upsert",
  "id_property": "external_customer_id",
  "property_mapping": {
    "customer_id": "external_customer_id",
    "email": "email",
    "first_name": "firstname"
  },
  "rows": "{{input}}"
}
```

## Operation reference

| Field | Meaning |
|---|---|
| `type` | `hubspot` |
| `object_type` | `contacts`, `companies`, `deals`, or a discovered custom object ID |
| `operation` | `list`, `search`, or `upsert` |
| `properties` | Read projection |
| `query`, `filter_groups`, `sorts` | Search terms, filter groups, and sort expressions |
| `limit` | Page size, 1–200 |
| `after` | Cursor from the preceding page |
| `id_property` | Existing unique property for upsert |
| `property_mapping` | Input-field-to-provider-property mapping, including the identity field |
| `rows` | Object/row array or `{{input}}` for sink input |

Filters use `property_name`, `operator`, and the appropriate `value`,
`high_value`, or `values`. Operators include EQ, NEQ, LT, LTE, GT, GTE, BETWEEN,
IN, NOT_IN, HAS_PROPERTY, NOT_HAS_PROPERTY, and CONTAINS_TOKEN. Values can use
`{{parameter_name}}` placeholders.

## Results, pagination, and replay

Reads return rows with `hs_object_id` and projected properties, plus
`column_names`, `row_count`, `next_cursor`, and `has_more`. Pass `next_cursor`
as `after` to request the next page. Pipeline sources use
`operation_type: hubspot`; a cursor contract can bind
`after: "{{runtime.incremental.lower_bound.cursor}}"` and observe `next_cursor`.

Sinks require a saved upsert operation and `idempotent_with_key`. Keys must
come from stable row values, not row order or run IDs. Contacts can use email
only where supported; partial upserts require a custom unique identifier.
Read-only, provider-managed, unknown, and unmapped properties are rejected.
Upsert results expose record IDs, created flags, and counts, not submitted
property values. Batch writes are not atomic: any row failure fails the step,
and replay uses the same unique keys.

## Limits and troubleshooting

- Upsert is the only write operation. No create-only, update-by-record-ID,
  associations, merge, schema mutation, archive, delete, or arbitrary API calls.
- Standard objects beyond contacts, companies, and deals are unsupported.
- File and multipart sink payloads are unsupported.
- Missing-scope errors require checking/re-authorizing the provider grant;
  changing `read_only` alone is insufficient.
- Unsupported custom object: refresh schema and verify account access.
- Upsert validation errors: check unique-property configuration, mappings,
  duplicate input keys, and writable fields.
- Reads may retry transient failures within a bounded deadline. Write rate
  limits, locks, timeouts, and provider failures fail the current attempt; use
  stable-key pipeline replay rather than assuming the entire batch rolled back.

See [operation and pipeline basics](connect-a-system.md#use-the-examples) for
where to use these statements, parameters, environment bindings, and replay
safety. Return to the [connector directory](index.md).
