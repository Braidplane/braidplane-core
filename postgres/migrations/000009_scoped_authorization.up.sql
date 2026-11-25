-- Existing role assignments remain explicit organization bindings. New grants
-- name immutable database IDs and never use a caller-supplied relationship.
ALTER TABLE api_keys ADD UNIQUE (organization_id, id);
ALTER TABLE runtime_variable_definitions ADD COLUMN authorization_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE;
ALTER TABLE project_repository_setups ADD COLUMN authorization_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE;
ALTER TABLE pipeline_creation_proposals ADD COLUMN authorization_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE;

CREATE TABLE authorization_scoped_grants (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug text NOT NULL UNIQUE DEFAULT gen_random_uuid()::text,
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
SELECT 'drift_impact_reports', r.id, r.slug, d.organization_id, NULL, NULL, d.id, '', '',
    ARRAY['datasources:' || d.id]
FROM drift_impact_reports r
JOIN data_sources d ON d.id = r.data_source_id
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
    -- A later trigger revision may point to a different logical pipeline.
    array_remove(ARRAY['pipeline_triggers:' || t.id,
        CASE WHEN (r.trigger_revision, r.pipeline_slug) = (t.revision, t.pipeline_slug) THEN 'pipelines:' || t.pipeline_id END], NULL)
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

CREATE VIEW authorization_target_locators AS
SELECT t.organization_id, t.kind, t.id, t.slug, t.parent_slug,
    COALESCE(p.slug, '')::text AS project_slug,
    COALESCE(e.slug, '')::text AS environment_slug,
    COALESCE(d.slug, '')::text AS data_source_slug
FROM authorization_targets t
LEFT JOIN projects p ON (p.organization_id, p.id) = (t.organization_id, t.project_id)
LEFT JOIN environments e ON (e.organization_id, e.id) = (t.organization_id, t.environment_id)
LEFT JOIN data_sources d ON (d.organization_id, d.id) = (t.organization_id, t.data_source_id);

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
) RETURNS jsonb LANGUAGE sql VOLATILE SET search_path = pg_catalog, public AS $$
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
      AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > clock_timestamp())
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
      AND CASE WHEN t.kind = 'drift_impact_reports' AND t.data_source_id IS NOT NULL THEN
          -- A binding makes the report visible on this route. The source remains its only owner.
          EXISTS (SELECT 1 FROM public.data_source_bindings b
              JOIN public.project_environments pe ON (pe.organization_id, pe.project_id, pe.id) =
                  (b.organization_id, b.project_id, b.project_environment_id)
              JOIN public.environments be ON (be.organization_id, be.id) = (pe.organization_id, pe.environment_id)
              JOIN public.projects bp ON (bp.organization_id, bp.id) = (pe.organization_id, pe.project_id)
              WHERE b.organization_id = o.id AND b.data_source_id = t.data_source_id AND bp.slug = project_slug
                AND pe.deleted_at IS NULL AND be.deleted_at IS NULL)
          ELSE project_slug = '' OR p.slug = project_slug END
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

-- A trigger revision can grant execution long after its editor has left.
-- Earlier revisions have no reliable principal ID and require an authorized edit.
ALTER TABLE pipeline_triggers ADD COLUMN completion_editor_id text;

CREATE FUNCTION pin_completion_trigger_editor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.completion_editor_id := NULL;
    IF NEW.trigger_type <> 'completion' THEN RETURN NEW; END IF;

    IF NEW.updated_by_actor_type = 'user' THEN
        SELECT 'member:' || m.id INTO NEW.completion_editor_id
        FROM projects p JOIN organization_members m ON m.organization_id = p.organization_id
        JOIN users u ON u.id = m.user_id
        WHERE p.id = NEW.project_id AND u.slug = NEW.updated_by_actor_slug
            AND u.deleted_at IS NULL AND u.verified_at IS NOT NULL
        FOR SHARE OF m, u;
    ELSIF NEW.updated_by_actor_type = 'api_key' THEN
        SELECT 'api_key:' || k.id INTO NEW.completion_editor_id
        FROM projects p JOIN api_keys k ON k.organization_id = p.organization_id
        WHERE p.id = NEW.project_id AND k.slug = NEW.updated_by_actor_slug
            AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > clock_timestamp())
        FOR SHARE OF k;
    END IF;
    RETURN NEW;
END;
$$;

-- The authenticated create and edit queries always write the editor columns.
CREATE TRIGGER pipeline_triggers_pin_completion_editor
    BEFORE INSERT OR UPDATE OF updated_by_actor_type, updated_by_actor_slug ON pipeline_triggers
    FOR EACH ROW EXECUTE FUNCTION pin_completion_trigger_editor();

-- Closure and execution use the same resolved scopes as request authorization.
CREATE FUNCTION pipeline_completion_scoped_authority(
    org_id bigint, env_id bigint, configuration jsonb, accepted boolean
) RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE
    editor_id text := configuration->>'completion_editor_id';
    actor_type text := configuration->>'updated_by_actor_type';
    actor_slug text := configuration->>'updated_by_actor_slug';
    member_key integer;
    machine_key bigint;
    assigned_role integer;
    user_slug text := '';
    key_slug text := '';
    org_name text;
    project_name text;
    environment_name text;
    trigger_facts jsonb;
    upstream_facts jsonb;
    downstream_facts jsonb;
BEGIN
    SELECT o.slug, p.slug, e.slug INTO org_name, project_name, environment_name
    FROM project_environments pe
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
    WHERE o.id = org_id AND pe.id = env_id;

    IF actor_type = 'user' THEN
        user_slug := actor_slug;
        SELECT m.id, m.role_id INTO member_key, assigned_role
        FROM organization_members m JOIN users u ON u.id = m.user_id
        WHERE m.organization_id = org_id AND u.slug = actor_slug AND editor_id = 'member:' || m.id
        FOR SHARE OF m, u;
    ELSIF actor_type = 'api_key' THEN
        key_slug := actor_slug;
        SELECT k.id, k.role_id INTO machine_key, assigned_role
        FROM api_keys k WHERE k.organization_id = org_id AND k.slug = actor_slug AND editor_id = 'api_key:' || k.id
        FOR SHARE;
    END IF;
    IF member_key IS NULL AND machine_key IS NULL THEN
        RETURN jsonb_build_object('valid', false);
    END IF;

    -- Hold every currently contributing binding and role until the outer admission commits.
    PERFORM 1 FROM authorization_scoped_grants g WHERE g.organization_id = org_id
        AND (g.member_id = member_key OR g.api_key_id = machine_key) ORDER BY g.id FOR SHARE;
    PERFORM 1 FROM roles r WHERE r.organization_id = org_id AND
        (r.id = assigned_role OR r.id IN (SELECT g.role_id FROM authorization_scoped_grants g
            WHERE g.organization_id = org_id AND (g.member_id = member_key OR g.api_key_id = machine_key)))
        ORDER BY r.id FOR SHARE;
    PERFORM 1 FROM role_permissions rp JOIN permissions pm ON pm.id = rp.permission_id WHERE
        rp.role_id = assigned_role OR rp.role_id IN (SELECT g.role_id FROM authorization_scoped_grants g
            WHERE g.organization_id = org_id AND (g.member_id = member_key OR g.api_key_id = machine_key))
        ORDER BY rp.id FOR SHARE OF rp, pm;

    upstream_facts := authorization_facts(org_name, user_slug, key_slug, 'pipelines',
        configuration#>>'{completion_config,upstream_pipeline_slug}', project_name, '', '');
    downstream_facts := authorization_facts(org_name, user_slug, key_slug, 'deployed_pipelines',
        configuration->>'pipeline_slug', project_name, environment_name, '');
    IF EXISTS (SELECT 1 FROM pipeline_triggers t WHERE t.project_environment_id = env_id
        AND t.id = (configuration->>'id')::bigint AND t.slug = configuration->>'slug') THEN
        trigger_facts := authorization_facts(org_name, user_slug, key_slug, 'pipeline_triggers',
            configuration->>'slug', project_name, environment_name, '');
    ELSIF accepted THEN
        -- An accepted child survives trigger deletion if its actor still has inherited authority.
        trigger_facts := authorization_facts(org_name, user_slug, key_slug, 'project_environments',
            '', project_name, environment_name, '') - 'target_id';
    END IF;
    RETURN jsonb_build_object(
        'valid', COALESCE((upstream_facts->>'valid')::boolean, false),
        'principal_id', editor_id,
        'scoped', jsonb_build_object('upstream', upstream_facts, 'trigger', trigger_facts, 'downstream', downstream_facts)
    );
END;
$$;

CREATE OR REPLACE FUNCTION public.snapshot_pipeline_completion_targets() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE source_run pipeline_runs%ROWTYPE;
        target_trigger pipeline_triggers%ROWTYPE;
        target_version pipeline_versions%ROWTYPE;
        deployment pipeline_deployments%ROWTYPE;
        resolution pipeline_deployment_resolutions%ROWTYPE;
        protected_target BOOLEAN;
        refusal TEXT;
        depth_value INTEGER;
        authority_value JSONB;
        ancestors TEXT[];
BEGIN
    SELECT run.* INTO source_run FROM pipeline_runs run
    WHERE run.project_environment_id = NEW.project_environment_id
        AND run.run_slug = NEW.final_attempt_run_slug AND run.status = NEW.status;
    IF NOT FOUND THEN RETURN NEW; END IF;
    -- Run inserts already hold foreign-key key-share locks on this row.
    SELECT e.protected INTO protected_target FROM project_environments pe
    JOIN environments e ON e.id = pe.environment_id AND e.deleted_at IS NULL
    WHERE pe.id = NEW.project_environment_id AND pe.deleted_at IS NULL
    FOR NO KEY UPDATE OF pe;
    IF NOT FOUND THEN RETURN NEW; END IF;
    SELECT parent.ancestor_pipeline_slugs || source_run.pipeline_slug::text INTO ancestors
    FROM pipeline_completion_snapshots parent
    WHERE parent.organization_id = NEW.organization_id AND parent.project_environment_id = NEW.project_environment_id
        AND parent.child_run_slug = NEW.root_run_slug;
    IF ancestors IS NULL THEN ancestors := ARRAY[source_run.pipeline_slug::text]; END IF;
    depth_value := cardinality(ancestors);
    FOR target_trigger IN SELECT pt.* FROM pipeline_triggers pt
        WHERE pt.project_environment_id = NEW.project_environment_id AND pt.trigger_type = 'completion'
            AND pt.completion_config->>'upstream_pipeline_slug' = source_run.pipeline_slug
            AND pt.completion_config->>'condition' = CASE NEW.status WHEN 'succeeded' THEN 'success' ELSE 'final_failure' END
        ORDER BY pt.id
    LOOP
        refusal := NULL;
        deployment := NULL;
        target_version := NULL;
        resolution := NULL;
        authority_value := NULL;
        IF NOT target_trigger.enabled THEN refusal := 'trigger_disabled';
        ELSIF NOT (target_trigger.completion_config->>'execution_grant_enabled')::boolean THEN refusal := 'grant_revoked';
        ELSIF protected_target THEN refusal := 'protected_environment';
        ELSIF depth_value > 8 THEN refusal := 'causal_depth_exceeded';
        ELSIF target_trigger.pipeline_slug = ANY(ancestors) THEN refusal := 'ancestor_pipeline_repeated';
        ELSE
            SELECT pd.* INTO deployment FROM pipeline_deployments pd
            WHERE pd.project_environment_id = NEW.project_environment_id AND pd.pipeline_slug = target_trigger.pipeline_slug;
            IF NOT FOUND THEN refusal := 'deployment_missing';
            ELSE
                SELECT pv.* INTO target_version FROM pipeline_versions pv
                WHERE pv.project_id = target_trigger.project_id AND pv.pipeline_slug = target_trigger.pipeline_slug
                    AND pv.id = CASE target_trigger.version_policy WHEN 'pinned' THEN target_trigger.pinned_pipeline_version_id ELSE deployment.current_version_id END;
                IF NOT FOUND THEN refusal := 'selected_version_missing';
                ELSE
                    -- A pinned version requires its own persisted materialization;
                    -- it cannot borrow a different version's current resolution.
                    SELECT dr.* INTO resolution FROM pipeline_deployment_resolutions dr
                    JOIN pipeline_compiled_plans cp ON cp.project_id = dr.project_id
                        AND cp.pipeline_slug = dr.pipeline_slug AND cp.digest = dr.compiled_digest
                    WHERE dr.project_environment_id = NEW.project_environment_id AND dr.pipeline_slug = target_trigger.pipeline_slug
                        AND cp.authored_spec_hash = target_version.spec_hash
                        AND (target_trigger.version_policy = 'pinned' OR dr.digest = deployment.current_resolution_digest)
                    ORDER BY dr.created_at DESC, dr.id DESC LIMIT 1;
                    IF NOT FOUND THEN refusal := 'execution_context_missing'; END IF;
                END IF;
            END IF;
        END IF;
        IF refusal IS NULL THEN
            authority_value := pipeline_completion_scoped_authority(NEW.organization_id,
                NEW.project_environment_id, to_jsonb(target_trigger), false);
            IF NOT COALESCE((authority_value->>'valid')::boolean, false) THEN refusal := 'authority_revoked'; END IF;
        END IF;
        INSERT INTO pipeline_completion_snapshots (
            organization_id, project_environment_id, closure_id, trigger_id, trigger_revision, trigger_slug,
            root_run_slug, final_attempt_run_slug, downstream_pipeline_slug,
            selected_version_id, selected_version_hash, selected_deployment_revision, selected_deployment_id, ancestor_pipeline_slugs,
            selected_resolution_digest, selected_materialization, trigger_configuration, closure_authority,
            causal_depth, state, decision_reason, completed_at
        ) VALUES (
            NEW.organization_id, NEW.project_environment_id, NEW.id, target_trigger.id, target_trigger.revision, target_trigger.slug,
            NEW.root_run_slug, NEW.final_attempt_run_slug, target_trigger.pipeline_slug,
            target_version.id, target_version.spec_hash, deployment.revision, deployment.id, ancestors,
            resolution.digest, resolution.materialization, to_jsonb(target_trigger), authority_value,
            depth_value, CASE WHEN refusal IS NULL THEN 'pending' ELSE 'refused' END, refusal,
            CASE WHEN refusal IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END
        ) ON CONFLICT (organization_id, closure_id, trigger_id, trigger_revision) DO NOTHING;
    END LOOP;
    RETURN NEW;
END;
$$;

DROP FUNCTION pipeline_completion_authority_snapshot(bigint, text, text);

-- A GitHub App proposal requires an active installation and the exact configured repository.
CREATE VIEW active_pipeline_proposal_authorities AS
SELECT o.slug AS org_slug, p.slug AS project_slug, pa.project_id, pa.organization_id,
    ds.updated_at AS source_updated_at,
    pa.id,
    pa.publication_mode,
    pa.provider_host,
    pa.provider_account_id,
    pa.provider_repository_id,
    pa.provider_credential_id,
    pa.executing_principal_id,
    pa.executing_principal_display,
    pa.authority_source,
    ds.config AS git_configuration,
    COALESCE(p.pipeline_backend_repo, '')::text AS git_repository,
    COALESCE(p.pipeline_backend_tracked_branch, '')::text AS git_base_branch,
    COALESCE(p.pipeline_backend_pipeline_dir, '')::text AS git_pipeline_directory,
    ds.slug AS git_data_source_slug,
    c.slug AS git_credential_slug,
    c.updated_at AS git_credential_updated_at
FROM pipeline_proposal_authorities pa
JOIN organizations o ON pa.organization_id = o.id
JOIN projects p ON pa.project_id = p.id
    AND p.organization_id = o.id
JOIN data_sources ds ON pa.data_source_id = ds.id
    AND ds.organization_id = o.id
    AND p.pipeline_backend_data_source_id = ds.id
JOIN credentials c ON pa.credential_id = c.id
    AND c.organization_id = o.id
    AND ds.credential_id = c.id
LEFT JOIN credential_oauth_grants cog ON c.id = cog.credential_id
WHERE ds.deleted_at IS NULL
    AND ds.is_active = TRUE
    AND ds.read_only = FALSE
    AND c.status = 'active'
    AND c.revoked_at IS NULL
    AND pa.revoked_at IS NULL
    AND (pa.publication_mode <> 'repository_credential' OR (
        ds.type = 'git' AND c.kind = 'static_secret'
        AND pa.authority_source = 'git_data_source_credential'
    ))
    AND (
        (ds.type <> 'github' AND pa.authority_source <> 'github_app_installation')
        OR (
            ds.type = 'github' AND pa.publication_mode = 'organization_service' AND pa.provider_host = 'github.com'
            AND pa.authority_source = 'github_app_installation' AND c.kind = 'static_secret'
            AND EXISTS (
                SELECT 1 FROM github_app_installations installation
                JOIN github_app_repositories repository ON repository.installation_id = installation.id AND repository.organization_id = o.id
                WHERE installation.organization_id = o.id AND installation.suspended_at IS NULL AND installation.deleted_at IS NULL
                    AND repository.removed_at IS NULL
                    AND installation.installation_id::text = pa.provider_credential_id
                    AND installation.account_id::text = pa.provider_account_id
                    AND repository.repository_id::text = pa.provider_repository_id
                    AND repository.repository_id::text = ds.config #>> '{repository,id}'
                    AND p.pipeline_backend_repo = 'github.com/' || repository.full_name
            )
        )
    )
    AND (
        pa.publication_mode <> 'delegated_user'
        OR (
            c.kind = 'oauth2_grant'
            AND cog.provider = ds.type
            AND cog.account ->> 'ID' = pa.executing_principal_id
            AND cog.account ->> 'ID' = pa.provider_account_id
        )
    );

-- Hash both timestamp revisions in UTC even when the control and executor sessions use different zones.
CREATE FUNCTION proposal_authority_digest(authority active_pipeline_proposal_authorities) RETURNS bytea
LANGUAGE sql STABLE SET TimeZone = 'UTC' AS $$
    SELECT sha256(convert_to(to_jsonb(authority)::text, 'UTF8'));
$$;

CREATE TABLE execution_proposal_targets (
    request_id uuid PRIMARY KEY REFERENCES execution_requests(id) ON DELETE CASCADE,
    member_id integer NOT NULL,
    project_id bigint NOT NULL,
    pipeline_id bigint,
    pipeline_slug text NOT NULL,
    attempt_id text NOT NULL,
    authority_id bigint NOT NULL,
    provider_digest bytea NOT NULL
);

-- The executor can inspect only its active claim's relevant permissions and target.
CREATE FUNCTION claimed_proposal_authorization(request_id uuid, claim_token uuid) RETURNS jsonb
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public AS $$
WITH allowed_target AS (
    SELECT b.pipeline_id, b.pipeline_slug, a.org_slug, a.project_slug, u.slug AS user_slug
    FROM public.execution_requests req
    JOIN public.execution_proposal_targets b ON b.request_id = req.id
    JOIN public.organization_members m ON (m.organization_id, m.id) = (req.organization_id, b.member_id)
    JOIN public.users u ON u.id = req.initiating_user_id AND u.id = m.user_id
    JOIN public.active_pipeline_proposal_authorities a ON (a.organization_id, a.project_id, a.id) =
        (req.organization_id, b.project_id, b.authority_id)
    WHERE req.id = $1 AND req.claim_token = $2 AND req.state = 'running'
        AND req.cancel_requested_at IS NULL AND req.deadline_at > clock_timestamp() AND req.lease_expires_at > clock_timestamp()
        AND b.provider_digest = public.proposal_authority_digest(a)
        AND ((req.kind = 'git_scoped_proposal' AND EXISTS (
            SELECT 1 FROM public.pipeline_specs s JOIN public.pipeline_drafts d
                ON (d.project_id, d.pipeline_slug) = (s.project_id, s.slug)
            WHERE s.id = b.pipeline_id AND s.project_id = b.project_id AND s.deleted_at IS NULL AND s.slug = b.pipeline_slug
                AND d.member_id = b.member_id AND d.publish_attempt_id = b.attempt_id AND d.proposal_authority_id = b.authority_id
                AND d.draft_state = 'publish_queued'
        )) OR (req.kind = 'git_scoped_creation_proposal' AND b.pipeline_id IS NULL AND EXISTS (
            SELECT 1 FROM public.pipeline_creation_proposals c WHERE c.project_id = b.project_id
                AND c.pipeline_slug = b.pipeline_slug AND c.member_id = b.member_id AND c.publish_attempt_id = b.attempt_id
                AND c.status = 'publish_queued'
        ) AND NOT EXISTS (SELECT 1 FROM public.pipeline_specs s WHERE s.project_id = b.project_id
            AND s.slug = b.pipeline_slug AND s.deleted_at IS NULL)))
), resolved AS MATERIALIZED (
    SELECT pipeline_id, public.authorization_facts(org_slug::text, user_slug::text, '',
        CASE WHEN pipeline_id IS NULL THEN 'projects' ELSE 'pipelines' END,
        CASE WHEN pipeline_id IS NULL THEN project_slug ELSE pipeline_slug END,
        project_slug::text, '', '') AS facts,
        CASE WHEN pipeline_id IS NULL THEN 'pipelines:create' ELSE 'pipelines:update' END AS permission
    FROM allowed_target
)
SELECT jsonb_set(facts - 'owner_slug', '{bindings}', COALESCE((
    SELECT jsonb_agg(jsonb_build_object('scope', b->'scope', 'scope_id', b->'scope_id', 'permissions', jsonb_build_array(p)))
    FROM jsonb_array_elements(facts->'bindings') b
    CROSS JOIN LATERAL jsonb_array_elements_text(b->'permissions') p
    WHERE p IN ('*:*','pipelines:*',permission)
        AND b->>'scope_id' = CASE b->>'scope' WHEN 'organization' THEN facts->>'organization_id'
            WHEN 'project' THEN facts->>'project_id' WHEN 'resource' THEN facts->>'target_id' END
), '[]'::jsonb)) FROM resolved
WHERE pipeline_id IS NULL OR facts->>'target_id' = 'pipelines:' || pipeline_id::text;
$$;
REVOKE ALL ON FUNCTION claimed_proposal_authorization(uuid, uuid) FROM PUBLIC;
