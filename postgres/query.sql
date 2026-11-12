-- name: IsUserSlugReserved :one
SELECT EXISTS (SELECT 1 FROM users WHERE slug = sqlc.arg('user_slug'))::boolean;

-- name: OrganizationDeletionBlocked :one
-- Keep existing queue rows pinned through deletion so a completed job cannot
-- be retried between the processing check and removal.
WITH jobs AS MATERIALIZED (
    SELECT state, attempted_at FROM river_job
    WHERE args->>'organization_slug' = sqlc.arg('org_slug')::text
        OR args->>'org_slug' = sqlc.arg('org_slug')::text
        OR args->>'organization' = sqlc.arg('org_slug')::text
    ORDER BY id FOR UPDATE
)
SELECT EXISTS (SELECT 1 FROM projects WHERE organization_id = o.id)
    OR EXISTS (SELECT 1 FROM data_sources WHERE organization_id = o.id AND deleted_at IS NULL)
    OR EXISTS (SELECT 1 FROM snowflake_wif_bindings WHERE organization_id = o.id)
    OR EXISTS (SELECT 1 FROM execution_requests WHERE organization_id = o.id
        AND (state NOT IN ('succeeded', 'failed', 'cancelled') OR (state = 'cancelled' AND started_at IS NOT NULL)))
    OR EXISTS (
        SELECT 1 FROM jobs
        WHERE (state NOT IN ('completed', 'cancelled', 'discarded') OR (state = 'cancelled' AND attempted_at IS NOT NULL))
    ) AS blocked
FROM organizations o WHERE o.slug = sqlc.arg('org_slug');


-- name: DeleteOrganization :execrows
WITH target AS MATERIALIZED (
    SELECT id FROM organizations WHERE slug = sqlc.arg('org_slug')
), executions AS (
    DELETE FROM execution_requests WHERE organization_id IN (SELECT id FROM target) RETURNING id
), jobs AS (
    DELETE FROM river_job WHERE args->>'organization_slug' = sqlc.arg('org_slug')::text
        OR args->>'org_slug' = sqlc.arg('org_slug')::text
        OR args->>'organization' = sqlc.arg('org_slug')::text
    RETURNING id
), sources AS (
    DELETE FROM data_sources WHERE organization_id IN (SELECT id FROM target) RETURNING id
), installs AS (
    DELETE FROM credential_oauth_installs WHERE organization_id IN (SELECT id FROM target) RETURNING id
), destinations AS (
    DELETE FROM alert_destinations WHERE organization_id IN (SELECT id FROM target) RETURNING id
), environments AS (
    DELETE FROM environments WHERE organization_id IN (SELECT id FROM target) RETURNING id
), keys AS (
    DELETE FROM api_keys WHERE organization_id IN (SELECT id FROM target) RETURNING id
), invitations AS (
    DELETE FROM organization_member_invitations WHERE organization_id IN (SELECT id FROM target) RETURNING id
), members AS (
    DELETE FROM organization_members WHERE organization_id IN (SELECT id FROM target) RETURNING id
)
DELETE FROM organizations WHERE id IN (SELECT id FROM target)
    AND (SELECT count(*) FROM executions) >= 0
    AND (SELECT count(*) FROM jobs) >= 0
    AND (SELECT count(*) FROM sources) >= 0
    AND (SELECT count(*) FROM installs) >= 0
    AND (SELECT count(*) FROM destinations) >= 0
    AND (SELECT count(*) FROM environments) >= 0
    AND (SELECT count(*) FROM keys) >= 0
    AND (SELECT count(*) FROM invitations) >= 0
    AND (SELECT count(*) FROM members) >= 0;

-- name: LockAccountForDeletion :one
SELECT u.id, u.slug, u.email, u.deleted_at
FROM users u WHERE u.slug = sqlc.arg('user_slug') FOR UPDATE OF u;

-- name: LockDeletionSubjectByID :one
SELECT u.id, u.slug, u.email, u.deleted_at
FROM users u WHERE u.id = sqlc.arg('user_id') FOR UPDATE OF u;

-- name: FindAccountDeletionRequest :one
SELECT * FROM account_deletion_requests WHERE user_id = sqlc.arg('user_id');

-- name: LockAccountDeletionRequest :one
SELECT * FROM account_deletion_requests WHERE slug = sqlc.arg('request_slug') FOR UPDATE;

-- name: CreateAccountDeletionRequest :one
INSERT INTO account_deletion_requests (slug, user_id)
VALUES (sqlc.arg('request_slug'), sqlc.arg('user_id')) RETURNING *;

-- name: CompleteAccountDeletionRequest :one
UPDATE account_deletion_requests SET state = 'completed', user_id = NULL,
    completed_at = CURRENT_TIMESTAMP, failure_code = NULL
WHERE id = sqlc.arg('request_id') RETURNING *;

-- name: FailAccountDeletionRequest :exec
UPDATE account_deletion_requests SET state = 'failed', failure_code = 'deletion_failed'
WHERE slug = sqlc.arg('request_slug') AND state <> 'completed';

-- name: LockAccountDeletionOrganizations :many
SELECT o.slug, r.slug AS role_slug
FROM organizations o
JOIN organization_members m ON m.organization_id = o.id
JOIN roles r ON r.id = m.role_id
WHERE m.user_id = sqlc.arg('user_id')
ORDER BY o.id FOR UPDATE OF o;

-- name: HasOtherAccountDeletionAdmin :one
SELECT EXISTS (
    SELECT 1 FROM organization_members m
    JOIN organizations o ON o.id = m.organization_id
    JOIN roles r ON r.id = m.role_id
    JOIN users u ON u.id = m.user_id
    WHERE o.slug = sqlc.arg('org_slug') AND r.slug = 'admin'
        AND u.id <> sqlc.arg('user_id') AND u.deleted_at IS NULL
)::boolean;

-- name: HasOtherAccountDeletionMember :one
SELECT EXISTS (
    SELECT 1 FROM organization_members m
    JOIN organizations o ON o.id = m.organization_id
    JOIN users u ON u.id = m.user_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND u.id <> sqlc.arg('user_id') AND u.deleted_at IS NULL
)::boolean;

-- name: DeleteAccountPATs :exec
DELETE FROM personal_access_tokens WHERE user_id = sqlc.arg('user_id');

-- name: DeleteAccountMFAChallenges :exec
DELETE FROM user_mfa_challenges WHERE user_id = sqlc.arg('user_id');

-- name: DeleteAccountMemberships :exec
DELETE FROM organization_members WHERE user_id = sqlc.arg('user_id');

-- name: RevokeAccountPendingInvitations :exec
DELETE FROM organization_member_invitations
WHERE status = 'pending' AND LOWER(email) = LOWER(sqlc.arg('email')::text);

-- name: CancelAccountExecutionRequests :exec
UPDATE execution_requests
SET cancel_requested_at = COALESCE(cancel_requested_at, CURRENT_TIMESTAMP),
    state = CASE WHEN state = 'pending' THEN 'cancelled' ELSE state END,
    completed_at = CASE WHEN state = 'pending' THEN CURRENT_TIMESTAMP ELSE completed_at END,
    updated_at = CURRENT_TIMESTAMP
WHERE initiating_user_id = sqlc.arg('user_id') AND state IN ('pending', 'running');

-- name: RevokeAccount :exec
UPDATE users SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP,
    password = NULL, oidc_provider = NULL, oidc_provider_id = NULL
WHERE id = sqlc.arg('user_id');

-- name: AccountDeletionProcessingPending :one
WITH jobs AS MATERIALIZED (
    SELECT state, attempted_at FROM river_job
    WHERE (kind IN ('pipeline_git_write_file', 'pipeline_git_delete_file') AND args->>'initiating_user_slug' = sqlc.arg('user_slug')::text)
        OR ((kind IN ('pipeline_git_publish_proposal', 'pipeline_git_refresh_proposal') AND args->>'member_slug' = sqlc.arg('user_slug')::text)
        OR (kind = 'pipeline_run_execution' AND args->'actor'->>'Type' = 'user' AND args->'actor'->>'Slug' = sqlc.arg('user_slug')::text)
        OR (kind = 'email_send' AND args->'user'->>'slug' = sqlc.arg('user_slug')::text))
    ORDER BY id FOR UPDATE
)
SELECT EXISTS (
    SELECT 1 FROM execution_requests WHERE initiating_user_id = sqlc.arg('user_id')
        AND (state NOT IN ('succeeded', 'failed', 'cancelled') OR (state = 'cancelled' AND started_at IS NOT NULL))
) OR EXISTS (
    SELECT 1 FROM jobs WHERE state NOT IN ('completed', 'cancelled', 'discarded')
        OR (state = 'cancelled' AND attempted_at IS NOT NULL)
) AS pending;


-- name: AccountDeletionLegacyProcessingPending :one
SELECT EXISTS (
    SELECT 1 FROM execution_requests e JOIN organization_members m ON m.organization_id = e.organization_id
    WHERE m.user_id = sqlc.arg('user_id') AND e.initiating_user_id IS NULL
        AND (e.state NOT IN ('succeeded', 'failed', 'cancelled') OR (e.state = 'cancelled' AND e.started_at IS NOT NULL))
) OR EXISTS (
    SELECT 1 FROM river_job j
    JOIN organizations o ON o.slug = j.args->>'organization_slug'
    JOIN organization_members m ON m.organization_id = o.id
    WHERE m.user_id = sqlc.arg('user_id') AND j.state NOT IN ('completed', 'cancelled', 'discarded')
        AND ((j.kind IN ('pipeline_git_write_file', 'pipeline_git_delete_file') AND COALESCE(j.args->>'initiating_user_slug', '') = '')
            OR (j.kind IN ('pipeline_git_publish_proposal', 'pipeline_git_refresh_proposal') AND COALESCE(j.args->>'member_slug', '') = ''))
) AS pending;

-- name: HardDeleteAccount :execrows
WITH executions AS (
    DELETE FROM execution_requests WHERE initiating_user_id = sqlc.arg('user_id') RETURNING id
), jobs AS (
    DELETE FROM river_job
    WHERE (kind IN ('pipeline_git_write_file', 'pipeline_git_delete_file') AND args->>'initiating_user_slug' = sqlc.arg('user_slug')::text)
        OR ((kind IN ('pipeline_git_publish_proposal', 'pipeline_git_refresh_proposal') AND args->>'member_slug' = sqlc.arg('user_slug')::text)
        OR (kind = 'pipeline_run_execution' AND args->'actor'->>'Type' = 'user' AND args->'actor'->>'Slug' = sqlc.arg('user_slug')::text)
        OR (kind = 'email_send' AND args->'user'->>'slug' = sqlc.arg('user_slug')::text))
    RETURNING id
), invitations AS (
    DELETE FROM organization_member_invitations WHERE accepted_by_user_id = sqlc.arg('user_id') RETURNING id
)
DELETE FROM users WHERE id = sqlc.arg('user_id') AND deleted_at IS NOT NULL
    AND (SELECT count(*) FROM executions) >= 0
    AND (SELECT count(*) FROM jobs) >= 0
    AND (SELECT count(*) FROM invitations) >= 0;

-- name: BindExecutionRequestUser :execrows
WITH active_user AS (
    SELECT id FROM users
    WHERE slug = sqlc.arg('user_slug') AND deleted_at IS NULL
    FOR SHARE
)
UPDATE execution_requests request
SET initiating_user_id = active_user.id
FROM active_user, organizations organization
WHERE request.id = sqlc.arg('request_id')::text::uuid
    AND request.organization_id = organization.id
    AND organization.slug = sqlc.arg('org_slug')
    AND request.state = 'pending'
    AND request.initiating_user_id IS NULL;

-- name: LockExecutionInitiatingAccount :one
SELECT EXISTS (
    SELECT 1 FROM users WHERE slug = sqlc.arg('user_slug') AND deleted_at IS NULL FOR SHARE
);

-- name: LockAccountJobs :many
-- Account removal crosses memberships but cancels only the subject's own jobs.
SELECT id FROM river_job
WHERE state NOT IN ('cancelled', 'completed', 'discarded')
    AND ((kind IN ('pipeline_git_write_file', 'pipeline_git_delete_file') AND args->>'initiating_user_slug' = sqlc.arg('user_slug')::text)
        OR ((kind IN ('pipeline_git_publish_proposal', 'pipeline_git_refresh_proposal') AND args->>'member_slug' = sqlc.arg('user_slug')::text)
        OR (kind = 'pipeline_run_execution' AND args->'actor'->>'Type' = 'user' AND args->'actor'->>'Slug' = sqlc.arg('user_slug')::text)
        OR (kind = 'email_send' AND args->'user'->>'slug' = sqlc.arg('user_slug')::text)))
ORDER BY id FOR UPDATE;

-- name: ExpireExecutionRequests :one
-- Payload expiry must not erase an unresolved processing obligation. A lease
-- can mark started work cancelled without acknowledgement from its executor.
WITH expired_files AS (
    DELETE FROM retained_file_streams
    WHERE expires_at <= clock_timestamp()
    RETURNING request_id
), scrubbed AS (
    UPDATE execution_requests
    SET request_ciphertext = ''::bytea, result_ciphertext = NULL,
        cancel_requested_at = COALESCE(cancel_requested_at, clock_timestamp()),
        updated_at = clock_timestamp()
    WHERE expires_at <= clock_timestamp()
        AND state NOT IN ('pending', 'succeeded', 'failed')
        AND (state <> 'cancelled' OR started_at IS NOT NULL)
        AND (octet_length(request_ciphertext) > 0 OR result_ciphertext IS NOT NULL OR cancel_requested_at IS NULL)
    RETURNING id
), deleted AS (
    DELETE FROM execution_requests
    WHERE expires_at <= clock_timestamp()
        AND (state IN ('pending', 'succeeded', 'failed') OR (state = 'cancelled' AND started_at IS NULL))
    RETURNING id
)
SELECT count(*) FROM deleted;

-- name: CreateUser :one
INSERT INTO
    users (
        slug,
        email,
        full_name,
        password,
        role,
        locale
    )
VALUES (
        sqlc.arg ('slug'),
        sqlc.arg ('email'),
        sqlc.arg ('full_name'),
        sqlc.arg ('password'),
        sqlc.arg ('role')::text,
        sqlc.arg ('locale')
    )
RETURNING
    *;

-- name: FindUserByEmail :one
SELECT * FROM users WHERE email = sqlc.arg ('email') AND deleted_at IS NULL FOR UPDATE;

-- name: FindUserBySlug :one
SELECT * FROM users WHERE slug = sqlc.arg ('slug') AND deleted_at IS NULL;

-- name: FindUserBySlugForUpdate :one
SELECT * FROM users WHERE slug = sqlc.arg ('slug') AND deleted_at IS NULL FOR UPDATE;

-- name: LockInstanceAdminRoleChanges :exec
SELECT pg_advisory_xact_lock(hashtextextended('instance-admin-role-changes', 0));

-- name: FindInstanceAdminUserForUpdate :one
SELECT slug, role, verified_at
FROM users
WHERE slug = sqlc.arg('slug') AND deleted_at IS NULL
FOR UPDATE;

-- name: CountSuperAdmins :one
SELECT count(*)
FROM users
WHERE role = 'super_admin' AND deleted_at IS NULL;

-- name: SetInstanceAdminUserRole :execrows
UPDATE users
SET role = sqlc.arg('role')::text, updated_at = NOW()
WHERE slug = sqlc.arg('slug') AND deleted_at IS NULL;

-- name: CreateInstanceAdminAudit :exec
INSERT INTO instance_admin_audits (
    target_user_slug,
    previous_role,
    new_role,
    operator_identity
) VALUES (
    sqlc.arg('target_user_slug'),
    sqlc.arg('previous_role'),
    sqlc.arg('new_role'),
    current_user
);

-- name: CreateOIDCUser :one
INSERT INTO
    users (
        slug,
        email,
        full_name,
        oidc_provider,
        oidc_provider_id,
        avatar_url,
        role,
        verified_at,
        locale
    )
VALUES (
        sqlc.arg ('slug'),
        sqlc.arg ('email'),
        sqlc.arg ('full_name'),
        sqlc.arg ('oidc_provider')::text,
        sqlc.arg ('oidc_provider_id'),
        sqlc.arg ('avatar_url'),
        sqlc.arg ('role')::text,
        sqlc.arg ('verified_at'),
        sqlc.arg ('locale')
    )
RETURNING
    *;

-- name: FindUserByOIDCProvider :one
SELECT *
FROM users
WHERE
    oidc_provider = sqlc.arg ('oidc_provider')::text
    AND oidc_provider_id = sqlc.arg('oidc_provider_id')
    AND deleted_at IS NULL
LIMIT 1;

-- name: CreateEmailVerificationToken :exec
UPDATE users
SET
    email_verification_token_hash = sqlc.arg('email_verification_token_hash'),
    email_verification_token_expires_at = sqlc.arg('email_verification_token_expires_at'),
    updated_at = NOW()
WHERE
    slug = sqlc.arg('slug') AND deleted_at IS NULL;

-- name: CreatePasswordResetToken :exec
UPDATE users
SET
    password_reset_token_hash = sqlc.arg('password_reset_token_hash'),
    password_reset_token_expires_at = sqlc.arg('password_reset_token_expires_at'),
    updated_at = NOW()
WHERE
    slug = sqlc.arg('slug') AND deleted_at IS NULL;

-- name: FindUserByEmailVerificationToken :one
SELECT *
FROM users
WHERE
    email_verification_token_hash = sqlc.arg('email_verification_token_hash')
    AND deleted_at IS NULL
    AND email_verification_token_expires_at > NOW();

-- name: FindUserByPasswordResetToken :one
SELECT *
FROM users
WHERE
    password_reset_token_hash = sqlc.arg('password_reset_token_hash')
    AND deleted_at IS NULL
    AND password_reset_token_expires_at > NOW()
FOR UPDATE;

-- name: MarkAsVerified :exec
UPDATE users
SET
    verified_at = sqlc.arg('verified_at'),
    email_verification_token_hash = NULL,
    email_verification_token_expires_at = NULL,
    updated_at = NOW()
WHERE
    slug = sqlc.arg('slug') AND deleted_at IS NULL;

-- name: UpdateUserPassword :exec
UPDATE users
SET
    password = sqlc.arg('password'),
    authentication_version = authentication_version + 1,
    password_reset_token_hash = NULL,
    password_reset_token_expires_at = NULL,
    updated_at = NOW()
WHERE
    slug = sqlc.arg('slug') AND deleted_at IS NULL;

-- name: UpdateUserProfile :exec
UPDATE users
SET
    full_name = CASE
        WHEN sqlc.arg('full_name_set')::boolean THEN sqlc.arg('full_name')
        ELSE full_name
    END,
    locale = CASE
        WHEN sqlc.arg('locale_set')::boolean THEN sqlc.arg('locale')
        ELSE locale
    END,
    avatar_url = CASE
        WHEN sqlc.arg('avatar_url_set')::boolean THEN NULLIF(sqlc.arg('avatar_url'), '')
        ELSE avatar_url
    END,
    phone_number = CASE
        WHEN sqlc.arg('phone_number_set')::boolean THEN NULLIF(sqlc.arg('phone_number'), '')
        ELSE phone_number
    END,
    job_title = CASE
        WHEN sqlc.arg('job_title_set')::boolean THEN NULLIF(sqlc.arg('job_title'), '')
        ELSE job_title
    END,
    timezone = CASE
        WHEN sqlc.arg('timezone_set')::boolean THEN NULLIF(sqlc.arg('timezone'), '')
        ELSE timezone
    END,
    mfa_email_enabled = CASE
        WHEN sqlc.arg('mfa_email_enabled_set')::boolean THEN sqlc.arg('mfa_email_enabled')::boolean
        ELSE mfa_email_enabled
    END,
    updated_at = NOW()
WHERE
    slug = sqlc.arg ('slug') AND deleted_at IS NULL;

-- name: CreateUserMFAChallenge :one
INSERT INTO
    user_mfa_challenges (
        user_id,
        authentication_version,
        challenge_token_hash,
        code_hash,
        expires_at
    )
SELECT
    users.id,
    users.authentication_version,
    sqlc.arg('challenge_token_hash'),
    sqlc.arg('code_hash'),
    sqlc.arg('expires_at')
FROM users
WHERE users.slug = sqlc.arg('user_slug') AND users.deleted_at IS NULL
FOR SHARE OF users
RETURNING
    *;

-- name: FindUserMFAChallengeByToken :one
SELECT
    user_mfa_challenges.*,
    users.slug AS user_slug,
    users.email,
    users.full_name,
    users.role,
    users.locale,
    users.oidc_provider,
    users.oidc_provider_id,
    users.avatar_url,
    users.phone_number,
    users.job_title,
    users.timezone,
    users.mfa_email_enabled,
    users.authentication_version AS user_authentication_version,
    users.password IS NOT NULL AND users.password <> '' AS has_local_password,
    users.verified_at,
    users.created_at AS user_created_at,
    users.updated_at AS user_updated_at
FROM user_mfa_challenges
JOIN users ON users.id = user_mfa_challenges.user_id
WHERE
    user_mfa_challenges.challenge_token_hash = sqlc.arg('challenge_token_hash')
    AND users.deleted_at IS NULL
    AND user_mfa_challenges.authentication_version = users.authentication_version
FOR UPDATE OF user_mfa_challenges;

-- name: IncrementUserMFAChallengeAttempts :exec
UPDATE user_mfa_challenges
SET
    attempt_count = attempt_count + 1,
    updated_at = NOW()
WHERE
    id = sqlc.arg('id');

-- name: ConsumeUserMFAChallenge :one
UPDATE user_mfa_challenges
SET
    consumed_at = NOW(),
    updated_at = NOW()
WHERE
    id = sqlc.arg('id')
    AND consumed_at IS NULL
RETURNING
    id;

-- =====================
-- Organizations
-- =====================

-- name: CreateOrganization :one
INSERT INTO organizations (name, slug)
VALUES (
        sqlc.arg ('name'),
        sqlc.arg ('slug')
)
RETURNING *;

-- name: FindOrganizationBySlug :one
SELECT o.*
FROM organizations o
WHERE o.slug = sqlc.arg ('slug');

-- name: FindOrganizationBySlugForUpdate :one
SELECT * FROM organizations WHERE slug = sqlc.arg ('slug') FOR UPDATE;

-- name: UpdateOrganization :exec
UPDATE organizations
SET
    name = sqlc.arg ('name'),
    updated_at = NOW()
WHERE
    slug = sqlc.arg ('slug');

-- name: UpdateOrganizationSettings :exec
UPDATE organizations
SET
    settings = sqlc.arg ('settings')::jsonb,
    updated_at = NOW()
WHERE
    slug = sqlc.arg ('slug');

-- name: ListOrganizationRetentionSettings :many
-- Used by the daily retention worker; unbounded across organizations by design.
SELECT slug, settings
FROM organizations
ORDER BY slug;

-- name: HasSucceededOrganizationRetentionRunForJob :one
SELECT EXISTS (
    SELECT 1
    FROM organization_retention_runs orr
    JOIN organizations o ON o.id = orr.organization_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND orr.source_job_id = sqlc.arg('source_job_id')
        AND orr.status = 'succeeded'
);

-- name: CreateOrganizationRetentionRun :execrows
INSERT INTO organization_retention_runs (
    organization_id,
    slug,
    source_job_id,
    attempt,
    status,
    policy_snapshot,
    query_executions_deleted,
    replay_checkpoints_deleted,
    pipeline_runs_deleted,
    events_deleted,
    failure_code,
    started_at,
    completed_at
)
SELECT
    o.id,
    sqlc.arg('slug'),
    sqlc.arg('source_job_id'),
    sqlc.arg('attempt'),
    sqlc.arg('status'),
    sqlc.arg('policy_snapshot')::jsonb,
    sqlc.arg('query_executions_deleted'),
    sqlc.arg('replay_checkpoints_deleted'),
    sqlc.arg('pipeline_runs_deleted'),
    sqlc.arg('events_deleted'),
    NULLIF(sqlc.arg('failure_code'), ''),
    sqlc.arg('started_at'),
    sqlc.arg('completed_at')
FROM organizations o
WHERE o.slug = sqlc.arg('org_slug')
FOR KEY SHARE;

-- name: ListOrganizationRetentionRuns :many
SELECT
    orr.slug,
    orr.attempt,
    orr.status,
    orr.policy_snapshot,
    orr.query_executions_deleted,
    orr.replay_checkpoints_deleted,
    orr.pipeline_runs_deleted,
    orr.events_deleted,
    orr.failure_code,
    orr.started_at,
    orr.completed_at
FROM organization_retention_runs orr
JOIN organizations o ON o.id = orr.organization_id
WHERE o.slug = sqlc.arg('org_slug')
ORDER BY orr.started_at DESC, orr.slug DESC
LIMIT 100;

-- name: DeleteExpiredOrganizationRetentionRuns :execrows
DELETE FROM organization_retention_runs orr
USING organizations o
WHERE orr.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND orr.completed_at < sqlc.arg('cutoff');

-- =====================
-- Wishlist registrations
-- =====================

-- name: UpsertWishlistRegistration :one
-- Re-submission refreshes mutable fields with the latest submitted values.
INSERT INTO
    wishlist_registrations (
        work_email,
        care_most_about,
        communication_request_version,
        workflow_to_test_first
    )
VALUES (
    sqlc.arg('work_email'),
    sqlc.arg('care_most_about'),
    sqlc.arg('communication_request_version'),
    NULLIF(sqlc.arg('workflow_to_test_first'), '')
)
ON CONFLICT (work_email) DO UPDATE
SET
    care_most_about = EXCLUDED.care_most_about,
    communication_request_version = EXCLUDED.communication_request_version,
    workflow_to_test_first = EXCLUDED.workflow_to_test_first,
    updated_at = NOW()
RETURNING
    *;

-- name: DeleteExpiredWishlistRegistrations :execrows
DELETE FROM wishlist_registrations
WHERE updated_at <= sqlc.arg('cutoff');

-- name: DeleteExpiredAbuseControlBuckets :execrows
DELETE FROM abuse_control_buckets
WHERE window_started_at < sqlc.arg('cutoff');

-- =====================
-- Projects
-- =====================

-- name: CreateProject :one
INSERT INTO
    projects (
        organization_id,
        slug,
        name
    )
SELECT
        o.id,
        sqlc.arg ('project_slug'),
        sqlc.arg ('name')
FROM organizations o
WHERE
    o.slug = sqlc.arg ('org_slug')
RETURNING
    projects.*,
    COALESCE((
        SELECT ds.slug::text
        FROM data_sources ds
        WHERE ds.id = projects.pipeline_backend_data_source_id
    ), '') AS pipeline_backend_data_source_slug,
    COALESCE(projects.pipeline_backend_repo, '') AS pipeline_backend_repo,
    COALESCE(projects.pipeline_backend_tracked_branch, '') AS pipeline_backend_tracked_branch;

-- name: FindProjectBySlug :one
SELECT
    p.*,
    COALESCE(ds.slug::text, '') AS pipeline_backend_data_source_slug,
    COALESCE(p.pipeline_backend_repo, '') AS pipeline_backend_repo,
    COALESCE(p.pipeline_backend_tracked_branch, '') AS pipeline_backend_tracked_branch
FROM
    projects p
    JOIN organizations o ON p.organization_id = o.id
    LEFT JOIN data_sources ds ON p.pipeline_backend_data_source_id = ds.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug');

-- name: FindProjectBySlugForUpdate :one
SELECT
    p.*,
    COALESCE(ds.slug::text, '') AS pipeline_backend_data_source_slug,
    COALESCE(p.pipeline_backend_repo, '') AS pipeline_backend_repo,
    COALESCE(p.pipeline_backend_tracked_branch, '') AS pipeline_backend_tracked_branch
FROM
    projects p
    JOIN organizations o ON p.organization_id = o.id
    LEFT JOIN data_sources ds ON p.pipeline_backend_data_source_id = ds.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
FOR UPDATE OF p;

-- name: ListProjectsByOrg :many
SELECT
    p.*,
    COALESCE(ds.slug::text, '') AS pipeline_backend_data_source_slug,
    COALESCE(p.pipeline_backend_repo, '') AS pipeline_backend_repo,
    COALESCE(p.pipeline_backend_tracked_branch, '') AS pipeline_backend_tracked_branch
FROM
    projects p
    JOIN organizations o ON p.organization_id = o.id
    LEFT JOIN data_sources ds ON p.pipeline_backend_data_source_id = ds.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (p.created_at, p.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (p.created_at, p.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (p.updated_at, p.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (p.updated_at, p.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'name'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (p.name, p.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (p.name, p.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN p.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN p.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN p.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN p.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'asc' THEN p.name
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'desc' THEN p.name
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN p.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN p.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: UpdateProject :execrows
UPDATE projects p
SET
    name = sqlc.arg ('name'),
    updated_at = NOW()
FROM organizations o
WHERE
    p.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug');

-- name: SetProjectPipelineBackend :execrows
UPDATE projects p
SET
    pipeline_backend_data_source_id = backend_ds.id,
    pipeline_backend_repo = sqlc.arg ('pipeline_backend_repo'),
    pipeline_backend_tracked_branch = sqlc.arg ('pipeline_backend_tracked_branch'),
    pipeline_backend_pipeline_dir = sqlc.arg ('pipeline_backend_pipeline_dir'),
    pipeline_backend_first_commit_sha = sqlc.narg ('pipeline_backend_first_commit_sha'),
    updated_at = NOW()
FROM organizations o
JOIN LATERAL (
    SELECT ds.id
    FROM data_sources ds
    WHERE
        ds.organization_id = o.id
        AND ds.slug = sqlc.arg ('pipeline_backend_data_source_slug')
        AND ds.deleted_at IS NULL
        AND ds.is_active = true
    FOR SHARE
) backend_ds ON TRUE
WHERE
    p.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug');

-- name: RevokeChangedProjectProposalAuthority :exec
UPDATE pipeline_proposal_authorities pa
SET revoked_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
FROM projects p, organizations o
WHERE pa.project_id = p.id AND pa.organization_id = o.id AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND pa.revoked_at IS NULL
    AND (
        sqlc.arg('executing_principal_id')::text <> ''
        OR pa.authority_source = 'github_app_installation'
        OR pa.publication_mode = 'repository_credential'
        OR pa.data_source_id IS DISTINCT FROM p.pipeline_backend_data_source_id
    )
    AND NOT (
        pa.publication_mode = 'repository_credential'
        AND pa.authority_source = 'git_data_source_credential'
        AND EXISTS (
            SELECT 1 FROM data_sources ds JOIN credentials c ON c.id = ds.credential_id AND c.organization_id = ds.organization_id
            WHERE ds.id = pa.data_source_id AND ds.id = p.pipeline_backend_data_source_id
                AND ds.organization_id = o.id AND ds.slug = sqlc.arg('data_source_slug')
                AND ds.type = 'git' AND c.id = pa.credential_id
        )
    )
    AND NOT (
        pa.data_source_id IS NOT DISTINCT FROM p.pipeline_backend_data_source_id
        AND EXISTS (SELECT 1 FROM data_sources ds JOIN credentials c ON c.id = ds.credential_id AND c.organization_id = ds.organization_id
            WHERE ds.id = pa.data_source_id AND ds.organization_id = o.id AND ds.slug = sqlc.arg('data_source_slug')
                AND c.id = pa.credential_id AND c.slug = sqlc.arg('credential_slug'))
        AND pa.publication_mode = 'organization_service' AND pa.provider_host = 'github.com'
        AND pa.provider_account_id = sqlc.arg('provider_account_id')
        AND pa.provider_repository_id = sqlc.arg('provider_repository_id')
        AND pa.provider_credential_id = sqlc.arg('provider_credential_id')
        AND pa.executing_principal_id = sqlc.arg('executing_principal_id')
        AND pa.authority_source = 'github_app_installation'
    );

-- name: BindProjectGitHubProposalAuthority :execrows
INSERT INTO pipeline_proposal_authorities (
    organization_id, project_id, data_source_id, credential_id, publication_mode,
    provider_host, provider_account_id, provider_repository_id, provider_credential_id,
    executing_principal_id, executing_principal_display, authority_source
)
SELECT o.id, p.id, ds.id, c.id, 'organization_service',
    'github.com', installation.account_id::text, repository.repository_id::text, installation.installation_id::text,
    sqlc.arg('executing_principal_id'), sqlc.arg('executing_principal_display'), 'github_app_installation'
FROM organizations o
JOIN projects p ON p.organization_id = o.id
JOIN data_sources ds ON ds.id = p.pipeline_backend_data_source_id AND ds.organization_id = o.id
JOIN credentials c ON c.id = ds.credential_id AND c.organization_id = o.id
JOIN github_app_installations installation ON installation.organization_id = o.id
JOIN github_app_repositories repository ON repository.organization_id = o.id AND repository.installation_id = installation.id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND ds.slug = sqlc.arg('data_source_slug') AND c.slug = sqlc.arg('credential_slug')
    AND ds.type = 'github' AND ds.is_active AND NOT ds.read_only AND ds.deleted_at IS NULL
    AND c.status = 'active' AND c.revoked_at IS NULL AND c.kind = 'static_secret'
    AND installation.suspended_at IS NULL AND installation.deleted_at IS NULL AND repository.removed_at IS NULL
    AND installation.account_id::text = sqlc.arg('provider_account_id')
    AND installation.installation_id::text = sqlc.arg('provider_credential_id')
    AND repository.repository_id::text = sqlc.arg('provider_repository_id')
    AND repository.repository_id::text = ds.config #>> '{repository,id}'
    AND p.pipeline_backend_repo = 'github.com/' || repository.full_name
ON CONFLICT (organization_id, project_id) WHERE revoked_at IS NULL
DO UPDATE SET executing_principal_display = EXCLUDED.executing_principal_display, updated_at = CURRENT_TIMESTAMP
WHERE pipeline_proposal_authorities.data_source_id = EXCLUDED.data_source_id
    AND pipeline_proposal_authorities.credential_id = EXCLUDED.credential_id
    AND pipeline_proposal_authorities.publication_mode = EXCLUDED.publication_mode
    AND pipeline_proposal_authorities.provider_host = EXCLUDED.provider_host
    AND pipeline_proposal_authorities.provider_account_id = EXCLUDED.provider_account_id
    AND pipeline_proposal_authorities.provider_repository_id = EXCLUDED.provider_repository_id
    AND pipeline_proposal_authorities.provider_credential_id = EXCLUDED.provider_credential_id
    AND pipeline_proposal_authorities.executing_principal_id = EXCLUDED.executing_principal_id
    AND pipeline_proposal_authorities.authority_source = EXCLUDED.authority_source;

-- name: BindProjectGitBranchProposalAuthority :execrows
INSERT INTO pipeline_proposal_authorities (
    organization_id, project_id, data_source_id, credential_id, publication_mode,
    provider_host, provider_account_id, provider_repository_id, provider_credential_id,
    executing_principal_id, executing_principal_display, authority_source
)
SELECT o.id, p.id, ds.id, c.id, 'repository_credential',
    '', '', '', '', '', 'Configured Git connection', 'git_data_source_credential'
FROM organizations o
JOIN projects p ON p.organization_id = o.id
JOIN data_sources ds ON ds.id = p.pipeline_backend_data_source_id AND ds.organization_id = o.id
JOIN credentials c ON c.id = ds.credential_id AND c.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND ds.slug = sqlc.arg('data_source_slug') AND ds.type = 'git'
    AND ds.is_active AND NOT ds.read_only AND ds.deleted_at IS NULL
    AND c.status = 'active' AND c.revoked_at IS NULL AND c.kind = 'static_secret'
ON CONFLICT (organization_id, project_id) WHERE revoked_at IS NULL
DO UPDATE SET executing_principal_display = EXCLUDED.executing_principal_display
WHERE pipeline_proposal_authorities.data_source_id = EXCLUDED.data_source_id
    AND pipeline_proposal_authorities.credential_id = EXCLUDED.credential_id
    AND pipeline_proposal_authorities.publication_mode = EXCLUDED.publication_mode
    AND pipeline_proposal_authorities.authority_source = EXCLUDED.authority_source;

-- name: ClearProjectPipelineBackend :execrows
UPDATE projects p
SET
    pipeline_backend_data_source_id = NULL,
    pipeline_backend_repo = NULL,
    pipeline_backend_tracked_branch = NULL,
    pipeline_backend_pipeline_dir = NULL,
    pipeline_backend_first_commit_sha = NULL,
    updated_at = NOW()
FROM organizations o
WHERE
    p.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug');

-- name: DeleteProject :execresult
DELETE FROM projects p
USING organizations o
WHERE
    p.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug');

-- =====================
-- Environments
-- =====================

-- name: CreateEnvironment :one
INSERT INTO
    environments (
        organization_id,
        slug,
        name,
        description,
        color,
        protected
    )
SELECT
    o.id,
    sqlc.arg ('environment_slug'),
    sqlc.arg ('name'),
    sqlc.narg ('description'),
    sqlc.arg ('color'),
    sqlc.arg ('protected')
FROM organizations o
WHERE
    o.slug = sqlc.arg ('org_slug')
RETURNING
    *;

-- name: FindEnvironmentBySlug :one
SELECT e.*
FROM
    environments e
    JOIN organizations o ON e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL;

-- name: FindAnyEnvironmentBySlug :one
SELECT e.*
FROM
    environments e
    JOIN organizations o ON e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug');

-- name: ListEnvironmentsByOrg :many
SELECT e.*
FROM
    environments e
    JOIN organizations o ON e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND (
        sqlc.arg ('include_deleted')::boolean = true
        OR e.deleted_at IS NULL
    )
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (e.created_at, e.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (e.created_at, e.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (e.updated_at, e.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (e.updated_at, e.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'name'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (e.name, e.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (e.name, e.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN e.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN e.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN e.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN e.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'asc' THEN e.name
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'desc' THEN e.name
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN e.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN e.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: UpdateEnvironment :one
UPDATE environments e
SET
    name = sqlc.arg ('name'),
    description = sqlc.narg ('description'),
    color = sqlc.arg ('color'),
    protected = sqlc.arg ('protected'),
    updated_at = NOW()
FROM organizations o
WHERE
    e.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL
RETURNING
    e.*;

-- name: DeleteEnvironment :execrows
UPDATE environments e
SET
    deleted_at = NOW(),
    delete_reason = sqlc.narg ('delete_reason'),
    updated_at = NOW()
FROM organizations o
WHERE
    e.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL;

-- name: ClearProjectEnvironmentDefault :execrows
UPDATE project_environments pe
SET
    is_default = false,
    updated_at = NOW()
FROM
    organizations o
    JOIN projects p ON p.organization_id = o.id
WHERE
    pe.project_id = p.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND pe.is_default = true
    AND pe.deleted_at IS NULL;

-- name: EnableProjectEnvironment :one
WITH upserted AS (
    INSERT INTO
        project_environments (
            organization_id,
            project_id,
            environment_id,
            is_default
        )
    SELECT
        p.organization_id,
        p.id,
        e.id,
        sqlc.arg ('is_default')
    FROM
        organizations o
        JOIN projects p ON p.organization_id = o.id
        JOIN environments e ON e.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND p.slug = sqlc.arg ('project_slug')
        AND e.slug = sqlc.arg ('environment_slug')
        AND e.deleted_at IS NULL
    ON CONFLICT (project_id, environment_id)
        DO UPDATE SET
            is_default = EXCLUDED.is_default,
            deleted_at = NULL,
            delete_reason = NULL,
            enabled_at = NOW(),
            updated_at = NOW()
    RETURNING
        *
)
SELECT
    upserted.*,
    e.slug AS environment_slug,
    e.name AS environment_name,
    e.description AS environment_description,
    e.color AS environment_color,
    e.protected AS environment_protected,
    e.deleted_at AS environment_deleted_at,
    e.created_at AS environment_created_at,
    e.updated_at AS environment_updated_at
FROM
    upserted
    JOIN environments e ON e.id = upserted.environment_id;

-- name: ListProjectEnvironments :many
SELECT
    pe.*,
    e.slug AS environment_slug,
    e.name AS environment_name,
    e.description AS environment_description,
    e.color AS environment_color,
    e.protected AS environment_protected,
    e.deleted_at AS environment_deleted_at,
    e.created_at AS environment_created_at,
    e.updated_at AS environment_updated_at
FROM
    project_environments pe
    JOIN projects p ON pe.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN environments e ON pe.environment_id = e.id
        AND e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (e.created_at, e.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (e.created_at, e.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (e.updated_at, e.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (e.updated_at, e.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'name'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (e.name, e.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (e.name, e.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN e.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN e.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN e.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN e.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'asc' THEN e.name
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'desc' THEN e.name
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN e.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN e.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: FindProjectEnvironmentProtection :one
SELECT
    e.slug AS environment_slug,
    e.protected
FROM
    project_environments pe
    JOIN projects p ON pe.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN environments e ON pe.environment_id = e.id
        AND e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL;

-- name: FindProjectEnvironmentCatalogProtection :one
SELECT
    e.slug AS environment_slug,
    e.protected
FROM
    organizations o
    JOIN projects p ON p.organization_id = o.id
    JOIN environments e ON e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL;

-- name: DisableProjectEnvironment :execrows
UPDATE project_environments pe
SET
    is_default = false,
    deleted_at = NOW(),
    delete_reason = NULL,
    updated_at = NOW()
FROM
    organizations o,
    projects p,
    environments e
WHERE
    pe.project_id = p.id
    AND pe.environment_id = e.id
    AND p.organization_id = o.id
    AND e.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND pe.deleted_at IS NULL;

-- name: UpsertRuntimeVariable :one
WITH target_environment AS (
    SELECT
        o.id AS organization_id,
        e.id AS environment_id,
        e.slug AS environment_slug
    FROM
        organizations o
        JOIN environments e ON e.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND e.slug = sqlc.arg ('environment_slug')
        AND e.deleted_at IS NULL
), upserted AS (
    INSERT INTO
        environment_runtime_variables (
            organization_id,
            environment_id,
            key,
            value_type,
            value_text,
            description
        )
    SELECT
        target_environment.organization_id,
        target_environment.environment_id,
        sqlc.arg ('variable_key'),
        sqlc.arg ('value_type'),
        sqlc.arg ('value_text'),
        sqlc.narg ('description')
    FROM target_environment
    ON CONFLICT (environment_id, key)
        DO UPDATE SET
            value_type = EXCLUDED.value_type,
            value_text = EXCLUDED.value_text,
            description = EXCLUDED.description,
            updated_at = NOW()
    RETURNING
        *
)
SELECT
    upserted.*,
    target_environment.environment_slug
FROM
    upserted
    JOIN target_environment
        ON target_environment.environment_id = upserted.environment_id;

-- name: FindRuntimeVariable :one
SELECT
    rv.*,
    e.slug AS environment_slug
FROM
    environment_runtime_variables rv
    JOIN organizations o ON o.id = rv.organization_id
    JOIN environments e ON e.id = rv.environment_id
        AND e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND rv.key = sqlc.arg ('variable_key')
    AND e.deleted_at IS NULL;

-- name: ListRuntimeVariables :many
SELECT
    rv.*,
    e.slug AS environment_slug
FROM
    environment_runtime_variables rv
    JOIN organizations o ON o.id = rv.organization_id
    JOIN environments e ON e.id = rv.environment_id
        AND e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL
    AND (
        sqlc.narg ('cursor_key')::text IS NULL
        OR (
            (
                sqlc.arg ('order_dir') = 'asc'
                AND (rv.updated_at, rv.key) > (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_key')::text
                )
            )
            OR (
                sqlc.arg ('order_dir') = 'desc'
                AND (rv.updated_at, rv.key) < (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_key')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN rv.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN rv.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN rv.key
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN rv.key
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: ListAllRuntimeVariables :many
SELECT
    rv.*,
    e.slug AS environment_slug
FROM
    environment_runtime_variables rv
    JOIN organizations o ON o.id = rv.organization_id
    JOIN environments e ON e.id = rv.environment_id
        AND e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL
ORDER BY
    rv.key ASC;

-- name: DeleteRuntimeVariable :execrows
DELETE FROM environment_runtime_variables rv
USING
    organizations o,
    environments e
WHERE
    rv.organization_id = o.id
    AND rv.environment_id = e.id
    AND e.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND rv.key = sqlc.arg ('variable_key')
    AND e.deleted_at IS NULL;

-- name: UpsertEnvironmentSecret :one
WITH target_environment AS (
    SELECT
        o.id AS organization_id,
        e.id AS environment_id,
        e.slug AS environment_slug
    FROM
        organizations o
        JOIN environments e ON e.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND e.slug = sqlc.arg ('environment_slug')
        AND e.deleted_at IS NULL
), target_credential AS (
    SELECT
        c.id AS credential_id,
        c.slug AS credential_slug
    FROM
        credentials c
        JOIN target_environment ON target_environment.organization_id = c.organization_id
    WHERE
        c.slug = sqlc.arg ('credential_slug')
        AND c.kind = 'static_secret'
        AND c.credential_type = 'environment_secret'
        AND c.status = 'active'
), upserted AS (
    INSERT INTO
        environment_secrets (
            organization_id,
            environment_id,
            credential_id,
            key,
            description
        )
    SELECT
        target_environment.organization_id,
        target_environment.environment_id,
        target_credential.credential_id,
        sqlc.arg ('environment_secret_key'),
        sqlc.narg ('description')
    FROM
        target_environment,
        target_credential
    ON CONFLICT (environment_id, key)
        DO UPDATE SET
            credential_id = EXCLUDED.credential_id,
            description = EXCLUDED.description,
            updated_at = NOW()
    RETURNING
        *
)
SELECT
    upserted.*,
    target_environment.environment_slug,
    target_credential.credential_slug
FROM
    upserted
    JOIN target_environment
        ON target_environment.environment_id = upserted.environment_id
    JOIN target_credential
        ON target_credential.credential_id = upserted.credential_id;

-- name: FindEnvironmentSecret :one
SELECT
    es.*,
    e.slug AS environment_slug,
    c.slug AS credential_slug
FROM
    environment_secrets es
    JOIN environments e ON e.id = es.environment_id
    JOIN organizations o ON o.id = e.organization_id
    JOIN credentials c ON c.id = es.credential_id
        AND c.organization_id = e.organization_id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND es.key = sqlc.arg ('environment_secret_key')
    AND e.deleted_at IS NULL;

-- name: ListEnvironmentSecrets :many
SELECT
    es.*,
    e.slug AS environment_slug,
    c.slug AS credential_slug
FROM
    environment_secrets es
    JOIN environments e ON e.id = es.environment_id
    JOIN organizations o ON o.id = e.organization_id
    JOIN credentials c ON c.id = es.credential_id
        AND c.organization_id = e.organization_id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL
    AND (
        sqlc.narg ('cursor_key')::text IS NULL
        OR (
            (
                sqlc.arg ('order_dir') = 'asc'
                AND (es.updated_at, es.key) > (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_key')::text
                )
            )
            OR (
                sqlc.arg ('order_dir') = 'desc'
                AND (es.updated_at, es.key) < (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_key')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN es.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN es.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN es.key
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN es.key
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: ListAllEnvironmentSecrets :many
SELECT
    es.*,
    e.slug AS environment_slug,
    c.slug AS credential_slug
FROM
    environment_secrets es
    JOIN environments e ON e.id = es.environment_id
    JOIN organizations o ON o.id = e.organization_id
    JOIN credentials c ON c.id = es.credential_id
        AND c.organization_id = e.organization_id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL
ORDER BY
    es.key ASC;

-- name: DeleteEnvironmentSecret :one
WITH target_environment AS (
    SELECT
        o.id AS organization_id,
        e.id AS environment_id
    FROM
        organizations o
        JOIN environments e ON e.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND e.slug = sqlc.arg ('environment_slug')
        AND e.deleted_at IS NULL
), deleted AS (
    DELETE FROM environment_secrets es
    USING target_environment
    WHERE
        es.environment_id = target_environment.environment_id
        AND es.key = sqlc.arg ('environment_secret_key')
    RETURNING es.credential_id
)
SELECT
    (SELECT count(*) FROM deleted)::bigint AS deleted_count;

-- name: DeleteEnvironmentSecretsForEnvironment :one
WITH target_environment AS (
    SELECT
        o.id AS organization_id,
        e.id AS environment_id
    FROM
        organizations o
        JOIN environments e ON e.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND e.slug = sqlc.arg ('environment_slug')
        AND e.deleted_at IS NULL
), deleted AS (
    DELETE FROM environment_secrets es
    USING target_environment
    WHERE
        es.environment_id = target_environment.environment_id
    RETURNING es.credential_id
)
SELECT
    (SELECT count(*) FROM deleted)::bigint AS deleted_count;

-- =====================
-- Data Source Refs and Bindings
-- =====================

-- name: CreateDataSourceRef :one
WITH inserted AS (
    INSERT INTO
        data_source_refs (
            pipeline_spec_id,
            slug,
            name,
            data_source_type,
            required_capabilities
        )
    SELECT
        ps.id,
        sqlc.arg ('data_source_ref_slug'),
        sqlc.arg ('name'),
        sqlc.arg ('data_source_type'),
        sqlc.arg ('required_capabilities')::text []
    FROM
        organizations o
        JOIN projects p ON p.organization_id = o.id
        JOIN pipeline_specs ps ON ps.project_id = p.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND p.slug = sqlc.arg ('project_slug')
        AND ps.slug = sqlc.arg ('pipeline_slug')
        AND ps.deleted_at IS NULL
    RETURNING
        *
)
SELECT
    inserted.*,
    ps.slug AS pipeline_slug
FROM
    inserted
    JOIN pipeline_specs ps ON ps.id = inserted.pipeline_spec_id;

-- name: FindDataSourceRefBySlug :one
SELECT
    dsr.*,
    ps.slug AS pipeline_slug
FROM
    data_source_refs dsr
    JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id
    JOIN projects p ON ps.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND ps.slug = sqlc.arg ('pipeline_slug')
    AND ps.deleted_at IS NULL
    AND dsr.slug = sqlc.arg ('data_source_ref_slug');

-- name: ListDataSourceRefs :many
SELECT
    dsr.*,
    ps.slug AS pipeline_slug
FROM
    data_source_refs dsr
    JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id
    JOIN projects p ON ps.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND ps.deleted_at IS NULL
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('order_dir') = 'asc'
                AND (dsr.created_at, ps.slug || '/' || dsr.slug) > (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg ('order_dir') = 'desc'
                AND (dsr.created_at, ps.slug || '/' || dsr.slug) < (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN dsr.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN dsr.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN ps.slug || '/' || dsr.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN ps.slug || '/' || dsr.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: ListDataSourceRefsByPipeline :many
SELECT
    dsr.*,
    ps.slug AS pipeline_slug
FROM
    data_source_refs dsr
    JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id
    JOIN projects p ON ps.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND ps.slug = sqlc.arg ('pipeline_slug')
    AND ps.deleted_at IS NULL
ORDER BY
    dsr.created_at ASC,
    dsr.slug ASC;

-- name: UpdateDataSourceRef :one
UPDATE data_source_refs dsr
SET
    name = sqlc.arg ('name'),
    data_source_type = sqlc.arg ('data_source_type'),
    required_capabilities = sqlc.arg ('required_capabilities')::text [],
    updated_at = NOW()
FROM
    organizations o,
    projects p,
    pipeline_specs ps
WHERE
    dsr.pipeline_spec_id = ps.id
    AND ps.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND ps.slug = sqlc.arg ('pipeline_slug')
    AND ps.deleted_at IS NULL
    AND dsr.slug = sqlc.arg ('data_source_ref_slug')
RETURNING
    dsr.*,
    ps.slug AS pipeline_slug;

-- name: DeleteDataSourceRef :execrows
DELETE FROM data_source_refs dsr
USING
    organizations o,
    projects p,
    pipeline_specs ps
WHERE
    dsr.pipeline_spec_id = ps.id
    AND ps.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND ps.slug = sqlc.arg ('pipeline_slug')
    AND ps.deleted_at IS NULL
    AND dsr.slug = sqlc.arg ('data_source_ref_slug');

-- name: DeleteDataSourceRefsForPipeline :execrows
DELETE FROM data_source_refs dsr
USING
    organizations o,
    projects p,
    pipeline_specs ps
WHERE
    dsr.pipeline_spec_id = ps.id
    AND ps.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND ps.slug = sqlc.arg ('pipeline_slug')
    AND ps.deleted_at IS NULL;

-- name: ListProtectedBindingEnvironmentsForDataSourceRef :many
SELECT
    e.slug AS environment_slug,
    e.protected
FROM
    data_source_refs dsr
    JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id
    JOIN projects p ON ps.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN data_source_bindings dsb ON dsb.data_source_ref_id = dsr.id
    JOIN project_environments pe ON pe.id = dsb.project_environment_id
        AND pe.project_id = p.id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND ps.slug = sqlc.arg ('pipeline_slug')
    AND ps.deleted_at IS NULL
    AND dsr.slug = sqlc.arg ('data_source_ref_slug')
    AND e.protected
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
ORDER BY
    e.slug ASC;

-- name: ListDataSourceBindingsForDataSourceRef :many
SELECT
    dsb.*,
    ps.slug AS pipeline_slug,
    dsr.slug AS data_source_ref_slug,
    dsr.name AS data_source_ref_name,
    dsr.required_capabilities AS ref_required_capabilities,
    e.slug AS environment_slug,
    ds.slug AS data_source_slug,
    ds.name AS data_source_name,
    ds.type AS data_source_type,
    ds.is_active AS data_source_active,
    ds.read_only AS data_source_read_only
FROM
    data_source_bindings dsb
    JOIN project_environments pe ON pe.id = dsb.project_environment_id
    JOIN data_source_refs dsr ON dsr.id = dsb.data_source_ref_id
    JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id
    JOIN projects p ON ps.project_id = p.id
        AND pe.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    JOIN data_sources ds ON ds.id = dsb.data_source_id
        AND ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND ps.slug = sqlc.arg ('pipeline_slug')
    AND ps.deleted_at IS NULL
    AND dsr.slug = sqlc.arg ('data_source_ref_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND ds.deleted_at IS NULL
ORDER BY
    e.slug ASC;

-- name: ProjectEnvironmentEnabled :one
SELECT EXISTS (
    SELECT 1
    FROM
        project_environments pe
        JOIN projects p ON pe.project_id = p.id
        JOIN organizations o ON p.organization_id = o.id
        JOIN environments e ON pe.environment_id = e.id
            AND e.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND p.slug = sqlc.arg ('project_slug')
        AND e.slug = sqlc.arg ('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
);

-- name: UpsertDataSourceBinding :one
WITH upserted AS (
    INSERT INTO
        data_source_bindings (
            organization_id,
            project_id,
            pipeline_spec_id,
            project_environment_id,
            data_source_ref_id,
            data_source_id
        )
    SELECT
        o.id,
        p.id,
        ps.id,
        pe.id,
        dsr.id,
        ds.id
    FROM
        organizations o
        JOIN projects p ON p.organization_id = o.id
        JOIN environments e ON e.organization_id = o.id
        JOIN project_environments pe ON pe.project_id = p.id
            AND pe.environment_id = e.id
        JOIN pipeline_specs ps ON ps.project_id = p.id
        JOIN data_source_refs dsr ON dsr.pipeline_spec_id = ps.id
        JOIN data_sources ds ON ds.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND p.slug = sqlc.arg ('project_slug')
        AND e.slug = sqlc.arg ('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
        AND ps.slug = sqlc.arg ('pipeline_slug')
        AND ps.deleted_at IS NULL
        AND dsr.slug = sqlc.arg ('data_source_ref_slug')
        AND ds.slug = sqlc.arg ('data_source_slug')
        AND ds.deleted_at IS NULL
    ON CONFLICT (project_environment_id, data_source_ref_id)
        DO UPDATE SET
            data_source_id = EXCLUDED.data_source_id,
            updated_at = NOW()
    RETURNING
        *
)
SELECT
    upserted.*,
    ps.slug AS pipeline_slug,
    dsr.slug AS data_source_ref_slug,
    dsr.name AS data_source_ref_name,
    dsr.data_source_type AS ref_data_source_type,
    dsr.required_capabilities AS ref_required_capabilities,
    e.slug AS environment_slug,
    ds.slug AS data_source_slug,
    ds.name AS data_source_name,
    ds.type AS data_source_type,
    ds.is_active AS data_source_active,
    ds.read_only AS data_source_read_only
FROM
    upserted
    JOIN data_source_refs dsr ON dsr.id = upserted.data_source_ref_id
    JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id
    JOIN project_environments pe ON pe.id = upserted.project_environment_id
    JOIN environments e ON e.id = pe.environment_id
    JOIN data_sources ds ON ds.id = upserted.data_source_id;

-- name: FindDataSourceBinding :one
SELECT
    dsb.*,
    ps.slug AS pipeline_slug,
    dsr.slug AS data_source_ref_slug,
    dsr.name AS data_source_ref_name,
    dsr.data_source_type AS ref_data_source_type,
    dsr.required_capabilities AS ref_required_capabilities,
    e.slug AS environment_slug,
    ds.slug AS data_source_slug,
    ds.name AS data_source_name,
    ds.type AS data_source_type,
    ds.is_active AS data_source_active,
    ds.read_only AS data_source_read_only
FROM
    data_source_bindings dsb
    JOIN project_environments pe ON pe.id = dsb.project_environment_id
    JOIN data_source_refs dsr ON dsr.id = dsb.data_source_ref_id
    JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id
    JOIN projects p ON ps.project_id = p.id
        AND pe.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    JOIN data_sources ds ON ds.id = dsb.data_source_id
        AND ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND ps.slug = sqlc.arg ('pipeline_slug')
    AND ps.deleted_at IS NULL
    AND dsr.slug = sqlc.arg ('data_source_ref_slug')
    AND ds.deleted_at IS NULL;

-- name: ListDataSourceBindings :many
SELECT
    dsb.*,
    ps.slug AS pipeline_slug,
    dsr.slug AS data_source_ref_slug,
    dsr.name AS data_source_ref_name,
    dsr.data_source_type AS ref_data_source_type,
    dsr.required_capabilities AS ref_required_capabilities,
    e.slug AS environment_slug,
    ds.slug AS data_source_slug,
    ds.name AS data_source_name,
    ds.type AS data_source_type,
    ds.is_active AS data_source_active,
    ds.read_only AS data_source_read_only
FROM
    data_source_bindings dsb
    JOIN project_environments pe ON pe.id = dsb.project_environment_id
    JOIN data_source_refs dsr ON dsr.id = dsb.data_source_ref_id
    JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id
    JOIN projects p ON ps.project_id = p.id
        AND pe.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    JOIN data_sources ds ON ds.id = dsb.data_source_id
        AND ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND ps.deleted_at IS NULL
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND ds.deleted_at IS NULL
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('order_dir') = 'asc'
                AND (dsb.updated_at, ps.slug || '/' || dsr.slug) > (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg ('order_dir') = 'desc'
                AND (dsb.updated_at, ps.slug || '/' || dsr.slug) < (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN dsb.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN dsb.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN ps.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN ps.slug
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN dsr.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN dsr.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: DeleteDataSourceBinding :execrows
DELETE FROM data_source_bindings dsb
USING
    organizations o,
    projects p,
    pipeline_specs ps,
    project_environments pe,
    environments e,
    data_source_refs dsr
WHERE
    dsb.project_environment_id = pe.id
    AND dsb.data_source_ref_id = dsr.id
    AND dsr.pipeline_spec_id = ps.id
    AND ps.project_id = p.id
    AND pe.project_id = p.id
    AND p.organization_id = o.id
    AND pe.environment_id = e.id
    AND e.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND pe.deleted_at IS NULL
    AND ps.slug = sqlc.arg ('pipeline_slug')
    AND ps.deleted_at IS NULL
    AND dsr.slug = sqlc.arg ('data_source_ref_slug');

-- =====================
-- Organization Members
-- =====================

-- name: CreateOrganizationMember :one
INSERT INTO
    organization_members (
        organization_id,
        user_id,
        role_id
    )
VALUES (
        (
            SELECT id
            FROM organizations
            WHERE
                slug = sqlc.arg ('org_slug')
        ),
        (
            SELECT id
            FROM users
            WHERE
                slug = sqlc.arg ('user_slug')
                AND deleted_at IS NULL
            FOR SHARE
        ),
        (
            SELECT id
            FROM roles
            WHERE
                slug = sqlc.arg ('role_slug')
                AND deleted_at IS NULL
                AND organization_id = (
                    SELECT id
                    FROM organizations
                    WHERE
                        slug = sqlc.arg ('org_slug')
                )
        )
    )
RETURNING
    *,
    (SELECT email FROM users WHERE id = organization_members.user_id) AS email;

-- name: FindOrganizationMember :one
SELECT om.*, u.slug, u.email, r.slug AS role_slug
FROM
    organization_members om
    JOIN users u ON om.user_id = u.id
    JOIN organizations o ON om.organization_id = o.id
    JOIN roles r ON om.role_id = r.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND u.slug = sqlc.arg ('user_slug');

-- name: FindOrganizationMemberByEmail :one
SELECT om.*, u.slug, u.email, r.slug AS role_slug
FROM
    organization_members om
    JOIN users u ON om.user_id = u.id
    JOIN organizations o ON om.organization_id = o.id
    JOIN roles r ON om.role_id = r.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND lower(u.email) = sqlc.arg ('email')
ORDER BY
    u.created_at ASC,
    u.slug ASC
LIMIT 1;

-- name: ListOrganizationMembers :many
SELECT om.*, u.slug, u.email, r.slug AS role_slug
FROM
    organization_members om
    JOIN users u ON om.user_id = u.id
    JOIN organizations o ON om.organization_id = o.id
    JOIN roles r ON om.role_id = r.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (om.created_at, u.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (om.created_at, u.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (om.updated_at, u.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (om.updated_at, u.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'email'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (u.email, u.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (u.email, u.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN om.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN om.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN om.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN om.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'email'
        AND sqlc.arg ('order_dir') = 'asc' THEN u.email
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'email'
        AND sqlc.arg ('order_dir') = 'desc' THEN u.email
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN u.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN u.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: ListUserOrganizations :many
SELECT
    om.*,
    o.name,
    o.slug,
    o.settings,
    o.created_at AS org_created_at,
    o.updated_at AS org_updated_at,
    r.slug AS role_slug
FROM
    organization_members om
    JOIN organizations o ON om.organization_id = o.id
    JOIN users u ON om.user_id = u.id
    JOIN roles r ON om.role_id = r.id
WHERE
    u.slug = sqlc.arg ('slug')
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (o.created_at, o.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (o.created_at, o.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (o.updated_at, o.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (o.updated_at, o.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'name'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (o.name, o.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (o.name, o.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN o.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN o.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN o.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN o.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'asc' THEN o.name
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'desc' THEN o.name
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN o.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN o.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: UpdateOrganizationMember :exec
UPDATE organization_members
SET
    role_id = (
        SELECT id
        FROM roles
        WHERE
            slug = sqlc.arg ('role_slug')
            AND deleted_at IS NULL
            AND organization_id = (
                SELECT id
                FROM organizations
                WHERE
                    slug = sqlc.arg ('org_slug')
            )
    ),
    updated_at = NOW()
WHERE
    organization_id = (
        SELECT id
        FROM organizations
        WHERE
            slug = sqlc.arg ('org_slug')
    )
    AND user_id = (
        SELECT id
        FROM users
        WHERE
            slug = sqlc.arg ('user_slug')
    );

-- name: RemoveOrganizationMember :exec
DELETE FROM organization_members
WHERE
    organization_id = (
        SELECT id
        FROM organizations
        WHERE
            slug = sqlc.arg ('org_slug')
    )
    AND user_id = (
        SELECT id
        FROM users
        WHERE
            slug = sqlc.arg ('user_slug')
    );

-- name: CountOrganizationMembers :one
SELECT COUNT(*)
FROM
    organization_members om
    JOIN organizations o ON om.organization_id = o.id
    JOIN users u ON om.user_id = u.id AND u.deleted_at IS NULL
WHERE
    o.slug = sqlc.arg ('org_slug');

-- name: CountOrganizationMembersByRole :one
SELECT COUNT(*)
FROM
    organization_members om
    JOIN organizations o ON om.organization_id = o.id
    JOIN users u ON om.user_id = u.id AND u.deleted_at IS NULL
    JOIN roles r ON om.role_id = r.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND r.slug = sqlc.arg ('role_slug');

-- =====================
-- Organization Member Invitations
-- =====================

-- name: CreateOrganizationMemberInvitation :one
WITH inserted AS (
    INSERT INTO
        organization_member_invitations (
            organization_id,
            slug,
            email,
            role_id,
            invited_by_user_id,
            token_hash,
            expires_at
        )
    VALUES (
            (
                SELECT id
                FROM organizations
                WHERE
                    slug = sqlc.arg ('org_slug')
            ),
            sqlc.arg ('invitation_slug'),
            sqlc.arg ('email'),
            (
                SELECT id
                FROM roles
                WHERE
                    slug = sqlc.arg ('role_slug')
                    AND deleted_at IS NULL
                    AND organization_id = (
                        SELECT id
                        FROM organizations
                        WHERE
                            slug = sqlc.arg ('org_slug')
                    )
            ),
            (
                SELECT id
                FROM users
                WHERE
                    slug = sqlc.narg ('invited_by_user_slug')
            ),
            sqlc.arg ('token_hash'),
            sqlc.arg ('expires_at')
        )
    RETURNING
        *
)
SELECT
    inserted.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    inserted
    JOIN organizations o ON inserted.organization_id = o.id
    JOIN roles r ON inserted.role_id = r.id
    LEFT JOIN users invited_by ON inserted.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON inserted.accepted_by_user_id = accepted_by.id;

-- name: FindOrganizationMemberInvitation :one
SELECT
    omi.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    organization_member_invitations omi
    JOIN organizations o ON omi.organization_id = o.id
    JOIN roles r ON omi.role_id = r.id
    LEFT JOIN users invited_by ON omi.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON omi.accepted_by_user_id = accepted_by.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND omi.slug = sqlc.arg ('invitation_slug');

-- name: FindOrganizationMemberInvitationByTokenHash :one
SELECT
    omi.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    organization_member_invitations omi
    JOIN organizations o ON omi.organization_id = o.id
    JOIN roles r ON omi.role_id = r.id
    LEFT JOIN users invited_by ON omi.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON omi.accepted_by_user_id = accepted_by.id
WHERE
    omi.token_hash = sqlc.arg ('token_hash');

-- name: FindPendingOrganizationMemberInvitationByEmail :one
SELECT
    omi.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    organization_member_invitations omi
    JOIN organizations o ON omi.organization_id = o.id
    JOIN roles r ON omi.role_id = r.id
    LEFT JOIN users invited_by ON omi.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON omi.accepted_by_user_id = accepted_by.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND omi.email = sqlc.arg ('email')
    AND omi.status = 'pending';

-- name: ListOrganizationMemberInvitations :many
SELECT
    omi.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    organization_member_invitations omi
    JOIN organizations o ON omi.organization_id = o.id
    JOIN roles r ON omi.role_id = r.id
    LEFT JOIN users invited_by ON omi.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON omi.accepted_by_user_id = accepted_by.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND (
        sqlc.narg ('status')::text IS NULL
        OR omi.status = sqlc.narg ('status')::text
    )
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (omi.created_at, omi.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (omi.created_at, omi.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (omi.updated_at, omi.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (omi.updated_at, omi.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'email'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (omi.email, omi.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (omi.email, omi.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'expires_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (omi.expires_at, omi.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (omi.expires_at, omi.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN omi.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN omi.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN omi.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN omi.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'email'
        AND sqlc.arg ('order_dir') = 'asc' THEN omi.email
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'email'
        AND sqlc.arg ('order_dir') = 'desc' THEN omi.email
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'expires_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN omi.expires_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'expires_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN omi.expires_at
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN omi.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN omi.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: ExpirePendingOrganizationMemberInvitations :many
WITH expired AS (
    UPDATE organization_member_invitations omi
    SET
        status = 'expired',
        expired_at = sqlc.arg ('expired_at'),
        updated_at = sqlc.arg ('expired_at')
    WHERE
        omi.organization_id = (
            SELECT id
            FROM organizations
            WHERE
                slug = sqlc.arg ('org_slug')
        )
        AND omi.status = 'pending'
        AND omi.expires_at <= sqlc.arg ('expired_at')
    RETURNING omi.*
)
SELECT
    expired.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    expired
    JOIN organizations o ON expired.organization_id = o.id
    JOIN roles r ON expired.role_id = r.id
    LEFT JOIN users invited_by ON expired.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON expired.accepted_by_user_id = accepted_by.id
ORDER BY
    expired.updated_at ASC,
    expired.slug ASC;

-- name: ListPendingOrganizationMemberInvitationsByEmail :many
SELECT
    omi.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    organization_member_invitations omi
    JOIN organizations o ON omi.organization_id = o.id
    JOIN roles r ON omi.role_id = r.id
    LEFT JOIN users invited_by ON omi.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON omi.accepted_by_user_id = accepted_by.id
WHERE
    omi.email = sqlc.arg ('email')
    AND omi.status = 'pending'
    AND omi.expires_at > sqlc.arg ('as_of')
ORDER BY
    omi.created_at ASC,
    omi.slug ASC;

-- name: RefreshOrganizationMemberInvitationToken :one
WITH refreshed AS (
    UPDATE organization_member_invitations omi
    SET
        token_hash = sqlc.arg ('token_hash'),
        expires_at = sqlc.arg ('expires_at'),
        last_sent_at = sqlc.arg ('last_sent_at'),
        updated_at = sqlc.arg ('last_sent_at')
    WHERE
        omi.organization_id = (
            SELECT id
            FROM organizations
            WHERE
                slug = sqlc.arg ('org_slug')
        )
        AND omi.slug = sqlc.arg ('invitation_slug')
        AND omi.status = 'pending'
    RETURNING
        *
)
SELECT
    refreshed.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    refreshed
    JOIN organizations o ON refreshed.organization_id = o.id
    JOIN roles r ON refreshed.role_id = r.id
    LEFT JOIN users invited_by ON refreshed.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON refreshed.accepted_by_user_id = accepted_by.id;

-- name: AcceptOrganizationMemberInvitation :one
WITH accepted AS (
    UPDATE organization_member_invitations omi
    SET
        status = 'accepted',
        accepted_by_user_id = (
            SELECT id
            FROM users
            WHERE
                slug = sqlc.arg ('accepted_user_slug')
        ),
        accepted_member_id = (
            SELECT om.id
            FROM
                organization_members om
                JOIN users u ON om.user_id = u.id
            WHERE
                om.organization_id = omi.organization_id
                AND u.slug = sqlc.arg ('accepted_user_slug')
        ),
        accepted_at = sqlc.arg ('accepted_at'),
        updated_at = sqlc.arg ('accepted_at')
    WHERE
        omi.organization_id = (
            SELECT id
            FROM organizations
            WHERE
                slug = sqlc.arg ('org_slug')
        )
        AND omi.slug = sqlc.arg ('invitation_slug')
        AND omi.status = 'pending'
    RETURNING
        *
)
SELECT
    accepted.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    accepted
    JOIN organizations o ON accepted.organization_id = o.id
    JOIN roles r ON accepted.role_id = r.id
    LEFT JOIN users invited_by ON accepted.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON accepted.accepted_by_user_id = accepted_by.id;

-- name: DeclineOrganizationMemberInvitation :one
WITH declined AS (
    UPDATE organization_member_invitations omi
    SET
        status = 'declined',
        declined_at = sqlc.arg ('declined_at'),
        updated_at = sqlc.arg ('declined_at')
    WHERE
        omi.organization_id = (
            SELECT id
            FROM organizations
            WHERE
                slug = sqlc.arg ('org_slug')
        )
        AND omi.slug = sqlc.arg ('invitation_slug')
        AND omi.status = 'pending'
    RETURNING
        *
)
SELECT
    declined.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    declined
    JOIN organizations o ON declined.organization_id = o.id
    JOIN roles r ON declined.role_id = r.id
    LEFT JOIN users invited_by ON declined.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON declined.accepted_by_user_id = accepted_by.id;

-- name: RevokeOrganizationMemberInvitation :one
WITH revoked AS (
    UPDATE organization_member_invitations omi
    SET
        status = 'revoked',
        revoked_at = sqlc.arg ('revoked_at'),
        updated_at = sqlc.arg ('revoked_at')
    WHERE
        omi.organization_id = (
            SELECT id
            FROM organizations
            WHERE
                slug = sqlc.arg ('org_slug')
        )
        AND omi.slug = sqlc.arg ('invitation_slug')
        AND omi.status = 'pending'
    RETURNING
        *
)
SELECT
    revoked.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    revoked
    JOIN organizations o ON revoked.organization_id = o.id
    JOIN roles r ON revoked.role_id = r.id
    LEFT JOIN users invited_by ON revoked.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON revoked.accepted_by_user_id = accepted_by.id;

-- name: ExpireOrganizationMemberInvitation :one
WITH expired AS (
    UPDATE organization_member_invitations omi
    SET
        status = 'expired',
        expired_at = sqlc.arg ('expired_at'),
        updated_at = sqlc.arg ('expired_at')
    WHERE
        omi.organization_id = (
            SELECT id
            FROM organizations
            WHERE
                slug = sqlc.arg ('org_slug')
        )
        AND omi.slug = sqlc.arg ('invitation_slug')
        AND omi.status = 'pending'
        AND omi.expires_at <= sqlc.arg ('expired_at')
    RETURNING
        *
)
SELECT
    expired.*,
    o.slug AS organization_slug,
    r.slug AS role_slug,
    COALESCE(invited_by.slug::text, '') AS invited_by_user_slug,
    COALESCE(accepted_by.slug::text, '') AS accepted_user_slug
FROM
    expired
    JOIN organizations o ON expired.organization_id = o.id
    JOIN roles r ON expired.role_id = r.id
    LEFT JOIN users invited_by ON expired.invited_by_user_id = invited_by.id
    LEFT JOIN users accepted_by ON expired.accepted_by_user_id = accepted_by.id;

-- =====================
-- Roles
-- =====================

-- name: CreateRole :one
INSERT INTO
    roles (
        organization_id,
        slug,
        name,
        is_default
    )
VALUES (
        (
            SELECT id
            FROM organizations
            WHERE
                slug = sqlc.arg ('org_slug')
        ),
        sqlc.arg ('role_slug'),
        sqlc.arg ('name')::text,
        sqlc.arg ('is_default')::boolean
    )
RETURNING
    *;

-- name: EnsureDefaultRole :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
),
inserted AS (
    INSERT INTO roles (
        organization_id,
        slug,
        name,
        is_default
    )
    SELECT
        org_row.id,
        sqlc.arg('role_slug'),
        sqlc.arg('name')::text,
        true
    FROM org_row
    ON CONFLICT (organization_id, slug) WHERE deleted_at IS NULL DO NOTHING
    RETURNING *
)
SELECT *
FROM inserted
UNION ALL
SELECT r.*
FROM roles r
JOIN org_row o ON r.organization_id = o.id
WHERE r.slug = sqlc.arg('role_slug')
    AND r.deleted_at IS NULL
LIMIT 1;

-- name: FindRoleBySlug :one
SELECT r.*
FROM roles r
    JOIN organizations o ON r.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND r.slug = sqlc.arg ('role_slug')
    AND r.deleted_at IS NULL;

-- name: ListRolesByOrganization :many
SELECT r.*
FROM roles r
WHERE
    r.organization_id = (
        SELECT id
        FROM organizations
        WHERE
            slug = sqlc.arg ('org_slug')
    )
    AND (
        (
            sqlc.arg ('only_deleted')::boolean
            AND r.deleted_at IS NOT NULL
        )
        OR (
            NOT sqlc.arg ('only_deleted')::boolean
            AND (
                sqlc.arg ('include_deleted')::boolean
                OR r.deleted_at IS NULL
            )
        )
    )
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (r.created_at, r.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (r.created_at, r.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (r.updated_at, r.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (r.updated_at, r.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'name'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (r.name, r.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (r.name, r.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN r.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN r.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN r.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN r.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'asc' THEN r.name
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'desc' THEN r.name
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN r.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN r.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: GetUserPermissionsInOrganization :many
SELECT
    COALESCE(p.resource, '') AS resource,
    COALESCE(p.action, '') AS action
FROM
    organization_members om
    JOIN roles r ON om.role_id = r.id
    JOIN users u ON om.user_id = u.id
    JOIN organizations o ON om.organization_id = o.id
    LEFT JOIN role_permissions rp ON rp.role_id = r.id
    LEFT JOIN permissions p ON rp.permission_id = p.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND u.slug = sqlc.arg ('user_slug')
    AND r.deleted_at IS NULL;

-- =====================
-- API Keys
-- =====================

-- name: CreateAPIKey :one
INSERT INTO
    api_keys (
        organization_id,
        slug,
        name,
        description,
        token_hash,
        token_prefix,
        created_by_member_id,
        role_id,
        expires_at,
        can_manage_api_keys
    )
SELECT
    o.id,
    sqlc.arg ('api_key_slug'),
    sqlc.arg ('name'),
    sqlc.narg ('description'),
    sqlc.arg ('token_hash'),
    sqlc.arg ('token_prefix'),
    (
        SELECT om.id
        FROM organization_members om
        JOIN users u ON u.id = om.user_id
        WHERE om.organization_id = o.id
            AND u.slug = sqlc.narg ('created_by_user_slug')
        LIMIT 1
    ),
    r.id,
    sqlc.narg ('expires_at'),
    sqlc.arg ('can_manage_api_keys')
FROM
    organizations o
    JOIN roles r ON r.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND r.slug = sqlc.arg ('role_slug')
    AND r.deleted_at IS NULL
RETURNING
    api_keys.*;

-- name: FindAPIKeyBySlug :one
SELECT
    ak.*,
    r.slug AS role_slug,
    COALESCE(u.slug, '') AS created_by_user_slug
FROM
    api_keys ak
    JOIN organizations o ON ak.organization_id = o.id
    JOIN roles r ON ak.role_id = r.id
    LEFT JOIN organization_members om ON ak.created_by_member_id = om.id
    LEFT JOIN users u ON om.user_id = u.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ak.slug = sqlc.arg ('api_key_slug');

-- name: ListAPIKeysByOrganization :many
SELECT
    ak.*,
    r.slug AS role_slug,
    COALESCE(u.slug, '') AS created_by_user_slug
FROM
    api_keys ak
    JOIN organizations o ON ak.organization_id = o.id
    JOIN roles r ON ak.role_id = r.id
    LEFT JOIN organization_members om ON ak.created_by_member_id = om.id
    LEFT JOIN users u ON om.user_id = u.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND (
        sqlc.arg ('role_slug_filter')::text = ''
        OR r.slug = sqlc.arg ('role_slug_filter')
    )
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (ak.created_at, ak.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (ak.created_at, ak.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (ak.updated_at, ak.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (ak.updated_at, ak.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'name'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (ak.name, ak.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (ak.name, ak.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN ak.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN ak.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN ak.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN ak.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'asc' THEN ak.name
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'desc' THEN ak.name
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN ak.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN ak.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: UpdateAPIKey :exec
UPDATE api_keys ak
SET
    name = sqlc.arg ('name'),
    description = sqlc.narg ('description'),
    expires_at = sqlc.narg ('expires_at'),
    can_manage_api_keys = CASE
        WHEN sqlc.arg ('can_manage_api_keys_set')::boolean THEN sqlc.arg ('can_manage_api_keys')
        ELSE ak.can_manage_api_keys
    END,
    updated_at = NOW()
FROM organizations o
WHERE
    ak.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ak.slug = sqlc.arg ('api_key_slug');

-- name: RevokeAPIKey :exec
UPDATE api_keys ak
SET
    revoked_at = NOW(),
    updated_at = NOW()
FROM organizations o
WHERE
    ak.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ak.slug = sqlc.arg ('api_key_slug')
    AND ak.revoked_at IS NULL;

-- name: DeleteRevokedAPIKey :execrows
DELETE FROM api_keys ak USING organizations o
WHERE
    ak.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ak.slug = sqlc.arg ('api_key_slug')
    AND ak.revoked_at IS NOT NULL;

-- name: RotateAPIKey :one
WITH rotated AS (
    UPDATE api_keys ak
    SET
        token_hash = sqlc.arg ('token_hash'),
        token_prefix = sqlc.arg ('token_prefix'),
        last_used_at = NULL,
        updated_at = NOW()
    FROM organizations o
    WHERE
        ak.organization_id = o.id
        AND o.slug = sqlc.arg ('org_slug')
        AND ak.slug = sqlc.arg ('api_key_slug')
        AND ak.revoked_at IS NULL
    RETURNING
        ak.*
)
SELECT
    rotated.*,
    r.slug AS role_slug,
    COALESCE(u.slug, '') AS created_by_user_slug
FROM
    rotated
    JOIN roles r ON rotated.role_id = r.id
    LEFT JOIN organization_members om ON rotated.created_by_member_id = om.id
    LEFT JOIN users u ON om.user_id = u.id;

-- name: FindActiveAPIKeyByTokenHash :one
SELECT
    ak.*,
    o.slug AS org_slug,
    r.slug AS role_slug
FROM
    api_keys ak
    JOIN organizations o ON ak.organization_id = o.id
    JOIN roles r ON ak.role_id = r.id
WHERE
    ak.token_hash = sqlc.arg ('token_hash')
    AND r.deleted_at IS NULL
    AND ak.revoked_at IS NULL
    AND (
        ak.expires_at IS NULL
        OR ak.expires_at > NOW()
    );

-- name: MarkAPIKeyUsed :execrows
UPDATE api_keys ak
SET
    last_used_at = NOW(),
    updated_at = NOW()
FROM organizations o
WHERE
    ak.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ak.slug = sqlc.arg ('api_key_slug')
    AND ak.revoked_at IS NULL
    AND (
        ak.expires_at IS NULL
        OR ak.expires_at > NOW()
    );

-- =====================
-- Personal Access Tokens
-- =====================

-- name: CreatePersonalAccessToken :one
INSERT INTO
    personal_access_tokens (
        user_id,
        slug,
        name,
        description,
        token_hash,
        token_prefix,
        expires_at,
        can_manage_personal_access_tokens
    )
SELECT
    u.id,
    sqlc.arg ('pat_slug'),
    sqlc.arg ('name'),
    sqlc.narg ('description'),
    sqlc.arg ('token_hash'),
    sqlc.arg ('token_prefix'),
    sqlc.narg ('expires_at'),
    sqlc.arg ('can_manage_personal_access_tokens')
FROM users u
WHERE
    u.slug = sqlc.arg ('user_slug')
    AND u.deleted_at IS NULL
FOR SHARE OF u
RETURNING
    personal_access_tokens.*;

-- name: FindPersonalAccessTokenBySlug :one
SELECT
    pat.*,
    u.slug AS user_slug
FROM
    personal_access_tokens pat
    JOIN users u ON pat.user_id = u.id
WHERE
    u.slug = sqlc.arg ('user_slug')
    AND pat.slug = sqlc.arg ('pat_slug');

-- name: ListPersonalAccessTokensByUser :many
SELECT
    pat.*,
    u.slug AS user_slug
FROM
    personal_access_tokens pat
    JOIN users u ON pat.user_id = u.id
WHERE
    u.slug = sqlc.arg ('user_slug')
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (pat.created_at, pat.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (pat.created_at, pat.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (pat.updated_at, pat.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (pat.updated_at, pat.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'name'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (pat.name, pat.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (pat.name, pat.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN pat.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN pat.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN pat.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN pat.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'asc' THEN pat.name
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'desc' THEN pat.name
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN pat.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN pat.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: UpdatePersonalAccessToken :execrows
UPDATE personal_access_tokens pat
SET
    name = sqlc.arg ('name'),
    description = CASE
        WHEN sqlc.arg ('description_set')::boolean THEN sqlc.narg ('description')
        ELSE pat.description
    END,
    expires_at = CASE
        WHEN sqlc.arg ('expires_at_set')::boolean THEN sqlc.narg ('expires_at')
        ELSE pat.expires_at
    END,
    can_manage_personal_access_tokens = CASE
        WHEN sqlc.arg ('can_manage_personal_access_tokens_set')::boolean THEN sqlc.arg ('can_manage_personal_access_tokens')
        ELSE pat.can_manage_personal_access_tokens
    END,
    updated_at = NOW()
FROM users u
WHERE
    pat.user_id = u.id
    AND u.slug = sqlc.arg ('user_slug')
    AND pat.slug = sqlc.arg ('pat_slug')
    AND pat.revoked_at IS NULL;

-- name: RevokePersonalAccessToken :execrows
UPDATE personal_access_tokens pat
SET
    revoked_at = NOW(),
    updated_at = NOW()
FROM users u
WHERE
    pat.user_id = u.id
    AND u.slug = sqlc.arg ('user_slug')
    AND pat.slug = sqlc.arg ('pat_slug')
    AND pat.revoked_at IS NULL;

-- name: DeleteRevokedPersonalAccessToken :execrows
DELETE FROM personal_access_tokens pat USING users u
WHERE
    pat.user_id = u.id
    AND u.slug = sqlc.arg ('user_slug')
    AND pat.slug = sqlc.arg ('pat_slug')
    AND pat.revoked_at IS NOT NULL;

-- name: RotatePersonalAccessToken :one
WITH rotated AS (
    UPDATE personal_access_tokens pat
    SET
        token_hash = sqlc.arg ('token_hash'),
        token_prefix = sqlc.arg ('token_prefix'),
        last_used_at = NULL,
        updated_at = NOW()
    FROM users u
    WHERE
        pat.user_id = u.id
        AND u.slug = sqlc.arg ('user_slug')
        AND pat.slug = sqlc.arg ('pat_slug')
        AND pat.revoked_at IS NULL
        AND (
            pat.expires_at IS NULL
            OR pat.expires_at > NOW()
        )
    RETURNING
        pat.*
)
SELECT
    rotated.*,
    u.slug AS user_slug
FROM
    rotated
    JOIN users u ON rotated.user_id = u.id;

-- name: FindActivePersonalAccessTokenByTokenHash :one
SELECT
    pat.*,
    u.slug AS user_slug
FROM
    personal_access_tokens pat
    JOIN users u ON pat.user_id = u.id
WHERE
    pat.token_hash = sqlc.arg ('token_hash')
    AND u.deleted_at IS NULL
    AND pat.revoked_at IS NULL
    AND (
        pat.expires_at IS NULL
        OR pat.expires_at > NOW()
    );

-- name: MarkPersonalAccessTokenUsed :execrows
UPDATE personal_access_tokens pat
SET
    last_used_at = NOW(),
    updated_at = NOW()
FROM users u
WHERE
    pat.user_id = u.id
    AND u.slug = sqlc.arg ('user_slug')
    AND pat.slug = sqlc.arg ('pat_slug')
    AND pat.revoked_at IS NULL
    AND (
        pat.expires_at IS NULL
        OR pat.expires_at > NOW()
    );

-- name: UpdateRole :execresult
UPDATE roles
SET
    name = sqlc.arg ('name')::text,
    updated_at = NOW()
WHERE
    organization_id = (
        SELECT id
        FROM organizations
        WHERE
            slug = sqlc.arg ('org_slug')
    )
    AND slug = sqlc.arg ('role_slug')
    AND is_default = false
    AND deleted_at IS NULL;

-- name: DeleteRole :execresult
UPDATE roles
SET
    deleted_at = NOW(),
    delete_reason = sqlc.narg ('delete_reason'),
    updated_at = NOW()
WHERE
    organization_id = (
        SELECT id
        FROM organizations
        WHERE
            slug = sqlc.arg ('org_slug')
    )
    AND slug = sqlc.arg ('role_slug')
    AND is_default = false
    AND deleted_at IS NULL;

-- name: RestoreRole :execresult
UPDATE roles
SET
    deleted_at = NULL,
    delete_reason = NULL,
    updated_at = NOW()
WHERE
    organization_id = (
        SELECT id
        FROM organizations
        WHERE
            slug = sqlc.arg ('org_slug')
    )
    AND slug = sqlc.arg ('role_slug')
    AND is_default = false
    AND deleted_at IS NOT NULL;

-- =====================
-- Permissions
-- =====================

-- name: CreatePermission :one
INSERT INTO
    permissions (resource, action)
VALUES (
        sqlc.arg ('resource')::text,
        sqlc.arg ('action')::text
    )
ON CONFLICT (resource, action) DO
UPDATE
SET
    resource = EXCLUDED.resource
RETURNING
    *;

-- name: FindOrCreatePermissions :many
INSERT INTO
    permissions (resource, action)
SELECT *
FROM UNNEST(
        sqlc.arg ('resources')::text [], sqlc.arg ('actions')::text []
    ) AS t (resource, action)
ON CONFLICT (resource, action) DO
UPDATE
SET
    resource = EXCLUDED.resource
RETURNING
    *;

-- name: ListAllPermissions :many
-- Bounded by design: permissions are generated from finite resource/action enums.
SELECT * FROM permissions ORDER BY resource, action;

-- name: ListPermissionsForRole :many
SELECT p.*
FROM
    permissions p
    JOIN role_permissions rp ON p.id = rp.permission_id
    JOIN roles r ON rp.role_id = r.id
WHERE
    r.organization_id = (
        SELECT id
        FROM organizations
        WHERE
            slug = sqlc.arg ('org_slug')
    )
    AND r.slug = sqlc.arg ('role_slug')
ORDER BY p.resource, p.action;

-- name: AddPermissionToRole :exec
INSERT INTO
    role_permissions (role_id, permission_id)
VALUES (
        (
            SELECT id
            FROM roles
            WHERE
                organization_id = (
                    SELECT id
                    FROM organizations
                    WHERE
                        slug = sqlc.arg ('org_slug')
                )
                AND slug = sqlc.arg ('role_slug')
                AND deleted_at IS NULL
        ),
        (
            SELECT id
            FROM permissions
            WHERE
                resource = sqlc.arg ('resource')::text
                AND action = sqlc.arg ('action')::text
        )
    )
ON CONFLICT DO NOTHING;

-- name: RemovePermissionFromRole :exec
DELETE FROM role_permissions
WHERE
    role_id = (
        SELECT id
        FROM roles
        WHERE
            organization_id = (
                SELECT id
                FROM organizations
                WHERE
                    slug = sqlc.arg ('org_slug')
            )
            AND slug = sqlc.arg ('role_slug')
            AND deleted_at IS NULL
    )
    AND permission_id = (
        SELECT id
        FROM permissions
        WHERE
            resource = sqlc.arg ('resource')::text
            AND action = sqlc.arg ('action')::text
    );

-- name: ClearRolePermissions :exec
DELETE FROM role_permissions
WHERE
    role_id = (
        SELECT id
        FROM roles
        WHERE
            organization_id = (
                SELECT id
                FROM organizations
                WHERE
                    slug = sqlc.arg ('org_slug')
            )
            AND slug = sqlc.arg ('role_slug')
            AND deleted_at IS NULL
    );

-- =====================
-- Data Sources
-- =====================

-- name: CreateDataSource :one
INSERT INTO
    data_sources (
        organization_id,
        credential_id,
        name,
        slug,
        type,
        config,
        is_active,
        read_only
    )
SELECT
        o.id,
        c.id,
        sqlc.arg ('name'),
        sqlc.arg ('slug'),
        sqlc.arg ('type'),
        sqlc.arg ('config_json')::jsonb,
        false,
        sqlc.arg ('read_only')
FROM organizations o
JOIN credentials c ON c.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND c.slug = sqlc.arg ('credential_slug')
RETURNING
    *;

-- name: FindDataSourceBySlug :one
SELECT
    sqlc.embed(ds),
    jsonb_build_object(
        'slug', c.slug,
        'kind', c.kind,
        'status', c.status,
        'provider', CASE
            WHEN c.kind = 'oauth2_grant' THEN COALESCE(g.provider, ds.type)
            ELSE ''
        END,
        'updated_at', c.updated_at,
        'access_token_expires_at', g.access_token_expires_at,
        'scopes', COALESCE(g.scopes, '{}'::text[]),
        'account', COALESCE(g.account, '{}'::jsonb),
        'last_refreshed_at', g.last_refreshed_at,
        'revoked_at', c.revoked_at,
        'failure_code', c.failure_code,
        'failure_message', c.failure_message
    ) AS credential_metadata
FROM
    data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
    JOIN credentials c
        ON c.organization_id = ds.organization_id
        AND c.id = ds.credential_id
    LEFT JOIN credential_oauth_grants g ON g.credential_id = c.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL;

-- name: IsDataSourceSlugReserved :one
SELECT EXISTS (
    SELECT 1
    FROM data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND ds.slug = sqlc.arg ('ds_slug')
);

-- name: UpdateDataSourceCredentials :exec
WITH target AS (
    SELECT
        ds.id,
        ds.credential_id AS old_credential_id
    FROM data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND ds.slug = sqlc.arg ('ds_slug')
        AND ds.deleted_at IS NULL
    FOR UPDATE OF ds
),
replacement AS (
    SELECT c.id
    FROM credentials c
    JOIN organizations o ON c.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND c.slug = sqlc.arg ('credential_slug')
),
attached AS (
    UPDATE data_sources ds
    SET
        credential_id = replacement.id,
        connection_test_revision = ds.connection_test_revision + 1,
        updated_at = NOW()
    FROM target, replacement
    WHERE ds.id = target.id
    RETURNING
        ds.id,
        target.old_credential_id,
        replacement.id AS new_credential_id
),
retired AS (
    UPDATE credentials c
    SET
        status = 'rotated',
        rotated_at = NOW(),
        encrypted_payload = NULL,
        updated_at = NOW()
    FROM attached
    WHERE
        c.id = attached.old_credential_id
        AND c.id <> attached.new_credential_id
        AND NOT EXISTS (
            SELECT 1
            FROM data_sources ds2
            WHERE
                ds2.credential_id = attached.old_credential_id
                AND ds2.id <> attached.id
        )
    RETURNING c.id
),
tombstoned_old_grants AS (
    UPDATE credential_oauth_grants g
    SET
        encrypted_token_set = decode('', 'hex'),
        updated_at = NOW()
    FROM retired
    WHERE g.credential_id = retired.id
    RETURNING g.credential_id
)
SELECT retired.id
FROM retired
LEFT JOIN tombstoned_old_grants ON tombstoned_old_grants.credential_id = retired.id;

-- name: ListDataSourcesByOrg :many
SELECT
    sqlc.embed(ds),
    jsonb_build_object(
        'slug', c.slug,
        'kind', c.kind,
        'status', c.status,
        'provider', CASE
            WHEN c.kind = 'oauth2_grant' THEN COALESCE(g.provider, ds.type)
            ELSE ''
        END,
        'updated_at', c.updated_at,
        'access_token_expires_at', g.access_token_expires_at,
        'scopes', COALESCE(g.scopes, '{}'::text[]),
        'account', COALESCE(g.account, '{}'::jsonb),
        'last_refreshed_at', g.last_refreshed_at,
        'revoked_at', c.revoked_at,
        'failure_code', c.failure_code,
        'failure_message', c.failure_message
    ) AS credential_metadata
FROM
    data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
    JOIN credentials c
        ON c.organization_id = ds.organization_id
        AND c.id = ds.credential_id
    LEFT JOIN credential_oauth_grants g ON g.credential_id = c.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.deleted_at IS NULL
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (ds.created_at, ds.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (ds.created_at, ds.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (ds.updated_at, ds.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (ds.updated_at, ds.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'name'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (ds.name, ds.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (ds.name, ds.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN ds.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN ds.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN ds.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN ds.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'asc' THEN ds.name
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'desc' THEN ds.name
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN ds.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN ds.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: UpdateDataSource :exec
UPDATE data_sources ds
SET
    name = sqlc.arg ('name'),
    config = sqlc.arg ('config_json')::jsonb,
    is_active = sqlc.arg ('is_active'),
    read_only = sqlc.arg ('read_only'),
    connection_test_revision = ds.connection_test_revision + 1,
    updated_at = NOW()
FROM organizations o
WHERE
    ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL;

-- name: SetDataSourceReadOnly :exec
UPDATE data_sources ds
SET
    read_only = sqlc.arg ('read_only'),
    updated_at = NOW()
FROM organizations o
WHERE
    ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL;

-- name: MarkDataSourceTestSuccess :one
UPDATE data_sources ds
SET
    is_active = true,
    test_error = NULL,
    last_tested_at = sqlc.arg ('tested_at_ts'),
    connection_test_revision = ds.connection_test_revision + 1,
    updated_at = NOW()
FROM organizations o
WHERE
    ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.connection_test_revision = sqlc.arg ('expected_revision')
    AND ds.deleted_at IS NULL
RETURNING ds.*;

-- name: MarkDataSourceTestFailure :one
UPDATE data_sources ds
SET
    is_active = false,
    test_error = sqlc.arg ('error_message'),
    last_tested_at = sqlc.arg ('tested_at_ts'),
    connection_test_revision = ds.connection_test_revision + 1,
    updated_at = NOW()
FROM organizations o
WHERE
    ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.connection_test_revision = sqlc.arg ('expected_revision')
    AND ds.deleted_at IS NULL
RETURNING ds.*;

-- name: DataSourceHasProjectBackendReferences :one
SELECT EXISTS (
    SELECT 1
    FROM
        data_sources ds
        JOIN organizations o ON ds.organization_id = o.id
        JOIN projects p ON p.organization_id = o.id
            AND p.pipeline_backend_data_source_id = ds.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND ds.slug = sqlc.arg ('ds_slug')
        AND ds.deleted_at IS NULL
);

-- name: LockDataSourceForUpdate :one
SELECT ds.id
FROM
    data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL
FOR UPDATE;

-- name: DeleteDataSource :execresult
UPDATE data_sources ds
SET
    deleted_at = NOW(),
    delete_reason = sqlc.narg ('delete_reason'),
    updated_at = NOW()
FROM organizations o
WHERE
    ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL
    AND NOT EXISTS (
        SELECT 1
        FROM projects p
        WHERE
            p.organization_id = o.id
            AND p.pipeline_backend_data_source_id = ds.id
    );

-- name: RestoreDataSource :execresult
UPDATE data_sources ds
SET
    deleted_at = NULL,
    delete_reason = NULL,
    updated_at = NOW()
FROM organizations o
WHERE
    ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug');

-- =====================
-- Credentials
-- =====================

-- name: CreatePendingOAuthCredential :one
INSERT INTO
    credentials (
        organization_id,
        slug,
        kind,
        status
    )
SELECT
    o.id,
    sqlc.arg('credential_slug'),
    'oauth2_grant',
    sqlc.arg('status')
FROM organizations o
WHERE o.slug = sqlc.arg('org_slug')
RETURNING *;

-- name: CreateStaticCredential :one
INSERT INTO
    credentials (
        organization_id,
        slug,
        kind,
        credential_type,
        status,
        encrypted_payload
    )
SELECT
    o.id,
    sqlc.arg('credential_slug'),
    'static_secret',
    sqlc.arg('credential_type'),
    sqlc.arg('status'),
    sqlc.arg('encrypted_payload')
FROM organizations o
WHERE o.slug = sqlc.arg('org_slug')
RETURNING *;

-- name: CreatePipelineWebhookCredential :one
WITH target AS (
    SELECT o.id AS organization_id, p.id AS project_id, pt.id AS pipeline_trigger_id
    FROM organizations o
    JOIN projects p ON p.organization_id = o.id
    JOIN project_environments pe ON pe.project_id = p.id
    JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
    JOIN pipeline_triggers pt ON pt.project_id = p.id AND pt.project_environment_id = pe.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND pt.slug = sqlc.arg('trigger_slug')
        AND pt.trigger_type = 'webhook'
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
    FOR UPDATE OF pt
), next_version AS (
    SELECT target.*, COALESCE(MAX(pws.key_version), 0) + 1 AS key_version
    FROM target
    LEFT JOIN pipeline_webhook_secret_versions pws ON pws.pipeline_trigger_id = target.pipeline_trigger_id
    GROUP BY target.organization_id, target.project_id, target.pipeline_trigger_id
), inserted AS (
    INSERT INTO credentials (organization_id, slug, kind, credential_type, status, encrypted_payload)
    SELECT organization_id, sqlc.arg('credential_slug'), 'static_secret', 'pipeline_webhook', 'active', sqlc.arg('encrypted_payload')
    FROM next_version
    RETURNING *
), rotated AS (
    UPDATE credentials c
    SET status = 'rotated', encrypted_payload = NULL, rotated_at = sqlc.arg('rotated_at'), updated_at = NOW()
    FROM pipeline_webhook_secret_versions pws, next_version, inserted
    WHERE pws.pipeline_trigger_id = next_version.pipeline_trigger_id
        AND pws.credential_id = c.id
        AND pws.enabled
        AND c.id <> inserted.id
    RETURNING c.id
), disabled AS (
    UPDATE pipeline_webhook_secret_versions pws
    SET enabled = FALSE, disabled_at = sqlc.arg('rotated_at')
    FROM next_version, inserted
    WHERE pws.pipeline_trigger_id = next_version.pipeline_trigger_id
        AND pws.enabled
        AND pws.credential_id <> inserted.id
    RETURNING pws.credential_id
), linked AS (
    INSERT INTO pipeline_webhook_secret_versions (
        organization_id, project_id, pipeline_trigger_id, credential_id, key_version
    )
    SELECT next_version.organization_id, next_version.project_id, next_version.pipeline_trigger_id, inserted.id, next_version.key_version
    FROM next_version, inserted
    RETURNING key_version
)
SELECT inserted.*, linked.key_version
FROM inserted, linked;

-- name: LockPipelineWebhookCredentialOwner :one
SELECT pt.id
FROM organizations o
JOIN projects p ON p.organization_id = o.id
JOIN project_environments pe ON pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
JOIN pipeline_triggers pt ON pt.project_id = p.id AND pt.project_environment_id = pe.id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND pt.slug = sqlc.arg('trigger_slug')
    AND pt.trigger_type = 'webhook'
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
FOR UPDATE OF pt;

-- name: ResolvePipelineWebhookCredential :one
SELECT c.*
FROM credentials c
JOIN pipeline_webhook_secret_versions pws
    ON pws.credential_id = c.id
    AND pws.organization_id = c.organization_id
JOIN pipeline_triggers pt ON pt.id = pws.pipeline_trigger_id
JOIN project_environments pe ON pe.id = pt.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND pt.slug = sqlc.arg('trigger_slug')
    AND pt.trigger_type = 'webhook'
    AND pt.enabled
    AND pt.disabled_at IS NULL
    AND pe.deleted_at IS NULL
    AND e.deleted_at IS NULL
    AND pws.key_version = sqlc.arg('key_version')
    AND pws.enabled
    AND pws.disabled_at IS NULL
    AND c.kind = 'static_secret'
    AND c.credential_type = 'pipeline_webhook'
    AND c.status = 'active'
    AND c.revoked_at IS NULL;

-- name: RevokePipelineWebhookCredentials :many
WITH target AS (
    SELECT pws.credential_id
    FROM organizations o
    JOIN projects p ON p.organization_id = o.id
    JOIN project_environments pe ON pe.project_id = p.id
    JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
    JOIN pipeline_triggers pt ON pt.project_id = p.id AND pt.project_environment_id = pe.id
    JOIN pipeline_webhook_secret_versions pws ON pws.pipeline_trigger_id = pt.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND pt.slug = sqlc.arg('trigger_slug')
), disabled AS (
    UPDATE pipeline_webhook_secret_versions pws
    SET enabled = FALSE, disabled_at = COALESCE(pws.disabled_at, sqlc.arg('revoked_at'))
    FROM target
    WHERE pws.credential_id = target.credential_id
    RETURNING pws.credential_id
)
UPDATE credentials c
SET status = 'revoked', encrypted_payload = NULL, revoked_at = COALESCE(c.revoked_at, sqlc.arg('revoked_at')), updated_at = NOW()
FROM disabled
WHERE c.id = disabled.credential_id
    AND c.credential_type = 'pipeline_webhook'
    AND c.status IN ('active', 'rotated', 'revoked')
RETURNING c.*;

-- name: FindStaticCredentialByDataSource :one
SELECT c.*
FROM credentials c
JOIN organizations o ON o.id = c.organization_id
JOIN data_sources ds ON ds.credential_id = c.id
    AND ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg('org_slug')
    AND ds.slug = sqlc.arg('data_source_slug')
    AND c.kind = 'static_secret'
    AND c.status = 'active'
    AND ds.deleted_at IS NULL
LIMIT 1;

-- name: FindStaticCredentialByEnvironmentSecret :one
SELECT c.*
FROM credentials c
JOIN organizations o ON o.id = c.organization_id
JOIN environment_secrets es ON es.credential_id = c.id
JOIN environments e ON e.id = es.environment_id
    AND e.organization_id = o.id
WHERE
    o.slug = sqlc.arg('org_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND es.key = sqlc.arg('environment_secret_key')
    AND c.kind = 'static_secret'
    AND c.credential_type = 'environment_secret'
    AND c.status = 'active'
    AND e.deleted_at IS NULL
LIMIT 1;

-- name: MarkStaticEnvironmentSecretRotated :many
UPDATE credentials c
SET
    status = 'rotated',
    encrypted_payload = NULL,
    rotated_at = sqlc.arg('rotated_at'),
    updated_at = NOW()
FROM organizations o
JOIN environments e ON e.organization_id = o.id
JOIN environment_secrets es ON es.environment_id = e.id
WHERE
    c.organization_id = o.id
    AND c.id = es.credential_id
    AND o.slug = sqlc.arg('org_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND es.key = sqlc.arg('environment_secret_key')
    AND c.slug <> sqlc.arg('replacement_credential_slug')
    AND c.kind = 'static_secret'
    AND c.credential_type = 'environment_secret'
    AND c.status = 'active'
    AND e.deleted_at IS NULL
RETURNING c.*;

-- name: MarkStaticEnvironmentSecretRevoked :one
UPDATE credentials c
SET
    status = 'revoked',
    encrypted_payload = NULL,
    revoked_at = COALESCE(c.revoked_at, sqlc.arg('revoked_at')),
    updated_at = NOW()
FROM organizations o
JOIN environments e ON e.organization_id = o.id
JOIN environment_secrets es ON es.environment_id = e.id
WHERE
    c.organization_id = o.id
    AND c.id = es.credential_id
    AND o.slug = sqlc.arg('org_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND es.key = sqlc.arg('environment_secret_key')
    AND c.kind = 'static_secret'
    AND c.credential_type = 'environment_secret'
    AND c.status IN ('active', 'revoked')
    AND e.deleted_at IS NULL
RETURNING c.*;

-- name: CreateOAuthInstall :one
WITH inserted AS (
INSERT INTO
    credential_oauth_installs (
        organization_id,
        data_source_id,
        slug,
        provider,
        state_hash,
        encrypted_pkce_verifier,
        intent,
        expected_credential_id,
        expected_credential_status,
        expected_source_revision,
        actor_type,
        actor_slug,
        requested_scopes,
        expires_at
    )
SELECT
    o.id,
    ds.id,
    sqlc.arg('install_slug'),
    sqlc.arg('provider'),
    sqlc.arg('state_hash'),
    sqlc.narg('encrypted_pkce_verifier'),
    sqlc.arg('intent'),
    c.id,
    c.status,
    ds.connection_test_revision,
    sqlc.arg('actor_type'),
    sqlc.narg('actor_slug'),
    sqlc.arg('requested_scopes')::text[],
    sqlc.arg('expires_at')
FROM organizations o
JOIN data_sources ds ON ds.organization_id = o.id
JOIN credentials c ON c.id = ds.credential_id
    AND c.organization_id = ds.organization_id
WHERE
    o.slug = sqlc.arg('org_slug')
    AND ds.slug = sqlc.arg('data_source_slug')
    AND ds.deleted_at IS NULL
    AND ds.organization_gitlab_connection_id IS NULL
RETURNING *
)
SELECT
    inserted.id,
    inserted.organization_id,
    inserted.data_source_id,
    inserted.slug,
    inserted.provider,
    inserted.state_hash,
    inserted.encrypted_pkce_verifier,
    inserted.intent,
    expected_credential.slug AS expected_credential_slug,
    inserted.expected_credential_status,
    inserted.expected_source_revision,
    inserted.actor_type,
    inserted.actor_slug,
    inserted.requested_scopes,
    inserted.expires_at,
    inserted.consumed_at,
    inserted.created_at,
    inserted.updated_at
FROM inserted
JOIN credentials expected_credential
    ON expected_credential.organization_id = inserted.organization_id
    AND expected_credential.id = inserted.expected_credential_id;

-- name: FindOAuthInstallByState :one
SELECT
    i.id,
    i.organization_id,
    i.data_source_id,
    i.slug,
    i.provider,
    i.state_hash,
    i.encrypted_pkce_verifier,
    i.intent,
    expected_credential.slug AS expected_credential_slug,
    i.expected_credential_status,
    i.expected_source_revision,
    i.repository_setup_slug,
    i.repository_setup_project_slug,
    i.actor_type,
    i.actor_slug,
    i.requested_scopes,
    i.expires_at,
    i.consumed_at,
    i.created_at,
    i.updated_at,
    o.slug AS org_slug,
    COALESCE(ds.slug, '')::text AS data_source_slug,
    (i.organization_gitlab_connection_id IS NOT NULL)::boolean AS organization_owned,
    COALESCE(nc.slug, '')::text AS notification_connection_slug,
    CASE
        WHEN i.consumed_at IS NOT NULL THEN 'consumed'
        WHEN i.expires_at < sqlc.arg('now') THEN 'expired'
        ELSE 'valid'
    END AS state_status
FROM credential_oauth_installs i
JOIN organizations o ON o.id = i.organization_id
LEFT JOIN data_sources ds ON ds.id = i.data_source_id
    AND ds.organization_id = o.id
    AND ds.deleted_at IS NULL
LEFT JOIN organization_gitlab_connections connection
    ON connection.organization_id = i.organization_id AND connection.id = i.organization_gitlab_connection_id
LEFT JOIN notification_connections nc ON nc.organization_id = i.organization_id AND nc.id = i.notification_connection_id
JOIN credentials expected_credential
    ON expected_credential.organization_id = i.organization_id
    AND expected_credential.id = i.expected_credential_id
WHERE
    (ds.id IS NOT NULL OR connection.id IS NOT NULL OR nc.id IS NOT NULL)
    AND i.provider = sqlc.arg('provider')
    AND i.state_hash = sqlc.arg('state_hash')
LIMIT 1;

-- name: ConsumeOAuthInstall :one
WITH matched AS (
    SELECT
        i.*,
        o.slug AS org_slug,
        COALESCE(ds.slug, '')::text AS data_source_slug,
        (i.organization_gitlab_connection_id IS NOT NULL)::boolean AS organization_owned,
    COALESCE(nc.slug, '')::text AS notification_connection_slug,
        expected_credential.slug AS expected_credential_slug,
        CASE
            WHEN i.consumed_at IS NOT NULL THEN 'consumed'
            WHEN i.expires_at < sqlc.arg('consumed_at') THEN 'expired'
            ELSE 'valid'
        END AS state_status
    FROM credential_oauth_installs i
    JOIN organizations o ON o.id = i.organization_id
    LEFT JOIN data_sources ds ON ds.id = i.data_source_id
        AND ds.organization_id = o.id
        AND ds.deleted_at IS NULL
    LEFT JOIN organization_gitlab_connections connection
        ON connection.organization_id = i.organization_id AND connection.id = i.organization_gitlab_connection_id
    LEFT JOIN notification_connections nc ON nc.organization_id = i.organization_id AND nc.id = i.notification_connection_id
JOIN credentials expected_credential
        ON expected_credential.organization_id = i.organization_id
        AND expected_credential.id = i.expected_credential_id
    WHERE
        (ds.id IS NOT NULL OR connection.id IS NOT NULL OR nc.id IS NOT NULL)
        AND i.provider = sqlc.arg('provider')
        AND i.state_hash = sqlc.arg('state_hash')
    LIMIT 1
),
updated AS (
    UPDATE credential_oauth_installs i
    SET
        consumed_at = sqlc.arg('consumed_at'),
        updated_at = NOW()
    FROM matched
    WHERE
        i.id = matched.id
        AND matched.state_status = 'valid'
        AND i.consumed_at IS NULL
        AND i.expires_at >= sqlc.arg('consumed_at')
    RETURNING i.*
)
SELECT
    matched.id,
    matched.organization_id,
    matched.data_source_id,
    matched.slug,
    matched.provider,
    matched.state_hash,
    matched.encrypted_pkce_verifier,
    matched.intent,
    matched.expected_credential_slug,
    matched.expected_credential_status,
    matched.expected_source_revision,
    matched.repository_setup_slug,
    matched.repository_setup_project_slug,
    matched.actor_type,
    matched.actor_slug,
    matched.requested_scopes,
    matched.expires_at,
    COALESCE(updated.consumed_at, matched.consumed_at) AS consumed_at,
    matched.created_at,
    COALESCE(updated.updated_at, matched.updated_at) AS updated_at,
    matched.org_slug,
    matched.data_source_slug,
    matched.organization_owned,
    matched.notification_connection_slug,
    (
        CASE
        WHEN matched.state_status = 'valid' AND updated.id IS NULL THEN 'consumed'
        ELSE matched.state_status
        END
    )::text AS state_status
FROM matched
LEFT JOIN updated ON true;

-- name: LockOAuthGrantIntent :one
SELECT
    c.slug AS credential_slug,
    c.kind,
    c.status,
    ds.connection_test_revision,
    EXISTS (
        SELECT 1
        FROM credential_oauth_grants g
        WHERE g.credential_id = c.id
    ) AS has_grant
FROM data_sources ds
JOIN organizations o ON o.id = ds.organization_id
JOIN credentials c ON c.id = ds.credential_id
    AND c.organization_id = ds.organization_id
WHERE
    o.slug = sqlc.arg('org_slug')
    AND ds.slug = sqlc.arg('data_source_slug')
    AND ds.deleted_at IS NULL
    AND ds.organization_gitlab_connection_id IS NULL
FOR UPDATE OF ds, c;

-- name: CreateOAuthGrant :one
WITH target AS (
    SELECT
        ds.id,
        ds.organization_id,
        ds.credential_id AS old_credential_id,
        c.kind AS old_credential_kind,
        c.status AS old_credential_status
    FROM data_sources ds
    JOIN organizations o ON o.id = ds.organization_id
    JOIN credentials c ON c.id = ds.credential_id
        AND c.organization_id = ds.organization_id
    WHERE
        o.slug = sqlc.arg('org_slug')
        AND ds.slug = sqlc.arg('data_source_slug')
        AND ds.deleted_at IS NULL
    AND ds.organization_gitlab_connection_id IS NULL
    FOR UPDATE OF ds
),
pending AS (
    SELECT target.old_credential_id AS id
    FROM target
    WHERE
        sqlc.arg('intent') = 'create_grant'
        AND target.old_credential_kind = 'oauth2_grant'
        AND target.old_credential_status = 'pending'
        AND NOT EXISTS (
            SELECT 1
            FROM credential_oauth_grants grant_record
            WHERE grant_record.credential_id = target.old_credential_id
        )
),
inserted AS (
    INSERT INTO
        credentials (
            organization_id,
            slug,
            kind,
            status
        )
    SELECT
        target.organization_id,
        sqlc.arg('credential_slug'),
        'oauth2_grant',
        sqlc.arg('status')
    FROM target
    WHERE sqlc.arg('intent') = 'rotate_grant'
    RETURNING *
),
selected AS (
    SELECT pending.id
    FROM pending
    UNION ALL
    SELECT inserted.id
    FROM inserted
),
activated_pending AS (
    UPDATE credentials c
    SET
        status = 'active',
        failure_code = NULL,
        failure_message = NULL,
        updated_at = NOW()
    FROM pending
    WHERE c.id = pending.id
    RETURNING c.*
),
active_credential AS (
    SELECT inserted.*
    FROM inserted
    UNION ALL
    SELECT activated_pending.*
    FROM activated_pending
),
attached AS (
    UPDATE data_sources ds
    SET
        credential_id = selected.id,
        connection_test_revision = ds.connection_test_revision + 1,
        updated_at = NOW()
    FROM selected, target
    WHERE
        ds.id = target.id
    RETURNING
        ds.id,
        target.old_credential_id,
        selected.id AS new_credential_id
),
retired AS (
    UPDATE credentials c
    SET
        status = 'rotated',
        rotated_at = NOW(),
        encrypted_payload = NULL,
        updated_at = NOW()
    FROM attached
    WHERE
        c.id = attached.old_credential_id
        AND c.id <> attached.new_credential_id
        AND NOT EXISTS (
            SELECT 1
            FROM data_sources ds2
            WHERE
                ds2.credential_id = attached.old_credential_id
                AND ds2.id <> attached.id
        )
    RETURNING c.id
),
tombstoned_old_grants AS (
    UPDATE credential_oauth_grants g
    SET
        encrypted_token_set = decode('', 'hex'),
        updated_at = NOW()
    FROM retired
    WHERE g.credential_id = retired.id
    RETURNING g.credential_id
),
upserted AS (
    INSERT INTO
        credential_oauth_grants (
            credential_id,
            provider,
            encrypted_token_set,
            access_token_expires_at,
            scopes,
            account,
            last_refreshed_at
        )
    SELECT
        selected.id,
        sqlc.arg('provider'),
        sqlc.arg('encrypted_token_set'),
        sqlc.arg('access_token_expires_at'),
        sqlc.arg('scopes')::text[],
        sqlc.arg('account')::jsonb,
        sqlc.narg('last_refreshed_at')
    FROM selected
    JOIN attached ON true
    ON CONFLICT (credential_id)
    DO UPDATE SET
        provider = EXCLUDED.provider,
        encrypted_token_set = EXCLUDED.encrypted_token_set,
        access_token_expires_at = EXCLUDED.access_token_expires_at,
        scopes = EXCLUDED.scopes,
        account = EXCLUDED.account,
        last_refreshed_at = EXCLUDED.last_refreshed_at,
        updated_at = NOW()
    RETURNING *
)
SELECT
    active_credential.*,
    upserted.id AS grant_id,
    upserted.provider,
    upserted.encrypted_token_set,
    upserted.access_token_expires_at,
    upserted.scopes,
    upserted.account,
    upserted.last_refreshed_at,
    upserted.created_at AS grant_created_at,
    upserted.updated_at AS grant_updated_at
FROM active_credential
JOIN upserted ON true;

-- name: UpsertOAuthGrant :one
WITH credential AS (
    SELECT
        c.id,
        c.organization_id
    FROM credentials c
    JOIN organizations o ON o.id = c.organization_id
    WHERE
        o.slug = sqlc.arg('org_slug')
        AND c.slug = sqlc.arg('expected_credential_slug')
        AND c.kind = 'oauth2_grant'
        AND (
            (sqlc.arg('owner_kind')::text = 'organization_gitlab' AND EXISTS (
                SELECT 1 FROM organization_gitlab_connections connection
                WHERE connection.organization_id = c.organization_id AND connection.credential_id = c.id
            )) OR (sqlc.arg('owner_kind')::text = 'notification_connection' AND EXISTS (
                SELECT 1 FROM notification_connections connection
                WHERE connection.organization_id = c.organization_id AND connection.credential_id = c.id
                    AND connection.slug = sqlc.arg('notification_connection_slug')
            )) OR (sqlc.arg('owner_kind')::text = 'data_source' AND EXISTS (
                SELECT 1 FROM data_sources ds
                WHERE ds.organization_id = c.organization_id AND ds.credential_id = c.id
                    AND ds.slug = sqlc.arg('data_source_slug') AND ds.deleted_at IS NULL
                    AND ds.organization_gitlab_connection_id IS NULL
            ))
        )
    FOR UPDATE OF c
),
upserted AS (
INSERT INTO
    credential_oauth_grants (
        credential_id,
        provider,
        encrypted_token_set,
        access_token_expires_at,
        scopes,
        account,
        last_refreshed_at
)
SELECT
    credential.id,
    sqlc.arg('provider'),
    sqlc.arg('encrypted_token_set'),
    sqlc.arg('access_token_expires_at'),
    sqlc.arg('scopes')::text[],
    sqlc.arg('account')::jsonb,
    sqlc.narg('last_refreshed_at')
FROM credential
ON CONFLICT (credential_id)
DO UPDATE SET
    provider = EXCLUDED.provider,
    encrypted_token_set = EXCLUDED.encrypted_token_set,
    access_token_expires_at = EXCLUDED.access_token_expires_at,
    scopes = EXCLUDED.scopes,
    account = EXCLUDED.account,
    last_refreshed_at = EXCLUDED.last_refreshed_at,
    updated_at = NOW()
WHERE credential_oauth_grants.updated_at = sqlc.arg('expected_grant_updated_at')
RETURNING *
),
credential_updated AS (
    UPDATE credentials c
    SET updated_at = NOW()
    FROM upserted
    WHERE c.id = upserted.credential_id
    RETURNING c.*
),
revision_bumped AS (
    UPDATE data_sources ds
    SET
        connection_test_revision = ds.connection_test_revision + 1,
        updated_at = NOW()
    FROM credential, upserted
    WHERE ds.credential_id = credential.id AND ds.organization_id = credential.organization_id
    RETURNING ds.id
)
SELECT
    c.*,
    upserted.id AS grant_id,
    upserted.provider,
    upserted.encrypted_token_set,
    upserted.access_token_expires_at,
    upserted.scopes,
    upserted.account,
    upserted.last_refreshed_at,
    upserted.created_at AS grant_created_at,
    upserted.updated_at AS grant_updated_at
FROM upserted
JOIN credential_updated c ON c.id = upserted.credential_id
;

-- name: FindOAuthGrantByDataSource :one
SELECT
    c.*,
    (ds.organization_gitlab_connection_id IS NOT NULL)::boolean AS organization_owned,
    g.id AS grant_id,
    g.provider,
    g.encrypted_token_set,
    g.access_token_expires_at,
    g.scopes,
    g.account,
    g.last_refreshed_at,
    g.created_at AS grant_created_at,
    g.updated_at AS grant_updated_at
FROM credentials c
JOIN credential_oauth_grants g ON g.credential_id = c.id
JOIN organizations o ON o.id = c.organization_id
JOIN data_sources ds ON ds.credential_id = c.id
    AND ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg('org_slug')
    AND ds.slug = sqlc.arg('data_source_slug')
    AND c.kind = 'oauth2_grant'
    AND ds.deleted_at IS NULL
    AND (ds.organization_gitlab_connection_id IS NULL OR EXISTS (SELECT 1 FROM organization_gitlab_connections connection WHERE connection.organization_id = ds.organization_id AND connection.id = ds.organization_gitlab_connection_id AND connection.credential_id = c.id))
LIMIT 1
;

-- name: MarkCredentialRevoked :one
WITH target AS (
    SELECT
        c.id AS credential_id,
        c.organization_id
    FROM credentials c
    JOIN organizations o ON o.id = c.organization_id
    JOIN credential_oauth_grants g ON g.credential_id = c.id
    WHERE
        o.slug = sqlc.arg('org_slug')
        AND c.slug = sqlc.arg('expected_credential_slug')
        AND g.updated_at = sqlc.arg('expected_grant_updated_at')
        AND c.kind = 'oauth2_grant'
        AND (
            (sqlc.arg('owner_kind')::text = 'organization_gitlab' AND EXISTS (
                SELECT 1 FROM organization_gitlab_connections connection
                WHERE connection.organization_id = c.organization_id AND connection.credential_id = c.id
            )) OR (sqlc.arg('owner_kind')::text = 'notification_connection' AND EXISTS (
                SELECT 1 FROM notification_connections connection
                WHERE connection.organization_id = c.organization_id AND connection.credential_id = c.id
                    AND connection.slug = sqlc.arg('notification_connection_slug')
            )) OR (sqlc.arg('owner_kind')::text = 'data_source' AND EXISTS (
                SELECT 1 FROM data_sources ds
                WHERE ds.organization_id = c.organization_id AND ds.credential_id = c.id
                    AND ds.slug = sqlc.arg('data_source_slug') AND ds.deleted_at IS NULL
                    AND ds.organization_gitlab_connection_id IS NULL
            ))
        )
    FOR UPDATE OF c, g
),
tombstoned AS (
    UPDATE credential_oauth_grants g
    SET
        encrypted_token_set = decode('', 'hex'),
        updated_at = NOW()
    FROM target
    WHERE
        g.credential_id = target.credential_id
        AND g.updated_at = sqlc.arg('expected_grant_updated_at')
    RETURNING
        target.credential_id,
        target.organization_id,
        g.updated_at AS grant_updated_at
),
revoked AS (
    UPDATE credentials c
    SET
        status = 'revoked',
        revoked_at = sqlc.arg('revoked_at'),
        failure_code = sqlc.narg('failure_code'),
        failure_message = sqlc.narg('failure_message'),
        updated_at = NOW()
    FROM tombstoned
    WHERE
        c.id = tombstoned.credential_id
        AND c.organization_id = tombstoned.organization_id
    RETURNING c.*
),
deactivated AS (
    UPDATE data_sources ds
    SET
        is_active = false,
        test_error = COALESCE(sqlc.narg('failure_message'), 'Provider grant revoked'),
        last_tested_at = NOW(),
        connection_test_revision = ds.connection_test_revision + 1,
        updated_at = NOW()
    FROM revoked
    WHERE
        ds.credential_id = revoked.id
        AND ds.organization_id = revoked.organization_id
    RETURNING ds.credential_id
)
SELECT revoked.*, tombstoned.grant_updated_at
FROM revoked
JOIN tombstoned ON tombstoned.credential_id = revoked.id
;

-- =====================
-- Schema Metadata
-- =====================

-- name: FindSchemaMetadataByDataSourceSlug :one
SELECT sm.*
FROM
    schema_metadata sm
    JOIN data_sources ds ON sm.data_source_id = ds.id
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL;

-- name: DeleteSchemaMetadataByDataSourceSlug :exec
DELETE FROM schema_metadata sm
USING data_sources ds, organizations o
WHERE
    sm.data_source_id = ds.id
    AND ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL;

-- name: UpsertSchemaMetadataByDataSourceSlug :one
INSERT INTO
    schema_metadata (
        slug,
        data_source_id,
        schema_data
    )
VALUES (
        (
            sqlc.arg ('ds_slug') || '-' || substring(md5(random()::text), 1, 6)
        ),
        (
            SELECT ds.id
            FROM
                data_sources ds
                JOIN organizations o ON ds.organization_id = o.id
            WHERE
                o.slug = sqlc.arg ('org_slug')
                AND ds.slug = sqlc.arg ('ds_slug')
                AND ds.deleted_at IS NULL
        ),
        sqlc.arg ('schema_data')::jsonb
    )
ON CONFLICT (data_source_id) DO
UPDATE
SET
    schema_data = EXCLUDED.schema_data,
    updated_at = NOW()
RETURNING
    id,
    slug,
    data_source_id,
    schema_data,
    created_at,
    updated_at;

-- name: FindDataSourceSchemaImportState :one
SELECT
    ds.slug,
    ds.schema_import_status,
    ds.schema_import_error_code,
    ds.schema_import_error_message,
    ds.schema_import_started_at,
    ds.schema_import_finished_at,
    false::boolean AS started
FROM
    data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL;

-- name: StartDataSourceSchemaImport :one
WITH target AS (
    SELECT
        ds.id,
        ds.slug,
        ds.schema_import_status,
        ds.schema_import_error_code,
        ds.schema_import_error_message,
        ds.schema_import_attempt_id,
        ds.schema_import_started_at,
        ds.schema_import_finished_at
    FROM
        data_sources ds
        JOIN organizations o ON ds.organization_id = o.id
    WHERE
        o.slug = sqlc.arg ('org_slug')
        AND ds.slug = sqlc.arg ('ds_slug')
        AND ds.deleted_at IS NULL
    FOR UPDATE OF ds
),
updated AS (
    UPDATE data_sources ds
    SET
        schema_import_status = 'importing',
        schema_import_error_code = NULL,
        schema_import_error_message = NULL,
        schema_import_attempt_id = sqlc.arg ('attempt_id'),
        schema_import_started_at = sqlc.arg ('started_at')::timestamptz,
        schema_import_finished_at = NULL,
        updated_at = NOW()
    FROM target
    WHERE
        ds.id = target.id
        AND target.schema_import_status <> 'importing'
    RETURNING
        ds.slug,
        ds.schema_import_status,
        ds.schema_import_error_code,
        ds.schema_import_error_message,
        ds.schema_import_started_at,
        ds.schema_import_finished_at,
        true::boolean AS started
)
SELECT *
FROM updated
UNION ALL
SELECT
    target.slug,
    target.schema_import_status,
    target.schema_import_error_code,
    target.schema_import_error_message,
    target.schema_import_started_at,
    target.schema_import_finished_at,
    false::boolean AS started
FROM target
WHERE NOT EXISTS (SELECT 1 FROM updated);

-- name: FindCurrentDataSourceSchemaImportAttempt :one
SELECT
    ds.slug,
    ds.schema_import_status,
    ds.schema_import_error_code,
    ds.schema_import_error_message,
    ds.schema_import_started_at,
    ds.schema_import_finished_at,
    false::boolean AS started
FROM
    data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL
    AND ds.schema_import_status = 'importing'
    AND ds.schema_import_attempt_id = sqlc.arg ('attempt_id')
FOR UPDATE OF ds;

-- name: MarkDataSourceSchemaImportReady :one
UPDATE data_sources ds
SET
    schema_import_status = 'ready',
    schema_import_error_code = NULL,
    schema_import_error_message = NULL,
    schema_import_attempt_id = NULL,
    schema_import_finished_at = sqlc.arg ('finished_at')::timestamptz,
    updated_at = NOW()
FROM organizations o
WHERE
    ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL
    AND ds.schema_import_status = 'importing'
    AND ds.schema_import_attempt_id = sqlc.arg ('attempt_id')
RETURNING
    ds.slug,
    ds.schema_import_status,
    ds.schema_import_error_code,
    ds.schema_import_error_message,
    ds.schema_import_started_at,
    ds.schema_import_finished_at,
    false::boolean AS started;

-- name: MarkDataSourceSchemaImportFailed :one
UPDATE data_sources ds
SET
    schema_import_status = 'failed',
    schema_import_error_code = sqlc.arg ('error_code'),
    schema_import_error_message = sqlc.arg ('error_message'),
    schema_import_attempt_id = NULL,
    schema_import_finished_at = sqlc.arg ('finished_at')::timestamptz,
    updated_at = NOW()
FROM organizations o
WHERE
    ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL
    AND ds.schema_import_status = 'importing'
    AND ds.schema_import_attempt_id = sqlc.arg ('attempt_id')
RETURNING
    ds.slug,
    ds.schema_import_status,
    ds.schema_import_error_code,
    ds.schema_import_error_message,
    ds.schema_import_started_at,
    ds.schema_import_finished_at,
    false::boolean AS started;

-- name: ResetDataSourceSchemaImport :one
UPDATE data_sources ds
SET
    schema_import_status = 'not_imported',
    schema_import_error_code = NULL,
    schema_import_error_message = NULL,
    schema_import_attempt_id = NULL,
    schema_import_started_at = NULL,
    schema_import_finished_at = NULL,
    updated_at = NOW()
FROM organizations o
WHERE
    ds.organization_id = o.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('ds_slug')
    AND ds.deleted_at IS NULL
RETURNING
    ds.slug,
    ds.schema_import_status,
    ds.schema_import_error_code,
    ds.schema_import_error_message,
    ds.schema_import_started_at,
    ds.schema_import_finished_at,
    false::boolean AS started;

-- =====================
-- Queries
-- =====================

-- name: CreateQuery :one
INSERT INTO
    queries (
        data_source_id,
        slug,
        name,
        description,
        data,
        expected_input_contract,
        expected_input_contract_ref,
        output_contract,
        output_contract_ref
    )
SELECT
    ds.id,
    sqlc.arg ('slug'),
    sqlc.arg ('name'),
    sqlc.narg ('description'),
    sqlc.arg ('data'),
    sqlc.narg ('expected_input_contract'),
    sqlc.narg ('expected_input_contract_ref'),
    sqlc.narg ('output_contract'),
    sqlc.narg ('output_contract_ref')
FROM
    data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
RETURNING *;

-- name: FindQueryBySlug :one
SELECT q.*
FROM
    queries q
    JOIN data_sources ds ON q.data_source_id = ds.id
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
    AND q.slug = sqlc.arg ('query_slug')
    AND q.deleted_at IS NULL;

-- name: FindSavedOperationSnapshot :one
SELECT ds.type AS data_source_type, q.data, q.expected_input_contract, q.expected_input_contract_ref, q.output_contract, q.output_contract_ref, q.updated_at,
    COALESCE((SELECT jsonb_agg(jsonb_build_object(
        'slug', qp.slug, 'name', qp.name, 'data_type', qp.data_type,
        'required', qp.is_required, 'default', qp.default_value
    ) ORDER BY qp.slug) FROM query_parameters qp WHERE qp.query_id = q.id), '[]'::jsonb)::jsonb AS parameters
FROM queries q
JOIN data_sources ds ON q.data_source_id = ds.id
JOIN organizations o ON ds.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND ds.slug = sqlc.arg('data_source_slug') AND ds.deleted_at IS NULL
    AND q.slug = sqlc.arg('query_slug') AND q.deleted_at IS NULL;

-- name: ListQueriesByDataSource :many
SELECT q.*
FROM
    queries q
    JOIN data_sources ds ON q.data_source_id = ds.id
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
    AND (
        sqlc.arg ('include_deleted')::boolean
        OR q.deleted_at IS NULL
    )
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('sort_by') = 'created_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (q.created_at, q.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (q.created_at, q.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'updated_at'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (q.updated_at, q.slug) > (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (q.updated_at, q.slug) < (
                            sqlc.narg ('cursor_timestamp')::timestamptz,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
            OR (
                sqlc.arg ('sort_by') = 'name'
                AND (
                    (
                        sqlc.arg ('order_dir') = 'asc'
                        AND (q.name, q.slug) > (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                    OR (
                        sqlc.arg ('order_dir') = 'desc'
                        AND (q.name, q.slug) < (
                            sqlc.narg ('cursor_text')::text,
                            sqlc.narg ('cursor_slug')::text
                        )
                    )
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN q.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'created_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN q.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'asc' THEN q.updated_at
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'updated_at'
        AND sqlc.arg ('order_dir') = 'desc' THEN q.updated_at
    END DESC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'asc' THEN q.name
    END ASC,
    CASE
        WHEN sqlc.arg ('sort_by') = 'name'
        AND sqlc.arg ('order_dir') = 'desc' THEN q.name
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN q.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN q.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: UpdateQuery :one
UPDATE queries q
SET
    name = sqlc.arg ('name'),
    description = sqlc.narg ('description'),
    data = sqlc.arg ('data'),
    expected_input_contract = sqlc.narg ('expected_input_contract'),
    expected_input_contract_ref = sqlc.narg ('expected_input_contract_ref'),
    output_contract = sqlc.narg ('output_contract'),
    output_contract_ref = sqlc.narg ('output_contract_ref'),
    updated_at = NOW()
FROM
    data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND q.data_source_id = ds.id
    AND q.slug = sqlc.arg ('query_slug')
    AND ds.deleted_at IS NULL
    AND q.deleted_at IS NULL
RETURNING q.*;

-- name: DeleteQuery :execresult
UPDATE queries q
SET
    deleted_at = NOW(),
    delete_reason = sqlc.narg ('delete_reason'),
    updated_at = NOW()
FROM
    data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    q.data_source_id = ds.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
    AND q.slug = sqlc.arg ('query_slug')
    AND q.deleted_at IS NULL;

-- name: RestoreQuery :execresult
UPDATE queries q
SET
    deleted_at = NULL,
    delete_reason = NULL,
    updated_at = NOW()
FROM
    data_sources ds
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    q.data_source_id = ds.id
    AND o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
    AND q.slug = sqlc.arg ('query_slug');

-- name: FindQueryDeletionState :one
SELECT q.deleted_at
FROM
    queries q
    JOIN data_sources ds ON q.data_source_id = ds.id
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
    AND q.slug = sqlc.arg ('query_slug');

-- name: PurgeQuery :execresult
DELETE FROM queries
WHERE
    data_source_id = (
        SELECT ds.id
        FROM
            data_sources ds
            JOIN organizations o ON ds.organization_id = o.id
        WHERE
            o.slug = sqlc.arg ('org_slug')
            AND ds.slug = sqlc.arg ('data_source_slug')
            AND ds.deleted_at IS NULL
    )
    AND slug = sqlc.arg ('query_slug')
    AND deleted_at IS NOT NULL;

-- =====================
-- Query Parameters
-- =====================

-- name: CreateQueryParameter :one
INSERT INTO
    query_parameters (
        query_id,
        slug,
        name,
        data_type,
        is_required,
        default_value,
        description
    )
SELECT q.id, sqlc.arg ('slug'), sqlc.arg ('name'), sqlc.arg ('data_type'), sqlc.arg ('is_required'), sqlc.narg ('default_value'), sqlc.narg ('description')
FROM
    queries q
    JOIN data_sources ds ON q.data_source_id = ds.id
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
    AND q.slug = sqlc.arg ('query_slug')
    AND q.deleted_at IS NULL
RETURNING
    id,
    query_id,
    slug,
    name,
    data_type,
    is_required,
    default_value,
    description,
    created_at;

-- name: ListQueryParametersByQuery :many
SELECT qp.*
FROM query_parameters qp
WHERE
    query_id = (
        SELECT q.id
        FROM
            queries q
            JOIN data_sources ds ON q.data_source_id = ds.id
            JOIN organizations o ON ds.organization_id = o.id
        WHERE
            o.slug = sqlc.arg ('org_slug')
            AND ds.slug = sqlc.arg ('data_source_slug')
            AND ds.deleted_at IS NULL
            AND q.slug = sqlc.arg ('query_slug')
            AND q.deleted_at IS NULL
    )
ORDER BY qp.id;

-- name: DeleteQueryParametersByQuery :exec
DELETE FROM query_parameters
WHERE
    query_id = (
        SELECT q.id
        FROM
            queries q
            JOIN data_sources ds ON q.data_source_id = ds.id
            JOIN organizations o ON ds.organization_id = o.id
        WHERE
            o.slug = sqlc.arg ('org_slug')
            AND ds.slug = sqlc.arg ('data_source_slug')
            AND ds.deleted_at IS NULL
            AND q.slug = sqlc.arg ('query_slug')
            AND q.deleted_at IS NULL
    );

-- =====================
-- Query Executions
-- =====================

-- name: CreateQueryExecution :one
INSERT INTO
    query_executions (
        query_id,
        slug,
        status,
        result
    )
SELECT q.id, sqlc.arg ('slug'), sqlc.arg ('status'), sqlc.arg ('result')
FROM
    queries q
    JOIN data_sources ds ON q.data_source_id = ds.id
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
    AND q.slug = sqlc.arg ('query_slug')
    AND q.deleted_at IS NULL
RETURNING
    id,
    query_id,
    slug,
    status,
    result,
    executed_at;

-- name: ListQueryExecutionsByQuery :many
SELECT qe.*
FROM
    query_executions qe
    JOIN queries q ON qe.query_id = q.id
    JOIN data_sources ds ON q.data_source_id = ds.id
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
    AND q.slug = sqlc.arg ('query_slug')
    AND q.deleted_at IS NULL
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('order_dir') = 'asc'
                AND (qe.executed_at, qe.slug) > (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg ('order_dir') = 'desc'
                AND (qe.executed_at, qe.slug) < (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN qe.executed_at
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN qe.executed_at
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN qe.slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN qe.slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: FindQueryExecutionBySlug :one
SELECT qe.*
FROM
    query_executions qe
    JOIN queries q ON qe.query_id = q.id
    JOIN data_sources ds ON q.data_source_id = ds.id
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.slug = sqlc.arg ('data_source_slug')
    AND ds.deleted_at IS NULL
    AND q.slug = sqlc.arg ('query_slug')
    AND q.deleted_at IS NULL
    AND qe.slug = sqlc.arg ('execution_slug');

-- name: FindLatestSuccessfulQueryExecutionByOrganization :one
SELECT
    qe.slug AS execution_slug,
    ds.slug AS data_source_slug,
    q.slug AS query_slug,
    qe.executed_at
FROM
    query_executions qe
    JOIN queries q ON qe.query_id = q.id
    JOIN data_sources ds ON q.data_source_id = ds.id
    JOIN organizations o ON ds.organization_id = o.id
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND ds.deleted_at IS NULL
    AND q.deleted_at IS NULL
    AND qe.status = 'success'
ORDER BY qe.executed_at DESC, qe.slug DESC
LIMIT 1;

-- name: DeleteExpiredOrganizationQueryExecutions :execrows
DELETE FROM query_executions qe
USING queries q, data_sources ds, organizations o
WHERE qe.query_id = q.id
    AND q.data_source_id = ds.id
    AND ds.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND qe.executed_at < sqlc.arg('cutoff');

-- =====================
-- Events
-- =====================

-- name: CreateEvent :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
)
INSERT INTO events (
    organization_id,
    slug,
    event_class,
    actor_type,
    actor_slug,
    entity_type,
    entity_slug,
    action,
    occurred_at,
    request_id,
    schema_version,
    metadata
)
SELECT
    (SELECT id FROM org_row),
    sqlc.arg('slug'),
    sqlc.arg('event_class'),
    sqlc.arg('actor_type'),
    sqlc.narg('actor_slug'),
    sqlc.arg('entity_type'),
    sqlc.narg('entity_slug'),
    sqlc.arg('action'),
    sqlc.arg('occurred_at'),
    sqlc.narg('request_id'),
    sqlc.arg('schema_version'),
    sqlc.arg('metadata')
WHERE EXISTS (SELECT 1 FROM org_row)
RETURNING *;

-- name: FindEventBySlug :one
SELECT e.*
FROM events e
JOIN organizations o ON e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND e.slug = sqlc.arg('slug');

-- name: ListEventsByOrganization :many
SELECT e.*
FROM events e
JOIN organizations o ON e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND (
        sqlc.narg('event_class')::text IS NULL
        OR e.event_class = sqlc.narg('event_class')::text
    )
    AND (
        sqlc.narg('request_id')::text IS NULL
        OR e.request_id = sqlc.narg('request_id')::text
    )
    AND (
        sqlc.narg('occurred_from')::timestamptz IS NULL
        OR e.occurred_at >= sqlc.narg('occurred_from')::timestamptz
    )
    AND (
        sqlc.narg('occurred_before')::timestamptz IS NULL
        OR e.occurred_at < sqlc.narg('occurred_before')::timestamptz
    )
    AND (
        sqlc.narg('actor_type')::text IS NULL
        OR e.actor_type = sqlc.narg('actor_type')::text
    )
    AND (
        sqlc.narg('actor_slug')::text IS NULL
        OR e.actor_slug = sqlc.narg('actor_slug')::text
    )
    AND (
        sqlc.narg('entity_type')::text IS NULL
        OR e.entity_type = sqlc.narg('entity_type')::text
    )
    AND (
        sqlc.narg('entity_slug')::text IS NULL
        OR e.entity_slug = sqlc.narg('entity_slug')::text
    )
    AND (
        sqlc.narg('action')::text IS NULL
        OR e.action = sqlc.narg('action')::text
    )
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg('order_dir') = 'asc'
                AND (e.occurred_at, e.slug) > (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg('order_dir') = 'desc'
                AND (e.occurred_at, e.slug) < (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN e.occurred_at
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN e.occurred_at
    END DESC,
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN e.slug
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN e.slug
    END DESC
LIMIT sqlc.arg('limit_count');

-- name: ListEventSourceRefsByOrganization :many
WITH source_ref_candidates AS (
    SELECT
        COALESCE(
            NULLIF(e.metadata ->> 'source_ref', ''),
            CASE e.entity_type
                WHEN 'data_source_binding' THEN NULLIF(e.metadata ->> 'data_source_ref_slug', '')
                WHEN 'pipeline_deployment' THEN NULLIF(e.metadata ->> 'pipeline_slug', '')
                WHEN 'pipeline_run' THEN NULLIF(e.metadata ->> 'pipeline_slug', '')
                WHEN 'pipeline_trigger' THEN NULLIF(e.metadata ->> 'trigger_slug', '')
                WHEN 'pipeline_version' THEN NULLIF(e.metadata ->> 'pipeline_slug', '')
                WHEN 'query_execution' THEN NULLIF(e.metadata ->> 'query_slug', '')
                ELSE NULL
            END,
            NULLIF(e.metadata ->> (e.entity_type || '_slug'), ''),
            e.entity_slug
        ) AS source_ref,
        COALESCE(
            NULLIF(e.metadata ->> 'source_label', ''),
            NULLIF(e.metadata ->> 'pipeline_slug', ''),
            NULLIF(e.metadata ->> 'project_slug', ''),
            e.entity_slug
        ) AS source_label,
        e.occurred_at
    FROM events e
    JOIN organizations o ON e.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND (
            sqlc.narg('source_kind')::text IS NULL
            OR e.entity_type = sqlc.narg('source_kind')::text
        )
        AND (
            sqlc.narg('source_action')::text IS NULL
            OR e.action = sqlc.narg('source_action')::text
        )
        AND (
            sqlc.narg('project_slug')::text IS NULL
            OR e.metadata ->> 'project_slug' = sqlc.narg('project_slug')::text
            OR (
                e.entity_type = 'project'
                AND e.entity_slug = sqlc.narg('project_slug')::text
            )
        )
        AND (
            sqlc.narg('environment_slug')::text IS NULL
            OR e.metadata ->> 'environment_slug' = sqlc.narg('environment_slug')::text
            OR (
                e.entity_type = 'environment'
                AND e.entity_slug = sqlc.narg('environment_slug')::text
            )
        )
),
source_refs AS (
    SELECT DISTINCT ON (source_ref)
        source_ref,
        source_label
	FROM source_ref_candidates
	WHERE source_ref IS NOT NULL
	    AND char_length(source_ref) BETWEEN 1 AND 100
	    AND source_ref ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
	ORDER BY source_ref, occurred_at DESC, source_label
)
SELECT source_ref::text, source_label::text
FROM source_refs
ORDER BY LOWER(source_label), source_ref
LIMIT sqlc.arg('limit_count');

-- name: DeleteEventsOccurredBefore :execrows
DELETE FROM events
WHERE occurred_at < sqlc.arg('cutoff')
    AND NOT (event_class = 'security_audit' AND action = 'delete' AND metadata ? 'proof_receipt_key');

-- name: DeleteExpiredOrganizationEvents :execrows
DELETE FROM events e
USING organizations o
WHERE e.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND e.occurred_at < sqlc.arg('cutoff')
    AND NOT (e.event_class = 'security_audit' AND e.action = 'delete' AND e.metadata ? 'proof_receipt_key');

-- =====================
-- Notifications
-- =====================

-- name: CreateAlertDestination :one
INSERT INTO alert_destinations (
    organization_id,
    slug,
    name,
    type,
    non_secret_config,
    enabled
)
SELECT
    o.id,
    sqlc.arg('slug'),
    sqlc.arg('name'),
    sqlc.arg('type'),
    sqlc.arg('non_secret_config'),
    sqlc.arg('enabled')
FROM organizations o
WHERE o.slug = sqlc.arg('org_slug')
    AND (sqlc.arg('type')::text <> 'slack' OR EXISTS (
        SELECT 1 FROM notification_connections nc
        JOIN credentials c ON c.id=nc.credential_id AND c.organization_id=nc.organization_id
        WHERE nc.organization_id=o.id AND nc.provider='slack'
            AND nc.slug=sqlc.arg('non_secret_config')::jsonb->>'connection_slug'
            AND c.status='active'
    ))
RETURNING alert_destinations.*;

-- name: ListAlertDestinationsByOrganization :many
SELECT ad.*
FROM alert_destinations ad
JOIN organizations o ON o.id = ad.organization_id
WHERE o.slug = sqlc.arg('org_slug')
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg('order_dir') = 'asc'
                AND (ad.created_at, ad.slug) > (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg('order_dir') = 'desc'
                AND (ad.created_at, ad.slug) < (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN ad.created_at
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN ad.created_at
    END DESC,
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN ad.slug
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN ad.slug
    END DESC
LIMIT sqlc.arg('limit_count');

-- name: FindAlertDestinationBySlug :one
SELECT ad.*
FROM alert_destinations ad
JOIN organizations o ON o.id = ad.organization_id
WHERE o.slug = sqlc.arg('org_slug')
    AND ad.slug = sqlc.arg('destination_slug');

-- name: UpdateAlertDestination :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
),
updated AS (
    UPDATE alert_destinations ad
    SET
        name = sqlc.arg('name'),
        type = sqlc.arg('type'),
        non_secret_config = sqlc.arg('non_secret_config'),
        enabled = sqlc.arg('enabled'),
        updated_at = NOW()
    FROM org_row
    WHERE ad.organization_id = org_row.id
        AND ad.slug = sqlc.arg('destination_slug')
        AND (sqlc.arg('type')::text <> 'slack' OR EXISTS (
            SELECT 1 FROM notification_connections nc
            JOIN credentials c ON c.id=nc.credential_id AND c.organization_id=nc.organization_id
            WHERE nc.organization_id=org_row.id AND nc.provider='slack'
                AND nc.slug=sqlc.arg('non_secret_config')::jsonb->>'connection_slug'
                AND (NOT sqlc.arg('enabled')::boolean OR c.status='active')
        ))
    RETURNING ad.*
)
SELECT updated.*
FROM updated;

-- name: DeleteAlertDestination :one
DELETE FROM alert_destinations ad
USING organizations o
WHERE ad.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND ad.slug = sqlc.arg('destination_slug')
RETURNING ad.slug;

-- name: CreateAlertPolicy :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
),
project_row AS (
    SELECT p.id, p.slug
    FROM projects p
    JOIN org_row o ON o.id = p.organization_id
    WHERE p.slug = sqlc.narg('project_slug')
),
environment_row AS (
    SELECT e.id, e.slug
    FROM environments e
    JOIN org_row o ON o.id = e.organization_id
    WHERE e.slug = sqlc.narg('environment_slug')
        AND e.deleted_at IS NULL
),
project_environment_row AS (
    SELECT pe.project_id, pe.environment_id
    FROM project_environments pe
    JOIN project_row p ON p.id = pe.project_id
    JOIN environment_row e ON e.id = pe.environment_id
    WHERE pe.deleted_at IS NULL
)
INSERT INTO alert_policies (
    organization_id,
    slug,
    name,
    scope,
    project_id,
    environment_id,
    notification_type,
    severity,
    lifecycle_state,
    source_kind,
    source_action,
    source_ref,
    enabled
)
SELECT
    (SELECT id FROM org_row),
    sqlc.arg('slug'),
    sqlc.arg('name'),
    sqlc.arg('scope'),
    (SELECT id FROM project_row),
    (SELECT id FROM environment_row),
    sqlc.narg('notification_type'),
    sqlc.narg('severity'),
    sqlc.narg('lifecycle_state'),
    sqlc.narg('source_kind'),
    sqlc.narg('source_action'),
    sqlc.narg('source_ref'),
    sqlc.arg('enabled')
WHERE EXISTS (SELECT 1 FROM org_row)
    AND (
        sqlc.narg('project_slug')::text IS NULL
        OR EXISTS (SELECT 1 FROM project_row)
    )
    AND (
        sqlc.narg('environment_slug')::text IS NULL
        OR EXISTS (SELECT 1 FROM environment_row)
    )
    AND (
        sqlc.narg('environment_slug')::text IS NULL
        OR EXISTS (SELECT 1 FROM project_environment_row)
    )
RETURNING
    alert_policies.*,
    COALESCE((SELECT slug FROM project_row), '')::text AS project_slug,
    COALESCE((SELECT slug FROM environment_row), '')::text AS environment_slug;

-- name: ListAlertPoliciesByOrganization :many
SELECT
    ap.*,
    COALESCE(p.slug, '')::text AS project_slug,
    COALESCE(e.slug, '')::text AS environment_slug,
    COALESCE(
        (
            SELECT ARRAY_AGG(ad.slug ORDER BY ad.slug)
            FROM alert_policy_destinations apd
            JOIN alert_destinations ad ON ad.id = apd.destination_id
            WHERE apd.alert_policy_id = ap.id
        ),
        ARRAY[]::text[]
    )::text[] AS destination_slugs
FROM alert_policies ap
JOIN organizations o ON o.id = ap.organization_id
LEFT JOIN projects p ON p.id = ap.project_id
LEFT JOIN environments e ON e.id = ap.environment_id
WHERE o.slug = sqlc.arg('org_slug')
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg('order_dir') = 'asc'
                AND (ap.created_at, ap.slug) > (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg('order_dir') = 'desc'
                AND (ap.created_at, ap.slug) < (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN ap.created_at
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN ap.created_at
    END DESC,
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN ap.slug
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN ap.slug
    END DESC
LIMIT sqlc.arg('limit_count');

-- name: FindAlertPolicyBySlug :one
SELECT
    ap.*,
    COALESCE(p.slug, '')::text AS project_slug,
    COALESCE(e.slug, '')::text AS environment_slug,
    COALESCE(
        (
            SELECT ARRAY_AGG(ad.slug ORDER BY ad.slug)
            FROM alert_policy_destinations apd
            JOIN alert_destinations ad ON ad.id = apd.destination_id
            WHERE apd.alert_policy_id = ap.id
        ),
        ARRAY[]::text[]
    )::text[] AS destination_slugs
FROM alert_policies ap
JOIN organizations o ON o.id = ap.organization_id
LEFT JOIN projects p ON p.id = ap.project_id
LEFT JOIN environments e ON e.id = ap.environment_id
WHERE o.slug = sqlc.arg('org_slug')
    AND ap.slug = sqlc.arg('policy_slug');

-- name: UpdateAlertPolicy :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
),
project_row AS (
    SELECT p.id, p.slug
    FROM projects p
    JOIN org_row o ON o.id = p.organization_id
    WHERE p.slug = sqlc.narg('project_slug')
),
environment_row AS (
    SELECT e.id, e.slug
    FROM environments e
    JOIN org_row o ON o.id = e.organization_id
    WHERE e.slug = sqlc.narg('environment_slug')
        AND e.deleted_at IS NULL
),
project_environment_row AS (
    SELECT pe.project_id, pe.environment_id
    FROM project_environments pe
    JOIN project_row p ON p.id = pe.project_id
    JOIN environment_row e ON e.id = pe.environment_id
    WHERE pe.deleted_at IS NULL
),
updated AS (
    UPDATE alert_policies ap
    SET
        name = sqlc.arg('name'),
        scope = sqlc.arg('scope'),
        project_id = (SELECT id FROM project_row),
        environment_id = (SELECT id FROM environment_row),
        notification_type = sqlc.narg('notification_type'),
        severity = sqlc.narg('severity'),
        lifecycle_state = sqlc.narg('lifecycle_state'),
        source_kind = sqlc.narg('source_kind'),
        source_action = sqlc.narg('source_action'),
        source_ref = sqlc.narg('source_ref'),
        enabled = sqlc.arg('enabled'),
        updated_at = NOW()
    FROM org_row
    WHERE ap.organization_id = org_row.id
        AND ap.slug = sqlc.arg('policy_slug')
        AND (
            sqlc.narg('project_slug')::text IS NULL
            OR EXISTS (SELECT 1 FROM project_row)
        )
        AND (
            sqlc.narg('environment_slug')::text IS NULL
            OR EXISTS (SELECT 1 FROM environment_row)
        )
        AND (
            sqlc.narg('environment_slug')::text IS NULL
            OR EXISTS (SELECT 1 FROM project_environment_row)
        )
    RETURNING ap.*
)
SELECT
    updated.*,
    COALESCE((SELECT slug FROM project_row), '')::text AS project_slug,
    COALESCE((SELECT slug FROM environment_row), '')::text AS environment_slug,
    COALESCE(
        (
            SELECT ARRAY_AGG(ad.slug ORDER BY ad.slug)
            FROM alert_policy_destinations apd
            JOIN alert_destinations ad ON ad.id = apd.destination_id
            WHERE apd.alert_policy_id = updated.id
        ),
        ARRAY[]::text[]
    )::text[] AS destination_slugs
FROM updated;

-- name: ClearAlertPolicyDestinations :execrows
DELETE FROM alert_policy_destinations apd
USING alert_policies ap, organizations o
WHERE apd.alert_policy_id = ap.id
    AND ap.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND ap.slug = sqlc.arg('policy_slug');

-- name: DeleteAlertPolicy :one
DELETE FROM alert_policies ap
USING organizations o
WHERE ap.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND ap.slug = sqlc.arg('policy_slug')
RETURNING ap.slug;

-- name: BindAlertPolicyDestination :one
WITH target_policy AS (
    SELECT np.organization_id, np.id
    FROM alert_policies np
    JOIN organizations o ON o.id = np.organization_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND np.slug = sqlc.arg('alert_policy_slug')
),
target_destination AS (
    SELECT nd.id
    FROM alert_destinations nd
    JOIN organizations o ON o.id = nd.organization_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND nd.slug = sqlc.arg('destination_slug')
),
inserted AS (
    INSERT INTO alert_policy_destinations (
        organization_id,
        alert_policy_id,
        destination_id
    )
    SELECT
        target_policy.organization_id,
        target_policy.id,
        target_destination.id
    FROM target_policy
    CROSS JOIN target_destination
    ON CONFLICT (alert_policy_id, destination_id) DO NOTHING
    RETURNING id
)
SELECT
    EXISTS (SELECT 1 FROM target_policy) AS policy_exists,
    EXISTS (SELECT 1 FROM target_destination) AS destination_exists,
    EXISTS (SELECT 1 FROM inserted) AS inserted;

-- name: UpsertNotificationRecord :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
),
project_row AS (
    SELECT p.id, p.slug
    FROM projects p
    JOIN org_row o ON o.id = p.organization_id
    WHERE p.slug = sqlc.narg('project_slug')
),
environment_row AS (
    SELECT e.id, e.slug
    FROM environments e
    JOIN org_row o ON o.id = e.organization_id
    WHERE e.slug = sqlc.narg('environment_slug')
        AND e.deleted_at IS NULL
),
project_environment_row AS (
    SELECT pe.project_id, pe.environment_id
    FROM project_environments pe
    JOIN project_row p ON p.id = pe.project_id
    JOIN environment_row e ON e.id = pe.environment_id
    WHERE pe.deleted_at IS NULL
),
upserted AS (
    INSERT INTO notification_records (
        organization_id,
        slug,
        project_id,
        environment_id,
        notification_type,
        severity,
        lifecycle_state,
        dedupe_fingerprint,
        source_event_slug,
        source_entity_type,
        source_action,
        source_refs,
        safe_metadata,
        first_seen_at,
        last_seen_at,
        occurrence_count,
        last_occurrence_at,
        dispatch_summary,
        resolved_at
    )
    SELECT
        (SELECT id FROM org_row),
        sqlc.arg('slug'),
        (SELECT id FROM project_row),
        (SELECT id FROM environment_row),
        sqlc.arg('notification_type'),
        sqlc.arg('severity'),
        sqlc.arg('lifecycle_state'),
        sqlc.arg('dedupe_fingerprint'),
        sqlc.narg('source_event_slug'),
        sqlc.arg('source_entity_type'),
        sqlc.arg('source_action'),
        sqlc.arg('source_refs'),
        sqlc.arg('safe_metadata'),
        sqlc.arg('observed_at'),
        sqlc.arg('observed_at'),
        1,
        sqlc.arg('observed_at'),
        '{}'::jsonb,
        CASE WHEN sqlc.arg('lifecycle_state') = 'resolved' THEN sqlc.arg('observed_at')::timestamptz END
    WHERE EXISTS (SELECT 1 FROM org_row)
        AND (
            sqlc.narg('project_slug')::text IS NULL
            OR EXISTS (SELECT 1 FROM project_row)
        )
        AND (
            sqlc.narg('environment_slug')::text IS NULL
            OR EXISTS (SELECT 1 FROM environment_row)
        )
        AND (
            sqlc.narg('environment_slug')::text IS NULL
            OR EXISTS (SELECT 1 FROM project_environment_row)
        )
    ON CONFLICT (organization_id, dedupe_fingerprint)
    WHERE lifecycle_state <> 'resolved'
    DO UPDATE SET
        project_id = COALESCE(notification_records.project_id, EXCLUDED.project_id),
        environment_id = COALESCE(notification_records.environment_id, EXCLUDED.environment_id),
        severity = EXCLUDED.severity,
        source_event_slug = COALESCE(notification_records.source_event_slug, EXCLUDED.source_event_slug),
        source_entity_type = EXCLUDED.source_entity_type,
        source_action = EXCLUDED.source_action,
        source_refs = EXCLUDED.source_refs,
        safe_metadata = EXCLUDED.safe_metadata,
        last_seen_at = EXCLUDED.last_seen_at,
        occurrence_count = notification_records.occurrence_count + 1,
        last_occurrence_at = EXCLUDED.last_occurrence_at,
        updated_at = CURRENT_TIMESTAMP
    RETURNING *
)
SELECT
    upserted.*,
    COALESCE((SELECT p.slug FROM projects p WHERE p.id = upserted.project_id), '')::text AS project_slug,
    COALESCE((SELECT e.slug FROM environments e WHERE e.id = upserted.environment_id), '')::text AS environment_slug
FROM upserted
;

-- name: ListNotificationRecordsByOrganization :many
SELECT
    nr.*,
    COALESCE((SELECT p.slug FROM projects p WHERE p.id = nr.project_id), '')::text AS project_slug,
    COALESCE((SELECT e.slug FROM environments e WHERE e.id = nr.environment_id), '')::text AS environment_slug
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = sqlc.arg('org_slug')
    AND (
        sqlc.narg('lifecycle_state')::text IS NULL
        OR nr.lifecycle_state = sqlc.narg('lifecycle_state')::text
    )
    AND (
        sqlc.narg('lifecycle_state')::text IS NOT NULL
        OR sqlc.arg('include_resolved')::boolean
        OR nr.lifecycle_state <> 'resolved'
    )
    AND (
        sqlc.narg('notification_type')::text IS NULL
        OR nr.notification_type = sqlc.narg('notification_type')::text
    )
    AND (
        sqlc.narg('severity')::text IS NULL
        OR nr.severity = sqlc.narg('severity')::text
    )
    AND (
        sqlc.narg('source_kind')::text IS NULL
        OR nr.source_entity_type = sqlc.narg('source_kind')::text
    )
    AND (
        sqlc.narg('source_action')::text IS NULL
        OR nr.source_action = sqlc.narg('source_action')::text
    )
    AND (
        sqlc.narg('source_ref')::text IS NULL
        OR nr.source_refs ->> 'source_ref' = sqlc.narg('source_ref')::text
        OR (
            nr.source_entity_type = 'pipeline_run'
            AND (
                sqlc.narg('source_kind')::text IS NULL
                OR sqlc.narg('source_kind')::text = 'pipeline_run'
            )
            AND nr.source_refs ->> 'pipeline_slug' = sqlc.narg('source_ref')::text
        )
    )
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg('order_dir') = 'asc'
                AND (nr.last_seen_at, nr.slug) > (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg('order_dir') = 'desc'
                AND (nr.last_seen_at, nr.slug) < (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN nr.last_seen_at
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN nr.last_seen_at
    END DESC,
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN nr.slug
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN nr.slug
    END DESC
LIMIT sqlc.arg('limit_count');

-- name: FindNotificationRecordBySlug :one
SELECT
    nr.*,
    COALESCE((SELECT p.slug FROM projects p WHERE p.id = nr.project_id), '')::text AS project_slug,
    COALESCE((SELECT e.slug FROM environments e WHERE e.id = nr.environment_id), '')::text AS environment_slug
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = sqlc.arg('org_slug')
    AND nr.slug = sqlc.arg('record_slug');

-- name: AcknowledgeNotificationRecord :one
WITH target_record AS (
    SELECT nr.id
    FROM notification_records nr
    JOIN organizations o ON o.id = nr.organization_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND nr.slug = sqlc.arg('record_slug')
),
updated AS (
    UPDATE notification_records nr
    SET lifecycle_state = CASE
            WHEN nr.lifecycle_state = 'open' THEN 'acknowledged'
            ELSE nr.lifecycle_state
        END,
        acknowledged_at = CASE
            WHEN nr.lifecycle_state <> 'resolved' AND nr.acknowledged_at IS NULL THEN sqlc.arg('acknowledged_at')
            ELSE nr.acknowledged_at
        END,
        updated_at = CASE
            WHEN nr.lifecycle_state <> 'resolved'
                AND (nr.lifecycle_state = 'open' OR nr.acknowledged_at IS NULL) THEN CURRENT_TIMESTAMP
            ELSE nr.updated_at
        END
    FROM target_record
    WHERE nr.id = target_record.id
    RETURNING nr.*
)
SELECT
    updated.*,
    COALESCE((SELECT p.slug FROM projects p WHERE p.id = updated.project_id), '')::text AS project_slug,
    COALESCE((SELECT e.slug FROM environments e WHERE e.id = updated.environment_id), '')::text AS environment_slug
FROM updated;

-- name: ResolveNotificationRecord :one
WITH target_record AS (
    SELECT nr.id
    FROM notification_records nr
    JOIN organizations o ON o.id = nr.organization_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND nr.slug = sqlc.arg('record_slug')
),
updated AS (
    UPDATE notification_records nr
    SET lifecycle_state = 'resolved',
        resolved_at = CASE
            WHEN nr.lifecycle_state <> 'resolved' THEN COALESCE(nr.resolved_at, sqlc.arg('resolved_at'))
            ELSE nr.resolved_at
        END,
        updated_at = CASE
            WHEN nr.lifecycle_state <> 'resolved' OR nr.resolved_at IS NULL THEN CURRENT_TIMESTAMP
            ELSE nr.updated_at
        END
    FROM target_record
    WHERE nr.id = target_record.id
    RETURNING nr.*
)
SELECT
    updated.*,
    COALESCE((SELECT p.slug FROM projects p WHERE p.id = updated.project_id), '')::text AS project_slug,
    COALESCE((SELECT e.slug FROM environments e WHERE e.id = updated.environment_id), '')::text AS environment_slug
FROM updated;

-- name: ListNotificationDeliveryTargetsForRecord :many
WITH matching_targets AS (
    SELECT
        o.slug AS org_slug,
        o.name AS organization_name,
        nr.slug AS record_slug,
        ap.slug AS alert_policy_slug,
        ad.slug AS destination_slug,
        ad.name AS destination_name,
        ad.type AS destination_type,
        ad.non_secret_config,
        nr.notification_type,
        nr.severity,
        nr.source_entity_type,
        nr.source_action,
        nr.source_refs,
        nr.safe_metadata,
        nr.occurrence_count,
        CASE ap.scope
            WHEN 'project_environment' THEN 3
            WHEN 'project' THEN 2
            WHEN 'organization' THEN 1
            ELSE 0
        END AS alert_scope_specificity
    FROM notification_records nr
    JOIN organizations o ON o.id = nr.organization_id
    JOIN alert_policies ap ON ap.organization_id = nr.organization_id
    JOIN alert_policy_destinations apd ON apd.alert_policy_id = ap.id
        AND apd.organization_id = nr.organization_id
    JOIN alert_destinations ad ON ad.id = apd.destination_id
        AND ad.organization_id = nr.organization_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND nr.slug = sqlc.arg('record_slug')
        AND ap.enabled
        AND ad.enabled
        AND ad.type IN ('email', 'slack')
        AND (nr.notification_type <> 'pipeline.run_succeeded'
            OR ap.notification_type = 'pipeline.run_succeeded')
        AND (
            ap.notification_type IS NULL
            OR ap.notification_type = nr.notification_type
        )
        AND (
            ap.severity IS NULL
            OR ap.severity = nr.severity
        )
        AND (
            ap.lifecycle_state IS NULL
            OR ap.lifecycle_state = nr.lifecycle_state
        )
        AND (
            ap.source_kind IS NULL
            OR ap.source_kind = nr.source_entity_type
        )
        AND (
            ap.source_action IS NULL
            OR ap.source_action = nr.source_action
        )
        AND (
            ap.source_ref IS NULL
            OR nr.source_refs ->> 'source_ref' = ap.source_ref
            OR (
                ap.source_kind = 'pipeline_run'
                AND nr.source_refs ->> 'pipeline_slug' = ap.source_ref
            )
        )
        AND (
            (
                ap.scope = 'project_environment'
                AND nr.project_id IS NOT NULL
                AND nr.environment_id IS NOT NULL
                AND ap.project_id = nr.project_id
                AND ap.environment_id = nr.environment_id
            )
            OR (
                ap.scope = 'project'
                AND nr.project_id IS NOT NULL
                AND ap.project_id = nr.project_id
                AND ap.environment_id IS NULL
            )
            OR (
                ap.scope = 'organization'
                AND ap.project_id IS NULL
                AND ap.environment_id IS NULL
            )
        )
        AND NOT nr.dispatch_summary ? ad.slug
)
SELECT
    org_slug,
    organization_name,
    record_slug,
    alert_policy_slug,
    destination_slug,
    destination_name,
    destination_type,
    non_secret_config,
    notification_type,
    severity,
    source_entity_type,
    source_action,
    source_refs,
    safe_metadata,
    occurrence_count
FROM (
    SELECT DISTINCT ON (destination_slug)
        *
    FROM matching_targets
    ORDER BY destination_slug, alert_scope_specificity DESC, alert_policy_slug ASC
) selected_targets
ORDER BY destination_slug ASC;

-- name: FindNotificationDeliveryWork :one
SELECT
    o.slug AS org_slug,
    o.name AS organization_name,
    nr.slug AS record_slug,
    ap.slug AS alert_policy_slug,
    nd.slug AS destination_slug,
    nd.name AS destination_name,
    nd.type AS destination_type,
    nd.non_secret_config,
    nr.notification_type,
    nr.severity,
    nr.source_entity_type,
    nr.source_action,
    nr.source_refs,
    nr.safe_metadata,
    nr.occurrence_count
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
JOIN alert_destinations nd ON nd.organization_id = nr.organization_id
JOIN alert_policies ap ON ap.organization_id = nr.organization_id
    AND ap.slug = sqlc.arg('alert_policy_slug')
WHERE o.slug = sqlc.arg('org_slug')
    AND nr.slug = sqlc.arg('record_slug')
    AND nd.slug = sqlc.arg('destination_slug')
    AND (nr.notification_type <> 'pipeline.run_succeeded' OR ap.notification_type = 'pipeline.run_succeeded')
    AND (nd.type <> 'slack' OR (nd.enabled AND ap.enabled AND EXISTS (
        SELECT 1 FROM alert_policy_destinations binding
        WHERE binding.organization_id=o.id AND binding.destination_id=nd.id AND binding.alert_policy_id=ap.id
    )));

-- name: CreateAlertDeliveryAttempt :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
),
record_row AS (
    SELECT nr.id
    FROM notification_records nr
    JOIN org_row o ON o.id = nr.organization_id
    WHERE nr.slug = sqlc.arg('record_slug')
),
policy_row AS (
    SELECT ap.id
    FROM alert_policies ap
    JOIN org_row o ON o.id = ap.organization_id
    WHERE ap.slug = sqlc.arg('alert_policy_slug')
),
destination_row AS (
    SELECT ad.id
    FROM alert_destinations ad
    JOIN org_row o ON o.id = ad.organization_id
    WHERE ad.slug = sqlc.arg('destination_slug')
),
binding_row AS (
    SELECT 1
    FROM alert_policy_destinations apd
    JOIN policy_row pr ON pr.id = apd.alert_policy_id
    JOIN destination_row dr ON dr.id = apd.destination_id
),
inserted AS (
    INSERT INTO alert_delivery_attempts (
        organization_id,
        slug,
        notification_record_id,
        alert_policy_id,
        destination_id,
        destination_type,
        status,
        provider_attempt,
        job_kind,
        job_queue,
        job_state,
        provider_response_metadata,
        queued_at
    )
    SELECT
        (SELECT id FROM org_row),
        sqlc.arg('slug')::text,
        (SELECT id FROM record_row),
        (SELECT id FROM policy_row),
        (SELECT id FROM destination_row),
        sqlc.arg('destination_type')::text,
        sqlc.arg('status')::text,
        sqlc.arg('provider_attempt')::int,
        sqlc.arg('job_kind')::text,
        sqlc.arg('job_queue')::text,
        sqlc.arg('job_state')::text,
        sqlc.arg('provider_response_metadata')::jsonb,
        sqlc.arg('queued_at')::timestamptz
    WHERE EXISTS (SELECT 1 FROM org_row)
        AND EXISTS (SELECT 1 FROM record_row)
        AND EXISTS (SELECT 1 FROM policy_row)
        AND EXISTS (SELECT 1 FROM destination_row)
        AND EXISTS (SELECT 1 FROM binding_row)
    RETURNING *
)
SELECT
    inserted.*,
    o.slug AS org_slug,
    nr.slug AS notification_record_slug,
    COALESCE(ap.slug, '')::text AS alert_policy_slug,
    COALESCE(ad.slug, '')::text AS destination_slug,
    COALESCE(ad.name, '')::text AS destination_name
FROM inserted
JOIN notification_records nr ON nr.id = inserted.notification_record_id
JOIN organizations o ON o.id = nr.organization_id
JOIN alert_policies ap ON ap.id = inserted.alert_policy_id
JOIN alert_destinations ad ON ad.id = inserted.destination_id;

-- name: MarkAlertDeliveryAttemptSent :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
),
updated AS (
    UPDATE alert_delivery_attempts ada
    SET
        status = 'sent',
        provider_attempt = sqlc.arg('provider_attempt')::int,
        job_state = sqlc.arg('job_state')::text,
        provider_response_metadata = sqlc.arg('provider_response_metadata')::jsonb,
        error_category = NULL,
        error_message = NULL,
        sent_at = sqlc.arg('sent_at')::timestamptz,
        failed_at = NULL,
        updated_at = CURRENT_TIMESTAMP
    FROM notification_records nr
    JOIN org_row o ON o.id = nr.organization_id
    WHERE ada.slug = sqlc.arg('attempt_slug')
        AND ada.notification_record_id = nr.id
        AND ada.status = 'queued'
    RETURNING ada.*
)
SELECT
    updated.*,
    o.slug AS org_slug,
    nr.slug AS notification_record_slug,
    COALESCE(ap.slug, '')::text AS alert_policy_slug,
    COALESCE(ad.slug, '')::text AS destination_slug,
    COALESCE(ad.name, '')::text AS destination_name
FROM updated
JOIN notification_records nr ON nr.id = updated.notification_record_id
JOIN organizations o ON o.id = nr.organization_id
LEFT JOIN alert_policies ap ON ap.id = updated.alert_policy_id
    AND ap.organization_id = nr.organization_id
LEFT JOIN alert_destinations ad ON ad.id = updated.destination_id
    AND ad.organization_id = nr.organization_id;

-- name: MarkAlertDeliveryAttemptFailed :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
),
updated AS (
    UPDATE alert_delivery_attempts ada
    SET
        status = 'failed',
        provider_attempt = sqlc.arg('provider_attempt')::int,
        job_state = sqlc.arg('job_state')::text,
        provider_response_metadata = sqlc.arg('provider_response_metadata')::jsonb,
        error_category = sqlc.narg('error_category')::text,
        error_message = sqlc.narg('error_message')::text,
        sent_at = NULL,
        failed_at = sqlc.arg('failed_at')::timestamptz,
        updated_at = CURRENT_TIMESTAMP
    FROM notification_records nr
    JOIN org_row o ON o.id = nr.organization_id
    WHERE ada.slug = sqlc.arg('attempt_slug')
        AND ada.notification_record_id = nr.id
        AND ada.status = 'queued'
    RETURNING ada.*
)
SELECT
    updated.*,
    o.slug AS org_slug,
    nr.slug AS notification_record_slug,
    COALESCE(ap.slug, '')::text AS alert_policy_slug,
    COALESCE(ad.slug, '')::text AS destination_slug,
    COALESCE(ad.name, '')::text AS destination_name
FROM updated
JOIN notification_records nr ON nr.id = updated.notification_record_id
JOIN organizations o ON o.id = nr.organization_id
LEFT JOIN alert_policies ap ON ap.id = updated.alert_policy_id
    AND ap.organization_id = nr.organization_id
LEFT JOIN alert_destinations ad ON ad.id = updated.destination_id
    AND ad.organization_id = nr.organization_id;

-- name: ListAlertDeliveryAttemptsForRecord :many
SELECT
    ada.*,
    o.slug AS org_slug,
    nr.slug AS notification_record_slug,
    COALESCE(ap.slug, '')::text AS alert_policy_slug,
    COALESCE(ad.slug, '')::text AS destination_slug,
    COALESCE(ad.name, '')::text AS destination_name
FROM alert_delivery_attempts ada
JOIN notification_records nr ON nr.id = ada.notification_record_id
JOIN organizations o ON o.id = nr.organization_id
LEFT JOIN alert_policies ap ON ap.id = ada.alert_policy_id
    AND ap.organization_id = nr.organization_id
LEFT JOIN alert_destinations ad ON ad.id = ada.destination_id
    AND ad.organization_id = nr.organization_id
WHERE o.slug = sqlc.arg('org_slug')
    AND nr.slug = sqlc.arg('record_slug')
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg('order_dir') = 'asc'
                AND (ada.created_at, ada.slug) > (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg('order_dir') = 'desc'
                AND (ada.created_at, ada.slug) < (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN ada.created_at
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN ada.created_at
    END DESC,
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN ada.slug
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN ada.slug
    END DESC
LIMIT sqlc.arg('limit_count');

-- name: ListRecentAlertDeliveryAttemptsForRecords :many
WITH ranked AS (
    SELECT
        ada.*,
        o.slug AS org_slug,
        nr.slug AS notification_record_slug,
        COALESCE(ap.slug, '')::text AS alert_policy_slug,
        COALESCE(ad.slug, '')::text AS destination_slug,
        COALESCE(ad.name, '')::text AS destination_name,
        row_number() OVER (
            PARTITION BY nr.slug
            ORDER BY ada.created_at DESC, ada.slug DESC
        ) AS attempt_rank
    FROM alert_delivery_attempts ada
    JOIN notification_records nr ON nr.id = ada.notification_record_id
    JOIN organizations o ON o.id = nr.organization_id
    LEFT JOIN alert_policies ap ON ap.id = ada.alert_policy_id
        AND ap.organization_id = nr.organization_id
    LEFT JOIN alert_destinations ad ON ad.id = ada.destination_id
        AND ad.organization_id = nr.organization_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND nr.slug = ANY(sqlc.arg('record_slugs')::text[])
)
SELECT *
FROM ranked
WHERE attempt_rank <= sqlc.arg('limit_per_record')::int
ORDER BY notification_record_slug, created_at DESC, slug DESC;

-- name: UpdateNotificationRecordDispatchSummary :one
WITH org_row AS (
    SELECT id
    FROM organizations
    WHERE slug = sqlc.arg('org_slug')
),
destination_row AS (
    SELECT nd.slug
    FROM alert_destinations nd
    JOIN org_row o ON o.id = nd.organization_id
    WHERE nd.slug = sqlc.arg('destination_slug')
),
updated AS (
    UPDATE notification_records nr
    SET
        dispatch_summary = jsonb_set(
            nr.dispatch_summary,
            ARRAY[(SELECT slug FROM destination_row)]::text[],
            jsonb_build_object(
                'status', sqlc.arg('status')::text,
                'queued_at', sqlc.arg('queued_at')::timestamptz,
                'job_kind', sqlc.arg('job_kind')::text,
                'job_queue', sqlc.arg('job_queue')::text,
                'job_state', sqlc.arg('job_state')::text
            ),
            true
        ),
        updated_at = CURRENT_TIMESTAMP
    FROM org_row
    WHERE nr.organization_id = org_row.id
        AND nr.slug = sqlc.arg('record_slug')
        AND EXISTS (SELECT 1 FROM destination_row)
    RETURNING nr.*
)
SELECT
    updated.*,
    COALESCE((SELECT p.slug FROM projects p WHERE p.id = updated.project_id), '')::text AS project_slug,
    COALESCE((SELECT e.slug FROM environments e WHERE e.id = updated.environment_id), '')::text AS environment_slug
FROM updated
;

-- =====================
-- Pipeline Sink Effects
-- =====================

-- name: BeginPipelineSinkEffect :one
WITH scoped_environment AS (
    SELECT pe.id AS project_environment_id
    FROM project_environments pe
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
),
inserted AS (
    INSERT INTO pipeline_sink_effects AS pse (
        project_environment_id,
        run_slug,
        sink_step_slug,
        idempotency_key,
        idempotency_class,
        replay_method,
        replay_decision,
        diagnostic_code,
        diagnostic_summary
    )
    SELECT
        project_environment_id,
        sqlc.arg('replay_identity'),
        sqlc.arg('sink_step_slug'),
        sqlc.arg('idempotency_key'),
        sqlc.arg('idempotency_class'),
        sqlc.arg('replay_method'),
        sqlc.arg('replay_decision'),
        sqlc.narg('diagnostic_code'),
        sqlc.narg('diagnostic_summary')
    FROM scoped_environment
    ON CONFLICT (project_environment_id, run_slug, sink_step_slug, idempotency_key)
    DO UPDATE SET updated_at = pse.updated_at
    RETURNING pse.*, (xmax = 0) AS claimed
)
SELECT * FROM inserted;

-- name: MarkPipelineSinkEffectSucceeded :one
UPDATE pipeline_sink_effects pse
SET
    state = 'succeeded',
    replay_decision = sqlc.arg('replay_decision'),
    diagnostic_code = sqlc.narg('diagnostic_code'),
    diagnostic_summary = sqlc.narg('diagnostic_summary'),
    succeeded_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
FROM project_environments pe
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
WHERE pse.project_environment_id = pe.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pse.run_slug = sqlc.arg('replay_identity')
    AND pse.sink_step_slug = sqlc.arg('sink_step_slug')
    AND pse.idempotency_key = sqlc.arg('idempotency_key')
    AND pse.state = 'in_progress'
RETURNING pse.*;

-- =====================
-- Pipeline Runs
-- =====================

-- name: CreatePipelineRun :one
WITH project_row AS (
    SELECT p.id AS project_id
    FROM projects p
    JOIN organizations o ON p.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
),
project_environment_row AS (
    SELECT pe.id AS project_environment_id, e.slug AS environment_slug
    FROM project_environments pe
    JOIN environments e ON pe.environment_id = e.id
    JOIN project_row p ON pe.project_id = p.project_id
    WHERE e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
),
version_row AS (
    SELECT pv.id, pv.spec_hash
    FROM pipeline_versions pv
    JOIN project_row p ON pv.project_id = p.project_id
    WHERE pv.id = sqlc.narg('pipeline_version_id')::bigint
        AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
)
INSERT INTO pipeline_runs AS pr (
    project_id,
    project_environment_id,
    worker_job_id,
    pipeline_version_id,
    deployment_resolution_digest,
    deployment_id,
    deployment_revision,
    pipeline_version_spec_hash,
    api_version,
    pipeline_slug,
    run_slug,
    request_id,
    source_type,
    source_summary,
    source_entrypoint,
    source_actor_type,
    source_actor_slug,
    source_request_id,
    source_trace_id,
    source_environment_slug,
    source_parameter_keys,
    source_schedule_trigger_slug,
    source_schedule_trigger_revision,
    source_schedule_cron,
    source_schedule_timezone,
    source_schedule_scheduled_fire_at,
    source_schedule_actual_fire_at,
    source_schedule_misfire_result,
    source_webhook_delivery_id,
    status,
    duration_ms,
    step_count,
    step_slug,
    error_summary,
    failure_kind,
    failure_side,
    started_at,
    ended_at,
    replay_of_run_slug,
    replay_from_step,
    source_attempt_number,
    source_root_run_slug,
    source_retry_of_run_slug,
    run_mode,
    checkpoint_status,
    incremental_source_step_slug,
    incremental_blocked_reason,
    replay_safety_status,
    replay_blocked_reason,
    backfill_series_slug,
    backfill_chunk_index,
    backfill_chunk_count,
    incremental_diagnostics,
    timeline
)
SELECT
    (SELECT project_id FROM project_row),
    (SELECT project_environment_id FROM project_environment_row),
    sqlc.narg('worker_job_id')::bigint,
    (SELECT id FROM version_row),
    sqlc.narg('deployment_resolution_digest'),
    sqlc.narg('deployment_id')::bigint,
    sqlc.narg('deployment_revision')::bigint,
    (SELECT spec_hash FROM version_row),
    sqlc.arg('api_version'),
    sqlc.arg('pipeline_slug'),
    sqlc.arg('run_slug'),
    sqlc.narg('request_id'),
    sqlc.arg('source_type'),
    sqlc.arg('source_summary'),
    sqlc.arg('source_entrypoint'),
    sqlc.arg('source_actor_type'),
    sqlc.narg('source_actor_slug'),
    sqlc.narg('source_request_id'),
    sqlc.narg('source_trace_id'),
    (SELECT environment_slug FROM project_environment_row),
    sqlc.arg('source_parameter_keys'),
    sqlc.narg('source_schedule_trigger_slug'),
    sqlc.narg('source_schedule_trigger_revision')::bigint,
    sqlc.narg('source_schedule_cron'),
    sqlc.narg('source_schedule_timezone'),
    sqlc.narg('source_schedule_scheduled_fire_at')::timestamptz,
    sqlc.narg('source_schedule_actual_fire_at')::timestamptz,
    sqlc.narg('source_schedule_misfire_result'),
    sqlc.narg('source_webhook_delivery_id')::bigint,
    sqlc.arg('status'),
    sqlc.arg('duration_ms'),
    sqlc.arg('step_count'),
    sqlc.arg('step_slug'),
    sqlc.narg('error_summary'),
    sqlc.narg('failure_kind'),
    sqlc.narg('failure_side'),
    sqlc.arg('started_at'),
    sqlc.narg('ended_at'),
    sqlc.narg('replay_of_run_slug'),
    sqlc.narg('replay_from_step'),
    sqlc.arg('source_attempt_number'),
    sqlc.arg('source_root_run_slug'),
    sqlc.narg('source_retry_of_run_slug'),
    sqlc.arg('run_mode'),
    sqlc.arg('checkpoint_status'),
    sqlc.narg('incremental_source_step_slug'),
    sqlc.narg('incremental_blocked_reason'),
    sqlc.narg('replay_safety_status'),
    sqlc.narg('replay_blocked_reason'),
    sqlc.narg('backfill_series_slug'),
    sqlc.arg('backfill_chunk_index'),
    sqlc.arg('backfill_chunk_count'),
    sqlc.arg('incremental_diagnostics')::jsonb,
    sqlc.arg('timeline')
WHERE (EXISTS (SELECT 1 FROM version_row) OR
        (sqlc.narg('pipeline_version_id')::bigint IS NULL AND sqlc.arg('status') = 'queued'
            AND EXISTS (SELECT 1 FROM pipeline_specs ps JOIN project_row p ON ps.project_id = p.project_id
                WHERE ps.slug = sqlc.arg('pipeline_slug') AND ps.deleted_at IS NULL)))
    AND EXISTS (SELECT 1 FROM project_environment_row)
-- Preserve the row so duplicate intake can be inspected without aborting its enqueue transaction.
ON CONFLICT (project_environment_id, run_slug) DO UPDATE SET run_slug = pr.run_slug
RETURNING
    (xmax = 0)::boolean AS inserted,
    sqlc.embed(pr),
    (
        SELECT jsonb_build_object(
            'trigger_slug', pt.slug,
            'trigger_revision', pwd.trigger_revision,
            'delivery_id', pwd.delivery_id,
            'payload_digest', pwd.payload_digest,
            'payload_size_bytes', pwd.payload_size_bytes,
            'received_at', pwd.received_at,
            'verification_status', 'verified'
        )
        FROM pipeline_webhook_deliveries pwd
        JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id
            AND pt.project_id = pwd.project_id
        WHERE pwd.id = pr.source_webhook_delivery_id
            AND pwd.project_environment_id = pr.project_environment_id
            AND pwd.pipeline_slug = pr.pipeline_slug
    ) AS source_webhook;

-- name: UpdatePipelineRunSourceEvent :execrows
UPDATE pipeline_runs pr
SET source_event_slug = sqlc.arg('source_event_slug')
FROM project_environments pe
JOIN projects p ON pe.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN environments e ON pe.environment_id = e.id
WHERE
    pr.project_environment_id = pe.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pr.run_slug = sqlc.arg('run_slug');

-- name: MarkPipelineRunRunning :one
-- Completion authority is fenced by the claim owner; retired contexts still need a refusal outcome.
UPDATE pipeline_runs pr
SET
    status = 'running',
    duration_ms = 0,
    started_at = sqlc.arg('started_at')::timestamptz,
    ended_at = NULL,
    timeline = CASE WHEN pr.timeline ? 'structural_checks' THEN pr.timeline ELSE sqlc.arg('timeline')::jsonb END
FROM project_environments pe
JOIN projects p ON pe.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN environments e ON pe.environment_id = e.id
WHERE
    pr.project_environment_id = pe.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND ((e.deleted_at IS NULL AND pe.deleted_at IS NULL) OR pr.source_type = 'completion')
    AND pr.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pr.run_slug = sqlc.arg('run_slug')
    AND pr.status = 'queued'
RETURNING
    sqlc.embed(pr),
    (
        SELECT jsonb_build_object(
            'trigger_slug', pt.slug,
            'trigger_revision', pwd.trigger_revision,
            'delivery_id', pwd.delivery_id,
            'payload_digest', pwd.payload_digest,
            'payload_size_bytes', pwd.payload_size_bytes,
            'received_at', pwd.received_at,
            'verification_status', 'verified'
        )
        FROM pipeline_webhook_deliveries pwd
        JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id
            AND pt.project_id = pwd.project_id
        WHERE pwd.id = pr.source_webhook_delivery_id
            AND pwd.project_environment_id = pr.project_environment_id
            AND pwd.pipeline_slug = pr.pipeline_slug
    ) AS source_webhook;

-- name: CompletePipelineRun :one
UPDATE pipeline_runs pr
SET
    pipeline_version_id = COALESCE(pr.pipeline_version_id, (
        SELECT pv.id FROM pipeline_versions pv WHERE pv.id = sqlc.narg('pipeline_version_id')::bigint
            AND pv.project_id = pr.project_id AND pv.pipeline_slug = pr.pipeline_slug)),
    pipeline_version_spec_hash = COALESCE(pr.pipeline_version_spec_hash, (
        SELECT pv.spec_hash FROM pipeline_versions pv WHERE pv.id = sqlc.narg('pipeline_version_id')::bigint
            AND pv.project_id = pr.project_id AND pv.pipeline_slug = pr.pipeline_slug)),
    deployment_id = COALESCE(pr.deployment_id, sqlc.narg('deployment_id')::bigint),
    deployment_revision = COALESCE(pr.deployment_revision, sqlc.narg('deployment_revision')::bigint),
    deployment_resolution_digest = COALESCE(pr.deployment_resolution_digest, sqlc.narg('deployment_resolution_digest')::text),
    status = sqlc.arg('status'),
    duration_ms = sqlc.arg('duration_ms'),
    step_count = sqlc.arg('step_count'),
    step_slug = sqlc.arg('step_slug'),
    error_summary = sqlc.narg('error_summary'),
    failure_kind = sqlc.narg('failure_kind'),
    failure_side = sqlc.narg('failure_side'),
    started_at = sqlc.arg('started_at')::timestamptz,
    ended_at = sqlc.narg('ended_at')::timestamptz,
    replay_of_run_slug = sqlc.narg('replay_of_run_slug'),
    replay_from_step = sqlc.narg('replay_from_step'),
    run_mode = sqlc.arg('run_mode'),
    checkpoint_status = sqlc.arg('checkpoint_status'),
    incremental_source_step_slug = sqlc.narg('incremental_source_step_slug'),
    incremental_blocked_reason = sqlc.narg('incremental_blocked_reason'),
    replay_safety_status = sqlc.narg('replay_safety_status'),
    replay_blocked_reason = sqlc.narg('replay_blocked_reason'),
    backfill_series_slug = sqlc.narg('backfill_series_slug'),
    backfill_chunk_index = sqlc.arg('backfill_chunk_index'),
    backfill_chunk_count = sqlc.arg('backfill_chunk_count'),
    incremental_diagnostics = sqlc.arg('incremental_diagnostics')::jsonb,
    timeline = CASE WHEN pr.timeline ? 'execution_context'
        THEN sqlc.arg('timeline')::jsonb || jsonb_build_object('execution_context', pr.timeline->'execution_context')
        ELSE sqlc.arg('timeline')::jsonb END
        || CASE WHEN pr.timeline ? 'worker_observed_at' THEN jsonb_build_object(
            'worker_observed_at', GREATEST((pr.timeline->>'worker_observed_at')::timestamptz,
                (sqlc.arg('timeline')::jsonb->>'worker_observed_at')::timestamptz)) ELSE '{}'::jsonb END
FROM project_environments pe
JOIN projects p ON pe.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN environments e ON pe.environment_id = e.id
WHERE
    pr.project_environment_id = pe.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND ((e.deleted_at IS NULL AND pe.deleted_at IS NULL) OR pr.source_type = 'completion')
    AND pr.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pr.run_slug = sqlc.arg('run_slug')
    AND pr.status IN ('queued', 'running')
    AND (sqlc.narg('pipeline_version_id')::bigint IS NULL OR pr.pipeline_version_id IS NULL
        OR pr.pipeline_version_id = sqlc.narg('pipeline_version_id')::bigint)
    AND (sqlc.narg('pipeline_version_id')::bigint IS NULL OR EXISTS (
        SELECT 1 FROM pipeline_versions pv WHERE pv.id = sqlc.narg('pipeline_version_id')::bigint
            AND pv.project_id = p.id AND pv.pipeline_slug = pr.pipeline_slug))
    AND (pr.deployment_id IS NULL OR sqlc.narg('deployment_id')::bigint IS NULL
        OR pr.deployment_id = sqlc.narg('deployment_id')::bigint)
    AND (pr.deployment_revision IS NULL OR sqlc.narg('deployment_revision')::bigint IS NULL
        OR pr.deployment_revision = sqlc.narg('deployment_revision')::bigint)
    AND (pr.deployment_resolution_digest IS NULL OR sqlc.narg('deployment_resolution_digest')::text IS NULL
        OR pr.deployment_resolution_digest = sqlc.narg('deployment_resolution_digest')::text)
    -- A revision is an attempt-local compare-and-swap fence, including terminal writes.
    AND (COALESCE((sqlc.arg('timeline')::jsonb->>'revision')::bigint, 0)
            = COALESCE((pr.timeline->>'revision')::bigint, 0) + 1
        OR (NOT (pr.timeline ? 'revision') AND NOT (sqlc.arg('timeline')::jsonb ? 'revision')))
    AND (NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF(pr.timeline->'events', 'null'::jsonb), '[]'::jsonb)) WITH ORDINALITY AS prior(event, position)
        WHERE prior.event IS DISTINCT FROM sqlc.arg('timeline')::jsonb->'events'->(prior.position::int - 1)
    ))
    AND (NOT (pr.timeline ? 'revision') OR NOT EXISTS (
        SELECT 1 FROM jsonb_each(pr.timeline) AS identity(key, value)
        WHERE identity.key IN ('compiled_plan_digest', 'deployment_resolution_digest')
            AND identity.value IS DISTINCT FROM sqlc.arg('timeline')::jsonb->identity.key
    ))
    AND (NOT (pr.timeline ? 'execution_context')
        OR NOT (sqlc.arg('timeline')::jsonb ? 'execution_context')
        OR pr.timeline->'execution_context' = sqlc.arg('timeline')::jsonb->'execution_context')
RETURNING
    sqlc.embed(pr),
    (
        SELECT jsonb_build_object(
            'trigger_slug', pt.slug,
            'trigger_revision', pwd.trigger_revision,
            'delivery_id', pwd.delivery_id,
            'payload_digest', pwd.payload_digest,
            'payload_size_bytes', pwd.payload_size_bytes,
            'received_at', pwd.received_at,
            'verification_status', 'verified'
        )
        FROM pipeline_webhook_deliveries pwd
        JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id
            AND pt.project_id = pwd.project_id
        WHERE pwd.id = pr.source_webhook_delivery_id
            AND pwd.project_environment_id = pr.project_environment_id
            AND pwd.pipeline_slug = pr.pipeline_slug
    ) AS source_webhook;

-- name: ListPipelineRunsByProject :many
-- Historical reads intentionally do not filter environments.deleted_at.
-- Deleting an environment catalog row must not hide persisted run/audit history.
SELECT
    sqlc.embed(pr),
    (
        SELECT jsonb_build_object(
            'trigger_slug', pt.slug,
            'trigger_revision', pwd.trigger_revision,
            'delivery_id', pwd.delivery_id,
            'payload_digest', pwd.payload_digest,
            'payload_size_bytes', pwd.payload_size_bytes,
            'received_at', pwd.received_at,
            'verification_status', 'verified'
        )
        FROM pipeline_webhook_deliveries pwd
        JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id
            AND pt.project_id = pwd.project_id
        WHERE pwd.id = pr.source_webhook_delivery_id
            AND pwd.project_environment_id = pr.project_environment_id
            AND pwd.pipeline_slug = pr.pipeline_slug
    ) AS source_webhook
FROM
    pipeline_runs pr
    JOIN project_environments pe ON pr.project_environment_id = pe.id
    JOIN projects p ON pe.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN environments e ON pe.environment_id = e.id
    JOIN pipeline_specs ps ON ps.project_id = pe.project_id
        AND ps.slug = pr.pipeline_slug
        AND ps.deleted_at IS NULL
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND (
        sqlc.narg ('status')::text IS NULL
        OR pr.status = sqlc.narg ('status')::text
    )
    AND (
        sqlc.narg ('pipeline_slug')::text IS NULL
        OR pr.pipeline_slug = sqlc.narg ('pipeline_slug')::text
    )
    AND (
        sqlc.narg ('run_mode')::text IS NULL
        OR pr.run_mode = sqlc.narg ('run_mode')::text
    )
    AND (
        sqlc.narg ('backfill_series_slug')::text IS NULL
        OR pr.backfill_series_slug = sqlc.narg ('backfill_series_slug')::text
    )
    AND (
        sqlc.narg ('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg ('order_dir') = 'asc'
                AND (pr.created_at, pr.run_slug) > (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg ('order_dir') = 'desc'
                AND (pr.created_at, pr.run_slug) < (
                    sqlc.narg ('cursor_timestamp')::timestamptz,
                    sqlc.narg ('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN pr.created_at
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN pr.created_at
    END DESC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'asc' THEN pr.run_slug
    END ASC,
    CASE
        WHEN sqlc.arg ('order_dir') = 'desc' THEN pr.run_slug
    END DESC
LIMIT sqlc.arg ('limit_count');

-- name: FindPipelineRunByRunSlug :one
-- Historical reads intentionally do not filter environments.deleted_at.
-- Deleting an environment catalog row must not hide persisted run/audit history.
SELECT
    sqlc.embed(pr),
    (
        SELECT jsonb_build_object(
            'trigger_slug', pt.slug,
            'trigger_revision', pwd.trigger_revision,
            'delivery_id', pwd.delivery_id,
            'payload_digest', pwd.payload_digest,
            'payload_size_bytes', pwd.payload_size_bytes,
            'received_at', pwd.received_at,
            'verification_status', 'verified'
        )
        FROM pipeline_webhook_deliveries pwd
        JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id
            AND pt.project_id = pwd.project_id
        WHERE pwd.id = pr.source_webhook_delivery_id
            AND pwd.project_environment_id = pr.project_environment_id
            AND pwd.pipeline_slug = pr.pipeline_slug
    ) AS source_webhook
FROM
    pipeline_runs pr
    JOIN project_environments pe ON pr.project_environment_id = pe.id
    JOIN projects p ON pe.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN environments e ON pe.environment_id = e.id
    JOIN pipeline_specs ps ON ps.project_id = pe.project_id
        AND ps.slug = pr.pipeline_slug
        AND (ps.deleted_at IS NULL OR pr.source_type = 'completion')
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND pr.run_slug = sqlc.arg ('run_slug');

-- name: FindLatestPipelineRunByPipelineSlug :one
-- Historical reads intentionally do not filter environments.deleted_at.
-- Deleting an environment catalog row must not hide persisted run/audit history.
SELECT
    sqlc.embed(pr),
    (
        SELECT jsonb_build_object(
            'trigger_slug', pt.slug,
            'trigger_revision', pwd.trigger_revision,
            'delivery_id', pwd.delivery_id,
            'payload_digest', pwd.payload_digest,
            'payload_size_bytes', pwd.payload_size_bytes,
            'received_at', pwd.received_at,
            'verification_status', 'verified'
        )
        FROM pipeline_webhook_deliveries pwd
        JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id
            AND pt.project_id = pwd.project_id
        WHERE pwd.id = pr.source_webhook_delivery_id
            AND pwd.project_environment_id = pr.project_environment_id
            AND pwd.pipeline_slug = pr.pipeline_slug
    ) AS source_webhook
FROM
    pipeline_runs pr
    JOIN project_environments pe ON pr.project_environment_id = pe.id
    JOIN projects p ON pe.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN environments e ON pe.environment_id = e.id
    JOIN pipeline_specs ps ON ps.project_id = pe.project_id
        AND ps.slug = pr.pipeline_slug
        AND ps.deleted_at IS NULL
WHERE
    o.slug = sqlc.arg ('org_slug')
    AND p.slug = sqlc.arg ('project_slug')
    AND e.slug = sqlc.arg ('environment_slug')
    AND pr.pipeline_slug = sqlc.arg ('pipeline_slug')
ORDER BY pr.created_at DESC
LIMIT 1;

-- name: SaveCheckpoint :one
-- Checkpoint writes are active runtime operations, so they require an active
-- environment catalog row and active project-environment enablement.
WITH project_environment_row AS (
    SELECT pe.id AS project_environment_id
    FROM project_environments pe
    JOIN projects p ON pe.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    JOIN environments e ON pe.environment_id = e.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
),
run_row AS (
    SELECT pr.project_environment_id
    FROM pipeline_runs pr
    JOIN project_environment_row pe ON pr.project_environment_id = pe.project_environment_id
    WHERE pr.run_slug = sqlc.arg('run_slug')
)
INSERT INTO pipeline_checkpoint (
    project_environment_id,
    run_slug,
    step_slug,
    pipeline_spec,
    step_results,
    incremental_source_windows,
    compiled_plan,
    compiled_plan_digest,
    deployment_plan,
    deployment_resolution_digest
)
SELECT
    (SELECT project_environment_id FROM run_row),
    sqlc.arg('run_slug'),
    sqlc.arg('step_slug'),
    sqlc.arg('pipeline_spec')::jsonb,
    sqlc.arg('step_results')::jsonb,
    sqlc.arg('incremental_source_windows')::jsonb,
    sqlc.narg('compiled_plan'),
    sqlc.narg('compiled_plan_digest'),
    sqlc.narg('deployment_plan'),
    sqlc.narg('deployment_resolution_digest')
WHERE EXISTS (SELECT 1 FROM run_row)
ON CONFLICT (project_environment_id, run_slug) DO UPDATE
    SET step_slug = sqlc.arg('step_slug'),
        pipeline_spec = sqlc.arg('pipeline_spec')::jsonb,
        step_results = sqlc.arg('step_results')::jsonb,
        incremental_source_windows = sqlc.arg('incremental_source_windows')::jsonb,
        compiled_plan = sqlc.narg('compiled_plan'),
        compiled_plan_digest = sqlc.narg('compiled_plan_digest'),
        deployment_plan = sqlc.narg('deployment_plan'),
        deployment_resolution_digest = sqlc.narg('deployment_resolution_digest'),
        updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: GetCheckpoint :one
-- Historical reads intentionally do not filter environments.deleted_at.
-- Deleting an environment catalog row must not hide persisted replay history.
SELECT pc.*
FROM pipeline_checkpoint pc
JOIN project_environments pe ON pc.project_environment_id = pe.id
JOIN projects p ON pe.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN environments e ON pe.environment_id = e.id
JOIN pipeline_runs pr ON pr.project_environment_id = pc.project_environment_id
    AND pr.run_slug = pc.run_slug
JOIN pipeline_specs ps ON ps.project_id = pe.project_id
    AND ps.slug = pr.pipeline_slug
    AND ps.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND pc.run_slug = sqlc.arg('run_slug');

-- name: DeleteExpiredOrganizationReplayCheckpoints :execrows
DELETE FROM pipeline_checkpoint pc
USING project_environments pe, projects p, organizations o
WHERE pc.project_environment_id = pe.id
    AND pe.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND pc.created_at < sqlc.arg('cutoff');

-- name: DeleteExpiredOrganizationPipelineRuns :execrows
DELETE FROM pipeline_runs pr
USING project_environments pe, projects p, organizations o
WHERE pr.project_environment_id = pe.id
    AND pe.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND pr.ended_at IS NOT NULL
    AND pr.ended_at < sqlc.arg('cutoff')
    AND NOT EXISTS (SELECT 1 FROM pipeline_completion_snapshots snapshot
        WHERE snapshot.project_environment_id = pr.project_environment_id
            AND snapshot.root_run_slug = COALESCE(NULLIF(pr.source_root_run_slug, ''), pr.run_slug)
            AND snapshot.completed_at IS NULL)
    AND NOT EXISTS (
        SELECT 1
        FROM pipeline_run_terminal_obligations obligation
        LEFT JOIN pipeline_run_logical_closures closure
            ON closure.project_environment_id = obligation.project_environment_id
            AND closure.root_run_slug = obligation.root_run_slug
        WHERE obligation.project_environment_id = pr.project_environment_id
            AND obligation.root_run_slug = COALESCE(NULLIF(pr.source_root_run_slug, ''), pr.run_slug)
            AND NOT obligation.excluded
            AND closure.id IS NULL
    );


-- name: ListPendingRunFinalizationsForSystem :many
WITH owners AS (
    SELECT project_environment_id, pipeline_trigger_id, pipeline_slug, fire_slug AS run_slug,
        trigger_revision, scheduled_fire_at AS scheduled_at, ''::text AS retry_attempt_slug,
        ''::text AS receiver_id, ''::text AS delivery_id, 0::integer AS attempt_number
    FROM pipeline_schedule_fires WHERE status = 'running'
    UNION ALL
    SELECT project_environment_id, pipeline_trigger_id, pipeline_slug, run_slug,
        trigger_revision, scheduled_at, attempt_slug, '', '', 0
    FROM pipeline_trigger_retry_attempts WHERE status = 'running' AND source_type <> 'completion'
    UNION ALL
    SELECT project_environment_id, pipeline_trigger_id, pipeline_slug,
        CASE WHEN attempt_number = 1 THEN run_slug ELSE run_slug || '-retry-' || attempt_number::text END,
        trigger_revision, COALESCE(next_retry_at, received_at), '', receiver_id, delivery_id, attempt_number
    FROM pipeline_webhook_deliveries WHERE status = 'running'
        AND encrypted_envelope IS NOT NULL AND envelope_expires_at > NOW()
)
SELECT o.slug AS org_slug, p.slug AS project_slug, e.slug AS environment_slug,
    pt.slug AS trigger_slug, owners.trigger_revision, owners.scheduled_at,
    owners.retry_attempt_slug, owners.receiver_id, owners.delivery_id, owners.attempt_number
FROM owners
JOIN pipeline_run_terminal_obligations obligation
    ON obligation.project_environment_id = owners.project_environment_id
    AND obligation.attempt_run_slug = owners.run_slug
JOIN project_environments pe ON pe.id = owners.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id AND o.id = obligation.organization_id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
JOIN pipeline_triggers pt ON pt.id = owners.pipeline_trigger_id AND pt.project_environment_id = pe.id
JOIN pipeline_specs ps ON ps.project_id = p.id AND ps.slug = owners.pipeline_slug AND ps.deleted_at IS NULL
WHERE pe.deleted_at IS NULL AND e.deleted_at IS NULL
    AND NOT obligation.outcome_uncertain AND (NOT obligation.excluded OR obligation.exclusion_reason = 'canceled')
ORDER BY obligation.id
LIMIT 100;

-- name: ReconcilePipelineRunFinalizationsForSystem :exec
WITH eligible AS (
    SELECT obligation.*,
        COALESCE(retry.decision_reason, delivery.decision_reason) AS reason
    FROM pipeline_run_terminal_obligations obligation
    LEFT JOIN pipeline_trigger_retry_attempts retry
        ON retry.project_environment_id = obligation.project_environment_id
        AND retry.root_run_slug = obligation.root_run_slug
        AND retry.retry_of_run_slug = obligation.attempt_run_slug
        AND retry.status IN ('skipped', 'exhausted')
    LEFT JOIN pipeline_runs run
        ON run.project_environment_id = obligation.project_environment_id
        AND run.run_slug = obligation.attempt_run_slug
    LEFT JOIN pipeline_webhook_deliveries delivery
        ON delivery.id = run.source_webhook_delivery_id
        AND delivery.project_environment_id = obligation.project_environment_id
        AND delivery.attempt_number = run.source_attempt_number
        AND delivery.status = 'failed'
        AND delivery.decision_reason IS DISTINCT FROM 'retryable_failure_kind'
    WHERE obligation.resolved_at IS NULL AND NOT obligation.excluded AND NOT obligation.outcome_uncertain
        AND obligation.next_evaluation_at <= clock_timestamp()
        AND (obligation.attempt_status = 'succeeded'
            OR (obligation.attempt_status = 'failed' AND
                (obligation.source_type IN ('manual', 'api') OR retry.id IS NOT NULL OR delivery.id IS NOT NULL)))
        AND NOT EXISTS (
            SELECT 1 FROM pipeline_run_logical_closures closure
            WHERE closure.project_environment_id = obligation.project_environment_id
                AND closure.root_run_slug = obligation.root_run_slug
        )
    ORDER BY obligation.next_evaluation_at, obligation.id
    LIMIT 100
), locked AS MATERIALIZED (
    SELECT eligible.*, pg_advisory_xact_lock(hashtextextended(
        project_environment_id::text || ':' || root_run_slug, 0))
    FROM eligible
)
INSERT INTO pipeline_run_logical_closures (
    organization_id, project_environment_id, root_run_slug,
    final_attempt_run_slug, status, decision_reason
)
SELECT organization_id, project_environment_id, root_run_slug,
    attempt_run_slug, attempt_status, COALESCE(reason,
        CASE WHEN attempt_status = 'succeeded' THEN 'succeeded'
            WHEN source_type IN ('manual', 'api') THEN 'no_automatic_retry' ELSE 'final_retry_decision' END)
FROM locked
ON CONFLICT (project_environment_id, root_run_slug) DO NOTHING;

-- name: GetCurrentIncrementalCheckpoint :one
SELECT pic.*
FROM pipeline_incremental_checkpoints pic
JOIN projects p ON p.id = pic.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN project_environments pe ON pe.id = pic.project_environment_id
    AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id
JOIN pipeline_specs ps ON ps.project_id = pic.project_id
    AND ps.slug = pic.pipeline_slug
    AND ps.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pic.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pic.source_step_slug = sqlc.arg('source_step_slug')
    AND pic.checkpoint_namespace = sqlc.arg('checkpoint_namespace')
    AND pic.operation_identity = sqlc.arg('operation_identity')
    AND pic.commit_status = 'committed'
    AND pic.superseded_at IS NULL
    AND pic.expired_at IS NULL
ORDER BY pic.committed_at DESC, pic.created_at DESC, pic.id DESC
LIMIT 1;

-- name: EnsureIncrementalCheckpointCommitLock :exec
WITH target AS (
    SELECT
        p.id AS project_id,
        pe.id AS project_environment_id
    FROM projects p
    JOIN organizations o ON o.id = p.organization_id
    JOIN project_environments pe ON pe.project_id = p.id
    JOIN environments e ON e.id = pe.environment_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
)
INSERT INTO pipeline_incremental_checkpoint_locks (
    project_id,
    project_environment_id,
    pipeline_slug,
    source_step_slug,
    checkpoint_namespace,
    operation_identity
)
SELECT
    target.project_id,
    target.project_environment_id,
    sqlc.arg('pipeline_slug'),
    sqlc.arg('source_step_slug'),
    sqlc.arg('checkpoint_namespace'),
    sqlc.arg('operation_identity')
FROM target
ON CONFLICT (
    project_environment_id,
    pipeline_slug,
    source_step_slug,
    checkpoint_namespace,
    operation_identity
) DO NOTHING;

-- name: LockIncrementalCheckpointCommit :one
SELECT lock_row.id
FROM pipeline_incremental_checkpoint_locks lock_row
JOIN projects p ON p.id = lock_row.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN project_environments pe ON pe.id = lock_row.project_environment_id
    AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id
WHERE lock_row.pipeline_slug = sqlc.arg('pipeline_slug')
    AND lock_row.source_step_slug = sqlc.arg('source_step_slug')
    AND lock_row.checkpoint_namespace = sqlc.arg('checkpoint_namespace')
    AND lock_row.operation_identity = sqlc.arg('operation_identity')
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
FOR UPDATE OF lock_row;

-- name: SupersedeCurrentIncrementalCheckpoint :exec
UPDATE pipeline_incremental_checkpoints pic
SET commit_status = 'superseded',
    superseded_at = sqlc.arg('superseded_at')
FROM projects p, organizations o, project_environments pe, environments e, pipeline_specs ps
WHERE pic.project_id = p.id
    AND p.organization_id = o.id
    AND pic.project_environment_id = pe.id
    AND pe.project_id = p.id
    AND pe.environment_id = e.id
    AND ps.project_id = pic.project_id
    AND ps.slug = pic.pipeline_slug
    AND ps.deleted_at IS NULL
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pic.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pic.source_step_slug = sqlc.arg('source_step_slug')
    AND pic.checkpoint_namespace = sqlc.arg('checkpoint_namespace')
    AND pic.operation_identity = sqlc.arg('operation_identity')
    AND pic.commit_status = 'committed'
    AND pic.superseded_at IS NULL
    AND pic.expired_at IS NULL;

-- name: InsertCommittedIncrementalCheckpoint :one
WITH target AS (
    SELECT
        p.id AS project_id,
        pe.id AS project_environment_id
    FROM projects p
    JOIN organizations o ON p.organization_id = o.id
    JOIN project_environments pe ON pe.project_id = p.id
    JOIN environments e ON e.id = pe.environment_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
),
version_row AS (
    SELECT pv.id AS pipeline_version_id
    FROM pipeline_versions pv
    JOIN target ON target.project_id = pv.project_id
    JOIN pipeline_specs ps ON ps.project_id = pv.project_id
        AND ps.slug = pv.pipeline_slug
        AND ps.deleted_at IS NULL
    WHERE pv.id = sqlc.arg('pipeline_version_id')
        AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
)
INSERT INTO pipeline_incremental_checkpoints (
    project_id,
    project_environment_id,
    pipeline_slug,
    pipeline_version_id,
    source_step_slug,
    checkpoint_namespace,
    checkpoint_kind,
    data_source_slug,
    operation_type,
    operation_ref,
    operation_inline_hash,
    operation_identity,
    observed_lower_bound,
    observed_upper_bound,
    display_metadata,
    commit_status,
    observed_run_slug,
    committing_run_slug,
    observed_at,
    committed_at
)
SELECT
    target.project_id,
    target.project_environment_id,
    sqlc.arg('pipeline_slug'),
    version_row.pipeline_version_id,
    sqlc.arg('source_step_slug'),
    sqlc.arg('checkpoint_namespace'),
    sqlc.arg('checkpoint_kind'),
    sqlc.arg('data_source_slug'),
    sqlc.arg('operation_type'),
    sqlc.narg('operation_ref'),
    sqlc.narg('operation_inline_hash'),
    sqlc.arg('operation_identity'),
    sqlc.arg('observed_lower_bound')::jsonb,
    sqlc.arg('observed_upper_bound')::jsonb,
    sqlc.arg('display_metadata')::jsonb,
    'committed',
    sqlc.arg('observed_run_slug'),
    sqlc.arg('committing_run_slug'),
    sqlc.arg('observed_at'),
    sqlc.arg('committed_at')
FROM target, version_row
RETURNING *;

-- name: BootstrapIncrementalCheckpoint :one
WITH target AS (
    SELECT
        p.id AS project_id,
        pe.id AS project_environment_id
    FROM projects p
    JOIN organizations o ON p.organization_id = o.id
    JOIN project_environments pe ON pe.project_id = p.id
    JOIN environments e ON e.id = pe.environment_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
),
version_row AS (
    SELECT pv.id AS pipeline_version_id
    FROM pipeline_versions pv
    JOIN target ON target.project_id = pv.project_id
    JOIN pipeline_specs ps ON ps.project_id = pv.project_id
        AND ps.slug = pv.pipeline_slug
        AND ps.deleted_at IS NULL
    WHERE pv.id = sqlc.arg('pipeline_version_id')
        AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
)
INSERT INTO pipeline_incremental_checkpoints (
    project_id,
    project_environment_id,
    pipeline_slug,
    pipeline_version_id,
    source_step_slug,
    checkpoint_namespace,
    checkpoint_kind,
    data_source_slug,
    operation_type,
    operation_ref,
    operation_inline_hash,
    operation_identity,
    observed_lower_bound,
    observed_upper_bound,
    display_metadata,
    commit_status,
    observed_run_slug,
    committing_run_slug,
    observed_at,
    committed_at
)
SELECT
    target.project_id,
    target.project_environment_id,
    sqlc.arg('pipeline_slug'),
    version_row.pipeline_version_id,
    sqlc.arg('source_step_slug'),
    sqlc.arg('checkpoint_namespace'),
    sqlc.arg('checkpoint_kind'),
    sqlc.arg('data_source_slug'),
    sqlc.arg('operation_type'),
    sqlc.narg('operation_ref'),
    sqlc.narg('operation_inline_hash'),
    sqlc.arg('operation_identity'),
    sqlc.arg('observed_lower_bound')::jsonb,
    sqlc.arg('observed_upper_bound')::jsonb,
    sqlc.arg('display_metadata')::jsonb,
    'committed',
    sqlc.arg('observed_run_slug'),
    sqlc.arg('committing_run_slug'),
    sqlc.arg('observed_at'),
    sqlc.arg('committed_at')
FROM target, version_row
ON CONFLICT (
    project_environment_id,
    pipeline_slug,
    source_step_slug,
    checkpoint_namespace,
    operation_identity
) WHERE commit_status = 'committed'
    AND superseded_at IS NULL
    AND expired_at IS NULL
DO NOTHING
RETURNING *;

-- name: InsertBlockedIncrementalCheckpoint :one
WITH target AS (
    SELECT
        p.id AS project_id,
        pe.id AS project_environment_id
    FROM projects p
    JOIN organizations o ON p.organization_id = o.id
    JOIN project_environments pe ON pe.project_id = p.id
    JOIN environments e ON e.id = pe.environment_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
),
version_row AS (
    SELECT pv.id AS pipeline_version_id
    FROM pipeline_versions pv
    JOIN target ON target.project_id = pv.project_id
    JOIN pipeline_specs ps ON ps.project_id = pv.project_id
        AND ps.slug = pv.pipeline_slug
        AND ps.deleted_at IS NULL
    WHERE pv.id = sqlc.arg('pipeline_version_id')
        AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
)
INSERT INTO pipeline_incremental_checkpoints (
    project_id,
    project_environment_id,
    pipeline_slug,
    pipeline_version_id,
    source_step_slug,
    checkpoint_namespace,
    checkpoint_kind,
    data_source_slug,
    operation_type,
    operation_ref,
    operation_inline_hash,
    operation_identity,
    observed_lower_bound,
    observed_upper_bound,
    display_metadata,
    commit_status,
    observed_run_slug,
    blocked_reason,
    observed_at,
    blocked_at
)
SELECT
    target.project_id,
    target.project_environment_id,
    sqlc.arg('pipeline_slug'),
    version_row.pipeline_version_id,
    sqlc.arg('source_step_slug'),
    sqlc.arg('checkpoint_namespace'),
    sqlc.arg('checkpoint_kind'),
    sqlc.arg('data_source_slug'),
    sqlc.arg('operation_type'),
    sqlc.narg('operation_ref'),
    sqlc.narg('operation_inline_hash'),
    sqlc.arg('operation_identity'),
    sqlc.arg('observed_lower_bound')::jsonb,
    sqlc.arg('observed_upper_bound')::jsonb,
    sqlc.arg('display_metadata')::jsonb,
    'blocked',
    sqlc.arg('observed_run_slug'),
    sqlc.arg('blocked_reason'),
    sqlc.arg('observed_at'),
    sqlc.arg('blocked_at')
FROM target, version_row
RETURNING *;

-- name: GetRunTimeline :one
-- Historical reads intentionally do not filter environments.deleted_at.
-- Deleting an environment catalog row must not hide persisted diagnostics.
SELECT timeline
FROM pipeline_runs pr
JOIN project_environments pe ON pr.project_environment_id = pe.id
JOIN projects p ON pe.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN environments e ON pe.environment_id = e.id
JOIN pipeline_specs ps ON ps.project_id = pe.project_id
    AND ps.slug = pr.pipeline_slug
    AND ps.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND pr.run_slug = sqlc.arg('run_slug')
LIMIT 1;
-- name: UpsertPipelineReference :one
WITH project_row AS (
    SELECT p.id AS project_id
    FROM projects p
    JOIN organizations o ON p.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
)
INSERT INTO pipeline_references (
    project_id,
    slug,
    name,
    updated_at
)
SELECT
    project_id,
    sqlc.arg('slug'),
    sqlc.arg('name'),
    NOW()
FROM project_row
ON CONFLICT (project_id, slug)
DO UPDATE SET
    name = EXCLUDED.name,
    updated_at = NOW()
RETURNING *;

-- name: FindPipelineReferenceBySlug :one
SELECT pr.*
FROM pipeline_references pr
JOIN projects p ON pr.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pr.slug = sqlc.arg('slug');

-- name: ListPipelineReferencesByProject :many
SELECT pr.*
FROM pipeline_references pr
JOIN projects p ON pr.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
ORDER BY pr.created_at DESC;

-- name: DeletePipelineReference :execresult
DELETE FROM pipeline_references pr
USING projects p, organizations o
WHERE pr.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pr.slug = sqlc.arg('slug');

-- name: SetPipelineReferenceHasDraft :execresult
UPDATE pipeline_references pr
SET has_draft = sqlc.arg('has_draft')
FROM projects p, organizations o
WHERE pr.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pr.slug = sqlc.arg('pipeline_slug');

-- name: UpsertPipelineDraft :one
WITH project_row AS (
    SELECT p.id AS project_id, p.organization_id
    FROM projects p
    JOIN organizations o ON p.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
),
version_row AS (
    SELECT pv.id
    FROM pipeline_versions pv
    JOIN project_row p ON pv.project_id = p.project_id
    WHERE pv.id = sqlc.narg('based_on_version_id')
        AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
)
INSERT INTO pipeline_drafts (
    organization_id,
    project_id,
    pipeline_slug,
    member_id,
    based_on_version_id,
    spec_jsonb,
    draft_state,
    updated_at
)
SELECT
    p.organization_id,
    p.project_id,
    sqlc.arg('pipeline_slug'),
    (
        SELECT om.id
        FROM organization_members om
        JOIN users u ON om.user_id = u.id
        WHERE om.organization_id = p.organization_id
            AND u.slug = sqlc.arg('member_slug')
    ),
    CASE
        WHEN sqlc.narg('based_on_version_id')::bigint IS NULL THEN NULL
        ELSE (SELECT id FROM version_row)
    END,
    sqlc.arg('spec_jsonb'),
    sqlc.arg('draft_state'),
    NOW()
FROM project_row p
JOIN pipeline_specs ps ON ps.project_id = p.project_id
    AND ps.slug = sqlc.arg('pipeline_slug')
    AND ps.deleted_at IS NULL
WHERE sqlc.narg('based_on_version_id')::bigint IS NULL
    OR EXISTS (SELECT 1 FROM version_row)
ON CONFLICT (project_id, pipeline_slug, member_id)
DO UPDATE SET
    based_on_version_id = EXCLUDED.based_on_version_id,
    spec_jsonb = EXCLUDED.spec_jsonb,
    draft_state = EXCLUDED.draft_state,
    failed_at = NULL,
    failure_code = NULL,
    failure_message = NULL,
    updated_at = NOW()
RETURNING *;

-- name: FindPipelineDraftBySlug :one
SELECT pd.*, pv.spec_hash AS based_on_spec_hash, ps.current_version_id
FROM pipeline_drafts pd
JOIN projects p ON pd.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN pipeline_specs ps ON ps.slug = pd.pipeline_slug
    AND ps.project_id = pd.project_id
    AND ps.deleted_at IS NULL
LEFT JOIN pipeline_versions pv ON pv.id = pd.based_on_version_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pd.member_id = (
        SELECT om.id
        FROM organization_members om
        JOIN users u ON om.user_id = u.id
        WHERE om.organization_id = p.organization_id
            AND u.slug = sqlc.arg('member_slug')
    );

-- name: FindAnyPipelineDraftBySlug :one
SELECT pd.*, pv.spec_hash AS based_on_spec_hash, ps.current_version_id
FROM pipeline_drafts pd
JOIN projects p ON pd.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN pipeline_specs ps ON ps.slug = pd.pipeline_slug
    AND ps.project_id = pd.project_id
    AND ps.deleted_at IS NULL
LEFT JOIN pipeline_versions pv ON pv.id = pd.based_on_version_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
ORDER BY pd.updated_at DESC
LIMIT 1;

-- name: FindPipelineProposalAuthority :one
SELECT
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
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND ds.deleted_at IS NULL
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

-- name: QueuePipelineDraftPublish :execresult
UPDATE pipeline_drafts pd
SET
    draft_state = sqlc.arg('draft_state'),
    base_ref = sqlc.narg('base_ref'),
    base_commit_sha = sqlc.narg('base_commit_sha'),
    branch_name = sqlc.arg('branch_name'),
    publish_attempt_id = sqlc.arg('publish_attempt_id'),
    proposal_authority_id = sqlc.arg('proposal_authority_id'),
    publication_mode = sqlc.arg('publication_mode'),
    provider_host = sqlc.arg('provider_host'),
    provider_account_id = sqlc.arg('provider_account_id'),
    provider_repository_id = sqlc.arg('provider_repository_id'),
    provider_credential_id = sqlc.arg('provider_credential_id'),
    executing_principal_id = sqlc.arg('executing_principal_id'),
    executing_principal_display = sqlc.arg('executing_principal_display'),
    authority_source = sqlc.arg('authority_source'),
    queued_at = NOW(),
    failed_at = NULL,
    failure_code = NULL,
    failure_message = NULL,
    updated_at = NOW()
FROM projects p, organizations o
WHERE pd.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pd.member_id = (
        SELECT om.id
        FROM organization_members om
        JOIN users u ON om.user_id = u.id
        WHERE om.organization_id = p.organization_id
            AND u.slug = sqlc.arg('member_slug')
    );

-- name: CompletePipelineDraftPublish :one
WITH target AS (
    SELECT pd.id, pd.publish_attempt_id
    FROM pipeline_drafts pd
    JOIN projects p ON pd.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
        AND pd.member_id = (
            SELECT om.id
            FROM organization_members om
            JOIN users u ON om.user_id = u.id
            WHERE om.organization_id = p.organization_id
                AND u.slug = sqlc.arg('member_slug')
        )
), updated AS (
UPDATE pipeline_drafts pd
SET
    draft_state = sqlc.arg('draft_state'),
    branch_name = sqlc.arg('branch_name'),
    branch_head_sha = sqlc.narg('branch_head_sha'),
    proposal_provider = sqlc.arg('proposal_provider'),
    proposal_id = sqlc.arg('proposal_id'),
    proposal_url = sqlc.arg('proposal_url'),
    proposal_state = sqlc.arg('proposal_state'),
    last_proposed_draft_hash = sqlc.arg('last_proposed_draft_hash'),
    proposal_updated_at = NOW(),
    failed_at = NULL,
    failure_code = NULL,
    failure_message = NULL,
    updated_at = NOW()
FROM target
WHERE pd.id = target.id
    AND target.publish_attempt_id = sqlc.arg('publish_attempt_id')
RETURNING pd.id
)
SELECT EXISTS(SELECT 1 FROM target) AS draft_exists,
       EXISTS(SELECT 1 FROM updated) AS updated;

-- name: FailPipelineDraftPublish :execresult
UPDATE pipeline_drafts pd
SET
    draft_state = sqlc.arg('draft_state'),
    failure_code = sqlc.narg('failure_code'),
    failure_message = sqlc.narg('failure_message'),
    failed_at = NOW(),
    updated_at = NOW()
FROM projects p, organizations o
WHERE pd.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pd.publish_attempt_id = sqlc.arg('publish_attempt_id')
    AND pd.draft_state = 'publish_queued'
    AND pd.member_id = (
        SELECT om.id
        FROM organization_members om
        JOIN users u ON om.user_id = u.id
        WHERE om.organization_id = p.organization_id
            AND u.slug = sqlc.arg('member_slug')
    );

-- name: QueuePipelineDraftProposalRefresh :execresult
UPDATE pipeline_drafts pd
SET refresh_attempt_id = sqlc.arg('refresh_attempt_id'),
    failure_code = NULL,
    failure_message = NULL,
    failed_at = NULL,
    updated_at = NOW()
FROM projects p, organizations o
WHERE pd.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pd.proposal_id = sqlc.arg('proposal_id')
    AND pd.member_id = (
        SELECT om.id
        FROM organization_members om
        JOIN users u ON om.user_id = u.id
        WHERE om.organization_id = p.organization_id
            AND u.slug = sqlc.arg('member_slug')
    );

-- name: RefreshPipelineDraftProposal :one
WITH target AS (
    SELECT pd.id, pd.proposal_id, pd.refresh_attempt_id
    FROM pipeline_drafts pd
    JOIN projects p ON pd.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
        AND pd.member_id = (
            SELECT om.id
            FROM organization_members om
            JOIN users u ON om.user_id = u.id
            WHERE om.organization_id = p.organization_id
                AND u.slug = sqlc.arg('member_slug')
        )
), updated AS (
UPDATE pipeline_drafts pd
SET
    draft_state = sqlc.arg('draft_state'),
    failed_at = NULL,
    failure_code = NULL,
    failure_message = NULL,
    branch_name = sqlc.arg('branch_name'),
    branch_head_sha = sqlc.narg('branch_head_sha'),
    proposal_provider = sqlc.arg('proposal_provider'),
    proposal_url = sqlc.arg('proposal_url'),
    proposal_state = sqlc.arg('proposal_state'),
    proposal_updated_at = NOW(),
    updated_at = NOW()
FROM target
WHERE pd.id = target.id
    AND target.proposal_id = sqlc.arg('proposal_id')
    AND target.refresh_attempt_id = sqlc.arg('refresh_attempt_id')
RETURNING pd.id
)
SELECT EXISTS(SELECT 1 FROM target) AS draft_exists,
       EXISTS(SELECT 1 FROM updated) AS updated;

-- name: FailPipelineDraftProposalRefresh :one
WITH target AS (
    SELECT pd.id, pd.proposal_id, pd.refresh_attempt_id
    FROM pipeline_drafts pd
    JOIN projects p ON pd.project_id = p.id
    JOIN organizations o ON p.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
        AND pd.member_id = (
            SELECT om.id
            FROM organization_members om
            JOIN users u ON om.user_id = u.id
            WHERE om.organization_id = p.organization_id
                AND u.slug = sqlc.arg('member_slug')
        )
), updated AS (
UPDATE pipeline_drafts pd
SET failure_code = sqlc.arg('failure_code'),
    failure_message = sqlc.arg('failure_message'),
    failed_at = NOW(),
    updated_at = NOW()
FROM target
WHERE pd.id = target.id
    AND target.proposal_id = sqlc.arg('proposal_id')
    AND target.refresh_attempt_id = sqlc.arg('refresh_attempt_id')
RETURNING pd.id
)
SELECT EXISTS(SELECT 1 FROM target) AS draft_exists,
       EXISTS(SELECT 1 FROM updated) AS updated;

-- name: DeletePipelineDraft :execresult
DELETE FROM pipeline_drafts pd
USING projects p, organizations o
WHERE pd.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
    AND EXISTS (
        SELECT 1
        FROM pipeline_specs ps
        WHERE ps.project_id = pd.project_id
            AND ps.slug = pd.pipeline_slug
            AND ps.deleted_at IS NULL
    )
    AND pd.member_id = (
        SELECT om.id
        FROM organization_members om
        JOIN users u ON om.user_id = u.id
        WHERE om.organization_id = p.organization_id
            AND u.slug = sqlc.arg('member_slug')
    );

-- name: UpsertPipelineSpec :one
WITH project_row AS (
    SELECT p.id AS project_id, p.organization_id
    FROM projects p
    JOIN organizations o ON p.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
),
upserted AS (
    INSERT INTO pipeline_specs (
        project_id,
        slug,
        name,
        spec_jsonb,
        spec_hash,
        sync_status,
        source_path,
        source_ref,
        last_applied_commit,
        updated_at
    )
    SELECT
        project_row.project_id,
        sqlc.arg('slug'),
        sqlc.arg('name'),
        sqlc.arg('spec_jsonb'),
        sqlc.arg('spec_hash'),
        sqlc.arg('sync_status'),
        sqlc.arg('source_path'),
        sqlc.arg('source_ref'),
        sqlc.arg('last_applied_commit'),
        NOW()
    FROM project_row
    ON CONFLICT (project_id, slug)
    DO UPDATE SET
        name = EXCLUDED.name,
        spec_jsonb = EXCLUDED.spec_jsonb,
        spec_hash = EXCLUDED.spec_hash,
        sync_status = EXCLUDED.sync_status,
        source_path = EXCLUDED.source_path,
        source_ref = EXCLUDED.source_ref,
        last_applied_commit = EXCLUDED.last_applied_commit,
        updated_at = NOW()
    WHERE pipeline_specs.deleted_at IS NULL
    RETURNING *
)
SELECT * FROM upserted;

-- name: FindPipelineSpecBySlug :one
SELECT ps.*, pv.spec_hash AS current_version_spec_hash
FROM pipeline_specs ps
JOIN projects p ON ps.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
LEFT JOIN pipeline_versions pv ON pv.id = ps.current_version_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND ps.slug = sqlc.arg('slug')
    AND ps.deleted_at IS NULL;

-- name: FindPipelineSpecBySlugForUpdate :one
SELECT ps.*, pv.spec_hash AS current_version_spec_hash
FROM pipeline_specs ps
JOIN projects p ON ps.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
LEFT JOIN pipeline_versions pv ON pv.id = ps.current_version_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND ps.slug = sqlc.arg('slug')
    AND ps.deleted_at IS NULL
FOR UPDATE OF ps;

-- name: ListPipelineSpecsByProject :many
SELECT ps.*, pv.spec_hash AS current_version_spec_hash
FROM pipeline_specs ps
JOIN projects p ON ps.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
LEFT JOIN pipeline_versions pv ON pv.id = ps.current_version_id
WHERE
    o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND ps.deleted_at IS NULL
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            (
                COALESCE(NULLIF(sqlc.arg('order_dir')::text, ''), 'desc') = 'asc'
                AND (ps.created_at, ps.slug) > (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
            OR (
                COALESCE(NULLIF(sqlc.arg('order_dir')::text, ''), 'desc') = 'desc'
                AND (ps.created_at, ps.slug) < (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN COALESCE(NULLIF(sqlc.arg('order_dir')::text, ''), 'desc') = 'asc' THEN ps.created_at
    END ASC,
    CASE
        WHEN COALESCE(NULLIF(sqlc.arg('order_dir')::text, ''), 'desc') = 'desc' THEN ps.created_at
    END DESC,
    CASE
        WHEN COALESCE(NULLIF(sqlc.arg('order_dir')::text, ''), 'desc') = 'asc' THEN ps.slug
    END ASC,
    CASE
        WHEN COALESCE(NULLIF(sqlc.arg('order_dir')::text, ''), 'desc') = 'desc' THEN ps.slug
    END DESC
LIMIT CASE
    WHEN sqlc.arg('limit_count')::bigint > 0 THEN sqlc.arg('limit_count')::bigint
    ELSE NULL
END;

-- name: DeletePipelineSpec :execresult
UPDATE pipeline_specs ps
SET
    deleted_at = NOW(),
    deleted_by = (
        SELECT om.id
        FROM organization_members om
        JOIN users u ON om.user_id = u.id
        WHERE om.organization_id = p.organization_id
            AND u.slug = sqlc.arg('deleted_by_slug')
    ),
    delete_reason = sqlc.narg('delete_reason'),
    updated_at = NOW()
FROM projects p, organizations o
WHERE ps.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND ps.slug = sqlc.arg('slug')
    AND ps.deleted_at IS NULL;

-- name: RestorePipelineSpec :execresult
UPDATE pipeline_specs ps
SET
    deleted_at = NULL,
    deleted_by = NULL,
    delete_reason = NULL,
    updated_at = NOW()
FROM projects p, organizations o
WHERE ps.project_id = p.id
    AND p.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND ps.slug = sqlc.arg('slug');

-- name: FindPipelineSpecDeletedAtForUpdate :one
SELECT ps.deleted_at
FROM pipeline_specs ps
JOIN projects p ON ps.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND ps.slug = sqlc.arg('slug')
FOR UPDATE OF ps;

-- name: CreatePipelineVersion :one
WITH project_row AS (
    SELECT p.id AS project_id
    FROM projects p
    JOIN organizations o ON p.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
)
INSERT INTO pipeline_versions (
    project_id,
    pipeline_slug,
    git_commit_sha,
    source_path,
    spec_jsonb,
    spec_hash,
    resolved_contracts
)
SELECT
    project_id,
    sqlc.arg('pipeline_slug'),
    sqlc.narg('git_commit_sha'),
    sqlc.narg('source_path'),
    sqlc.arg('spec_jsonb'),
    sqlc.arg('spec_hash'),
    sqlc.arg('resolved_contracts')
FROM project_row
RETURNING *;

-- name: FindPipelineVersionByCommitSHA :one
SELECT pv.*
FROM pipeline_versions pv
JOIN projects p ON pv.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = pv.project_id
    AND ps.slug = pv.pipeline_slug
    AND ps.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pv.git_commit_sha = sqlc.arg('git_commit_sha');

-- name: FindPipelineVersionBySpecHash :one
SELECT pv.*
FROM pipeline_versions pv
JOIN projects p ON pv.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = pv.project_id
    AND ps.slug = pv.pipeline_slug
    AND ps.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pv.spec_hash = sqlc.arg('spec_hash');

-- name: FindPipelineVersionsBySpecHashPrefix :many
SELECT pv.*
FROM pipeline_versions pv
JOIN projects p ON pv.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = pv.project_id
    AND ps.slug = pv.pipeline_slug
    AND ps.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
    AND left(pv.spec_hash, length(sqlc.arg('spec_hash_prefix'))) = sqlc.arg('spec_hash_prefix')
ORDER BY pv.created_at DESC, pv.id DESC
LIMIT 2;

-- name: ListPipelineVersionsByPipeline :many
SELECT pv.*
FROM pipeline_versions pv
JOIN projects p ON pv.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = pv.project_id
    AND ps.slug = pv.pipeline_slug
    AND ps.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
ORDER BY pv.created_at DESC, pv.id DESC;

-- name: FindPipelineVersionByID :one
SELECT pv.*
FROM pipeline_versions pv
JOIN projects p ON pv.project_id = p.id
JOIN organizations o ON p.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = pv.project_id
    AND ps.slug = pv.pipeline_slug
    AND ps.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pv.id = sqlc.arg('version_id');

-- name: SetCurrentPipelineVersion :execresult
WITH project_row AS (
    SELECT p.id AS project_id
    FROM projects p
    JOIN organizations o ON p.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
),
version_row AS (
    SELECT pv.id
    FROM pipeline_versions pv
    JOIN project_row p ON pv.project_id = p.project_id
    WHERE pv.id = sqlc.arg('version_id')
        AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
),
ref_update AS (
    UPDATE pipeline_references
    SET current_version_id = (SELECT id FROM version_row)
    WHERE project_id = (SELECT project_id FROM project_row)
        AND slug = sqlc.arg('pipeline_slug')
        AND EXISTS (SELECT 1 FROM version_row)
    RETURNING 1
)
UPDATE pipeline_specs
SET current_version_id = (SELECT id FROM version_row)
WHERE project_id = (SELECT project_id FROM project_row)
    AND slug = sqlc.arg('pipeline_slug')
    AND EXISTS (SELECT 1 FROM version_row);

-- name: UpsertPipelineDeployment :one
WITH target_project_environment AS (
    SELECT
        pe.id AS project_environment_id,
        p.id AS project_id,
        p.slug AS project_slug,
        e.slug AS environment_slug
    FROM project_environments pe
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
        AND p.slug = sqlc.arg('project_slug')
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
    WHERE o.slug = sqlc.arg('org_slug')
        AND pe.deleted_at IS NULL
),
existing_deployment AS (
    SELECT current_version.spec_hash AS current_version_spec_hash
    FROM pipeline_deployments pd
    JOIN target_project_environment tpe
        ON tpe.project_environment_id = pd.project_environment_id
    JOIN pipeline_versions current_version ON current_version.id = pd.current_version_id
    WHERE pd.pipeline_slug = sqlc.arg('pipeline_slug')
    FOR UPDATE OF pd
),
version_row AS (
    SELECT pv.id, pv.spec_hash
    FROM pipeline_versions pv
    JOIN target_project_environment tpe ON tpe.project_id = pv.project_id
    JOIN pipeline_specs ps ON ps.project_id = pv.project_id
        AND ps.slug = pv.pipeline_slug
        AND ps.deleted_at IS NULL
    WHERE pv.id = sqlc.arg('version_id')
        AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
),
upserted AS (
    INSERT INTO pipeline_deployments (
        project_id,
        project_environment_id,
        pipeline_slug,
        current_version_id,
        previous_version_id,
        current_resolution_digest,
        deployed_by_actor_type,
        deployed_by_actor_slug,
        deployed_at,
        updated_at
    )
    SELECT
        tpe.project_id,
        tpe.project_environment_id,
        sqlc.arg('pipeline_slug'),
        vr.id,
        NULL,
        sqlc.narg('resolution_digest'),
        sqlc.arg('deployed_by_actor_type'),
        sqlc.narg('deployed_by_actor_slug'),
        NOW(),
        NOW()
    FROM target_project_environment tpe
    JOIN version_row vr ON true
    WHERE (sqlc.narg('expected_current_spec_hash')::text IS NULL
        OR EXISTS (
            SELECT 1
            FROM existing_deployment ed
            WHERE ed.current_version_spec_hash = sqlc.narg('expected_current_spec_hash')
        )
    ) AND (sqlc.narg('expected_revision')::bigint IS NULL
        OR sqlc.narg('expected_revision') = 0
        OR EXISTS (SELECT 1 FROM pipeline_deployments pd
            JOIN target_project_environment tpe ON tpe.project_environment_id = pd.project_environment_id
            WHERE pd.pipeline_slug = sqlc.arg('pipeline_slug') AND pd.revision = sqlc.narg('expected_revision')))
    ON CONFLICT (project_environment_id, pipeline_slug)
    DO UPDATE SET
        previous_version_id = CASE
            WHEN pipeline_deployments.current_version_id <> EXCLUDED.current_version_id
                THEN pipeline_deployments.current_version_id
            ELSE pipeline_deployments.previous_version_id
        END,
        previous_resolution_digest = pipeline_deployments.current_resolution_digest,
        current_resolution_digest = EXCLUDED.current_resolution_digest,
        revision = pipeline_deployments.revision + 1,
        current_version_id = EXCLUDED.current_version_id,
        deployed_by_actor_type = EXCLUDED.deployed_by_actor_type,
        deployed_by_actor_slug = EXCLUDED.deployed_by_actor_slug,
        deployed_at = NOW(),
        updated_at = NOW()
    WHERE (sqlc.narg('expected_current_spec_hash')::text IS NULL
        OR EXISTS (
            SELECT 1
            FROM pipeline_versions expected_current
            WHERE expected_current.id = pipeline_deployments.current_version_id
                AND expected_current.spec_hash = sqlc.narg('expected_current_spec_hash')
        )
    ) AND (sqlc.narg('expected_revision')::bigint IS NULL
        OR pipeline_deployments.revision = sqlc.narg('expected_revision'))
        AND (pipeline_deployments.current_resolution_digest IS NULL
            OR EXCLUDED.current_resolution_digest IS NOT NULL
            OR pipeline_deployments.current_version_id <> EXCLUDED.current_version_id)
    RETURNING *
)
SELECT
    upserted.*,
    tpe.project_slug,
    tpe.environment_slug,
    current_version.spec_hash AS current_version_spec_hash,
    previous_version.spec_hash AS previous_version_spec_hash
FROM upserted
JOIN target_project_environment tpe ON tpe.project_environment_id = upserted.project_environment_id
JOIN pipeline_versions current_version ON current_version.id = upserted.current_version_id
LEFT JOIN pipeline_versions previous_version ON previous_version.id = upserted.previous_version_id;

-- name: FindPipelineDeployment :one
SELECT
    pd.*,
    p.slug AS project_slug,
    e.slug AS environment_slug,
    current_version.spec_hash AS current_version_spec_hash,
    previous_version.spec_hash AS previous_version_spec_hash
FROM pipeline_deployments pd
JOIN project_environments pe ON pe.id = pd.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = p.id
    AND ps.slug = pd.pipeline_slug
    AND ps.deleted_at IS NULL
JOIN pipeline_versions current_version ON current_version.id = pd.current_version_id
LEFT JOIN pipeline_versions previous_version ON previous_version.id = pd.previous_version_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pd.pipeline_slug = sqlc.arg('pipeline_slug');

-- name: ListPipelineDeployments :many
SELECT
    pd.*,
    p.slug AS project_slug,
    e.slug AS environment_slug,
    current_version.spec_hash AS current_version_spec_hash,
    previous_version.spec_hash AS previous_version_spec_hash
FROM pipeline_deployments pd
JOIN project_environments pe ON pe.id = pd.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = p.id
    AND ps.slug = pd.pipeline_slug
    AND ps.deleted_at IS NULL
JOIN pipeline_versions current_version ON current_version.id = pd.current_version_id
LEFT JOIN pipeline_versions previous_version ON previous_version.id = pd.previous_version_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
ORDER BY pd.updated_at DESC, pd.pipeline_slug ASC;

-- =====================
-- Pipeline Triggers
-- =====================

-- name: PipelineTriggerProjectEnvironmentEnabled :one
SELECT EXISTS (
    SELECT 1
    FROM project_environments pe
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
);

-- name: PipelineTriggerPipelineExists :one
SELECT EXISTS (
    SELECT 1
    FROM pipeline_specs ps
    JOIN projects p ON p.id = ps.project_id
    JOIN organizations o ON o.id = p.organization_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND ps.slug = sqlc.arg('pipeline_slug')
        AND ps.deleted_at IS NULL
);

-- name: PipelineTriggerPinnedVersionsByHashPrefix :many
SELECT pv.spec_hash
FROM pipeline_versions pv
JOIN pipeline_specs ps ON ps.project_id = pv.project_id
    AND ps.slug = pv.pipeline_slug
    AND ps.deleted_at IS NULL
JOIN projects p ON p.id = pv.project_id
JOIN organizations o ON o.id = p.organization_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND pv.pipeline_slug = sqlc.arg('pipeline_slug')
    AND left(pv.spec_hash, length(sqlc.arg('pinned_version_hash'))) = sqlc.arg('pinned_version_hash')
ORDER BY pv.created_at DESC, pv.id DESC
LIMIT 2;

-- name: PipelineTriggerDeploymentExists :one
SELECT EXISTS (
    SELECT 1
    FROM pipeline_deployments pd
    JOIN project_environments pe ON pe.id = pd.project_environment_id
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    JOIN pipeline_specs ps ON ps.project_id = p.id
        AND ps.slug = pd.pipeline_slug
        AND ps.deleted_at IS NULL
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
        AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
);

-- name: CreatePipelineTrigger :one
WITH target AS (
    SELECT
        p.id AS project_id,
        pe.id AS project_environment_id,
        ps.id AS pipeline_id,
        ps.slug AS pipeline_slug,
        pinned.id AS pinned_pipeline_version_id
    FROM organizations o
    JOIN projects p ON p.organization_id = o.id
        AND p.slug = sqlc.arg('project_slug')
    JOIN environments e ON e.organization_id = o.id
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
    JOIN project_environments pe ON pe.project_id = p.id
        AND pe.environment_id = e.id
        AND pe.deleted_at IS NULL
    JOIN pipeline_specs ps ON ps.project_id = p.id
        AND ps.slug = sqlc.arg('pipeline_slug')
        AND ps.deleted_at IS NULL
    LEFT JOIN pipeline_versions pinned ON pinned.project_id = p.id
        AND pinned.pipeline_slug = ps.slug
        AND pinned.spec_hash = sqlc.narg('pinned_version_hash')::text
    WHERE o.slug = sqlc.arg('org_slug')
),
inserted AS (
    INSERT INTO pipeline_triggers (
        project_id,
        project_environment_id,
        pipeline_id,
        pipeline_slug,
        pinned_pipeline_version_id,
        slug,
        name,
        trigger_type,
        enabled,
        version_policy,
        runtime_input_keys,
        retry_enabled,
        retry_max_attempts,
        retry_backoff_strategy,
        retry_initial_delay_seconds,
        retry_max_delay_seconds,
        retry_jitter,
        retryable_failure_kinds,
        retry_max_elapsed_seconds,
        schedule_cron,
        schedule_timezone,
        schedule_misfire_policy,
        receiver_id,
        webhook_verification_strategy,
        webhook_input_mappings,
        completion_config,
        created_by_actor_type,
        created_by_actor_slug,
        updated_by_actor_type,
        updated_by_actor_slug,
        disabled_at
    )
    SELECT
        target.project_id,
        target.project_environment_id,
        target.pipeline_id,
        target.pipeline_slug,
        CASE
            WHEN sqlc.arg('version_policy') = 'pinned' THEN target.pinned_pipeline_version_id
            ELSE NULL
        END,
        sqlc.arg('trigger_slug'),
        sqlc.arg('name'),
        sqlc.arg('trigger_type'),
        sqlc.arg('enabled'),
        sqlc.arg('version_policy'),
        sqlc.arg('runtime_input_keys')::text[],
        sqlc.arg('retry_enabled'),
        sqlc.arg('retry_max_attempts'),
        sqlc.arg('retry_backoff_strategy'),
        sqlc.arg('retry_initial_delay_seconds'),
        sqlc.arg('retry_max_delay_seconds'),
        sqlc.arg('retry_jitter'),
        sqlc.arg('retryable_failure_kinds')::text[],
        sqlc.narg('retry_max_elapsed_seconds')::integer,
        sqlc.narg('schedule_cron')::text,
        sqlc.narg('schedule_timezone')::text,
        sqlc.narg('schedule_misfire_policy')::text,
        sqlc.narg('receiver_id')::text,
        sqlc.narg('webhook_verification_strategy')::text,
        sqlc.narg('webhook_input_mappings')::jsonb,
        sqlc.narg('completion_config')::jsonb,
        sqlc.arg('created_by_actor_type'),
        sqlc.narg('created_by_actor_slug')::text,
        sqlc.arg('updated_by_actor_type'),
        sqlc.narg('updated_by_actor_slug')::text,
        CASE WHEN sqlc.arg('enabled') THEN NULL ELSE NOW() END
    FROM target
    WHERE sqlc.arg('version_policy') <> 'pinned'
        OR target.pinned_pipeline_version_id IS NOT NULL
    RETURNING *
)
SELECT
    inserted.*,
    e.slug AS environment_slug,
    ps.slug AS pipeline_slug,
    pinned.spec_hash AS pinned_version_hash,
    ARRAY(SELECT pws.key_version FROM pipeline_webhook_secret_versions pws WHERE pws.pipeline_trigger_id = inserted.id AND pws.enabled ORDER BY pws.key_version) AS enabled_key_versions
FROM inserted
JOIN project_environments pe ON pe.id = inserted.project_environment_id
JOIN environments e ON e.id = pe.environment_id
JOIN pipeline_specs ps ON ps.id = inserted.pipeline_id
LEFT JOIN pipeline_versions pinned ON pinned.id = inserted.pinned_pipeline_version_id;

-- name: FindPipelineTriggerBySlug :one
SELECT
    pt.*,
    e.slug AS environment_slug,
    ps.slug AS pipeline_slug,
    pinned.spec_hash AS pinned_version_hash,
    ARRAY(SELECT pws.key_version FROM pipeline_webhook_secret_versions pws WHERE pws.pipeline_trigger_id = pt.id AND pws.enabled ORDER BY pws.key_version) AS enabled_key_versions
FROM pipeline_triggers pt
JOIN project_environments pe ON pe.id = pt.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.id = pt.pipeline_id
    AND ps.project_id = p.id
    AND ps.deleted_at IS NULL
LEFT JOIN pipeline_versions pinned ON pinned.id = pt.pinned_pipeline_version_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pt.slug = sqlc.arg('trigger_slug');

-- name: ListPipelineTriggers :many
SELECT
    pt.*,
    e.slug AS environment_slug,
    ps.slug AS pipeline_slug,
    pinned.spec_hash AS pinned_version_hash,
    ARRAY(SELECT pws.key_version FROM pipeline_webhook_secret_versions pws WHERE pws.pipeline_trigger_id = pt.id AND pws.enabled ORDER BY pws.key_version) AS enabled_key_versions
FROM pipeline_triggers pt
JOIN project_environments pe ON pe.id = pt.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.id = pt.pipeline_id
    AND ps.project_id = p.id
    AND ps.deleted_at IS NULL
LEFT JOIN pipeline_versions pinned ON pinned.id = pt.pinned_pipeline_version_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg('order_dir') = 'asc'
                AND (pt.created_at, pt.slug) > (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg('order_dir') = 'desc'
                AND (pt.created_at, pt.slug) < (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN pt.created_at
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN pt.created_at
    END DESC,
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN pt.slug
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN pt.slug
    END DESC
LIMIT sqlc.arg('limit_count');

-- name: UpdatePipelineTrigger :one
WITH target AS (
    SELECT
        pt.id AS trigger_id,
        ps.id AS pipeline_id,
        ps.slug AS pipeline_slug,
        pinned.id AS pinned_pipeline_version_id
    FROM pipeline_triggers pt
    JOIN project_environments pe ON pe.id = pt.project_environment_id
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    JOIN pipeline_specs current_ps ON current_ps.id = pt.pipeline_id
        AND current_ps.project_id = p.id
        AND current_ps.deleted_at IS NULL
    JOIN pipeline_specs ps ON ps.project_id = p.id
        AND ps.slug = sqlc.arg('pipeline_slug')
        AND ps.deleted_at IS NULL
    LEFT JOIN pipeline_versions pinned ON pinned.project_id = p.id
        AND pinned.pipeline_slug = ps.slug
        AND pinned.spec_hash = sqlc.narg('pinned_version_hash')::text
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
        AND pt.slug = sqlc.arg('trigger_slug')
        AND (
            sqlc.arg('version_policy') <> 'pinned'
            OR pinned.id IS NOT NULL
        )
),
updated AS (
    UPDATE pipeline_triggers pt
    SET
        pipeline_id = target.pipeline_id,
        pipeline_slug = target.pipeline_slug,
        pinned_pipeline_version_id = CASE
            WHEN sqlc.arg('version_policy') = 'pinned' THEN target.pinned_pipeline_version_id
            ELSE NULL
        END,
        completion_config = sqlc.narg('completion_config')::jsonb,
        name = sqlc.arg('name'),
        trigger_type = sqlc.arg('trigger_type'),
        enabled = sqlc.arg('enabled'),
        revision = pt.revision + 1,
        version_policy = sqlc.arg('version_policy'),
        runtime_input_keys = sqlc.arg('runtime_input_keys')::text[],
        retry_enabled = sqlc.arg('retry_enabled'),
        retry_max_attempts = sqlc.arg('retry_max_attempts'),
        retry_backoff_strategy = sqlc.arg('retry_backoff_strategy'),
        retry_initial_delay_seconds = sqlc.arg('retry_initial_delay_seconds'),
        retry_max_delay_seconds = sqlc.arg('retry_max_delay_seconds'),
        retry_jitter = sqlc.arg('retry_jitter'),
        retryable_failure_kinds = sqlc.arg('retryable_failure_kinds')::text[],
        retry_max_elapsed_seconds = sqlc.narg('retry_max_elapsed_seconds')::integer,
        schedule_cron = CASE
            WHEN sqlc.arg('trigger_type') = 'schedule' THEN sqlc.narg('schedule_cron')::text
            ELSE NULL
        END,
        schedule_timezone = CASE
            WHEN sqlc.arg('trigger_type') = 'schedule' THEN sqlc.narg('schedule_timezone')::text
            ELSE NULL
        END,
        schedule_misfire_policy = CASE
            WHEN sqlc.arg('trigger_type') = 'schedule' THEN sqlc.narg('schedule_misfire_policy')::text
            ELSE NULL
        END,
        receiver_id = CASE
            WHEN sqlc.arg('trigger_type') = 'webhook' THEN sqlc.narg('receiver_id')::text
            ELSE NULL
        END,
        webhook_verification_strategy = CASE
            WHEN sqlc.arg('trigger_type') = 'webhook' THEN sqlc.narg('webhook_verification_strategy')::text
            ELSE NULL
        END,
        webhook_input_mappings = CASE
            WHEN sqlc.arg('trigger_type') = 'webhook' THEN sqlc.narg('webhook_input_mappings')::jsonb
            ELSE NULL
        END,
        updated_by_actor_type = sqlc.arg('updated_by_actor_type'),
        updated_by_actor_slug = sqlc.narg('updated_by_actor_slug')::text,
        disabled_at = CASE
            WHEN pt.enabled AND NOT sqlc.arg('enabled') THEN NOW()
            WHEN sqlc.arg('enabled') THEN NULL
            ELSE pt.disabled_at
        END,
        updated_at = NOW()
    FROM target
    WHERE pt.id = target.trigger_id
    RETURNING pt.*
)
SELECT
    updated.*,
    e.slug AS environment_slug,
    ps.slug AS pipeline_slug,
    pinned.spec_hash AS pinned_version_hash,
    ARRAY(SELECT pws.key_version FROM pipeline_webhook_secret_versions pws WHERE pws.pipeline_trigger_id = updated.id AND pws.enabled ORDER BY pws.key_version) AS enabled_key_versions
FROM updated
JOIN project_environments pe ON pe.id = updated.project_environment_id
JOIN environments e ON e.id = pe.environment_id
JOIN pipeline_specs ps ON ps.id = updated.pipeline_id
LEFT JOIN pipeline_versions pinned ON pinned.id = updated.pinned_pipeline_version_id;

-- name: DeletePipelineTrigger :execrows
DELETE FROM pipeline_triggers pt
USING organizations o, projects p, project_environments pe, environments e
WHERE pt.project_environment_id = pe.id
    AND pe.project_id = p.id
    AND p.organization_id = o.id
    AND pe.environment_id = e.id
    AND e.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pt.slug = sqlc.arg('trigger_slug');

-- Pre-tenant lookup: the public receiver presents only an opaque receiver ID.
-- Tenant identity is derived from the trigger and scopes every downstream call.
-- name: ResolvePipelineWebhookReceiver :one
SELECT
    o.slug AS org_slug,
    p.slug AS project_slug,
    e.slug AS environment_slug,
    pt.slug AS trigger_slug,
    pt.revision AS trigger_revision,
    ps.slug AS pipeline_slug,
    pt.version_policy,
    pinned.spec_hash AS pinned_version_hash,
    pt.runtime_input_keys,
    pt.webhook_input_mappings,
    pt.retry_enabled,
    pt.retry_max_attempts,
    pt.retry_backoff_strategy,
    pt.retry_initial_delay_seconds,
    pt.retry_max_delay_seconds,
    pt.retry_jitter,
    pt.retryable_failure_kinds,
    pt.retry_max_elapsed_seconds
FROM pipeline_triggers pt
JOIN project_environments pe ON pe.id = pt.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.id = pt.pipeline_id
    AND ps.project_id = p.id
    AND ps.deleted_at IS NULL
LEFT JOIN pipeline_versions pinned ON pinned.id = pt.pinned_pipeline_version_id
WHERE pt.receiver_id = sqlc.arg('receiver_id')
    AND pt.trigger_type = 'webhook'
    AND pt.enabled
    AND pt.disabled_at IS NULL
    AND pe.deleted_at IS NULL
    AND e.deleted_at IS NULL;

-- Pre-tenant acceptance: the opaque receiver ID resolves the trigger-owned tenant,
-- and the inserted delivery copies those tenant IDs inside this transaction.
-- name: AcceptPipelineWebhookDelivery :one
WITH target AS (
    SELECT
        o.id AS organization_id,
        p.id AS project_id,
        pe.id AS project_environment_id,
        pt.id AS pipeline_trigger_id
    FROM pipeline_triggers pt
    JOIN project_environments pe ON pe.id = pt.project_environment_id
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    JOIN pipeline_webhook_secret_versions pws ON pws.pipeline_trigger_id = pt.id
    WHERE pt.receiver_id = sqlc.arg('receiver_id')
        AND pt.trigger_type = 'webhook'
        AND pt.enabled
        AND pt.disabled_at IS NULL
        AND pt.revision = sqlc.arg('trigger_revision')
        AND pws.key_version = sqlc.arg('key_version')
        AND pws.enabled
        AND pws.disabled_at IS NULL
        AND pe.deleted_at IS NULL
        AND e.deleted_at IS NULL
)
INSERT INTO pipeline_webhook_deliveries AS pwd (
    organization_id,
    project_id,
    project_environment_id,
    pipeline_trigger_id,
    receiver_id,
    delivery_id,
    payload_digest,
    payload_size_bytes,
    key_version,
    trigger_revision,
    pipeline_slug,
    version_policy,
    pinned_version_hash,
    runtime_input_keys,
    retry_enabled,
    retry_max_attempts,
    retry_backoff_strategy,
    retry_initial_delay_seconds,
    retry_max_delay_seconds,
    retry_jitter,
    retryable_failure_kinds,
    retry_max_elapsed_seconds,
    run_slug,
    encrypted_envelope,
    envelope_expires_at,
    received_at
)
SELECT
    target.organization_id,
    target.project_id,
    target.project_environment_id,
    target.pipeline_trigger_id,
    sqlc.arg('receiver_id'),
    sqlc.arg('delivery_id'),
    sqlc.arg('payload_digest'),
    sqlc.arg('payload_size_bytes'),
    sqlc.arg('key_version'),
    sqlc.arg('trigger_revision'),
    sqlc.arg('pipeline_slug'),
    sqlc.arg('version_policy'),
    sqlc.narg('pinned_version_hash')::text,
    sqlc.arg('runtime_input_keys')::text[],
    sqlc.arg('retry_enabled'),
    sqlc.arg('retry_max_attempts'),
    sqlc.arg('retry_backoff_strategy'),
    sqlc.arg('retry_initial_delay_seconds'),
    sqlc.arg('retry_max_delay_seconds'),
    sqlc.arg('retry_jitter'),
    sqlc.arg('retryable_failure_kinds')::text[],
    sqlc.narg('retry_max_elapsed_seconds')::integer,
    sqlc.arg('run_slug'),
    sqlc.arg('encrypted_envelope'),
    sqlc.arg('envelope_expires_at'),
    sqlc.arg('received_at')
FROM target
ON CONFLICT (receiver_id, delivery_id) DO UPDATE
SET duplicate_count = pwd.duplicate_count + CASE
        WHEN pwd.payload_digest = EXCLUDED.payload_digest THEN 1
        ELSE 0
    END,
    last_duplicate_at = CASE
        WHEN pwd.payload_digest = EXCLUDED.payload_digest THEN EXCLUDED.received_at
        ELSE pwd.last_duplicate_at
    END,
    updated_at = CASE
        WHEN pwd.payload_digest = EXCLUDED.payload_digest THEN EXCLUDED.received_at
        ELSE pwd.updated_at
    END
RETURNING pwd.payload_digest, pwd.run_slug, pwd.status, (xmax = 0)::boolean AS inserted;

-- A signed duplicate is trusted only after receiver resolution and HMAC verification.
-- The CTE locks an existing identity so concurrent callers cannot both report it as new.
-- name: RecordPipelineWebhookDuplicate :one
WITH existing AS (
    SELECT id, payload_digest, run_slug, status
    FROM pipeline_webhook_deliveries
    WHERE receiver_id = sqlc.arg('receiver_id')
        AND delivery_id = sqlc.arg('delivery_id')
    FOR UPDATE
), updated AS (
    UPDATE pipeline_webhook_deliveries pwd
    SET duplicate_count = pwd.duplicate_count + 1,
        last_duplicate_at = sqlc.arg('received_at'),
        updated_at = sqlc.arg('received_at')
    FROM existing
    WHERE pwd.id = existing.id
        AND existing.payload_digest = sqlc.arg('payload_digest')
    RETURNING pwd.id
)
SELECT
    existing.payload_digest,
    existing.run_slug,
    existing.status,
    updated.id IS NOT NULL AS duplicate
FROM existing
LEFT JOIN updated ON updated.id = existing.id;

-- Persist only safe rejection metadata after the request signature has been verified.
-- name: RejectPipelineWebhookDelivery :one
WITH target AS (
    SELECT
        o.id AS organization_id,
        p.id AS project_id,
        pe.id AS project_environment_id,
        pt.id AS pipeline_trigger_id
    FROM pipeline_triggers pt
    JOIN project_environments pe ON pe.id = pt.project_environment_id
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    WHERE pt.receiver_id = sqlc.arg('receiver_id')
        AND pt.trigger_type = 'webhook'
        AND pt.revision = sqlc.arg('trigger_revision')
        AND pe.deleted_at IS NULL
        AND e.deleted_at IS NULL
)
INSERT INTO pipeline_webhook_deliveries AS pwd (
    organization_id, project_id, project_environment_id, pipeline_trigger_id,
    receiver_id, delivery_id, payload_digest, payload_size_bytes, key_version,
    trigger_revision, pipeline_slug, version_policy, pinned_version_hash,
    runtime_input_keys, retry_enabled, retry_max_attempts,
    retry_backoff_strategy, retry_initial_delay_seconds, retry_max_delay_seconds,
    retry_jitter, retryable_failure_kinds, retry_max_elapsed_seconds,
    run_slug, status, decision_reason, received_at, completed_at
)
SELECT
    target.organization_id, target.project_id, target.project_environment_id,
    target.pipeline_trigger_id, sqlc.arg('receiver_id'), sqlc.arg('delivery_id'),
    sqlc.arg('payload_digest'), sqlc.arg('payload_size_bytes'), sqlc.arg('key_version'),
    sqlc.arg('trigger_revision'), sqlc.arg('pipeline_slug'), sqlc.arg('version_policy'),
    sqlc.narg('pinned_version_hash')::text, sqlc.arg('runtime_input_keys')::text[],
    sqlc.arg('retry_enabled'), sqlc.arg('retry_max_attempts'),
    sqlc.arg('retry_backoff_strategy'), sqlc.arg('retry_initial_delay_seconds'),
    sqlc.arg('retry_max_delay_seconds'), sqlc.arg('retry_jitter'),
    sqlc.arg('retryable_failure_kinds')::text[], sqlc.narg('retry_max_elapsed_seconds')::integer,
    sqlc.arg('run_slug'), 'rejected', sqlc.arg('decision_reason'),
    sqlc.arg('received_at'), sqlc.arg('received_at')
FROM target
ON CONFLICT (receiver_id, delivery_id) DO UPDATE
SET duplicate_count = pwd.duplicate_count + CASE
        WHEN pwd.payload_digest = EXCLUDED.payload_digest THEN 1
        ELSE 0
    END,
    last_duplicate_at = CASE
        WHEN pwd.payload_digest = EXCLUDED.payload_digest THEN EXCLUDED.received_at
        ELSE pwd.last_duplicate_at
    END,
    updated_at = CASE
        WHEN pwd.payload_digest = EXCLUDED.payload_digest THEN EXCLUDED.received_at
        ELSE pwd.updated_at
    END
RETURNING pwd.payload_digest, pwd.run_slug, pwd.status, (xmax = 0)::boolean AS inserted;

-- name: ExpirePipelineWebhookDelivery :execrows
UPDATE pipeline_webhook_deliveries
SET status = CASE WHEN status IN ('accepted', 'running') THEN 'failed' ELSE status END,
    failure_kind = CASE WHEN status IN ('accepted', 'running') THEN 'platform' ELSE failure_kind END,
    decision_reason = CASE WHEN status IN ('accepted', 'running') THEN 'execution_envelope_expired' ELSE decision_reason END,
    next_retry_at = NULL,
    encrypted_envelope = NULL,
    envelope_expires_at = NULL,
    completed_at = CASE WHEN status IN ('accepted', 'running') THEN sqlc.arg('now') ELSE completed_at END,
    updated_at = sqlc.arg('now')
WHERE receiver_id = sqlc.arg('receiver_id')
    AND delivery_id = sqlc.arg('delivery_id')
    AND encrypted_envelope IS NOT NULL
    AND envelope_expires_at <= sqlc.arg('now');

-- name: LoadPipelineWebhookDelivery :one
SELECT
    pwd.*,
    o.slug AS org_slug,
    p.slug AS project_slug,
    e.slug AS environment_slug,
    pt.slug AS trigger_slug
FROM pipeline_webhook_deliveries pwd
JOIN organizations o ON o.id = pwd.organization_id
JOIN projects p ON p.id = pwd.project_id
    AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = pwd.project_environment_id
    AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id
    AND pt.project_id = p.id
WHERE pwd.receiver_id = sqlc.arg('receiver_id')
    AND pwd.delivery_id = sqlc.arg('delivery_id')
    AND pwd.attempt_number = sqlc.arg('attempt_number')
    AND pwd.encrypted_envelope IS NOT NULL
    AND pwd.envelope_expires_at > sqlc.arg('now');

-- name: MarkPipelineWebhookDeliveryRunning :execrows
UPDATE pipeline_webhook_deliveries
SET status = 'running',
    next_retry_at = NULL,
    started_at = COALESCE(started_at, sqlc.arg('started_at')),
    updated_at = sqlc.arg('started_at')
WHERE receiver_id = sqlc.arg('receiver_id')
    AND delivery_id = sqlc.arg('delivery_id')
    AND attempt_number = sqlc.arg('attempt_number')
    AND status IN ('accepted', 'running')
    AND encrypted_envelope IS NOT NULL;

-- name: ResetPipelineWebhookPreflight :execrows
UPDATE pipeline_webhook_deliveries
SET status = 'accepted', started_at = NULL,
    decision_reason = 'execution_preflight_retry', updated_at = NOW()
WHERE receiver_id = sqlc.arg('receiver_id') AND delivery_id = sqlc.arg('delivery_id')
    AND attempt_number = sqlc.arg('attempt_number') AND status = 'running';

-- name: CompletePipelineWebhookDelivery :execrows
UPDATE pipeline_webhook_deliveries
SET status = sqlc.arg('status'),
    failure_kind = sqlc.narg('failure_kind'),
    decision_reason = sqlc.narg('decision_reason'),
    next_retry_at = NULL,
    encrypted_envelope = CASE
        WHEN sqlc.arg('status') = 'failed' THEN encrypted_envelope
        ELSE NULL
    END,
    envelope_expires_at = CASE
        WHEN sqlc.arg('status') = 'failed' THEN envelope_expires_at
        ELSE NULL
    END,
    completed_at = sqlc.arg('completed_at'),
    updated_at = sqlc.arg('completed_at')
WHERE receiver_id = sqlc.arg('receiver_id')
    AND delivery_id = sqlc.arg('delivery_id')
    AND attempt_number = sqlc.arg('attempt_number')
    AND status = 'running';

-- name: ListPipelineWebhookDeliveriesForTrigger :many
SELECT
    pwd.*,
    pt.slug AS trigger_slug,
    COALESCE(latest_run.run_slug, '') AS linked_run_slug
FROM pipeline_webhook_deliveries pwd
JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id
    AND pt.project_environment_id = pwd.project_environment_id
JOIN project_environments pe ON pe.id = pwd.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
LEFT JOIN LATERAL (
    SELECT pr.run_slug
    FROM pipeline_runs pr
    WHERE pr.source_webhook_delivery_id = pwd.id
    ORDER BY pr.created_at DESC, pr.id DESC
    LIMIT 1
) latest_run ON TRUE
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pt.slug = sqlc.arg('trigger_slug')
    AND pt.trigger_type = 'webhook'
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            (sqlc.arg('order_dir') = 'asc' AND (pwd.received_at, pwd.delivery_id) > (sqlc.narg('cursor_timestamp')::timestamptz, sqlc.narg('cursor_slug')::text))
            OR
            (sqlc.arg('order_dir') = 'desc' AND (pwd.received_at, pwd.delivery_id) < (sqlc.narg('cursor_timestamp')::timestamptz, sqlc.narg('cursor_slug')::text))
        )
    )
ORDER BY
    CASE WHEN sqlc.arg('order_dir') = 'asc' THEN pwd.received_at END ASC,
    CASE WHEN sqlc.arg('order_dir') = 'desc' THEN pwd.received_at END DESC,
    CASE WHEN sqlc.arg('order_dir') = 'asc' THEN pwd.delivery_id END ASC,
    CASE WHEN sqlc.arg('order_dir') = 'desc' THEN pwd.delivery_id END DESC
LIMIT sqlc.arg('limit_count');

-- name: GetPipelineWebhookDeliveryForTrigger :one
SELECT
    pwd.*,
    pt.slug AS trigger_slug,
    COALESCE(latest_run.run_slug, '') AS linked_run_slug
FROM pipeline_webhook_deliveries pwd
JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id
    AND pt.project_environment_id = pwd.project_environment_id
JOIN project_environments pe ON pe.id = pwd.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
LEFT JOIN LATERAL (
    SELECT pr.run_slug
    FROM pipeline_runs pr
    WHERE pr.source_webhook_delivery_id = pwd.id
    ORDER BY pr.created_at DESC, pr.id DESC
    LIMIT 1
) latest_run ON TRUE
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pt.slug = sqlc.arg('trigger_slug')
    AND pt.trigger_type = 'webhook'
    AND pwd.delivery_id = sqlc.arg('delivery_id');

-- Replay is limited to failures that never created a run; linked runs use the
-- pipeline run replay contract so sink idempotency remains enforced there.
-- name: ReplayPipelineWebhookDelivery :one
UPDATE pipeline_webhook_deliveries pwd
SET status = 'accepted',
    attempt_number = pwd.attempt_number + 1,
    failure_kind = NULL,
    decision_reason = 'operator_replay',
    next_retry_at = NULL,
    started_at = NULL,
    completed_at = NULL,
    updated_at = sqlc.arg('replayed_at')
FROM pipeline_triggers pt, project_environments pe, projects p, organizations o, environments e
WHERE pt.id = pwd.pipeline_trigger_id
    AND pt.project_environment_id = pwd.project_environment_id
    AND pe.id = pwd.project_environment_id
    AND p.id = pe.project_id
    AND o.id = p.organization_id
    AND e.id = pe.environment_id
    AND e.organization_id = o.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pt.slug = sqlc.arg('trigger_slug')
    AND pt.trigger_type = 'webhook'
    AND pwd.delivery_id = sqlc.arg('delivery_id')
    AND pwd.status = 'failed'
    AND pwd.encrypted_envelope IS NOT NULL
    AND pwd.envelope_expires_at > sqlc.arg('replayed_at')
    AND pwd.attempt_number < 10
    AND NOT EXISTS (
        SELECT 1 FROM pipeline_runs pr WHERE pr.source_webhook_delivery_id = pwd.id
    )
RETURNING pwd.receiver_id, pwd.delivery_id, pwd.attempt_number;

-- name: RecordPipelineWebhookDeliveryRetry :execrows
UPDATE pipeline_webhook_deliveries
SET status = 'accepted',
    pipeline_version_spec_hash = COALESCE(pipeline_version_spec_hash, sqlc.arg('pipeline_version_spec_hash')),
    attempt_number = sqlc.arg('next_attempt_number'),
    failure_kind = sqlc.arg('failure_kind'),
    decision_reason = 'retryable_failure_kind',
    next_retry_at = sqlc.arg('next_retry_at'),
    updated_at = sqlc.arg('updated_at')
WHERE receiver_id = sqlc.arg('receiver_id')
    AND delivery_id = sqlc.arg('delivery_id')
    AND attempt_number = sqlc.arg('attempt_number')
    AND status = 'running'
    AND encrypted_envelope IS NOT NULL;

-- name: ListDueScheduleTriggers :many
SELECT
    o.slug AS org_slug,
    p.slug AS project_slug,
    e.slug AS environment_slug,
    pt.slug AS trigger_slug,
    pt.revision AS trigger_revision,
    pt.enabled,
    ps.slug AS pipeline_slug,
    pt.version_policy,
    pinned.spec_hash AS pinned_version_hash,
    pt.runtime_input_keys,
    pt.retry_enabled,
    pt.retry_max_attempts,
    pt.retry_backoff_strategy,
    pt.retry_initial_delay_seconds,
    pt.retry_max_delay_seconds,
    pt.retry_jitter,
    pt.retryable_failure_kinds,
    pt.retry_max_elapsed_seconds,
    pt.schedule_cron,
    pt.schedule_timezone,
    pt.schedule_misfire_policy,
    pt.created_at AS trigger_created_at,
    last_fire.scheduled_fire_at AS last_scheduled_fire_at
FROM pipeline_triggers pt
JOIN project_environments pe ON pe.id = pt.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.id = pt.pipeline_id
    AND ps.project_id = p.id
    AND ps.deleted_at IS NULL
LEFT JOIN pipeline_versions pinned ON pinned.id = pt.pinned_pipeline_version_id
LEFT JOIN LATERAL (
    SELECT psf.scheduled_fire_at
    FROM pipeline_schedule_fires psf
    WHERE psf.pipeline_trigger_id = pt.id
        AND psf.trigger_revision = pt.revision
    ORDER BY psf.scheduled_fire_at DESC
    LIMIT 1
) last_fire ON true
WHERE pt.trigger_type = 'schedule'
    AND pt.enabled = true
    AND pt.schedule_cron IS NOT NULL
    AND pt.schedule_timezone IS NOT NULL
    AND pt.schedule_misfire_policy IS NOT NULL
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
ORDER BY o.slug, p.slug, e.slug, pt.slug;

-- name: ClaimScheduleFire :one
WITH target AS (
    SELECT
        pt.id AS pipeline_trigger_id,
        pe.id AS project_environment_id,
        ps.slug AS pipeline_slug,
        pt.version_policy,
        pinned.spec_hash AS pinned_version_hash,
        pt.runtime_input_keys,
        pt.retry_enabled,
        pt.retry_max_attempts,
        pt.retry_backoff_strategy,
        pt.retry_initial_delay_seconds,
        pt.retry_max_delay_seconds,
        pt.retry_jitter,
        pt.retryable_failure_kinds,
        pt.retry_max_elapsed_seconds,
        pt.schedule_cron,
        pt.schedule_timezone,
        pt.schedule_misfire_policy
    FROM pipeline_triggers pt
    JOIN project_environments pe ON pe.id = pt.project_environment_id
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    JOIN pipeline_specs ps ON ps.id = pt.pipeline_id
        AND ps.project_id = p.id
        AND ps.deleted_at IS NULL
    LEFT JOIN pipeline_versions pinned ON pinned.id = pt.pinned_pipeline_version_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
        AND pt.slug = sqlc.arg('trigger_slug')
        AND pt.revision = sqlc.arg('trigger_revision')
        AND pt.trigger_type = 'schedule'
        AND pt.enabled = true
),
inserted AS (
    INSERT INTO pipeline_schedule_fires (
        project_environment_id,
        pipeline_trigger_id,
        fire_slug,
        trigger_revision,
        pipeline_slug,
        version_policy,
        pinned_version_hash,
        runtime_input_keys,
        retry_enabled,
        retry_max_attempts,
        retry_backoff_strategy,
        retry_initial_delay_seconds,
        retry_max_delay_seconds,
        retry_jitter,
        retryable_failure_kinds,
        retry_max_elapsed_seconds,
        schedule_cron,
        schedule_timezone,
        schedule_misfire_policy,
        scheduled_fire_at,
        actual_fire_at,
        misfire_result,
        status
    )
    SELECT
        target.project_environment_id,
        target.pipeline_trigger_id,
        sqlc.arg('fire_slug'),
        sqlc.arg('trigger_revision'),
        target.pipeline_slug,
        target.version_policy,
        target.pinned_version_hash,
        target.runtime_input_keys,
        target.retry_enabled,
        target.retry_max_attempts,
        target.retry_backoff_strategy,
        target.retry_initial_delay_seconds,
        target.retry_max_delay_seconds,
        target.retry_jitter,
        target.retryable_failure_kinds,
        target.retry_max_elapsed_seconds,
        target.schedule_cron,
        target.schedule_timezone,
        target.schedule_misfire_policy,
        sqlc.arg('scheduled_fire_at'),
        sqlc.arg('actual_fire_at'),
        sqlc.arg('misfire_result'),
        'claimed'
    FROM target
    ON CONFLICT (pipeline_trigger_id, trigger_revision, scheduled_fire_at) DO NOTHING
    RETURNING fire_slug
)
SELECT
    EXISTS (SELECT 1 FROM inserted) AS claimed,
    COALESCE(
        (SELECT fire_slug FROM inserted),
        (
            SELECT psf.fire_slug
            FROM pipeline_schedule_fires psf
            JOIN target ON target.pipeline_trigger_id = psf.pipeline_trigger_id
            WHERE psf.trigger_revision = sqlc.arg('trigger_revision')
                AND psf.scheduled_fire_at = sqlc.arg('scheduled_fire_at')
        ),
        ''
    ) AS fire_slug;

-- name: LoadClaimedScheduleFire :one
SELECT
    o.slug AS org_slug,
    p.slug AS project_slug,
    e.slug AS environment_slug,
    pt.slug AS trigger_slug,
    psf.fire_slug,
    psf.status,
    psf.trigger_revision,
    psf.pipeline_slug,
    psf.version_policy,
    psf.pinned_version_hash,
    psf.runtime_input_keys,
    psf.retry_enabled,
    psf.retry_max_attempts,
    psf.retry_backoff_strategy,
    psf.retry_initial_delay_seconds,
    psf.retry_max_delay_seconds,
    psf.retry_jitter,
    psf.retryable_failure_kinds,
    psf.retry_max_elapsed_seconds,
    psf.schedule_cron,
    psf.schedule_timezone,
    psf.schedule_misfire_policy,
    psf.scheduled_fire_at,
    psf.actual_fire_at,
    psf.misfire_result,
    pt.created_at AS trigger_created_at
FROM pipeline_schedule_fires psf
JOIN pipeline_triggers pt ON pt.id = psf.pipeline_trigger_id
JOIN project_environments pe ON pe.id = psf.project_environment_id
    AND pe.id = pt.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = p.id
    AND ps.slug = psf.pipeline_slug
    AND ps.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pt.slug = sqlc.arg('trigger_slug')
    AND psf.trigger_revision = sqlc.arg('trigger_revision')
    AND psf.scheduled_fire_at = sqlc.arg('scheduled_fire_at')
    AND psf.status IN ('claimed','running');

-- name: MarkScheduleFireRunning :execrows
UPDATE pipeline_schedule_fires psf
SET
    status = 'running',
    updated_at = NOW()
FROM pipeline_triggers pt
JOIN project_environments pe ON pe.id = pt.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = p.id
WHERE psf.pipeline_trigger_id = pt.id
    AND psf.project_environment_id = pe.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND ps.slug = psf.pipeline_slug
    AND ps.deleted_at IS NULL
    AND pt.slug = sqlc.arg('trigger_slug')
    AND psf.trigger_revision = sqlc.arg('trigger_revision')
    AND psf.scheduled_fire_at = sqlc.arg('scheduled_fire_at')
    AND psf.status = 'claimed';

-- name: CompleteScheduleFire :execrows
UPDATE pipeline_schedule_fires psf
SET
    status = sqlc.arg('status'),
    run_slug = sqlc.narg('run_slug'),
    source_event_slug = sqlc.narg('source_event_slug'),
    error_summary = sqlc.narg('error_summary'),
    updated_at = NOW()
FROM pipeline_triggers pt
JOIN project_environments pe ON pe.id = pt.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
WHERE psf.pipeline_trigger_id = pt.id
    AND psf.project_environment_id = pe.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND pt.slug = sqlc.arg('trigger_slug')
    AND psf.trigger_revision = sqlc.arg('trigger_revision')
    AND psf.scheduled_fire_at = sqlc.arg('scheduled_fire_at')
    AND psf.status IN ('claimed', 'running');

-- name: ListScheduleFiresForTrigger :many
SELECT
    psf.fire_slug,
    pt.slug AS trigger_slug,
    psf.trigger_revision,
    psf.pipeline_slug,
    psf.scheduled_fire_at,
    psf.actual_fire_at,
    psf.status,
    psf.misfire_result,
    psf.run_slug,
    psf.source_event_slug,
    psf.error_summary,
    psf.created_at,
    psf.updated_at
FROM pipeline_schedule_fires psf
JOIN pipeline_triggers pt ON pt.id = psf.pipeline_trigger_id
    AND pt.project_environment_id = psf.project_environment_id
JOIN project_environments pe ON pe.id = psf.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND pt.slug = sqlc.arg('trigger_slug')
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            (
                sqlc.arg('order_dir') = 'asc'
                AND (psf.scheduled_fire_at, psf.fire_slug) > (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
            OR (
                sqlc.arg('order_dir') = 'desc'
                AND (psf.scheduled_fire_at, psf.fire_slug) < (
                    sqlc.narg('cursor_timestamp')::timestamptz,
                    sqlc.narg('cursor_slug')::text
                )
            )
        )
    )
ORDER BY
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN psf.scheduled_fire_at
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN psf.scheduled_fire_at
    END DESC,
    CASE
        WHEN sqlc.arg('order_dir') = 'asc' THEN psf.fire_slug
    END ASC,
    CASE
        WHEN sqlc.arg('order_dir') = 'desc' THEN psf.fire_slug
    END DESC
LIMIT sqlc.arg('limit_count');

-- name: RecordTriggerRetryAttempt :one
WITH target AS (
    SELECT
        pt.id AS pipeline_trigger_id,
        NULL::text AS completion_trigger_slug,
        pe.id AS project_environment_id
    FROM pipeline_triggers pt
    JOIN project_environments pe ON pe.id = pt.project_environment_id
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
        AND pt.slug = sqlc.arg('trigger_slug') AND sqlc.arg('source_type')::text <> 'completion'
    UNION ALL
    SELECT NULL::bigint AS pipeline_trigger_id, snapshot.trigger_slug::text AS completion_trigger_slug, snapshot.project_environment_id
    FROM pipeline_completion_snapshots snapshot
    JOIN organizations o ON o.id = snapshot.organization_id
    JOIN project_environments pe ON pe.id = snapshot.project_environment_id
    JOIN projects p ON p.id = pe.project_id AND p.organization_id = o.id
    JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND (sqlc.arg('status')::text IN ('skipped', 'exhausted') OR (pe.deleted_at IS NULL AND e.deleted_at IS NULL))
        AND sqlc.arg('source_type')::text = 'completion' AND snapshot.state = 'accepted'
        AND COALESCE(snapshot.child_run_slug, '') = sqlc.arg('root_run_slug')::text
        AND snapshot.trigger_slug = sqlc.arg('trigger_slug') AND snapshot.trigger_revision = sqlc.arg('trigger_revision')
        AND snapshot.downstream_pipeline_slug = sqlc.arg('pipeline_slug') AND COALESCE(snapshot.selected_version_hash, '') = sqlc.arg('pipeline_version_spec_hash')::text
),
inserted AS (
    INSERT INTO pipeline_trigger_retry_attempts (
        project_environment_id,
        pipeline_trigger_id,
        completion_trigger_slug,
        attempt_slug,
        status,
        decision_reason,
        trigger_revision,
        pipeline_slug,
        version_policy,
        pinned_version_hash,
        pipeline_version_spec_hash,
        runtime_input_keys,
        retry_enabled,
        retry_max_attempts,
        retry_backoff_strategy,
        retry_initial_delay_seconds,
        retry_max_delay_seconds,
        retry_jitter,
        retryable_failure_kinds,
        retry_max_elapsed_seconds,
        source_type,
        source_summary,
        source_entrypoint,
        source_actor_type,
        source_actor_slug,
        source_request_id,
        source_trace_id,
        source_event_slug,
        source_parameter_keys,
        source_schedule_trigger_slug,
        source_schedule_trigger_revision,
        source_schedule_cron,
        source_schedule_timezone,
        source_schedule_scheduled_fire_at,
        source_schedule_actual_fire_at,
        source_schedule_misfire_result,
        root_run_slug,
        retry_of_run_slug,
        run_slug,
        attempt_number,
        failure_kind,
        scheduled_at,
        enqueued_at
    )
    SELECT
        target.project_environment_id,
        target.pipeline_trigger_id,
        target.completion_trigger_slug,
        sqlc.arg('attempt_slug'),
        sqlc.arg('status'),
        sqlc.narg('decision_reason'),
        sqlc.arg('trigger_revision'),
        sqlc.arg('pipeline_slug'),
        sqlc.arg('version_policy'),
        sqlc.narg('pinned_version_hash'),
        sqlc.arg('pipeline_version_spec_hash'),
        COALESCE(sqlc.arg('runtime_input_keys')::text[], '{}'::text[]),
        sqlc.arg('retry_enabled'),
        sqlc.arg('retry_max_attempts'),
        sqlc.arg('retry_backoff_strategy'),
        sqlc.arg('retry_initial_delay_seconds'),
        sqlc.arg('retry_max_delay_seconds'),
        sqlc.arg('retry_jitter'),
        sqlc.arg('retryable_failure_kinds')::text[],
        sqlc.narg('retry_max_elapsed_seconds')::integer,
        sqlc.arg('source_type'),
        sqlc.arg('source_summary'),
        sqlc.arg('source_entrypoint'),
        sqlc.arg('source_actor_type'),
        sqlc.narg('source_actor_slug'),
        sqlc.narg('source_request_id'),
        sqlc.narg('source_trace_id'),
        sqlc.narg('source_event_slug'),
        sqlc.arg('source_parameter_keys')::text[],
        sqlc.narg('source_schedule_trigger_slug'),
        sqlc.narg('source_schedule_trigger_revision')::bigint,
        sqlc.narg('source_schedule_cron'),
        sqlc.narg('source_schedule_timezone'),
        sqlc.narg('source_schedule_scheduled_fire_at')::timestamptz,
        sqlc.narg('source_schedule_actual_fire_at')::timestamptz,
        sqlc.narg('source_schedule_misfire_result'),
        sqlc.arg('root_run_slug'),
        sqlc.arg('retry_of_run_slug'),
        sqlc.arg('run_slug'),
        sqlc.arg('attempt_number'),
        sqlc.narg('failure_kind'),
        sqlc.arg('scheduled_at'),
        CASE WHEN sqlc.arg('status') = 'queued' THEN NOW() ELSE NULL END
    FROM target
    ON CONFLICT (project_environment_id, attempt_slug) DO UPDATE
    SET updated_at = EXCLUDED.updated_at
    RETURNING *
)
SELECT * FROM inserted
UNION ALL
SELECT ptra.*
FROM pipeline_trigger_retry_attempts ptra
JOIN target ON target.project_environment_id = ptra.project_environment_id
WHERE ptra.attempt_slug = sqlc.arg('attempt_slug')
LIMIT 1;

-- name: ListTriggerRetryAttemptsForRun :many
-- Historical retry reads intentionally do not filter environments.deleted_at.
-- Deleting an environment catalog row must not hide persisted run/retry history.
WITH target_run AS (
    SELECT
        pr.project_environment_id,
        COALESCE(NULLIF(pr.source_root_run_slug, ''), pr.run_slug) AS root_run_slug
    FROM
        pipeline_runs pr
        JOIN project_environments pe ON pr.project_environment_id = pe.id
        JOIN projects p ON pe.project_id = p.id
        JOIN organizations o ON p.organization_id = o.id
        JOIN environments e ON pe.environment_id = e.id
        JOIN pipeline_specs ps ON ps.project_id = pe.project_id
            AND ps.slug = pr.pipeline_slug
            AND ps.deleted_at IS NULL
    WHERE
        o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND pr.run_slug = sqlc.arg('run_slug')
)
SELECT
    ptra.attempt_slug,
    ptra.status,
    ptra.decision_reason,
    COALESCE(pt.slug, ptra.completion_trigger_slug)::text AS trigger_slug,
    ptra.trigger_revision,
    ptra.pipeline_slug,
    ptra.root_run_slug,
    ptra.retry_of_run_slug,
    ptra.run_slug,
    ptra.attempt_number,
    ptra.failure_kind,
    ptra.source_event_slug,
    ptra.scheduled_at,
    ptra.enqueued_at,
    ptra.started_at,
    ptra.completed_at,
    ptra.error_summary,
    ptra.created_at,
    ptra.updated_at
FROM
    pipeline_trigger_retry_attempts ptra
    JOIN target_run ON target_run.project_environment_id = ptra.project_environment_id
        AND target_run.root_run_slug = ptra.root_run_slug
    LEFT JOIN pipeline_triggers pt ON pt.id = ptra.pipeline_trigger_id
        AND pt.project_environment_id = ptra.project_environment_id
ORDER BY
    ptra.attempt_number ASC,
    ptra.created_at ASC,
    ptra.id ASC;

-- name: LoadTriggerRetryAttemptForExecution :one
SELECT
    ptra.*,
    o.slug AS org_slug,
    p.slug AS project_slug,
    e.slug AS environment_slug,
    pt.slug AS trigger_slug
FROM pipeline_trigger_retry_attempts ptra
JOIN pipeline_triggers pt ON pt.id = ptra.pipeline_trigger_id
    AND pt.project_environment_id = ptra.project_environment_id
JOIN project_environments pe ON pe.id = ptra.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = p.id
    AND ps.slug = ptra.pipeline_slug
    AND ps.deleted_at IS NULL
JOIN pipeline_versions pv ON pv.project_id = p.id
    AND pv.pipeline_slug = ptra.pipeline_slug
    AND pv.spec_hash = ptra.pipeline_version_spec_hash
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND ptra.attempt_slug = sqlc.arg('attempt_slug')
    AND ptra.status IN ('queued','running');

-- name: MarkTriggerRetryAttemptRunning :execrows
UPDATE pipeline_trigger_retry_attempts ptra
SET
    status = 'running',
    started_at = NOW(),
    updated_at = NOW()
FROM project_environments pe
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN pipeline_triggers pt ON pt.project_environment_id = pe.id
JOIN pipeline_specs ps ON ps.project_id = p.id
    AND ps.deleted_at IS NULL
JOIN pipeline_versions pv ON pv.project_id = p.id
WHERE ptra.project_environment_id = pe.id
    AND pt.id = ptra.pipeline_trigger_id
    AND pt.project_environment_id = ptra.project_environment_id
    AND ps.slug = ptra.pipeline_slug
    AND pv.pipeline_slug = ptra.pipeline_slug
    AND pv.spec_hash = ptra.pipeline_version_spec_hash
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND e.deleted_at IS NULL
    AND pe.deleted_at IS NULL
    AND ptra.attempt_slug = sqlc.arg('attempt_slug')
    AND ptra.status = 'queued';

-- name: CompleteTriggerRetryAttempt :execrows
UPDATE pipeline_trigger_retry_attempts ptra
SET
    status = sqlc.arg('status'),
    source_event_slug = COALESCE(sqlc.narg('source_event_slug'), ptra.source_event_slug),
    error_summary = sqlc.narg('error_summary'),
    completed_at = NOW(),
    updated_at = NOW()
FROM project_environments pe
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
WHERE ptra.project_environment_id = pe.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND ptra.attempt_slug = sqlc.arg('attempt_slug')
    AND ptra.status = 'running';

-- =====================
-- Drift Impact Reports
-- =====================

-- name: SupersedeOpenDriftImpactReports :exec
WITH target AS (
    SELECT
        ps.id AS pipeline_spec_id,
        ds.id AS data_source_id
    FROM organizations o
    LEFT JOIN projects p ON p.organization_id = o.id
        AND p.slug = sqlc.narg('project_slug')::text
    LEFT JOIN pipeline_specs ps ON ps.project_id = p.id
        AND ps.slug = sqlc.narg('pipeline_slug')::text
        AND ps.deleted_at IS NULL
    LEFT JOIN data_sources ds ON ds.organization_id = o.id
        AND ds.slug = sqlc.narg('data_source_slug')::text
        AND ds.deleted_at IS NULL
    WHERE o.slug = sqlc.arg('org_slug')
)
UPDATE drift_impact_reports r
SET status = 'superseded',
    superseded_at = NOW()
FROM target
WHERE r.report_type = sqlc.arg('report_type')
    AND r.status = 'open'
    AND (
        EXISTS (
            SELECT 1
            FROM pipeline_specs scoped_ps
            JOIN projects scoped_p ON scoped_p.id = scoped_ps.project_id
            JOIN organizations scoped_o ON scoped_o.id = scoped_p.organization_id
            WHERE scoped_ps.id = r.pipeline_spec_id
                AND scoped_o.slug = sqlc.arg('org_slug')
        )
        OR EXISTS (
            SELECT 1
            FROM data_sources scoped_ds
            JOIN organizations scoped_o ON scoped_o.id = scoped_ds.organization_id
            WHERE scoped_ds.id = r.data_source_id
                AND scoped_o.slug = sqlc.arg('org_slug')
        )
    )
    AND (sqlc.narg('pipeline_slug')::text IS NULL OR r.pipeline_spec_id = target.pipeline_spec_id)
    AND (sqlc.narg('data_source_slug')::text IS NULL OR r.data_source_id = target.data_source_id);

-- name: CreateDriftImpactReport :one
WITH target AS (
    SELECT
        ps.id AS pipeline_spec_id,
        ds.id AS data_source_id
    FROM organizations o
    LEFT JOIN projects p ON p.organization_id = o.id
        AND p.slug = sqlc.narg('project_slug')::text
    LEFT JOIN pipeline_specs ps ON ps.project_id = p.id
        AND ps.slug = sqlc.narg('pipeline_slug')::text
        AND ps.deleted_at IS NULL
    LEFT JOIN data_sources ds ON ds.organization_id = o.id
        AND ds.slug = sqlc.narg('data_source_slug')::text
        AND ds.deleted_at IS NULL
    WHERE o.slug = sqlc.arg('org_slug')
)
INSERT INTO drift_impact_reports (
    pipeline_spec_id,
    data_source_id,
    slug,
    report_type,
    status,
    severity,
    summary,
    changes,
    impacts
)
SELECT
    target.pipeline_spec_id,
    target.data_source_id,
    sqlc.arg('slug'),
    sqlc.arg('report_type'),
    sqlc.arg('status'),
    sqlc.arg('severity'),
    sqlc.arg('summary'),
    sqlc.arg('changes')::jsonb,
    sqlc.arg('impacts')::jsonb
FROM target
WHERE (
        target.pipeline_spec_id IS NOT NULL
        AND target.data_source_id IS NULL
    )
    OR (
        target.pipeline_spec_id IS NULL
        AND target.data_source_id IS NOT NULL
    )
RETURNING *;

-- name: FindDriftImpactReportBySlug :one
SELECT
    r.*,
    COALESCE(p.slug, sqlc.arg('project_slug')::text) AS project_slug,
    COALESCE(ps.slug, '') AS pipeline_slug,
    COALESCE(ds.slug, '') AS data_source_slug
FROM drift_impact_reports r
LEFT JOIN pipeline_specs ps ON ps.id = r.pipeline_spec_id
LEFT JOIN projects p ON p.id = ps.project_id
LEFT JOIN data_sources ds ON ds.id = r.data_source_id
LEFT JOIN organizations po ON po.id = p.organization_id
LEFT JOIN organizations dso ON dso.id = ds.organization_id
WHERE (po.slug = sqlc.arg('org_slug') OR dso.slug = sqlc.arg('org_slug'))
    AND r.slug = sqlc.arg('report_slug')
    AND (
        p.slug = sqlc.arg('project_slug')::text
        OR (
            r.pipeline_spec_id IS NULL
            AND EXISTS (
                SELECT 1
                FROM data_source_bindings dsb
                JOIN project_environments pe ON pe.id = dsb.project_environment_id
                JOIN environments e ON e.id = pe.environment_id
                JOIN projects bound_p ON bound_p.id = pe.project_id
                WHERE dsb.data_source_id = r.data_source_id
                    AND pe.deleted_at IS NULL
                    AND e.deleted_at IS NULL
                    AND bound_p.slug = sqlc.arg('project_slug')::text
                    AND bound_p.organization_id = COALESCE(po.id, dso.id)
            )
        )
    );

-- name: ListDriftImpactReports :many
SELECT
    r.*,
    COALESCE(p.slug, sqlc.narg('project_slug')::text, '') AS project_slug,
    COALESCE(ps.slug, '') AS pipeline_slug,
    COALESCE(ds.slug, '') AS data_source_slug
FROM drift_impact_reports r
LEFT JOIN pipeline_specs ps ON ps.id = r.pipeline_spec_id
LEFT JOIN projects p ON p.id = ps.project_id
LEFT JOIN data_sources ds ON ds.id = r.data_source_id
LEFT JOIN organizations po ON po.id = p.organization_id
LEFT JOIN organizations dso ON dso.id = ds.organization_id
WHERE (po.slug = sqlc.arg('org_slug') OR dso.slug = sqlc.arg('org_slug'))
    AND (
        sqlc.narg('project_slug')::text IS NULL
        OR p.slug = sqlc.narg('project_slug')::text
        OR (
            r.pipeline_spec_id IS NULL
            AND EXISTS (
                SELECT 1
                FROM data_source_bindings dsb
                JOIN project_environments pe ON pe.id = dsb.project_environment_id
                JOIN environments e ON e.id = pe.environment_id
                JOIN projects bound_p ON bound_p.id = pe.project_id
                WHERE dsb.data_source_id = r.data_source_id
                    AND pe.deleted_at IS NULL
                    AND e.deleted_at IS NULL
                    AND bound_p.slug = sqlc.narg('project_slug')::text
                    AND bound_p.organization_id = COALESCE(po.id, dso.id)
            )
        )
    )
    AND (sqlc.narg('pipeline_slug')::text IS NULL OR ps.slug = sqlc.narg('pipeline_slug')::text)
    AND (sqlc.narg('data_source_slug')::text IS NULL OR ds.slug = sqlc.narg('data_source_slug')::text)
    AND (sqlc.narg('status')::text IS NULL OR r.status = sqlc.narg('status')::text)
    AND (sqlc.narg('severity')::text IS NULL OR r.severity = sqlc.narg('severity')::text)
ORDER BY r.created_at DESC, r.slug ASC
LIMIT sqlc.arg('limit_count');

-- name: CreatePipelineValidationResult :one
WITH target AS (
    SELECT
        p.id AS project_id,
        pe.id AS project_environment_id,
        ps.id AS pipeline_id,
        ps.current_version_id AS pipeline_version_id,
        ps.spec_hash AS pipeline_spec_hash,
        current_version.spec_hash AS current_version_spec_hash
    FROM organizations o
    JOIN projects p ON p.organization_id = o.id
    JOIN project_environments pe ON pe.project_id = p.id
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
    JOIN pipeline_specs ps ON ps.project_id = p.id
        AND ps.slug = sqlc.arg('pipeline_slug')
        AND ps.deleted_at IS NULL
    LEFT JOIN pipeline_versions current_version ON current_version.id = ps.current_version_id
    WHERE o.slug = sqlc.arg('org_slug')
        AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
        AND pe.deleted_at IS NULL
),
inserted AS (
    INSERT INTO pipeline_validation_results (
        project_id,
        project_environment_id,
        pipeline_id,
        pipeline_version_id,
        validation_slug,
        pipeline_slug,
        source,
        valid,
        spec_hash,
        diagnostics,
        impact_report_slug,
        impact_pipeline_id
    )
    SELECT
        target.project_id,
        target.project_environment_id,
        target.pipeline_id,
        target.pipeline_version_id,
        sqlc.arg('validation_slug'),
        sqlc.arg('pipeline_slug'),
        sqlc.arg('source'),
        sqlc.arg('valid'),
        COALESCE(sqlc.narg('spec_hash')::text, target.pipeline_spec_hash, target.current_version_spec_hash),
        sqlc.arg('diagnostics')::jsonb,
        sqlc.narg('impact_report_slug')::text,
        CASE WHEN sqlc.narg('impact_report_slug')::text IS NOT NULL THEN target.pipeline_id END
    FROM target
    RETURNING *
)
SELECT
    inserted.*,
    o.slug AS org_slug,
    p.slug AS project_slug,
    e.slug AS environment_slug
FROM inserted
JOIN project_environments pe ON pe.id = inserted.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id;

-- name: FindPipelineValidationResultBySlug :one
SELECT
    r.*,
    o.slug AS org_slug,
    p.slug AS project_slug,
    e.slug AS environment_slug
FROM pipeline_validation_results r
JOIN project_environments pe ON pe.id = r.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND r.validation_slug = sqlc.arg('validation_slug');

-- name: ListPipelineValidationResults :many
SELECT
    r.*,
    o.slug AS org_slug,
    p.slug AS project_slug,
    e.slug AS environment_slug
FROM pipeline_validation_results r
JOIN project_environments pe ON pe.id = r.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND (
        sqlc.narg('pipeline_slug')::text IS NULL
        OR EXISTS (
            SELECT 1
            FROM pipeline_specs ps
            WHERE ps.project_id = p.id
                AND ps.slug = sqlc.narg('pipeline_slug')::text
                AND ps.deleted_at IS NULL
                AND r.pipeline_id = ps.id
        )
    )
ORDER BY r.created_at DESC, r.validation_slug DESC
LIMIT sqlc.arg('limit_count');

-- name: FindProofCandidateReceiptTarget :one
SELECT
    c.*,
    m.organization_id,
    m.project_id,
    m.project_environment_id,
    m.provider_connection_id,
    m.repository_id,
    m.target_ref,
    m.pipeline_path,
    o.slug AS organization_slug,
    p.slug AS project_slug,
    e.slug AS environment_slug,
    ds.slug AS provider_connection_slug
FROM proof_change_candidates c
JOIN active_proof_candidate_mappings m ON m.id = c.mapping_id
    AND m.organization_id = c.organization_id
JOIN organizations o ON o.id = m.organization_id
JOIN projects p ON p.id = m.project_id
    AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = m.project_environment_id
    AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
JOIN data_sources ds ON ds.id = m.data_source_id
    AND ds.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug')
    AND c.slug = sqlc.arg('candidate_slug');

-- name: LockProofReceiptTarget :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg('target_key'), 0));

-- name: CreateProofEvidence :one
WITH inserted AS (
INSERT INTO proof_evidence (
    organization_id,
    project_id,
    project_environment_id,
    mapping_id,
    candidate_id,
    spec_hash,
    binding_fingerprint,
    capability_fingerprint,
    kind,
    digest,
    source_identifier,
    observed_at,
    expires_at
) VALUES (
    sqlc.arg('organization_id'),
    sqlc.arg('project_id'),
    sqlc.arg('project_environment_id'),
    sqlc.arg('mapping_id'),
    sqlc.arg('candidate_id'),
    sqlc.arg('spec_hash'),
    sqlc.arg('binding_fingerprint'),
    sqlc.arg('capability_fingerprint'),
    sqlc.arg('kind'),
    sqlc.arg('digest'),
    sqlc.arg('source_identifier'),
    sqlc.arg('observed_at'),
    sqlc.narg('expires_at')
)
ON CONFLICT (organization_id, candidate_id, digest) DO NOTHING
RETURNING *
)
SELECT * FROM inserted
UNION ALL
SELECT existing.*
FROM proof_evidence existing
WHERE existing.organization_id = sqlc.arg('organization_id')
    AND existing.candidate_id = sqlc.arg('candidate_id')
    AND existing.digest = sqlc.arg('digest')
    AND NOT EXISTS (SELECT 1 FROM inserted)
LIMIT 1;

-- name: FindLatestProofReceiptForChange :one
SELECT r.id, r.slug
FROM proof_receipts r
JOIN proof_change_candidates prior ON prior.id = r.candidate_id
    AND prior.organization_id = r.organization_id
WHERE r.receipt->'operational' IS NULL
    AND LEAST(r.retain_until, r.created_at + interval '168 hours') > statement_timestamp()
    AND r.organization_id = sqlc.arg('organization_id')
    AND r.mapping_id = sqlc.arg('mapping_id')
    AND prior.change_request_id = sqlc.arg('change_request_id')
    AND (
        prior.id = sqlc.arg('candidate_id')
        OR prior.id = sqlc.arg('supersedes_candidate_id')
    )
    AND r.receipt_key <> sqlc.arg('receipt_key')
ORDER BY (prior.id = sqlc.arg('candidate_id')) DESC, r.created_at DESC, r.id DESC
LIMIT 1;

-- name: CreateProofReceipt :one
WITH inserted AS (
    INSERT INTO proof_receipts (
        organization_id,
        project_id,
        project_environment_id,
        mapping_id,
        candidate_id,
        evidence_id,
        slug,
        receipt_key,
        spec_hash,
        binding_fingerprint,
        capability_fingerprint,
        compiler_version,
        policy_version,
        aggregate_verdict,
        receipt,
        supersedes_receipt_id,
        created_at,
        retain_until
    ) SELECT
        sqlc.arg('organization_id'),
        sqlc.arg('project_id'),
        sqlc.arg('project_environment_id'),
        sqlc.arg('mapping_id'),
        sqlc.arg('candidate_id'),
        sqlc.arg('evidence_id'),
        sqlc.arg('receipt_slug'),
        sqlc.arg('receipt_key'),
        sqlc.arg('spec_hash'),
        sqlc.arg('binding_fingerprint'),
        sqlc.arg('capability_fingerprint'),
        sqlc.arg('compiler_version'),
        sqlc.arg('policy_version'),
        sqlc.arg('aggregate_verdict'),
        sqlc.arg('receipt')::jsonb,
        sqlc.narg('supersedes_receipt_id'),
        sqlc.arg('created_at'),
        sqlc.arg('retain_until')
    WHERE NOT EXISTS (
        SELECT 1 FROM events event
        WHERE event.organization_id = sqlc.arg('organization_id')
            AND event.event_class = 'security_audit' AND event.action = 'delete'
            AND event.metadata->>'proof_receipt_key' = sqlc.arg('receipt_key')
    )
    ON CONFLICT (organization_id, receipt_key) DO NOTHING
    RETURNING *
)
SELECT *, FALSE AS superseded
FROM inserted
UNION ALL
SELECT existing.*, EXISTS (
    SELECT 1 FROM proof_receipts newer
    WHERE newer.organization_id = existing.organization_id
        AND newer.supersedes_receipt_id = existing.id
) AS superseded
FROM proof_receipts existing
WHERE existing.receipt_key = sqlc.arg('receipt_key')
    AND existing.organization_id = sqlc.arg('organization_id')
    AND LEAST(existing.retain_until, existing.created_at + interval '168 hours') > statement_timestamp()
    AND NOT EXISTS (SELECT 1 FROM inserted)
LIMIT 1;

-- name: FindProofReceiptBySlug :one
SELECT r.*, EXISTS (
    SELECT 1 FROM proof_receipts newer
    WHERE newer.organization_id = r.organization_id
        AND newer.supersedes_receipt_id = r.id
) AS superseded
FROM proof_receipts r
JOIN proof_change_candidate_mappings m ON m.id = r.mapping_id
    AND m.organization_id = r.organization_id
    AND m.project_id = r.project_id
    AND m.project_environment_id = r.project_environment_id
JOIN organizations o ON o.id = r.organization_id
JOIN projects p ON p.id = r.project_id
    AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = r.project_environment_id
    AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
WHERE LEAST(r.retain_until, r.created_at + interval '168 hours') > statement_timestamp()
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND r.slug = sqlc.arg('receipt_slug');

-- name: ListProofReceipts :many
SELECT r.*, EXISTS (
    SELECT 1 FROM proof_receipts newer
    WHERE newer.organization_id = r.organization_id
        AND newer.supersedes_receipt_id = r.id
) AS superseded
FROM proof_receipts r
JOIN organizations o ON o.id = r.organization_id
JOIN projects p ON p.id = r.project_id
    AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = r.project_environment_id
    AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id
    AND e.organization_id = o.id
WHERE LEAST(r.retain_until, r.created_at + interval '168 hours') > statement_timestamp()
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND (
        sqlc.narg('cursor_slug')::text IS NULL
        OR (
            sqlc.arg('order_dir') = 'asc'
            AND (r.created_at, r.slug) > (sqlc.narg('cursor_timestamp')::timestamptz, sqlc.narg('cursor_slug')::text)
        )
        OR (
            sqlc.arg('order_dir') = 'desc'
            AND (r.created_at, r.slug) < (sqlc.narg('cursor_timestamp')::timestamptz, sqlc.narg('cursor_slug')::text)
        )
    )
ORDER BY
    CASE WHEN sqlc.arg('order_dir') = 'asc' THEN r.created_at END ASC,
    CASE WHEN sqlc.arg('order_dir') = 'desc' THEN r.created_at END DESC,
    CASE WHEN sqlc.arg('order_dir') = 'asc' THEN r.slug END ASC,
    CASE WHEN sqlc.arg('order_dir') = 'desc' THEN r.slug END DESC
LIMIT sqlc.arg('limit_count');

-- name: FindLatestProofReceiptForCandidate :one
SELECT r.receipt, FALSE AS superseded
FROM proof_receipts r
JOIN proof_change_candidates c ON c.id = r.candidate_id AND c.organization_id = r.organization_id
JOIN organizations o ON o.id = r.organization_id
WHERE r.receipt->'operational' IS NULL
    AND LEAST(r.retain_until, r.created_at + interval '168 hours') > statement_timestamp()
    AND o.slug = sqlc.arg('org_slug') AND c.slug = sqlc.arg('candidate_slug')
ORDER BY r.created_at DESC, r.id DESC
LIMIT 1;

-- name: AttemptAbuseControl :one
WITH params AS (
    SELECT
        clock_timestamp() AS now_at,
        make_interval(secs => sqlc.arg('window_seconds')::double precision) AS window_duration
),
pruned AS (
    DELETE FROM abuse_control_buckets bucket
    USING params
    WHERE bucket.window_started_at < params.now_at - INTERVAL '24 hours'
),
attempt AS (
    INSERT INTO abuse_control_buckets (
        scope,
        subject_hash,
        window_started_at,
        attempt_count
    )
    SELECT
        sqlc.arg('scope'),
        sqlc.arg('subject_hash'),
        params.now_at,
        1
    FROM params
    ON CONFLICT (scope, subject_hash) DO UPDATE
    SET
        window_started_at = CASE
            WHEN abuse_control_buckets.window_started_at + (SELECT window_duration FROM params)
                <= (SELECT now_at FROM params)
            THEN (SELECT now_at FROM params)
            ELSE abuse_control_buckets.window_started_at
        END,
        attempt_count = CASE
            WHEN abuse_control_buckets.window_started_at + (SELECT window_duration FROM params)
                <= (SELECT now_at FROM params)
            THEN 1
            ELSE LEAST(
                abuse_control_buckets.attempt_count + 1,
                sqlc.arg('attempt_limit') + 1
            )
        END
    RETURNING window_started_at, attempt_count
)
SELECT
    attempt.attempt_count <= sqlc.arg('attempt_limit') AS allowed,
    attempt.window_started_at + params.window_duration AS reset_at
FROM attempt, params;



-- =====================
-- Proof change candidates
-- =====================

-- name: CreateProofChangeCandidateMapping :one
INSERT INTO proof_change_candidate_mappings (
    organization_id,
    project_id,
    project_environment_id,
    data_source_id,
    provider,
    provider_connection_id,
    repository_id,
    target_ref,
    pipeline_path
)
SELECT
    organization.id,
    project.id,
    project_environment.id,
    data_source.id,
    sqlc.arg('provider'),
    sqlc.arg('provider_connection_id'),
    sqlc.arg('repository_id'),
    sqlc.arg('target_ref'),
    sqlc.arg('pipeline_path')
FROM organizations organization
JOIN projects project ON project.organization_id = organization.id
JOIN data_sources data_source
    ON data_source.organization_id = organization.id
    AND data_source.deleted_at IS NULL
    AND data_source.is_active = true
JOIN environments environment
    ON environment.organization_id = organization.id
    AND environment.deleted_at IS NULL
JOIN project_environments project_environment
    ON project_environment.project_id = project.id
    AND project_environment.environment_id = environment.id
    AND project_environment.deleted_at IS NULL
WHERE organization.slug = sqlc.arg('org_slug')
    AND project.slug = sqlc.arg('project_slug')
    AND project.pipeline_backend_data_source_id = data_source.id
    AND data_source.type = sqlc.arg('provider')
    AND data_source.slug = sqlc.arg('provider_connection_slug')
    AND environment.slug = sqlc.arg('environment_slug')
    AND 'refs/heads/' || project.pipeline_backend_tracked_branch = sqlc.arg('target_ref')
    AND LEFT(
        sqlc.arg('pipeline_path'),
        LENGTH(project.pipeline_backend_pipeline_dir) + 1
    ) = project.pipeline_backend_pipeline_dir || '/'
RETURNING id;

-- name: ResolveProofChangeCandidateMappings :many
SELECT
    mapping.id,
    organization.slug AS organization_slug,
    project.slug AS project_slug,
    environment.slug AS environment_slug,
    data_source.slug AS provider_connection_slug,
    mapping.provider,
    mapping.provider_connection_id,
    mapping.repository_id,
    mapping.target_ref,
    mapping.pipeline_path
FROM proof_change_candidate_mappings mapping
JOIN organizations organization ON organization.id = mapping.organization_id
JOIN projects project
    ON project.id = mapping.project_id
    AND project.organization_id = mapping.organization_id
JOIN project_environments project_environment
    ON project_environment.id = mapping.project_environment_id
    AND project_environment.project_id = mapping.project_id
    AND project_environment.deleted_at IS NULL
JOIN environments environment
    ON environment.id = project_environment.environment_id
    AND environment.organization_id = mapping.organization_id
    AND environment.deleted_at IS NULL
JOIN data_sources data_source
    ON data_source.id = mapping.data_source_id
    AND data_source.organization_id = mapping.organization_id
    AND data_source.deleted_at IS NULL
    AND data_source.is_active = true
JOIN credentials credential
    ON credential.id = data_source.credential_id
    AND credential.organization_id = mapping.organization_id
    AND credential.status = 'active'
    AND credential.revoked_at IS NULL
WHERE mapping.id IN (SELECT active.id FROM active_proof_candidate_mappings active)
    AND mapping.provider = sqlc.arg('provider')
    AND (sqlc.arg('mapping_id')::bigint = 0 OR mapping.id = sqlc.arg('mapping_id'))
    AND organization.slug = sqlc.arg('org_slug')
    AND mapping.provider_connection_id = sqlc.arg('provider_connection_id')
    AND mapping.repository_id = sqlc.arg('repository_id')
    AND mapping.target_ref = sqlc.arg('target_ref')
    AND mapping.pipeline_path = sqlc.arg('pipeline_path')
-- ponytail: one mapping-row lock serializes candidate writes; use a keyed
-- advisory lock only if one repository mapping becomes a measured bottleneck.
FOR UPDATE OF mapping;

-- name: ResolveProofChangeWebhookMappings :many
-- Pre-tenant webhook intake derives tenancy from the verified provider
-- installation. The provider connection, repository, and target ref identify
-- candidates; organization/project/environment/data-source/credential joins
-- enforce the tenant boundary and fail closed for inactive credentials.
SELECT
    mapping.id,
    organization.slug AS organization_slug,
    project.slug AS project_slug,
    environment.slug AS environment_slug,
    data_source.slug AS provider_connection_slug,
    mapping.provider,
    mapping.provider_connection_id,
    mapping.repository_id,
    mapping.target_ref,
    mapping.pipeline_path
FROM proof_change_candidate_mappings mapping
JOIN organizations organization ON organization.id = mapping.organization_id
JOIN projects project
    ON project.id = mapping.project_id
    AND project.organization_id = mapping.organization_id
JOIN project_environments project_environment
    ON project_environment.id = mapping.project_environment_id
    AND project_environment.project_id = mapping.project_id
    AND project_environment.deleted_at IS NULL
JOIN environments environment
    ON environment.id = project_environment.environment_id
    AND environment.organization_id = mapping.organization_id
    AND environment.deleted_at IS NULL
JOIN data_sources data_source
    ON data_source.id = mapping.data_source_id
    AND data_source.organization_id = mapping.organization_id
    AND data_source.deleted_at IS NULL
    AND data_source.is_active = true
JOIN credentials credential
    ON credential.id = data_source.credential_id
    AND credential.organization_id = mapping.organization_id
    AND credential.status = 'active'
    AND credential.revoked_at IS NULL
WHERE mapping.id IN (SELECT active.id FROM active_proof_candidate_mappings active)
    AND mapping.provider = sqlc.arg('provider')
    AND mapping.provider_connection_id = sqlc.arg('provider_connection_id')
    AND mapping.repository_id = sqlc.arg('repository_id')
    AND mapping.target_ref = sqlc.arg('target_ref')
ORDER BY organization.slug, mapping.pipeline_path, mapping.id;

-- name: GetGitLabProofWebhookByDataSource :one
SELECT
    webhook.id,
    webhook.organization_id,
    organization.slug AS organization_slug,
    data_source.slug AS data_source_slug,
    webhook.project_id,
    webhook.hook_id,
    webhook.status,
    webhook.route_key_hash,
    webhook.encrypted_signing_token
FROM gitlab_proof_webhooks webhook
JOIN organizations organization ON organization.id = webhook.organization_id
JOIN data_sources data_source
    ON data_source.id = webhook.data_source_id
    AND data_source.organization_id = webhook.organization_id
    AND data_source.deleted_at IS NULL
WHERE organization.slug = sqlc.arg('organization_slug')
    AND data_source.slug = sqlc.arg('data_source_slug');

-- name: GetGitLabProofWebhookByRoute :one
SELECT
    webhook.id,
    webhook.organization_id,
    organization.slug AS organization_slug,
    data_source.slug AS data_source_slug,
    webhook.project_id,
    webhook.hook_id,
    webhook.status,
    webhook.route_key_hash,
    webhook.encrypted_signing_token
FROM gitlab_proof_webhooks webhook
JOIN organizations organization ON organization.id = webhook.organization_id
JOIN data_sources data_source
    ON data_source.id = webhook.data_source_id
    AND data_source.organization_id = webhook.organization_id
    AND data_source.deleted_at IS NULL
    AND data_source.is_active = true
JOIN credentials credential
    ON credential.id = data_source.credential_id
    AND credential.organization_id = webhook.organization_id
    AND credential.status = 'active'
    AND credential.revoked_at IS NULL
WHERE webhook.route_key_hash = sqlc.arg('route_key_hash');

-- name: ClaimGitLabProofWebhookSetup :one
INSERT INTO gitlab_proof_webhooks (
    organization_id,
    data_source_id,
    project_id,
    hook_id,
    status,
    route_key_hash,
    encrypted_signing_token
)
SELECT
    organization.id,
    data_source.id,
    sqlc.arg('project_id'),
    sqlc.arg('hook_id'),
    'pending',
    sqlc.arg('route_key_hash'),
    sqlc.arg('encrypted_signing_token')
FROM organizations organization
JOIN data_sources data_source
    ON data_source.organization_id = organization.id
    AND data_source.slug = sqlc.arg('data_source_slug')
    AND data_source.deleted_at IS NULL
WHERE organization.slug = sqlc.arg('organization_slug')
ON CONFLICT (organization_id, data_source_id) DO UPDATE SET
    project_id = EXCLUDED.project_id,
    hook_id = EXCLUDED.hook_id,
    status = 'pending',
    route_key_hash = EXCLUDED.route_key_hash,
    encrypted_signing_token = EXCLUDED.encrypted_signing_token,
    updated_at = NOW()
WHERE gitlab_proof_webhooks.status = 'failed'
    OR (gitlab_proof_webhooks.status = 'active' AND gitlab_proof_webhooks.project_id <> EXCLUDED.project_id)
    OR (
        gitlab_proof_webhooks.status = 'pending'
        AND gitlab_proof_webhooks.updated_at < NOW() - INTERVAL '15 minutes'
    )
RETURNING *;

-- name: ActivateGitLabProofWebhook :one
UPDATE gitlab_proof_webhooks webhook
SET hook_id = sqlc.arg('hook_id'), status = 'active', updated_at = NOW()
FROM organizations organization, data_sources data_source
WHERE webhook.organization_id = organization.id
    AND webhook.data_source_id = data_source.id
    AND data_source.organization_id = organization.id
    AND organization.slug = sqlc.arg('organization_slug')
    AND data_source.slug = sqlc.arg('data_source_slug')
    AND webhook.route_key_hash = sqlc.arg('route_key_hash')
    AND webhook.status = 'pending'
RETURNING webhook.*;

-- name: FailGitLabProofWebhookSetup :exec
UPDATE gitlab_proof_webhooks webhook
SET status = 'failed', updated_at = NOW()
FROM organizations organization, data_sources data_source
WHERE webhook.organization_id = organization.id
    AND webhook.data_source_id = data_source.id
    AND data_source.organization_id = organization.id
    AND organization.slug = sqlc.arg('organization_slug')
    AND data_source.slug = sqlc.arg('data_source_slug')
    AND webhook.route_key_hash = sqlc.arg('route_key_hash')
    AND webhook.status = 'pending';

-- name: ClaimProofWebhookDelivery :one
INSERT INTO proof_webhook_deliveries (provider, delivery_id, payload_sha256)
VALUES (sqlc.arg('provider'), sqlc.arg('delivery_id'), sqlc.arg('payload_sha256'))
ON CONFLICT (provider, delivery_id) DO NOTHING
RETURNING payload_sha256;

-- name: GetProofWebhookDeliveryDigest :one
SELECT payload_sha256
FROM proof_webhook_deliveries
WHERE provider = sqlc.arg('provider')
    AND delivery_id = sqlc.arg('delivery_id');

-- name: FindProofChangeCandidateByKey :one
SELECT candidate.*
FROM proof_change_candidates candidate
JOIN organizations organization ON organization.id = candidate.organization_id
WHERE organization.slug = sqlc.arg('org_slug')
    AND candidate.candidate_key = sqlc.arg('candidate_key');

-- name: FindProofChangeCandidateByID :one
SELECT candidate.*
FROM proof_change_candidates candidate
JOIN organizations organization ON organization.id = candidate.organization_id
WHERE organization.slug = sqlc.arg('org_slug')
    AND candidate.id = sqlc.arg('candidate_id');

-- name: FindLatestProofChangeCandidate :one
SELECT candidate.*
FROM proof_change_candidates candidate
JOIN organizations organization ON organization.id = candidate.organization_id
WHERE organization.slug = sqlc.arg('org_slug')
    AND candidate.mapping_id = sqlc.arg('mapping_id')
    AND candidate.change_request_id = sqlc.arg('change_request_id')
ORDER BY candidate.created_at DESC, candidate.id DESC
LIMIT 1;

-- name: CloseLatestProofChangeCandidate :one
UPDATE proof_change_candidates candidate
SET closed_at = COALESCE(candidate.closed_at, CURRENT_TIMESTAMP)
WHERE candidate.organization_id = (
        SELECT organization.id
        FROM organizations organization
        WHERE organization.slug = sqlc.arg('org_slug')
    )
    AND candidate.mapping_id = sqlc.arg('mapping_id')
    AND candidate.change_request_id = sqlc.arg('change_request_id')
    AND candidate.head_sha = sqlc.arg('head_sha')
    AND candidate.id = (
        SELECT latest.id
        FROM proof_change_candidates latest
        WHERE latest.organization_id = candidate.organization_id
            AND latest.mapping_id = candidate.mapping_id
            AND latest.change_request_id = candidate.change_request_id
        ORDER BY latest.created_at DESC, latest.id DESC
        LIMIT 1
    )
RETURNING candidate.*;

-- name: ReopenProofChangeCandidateByKey :one
UPDATE proof_change_candidates candidate
SET closed_at = NULL
WHERE candidate.organization_id = (
        SELECT organization.id
        FROM organizations organization
        WHERE organization.slug = sqlc.arg('org_slug')
    )
    AND candidate.candidate_key = sqlc.arg('candidate_key')
RETURNING candidate.*;

-- name: CreateProofChangeCandidate :one
INSERT INTO proof_change_candidates (
    organization_id,
    mapping_id,
    slug,
    candidate_key,
    change_request_id,
    source_ref,
    head_sha,
    source_sha256,
    spec_hash,
    canonical_spec,
    supersedes_candidate_id
)
SELECT
    mapping.organization_id,
    mapping.id,
    sqlc.arg('candidate_slug'),
    sqlc.arg('candidate_key'),
    sqlc.arg('change_request_id'),
    sqlc.arg('source_ref'),
    sqlc.arg('head_sha'),
    sqlc.arg('source_sha256'),
    sqlc.arg('spec_hash'),
    sqlc.arg('canonical_spec'),
    sqlc.narg('supersedes_candidate_id')
FROM active_proof_candidate_mappings mapping
WHERE mapping.id = sqlc.arg('mapping_id')
    AND mapping.organization_id = (
        SELECT organization.id
        FROM organizations organization
        WHERE organization.slug = sqlc.arg('org_slug')
    )
RETURNING *;
-- name: CreateGitHubAppInstallIntent :one
INSERT INTO github_app_install_intents (organization_id, actor_slug, state_hash, expires_at, repository_setup_slug, requested_account_login)
SELECT organization.id, sqlc.arg('actor_slug'), sqlc.arg('state_hash'), sqlc.arg('expires_at'), sqlc.narg('repository_setup_slug'), sqlc.narg('requested_account_login')
FROM organizations organization
WHERE organization.slug = sqlc.arg('organization_slug')
RETURNING *;

-- name: GetGitHubAppInstallIntentForUpdate :one
SELECT intent.*, organization.slug AS organization_slug
FROM github_app_install_intents intent
JOIN organizations organization ON organization.id = intent.organization_id
WHERE intent.state_hash = sqlc.arg('state_hash')
FOR UPDATE OF intent;

-- name: GetGitHubAppInstallIntent :one
SELECT intent.*, organization.slug AS organization_slug
FROM github_app_install_intents intent
JOIN organizations organization ON organization.id = intent.organization_id
WHERE intent.state_hash = sqlc.arg('state_hash');

-- name: ConsumeGitHubAppInstallIntent :one
UPDATE github_app_install_intents
SET consumed_at = NOW()
WHERE id = sqlc.arg('id') AND consumed_at IS NULL
RETURNING id;

-- name: UpsertGitHubAppInstallation :one
INSERT INTO github_app_installations (
    organization_id, installation_id, account_id, account_login, account_type, repository_selection
)
VALUES (
    sqlc.arg('organization_id'), sqlc.arg('installation_id'), sqlc.arg('account_id'),
    sqlc.arg('account_login'), sqlc.arg('account_type'), sqlc.arg('repository_selection')
)
ON CONFLICT (organization_id) DO UPDATE SET
    installation_id = EXCLUDED.installation_id,
    account_id = EXCLUDED.account_id,
    account_login = EXCLUDED.account_login,
    account_type = EXCLUDED.account_type,
    repository_selection = EXCLUDED.repository_selection,
    suspended_at = NULL,
    deleted_at = NULL,
    updated_at = NOW()
WHERE github_app_installations.installation_id = EXCLUDED.installation_id OR sqlc.arg('allow_replacement')::boolean
RETURNING *;

-- name: UpsertGitHubAppRepository :exec
INSERT INTO github_app_repositories (
    organization_id, installation_id, repository_id, full_name, owner_login, name
)
VALUES (
    sqlc.arg('organization_id'), sqlc.arg('installation_id'), sqlc.arg('repository_id'),
    sqlc.arg('full_name'), sqlc.arg('owner_login'), sqlc.arg('name')
)
ON CONFLICT (installation_id, repository_id) DO UPDATE SET
    full_name = EXCLUDED.full_name,
    owner_login = EXCLUDED.owner_login,
    name = EXCLUDED.name,
    removed_at = NULL,
    updated_at = NOW();

-- name: RemoveMissingGitHubAppRepositories :exec
UPDATE github_app_repositories
SET removed_at = NOW(), updated_at = NOW()
WHERE organization_id = sqlc.arg('organization_id')
  AND installation_id = sqlc.arg('installation_id')
  AND removed_at IS NULL
  AND NOT (repository_id = ANY(sqlc.arg('repository_ids')::BIGINT[]));

-- name: GetGitHubAppInstallationByOrg :one
SELECT installation.*
FROM github_app_installations installation
JOIN organizations organization ON organization.id = installation.organization_id
WHERE organization.slug = sqlc.arg('organization_slug');

-- name: ListGitHubAppRepositoriesByOrg :many
SELECT repository.*
FROM github_app_repositories repository
JOIN github_app_installations installation
  ON installation.organization_id = repository.organization_id
 AND installation.id = repository.installation_id
JOIN organizations organization ON organization.id = repository.organization_id
WHERE organization.slug = sqlc.arg('organization_slug')
  AND repository.removed_at IS NULL
  AND installation.deleted_at IS NULL
ORDER BY repository.full_name;

-- name: DisconnectGitHubAppInstallation :execrows
UPDATE github_app_installations installation
SET deleted_at = NOW(), updated_at = NOW()
FROM organizations organization
WHERE organization.id = installation.organization_id
  AND organization.slug = sqlc.arg('organization_slug')
  AND installation.deleted_at IS NULL;

-- name: GetGitHubAppOrganizationByInstallation :one
SELECT organization.slug AS organization_slug
FROM github_app_installations installation
JOIN organizations organization ON organization.id = installation.organization_id
WHERE installation.installation_id = sqlc.arg('provider_installation_id')
  AND installation.deleted_at IS NULL
FOR UPDATE OF installation;

-- name: ResolveGitHubAppRepositoryBinding :one
SELECT organization.slug AS organization_slug, installation.installation_id
FROM github_app_repositories repository
JOIN github_app_installations installation
  ON installation.organization_id = repository.organization_id
 AND installation.id = repository.installation_id
JOIN organizations organization ON organization.id = repository.organization_id
WHERE repository.repository_id = sqlc.arg('repository_id')
  AND installation.installation_id = sqlc.arg('provider_installation_id')
  AND repository.removed_at IS NULL
  AND installation.suspended_at IS NULL
  AND installation.deleted_at IS NULL;

-- name: UpdateGitHubAppInstallationLifecycle :execrows
UPDATE github_app_installations
SET suspended_at = CASE sqlc.arg('action')::TEXT
        WHEN 'suspend' THEN NOW()
        WHEN 'unsuspend' THEN NULL
        ELSE suspended_at
    END,
    deleted_at = CASE WHEN sqlc.arg('action')::TEXT = 'deleted' THEN NOW() ELSE deleted_at END,
    updated_at = NOW()
WHERE installation_id = sqlc.arg('provider_installation_id')
  AND deleted_at IS NULL;

-- name: AddGitHubAppRepositoryByProviderInstallation :execrows
INSERT INTO github_app_repositories (organization_id, installation_id, repository_id, full_name, owner_login, name)
SELECT installation.organization_id, installation.id, sqlc.arg('repository_id'), sqlc.arg('full_name'),
       sqlc.arg('owner_login'), sqlc.arg('name')
FROM github_app_installations installation
WHERE installation.installation_id = sqlc.arg('provider_installation_id')
  AND installation.deleted_at IS NULL
ON CONFLICT (installation_id, repository_id) DO UPDATE SET
    full_name = EXCLUDED.full_name, owner_login = EXCLUDED.owner_login, name = EXCLUDED.name,
    removed_at = NULL, updated_at = NOW();

-- name: RemoveGitHubAppRepositoryByProviderInstallation :execrows
UPDATE github_app_repositories repository
SET removed_at = NOW(), updated_at = NOW()
FROM github_app_installations installation
WHERE installation.id = repository.installation_id
  AND installation.installation_id = sqlc.arg('provider_installation_id')
  AND repository.repository_id = sqlc.arg('repository_id')
  AND repository.removed_at IS NULL;
-- name: CreateProofDeploymentLink :one
WITH target AS (
    SELECT
        o.id AS organization_id,
        p.id AS project_id,
        pe.id AS project_environment_id,
        pd.id AS deployment_id
    FROM organizations o
    JOIN projects p ON p.organization_id = o.id AND p.slug = sqlc.arg('project_slug')
    JOIN project_environments pe ON pe.project_id = p.id AND pe.deleted_at IS NULL
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
    JOIN pipeline_deployments pd ON pd.project_environment_id = pe.id
        AND pd.pipeline_slug = sqlc.arg('pipeline_slug')
    WHERE o.slug = sqlc.arg('org_slug')
), receipt AS (
    SELECT pr.id, pr.candidate_id, pr.slug, pr.binding_fingerprint, pr.capability_fingerprint
    FROM proof_receipts pr
    JOIN target t ON t.organization_id = pr.organization_id
        AND t.project_id = pr.project_id
        AND t.project_environment_id = pr.project_environment_id
    WHERE pr.slug = sqlc.arg('receipt_slug')
        AND pr.receipt->'operational' IS NULL
        AND LEAST(pr.retain_until, pr.created_at + interval '168 hours') > statement_timestamp()
    FOR KEY SHARE OF pr
), inserted AS (
INSERT INTO proof_lifecycle_links (
    organization_id, project_id, project_environment_id, receipt_id, candidate_id,
    kind, identifier, deployment_id, spec_hash, binding_fingerprint, capability_fingerprint, linked_at
)
SELECT
    t.organization_id, t.project_id, t.project_environment_id, r.id, r.candidate_id,
    'deployment', sqlc.arg('identifier'), t.deployment_id, sqlc.arg('spec_hash'),
    r.binding_fingerprint, r.capability_fingerprint, sqlc.arg('linked_at')
FROM target t
JOIN receipt r ON true
ON CONFLICT (organization_id, project_environment_id, kind, identifier) DO NOTHING
RETURNING *
), linked AS (
    SELECT * FROM inserted
    UNION ALL
    SELECT existing.*
    FROM proof_lifecycle_links existing
    JOIN target t ON t.organization_id = existing.organization_id
        AND t.project_id = existing.project_id
        AND t.project_environment_id = existing.project_environment_id
        AND t.deployment_id = existing.deployment_id
        AND existing.spec_hash = sqlc.arg('spec_hash')
    JOIN receipt r ON r.id = existing.receipt_id
        AND r.candidate_id = existing.candidate_id
        AND r.binding_fingerprint = existing.binding_fingerprint
        AND r.capability_fingerprint = existing.capability_fingerprint
    WHERE existing.kind = 'deployment'
        AND existing.identifier = sqlc.arg('identifier')
)
SELECT linked.*, pr.slug AS receipt_slug, LEAST(pr.retain_until, pr.created_at + interval '168 hours') AS retain_until,
    EXISTS (
        SELECT 1 FROM proof_receipts newer
        WHERE newer.organization_id = pr.organization_id
            AND newer.supersedes_receipt_id = pr.id
    ) AS superseded
FROM linked
JOIN proof_receipts pr ON pr.organization_id = linked.organization_id AND pr.id = linked.receipt_id
LIMIT 1;

-- name: CreateProofRunLink :one
WITH target AS (
    SELECT
        o.id AS organization_id,
        p.id AS project_id,
        pe.id AS project_environment_id,
        pr.id AS run_id,
        pr.pipeline_version_spec_hash AS spec_hash
    FROM organizations o
    JOIN projects p ON p.organization_id = o.id AND p.slug = sqlc.arg('project_slug')
    JOIN project_environments pe ON pe.project_id = p.id AND pe.deleted_at IS NULL
    JOIN environments e ON e.id = pe.environment_id
        AND e.organization_id = o.id
        AND e.slug = sqlc.arg('environment_slug')
        AND e.deleted_at IS NULL
    JOIN pipeline_runs pr ON pr.project_environment_id = pe.id
        AND pr.run_slug = sqlc.arg('run_slug')
    WHERE o.slug = sqlc.arg('org_slug')
), deployment_link AS (
    SELECT pll.*
    FROM proof_lifecycle_links pll
    JOIN proof_receipts receipt ON receipt.organization_id = pll.organization_id AND receipt.id = pll.receipt_id
        AND receipt.receipt->'operational' IS NULL
        AND LEAST(receipt.retain_until, receipt.created_at + interval '168 hours') > statement_timestamp()
    JOIN target t ON t.organization_id = pll.organization_id
        AND t.project_id = pll.project_id
        AND t.project_environment_id = pll.project_environment_id
        AND t.spec_hash = pll.spec_hash
    WHERE pll.kind = 'deployment'
        AND pll.deployment_id = sqlc.arg('deployment_id')
        AND pll.identifier = sqlc.arg('deployment_identifier')
    ORDER BY pll.linked_at DESC, pll.id DESC
    LIMIT 1
), inserted AS (
INSERT INTO proof_lifecycle_links (
    organization_id, project_id, project_environment_id, receipt_id, candidate_id,
    kind, identifier, deployment_id, run_id, spec_hash, binding_fingerprint, capability_fingerprint, linked_at
)
SELECT
    dl.organization_id, dl.project_id, dl.project_environment_id, dl.receipt_id, dl.candidate_id,
    'run', sqlc.arg('identifier'), dl.deployment_id, t.run_id, t.spec_hash,
    dl.binding_fingerprint, dl.capability_fingerprint, sqlc.arg('linked_at')
FROM deployment_link dl
JOIN target t ON true
ON CONFLICT (organization_id, project_environment_id, kind, identifier) DO NOTHING
RETURNING *
), linked AS (
    SELECT * FROM inserted
    UNION ALL
    SELECT existing.*
    FROM proof_lifecycle_links existing
    JOIN target t ON t.organization_id = existing.organization_id
        AND t.project_id = existing.project_id
        AND t.project_environment_id = existing.project_environment_id
        AND t.run_id = existing.run_id
        AND t.spec_hash = existing.spec_hash
    JOIN deployment_link dl ON dl.receipt_id = existing.receipt_id
        AND dl.candidate_id = existing.candidate_id
        AND dl.deployment_id = existing.deployment_id
        AND dl.binding_fingerprint = existing.binding_fingerprint
        AND dl.capability_fingerprint = existing.capability_fingerprint
    WHERE existing.kind = 'run'
        AND existing.identifier = sqlc.arg('identifier')
)
SELECT linked.*, pr.slug AS receipt_slug, LEAST(pr.retain_until, pr.created_at + interval '168 hours') AS retain_until,
    EXISTS (
        SELECT 1 FROM proof_receipts newer
        WHERE newer.organization_id = pr.organization_id
            AND newer.supersedes_receipt_id = pr.id
    ) AS superseded
FROM linked
JOIN proof_receipts pr ON pr.organization_id = linked.organization_id AND pr.id = linked.receipt_id
LIMIT 1;

-- name: FindProofDeploymentLink :one
SELECT pll.*, pr.slug AS receipt_slug, LEAST(pr.retain_until, pr.created_at + interval '168 hours') AS retain_until,
    EXISTS (
        SELECT 1 FROM proof_receipts newer
        WHERE newer.organization_id = pr.organization_id
            AND newer.supersedes_receipt_id = pr.id
    ) AS superseded
FROM proof_lifecycle_links pll
JOIN proof_receipts pr ON pr.organization_id = pll.organization_id AND pr.id = pll.receipt_id
JOIN organizations o ON o.id = pll.organization_id
JOIN projects p ON p.id = pll.project_id AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = pll.project_environment_id AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
JOIN pipeline_deployments deployment ON deployment.id = pll.deployment_id
    AND deployment.project_environment_id = pe.id
WHERE LEAST(pr.retain_until, pr.created_at + interval '168 hours') > statement_timestamp()
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND deployment.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pll.kind = 'deployment'
ORDER BY pll.linked_at DESC, pll.id DESC
LIMIT 1;

-- name: FindProofRunLink :one
SELECT pll.*, pr.slug AS receipt_slug, LEAST(pr.retain_until, pr.created_at + interval '168 hours') AS retain_until,
    EXISTS (
        SELECT 1 FROM proof_receipts newer
        WHERE newer.organization_id = pr.organization_id
            AND newer.supersedes_receipt_id = pr.id
    ) AS superseded
FROM proof_lifecycle_links pll
JOIN proof_receipts pr ON pr.organization_id = pll.organization_id AND pr.id = pll.receipt_id
JOIN organizations o ON o.id = pll.organization_id
JOIN projects p ON p.id = pll.project_id AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = pll.project_environment_id AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
JOIN pipeline_runs run ON run.id = pll.run_id AND run.project_environment_id = pe.id
WHERE LEAST(pr.retain_until, pr.created_at + interval '168 hours') > statement_timestamp()
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND run.run_slug = sqlc.arg('run_slug')
    AND pll.kind = 'run'
LIMIT 1;

-- name: ListProofLifecycleLinksByReceipt :many
SELECT pll.*, pr.slug AS receipt_slug, LEAST(pr.retain_until, pr.created_at + interval '168 hours') AS retain_until,
    EXISTS (
        SELECT 1 FROM proof_receipts newer
        WHERE newer.organization_id = pr.organization_id
            AND newer.supersedes_receipt_id = pr.id
    ) AS superseded,
    deployment.pipeline_slug,
    COALESCE(run_version.spec_hash, current_version.spec_hash) AS current_version_spec_hash,
    deployment.deployed_at,
    run.run_slug
FROM proof_lifecycle_links pll
JOIN proof_receipts pr ON pr.organization_id = pll.organization_id AND pr.id = pll.receipt_id
JOIN organizations o ON o.id = pll.organization_id
JOIN projects p ON p.id = pll.project_id AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = pll.project_environment_id AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
LEFT JOIN pipeline_deployments deployment ON deployment.id = pll.deployment_id
    AND deployment.project_environment_id = pe.id
LEFT JOIN pipeline_versions current_version ON current_version.id = deployment.current_version_id
LEFT JOIN pipeline_runs run ON run.id = pll.run_id
    AND run.project_environment_id = pe.id
LEFT JOIN pipeline_versions run_version ON run_version.id = run.pipeline_version_id
WHERE LEAST(pr.retain_until, pr.created_at + interval '168 hours') > statement_timestamp()
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND pr.slug = sqlc.arg('receipt_slug')
ORDER BY pll.linked_at, pll.id
LIMIT 1000;

-- name: SaveProofObservationPolicy :one
INSERT INTO proof_observation_policies (
    organization_id, project_id, project_environment_id, version, enabled, worker_identity, policy
)
SELECT o.id, p.id, pe.id, sqlc.arg('version'), sqlc.arg('enabled'), sqlc.arg('worker_identity'), sqlc.arg('policy')::jsonb
FROM organizations o
JOIN projects p ON p.organization_id = o.id
JOIN project_environments pe ON pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug') AND e.slug = sqlc.arg('environment_slug')
    AND pe.deleted_at IS NULL AND e.deleted_at IS NULL
ON CONFLICT (organization_id, project_environment_id) DO UPDATE
SET version = EXCLUDED.version, enabled = EXCLUDED.enabled, worker_identity = EXCLUDED.worker_identity,
    policy = EXCLUDED.policy, updated_at = clock_timestamp()
WHERE proof_observation_policies.version <> EXCLUDED.version
   OR (proof_observation_policies.policy = EXCLUDED.policy AND proof_observation_policies.worker_identity = EXCLUDED.worker_identity
       AND proof_observation_policies.enabled = EXCLUDED.enabled)
RETURNING *;

-- name: LockProofObservationPolicy :one
SELECT policy.* FROM proof_observation_policies policy
JOIN organizations o ON o.id = policy.organization_id
JOIN projects p ON p.id = policy.project_id AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = policy.project_environment_id AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug') AND e.slug = sqlc.arg('environment_slug')
    AND pe.deleted_at IS NULL AND e.deleted_at IS NULL
FOR UPDATE OF policy;

-- name: LockProofObservationPolicyForCleanup :one
SELECT policy.* FROM proof_observation_policies policy
JOIN organizations o ON o.id = policy.organization_id
JOIN projects p ON p.id = policy.project_id AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = policy.project_environment_id AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug') AND e.slug = sqlc.arg('environment_slug')
FOR UPDATE OF policy;

-- name: FindProofObservationJob :one
SELECT job.* FROM proof_observation_jobs job
JOIN organizations o ON o.id = job.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND job.slug = sqlc.arg('job_slug');

-- name: IsProofObservationActive :one
SELECT EXISTS (
    SELECT 1 FROM proof_observation_jobs job
    JOIN organizations o ON o.id = job.organization_id
    JOIN data_sources ds ON ds.organization_id = job.organization_id AND ds.id = job.data_source_id
    WHERE o.slug = sqlc.arg('org_slug') AND job.slug = sqlc.arg('job_slug')
        AND job.request_digest = sqlc.arg('request_digest') AND job.state = 'running'
        AND job.canceled_at IS NULL AND job.deadline_at > clock_timestamp()
        AND ds.is_active AND ds.deleted_at IS NULL
) AS active;

-- name: ProofObservationBudgetUsage :one
SELECT
    count(*) FILTER (WHERE completed_at IS NULL AND deadline_at > statement_timestamp())::bigint AS active_jobs,
    coalesce(sum(reserved_requests) FILTER (WHERE budget_expires_at > statement_timestamp()), 0)::bigint AS requests,
    coalesce(sum(reserved_bytes) FILTER (WHERE budget_expires_at > statement_timestamp()), 0)::bigint AS bytes,
    coalesce(sum(reserved_rows) FILTER (WHERE budget_expires_at > statement_timestamp()), 0)::bigint AS rows,
    coalesce(sum(reserved_cost) FILTER (WHERE budget_expires_at > statement_timestamp()), 0)::bigint AS cost
FROM proof_observation_jobs
WHERE organization_id = sqlc.arg('organization_id') AND project_environment_id = sqlc.arg('project_environment_id')
    AND connector_type = sqlc.arg('connector_type') AND operation = sqlc.arg('operation')
    AND budget_expires_at > statement_timestamp();

-- name: CreateProofObservationJob :one
INSERT INTO proof_observation_jobs (
    organization_id, project_id, project_environment_id, candidate_id, data_source_id, slug,
    request_digest, request, authorization_decision, target_ciphertext, policy_version, worker_identity,
    connector_type, operation, reserved_requests, reserved_bytes, reserved_rows, reserved_cost,
    budget_expires_at, deadline_at, evidence_retain_until, metadata_retain_until
)
SELECT o.id, m.project_id, m.project_environment_id, candidate.id, ds.id, sqlc.arg('job_slug'),
    sqlc.arg('request_digest'), sqlc.arg('request')::jsonb, sqlc.arg('authorization')::jsonb, sqlc.arg('target_ciphertext'),
    sqlc.arg('policy_version'), sqlc.arg('worker_identity'), ds.type, sqlc.arg('operation'),
    sqlc.arg('reserved_requests'), sqlc.arg('reserved_bytes'), sqlc.arg('reserved_rows'), sqlc.arg('reserved_cost'),
    sqlc.arg('budget_expires_at'), LEAST(sqlc.arg('deadline_at')::timestamptz, clock_timestamp() + sqlc.arg('timeout_microseconds')::bigint * interval '1 microsecond'), sqlc.arg('evidence_retain_until'), sqlc.arg('metadata_retain_until')
FROM organizations o
JOIN proof_change_candidates candidate ON candidate.organization_id = o.id
JOIN proof_change_candidate_mappings m ON m.organization_id = o.id AND m.id = candidate.mapping_id
JOIN data_sources provider ON provider.organization_id = o.id AND provider.id = m.data_source_id
JOIN data_sources ds ON ds.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug') AND candidate.slug = sqlc.arg('candidate_slug')
    AND candidate.closed_at IS NULL AND candidate.head_sha = sqlc.arg('head_sha') AND candidate.spec_hash = sqlc.arg('spec_hash')
    AND m.project_id = sqlc.arg('project_id') AND m.project_environment_id = sqlc.arg('project_environment_id')
    AND candidate.change_request_id = (sqlc.arg('request')::jsonb #>> '{candidate,change_request_id}')
    AND m.repository_id = (sqlc.arg('request')::jsonb #>> '{candidate,repository_id}')
    AND m.target_ref = (sqlc.arg('request')::jsonb #>> '{candidate,target_ref}')
    AND m.pipeline_path = (sqlc.arg('request')::jsonb #>> '{candidate,pipeline_path}')
    AND provider.slug = (sqlc.arg('request')::jsonb #>> '{candidate,provider_connection_slug}')
    AND ds.slug = sqlc.arg('data_source_slug') AND ds.type = sqlc.arg('connector_type') AND ds.deleted_at IS NULL AND ds.is_active = true
ON CONFLICT (organization_id, slug) DO NOTHING
RETURNING *;

-- name: CancelProofObservationJob :execrows
UPDATE proof_observation_jobs job
SET canceled_at = coalesce(canceled_at, clock_timestamp()),
    state = CASE WHEN state = 'pending' THEN 'finished' ELSE state END,
    completed_at = CASE WHEN state = 'pending' THEN clock_timestamp() ELSE completed_at END,
    result = CASE WHEN state = 'pending' THEN '{"outcome":"canceled"}'::jsonb ELSE result END
FROM organizations o
WHERE o.id = job.organization_id AND o.slug = sqlc.arg('org_slug') AND job.slug = sqlc.arg('job_slug')
    AND job.state IN ('pending', 'running');

-- name: ExpireProofObservationJobs :many
WITH selected AS (
    SELECT id FROM proof_observation_jobs
    WHERE organization_id = sqlc.arg('organization_id')::integer AND project_environment_id = sqlc.arg('project_environment_id')::bigint
        AND state IN ('pending', 'running') AND deadline_at <= clock_timestamp()
    ORDER BY id LIMIT 100 FOR UPDATE
)
UPDATE proof_observation_jobs job
SET state = 'finished', completed_at = clock_timestamp(), snapshot_ciphertext = NULL,
    result = jsonb_build_object('outcome', CASE WHEN job.canceled_at IS NOT NULL THEN 'canceled' ELSE 'worker_lost' END)
FROM selected WHERE job.id = selected.id
RETURNING job.slug, job.request_digest;

-- name: PurgeProofObservationSnapshots :many
WITH selected AS (
    SELECT id FROM proof_observation_jobs
    WHERE organization_id = sqlc.arg('organization_id')::integer AND project_environment_id = sqlc.arg('project_environment_id')::bigint
        AND evidence_retain_until <= clock_timestamp() AND (target_ciphertext IS NOT NULL OR snapshot_ciphertext IS NOT NULL)
    ORDER BY id LIMIT 100 FOR UPDATE
)
UPDATE proof_observation_jobs job SET target_ciphertext = NULL, snapshot_ciphertext = NULL
FROM selected WHERE job.id = selected.id
RETURNING job.slug, job.request_digest;

-- name: DeleteExpiredProofObservationJobs :many
WITH selected AS (
    SELECT id FROM proof_observation_jobs
    WHERE organization_id = sqlc.arg('organization_id')::integer AND project_environment_id = sqlc.arg('project_environment_id')::bigint
        AND metadata_retain_until <= clock_timestamp() AND budget_expires_at <= clock_timestamp() AND completed_at IS NOT NULL
    ORDER BY id LIMIT 100 FOR UPDATE
)
DELETE FROM proof_observation_jobs job USING selected WHERE job.id = selected.id
RETURNING job.slug, job.request_digest;

-- name: StartProofObservation :execrows
UPDATE proof_observation_jobs job
SET state = 'running', started_at = clock_timestamp()
FROM organizations o
WHERE o.id = job.organization_id AND o.slug = sqlc.arg('org_slug') AND job.slug = sqlc.arg('job_slug')
    AND job.request_digest = sqlc.arg('request_digest') AND job.state = 'pending'
    AND job.canceled_at IS NULL AND job.deadline_at > clock_timestamp();

-- name: FinishProofObservation :execrows
UPDATE proof_observation_jobs job
SET state = 'finished', completed_at = clock_timestamp(),
    result = sqlc.arg('observation')::jsonb, snapshot_ciphertext = sqlc.narg('protected_snapshot')::bytea
FROM organizations o
WHERE o.id = job.organization_id AND o.slug = sqlc.arg('org_slug') AND job.slug = sqlc.arg('job_slug')
    AND job.request_digest = sqlc.arg('request_digest') AND job.state = 'running'
    AND job.canceled_at IS NULL AND job.deadline_at > clock_timestamp();

-- name: FinishInterruptedProofObservation :execrows
UPDATE proof_observation_jobs job
SET state = 'finished', completed_at = clock_timestamp(), snapshot_ciphertext = NULL,
    result = jsonb_build_object('outcome', CASE WHEN canceled_at IS NOT NULL THEN 'canceled' ELSE 'worker_lost' END)
FROM organizations o
WHERE o.id = job.organization_id AND o.slug = sqlc.arg('org_slug') AND job.slug = sqlc.arg('job_slug')
    AND job.request_digest = sqlc.arg('request_digest') AND job.state = 'running'
    AND (job.canceled_at IS NOT NULL OR job.deadline_at <= clock_timestamp());

-- name: CancelObsoleteProofObservationJobs :many
WITH selected AS (
SELECT job.id
FROM proof_observation_jobs job JOIN proof_observation_policies policy
    ON policy.organization_id = job.organization_id AND policy.project_environment_id = job.project_environment_id
WHERE job.organization_id = sqlc.arg('organization_id')::integer AND job.project_environment_id = sqlc.arg('project_environment_id')::bigint
    AND job.state IN ('pending', 'running') AND job.canceled_at IS NULL
    AND (NOT policy.enabled OR policy.version <> job.policy_version OR policy.worker_identity <> job.worker_identity)
ORDER BY job.id LIMIT 100 FOR UPDATE OF job
)
UPDATE proof_observation_jobs job
SET canceled_at = coalesce(job.canceled_at, clock_timestamp()),
    state = CASE WHEN job.state = 'pending' THEN 'finished' ELSE job.state END,
    completed_at = CASE WHEN job.state = 'pending' THEN clock_timestamp() ELSE job.completed_at END,
    result = CASE WHEN job.state = 'pending' THEN '{"outcome":"canceled"}'::jsonb ELSE job.result END
FROM selected WHERE job.id = selected.id
RETURNING job.slug, job.request_digest;

-- name: FindScopedProofObservationJob :one
SELECT job.* FROM proof_observation_jobs job
JOIN organizations o ON o.id = job.organization_id
JOIN projects p ON p.id = job.project_id AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = job.project_environment_id AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND job.slug = sqlc.arg('job_slug')
    AND job.metadata_retain_until > statement_timestamp();

-- name: FindProofObservationSlugByDigest :one
SELECT job.slug FROM proof_observation_jobs job
JOIN organizations organization ON organization.id = job.organization_id
WHERE organization.slug = sqlc.arg('org_slug') AND job.request_digest = sqlc.arg('request_digest')
LIMIT 1;

-- name: ListProofObservationMaintenanceScopes :many
SELECT policy.organization_id, policy.project_environment_id, o.slug AS organization_slug, p.slug AS project_slug, e.slug AS environment_slug,
    (e.deleted_at IS NOT NULL OR pe.deleted_at IS NOT NULL)::boolean AS disabled
FROM proof_observation_policies policy
JOIN organizations o ON o.id = policy.organization_id
JOIN projects p ON p.id = policy.project_id AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = policy.project_environment_id AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
WHERE (policy.organization_id, policy.project_environment_id) > (sqlc.arg('after_organization_id')::integer, sqlc.arg('after_project_environment_id')::bigint)
ORDER BY policy.organization_id, policy.project_environment_id
LIMIT 100;

-- name: LockProofObservationMaintenanceScope :one
WITH target AS MATERIALIZED (
    SELECT o.id AS organization_id, pe.id AS project_environment_id, o.slug AS organization_slug, p.slug AS project_slug, e.slug AS environment_slug,
        (e.deleted_at IS NOT NULL OR pe.deleted_at IS NOT NULL)::boolean AS disabled
    FROM organizations o JOIN projects p ON p.organization_id = o.id
    JOIN project_environments pe ON pe.project_id = p.id
    JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug') AND e.slug = sqlc.arg('environment_slug')
    FOR SHARE OF pe, e
)
SELECT target.* FROM target JOIN proof_observation_policies policy
    ON policy.organization_id = target.organization_id AND policy.project_environment_id = target.project_environment_id
FOR UPDATE OF policy;

-- name: DisableProofObservationPolicy :exec
UPDATE proof_observation_policies
SET enabled = false, policy = jsonb_set(policy, '{Enabled}', 'false'::jsonb), updated_at = clock_timestamp()
WHERE organization_id = sqlc.arg('organization_id') AND project_environment_id = sqlc.arg('project_environment_id') AND enabled;

-- name: ProofObservationMaintenanceHealth :one
-- Deployment-only aggregates: no tenant identities or protected values leave this query.
SELECT
    (SELECT coalesce(extract(epoch FROM max(finalized_at)), 0)::double precision
     FROM river_job WHERE kind = 'proof_observation_maintenance' AND queue = 'event_maintenance' AND state = 'completed') AS last_success_timestamp,
    (SELECT count(*) FROM river_job WHERE kind = 'proof_observation_maintenance' AND queue = 'event_maintenance'
        AND state = 'discarded' AND finalized_at > statement_timestamp() - interval '10 minutes') AS recent_failures,
    count(*) FILTER (WHERE job.state IN ('pending', 'running') AND job.canceled_at IS NULL
        AND (NOT policy.enabled OR policy.version <> job.policy_version OR policy.worker_identity <> job.worker_identity
            OR pe.deleted_at IS NOT NULL OR e.deleted_at IS NOT NULL)) AS cancellation_due,
    count(*) FILTER (WHERE job.state IN ('pending', 'running') AND job.deadline_at <= statement_timestamp()) AS expiry_due,
    count(*) FILTER (WHERE job.evidence_retain_until <= statement_timestamp()
        AND (job.target_ciphertext IS NOT NULL OR job.snapshot_ciphertext IS NOT NULL)) AS evidence_due,
    count(*) FILTER (WHERE job.metadata_retain_until <= statement_timestamp()
        AND job.budget_expires_at <= statement_timestamp() AND job.completed_at IS NOT NULL) AS metadata_due
FROM proof_observation_jobs job
JOIN proof_observation_policies policy ON policy.organization_id = job.organization_id AND policy.project_environment_id = job.project_environment_id
JOIN project_environments pe ON pe.id = job.project_environment_id AND pe.project_id = job.project_id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = job.organization_id;

-- name: ProofObservationActivity :many
-- Deployment-only counts. The repository maps stored outcomes to the closed vocabulary.
WITH results AS (
    SELECT coalesce(result->>'outcome', 'unknown')::text AS outcome, count(*) AS jobs
    FROM proof_observation_jobs
    WHERE state = 'finished' AND completed_at > statement_timestamp() - interval '10 minutes'
        AND completed_at <= statement_timestamp()
    GROUP BY 1
), starts AS (
    SELECT count(*) AS jobs FROM proof_observation_jobs
    WHERE started_at > statement_timestamp() - interval '10 minutes' AND started_at <= statement_timestamp()
)
SELECT starts.jobs AS starts, coalesce(results.outcome, '')::text AS outcome, coalesce(results.jobs, 0)::bigint AS results
FROM starts LEFT JOIN results ON true;

-- =====================
-- Snowflake workload identity lifecycle
-- =====================

-- name: LockSnowflakeWIFConnection :one
SELECT ds.id
FROM data_sources ds JOIN organizations o ON o.id = ds.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND ds.slug = sqlc.arg('ds_slug')
FOR UPDATE OF ds;

-- name: RevokeOtherSnowflakeWIFBindings :exec
UPDATE snowflake_wif_bindings b
SET desired = false, state = 'revoking', verified_at = NULL, reconcile_after = CURRENT_TIMESTAMP
FROM organizations o, data_sources ds
WHERE o.id = b.organization_id AND ds.organization_id = b.organization_id AND ds.id = b.data_source_id
    AND o.slug = sqlc.arg('org_slug') AND ds.slug = sqlc.arg('ds_slug')
    AND b.desired
    AND (NOT sqlc.arg('enabled')::boolean OR b.account <> sqlc.arg('account') OR b.issuer <> sqlc.arg('issuer'));

-- name: UpsertSnowflakeWIFBinding :exec
INSERT INTO snowflake_wif_bindings(organization_id, data_source_id, issuer, account)
SELECT ds.organization_id, ds.id, sqlc.arg('issuer'), sqlc.arg('account')
FROM data_sources ds JOIN organizations o ON o.id = ds.organization_id
JOIN credentials c ON c.organization_id = ds.organization_id AND c.id = ds.credential_id
WHERE o.slug = sqlc.arg('org_slug') AND ds.slug = sqlc.arg('ds_slug')
    AND ds.deleted_at IS NULL AND ds.type = 'snowflake'
    AND ds.config->>'account' = sqlc.arg('account') AND c.status = 'active'
ON CONFLICT (organization_id, data_source_id, issuer, account) DO UPDATE
SET desired = true, state = 'pending', verified_at = NULL, reconcile_after = CURRENT_TIMESTAMP
WHERE NOT snowflake_wif_bindings.desired;

-- name: ListSnowflakeWIFReconciliation :many
SELECT b.id, o.slug AS organization, ds.slug AS data_source
FROM snowflake_wif_bindings b
JOIN organizations o ON o.id = b.organization_id
JOIN data_sources ds ON ds.organization_id = b.organization_id AND ds.id = b.data_source_id
WHERE b.reconcile_after <= CURRENT_TIMESTAMP
ORDER BY b.reconcile_after, b.id
LIMIT 100;

-- name: GetSnowflakeWIFBindingForReconciliation :one
SELECT b.*, o.slug AS organization, ds.slug AS data_source,
    (b.desired AND ds.deleted_at IS NULL AND ds.type = 'snowflake'
        AND ds.config->>'account' = b.account AND c.status = 'active')::boolean AS effective_desired
FROM snowflake_wif_bindings b
JOIN organizations o ON o.id = b.organization_id
JOIN data_sources ds ON ds.organization_id = b.organization_id AND ds.id = b.data_source_id
JOIN credentials c ON c.organization_id = ds.organization_id AND c.id = ds.credential_id
WHERE b.id = sqlc.arg('binding_id') AND o.slug = sqlc.arg('org_slug') AND ds.slug = sqlc.arg('ds_slug')
    AND b.reconcile_after <= CURRENT_TIMESTAMP
FOR UPDATE OF b;

-- name: SetSnowflakeWIFBindingState :exec
UPDATE snowflake_wif_bindings b
SET desired = sqlc.arg('desired'), state = sqlc.arg('state'), entry_id = sqlc.arg('entry_id'),
    verified_at = CASE WHEN sqlc.arg('state')::text = 'ready' THEN CURRENT_TIMESTAMP ELSE NULL END,
    reconcile_after = CURRENT_TIMESTAMP + INTERVAL '20 seconds'
FROM organizations o, data_sources ds
WHERE b.organization_id = o.id AND ds.organization_id = b.organization_id AND ds.id = b.data_source_id
    AND b.id = sqlc.arg('binding_id') AND o.slug = sqlc.arg('org_slug') AND ds.slug = sqlc.arg('ds_slug');

-- name: DeleteRevokedSnowflakeWIFBinding :exec
DELETE FROM snowflake_wif_bindings b
USING organizations o, data_sources ds
WHERE b.organization_id = o.id AND ds.organization_id = b.organization_id AND ds.id = b.data_source_id
    AND b.id = sqlc.arg('binding_id') AND o.slug = sqlc.arg('org_slug') AND ds.slug = sqlc.arg('ds_slug');

-- name: SnowflakeWIFReady :one
SELECT snowflake_wif_ready(sqlc.arg('org_slug')::text, sqlc.arg('ds_slug')::text,
    sqlc.arg('account')::text, sqlc.arg('issuer')::text)::boolean AS ready;

-- name: GetSnowflakeWIFOnboardingState :one
SELECT CASE
    WHEN NOT b.desired THEN 'revoking'
    WHEN b.state = 'ready' AND b.verified_at <= CURRENT_TIMESTAMP - INTERVAL '60 seconds' THEN 'pending'
    ELSE b.state
END::text AS state
FROM snowflake_wif_bindings b
JOIN organizations o ON o.id = b.organization_id
JOIN data_sources ds ON ds.organization_id = b.organization_id AND ds.id = b.data_source_id
WHERE o.slug = sqlc.arg('org_slug') AND ds.slug = sqlc.arg('ds_slug')
    AND ds.deleted_at IS NULL AND ds.type = 'snowflake'
    AND b.account = sqlc.arg('account') AND b.issuer = sqlc.arg('issuer');

-- name: ListDataSourceProofConfigurations :many
SELECT ds.slug, ds.name, ds.type, ds.proof_enabled, ds.proof_checks_enabled,
    COALESCE(ds.config #>> '{repository,id}', '')::text AS repository_id,
    ds.is_active, credential.kind AS credential_kind, credential.status AS credential_status,
    credential.revoked_at,
    NOT EXISTS (SELECT 1 FROM proof_change_candidate_mappings mapping
        WHERE mapping.organization_id = ds.organization_id AND mapping.data_source_id = ds.id
            AND mapping.disabled_at IS NULL
            AND mapping.provider_connection_id IS DISTINCT FROM
                CASE WHEN ds.type = 'github' THEN installation.installation_id::text ELSE ds.slug::text END
    )::boolean AS mappings_current,
    COALESCE(installation.installation_id::text, '')::text AS installation_id,
    (installation.id IS NOT NULL AND installation.suspended_at IS NULL AND installation.deleted_at IS NULL)::boolean AS github_connected,
    EXISTS (SELECT 1 FROM github_app_repositories repository WHERE repository.organization_id=ds.organization_id
        AND repository.installation_id=installation.id AND repository.repository_id::text=ds.config #>> '{repository,id}'
        AND repository.removed_at IS NULL)::boolean AS github_repository_access,
    EXISTS (SELECT 1 FROM gitlab_proof_webhooks webhook WHERE webhook.organization_id=ds.organization_id
        AND webhook.data_source_id=ds.id AND webhook.project_id::text=ds.config #>> '{repository,id}' AND webhook.status='active')::boolean AS gitlab_webhook_ready
FROM data_sources ds
JOIN organizations organization ON organization.id=ds.organization_id
JOIN credentials credential ON credential.organization_id=ds.organization_id AND credential.id=ds.credential_id
LEFT JOIN github_app_installations installation ON installation.organization_id=ds.organization_id
WHERE organization.slug=sqlc.arg('org_slug') AND ds.deleted_at IS NULL
    AND ds.type IN ('github', 'gitlab', 'git')
    AND (sqlc.arg('data_source_slug')::text='' OR ds.slug=sqlc.arg('data_source_slug'))
ORDER BY ds.name, ds.slug;

-- name: ListDataSourceProofTargets :many
SELECT project.slug AS project_slug, environment.slug AS environment_slug, mapping.target_ref, mapping.pipeline_path
FROM proof_change_candidate_mappings mapping
JOIN organizations organization ON organization.id=mapping.organization_id
JOIN data_sources ds ON ds.organization_id=mapping.organization_id AND ds.id=mapping.data_source_id
JOIN projects project ON project.organization_id=mapping.organization_id AND project.id=mapping.project_id
JOIN project_environments pe ON pe.project_id=project.id AND pe.id=mapping.project_environment_id
JOIN environments environment ON environment.organization_id=mapping.organization_id AND environment.id=pe.environment_id
WHERE organization.slug=sqlc.arg('org_slug') AND ds.slug=sqlc.arg('data_source_slug') AND mapping.disabled_at IS NULL
ORDER BY project.slug, environment.slug, mapping.pipeline_path;

-- name: ListDataSourceProofTargetContexts :many
SELECT project.slug AS project_slug, environment.slug AS environment_slug,
    ('refs/heads/' || project.pipeline_backend_tracked_branch)::text AS target_ref,
    project.pipeline_backend_pipeline_dir AS pipeline_directory
FROM projects project
JOIN organizations organization ON organization.id=project.organization_id
JOIN data_sources ds ON ds.organization_id=project.organization_id AND ds.id=project.pipeline_backend_data_source_id
JOIN project_environments pe ON pe.project_id=project.id AND pe.deleted_at IS NULL
JOIN environments environment ON environment.organization_id=project.organization_id AND environment.id=pe.environment_id AND environment.deleted_at IS NULL
WHERE organization.slug=sqlc.arg('org_slug') AND ds.slug=sqlc.arg('data_source_slug') AND ds.deleted_at IS NULL
ORDER BY project.slug, environment.slug;

-- name: SetDataSourceProofControls :execrows
UPDATE data_sources ds
SET proof_enabled=sqlc.arg('enabled'), proof_checks_enabled=sqlc.arg('publish_checks'), updated_at=CURRENT_TIMESTAMP
FROM organizations organization
WHERE organization.id=ds.organization_id AND organization.slug=sqlc.arg('org_slug')
    AND ds.slug=sqlc.arg('data_source_slug') AND ds.deleted_at IS NULL;

-- name: RetireDataSourceProofTargets :exec
UPDATE proof_change_candidate_mappings mapping SET disabled_at=CURRENT_TIMESTAMP
FROM organizations organization, data_sources ds
WHERE organization.id=mapping.organization_id AND ds.organization_id=mapping.organization_id AND ds.id=mapping.data_source_id
    AND organization.slug=sqlc.arg('org_slug') AND ds.slug=sqlc.arg('data_source_slug') AND mapping.disabled_at IS NULL;

-- name: ProofReceiptPublicationEnabled :one
SELECT EXISTS (
    SELECT 1 FROM proof_receipts receipt
    JOIN organizations organization ON organization.id=receipt.organization_id
    JOIN active_proof_candidate_mappings mapping ON mapping.organization_id=receipt.organization_id AND mapping.id=receipt.mapping_id
    JOIN data_sources ds ON ds.organization_id=mapping.organization_id AND ds.id=mapping.data_source_id
    WHERE organization.slug=sqlc.arg('org_slug') AND receipt.slug=sqlc.arg('receipt_slug') AND ds.proof_checks_enabled
        AND receipt.receipt->'operational' IS NULL
        AND LEAST(receipt.retain_until, receipt.created_at + interval '168 hours') > statement_timestamp()
)::boolean;

-- name: FindOrganizationGitLabOAuthGrant :one
SELECT c.*, g.id AS grant_id, g.provider, g.encrypted_token_set, g.access_token_expires_at, g.scopes, g.account, g.last_refreshed_at, g.created_at AS grant_created_at, g.updated_at AS grant_updated_at
FROM organization_gitlab_connections connection JOIN organizations o ON o.id = connection.organization_id JOIN credentials c ON c.organization_id = connection.organization_id AND c.id = connection.credential_id JOIN credential_oauth_grants g ON g.credential_id = c.id
WHERE o.slug = sqlc.arg('org_slug') AND c.kind = 'oauth2_grant';

-- name: FindOrganizationGitLabConnection :one
SELECT c.* FROM organization_gitlab_connections connection JOIN organizations o ON o.id = connection.organization_id JOIN credentials c ON c.organization_id = connection.organization_id AND c.id = connection.credential_id WHERE o.slug = sqlc.arg('org_slug');

-- name: CreateOrganizationGitLabOAuthInstall :one
INSERT INTO credential_oauth_installs (organization_id, organization_gitlab_connection_id, slug, provider, state_hash, intent, expected_credential_id, expected_credential_status, actor_type, actor_slug, requested_scopes, expires_at, repository_setup_slug, repository_setup_project_slug)
SELECT connection.organization_id, connection.id, sqlc.arg('install_slug'), sqlc.arg('provider'), sqlc.arg('state_hash'), sqlc.arg('intent'), c.id, c.status, sqlc.arg('actor_type'), sqlc.narg('actor_slug'), sqlc.arg('requested_scopes')::text[], sqlc.arg('expires_at'), sqlc.narg('repository_setup_slug'), sqlc.narg('repository_setup_project_slug')
FROM organization_gitlab_connections connection JOIN organizations o ON o.id=connection.organization_id JOIN credentials c ON c.organization_id=connection.organization_id AND c.id=connection.credential_id
WHERE o.slug=sqlc.arg('org_slug')
RETURNING *;

-- name: LockOrganizationGitLabOAuthGrantIntent :one
SELECT c.slug AS credential_slug, c.kind, c.status, EXISTS (SELECT 1 FROM credential_oauth_grants g WHERE g.credential_id = c.id) AS has_grant
FROM organization_gitlab_connections connection JOIN organizations o ON o.id = connection.organization_id JOIN credentials c ON c.organization_id = connection.organization_id AND c.id = connection.credential_id
WHERE o.slug = sqlc.arg('org_slug') FOR UPDATE OF connection, c;

-- name: CreateOrganizationGitLabOAuthGrant :one
WITH target AS (
 SELECT connection.id, connection.organization_id, connection.credential_id AS old_credential_id, c.kind AS old_kind, c.status AS old_status FROM organization_gitlab_connections connection JOIN organizations o ON o.id=connection.organization_id JOIN credentials c ON c.organization_id=connection.organization_id AND c.id=connection.credential_id WHERE o.slug=sqlc.arg('org_slug') FOR UPDATE OF connection,c
), inserted AS (
 INSERT INTO credentials (organization_id,slug,kind,status) SELECT organization_id,sqlc.arg('credential_slug'),'oauth2_grant',sqlc.arg('status') FROM target WHERE sqlc.arg('intent')='rotate_grant' RETURNING *
), selected AS (
 SELECT old_credential_id AS id FROM target WHERE sqlc.arg('intent')='create_grant' AND old_kind='oauth2_grant' AND old_status='pending' UNION ALL SELECT id FROM inserted
), attached AS (
 UPDATE organization_gitlab_connections connection SET credential_id=selected.id,updated_at=NOW() FROM target,selected WHERE connection.id=target.id RETURNING target.old_credential_id,selected.id AS credential_id,connection.organization_id,connection.id AS connection_id
), activated_pending AS (UPDATE credentials c SET status='active',failure_code=NULL,failure_message=NULL,updated_at=NOW() FROM target WHERE c.id=target.old_credential_id AND sqlc.arg('intent')='create_grant' RETURNING c.*), active AS (SELECT * FROM inserted UNION ALL SELECT * FROM activated_pending), sources AS (UPDATE data_sources ds SET credential_id=attached.credential_id,is_active=false,test_error='Test the repository connection after reconnecting GitLab',connection_test_revision=ds.connection_test_revision+1,updated_at=NOW() FROM attached WHERE ds.organization_id=attached.organization_id AND ds.organization_gitlab_connection_id=attached.connection_id RETURNING ds.id), retired AS (UPDATE credentials c SET status='rotated',encrypted_payload=NULL,rotated_at=NOW(),updated_at=NOW() FROM attached WHERE c.id=attached.old_credential_id AND c.id<>attached.credential_id RETURNING c.id), tombstone AS (UPDATE credential_oauth_grants g SET encrypted_token_set=decode('', 'hex'),updated_at=NOW() FROM retired WHERE g.credential_id=retired.id), oauth_grant AS (
 INSERT INTO credential_oauth_grants (credential_id,provider,encrypted_token_set,access_token_expires_at,scopes,account,last_refreshed_at) SELECT attached.credential_id,sqlc.arg('provider'),sqlc.arg('encrypted_token_set'),sqlc.arg('access_token_expires_at'),sqlc.arg('scopes')::text[],sqlc.arg('account')::jsonb,sqlc.narg('last_refreshed_at') FROM attached ON CONFLICT (credential_id) DO UPDATE SET provider=EXCLUDED.provider,encrypted_token_set=EXCLUDED.encrypted_token_set,access_token_expires_at=EXCLUDED.access_token_expires_at,scopes=EXCLUDED.scopes,account=EXCLUDED.account,last_refreshed_at=EXCLUDED.last_refreshed_at,updated_at=NOW() RETURNING *
) SELECT active.*,oauth_grant.id AS grant_id,oauth_grant.provider,oauth_grant.encrypted_token_set,oauth_grant.access_token_expires_at,oauth_grant.scopes,oauth_grant.account,oauth_grant.last_refreshed_at,oauth_grant.created_at AS grant_created_at,oauth_grant.updated_at AS grant_updated_at FROM active JOIN oauth_grant ON oauth_grant.credential_id=active.id;

-- name: CreateOrganizationGitLabConnection :one
WITH credential AS (
    INSERT INTO credentials (organization_id, slug, kind, status)
    SELECT id, sqlc.arg('credential_slug'), 'oauth2_grant', 'pending'
    FROM organizations WHERE slug = sqlc.arg('org_slug')
    RETURNING *
), connection AS (
    INSERT INTO organization_gitlab_connections (organization_id, credential_id)
    SELECT organization_id, id FROM credential
    RETURNING organization_id, credential_id
)
SELECT credential.* FROM credential JOIN connection ON connection.credential_id = credential.id;

-- name: AttachOrganizationGitLabConnection :one
WITH target AS (
    SELECT ds.id, ds.organization_id, ds.credential_id
    FROM data_sources ds
    JOIN organizations organization ON organization.id = ds.organization_id
    WHERE organization.slug = sqlc.arg('org_slug')
        AND ds.slug = sqlc.arg('data_source_slug')
        AND ds.type = 'gitlab' AND ds.deleted_at IS NULL
        AND ds.config #>> '{repository,id}' = sqlc.arg('expected_repository_id')::text
    FOR UPDATE OF ds
), attached AS (
    UPDATE data_sources ds
    SET credential_id = connection.credential_id,
        is_active = false,
        organization_gitlab_connection_id = connection.id,
        connection_test_revision = ds.connection_test_revision + 1,
        updated_at = NOW()
    FROM target
    JOIN organization_gitlab_connections connection ON connection.organization_id = target.organization_id
    JOIN credentials credential ON credential.organization_id = connection.organization_id AND credential.id = connection.credential_id
    WHERE ds.id = target.id
        AND credential.slug = sqlc.arg('expected_credential_slug') AND credential.status = 'active'
    RETURNING ds.id, ds.credential_id, target.credential_id AS old_credential_id
), retired AS (
    UPDATE credentials credential
    SET status = 'rotated', encrypted_payload = NULL, rotated_at = NOW(), updated_at = NOW()
    FROM attached
    WHERE credential.id = attached.old_credential_id
        AND credential.id <> attached.credential_id
        AND NOT EXISTS (SELECT 1 FROM data_sources other WHERE other.credential_id = credential.id AND other.id <> attached.id)
        AND NOT EXISTS (SELECT 1 FROM organization_gitlab_connections connection WHERE connection.credential_id = credential.id)
    RETURNING credential.id
), tombstone AS (
    UPDATE credential_oauth_grants oauth_grant
    SET encrypted_token_set = decode('', 'hex'), updated_at = NOW()
    FROM retired WHERE oauth_grant.credential_id = retired.id
)
SELECT id FROM attached;

-- name: ListProofReceiptMaintenanceOrganizations :many
SELECT DISTINCT organization.slug, receipt.organization_id
FROM organizations organization
JOIN proof_receipts receipt ON receipt.organization_id = organization.id
WHERE receipt.organization_id > sqlc.arg('after_organization_id')::integer
ORDER BY receipt.organization_id
LIMIT 100;

-- name: DeleteExpiredProofReceipts :many
WITH expired AS MATERIALIZED (
    SELECT receipt.id FROM proof_receipts receipt
    JOIN organizations organization ON organization.id = receipt.organization_id
    WHERE organization.slug = sqlc.arg('org_slug')
        AND LEAST(receipt.retain_until, receipt.created_at + interval '168 hours') <= statement_timestamp()
    ORDER BY receipt.created_at, receipt.id
    LIMIT 100
    FOR UPDATE OF receipt
), cleared AS (
    UPDATE proof_receipts receipt SET supersedes_receipt_id = NULL
    FROM organizations organization
    WHERE organization.id = receipt.organization_id AND organization.slug = sqlc.arg('org_slug')
        AND receipt.supersedes_receipt_id IN (SELECT id FROM expired)
        AND receipt.id NOT IN (SELECT id FROM expired)
    RETURNING receipt.id
)
DELETE FROM proof_receipts receipt USING organizations organization
WHERE organization.id = receipt.organization_id AND organization.slug = sqlc.arg('org_slug')
    AND receipt.id IN (SELECT id FROM expired)
    AND (SELECT count(*) FROM cleared) >= 0
RETURNING receipt.slug, receipt.receipt_key, receipt.evidence_id;

-- name: DeleteUnusedProofEvidence :exec
DELETE FROM proof_evidence evidence USING organizations organization
WHERE evidence.organization_id = organization.id AND organization.slug = sqlc.arg('org_slug')
    AND evidence.id = ANY(sqlc.arg('evidence_ids')::bigint[])
    AND NOT EXISTS (SELECT 1 FROM proof_receipts receipt WHERE receipt.organization_id = evidence.organization_id AND receipt.evidence_id = evidence.id);

-- =====================
-- ODCS contract catalog
-- =====================

-- name: CreateODCSContract :one
INSERT INTO odcs_contracts (organization_id, project_id, slug, name, odcs_id)
SELECT p.organization_id, p.id, sqlc.arg('contract_slug'), sqlc.arg('name'), sqlc.arg('odcs_id')
FROM projects p JOIN organizations o ON o.id = p.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
RETURNING *;

-- name: FindODCSContract :one
SELECT c.* FROM odcs_contracts c
JOIN organizations o ON o.id = c.organization_id
JOIN projects p ON p.id = c.project_id AND p.organization_id = c.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND c.slug = sqlc.arg('contract_slug');

-- name: LockODCSContract :one
SELECT c.* FROM odcs_contracts c
JOIN organizations o ON o.id = c.organization_id
JOIN projects p ON p.id = c.project_id AND p.organization_id = c.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND c.slug = sqlc.arg('contract_slug') FOR UPDATE OF c;

-- name: UpdateODCSContract :one
UPDATE odcs_contracts c SET name = sqlc.arg('name'), status = sqlc.arg('status'), updated_at = NOW()
FROM projects p JOIN organizations o ON o.id = p.organization_id
WHERE c.project_id = p.id AND c.organization_id = p.organization_id
AND o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND c.slug = sqlc.arg('contract_slug') RETURNING c.*;

-- name: CreateODCSRevision :one
INSERT INTO odcs_contract_revisions (organization_id, project_id, contract_id, slug, digest,
    odcs_version, document_version, declared_status, document, schema_objects)
SELECT c.organization_id, c.project_id, c.id, sqlc.arg('revision_slug'), sqlc.arg('digest'),
    sqlc.arg('odcs_version'), sqlc.arg('document_version'), sqlc.arg('declared_status'),
    sqlc.arg('document'), sqlc.arg('schema_objects')
FROM odcs_contracts c JOIN organizations o ON o.id = c.organization_id
JOIN projects p ON p.id = c.project_id AND p.organization_id = c.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND c.slug = sqlc.arg('contract_slug') AND c.status = 'active' RETURNING *;

-- name: FindODCSRevision :one
SELECT r.* FROM odcs_contract_revisions r
JOIN odcs_contracts c ON c.id = r.contract_id AND c.project_id = r.project_id AND c.organization_id = r.organization_id
JOIN projects p ON p.id = c.project_id AND p.organization_id = c.organization_id
JOIN organizations o ON o.id = c.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND c.slug = sqlc.arg('contract_slug') AND r.slug = sqlc.arg('revision_slug');

-- name: ListODCSContracts :many
SELECT c.* FROM odcs_contracts c
JOIN projects p ON p.id = c.project_id AND p.organization_id = c.organization_id
JOIN organizations o ON o.id = c.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND (sqlc.narg('cursor_slug')::text IS NULL OR
    (sqlc.arg('order_dir')::text = 'asc' AND (c.created_at, c.slug) > (sqlc.narg('cursor_timestamp')::timestamptz, sqlc.narg('cursor_slug')::text)) OR
    (sqlc.arg('order_dir')::text = 'desc' AND (c.created_at, c.slug) < (sqlc.narg('cursor_timestamp')::timestamptz, sqlc.narg('cursor_slug')::text)))
ORDER BY
    CASE WHEN sqlc.arg('order_dir')::text = 'asc' THEN c.created_at END ASC,
    CASE WHEN sqlc.arg('order_dir')::text = 'desc' THEN c.created_at END DESC,
    CASE WHEN sqlc.arg('order_dir')::text = 'asc' THEN c.slug END ASC,
    CASE WHEN sqlc.arg('order_dir')::text = 'desc' THEN c.slug END DESC
LIMIT sqlc.arg('limit_count');

-- name: ListODCSRevisions :many
SELECT r.id, r.organization_id, r.project_id, r.contract_id, r.slug, r.digest, r.odcs_version, r.document_version, r.declared_status, r.status, r.schema_objects, r.created_at FROM odcs_contract_revisions r
JOIN odcs_contracts c ON c.id = r.contract_id AND c.organization_id = r.organization_id AND c.project_id = r.project_id
JOIN projects p ON p.id = c.project_id AND p.organization_id = c.organization_id
JOIN organizations o ON o.id = c.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND c.slug = sqlc.arg('contract_slug')
AND (sqlc.narg('cursor_slug')::text IS NULL OR
    (sqlc.arg('order_dir')::text = 'asc' AND (r.created_at, r.slug) > (sqlc.narg('cursor_timestamp')::timestamptz, sqlc.narg('cursor_slug')::text)) OR
    (sqlc.arg('order_dir')::text = 'desc' AND (r.created_at, r.slug) < (sqlc.narg('cursor_timestamp')::timestamptz, sqlc.narg('cursor_slug')::text)))
ORDER BY
    CASE WHEN sqlc.arg('order_dir')::text = 'asc' THEN r.created_at END ASC,
    CASE WHEN sqlc.arg('order_dir')::text = 'desc' THEN r.created_at END DESC,
    CASE WHEN sqlc.arg('order_dir')::text = 'asc' THEN r.slug END ASC,
    CASE WHEN sqlc.arg('order_dir')::text = 'desc' THEN r.slug END DESC
LIMIT sqlc.arg('limit_count');


-- name: ListODCSConsumerSnapshots :many
WITH scope AS (
    SELECT p.id, p.organization_id FROM projects p JOIN organizations o ON o.id = p.organization_id
    WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
), snapshots AS (
    SELECT 'deployed'::text AS origin, e.slug::text AS environment_slug,
           v.spec_hash, v.spec_jsonb, v.resolved_contracts
    FROM scope p
    JOIN pipeline_specs ps ON ps.project_id = p.id AND ps.deleted_at IS NULL
    JOIN project_environments pe ON pe.project_id = p.id AND pe.deleted_at IS NULL
    JOIN environments e ON e.id = pe.environment_id AND e.organization_id = p.organization_id AND e.deleted_at IS NULL
    JOIN pipeline_deployments d ON d.project_environment_id = pe.id AND d.pipeline_slug = ps.slug
    JOIN pipeline_versions v ON v.id = d.current_version_id AND v.project_id = p.id AND v.pipeline_slug = ps.slug
    UNION ALL
    SELECT 'candidate'::text, ''::text, ps.spec_hash, ps.spec_jsonb, '[]'::jsonb
    FROM scope p JOIN pipeline_specs ps ON ps.project_id = p.id AND ps.deleted_at IS NULL
    UNION ALL
    SELECT DISTINCT 'draft'::text, ''::text, ''::text, d.spec_jsonb, '[]'::jsonb
    FROM scope p JOIN pipeline_specs ps ON ps.project_id = p.id AND ps.deleted_at IS NULL
    JOIN pipeline_drafts d ON d.project_id = p.id AND d.pipeline_slug = ps.slug
)
SELECT origin, environment_slug, spec_hash, spec_jsonb, resolved_contracts
FROM snapshots ORDER BY origin, environment_slug, spec_hash, spec_jsonb::text;

-- name: FindODCSContractByIdentity :one
SELECT c.* FROM odcs_contracts c
JOIN organizations o ON o.id = c.organization_id
JOIN projects p ON p.id = c.project_id AND p.organization_id = c.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND c.odcs_id = sqlc.arg('odcs_id');

-- name: FindODCSRevisionByDigest :one
SELECT r.* FROM odcs_contract_revisions r
JOIN odcs_contracts c ON c.id = r.contract_id AND c.organization_id = r.organization_id AND c.project_id = r.project_id
JOIN projects p ON p.id = c.project_id AND p.organization_id = c.organization_id
JOIN organizations o ON o.id = c.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND c.slug = sqlc.arg('contract_slug') AND r.digest = sqlc.arg('digest');

-- name: CreateODCSArtifact :execrows
INSERT INTO odcs_artifacts (organization_id, project_id, contract_id, revision_slug,
    source_repo, source_commit, source_path, format, artifact_digest, original)
SELECT c.organization_id, c.project_id, c.id, sqlc.arg('revision_slug'),
    sqlc.arg('source_repo'), sqlc.arg('source_commit'), sqlc.arg('source_path'),
    sqlc.arg('format'), sqlc.arg('artifact_digest'), sqlc.arg('original')
FROM odcs_contracts c JOIN organizations o ON o.id = c.organization_id
JOIN projects p ON p.id = c.project_id AND p.organization_id = c.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug') AND c.slug = sqlc.arg('contract_slug')
ON CONFLICT (project_id, source_repo, source_commit, source_path) DO NOTHING;

-- name: FindODCSArtifact :one
SELECT a.*, c.slug AS contract_slug FROM odcs_artifacts a
JOIN odcs_contracts c ON c.id = a.contract_id AND c.organization_id = a.organization_id AND c.project_id = a.project_id
JOIN projects p ON p.id = a.project_id AND p.organization_id = a.organization_id
JOIN organizations o ON o.id = a.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
AND a.source_repo = sqlc.arg('source_repo') AND a.source_commit = sqlc.arg('source_commit')
AND a.source_path = sqlc.arg('source_path');

-- name: SaveHTTPOAuthApp :one
WITH target AS (
    UPDATE data_sources ds
    SET connection_test_revision = connection_test_revision + 1, updated_at = NOW()
    FROM organizations o
    WHERE o.id = ds.organization_id
        AND o.slug = sqlc.arg('org_slug')
        AND ds.slug = sqlc.arg('data_source_slug')
        AND ds.type = 'http'
        AND ds.deleted_at IS NULL
    RETURNING ds.organization_id, ds.id
)
INSERT INTO credential_http_oauth_apps (organization_id, data_source_id, encrypted_config)
SELECT organization_id, id, sqlc.arg('encrypted_config') FROM target
ON CONFLICT (organization_id, data_source_id)
DO UPDATE SET encrypted_config = EXCLUDED.encrypted_config, updated_at = NOW()
RETURNING encrypted_config;

-- name: FindHTTPOAuthApp :one
SELECT app.encrypted_config
FROM credential_http_oauth_apps app
JOIN organizations o ON o.id = app.organization_id
JOIN data_sources ds ON ds.organization_id = app.organization_id AND ds.id = app.data_source_id
WHERE o.slug = sqlc.arg('org_slug')
    AND ds.slug = sqlc.arg('data_source_slug')
    AND ds.type = 'http'
    AND ds.deleted_at IS NULL;

-- name: ClaimHTTPOAuthRefresh :one
WITH claimed AS (
    UPDATE credential_http_oauth_apps app
    SET refresh_claim_id = sqlc.arg('claim_id')::text,
        refresh_claim_expires_at = NOW() + INTERVAL '1 minute'
    FROM data_sources ds
    JOIN organizations o ON o.id = ds.organization_id
    JOIN credentials c ON c.organization_id = ds.organization_id AND c.id = ds.credential_id
    JOIN credential_oauth_grants g ON g.credential_id = c.id
    WHERE app.organization_id = ds.organization_id AND app.data_source_id = ds.id
        AND o.slug = sqlc.arg('org_slug') AND ds.slug = sqlc.arg('data_source_slug')
        AND c.slug = sqlc.arg('credential_slug') AND c.status = 'active'
        AND g.provider = 'http' AND g.updated_at = sqlc.arg('grant_updated_at')
        AND ds.deleted_at IS NULL
        AND (app.refresh_claim_id IS NULL OR app.refresh_claim_expires_at <= NOW())
    RETURNING app.data_source_id
)
SELECT EXISTS (SELECT 1 FROM claimed);

-- name: ReleaseHTTPOAuthRefresh :exec
UPDATE credential_http_oauth_apps app
SET refresh_claim_id = NULL, refresh_claim_expires_at = NULL
FROM data_sources ds JOIN organizations o ON o.id = ds.organization_id
WHERE app.organization_id = ds.organization_id AND app.data_source_id = ds.id
    AND o.slug = sqlc.arg('org_slug') AND ds.slug = sqlc.arg('data_source_slug')
    AND app.refresh_claim_id = sqlc.arg('claim_id')::text;

-- name: CreatePipelineCompiledPlan :one
INSERT INTO pipeline_compiled_plans (
    project_id, pipeline_slug, digest, authored_spec_hash, format_version, materialization
)
SELECT
    p.id, sqlc.arg('pipeline_slug'), sqlc.arg('digest'), sqlc.arg('authored_spec_hash'),
    sqlc.arg('format_version'), sqlc.arg('materialization')
FROM projects p
JOIN organizations o ON o.id = p.organization_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
ON CONFLICT (project_id, pipeline_slug, digest) DO NOTHING
RETURNING pipeline_slug, digest, authored_spec_hash, format_version, materialization, created_at;

-- name: FindPipelineCompiledPlan :one
SELECT
    plan.pipeline_slug, plan.digest, plan.authored_spec_hash, plan.format_version,
    plan.materialization, plan.created_at
FROM pipeline_compiled_plans plan
JOIN projects p ON p.id = plan.project_id
JOIN organizations o ON o.id = p.organization_id
WHERE o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND plan.pipeline_slug = sqlc.arg('pipeline_slug')
    AND plan.digest = sqlc.arg('digest');

-- name: CreatePipelineDeploymentResolution :one
INSERT INTO pipeline_deployment_resolutions (
    project_id, project_environment_id, pipeline_slug, compiled_digest, digest, materialization
)
SELECT p.id, pe.id, cp.pipeline_slug, cp.digest, sqlc.arg('digest'), sqlc.arg('materialization')
FROM projects p
JOIN organizations o ON o.id = p.organization_id
JOIN project_environments pe ON pe.project_id = p.id AND pe.deleted_at IS NULL
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id AND e.deleted_at IS NULL
JOIN pipeline_compiled_plans cp ON cp.project_id = p.id AND cp.pipeline_slug = sqlc.arg('pipeline_slug')
    AND cp.digest = sqlc.arg('compiled_digest')
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug') AND e.slug = sqlc.arg('environment_slug')
ON CONFLICT (project_environment_id, pipeline_slug, digest) DO NOTHING
RETURNING digest, materialization;

-- name: FindPipelineDeploymentResolution :one
SELECT r.digest, r.materialization
FROM pipeline_deployment_resolutions r
JOIN projects p ON p.id = r.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN project_environments pe ON pe.id = r.project_environment_id AND pe.project_id = p.id AND pe.deleted_at IS NULL
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id AND e.deleted_at IS NULL
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug') AND e.slug = sqlc.arg('environment_slug')
    AND r.pipeline_slug = sqlc.arg('pipeline_slug') AND r.digest = sqlc.arg('digest');

-- name: CheckConnectorDispatch :one
SELECT check_connector_dispatch(
    sqlc.arg('request_id'), sqlc.arg('expected_claim_token')::uuid, sqlc.arg('organization_slug')::text,
    sqlc.arg('data_source_slug')::text, sqlc.arg('data_source_type')::text,
    sqlc.narg('expected_config')::jsonb, sqlc.narg('credential_slug')::text,
    sqlc.narg('credential_updated_at')::timestamptz, sqlc.arg('read_only')::boolean
)::boolean AS allowed;

-- name: FindProofOperationalRun :one
SELECT pr.pipeline_slug, pr.run_slug, pr.source_attempt_number, pr.status,
    pr.timeline, pr.ended_at, pv.spec_hash, pv.git_commit_sha,
    resolution.digest AS resolution_digest, resolution.materialization,
    o.settings
FROM pipeline_runs pr
JOIN project_environments pe ON pe.id = pr.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.project_id = p.id AND ps.slug = pr.pipeline_slug AND ps.deleted_at IS NULL
JOIN pipeline_versions pv ON pv.id = pr.pipeline_version_id
    AND pv.project_id = p.id AND pv.pipeline_slug = pr.pipeline_slug
JOIN pipeline_deployment_resolutions resolution
    ON resolution.project_id = p.id AND resolution.project_environment_id = pe.id
    AND resolution.pipeline_slug = pr.pipeline_slug
    AND resolution.digest = pr.deployment_resolution_digest
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND pr.run_slug = sqlc.arg('run_slug');

-- name: FindNotificationConnectionOAuthGrant :one
SELECT c.*, g.id AS grant_id, g.provider, g.encrypted_token_set, g.access_token_expires_at, g.scopes, g.account, g.last_refreshed_at, g.created_at AS grant_created_at, g.updated_at AS grant_updated_at
FROM notification_connections connection JOIN organizations o ON o.id = connection.organization_id JOIN credentials c ON c.organization_id = connection.organization_id AND c.id = connection.credential_id JOIN credential_oauth_grants g ON g.credential_id = c.id
WHERE o.slug = sqlc.arg('org_slug') AND connection.slug = sqlc.arg('notification_connection_slug') AND c.kind = 'oauth2_grant';

-- name: FindNotificationConnectionCredential :one
SELECT c.*, connection.provider FROM notification_connections connection JOIN organizations o ON o.id = connection.organization_id JOIN credentials c ON c.organization_id = connection.organization_id AND c.id = connection.credential_id WHERE o.slug = sqlc.arg('org_slug') AND connection.slug = sqlc.arg('notification_connection_slug');

-- name: CreateNotificationConnectionOAuthInstall :one
INSERT INTO credential_oauth_installs (organization_id, notification_connection_id, slug, provider, state_hash, intent, expected_credential_id, expected_credential_status, actor_type, actor_slug, requested_scopes, expires_at)
SELECT connection.organization_id, connection.id, sqlc.arg('install_slug'), sqlc.arg('provider'), sqlc.arg('state_hash'), sqlc.arg('intent'), c.id, c.status, sqlc.arg('actor_type'), sqlc.narg('actor_slug'), sqlc.arg('requested_scopes')::text[], sqlc.arg('expires_at')
FROM notification_connections connection JOIN organizations o ON o.id=connection.organization_id JOIN credentials c ON c.organization_id=connection.organization_id AND c.id=connection.credential_id
WHERE o.slug=sqlc.arg('org_slug') AND connection.slug=sqlc.arg('notification_connection_slug') AND connection.provider=sqlc.arg('provider')
RETURNING *;

-- name: LockNotificationConnectionOAuthGrantIntent :one
SELECT c.slug AS credential_slug, c.kind, c.status, EXISTS (SELECT 1 FROM credential_oauth_grants g WHERE g.credential_id = c.id) AS has_grant
FROM notification_connections connection JOIN organizations o ON o.id = connection.organization_id JOIN credentials c ON c.organization_id = connection.organization_id AND c.id = connection.credential_id
WHERE o.slug = sqlc.arg('org_slug') AND connection.slug = sqlc.arg('notification_connection_slug') FOR UPDATE OF connection, c;

-- name: CreateNotificationConnectionOAuthGrant :one
WITH target AS (
 SELECT connection.id, connection.organization_id, connection.credential_id AS old_credential_id, c.kind AS old_kind, c.status AS old_status FROM notification_connections connection JOIN organizations o ON o.id=connection.organization_id JOIN credentials c ON c.organization_id=connection.organization_id AND c.id=connection.credential_id WHERE o.slug=sqlc.arg('org_slug') AND connection.slug=sqlc.arg('notification_connection_slug') FOR UPDATE OF connection,c
), inserted AS (
 INSERT INTO credentials (organization_id,slug,kind,status) SELECT organization_id,sqlc.arg('credential_slug'),'oauth2_grant',sqlc.arg('status') FROM target WHERE sqlc.arg('intent')='rotate_grant' RETURNING *
), selected AS (
 SELECT old_credential_id AS id FROM target WHERE sqlc.arg('intent')='create_grant' AND old_kind='oauth2_grant' AND old_status='pending' UNION ALL SELECT id FROM inserted
), attached AS (
 UPDATE notification_connections connection SET credential_id=selected.id,updated_at=NOW() FROM target,selected WHERE connection.id=target.id RETURNING target.old_credential_id,selected.id AS credential_id,connection.organization_id,connection.id AS connection_id
), activated_pending AS (UPDATE credentials c SET status='active',failure_code=NULL,failure_message=NULL,updated_at=NOW() FROM target WHERE c.id=target.old_credential_id AND sqlc.arg('intent')='create_grant' RETURNING c.*), active AS (SELECT * FROM inserted UNION ALL SELECT * FROM activated_pending), retired AS (UPDATE credentials c SET status='rotated',encrypted_payload=NULL,rotated_at=NOW(),updated_at=NOW() FROM attached WHERE c.id=attached.old_credential_id AND c.id<>attached.credential_id RETURNING c.id), tombstone AS (UPDATE credential_oauth_grants g SET encrypted_token_set=decode('', 'hex'),updated_at=NOW() FROM retired WHERE g.credential_id=retired.id), oauth_grant AS (
 INSERT INTO credential_oauth_grants (credential_id,provider,encrypted_token_set,access_token_expires_at,scopes,account,last_refreshed_at) SELECT attached.credential_id,sqlc.arg('provider'),sqlc.arg('encrypted_token_set'),sqlc.arg('access_token_expires_at'),sqlc.arg('scopes')::text[],sqlc.arg('account')::jsonb,sqlc.narg('last_refreshed_at') FROM attached ON CONFLICT (credential_id) DO UPDATE SET provider=EXCLUDED.provider,encrypted_token_set=EXCLUDED.encrypted_token_set,access_token_expires_at=EXCLUDED.access_token_expires_at,scopes=EXCLUDED.scopes,account=EXCLUDED.account,last_refreshed_at=EXCLUDED.last_refreshed_at,updated_at=NOW() RETURNING *
) SELECT active.*,oauth_grant.id AS grant_id,oauth_grant.provider,oauth_grant.encrypted_token_set,oauth_grant.access_token_expires_at,oauth_grant.scopes,oauth_grant.account,oauth_grant.last_refreshed_at,oauth_grant.created_at AS grant_created_at,oauth_grant.updated_at AS grant_updated_at FROM active JOIN oauth_grant ON oauth_grant.credential_id=active.id;

-- name: CreateNotificationConnection :one
WITH credential AS (
    INSERT INTO credentials (organization_id, slug, kind, status)
    SELECT id, sqlc.arg('credential_slug'), 'oauth2_grant', 'pending'
    FROM organizations WHERE slug = sqlc.arg('org_slug')
    RETURNING *
), connection AS (
    INSERT INTO notification_connections (organization_id, credential_id, slug, name, provider)
    SELECT organization_id, id, sqlc.arg('notification_connection_slug'), sqlc.arg('name'), sqlc.arg('provider') FROM credential
    RETURNING organization_id, credential_id
)
SELECT credential.* FROM credential JOIN connection ON connection.credential_id = credential.id;


-- name: ListNotificationConnections :many
SELECT connection.slug, connection.name, connection.provider, c.status, c.updated_at,
       COALESCE(g.account, '{}'::jsonb)::jsonb AS account
FROM notification_connections connection
JOIN organizations o ON o.id=connection.organization_id
JOIN credentials c ON c.organization_id=connection.organization_id AND c.id=connection.credential_id
LEFT JOIN credential_oauth_grants g ON g.credential_id=c.id
WHERE o.slug=sqlc.arg('org_slug')
ORDER BY connection.created_at, connection.slug;

-- name: ClaimConnectionDelivery :one
WITH claimed AS (
    INSERT INTO alert_delivery_attempts (
        organization_id, slug, notification_record_id, alert_policy_id, destination_id,
        destination_type, status, provider_attempt, job_kind, job_queue, job_state,
        queued_at, delivery_lease_until
    )
    SELECT o.id, sqlc.arg('attempt_slug'), nr.id, ap.id, ad.id,
        ad.type, 'queued', 1, 'notification_delivery', 'notification_delivery', 'running',
        sqlc.arg('now')::timestamptz, sqlc.arg('lease_until')::timestamptz
    FROM organizations o
    JOIN notification_records nr ON nr.organization_id=o.id AND nr.slug=sqlc.arg('record_slug')
    JOIN alert_destinations ad ON ad.organization_id=o.id AND ad.slug=sqlc.arg('destination_slug')
    JOIN alert_policies ap ON ap.organization_id=o.id AND ap.slug=sqlc.arg('alert_policy_slug')
    JOIN alert_policy_destinations binding ON binding.organization_id=o.id AND binding.destination_id=ad.id AND binding.alert_policy_id=ap.id
    WHERE o.slug=sqlc.arg('org_slug') AND ad.type='slack' AND ad.enabled AND ap.enabled
    ON CONFLICT (organization_id, notification_record_id, destination_id) WHERE destination_type='slack'
    DO UPDATE SET
        status='queued', provider_attempt=alert_delivery_attempts.provider_attempt+1,
        delivery_lease_until=EXCLUDED.delivery_lease_until, job_state='running', updated_at=NOW()
    WHERE alert_delivery_attempts.status<>'sent' AND alert_delivery_attempts.job_state<>'cancelled'
        AND (alert_delivery_attempts.delivery_lease_until IS NULL OR alert_delivery_attempts.delivery_lease_until <= sqlc.arg('now')::timestamptz)
        AND (alert_delivery_attempts.retry_after_at IS NULL OR alert_delivery_attempts.retry_after_at <= sqlc.arg('now')::timestamptz)
    RETURNING slug, provider_attempt, status, job_state, delivery_lease_until, retry_after_at
)
SELECT claimed.slug, claimed.provider_attempt, claimed.status, claimed.job_state, claimed.delivery_lease_until, claimed.retry_after_at, true AS acquired
FROM claimed
UNION ALL
SELECT attempt.slug, attempt.provider_attempt, attempt.status, attempt.job_state, attempt.delivery_lease_until, attempt.retry_after_at, false AS acquired
FROM alert_delivery_attempts attempt
JOIN organizations o ON o.id=attempt.organization_id
JOIN notification_records nr ON nr.organization_id=o.id AND nr.id=attempt.notification_record_id
JOIN alert_destinations ad ON ad.organization_id=o.id AND ad.id=attempt.destination_id
WHERE o.slug=sqlc.arg('org_slug') AND nr.slug=sqlc.arg('record_slug') AND ad.slug=sqlc.arg('destination_slug')
    AND attempt.destination_type='slack' AND NOT EXISTS (SELECT 1 FROM claimed)
LIMIT 1;

-- name: FinishConnectionDelivery :execrows
UPDATE alert_delivery_attempts attempt
SET status=sqlc.arg('status')::text, job_state=sqlc.arg('job_state')::text,
    delivery_lease_until=NULL, retry_after_at=sqlc.narg('retry_after_at')::timestamptz,
    error_category=sqlc.narg('error_category')::text, error_message=sqlc.narg('error_message')::text,
    provider_response_metadata=sqlc.arg('provider_response_metadata')::jsonb,
    sent_at=CASE WHEN sqlc.arg('status')::text='sent' THEN sqlc.arg('now')::timestamptz ELSE NULL END,
    failed_at=CASE WHEN sqlc.arg('status')::text='failed' THEN sqlc.arg('now')::timestamptz ELSE NULL END,
    updated_at=NOW()
FROM organizations o
WHERE o.id=attempt.organization_id AND o.slug=sqlc.arg('org_slug')
    AND attempt.slug=sqlc.arg('attempt_slug') AND attempt.destination_type='slack'
    AND attempt.status='queued' AND attempt.provider_attempt=sqlc.arg('provider_attempt')::integer;

-- name: ListDelayedScheduleFiresForSystem :many
SELECT o.slug AS org_slug, p.slug AS project_slug, e.slug AS environment_slug,
       pt.slug AS trigger_slug, psf.trigger_revision, psf.pipeline_slug,
       psf.scheduled_fire_at, psf.actual_fire_at
FROM pipeline_schedule_fires psf
JOIN pipeline_triggers pt ON pt.id = psf.pipeline_trigger_id
    AND pt.project_environment_id = psf.project_environment_id
JOIN project_environments pe ON pe.id = psf.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
JOIN pipeline_specs ps ON ps.id = pt.pipeline_id AND ps.project_id = p.id
WHERE psf.status IN ('claimed', 'running')
  AND psf.scheduled_fire_at < sqlc.arg('before_at')::timestamptz
  AND pe.deleted_at IS NULL AND e.deleted_at IS NULL AND ps.deleted_at IS NULL
ORDER BY psf.scheduled_fire_at, psf.id;

-- name: RecordRunRecencySuccess :exec
INSERT INTO pipeline_run_recency_successes AS evidence (
    project_environment_id, pipeline_slug, deployment_id, deployment_revision,
    pipeline_version_id, deployment_resolution_digest, run_slug, succeeded_at
)
SELECT r.project_environment_id, r.pipeline_slug, r.deployment_id, r.deployment_revision,
    r.pipeline_version_id, r.deployment_resolution_digest, r.run_slug, r.ended_at
FROM pipeline_runs r
JOIN project_environments pe ON pe.id = r.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND r.run_slug = sqlc.arg('run_slug')
    AND r.status = 'succeeded' AND r.ended_at IS NOT NULL
    AND r.deployment_id IS NOT NULL AND r.deployment_revision IS NOT NULL
ON CONFLICT (project_environment_id, pipeline_slug, deployment_id, deployment_revision)
DO UPDATE SET run_slug = EXCLUDED.run_slug, succeeded_at = EXCLUDED.succeeded_at
WHERE (EXCLUDED.succeeded_at, EXCLUDED.run_slug) > (evidence.succeeded_at, evidence.run_slug);

-- name: CreateRunRecencyExpectation :one
INSERT INTO pipeline_run_recency_expectations (
    project_environment_id, pipeline_slug, revision, maximum_age_seconds, enabled, enabled_at
)
SELECT pe.id, d.pipeline_slug, sqlc.arg('revision'), sqlc.arg('maximum_age_seconds'),
    sqlc.arg('enabled'), sqlc.arg('enabled_at')
FROM pipeline_deployments d
JOIN project_environments pe ON pe.id = d.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND d.pipeline_slug = sqlc.arg('pipeline_slug')
    AND pe.deleted_at IS NULL AND e.deleted_at IS NULL
RETURNING *;

-- name: LockRunRecencyScope :exec
SELECT pg_advisory_xact_lock(hashtextextended(
    sqlc.arg('org_slug')::text || '/' || sqlc.arg('project_slug')::text || '/' ||
    sqlc.arg('environment_slug')::text || '/' || sqlc.arg('pipeline_slug')::text, 0));

-- name: FindLatestRunRecencyExpectation :one
SELECT expectation.*
FROM pipeline_run_recency_expectations expectation
JOIN project_environments pe ON pe.id = expectation.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND expectation.pipeline_slug = sqlc.arg('pipeline_slug')
ORDER BY expectation.revision DESC LIMIT 1;

-- name: ListRunRecencyExpectationsForSystem :many
-- Worker-only cross-organization enumeration. Every mutation below retains the
-- environment identity selected here; this is not exposed to tenant callers.
SELECT expectation.*, o.slug AS org_slug, p.slug AS project_slug, e.slug AS environment_slug,
    COALESCE(d.id, 0)::bigint AS deployment_id, COALESCE(d.revision, 0)::bigint AS deployment_revision, COALESCE(d.current_version_id, 0)::bigint AS current_version_id,
    d.current_resolution_digest, d.deployed_at,
    evidence.run_slug AS success_run_slug, evidence.succeeded_at,
    (pe.deleted_at IS NOT NULL OR e.deleted_at IS NOT NULL) AS scope_disabled
FROM pipeline_run_recency_expectations expectation
JOIN project_environments pe ON pe.id = expectation.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
LEFT JOIN pipeline_deployments d ON d.project_environment_id = pe.id AND d.pipeline_slug = expectation.pipeline_slug
LEFT JOIN pipeline_run_recency_successes evidence
    ON evidence.project_environment_id = pe.id AND evidence.pipeline_slug = expectation.pipeline_slug
    AND evidence.deployment_id = d.id AND evidence.deployment_revision = d.revision
    AND evidence.pipeline_version_id = d.current_version_id
    AND evidence.deployment_resolution_digest IS NOT DISTINCT FROM d.current_resolution_digest
WHERE NOT EXISTS (SELECT 1 FROM pipeline_run_recency_expectations newer
    WHERE newer.project_environment_id = expectation.project_environment_id
        AND newer.pipeline_slug = expectation.pipeline_slug AND newer.revision > expectation.revision)
ORDER BY expectation.id;

-- name: FindCurrentRunRecencyExpectationForSystem :one
SELECT expectation.*, o.slug AS org_slug, p.slug AS project_slug, e.slug AS environment_slug,
    COALESCE(d.id, 0)::bigint AS deployment_id, COALESCE(d.revision, 0)::bigint AS deployment_revision, COALESCE(d.current_version_id, 0)::bigint AS current_version_id,
    d.current_resolution_digest, d.deployed_at,
    evidence.run_slug AS success_run_slug, evidence.succeeded_at,
    (pe.deleted_at IS NOT NULL OR e.deleted_at IS NOT NULL) AS scope_disabled
FROM pipeline_run_recency_expectations expectation
JOIN project_environments pe ON pe.id = expectation.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
LEFT JOIN pipeline_deployments d ON d.project_environment_id = pe.id AND d.pipeline_slug = expectation.pipeline_slug
LEFT JOIN pipeline_run_recency_successes evidence
    ON evidence.project_environment_id = pe.id AND evidence.pipeline_slug = expectation.pipeline_slug
    AND evidence.deployment_id = d.id AND evidence.deployment_revision = d.revision
    AND evidence.pipeline_version_id = d.current_version_id
    AND evidence.deployment_resolution_digest IS NOT DISTINCT FROM d.current_resolution_digest
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND expectation.pipeline_slug = sqlc.arg('pipeline_slug')
    AND NOT EXISTS (SELECT 1 FROM pipeline_run_recency_expectations newer
    WHERE newer.project_environment_id = expectation.project_environment_id
        AND newer.pipeline_slug = expectation.pipeline_slug AND newer.revision > expectation.revision)
ORDER BY expectation.id
LIMIT 1 FOR UPDATE OF expectation;

-- name: ListActiveRunRecencyEpisodes :many
SELECT episode.* FROM pipeline_run_recency_episodes episode
JOIN pipeline_run_recency_expectations expectation ON expectation.id = episode.expectation_id
WHERE expectation.project_environment_id = sqlc.arg('project_environment_id')
    AND expectation.pipeline_slug = sqlc.arg('pipeline_slug') AND episode.resolved_at IS NULL
ORDER BY episode.id FOR UPDATE OF episode;

-- name: CreateRunRecencyEpisode :one
INSERT INTO pipeline_run_recency_episodes (
    slug, expectation_id, deployment_id, deployment_revision, pipeline_version_id,
    deployment_resolution_digest, deployed_at, deadline_at, breached_at, last_success_run_slug, last_success_at
) VALUES (
    sqlc.arg('slug'), sqlc.arg('expectation_id'), sqlc.arg('deployment_id'), sqlc.arg('deployment_revision'),
    sqlc.arg('pipeline_version_id'), sqlc.narg('deployment_resolution_digest'), sqlc.arg('deployed_at'),
    sqlc.arg('deadline_at'), sqlc.arg('breached_at'), sqlc.narg('last_success_run_slug'), sqlc.narg('last_success_at')
) RETURNING *;

-- name: ResolveRunRecencyEpisode :exec
UPDATE pipeline_run_recency_episodes episode SET resolved_at = sqlc.arg('resolved_at'),
    resolution_reason = sqlc.arg('resolution_reason'), recovery_run_slug = sqlc.narg('recovery_run_slug'),
    recovery_succeeded_at = sqlc.narg('recovery_succeeded_at')
FROM pipeline_run_recency_expectations expectation
WHERE episode.expectation_id = expectation.id
    AND expectation.project_environment_id = sqlc.arg('project_environment_id')
    AND expectation.pipeline_slug = sqlc.arg('pipeline_slug')
    AND episode.slug = sqlc.arg('slug') AND episode.resolved_at IS NULL;

-- name: CreateProjectRepositorySetup :execrows
INSERT INTO project_repository_setups (organization_id, project_id, slug, request)
SELECT p.organization_id, p.id, sqlc.arg('setup_slug'), sqlc.arg('request')
FROM projects p
JOIN organizations o ON o.id = p.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
ON CONFLICT (project_id) WHERE abandoned_at IS NULL DO NOTHING;

-- name: GetProjectRepositorySetup :one
SELECT s.slug, s.request, s.result, s.status, s.failure_code, s.abandoned_at
FROM project_repository_setups s
JOIN projects p ON p.id = s.project_id AND p.organization_id = s.organization_id
JOIN organizations o ON o.id = s.organization_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug') AND s.abandoned_at IS NULL;

-- name: ClaimProjectRepositorySetup :one
UPDATE project_repository_setups s
SET lease_owner = sqlc.arg('lease_owner'), lease_until = clock_timestamp() + INTERVAL '3 minutes'
FROM projects p, organizations o
WHERE p.id = s.project_id AND p.organization_id = s.organization_id
    AND o.id = s.organization_id AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND s.slug = sqlc.arg('setup_slug') AND s.abandoned_at IS NULL
    AND (s.lease_until IS NULL OR s.lease_until <= clock_timestamp())
RETURNING s.slug, s.request, s.result, s.status, s.failure_code, s.abandoned_at;

-- name: UpdateProjectRepositorySetup :execrows
UPDATE project_repository_setups s
SET result = sqlc.narg('result'), status = sqlc.arg('status'),
    failure_code = sqlc.arg('failure_code'), updated_at = clock_timestamp()
FROM projects p, organizations o
WHERE p.id = s.project_id AND p.organization_id = s.organization_id
    AND o.id = s.organization_id AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug') AND s.lease_owner = sqlc.arg('lease_owner') AND s.abandoned_at IS NULL;

-- name: ReleaseProjectRepositorySetup :execrows
UPDATE project_repository_setups s
SET lease_owner = NULL, lease_until = NULL
FROM projects p, organizations o
WHERE p.id = s.project_id AND p.organization_id = s.organization_id
    AND o.id = s.organization_id AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug') AND s.lease_owner = sqlc.arg('lease_owner') AND s.abandoned_at IS NULL;

-- name: FindProjectForRepositorySetup :one
SELECT p.slug
FROM project_repository_setups s
JOIN projects p ON p.organization_id=s.organization_id AND p.id=s.project_id
JOIN organizations o ON o.id=s.organization_id
WHERE o.slug=sqlc.arg('org_slug') AND s.slug=sqlc.arg('setup_slug') AND s.abandoned_at IS NULL;

-- name: GetProjectRepositorySetupAttempt :one
SELECT s.slug, s.request, s.result, s.status, s.failure_code, s.abandoned_at
FROM project_repository_setups s
JOIN projects p ON p.id=s.project_id AND p.organization_id=s.organization_id
JOIN organizations o ON o.id=s.organization_id
WHERE o.slug=sqlc.arg('org_slug') AND p.slug=sqlc.arg('project_slug') AND s.slug=sqlc.arg('setup_slug');

-- name: AbandonProjectRepositorySetup :one
UPDATE project_repository_setups s
SET abandoned_at=clock_timestamp(), updated_at=clock_timestamp(), lease_owner=NULL, lease_until=NULL
FROM projects p, organizations o
WHERE p.id=s.project_id AND p.organization_id=s.organization_id
    AND o.id=s.organization_id AND o.slug=sqlc.arg('org_slug')
    AND p.slug=sqlc.arg('project_slug') AND s.slug=sqlc.arg('setup_slug')
    AND s.lease_owner=sqlc.arg('lease_owner') AND s.abandoned_at IS NULL
    AND s.status <> 'attached' AND p.pipeline_backend_data_source_id IS NULL
RETURNING s.slug, s.request, s.result, s.status, s.failure_code, s.abandoned_at;

-- name: CreatePipelineCreationProposal :execrows
INSERT INTO pipeline_creation_proposals (organization_id, project_id, member_id, slug, pipeline_slug, request, publish_attempt_id)
SELECT o.id, p.id, m.id, sqlc.arg('creation_slug'), sqlc.arg('pipeline_slug'), sqlc.arg('request'), sqlc.arg('publish_attempt_id')
FROM organizations o
JOIN projects p ON p.organization_id=o.id AND p.slug=sqlc.arg('project_slug')
JOIN organization_members m ON m.organization_id=o.id
JOIN users u ON u.id=m.user_id AND u.slug=sqlc.arg('member_slug') AND u.deleted_at IS NULL
WHERE o.slug=sqlc.arg('org_slug')
ON CONFLICT (project_id, pipeline_slug) DO NOTHING;

-- name: GetPipelineCreationProposal :one
SELECT c.*, u.slug AS member_slug
FROM pipeline_creation_proposals c
JOIN projects p ON p.id=c.project_id AND p.organization_id=c.organization_id
JOIN organizations o ON o.id=c.organization_id
JOIN organization_members m ON m.id=c.member_id AND m.organization_id=c.organization_id
JOIN users u ON u.id=m.user_id AND u.deleted_at IS NULL
WHERE o.slug=sqlc.arg('org_slug') AND p.slug=sqlc.arg('project_slug') AND c.slug=sqlc.arg('creation_slug');

-- name: FindPipelineCreationProposal :one
SELECT c.*, u.slug AS member_slug
FROM pipeline_creation_proposals c
JOIN projects p ON p.id=c.project_id AND p.organization_id=c.organization_id
JOIN organizations o ON o.id=c.organization_id
JOIN organization_members m ON m.id=c.member_id AND m.organization_id=c.organization_id
JOIN users u ON u.id=m.user_id AND u.deleted_at IS NULL
WHERE o.slug=sqlc.arg('org_slug') AND p.slug=sqlc.arg('project_slug') AND c.pipeline_slug=sqlc.arg('pipeline_slug')
FOR UPDATE OF c;

-- name: ListPipelineCreationProposals :many
SELECT c.*, u.slug AS member_slug
FROM pipeline_creation_proposals c
JOIN projects p ON p.id=c.project_id AND p.organization_id=c.organization_id
JOIN organizations o ON o.id=c.organization_id
JOIN organization_members m ON m.id=c.member_id AND m.organization_id=c.organization_id
JOIN users u ON u.id=m.user_id AND u.deleted_at IS NULL
WHERE o.slug=sqlc.arg('org_slug') AND p.slug=sqlc.arg('project_slug')
  AND c.slug > sqlc.arg('after_slug')
ORDER BY c.slug
LIMIT sqlc.arg('page_limit');

-- name: UpdatePipelineCreationProposalResult :execrows
UPDATE pipeline_creation_proposals c
SET status=sqlc.arg('status'), result=sqlc.narg('result'), failure_code=sqlc.arg('failure_code'), updated_at=clock_timestamp()
FROM organizations o, projects p
WHERE o.id=c.organization_id AND p.id=c.project_id AND p.organization_id=o.id
    AND o.slug=sqlc.arg('org_slug') AND p.slug=sqlc.arg('project_slug') AND c.slug=sqlc.arg('creation_slug')
    AND c.publish_attempt_id=sqlc.arg('publish_attempt_id')
    AND c.status NOT IN ('proposal_open', 'branch_pushed');

-- Serialize topology without upgrading concurrent run inserts' foreign-key locks.
-- name: LockPipelineTriggerTopology :one
SELECT e.protected FROM project_environments pe
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
FOR NO KEY UPDATE OF pe, e;

-- name: ListPipelineCompletionEdges :many
SELECT pt.slug, pt.pipeline_slug, pt.enabled, pt.completion_config
FROM pipeline_triggers pt
JOIN project_environments pe ON pe.id = pt.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND e.deleted_at IS NULL AND pe.deleted_at IS NULL
    AND pt.trigger_type = 'completion';

-- name: LoadPipelineCompletionSnapshot :one
SELECT snapshot.*,
    CASE
        WHEN pe.id IS NULL OR pe.deleted_at IS NOT NULL OR e.deleted_at IS NOT NULL THEN 'environment_missing'
        WHEN e.protected THEN 'protected_environment'
        WHEN snapshot.state = 'pending' AND pt.id IS NULL THEN 'trigger_deleted'
        WHEN snapshot.state = 'pending' AND NOT pt.enabled THEN 'trigger_disabled'
        WHEN snapshot.state = 'pending' AND pt.revision <> snapshot.trigger_revision THEN 'trigger_changed'
        WHEN snapshot.state = 'pending' AND (grant_record.trigger_id IS NULL OR grant_record.revoked_at IS NOT NULL) THEN 'grant_revoked'
        WHEN ps.id IS NULL OR ps.deleted_at IS NOT NULL THEN 'pipeline_missing'
        WHEN pv.id IS NULL THEN 'selected_version_missing'
        WHEN deployment.id IS NULL THEN 'selected_deployment_missing'
        WHEN cardinality(snapshot.ancestor_pipeline_slugs) <> snapshot.causal_depth THEN 'ancestor_context_missing'
        WHEN snapshot.downstream_pipeline_slug = ANY(snapshot.ancestor_pipeline_slugs) THEN 'ancestor_pipeline_repeated'
        WHEN resolution.digest IS NULL OR resolution.materialization <> snapshot.selected_materialization THEN 'execution_context_missing'
        ELSE ''
    END::text AS resource_refusal,
    jsonb_build_object(
        'Enabled', snapshot.trigger_configuration->'retry_enabled',
        'MaxAttempts', snapshot.trigger_configuration->'retry_max_attempts',
        'BackoffStrategy', snapshot.trigger_configuration->'retry_backoff_strategy',
        'InitialDelaySeconds', snapshot.trigger_configuration->'retry_initial_delay_seconds',
        'MaxDelaySeconds', snapshot.trigger_configuration->'retry_max_delay_seconds',
        'Jitter', snapshot.trigger_configuration->'retry_jitter',
        'RetryableFailureKinds', snapshot.trigger_configuration->'retryable_failure_kinds',
        'MaxElapsedSeconds', snapshot.trigger_configuration->'retry_max_elapsed_seconds') AS retry_policy,
    pipeline_completion_authority_snapshot(snapshot.organization_id,
        snapshot.trigger_configuration->>'updated_by_actor_type',
        snapshot.trigger_configuration->>'updated_by_actor_slug') AS current_authority
FROM pipeline_completion_snapshots snapshot
JOIN organizations o ON o.id = snapshot.organization_id
JOIN projects p ON p.organization_id = o.id AND p.slug = sqlc.arg('project_slug')
LEFT JOIN project_environments pe ON pe.id = snapshot.project_environment_id AND pe.project_id = p.id
LEFT JOIN environments e ON e.id = pe.environment_id AND e.slug = sqlc.arg('environment_slug') AND e.organization_id = o.id
LEFT JOIN pipeline_triggers pt ON pt.id = snapshot.trigger_id AND pt.project_environment_id = snapshot.project_environment_id
LEFT JOIN pipeline_completion_trigger_revisions grant_record ON grant_record.project_environment_id = snapshot.project_environment_id
    AND grant_record.trigger_id = snapshot.trigger_id AND grant_record.revision = snapshot.trigger_revision
LEFT JOIN pipeline_specs ps ON ps.project_id = p.id AND ps.slug = snapshot.downstream_pipeline_slug
LEFT JOIN pipeline_versions pv ON pv.project_id = p.id AND pv.pipeline_slug = snapshot.downstream_pipeline_slug
    AND pv.id = snapshot.selected_version_id AND pv.spec_hash = snapshot.selected_version_hash
LEFT JOIN pipeline_deployment_resolutions resolution ON resolution.project_environment_id = snapshot.project_environment_id
    AND resolution.pipeline_slug = snapshot.downstream_pipeline_slug AND resolution.digest = snapshot.selected_resolution_digest
LEFT JOIN pipeline_deployments deployment ON deployment.id = snapshot.selected_deployment_id
    AND deployment.project_environment_id = snapshot.project_environment_id AND deployment.pipeline_slug = snapshot.downstream_pipeline_slug
WHERE o.slug = sqlc.arg('org_slug') AND snapshot.id = sqlc.arg('snapshot_id') AND e.slug = sqlc.arg('environment_slug')
FOR UPDATE OF snapshot;

-- name: ListPendingPipelineCompletionsForSystem :many
-- Worker-only bounded discovery; admission reloads and locks the tenant scope.
SELECT snapshot.id, o.slug AS org_slug, p.slug AS project_slug, e.slug AS environment_slug
FROM pipeline_completion_snapshots snapshot
JOIN organizations o ON o.id = snapshot.organization_id
JOIN project_environments pe ON pe.id = snapshot.project_environment_id
JOIN projects p ON p.id = pe.project_id AND p.organization_id = o.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
WHERE snapshot.state = 'pending' AND snapshot.next_evaluation_at <= clock_timestamp()
ORDER BY snapshot.next_evaluation_at, snapshot.id LIMIT 100;

-- name: DeferPipelineCompletionSnapshot :execrows
UPDATE pipeline_completion_snapshots snapshot
SET decision_reason = sqlc.arg('reason'), next_evaluation_at = clock_timestamp() + INTERVAL '1 minute'
FROM organizations o
WHERE snapshot.organization_id = o.id AND o.slug = sqlc.arg('org_slug')
    AND snapshot.id = sqlc.arg('snapshot_id') AND snapshot.state = 'pending';

-- name: AcceptPipelineCompletionSnapshot :execrows
UPDATE pipeline_completion_snapshots snapshot
SET state = 'accepted', decision_reason = NULL, child_run_slug = sqlc.arg('child_run_slug'),
    execution_args = sqlc.arg('execution_args')
FROM organizations o
WHERE snapshot.organization_id = o.id AND o.slug = sqlc.arg('org_slug')
    AND snapshot.id = sqlc.arg('snapshot_id') AND snapshot.state = 'pending';

-- name: ListPipelineCompletionRepairsForSystem :many
-- Repair queued attempts only. A running attempt may already have provider effects.
WITH candidates AS (
    SELECT id, organization_id, project_environment_id, execution_args, child_run_slug AS run_slug
    FROM pipeline_completion_snapshots WHERE state = 'accepted'
    UNION ALL
    SELECT snapshot.id, snapshot.organization_id, snapshot.project_environment_id,
        retry.completion_execution_args, retry.run_slug
    FROM pipeline_completion_snapshots snapshot
    JOIN pipeline_trigger_retry_attempts retry ON retry.project_environment_id = snapshot.project_environment_id
        AND retry.root_run_slug = snapshot.child_run_slug AND retry.source_type = 'completion'
    WHERE snapshot.state = 'accepted'
)
SELECT candidate.id, o.slug AS org_slug, p.slug AS project_slug, e.slug AS environment_slug, candidate.execution_args
FROM candidates candidate
JOIN organizations o ON o.id = candidate.organization_id
JOIN project_environments pe ON pe.id = candidate.project_environment_id
JOIN projects p ON p.id = pe.project_id AND p.organization_id = o.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
JOIN pipeline_runs run ON run.project_environment_id = pe.id AND run.run_slug = candidate.run_slug
WHERE run.status = 'queued' AND candidate.execution_args IS NOT NULL
    AND NOT EXISTS (SELECT 1 FROM river_job job
        WHERE job.kind = 'pipeline_run_execution'
            AND job.args->>'organization_slug' = o.slug AND job.args->>'project_slug' = p.slug
            AND job.args->>'environment_slug' = e.slug AND job.args->>'run_slug' = candidate.run_slug
            AND job.state IN ('available', 'pending', 'retryable', 'running', 'scheduled'))
ORDER BY candidate.id, candidate.run_slug LIMIT 100;

-- name: ListCompletionRunFinalizationsForSystem :many
-- Immutable failed-attempt obligations drive policy decisions, never missing jobs.
SELECT snapshot.id, o.slug AS org_slug, p.slug AS project_slug, e.slug AS environment_slug,
    obligation.attempt_run_slug AS failed_run_slug
FROM pipeline_run_terminal_obligations obligation
JOIN pipeline_completion_snapshots snapshot ON snapshot.organization_id = obligation.organization_id
    AND snapshot.project_environment_id = obligation.project_environment_id AND snapshot.child_run_slug = obligation.root_run_slug
JOIN organizations o ON o.id = snapshot.organization_id
JOIN project_environments pe ON pe.id = snapshot.project_environment_id
JOIN projects p ON p.id = pe.project_id AND p.organization_id = o.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
WHERE snapshot.state = 'accepted' AND obligation.resolved_at IS NULL
    AND obligation.attempt_status = 'failed' AND NOT obligation.excluded AND NOT obligation.outcome_uncertain
    AND obligation.next_evaluation_at <= clock_timestamp()
    AND NOT EXISTS (SELECT 1 FROM pipeline_trigger_retry_attempts decision
        WHERE decision.project_environment_id = obligation.project_environment_id AND decision.root_run_slug = obligation.root_run_slug
            AND decision.retry_of_run_slug = obligation.attempt_run_slug)
ORDER BY obligation.next_evaluation_at, obligation.id LIMIT 100;

-- name: SaveCompletionRetryExecution :execrows
UPDATE pipeline_trigger_retry_attempts retry SET completion_execution_args = sqlc.arg('execution_args')
FROM pipeline_completion_snapshots snapshot JOIN organizations o ON o.id = snapshot.organization_id
WHERE retry.project_environment_id = snapshot.project_environment_id AND retry.root_run_slug = snapshot.child_run_slug
    AND snapshot.id = sqlc.arg('snapshot_id') AND o.slug = sqlc.arg('org_slug')
    AND retry.run_slug = sqlc.arg('run_slug') AND retry.source_type = 'completion';

-- name: FenceCompletionRetryDecision :one
-- A row write makes repeatable-read contenders retry with a fresh decision view.
UPDATE pipeline_completion_snapshots snapshot SET state = state
FROM organizations o WHERE o.id = snapshot.organization_id AND o.slug = sqlc.arg('org_slug')
    AND snapshot.id = sqlc.arg('snapshot_id') AND snapshot.state = 'accepted'
RETURNING clock_timestamp() AS evaluated_at;

-- name: DeferCompletionRunFinalization :execrows
UPDATE pipeline_run_terminal_obligations obligation
SET next_evaluation_at = clock_timestamp() + INTERVAL '1 minute', finalization_reason = sqlc.arg('reason')
FROM pipeline_completion_snapshots snapshot JOIN organizations o ON o.id = snapshot.organization_id
WHERE o.id = obligation.organization_id AND o.slug = sqlc.arg('org_slug') AND snapshot.id = sqlc.arg('snapshot_id')
    AND obligation.project_environment_id = snapshot.project_environment_id AND obligation.root_run_slug = snapshot.child_run_slug
    AND obligation.attempt_run_slug = sqlc.arg('run_slug') AND obligation.resolved_at IS NULL;

-- name: CompletePipelineCompletionSnapshotsForSystem :exec
WITH finished AS (
    SELECT snapshot.id FROM pipeline_completion_snapshots snapshot
    JOIN pipeline_run_logical_closures closure ON closure.organization_id = snapshot.organization_id
        AND closure.project_environment_id = snapshot.project_environment_id AND closure.root_run_slug = snapshot.child_run_slug
    WHERE snapshot.state = 'accepted'
    ORDER BY snapshot.id LIMIT 100
)
UPDATE pipeline_completion_snapshots snapshot SET state = 'completed', completed_at = clock_timestamp()
FROM finished WHERE snapshot.id = finished.id;

-- name: RefusePipelineCompletionSnapshot :execrows
UPDATE pipeline_completion_snapshots snapshot
SET state = 'refused', decision_reason = sqlc.arg('reason'), completed_at = clock_timestamp()
FROM organizations o
WHERE snapshot.organization_id = o.id AND o.slug = sqlc.arg('org_slug')
    AND snapshot.id = sqlc.arg('snapshot_id') AND snapshot.state = 'pending';

-- name: PrunePipelineCompletionDetails :one
WITH pruned AS (
    DELETE FROM pipeline_completion_snapshots snapshot
    USING organizations o
    WHERE snapshot.organization_id = o.id AND o.slug = sqlc.arg('org_slug')
        AND snapshot.completed_at < sqlc.arg('cutoff') AND snapshot.state IN ('refused', 'completed')
        AND snapshot.trigger_configuration IS NOT NULL
    RETURNING snapshot.id
), pruned_revisions AS (
    UPDATE pipeline_completion_trigger_revisions revision
    SET configuration = NULL
    FROM organizations o
    WHERE revision.organization_id = o.id AND o.slug = sqlc.arg('org_slug')
        AND revision.revoked_at < sqlc.arg('cutoff') AND revision.configuration IS NOT NULL
        AND NOT EXISTS (SELECT 1 FROM pipeline_completion_snapshots snapshot
            WHERE snapshot.organization_id = revision.organization_id
                AND snapshot.project_environment_id = revision.project_environment_id
                AND snapshot.trigger_id = revision.trigger_id AND snapshot.trigger_revision = revision.revision
                AND snapshot.completed_at IS NULL)
    RETURNING revision.trigger_id
)
SELECT (SELECT count(*) FROM pruned) + (SELECT count(*) FROM pruned_revisions) AS pruned_count;

-- name: InspectRunRecency :one
SELECT COALESCE(expectation.id, 0)::bigint AS expectation_id,
    pe.id AS project_environment_id,
    COALESCE(expectation.revision, 0)::bigint AS revision,
    COALESCE(expectation.maximum_age_seconds, 0)::bigint AS maximum_age_seconds,
    COALESCE(expectation.enabled, false)::boolean AS enabled, expectation.enabled_at,
    COALESCE(d.id, 0)::bigint AS deployment_id,
    COALESCE(d.revision, 0)::bigint AS deployment_revision,
    COALESCE(d.current_version_id, 0)::bigint AS current_version_id,
    v.spec_hash AS version_spec_hash, d.current_resolution_digest, d.deployed_at,
    evidence.run_slug AS success_run_slug, evidence.succeeded_at,
    (pe.deleted_at IS NOT NULL OR e.deleted_at IS NOT NULL) AS scope_disabled
FROM project_environments pe
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
JOIN pipeline_specs ps ON ps.project_id = p.id AND ps.slug = sqlc.arg('pipeline_slug')
    AND ps.deleted_at IS NULL
LEFT JOIN pipeline_deployments d ON d.project_environment_id = pe.id AND d.pipeline_slug = ps.slug
LEFT JOIN pipeline_versions v ON v.id = d.current_version_id
LEFT JOIN LATERAL (
    SELECT x.* FROM pipeline_run_recency_expectations x
    WHERE x.project_environment_id = pe.id AND x.pipeline_slug = ps.slug
    ORDER BY x.revision DESC LIMIT 1
) expectation ON true
LEFT JOIN pipeline_run_recency_successes evidence
    ON evidence.project_environment_id = pe.id AND evidence.pipeline_slug = ps.slug
    AND evidence.deployment_id = d.id AND evidence.deployment_revision = d.revision
    AND evidence.pipeline_version_id = d.current_version_id
    AND evidence.deployment_resolution_digest IS NOT DISTINCT FROM d.current_resolution_digest
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug');

-- name: InspectRunRecencyEpisodes :many
SELECT episode.*, expectation.revision AS expectation_revision,
    expectation.maximum_age_seconds, expectation.enabled_at, v.spec_hash AS version_spec_hash
FROM pipeline_run_recency_episodes episode
JOIN pipeline_run_recency_expectations expectation ON expectation.id = episode.expectation_id
JOIN project_environments pe ON pe.id = expectation.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
JOIN pipeline_versions v ON v.id = episode.pipeline_version_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND expectation.pipeline_slug = sqlc.arg('pipeline_slug')
ORDER BY episode.id DESC LIMIT 21;

-- name: InspectPipelineLogicalRun :many
WITH target AS (
    SELECT p.organization_id, pr.project_environment_id, COALESCE(NULLIF(pr.source_root_run_slug, ''), pr.run_slug)::text AS root_run_slug
    FROM pipeline_runs pr
    JOIN project_environments pe ON pe.id = pr.project_environment_id
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug') AND pr.run_slug = sqlc.arg('run_slug')
)
SELECT target.root_run_slug, closure.id IS NOT NULL AS closed,
    closure.status AS outcome, closure.final_attempt_run_slug, closure.decision_reason,
    (closure.id IS NULL AND EXISTS (
        SELECT 1 FROM pipeline_trigger_retry_attempts retry
        WHERE retry.project_environment_id = target.project_environment_id AND retry.root_run_slug = target.root_run_slug
            AND retry.status IN ('queued', 'running')
    ))::boolean AS waiting_for_retry,
    attempt.pipeline_slug, attempt.run_slug, attempt.source_attempt_number, attempt.status,
    attempt.pipeline_version_spec_hash, attempt.deployment_revision
FROM target
JOIN pipeline_runs attempt ON attempt.project_environment_id = target.project_environment_id
    AND COALESCE(NULLIF(attempt.source_root_run_slug, ''), attempt.run_slug) = target.root_run_slug
LEFT JOIN pipeline_run_logical_closures closure ON closure.organization_id=target.organization_id AND closure.project_environment_id = target.project_environment_id
    AND closure.root_run_slug = target.root_run_slug
ORDER BY attempt.source_attempt_number, attempt.id
LIMIT 11;

-- name: ListPipelineRunCompletionDecisions :many
WITH target AS (
    SELECT p.organization_id, pr.project_environment_id, COALESCE(NULLIF(pr.source_root_run_slug, ''), pr.run_slug)::text AS root_run_slug
    FROM pipeline_runs pr
    JOIN project_environments pe ON pe.id = pr.project_environment_id
    JOIN projects p ON p.id = pe.project_id
    JOIN organizations o ON o.id = p.organization_id
    JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
    WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
        AND e.slug = sqlc.arg('environment_slug') AND pr.run_slug = sqlc.arg('run_slug')
)
SELECT COALESCE(snapshot.child_run_slug = target.root_run_slug, false)::boolean AS is_parent,
    snapshot.trigger_slug, snapshot.trigger_revision, COALESCE(upstream.pipeline_slug, '')::text AS upstream_pipeline_slug,
    snapshot.root_run_slug, snapshot.final_attempt_run_slug, snapshot.downstream_pipeline_slug,
    COALESCE(snapshot.trigger_configuration->'completion_config'->>'condition', '')::text AS condition,
    snapshot.state, snapshot.decision_reason, snapshot.child_run_slug,
    child.status AS child_status, child_closure.status AS child_outcome,
    snapshot.selected_version_hash, snapshot.selected_deployment_revision, snapshot.selected_resolution_digest
FROM target
JOIN pipeline_completion_snapshots snapshot ON snapshot.organization_id=target.organization_id AND snapshot.project_environment_id = target.project_environment_id
    AND (snapshot.root_run_slug = target.root_run_slug OR snapshot.child_run_slug = target.root_run_slug)
LEFT JOIN pipeline_runs upstream ON upstream.project_environment_id = target.project_environment_id
    AND upstream.run_slug = snapshot.final_attempt_run_slug
LEFT JOIN pipeline_runs child ON child.project_environment_id = target.project_environment_id
    AND child.run_slug = snapshot.child_run_slug
LEFT JOIN pipeline_run_logical_closures child_closure ON child_closure.project_environment_id = target.project_environment_id
    AND child_closure.root_run_slug = snapshot.child_run_slug
ORDER BY is_parent DESC, snapshot.id
LIMIT 101;

-- name: FindPreviousSuccessfulPipelineRun :one
SELECT pr.run_slug, pr.started_at, pr.ended_at
FROM pipeline_runs pr
JOIN project_environments pe ON pe.id = pr.project_environment_id
JOIN projects p ON p.id = pe.project_id AND p.id = pr.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
WHERE o.slug = sqlc.arg('org_slug')
  AND p.slug = sqlc.arg('project_slug')
  AND e.slug = sqlc.arg('environment_slug')
  AND pr.pipeline_slug = sqlc.arg('pipeline_slug')
  AND pr.pipeline_version_id = sqlc.arg('pipeline_version_id')
  AND pr.status = 'succeeded'
  AND pr.ended_at < sqlc.arg('before_at')::timestamptz
  AND (pr.replay_of_run_slug IS NULL OR pr.source_attempt_number > 1)
  AND pr.backfill_series_slug IS NULL
ORDER BY pr.ended_at DESC, pr.id DESC
LIMIT 1;

-- name: ObservePipelineRunWorker :execrows
UPDATE pipeline_runs pr
SET timeline = jsonb_set(pr.timeline, '{worker_observed_at}', to_jsonb(GREATEST(
    (pr.timeline->>'worker_observed_at')::timestamptz, sqlc.arg('observed_at')::timestamptz)))
FROM project_environments pe
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
WHERE pr.project_environment_id = pe.id
    AND o.slug = sqlc.arg('org_slug')
    AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug')
    AND pr.run_slug = sqlc.arg('run_slug')
    AND pr.status = 'running'
    AND pr.timeline ? 'revision';

-- name: BindPipelineRunWorkerJob :execrows
UPDATE pipeline_runs pr
SET worker_job_id = sqlc.arg('worker_job_id')
FROM project_environments pe
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
WHERE pr.project_environment_id = pe.id
    AND o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND pr.run_slug = sqlc.arg('run_slug')
    AND pr.status IN ('queued', 'running')
    AND (pr.worker_job_id IS NULL OR pr.worker_job_id = sqlc.arg('worker_job_id'));

-- name: ListInterruptedPipelineRunsForSystem :many
-- Queue state fences recovery; silence or an absent queue row never proves worker loss.
SELECT o.slug AS org_slug, p.slug AS project_slug, e.slug AS environment_slug,
    pr.run_slug, job.state::text AS job_state
FROM pipeline_runs pr
JOIN river_job job ON job.id = pr.worker_job_id
JOIN project_environments pe ON pe.id = pr.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
WHERE pr.status IN ('queued', 'running')
    AND job.state IN ('available', 'retryable', 'scheduled', 'completed', 'cancelled', 'discarded')
    AND (job.state IN ('completed', 'cancelled', 'discarded') OR (pr.status = 'running' AND job.attempt > 0))
    AND GREATEST(pr.started_at, COALESCE((pr.timeline->>'worker_observed_at')::timestamptz, pr.started_at)) < sqlc.arg('cutoff')::timestamptz
ORDER BY pr.id
LIMIT 100
FOR UPDATE OF pr, job SKIP LOCKED;

-- name: LockPipelineRunRecoveryFence :one
-- A retained queue row must prove ownership has ended; absence and silence do not.
SELECT pr.id
FROM pipeline_runs pr
JOIN river_job job ON job.id = pr.worker_job_id
JOIN project_environments pe ON pe.id = pr.project_environment_id
JOIN projects p ON p.id = pe.project_id
JOIN organizations o ON o.id = p.organization_id
JOIN environments e ON e.id = pe.environment_id
WHERE o.slug = sqlc.arg('org_slug') AND p.slug = sqlc.arg('project_slug')
    AND e.slug = sqlc.arg('environment_slug') AND pr.run_slug = sqlc.arg('run_slug')
    AND pr.status = 'failed'
    AND job.state IN ('available', 'retryable', 'scheduled', 'completed', 'cancelled', 'discarded')
    AND job.attempt > 0
FOR UPDATE OF pr, job;

-- name: GetPipelineWebhookIntake :one
-- Queue-owned lookup: private receiver/delivery identity yields only retained source metadata.
SELECT pwd.id, pwd.receiver_id, pwd.delivery_id, pwd.payload_digest, pwd.payload_size_bytes,
    pwd.run_slug, pwd.received_at, pwd.trigger_revision, pwd.pipeline_slug, pwd.runtime_input_keys,
    o.slug AS org_slug, p.slug AS project_slug, e.slug AS environment_slug, pt.slug AS trigger_slug
FROM pipeline_webhook_deliveries pwd
JOIN organizations o ON o.id = pwd.organization_id
JOIN projects p ON p.id = pwd.project_id AND p.organization_id = o.id
JOIN project_environments pe ON pe.id = pwd.project_environment_id AND pe.project_id = p.id
JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
JOIN pipeline_triggers pt ON pt.id = pwd.pipeline_trigger_id AND pt.project_id = p.id
WHERE pwd.receiver_id = sqlc.arg('receiver_id') AND pwd.delivery_id = sqlc.arg('delivery_id');

-- name: BindExecutionRequestSource :execrows
UPDATE execution_requests r SET scope_data_source_id = d.id
FROM organizations o, data_sources d
WHERE r.id = sqlc.arg('request_id')::uuid AND r.organization_id = o.id AND d.organization_id = o.id
  AND o.slug = sqlc.arg('org_slug') AND d.slug = sqlc.arg('source_slug')
  AND d.deleted_at IS NULL AND r.state = 'pending' AND r.scope_data_source_id IS NULL;

-- name: ListCredentialCiphertexts :many
-- Operator-only census includes retained ciphertext for deleted/expired owners.
WITH payloads AS (
    SELECT 1::integer AS family, c.id AS record_id, o.slug::text AS tenant,
        c.slug::text AS resource, ''::text AS resource_aux,
        CASE WHEN c.credential_type = 'pipeline_webhook' THEN 'pipeline-webhook-secret' ELSE 'static-secret' END::text AS purpose,
        c.encrypted_payload AS ciphertext
    FROM credentials c JOIN organizations o ON o.id = c.organization_id
    WHERE octet_length(c.encrypted_payload) > 0
    UNION ALL
    SELECT 2, g.id, o.slug::text, c.slug::text, '', 'oauth-token-set', g.encrypted_token_set
    FROM credential_oauth_grants g JOIN credentials c ON c.id = g.credential_id
    JOIN organizations o ON o.id = c.organization_id
    WHERE octet_length(g.encrypted_token_set) > 0
    UNION ALL
    SELECT 3, i.id, o.slug::text, i.slug::text, '', 'oauth-install-snapshot', i.encrypted_pkce_verifier
    FROM credential_oauth_installs i JOIN organizations o ON o.id = i.organization_id
    WHERE octet_length(i.encrypted_pkce_verifier) > 0
    UNION ALL
    SELECT 4, a.data_source_id, o.slug::text, d.slug::text, '', 'http-oauth-app', a.encrypted_config
    FROM credential_http_oauth_apps a
    JOIN data_sources d ON d.id = a.data_source_id AND d.organization_id = a.organization_id
    JOIN organizations o ON o.id = a.organization_id
    WHERE octet_length(a.encrypted_config) > 0
    UNION ALL
    SELECT 5, h.id, o.slug::text, d.slug::text, '', 'gitlab-webhook-secret', h.encrypted_signing_token
    FROM gitlab_proof_webhooks h
    JOIN data_sources d ON d.id = h.data_source_id AND d.organization_id = h.organization_id
    JOIN organizations o ON o.id = h.organization_id
    WHERE octet_length(h.encrypted_signing_token) > 0
    UNION ALL
    SELECT 6, w.id, o.slug::text, w.receiver_id::text, w.delivery_id::text, 'pipeline-webhook-envelope', w.encrypted_envelope
    FROM pipeline_webhook_deliveries w JOIN organizations o ON o.id = w.organization_id
    WHERE octet_length(w.encrypted_envelope) > 0
)
SELECT family, record_id, tenant, resource, resource_aux, purpose, ciphertext FROM payloads
WHERE (family, record_id) > (sqlc.arg('after_family')::integer, sqlc.arg('after_id')::bigint)
ORDER BY family, record_id
LIMIT sqlc.arg('batch_size')::integer;

-- name: ReplaceCredentialCiphertext :one
-- Compare-and-swap preserves concurrent refresh/revocation and lifecycle versions.
WITH static_rekey AS (
    UPDATE credentials c SET encrypted_payload = sqlc.arg('replacement')::bytea
    FROM organizations o
    WHERE sqlc.arg('family')::integer = 1 AND c.id = sqlc.arg('record_id')::bigint
      AND o.id = c.organization_id AND o.slug = sqlc.arg('tenant')::text
      AND c.slug = sqlc.arg('resource')::text AND c.encrypted_payload = sqlc.arg('expected')::bytea
    RETURNING 1
), grant_rekey AS (
    UPDATE credential_oauth_grants g SET encrypted_token_set = sqlc.arg('replacement')::bytea
    FROM credentials c, organizations o
    WHERE sqlc.arg('family')::integer = 2 AND g.id = sqlc.arg('record_id')::bigint
      AND c.id = g.credential_id AND o.id = c.organization_id AND o.slug = sqlc.arg('tenant')::text
      AND c.slug = sqlc.arg('resource')::text AND g.encrypted_token_set = sqlc.arg('expected')::bytea
    RETURNING 1
), install_rekey AS (
    UPDATE credential_oauth_installs i SET encrypted_pkce_verifier = sqlc.arg('replacement')::bytea
    FROM organizations o
    WHERE sqlc.arg('family')::integer = 3 AND i.id = sqlc.arg('record_id')::bigint
      AND o.id = i.organization_id AND o.slug = sqlc.arg('tenant')::text
      AND i.slug = sqlc.arg('resource')::text AND i.encrypted_pkce_verifier = sqlc.arg('expected')::bytea
    RETURNING 1
), app_rekey AS (
    UPDATE credential_http_oauth_apps a SET encrypted_config = sqlc.arg('replacement')::bytea
    FROM data_sources d, organizations o
    WHERE sqlc.arg('family')::integer = 4 AND a.data_source_id = sqlc.arg('record_id')::bigint
      AND d.id = a.data_source_id AND d.organization_id = a.organization_id AND o.id = a.organization_id
      AND o.slug = sqlc.arg('tenant')::text AND d.slug = sqlc.arg('resource')::text
      AND a.encrypted_config = sqlc.arg('expected')::bytea
    RETURNING 1
), hook_rekey AS (
    UPDATE gitlab_proof_webhooks h SET encrypted_signing_token = sqlc.arg('replacement')::bytea
    FROM data_sources d, organizations o
    WHERE sqlc.arg('family')::integer = 5 AND h.id = sqlc.arg('record_id')::bigint
      AND d.id = h.data_source_id AND d.organization_id = h.organization_id AND o.id = h.organization_id
      AND o.slug = sqlc.arg('tenant')::text AND d.slug = sqlc.arg('resource')::text
      AND h.encrypted_signing_token = sqlc.arg('expected')::bytea
    RETURNING 1
), envelope_rekey AS (
    UPDATE pipeline_webhook_deliveries w SET encrypted_envelope = sqlc.arg('replacement')::bytea
    FROM organizations o
    WHERE sqlc.arg('family')::integer = 6 AND w.id = sqlc.arg('record_id')::bigint
      AND o.id = w.organization_id AND o.slug = sqlc.arg('tenant')::text
      AND w.receiver_id = sqlc.arg('resource')::text AND w.delivery_id = sqlc.arg('resource_aux')::text
      AND w.encrypted_envelope = sqlc.arg('expected')::bytea
    RETURNING 1
)
SELECT (SELECT count(*) FROM static_rekey) + (SELECT count(*) FROM grant_rekey)
    + (SELECT count(*) FROM install_rekey) + (SELECT count(*) FROM app_rekey)
    + (SELECT count(*) FROM hook_rekey) + (SELECT count(*) FROM envelope_rekey) AS changed;

-- name: BeginRetainedFile :one
SELECT begin_retained_file(sqlc.arg('request_id')::uuid, sqlc.arg('claim_token')::uuid, sqlc.arg('org_slug')::text, sqlc.arg('max_bytes')::bigint, sqlc.arg('expires_at')::timestamptz)::boolean;

-- name: AppendRetainedFileChunk :one
SELECT append_retained_file_chunk(sqlc.arg('request_id')::uuid, sqlc.arg('claim_token')::uuid, sqlc.arg('org_slug')::text, sqlc.arg('sequence')::integer, sqlc.arg('plain_bytes')::integer, sqlc.arg('ciphertext')::bytea)::boolean;

-- name: SealRetainedFile :one
SELECT seal_retained_file(sqlc.arg('request_id')::uuid, sqlc.arg('claim_token')::uuid, sqlc.arg('org_slug')::text, sqlc.arg('size_bytes')::bigint, sqlc.arg('sha256')::text)::boolean;

-- name: ReadRetainedFile :one
SELECT size_bytes::bigint, chunk_count::integer, sha256::text, expires_at::timestamptz
FROM read_retained_file(sqlc.arg('request_id')::uuid, sqlc.arg('claim_token')::uuid, sqlc.arg('org_slug')::text, sqlc.arg('source_request_id')::uuid);

-- name: ReadRetainedFileChunk :one
SELECT read_retained_file_chunk(sqlc.arg('request_id')::uuid, sqlc.arg('claim_token')::uuid, sqlc.arg('org_slug')::text, sqlc.arg('source_request_id')::uuid, sqlc.arg('sequence')::integer)::bytea;

-- name: DiscardRetainedFile :one
SELECT discard_retained_file(sqlc.arg('request_id')::uuid, sqlc.arg('claim_token')::uuid, sqlc.arg('org_slug')::text)::boolean;

-- name: RetainedFileCapabilitiesReady :one
SELECT COALESCE(
    to_regclass('public.retained_file_streams') IS NOT NULL
    AND to_regclass('public.retained_file_chunks') IS NOT NULL
    AND has_function_privilege(current_user, to_regprocedure('public.begin_retained_file(uuid,uuid,text,bigint,timestamptz)'), 'EXECUTE')
    AND has_function_privilege(current_user, to_regprocedure('public.append_retained_file_chunk(uuid,uuid,text,integer,integer,bytea)'), 'EXECUTE')
    AND has_function_privilege(current_user, to_regprocedure('public.seal_retained_file(uuid,uuid,text,bigint,text)'), 'EXECUTE')
    AND has_function_privilege(current_user, to_regprocedure('public.read_retained_file(uuid,uuid,text,uuid)'), 'EXECUTE')
    AND has_function_privilege(current_user, to_regprocedure('public.read_retained_file_chunk(uuid,uuid,text,uuid,integer)'), 'EXECUTE')
    AND has_function_privilege(current_user, to_regprocedure('public.discard_retained_file(uuid,uuid,text)'), 'EXECUTE'), FALSE)::boolean AS ready;
