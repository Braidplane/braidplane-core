# Adopting scoped role grants

Core migration 9 exposes each existing organization membership and API key role as an explicit organization-wide binding in `authorization_role_bindings`. It reads the current assignment directly, so a role change takes effect on the next authorization decision. Existing wildcard permissions keep their organization scope. The migration creates zero narrower grants and leaves each existing role's permissions intact.

A user must have an active, verified account and belong to the requested organization. An API key must belong to that organization and be unrevoked and unexpired. Only active roles contribute permissions. Global roles do not supply tenant permissions. Revoking a role, role permission, grant, membership or key changes later decisions. An already admitted operation can finish under the decision made at admission. Queued completion work rechecks the initiating authority before admission.

### Permission changes on upgrade

An object-scoped request returns HTTP 403 with `AUTH_FORBIDDEN` when Core
cannot resolve the object in the requested tenant. This includes deleted
objects. Clients that previously treated HTTP 404 as the only indication
that an object is unavailable must handle this denial as well.

A legacy wildcard covers the registered Core actions at its original scope.
This is a breaking change for distributions that supplied custom resource
types or relied on an empty operation ID. Core's Go authorization input now
requires a `model.Resource`. Exact grants for private permission names no
longer authorize a Core decision. Before upgrading, move checks for private
resources into the distribution's authorization boundary. For Core resources,
update adapters to select the registered operation for the actual request.
New Core operations need a registered
principal, permission and target mapping before they can run. A global
super administrator retains the mapped global user administration operations.
Access to an organization still requires membership and an applicable tenant
binding. Role assignment and credential delegation require organization-wide
authority.

## Granting and revoking access

A role defines the permitted actions. A grant assigns that role to an organization member or API key at a particular scope. To inspect explicit grants, call `GET /organizations/{slug}/authorization/grants` with organization-wide `roles:read`. The member or key's primary role is a separate organization-wide assignment.

To grant an existing role on one project, send this JSON to `POST /organizations/{slug}/authorization/grants` using a session or personal access token with organization-wide `roles:update`.

```json
{
  "subject_kind": "member",
  "subject_slug": "alice",
  "role_slug": "pipeline-operator",
  "scope": "project",
  "target_slug": "billing"
}
```

The response includes the grant's public slug. Revoke that assignment with `DELETE /organizations/{slug}/authorization/grants/{grant_slug}`. Grant creation and revocation record the actor in the organization's audit trail. An organization administrator with grant management authority can assign an administrative role.

Use subject kind `api_key` for a machine principal. Organization scope uses the active organization. Project and environment scopes require the target slug. For individual resource scope, supply the target kind and slug and any parent qualifiers needed to identify it uniquely inside the organization. Core resolves these names to persisted identities. Recreating a target under the same slug leaves the old grant ineffective.

Grants add authority. To limit a member or key to the billing project, give its primary role only the permissions intended organization-wide, which may be empty. Review other grants that authorize the same action. Deleting a membership or key removes its explicit grants.

A project grant covers project-owned targets. A project referring to a shared organization data source conveys no access to the source or its stored queries. A project and environment pair must resolve to a persisted association. Environment-scoped execution of a deployed pipeline resolves its actual deployment. A standalone logical pipeline belongs to its project.

## Owned child resources

The resolver uses database foreign keys to find each owner. A grant on a stored query can apply to its executions, and a data source can own queries and their executions. The grant must still contain the permission for the requested action. Environment secrets and variable values belong to their environment. A webhook delivery belongs to its trigger. A read grant inherited through the current pipeline covers only deliveries recorded against the current trigger revision and pipeline. A direct trigger grant or a containing project, environment or organization grant covers the trigger’s full delivery history. A trigger or validation result with a verified pipeline ID can inherit from that pipeline. A run inherits from a pipeline only when its recorded version identifies the same logical pipeline.

Pipeline references and their environment-specific bindings can inherit from the pipeline that defines them. A binding can also inherit from its data source reference. The shared data source selected by the binding keeps its own independent authority. Stored contract revisions, compiled plans and pipeline versions use their logical resource as the target.

For an upsert, an existing key resolves to its stored row ID. A new secret or variable value requires authority at its verified environment, and a new definition requires organization authority. A new data source binding requires the referenced pipeline definition and the project/environment association. After deletion, an old individual grant cannot authorize a retry against a replacement with the same key.

## Rollout

Apply the forward-only Core catalog on a disposable clone, then verify representative member and machine decisions against the recorded bindings. Upgrade every serving process and worker before depending on narrow grants. Older binaries do not enforce the scoped authorization contract. Migration 9 records the verified targets and bindings for queued completion decisions. A live rollout requires its own authorization.

### Queued work

| Jobs | Authority when the job runs |
|---|---|
| Git proposal publish, including creation | The control worker rechecks the initiating member against the project for creation or the logical pipeline for an update. The isolated executor checks the bound claim, current provider binding and scoped permissions before the branch push and PR/MR mutation. |
| Completion dispatch, child runs and their retries | Admission and execution require both the captured closure authority and current authority. Each requires read on the upstream pipeline, update on the trigger and execute on the deployed downstream pipeline. After acceptance, a deleted trigger can only be covered by inherited authority. Core binds each newly created or edited trigger to the editor’s organization membership ID or API key ID. Replacing that membership or key under the same slug requires a new authorized edit. After upgrading, an authorized editor must save each existing completion trigger before it can authorize new closures. A pending snapshot without a recorded editor ID is refused. For an already accepted child, the worker records a failed run with `authority_revoked` and the dispatcher completes the snapshot. To authorize future runs, a current editor must save the trigger and start a new upstream run. |
| Schedule dispatch and fire, webhook delivery and trigger retries | The configured schedule or authenticated delivery starts the run. Workers use its persisted trigger, environment, deployment and attempt state. The actor who configured the trigger is audit attribution. |
| Manual pipeline run and recovery, isolated execution request | Admission authorizes the initiating operation. The worker claims the admitted attempt and checks its execution state, deployment, resource limits and operation-specific dispatch guards. |
| Git proposal refresh, Git file write/delete | Refresh reads the recorded provider proposal and updates the matching draft attempt. File jobs carry a fixed repository operation, including project-deletion cleanup. These jobs do not issue fresh member permissions. |
| Data-source schema import | The worker refreshes the organization source's cached schema using its connector. Persistence checks the current import attempt. |
| Proof webhook, publication and maintenance | Workers process the verified delivery or stored observation/publication and its provider, project and environment bindings. |
| Email, event recording and pruning, notification candidate and delivery, account deletion | These jobs fulfill a recorded account, audit, retention or delivery obligation and run under the owning service. |

GitHub data sources used to publish proposals require an explicit repository ID and a matching active GitHub App installation. Reattach sources missing that binding before publishing.

Migration 9 binds new encrypted proposal execution requests to the member, attempt, target and provider authority. Reapply the installation's executor function grants after migration. Drain old executors before starting scoped proposal processing. The new executor refuses earlier encrypted proposal kinds. Retrying the control job rechecks the original proposal attempt and submits a fresh bound request.
