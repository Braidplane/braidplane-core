-- Final core schema; includes the existing pinned River v0.37.0 bootstrap (main 1..6).
--
-- PostgreSQL database dump
--


-- Dumped from database version 18.6 (Debian 18.6-1.pgdg13+2)
-- Dumped by pg_dump version 18.6 (Debian 18.6-1.pgdg13+2)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: river_job_state; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.river_job_state AS ENUM (
    'available',
    'cancelled',
    'completed',
    'discarded',
    'pending',
    'retryable',
    'running',
    'scheduled'
);


--
-- Name: check_connector_dispatch(uuid, uuid, text, text, text, jsonb, text, timestamp with time zone, boolean); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.check_connector_dispatch(request_id uuid, expected_claim_token uuid, organization_slug text, data_source_slug text, data_source_type text, expected_config jsonb, expected_credential_slug text, expected_credential_updated_at timestamp with time zone, admitted_read_only boolean) RETURNS boolean
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
        WHERE request.id = request_id AND request.claim_token = expected_claim_token AND (request.kind = 'query' OR (request.kind = 'prepare_s3_write' AND source.type = 's3')) AND request.state = 'running'
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


--
-- Name: claim_execution_request(uuid); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.claim_execution_request(request_id uuid) RETURNS TABLE(id uuid, organization_slug text, kind text, state text, request_ciphertext bytea, result_ciphertext bytea, deadline_at timestamp with time zone, cancel_requested_at timestamp with time zone, claim_token uuid)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    target_data_source_id BIGINT;
    target_created_at TIMESTAMPTZ;
    target_state TEXT;
    effective_concurrency_limit INTEGER;
    active_count BIGINT;
BEGIN
    SELECT request.data_source_id, request.created_at, request.state
    INTO target_data_source_id, target_created_at, target_state
    FROM public.execution_requests AS request
    WHERE request.id = request_id
    FOR UPDATE;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    -- Pending work that can no longer run is terminalized before admission so
    -- River never acknowledges an envelope that remains queued forever.
    RETURN QUERY
    UPDATE public.execution_requests AS request
    SET state = 'cancelled',
        result_ciphertext = NULL,
        completed_at = clock_timestamp(),
        updated_at = clock_timestamp()
    WHERE request.id = request_id
      AND request.state = 'pending'
      AND (
          request.cancel_requested_at IS NOT NULL
          OR request.deadline_at <= clock_timestamp()
          OR (
              request.queue_deadline_at IS NOT NULL
              AND request.queue_deadline_at <= clock_timestamp()
          )
      )
    RETURNING request.id,
        (SELECT organization.slug::TEXT
         FROM public.organizations AS organization
         WHERE organization.id = request.organization_id),
        request.kind::TEXT,
        request.state::TEXT,
        request.request_ciphertext,
        request.result_ciphertext,
        request.deadline_at,
        request.cancel_requested_at,
        request.claim_token;
    IF FOUND THEN
        RETURN;
    END IF;

    IF target_data_source_id IS NOT NULL AND target_state = 'pending' THEN
        -- Serialize only claim decisions for this data source. The lock is
        -- released with the caller's short claim transaction.
        PERFORM data_source.id
        FROM public.data_sources AS data_source
        WHERE data_source.id = target_data_source_id
        FOR UPDATE;

        -- A later request cannot overtake an earlier live request for the same
        -- organization/data source.
        IF EXISTS (
            SELECT 1
            FROM public.execution_requests AS earlier
            WHERE earlier.data_source_id = target_data_source_id
              AND earlier.state = 'pending'
              AND earlier.cancel_requested_at IS NULL
              AND earlier.deadline_at > clock_timestamp()
              AND (
                  earlier.queue_deadline_at IS NULL
                  OR earlier.queue_deadline_at > clock_timestamp()
              )
              AND (earlier.created_at, earlier.id) < (target_created_at, request_id)
        ) THEN
            RETURN;
        END IF;

        -- The strictest live setting wins during rolling configuration changes.
        SELECT MIN(request.concurrency_limit)
        INTO effective_concurrency_limit
        FROM public.execution_requests AS request
        WHERE request.data_source_id = target_data_source_id
          AND request.state IN ('pending', 'running')
          AND request.cancel_requested_at IS NULL
          AND request.deadline_at > clock_timestamp()
          AND (
              request.state = 'running'
              OR request.queue_deadline_at IS NULL
              OR request.queue_deadline_at > clock_timestamp()
          );

        SELECT COUNT(*)
        INTO active_count
        FROM public.execution_requests AS request
        WHERE request.data_source_id = target_data_source_id
          AND request.state = 'running'
          AND request.lease_expires_at > clock_timestamp();

        IF effective_concurrency_limit IS NULL
            OR active_count >= effective_concurrency_limit THEN
            RETURN;
        END IF;
    END IF;

    RETURN QUERY
    UPDATE public.execution_requests AS request
    SET state = 'running',
        claim_token = gen_random_uuid(),
        started_at = COALESCE(request.started_at, clock_timestamp()),
        lease_expires_at = LEAST(
            request.deadline_at,
            clock_timestamp() + INTERVAL '30 seconds'
        ),
        updated_at = clock_timestamp()
    WHERE request.id = request_id
      AND request.state = 'pending'
      AND request.cancel_requested_at IS NULL
      AND request.deadline_at > clock_timestamp()
      AND (
          request.queue_deadline_at IS NULL
          OR request.queue_deadline_at > clock_timestamp()
      )
    RETURNING request.id,
        (SELECT organization.slug::TEXT
         FROM public.organizations AS organization
         WHERE organization.id = request.organization_id),
        request.kind::TEXT,
        request.state::TEXT,
        request.request_ciphertext,
        request.result_ciphertext,
        request.deadline_at,
        request.cancel_requested_at,
        request.claim_token;
    IF FOUND THEN
        RETURN;
    END IF;

    RETURN QUERY
    UPDATE public.execution_requests AS request
    SET state = 'cancelled',
        result_ciphertext = NULL,
        completed_at = clock_timestamp(),
        updated_at = clock_timestamp()
    WHERE request.id = request_id
      AND request.state = 'running'
      AND request.lease_expires_at <= clock_timestamp()
      AND request.kind IN (
          'connection_test',
          'schema_introspection',
          'transform',
          'git_source_snapshot',
          'git_branch_list'
      )
      AND (
          request.cancel_requested_at IS NOT NULL
          OR request.deadline_at <= clock_timestamp()
      )
    RETURNING request.id,
        (SELECT organization.slug::TEXT
         FROM public.organizations AS organization
         WHERE organization.id = request.organization_id),
        request.kind::TEXT,
        request.state::TEXT,
        request.request_ciphertext,
        request.result_ciphertext,
        request.deadline_at,
        request.cancel_requested_at,
        request.claim_token;
    IF FOUND THEN
        RETURN;
    END IF;

    RETURN QUERY
    UPDATE public.execution_requests AS request
    SET claim_token = gen_random_uuid(),
        lease_expires_at = LEAST(
            request.deadline_at,
            clock_timestamp() + INTERVAL '30 seconds'
        ),
        updated_at = clock_timestamp()
    WHERE request.id = request_id
      AND request.state = 'running'
      AND request.lease_expires_at <= clock_timestamp()
      AND request.kind IN (
          'connection_test',
          'schema_introspection',
          'transform',
          'git_source_snapshot',
          'git_branch_list'
      )
      AND request.cancel_requested_at IS NULL
      AND request.deadline_at > clock_timestamp()
    RETURNING request.id,
        (SELECT organization.slug::TEXT
         FROM public.organizations AS organization
         WHERE organization.id = request.organization_id),
        request.kind::TEXT,
        request.state::TEXT,
        request.request_ciphertext,
        request.result_ciphertext,
        request.deadline_at,
        request.cancel_requested_at,
        request.claim_token;
    IF FOUND THEN
        RETURN;
    END IF;

    RETURN QUERY
    UPDATE public.execution_requests AS request
    SET state = 'ambiguous',
        completed_at = clock_timestamp(),
        updated_at = clock_timestamp()
    WHERE request.id = request_id
      AND request.state = 'running'
      AND request.lease_expires_at <= clock_timestamp()
      AND request.kind NOT IN (
          'connection_test',
          'schema_introspection',
          'transform',
          'git_source_snapshot',
          'git_branch_list'
      )
    RETURNING request.id,
        (SELECT organization.slug::TEXT
         FROM public.organizations AS organization
         WHERE organization.id = request.organization_id),
        request.kind::TEXT,
        request.state::TEXT,
        request.request_ciphertext,
        request.result_ciphertext,
        request.deadline_at,
        request.cancel_requested_at,
        request.claim_token;
END;
$$;


--
-- Name: claim_next_execution_request(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.claim_next_execution_request() RETURNS TABLE(id uuid, organization_slug text, kind text, state text, request_ciphertext bytea, result_ciphertext bytea, deadline_at timestamp with time zone, cancel_requested_at timestamp with time zone, claim_token uuid)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    candidate_id UUID;
BEGIN
    FOR candidate_id IN
        SELECT request.id
        FROM public.execution_requests AS request
        WHERE (
                request.state = 'pending'
                AND (
                    request.cancel_requested_at IS NOT NULL
                    OR request.deadline_at <= clock_timestamp()
                    OR (
                        request.queue_deadline_at IS NOT NULL
                        AND request.queue_deadline_at <= clock_timestamp()
                    )
                    OR request.data_source_id IS NULL
                    OR (
                        NOT EXISTS (
                            SELECT 1
                            FROM public.execution_requests AS earlier
                            WHERE earlier.data_source_id = request.data_source_id
                              AND earlier.state = 'pending'
                              AND earlier.cancel_requested_at IS NULL
                              AND earlier.deadline_at > clock_timestamp()
                              AND (
                                  earlier.queue_deadline_at IS NULL
                                  OR earlier.queue_deadline_at > clock_timestamp()
                              )
                              AND (earlier.created_at, earlier.id)
                                  < (request.created_at, request.id)
                        )
                        AND (
                            SELECT COUNT(*)
                            FROM public.execution_requests AS active
                            WHERE active.data_source_id = request.data_source_id
                              AND active.state = 'running'
                              AND active.lease_expires_at > clock_timestamp()
                        ) < (
                            SELECT MIN(live.concurrency_limit)
                            FROM public.execution_requests AS live
                            WHERE live.data_source_id = request.data_source_id
                              AND live.state IN ('pending', 'running')
                              AND live.cancel_requested_at IS NULL
                              AND live.deadline_at > clock_timestamp()
                              AND (
                                  live.state = 'running'
                                  OR live.queue_deadline_at IS NULL
                                  OR live.queue_deadline_at > clock_timestamp()
                              )
                          )
                    )
                )
            ) OR (
                request.state = 'running'
                AND request.lease_expires_at <= clock_timestamp()
        )
        ORDER BY request.created_at, request.id
        LIMIT 50
    LOOP
        RETURN QUERY
        SELECT * FROM public.claim_execution_request(candidate_id);
        IF FOUND THEN
            RETURN;
        END IF;
    END LOOP;
END;
$$;


--
-- Name: close_exhausted_schedule_run_root(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.close_exhausted_schedule_run_root() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.status NOT IN ('exhausted', 'skipped') OR NOT EXISTS (
        SELECT 1 FROM pipeline_run_terminal_obligations obligation
        WHERE obligation.project_environment_id = NEW.project_environment_id
            AND obligation.attempt_run_slug = NEW.retry_of_run_slug
            AND obligation.root_run_slug = NEW.root_run_slug
            AND obligation.attempt_status = 'failed' AND NOT obligation.excluded AND NOT obligation.outcome_uncertain
    ) THEN
        RETURN NEW;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        NEW.project_environment_id::text || ':' || NEW.root_run_slug, 0));
    INSERT INTO pipeline_run_logical_closures (
        organization_id, project_environment_id, root_run_slug, final_attempt_run_slug, status, decision_reason
    ) VALUES (
        (SELECT p.organization_id FROM project_environments pe JOIN projects p ON p.id = pe.project_id WHERE pe.id = NEW.project_environment_id),
        NEW.project_environment_id, NEW.root_run_slug, NEW.retry_of_run_slug, 'failed',
        COALESCE(NEW.decision_reason, 'retry_exhausted')
    ) ON CONFLICT (project_environment_id, root_run_slug) DO NOTHING;
    RETURN NEW;
END;
$$;


--
-- Name: close_final_webhook_run_root(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.close_final_webhook_run_root() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE terminal_run pipeline_runs%ROWTYPE;
        final_attempt integer;
BEGIN
    IF NEW.status <> 'failed' OR NEW.decision_reason = 'retryable_failure_kind' THEN
        RETURN NEW;
    END IF;
    final_attempt := NEW.attempt_number;
    IF NEW.decision_reason = 'execution_envelope_expired' AND OLD.status = 'accepted'
        AND NEW.attempt_number > 1 THEN
        final_attempt := NEW.attempt_number - 1;
    END IF;
    SELECT run.* INTO terminal_run
    FROM pipeline_runs run
    WHERE project_environment_id = NEW.project_environment_id
        AND source_webhook_delivery_id = NEW.id
        AND source_attempt_number = final_attempt
        AND run_slug = CASE WHEN final_attempt = 1 THEN NEW.run_slug
            ELSE NEW.run_slug || '-retry-' || final_attempt::text END
        AND EXISTS (SELECT 1 FROM pipeline_run_terminal_obligations obligation
            WHERE obligation.project_environment_id = run.project_environment_id
                AND obligation.attempt_run_slug = run.run_slug AND NOT obligation.excluded)
        AND run_mode <> 'backfill'
        AND NOT COALESCE((timeline #>> '{failure,outcome_uncertain}')::boolean, false)
        AND status = 'failed';
    IF NOT FOUND THEN
        RETURN NEW;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        terminal_run.project_environment_id::text || ':' ||
        COALESCE(NULLIF(terminal_run.source_root_run_slug, ''), terminal_run.run_slug), 0));
    INSERT INTO pipeline_run_logical_closures (
        organization_id, project_environment_id, root_run_slug, final_attempt_run_slug, status, decision_reason
    ) VALUES (
        (SELECT p.organization_id FROM project_environments pe JOIN projects p ON p.id = pe.project_id WHERE pe.id = terminal_run.project_environment_id),
        terminal_run.project_environment_id,
        COALESCE(NULLIF(terminal_run.source_root_run_slug, ''), terminal_run.run_slug),
        terminal_run.run_slug, 'failed', COALESCE(NEW.decision_reason, 'webhook_failed')
    ) ON CONFLICT (project_environment_id, root_run_slug) DO NOTHING;
    RETURN NEW;
END;
$$;


--
-- Name: complete_execution_request(uuid, uuid, bytea, boolean); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.complete_execution_request(request_id uuid, expected_claim_token uuid, result bytea, succeeded boolean) RETURNS boolean
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
            WHEN request.kind = 'proof_metadata'
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
            WHEN request.kind = 'proof_metadata'
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


--
-- Name: connector_execution_admission_metrics(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.connector_execution_admission_metrics() RETURNS TABLE(active_count bigint, queued_count bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    SELECT
        COUNT(*) FILTER (
            WHERE request.state = 'running'
              AND request.lease_expires_at > clock_timestamp()
        ),
        COUNT(*) FILTER (
            WHERE request.state = 'pending'
              AND request.cancel_requested_at IS NULL
              AND request.deadline_at > clock_timestamp()
              AND (
                  request.queue_deadline_at IS NULL
                  OR request.queue_deadline_at > clock_timestamp()
              )
        )
    FROM public.execution_requests AS request
    WHERE request.data_source_id IS NOT NULL
      AND request.state IN ('pending', 'running');
$$;


--
-- Name: create_admitted_execution_request(uuid, text, text, text, bytea, timestamp with time zone, timestamp with time zone, timestamp with time zone, integer, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.create_admitted_execution_request(request_id uuid, organization_slug text, data_source_slug text, request_kind text, request_payload bytea, request_deadline_at timestamp with time zone, request_queue_deadline_at timestamp with time zone, request_expires_at timestamp with time zone, requested_concurrency_limit integer, requested_queue_capacity integer) RETURNS text
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    target_organization_id INTEGER;
    target_data_source_id BIGINT;
    effective_queue_capacity INTEGER;
    queued_count BIGINT;
BEGIN
    IF requested_concurrency_limit < 1 OR requested_queue_capacity < 1 THEN
        RAISE EXCEPTION 'execution admission limits must be positive';
    END IF;

    SELECT organization.id, data_source.id
    INTO target_organization_id, target_data_source_id
    FROM public.organizations AS organization
    JOIN public.data_sources AS data_source
      ON data_source.organization_id = organization.id
    WHERE organization.slug = organization_slug
      AND data_source.slug = data_source_slug
    FOR UPDATE OF data_source;

    IF NOT FOUND THEN
        RETURN 'not_found';
    END IF;

    -- During a rolling configuration change, the strictest live queue setting
    -- wins so a replica with an older, larger value cannot bypass the bound.
    SELECT LEAST(
        requested_queue_capacity,
        COALESCE(MIN(request.queue_capacity), requested_queue_capacity)
    )
    INTO effective_queue_capacity
    FROM public.execution_requests AS request
    WHERE request.data_source_id = target_data_source_id
      AND request.state IN ('pending', 'running')
      AND request.cancel_requested_at IS NULL
      AND request.deadline_at > clock_timestamp()
      AND (
          request.state = 'running'
          OR request.queue_deadline_at IS NULL
          OR request.queue_deadline_at > clock_timestamp()
      );

    SELECT COUNT(*)
    INTO queued_count
    FROM public.execution_requests AS request
    WHERE request.data_source_id = target_data_source_id
      AND request.state = 'pending'
      AND request.cancel_requested_at IS NULL
      AND request.deadline_at > clock_timestamp()
      AND (
          request.queue_deadline_at IS NULL
          OR request.queue_deadline_at > clock_timestamp()
      );

    IF queued_count >= effective_queue_capacity THEN
        RETURN 'queue_full';
    END IF;

    INSERT INTO public.execution_requests (
        id,
        organization_id,
        data_source_id,
        kind,
        request_ciphertext,
        deadline_at,
        queue_deadline_at,
        expires_at,
        concurrency_limit,
        queue_capacity
    ) VALUES (
        request_id,
        target_organization_id,
        target_data_source_id,
        request_kind,
        request_payload,
        request_deadline_at,
        request_queue_deadline_at,
        request_expires_at,
        requested_concurrency_limit,
        requested_queue_capacity
    );

    RETURN 'created';
END;
$$;


--
-- Name: execution_request_metrics(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.execution_request_metrics() RETURNS TABLE(pending_count bigint, running_count bigint, oldest_pending_age_seconds double precision)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    SELECT
        COUNT(*) FILTER (WHERE request.state = 'pending'),
        COUNT(*) FILTER (WHERE request.state = 'running'),
        COALESCE(
            EXTRACT(EPOCH FROM (
                clock_timestamp() - MIN(request.created_at) FILTER (WHERE request.state = 'pending')
            )),
            0
        )::DOUBLE PRECISION
    FROM public.execution_requests AS request;
$$;


--
-- Name: fence_pipeline_completion_redelivery(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.fence_pipeline_completion_redelivery() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO pipeline_completion_tombstones (organization_id, closure_id, trigger_id, trigger_revision)
    VALUES (NEW.organization_id, NEW.closure_id, NEW.trigger_id, NEW.trigger_revision)
    ON CONFLICT DO NOTHING;
    IF NOT FOUND THEN RETURN NULL; END IF;
    RETURN NEW;
END;
$$;


--
-- Name: fence_schedule_run_retry(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.fence_schedule_run_retry() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        NEW.project_environment_id::text || ':' || NEW.root_run_slug, 0));
    IF NEW.status = 'queued' AND EXISTS (
        SELECT 1 FROM pipeline_run_terminal_obligations WHERE project_environment_id=NEW.project_environment_id
            AND attempt_run_slug=NEW.retry_of_run_slug AND outcome_uncertain
    ) THEN RAISE EXCEPTION 'uncertain run outcome cannot accept automatic retry'; END IF;
    IF NEW.status = 'queued' AND EXISTS (
        SELECT 1 FROM pipeline_run_logical_closures
        WHERE project_environment_id = NEW.project_environment_id
            AND root_run_slug = NEW.root_run_slug
    ) AND NOT EXISTS (
        SELECT 1 FROM pipeline_trigger_retry_attempts existing
        WHERE existing.project_environment_id = NEW.project_environment_id
            AND existing.attempt_slug = NEW.attempt_slug
    ) THEN
        RAISE EXCEPTION 'closed logical run cannot accept another retry';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: fence_webhook_run_retry(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.fence_webhook_run_retry() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        NEW.project_environment_id::text || ':' || NEW.run_slug, 0));
    IF NEW.attempt_number > OLD.attempt_number AND EXISTS (
        SELECT 1 FROM pipeline_run_logical_closures
        WHERE project_environment_id = NEW.project_environment_id
            AND root_run_slug = NEW.run_slug
    ) THEN
        RAISE EXCEPTION 'closed logical run cannot accept another retry';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: heartbeat_execution_request(uuid, uuid); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.heartbeat_execution_request(request_id uuid, expected_claim_token uuid) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    current_request public.execution_requests%ROWTYPE;
    observed_at TIMESTAMPTZ;
BEGIN
    SELECT * INTO current_request FROM public.execution_requests AS request
    WHERE request.id = request_id FOR UPDATE;
    observed_at := clock_timestamp();
    IF NOT FOUND OR current_request.state <> 'running'
        OR current_request.claim_token IS DISTINCT FROM expected_claim_token
        OR current_request.lease_expires_at <= observed_at THEN
        RETURN TRUE;
    END IF;
    UPDATE public.execution_requests AS request
    SET lease_expires_at = LEAST(request.deadline_at, observed_at + INTERVAL '30 seconds'),
        updated_at = observed_at
    WHERE request.id = request_id;
    RETURN current_request.cancel_requested_at IS NOT NULL OR current_request.deadline_at <= observed_at;
END;
$$;


--
-- Name: invalidate_data_source_proof(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.invalidate_data_source_proof() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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


--
-- Name: pipeline_completion_authority_snapshot(bigint, text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.pipeline_completion_authority_snapshot(org_id bigint, actor_type text, actor_slug text) RETURNS jsonb
    LANGUAGE plpgsql
    AS $$
DECLARE role_id_value INTEGER;
        valid_actor BOOLEAN := false;
        permissions_value JSONB;
BEGIN
    IF actor_type = 'user' THEN
        SELECT member.role_id, usr.verified_at IS NOT NULL INTO role_id_value, valid_actor
        FROM organization_members member JOIN users usr ON usr.id = member.user_id
        WHERE member.organization_id = org_id AND usr.slug = actor_slug AND usr.deleted_at IS NULL
        FOR SHARE OF member, usr;
    ELSIF actor_type = 'api_key' THEN
        SELECT key_record.role_id, key_record.revoked_at IS NULL AND
            (key_record.expires_at IS NULL OR key_record.expires_at > clock_timestamp())
        INTO role_id_value, valid_actor
        FROM api_keys key_record WHERE key_record.organization_id = org_id AND key_record.slug = actor_slug
        FOR SHARE OF key_record;
    END IF;
    PERFORM id FROM roles WHERE id = role_id_value AND organization_id = org_id AND deleted_at IS NULL FOR SHARE;
    IF NOT FOUND OR NOT COALESCE(valid_actor, false) THEN
        RETURN jsonb_build_object('valid', false, 'permissions', '[]'::jsonb);
    END IF;
    -- Hold existing permission rows through the admission/execution transaction.
    PERFORM id FROM role_permissions WHERE role_id = role_id_value FOR SHARE;
    SELECT COALESCE(jsonb_agg(permission.resource || ':' || permission.action ORDER BY permission.resource, permission.action), '[]'::jsonb)
        INTO permissions_value FROM role_permissions rp JOIN permissions permission ON permission.id = rp.permission_id
        WHERE rp.role_id = role_id_value;
    RETURN jsonb_build_object('valid', true, 'permissions', permissions_value);
END;
$$;


--
-- Name: preserve_pending_account_job(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.preserve_pending_account_job() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    subject_slug TEXT;
    revoked_at TIMESTAMPTZ;
BEGIN
    subject_slug := CASE
        WHEN OLD.kind IN ('pipeline_git_write_file', 'pipeline_git_delete_file') THEN OLD.args->>'initiating_user_slug'
        WHEN OLD.kind IN ('pipeline_git_publish_proposal', 'pipeline_git_refresh_proposal') THEN OLD.args->>'member_slug'
        WHEN OLD.kind = 'pipeline_run_execution' AND OLD.args->'actor'->>'Type' = 'user' THEN OLD.args->'actor'->>'Slug'
        WHEN OLD.kind = 'email_send' THEN OLD.args->'user'->>'slug'
    END;
    SELECT deleted_at INTO revoked_at FROM users WHERE slug = subject_slug FOR SHARE;
    IF revoked_at IS NOT NULL THEN
        RETURN NULL;
    END IF;
    RETURN OLD;
END;
$$;


--
-- Name: protect_odcs_artifact(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.protect_odcs_artifact() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'ODCS artifacts are immutable' USING ERRCODE = '23514';
END;
$$;


--
-- Name: protect_odcs_catalog_identity(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.protect_odcs_catalog_identity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_TABLE_NAME = 'odcs_contract_revisions' THEN
        RAISE EXCEPTION 'ODCS revisions are immutable' USING ERRCODE = '23514';
    END IF;
    IF (NEW.id, NEW.organization_id, NEW.project_id, NEW.slug, NEW.odcs_id, NEW.created_at)
        IS DISTINCT FROM (OLD.id, OLD.organization_id, OLD.project_id, OLD.slug, OLD.odcs_id, OLD.created_at) THEN
        RAISE EXCEPTION 'ODCS contract identity is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: record_pipeline_completion_trigger_revision(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.record_pipeline_completion_trigger_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') AND OLD.trigger_type = 'completion' THEN
        UPDATE pipeline_completion_trigger_revisions SET revoked_at = CURRENT_TIMESTAMP
        WHERE project_environment_id = OLD.project_environment_id AND trigger_id = OLD.id
            AND revoked_at IS NULL;
    END IF;
    IF TG_OP <> 'DELETE' AND NEW.trigger_type = 'completion' THEN
        INSERT INTO pipeline_completion_trigger_revisions (
            organization_id, project_environment_id, trigger_id, trigger_slug, revision, configuration, revoked_at
        ) SELECT p.organization_id, NEW.project_environment_id, NEW.id, NEW.slug, NEW.revision, to_jsonb(NEW),
            CASE WHEN NEW.enabled AND (NEW.completion_config->>'execution_grant_enabled')::boolean
                THEN NULL ELSE CURRENT_TIMESTAMP END
        FROM projects p WHERE p.id = NEW.project_id;
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: record_pipeline_run_terminal_obligation(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.record_pipeline_run_terminal_obligation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE explicit_replay boolean;
BEGIN
    IF NEW.status NOT IN ('succeeded', 'failed', 'canceled') OR
        (TG_OP = 'UPDATE' AND OLD.status IN ('succeeded', 'failed', 'canceled')) THEN
        RETURN NEW;
    END IF;

    PERFORM pg_advisory_xact_lock(hashtextextended(
        NEW.project_environment_id::text || ':' || COALESCE(NULLIF(NEW.source_root_run_slug, ''), NEW.run_slug), 0));

    -- Automatic retries reuse replay_of_run_slug for the effect namespace.
    -- Their validated retry source distinguishes them from explicit replay.
    explicit_replay := NEW.replay_of_run_slug IS NOT NULL AND NOT (
        NEW.source_type IN ('schedule', 'webhook', 'completion') AND NEW.source_attempt_number > 1
        AND NEW.source_retry_of_run_slug IS NOT NULL
        AND NEW.source_retry_of_run_slug = NEW.replay_of_run_slug
        AND COALESCE(NEW.source_root_run_slug, '') <> '');

    INSERT INTO pipeline_run_terminal_obligations (
        organization_id, project_environment_id, root_run_slug, attempt_run_slug, attempt_status,
        source_type, outcome_uncertain, excluded, exclusion_reason
    ) VALUES (
        (SELECT p.organization_id FROM project_environments pe JOIN projects p ON p.id = pe.project_id WHERE pe.id = NEW.project_environment_id),
        NEW.project_environment_id,
        COALESCE(NULLIF(NEW.source_root_run_slug, ''), NEW.run_slug),
        NEW.run_slug,
        NEW.status,
        NEW.source_type,
        COALESCE((NEW.timeline #>> '{failure,outcome_uncertain}')::boolean, false),
        explicit_replay OR NEW.run_mode = 'backfill' OR NEW.status = 'canceled',
        CASE
            WHEN explicit_replay THEN 'replay'
            WHEN NEW.run_mode = 'backfill' THEN 'backfill'
            WHEN NEW.status = 'canceled' THEN 'canceled'
            ELSE NULL
        END
    ) ON CONFLICT (project_environment_id, attempt_run_slug) DO NOTHING;

    IF NOT COALESCE((NEW.timeline #>> '{failure,outcome_uncertain}')::boolean, false)
        AND NOT explicit_replay
        AND NEW.run_mode <> 'backfill'
        AND NEW.status <> 'canceled'
        AND (NEW.status = 'succeeded' OR NEW.source_type IN ('manual', 'api')) THEN
        INSERT INTO pipeline_run_logical_closures (
            organization_id, project_environment_id, root_run_slug, final_attempt_run_slug, status, decision_reason
        ) VALUES (
            (SELECT p.organization_id FROM project_environments pe JOIN projects p ON p.id = pe.project_id WHERE pe.id = NEW.project_environment_id),
            NEW.project_environment_id,
            COALESCE(NULLIF(NEW.source_root_run_slug, ''), NEW.run_slug),
            NEW.run_slug,
            NEW.status,
            CASE WHEN NEW.status = 'succeeded' THEN 'succeeded' ELSE 'no_automatic_retry' END
        ) ON CONFLICT (project_environment_id, root_run_slug) DO NOTHING;
    END IF;
    UPDATE pipeline_run_terminal_obligations obligation
    SET resolved_at = closure.closed_at
    FROM pipeline_run_logical_closures closure
    WHERE obligation.project_environment_id = NEW.project_environment_id
        AND obligation.attempt_run_slug = NEW.run_slug AND obligation.resolved_at IS NULL
        AND closure.project_environment_id = obligation.project_environment_id
        AND closure.root_run_slug = obligation.root_run_slug;
    RETURN NEW;
END;
$$;


--
-- Name: reject_completion_retry_execution_rewrite(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reject_completion_retry_execution_rewrite() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.completion_execution_args IS NOT NULL AND NEW.completion_execution_args IS DISTINCT FROM OLD.completion_execution_args THEN
        RAISE EXCEPTION 'completion retry execution arguments are immutable';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: reject_pipeline_completion_cause_rewrite(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reject_pipeline_completion_cause_rewrite() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF ROW(NEW.selected_deployment_id, NEW.ancestor_pipeline_slugs)
        IS DISTINCT FROM ROW(OLD.selected_deployment_id, OLD.ancestor_pipeline_slugs) THEN
        RAISE EXCEPTION 'completion deployment and ancestors are immutable';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: reject_pipeline_completion_execution_rewrite(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reject_pipeline_completion_execution_rewrite() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.execution_args IS NOT NULL AND NEW.execution_args IS DISTINCT FROM OLD.execution_args THEN
        RAISE EXCEPTION 'completion execution arguments are immutable';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: reject_pipeline_completion_revision_rewrite(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reject_pipeline_completion_revision_rewrite() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF ROW(NEW.organization_id, NEW.project_environment_id, NEW.trigger_id, NEW.trigger_slug, NEW.revision,
        NEW.created_at) IS DISTINCT FROM
       ROW(OLD.organization_id, OLD.project_environment_id, OLD.trigger_id, OLD.trigger_slug, OLD.revision,
        OLD.created_at) OR (NEW.configuration IS DISTINCT FROM OLD.configuration AND NOT (NEW.configuration IS NULL AND OLD.revoked_at IS NOT NULL)) OR
       (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) THEN
        RAISE EXCEPTION 'completion trigger revision is immutable and revocation is irreversible';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: reject_pipeline_completion_snapshot_rewrite(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reject_pipeline_completion_snapshot_rewrite() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF (OLD.state IN ('refused', 'completed') AND ROW(NEW.state, NEW.decision_reason, NEW.completed_at)
        IS DISTINCT FROM ROW(OLD.state, OLD.decision_reason, OLD.completed_at))
       OR (OLD.child_run_slug IS NOT NULL AND NEW.child_run_slug IS DISTINCT FROM OLD.child_run_slug)
       OR ROW(NEW.organization_id, NEW.project_environment_id, NEW.closure_id, NEW.trigger_id, NEW.trigger_revision,
           NEW.trigger_slug, NEW.root_run_slug, NEW.final_attempt_run_slug, NEW.downstream_pipeline_slug,
           NEW.selected_version_id, NEW.selected_version_hash, NEW.selected_deployment_revision,
           NEW.selected_resolution_digest, NEW.causal_depth, NEW.created_at)
       IS DISTINCT FROM
       ROW(OLD.organization_id, OLD.project_environment_id, OLD.closure_id, OLD.trigger_id, OLD.trigger_revision,
           OLD.trigger_slug, OLD.root_run_slug, OLD.final_attempt_run_slug, OLD.downstream_pipeline_slug,
           OLD.selected_version_id, OLD.selected_version_hash, OLD.selected_deployment_revision,
           OLD.selected_resolution_digest, OLD.causal_depth, OLD.created_at)
       OR ROW(NEW.selected_materialization, NEW.trigger_configuration, NEW.closure_authority)
           IS DISTINCT FROM ROW(OLD.selected_materialization, OLD.trigger_configuration, OLD.closure_authority) THEN
        RAISE EXCEPTION 'completion execution identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: reject_pipeline_run_deployment_rewrite(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reject_pipeline_run_deployment_rewrite() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF ROW(NEW.deployment_id, NEW.deployment_revision)
       IS DISTINCT FROM ROW(OLD.deployment_id, OLD.deployment_revision)
       AND NOT (OLD.pipeline_version_id IS NULL
           AND OLD.deployment_id IS NULL AND OLD.deployment_revision IS NULL
           AND NOT (OLD.timeline ? 'revision')
           AND NEW.pipeline_version_id IS NOT NULL
           AND COALESCE((NEW.timeline->>'revision')::bigint, 0) = 1
           AND OLD.status IN ('queued', 'running')) THEN
        RAISE EXCEPTION 'pipeline run deployment identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: reject_pipeline_run_terminal_rewrite(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reject_pipeline_run_terminal_rewrite() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.status IN ('succeeded', 'failed', 'canceled') AND
        (NEW.status IS DISTINCT FROM OLD.status OR NEW.run_slug IS DISTINCT FROM OLD.run_slug OR
         NEW.source_root_run_slug IS DISTINCT FROM OLD.source_root_run_slug OR
         NEW.source_attempt_number IS DISTINCT FROM OLD.source_attempt_number OR
         ROW(NEW.project_id,NEW.project_environment_id,NEW.pipeline_slug,NEW.pipeline_version_id,
             NEW.pipeline_version_spec_hash,NEW.deployment_resolution_digest,NEW.source_type,
             NEW.replay_of_run_slug,NEW.run_mode,NEW.source_retry_of_run_slug,NEW.source_webhook_delivery_id)
         IS DISTINCT FROM
         ROW(OLD.project_id,OLD.project_environment_id,OLD.pipeline_slug,OLD.pipeline_version_id,
             OLD.pipeline_version_spec_hash,OLD.deployment_resolution_digest,OLD.source_type,
             OLD.replay_of_run_slug,OLD.run_mode,OLD.source_retry_of_run_slug,OLD.source_webhook_delivery_id) OR
         (COALESCE((OLD.timeline #>> '{failure,outcome_uncertain}')::boolean,false) AND
          NOT COALESCE((NEW.timeline #>> '{failure,outcome_uncertain}')::boolean,false))) THEN
        RAISE EXCEPTION 'terminal pipeline run identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: release_pipeline_completion_capacity(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.release_pipeline_completion_capacity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE completion_id BIGINT;
BEGIN
    IF TG_TABLE_NAME = 'pipeline_run_logical_closures' THEN
        completion_id := NEW.id;
    ELSE
        completion_id := NEW.closure_id;
    END IF;
    DELETE FROM pipeline_completion_capacity capacity
    WHERE capacity.organization_id = NEW.organization_id AND capacity.closure_id = completion_id
        AND NOT EXISTS (SELECT 1 FROM pipeline_completion_snapshots snapshot
            WHERE snapshot.organization_id = capacity.organization_id AND snapshot.closure_id = capacity.closure_id
                AND snapshot.state = 'pending');
    RETURN NEW;
END;
$$;


--
-- Name: reserve_pipeline_completion_capacity(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reserve_pipeline_completion_capacity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE reserved_slot INTEGER;
        inherited_batches INTEGER;
BEGIN
    -- Match resource mutation and snapshot selection lock ordering.
    PERFORM 1 FROM project_environments WHERE id = NEW.project_environment_id FOR NO KEY UPDATE;
    IF EXISTS (SELECT 1 FROM pipeline_run_logical_closures
        WHERE project_environment_id = NEW.project_environment_id AND root_run_slug = NEW.root_run_slug) THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pipeline_triggers trigger
        JOIN pipeline_runs run ON run.project_environment_id = trigger.project_environment_id
            AND run.run_slug = NEW.final_attempt_run_slug
        WHERE trigger.project_environment_id = NEW.project_environment_id
            AND trigger.trigger_type = 'completion' AND trigger.enabled
            AND trigger.completion_config->>'upstream_pipeline_slug' = run.pipeline_slug
            AND trigger.completion_config->>'condition' = CASE NEW.status WHEN 'succeeded' THEN 'success' ELSE 'final_failure' END
    ) THEN RETURN NEW; END IF;

    -- Retained snapshots from before this migration still consume capacity.
    SELECT count(DISTINCT snapshot.closure_id) INTO inherited_batches
    FROM pipeline_completion_snapshots snapshot
    WHERE snapshot.organization_id = NEW.organization_id AND snapshot.state = 'pending'
        AND NOT EXISTS (SELECT 1 FROM pipeline_completion_capacity capacity
            WHERE capacity.organization_id = snapshot.organization_id AND capacity.closure_id = snapshot.closure_id);
    INSERT INTO pipeline_completion_capacity (organization_id, slot, closure_id)
    SELECT NEW.organization_id, candidate, NEW.id FROM generate_series(1, 100) candidate
    WHERE candidate > inherited_batches AND NOT EXISTS (SELECT 1 FROM pipeline_completion_capacity capacity
        WHERE capacity.organization_id = NEW.organization_id AND capacity.slot = candidate)
    ORDER BY candidate LIMIT 1
    ON CONFLICT DO NOTHING RETURNING slot INTO reserved_slot;
    -- A concurrent reservation may win the selected slot. Reconciliation retries
    -- the original obligation rather than blocking terminal persistence.
    IF reserved_slot IS NULL THEN
        UPDATE pipeline_run_terminal_obligations
        SET next_evaluation_at = clock_timestamp() + INTERVAL '1 minute',
            finalization_reason = 'completion_capacity_waiting'
        WHERE organization_id = NEW.organization_id AND project_environment_id = NEW.project_environment_id
            AND root_run_slug = NEW.root_run_slug AND resolved_at IS NULL;
        RETURN NULL;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: resolve_pipeline_run_terminal_obligations(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.resolve_pipeline_run_terminal_obligations() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE pipeline_run_terminal_obligations SET resolved_at = NEW.closed_at
    WHERE project_environment_id = NEW.project_environment_id AND organization_id = NEW.organization_id
        AND root_run_slug = NEW.root_run_slug AND resolved_at IS NULL;
    RETURN NEW;
END;
$$;


--
-- Name: river_job_state_in_bitmask(bit, public.river_job_state); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.river_job_state_in_bitmask(bitmask bit, state public.river_job_state) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$
    SELECT CASE state
        WHEN 'available' THEN get_bit(bitmask, 7)
        WHEN 'cancelled' THEN get_bit(bitmask, 6)
        WHEN 'completed' THEN get_bit(bitmask, 5)
        WHEN 'discarded' THEN get_bit(bitmask, 4)
        WHEN 'pending' THEN get_bit(bitmask, 3)
        WHEN 'retryable' THEN get_bit(bitmask, 2)
        WHEN 'running' THEN get_bit(bitmask, 1)
        WHEN 'scheduled' THEN get_bit(bitmask, 0)
        ELSE 0
    END = 1;
$$;


--
-- Name: snapshot_pipeline_completion_targets(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.snapshot_pipeline_completion_targets() RETURNS trigger
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
        authority_value := pipeline_completion_authority_snapshot(NEW.organization_id,
            target_trigger.updated_by_actor_type, target_trigger.updated_by_actor_slug);
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


--
-- Name: snowflake_wif_ready(text, text, text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.snowflake_wif_ready(org_slug text, ds_slug text, exact_account text, exact_issuer text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog'
    AS $$
    SELECT EXISTS (
        SELECT 1
        FROM public.snowflake_wif_bindings b
        JOIN public.organizations o ON o.id = b.organization_id
        JOIN public.data_sources ds ON ds.organization_id = b.organization_id AND ds.id = b.data_source_id
        JOIN public.credentials c ON c.organization_id = ds.organization_id AND c.id = ds.credential_id
        WHERE o.slug = org_slug AND ds.slug = ds_slug
            AND b.account = exact_account AND b.issuer = exact_issuer
            AND ds.type = 'snowflake' AND ds.config->>'account' = b.account
            AND ds.deleted_at IS NULL AND c.status = 'active'
            AND b.desired AND b.state = 'ready'
            AND b.verified_at > CURRENT_TIMESTAMP - INTERVAL '60 seconds'
    );
$$;


--
-- Name: suppress_pending_pipeline_completions(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.suppress_pending_pipeline_completions() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE pipeline_completion_snapshots SET state = 'refused',
        decision_reason = CASE WHEN TG_OP = 'DELETE' THEN 'trigger_deleted'
            WHEN NOT NEW.enabled THEN 'trigger_disabled'
            WHEN NOT (NEW.completion_config->>'execution_grant_enabled')::boolean THEN 'grant_revoked'
            ELSE 'trigger_changed' END,
        completed_at = CURRENT_TIMESTAMP
    WHERE project_environment_id = OLD.project_environment_id AND trigger_id = OLD.id AND state = 'pending';
    RETURN NULL;
END;
$$;


--
-- Name: synchronize_pipeline_completion_retry_run(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.synchronize_pipeline_completion_retry_run() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.source_type = 'completion' AND NEW.source_attempt_number > 1
        AND NEW.status IN ('running', 'succeeded', 'failed') THEN
        UPDATE pipeline_trigger_retry_attempts
        SET status = NEW.status,
            started_at = CASE WHEN NEW.status = 'running' THEN NEW.started_at ELSE started_at END,
            completed_at = CASE WHEN NEW.status IN ('succeeded', 'failed', 'canceled') THEN NEW.ended_at ELSE completed_at END,
            error_summary = NEW.error_summary, updated_at = clock_timestamp()
        WHERE project_environment_id = NEW.project_environment_id AND root_run_slug = NEW.source_root_run_slug
            AND run_slug = NEW.run_slug AND source_type = 'completion';
    END IF;
    RETURN NEW;
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: abuse_control_buckets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.abuse_control_buckets (
    scope text NOT NULL,
    subject_hash bytea NOT NULL,
    window_started_at timestamp with time zone NOT NULL,
    attempt_count bigint NOT NULL
);


--
-- Name: account_deletion_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.account_deletion_requests (
    id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    user_id integer,
    state text DEFAULT 'pending'::text NOT NULL,
    requested_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    completed_at timestamp with time zone,
    failure_code text
);


--
-- Name: account_deletion_requests_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.account_deletion_requests_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: account_deletion_requests_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.account_deletion_requests_id_seq OWNED BY public.account_deletion_requests.id;


--
-- Name: credentials; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credentials (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    kind text NOT NULL,
    credential_type text,
    status text NOT NULL,
    encrypted_payload bytea,
    rotated_at timestamp with time zone,
    revoked_at timestamp with time zone,
    failure_code text,
    failure_message text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: data_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.data_sources (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    credential_id bigint NOT NULL,
    name character varying(255) NOT NULL,
    slug character varying(100) NOT NULL,
    type text NOT NULL,
    config jsonb NOT NULL,
    is_active boolean DEFAULT false NOT NULL,
    read_only boolean DEFAULT false NOT NULL,
    deleted_at timestamp with time zone,
    delete_reason text,
    last_tested_at timestamp with time zone,
    test_error text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    schema_import_status text DEFAULT 'not_imported'::text NOT NULL,
    schema_import_error_code text,
    schema_import_error_message text,
    schema_import_attempt_id text,
    schema_import_started_at timestamp with time zone,
    schema_import_finished_at timestamp with time zone,
    connection_test_revision bigint DEFAULT 1 NOT NULL,
    organization_gitlab_connection_id bigint,
    proof_enabled boolean DEFAULT false NOT NULL,
    proof_checks_enabled boolean DEFAULT false NOT NULL
);


--
-- Name: environments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.environments (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    protected boolean DEFAULT false NOT NULL,
    deleted_at timestamp with time zone,
    delete_reason text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    color text DEFAULT 'gray'::text NOT NULL
);


--
-- Name: github_app_installations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.github_app_installations (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    installation_id bigint NOT NULL,
    account_id bigint NOT NULL,
    account_login text NOT NULL,
    account_type text NOT NULL,
    repository_selection text NOT NULL,
    suspended_at timestamp with time zone,
    deleted_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT github_app_installations_identity_valid CHECK (((installation_id > 0) AND (account_id > 0) AND (NULLIF(btrim(account_login), ''::text) IS NOT NULL) AND (NULLIF(btrim(account_type), ''::text) IS NOT NULL) AND (repository_selection = ANY (ARRAY['all'::text, 'selected'::text]))))
);


--
-- Name: github_app_repositories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.github_app_repositories (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    installation_id bigint NOT NULL,
    repository_id bigint NOT NULL,
    full_name text NOT NULL,
    owner_login text NOT NULL,
    name text NOT NULL,
    removed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT github_app_repositories_identity_valid CHECK (((repository_id > 0) AND (NULLIF(btrim(full_name), ''::text) IS NOT NULL) AND (NULLIF(btrim(owner_login), ''::text) IS NOT NULL) AND (NULLIF(btrim(name), ''::text) IS NOT NULL)))
);


--
-- Name: project_environments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_environments (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    environment_id bigint NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    deleted_at timestamp with time zone,
    delete_reason text,
    enabled_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    organization_id integer NOT NULL
);


--
-- Name: projects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.projects (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    pipeline_backend_data_source_id bigint,
    pipeline_backend_repo text,
    pipeline_backend_tracked_branch text,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL,
    pipeline_backend_pipeline_dir text,
    pipeline_backend_first_commit_sha text
);


--
-- Name: proof_change_candidate_mappings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.proof_change_candidate_mappings (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    data_source_id bigint NOT NULL,
    provider text NOT NULL,
    provider_connection_id text NOT NULL,
    repository_id text NOT NULL,
    target_ref text NOT NULL,
    pipeline_path text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    disabled_at timestamp with time zone,
    CONSTRAINT proof_candidate_mappings_identity_nonempty CHECK (((NULLIF(btrim(provider), ''::text) IS NOT NULL) AND (NULLIF(btrim(provider_connection_id), ''::text) IS NOT NULL) AND (NULLIF(btrim(repository_id), ''::text) IS NOT NULL) AND (NULLIF(btrim(target_ref), ''::text) IS NOT NULL) AND (NULLIF(btrim(pipeline_path), ''::text) IS NOT NULL)))
);


--
-- Name: active_proof_candidate_mappings; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.active_proof_candidate_mappings AS
 SELECT mapping.id,
    mapping.organization_id,
    mapping.project_id,
    mapping.project_environment_id,
    mapping.data_source_id,
    mapping.provider,
    mapping.provider_connection_id,
    mapping.repository_id,
    mapping.target_ref,
    mapping.pipeline_path,
    mapping.created_at,
    mapping.disabled_at
   FROM (((((public.proof_change_candidate_mappings mapping
     JOIN public.data_sources ds ON (((ds.organization_id = mapping.organization_id) AND (ds.id = mapping.data_source_id))))
     JOIN public.credentials credential ON (((credential.organization_id = ds.organization_id) AND (credential.id = ds.credential_id))))
     JOIN public.projects project ON (((project.organization_id = mapping.organization_id) AND (project.id = mapping.project_id))))
     JOIN public.project_environments pe ON (((pe.project_id = project.id) AND (pe.id = mapping.project_environment_id))))
     JOIN public.environments environment ON (((environment.organization_id = mapping.organization_id) AND (environment.id = pe.environment_id))))
  WHERE ((mapping.disabled_at IS NULL) AND ds.proof_enabled AND ds.is_active AND (ds.deleted_at IS NULL) AND (ds.type = mapping.provider) AND ((ds.config #>> '{repository,id}'::text[]) = mapping.repository_id) AND (credential.status = 'active'::text) AND (credential.revoked_at IS NULL) AND (pe.deleted_at IS NULL) AND (environment.deleted_at IS NULL) AND (project.pipeline_backend_data_source_id = ds.id) AND (('refs/heads/'::text || project.pipeline_backend_tracked_branch) = mapping.target_ref) AND ("left"(mapping.pipeline_path, (length(project.pipeline_backend_pipeline_dir) + 1)) = (project.pipeline_backend_pipeline_dir || '/'::text)) AND (((mapping.provider = 'github'::text) AND (EXISTS ( SELECT 1
           FROM (public.github_app_installations installation
             JOIN public.github_app_repositories repository ON (((repository.organization_id = installation.organization_id) AND (repository.installation_id = installation.id))))
          WHERE ((installation.organization_id = mapping.organization_id) AND ((installation.installation_id)::text = mapping.provider_connection_id) AND ((repository.repository_id)::text = mapping.repository_id) AND (installation.suspended_at IS NULL) AND (installation.deleted_at IS NULL) AND (repository.removed_at IS NULL))))) OR ((mapping.provider = 'gitlab'::text) AND (mapping.provider_connection_id = (ds.slug)::text) AND (credential.kind = 'oauth2_grant'::text))));


--
-- Name: alert_delivery_attempts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.alert_delivery_attempts (
    id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    notification_record_id bigint NOT NULL,
    alert_policy_id bigint,
    destination_id bigint,
    destination_type text NOT NULL,
    status text NOT NULL,
    provider_attempt integer DEFAULT 1 NOT NULL,
    job_kind text NOT NULL,
    job_queue text NOT NULL,
    job_state text NOT NULL,
    provider_response_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    error_category text,
    error_message text,
    queued_at timestamp with time zone NOT NULL,
    sent_at timestamp with time zone,
    failed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    organization_id integer NOT NULL,
    delivery_lease_until timestamp with time zone,
    retry_after_at timestamp with time zone
);


--
-- Name: alert_delivery_attempts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.alert_delivery_attempts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: alert_delivery_attempts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.alert_delivery_attempts_id_seq OWNED BY public.alert_delivery_attempts.id;


--
-- Name: alert_destinations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.alert_destinations (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    type text NOT NULL,
    non_secret_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: alert_destinations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.alert_destinations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: alert_destinations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.alert_destinations_id_seq OWNED BY public.alert_destinations.id;


--
-- Name: alert_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.alert_policies (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    scope text NOT NULL,
    project_id bigint,
    environment_id bigint,
    notification_type text,
    severity text,
    lifecycle_state text,
    source_kind text,
    source_ref text,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    source_action text,
    CONSTRAINT ck_alert_policies_environment_requires_project CHECK (((environment_id IS NULL) OR (project_id IS NOT NULL))),
    CONSTRAINT ck_alert_policies_source_ref_requires_source_kind CHECK (((source_ref IS NULL) OR (source_kind IS NOT NULL)))
);


--
-- Name: alert_policies_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.alert_policies_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: alert_policies_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.alert_policies_id_seq OWNED BY public.alert_policies.id;


--
-- Name: alert_policy_destinations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.alert_policy_destinations (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    alert_policy_id bigint NOT NULL,
    destination_id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: alert_policy_destinations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.alert_policy_destinations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: alert_policy_destinations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.alert_policy_destinations_id_seq OWNED BY public.alert_policy_destinations.id;


--
-- Name: api_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_keys (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    token_hash text NOT NULL,
    token_prefix character varying(32) NOT NULL,
    created_by_member_id integer,
    role_id integer NOT NULL,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    can_manage_api_keys boolean DEFAULT false NOT NULL
);


--
-- Name: api_keys_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.api_keys_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: api_keys_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.api_keys_id_seq OWNED BY public.api_keys.id;


--
-- Name: credential_http_oauth_apps; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_http_oauth_apps (
    organization_id integer NOT NULL,
    data_source_id bigint NOT NULL,
    encrypted_config bytea NOT NULL,
    refresh_claim_id text,
    refresh_claim_expires_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: credential_oauth_grants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_oauth_grants (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    provider text NOT NULL,
    encrypted_token_set bytea NOT NULL,
    access_token_expires_at timestamp with time zone,
    scopes text[] DEFAULT '{}'::text[] NOT NULL,
    account jsonb DEFAULT '{}'::jsonb NOT NULL,
    last_refreshed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: credential_oauth_grants_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_oauth_grants_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_oauth_grants_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_oauth_grants_id_seq OWNED BY public.credential_oauth_grants.id;


--
-- Name: credential_oauth_installs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_oauth_installs (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    data_source_id bigint,
    slug character varying(100) NOT NULL,
    provider text NOT NULL,
    state_hash text NOT NULL,
    intent text NOT NULL,
    actor_type text NOT NULL,
    actor_slug character varying(100),
    requested_scopes text[] DEFAULT '{}'::text[] NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    expected_credential_id bigint NOT NULL,
    expected_credential_status text NOT NULL,
    organization_gitlab_connection_id bigint,
    expected_source_revision bigint,
    encrypted_pkce_verifier bytea,
    notification_connection_id bigint,
    repository_setup_slug character varying(100),
    repository_setup_project_slug character varying(100),
    CONSTRAINT ck_credential_oauth_installs_owner CHECK ((num_nonnulls(data_source_id, organization_gitlab_connection_id, notification_connection_id) = 1))
);


--
-- Name: credential_oauth_installs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_oauth_installs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_oauth_installs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_oauth_installs_id_seq OWNED BY public.credential_oauth_installs.id;


--
-- Name: credentials_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credentials_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credentials_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credentials_id_seq OWNED BY public.credentials.id;


--
-- Name: data_source_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.data_source_bindings (
    id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    data_source_ref_id bigint NOT NULL,
    data_source_id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    pipeline_spec_id bigint NOT NULL
);


--
-- Name: data_source_bindings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.data_source_bindings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: data_source_bindings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.data_source_bindings_id_seq OWNED BY public.data_source_bindings.id;


--
-- Name: data_source_refs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.data_source_refs (
    id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    data_source_type text NOT NULL,
    required_capabilities text[] NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    pipeline_spec_id bigint NOT NULL
);


--
-- Name: data_source_refs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.data_source_refs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: data_source_refs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.data_source_refs_id_seq OWNED BY public.data_source_refs.id;


--
-- Name: data_sources_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.data_sources_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: data_sources_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.data_sources_id_seq OWNED BY public.data_sources.id;


--
-- Name: drift_impact_reports; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.drift_impact_reports (
    id bigint NOT NULL,
    pipeline_spec_id bigint,
    data_source_id bigint,
    slug character varying(120) NOT NULL,
    report_type text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    severity text DEFAULT 'safe'::text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    changes jsonb DEFAULT '[]'::jsonb NOT NULL,
    impacts jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    superseded_at timestamp with time zone,
    CONSTRAINT ck_drift_impact_reports_single_owner CHECK ((((pipeline_spec_id IS NOT NULL) AND (data_source_id IS NULL)) OR ((pipeline_spec_id IS NULL) AND (data_source_id IS NOT NULL))))
);


--
-- Name: drift_impact_reports_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.drift_impact_reports_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: drift_impact_reports_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.drift_impact_reports_id_seq OWNED BY public.drift_impact_reports.id;


--
-- Name: environment_runtime_variables; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.environment_runtime_variables (
    id bigint NOT NULL,
    organization_id bigint NOT NULL,
    environment_id bigint NOT NULL,
    key character varying(100) NOT NULL,
    value_type text NOT NULL,
    value_text text NOT NULL,
    description text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: environment_runtime_variables_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.environment_runtime_variables_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: environment_runtime_variables_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.environment_runtime_variables_id_seq OWNED BY public.environment_runtime_variables.id;


--
-- Name: environment_secrets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.environment_secrets (
    id bigint NOT NULL,
    environment_id bigint NOT NULL,
    credential_id bigint NOT NULL,
    key character varying(100) NOT NULL,
    description text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    organization_id integer NOT NULL
);


--
-- Name: environment_secrets_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.environment_secrets_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: environment_secrets_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.environment_secrets_id_seq OWNED BY public.environment_secrets.id;


--
-- Name: environments_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.environments_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: environments_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.environments_id_seq OWNED BY public.environments.id;


--
-- Name: events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.events (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    actor_type text NOT NULL,
    actor_slug character varying(100),
    entity_type text NOT NULL,
    entity_slug character varying(100),
    action text NOT NULL,
    occurred_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    event_class text DEFAULT 'activity'::text NOT NULL,
    request_id text,
    schema_version integer DEFAULT 1 NOT NULL
);


--
-- Name: events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.events_id_seq OWNED BY public.events.id;


--
-- Name: execution_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.execution_requests (
    id uuid NOT NULL,
    organization_id integer NOT NULL,
    kind text NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    request_ciphertext bytea NOT NULL,
    result_ciphertext bytea,
    deadline_at timestamp with time zone NOT NULL,
    cancel_requested_at timestamp with time zone,
    lease_expires_at timestamp with time zone,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    data_source_id bigint,
    concurrency_limit integer,
    queue_capacity integer,
    queue_deadline_at timestamp with time zone,
    initiating_user_id integer,
    scope_data_source_id bigint,
    claim_token uuid DEFAULT gen_random_uuid() NOT NULL
);


--
-- Name: github_app_install_intents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.github_app_install_intents (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    actor_slug text NOT NULL,
    state_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    repository_setup_slug character varying(100),
    requested_account_login text,
    CONSTRAINT github_app_install_intents_identity_nonempty CHECK (((NULLIF(btrim(actor_slug), ''::text) IS NOT NULL) AND (NULLIF(btrim(state_hash), ''::text) IS NOT NULL)))
);


--
-- Name: github_app_install_intents_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.github_app_install_intents_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: github_app_install_intents_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.github_app_install_intents_id_seq OWNED BY public.github_app_install_intents.id;


--
-- Name: github_app_installations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.github_app_installations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: github_app_installations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.github_app_installations_id_seq OWNED BY public.github_app_installations.id;


--
-- Name: github_app_repositories_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.github_app_repositories_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: github_app_repositories_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.github_app_repositories_id_seq OWNED BY public.github_app_repositories.id;


--
-- Name: gitlab_proof_webhooks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.gitlab_proof_webhooks (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    data_source_id bigint NOT NULL,
    project_id bigint NOT NULL,
    hook_id bigint NOT NULL,
    status text NOT NULL,
    route_key_hash text NOT NULL,
    encrypted_signing_token bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT gitlab_proof_webhooks_identity_valid CHECK (((project_id > 0) AND (hook_id >= 0) AND (status = ANY (ARRAY['pending'::text, 'active'::text, 'failed'::text])) AND (NULLIF(btrim(route_key_hash), ''::text) IS NOT NULL)))
);


--
-- Name: gitlab_proof_webhooks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.gitlab_proof_webhooks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: gitlab_proof_webhooks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.gitlab_proof_webhooks_id_seq OWNED BY public.gitlab_proof_webhooks.id;


--
-- Name: notification_connections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_connections (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(255) NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    credential_id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: notification_connections_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.notification_connections_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: notification_connections_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.notification_connections_id_seq OWNED BY public.notification_connections.id;


--
-- Name: notification_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_records (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    project_id bigint,
    environment_id bigint,
    notification_type text NOT NULL,
    severity text NOT NULL,
    lifecycle_state text NOT NULL,
    dedupe_fingerprint text NOT NULL,
    source_event_slug character varying(100),
    source_entity_type text NOT NULL,
    source_action text NOT NULL,
    source_refs jsonb DEFAULT '{}'::jsonb NOT NULL,
    safe_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    first_seen_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    last_seen_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    occurrence_count integer DEFAULT 1 NOT NULL,
    last_occurrence_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    dispatch_summary jsonb DEFAULT '{}'::jsonb NOT NULL,
    acknowledged_at timestamp with time zone,
    resolved_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT ck_notification_records_environment_requires_project CHECK (((environment_id IS NULL) OR (project_id IS NOT NULL)))
);


--
-- Name: notification_records_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.notification_records_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: notification_records_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.notification_records_id_seq OWNED BY public.notification_records.id;


--
-- Name: odcs_artifacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.odcs_artifacts (
    organization_id integer NOT NULL,
    project_id integer NOT NULL,
    contract_id integer NOT NULL,
    revision_slug character varying(255) NOT NULL,
    source_repo text NOT NULL,
    source_commit text NOT NULL,
    source_path text NOT NULL,
    format text NOT NULL,
    artifact_digest text NOT NULL,
    original bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: odcs_contract_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.odcs_contract_revisions (
    id integer NOT NULL,
    organization_id integer NOT NULL,
    project_id integer NOT NULL,
    contract_id integer NOT NULL,
    slug character varying(255) NOT NULL,
    digest text NOT NULL,
    odcs_version text NOT NULL,
    document_version text NOT NULL,
    declared_status text NOT NULL,
    status text DEFAULT 'recorded'::text NOT NULL,
    document bytea NOT NULL,
    schema_objects jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT odcs_contract_revisions_status_check CHECK ((status = 'recorded'::text))
);


--
-- Name: odcs_contract_revisions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.odcs_contract_revisions_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: odcs_contract_revisions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.odcs_contract_revisions_id_seq OWNED BY public.odcs_contract_revisions.id;


--
-- Name: odcs_contracts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.odcs_contracts (
    id integer NOT NULL,
    organization_id integer NOT NULL,
    project_id integer NOT NULL,
    slug character varying(255) NOT NULL,
    name character varying(255) NOT NULL,
    odcs_id character varying(255) NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT odcs_contracts_status_check CHECK ((status = ANY (ARRAY['active'::text, 'archived'::text])))
);


--
-- Name: odcs_contracts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.odcs_contracts_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: odcs_contracts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.odcs_contracts_id_seq OWNED BY public.odcs_contracts.id;




--
-- Name: organization_gitlab_connections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.organization_gitlab_connections (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    credential_id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: organization_gitlab_connections_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.organization_gitlab_connections_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: organization_gitlab_connections_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.organization_gitlab_connections_id_seq OWNED BY public.organization_gitlab_connections.id;


--
-- Name: organization_member_invitations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.organization_member_invitations (
    id integer NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    email character varying(255) NOT NULL,
    role_id integer NOT NULL,
    invited_by_user_id integer,
    accepted_by_user_id integer,
    accepted_member_id integer,
    token_hash text NOT NULL,
    status character varying(32) DEFAULT 'pending'::character varying NOT NULL,
    expires_at timestamp without time zone NOT NULL,
    last_sent_at timestamp without time zone DEFAULT now() NOT NULL,
    accepted_at timestamp without time zone,
    declined_at timestamp without time zone,
    revoked_at timestamp without time zone,
    expired_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: organization_member_invitations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.organization_member_invitations_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: organization_member_invitations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.organization_member_invitations_id_seq OWNED BY public.organization_member_invitations.id;


--
-- Name: organization_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.organization_members (
    id integer NOT NULL,
    organization_id integer NOT NULL,
    user_id integer NOT NULL,
    role_id integer NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: organization_members_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.organization_members_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: organization_members_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.organization_members_id_seq OWNED BY public.organization_members.id;


--
-- Name: organization_retention_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.organization_retention_runs (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    source_job_id bigint NOT NULL,
    attempt integer NOT NULL,
    status text NOT NULL,
    policy_snapshot jsonb,
    query_executions_deleted bigint DEFAULT 0 NOT NULL,
    replay_checkpoints_deleted bigint DEFAULT 0 NOT NULL,
    pipeline_runs_deleted bigint DEFAULT 0 NOT NULL,
    events_deleted bigint DEFAULT 0 NOT NULL,
    failure_code text,
    started_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT organization_retention_runs_attempt_positive CHECK ((attempt >= 1)),
    CONSTRAINT organization_retention_runs_counts_nonnegative CHECK (((query_executions_deleted >= 0) AND (replay_checkpoints_deleted >= 0) AND (pipeline_runs_deleted >= 0) AND (events_deleted >= 0))),
    CONSTRAINT organization_retention_runs_status_failure_consistent CHECK ((((status = 'succeeded'::text) AND (failure_code IS NULL) AND (policy_snapshot IS NOT NULL)) OR ((status = 'failed'::text) AND (failure_code = ANY (ARRAY['invalid_policy'::text, 'maintenance_failed'::text])) AND (query_executions_deleted = 0) AND (replay_checkpoints_deleted = 0) AND (pipeline_runs_deleted = 0) AND (events_deleted = 0))))
);


--
-- Name: organization_retention_runs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.organization_retention_runs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: organization_retention_runs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.organization_retention_runs_id_seq OWNED BY public.organization_retention_runs.id;


--
-- Name: organizations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.organizations (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    slug character varying(100) NOT NULL,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: organizations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.organizations_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: organizations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.organizations_id_seq OWNED BY public.organizations.id;


--
-- Name: permissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.permissions (
    id integer NOT NULL,
    resource character varying(100) NOT NULL,
    action character varying(100) NOT NULL
);


--
-- Name: permissions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.permissions_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: permissions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.permissions_id_seq OWNED BY public.permissions.id;


--
-- Name: personal_access_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.personal_access_tokens (
    id bigint NOT NULL,
    user_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    token_hash text NOT NULL,
    token_prefix character varying(32) NOT NULL,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    can_manage_personal_access_tokens boolean DEFAULT false CONSTRAINT personal_access_tokens_can_manage_personal_access_toke_not_null NOT NULL
);


--
-- Name: personal_access_tokens_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.personal_access_tokens_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: personal_access_tokens_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.personal_access_tokens_id_seq OWNED BY public.personal_access_tokens.id;


--
-- Name: pipeline_checkpoint; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_checkpoint (
    id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    run_slug character varying(100) NOT NULL,
    step_slug character varying(100) NOT NULL,
    pipeline_spec jsonb NOT NULL,
    step_results jsonb NOT NULL,
    incremental_source_windows jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    compiled_plan bytea,
    compiled_plan_digest text,
    deployment_plan bytea,
    deployment_resolution_digest text
);


--
-- Name: pipeline_checkpoint_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_checkpoint_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_checkpoint_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_checkpoint_id_seq OWNED BY public.pipeline_checkpoint.id;


--
-- Name: pipeline_compiled_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_compiled_plans (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    digest text NOT NULL,
    authored_spec_hash text NOT NULL,
    format_version integer NOT NULL,
    materialization bytea NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_compiled_plans_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_compiled_plans_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_compiled_plans_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_compiled_plans_id_seq OWNED BY public.pipeline_compiled_plans.id;


--
-- Name: pipeline_completion_capacity; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_completion_capacity (
    organization_id bigint NOT NULL,
    slot integer NOT NULL,
    closure_id bigint NOT NULL,
    CONSTRAINT pipeline_completion_capacity_slot_check CHECK (((slot >= 1) AND (slot <= 100)))
);


--
-- Name: pipeline_completion_snapshots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_completion_snapshots (
    id bigint NOT NULL,
    organization_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    closure_id bigint NOT NULL,
    trigger_id bigint NOT NULL,
    trigger_revision bigint NOT NULL,
    trigger_slug character varying(100) NOT NULL,
    root_run_slug character varying(100) NOT NULL,
    final_attempt_run_slug character varying(100) NOT NULL,
    downstream_pipeline_slug character varying(100) NOT NULL,
    selected_version_id bigint,
    selected_version_hash text,
    selected_deployment_revision bigint,
    selected_resolution_digest text,
    selected_materialization bytea,
    trigger_configuration jsonb,
    closure_authority jsonb,
    causal_depth integer NOT NULL,
    state text NOT NULL,
    decision_reason text,
    child_run_slug character varying(100),
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    completed_at timestamp with time zone,
    execution_args jsonb,
    next_evaluation_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    selected_deployment_id bigint,
    ancestor_pipeline_slugs text[] DEFAULT '{}'::text[] NOT NULL
);


--
-- Name: pipeline_completion_snapshots_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_completion_snapshots_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_completion_snapshots_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_completion_snapshots_id_seq OWNED BY public.pipeline_completion_snapshots.id;


--
-- Name: pipeline_completion_tombstones; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_completion_tombstones (
    organization_id bigint NOT NULL,
    closure_id bigint NOT NULL,
    trigger_id bigint NOT NULL,
    trigger_revision bigint NOT NULL
);


--
-- Name: pipeline_completion_trigger_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_completion_trigger_revisions (
    organization_id bigint NOT NULL,
    project_environment_id bigint CONSTRAINT pipeline_completion_trigger_rev_project_environment_id_not_null NOT NULL,
    trigger_id bigint NOT NULL,
    trigger_slug character varying(100) NOT NULL,
    revision bigint NOT NULL,
    configuration jsonb,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_creation_proposals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_creation_proposals (
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    member_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    request jsonb NOT NULL,
    result jsonb,
    status text DEFAULT 'publish_queued'::text NOT NULL,
    failure_code text DEFAULT ''::text NOT NULL,
    publish_attempt_id text NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL
);


--
-- Name: pipeline_deployment_resolutions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_deployment_resolutions (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    compiled_digest text NOT NULL,
    digest text NOT NULL,
    materialization bytea NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_deployment_resolutions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_deployment_resolutions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_deployment_resolutions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_deployment_resolutions_id_seq OWNED BY public.pipeline_deployment_resolutions.id;


--
-- Name: pipeline_deployments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_deployments (
    id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    current_version_id bigint NOT NULL,
    previous_version_id bigint,
    deployed_by_actor_type text NOT NULL,
    deployed_by_actor_slug character varying(100),
    deployed_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    project_id bigint NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    current_resolution_digest text,
    previous_resolution_digest text
);


--
-- Name: pipeline_deployments_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_deployments_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_deployments_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_deployments_id_seq OWNED BY public.pipeline_deployments.id;


--
-- Name: pipeline_drafts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_drafts (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    member_id integer NOT NULL,
    based_on_version_id bigint,
    spec_jsonb jsonb NOT NULL,
    draft_state text,
    base_ref text,
    base_commit_sha text,
    branch_name text,
    branch_head_sha text,
    proposal_provider text,
    proposal_id text,
    proposal_url text,
    proposal_state text,
    last_proposed_draft_hash text,
    queued_at timestamp with time zone,
    proposal_updated_at timestamp with time zone,
    failed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    publish_attempt_id text,
    refresh_attempt_id text,
    failure_code text,
    failure_message text,
    proposal_authority_id bigint,
    publication_mode text,
    provider_host text,
    provider_account_id text,
    provider_repository_id text,
    provider_credential_id text,
    executing_principal_id text,
    executing_principal_display text,
    authority_source text,
    organization_id integer NOT NULL
);


--
-- Name: pipeline_drafts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_drafts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_drafts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_drafts_id_seq OWNED BY public.pipeline_drafts.id;


--
-- Name: pipeline_incremental_checkpoint_locks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_incremental_checkpoint_locks (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint CONSTRAINT pipeline_incremental_checkpoin_project_environment_id_not_null1 NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    source_step_slug character varying(100) NOT NULL,
    checkpoint_namespace text DEFAULT 'normal'::text CONSTRAINT pipeline_incremental_checkpoint_l_checkpoint_namespace_not_null NOT NULL,
    operation_identity text CONSTRAINT pipeline_incremental_checkpoint_loc_operation_identity_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_incremental_checkpoint_locks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_incremental_checkpoint_locks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_incremental_checkpoint_locks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_incremental_checkpoint_locks_id_seq OWNED BY public.pipeline_incremental_checkpoint_locks.id;


--
-- Name: pipeline_incremental_checkpoints; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_incremental_checkpoints (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint CONSTRAINT pipeline_incremental_checkpoint_project_environment_id_not_null NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    pipeline_version_id bigint NOT NULL,
    source_step_slug character varying(100) NOT NULL,
    checkpoint_namespace text DEFAULT 'normal'::text NOT NULL,
    checkpoint_kind text NOT NULL,
    data_source_slug character varying(100) NOT NULL,
    operation_type text NOT NULL,
    operation_ref character varying(100),
    operation_inline_hash text,
    operation_identity text NOT NULL,
    observed_lower_bound jsonb,
    observed_upper_bound jsonb,
    display_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    commit_status text NOT NULL,
    observed_run_slug character varying(100) NOT NULL,
    committing_run_slug character varying(100),
    blocked_reason text,
    observed_at timestamp with time zone NOT NULL,
    committed_at timestamp with time zone,
    superseded_at timestamp with time zone,
    blocked_at timestamp with time zone,
    expired_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_incremental_checkpoints_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_incremental_checkpoints_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_incremental_checkpoints_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_incremental_checkpoints_id_seq OWNED BY public.pipeline_incremental_checkpoints.id;


--
-- Name: pipeline_proposal_authorities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_proposal_authorities (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    data_source_id bigint NOT NULL,
    credential_id bigint NOT NULL,
    publication_mode text NOT NULL,
    provider_host text NOT NULL,
    provider_account_id text NOT NULL,
    provider_repository_id text NOT NULL,
    provider_credential_id text NOT NULL,
    executing_principal_id text NOT NULL,
    executing_principal_display text CONSTRAINT pipeline_proposal_authoriti_executing_principal_displa_not_null NOT NULL,
    authority_source text NOT NULL,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_proposal_authorities_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_proposal_authorities_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_proposal_authorities_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_proposal_authorities_id_seq OWNED BY public.pipeline_proposal_authorities.id;


--
-- Name: pipeline_references; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_references (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) DEFAULT ''::character varying NOT NULL,
    current_version_id bigint,
    has_draft boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_references_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_references_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_references_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_references_id_seq OWNED BY public.pipeline_references.id;


--
-- Name: pipeline_run_logical_closures; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_run_logical_closures (
    id bigint NOT NULL,
    organization_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    root_run_slug character varying(100) NOT NULL,
    final_attempt_run_slug character varying(100) NOT NULL,
    status text NOT NULL,
    decision_reason text NOT NULL,
    closed_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_run_logical_closures_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_run_logical_closures_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_run_logical_closures_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_run_logical_closures_id_seq OWNED BY public.pipeline_run_logical_closures.id;


--
-- Name: pipeline_run_recency_episodes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_run_recency_episodes (
    id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    expectation_id bigint NOT NULL,
    deployment_id bigint NOT NULL,
    deployment_revision bigint NOT NULL,
    pipeline_version_id bigint NOT NULL,
    deployment_resolution_digest text,
    deployed_at timestamp with time zone NOT NULL,
    deadline_at timestamp with time zone NOT NULL,
    breached_at timestamp with time zone NOT NULL,
    last_success_run_slug character varying(100),
    last_success_at timestamp with time zone,
    resolved_at timestamp with time zone,
    resolution_reason text,
    recovery_run_slug character varying(100),
    recovery_succeeded_at timestamp with time zone
);


--
-- Name: pipeline_run_recency_episodes_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_run_recency_episodes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_run_recency_episodes_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_run_recency_episodes_id_seq OWNED BY public.pipeline_run_recency_episodes.id;


--
-- Name: pipeline_run_recency_expectations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_run_recency_expectations (
    id bigint NOT NULL,
    project_environment_id bigint CONSTRAINT pipeline_run_recency_expectatio_project_environment_id_not_null NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    revision bigint NOT NULL,
    maximum_age_seconds bigint NOT NULL,
    enabled boolean NOT NULL,
    enabled_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT pipeline_run_recency_expectations_maximum_age_seconds_check CHECK ((maximum_age_seconds > 0))
);


--
-- Name: pipeline_run_recency_expectations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_run_recency_expectations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_run_recency_expectations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_run_recency_expectations_id_seq OWNED BY public.pipeline_run_recency_expectations.id;


--
-- Name: pipeline_run_recency_successes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_run_recency_successes (
    project_environment_id bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    deployment_id bigint NOT NULL,
    deployment_revision bigint NOT NULL,
    pipeline_version_id bigint NOT NULL,
    deployment_resolution_digest text,
    run_slug character varying(100) NOT NULL,
    succeeded_at timestamp with time zone NOT NULL
);


--
-- Name: pipeline_run_terminal_obligations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_run_terminal_obligations (
    id bigint NOT NULL,
    organization_id bigint NOT NULL,
    project_environment_id bigint CONSTRAINT pipeline_run_terminal_obligatio_project_environment_id_not_null NOT NULL,
    root_run_slug character varying(100) NOT NULL,
    attempt_run_slug character varying(100) NOT NULL,
    attempt_status text NOT NULL,
    source_type text NOT NULL,
    outcome_uncertain boolean NOT NULL,
    excluded boolean NOT NULL,
    exclusion_reason text,
    resolved_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    next_evaluation_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    finalization_reason text
);


--
-- Name: pipeline_run_terminal_obligations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_run_terminal_obligations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_run_terminal_obligations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_run_terminal_obligations_id_seq OWNED BY public.pipeline_run_terminal_obligations.id;


--
-- Name: pipeline_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_runs (
    id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    pipeline_version_id bigint,
    pipeline_version_spec_hash text,
    api_version text NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    run_slug character varying(100) NOT NULL,
    request_id text,
    source_type text DEFAULT 'manual'::text NOT NULL,
    source_summary text DEFAULT 'Manual run'::text NOT NULL,
    source_entrypoint text DEFAULT 'ui'::text NOT NULL,
    source_actor_type text DEFAULT 'unknown'::text NOT NULL,
    source_actor_slug character varying(100),
    source_request_id text,
    source_trace_id text,
    source_environment_slug character varying(100) NOT NULL,
    source_parameter_keys text[] DEFAULT '{}'::text[] NOT NULL,
    status text NOT NULL,
    duration_ms bigint NOT NULL,
    step_count integer DEFAULT 0 NOT NULL,
    step_slug character varying(100) NOT NULL,
    error_summary text,
    failure_kind text,
    failure_side text,
    started_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    ended_at timestamp with time zone,
    replay_of_run_slug character varying(100),
    replay_from_step character varying(100),
    source_event_slug character varying(100),
    source_attempt_number integer DEFAULT 1 NOT NULL,
    source_root_run_slug character varying(100),
    source_retry_of_run_slug character varying(100),
    source_schedule_trigger_slug character varying(100),
    source_schedule_trigger_revision bigint,
    source_schedule_cron text,
    source_schedule_timezone text,
    source_schedule_scheduled_fire_at timestamp with time zone,
    source_schedule_actual_fire_at timestamp with time zone,
    source_schedule_misfire_result text,
    run_mode text DEFAULT 'full'::text NOT NULL,
    checkpoint_status text DEFAULT 'not_applicable'::text NOT NULL,
    incremental_source_step_slug character varying(100),
    incremental_blocked_reason text,
    replay_safety_status text,
    replay_blocked_reason text,
    backfill_series_slug character varying(100),
    backfill_chunk_index integer DEFAULT 0 NOT NULL,
    backfill_chunk_count integer DEFAULT 0 NOT NULL,
    incremental_diagnostics jsonb DEFAULT '{}'::jsonb NOT NULL,
    timeline jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    source_webhook_delivery_id bigint,
    project_id bigint NOT NULL,
    deployment_resolution_digest text,
    deployment_id bigint,
    deployment_revision bigint,
    worker_job_id bigint,
    CONSTRAINT pipeline_runs_admitted_version CHECK ((((pipeline_version_id IS NULL) = (pipeline_version_spec_hash IS NULL)) AND ((pipeline_version_id IS NOT NULL) OR ((status <> 'succeeded'::text) AND (NOT (timeline ? 'revision'::text))))))
);


--
-- Name: pipeline_runs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_runs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_runs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_runs_id_seq OWNED BY public.pipeline_runs.id;


--
-- Name: pipeline_schedule_fires; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_schedule_fires (
    id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    pipeline_trigger_id bigint NOT NULL,
    fire_slug character varying(100) NOT NULL,
    trigger_revision bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    version_policy text NOT NULL,
    pinned_version_hash text,
    runtime_input_keys text[] DEFAULT '{}'::text[] NOT NULL,
    retry_enabled boolean DEFAULT false NOT NULL,
    retry_max_attempts integer DEFAULT 1 NOT NULL,
    retry_backoff_strategy text DEFAULT 'none'::text NOT NULL,
    retry_initial_delay_seconds integer DEFAULT 0 NOT NULL,
    retry_max_delay_seconds integer DEFAULT 0 NOT NULL,
    retry_jitter boolean DEFAULT false NOT NULL,
    retryable_failure_kinds text[] DEFAULT '{}'::text[] NOT NULL,
    retry_max_elapsed_seconds integer,
    schedule_cron text NOT NULL,
    schedule_timezone text NOT NULL,
    schedule_misfire_policy text NOT NULL,
    scheduled_fire_at timestamp with time zone NOT NULL,
    actual_fire_at timestamp with time zone NOT NULL,
    misfire_result text NOT NULL,
    status text DEFAULT 'claimed'::text NOT NULL,
    run_slug character varying(100),
    source_event_slug character varying(100),
    error_summary text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_schedule_fires_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_schedule_fires_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_schedule_fires_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_schedule_fires_id_seq OWNED BY public.pipeline_schedule_fires.id;


--
-- Name: pipeline_sink_effects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_sink_effects (
    id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    run_slug character varying(100) NOT NULL,
    sink_step_slug character varying(100) NOT NULL,
    idempotency_key text NOT NULL,
    idempotency_class text NOT NULL,
    replay_method text NOT NULL,
    replay_decision text NOT NULL,
    state text DEFAULT 'in_progress'::text NOT NULL,
    diagnostic_code text,
    diagnostic_summary text,
    started_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    succeeded_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: pipeline_sink_effects_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.pipeline_sink_effects ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.pipeline_sink_effects_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: pipeline_specs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_specs (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) DEFAULT ''::character varying NOT NULL,
    current_version_id bigint,
    spec_jsonb jsonb NOT NULL,
    spec_hash text DEFAULT ''::text NOT NULL,
    sync_status text DEFAULT 'synced'::text NOT NULL,
    source_path text NOT NULL,
    source_ref text NOT NULL,
    last_applied_commit text NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by integer,
    delete_reason text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT pipeline_specs_git_provenance_nonempty CHECK (((NULLIF(btrim(source_path), ''::text) IS NOT NULL) AND (NULLIF(btrim(source_ref), ''::text) IS NOT NULL) AND (NULLIF(btrim(last_applied_commit), ''::text) IS NOT NULL)))
);


--
-- Name: pipeline_specs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_specs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_specs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_specs_id_seq OWNED BY public.pipeline_specs.id;


--
-- Name: pipeline_trigger_retry_attempts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_trigger_retry_attempts (
    id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    pipeline_trigger_id bigint,
    attempt_slug character varying(100) NOT NULL,
    status text NOT NULL,
    decision_reason text,
    trigger_revision bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    version_policy text NOT NULL,
    pinned_version_hash text,
    pipeline_version_spec_hash text CONSTRAINT pipeline_trigger_retry_atte_pipeline_version_spec_hash_not_null NOT NULL,
    runtime_input_keys text[] DEFAULT '{}'::text[] NOT NULL,
    retry_enabled boolean DEFAULT false NOT NULL,
    retry_max_attempts integer DEFAULT 1 NOT NULL,
    retry_backoff_strategy text DEFAULT 'none'::text NOT NULL,
    retry_initial_delay_seconds integer DEFAULT 0 CONSTRAINT pipeline_trigger_retry_atte_retry_initial_delay_second_not_null NOT NULL,
    retry_max_delay_seconds integer DEFAULT 0 CONSTRAINT pipeline_trigger_retry_attempt_retry_max_delay_seconds_not_null NOT NULL,
    retry_jitter boolean DEFAULT false NOT NULL,
    retryable_failure_kinds text[] DEFAULT '{}'::text[] CONSTRAINT pipeline_trigger_retry_attempt_retryable_failure_kinds_not_null NOT NULL,
    retry_max_elapsed_seconds integer,
    source_type text NOT NULL,
    source_summary text NOT NULL,
    source_entrypoint text NOT NULL,
    source_actor_type text NOT NULL,
    source_actor_slug character varying(100),
    source_request_id text,
    source_trace_id text,
    source_event_slug character varying(100),
    source_parameter_keys text[] DEFAULT '{}'::text[] NOT NULL,
    source_schedule_trigger_slug character varying(100),
    source_schedule_trigger_revision bigint,
    source_schedule_cron text,
    source_schedule_timezone text,
    source_schedule_scheduled_fire_at timestamp with time zone,
    source_schedule_actual_fire_at timestamp with time zone,
    source_schedule_misfire_result text,
    root_run_slug character varying(100) NOT NULL,
    retry_of_run_slug character varying(100) NOT NULL,
    run_slug character varying(100) NOT NULL,
    attempt_number integer NOT NULL,
    failure_kind text,
    scheduled_at timestamp with time zone NOT NULL,
    enqueued_at timestamp with time zone,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    error_summary text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    completion_execution_args jsonb,
    completion_trigger_slug text,
    CONSTRAINT pipeline_retry_completion_owner CHECK ((((source_type = 'completion'::text) AND (pipeline_trigger_id IS NULL) AND (completion_trigger_slug IS NOT NULL)) OR ((source_type <> 'completion'::text) AND (pipeline_trigger_id IS NOT NULL) AND (completion_trigger_slug IS NULL))))
);


--
-- Name: pipeline_trigger_retry_attempts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_trigger_retry_attempts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_trigger_retry_attempts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_trigger_retry_attempts_id_seq OWNED BY public.pipeline_trigger_retry_attempts.id;


--
-- Name: pipeline_triggers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_triggers (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    pipeline_id bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    pinned_pipeline_version_id bigint,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    trigger_type text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    version_policy text NOT NULL,
    runtime_input_keys text[] DEFAULT '{}'::text[] NOT NULL,
    retry_enabled boolean DEFAULT false NOT NULL,
    retry_max_attempts integer DEFAULT 1 NOT NULL,
    retry_backoff_strategy text DEFAULT 'none'::text NOT NULL,
    retry_initial_delay_seconds integer DEFAULT 0 NOT NULL,
    retry_max_delay_seconds integer DEFAULT 0 NOT NULL,
    retry_jitter boolean DEFAULT false NOT NULL,
    retryable_failure_kinds text[] DEFAULT '{}'::text[] NOT NULL,
    retry_max_elapsed_seconds integer,
    schedule_cron text,
    schedule_timezone text,
    schedule_misfire_policy text,
    created_by_actor_type text NOT NULL,
    created_by_actor_slug character varying(100),
    updated_by_actor_type text NOT NULL,
    updated_by_actor_slug character varying(100),
    disabled_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    receiver_id character varying(128),
    webhook_verification_strategy text,
    webhook_input_mappings jsonb,
    completion_config jsonb,
    CONSTRAINT chk_pipeline_triggers_type_fields CHECK ((((trigger_type = 'schedule'::text) AND (schedule_cron IS NOT NULL) AND (schedule_timezone IS NOT NULL) AND (schedule_misfire_policy IS NOT NULL) AND (receiver_id IS NULL) AND (webhook_verification_strategy IS NULL) AND (webhook_input_mappings IS NULL) AND (completion_config IS NULL)) OR ((trigger_type = 'webhook'::text) AND (schedule_cron IS NULL) AND (schedule_timezone IS NULL) AND (schedule_misfire_policy IS NULL) AND (receiver_id IS NOT NULL) AND (webhook_verification_strategy IS NOT NULL) AND (webhook_input_mappings IS NOT NULL) AND (completion_config IS NULL)) OR ((trigger_type = 'completion'::text) AND (schedule_cron IS NULL) AND (schedule_timezone IS NULL) AND (schedule_misfire_policy IS NULL) AND (receiver_id IS NULL) AND (webhook_verification_strategy IS NULL) AND (webhook_input_mappings IS NULL) AND (completion_config IS NOT NULL) AND (completion_config ?& ARRAY['upstream_pipeline_slug'::text, 'condition'::text, 'execution_grant_enabled'::text]) AND ((completion_config ->> 'condition'::text) = ANY (ARRAY['success'::text, 'final_failure'::text])) AND (jsonb_typeof((completion_config -> 'execution_grant_enabled'::text)) = 'boolean'::text))))
);


--
-- Name: pipeline_triggers_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_triggers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_triggers_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_triggers_id_seq OWNED BY public.pipeline_triggers.id;


--
-- Name: pipeline_validation_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_validation_results (
    id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    pipeline_id bigint,
    pipeline_version_id bigint,
    validation_slug character varying(120) NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    source text NOT NULL,
    valid boolean NOT NULL,
    spec_hash text,
    diagnostics jsonb DEFAULT '[]'::jsonb NOT NULL,
    impact_report_slug character varying(120),
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    project_id bigint NOT NULL,
    impact_pipeline_id bigint
);


--
-- Name: pipeline_validation_results_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_validation_results_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_validation_results_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_validation_results_id_seq OWNED BY public.pipeline_validation_results.id;


--
-- Name: pipeline_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_versions (
    id bigint NOT NULL,
    project_id bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    git_commit_sha text,
    spec_jsonb jsonb DEFAULT '{}'::jsonb NOT NULL,
    spec_hash text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    resolved_contracts jsonb DEFAULT '[]'::jsonb NOT NULL,
    source_path text
);


--
-- Name: pipeline_versions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_versions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_versions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_versions_id_seq OWNED BY public.pipeline_versions.id;


--
-- Name: pipeline_webhook_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_webhook_deliveries (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    pipeline_trigger_id bigint NOT NULL,
    receiver_id character varying(128) NOT NULL,
    delivery_id character varying(128) NOT NULL,
    payload_digest character(64) NOT NULL,
    payload_size_bytes bigint NOT NULL,
    key_version integer NOT NULL,
    trigger_revision bigint NOT NULL,
    pipeline_slug character varying(100) NOT NULL,
    version_policy text NOT NULL,
    pinned_version_hash text,
    runtime_input_keys text[] DEFAULT '{}'::text[] NOT NULL,
    retry_enabled boolean DEFAULT false NOT NULL,
    retry_max_attempts integer DEFAULT 1 NOT NULL,
    retry_backoff_strategy text DEFAULT 'none'::text NOT NULL,
    retry_initial_delay_seconds integer DEFAULT 0 CONSTRAINT pipeline_webhook_deliveries_retry_initial_delay_second_not_null NOT NULL,
    retry_max_delay_seconds integer DEFAULT 0 NOT NULL,
    retry_jitter boolean DEFAULT false NOT NULL,
    retryable_failure_kinds text[] DEFAULT '{}'::text[] NOT NULL,
    retry_max_elapsed_seconds integer,
    pipeline_version_spec_hash text,
    run_slug character varying(100) NOT NULL,
    attempt_number integer DEFAULT 1 NOT NULL,
    status text DEFAULT 'accepted'::text NOT NULL,
    failure_kind text,
    decision_reason text,
    next_retry_at timestamp with time zone,
    encrypted_envelope bytea,
    envelope_expires_at timestamp with time zone,
    received_at timestamp with time zone NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    duplicate_count integer DEFAULT 0 NOT NULL,
    last_duplicate_at timestamp with time zone,
    CONSTRAINT chk_pipeline_webhook_deliveries_identity CHECK ((((delivery_id)::text ~ '^[A-Za-z0-9._:-]{1,128}$'::text) AND (payload_digest ~ '^[0-9a-f]{64}$'::text) AND (payload_size_bytes >= 0) AND (key_version > 0) AND (trigger_revision > 0) AND ((attempt_number >= 1) AND (attempt_number <= 10)))),
    CONSTRAINT chk_pipeline_webhook_deliveries_retry CHECK ((((retry_max_attempts >= 1) AND (retry_max_attempts <= 10)) AND ((retry_initial_delay_seconds >= 0) AND (retry_initial_delay_seconds <= 86400)) AND ((retry_max_delay_seconds >= 0) AND (retry_max_delay_seconds <= 604800)) AND ((retry_max_elapsed_seconds IS NULL) OR ((retry_max_elapsed_seconds >= 1) AND (retry_max_elapsed_seconds <= 2592000))))),
    CONSTRAINT chk_pipeline_webhook_deliveries_version_policy CHECK ((((version_policy = 'latest_deployed'::text) AND (pinned_version_hash IS NULL)) OR ((version_policy = 'pinned'::text) AND (pinned_version_hash IS NOT NULL))))
);


--
-- Name: pipeline_webhook_deliveries_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipeline_webhook_deliveries_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pipeline_webhook_deliveries_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipeline_webhook_deliveries_id_seq OWNED BY public.pipeline_webhook_deliveries.id;


--
-- Name: pipeline_webhook_secret_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipeline_webhook_secret_versions (
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    pipeline_trigger_id bigint NOT NULL,
    credential_id bigint NOT NULL,
    key_version integer NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    disabled_at timestamp with time zone,
    CONSTRAINT pipeline_webhook_secret_versions_key_version_check CHECK ((key_version > 0))
);


--
-- Name: project_environments_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.project_environments_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: project_environments_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.project_environments_id_seq OWNED BY public.project_environments.id;


--
-- Name: project_repository_setups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_repository_setups (
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    request jsonb NOT NULL,
    result jsonb,
    status text DEFAULT 'pending'::text NOT NULL,
    failure_code text DEFAULT ''::text NOT NULL,
    lease_owner text,
    lease_until timestamp with time zone,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    abandoned_at timestamp with time zone
);


--
-- Name: projects_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.projects_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: projects_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.projects_id_seq OWNED BY public.projects.id;


--
-- Name: proof_change_candidate_mappings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.proof_change_candidate_mappings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: proof_change_candidate_mappings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.proof_change_candidate_mappings_id_seq OWNED BY public.proof_change_candidate_mappings.id;


--
-- Name: proof_change_candidates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.proof_change_candidates (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    mapping_id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    candidate_key text NOT NULL,
    change_request_id text NOT NULL,
    source_ref text NOT NULL,
    head_sha text NOT NULL,
    source_sha256 text NOT NULL,
    spec_hash text NOT NULL,
    canonical_spec bytea NOT NULL,
    supersedes_candidate_id bigint,
    closed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT proof_change_candidates_identity_nonempty CHECK (((NULLIF(btrim(candidate_key), ''::text) IS NOT NULL) AND (NULLIF(btrim(change_request_id), ''::text) IS NOT NULL) AND (NULLIF(btrim(source_ref), ''::text) IS NOT NULL) AND (NULLIF(btrim(head_sha), ''::text) IS NOT NULL) AND (NULLIF(btrim(source_sha256), ''::text) IS NOT NULL) AND (NULLIF(btrim(spec_hash), ''::text) IS NOT NULL)))
);


--
-- Name: proof_change_candidates_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.proof_change_candidates_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: proof_change_candidates_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.proof_change_candidates_id_seq OWNED BY public.proof_change_candidates.id;


--
-- Name: proof_evidence; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.proof_evidence (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    mapping_id bigint NOT NULL,
    candidate_id bigint NOT NULL,
    spec_hash text NOT NULL,
    binding_fingerprint text NOT NULL,
    capability_fingerprint text NOT NULL,
    kind text NOT NULL,
    digest text NOT NULL,
    source_identifier text NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: proof_evidence_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.proof_evidence_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: proof_evidence_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.proof_evidence_id_seq OWNED BY public.proof_evidence.id;


--
-- Name: proof_lifecycle_links; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.proof_lifecycle_links (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    receipt_id bigint NOT NULL,
    candidate_id bigint NOT NULL,
    kind text NOT NULL,
    identifier character varying(255) NOT NULL,
    deployment_id bigint,
    run_id bigint,
    spec_hash text NOT NULL,
    binding_fingerprint text NOT NULL,
    capability_fingerprint text NOT NULL,
    linked_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT ck_proof_lifecycle_links_target CHECK ((((kind = 'deployment'::text) AND (deployment_id IS NOT NULL) AND (run_id IS NULL)) OR ((kind = 'run'::text) AND (deployment_id IS NOT NULL) AND (run_id IS NOT NULL))))
);


--
-- Name: proof_lifecycle_links_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.proof_lifecycle_links_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: proof_lifecycle_links_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.proof_lifecycle_links_id_seq OWNED BY public.proof_lifecycle_links.id;


--
-- Name: proof_observation_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.proof_observation_jobs (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    candidate_id bigint NOT NULL,
    data_source_id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    request_digest text NOT NULL,
    request jsonb NOT NULL,
    authorization_decision jsonb NOT NULL,
    target_ciphertext bytea,
    policy_version text NOT NULL,
    worker_identity text NOT NULL,
    connector_type text NOT NULL,
    operation text NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    reserved_requests bigint NOT NULL,
    reserved_bytes bigint NOT NULL,
    reserved_rows bigint NOT NULL,
    reserved_cost bigint NOT NULL,
    budget_expires_at timestamp with time zone NOT NULL,
    deadline_at timestamp with time zone NOT NULL,
    evidence_retain_until timestamp with time zone NOT NULL,
    metadata_retain_until timestamp with time zone NOT NULL,
    canceled_at timestamp with time zone,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    result jsonb,
    snapshot_ciphertext bytea,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL
);


--
-- Name: proof_observation_jobs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.proof_observation_jobs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: proof_observation_jobs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.proof_observation_jobs_id_seq OWNED BY public.proof_observation_jobs.id;


--
-- Name: proof_observation_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.proof_observation_policies (
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    version text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    worker_identity text NOT NULL,
    policy jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL
);


--
-- Name: proof_receipts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.proof_receipts (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    mapping_id bigint NOT NULL,
    candidate_id bigint NOT NULL,
    evidence_id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    receipt_key text NOT NULL,
    spec_hash text NOT NULL,
    binding_fingerprint text NOT NULL,
    capability_fingerprint text NOT NULL,
    compiler_version text NOT NULL,
    policy_version text NOT NULL,
    aggregate_verdict text NOT NULL,
    receipt jsonb NOT NULL,
    supersedes_receipt_id bigint,
    created_at timestamp with time zone NOT NULL,
    retain_until timestamp with time zone NOT NULL
);


--
-- Name: proof_receipts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.proof_receipts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: proof_receipts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.proof_receipts_id_seq OWNED BY public.proof_receipts.id;


--
-- Name: proof_webhook_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.proof_webhook_deliveries (
    provider text NOT NULL,
    delivery_id text NOT NULL,
    payload_sha256 text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT proof_webhook_deliveries_digest_valid CHECK ((payload_sha256 ~ '^[0-9a-f]{64}$'::text))
);


--
-- Name: queries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.queries (
    id bigint NOT NULL,
    data_source_id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    data jsonb NOT NULL,
    expected_input_contract jsonb,
    deleted_at timestamp with time zone,
    delete_reason text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    expected_input_contract_ref jsonb,
    output_contract jsonb,
    output_contract_ref jsonb
);


--
-- Name: queries_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.queries_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: queries_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.queries_id_seq OWNED BY public.queries.id;


--
-- Name: query_executions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.query_executions (
    id bigint NOT NULL,
    query_id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    status text NOT NULL,
    result jsonb NOT NULL,
    executed_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: query_executions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.query_executions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: query_executions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.query_executions_id_seq OWNED BY public.query_executions.id;


--
-- Name: query_parameters; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.query_parameters (
    id bigint NOT NULL,
    query_id bigint NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    data_type character varying(50) NOT NULL,
    is_required boolean DEFAULT false NOT NULL,
    default_value text,
    description text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: query_parameters_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.query_parameters_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: query_parameters_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.query_parameters_id_seq OWNED BY public.query_parameters.id;


--
-- Name: river_client; Type: TABLE; Schema: public; Owner: -
--

CREATE UNLOGGED TABLE public.river_client (
    id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    paused_at timestamp with time zone,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT name_length CHECK (((char_length(id) > 0) AND (char_length(id) < 128)))
);


--
-- Name: river_client_queue; Type: TABLE; Schema: public; Owner: -
--

CREATE UNLOGGED TABLE public.river_client_queue (
    river_client_id text NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    max_workers bigint DEFAULT 0 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    num_jobs_completed bigint DEFAULT 0 NOT NULL,
    num_jobs_running bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT num_jobs_completed_zero_or_positive CHECK ((num_jobs_completed >= 0)),
    CONSTRAINT num_jobs_running_zero_or_positive CHECK ((num_jobs_running >= 0)),
    CONSTRAINT river_client_queue_name_length CHECK (((char_length(name) > 0) AND (char_length(name) < 128)))
);


--
-- Name: river_job; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.river_job (
    id bigint NOT NULL,
    state public.river_job_state DEFAULT 'available'::public.river_job_state NOT NULL,
    attempt smallint DEFAULT 0 NOT NULL,
    max_attempts smallint NOT NULL,
    attempted_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    finalized_at timestamp with time zone,
    scheduled_at timestamp with time zone DEFAULT now() NOT NULL,
    priority smallint DEFAULT 1 NOT NULL,
    args jsonb NOT NULL,
    attempted_by text[],
    errors jsonb[],
    kind text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    queue text DEFAULT 'default'::text NOT NULL,
    tags character varying(255)[] DEFAULT '{}'::character varying[] NOT NULL,
    unique_key bytea,
    unique_states bit(8),
    CONSTRAINT finalized_or_finalized_at_null CHECK ((((finalized_at IS NULL) AND (state <> ALL (ARRAY['cancelled'::public.river_job_state, 'completed'::public.river_job_state, 'discarded'::public.river_job_state]))) OR ((finalized_at IS NOT NULL) AND (state = ANY (ARRAY['cancelled'::public.river_job_state, 'completed'::public.river_job_state, 'discarded'::public.river_job_state]))))),
    CONSTRAINT kind_length CHECK (((char_length(kind) > 0) AND (char_length(kind) < 128))),
    CONSTRAINT max_attempts_is_positive CHECK ((max_attempts > 0)),
    CONSTRAINT priority_in_range CHECK (((priority >= 1) AND (priority <= 4))),
    CONSTRAINT queue_length CHECK (((char_length(queue) > 0) AND (char_length(queue) < 128)))
);


--
-- Name: river_job_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.river_job_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: river_job_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.river_job_id_seq OWNED BY public.river_job.id;


--
-- Name: river_leader; Type: TABLE; Schema: public; Owner: -
--

CREATE UNLOGGED TABLE public.river_leader (
    elected_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    leader_id text NOT NULL,
    name text DEFAULT 'default'::text NOT NULL,
    CONSTRAINT leader_id_length CHECK (((char_length(leader_id) > 0) AND (char_length(leader_id) < 128))),
    CONSTRAINT name_length CHECK ((name = 'default'::text))
);


--
-- Name: river_migration; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.river_migration (
    line text NOT NULL,
    version bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT line_length CHECK (((char_length(line) > 0) AND (char_length(line) < 128))),
    CONSTRAINT version_gte_1 CHECK ((version >= 1))
);


--
-- Name: river_queue; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.river_queue (
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    paused_at timestamp with time zone,
    updated_at timestamp with time zone NOT NULL
);


--
-- Name: role_permissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.role_permissions (
    id integer NOT NULL,
    role_id integer NOT NULL,
    permission_id integer NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: role_permissions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.role_permissions_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: role_permissions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.role_permissions_id_seq OWNED BY public.role_permissions.id;


--
-- Name: roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.roles (
    id integer NOT NULL,
    organization_id integer NOT NULL,
    slug character varying(100) NOT NULL,
    name character varying(255) NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    delete_reason text
);


--
-- Name: roles_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.roles_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: roles_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.roles_id_seq OWNED BY public.roles.id;


--
-- Name: schema_metadata; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_metadata (
    id bigint NOT NULL,
    slug character varying(120) NOT NULL,
    data_source_id bigint NOT NULL,
    schema_data jsonb NOT NULL,
    created_at timestamp without time zone DEFAULT now(),
    updated_at timestamp without time zone DEFAULT now()
);


--
-- Name: schema_metadata_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.schema_metadata_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: schema_metadata_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.schema_metadata_id_seq OWNED BY public.schema_metadata.id;


--
-- Name: snowflake_wif_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.snowflake_wif_bindings (
    id bigint NOT NULL,
    organization_id integer NOT NULL,
    data_source_id bigint NOT NULL,
    issuer text NOT NULL,
    account text NOT NULL,
    desired boolean DEFAULT true NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    entry_id text DEFAULT (gen_random_uuid())::text NOT NULL,
    verified_at timestamp with time zone,
    reconcile_after timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT snowflake_wif_bindings_check CHECK (((state <> 'ready'::text) OR (desired AND (entry_id <> ''::text) AND (verified_at IS NOT NULL)))),
    CONSTRAINT snowflake_wif_bindings_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'ready'::text, 'failed'::text, 'revoking'::text])))
);


--
-- Name: snowflake_wif_bindings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.snowflake_wif_bindings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: snowflake_wif_bindings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.snowflake_wif_bindings_id_seq OWNED BY public.snowflake_wif_bindings.id;


--
-- Name: user_mfa_challenges; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_mfa_challenges (
    id bigint NOT NULL,
    user_id integer NOT NULL,
    challenge_token_hash text NOT NULL,
    code_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    attempt_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    authentication_version bigint DEFAULT 1 NOT NULL
);


--
-- Name: user_mfa_challenges_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.user_mfa_challenges_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: user_mfa_challenges_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.user_mfa_challenges_id_seq OWNED BY public.user_mfa_challenges.id;


--
-- Name: user_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_sessions (
    k character varying(64) NOT NULL,
    v bytea NOT NULL,
    e bigint DEFAULT 0 NOT NULL
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id integer NOT NULL,
    slug character varying(100) NOT NULL,
    email character varying(255) NOT NULL,
    full_name character varying(255) DEFAULT ''::character varying NOT NULL,
    password character varying(255),
    role character varying(50) NOT NULL,
    locale character varying(10) DEFAULT 'en'::character varying NOT NULL,
    oidc_provider character varying(50),
    oidc_provider_id character varying(255),
    avatar_url text,
    phone_number character varying(32),
    job_title character varying(120),
    timezone character varying(64),
    billing_line1 character varying(255),
    billing_line2 character varying(255),
    billing_city character varying(120),
    billing_region character varying(120),
    billing_postal_code character varying(32),
    billing_country character(2),
    mfa_email_enabled boolean DEFAULT false NOT NULL,
    verified_at timestamp without time zone,
    email_verification_token_hash text,
    email_verification_token_expires_at timestamp without time zone,
    password_reset_token_hash text,
    password_reset_token_expires_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    authentication_version bigint DEFAULT 1 NOT NULL
);


--
-- Name: users_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.users_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;


--
-- Name: wishlist_registrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.wishlist_registrations (
    id integer NOT NULL,
    work_email character varying(254) NOT NULL,
    care_most_about character varying(80) NOT NULL,
    workflow_to_test_first character varying(500),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    communication_request_version character varying(80)
);


--
-- Name: wishlist_registrations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.wishlist_registrations_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: wishlist_registrations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.wishlist_registrations_id_seq OWNED BY public.wishlist_registrations.id;


--
-- Name: account_deletion_requests id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_deletion_requests ALTER COLUMN id SET DEFAULT nextval('public.account_deletion_requests_id_seq'::regclass);


--
-- Name: alert_delivery_attempts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_delivery_attempts ALTER COLUMN id SET DEFAULT nextval('public.alert_delivery_attempts_id_seq'::regclass);


--
-- Name: alert_destinations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_destinations ALTER COLUMN id SET DEFAULT nextval('public.alert_destinations_id_seq'::regclass);


--
-- Name: alert_policies id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policies ALTER COLUMN id SET DEFAULT nextval('public.alert_policies_id_seq'::regclass);


--
-- Name: alert_policy_destinations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policy_destinations ALTER COLUMN id SET DEFAULT nextval('public.alert_policy_destinations_id_seq'::regclass);


--
-- Name: api_keys id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys ALTER COLUMN id SET DEFAULT nextval('public.api_keys_id_seq'::regclass);


--
-- Name: credential_oauth_grants id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_grants ALTER COLUMN id SET DEFAULT nextval('public.credential_oauth_grants_id_seq'::regclass);


--
-- Name: credential_oauth_installs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs ALTER COLUMN id SET DEFAULT nextval('public.credential_oauth_installs_id_seq'::regclass);


--
-- Name: credentials id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials ALTER COLUMN id SET DEFAULT nextval('public.credentials_id_seq'::regclass);


--
-- Name: data_source_bindings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_bindings ALTER COLUMN id SET DEFAULT nextval('public.data_source_bindings_id_seq'::regclass);


--
-- Name: data_source_refs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_refs ALTER COLUMN id SET DEFAULT nextval('public.data_source_refs_id_seq'::regclass);


--
-- Name: data_sources id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_sources ALTER COLUMN id SET DEFAULT nextval('public.data_sources_id_seq'::regclass);


--
-- Name: drift_impact_reports id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.drift_impact_reports ALTER COLUMN id SET DEFAULT nextval('public.drift_impact_reports_id_seq'::regclass);


--
-- Name: environment_runtime_variables id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_runtime_variables ALTER COLUMN id SET DEFAULT nextval('public.environment_runtime_variables_id_seq'::regclass);


--
-- Name: environment_secrets id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_secrets ALTER COLUMN id SET DEFAULT nextval('public.environment_secrets_id_seq'::regclass);


--
-- Name: environments id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environments ALTER COLUMN id SET DEFAULT nextval('public.environments_id_seq'::regclass);


--
-- Name: events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.events ALTER COLUMN id SET DEFAULT nextval('public.events_id_seq'::regclass);


--
-- Name: github_app_install_intents id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_install_intents ALTER COLUMN id SET DEFAULT nextval('public.github_app_install_intents_id_seq'::regclass);


--
-- Name: github_app_installations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_installations ALTER COLUMN id SET DEFAULT nextval('public.github_app_installations_id_seq'::regclass);


--
-- Name: github_app_repositories id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_repositories ALTER COLUMN id SET DEFAULT nextval('public.github_app_repositories_id_seq'::regclass);


--
-- Name: gitlab_proof_webhooks id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gitlab_proof_webhooks ALTER COLUMN id SET DEFAULT nextval('public.gitlab_proof_webhooks_id_seq'::regclass);


--
-- Name: notification_connections id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_connections ALTER COLUMN id SET DEFAULT nextval('public.notification_connections_id_seq'::regclass);


--
-- Name: notification_records id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_records ALTER COLUMN id SET DEFAULT nextval('public.notification_records_id_seq'::regclass);


--
-- Name: odcs_contract_revisions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contract_revisions ALTER COLUMN id SET DEFAULT nextval('public.odcs_contract_revisions_id_seq'::regclass);


--
-- Name: odcs_contracts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contracts ALTER COLUMN id SET DEFAULT nextval('public.odcs_contracts_id_seq'::regclass);


--
-- Name: organization_gitlab_connections id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_gitlab_connections ALTER COLUMN id SET DEFAULT nextval('public.organization_gitlab_connections_id_seq'::regclass);


--
-- Name: organization_member_invitations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_member_invitations ALTER COLUMN id SET DEFAULT nextval('public.organization_member_invitations_id_seq'::regclass);


--
-- Name: organization_members id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_members ALTER COLUMN id SET DEFAULT nextval('public.organization_members_id_seq'::regclass);


--
-- Name: organization_retention_runs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_retention_runs ALTER COLUMN id SET DEFAULT nextval('public.organization_retention_runs_id_seq'::regclass);


--
-- Name: organizations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organizations ALTER COLUMN id SET DEFAULT nextval('public.organizations_id_seq'::regclass);


--
-- Name: permissions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permissions ALTER COLUMN id SET DEFAULT nextval('public.permissions_id_seq'::regclass);


--
-- Name: personal_access_tokens id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_access_tokens ALTER COLUMN id SET DEFAULT nextval('public.personal_access_tokens_id_seq'::regclass);


--
-- Name: pipeline_checkpoint id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_checkpoint ALTER COLUMN id SET DEFAULT nextval('public.pipeline_checkpoint_id_seq'::regclass);


--
-- Name: pipeline_compiled_plans id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_compiled_plans ALTER COLUMN id SET DEFAULT nextval('public.pipeline_compiled_plans_id_seq'::regclass);


--
-- Name: pipeline_completion_snapshots id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_snapshots ALTER COLUMN id SET DEFAULT nextval('public.pipeline_completion_snapshots_id_seq'::regclass);


--
-- Name: pipeline_deployment_resolutions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployment_resolutions ALTER COLUMN id SET DEFAULT nextval('public.pipeline_deployment_resolutions_id_seq'::regclass);


--
-- Name: pipeline_deployments id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployments ALTER COLUMN id SET DEFAULT nextval('public.pipeline_deployments_id_seq'::regclass);


--
-- Name: pipeline_drafts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_drafts ALTER COLUMN id SET DEFAULT nextval('public.pipeline_drafts_id_seq'::regclass);


--
-- Name: pipeline_incremental_checkpoint_locks id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoint_locks ALTER COLUMN id SET DEFAULT nextval('public.pipeline_incremental_checkpoint_locks_id_seq'::regclass);


--
-- Name: pipeline_incremental_checkpoints id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoints ALTER COLUMN id SET DEFAULT nextval('public.pipeline_incremental_checkpoints_id_seq'::regclass);


--
-- Name: pipeline_proposal_authorities id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_proposal_authorities ALTER COLUMN id SET DEFAULT nextval('public.pipeline_proposal_authorities_id_seq'::regclass);


--
-- Name: pipeline_references id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_references ALTER COLUMN id SET DEFAULT nextval('public.pipeline_references_id_seq'::regclass);


--
-- Name: pipeline_run_logical_closures id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_logical_closures ALTER COLUMN id SET DEFAULT nextval('public.pipeline_run_logical_closures_id_seq'::regclass);


--
-- Name: pipeline_run_recency_episodes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_episodes ALTER COLUMN id SET DEFAULT nextval('public.pipeline_run_recency_episodes_id_seq'::regclass);


--
-- Name: pipeline_run_recency_expectations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_expectations ALTER COLUMN id SET DEFAULT nextval('public.pipeline_run_recency_expectations_id_seq'::regclass);


--
-- Name: pipeline_run_terminal_obligations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_terminal_obligations ALTER COLUMN id SET DEFAULT nextval('public.pipeline_run_terminal_obligations_id_seq'::regclass);


--
-- Name: pipeline_runs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_runs ALTER COLUMN id SET DEFAULT nextval('public.pipeline_runs_id_seq'::regclass);


--
-- Name: pipeline_schedule_fires id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_schedule_fires ALTER COLUMN id SET DEFAULT nextval('public.pipeline_schedule_fires_id_seq'::regclass);


--
-- Name: pipeline_specs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_specs ALTER COLUMN id SET DEFAULT nextval('public.pipeline_specs_id_seq'::regclass);


--
-- Name: pipeline_trigger_retry_attempts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_trigger_retry_attempts ALTER COLUMN id SET DEFAULT nextval('public.pipeline_trigger_retry_attempts_id_seq'::regclass);


--
-- Name: pipeline_triggers id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_triggers ALTER COLUMN id SET DEFAULT nextval('public.pipeline_triggers_id_seq'::regclass);


--
-- Name: pipeline_validation_results id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_validation_results ALTER COLUMN id SET DEFAULT nextval('public.pipeline_validation_results_id_seq'::regclass);


--
-- Name: pipeline_versions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_versions ALTER COLUMN id SET DEFAULT nextval('public.pipeline_versions_id_seq'::regclass);


--
-- Name: pipeline_webhook_deliveries id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_deliveries ALTER COLUMN id SET DEFAULT nextval('public.pipeline_webhook_deliveries_id_seq'::regclass);


--
-- Name: project_environments id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_environments ALTER COLUMN id SET DEFAULT nextval('public.project_environments_id_seq'::regclass);


--
-- Name: projects id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects ALTER COLUMN id SET DEFAULT nextval('public.projects_id_seq'::regclass);


--
-- Name: proof_change_candidate_mappings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidate_mappings ALTER COLUMN id SET DEFAULT nextval('public.proof_change_candidate_mappings_id_seq'::regclass);


--
-- Name: proof_change_candidates id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidates ALTER COLUMN id SET DEFAULT nextval('public.proof_change_candidates_id_seq'::regclass);


--
-- Name: proof_evidence id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_evidence ALTER COLUMN id SET DEFAULT nextval('public.proof_evidence_id_seq'::regclass);


--
-- Name: proof_lifecycle_links id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_lifecycle_links ALTER COLUMN id SET DEFAULT nextval('public.proof_lifecycle_links_id_seq'::regclass);


--
-- Name: proof_observation_jobs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_jobs ALTER COLUMN id SET DEFAULT nextval('public.proof_observation_jobs_id_seq'::regclass);


--
-- Name: proof_receipts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_receipts ALTER COLUMN id SET DEFAULT nextval('public.proof_receipts_id_seq'::regclass);


--
-- Name: queries id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.queries ALTER COLUMN id SET DEFAULT nextval('public.queries_id_seq'::regclass);


--
-- Name: query_executions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.query_executions ALTER COLUMN id SET DEFAULT nextval('public.query_executions_id_seq'::regclass);


--
-- Name: query_parameters id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.query_parameters ALTER COLUMN id SET DEFAULT nextval('public.query_parameters_id_seq'::regclass);


--
-- Name: river_job id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.river_job ALTER COLUMN id SET DEFAULT nextval('public.river_job_id_seq'::regclass);


--
-- Name: role_permissions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions ALTER COLUMN id SET DEFAULT nextval('public.role_permissions_id_seq'::regclass);


--
-- Name: roles id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles ALTER COLUMN id SET DEFAULT nextval('public.roles_id_seq'::regclass);


--
-- Name: schema_metadata id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_metadata ALTER COLUMN id SET DEFAULT nextval('public.schema_metadata_id_seq'::regclass);


--
-- Name: snowflake_wif_bindings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.snowflake_wif_bindings ALTER COLUMN id SET DEFAULT nextval('public.snowflake_wif_bindings_id_seq'::regclass);


--
-- Name: user_mfa_challenges id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_mfa_challenges ALTER COLUMN id SET DEFAULT nextval('public.user_mfa_challenges_id_seq'::regclass);


--
-- Name: users id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);


--
-- Name: wishlist_registrations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wishlist_registrations ALTER COLUMN id SET DEFAULT nextval('public.wishlist_registrations_id_seq'::regclass);


--
-- Name: abuse_control_buckets abuse_control_buckets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.abuse_control_buckets
    ADD CONSTRAINT abuse_control_buckets_pkey PRIMARY KEY (scope, subject_hash);


--
-- Name: account_deletion_requests account_deletion_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_deletion_requests
    ADD CONSTRAINT account_deletion_requests_pkey PRIMARY KEY (id);


--
-- Name: account_deletion_requests account_deletion_requests_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_deletion_requests
    ADD CONSTRAINT account_deletion_requests_slug_key UNIQUE (slug);


--
-- Name: account_deletion_requests account_deletion_requests_user_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_deletion_requests
    ADD CONSTRAINT account_deletion_requests_user_id_key UNIQUE (user_id);


--
-- Name: alert_delivery_attempts alert_delivery_attempts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_delivery_attempts
    ADD CONSTRAINT alert_delivery_attempts_pkey PRIMARY KEY (id);


--
-- Name: alert_destinations alert_destinations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_destinations
    ADD CONSTRAINT alert_destinations_pkey PRIMARY KEY (id);


--
-- Name: alert_policies alert_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policies
    ADD CONSTRAINT alert_policies_pkey PRIMARY KEY (id);


--
-- Name: alert_policy_destinations alert_policy_destinations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policy_destinations
    ADD CONSTRAINT alert_policy_destinations_pkey PRIMARY KEY (id);


--
-- Name: api_keys api_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);


--
-- Name: credential_http_oauth_apps credential_http_oauth_apps_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_http_oauth_apps
    ADD CONSTRAINT credential_http_oauth_apps_pkey PRIMARY KEY (organization_id, data_source_id);


--
-- Name: credential_oauth_grants credential_oauth_grants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_grants
    ADD CONSTRAINT credential_oauth_grants_pkey PRIMARY KEY (id);


--
-- Name: credential_oauth_installs credential_oauth_installs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT credential_oauth_installs_pkey PRIMARY KEY (id);


--
-- Name: credentials credentials_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_pkey PRIMARY KEY (id);


--
-- Name: data_source_bindings data_source_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_bindings
    ADD CONSTRAINT data_source_bindings_pkey PRIMARY KEY (id);


--
-- Name: data_source_refs data_source_refs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_refs
    ADD CONSTRAINT data_source_refs_pkey PRIMARY KEY (id);


--
-- Name: data_sources data_sources_organization_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_sources
    ADD CONSTRAINT data_sources_organization_id_slug_key UNIQUE (organization_id, slug);


--
-- Name: data_sources data_sources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_sources
    ADD CONSTRAINT data_sources_pkey PRIMARY KEY (id);


--
-- Name: drift_impact_reports drift_impact_reports_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.drift_impact_reports
    ADD CONSTRAINT drift_impact_reports_pkey PRIMARY KEY (id);


--
-- Name: drift_impact_reports drift_impact_reports_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.drift_impact_reports
    ADD CONSTRAINT drift_impact_reports_slug_key UNIQUE (slug);


--
-- Name: environment_runtime_variables environment_runtime_variables_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_runtime_variables
    ADD CONSTRAINT environment_runtime_variables_pkey PRIMARY KEY (id);


--
-- Name: environment_secrets environment_secrets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_secrets
    ADD CONSTRAINT environment_secrets_pkey PRIMARY KEY (id);


--
-- Name: environments environments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environments
    ADD CONSTRAINT environments_pkey PRIMARY KEY (id);


--
-- Name: events events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.events
    ADD CONSTRAINT events_pkey PRIMARY KEY (id);


--
-- Name: execution_requests execution_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.execution_requests
    ADD CONSTRAINT execution_requests_pkey PRIMARY KEY (id);


--
-- Name: github_app_install_intents github_app_install_intents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_install_intents
    ADD CONSTRAINT github_app_install_intents_pkey PRIMARY KEY (id);


--
-- Name: github_app_install_intents github_app_install_intents_state_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_install_intents
    ADD CONSTRAINT github_app_install_intents_state_hash_key UNIQUE (state_hash);


--
-- Name: github_app_installations github_app_installations_installation_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_installations
    ADD CONSTRAINT github_app_installations_installation_id_key UNIQUE (installation_id);


--
-- Name: github_app_installations github_app_installations_organization_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_installations
    ADD CONSTRAINT github_app_installations_organization_id_key UNIQUE (organization_id);


--
-- Name: github_app_installations github_app_installations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_installations
    ADD CONSTRAINT github_app_installations_pkey PRIMARY KEY (id);


--
-- Name: github_app_repositories github_app_repositories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_repositories
    ADD CONSTRAINT github_app_repositories_pkey PRIMARY KEY (id);


--
-- Name: gitlab_proof_webhooks gitlab_proof_webhooks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gitlab_proof_webhooks
    ADD CONSTRAINT gitlab_proof_webhooks_pkey PRIMARY KEY (id);


--
-- Name: gitlab_proof_webhooks gitlab_proof_webhooks_route_key_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gitlab_proof_webhooks
    ADD CONSTRAINT gitlab_proof_webhooks_route_key_hash_key UNIQUE (route_key_hash);


--
-- Name: notification_connections notification_connections_organization_id_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_connections
    ADD CONSTRAINT notification_connections_organization_id_id_key UNIQUE (organization_id, id);


--
-- Name: notification_connections notification_connections_organization_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_connections
    ADD CONSTRAINT notification_connections_organization_id_slug_key UNIQUE (organization_id, slug);


--
-- Name: notification_connections notification_connections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_connections
    ADD CONSTRAINT notification_connections_pkey PRIMARY KEY (id);


--
-- Name: notification_records notification_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_records
    ADD CONSTRAINT notification_records_pkey PRIMARY KEY (id);


--
-- Name: odcs_artifacts odcs_artifacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_artifacts
    ADD CONSTRAINT odcs_artifacts_pkey PRIMARY KEY (project_id, source_repo, source_commit, source_path);


--
-- Name: odcs_contract_revisions odcs_contract_revisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contract_revisions
    ADD CONSTRAINT odcs_contract_revisions_pkey PRIMARY KEY (id);


--
-- Name: odcs_contracts odcs_contracts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contracts
    ADD CONSTRAINT odcs_contracts_pkey PRIMARY KEY (id);




--
-- Name: organization_gitlab_connections organization_gitlab_connections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_gitlab_connections
    ADD CONSTRAINT organization_gitlab_connections_pkey PRIMARY KEY (id);


--
-- Name: organization_member_invitations organization_member_invitations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_member_invitations
    ADD CONSTRAINT organization_member_invitations_pkey PRIMARY KEY (id);


--
-- Name: organization_members organization_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_members
    ADD CONSTRAINT organization_members_pkey PRIMARY KEY (id);


--
-- Name: organization_retention_runs organization_retention_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_retention_runs
    ADD CONSTRAINT organization_retention_runs_pkey PRIMARY KEY (id);


--
-- Name: organizations organizations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organizations
    ADD CONSTRAINT organizations_pkey PRIMARY KEY (id);


--
-- Name: organizations organizations_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organizations
    ADD CONSTRAINT organizations_slug_key UNIQUE (slug);


--
-- Name: permissions permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permissions
    ADD CONSTRAINT permissions_pkey PRIMARY KEY (id);


--
-- Name: personal_access_tokens personal_access_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_access_tokens
    ADD CONSTRAINT personal_access_tokens_pkey PRIMARY KEY (id);


--
-- Name: pipeline_checkpoint pipeline_checkpoint_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_checkpoint
    ADD CONSTRAINT pipeline_checkpoint_pkey PRIMARY KEY (id);


--
-- Name: pipeline_checkpoint pipeline_checkpoint_project_environment_id_run_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_checkpoint
    ADD CONSTRAINT pipeline_checkpoint_project_environment_id_run_slug_key UNIQUE (project_environment_id, run_slug);


--
-- Name: pipeline_compiled_plans pipeline_compiled_plans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_compiled_plans
    ADD CONSTRAINT pipeline_compiled_plans_pkey PRIMARY KEY (id);


--
-- Name: pipeline_compiled_plans pipeline_compiled_plans_project_id_pipeline_slug_digest_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_compiled_plans
    ADD CONSTRAINT pipeline_compiled_plans_project_id_pipeline_slug_digest_key UNIQUE (project_id, pipeline_slug, digest);


--
-- Name: pipeline_completion_capacity pipeline_completion_capacity_organization_id_closure_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_capacity
    ADD CONSTRAINT pipeline_completion_capacity_organization_id_closure_id_key UNIQUE (organization_id, closure_id);


--
-- Name: pipeline_completion_capacity pipeline_completion_capacity_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_capacity
    ADD CONSTRAINT pipeline_completion_capacity_pkey PRIMARY KEY (organization_id, slot);


--
-- Name: pipeline_completion_snapshots pipeline_completion_snapshots_organization_id_closure_id_tr_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_snapshots
    ADD CONSTRAINT pipeline_completion_snapshots_organization_id_closure_id_tr_key UNIQUE (organization_id, closure_id, trigger_id, trigger_revision);


--
-- Name: pipeline_completion_snapshots pipeline_completion_snapshots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_snapshots
    ADD CONSTRAINT pipeline_completion_snapshots_pkey PRIMARY KEY (id);


--
-- Name: pipeline_completion_tombstones pipeline_completion_tombstones_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_tombstones
    ADD CONSTRAINT pipeline_completion_tombstones_pkey PRIMARY KEY (organization_id, closure_id, trigger_id, trigger_revision);


--
-- Name: pipeline_completion_trigger_revisions pipeline_completion_trigger_revisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_trigger_revisions
    ADD CONSTRAINT pipeline_completion_trigger_revisions_pkey PRIMARY KEY (project_environment_id, trigger_id, revision);


--
-- Name: pipeline_creation_proposals pipeline_creation_proposals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_creation_proposals
    ADD CONSTRAINT pipeline_creation_proposals_pkey PRIMARY KEY (organization_id, slug);


--
-- Name: pipeline_creation_proposals pipeline_creation_proposals_project_id_pipeline_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_creation_proposals
    ADD CONSTRAINT pipeline_creation_proposals_project_id_pipeline_slug_key UNIQUE (project_id, pipeline_slug);


--
-- Name: pipeline_deployment_resolutions pipeline_deployment_resolutio_project_environment_id_pipeli_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployment_resolutions
    ADD CONSTRAINT pipeline_deployment_resolutio_project_environment_id_pipeli_key UNIQUE (project_environment_id, pipeline_slug, digest);


--
-- Name: pipeline_deployment_resolutions pipeline_deployment_resolutions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployment_resolutions
    ADD CONSTRAINT pipeline_deployment_resolutions_pkey PRIMARY KEY (id);


--
-- Name: pipeline_deployments pipeline_deployments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployments
    ADD CONSTRAINT pipeline_deployments_pkey PRIMARY KEY (id);


--
-- Name: pipeline_drafts pipeline_drafts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_drafts
    ADD CONSTRAINT pipeline_drafts_pkey PRIMARY KEY (id);


--
-- Name: pipeline_drafts pipeline_drafts_project_id_pipeline_slug_member_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_drafts
    ADD CONSTRAINT pipeline_drafts_project_id_pipeline_slug_member_id_key UNIQUE (project_id, pipeline_slug, member_id);


--
-- Name: pipeline_incremental_checkpoint_locks pipeline_incremental_checkpoint_locks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoint_locks
    ADD CONSTRAINT pipeline_incremental_checkpoint_locks_pkey PRIMARY KEY (id);


--
-- Name: pipeline_incremental_checkpoints pipeline_incremental_checkpoints_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoints
    ADD CONSTRAINT pipeline_incremental_checkpoints_pkey PRIMARY KEY (id);


--
-- Name: pipeline_proposal_authorities pipeline_proposal_authorities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_proposal_authorities
    ADD CONSTRAINT pipeline_proposal_authorities_pkey PRIMARY KEY (id);


--
-- Name: pipeline_references pipeline_references_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_references
    ADD CONSTRAINT pipeline_references_pkey PRIMARY KEY (id);


--
-- Name: pipeline_references pipeline_references_project_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_references
    ADD CONSTRAINT pipeline_references_project_id_slug_key UNIQUE (project_id, slug);


--
-- Name: pipeline_run_logical_closures pipeline_run_logical_closures_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_logical_closures
    ADD CONSTRAINT pipeline_run_logical_closures_pkey PRIMARY KEY (id);


--
-- Name: pipeline_run_recency_episodes pipeline_run_recency_episodes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_episodes
    ADD CONSTRAINT pipeline_run_recency_episodes_pkey PRIMARY KEY (id);


--
-- Name: pipeline_run_recency_episodes pipeline_run_recency_episodes_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_episodes
    ADD CONSTRAINT pipeline_run_recency_episodes_slug_key UNIQUE (slug);


--
-- Name: pipeline_run_recency_expectations pipeline_run_recency_expectat_project_environment_id_pipeli_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_expectations
    ADD CONSTRAINT pipeline_run_recency_expectat_project_environment_id_pipeli_key UNIQUE (project_environment_id, pipeline_slug, revision);


--
-- Name: pipeline_run_recency_expectations pipeline_run_recency_expectations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_expectations
    ADD CONSTRAINT pipeline_run_recency_expectations_pkey PRIMARY KEY (id);


--
-- Name: pipeline_run_recency_successes pipeline_run_recency_successes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_successes
    ADD CONSTRAINT pipeline_run_recency_successes_pkey PRIMARY KEY (project_environment_id, pipeline_slug, deployment_id, deployment_revision);


--
-- Name: pipeline_run_terminal_obligations pipeline_run_terminal_obligations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_terminal_obligations
    ADD CONSTRAINT pipeline_run_terminal_obligations_pkey PRIMARY KEY (id);


--
-- Name: pipeline_runs pipeline_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_runs
    ADD CONSTRAINT pipeline_runs_pkey PRIMARY KEY (id);


--
-- Name: pipeline_runs pipeline_runs_project_environment_id_run_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_runs
    ADD CONSTRAINT pipeline_runs_project_environment_id_run_slug_key UNIQUE (project_environment_id, run_slug);


--
-- Name: pipeline_schedule_fires pipeline_schedule_fires_pipeline_trigger_id_trigger_revisio_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_schedule_fires
    ADD CONSTRAINT pipeline_schedule_fires_pipeline_trigger_id_trigger_revisio_key UNIQUE (pipeline_trigger_id, trigger_revision, scheduled_fire_at);


--
-- Name: pipeline_schedule_fires pipeline_schedule_fires_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_schedule_fires
    ADD CONSTRAINT pipeline_schedule_fires_pkey PRIMARY KEY (id);


--
-- Name: pipeline_schedule_fires pipeline_schedule_fires_project_environment_id_fire_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_schedule_fires
    ADD CONSTRAINT pipeline_schedule_fires_project_environment_id_fire_slug_key UNIQUE (project_environment_id, fire_slug);


--
-- Name: pipeline_sink_effects pipeline_sink_effects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_sink_effects
    ADD CONSTRAINT pipeline_sink_effects_pkey PRIMARY KEY (id);


--
-- Name: pipeline_sink_effects pipeline_sink_effects_project_environment_id_run_slug_sink__key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_sink_effects
    ADD CONSTRAINT pipeline_sink_effects_project_environment_id_run_slug_sink__key UNIQUE (project_environment_id, run_slug, sink_step_slug, idempotency_key);


--
-- Name: pipeline_specs pipeline_specs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_specs
    ADD CONSTRAINT pipeline_specs_pkey PRIMARY KEY (id);


--
-- Name: pipeline_specs pipeline_specs_project_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_specs
    ADD CONSTRAINT pipeline_specs_project_id_slug_key UNIQUE (project_id, slug);


--
-- Name: pipeline_trigger_retry_attempts pipeline_trigger_retry_attemp_project_environment_id_attemp_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_trigger_retry_attempts
    ADD CONSTRAINT pipeline_trigger_retry_attemp_project_environment_id_attemp_key UNIQUE (project_environment_id, attempt_slug);


--
-- Name: pipeline_trigger_retry_attempts pipeline_trigger_retry_attemp_project_environment_id_run_sl_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_trigger_retry_attempts
    ADD CONSTRAINT pipeline_trigger_retry_attemp_project_environment_id_run_sl_key UNIQUE (project_environment_id, run_slug);


--
-- Name: pipeline_trigger_retry_attempts pipeline_trigger_retry_attempts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_trigger_retry_attempts
    ADD CONSTRAINT pipeline_trigger_retry_attempts_pkey PRIMARY KEY (id);


--
-- Name: pipeline_triggers pipeline_triggers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_triggers
    ADD CONSTRAINT pipeline_triggers_pkey PRIMARY KEY (id);


--
-- Name: pipeline_validation_results pipeline_validation_results_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_validation_results
    ADD CONSTRAINT pipeline_validation_results_pkey PRIMARY KEY (id);


--
-- Name: pipeline_versions pipeline_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_versions
    ADD CONSTRAINT pipeline_versions_pkey PRIMARY KEY (id);


--
-- Name: pipeline_webhook_deliveries pipeline_webhook_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_deliveries
    ADD CONSTRAINT pipeline_webhook_deliveries_pkey PRIMARY KEY (id);


--
-- Name: project_environments project_environments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_environments
    ADD CONSTRAINT project_environments_pkey PRIMARY KEY (id);


--
-- Name: project_repository_setups project_repository_setups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_repository_setups
    ADD CONSTRAINT project_repository_setups_pkey PRIMARY KEY (organization_id, slug);


--
-- Name: projects projects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_pkey PRIMARY KEY (id);


--
-- Name: proof_change_candidate_mappings proof_change_candidate_mappings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidate_mappings
    ADD CONSTRAINT proof_change_candidate_mappings_pkey PRIMARY KEY (id);


--
-- Name: proof_change_candidates proof_change_candidates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidates
    ADD CONSTRAINT proof_change_candidates_pkey PRIMARY KEY (id);


--
-- Name: proof_evidence proof_evidence_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_evidence
    ADD CONSTRAINT proof_evidence_pkey PRIMARY KEY (id);


--
-- Name: proof_lifecycle_links proof_lifecycle_links_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_lifecycle_links
    ADD CONSTRAINT proof_lifecycle_links_pkey PRIMARY KEY (id);


--
-- Name: proof_observation_jobs proof_observation_jobs_organization_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_jobs
    ADD CONSTRAINT proof_observation_jobs_organization_id_slug_key UNIQUE (organization_id, slug);


--
-- Name: proof_observation_jobs proof_observation_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_jobs
    ADD CONSTRAINT proof_observation_jobs_pkey PRIMARY KEY (id);


--
-- Name: proof_observation_policies proof_observation_policies_organization_id_project_id_proje_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_policies
    ADD CONSTRAINT proof_observation_policies_organization_id_project_id_proje_key UNIQUE (organization_id, project_id, project_environment_id);


--
-- Name: proof_observation_policies proof_observation_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_policies
    ADD CONSTRAINT proof_observation_policies_pkey PRIMARY KEY (organization_id, project_environment_id);


--
-- Name: proof_receipts proof_receipts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_receipts
    ADD CONSTRAINT proof_receipts_pkey PRIMARY KEY (id);


--
-- Name: proof_webhook_deliveries proof_webhook_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_webhook_deliveries
    ADD CONSTRAINT proof_webhook_deliveries_pkey PRIMARY KEY (provider, delivery_id);


--
-- Name: queries queries_data_source_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.queries
    ADD CONSTRAINT queries_data_source_id_slug_key UNIQUE (data_source_id, slug);


--
-- Name: queries queries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.queries
    ADD CONSTRAINT queries_pkey PRIMARY KEY (id);


--
-- Name: query_executions query_executions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.query_executions
    ADD CONSTRAINT query_executions_pkey PRIMARY KEY (id);


--
-- Name: query_executions query_executions_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.query_executions
    ADD CONSTRAINT query_executions_slug_key UNIQUE (slug);


--
-- Name: query_parameters query_parameters_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.query_parameters
    ADD CONSTRAINT query_parameters_pkey PRIMARY KEY (id);


--
-- Name: query_parameters query_parameters_query_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.query_parameters
    ADD CONSTRAINT query_parameters_query_id_name_key UNIQUE (query_id, name);


--
-- Name: query_parameters query_parameters_query_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.query_parameters
    ADD CONSTRAINT query_parameters_query_id_slug_key UNIQUE (query_id, slug);


--
-- Name: river_client river_client_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.river_client
    ADD CONSTRAINT river_client_pkey PRIMARY KEY (id);


--
-- Name: river_client_queue river_client_queue_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.river_client_queue
    ADD CONSTRAINT river_client_queue_pkey PRIMARY KEY (river_client_id, name);


--
-- Name: river_job river_job_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.river_job
    ADD CONSTRAINT river_job_pkey PRIMARY KEY (id);


--
-- Name: river_leader river_leader_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.river_leader
    ADD CONSTRAINT river_leader_pkey PRIMARY KEY (name);


--
-- Name: river_migration river_migration_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.river_migration
    ADD CONSTRAINT river_migration_pkey PRIMARY KEY (line, version);


--
-- Name: river_queue river_queue_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.river_queue
    ADD CONSTRAINT river_queue_pkey PRIMARY KEY (name);


--
-- Name: role_permissions role_permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_pkey PRIMARY KEY (id);


--
-- Name: roles roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (id);


--
-- Name: schema_metadata schema_metadata_data_source_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_metadata
    ADD CONSTRAINT schema_metadata_data_source_id_key UNIQUE (data_source_id);


--
-- Name: schema_metadata schema_metadata_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_metadata
    ADD CONSTRAINT schema_metadata_pkey PRIMARY KEY (id);


--
-- Name: schema_metadata schema_metadata_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_metadata
    ADD CONSTRAINT schema_metadata_slug_key UNIQUE (slug);


--
-- Name: snowflake_wif_bindings snowflake_wif_bindings_organization_id_data_source_id_issue_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.snowflake_wif_bindings
    ADD CONSTRAINT snowflake_wif_bindings_organization_id_data_source_id_issue_key UNIQUE (organization_id, data_source_id, issuer, account);


--
-- Name: snowflake_wif_bindings snowflake_wif_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.snowflake_wif_bindings
    ADD CONSTRAINT snowflake_wif_bindings_pkey PRIMARY KEY (id);


--
-- Name: users unique_oidc_provider; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT unique_oidc_provider UNIQUE (oidc_provider, oidc_provider_id);


--
-- Name: alert_delivery_attempts uq_alert_delivery_attempts_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_delivery_attempts
    ADD CONSTRAINT uq_alert_delivery_attempts_slug UNIQUE (slug);


--
-- Name: alert_destinations uq_alert_destinations_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_destinations
    ADD CONSTRAINT uq_alert_destinations_org_id UNIQUE (organization_id, id);


--
-- Name: alert_destinations uq_alert_destinations_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_destinations
    ADD CONSTRAINT uq_alert_destinations_org_slug UNIQUE (organization_id, slug);


--
-- Name: alert_policies uq_alert_policies_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policies
    ADD CONSTRAINT uq_alert_policies_org_id UNIQUE (organization_id, id);


--
-- Name: alert_policies uq_alert_policies_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policies
    ADD CONSTRAINT uq_alert_policies_org_slug UNIQUE (organization_id, slug);


--
-- Name: alert_policy_destinations uq_alert_policy_destination; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policy_destinations
    ADD CONSTRAINT uq_alert_policy_destination UNIQUE (alert_policy_id, destination_id);


--
-- Name: api_keys uq_api_keys_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT uq_api_keys_org_slug UNIQUE (organization_id, slug);


--
-- Name: credential_oauth_grants uq_credential_oauth_grants_credential; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_grants
    ADD CONSTRAINT uq_credential_oauth_grants_credential UNIQUE (credential_id);


--
-- Name: credential_oauth_installs uq_credential_oauth_installs_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT uq_credential_oauth_installs_org_slug UNIQUE (organization_id, slug);


--
-- Name: credential_oauth_installs uq_credential_oauth_installs_state_hash; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT uq_credential_oauth_installs_state_hash UNIQUE (state_hash);


--
-- Name: credentials uq_credentials_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT uq_credentials_org_id UNIQUE (organization_id, id);


--
-- Name: credentials uq_credentials_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT uq_credentials_org_slug UNIQUE (organization_id, slug);


--
-- Name: data_source_bindings uq_data_source_bindings_project_environment_ref; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_bindings
    ADD CONSTRAINT uq_data_source_bindings_project_environment_ref UNIQUE (project_environment_id, data_source_ref_id);


--
-- Name: data_source_refs uq_data_source_refs_pipeline_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_refs
    ADD CONSTRAINT uq_data_source_refs_pipeline_id UNIQUE (pipeline_spec_id, id);


--
-- Name: data_source_refs uq_data_source_refs_pipeline_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_refs
    ADD CONSTRAINT uq_data_source_refs_pipeline_slug UNIQUE (pipeline_spec_id, slug);


--
-- Name: data_sources uq_data_sources_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_sources
    ADD CONSTRAINT uq_data_sources_org_id UNIQUE (organization_id, id);


--
-- Name: drift_impact_reports uq_drift_impact_reports_pipeline_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.drift_impact_reports
    ADD CONSTRAINT uq_drift_impact_reports_pipeline_slug UNIQUE (pipeline_spec_id, slug);


--
-- Name: environment_runtime_variables uq_environment_runtime_variables_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_runtime_variables
    ADD CONSTRAINT uq_environment_runtime_variables_key UNIQUE (environment_id, key);


--
-- Name: environment_secrets uq_environment_secrets_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_secrets
    ADD CONSTRAINT uq_environment_secrets_key UNIQUE (environment_id, key);


--
-- Name: environments uq_environments_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environments
    ADD CONSTRAINT uq_environments_org_id UNIQUE (organization_id, id);


--
-- Name: environments uq_environments_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environments
    ADD CONSTRAINT uq_environments_org_slug UNIQUE (organization_id, slug);


--
-- Name: events uq_events_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.events
    ADD CONSTRAINT uq_events_org_slug UNIQUE (organization_id, slug);


--
-- Name: github_app_repositories uq_github_app_repositories_provider_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_repositories
    ADD CONSTRAINT uq_github_app_repositories_provider_id UNIQUE (installation_id, repository_id);


--
-- Name: gitlab_proof_webhooks uq_gitlab_proof_webhooks_data_source; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gitlab_proof_webhooks
    ADD CONSTRAINT uq_gitlab_proof_webhooks_data_source UNIQUE (organization_id, data_source_id);


--
-- Name: notification_records uq_notification_records_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_records
    ADD CONSTRAINT uq_notification_records_org_id UNIQUE (organization_id, id);


--
-- Name: notification_records uq_notification_records_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_records
    ADD CONSTRAINT uq_notification_records_org_slug UNIQUE (organization_id, slug);


--
-- Name: odcs_contracts uq_odcs_contracts_identity; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contracts
    ADD CONSTRAINT uq_odcs_contracts_identity UNIQUE (project_id, odcs_id);


--
-- Name: odcs_contracts uq_odcs_contracts_scope; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contracts
    ADD CONSTRAINT uq_odcs_contracts_scope UNIQUE (organization_id, project_id, id);


--
-- Name: odcs_contracts uq_odcs_contracts_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contracts
    ADD CONSTRAINT uq_odcs_contracts_slug UNIQUE (project_id, slug);


--
-- Name: odcs_contract_revisions uq_odcs_revisions_digest; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contract_revisions
    ADD CONSTRAINT uq_odcs_revisions_digest UNIQUE (contract_id, digest);


--
-- Name: odcs_contract_revisions uq_odcs_revisions_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contract_revisions
    ADD CONSTRAINT uq_odcs_revisions_slug UNIQUE (contract_id, slug);


--
-- Name: organization_gitlab_connections uq_organization_gitlab_connections_org; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_gitlab_connections
    ADD CONSTRAINT uq_organization_gitlab_connections_org UNIQUE (organization_id);


--
-- Name: organization_gitlab_connections uq_organization_gitlab_connections_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_gitlab_connections
    ADD CONSTRAINT uq_organization_gitlab_connections_org_id UNIQUE (organization_id, id);


--
-- Name: organization_members uq_organization_member; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_members
    ADD CONSTRAINT uq_organization_member UNIQUE (organization_id, user_id);


--
-- Name: organization_member_invitations uq_organization_member_invitation_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_member_invitations
    ADD CONSTRAINT uq_organization_member_invitation_slug UNIQUE (organization_id, slug);


--
-- Name: organization_member_invitations uq_organization_member_invitations_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_member_invitations
    ADD CONSTRAINT uq_organization_member_invitations_org_id UNIQUE (organization_id, id);


--
-- Name: organization_members uq_organization_members_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_members
    ADD CONSTRAINT uq_organization_members_org_id UNIQUE (organization_id, id);


--
-- Name: organization_retention_runs uq_organization_retention_runs_job_attempt; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_retention_runs
    ADD CONSTRAINT uq_organization_retention_runs_job_attempt UNIQUE (organization_id, source_job_id, attempt);


--
-- Name: organization_retention_runs uq_organization_retention_runs_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_retention_runs
    ADD CONSTRAINT uq_organization_retention_runs_slug UNIQUE (organization_id, slug);


--
-- Name: personal_access_tokens uq_personal_access_tokens_user_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_access_tokens
    ADD CONSTRAINT uq_personal_access_tokens_user_slug UNIQUE (user_id, slug);


--
-- Name: pipeline_deployments uq_pipeline_deployments_project_pipeline_environment; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployments
    ADD CONSTRAINT uq_pipeline_deployments_project_pipeline_environment UNIQUE (project_environment_id, pipeline_slug);


--
-- Name: pipeline_incremental_checkpoint_locks uq_pipeline_incremental_checkpoint_locks_identity; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoint_locks
    ADD CONSTRAINT uq_pipeline_incremental_checkpoint_locks_identity UNIQUE (project_environment_id, pipeline_slug, source_step_slug, checkpoint_namespace, operation_identity);


--
-- Name: pipeline_proposal_authorities uq_pipeline_proposal_authorities_org_project_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_proposal_authorities
    ADD CONSTRAINT uq_pipeline_proposal_authorities_org_project_id UNIQUE (organization_id, project_id, id);


--
-- Name: pipeline_run_logical_closures uq_pipeline_run_logical_closures_root; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_logical_closures
    ADD CONSTRAINT uq_pipeline_run_logical_closures_root UNIQUE (project_environment_id, root_run_slug);


--
-- Name: pipeline_run_terminal_obligations uq_pipeline_run_terminal_obligations_attempt; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_terminal_obligations
    ADD CONSTRAINT uq_pipeline_run_terminal_obligations_attempt UNIQUE (project_environment_id, attempt_run_slug);


--
-- Name: pipeline_specs uq_pipeline_specs_project_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_specs
    ADD CONSTRAINT uq_pipeline_specs_project_id UNIQUE (project_id, id);


--
-- Name: pipeline_specs uq_pipeline_specs_project_id_id_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_specs
    ADD CONSTRAINT uq_pipeline_specs_project_id_id_slug UNIQUE (project_id, id, slug);


--
-- Name: pipeline_triggers uq_pipeline_triggers_id_project_environment; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_triggers
    ADD CONSTRAINT uq_pipeline_triggers_id_project_environment UNIQUE (id, project_environment_id);


--
-- Name: pipeline_triggers uq_pipeline_triggers_project_environment_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_triggers
    ADD CONSTRAINT uq_pipeline_triggers_project_environment_slug UNIQUE (project_environment_id, slug);


--
-- Name: pipeline_triggers uq_pipeline_triggers_project_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_triggers
    ADD CONSTRAINT uq_pipeline_triggers_project_id UNIQUE (project_id, id);


--
-- Name: pipeline_validation_results uq_pipeline_validation_results_project_environment_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_validation_results
    ADD CONSTRAINT uq_pipeline_validation_results_project_environment_slug UNIQUE (project_environment_id, validation_slug);


--
-- Name: pipeline_webhook_deliveries uq_pipeline_webhook_deliveries_environment_pipeline_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_deliveries
    ADD CONSTRAINT uq_pipeline_webhook_deliveries_environment_pipeline_id UNIQUE (project_environment_id, pipeline_slug, id);


--
-- Name: pipeline_webhook_deliveries uq_pipeline_webhook_deliveries_environment_run; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_deliveries
    ADD CONSTRAINT uq_pipeline_webhook_deliveries_environment_run UNIQUE (project_environment_id, run_slug);


--
-- Name: pipeline_webhook_deliveries uq_pipeline_webhook_deliveries_receiver_delivery; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_deliveries
    ADD CONSTRAINT uq_pipeline_webhook_deliveries_receiver_delivery UNIQUE (receiver_id, delivery_id);


--
-- Name: pipeline_webhook_secret_versions uq_pipeline_webhook_secret_versions_credential; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_secret_versions
    ADD CONSTRAINT uq_pipeline_webhook_secret_versions_credential UNIQUE (credential_id);


--
-- Name: pipeline_webhook_secret_versions uq_pipeline_webhook_secret_versions_trigger_version; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_secret_versions
    ADD CONSTRAINT uq_pipeline_webhook_secret_versions_trigger_version UNIQUE (pipeline_trigger_id, key_version);


--
-- Name: project_environments uq_project_environments_org_project_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_environments
    ADD CONSTRAINT uq_project_environments_org_project_id UNIQUE (organization_id, project_id, id);


--
-- Name: project_environments uq_project_environments_project_environment; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_environments
    ADD CONSTRAINT uq_project_environments_project_environment UNIQUE (project_id, environment_id);


--
-- Name: project_environments uq_project_environments_project_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_environments
    ADD CONSTRAINT uq_project_environments_project_id UNIQUE (project_id, id);


--
-- Name: projects uq_projects_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT uq_projects_org_id UNIQUE (organization_id, id);


--
-- Name: projects uq_projects_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT uq_projects_org_slug UNIQUE (organization_id, slug);


--
-- Name: proof_change_candidate_mappings uq_proof_candidate_mappings_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidate_mappings
    ADD CONSTRAINT uq_proof_candidate_mappings_org_id UNIQUE (organization_id, id);


--
-- Name: proof_change_candidate_mappings uq_proof_candidate_mappings_target; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidate_mappings
    ADD CONSTRAINT uq_proof_candidate_mappings_target UNIQUE (organization_id, project_id, project_environment_id, id);


--
-- Name: proof_change_candidates uq_proof_change_candidates_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidates
    ADD CONSTRAINT uq_proof_change_candidates_key UNIQUE (candidate_key);


--
-- Name: proof_change_candidates uq_proof_change_candidates_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidates
    ADD CONSTRAINT uq_proof_change_candidates_org_id UNIQUE (organization_id, id);


--
-- Name: proof_change_candidates uq_proof_change_candidates_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidates
    ADD CONSTRAINT uq_proof_change_candidates_org_slug UNIQUE (organization_id, slug);


--
-- Name: proof_change_candidates uq_proof_change_candidates_receipt_target; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidates
    ADD CONSTRAINT uq_proof_change_candidates_receipt_target UNIQUE (organization_id, mapping_id, id, spec_hash);


--
-- Name: proof_evidence uq_proof_evidence_candidate_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_evidence
    ADD CONSTRAINT uq_proof_evidence_candidate_id UNIQUE (organization_id, candidate_id, id);


--
-- Name: proof_evidence uq_proof_evidence_digest; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_evidence
    ADD CONSTRAINT uq_proof_evidence_digest UNIQUE (organization_id, candidate_id, digest);


--
-- Name: proof_lifecycle_links uq_proof_lifecycle_links_identity; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_lifecycle_links
    ADD CONSTRAINT uq_proof_lifecycle_links_identity UNIQUE (organization_id, project_environment_id, kind, identifier);


--
-- Name: proof_receipts uq_proof_receipts_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_receipts
    ADD CONSTRAINT uq_proof_receipts_key UNIQUE (organization_id, receipt_key);


--
-- Name: proof_receipts uq_proof_receipts_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_receipts
    ADD CONSTRAINT uq_proof_receipts_org_id UNIQUE (organization_id, id);


--
-- Name: proof_receipts uq_proof_receipts_org_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_receipts
    ADD CONSTRAINT uq_proof_receipts_org_slug UNIQUE (organization_id, slug);


--
-- Name: permissions uq_resource_action; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permissions
    ADD CONSTRAINT uq_resource_action UNIQUE (resource, action);


--
-- Name: role_permissions uq_role_permission; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT uq_role_permission UNIQUE (role_id, permission_id);


--
-- Name: roles uq_roles_org_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT uq_roles_org_id UNIQUE (organization_id, id);


--
-- Name: wishlist_registrations uq_wishlist_registrations_work_email; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wishlist_registrations
    ADD CONSTRAINT uq_wishlist_registrations_work_email UNIQUE (work_email);


--
-- Name: user_mfa_challenges user_mfa_challenges_challenge_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_mfa_challenges
    ADD CONSTRAINT user_mfa_challenges_challenge_token_hash_key UNIQUE (challenge_token_hash);


--
-- Name: user_mfa_challenges user_mfa_challenges_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_mfa_challenges
    ADD CONSTRAINT user_mfa_challenges_pkey PRIMARY KEY (id);


--
-- Name: user_sessions user_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_sessions
    ADD CONSTRAINT user_sessions_pkey PRIMARY KEY (k);


--
-- Name: users users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_email_key UNIQUE (email);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: users users_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_slug_key UNIQUE (slug);


--
-- Name: wishlist_registrations wishlist_registrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wishlist_registrations
    ADD CONSTRAINT wishlist_registrations_pkey PRIMARY KEY (id);


--
-- Name: abuse_control_buckets_window_started_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX abuse_control_buckets_window_started_at_idx ON public.abuse_control_buckets USING btree (window_started_at);


--
-- Name: execution_requests_data_source_admission_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX execution_requests_data_source_admission_idx ON public.execution_requests USING btree (data_source_id, state, created_at, id, lease_expires_at, deadline_at) WHERE ((data_source_id IS NOT NULL) AND (state = ANY (ARRAY['pending'::text, 'running'::text])));


--
-- Name: execution_requests_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX execution_requests_expiry_idx ON public.execution_requests USING btree (expires_at);


--
-- Name: execution_requests_initiating_user_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX execution_requests_initiating_user_idx ON public.execution_requests USING btree (initiating_user_id) WHERE (initiating_user_id IS NOT NULL);


--
-- Name: execution_requests_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX execution_requests_pending_idx ON public.execution_requests USING btree (state, lease_expires_at, deadline_at) WHERE (state = ANY (ARRAY['pending'::text, 'running'::text]));


--
-- Name: idx_alert_delivery_attempts_destination_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_delivery_attempts_destination_created ON public.alert_delivery_attempts USING btree (destination_id, created_at DESC);


--
-- Name: idx_alert_delivery_attempts_org_policy; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_delivery_attempts_org_policy ON public.alert_delivery_attempts USING btree (organization_id, alert_policy_id);


--
-- Name: idx_alert_delivery_attempts_record_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_delivery_attempts_record_created ON public.alert_delivery_attempts USING btree (notification_record_id, created_at DESC);


--
-- Name: idx_alert_delivery_attempts_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_delivery_attempts_status_created ON public.alert_delivery_attempts USING btree (status, created_at DESC);


--
-- Name: idx_alert_destinations_org_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_destinations_org_enabled ON public.alert_destinations USING btree (organization_id, enabled, slug);


--
-- Name: idx_alert_policies_org_scope_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_policies_org_scope_type ON public.alert_policies USING btree (organization_id, scope, notification_type, severity, lifecycle_state, enabled);


--
-- Name: idx_alert_policies_org_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_policies_org_source ON public.alert_policies USING btree (organization_id, source_kind, source_action, source_ref) WHERE ((source_kind IS NOT NULL) OR (source_action IS NOT NULL) OR (source_ref IS NOT NULL));


--
-- Name: idx_alert_policies_project_environment; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_policies_project_environment ON public.alert_policies USING btree (organization_id, project_id, environment_id, enabled);


--
-- Name: idx_alert_policy_destinations_destination; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_policy_destinations_destination ON public.alert_policy_destinations USING btree (organization_id, destination_id);


--
-- Name: idx_api_keys_org_created_by_member; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_org_created_by_member ON public.api_keys USING btree (organization_id, created_by_member_id);


--
-- Name: idx_api_keys_org_role_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_org_role_id ON public.api_keys USING btree (organization_id, role_id);


--
-- Name: idx_api_keys_token_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_api_keys_token_hash ON public.api_keys USING btree (token_hash);


--
-- Name: idx_credential_oauth_grants_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_oauth_grants_provider ON public.credential_oauth_grants USING btree (provider);


--
-- Name: idx_credential_oauth_installs_provider_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_oauth_installs_provider_state ON public.credential_oauth_installs USING btree (provider, state_hash);


--
-- Name: idx_credentials_org_kind_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_org_kind_status ON public.credentials USING btree (organization_id, kind, status);


--
-- Name: idx_data_source_bindings_data_source_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_source_bindings_data_source_id ON public.data_source_bindings USING btree (data_source_id);


--
-- Name: idx_data_source_bindings_pipeline; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_source_bindings_pipeline ON public.data_source_bindings USING btree (pipeline_spec_id);


--
-- Name: idx_data_source_bindings_project_env; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_source_bindings_project_env ON public.data_source_bindings USING btree (project_environment_id);


--
-- Name: idx_data_source_bindings_ref; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_source_bindings_ref ON public.data_source_bindings USING btree (data_source_ref_id);


--
-- Name: idx_data_source_refs_pipeline_spec; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_source_refs_pipeline_spec ON public.data_source_refs USING btree (pipeline_spec_id);


--
-- Name: idx_data_sources_org_deleted_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_sources_org_deleted_slug ON public.data_sources USING btree (organization_id, deleted_at, slug);


--
-- Name: idx_data_sources_organization_gitlab_connection; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_sources_organization_gitlab_connection ON public.data_sources USING btree (organization_id, organization_gitlab_connection_id) WHERE (organization_gitlab_connection_id IS NOT NULL);


--
-- Name: idx_data_sources_schema_import_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_sources_schema_import_status ON public.data_sources USING btree (organization_id, schema_import_status) WHERE (deleted_at IS NULL);


--
-- Name: idx_data_sources_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_sources_slug ON public.data_sources USING btree (slug);


--
-- Name: idx_drift_impact_reports_data_source_status_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_drift_impact_reports_data_source_status_created_at ON public.drift_impact_reports USING btree (data_source_id, status, created_at DESC) WHERE (data_source_id IS NOT NULL);


--
-- Name: idx_drift_impact_reports_pipeline_status_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_drift_impact_reports_pipeline_status_created_at ON public.drift_impact_reports USING btree (pipeline_spec_id, status, created_at DESC) WHERE (pipeline_spec_id IS NOT NULL);


--
-- Name: idx_environment_runtime_variables_org_environment; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_environment_runtime_variables_org_environment ON public.environment_runtime_variables USING btree (organization_id, environment_id);


--
-- Name: idx_environment_secrets_credential_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_environment_secrets_credential_id ON public.environment_secrets USING btree (credential_id);


--
-- Name: idx_environment_secrets_org_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_environment_secrets_org_credential ON public.environment_secrets USING btree (organization_id, credential_id);


--
-- Name: idx_environment_secrets_org_environment; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_environment_secrets_org_environment ON public.environment_secrets USING btree (organization_id, environment_id);


--
-- Name: idx_environments_org_deleted_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_environments_org_deleted_slug ON public.environments USING btree (organization_id, deleted_at, slug);


--
-- Name: idx_events_org_action_occurred_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_org_action_occurred_at ON public.events USING btree (organization_id, action, occurred_at DESC);


--
-- Name: idx_events_org_actor_occurred_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_org_actor_occurred_at ON public.events USING btree (organization_id, actor_type, actor_slug, occurred_at DESC);


--
-- Name: idx_events_org_class_occurred_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_org_class_occurred_at ON public.events USING btree (organization_id, event_class, occurred_at DESC, slug);


--
-- Name: idx_events_org_entity_occurred_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_org_entity_occurred_at ON public.events USING btree (organization_id, entity_type, entity_slug, occurred_at DESC);


--
-- Name: idx_events_org_occurred_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_org_occurred_at ON public.events USING btree (organization_id, occurred_at DESC, slug);


--
-- Name: idx_events_org_request_occurred_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_org_request_occurred_at ON public.events USING btree (organization_id, request_id, occurred_at DESC, slug) WHERE (request_id IS NOT NULL);


--
-- Name: idx_events_proof_receipt_deletion; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_proof_receipt_deletion ON public.events USING btree (organization_id, ((metadata ->> 'proof_receipt_key'::text))) WHERE ((event_class = 'security_audit'::text) AND (action = 'delete'::text));


--
-- Name: idx_events_retention_occurred_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_retention_occurred_at ON public.events USING btree (occurred_at);


--
-- Name: idx_execution_requests_org_data_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_execution_requests_org_data_source ON public.execution_requests USING btree (organization_id, data_source_id);


--
-- Name: idx_github_app_install_intents_org_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_app_install_intents_org_created ON public.github_app_install_intents USING btree (organization_id, created_at DESC);


--
-- Name: idx_github_app_install_intents_pending_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_app_install_intents_pending_expiry ON public.github_app_install_intents USING btree (expires_at) WHERE (consumed_at IS NULL);


--
-- Name: idx_github_app_repositories_org_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_github_app_repositories_org_active ON public.github_app_repositories USING btree (organization_id, removed_at, full_name);


--
-- Name: idx_notification_records_org_lifecycle_seen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_records_org_lifecycle_seen ON public.notification_records USING btree (organization_id, lifecycle_state, last_seen_at DESC, slug);


--
-- Name: idx_notification_records_org_source_event; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_records_org_source_event ON public.notification_records USING btree (organization_id, source_event_slug) WHERE (source_event_slug IS NOT NULL);


--
-- Name: idx_notification_records_org_type_seen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_records_org_type_seen ON public.notification_records USING btree (organization_id, notification_type, last_seen_at DESC, slug);


--
-- Name: idx_notification_records_project_environment; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notification_records_project_environment ON public.notification_records USING btree (organization_id, project_id, environment_id, lifecycle_state);


--
-- Name: idx_odcs_contracts_page; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_odcs_contracts_page ON public.odcs_contracts USING btree (project_id, created_at, slug);


--
-- Name: idx_odcs_revisions_page; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_odcs_revisions_page ON public.odcs_contract_revisions USING btree (contract_id, created_at, slug);


--
-- Name: idx_organization_member_invitations_email_status_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_organization_member_invitations_email_status_expires_at ON public.organization_member_invitations USING btree (email, status, expires_at);


--
-- Name: idx_organization_member_invitations_org_accepted_member; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_organization_member_invitations_org_accepted_member ON public.organization_member_invitations USING btree (organization_id, accepted_member_id);


--
-- Name: idx_organization_member_invitations_org_role; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_organization_member_invitations_org_role ON public.organization_member_invitations USING btree (organization_id, role_id);


--
-- Name: idx_organization_member_invitations_org_status_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_organization_member_invitations_org_status_created_at ON public.organization_member_invitations USING btree (organization_id, status, created_at DESC, slug);


--
-- Name: idx_organization_member_invitations_pending_email; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_organization_member_invitations_pending_email ON public.organization_member_invitations USING btree (organization_id, email) WHERE ((status)::text = 'pending'::text);


--
-- Name: idx_organization_member_invitations_token_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_organization_member_invitations_token_hash ON public.organization_member_invitations USING btree (token_hash);


--
-- Name: idx_organization_members_org_role; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_organization_members_org_role ON public.organization_members USING btree (organization_id, role_id);


--
-- Name: idx_organization_retention_runs_org_completed_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_organization_retention_runs_org_completed_at ON public.organization_retention_runs USING btree (organization_id, completed_at);


--
-- Name: idx_organization_retention_runs_org_started_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_organization_retention_runs_org_started_at ON public.organization_retention_runs USING btree (organization_id, started_at DESC, slug);


--
-- Name: idx_personal_access_tokens_token_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_personal_access_tokens_token_hash ON public.personal_access_tokens USING btree (token_hash);


--
-- Name: idx_personal_access_tokens_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_personal_access_tokens_user_id ON public.personal_access_tokens USING btree (user_id);


--
-- Name: idx_pipeline_checkpoint_retention_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_checkpoint_retention_created_at ON public.pipeline_checkpoint USING btree (created_at);


--
-- Name: idx_pipeline_completion_retry_root; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_completion_retry_root ON public.pipeline_trigger_retry_attempts USING btree (project_environment_id, root_run_slug, retry_of_run_slug) WHERE (source_type = 'completion'::text);


--
-- Name: idx_pipeline_completion_snapshots_child; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_completion_snapshots_child ON public.pipeline_completion_snapshots USING btree (project_environment_id, child_run_slug) WHERE (child_run_slug IS NOT NULL);


--
-- Name: idx_pipeline_completion_snapshots_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_completion_snapshots_due ON public.pipeline_completion_snapshots USING btree (next_evaluation_at, id) WHERE (state = 'pending'::text);


--
-- Name: idx_pipeline_completion_snapshots_open_root; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_completion_snapshots_open_root ON public.pipeline_completion_snapshots USING btree (project_environment_id, root_run_slug) WHERE (completed_at IS NULL);


--
-- Name: idx_pipeline_completion_snapshots_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_completion_snapshots_pending ON public.pipeline_completion_snapshots USING btree (id) WHERE (completed_at IS NULL);


--
-- Name: idx_pipeline_completion_snapshots_pending_organization; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_completion_snapshots_pending_organization ON public.pipeline_completion_snapshots USING btree (organization_id, closure_id) WHERE (state = 'pending'::text);


--
-- Name: idx_pipeline_completion_snapshots_pending_trigger; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_completion_snapshots_pending_trigger ON public.pipeline_completion_snapshots USING btree (project_environment_id, trigger_id) WHERE (state = 'pending'::text);


--
-- Name: idx_pipeline_deployments_current_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_deployments_current_version ON public.pipeline_deployments USING btree (current_version_id);


--
-- Name: idx_pipeline_deployments_previous_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_deployments_previous_version ON public.pipeline_deployments USING btree (previous_version_id);


--
-- Name: idx_pipeline_drafts_authority; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_drafts_authority ON public.pipeline_drafts USING btree (proposal_authority_id);


--
-- Name: idx_pipeline_drafts_based_on_version_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_drafts_based_on_version_id ON public.pipeline_drafts USING btree (based_on_version_id);


--
-- Name: idx_pipeline_drafts_member; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_drafts_member ON public.pipeline_drafts USING btree (member_id);


--
-- Name: idx_pipeline_incremental_checkpoints_current_committed; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_pipeline_incremental_checkpoints_current_committed ON public.pipeline_incremental_checkpoints USING btree (project_environment_id, pipeline_slug, source_step_slug, checkpoint_namespace, operation_identity) WHERE ((commit_status = 'committed'::text) AND (superseded_at IS NULL) AND (expired_at IS NULL));


--
-- Name: idx_pipeline_incremental_checkpoints_project_env_pipeline_sourc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_incremental_checkpoints_project_env_pipeline_sourc ON public.pipeline_incremental_checkpoints USING btree (project_environment_id, pipeline_slug, source_step_slug, created_at DESC);


--
-- Name: idx_pipeline_incremental_checkpoints_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_incremental_checkpoints_version ON public.pipeline_incremental_checkpoints USING btree (pipeline_version_id);


--
-- Name: idx_pipeline_references_current_version_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_references_current_version_id ON public.pipeline_references USING btree (current_version_id);


--
-- Name: idx_pipeline_references_has_draft; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_references_has_draft ON public.pipeline_references USING btree (has_draft);


--
-- Name: idx_pipeline_run_terminal_obligations_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_run_terminal_obligations_due ON public.pipeline_run_terminal_obligations USING btree (next_evaluation_at, id) WHERE ((resolved_at IS NULL) AND (NOT excluded) AND (NOT outcome_uncertain));


--
-- Name: idx_pipeline_run_terminal_obligations_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_run_terminal_obligations_pending ON public.pipeline_run_terminal_obligations USING btree (id) WHERE ((resolved_at IS NULL) AND (NOT excluded) AND (NOT outcome_uncertain) AND (attempt_status = 'failed'::text));


--
-- Name: idx_pipeline_run_terminal_obligations_root; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_run_terminal_obligations_root ON public.pipeline_run_terminal_obligations USING btree (project_environment_id, root_run_slug, created_at);


--
-- Name: idx_pipeline_runs_pipeline_version_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_runs_pipeline_version_id ON public.pipeline_runs USING btree (pipeline_version_id);


--
-- Name: idx_pipeline_runs_project_environment_backfill_series_created_a; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_runs_project_environment_backfill_series_created_a ON public.pipeline_runs USING btree (project_environment_id, backfill_series_slug, created_at DESC);


--
-- Name: idx_pipeline_runs_project_environment_mode_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_runs_project_environment_mode_created_at ON public.pipeline_runs USING btree (project_environment_id, run_mode, created_at DESC);


--
-- Name: idx_pipeline_runs_project_environment_pipeline_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_runs_project_environment_pipeline_created_at ON public.pipeline_runs USING btree (project_environment_id, pipeline_slug, created_at DESC);


--
-- Name: idx_pipeline_runs_project_environment_root_run_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_runs_project_environment_root_run_created_at ON public.pipeline_runs USING btree (project_environment_id, source_root_run_slug, created_at DESC);


--
-- Name: idx_pipeline_runs_retention_ended_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_runs_retention_ended_at ON public.pipeline_runs USING btree (ended_at) WHERE (ended_at IS NOT NULL);


--
-- Name: idx_pipeline_runs_run_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_runs_run_slug ON public.pipeline_runs USING btree (run_slug);


--
-- Name: idx_pipeline_runs_worker_job; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_runs_worker_job ON public.pipeline_runs USING btree (worker_job_id) WHERE (worker_job_id IS NOT NULL);


--
-- Name: idx_pipeline_schedule_fires_finalizing; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_schedule_fires_finalizing ON public.pipeline_schedule_fires USING btree (project_environment_id, fire_slug) WHERE (status = 'running'::text);


--
-- Name: idx_pipeline_schedule_fires_project_environment_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_schedule_fires_project_environment_created_at ON public.pipeline_schedule_fires USING btree (project_environment_id, created_at DESC);


--
-- Name: idx_pipeline_schedule_fires_trigger_scheduled_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_schedule_fires_trigger_scheduled_at ON public.pipeline_schedule_fires USING btree (pipeline_trigger_id, scheduled_fire_at DESC);


--
-- Name: idx_pipeline_sink_effects_project_environment_run_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_sink_effects_project_environment_run_state ON public.pipeline_sink_effects USING btree (project_environment_id, run_slug, state);


--
-- Name: idx_pipeline_specs_current_version_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_specs_current_version_id ON public.pipeline_specs USING btree (current_version_id);


--
-- Name: idx_pipeline_specs_project_deleted_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_specs_project_deleted_slug ON public.pipeline_specs USING btree (project_id, deleted_at, slug);


--
-- Name: idx_pipeline_trigger_retry_attempts_finalizing; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_trigger_retry_attempts_finalizing ON public.pipeline_trigger_retry_attempts USING btree (project_environment_id, run_slug) WHERE (status = 'running'::text);


--
-- Name: idx_pipeline_trigger_retry_attempts_prior_decision; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_trigger_retry_attempts_prior_decision ON public.pipeline_trigger_retry_attempts USING btree (project_environment_id, root_run_slug, retry_of_run_slug);


--
-- Name: idx_pipeline_trigger_retry_attempts_project_environment_root_cr; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_trigger_retry_attempts_project_environment_root_cr ON public.pipeline_trigger_retry_attempts USING btree (project_environment_id, root_run_slug, created_at DESC);


--
-- Name: idx_pipeline_trigger_retry_attempts_project_environment_status_; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_trigger_retry_attempts_project_environment_status_ ON public.pipeline_trigger_retry_attempts USING btree (project_environment_id, status, scheduled_at);


--
-- Name: idx_pipeline_trigger_retry_attempts_trigger_scheduled_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_trigger_retry_attempts_trigger_scheduled_at ON public.pipeline_trigger_retry_attempts USING btree (pipeline_trigger_id, scheduled_at DESC);


--
-- Name: idx_pipeline_triggers_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_triggers_enabled ON public.pipeline_triggers USING btree (project_environment_id, trigger_type, enabled);


--
-- Name: idx_pipeline_triggers_pipeline; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_triggers_pipeline ON public.pipeline_triggers USING btree (pipeline_id);


--
-- Name: idx_pipeline_triggers_project_environment_created_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_triggers_project_environment_created_slug ON public.pipeline_triggers USING btree (project_environment_id, created_at DESC, slug DESC);


--
-- Name: idx_pipeline_validation_results_pipeline; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_validation_results_pipeline ON public.pipeline_validation_results USING btree (pipeline_id);


--
-- Name: idx_pipeline_validation_results_project_environment_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_validation_results_project_environment_created_at ON public.pipeline_validation_results USING btree (project_environment_id, created_at DESC, validation_slug DESC);


--
-- Name: idx_pipeline_validation_results_project_pipeline_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_validation_results_project_pipeline_created_at ON public.pipeline_validation_results USING btree (project_environment_id, pipeline_slug, created_at DESC, validation_slug DESC);


--
-- Name: idx_pipeline_validation_results_report; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_validation_results_report ON public.pipeline_validation_results USING btree (impact_pipeline_id, impact_report_slug);


--
-- Name: idx_pipeline_validation_results_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_validation_results_version ON public.pipeline_validation_results USING btree (pipeline_version_id);


--
-- Name: idx_pipeline_versions_project_slug_commit_nonnull; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_pipeline_versions_project_slug_commit_nonnull ON public.pipeline_versions USING btree (project_id, pipeline_slug, git_commit_sha) WHERE (git_commit_sha IS NOT NULL);


--
-- Name: idx_pipeline_versions_project_slug_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_versions_project_slug_created_at ON public.pipeline_versions USING btree (project_id, pipeline_slug, created_at DESC);


--
-- Name: idx_pipeline_versions_project_slug_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_pipeline_versions_project_slug_id ON public.pipeline_versions USING btree (project_id, pipeline_slug, id);


--
-- Name: idx_pipeline_versions_project_slug_spec_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_pipeline_versions_project_slug_spec_hash ON public.pipeline_versions USING btree (project_id, pipeline_slug, spec_hash) WHERE (spec_hash <> ''::text);


--
-- Name: idx_pipeline_webhook_deliveries_envelope_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_webhook_deliveries_envelope_expiry ON public.pipeline_webhook_deliveries USING btree (envelope_expires_at) WHERE (encrypted_envelope IS NOT NULL);


--
-- Name: idx_pipeline_webhook_deliveries_finalizing; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_webhook_deliveries_finalizing ON public.pipeline_webhook_deliveries USING btree (project_environment_id, run_slug) WHERE (status = 'running'::text);


--
-- Name: idx_pipeline_webhook_deliveries_trigger_received_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_webhook_deliveries_trigger_received_at ON public.pipeline_webhook_deliveries USING btree (pipeline_trigger_id, received_at DESC);


--
-- Name: idx_pipeline_webhook_secret_versions_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pipeline_webhook_secret_versions_enabled ON public.pipeline_webhook_secret_versions USING btree (pipeline_trigger_id, key_version) WHERE enabled;


--
-- Name: idx_project_environments_environment_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_environments_environment_id ON public.project_environments USING btree (environment_id);


--
-- Name: idx_project_environments_org_environment; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_environments_org_environment ON public.project_environments USING btree (organization_id, environment_id);


--
-- Name: idx_project_environments_org_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_environments_org_project ON public.project_environments USING btree (organization_id, project_id);


--
-- Name: idx_project_environments_project_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_environments_project_deleted ON public.project_environments USING btree (project_id, deleted_at);


--
-- Name: idx_projects_org_backend_data_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_projects_org_backend_data_source ON public.projects USING btree (organization_id, pipeline_backend_data_source_id);


--
-- Name: idx_projects_pipeline_backend_data_source_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_projects_pipeline_backend_data_source_id ON public.projects USING btree (pipeline_backend_data_source_id);


--
-- Name: idx_proof_candidate_mappings_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_candidate_mappings_lookup ON public.proof_change_candidate_mappings USING btree (provider, provider_connection_id, repository_id, target_ref, pipeline_path);


--
-- Name: idx_proof_change_candidates_change_request; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_change_candidates_change_request ON public.proof_change_candidates USING btree (mapping_id, change_request_id, created_at DESC, id DESC);


--
-- Name: idx_proof_lifecycle_links_deployment; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_lifecycle_links_deployment ON public.proof_lifecycle_links USING btree (deployment_id, organization_id) WHERE (kind = 'deployment'::text);


--
-- Name: idx_proof_lifecycle_links_receipt; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_lifecycle_links_receipt ON public.proof_lifecycle_links USING btree (organization_id, receipt_id, linked_at, id);


--
-- Name: idx_proof_lifecycle_links_run; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_lifecycle_links_run ON public.proof_lifecycle_links USING btree (run_id, organization_id) WHERE (kind = 'run'::text);


--
-- Name: idx_proof_receipts_evidence; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_receipts_evidence ON public.proof_receipts USING btree (organization_id, evidence_id);


--
-- Name: idx_proof_receipts_latest_change; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_receipts_latest_change ON public.proof_receipts USING btree (organization_id, mapping_id, created_at DESC, id DESC);


--
-- Name: idx_proof_receipts_retention; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_receipts_retention ON public.proof_receipts USING btree (organization_id, created_at, id);


--
-- Name: idx_proof_receipts_supersedes; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_receipts_supersedes ON public.proof_receipts USING btree (organization_id, supersedes_receipt_id) WHERE (supersedes_receipt_id IS NOT NULL);


--
-- Name: idx_proof_receipts_target; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_proof_receipts_target ON public.proof_receipts USING btree (organization_id, project_id, project_environment_id, candidate_id, created_at DESC, id DESC);


--
-- Name: idx_queries_data_source_deleted_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_queries_data_source_deleted_slug ON public.queries USING btree (data_source_id, deleted_at, slug);


--
-- Name: idx_queries_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_queries_slug ON public.queries USING btree (slug);


--
-- Name: idx_query_executions_retention_executed_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_query_executions_retention_executed_at ON public.query_executions USING btree (executed_at);


--
-- Name: idx_query_parameters_query_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_query_parameters_query_slug ON public.query_parameters USING btree (slug);


--
-- Name: idx_roles_org_deleted_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_roles_org_deleted_slug ON public.roles USING btree (organization_id, deleted_at, slug);


--
-- Name: idx_roles_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_roles_slug ON public.roles USING btree (slug);


--
-- Name: idx_user_mfa_challenges_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_mfa_challenges_user_id ON public.user_mfa_challenges USING btree (user_id);


--
-- Name: idx_users_password_reset_token_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_password_reset_token_hash ON public.users USING btree (password_reset_token_hash);


--
-- Name: idx_wishlist_registrations_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wishlist_registrations_created_at ON public.wishlist_registrations USING btree (created_at DESC, id DESC);


--
-- Name: idx_wishlist_registrations_updated_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wishlist_registrations_updated_at ON public.wishlist_registrations USING btree (updated_at);


--
-- Name: pipeline_run_recency_episodes_active; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX pipeline_run_recency_episodes_active ON public.pipeline_run_recency_episodes USING btree (expectation_id, deployment_id, deployment_revision) WHERE (resolved_at IS NULL);


--
-- Name: proof_observation_admission_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX proof_observation_admission_idx ON public.proof_observation_jobs USING btree (organization_id, project_environment_id, connector_type, operation, budget_expires_at);


--
-- Name: proof_observation_retention_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX proof_observation_retention_idx ON public.proof_observation_jobs USING btree (metadata_retain_until);


--
-- Name: river_job_args_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX river_job_args_index ON public.river_job USING gin (args);


--
-- Name: river_job_kind; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX river_job_kind ON public.river_job USING btree (kind);


--
-- Name: river_job_metadata_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX river_job_metadata_index ON public.river_job USING gin (metadata);


--
-- Name: river_job_prioritized_fetching_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX river_job_prioritized_fetching_index ON public.river_job USING btree (state, queue, priority, scheduled_at, id);


--
-- Name: river_job_state_and_finalized_at_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX river_job_state_and_finalized_at_index ON public.river_job USING btree (state, finalized_at) WHERE (finalized_at IS NOT NULL);


--
-- Name: river_job_unique_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX river_job_unique_idx ON public.river_job USING btree (unique_key) WHERE ((unique_key IS NOT NULL) AND (unique_states IS NOT NULL) AND public.river_job_state_in_bitmask(unique_states, state));


--
-- Name: snowflake_wif_bindings_reconcile_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX snowflake_wif_bindings_reconcile_idx ON public.snowflake_wif_bindings USING btree (reconcile_after, id);


--
-- Name: uq_github_app_installations_org_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_github_app_installations_org_id ON public.github_app_installations USING btree (organization_id, id);


--
-- Name: uq_notification_records_open_dedupe; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_notification_records_open_dedupe ON public.notification_records USING btree (organization_id, dedupe_fingerprint) WHERE (lifecycle_state <> 'resolved'::text);


--
-- Name: uq_organization_role_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_organization_role_slug ON public.roles USING btree (organization_id, slug) WHERE (deleted_at IS NULL);


--
-- Name: uq_pipeline_completion_child; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_pipeline_completion_child ON public.pipeline_completion_snapshots USING btree (organization_id, project_environment_id, child_run_slug) WHERE (child_run_slug IS NOT NULL);


--
-- Name: uq_pipeline_proposal_authorities_active_project; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_pipeline_proposal_authorities_active_project ON public.pipeline_proposal_authorities USING btree (organization_id, project_id) WHERE (revoked_at IS NULL);


--
-- Name: uq_pipeline_triggers_receiver_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_pipeline_triggers_receiver_id ON public.pipeline_triggers USING btree (receiver_id) WHERE (receiver_id IS NOT NULL);


--
-- Name: uq_project_environments_project_default; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_project_environments_project_default ON public.project_environments USING btree (project_id) WHERE ((is_default = true) AND (deleted_at IS NULL));


--
-- Name: uq_project_repository_setup_active; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_project_repository_setup_active ON public.project_repository_setups USING btree (project_id) WHERE (abandoned_at IS NULL);


--
-- Name: uq_projects_org_pipeline_backend_repo_branch; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_projects_org_pipeline_backend_repo_branch ON public.projects USING btree (organization_id, pipeline_backend_repo, pipeline_backend_tracked_branch) WHERE ((pipeline_backend_repo IS NOT NULL) AND (pipeline_backend_tracked_branch IS NOT NULL));


--
-- Name: uq_slack_notification_delivery; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_slack_notification_delivery ON public.alert_delivery_attempts USING btree (organization_id, notification_record_id, destination_id) WHERE (destination_type = 'slack'::text);


--
-- Name: user_sessions_e_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_sessions_e_idx ON public.user_sessions USING btree (e);


--
-- Name: data_sources invalidate_data_source_proof; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER invalidate_data_source_proof BEFORE UPDATE OF type, config, deleted_at ON public.data_sources FOR EACH ROW EXECUTE FUNCTION public.invalidate_data_source_proof();


--
-- Name: odcs_artifacts odcs_artifact_immutable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER odcs_artifact_immutable BEFORE UPDATE ON public.odcs_artifacts FOR EACH ROW EXECUTE FUNCTION public.protect_odcs_artifact();


--
-- Name: odcs_contracts odcs_contract_identity_immutable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER odcs_contract_identity_immutable BEFORE UPDATE ON public.odcs_contracts FOR EACH ROW EXECUTE FUNCTION public.protect_odcs_catalog_identity();


--
-- Name: odcs_contract_revisions odcs_revision_immutable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER odcs_revision_immutable BEFORE UPDATE ON public.odcs_contract_revisions FOR EACH ROW EXECUTE FUNCTION public.protect_odcs_catalog_identity();


--
-- Name: pipeline_completion_snapshots pipeline_completion_snapshots_cause_immutable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_completion_snapshots_cause_immutable BEFORE UPDATE ON public.pipeline_completion_snapshots FOR EACH ROW EXECUTE FUNCTION public.reject_pipeline_completion_cause_rewrite();


--
-- Name: pipeline_completion_snapshots pipeline_completion_snapshots_execution_immutable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_completion_snapshots_execution_immutable BEFORE UPDATE OF execution_args ON public.pipeline_completion_snapshots FOR EACH ROW EXECUTE FUNCTION public.reject_pipeline_completion_execution_rewrite();


--
-- Name: pipeline_completion_snapshots pipeline_completion_snapshots_fence_redelivery; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_completion_snapshots_fence_redelivery BEFORE INSERT ON public.pipeline_completion_snapshots FOR EACH ROW EXECUTE FUNCTION public.fence_pipeline_completion_redelivery();


--
-- Name: pipeline_completion_snapshots pipeline_completion_snapshots_immutable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_completion_snapshots_immutable BEFORE UPDATE ON public.pipeline_completion_snapshots FOR EACH ROW EXECUTE FUNCTION public.reject_pipeline_completion_snapshot_rewrite();


--
-- Name: pipeline_completion_snapshots pipeline_completion_snapshots_release_capacity; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_completion_snapshots_release_capacity AFTER UPDATE OF state ON public.pipeline_completion_snapshots FOR EACH ROW WHEN (((old.state = 'pending'::text) AND (new.state <> 'pending'::text))) EXECUTE FUNCTION public.release_pipeline_completion_capacity();


--
-- Name: pipeline_completion_trigger_revisions pipeline_completion_trigger_revisions_immutable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_completion_trigger_revisions_immutable BEFORE UPDATE ON public.pipeline_completion_trigger_revisions FOR EACH ROW EXECUTE FUNCTION public.reject_pipeline_completion_revision_rewrite();


--
-- Name: pipeline_trigger_retry_attempts pipeline_retry_completion_execution_immutable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_retry_completion_execution_immutable BEFORE UPDATE OF completion_execution_args ON public.pipeline_trigger_retry_attempts FOR EACH ROW EXECUTE FUNCTION public.reject_completion_retry_execution_rewrite();


--
-- Name: pipeline_run_logical_closures pipeline_run_logical_closures_reserve_capacity; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_run_logical_closures_reserve_capacity BEFORE INSERT ON public.pipeline_run_logical_closures FOR EACH ROW EXECUTE FUNCTION public.reserve_pipeline_completion_capacity();


--
-- Name: pipeline_run_logical_closures pipeline_run_logical_closures_resolve_obligations; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_run_logical_closures_resolve_obligations AFTER INSERT ON public.pipeline_run_logical_closures FOR EACH ROW EXECUTE FUNCTION public.resolve_pipeline_run_terminal_obligations();


--
-- Name: pipeline_run_logical_closures pipeline_run_logical_closures_snapshot_targets; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_run_logical_closures_snapshot_targets AFTER INSERT ON public.pipeline_run_logical_closures FOR EACH ROW EXECUTE FUNCTION public.snapshot_pipeline_completion_targets();


--
-- Name: pipeline_run_logical_closures pipeline_run_logical_closures_z_release_capacity; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_run_logical_closures_z_release_capacity AFTER INSERT ON public.pipeline_run_logical_closures FOR EACH ROW EXECUTE FUNCTION public.release_pipeline_completion_capacity();


--
-- Name: pipeline_runs pipeline_runs_completion_retry_state; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_runs_completion_retry_state AFTER UPDATE OF status ON public.pipeline_runs FOR EACH ROW EXECUTE FUNCTION public.synchronize_pipeline_completion_retry_run();


--
-- Name: pipeline_runs pipeline_runs_reject_deployment_rewrite; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_runs_reject_deployment_rewrite BEFORE UPDATE OF deployment_id, deployment_revision ON public.pipeline_runs FOR EACH ROW EXECUTE FUNCTION public.reject_pipeline_run_deployment_rewrite();


--
-- Name: pipeline_runs pipeline_runs_reject_terminal_rewrite; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_runs_reject_terminal_rewrite BEFORE UPDATE ON public.pipeline_runs FOR EACH ROW EXECUTE FUNCTION public.reject_pipeline_run_terminal_rewrite();


--
-- Name: pipeline_runs pipeline_runs_terminal_obligation; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_runs_terminal_obligation AFTER INSERT OR UPDATE OF status ON public.pipeline_runs FOR EACH ROW EXECUTE FUNCTION public.record_pipeline_run_terminal_obligation();


--
-- Name: pipeline_trigger_retry_attempts pipeline_trigger_retry_attempts_close_exhausted_root; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_trigger_retry_attempts_close_exhausted_root AFTER INSERT ON public.pipeline_trigger_retry_attempts FOR EACH ROW EXECUTE FUNCTION public.close_exhausted_schedule_run_root();


--
-- Name: pipeline_trigger_retry_attempts pipeline_trigger_retry_attempts_fence_root; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_trigger_retry_attempts_fence_root BEFORE INSERT ON public.pipeline_trigger_retry_attempts FOR EACH ROW EXECUTE FUNCTION public.fence_schedule_run_retry();


--
-- Name: pipeline_triggers pipeline_triggers_record_completion_revision; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_triggers_record_completion_revision AFTER INSERT OR DELETE OR UPDATE ON public.pipeline_triggers FOR EACH ROW EXECUTE FUNCTION public.record_pipeline_completion_trigger_revision();


--
-- Name: pipeline_triggers pipeline_triggers_suppress_pending_completions; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_triggers_suppress_pending_completions AFTER DELETE OR UPDATE ON public.pipeline_triggers FOR EACH ROW WHEN ((old.trigger_type = 'completion'::text)) EXECUTE FUNCTION public.suppress_pending_pipeline_completions();


--
-- Name: pipeline_webhook_deliveries pipeline_webhook_deliveries_close_final_root; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_webhook_deliveries_close_final_root AFTER UPDATE OF status ON public.pipeline_webhook_deliveries FOR EACH ROW EXECUTE FUNCTION public.close_final_webhook_run_root();


--
-- Name: pipeline_webhook_deliveries pipeline_webhook_deliveries_fence_root; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_webhook_deliveries_fence_root BEFORE UPDATE ON public.pipeline_webhook_deliveries FOR EACH ROW EXECUTE FUNCTION public.fence_webhook_run_retry();


--
-- Name: river_job preserve_pending_account_job; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER preserve_pending_account_job BEFORE DELETE ON public.river_job FOR EACH ROW WHEN (((old.state = 'cancelled'::public.river_job_state) AND (old.attempted_at IS NOT NULL))) EXECUTE FUNCTION public.preserve_pending_account_job();


--
-- Name: account_deletion_requests account_deletion_requests_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_deletion_requests
    ADD CONSTRAINT account_deletion_requests_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: alert_destinations alert_destinations_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_destinations
    ADD CONSTRAINT alert_destinations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: alert_policies alert_policies_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policies
    ADD CONSTRAINT alert_policies_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: alert_policy_destinations alert_policy_destinations_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policy_destinations
    ADD CONSTRAINT alert_policy_destinations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: api_keys api_keys_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: credential_http_oauth_apps credential_http_oauth_apps_organization_id_data_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_http_oauth_apps
    ADD CONSTRAINT credential_http_oauth_apps_organization_id_data_source_id_fkey FOREIGN KEY (organization_id, data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE CASCADE;


--
-- Name: credential_oauth_grants credential_oauth_grants_credential_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_grants
    ADD CONSTRAINT credential_oauth_grants_credential_id_fkey FOREIGN KEY (credential_id) REFERENCES public.credentials(id) ON DELETE CASCADE;


--
-- Name: credential_oauth_installs credential_oauth_installs_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT credential_oauth_installs_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: credentials credentials_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: data_source_refs data_source_refs_pipeline_spec_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_refs
    ADD CONSTRAINT data_source_refs_pipeline_spec_id_fkey FOREIGN KEY (pipeline_spec_id) REFERENCES public.pipeline_specs(id) ON DELETE CASCADE;


--
-- Name: data_sources data_sources_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_sources
    ADD CONSTRAINT data_sources_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: drift_impact_reports drift_impact_reports_data_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.drift_impact_reports
    ADD CONSTRAINT drift_impact_reports_data_source_id_fkey FOREIGN KEY (data_source_id) REFERENCES public.data_sources(id) ON DELETE CASCADE;


--
-- Name: drift_impact_reports drift_impact_reports_pipeline_spec_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.drift_impact_reports
    ADD CONSTRAINT drift_impact_reports_pipeline_spec_id_fkey FOREIGN KEY (pipeline_spec_id) REFERENCES public.pipeline_specs(id) ON DELETE CASCADE;


--
-- Name: environment_runtime_variables environment_runtime_variables_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_runtime_variables
    ADD CONSTRAINT environment_runtime_variables_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: environments environments_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environments
    ADD CONSTRAINT environments_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: events events_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.events
    ADD CONSTRAINT events_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: execution_requests execution_requests_initiating_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.execution_requests
    ADD CONSTRAINT execution_requests_initiating_user_id_fkey FOREIGN KEY (initiating_user_id) REFERENCES public.users(id) ON DELETE RESTRICT;


--
-- Name: execution_requests execution_requests_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.execution_requests
    ADD CONSTRAINT execution_requests_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE RESTRICT;


--
-- Name: execution_requests execution_requests_scope_data_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.execution_requests
    ADD CONSTRAINT execution_requests_scope_data_source_id_fkey FOREIGN KEY (scope_data_source_id) REFERENCES public.data_sources(id) ON DELETE CASCADE;


--
-- Name: alert_delivery_attempts fk_alert_delivery_attempts_org_destination; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_delivery_attempts
    ADD CONSTRAINT fk_alert_delivery_attempts_org_destination FOREIGN KEY (organization_id, destination_id) REFERENCES public.alert_destinations(organization_id, id) ON DELETE SET NULL (destination_id);


--
-- Name: alert_delivery_attempts fk_alert_delivery_attempts_org_policy; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_delivery_attempts
    ADD CONSTRAINT fk_alert_delivery_attempts_org_policy FOREIGN KEY (organization_id, alert_policy_id) REFERENCES public.alert_policies(organization_id, id) ON DELETE SET NULL (alert_policy_id);


--
-- Name: alert_delivery_attempts fk_alert_delivery_attempts_org_record; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_delivery_attempts
    ADD CONSTRAINT fk_alert_delivery_attempts_org_record FOREIGN KEY (organization_id, notification_record_id) REFERENCES public.notification_records(organization_id, id) ON DELETE CASCADE;


--
-- Name: alert_policies fk_alert_policies_org_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policies
    ADD CONSTRAINT fk_alert_policies_org_environment FOREIGN KEY (organization_id, environment_id) REFERENCES public.environments(organization_id, id) ON DELETE CASCADE;


--
-- Name: alert_policies fk_alert_policies_org_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policies
    ADD CONSTRAINT fk_alert_policies_org_project FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: alert_policies fk_alert_policies_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policies
    ADD CONSTRAINT fk_alert_policies_project_environment FOREIGN KEY (project_id, environment_id) REFERENCES public.project_environments(project_id, environment_id) ON DELETE CASCADE;


--
-- Name: alert_policy_destinations fk_alert_policy_destinations_org_destination; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policy_destinations
    ADD CONSTRAINT fk_alert_policy_destinations_org_destination FOREIGN KEY (organization_id, destination_id) REFERENCES public.alert_destinations(organization_id, id) ON DELETE CASCADE;


--
-- Name: alert_policy_destinations fk_alert_policy_destinations_org_policy; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_policy_destinations
    ADD CONSTRAINT fk_alert_policy_destinations_org_policy FOREIGN KEY (organization_id, alert_policy_id) REFERENCES public.alert_policies(organization_id, id) ON DELETE CASCADE;


--
-- Name: api_keys fk_api_keys_org_created_by_member; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT fk_api_keys_org_created_by_member FOREIGN KEY (organization_id, created_by_member_id) REFERENCES public.organization_members(organization_id, id) ON DELETE SET NULL (created_by_member_id);


--
-- Name: api_keys fk_api_keys_org_role; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT fk_api_keys_org_role FOREIGN KEY (organization_id, role_id) REFERENCES public.roles(organization_id, id) ON DELETE RESTRICT;


--
-- Name: credential_oauth_installs fk_credential_oauth_installs_expected_credential_org; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT fk_credential_oauth_installs_expected_credential_org FOREIGN KEY (organization_id, expected_credential_id) REFERENCES public.credentials(organization_id, id) ON DELETE RESTRICT;


--
-- Name: credential_oauth_installs fk_credential_oauth_installs_org_data_source; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT fk_credential_oauth_installs_org_data_source FOREIGN KEY (organization_id, data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE CASCADE;


--
-- Name: credential_oauth_installs fk_credential_oauth_installs_organization_gitlab_connection_org; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT fk_credential_oauth_installs_organization_gitlab_connection_org FOREIGN KEY (organization_id, organization_gitlab_connection_id) REFERENCES public.organization_gitlab_connections(organization_id, id) ON DELETE CASCADE;


--
-- Name: data_source_bindings fk_data_source_bindings_org_source; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_bindings
    ADD CONSTRAINT fk_data_source_bindings_org_source FOREIGN KEY (organization_id, data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE RESTRICT;


--
-- Name: data_source_bindings fk_data_source_bindings_pipeline_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_bindings
    ADD CONSTRAINT fk_data_source_bindings_pipeline_ref FOREIGN KEY (pipeline_spec_id, data_source_ref_id) REFERENCES public.data_source_refs(pipeline_spec_id, id) ON DELETE CASCADE;


--
-- Name: data_source_bindings fk_data_source_bindings_project_pipeline; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_bindings
    ADD CONSTRAINT fk_data_source_bindings_project_pipeline FOREIGN KEY (project_id, pipeline_spec_id) REFERENCES public.pipeline_specs(project_id, id) ON DELETE CASCADE;


--
-- Name: data_source_bindings fk_data_source_bindings_scope; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_source_bindings
    ADD CONSTRAINT fk_data_source_bindings_scope FOREIGN KEY (organization_id, project_id, project_environment_id) REFERENCES public.project_environments(organization_id, project_id, id) ON DELETE CASCADE;


--
-- Name: data_sources fk_data_sources_credential_org; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_sources
    ADD CONSTRAINT fk_data_sources_credential_org FOREIGN KEY (organization_id, credential_id) REFERENCES public.credentials(organization_id, id) ON DELETE RESTRICT;


--
-- Name: data_sources fk_data_sources_organization_gitlab_connection_org; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_sources
    ADD CONSTRAINT fk_data_sources_organization_gitlab_connection_org FOREIGN KEY (organization_id, organization_gitlab_connection_id) REFERENCES public.organization_gitlab_connections(organization_id, id) ON DELETE RESTRICT;


--
-- Name: environment_runtime_variables fk_environment_runtime_variables_org_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_runtime_variables
    ADD CONSTRAINT fk_environment_runtime_variables_org_environment FOREIGN KEY (organization_id, environment_id) REFERENCES public.environments(organization_id, id) ON DELETE CASCADE;


--
-- Name: environment_secrets fk_environment_secrets_org_credential; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_secrets
    ADD CONSTRAINT fk_environment_secrets_org_credential FOREIGN KEY (organization_id, credential_id) REFERENCES public.credentials(organization_id, id) ON DELETE RESTRICT;


--
-- Name: environment_secrets fk_environment_secrets_org_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environment_secrets
    ADD CONSTRAINT fk_environment_secrets_org_environment FOREIGN KEY (organization_id, environment_id) REFERENCES public.environments(organization_id, id) ON DELETE CASCADE;


--
-- Name: execution_requests fk_execution_requests_org_data_source; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.execution_requests
    ADD CONSTRAINT fk_execution_requests_org_data_source FOREIGN KEY (organization_id, data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE CASCADE;


--
-- Name: github_app_repositories fk_github_app_repositories_installation; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_repositories
    ADD CONSTRAINT fk_github_app_repositories_installation FOREIGN KEY (organization_id, installation_id) REFERENCES public.github_app_installations(organization_id, id);


--
-- Name: github_app_install_intents fk_github_install_intent_repository_setup; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_install_intents
    ADD CONSTRAINT fk_github_install_intent_repository_setup FOREIGN KEY (organization_id, repository_setup_slug) REFERENCES public.project_repository_setups(organization_id, slug) ON DELETE CASCADE;


--
-- Name: gitlab_proof_webhooks fk_gitlab_proof_webhooks_data_source; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gitlab_proof_webhooks
    ADD CONSTRAINT fk_gitlab_proof_webhooks_data_source FOREIGN KEY (organization_id, data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE CASCADE;


--
-- Name: notification_records fk_notification_records_org_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_records
    ADD CONSTRAINT fk_notification_records_org_environment FOREIGN KEY (organization_id, environment_id) REFERENCES public.environments(organization_id, id) ON DELETE CASCADE;


--
-- Name: notification_records fk_notification_records_org_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_records
    ADD CONSTRAINT fk_notification_records_org_project FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: notification_records fk_notification_records_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_records
    ADD CONSTRAINT fk_notification_records_project_environment FOREIGN KEY (project_id, environment_id) REFERENCES public.project_environments(project_id, environment_id) ON DELETE CASCADE;


--
-- Name: credential_oauth_installs fk_oauth_install_notification_connection_org; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT fk_oauth_install_notification_connection_org FOREIGN KEY (organization_id, notification_connection_id) REFERENCES public.notification_connections(organization_id, id) ON DELETE CASCADE;


--
-- Name: credential_oauth_installs fk_oauth_install_repository_setup; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT fk_oauth_install_repository_setup FOREIGN KEY (organization_id, repository_setup_slug) REFERENCES public.project_repository_setups(organization_id, slug) ON DELETE CASCADE;


--
-- Name: credential_oauth_installs fk_oauth_install_repository_setup_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_oauth_installs
    ADD CONSTRAINT fk_oauth_install_repository_setup_project FOREIGN KEY (organization_id, repository_setup_project_slug) REFERENCES public.projects(organization_id, slug) ON DELETE CASCADE;


--
-- Name: odcs_contracts fk_odcs_contracts_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contracts
    ADD CONSTRAINT fk_odcs_contracts_project FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: odcs_contract_revisions fk_odcs_revisions_contract; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_contract_revisions
    ADD CONSTRAINT fk_odcs_revisions_contract FOREIGN KEY (organization_id, project_id, contract_id) REFERENCES public.odcs_contracts(organization_id, project_id, id) ON DELETE CASCADE;


--
-- Name: organization_gitlab_connections fk_organization_gitlab_connections_credential_org; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_gitlab_connections
    ADD CONSTRAINT fk_organization_gitlab_connections_credential_org FOREIGN KEY (organization_id, credential_id) REFERENCES public.credentials(organization_id, id) ON DELETE RESTRICT;


--
-- Name: organization_member_invitations fk_organization_member_invitations_org_accepted_member; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_member_invitations
    ADD CONSTRAINT fk_organization_member_invitations_org_accepted_member FOREIGN KEY (organization_id, accepted_member_id) REFERENCES public.organization_members(organization_id, id) ON DELETE SET NULL (accepted_member_id);


--
-- Name: organization_member_invitations fk_organization_member_invitations_org_role; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_member_invitations
    ADD CONSTRAINT fk_organization_member_invitations_org_role FOREIGN KEY (organization_id, role_id) REFERENCES public.roles(organization_id, id) ON DELETE RESTRICT;


--
-- Name: organization_members fk_organization_members_org_role; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_members
    ADD CONSTRAINT fk_organization_members_org_role FOREIGN KEY (organization_id, role_id) REFERENCES public.roles(organization_id, id) ON DELETE RESTRICT;


--
-- Name: pipeline_deployments fk_pipeline_deployments_current_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployments
    ADD CONSTRAINT fk_pipeline_deployments_current_version FOREIGN KEY (project_id, pipeline_slug, current_version_id) REFERENCES public.pipeline_versions(project_id, pipeline_slug, id) ON DELETE CASCADE;


--
-- Name: pipeline_deployments fk_pipeline_deployments_previous_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployments
    ADD CONSTRAINT fk_pipeline_deployments_previous_version FOREIGN KEY (project_id, pipeline_slug, previous_version_id) REFERENCES public.pipeline_versions(project_id, pipeline_slug, id) ON DELETE SET NULL (previous_version_id);


--
-- Name: pipeline_deployments fk_pipeline_deployments_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployments
    ADD CONSTRAINT fk_pipeline_deployments_project_environment FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_drafts fk_pipeline_drafts_based_on_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_drafts
    ADD CONSTRAINT fk_pipeline_drafts_based_on_version FOREIGN KEY (project_id, pipeline_slug, based_on_version_id) REFERENCES public.pipeline_versions(project_id, pipeline_slug, id) ON DELETE SET NULL (based_on_version_id);


--
-- Name: pipeline_drafts fk_pipeline_drafts_org_member; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_drafts
    ADD CONSTRAINT fk_pipeline_drafts_org_member FOREIGN KEY (organization_id, member_id) REFERENCES public.organization_members(organization_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_drafts fk_pipeline_drafts_org_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_drafts
    ADD CONSTRAINT fk_pipeline_drafts_org_project FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_drafts fk_pipeline_drafts_project_authority; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_drafts
    ADD CONSTRAINT fk_pipeline_drafts_project_authority FOREIGN KEY (organization_id, project_id, proposal_authority_id) REFERENCES public.pipeline_proposal_authorities(organization_id, project_id, id) ON DELETE SET NULL (proposal_authority_id);


--
-- Name: pipeline_incremental_checkpoint_locks fk_pipeline_incremental_checkpoint_locks_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoint_locks
    ADD CONSTRAINT fk_pipeline_incremental_checkpoint_locks_project_environment FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_incremental_checkpoints fk_pipeline_incremental_checkpoints_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoints
    ADD CONSTRAINT fk_pipeline_incremental_checkpoints_project_environment FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_incremental_checkpoints fk_pipeline_incremental_checkpoints_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoints
    ADD CONSTRAINT fk_pipeline_incremental_checkpoints_version FOREIGN KEY (project_id, pipeline_slug, pipeline_version_id) REFERENCES public.pipeline_versions(project_id, pipeline_slug, id) ON DELETE CASCADE;


--
-- Name: pipeline_proposal_authorities fk_pipeline_proposal_authorities_org_credential; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_proposal_authorities
    ADD CONSTRAINT fk_pipeline_proposal_authorities_org_credential FOREIGN KEY (organization_id, credential_id) REFERENCES public.credentials(organization_id, id) ON DELETE RESTRICT;


--
-- Name: pipeline_proposal_authorities fk_pipeline_proposal_authorities_org_data_source; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_proposal_authorities
    ADD CONSTRAINT fk_pipeline_proposal_authorities_org_data_source FOREIGN KEY (organization_id, data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_proposal_authorities fk_pipeline_proposal_authorities_org_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_proposal_authorities
    ADD CONSTRAINT fk_pipeline_proposal_authorities_org_project FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_references fk_pipeline_references_current_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_references
    ADD CONSTRAINT fk_pipeline_references_current_version FOREIGN KEY (project_id, slug, current_version_id) REFERENCES public.pipeline_versions(project_id, pipeline_slug, id) ON DELETE SET NULL (current_version_id);


--
-- Name: pipeline_runs fk_pipeline_runs_pipeline_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_runs
    ADD CONSTRAINT fk_pipeline_runs_pipeline_version FOREIGN KEY (project_id, pipeline_slug, pipeline_version_id) REFERENCES public.pipeline_versions(project_id, pipeline_slug, id) ON DELETE CASCADE;


--
-- Name: pipeline_runs fk_pipeline_runs_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_runs
    ADD CONSTRAINT fk_pipeline_runs_project_environment FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_runs fk_pipeline_runs_source_webhook_delivery; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_runs
    ADD CONSTRAINT fk_pipeline_runs_source_webhook_delivery FOREIGN KEY (project_environment_id, pipeline_slug, source_webhook_delivery_id) REFERENCES public.pipeline_webhook_deliveries(project_environment_id, pipeline_slug, id) ON DELETE RESTRICT;


--
-- Name: pipeline_schedule_fires fk_pipeline_schedule_fires_trigger_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_schedule_fires
    ADD CONSTRAINT fk_pipeline_schedule_fires_trigger_environment FOREIGN KEY (pipeline_trigger_id, project_environment_id) REFERENCES public.pipeline_triggers(id, project_environment_id) ON DELETE CASCADE;


--
-- Name: pipeline_specs fk_pipeline_specs_current_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_specs
    ADD CONSTRAINT fk_pipeline_specs_current_version FOREIGN KEY (project_id, slug, current_version_id) REFERENCES public.pipeline_versions(project_id, pipeline_slug, id) ON DELETE SET NULL (current_version_id);


--
-- Name: pipeline_trigger_retry_attempts fk_pipeline_trigger_retry_attempts_trigger_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_trigger_retry_attempts
    ADD CONSTRAINT fk_pipeline_trigger_retry_attempts_trigger_environment FOREIGN KEY (pipeline_trigger_id, project_environment_id) REFERENCES public.pipeline_triggers(id, project_environment_id) ON DELETE CASCADE;


--
-- Name: pipeline_triggers fk_pipeline_triggers_pinned_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_triggers
    ADD CONSTRAINT fk_pipeline_triggers_pinned_version FOREIGN KEY (project_id, pipeline_slug, pinned_pipeline_version_id) REFERENCES public.pipeline_versions(project_id, pipeline_slug, id) ON DELETE SET NULL (pinned_pipeline_version_id);


--
-- Name: pipeline_triggers fk_pipeline_triggers_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_triggers
    ADD CONSTRAINT fk_pipeline_triggers_project_environment FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_triggers fk_pipeline_triggers_project_pipeline; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_triggers
    ADD CONSTRAINT fk_pipeline_triggers_project_pipeline FOREIGN KEY (project_id, pipeline_id, pipeline_slug) REFERENCES public.pipeline_specs(project_id, id, slug) ON DELETE CASCADE;


--
-- Name: pipeline_validation_results fk_pipeline_validation_results_pipeline; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_validation_results
    ADD CONSTRAINT fk_pipeline_validation_results_pipeline FOREIGN KEY (project_id, pipeline_id, pipeline_slug) REFERENCES public.pipeline_specs(project_id, id, slug) ON DELETE SET NULL (pipeline_id);


--
-- Name: pipeline_validation_results fk_pipeline_validation_results_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_validation_results
    ADD CONSTRAINT fk_pipeline_validation_results_project_environment FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_validation_results fk_pipeline_validation_results_report; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_validation_results
    ADD CONSTRAINT fk_pipeline_validation_results_report FOREIGN KEY (impact_pipeline_id, impact_report_slug) REFERENCES public.drift_impact_reports(pipeline_spec_id, slug) MATCH FULL ON DELETE SET NULL (impact_pipeline_id, impact_report_slug);


--
-- Name: pipeline_validation_results fk_pipeline_validation_results_report_pipeline; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_validation_results
    ADD CONSTRAINT fk_pipeline_validation_results_report_pipeline FOREIGN KEY (project_id, pipeline_slug, impact_pipeline_id) REFERENCES public.pipeline_specs(project_id, slug, id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: pipeline_validation_results fk_pipeline_validation_results_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_validation_results
    ADD CONSTRAINT fk_pipeline_validation_results_version FOREIGN KEY (project_id, pipeline_slug, pipeline_version_id) REFERENCES public.pipeline_versions(project_id, pipeline_slug, id) ON DELETE SET NULL (pipeline_version_id);


--
-- Name: pipeline_versions fk_pipeline_versions_pipeline_spec; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_versions
    ADD CONSTRAINT fk_pipeline_versions_pipeline_spec FOREIGN KEY (project_id, pipeline_slug) REFERENCES public.pipeline_specs(project_id, slug) ON DELETE CASCADE;


--
-- Name: pipeline_webhook_deliveries fk_pipeline_webhook_deliveries_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_deliveries
    ADD CONSTRAINT fk_pipeline_webhook_deliveries_project FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_webhook_deliveries fk_pipeline_webhook_deliveries_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_deliveries
    ADD CONSTRAINT fk_pipeline_webhook_deliveries_project_environment FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_webhook_deliveries fk_pipeline_webhook_deliveries_trigger; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_deliveries
    ADD CONSTRAINT fk_pipeline_webhook_deliveries_trigger FOREIGN KEY (project_id, pipeline_trigger_id) REFERENCES public.pipeline_triggers(project_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_webhook_secret_versions fk_pipeline_webhook_secret_versions_credential; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_secret_versions
    ADD CONSTRAINT fk_pipeline_webhook_secret_versions_credential FOREIGN KEY (organization_id, credential_id) REFERENCES public.credentials(organization_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_webhook_secret_versions fk_pipeline_webhook_secret_versions_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_secret_versions
    ADD CONSTRAINT fk_pipeline_webhook_secret_versions_project FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_webhook_secret_versions fk_pipeline_webhook_secret_versions_trigger; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_webhook_secret_versions
    ADD CONSTRAINT fk_pipeline_webhook_secret_versions_trigger FOREIGN KEY (project_id, pipeline_trigger_id) REFERENCES public.pipeline_triggers(project_id, id) ON DELETE CASCADE;


--
-- Name: project_environments fk_project_environments_org_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_environments
    ADD CONSTRAINT fk_project_environments_org_environment FOREIGN KEY (organization_id, environment_id) REFERENCES public.environments(organization_id, id) ON DELETE RESTRICT;


--
-- Name: project_environments fk_project_environments_org_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_environments
    ADD CONSTRAINT fk_project_environments_org_project FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: projects fk_projects_org_backend_data_source; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT fk_projects_org_backend_data_source FOREIGN KEY (organization_id, pipeline_backend_data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE RESTRICT;


--
-- Name: proof_change_candidate_mappings fk_proof_candidate_mappings_org_data_source; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidate_mappings
    ADD CONSTRAINT fk_proof_candidate_mappings_org_data_source FOREIGN KEY (organization_id, data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE RESTRICT;


--
-- Name: proof_change_candidate_mappings fk_proof_candidate_mappings_org_project; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidate_mappings
    ADD CONSTRAINT fk_proof_candidate_mappings_org_project FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: proof_change_candidate_mappings fk_proof_candidate_mappings_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidate_mappings
    ADD CONSTRAINT fk_proof_candidate_mappings_project_environment FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: proof_change_candidates fk_proof_change_candidates_org_mapping; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidates
    ADD CONSTRAINT fk_proof_change_candidates_org_mapping FOREIGN KEY (organization_id, mapping_id) REFERENCES public.proof_change_candidate_mappings(organization_id, id) ON DELETE CASCADE;


--
-- Name: proof_change_candidates fk_proof_change_candidates_org_superseded; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidates
    ADD CONSTRAINT fk_proof_change_candidates_org_superseded FOREIGN KEY (organization_id, supersedes_candidate_id) REFERENCES public.proof_change_candidates(organization_id, id) ON DELETE SET NULL (supersedes_candidate_id);


--
-- Name: proof_evidence fk_proof_evidence_candidate; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_evidence
    ADD CONSTRAINT fk_proof_evidence_candidate FOREIGN KEY (organization_id, mapping_id, candidate_id, spec_hash) REFERENCES public.proof_change_candidates(organization_id, mapping_id, id, spec_hash) ON DELETE CASCADE;


--
-- Name: proof_evidence fk_proof_evidence_mapping; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_evidence
    ADD CONSTRAINT fk_proof_evidence_mapping FOREIGN KEY (organization_id, project_id, project_environment_id, mapping_id) REFERENCES public.proof_change_candidate_mappings(organization_id, project_id, project_environment_id, id) ON DELETE CASCADE;


--
-- Name: proof_lifecycle_links fk_proof_lifecycle_links_candidate; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_lifecycle_links
    ADD CONSTRAINT fk_proof_lifecycle_links_candidate FOREIGN KEY (organization_id, candidate_id) REFERENCES public.proof_change_candidates(organization_id, id) ON DELETE CASCADE;


--
-- Name: proof_lifecycle_links fk_proof_lifecycle_links_project_environment; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_lifecycle_links
    ADD CONSTRAINT fk_proof_lifecycle_links_project_environment FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: proof_lifecycle_links fk_proof_lifecycle_links_receipt; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_lifecycle_links
    ADD CONSTRAINT fk_proof_lifecycle_links_receipt FOREIGN KEY (organization_id, receipt_id) REFERENCES public.proof_receipts(organization_id, id) ON DELETE CASCADE;


--
-- Name: proof_receipts fk_proof_receipts_candidate; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_receipts
    ADD CONSTRAINT fk_proof_receipts_candidate FOREIGN KEY (organization_id, mapping_id, candidate_id, spec_hash) REFERENCES public.proof_change_candidates(organization_id, mapping_id, id, spec_hash) ON DELETE CASCADE;


--
-- Name: proof_receipts fk_proof_receipts_evidence; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_receipts
    ADD CONSTRAINT fk_proof_receipts_evidence FOREIGN KEY (organization_id, candidate_id, evidence_id) REFERENCES public.proof_evidence(organization_id, candidate_id, id) ON DELETE CASCADE;


--
-- Name: proof_receipts fk_proof_receipts_mapping; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_receipts
    ADD CONSTRAINT fk_proof_receipts_mapping FOREIGN KEY (organization_id, project_id, project_environment_id, mapping_id) REFERENCES public.proof_change_candidate_mappings(organization_id, project_id, project_environment_id, id) ON DELETE CASCADE;


--
-- Name: proof_receipts fk_proof_receipts_supersedes; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_receipts
    ADD CONSTRAINT fk_proof_receipts_supersedes FOREIGN KEY (organization_id, supersedes_receipt_id) REFERENCES public.proof_receipts(organization_id, id) ON DELETE SET NULL (supersedes_receipt_id);


--
-- Name: schema_metadata fk_schema_metadata_ds; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_metadata
    ADD CONSTRAINT fk_schema_metadata_ds FOREIGN KEY (data_source_id) REFERENCES public.data_sources(id) ON DELETE CASCADE;


--
-- Name: github_app_install_intents github_app_install_intents_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_install_intents
    ADD CONSTRAINT github_app_install_intents_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);


--
-- Name: github_app_installations github_app_installations_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_installations
    ADD CONSTRAINT github_app_installations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);


--
-- Name: github_app_repositories github_app_repositories_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_repositories
    ADD CONSTRAINT github_app_repositories_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);


--
-- Name: gitlab_proof_webhooks gitlab_proof_webhooks_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gitlab_proof_webhooks
    ADD CONSTRAINT gitlab_proof_webhooks_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: notification_connections notification_connections_organization_id_credential_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_connections
    ADD CONSTRAINT notification_connections_organization_id_credential_id_fkey FOREIGN KEY (organization_id, credential_id) REFERENCES public.credentials(organization_id, id) ON DELETE RESTRICT;


--
-- Name: notification_connections notification_connections_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_connections
    ADD CONSTRAINT notification_connections_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: notification_records notification_records_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_records
    ADD CONSTRAINT notification_records_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: odcs_artifacts odcs_artifacts_contract_id_revision_slug_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_artifacts
    ADD CONSTRAINT odcs_artifacts_contract_id_revision_slug_fkey FOREIGN KEY (contract_id, revision_slug) REFERENCES public.odcs_contract_revisions(contract_id, slug) ON DELETE CASCADE;


--
-- Name: odcs_artifacts odcs_artifacts_organization_id_project_id_contract_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.odcs_artifacts
    ADD CONSTRAINT odcs_artifacts_organization_id_project_id_contract_id_fkey FOREIGN KEY (organization_id, project_id, contract_id) REFERENCES public.odcs_contracts(organization_id, project_id, id) ON DELETE CASCADE;




--
-- Name: organization_gitlab_connections organization_gitlab_connections_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_gitlab_connections
    ADD CONSTRAINT organization_gitlab_connections_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: organization_member_invitations organization_member_invitations_accepted_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_member_invitations
    ADD CONSTRAINT organization_member_invitations_accepted_by_user_id_fkey FOREIGN KEY (accepted_by_user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: organization_member_invitations organization_member_invitations_invited_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_member_invitations
    ADD CONSTRAINT organization_member_invitations_invited_by_user_id_fkey FOREIGN KEY (invited_by_user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: organization_member_invitations organization_member_invitations_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_member_invitations
    ADD CONSTRAINT organization_member_invitations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: organization_members organization_members_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_members
    ADD CONSTRAINT organization_members_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: organization_members organization_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_members
    ADD CONSTRAINT organization_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: organization_retention_runs organization_retention_runs_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organization_retention_runs
    ADD CONSTRAINT organization_retention_runs_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: personal_access_tokens personal_access_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_access_tokens
    ADD CONSTRAINT personal_access_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: pipeline_checkpoint pipeline_checkpoint_project_environment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_checkpoint
    ADD CONSTRAINT pipeline_checkpoint_project_environment_id_fkey FOREIGN KEY (project_environment_id) REFERENCES public.project_environments(id) ON DELETE CASCADE;


--
-- Name: pipeline_checkpoint pipeline_checkpoint_project_environment_id_run_slug_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_checkpoint
    ADD CONSTRAINT pipeline_checkpoint_project_environment_id_run_slug_fkey FOREIGN KEY (project_environment_id, run_slug) REFERENCES public.pipeline_runs(project_environment_id, run_slug) ON DELETE CASCADE;


--
-- Name: pipeline_compiled_plans pipeline_compiled_plans_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_compiled_plans
    ADD CONSTRAINT pipeline_compiled_plans_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: pipeline_completion_capacity pipeline_completion_capacity_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_capacity
    ADD CONSTRAINT pipeline_completion_capacity_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: pipeline_completion_snapshots pipeline_completion_snapshots_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_snapshots
    ADD CONSTRAINT pipeline_completion_snapshots_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: pipeline_completion_tombstones pipeline_completion_tombstones_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_tombstones
    ADD CONSTRAINT pipeline_completion_tombstones_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: pipeline_completion_trigger_revisions pipeline_completion_trigger_revisions_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_completion_trigger_revisions
    ADD CONSTRAINT pipeline_completion_trigger_revisions_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: pipeline_creation_proposals pipeline_creation_proposals_organization_id_member_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_creation_proposals
    ADD CONSTRAINT pipeline_creation_proposals_organization_id_member_id_fkey FOREIGN KEY (organization_id, member_id) REFERENCES public.organization_members(organization_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_creation_proposals pipeline_creation_proposals_organization_id_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_creation_proposals
    ADD CONSTRAINT pipeline_creation_proposals_organization_id_project_id_fkey FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_deployment_resolutions pipeline_deployment_resolutio_project_id_pipeline_slug_com_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployment_resolutions
    ADD CONSTRAINT pipeline_deployment_resolutio_project_id_pipeline_slug_com_fkey FOREIGN KEY (project_id, pipeline_slug, compiled_digest) REFERENCES public.pipeline_compiled_plans(project_id, pipeline_slug, digest);


--
-- Name: pipeline_deployment_resolutions pipeline_deployment_resolutio_project_id_project_environme_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployment_resolutions
    ADD CONSTRAINT pipeline_deployment_resolutio_project_id_project_environme_fkey FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: pipeline_deployment_resolutions pipeline_deployment_resolutions_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployment_resolutions
    ADD CONSTRAINT pipeline_deployment_resolutions_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: pipeline_deployments pipeline_deployments_project_environment_id_pipeline_slug__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployments
    ADD CONSTRAINT pipeline_deployments_project_environment_id_pipeline_slug__fkey FOREIGN KEY (project_environment_id, pipeline_slug, current_resolution_digest) REFERENCES public.pipeline_deployment_resolutions(project_environment_id, pipeline_slug, digest);


--
-- Name: pipeline_deployments pipeline_deployments_project_environment_id_pipeline_slug_fkey1; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_deployments
    ADD CONSTRAINT pipeline_deployments_project_environment_id_pipeline_slug_fkey1 FOREIGN KEY (project_environment_id, pipeline_slug, previous_resolution_digest) REFERENCES public.pipeline_deployment_resolutions(project_environment_id, pipeline_slug, digest);


--
-- Name: pipeline_drafts pipeline_drafts_project_id_pipeline_slug_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_drafts
    ADD CONSTRAINT pipeline_drafts_project_id_pipeline_slug_fkey FOREIGN KEY (project_id, pipeline_slug) REFERENCES public.pipeline_specs(project_id, slug) ON DELETE CASCADE;


--
-- Name: pipeline_incremental_checkpoint_locks pipeline_incremental_checkpoint_l_project_id_pipeline_slug_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoint_locks
    ADD CONSTRAINT pipeline_incremental_checkpoint_l_project_id_pipeline_slug_fkey FOREIGN KEY (project_id, pipeline_slug) REFERENCES public.pipeline_specs(project_id, slug) ON DELETE CASCADE;


--
-- Name: pipeline_incremental_checkpoint_locks pipeline_incremental_checkpoint_locks_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoint_locks
    ADD CONSTRAINT pipeline_incremental_checkpoint_locks_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: pipeline_incremental_checkpoints pipeline_incremental_checkpoints_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoints
    ADD CONSTRAINT pipeline_incremental_checkpoints_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: pipeline_incremental_checkpoints pipeline_incremental_checkpoints_project_id_pipeline_slug_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_incremental_checkpoints
    ADD CONSTRAINT pipeline_incremental_checkpoints_project_id_pipeline_slug_fkey FOREIGN KEY (project_id, pipeline_slug) REFERENCES public.pipeline_specs(project_id, slug) ON DELETE CASCADE;


--
-- Name: pipeline_proposal_authorities pipeline_proposal_authorities_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_proposal_authorities
    ADD CONSTRAINT pipeline_proposal_authorities_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: pipeline_references pipeline_references_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_references
    ADD CONSTRAINT pipeline_references_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: pipeline_references pipeline_references_project_id_slug_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_references
    ADD CONSTRAINT pipeline_references_project_id_slug_fkey FOREIGN KEY (project_id, slug) REFERENCES public.pipeline_specs(project_id, slug) ON DELETE CASCADE;


--
-- Name: pipeline_run_logical_closures pipeline_run_logical_closures_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_logical_closures
    ADD CONSTRAINT pipeline_run_logical_closures_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: pipeline_run_recency_episodes pipeline_run_recency_episodes_expectation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_episodes
    ADD CONSTRAINT pipeline_run_recency_episodes_expectation_id_fkey FOREIGN KEY (expectation_id) REFERENCES public.pipeline_run_recency_expectations(id) ON DELETE CASCADE;


--
-- Name: pipeline_run_recency_expectations pipeline_run_recency_expectations_project_environment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_expectations
    ADD CONSTRAINT pipeline_run_recency_expectations_project_environment_id_fkey FOREIGN KEY (project_environment_id) REFERENCES public.project_environments(id) ON DELETE CASCADE;


--
-- Name: pipeline_run_recency_successes pipeline_run_recency_successes_project_environment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_recency_successes
    ADD CONSTRAINT pipeline_run_recency_successes_project_environment_id_fkey FOREIGN KEY (project_environment_id) REFERENCES public.project_environments(id) ON DELETE CASCADE;


--
-- Name: pipeline_run_terminal_obligations pipeline_run_terminal_obligations_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_run_terminal_obligations
    ADD CONSTRAINT pipeline_run_terminal_obligations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: pipeline_runs pipeline_runs_worker_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_runs
    ADD CONSTRAINT pipeline_runs_worker_job_id_fkey FOREIGN KEY (worker_job_id) REFERENCES public.river_job(id) ON DELETE SET NULL;


--
-- Name: pipeline_schedule_fires pipeline_schedule_fires_project_environment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_schedule_fires
    ADD CONSTRAINT pipeline_schedule_fires_project_environment_id_fkey FOREIGN KEY (project_environment_id) REFERENCES public.project_environments(id) ON DELETE CASCADE;


--
-- Name: pipeline_sink_effects pipeline_sink_effects_project_environment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_sink_effects
    ADD CONSTRAINT pipeline_sink_effects_project_environment_id_fkey FOREIGN KEY (project_environment_id) REFERENCES public.project_environments(id) ON DELETE CASCADE;


--
-- Name: pipeline_specs pipeline_specs_deleted_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_specs
    ADD CONSTRAINT pipeline_specs_deleted_by_fkey FOREIGN KEY (deleted_by) REFERENCES public.organization_members(id) ON DELETE SET NULL;


--
-- Name: pipeline_specs pipeline_specs_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_specs
    ADD CONSTRAINT pipeline_specs_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: pipeline_trigger_retry_attempts pipeline_trigger_retry_attempts_project_environment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_trigger_retry_attempts
    ADD CONSTRAINT pipeline_trigger_retry_attempts_project_environment_id_fkey FOREIGN KEY (project_environment_id) REFERENCES public.project_environments(id) ON DELETE CASCADE;


--
-- Name: pipeline_versions pipeline_versions_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipeline_versions
    ADD CONSTRAINT pipeline_versions_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: project_repository_setups project_repository_setups_organization_id_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_repository_setups
    ADD CONSTRAINT project_repository_setups_organization_id_project_id_fkey FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: projects projects_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: proof_change_candidate_mappings proof_change_candidate_mappings_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidate_mappings
    ADD CONSTRAINT proof_change_candidate_mappings_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: proof_change_candidates proof_change_candidates_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_change_candidates
    ADD CONSTRAINT proof_change_candidates_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: proof_lifecycle_links proof_lifecycle_links_deployment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_lifecycle_links
    ADD CONSTRAINT proof_lifecycle_links_deployment_id_fkey FOREIGN KEY (deployment_id) REFERENCES public.pipeline_deployments(id);


--
-- Name: proof_lifecycle_links proof_lifecycle_links_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_lifecycle_links
    ADD CONSTRAINT proof_lifecycle_links_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.pipeline_runs(id);


--
-- Name: proof_observation_jobs proof_observation_jobs_organization_id_candidate_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_jobs
    ADD CONSTRAINT proof_observation_jobs_organization_id_candidate_id_fkey FOREIGN KEY (organization_id, candidate_id) REFERENCES public.proof_change_candidates(organization_id, id) ON DELETE CASCADE;


--
-- Name: proof_observation_jobs proof_observation_jobs_organization_id_data_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_jobs
    ADD CONSTRAINT proof_observation_jobs_organization_id_data_source_id_fkey FOREIGN KEY (organization_id, data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE RESTRICT;


--
-- Name: proof_observation_jobs proof_observation_jobs_organization_id_project_id_project__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_jobs
    ADD CONSTRAINT proof_observation_jobs_organization_id_project_id_project__fkey FOREIGN KEY (organization_id, project_id, project_environment_id) REFERENCES public.proof_observation_policies(organization_id, project_id, project_environment_id) ON DELETE CASCADE;


--
-- Name: proof_observation_policies proof_observation_policies_organization_id_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_policies
    ADD CONSTRAINT proof_observation_policies_organization_id_project_id_fkey FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;


--
-- Name: proof_observation_policies proof_observation_policies_project_id_project_environment__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.proof_observation_policies
    ADD CONSTRAINT proof_observation_policies_project_id_project_environment__fkey FOREIGN KEY (project_id, project_environment_id) REFERENCES public.project_environments(project_id, id) ON DELETE CASCADE;


--
-- Name: queries queries_data_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.queries
    ADD CONSTRAINT queries_data_source_id_fkey FOREIGN KEY (data_source_id) REFERENCES public.data_sources(id) ON DELETE CASCADE;


--
-- Name: query_executions query_executions_query_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.query_executions
    ADD CONSTRAINT query_executions_query_id_fkey FOREIGN KEY (query_id) REFERENCES public.queries(id) ON DELETE CASCADE;


--
-- Name: query_parameters query_parameters_query_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.query_parameters
    ADD CONSTRAINT query_parameters_query_id_fkey FOREIGN KEY (query_id) REFERENCES public.queries(id) ON DELETE CASCADE;


--
-- Name: river_client_queue river_client_queue_river_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.river_client_queue
    ADD CONSTRAINT river_client_queue_river_client_id_fkey FOREIGN KEY (river_client_id) REFERENCES public.river_client(id) ON DELETE CASCADE;


--
-- Name: role_permissions role_permissions_permission_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_permission_id_fkey FOREIGN KEY (permission_id) REFERENCES public.permissions(id) ON DELETE CASCADE;


--
-- Name: role_permissions role_permissions_role_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_role_id_fkey FOREIGN KEY (role_id) REFERENCES public.roles(id) ON DELETE CASCADE;


--
-- Name: roles roles_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: snowflake_wif_bindings snowflake_wif_bindings_organization_id_data_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.snowflake_wif_bindings
    ADD CONSTRAINT snowflake_wif_bindings_organization_id_data_source_id_fkey FOREIGN KEY (organization_id, data_source_id) REFERENCES public.data_sources(organization_id, id) ON DELETE RESTRICT;


--
-- Name: snowflake_wif_bindings snowflake_wif_bindings_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.snowflake_wif_bindings
    ADD CONSTRAINT snowflake_wif_bindings_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE RESTRICT;


--
-- Name: user_mfa_challenges user_mfa_challenges_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_mfa_challenges
    ADD CONSTRAINT user_mfa_challenges_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: FUNCTION check_connector_dispatch(request_id uuid, expected_claim_token uuid, organization_slug text, data_source_slug text, data_source_type text, expected_config jsonb, expected_credential_slug text, expected_credential_updated_at timestamp with time zone, admitted_read_only boolean); Type: ACL; Schema: public; Owner: -
--

REVOKE ALL ON FUNCTION public.check_connector_dispatch(request_id uuid, expected_claim_token uuid, organization_slug text, data_source_slug text, data_source_type text, expected_config jsonb, expected_credential_slug text, expected_credential_updated_at timestamp with time zone, admitted_read_only boolean) FROM PUBLIC;


--
-- Name: FUNCTION claim_execution_request(request_id uuid); Type: ACL; Schema: public; Owner: -
--

REVOKE ALL ON FUNCTION public.claim_execution_request(request_id uuid) FROM PUBLIC;


--
-- Name: FUNCTION claim_next_execution_request(); Type: ACL; Schema: public; Owner: -
--

REVOKE ALL ON FUNCTION public.claim_next_execution_request() FROM PUBLIC;


--
-- Name: FUNCTION complete_execution_request(request_id uuid, expected_claim_token uuid, result bytea, succeeded boolean); Type: ACL; Schema: public; Owner: -
--

REVOKE ALL ON FUNCTION public.complete_execution_request(request_id uuid, expected_claim_token uuid, result bytea, succeeded boolean) FROM PUBLIC;


--
-- Name: FUNCTION connector_execution_admission_metrics(); Type: ACL; Schema: public; Owner: -
--

REVOKE ALL ON FUNCTION public.connector_execution_admission_metrics() FROM PUBLIC;


--
-- Name: FUNCTION execution_request_metrics(); Type: ACL; Schema: public; Owner: -
--

REVOKE ALL ON FUNCTION public.execution_request_metrics() FROM PUBLIC;


--
-- Name: FUNCTION heartbeat_execution_request(request_id uuid, expected_claim_token uuid); Type: ACL; Schema: public; Owner: -
--

REVOKE ALL ON FUNCTION public.heartbeat_execution_request(request_id uuid, expected_claim_token uuid) FROM PUBLIC;


--
-- Name: FUNCTION snowflake_wif_ready(org_slug text, ds_slug text, exact_account text, exact_issuer text); Type: ACL; Schema: public; Owner: -
--

REVOKE ALL ON FUNCTION public.snowflake_wif_ready(org_slug text, ds_slug text, exact_account text, exact_issuer text) FROM PUBLIC;


--
-- PostgreSQL database dump complete
--


INSERT INTO public.permissions (id, resource, action) VALUES (1, 'notifications', 'read');
INSERT INTO public.permissions (id, resource, action) VALUES (2, 'notifications', 'update');
INSERT INTO public.permissions (id, resource, action) VALUES (3, 'alert_policies', 'read');
INSERT INTO public.permissions (id, resource, action) VALUES (4, 'alert_policies', 'create');
INSERT INTO public.permissions (id, resource, action) VALUES (5, 'alert_policies', 'update');
INSERT INTO public.permissions (id, resource, action) VALUES (6, 'alert_policies', 'delete');
INSERT INTO public.permissions (id, resource, action) VALUES (7, 'alert_destinations', 'read');
INSERT INTO public.permissions (id, resource, action) VALUES (8, 'alert_destinations', 'create');
INSERT INTO public.permissions (id, resource, action) VALUES (9, 'alert_destinations', 'update');
INSERT INTO public.permissions (id, resource, action) VALUES (10, 'alert_destinations', 'delete');
SELECT pg_catalog.setval('public.permissions_id_seq', 10, true);
INSERT INTO public.river_migration(line, version) VALUES ('main', 1), ('main', 2), ('main', 3), ('main', 4), ('main', 5), ('main', 6);

-- Instance administrator changes are global pre-tenant operator actions. They
-- intentionally retain slugs after account deletion and never grant membership.
CREATE TABLE public.instance_admin_audits (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    target_user_slug character varying(100) NOT NULL,
    previous_role text NOT NULL,
    new_role text NOT NULL,
    operator_identity text NOT NULL,
    occurred_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
