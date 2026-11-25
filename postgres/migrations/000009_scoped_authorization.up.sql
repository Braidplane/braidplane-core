-- Existing role assignments remain explicit organization bindings. New grants
-- name immutable database IDs and never use a caller-supplied relationship.
ALTER TABLE api_keys ADD UNIQUE (organization_id, id);
ALTER TABLE runtime_variable_definitions ADD COLUMN authorization_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE;
ALTER TABLE project_repository_setups ADD COLUMN authorization_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE;
ALTER TABLE pipeline_creation_proposals ADD COLUMN authorization_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE;

CREATE TABLE authorization_scoped_grants (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id integer NOT NULL,
    member_id integer,
    api_key_id bigint,
    role_id integer NOT NULL,
    scope text NOT NULL,
    target_kind text NOT NULL,
    target_id bigint NOT NULL,
    CHECK (num_nonnulls(member_id, api_key_id) = 1),
    CHECK ((scope = 'organization' AND target_kind = 'organizations' AND target_id = organization_id)
        OR (scope = 'project' AND target_kind = 'projects')
        OR (scope = 'environment' AND target_kind = 'environments')
        OR (scope = 'resource' AND target_kind <> 'organizations')),
    FOREIGN KEY (organization_id, member_id) REFERENCES organization_members(organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, api_key_id) REFERENCES api_keys(organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, role_id) REFERENCES roles(organization_id, id) ON DELETE CASCADE,
    UNIQUE NULLS NOT DISTINCT (organization_id, member_id, api_key_id, role_id, scope, target_kind, target_id)
);

-- Each row identifies a target and only its owning scopes. Bindings that
-- reference an organization data source or environment never make it project-owned.
CREATE VIEW authorization_targets AS
SELECT 'organizations'::text AS kind, r.id::bigint AS id, r.slug::text AS slug, r.id AS organization_id,
    NULL::bigint AS project_id, NULL::bigint AS environment_id, NULL::bigint AS data_source_id, ''::text AS owner_slug,
    ''::text AS parent_slug, ARRAY[]::text[] AS ancestor_ids
FROM organizations r
UNION ALL
SELECT 'projects'::text, r.id::bigint, r.slug::text, r.organization_id, r.id, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM projects r
UNION ALL
SELECT 'environments'::text, r.id::bigint, r.slug::text, r.organization_id, NULL::bigint, r.id, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM environments r
UNION ALL
SELECT 'project_environments'::text, r.id::bigint, e.slug::text, r.organization_id, r.project_id, r.environment_id, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM project_environments r
JOIN environments e ON (e.organization_id, e.id) = (r.organization_id, r.environment_id)
WHERE r.deleted_at IS NULL AND e.deleted_at IS NULL
UNION ALL
SELECT 'members'::text, r.id::bigint, u.slug::text, r.organization_id, NULL::bigint, NULL::bigint, NULL::bigint, u.slug,
    ''::text, ARRAY[]::text[]
FROM organization_members r
JOIN users u ON u.id = r.user_id
UNION ALL
SELECT 'roles'::text, r.id::bigint, r.slug::text, r.organization_id, NULL::bigint, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM roles r
UNION ALL
SELECT 'api_keys'::text, r.id::bigint, r.slug::text, r.organization_id, NULL::bigint, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM api_keys r
UNION ALL
SELECT 'datasources'::text, r.id::bigint, r.slug::text, r.organization_id, NULL::bigint, NULL::bigint, r.id, ''::text,
    ''::text, ARRAY[]::text[]
FROM data_sources r
UNION ALL
SELECT 'events'::text, r.id::bigint, r.slug::text, r.organization_id, NULL::bigint, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM events r
UNION ALL
SELECT 'notification_connections'::text, r.id::bigint, r.slug::text, r.organization_id, NULL::bigint, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM notification_connections r
UNION ALL
SELECT 'alert_destinations'::text, r.id::bigint, r.slug::text, r.organization_id, NULL::bigint, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM alert_destinations r
UNION ALL
SELECT 'member_invitations'::text, r.id::bigint, r.slug::text, r.organization_id, NULL::bigint, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM organization_member_invitations r
UNION ALL
SELECT 'queries'::text, r.id::bigint, r.slug::text, d.organization_id, NULL::bigint, NULL::bigint, d.id, ''::text,
    ''::text, ARRAY['datasources:' || d.id]
FROM queries r
JOIN data_sources d ON d.id = r.data_source_id
UNION ALL
SELECT 'alert_policies'::text, r.id::bigint, r.slug::text, r.organization_id, r.project_id, r.environment_id, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM alert_policies r
UNION ALL
SELECT 'notifications'::text, r.id::bigint, r.slug::text, r.organization_id, r.project_id, r.environment_id, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM notification_records r
UNION ALL
SELECT 'pipelines'::text, r.id::bigint, r.slug::text, p.organization_id, r.project_id, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM pipeline_specs r
JOIN projects p ON p.id = r.project_id
UNION ALL
SELECT 'contracts'::text, r.id::bigint, r.slug::text, r.organization_id, r.project_id, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM odcs_contracts r
UNION ALL
SELECT 'project_repository_setups'::text, r.authorization_id::bigint, r.slug::text, r.organization_id, r.project_id, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM project_repository_setups r
UNION ALL
SELECT 'pipeline_creations'::text, r.authorization_id::bigint, r.slug::text, r.organization_id, r.project_id, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM pipeline_creation_proposals r
UNION ALL
SELECT 'drift_impact_reports'::text, r.id::bigint, r.slug::text, p.organization_id, p.id, NULL::bigint, NULL::bigint, ''::text,
    ''::text, ARRAY['pipelines:' || r.pipeline_spec_id]
FROM drift_impact_reports r
JOIN pipeline_specs s ON s.id = r.pipeline_spec_id
JOIN projects p ON p.id = s.project_id
UNION ALL
SELECT 'pipeline_triggers'::text, r.id::bigint, r.slug::text, pe.organization_id, pe.project_id, pe.environment_id, NULL::bigint, ''::text,
    ''::text, ARRAY['pipelines:' || r.pipeline_id]
FROM pipeline_triggers r
JOIN project_environments pe ON (pe.project_id, pe.id) = (r.project_id, r.project_environment_id)
UNION ALL
SELECT 'runs'::text, r.id::bigint, r.run_slug::text, pe.organization_id, pe.project_id, pe.environment_id, NULL::bigint, ''::text,
    ''::text, array_remove(ARRAY['pipelines:' || s.id], NULL)
FROM pipeline_runs r
JOIN project_environments pe ON (pe.project_id, pe.id) = (r.project_id, r.project_environment_id)
LEFT JOIN pipeline_versions v ON (v.project_id, v.id, v.pipeline_slug) = (r.project_id, r.pipeline_version_id, r.pipeline_slug)
LEFT JOIN pipeline_specs s ON (s.project_id, s.slug) = (v.project_id, v.pipeline_slug)
UNION ALL
SELECT 'pipeline_validation_results'::text, r.id::bigint, r.validation_slug::text, pe.organization_id, pe.project_id, pe.environment_id, NULL::bigint, ''::text,
    ''::text, array_remove(ARRAY['pipelines:' || r.pipeline_id], NULL)
FROM pipeline_validation_results r
JOIN project_environments pe ON (pe.project_id, pe.id) = (r.project_id, r.project_environment_id)
UNION ALL
SELECT 'proof_observations'::text, r.id::bigint, r.slug::text, pe.organization_id, pe.project_id, pe.environment_id, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM proof_observation_jobs r
JOIN project_environments pe ON (pe.project_id, pe.id) = (r.project_id, r.project_environment_id)
UNION ALL
SELECT 'proof_receipts'::text, r.id::bigint, r.slug::text, pe.organization_id, pe.project_id, pe.environment_id, NULL::bigint, ''::text,
    ''::text, ARRAY[]::text[]
FROM proof_receipts r
JOIN project_environments pe ON (pe.project_id, pe.id) = (r.project_id, r.project_environment_id)
UNION ALL
SELECT 'environment_secrets', r.id, r.key, r.organization_id, NULL, r.environment_id, NULL, '', '', ARRAY['environments:' || r.environment_id]
FROM environment_secrets r
UNION ALL
SELECT 'environment_runtime_variables', r.id, r.key, r.organization_id, NULL, r.environment_id, NULL, '', '', ARRAY['environments:' || r.environment_id]
FROM environment_runtime_variables r
UNION ALL
SELECT 'runtime_variable_definitions', r.authorization_id, r.key, r.organization_id, NULL, NULL, NULL, '', '', ARRAY[]::text[]
FROM runtime_variable_definitions r
UNION ALL
SELECT 'query_executions', r.id, r.slug, d.organization_id, NULL, NULL, d.id, '', q.slug,
    ARRAY['queries:' || q.id, 'datasources:' || d.id]
FROM query_executions r
JOIN queries q ON q.id = r.query_id
JOIN data_sources d ON d.id = q.data_source_id
UNION ALL
SELECT 'webhook_deliveries', r.id, r.delivery_id, r.organization_id, r.project_id, pe.environment_id, NULL, '', t.slug,
    ARRAY['pipeline_triggers:' || t.id, 'pipelines:' || t.pipeline_id]
FROM pipeline_webhook_deliveries r
JOIN pipeline_triggers t ON (t.project_id, t.project_environment_id, t.id) = (r.project_id, r.project_environment_id, r.pipeline_trigger_id)
JOIN project_environments pe ON (pe.organization_id, pe.project_id, pe.id) = (r.organization_id, r.project_id, r.project_environment_id)
UNION ALL
SELECT 'data_source_refs', r.id, r.slug, p.organization_id, p.id, NULL, NULL, '', s.slug,
    ARRAY['pipelines:' || s.id]
FROM data_source_refs r
JOIN pipeline_specs s ON s.id = r.pipeline_spec_id
JOIN projects p ON p.id = s.project_id
UNION ALL
SELECT 'data_source_bindings', b.id, r.slug, b.organization_id, b.project_id, pe.environment_id, NULL, '', s.slug,
    ARRAY['data_source_refs:' || r.id, 'pipelines:' || s.id]
FROM data_source_bindings b
JOIN data_source_refs r ON (r.pipeline_spec_id, r.id) = (b.pipeline_spec_id, b.data_source_ref_id)
JOIN pipeline_specs s ON (s.project_id, s.id) = (b.project_id, b.pipeline_spec_id)
JOIN project_environments pe ON (pe.organization_id, pe.project_id, pe.id) = (b.organization_id, b.project_id, b.project_environment_id);

CREATE FUNCTION validate_authorization_target() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM public.authorization_targets t
        WHERE (t.organization_id, t.kind, t.id) = (NEW.organization_id, NEW.target_kind, NEW.target_id)
    ) THEN
        RAISE EXCEPTION 'authorization target is outside the organization or does not exist'
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER authorization_scoped_grant_target
BEFORE INSERT OR UPDATE OF organization_id, target_kind, target_id ON authorization_scoped_grants
FOR EACH ROW EXECUTE FUNCTION validate_authorization_target();

CREATE VIEW authorization_role_bindings AS
SELECT organization_id, id AS member_id, NULL::bigint AS api_key_id, role_id,
    'organization'::text AS scope, 'organizations'::text AS target_kind, organization_id::bigint AS target_id
FROM organization_members
UNION ALL
SELECT organization_id, NULL::integer, id, role_id, 'organization', 'organizations', organization_id
FROM api_keys
UNION ALL
SELECT organization_id, member_id, api_key_id, role_id, scope, target_kind, target_id
FROM authorization_scoped_grants;

-- Use one statement snapshot for identity, ownership, bindings, and permissions.
-- A selector that is ambiguous or disagrees with its parents has no result.
CREATE FUNCTION authorization_facts(
    org_slug text, user_slug text, key_slug text, lookup_kind text, resource_slug text,
    project_slug text, environment_slug text, data_source_slug text, parent_locator text DEFAULT ''
) RETURNS jsonb LANGUAGE sql STABLE SET search_path = pg_catalog, public AS $$
WITH tenant AS (
    SELECT o.id FROM public.organizations o WHERE o.slug = org_slug
), actor AS (
    SELECT m.organization_id, m.id AS member_id, NULL::bigint AS api_key_id, m.role_id, false AS key_management
    FROM public.organization_members m JOIN tenant t ON t.id = m.organization_id
    JOIN public.users u ON u.id = m.user_id
    WHERE key_slug = '' AND u.slug = user_slug AND u.deleted_at IS NULL AND u.verified_at IS NOT NULL
    UNION ALL
    SELECT k.organization_id, NULL::integer, k.id, k.role_id, k.can_manage_api_keys
    FROM public.api_keys k JOIN tenant t ON t.id = k.organization_id
    WHERE user_slug = '' AND k.slug = key_slug
      AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > statement_timestamp())
), selector AS (
    SELECT CASE lookup_kind WHEN '' THEN 'organizations' WHEN 'deleted_roles' THEN 'roles'
        WHEN 'environment_secrets_upsert' THEN 'environment_secrets'
        WHEN 'environment_runtime_variables_upsert' THEN 'environment_runtime_variables'
        WHEN 'runtime_variable_definitions_upsert' THEN 'runtime_variable_definitions'
        WHEN 'data_source_bindings_upsert' THEN 'data_source_bindings'
        ELSE lookup_kind END AS kind
), existing AS (
    SELECT t.kind, t.id, t.organization_id, t.project_id, t.environment_id, t.owner_slug, t.ancestor_ids
    FROM public.authorization_targets t JOIN tenant o ON t.organization_id = o.id
    LEFT JOIN public.projects p ON (p.organization_id, p.id) = (o.id, t.project_id)
    LEFT JOIN public.environments e ON (e.organization_id, e.id) = (o.id, t.environment_id)
    LEFT JOIN public.data_sources d ON (d.organization_id, d.id) = (o.id, t.data_source_id)
    WHERE t.kind = (SELECT kind FROM selector)
      AND (lookup_kind <> '' OR resource_slug = '')
      AND (parent_locator = '' OR t.parent_slug = parent_locator)
      AND (t.kind NOT IN ('environment_secrets', 'environment_runtime_variables') OR environment_slug <> '')
      AND (t.kind NOT IN ('data_source_refs', 'data_source_bindings') OR (project_slug <> '' AND parent_locator <> ''))
      AND (t.kind <> 'data_source_bindings' OR environment_slug <> '')
      AND (t.kind <> 'query_executions' OR (data_source_slug <> '' AND parent_locator <> ''))
      AND (t.kind <> 'webhook_deliveries' OR (project_slug <> '' AND environment_slug <> '' AND parent_locator <> ''))
      AND t.slug = CASE
          WHEN lookup_kind = '' AND resource_slug = '' THEN org_slug
          WHEN lookup_kind = 'project_environments' AND resource_slug = '' THEN environment_slug
          ELSE resource_slug END
      AND (project_slug = '' OR p.slug = project_slug)
      AND (environment_slug = '' OR e.slug = environment_slug)
      AND (data_source_slug = '' OR d.slug = data_source_slug)
      AND (lookup_kind <> 'project_environments' OR (project_slug <> '' AND environment_slug <> ''))
      AND (t.kind <> 'roles' OR EXISTS (
          SELECT 1 FROM public.roles r WHERE r.id = t.id AND (r.deleted_at IS NOT NULL) = (lookup_kind = 'deleted_roles')
      ))
), candidate AS (
    SELECT * FROM existing
    UNION ALL
    -- A new key has no identity to grant yet. Only its verified parent can authorize creation.
    SELECT 'environments', e.id, e.organization_id, NULL, e.id, '', ARRAY[]::text[]
    FROM public.environments e JOIN tenant o ON o.id = e.organization_id
    WHERE lookup_kind IN ('environment_runtime_variables_upsert', 'environment_secrets_upsert')
      AND e.slug = environment_slug AND e.deleted_at IS NULL AND resource_slug <> ''
      AND project_slug = '' AND data_source_slug = '' AND parent_locator = '' AND NOT EXISTS (SELECT 1 FROM existing)
    UNION ALL
    SELECT 'organizations', o.id, o.id, NULL, NULL, '', ARRAY[]::text[] FROM tenant o
    WHERE lookup_kind = 'runtime_variable_definitions_upsert' AND resource_slug <> '' AND project_slug = ''
      AND environment_slug = '' AND data_source_slug = '' AND parent_locator = '' AND NOT EXISTS (SELECT 1 FROM existing)
    UNION ALL
    SELECT 'data_source_refs', r.id, o.id, p.id, e.id, '', ARRAY['pipelines:' || s.id] FROM tenant o
    JOIN public.projects p ON p.organization_id = o.id AND p.slug = project_slug
    JOIN public.environments e ON e.organization_id = o.id AND e.slug = environment_slug AND e.deleted_at IS NULL
    JOIN public.project_environments pe ON (pe.organization_id, pe.project_id, pe.environment_id) = (o.id, p.id, e.id)
      AND pe.deleted_at IS NULL
    JOIN public.pipeline_specs s ON s.project_id = p.id AND s.slug = parent_locator AND s.deleted_at IS NULL
    JOIN public.data_source_refs r ON r.pipeline_spec_id = s.id AND r.slug = resource_slug
    WHERE lookup_kind = 'data_source_bindings_upsert' AND data_source_slug = '' AND NOT EXISTS (SELECT 1 FROM existing)
    UNION ALL
    SELECT 'pipelines', s.id, o.id, s.project_id, e.id, '', ARRAY[]::text[] FROM tenant o
    JOIN public.projects p ON p.organization_id = o.id AND p.slug = project_slug
    JOIN public.environments e ON e.organization_id = o.id AND e.slug = environment_slug AND e.deleted_at IS NULL
    JOIN public.project_environments pe ON (pe.organization_id, pe.project_id, pe.environment_id) = (o.id, p.id, e.id)
      AND pe.deleted_at IS NULL
    JOIN public.pipeline_specs s ON s.project_id = p.id AND s.slug = resource_slug AND s.deleted_at IS NULL
    WHERE data_source_slug = '' AND parent_locator = '' AND (
        lookup_kind = 'deployment_targets' OR (lookup_kind = 'deployed_pipelines' AND EXISTS (
            SELECT 1 FROM public.pipeline_deployments dep
            WHERE (dep.project_id, dep.project_environment_id, dep.pipeline_slug) = (p.id, pe.id, s.slug)
        ))
    )
), target AS (
    SELECT c.* FROM candidate c WHERE (SELECT count(*) FROM candidate) = 1
), bound_permissions AS (
    SELECT b.scope,
        CASE b.scope WHEN 'resource' THEN b.target_kind || ':' ELSE '' END || b.target_id::text AS scope_id,
        jsonb_agg(DISTINCT perm.resource || ':' || perm.action ORDER BY perm.resource || ':' || perm.action) AS permissions
    FROM actor a JOIN target t ON t.organization_id = a.organization_id
    JOIN public.authorization_role_bindings b ON b.organization_id = a.organization_id
        AND (b.member_id = a.member_id OR b.api_key_id = a.api_key_id)
    JOIN public.roles r ON (r.organization_id, r.id) = (b.organization_id, b.role_id) AND r.deleted_at IS NULL
    JOIN public.role_permissions rp ON rp.role_id = r.id JOIN public.permissions perm ON perm.id = rp.permission_id
    WHERE (b.scope = 'organization' AND b.target_id = t.organization_id)
       OR (b.scope = 'project' AND b.target_id = t.project_id)
       OR (b.scope = 'environment' AND b.target_id = t.environment_id)
       OR (b.scope = 'resource' AND (
           (b.target_kind, b.target_id) = (t.kind, t.id)
           OR b.target_kind || ':' || b.target_id::text = ANY(t.ancestor_ids)
       ))
    GROUP BY b.scope, scope_id
)
SELECT jsonb_build_object(
    'valid', true, 'organization_id', t.organization_id::text,
    'principal_id', COALESCE('member:' || actor.member_id, 'api_key:' || actor.api_key_id),
    'api_key_role_slug', COALESCE(active_key_role.slug, ''),
    'can_manage_api_keys', active_key_role.id IS NOT NULL AND actor.key_management AND (t.kind <> 'api_keys' OR EXISTS (
        SELECT 1 FROM public.api_keys k WHERE (k.organization_id, k.id, k.role_id) = (t.organization_id, t.id, actor.role_id)
    )),
    'target_id', CASE WHEN t.kind = 'organizations' THEN '' ELSE t.kind || ':' || t.id::text END,
    'project_id', COALESCE(t.project_id::text, ''), 'environment_id', COALESCE(t.environment_id::text, ''),
    'owner_slug', t.owner_slug, 'ancestor_ids', t.ancestor_ids,
    'bindings', COALESCE((SELECT jsonb_agg(to_jsonb(b) ORDER BY b.scope, b.scope_id) FROM bound_permissions b), '[]'::jsonb)
)
FROM actor JOIN target t ON t.organization_id = actor.organization_id
LEFT JOIN public.roles active_key_role ON actor.api_key_id IS NOT NULL AND
    (active_key_role.organization_id, active_key_role.id) = (actor.organization_id, actor.role_id) AND active_key_role.deleted_at IS NULL;
$$;
