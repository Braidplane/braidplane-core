# Credential security audit

Credential audit records use the organization-scoped security audit list and CSV export. Both require `events:read` for the selected organization.

## Lifecycle

| Path | Recorded change |
| --- | --- |
| Static data source credentials | Creation and replacement. The existing data source delete record also identifies the attached revision. |
| Environment secrets | Creation, replacement and revocation. The existing environment secret delete remains in the same transaction. |
| Data source, organization GitLab and notification OAuth | Pending credential creation, authorization start, activation, rotation, refresh and local revocation. |
| HTTP OAuth client credentials | Grant activation and replacement. |
| GitHub App installations | Authorization, disconnect, suspension, resumption, deletion and repository binding changes record the resulting installation revision. |
| Pipeline webhook signing keys | Trigger creation, deletion and key rotation record the affected key versions. |

The application writes each required audit row or its durable queue entry in the mutation transaction. Audit validation or persistence failure aborts the local mutation. OAuth exchanges and refresh calls happen outside that transaction. A provider can complete a call before local persistence fails. The local revocation record describes the committed local state. It does not certify provider-side revocation.

Audit metadata identifies stored credential revisions using a digest of the internal credential slug and update timestamp. The digest never uses plaintext, ciphertext or provider token material.

## Runtime release

Ordinary persisted credential resolution and admitted Proof credential resolution record `read` with a `released` or `denied` outcome. Ordinary resolution uses an opaque identifier derived from the server-owned request or worker job. Match the audit request ID to `audit_operation_id` in logs or traces. Proof uses its admitted observation job identity. A retry with the same operation, resource, version and outcome reuses the existing record. Proof checks that only revalidate authority do not add release events.

A release requires a durable record before the resolver returns plaintext. An audit failure discards the plaintext and fails the operation. An already denied request remains denied when the recorder cannot write the denial. A rejected pre-tenant request cannot produce organization history.

The execution broker records `enveloped` for a versioned query, S3 preparation, schema, Git or Proof handoff. The audit row, encrypted execution request and queue handoff share a transaction. A broker record proves entry into the encrypted envelope. The executor still checks dispatch authority before connector work, and its retries reuse the same envelope. Connection tests with credential input also record an unversioned handoff. Use the handoff's `parent_operation_id` to match it to the versioned release for a stored connection test.

GitHub App runtime releases record the saved installation revision and a digest of the requested repository scope. The revision and scope identify retries without using the provider-issued token. Known-installation denials are recorded when the audit store is available. An audit outage prevents release.

A webhook records the signing-key version once when a verified delivery is first committed. The audit shares the delivery transaction, including when a valid signature accompanies a rejected payload. Invalid signatures never establish a tenant audit identity.

## Retention and privileged access

Security audit rows follow the organization's event retention policy, which defaults to 30 days and supports 1 to 30 days. CSV export reads retained rows. Database owners, migration operators and administrators with equivalent access can alter or delete those rows. Organization deletion also removes its audit history.

## Check a self-hosted release

Record the release version and artifact digest. Use two synthetic organizations in an isolated installation. Give the audit reader access to the first organization, then use a separate reader for the second.

1. Create and replace a connection credential in the first organization. Test the stored connection and trigger a controlled connection failure with a synthetic secret marker. Confirm the lifecycle records, the versioned stored release, the broker handoff, and the expected failure.
2. Run `braidplane security-audit list` and `braidplane security-audit export` against each organization. Confirm that the second reader cannot read the first organization's history and that the second organization's export omits the first organization's rows.
3. Check the audit rows, CSV, HTTP failure, and available logs, traces and retained failure artifacts for the marker. Retain the artifact digest, audit event IDs, correlation IDs and the outcome of each check.
