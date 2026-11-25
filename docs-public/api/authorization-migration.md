# Adopting scoped role grants

Core migration 9 exposes each existing organization membership and API key role as an explicit organization-wide binding in `authorization_role_bindings`. It reads the current assignment directly, so a role change takes effect on the next authorization decision. Existing wildcard permissions keep their organization scope. The migration creates zero narrower grants and leaves each existing role's permissions intact.

A user must have an active, verified account and belong to the requested organization. An API key must belong to that organization and be unrevoked and unexpired. Only active roles contribute permissions. Global roles do not supply tenant permissions. Revoking a role, role permission, grant, membership or key changes later decisions. An already admitted operation can finish under the decision made at admission. Queued completion work rechecks the initiating authority before admission.

### Permission changes on upgrade

An object-scoped request returns HTTP 403 with `AUTH_FORBIDDEN` when Core
cannot resolve the object in the requested tenant. This includes deleted
objects. Clients that previously treated HTTP 404 as the only indication
that an object is unavailable must handle this denial as well.

A legacy wildcard covers the registered Core actions at its original scope.
Extensions that use their own resource types need exact resource/action grants.
Before upgrading, inventory those extension actions and assign the intended
permissions to their existing roles. New Core operations need a registered
principal, permission and target mapping before they can run. A global
super administrator retains the mapped global user administration operations.
Access to an organization still requires membership and an applicable tenant
binding. Role assignment and credential delegation require organization-wide
authority.

## Granting and revoking access

Use an authorized database administration session. Inspect the organization, subject, role, target and existing broad bindings first. The following example uses psql variables and grants an existing role to a member for one project. The insert verifies tenant ownership of each ID. Check the returned grant ID before committing.

```sql
BEGIN;
SELECT b.scope, b.target_kind, b.target_id, r.slug AS role
FROM authorization_role_bindings b
JOIN roles r ON (r.organization_id, r.id) = (b.organization_id, b.role_id)
JOIN organizations o ON o.id = b.organization_id
JOIN organization_members m ON (m.organization_id, m.id) = (o.id, b.member_id)
JOIN users u ON u.id = m.user_id
WHERE o.slug = :'organization' AND u.slug = :'member';

INSERT INTO authorization_scoped_grants
    (organization_id, member_id, role_id, scope, target_kind, target_id)
SELECT o.id, m.id, r.id, 'project', 'projects', p.id
FROM organizations o
JOIN organization_members m ON m.organization_id = o.id
JOIN users u ON u.id = m.user_id
JOIN roles r ON r.organization_id = o.id AND r.deleted_at IS NULL
JOIN projects p ON p.organization_id = o.id
WHERE o.slug = :'organization' AND u.slug = :'member'
  AND r.slug = :'role' AND p.slug = :'project'
RETURNING id;
COMMIT;
```

Each grant names exactly one `member_id` or `api_key_id`. For a machine principal, resolve the API key by organization ID and key slug. For an environment binding, use `scope = 'environment'`, `target_kind = 'environments'` and the persisted environment ID. An additional organization binding uses `'organization'`, `'organizations'` and the organization ID. An individual resource binding uses `'resource'` and the exact kind and ID from `authorization_targets`. Qualify child slugs with their persisted parent and tenant when reading this view. A resource kind and numeric ID together identify a target. Pipeline versions share their logical pipeline's ID. Recreating a resource with the same slug assigns a new ID.

Bindings add authority. To restrict an existing member or key to one project, replace the original organization-wide role with a role containing only the intended broad permissions, which may be empty. Creating the project grant alone preserves the whole-organization role. Review any other bindings that can authorize the same action. Deleting a membership or key cascades to its scoped grants. Purging a target makes its old numeric grant ineffective, and an administrator can remove that grant by ID.

Revoke a scoped grant within the organization and verify the row returned.

```sql
DELETE FROM authorization_scoped_grants g USING organizations o
WHERE g.organization_id = o.id AND o.slug = :'organization' AND g.id = :'grant_id'::bigint
RETURNING g.id;
```

A project grant covers project-owned targets. A project referring to a shared organization data source conveys no access to the source or its stored queries. A project and environment pair must resolve to a persisted association. Environment-scoped execution of a deployed pipeline resolves its actual deployment. A standalone logical pipeline belongs to its project.

## Owned child resources

The resolver uses database foreign keys to find each owner. A grant on a stored query can apply to its executions, and a data source can own queries and their executions. The grant must still contain the permission for the requested action. Environment secrets and variable values belong to their environment. A webhook delivery belongs to its trigger. A read grant inherited through the current pipeline covers only deliveries recorded against the current trigger revision and pipeline. A direct trigger grant or a containing project, environment or organization grant covers the trigger’s full delivery history. A trigger or validation result with a verified pipeline ID can inherit from that pipeline. A run inherits from a pipeline only when its recorded version identifies the same logical pipeline.

Pipeline references and their environment-specific bindings can inherit from the pipeline that defines them. A binding can also inherit from its data source reference. The shared data source selected by the binding keeps its own independent authority. Stored contract revisions, compiled plans and pipeline versions use their logical resource as the target.

For an upsert, an existing key resolves to its stored row ID. A new secret or variable value requires authority at its verified environment, and a new definition requires organization authority. A new data source binding requires the referenced pipeline definition and the project/environment association. After deletion, an old individual grant cannot authorize a retry against a replacement with the same key.

## Rollout

Apply the forward-only Core catalog on a disposable clone, then verify representative member and machine decisions against the recorded bindings. Upgrade every serving process and worker before depending on narrow grants. Older binaries do not enforce the scoped authorization contract. Migration 10 records the verified targets and bindings for queued completion decisions. A live rollout requires its own authorization.

### Queued work

| Jobs | Authority when the job runs |
|---|---|
| Git proposal publish, including creation | The control worker rechecks the initiating member against the project for creation or the logical pipeline for an update. The isolated executor checks the bound claim, current provider binding and scoped permissions before the branch push and PR/MR mutation. |
| Completion dispatch, child runs and their retries | Admission and execution require both the captured closure authority and current authority. Each requires read on the upstream pipeline, update on the trigger and execute on the deployed downstream pipeline. After acceptance, a deleted trigger can only be covered by inherited authority. Migration 12 binds each newly created or edited trigger to the editor’s organization membership ID or API key ID. Replacing that membership or key under the same slug requires a new authorized edit. After upgrading, an authorized editor must save each existing completion trigger before it can authorize new closures. A pending snapshot without a recorded editor ID is refused. For an already accepted child, the worker records a failed run with `authority_revoked` and the dispatcher completes the snapshot. To authorize future runs, a current editor must save the trigger and start a new upstream run. |
| Schedule dispatch and fire, webhook delivery and trigger retries | The configured schedule or authenticated delivery starts the run. Workers use its persisted trigger, environment, deployment and attempt state. The actor who configured the trigger is audit attribution. |
| Manual pipeline run and recovery, isolated execution request | Admission authorizes the initiating operation. The worker claims the admitted attempt and checks its execution state, deployment, resource limits and operation-specific dispatch guards. |
| Git proposal refresh, Git file write/delete | Refresh reads the recorded provider proposal and updates the matching draft attempt. File jobs carry a fixed repository operation, including project-deletion cleanup. These jobs do not issue fresh member permissions. |
| Data-source schema import | The worker refreshes the organization source's cached schema using its connector. Persistence checks the current import attempt. |
| Proof webhook, publication and maintenance | Workers process the verified delivery or stored observation/publication and its provider, project and environment bindings. |
| Email, event recording and pruning, notification candidate and delivery, account deletion | These jobs fulfill a recorded account, audit, retention or delivery obligation and run under the owning service. |

Migration 11 binds new encrypted proposal execution requests to the member, attempt, target and provider authority. Reapply the installation's executor function grants after migration. Drain old executors before starting scoped proposal processing. The new executor refuses earlier encrypted proposal kinds. Retrying the control job rechecks the original proposal attempt and submits a fresh bound request.
