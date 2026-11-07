# Pipeline proposal authority

Pipeline publication requires both current Braidplane permission for the
initiating member and authority for the exact Git repository operation. Neither
membership, email equality nor possession of a queued job establishes provider
authority. Provider branch rules remain authoritative.

## Explicit identity modes

Select one allowed mode and one bound principal for each attempt:

| Mode | Bound authority |
|---|---|
| Delegated user | Authenticated provider user grant, restricted by user and application permissions |
| Organization service | Explicitly enabled repository-scoped app installation or bot |
| Repository credential | Active writable plain Git data source and its configured static credential |

Never fall back to another principal, credential or mode after denial, expiry,
revocation or an unsupported provider capability. Immutable provider and
repository IDs bind provider-native authority; names, URLs, commit authors and
email remain display metadata. Email equality cannot authenticate, link or
recover an account, select credentials or grant access.

Plain Git authority binds organization, project, data source, credential,
credential revision, sanitized destination, tracked base ref and non-secret
configuration hash. It claims no GitHub/GitLab account, installation or PR/MR.
Its executor writes only a separate proposal branch using non-force pushes;
it never writes the tracked branch. Success records the exact branch and commit
with no provider review identity, URL or lifecycle state.

## Queue and revocation

The synchronous path records the initiating member, repository, selected mode
and principal. Queue payloads carry opaque references, not provider tokens,
sessions, email or general-purpose member credentials.

Before each provider mutation, reload and revalidate the member's current
permission, organization policy, repository binding, selected principal and
active credential/installation. Retries repeat those checks with the same
identity mode. Membership loss, grant revocation, repository deselection,
rebinding, credential revision changes and incompatible policy changes fail the
attempt closed. The proposal service and executor own this queued authorization
exception; HTTP endpoints still authorize through `authorization.Service.Can`.

Provider calls remain confined to the retained proposal branch and its PR/MR
lifecycle. No merge, repository administration, protection bypass, secret
management or unrelated repository operations are authorized. Unsupported
capabilities fail explicitly. Preserve partial remote state for explicit
recovery rather than silently deleting branches or proposals.

Creation proposals cannot overwrite an existing target file. A recorded success
cannot be replaced by a delayed failure. Git review/merge and reconciliation
remain explicit; publishing a proposal alone creates no canonical runtime version.

## Attribution and validation

Distinguish the initiating member from the executing provider principal in safe
audit/UI data. Record the selected mode, bound repository/branch, attempt,
operation, outcome and timestamps without exposing credentials, session values,
raw provider responses or database IDs.

The [lifecycle](../service/pipelinesvc/proposal_lifecycle.go),
[service authorization](../service/pipelinesvc/service.go) and
[executor](../service/pipelinegit/proposal_executor.go) implement this contract.
Test tenant mismatch, immutable binding changes, revocation while queued,
provider denial, retries without fallback, creation conflicts and safe results.
Use hermetic fakes for units and explicitly owned provider fixtures for
integration; tests must not require private source or credentials.
