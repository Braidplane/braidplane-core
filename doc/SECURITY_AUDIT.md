# Credential security audit

Credential audit records use the organization-scoped security audit list and CSV export. Both require `events:read` for the selected organization.

## Lifecycle

| Path | Recorded change |
| --- | --- |
| Static data source credentials | Creation and replacement. The existing data source delete record also identifies the attached revision. |
| Environment secrets | Creation, replacement and revocation. The existing environment secret delete remains in the same transaction. |
| Data source, organization GitLab and notification OAuth | Pending credential creation, authorization start, activation, rotation, refresh and local revocation. |
| HTTP OAuth client credentials | Grant activation and replacement. |
| Pipeline webhook signing keys | Trigger creation, deletion and key rotation record the affected key versions. |

The application writes each required audit row or its durable queue entry in the mutation transaction. Audit validation or persistence failure aborts the local mutation. OAuth exchanges and refresh calls happen outside that transaction. A provider can complete a call before local persistence fails. The local revocation record describes the committed local state. It does not certify provider-side revocation.

Audit metadata identifies stored credential revisions using a digest of the internal credential slug and update timestamp. The digest never uses plaintext, ciphertext or provider token material.

## Runtime release

Ordinary persisted credential resolution and admitted Proof credential resolution record `read` with a `released` or `denied` outcome. Ordinary resolution uses the server-owned request or worker job identity when available. Proof uses its admitted observation job identity. A retry with the same operation, resource, version and outcome reuses the existing record. Proof checks that only revalidate authority do not add release events.

A release requires a durable record before the resolver returns plaintext. An audit failure discards the plaintext and fails the operation. An already denied request remains denied when the recorder cannot write the denial. A rejected pre-tenant request cannot produce organization history.

The execution broker records `enveloped` for a versioned query, S3 preparation, schema, Git or Proof handoff. The audit row, encrypted execution request and queue handoff share a transaction. A broker record proves entry into the encrypted envelope. The executor still checks dispatch authority before connector work, and its retries reuse the same envelope. Draft connection tests with transient credentials lack a stored revision.

A webhook records the signing-key version once when a verified delivery is first committed. The audit shares the delivery transaction, including when a valid signature accompanies a rejected payload. Invalid signatures never establish a tenant audit identity.

## Retention and privileged access

Security audit rows follow the organization's event retention policy, which defaults to 30 days and supports 1 to 30 days. CSV export reads retained rows. Database owners, migration operators and administrators with equivalent access can alter or delete those rows. Organization deletion also removes its audit history.
