-- The control worker and claimed executor consult the same current provider binding.
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
        ds.type <> 'github' OR ds.config #>> '{repository,id}' IS NULL
        OR (
            pa.publication_mode = 'organization_service' AND pa.provider_host = 'github.com'
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
        AND b.provider_digest = sha256(convert_to(to_jsonb(a)::text, 'UTF8'))
        AND ((req.kind = 'git_scoped_proposal' AND EXISTS (
            SELECT 1 FROM public.pipeline_specs s JOIN public.pipeline_drafts d
                ON (d.project_id, d.pipeline_slug) = (s.project_id, s.slug)
            WHERE s.id = b.pipeline_id AND s.project_id = b.project_id AND s.deleted_at IS NULL AND s.slug = b.pipeline_slug
                AND d.member_id = b.member_id AND d.publish_attempt_id = b.attempt_id AND d.proposal_authority_id = b.authority_id
        )) OR (req.kind = 'git_scoped_creation_proposal' AND b.pipeline_id IS NULL AND EXISTS (
            SELECT 1 FROM public.pipeline_creation_proposals c WHERE c.project_id = b.project_id
                AND c.pipeline_slug = b.pipeline_slug AND c.member_id = b.member_id AND c.publish_attempt_id = b.attempt_id
                AND c.status = 'publish_queued'
        ) AND NOT EXISTS (SELECT 1 FROM public.pipeline_specs s WHERE s.project_id = b.project_id
            AND s.slug = b.pipeline_slug AND s.deleted_at IS NULL)))
), resolved AS (
    SELECT public.authorization_facts(org_slug::text, user_slug::text, '',
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
), '[]'::jsonb)) FROM resolved;
$$;
REVOKE ALL ON FUNCTION claimed_proposal_authorization(uuid, uuid) FROM PUBLIC;
