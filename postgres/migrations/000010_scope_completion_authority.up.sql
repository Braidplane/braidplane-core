-- Closure and execution use the same resolved scopes as request authorization.
CREATE FUNCTION pipeline_completion_scoped_authority(
    org_id bigint, env_id bigint, configuration jsonb, accepted boolean
) RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE
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
        WHERE m.organization_id = org_id AND u.slug = actor_slug
        FOR SHARE OF m, u;
    ELSIF actor_type = 'api_key' THEN
        key_slug := actor_slug;
        SELECT k.id, k.role_id INTO machine_key, assigned_role
        FROM api_keys k WHERE k.organization_id = org_id AND k.slug = actor_slug
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
        'principal_id', CASE WHEN member_key IS NOT NULL THEN 'member:' || member_key ELSE 'api_key:' || machine_key END,
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
        authority_value := pipeline_completion_scoped_authority(NEW.organization_id,
            NEW.project_environment_id, to_jsonb(target_trigger), false);
        IF NOT target_trigger.enabled THEN refusal := 'trigger_disabled';
        ELSIF NOT (target_trigger.completion_config->>'execution_grant_enabled')::boolean THEN refusal := 'grant_revoked';
        ELSIF protected_target THEN refusal := 'protected_environment';
        ELSIF depth_value > 8 THEN refusal := 'causal_depth_exceeded';
        ELSIF target_trigger.pipeline_slug = ANY(ancestors) THEN refusal := 'ancestor_pipeline_repeated';
        ELSIF NOT COALESCE((authority_value->>'valid')::boolean, false) THEN refusal := 'authority_revoked';
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
