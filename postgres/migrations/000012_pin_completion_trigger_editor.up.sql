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

ALTER FUNCTION pipeline_completion_scoped_authority(bigint, bigint, jsonb, boolean)
    RENAME TO resolve_pipeline_completion_scopes;

CREATE OR REPLACE FUNCTION pipeline_completion_scoped_authority(
    org_id bigint, env_id bigint, configuration jsonb, accepted boolean
) RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE
    editor_id text := configuration->>'completion_editor_id';
    facts jsonb;
BEGIN
    IF editor_id IS NULL THEN RETURN jsonb_build_object('valid', false); END IF;
    facts := resolve_pipeline_completion_scopes(org_id, env_id, configuration, accepted);
    IF facts->>'principal_id' IS DISTINCT FROM editor_id THEN RETURN jsonb_build_object('valid', false); END IF;
    RETURN facts;
END;
$$;
