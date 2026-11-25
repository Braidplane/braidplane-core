-- Legacy GitHub sources can omit their repository ID. Installation authorities still
-- require the recorded account, repository, and project path to be actively selected.
CREATE OR REPLACE VIEW active_pipeline_proposal_authorities AS
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
        (pa.authority_source <> 'github_app_installation' AND (ds.type <> 'github' OR ds.config #>> '{repository,id}' IS NULL))
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
                    AND (ds.config #>> '{repository,id}' IS NULL OR repository.repository_id::text = ds.config #>> '{repository,id}')
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
