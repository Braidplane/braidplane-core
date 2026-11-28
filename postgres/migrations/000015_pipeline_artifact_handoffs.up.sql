CREATE TABLE public.pipeline_artifact_deliveries (
    id uuid PRIMARY KEY,
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    producer_attempt_slug varchar(100) NOT NULL,
    sink_step_slug varchar(100) NOT NULL,
    effect_key text NOT NULL,
    producer_version_id bigint NOT NULL,
    deployment_id bigint NOT NULL,
    deployment_resolution_digest text NOT NULL,
    write_query_id bigint NOT NULL,
    write_query_updated_at timestamptz NOT NULL,
    data_source_id bigint NOT NULL,
    source_config_digest text NOT NULL,
    credential_id bigint NOT NULL,
    credential_updated_at timestamptz NOT NULL,
    final_path text NOT NULL,
    staging_path text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 0 AND 10485760),
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    publish_request_id uuid NOT NULL UNIQUE,
    submitted_at timestamptz,
    submission_token uuid,
    permit_spent_at timestamptz,
    confirmed_at timestamptz,
    CHECK (final_path <> staging_path),
    CHECK ((submitted_at IS NULL) = (submission_token IS NULL)),
    CHECK (permit_spent_at IS NULL OR submitted_at IS NOT NULL),
    CHECK (confirmed_at IS NULL OR permit_spent_at IS NOT NULL),
    UNIQUE (organization_id, project_environment_id, id),
    UNIQUE (project_environment_id, producer_attempt_slug, sink_step_slug),
    FOREIGN KEY (organization_id, project_id, project_environment_id)
        REFERENCES public.project_environments (organization_id, project_id, id) ON DELETE CASCADE,
    FOREIGN KEY (project_environment_id, producer_attempt_slug)
        REFERENCES public.pipeline_runs (project_environment_id, run_slug) ON DELETE CASCADE,
    FOREIGN KEY (project_environment_id, producer_attempt_slug, sink_step_slug, effect_key)
        REFERENCES public.pipeline_sink_effects (project_environment_id, run_slug, sink_step_slug, idempotency_key) ON DELETE CASCADE
);

-- The intent update is the submission lock. Call before creating the envelope
-- and River job, in the same transaction. Keep the marker after envelope expiry.
CREATE FUNCTION public.mark_ftp_artifact_submitted(
    delivery_id uuid, request_id uuid, org_slug text, source_slug text, project_env_id bigint, submit_token uuid
) RETURNS boolean
    LANGUAGE sql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    WITH marked AS (
        UPDATE public.pipeline_artifact_deliveries AS d
        SET submitted_at = clock_timestamp(), submission_token = submit_token
        FROM public.organizations AS org, public.data_sources AS source,
             public.pipeline_sink_effects AS effect
        WHERE d.id = delivery_id AND d.publish_request_id = request_id
          AND d.project_environment_id = project_env_id
          AND org.id = d.organization_id AND org.slug = org_slug
          AND source.id = d.data_source_id AND source.organization_id = org.id
          AND source.slug = source_slug AND source.type = 'ftp' AND source.is_active
          AND source.deleted_at IS NULL AND NOT source.read_only
          AND (effect.project_environment_id, effect.run_slug, effect.sink_step_slug, effect.idempotency_key) =
              (d.project_environment_id, d.producer_attempt_slug, d.sink_step_slug, d.effect_key)
          AND effect.state = 'in_progress' AND d.submitted_at IS NULL
        RETURNING d.id
    )
    SELECT EXISTS (SELECT 1 FROM marked);
$$;

-- Only a committed positive result may authorize provider I/O. A second call
-- with the same claim is also refused.
CREATE FUNCTION public.spend_ftp_artifact_permit(
    delivery_id uuid, request_id uuid, claim uuid, org_slug text, source_slug text,
    expected_config jsonb, config_digest text, credential_slug text,
    credential_revision timestamptz, statement jsonb, target text, staging text, bytes bigint, digest text
) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    request public.execution_requests%ROWTYPE;
BEGIN
    SELECT r.* INTO request FROM public.execution_requests AS r WHERE r.id = spend_ftp_artifact_permit.request_id FOR UPDATE;
    IF NOT FOUND OR request.kind <> 'ftp_artifact_publish' OR request.state <> 'running'
       OR request.claim_token IS DISTINCT FROM claim OR request.cancel_requested_at IS NOT NULL THEN
        RETURN FALSE;
    END IF;

    PERFORM 1
    FROM public.pipeline_artifact_deliveries AS d
    JOIN public.organizations AS org ON org.id = d.organization_id
    JOIN public.project_environments AS pe ON pe.id = d.project_environment_id
    JOIN public.environments AS env ON env.id = pe.environment_id
    JOIN public.pipeline_runs AS run ON run.project_environment_id = pe.id
        AND run.run_slug = d.producer_attempt_slug AND run.project_id = d.project_id
    JOIN public.data_sources AS source ON source.id = d.data_source_id AND source.organization_id = org.id
    JOIN public.credentials AS credential ON credential.id = source.credential_id AND credential.organization_id = org.id
    JOIN public.queries AS q ON q.id = d.write_query_id AND q.data_source_id = source.id
    JOIN public.pipeline_sink_effects AS effect ON
        (effect.project_environment_id, effect.run_slug, effect.sink_step_slug, effect.idempotency_key) =
        (d.project_environment_id, d.producer_attempt_slug, d.sink_step_slug, d.effect_key)
    WHERE d.id = delivery_id AND d.publish_request_id = spend_ftp_artifact_permit.request_id
      AND org.id = request.organization_id AND org.slug = org_slug
      AND NOT env.protected AND pe.deleted_at IS NULL AND env.deleted_at IS NULL
      AND run.status = 'running' AND run.pipeline_version_id = d.producer_version_id
      AND run.deployment_id = d.deployment_id AND run.deployment_resolution_digest = d.deployment_resolution_digest
      AND source.id = request.data_source_id AND source.slug = source_slug AND source.type = 'ftp'
      AND source.is_active AND source.deleted_at IS NULL AND NOT source.read_only
      AND source.config = expected_config AND d.source_config_digest = config_digest
      AND credential.id = d.credential_id AND credential.slug = credential_slug
      AND credential.updated_at = d.credential_updated_at AND credential.updated_at = credential_revision
      AND credential.status = 'active' AND credential.kind = 'static_secret'
      AND q.deleted_at IS NULL AND q.updated_at = d.write_query_updated_at AND q.data = statement
      AND effect.state = 'in_progress'
      AND (d.final_path, d.staging_path, d.size_bytes, d.sha256) = (target, staging, bytes, digest)
      AND d.submitted_at IS NOT NULL AND d.permit_spent_at IS NULL
    FOR UPDATE OF d FOR SHARE OF source, credential, q, effect, pe, env, run;
    IF NOT FOUND OR request.deadline_at <= clock_timestamp()
       OR request.lease_expires_at IS NULL OR request.lease_expires_at <= clock_timestamp() THEN
        RETURN FALSE;
    END IF;
    UPDATE public.pipeline_artifact_deliveries SET permit_spent_at = clock_timestamp() WHERE id = delivery_id;
    RETURN TRUE;
END;
$$;

REVOKE ALL ON TABLE public.pipeline_artifact_deliveries FROM PUBLIC;
REVOKE ALL ON FUNCTION public.mark_ftp_artifact_submitted(uuid, uuid, text, text, bigint, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.spend_ftp_artifact_permit(uuid, uuid, uuid, text, text, jsonb, text, text, timestamptz, jsonb, text, text, bigint, text) FROM PUBLIC;

-- A late artifact acknowledgement must never become a usable response.
CREATE OR REPLACE FUNCTION public.complete_execution_request(request_id uuid, expected_claim_token uuid, result bytea, succeeded boolean) RETURNS boolean
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    WITH locked AS MATERIALIZED (
        SELECT request.id
        FROM public.execution_requests AS request
        WHERE request.id = request_id AND request.state = 'running'
            AND request.claim_token = expected_claim_token
        FOR UPDATE
    ),
    completion AS MATERIALIZED (
        SELECT locked.id, clock_timestamp() AS observed_at
        FROM locked
    )
    UPDATE public.execution_requests AS request
    SET state = CASE
            WHEN request.kind IN ('proof_metadata', 'ftp_artifact_prepare', 'ftp_artifact_publish', 'ftp_artifact_read')
                AND (
                    request.cancel_requested_at IS NOT NULL
                    OR request.deadline_at <= completion.observed_at
                    OR request.lease_expires_at <= completion.observed_at
                )
                THEN 'ambiguous'
            WHEN (request.cancel_requested_at IS NOT NULL OR request.deadline_at <= NOW())
                AND request.kind IN ('connection_test', 'schema_introspection', 'transform', 'git_source_snapshot', 'git_branch_list')
                THEN 'cancelled'
            WHEN succeeded THEN 'succeeded'
            WHEN (request.cancel_requested_at IS NOT NULL OR request.deadline_at <= NOW())
                AND request.kind NOT IN ('connection_test', 'schema_introspection', 'transform', 'git_source_snapshot', 'git_branch_list')
                THEN 'ambiguous'
            ELSE 'failed'
        END,
        result_ciphertext = CASE
            WHEN request.expires_at <= completion.observed_at THEN NULL
            WHEN request.kind IN ('proof_metadata', 'ftp_artifact_prepare', 'ftp_artifact_publish', 'ftp_artifact_read')
                AND (
                    request.cancel_requested_at IS NOT NULL
                    OR request.deadline_at <= completion.observed_at
                    OR request.lease_expires_at <= completion.observed_at
                )
                THEN NULL
            WHEN (request.cancel_requested_at IS NOT NULL OR request.deadline_at <= NOW())
                AND request.kind IN ('connection_test', 'schema_introspection', 'transform', 'git_source_snapshot', 'git_branch_list')
                THEN NULL
            WHEN succeeded THEN result
            WHEN request.cancel_requested_at IS NOT NULL OR request.deadline_at <= NOW() THEN NULL
            ELSE result
        END,
        completed_at = NOW(), updated_at = NOW()
    FROM completion
    WHERE request.id = completion.id
        AND (request.kind = 'proof_metadata' OR request.lease_expires_at > completion.observed_at)
    RETURNING TRUE;
$$;

-- Call only after validating the private successful broker response. The effect
-- and receipt change together or the entire statement rolls back.
CREATE FUNCTION public.confirm_ftp_artifact_delivery(
    delivery_id uuid, request_id uuid, org_slug text, environment_id bigint
) RETURNS boolean
    LANGUAGE sql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    WITH request AS MATERIALIZED (
        SELECT r.id, r.organization_id, r.data_source_id
        FROM public.execution_requests AS r
        JOIN public.organizations AS org ON org.id = r.organization_id AND org.slug = org_slug
        WHERE r.id = request_id AND r.kind = 'ftp_artifact_publish' AND r.state = 'succeeded'
          AND r.cancel_requested_at IS NULL AND r.result_ciphertext IS NOT NULL
        FOR UPDATE OF r
    ), intent AS MATERIALIZED (
        SELECT d.*
        FROM request AS r
        JOIN public.pipeline_artifact_deliveries AS d
          ON d.publish_request_id = r.id AND d.organization_id = r.organization_id
         AND d.data_source_id = r.data_source_id
        JOIN public.pipeline_runs AS run ON run.project_environment_id = d.project_environment_id
            AND run.run_slug = d.producer_attempt_slug AND run.project_id = d.project_id
        WHERE run.status = 'running' AND run.pipeline_version_id = d.producer_version_id
          AND run.deployment_id = d.deployment_id AND run.deployment_resolution_digest = d.deployment_resolution_digest
          AND d.id = delivery_id AND d.project_environment_id = environment_id
          AND d.permit_spent_at IS NOT NULL AND d.confirmed_at IS NULL
        FOR UPDATE OF d FOR SHARE OF run
    ), confirmed_effect AS (
        UPDATE public.pipeline_sink_effects AS effect
        SET state = 'succeeded', succeeded_at = statement_timestamp(), updated_at = statement_timestamp()
        FROM intent AS d
        WHERE (effect.project_environment_id, effect.run_slug, effect.sink_step_slug, effect.idempotency_key) =
              (d.project_environment_id, d.producer_attempt_slug, d.sink_step_slug, d.effect_key)
          AND effect.state = 'in_progress'
        RETURNING d.id
    ), receipt AS (
        UPDATE public.pipeline_artifact_deliveries AS d SET confirmed_at = statement_timestamp()
        FROM confirmed_effect AS effect WHERE d.id = effect.id
        RETURNING d.id
    )
    SELECT EXISTS (SELECT 1 FROM receipt);
$$;

REVOKE ALL ON FUNCTION public.confirm_ftp_artifact_delivery(uuid, uuid, text, bigint) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.check_connector_dispatch(request_id uuid, expected_claim_token uuid, organization_slug text, data_source_slug text, data_source_type text, expected_config jsonb, expected_credential_slug text, expected_credential_updated_at timestamp with time zone, admitted_read_only boolean) RETURNS boolean
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    SELECT EXISTS (
        SELECT 1
        FROM public.execution_requests request
        JOIN public.organizations organization ON organization.id = request.organization_id
        JOIN public.data_sources source ON source.id = request.data_source_id AND source.organization_id = organization.id
        JOIN public.credentials credential ON credential.id = source.credential_id AND credential.organization_id = organization.id
        LEFT JOIN public.credential_oauth_grants grant_record ON grant_record.credential_id = credential.id
        WHERE request.id = request_id AND request.claim_token = expected_claim_token AND (request.kind = 'query' OR (request.kind IN ('ftp_artifact_read', 'ftp_artifact_publish') AND source.type = 'ftp' AND expected_config IS NOT NULL) OR (request.kind = 'prepare_s3_write' AND source.type = 's3')) AND request.state = 'running'
            AND request.cancel_requested_at IS NULL AND request.deadline_at > clock_timestamp()
            AND request.lease_expires_at > clock_timestamp()
            AND organization.slug = organization_slug AND source.slug = data_source_slug
            AND source.type = data_source_type AND source.deleted_at IS NULL AND source.is_active
            AND (expected_config IS NULL OR source.config = expected_config)
            AND (NOT source.read_only OR admitted_read_only)
            AND credential.slug = expected_credential_slug AND credential.updated_at = expected_credential_updated_at
            AND credential.status = 'active'
            AND (credential.kind = 'static_secret' OR (credential.kind = 'oauth2_grant' AND grant_record.access_token_expires_at > clock_timestamp()))
            AND (source.organization_gitlab_connection_id IS NULL OR EXISTS (
                SELECT 1 FROM public.organization_gitlab_connections connection
                WHERE connection.id = source.organization_gitlab_connection_id
                    AND connection.organization_id = organization.id AND connection.credential_id = credential.id
            ))
    );
$$;

CREATE FUNCTION public.check_ftp_artifact_read(
    request_id uuid, claim uuid, receipt_id uuid, environment_id bigint, query_id bigint, query_updated_at timestamptz,
    statement jsonb, config_digest text, target text, bytes bigint, digest text
) RETURNS boolean
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    SELECT EXISTS (
        SELECT 1 FROM public.execution_requests AS r
        JOIN public.pipeline_artifact_deliveries AS d ON d.id = check_ftp_artifact_read.receipt_id
            AND d.organization_id = r.organization_id AND d.data_source_id = r.data_source_id
        JOIN public.data_sources AS source ON source.id = d.data_source_id AND source.organization_id = d.organization_id
        JOIN public.credentials AS credential ON credential.id = source.credential_id AND credential.organization_id = d.organization_id
        JOIN public.queries AS q ON q.id = check_ftp_artifact_read.query_id AND q.data_source_id = d.data_source_id
        JOIN public.project_environments AS pe ON pe.id = d.project_environment_id
        JOIN public.environments AS env ON env.id = pe.environment_id
        JOIN public.pipeline_runs AS run ON run.project_environment_id = pe.id AND run.run_slug = d.producer_attempt_slug
        JOIN public.pipeline_sink_effects AS effect ON
            (effect.project_environment_id, effect.run_slug, effect.sink_step_slug, effect.idempotency_key) =
            (d.project_environment_id, d.producer_attempt_slug, d.sink_step_slug, d.effect_key)
        WHERE r.id = check_ftp_artifact_read.request_id AND r.kind = 'ftp_artifact_read' AND r.state = 'running'
          AND r.claim_token = check_ftp_artifact_read.claim AND r.cancel_requested_at IS NULL
          AND r.deadline_at > clock_timestamp() AND r.lease_expires_at > clock_timestamp()
          AND pe.id = check_ftp_artifact_read.environment_id AND NOT env.protected AND pe.deleted_at IS NULL AND env.deleted_at IS NULL
          AND run.status = 'succeeded' AND effect.state = 'succeeded'
          AND credential.id = d.credential_id AND credential.updated_at = d.credential_updated_at
          AND q.id <> d.write_query_id AND q.deleted_at IS NULL AND q.updated_at = check_ftp_artifact_read.query_updated_at AND q.data = check_ftp_artifact_read.statement
          AND d.source_config_digest = check_ftp_artifact_read.config_digest AND d.final_path = check_ftp_artifact_read.target AND d.size_bytes = check_ftp_artifact_read.bytes AND d.sha256 = check_ftp_artifact_read.digest
          AND d.confirmed_at + INTERVAL '24 hours' > clock_timestamp()
    );
$$;

REVOKE ALL ON FUNCTION public.check_ftp_artifact_read(uuid, uuid, uuid, bigint, bigint, timestamptz, jsonb, text, text, bigint, text) FROM PUBLIC;

CREATE FUNCTION public.reject_ftp_artifact_intent_rewrite() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF (to_jsonb(OLD) - ARRAY['submitted_at', 'submission_token', 'permit_spent_at', 'confirmed_at'])
       IS DISTINCT FROM (to_jsonb(NEW) - ARRAY['submitted_at', 'submission_token', 'permit_spent_at', 'confirmed_at'])
       OR (OLD.submitted_at IS NOT NULL AND OLD.submitted_at IS DISTINCT FROM NEW.submitted_at)
       OR (OLD.submission_token IS NOT NULL AND OLD.submission_token IS DISTINCT FROM NEW.submission_token)
       OR (OLD.permit_spent_at IS NOT NULL AND OLD.permit_spent_at IS DISTINCT FROM NEW.permit_spent_at)
       OR (OLD.confirmed_at IS NOT NULL AND OLD.confirmed_at IS DISTINCT FROM NEW.confirmed_at) THEN
        RAISE EXCEPTION 'FTP artifact intent cannot be rewritten';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER reject_ftp_artifact_intent_rewrite
    BEFORE UPDATE ON public.pipeline_artifact_deliveries
    FOR EACH ROW EXECUTE FUNCTION public.reject_ftp_artifact_intent_rewrite();
