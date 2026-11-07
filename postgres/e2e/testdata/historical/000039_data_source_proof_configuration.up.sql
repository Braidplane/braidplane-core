-- Archived transition regression fixture; outside the applied Core migration catalog.
CREATE TABLE organization_gitlab_connections (
    id BIGSERIAL PRIMARY KEY,
    organization_id INTEGER NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    credential_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_organization_gitlab_connections_org UNIQUE (organization_id),
    CONSTRAINT uq_organization_gitlab_connections_org_id UNIQUE (organization_id, id),
    CONSTRAINT fk_organization_gitlab_connections_credential_org FOREIGN KEY (organization_id, credential_id) REFERENCES credentials (organization_id, id) ON DELETE RESTRICT
);

ALTER TABLE data_sources
    ADD COLUMN organization_gitlab_connection_id BIGINT,
    ADD CONSTRAINT fk_data_sources_organization_gitlab_connection_org FOREIGN KEY (organization_id, organization_gitlab_connection_id) REFERENCES organization_gitlab_connections (organization_id, id) ON DELETE RESTRICT;

CREATE INDEX idx_data_sources_organization_gitlab_connection ON data_sources (organization_id, organization_gitlab_connection_id) WHERE organization_gitlab_connection_id IS NOT NULL;

ALTER TABLE credential_oauth_installs
    ALTER COLUMN data_source_id DROP NOT NULL,
    ADD COLUMN organization_gitlab_connection_id BIGINT,
    ADD CONSTRAINT fk_credential_oauth_installs_organization_gitlab_connection_org FOREIGN KEY (organization_id, organization_gitlab_connection_id) REFERENCES organization_gitlab_connections (organization_id, id) ON DELETE CASCADE,
    ADD CONSTRAINT ck_credential_oauth_installs_owner CHECK ((data_source_id IS NULL) <> (organization_gitlab_connection_id IS NULL));

ALTER TABLE data_sources
    ADD COLUMN proof_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN proof_checks_enabled BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE proof_change_candidate_mappings ADD COLUMN disabled_at TIMESTAMPTZ;

-- Preserve explicit existing assessment configuration. New sources opt in.
UPDATE data_sources ds
SET proof_enabled = true, proof_checks_enabled = (ds.type = 'github')
WHERE EXISTS (
    SELECT 1 FROM proof_change_candidate_mappings mapping
    WHERE mapping.organization_id = ds.organization_id AND mapping.data_source_id = ds.id
);

-- Mapping identity is a historical snapshot used by immutable candidates and
-- receipts. Replacing a repository retires its targets, even if later restored.
CREATE FUNCTION invalidate_data_source_proof() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.type IS DISTINCT FROM NEW.type
        OR OLD.config #>> '{repository,id}' IS DISTINCT FROM NEW.config #>> '{repository,id}'
        OR OLD.config ->> 'repo_url' IS DISTINCT FROM NEW.config ->> 'repo_url'
        OR (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL)
    THEN
        UPDATE proof_change_candidate_mappings
        SET disabled_at = CURRENT_TIMESTAMP
        WHERE organization_id = OLD.organization_id AND data_source_id = OLD.id
            AND disabled_at IS NULL;
        NEW.proof_enabled := false;
        NEW.proof_checks_enabled := false;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER invalidate_data_source_proof
BEFORE UPDATE OF type, config, deleted_at ON data_sources
FOR EACH ROW EXECUTE FUNCTION invalidate_data_source_proof();

-- Manual provider records become Generic Git in place. Credentials, source IDs,
-- references and read restrictions are retained; no new grant is inferred.
DO $$
DECLARE manual_count BIGINT;
BEGIN
    SELECT COUNT(*) INTO manual_count
    FROM data_sources ds
    JOIN credentials credential ON credential.organization_id = ds.organization_id AND credential.id = ds.credential_id
    WHERE credential.kind = 'static_secret' AND (
        (ds.type IN ('github', 'gitlab') AND COALESCE(ds.config->>'repo_url', '') <> '')
        OR (ds.type = 'gitlab' AND COALESCE(ds.config #>> '{repository,web_url}', '') <> '')
    );
    RAISE NOTICE 'Converting % manual provider data sources to Generic Git; identities and credentials are retained', manual_count;
END;
$$;

UPDATE data_sources ds
SET type = 'git',
    config = CASE WHEN COALESCE(ds.config->>'repo_url', '') <> '' THEN ds.config
        ELSE (ds.config - 'repository') || jsonb_strip_nulls(jsonb_build_object(
            'repo_url', regexp_replace(ds.config #>> '{repository,web_url}', '/+$', '') || '.git',
            'default_branch', ds.config #>> '{repository,default_branch}'
        )) END,
    connection_test_revision = ds.connection_test_revision + 1
FROM credentials credential
WHERE credential.organization_id = ds.organization_id AND credential.id = ds.credential_id
    AND credential.kind = 'static_secret' AND (
        (ds.type IN ('github', 'gitlab') AND COALESCE(ds.config->>'repo_url', '') <> '')
        OR (ds.type = 'gitlab' AND COALESCE(ds.config #>> '{repository,web_url}', '') <> '')
    );

-- Intake and new assessment work share this fail-closed eligibility boundary.
-- Historical receipt queries intentionally continue reading the retained tables.
CREATE VIEW active_proof_candidate_mappings AS
SELECT mapping.*
FROM proof_change_candidate_mappings mapping
JOIN data_sources ds ON ds.organization_id = mapping.organization_id AND ds.id = mapping.data_source_id
JOIN credentials credential ON credential.organization_id = ds.organization_id AND credential.id = ds.credential_id
JOIN projects project ON project.organization_id = mapping.organization_id AND project.id = mapping.project_id
JOIN project_environments pe ON pe.project_id = project.id AND pe.id = mapping.project_environment_id
JOIN environments environment ON environment.organization_id = mapping.organization_id AND environment.id = pe.environment_id
WHERE mapping.disabled_at IS NULL
    AND ds.proof_enabled AND ds.is_active AND ds.deleted_at IS NULL
    AND ds.type = mapping.provider
    AND ds.config #>> '{repository,id}' = mapping.repository_id
    AND credential.status = 'active' AND credential.revoked_at IS NULL
    AND pe.deleted_at IS NULL AND environment.deleted_at IS NULL
    AND project.pipeline_backend_data_source_id = ds.id
    AND 'refs/heads/' || project.pipeline_backend_tracked_branch = mapping.target_ref
    AND LEFT(mapping.pipeline_path, LENGTH(project.pipeline_backend_pipeline_dir) + 1) = project.pipeline_backend_pipeline_dir || '/'
    AND (
        (mapping.provider = 'github' AND EXISTS (
            SELECT 1 FROM github_app_installations installation
            JOIN github_app_repositories repository ON repository.organization_id = installation.organization_id
                AND repository.installation_id = installation.id
            WHERE installation.organization_id = mapping.organization_id
                AND installation.installation_id::text = mapping.provider_connection_id
                AND repository.repository_id::text = mapping.repository_id
                AND installation.suspended_at IS NULL AND installation.deleted_at IS NULL
                AND repository.removed_at IS NULL
        ))
        OR (mapping.provider = 'gitlab' AND mapping.provider_connection_id = ds.slug
            AND credential.kind = 'oauth2_grant')
    );
