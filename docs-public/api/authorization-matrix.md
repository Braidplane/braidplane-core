# Core authorization matrix

The [REST operation matrix](https://github.com/Braidplane/braidplane-core/blob/main/service/authorization/operations.csv) lists the method, route, accepted principals, OPA resource and action, and target selectors for each public OpenAPI operation. The [browser action matrix](https://github.com/Braidplane/braidplane-core/blob/main/frontend/authorization_matrix.csv) lists Community UI routes, browser identity requirements, backend operation IDs and local actions. Both files ship with the corresponding source revision. Core rejects an unrecognized REST operation before its handler runs. Community frontend registration requires a classification for every new route.

## Reading a decision

| Scope | Enforcement |
| --- | --- |
| Public authentication | Registration, login, MFA, verification and password recovery apply their own challenge, state or token checks. |
| Before tenant selection | A verified user can create an organization or list their own memberships. |
| Self service | Profile access uses the authenticated user's identity. Password changes and account deletion require a session. Changing MFA settings also requires authentication within the past five minutes. Personal token management requires a session or a management-enabled PAT, and permanent deletion requires a session. Only a session may set a personal token’s management capability. Invitation acceptance and decline verify the recipient in the membership service. |
| Tenant collection | A collection request authorizes against its parent. An individual grant allows direct access to that object. Listing a whole collection requires the corresponding parent grant. |
| Tenant object | Core resolves identifiers against persisted tenant, project, environment and parent relationships. Resource grants follow the stable object identity. Pipeline versions inherit from the logical pipeline. |
| Permission discovery | The service evaluates the caller's own tenant permissions. Each operation still authorizes its own execution. |
| Credential and role administration | The shared authorization service checks allowed credential sources and OPA checks assignment authority. A machine key performing key management needs the key-management capability. PATs cannot administer machine keys. |
| Global administration | Core's global user administration policy requires a super administrator. Tenant membership and grants apply only inside their organization. |
| Signed pipeline ingress | The receiver verifies the signature. Dispatch uses the configured trigger, deployment and recorded delivery state. |

The permissions column includes checks that depend on the request. For example, listing archived roles requires restore permission, optional readiness data requires reads of the relevant metadata, and enabling a completion execution grant checks the upstream pipeline, trigger and downstream deployment. A failed optional check can omit a section. Operations that need a user principal reject machine keys even when the key has the listed permission.

To inspect a decision, call `GET /organizations/{slug}/authorization/permissions` with the target and any required parent selectors. Omitting the target reports the permissions held organization-wide. An explicit target narrows the result to operations mapped to that resource or collection. Core returns permission pairs that OPA grants on the selected target and that at least one mapped operation accepts from the authenticated credential type. For example, a PAT receives no API key management permissions. A machine key also needs the stored management capability and can manage only keys assigned to its own active role. The role recorded by authentication must still match the database. When creating a child key, the handler enforces that same role and rejects attempts to set a management capability. A machine caller also cannot change this setting on an existing key. A permission pair can apply to more than one operation. Each operation enforces its own source restrictions, additional target checks, request validation, write admission and provider state.

Trusted adapters start an action with `authorization.WithDecisionScope`, pass that context to the policy check and the protected service operation, and select a registered operation ID and use the source established by authentication when calling `authorization.Service.Can`. The selectors column lists the target kinds accepted for each permission. The `binding.` prefix requires a project-owned data source reference or binding. A named suffix such as `@new_parent` applies only to that secondary check. The service verifies the operation, principal, permission and target kind before applying the current bindings. New Core operations require a matrix entry. Distribution code can use `RequestAuthorization.Can` for the mapped global user administration checks. Core authorization accepts registered operations with a `model.Resource` permission family. A distribution owns the authorization checks for its private resources. Queued proposal publication uses registered worker operations and rechecks the initiating member. The browser obtains permissions from this API and sends data reads and mutations through the corresponding protected REST operations.

Project pipeline definitions, drafts and versions use the logical pipeline target. Deployment and execution select the project and environment explicitly. Reads of a saved operation use its data source and query identity. Managing a reference or environment binding uses the owning pipeline or binding. Attaching a shared data source also requires permission to read the selected source. A persistent reference grants no access to the shared source's credentials or queries.

Listing a project's drift and impact reports requires `pipelines:read` at the project. When the caller supplies `pipeline_slug`, Core resolves the pipeline in that project and uses its `pipelines:read` decision. The same slug restricts the repository query. An unresolved pipeline returns HTTP 403. Both forms require organization-wide `datasources:read`.

## Browser pipeline actions

The browser uses a session and the selected workspace. The sync page checks `pipelines:update` on the project even when opening the form. The recency page checks `pipelines:read` against the selected project, environment and deployment. Deployment review looks up the logical pipeline and the selected deployment target separately. The saved-query setup and proposal progress pages look up the selected project’s effective permissions.

The corresponding POST routes can apply a sync, change recency, compile or deploy a version, or create or retry a proposal. The REST operation matrix gives the permission and selector for each submission. The GET routes load evidence and forms.

## Browser-local actions

The browser matrix marks local actions with `local:`. These actions use the browser boundary below. Any backend calls listed on the same row also use their REST principal, OPA and target checks.

| Browser action | Trusted scope |
| --- | --- |
| Root redirect and public authentication forms | Public presentation. Verification, recovery and sign-in submissions use the backend's token or challenge checks. |
| Logout | The current browser session cookie and applicable CSRF checks. |
| Workspace setup and organization switching | The current session and membership returned by `listOrganizations`. Setup can select the organization returned by an authorized `createOrganization`. Selection sets browser navigation state. Each tenant operation still resolves its own authority. |
| Builder import, export and mapping | The browser session and applicable CSRF checks for submitted content. When mapping uses a saved shape, `inspectODCSRevision` checks `projects:read` on its contract in the selected organization and project. Target validation uses `validatePipeline`. |
| Data source form and URI parsing | The browser session and applicable CSRF checks for form data. Saved sources and connection tests use their separately mapped REST operations. |

## Routes outside the OpenAPI document

| Route | Authority |
| --- | --- |
| GET /openapi.json, /healthz, /readyz | Public document, process health and readiness. |
| GET configured metrics path | Process metrics when the operator enables collection and exposes the listener. |
| POST /api/webhooks/github/pull-requests | GitHub delivery signature and event validation, with installation or proof-specific processing. |
| POST /api/webhooks/gitlab/merge-requests/:routeKey | Server-resolved binding and verified GitLab signing token. |

See [grant adoption and revocation](authorization-migration.md) for migration behavior, scope identifiers and administration.
