# Alpha API contract

This contract applies to Community Alpha releases while the version is below
`1.0.0`. Use the documentation and OpenAPI document from your installed release.
The schema's current version is `0.0.1`. Use the release notes for application
and CLI version information. RC prereleases collect changes since the latest
stable release; stable releases require explicit promotion of a completed RC.

## Alpha changes

Braidplane aims to follow semantic versioning on a best-effort basis before
`1.0.0`: patch releases contain compatible bug and performance fixes; new
features and breaking changes require an explicitly selected minor release.
Breaking changes are expected during Alpha and are marked explicitly in the
release notes.

Alpha releases may change APIs, generated clients and CLI behavior without a
deprecation period. Third-party clients currently have no compatibility guarantee.

The unversioned `/api` mount does not promise a stable v1 interface. On a
self-hosted application, append `/api` to the application origin. A distribution
may expose the API at the root of a separate API host. Use its documented base
URL and the OpenAPI document served by that installation at `/openapi.json`.

## Application, CLI and generated clients

Use the application and CLI from the same release. Cross-release combinations
are unsupported unless that release's validation and manifest explicitly list
them. The CLI projection contains only its reviewed operation allowlist; it is
not the complete API. Generated clients describe wire types and security
requirements; they do not grant permissions, install credentials or guarantee
retry safety. Regenerate third-party clients from the exact target release's
OpenAPI document and test their integration before upgrading.

Database migrations belong to the application release, run forward in order,
and remain immutable once applied. Back up the database and encryption keys
before upgrades. An application downgrade is not a database rollback: use a
tested backup restore and its matching application if recovery requires it.
Never assume an older CLI, application or migration catalog works with a newer
database.

## Authentication and public entry points

Protected operations inherit OpenAPI alternatives for `Authorization: Bearer`
(personal access token or organization API key) and the `__Host-session_id`
browser cookie. Local HTTP uses `session_id`. Organization permissions and
principal restrictions still apply. Organization API keys cannot act as users,
and some operations require a user session. Unsafe cookie-authenticated requests require `X-CSRF-Token`, obtained
from an authenticated safe request. Bearer authentication alone does not require
CSRF. An invalid credential may fail even on an otherwise public route.

The explicit anonymous exceptions are user creation, password login, MFA login
verification, authentication-method discovery, Google registration/sign-in and
its callback, email verification/resend, and password reset/request. Public
means no existing login is needed; account-creation configuration, passwords,
MFA challenges, verification tokens and OAuth state are still validated.

Account verification and password reset emails contain a link and a separate
one-time code. Open the link and paste the code into the form. API clients send
the code in the JSON `token` field. If you received an older email with the code
in its link, request a new email from the sign-in page.

Provider installation callbacks remain protected: a valid state resolves the
organization before user authorization. GitHub App installation and connection management, password changes, account deletion and permanent
deletion of revoked credentials require a user session. Changing MFA settings
also requires recent password or MFA authentication. A missing or invalid callback state may redirect or fail before the
login check. These are not anonymous sign-in callbacks.

Pipeline webhook delivery uses `Braidplane-Webhook-Signature`, with key version,
timestamp and delivery ID headers, instead of bearer/session authentication.
The signature binds the key version, timestamp, delivery ID and exact raw body.
The server also checks freshness.
`GET /healthz`, `GET /readyz` and `GET /openapi.json` are public routes outside the
schema's `/api` resource namespace. Readiness can return `503` without a login.

## Organization permissions and resource scope

Core checks the caller's permission for the operation in the target organization.
An organization API key uses its own role and is scoped to its organization.
A browser session or personal access token uses the authenticated user's permissions.

### Pipeline validation and history

The operation names below are OpenAPI operation IDs. The authorization checks
use the target organization's `resource:action` permissions. Every permission
listed for an operation is required.

| Operation | Required permissions | Caller and target |
|---|---|---|
| `validatePipeline` | `pipelines:read` and `datasources:read`. Add `projects:read` when the draft refers to an ODCS contract. | Browser session, personal access token, or organization API key. The route identifies the project and environment to validate against. |
| `listPipelineValidationResults` | `pipelines:read` and `datasources:read` | Browser session, personal access token, or organization API key. Results belong to the route's organization, project, and environment. |
| `getPipelineValidationResult` | `pipelines:read` and `datasources:read` | Same callers and scope as the list. The validation slug identifies a record within that scope. |
| `revalidatePipeline` | `pipelines:update` and `datasources:read`. Add `projects:read` when the canonical pipeline refers to an ODCS contract. | Browser session or personal access token. The route identifies the canonical pipeline and its project runtime target. Core stores the diagnostic result. |

When draft validation returns an invalid result for a named pipeline, Core
attempts to record it only if the caller also has `pipelines:update`. The caller
still receives the validation result when history recording is unavailable.

### Webhook replay and secret rotation

`replayPipelineWebhookDelivery` and `rotatePipelineWebhookSecret` each require
`pipeline_triggers:update` and `pipelines:execute`. Core resolves the trigger in
the route's organization, project, and environment. Replay applies to a
retained delivery for that trigger and still checks replay eligibility.

## Execution, retries and replay

Query execution, pipeline execution, run recovery, webhook replay, connection
tests and notification tests can cause external effects. Treat execution-triggering
requests as non-idempotent unless that operation explicitly documents a deduplication
contract. Sending an arbitrary `Idempotency-Key` does not create one. A timeout,
connection loss or `5xx` does not prove that execution was rejected or rolled back.
Keep the returned run identity, inspect its state and destination evidence, and
reconcile uncertain outcomes before submitting another execution.

An accepted pipeline execution is asynchronous. Poll its run resource until
`succeeded`, `failed` or `canceled`; acceptance is not completion. Recovery and
operator replay are explicit operations that can perform effects again. Pinned
inputs and replay identities do not by themselves guarantee exactly-once writes
at an external sink. Cancellation or failure does not undo completed writes.

For generic pipeline webhooks, reuse the receiver and delivery ID with the same
raw payload when retrying a delivery, and sign it with a fresh valid timestamp.
Within retained delivery records, identical accepted deliveries are deduplicated;
conflicting payloads or rejected records fail. This delivery contract does not
promise sink-side exactly-once execution or indefinite deduplication after
retention. Operator replay creates another attempt subject to retained evidence
and replay eligibility. Crash-after-side-effect sink idempotency requires
separate enforcement; this contract makes no broader sink guarantee.
