-- The core baseline leaves its migration session's search path empty.
SET search_path = public;

-- Bodies remain encrypted and individually bounded; manifests become readable
-- only after complete source consumption. Request expiry owns cascading cleanup.
ALTER TABLE execution_requests ADD CONSTRAINT execution_requests_id_organization_unique UNIQUE (id, organization_id);

CREATE TABLE retained_file_streams (
    request_id UUID PRIMARY KEY,
    organization_id INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    max_bytes BIGINT NOT NULL CHECK (max_bytes > 0),
    size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
    sha256 TEXT,
    sealed BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (request_id, organization_id) REFERENCES execution_requests(id, organization_id) ON DELETE CASCADE
);
CREATE INDEX retained_file_streams_expiry_idx ON retained_file_streams(expires_at);

CREATE TABLE retained_file_chunks (
    request_id UUID NOT NULL REFERENCES retained_file_streams(request_id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK (sequence >= 0),
    ciphertext BYTEA NOT NULL CHECK (octet_length(ciphertext) BETWEEN 29 AND 1049600),
    PRIMARY KEY (request_id, sequence)
);

-- A short request lock fences chunk operations against cancellation, completion
-- and claim replacement. This helper is private to the capability owner.
CREATE FUNCTION retained_file_claim(p_request_id UUID, p_claim_token UUID, p_org_slug TEXT)
RETURNS INTEGER LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public AS $$
DECLARE tenant_id INTEGER;
BEGIN
    SELECT request.organization_id INTO tenant_id
    FROM public.execution_requests request
    JOIN public.organizations organization ON organization.id = request.organization_id
    WHERE request.id = p_request_id AND request.claim_token = p_claim_token
        AND organization.slug = p_org_slug AND request.kind IN ('query', 'prepare_s3_write')
    FOR UPDATE OF request;
    IF NOT FOUND THEN RETURN NULL; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM public.execution_requests request
        WHERE request.id = p_request_id AND request.state = 'running'
            AND request.cancel_requested_at IS NULL
            AND request.deadline_at > clock_timestamp()
            AND request.lease_expires_at > clock_timestamp()
            AND request.expires_at > clock_timestamp()
    ) THEN RETURN NULL; END IF;
    RETURN tenant_id;
END;
$$;

CREATE FUNCTION begin_retained_file(p_request_id UUID, p_claim_token UUID, p_org_slug TEXT, byte_limit BIGINT, retain_until TIMESTAMPTZ)
RETURNS BOOLEAN LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public AS $$
DECLARE tenant_id INTEGER;
BEGIN
    tenant_id := public.retained_file_claim(p_request_id, p_claim_token, p_org_slug);
    IF tenant_id IS NULL OR byte_limit IS NULL OR byte_limit <= 0 OR retain_until IS NULL OR retain_until <= clock_timestamp() THEN RETURN FALSE; END IF;
    INSERT INTO public.retained_file_streams(request_id, organization_id, max_bytes, expires_at)
    SELECT request.id, tenant_id, byte_limit, LEAST(retain_until, request.expires_at)
    FROM public.execution_requests request WHERE request.id = p_request_id AND request.kind = 'query'
    ON CONFLICT DO NOTHING;
    RETURN FOUND;
END;
$$;

CREATE FUNCTION append_retained_file_chunk(p_request_id UUID, p_claim_token UUID, p_org_slug TEXT, chunk_sequence INTEGER, plain_bytes INTEGER, chunk_ciphertext BYTEA)
RETURNS BOOLEAN LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public AS $$
DECLARE tenant_id INTEGER;
BEGIN
    tenant_id := public.retained_file_claim(p_request_id, p_claim_token, p_org_slug);
    IF tenant_id IS NULL OR plain_bytes IS NULL OR chunk_ciphertext IS NULL OR plain_bytes NOT BETWEEN 1 AND 1048576
        OR octet_length(chunk_ciphertext) NOT BETWEEN 29 AND 1049600 THEN RETURN FALSE; END IF;
    UPDATE public.retained_file_streams stream
    SET size_bytes = stream.size_bytes + plain_bytes, chunk_count = stream.chunk_count + 1
    WHERE stream.request_id = p_request_id AND stream.organization_id = tenant_id
        AND NOT stream.sealed AND stream.expires_at > clock_timestamp()
        AND stream.chunk_count = chunk_sequence
        AND stream.size_bytes <= stream.max_bytes - plain_bytes;
    IF NOT FOUND THEN RETURN FALSE; END IF;
    INSERT INTO public.retained_file_chunks(request_id, sequence, ciphertext)
    VALUES (p_request_id, chunk_sequence, chunk_ciphertext);
    RETURN TRUE;
END;
$$;

CREATE FUNCTION seal_retained_file(p_request_id UUID, p_claim_token UUID, p_org_slug TEXT, total_bytes BIGINT, digest TEXT)
RETURNS BOOLEAN LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public AS $$
DECLARE tenant_id INTEGER;
BEGIN
    tenant_id := public.retained_file_claim(p_request_id, p_claim_token, p_org_slug);
    IF tenant_id IS NULL OR digest IS NULL OR digest !~ '^[a-f0-9]{64}$' THEN RETURN FALSE; END IF;
    UPDATE public.retained_file_streams stream SET sealed = TRUE, sha256 = digest
    WHERE stream.request_id = p_request_id AND stream.organization_id = tenant_id
        AND NOT stream.sealed AND stream.size_bytes = total_bytes
        AND stream.expires_at > clock_timestamp();
    RETURN FOUND;
END;
$$;

CREATE FUNCTION read_retained_file(p_request_id UUID, p_claim_token UUID, p_org_slug TEXT, source_request_id UUID)
RETURNS TABLE(size_bytes BIGINT, chunk_count INTEGER, sha256 TEXT, expires_at TIMESTAMPTZ)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE tenant_id INTEGER;
BEGIN
    tenant_id := public.retained_file_claim(p_request_id, p_claim_token, p_org_slug);
    IF tenant_id IS NULL THEN RETURN; END IF;
    RETURN QUERY SELECT stream.size_bytes, stream.chunk_count, stream.sha256, stream.expires_at
    FROM public.retained_file_streams stream
    WHERE stream.request_id = source_request_id AND stream.organization_id = tenant_id
        AND stream.sealed AND stream.expires_at > clock_timestamp();
END;
$$;

CREATE FUNCTION read_retained_file_chunk(p_request_id UUID, p_claim_token UUID, p_org_slug TEXT, source_request_id UUID, chunk_sequence INTEGER)
RETURNS BYTEA LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE tenant_id INTEGER; body BYTEA;
BEGIN
    tenant_id := public.retained_file_claim(p_request_id, p_claim_token, p_org_slug);
    IF tenant_id IS NULL THEN RETURN NULL; END IF;
    SELECT chunk.ciphertext INTO body FROM public.retained_file_chunks chunk
    JOIN public.retained_file_streams stream ON stream.request_id = chunk.request_id
    WHERE stream.request_id = source_request_id AND stream.organization_id = tenant_id
        AND stream.sealed AND stream.expires_at > clock_timestamp()
        AND chunk.sequence = chunk_sequence;
    RETURN body;
END;
$$;

CREATE FUNCTION discard_retained_file(p_request_id UUID, p_claim_token UUID, p_org_slug TEXT)
RETURNS BOOLEAN LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE tenant_id INTEGER;
BEGIN
    tenant_id := public.retained_file_claim(p_request_id, p_claim_token, p_org_slug);
    IF tenant_id IS NULL THEN RETURN FALSE; END IF;
    DELETE FROM public.retained_file_streams stream
    WHERE stream.request_id = p_request_id AND stream.organization_id = tenant_id;
    RETURN FOUND;
END;
$$;

REVOKE ALL ON FUNCTION retained_file_claim(UUID, UUID, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION begin_retained_file(UUID, UUID, TEXT, BIGINT, TIMESTAMPTZ) FROM PUBLIC;
REVOKE ALL ON FUNCTION append_retained_file_chunk(UUID, UUID, TEXT, INTEGER, INTEGER, BYTEA) FROM PUBLIC;
REVOKE ALL ON FUNCTION seal_retained_file(UUID, UUID, TEXT, BIGINT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION read_retained_file(UUID, UUID, TEXT, UUID) FROM PUBLIC;
REVOKE ALL ON FUNCTION read_retained_file_chunk(UUID, UUID, TEXT, UUID, INTEGER) FROM PUBLIC;
REVOKE ALL ON FUNCTION discard_retained_file(UUID, UUID, TEXT) FROM PUBLIC;

-- Restart matching request writers and processors for this capability change.
-- Connector completion releases I/O admission; a transfer spans its attempt.
-- Released scopes still count through provider lease/cleanup grace.
ALTER TABLE execution_requests
    ADD COLUMN org_connector_limit INTEGER NOT NULL DEFAULT 0 CHECK (org_connector_limit >= 0),
    ADD COLUMN org_file_limit INTEGER NOT NULL DEFAULT 0 CHECK (org_file_limit >= 0),
    ADD COLUMN file_transfer BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN file_transfer_scope_id UUID;
CREATE TABLE file_transfer_scopes (
    scope_id UUID PRIMARY KEY,
    organization_id INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    concurrency_limit INTEGER NOT NULL CHECK (concurrency_limit > 0),
    deadline_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    claimed_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ
);
CREATE INDEX file_transfer_scopes_org_idx ON file_transfer_scopes(organization_id, deadline_at);
CREATE INDEX file_transfer_scopes_expiry_idx ON file_transfer_scopes(expires_at);
CREATE INDEX execution_requests_org_active_idx ON execution_requests(organization_id, state, lease_expires_at);
CREATE INDEX execution_requests_file_scope_active_idx ON execution_requests(file_transfer_scope_id, state, lease_expires_at)
    WHERE file_transfer_scope_id IS NOT NULL;

CREATE OR REPLACE FUNCTION claim_execution_request(request_id UUID)
RETURNS TABLE (
    id UUID,
    organization_slug TEXT,
    kind TEXT,
    state TEXT,
    request_ciphertext BYTEA,
    result_ciphertext BYTEA,
    deadline_at TIMESTAMPTZ,
    cancel_requested_at TIMESTAMPTZ,
    claim_token UUID
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    target_data_source_id BIGINT;
    target_created_at TIMESTAMPTZ;
    target_state TEXT;
    effective_concurrency_limit INTEGER;
    active_count BIGINT;
    target_organization_id INTEGER;
    file_operation BOOLEAN;
    target_scope_id UUID;
    scope_claimed TIMESTAMPTZ;
    connector_limit INTEGER;
    file_limit INTEGER;
    active_connectors BIGINT;
    active_files BIGINT;
BEGIN
    SELECT request.data_source_id, request.created_at, request.state, request.organization_id, request.file_transfer, request.file_transfer_scope_id
    INTO target_data_source_id, target_created_at, target_state, target_organization_id, file_operation, target_scope_id
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
          OR (request.file_transfer AND (request.data_source_id IS NULL OR NOT EXISTS (
              SELECT 1 FROM public.file_transfer_scopes scope
              WHERE scope.scope_id=request.file_transfer_scope_id AND scope.organization_id=request.organization_id
                  AND scope.released_at IS NULL AND scope.deadline_at>clock_timestamp())))
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
        PERFORM organization.id FROM public.organizations organization
        WHERE organization.id = target_organization_id FOR NO KEY UPDATE;

        SELECT MIN(NULLIF(request.org_connector_limit,0)), MIN(NULLIF(request.org_file_limit,0))
        INTO connector_limit, file_limit
        FROM public.execution_requests request
        WHERE request.organization_id = target_organization_id AND (
            (request.state = 'pending' AND request.cancel_requested_at IS NULL
                AND request.deadline_at > clock_timestamp()
                AND (request.queue_deadline_at IS NULL OR request.queue_deadline_at > clock_timestamp()))
            OR (request.state IN ('running','ambiguous')
                AND request.lease_expires_at + INTERVAL '10 seconds' > clock_timestamp()));
        SELECT COUNT(*) INTO active_connectors
        FROM public.execution_requests request
        WHERE request.organization_id = target_organization_id AND request.data_source_id IS NOT NULL
            AND request.state IN ('running','ambiguous')
            AND request.lease_expires_at + INTERVAL '10 seconds' > clock_timestamp();
        IF connector_limit IS NOT NULL AND active_connectors >= connector_limit THEN RETURN; END IF;

        IF file_operation THEN
            SELECT scope.claimed_at INTO scope_claimed
            FROM public.file_transfer_scopes scope
            WHERE scope.scope_id = target_scope_id AND scope.organization_id = target_organization_id
                AND scope.released_at IS NULL AND scope.deadline_at > clock_timestamp();
            IF NOT FOUND THEN RETURN; END IF;

            SELECT COUNT(*), LEAST(file_limit, MIN(scope.concurrency_limit))
            INTO active_files, file_limit
            FROM public.file_transfer_scopes scope
            WHERE scope.organization_id = target_organization_id AND scope.claimed_at IS NOT NULL
                AND ((scope.released_at IS NULL AND scope.deadline_at > clock_timestamp())
                    OR EXISTS (SELECT 1 FROM public.execution_requests running
                        WHERE running.file_transfer_scope_id = scope.scope_id
                            AND running.organization_id = target_organization_id
                            AND running.state IN ('running','ambiguous')
                            AND running.lease_expires_at + INTERVAL '10 seconds' > clock_timestamp()));
            IF scope_claimed IS NULL AND file_limit IS NOT NULL AND active_files >= file_limit THEN RETURN; END IF;
        END IF;

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
              -- A new transfer waiting for this attempt's slot must not block
              -- the continuation that will release it on the same data source.
              AND NOT (file_operation AND scope_claimed IS NOT NULL
                  AND active_files >= file_limit AND earlier.file_transfer
                  AND earlier.file_transfer_scope_id IS DISTINCT FROM target_scope_id
                  AND NOT EXISTS (SELECT 1 FROM public.file_transfer_scopes earlier_scope
                      WHERE earlier_scope.scope_id = earlier.file_transfer_scope_id
                          AND earlier_scope.organization_id = target_organization_id
                          AND earlier_scope.claimed_at IS NOT NULL
                          AND earlier_scope.released_at IS NULL
                          AND earlier_scope.deadline_at > clock_timestamp()))
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
      AND (NOT request.file_transfer OR EXISTS (
          SELECT 1 FROM public.file_transfer_scopes scope
          WHERE scope.scope_id=request.file_transfer_scope_id AND scope.organization_id=request.organization_id
              AND scope.released_at IS NULL AND scope.deadline_at>clock_timestamp()
          FOR UPDATE
      ))
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
        IF file_operation THEN
            UPDATE public.file_transfer_scopes scope
            SET claimed_at = COALESCE(scope.claimed_at, clock_timestamp())
            WHERE scope.scope_id = target_scope_id AND scope.organization_id = target_organization_id;
        END IF;
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

-- Poller mode considers each data-source FIFO head plus unscoped work. A
-- saturated hot source therefore cannot block unrelated sources.
REVOKE ALL ON FUNCTION claim_execution_request(UUID) FROM PUBLIC;

CREATE OR REPLACE FUNCTION claim_next_execution_request()
RETURNS TABLE (
    id UUID,
    organization_slug TEXT,
    kind TEXT,
    state TEXT,
    request_ciphertext BYTEA,
    result_ciphertext BYTEA,
    deadline_at TIMESTAMPTZ,
    cancel_requested_at TIMESTAMPTZ,
    claim_token UUID
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
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
                    OR (request.file_transfer AND NOT EXISTS (
                        SELECT 1 FROM public.file_transfer_scopes scope
                        WHERE scope.scope_id=request.file_transfer_scope_id AND scope.organization_id=request.organization_id
                            AND scope.released_at IS NULL AND scope.deadline_at>clock_timestamp()))
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
                              AND NOT (request.file_transfer AND earlier.file_transfer
                                  AND request.file_transfer_scope_id IS DISTINCT FROM earlier.file_transfer_scope_id
                                  AND EXISTS (SELECT 1 FROM public.file_transfer_scopes current_scope
                                      WHERE current_scope.scope_id=request.file_transfer_scope_id
                                          AND current_scope.organization_id=request.organization_id
                                          AND current_scope.claimed_at IS NOT NULL AND current_scope.released_at IS NULL
                                          AND current_scope.deadline_at>clock_timestamp())
                                  AND NOT EXISTS (SELECT 1 FROM public.file_transfer_scopes earlier_scope
                                      WHERE earlier_scope.scope_id=earlier.file_transfer_scope_id
                                          AND earlier_scope.organization_id=earlier.organization_id
                                          AND earlier_scope.claimed_at IS NOT NULL AND earlier_scope.released_at IS NULL
                                          AND earlier_scope.deadline_at>clock_timestamp()))
                        )
                        AND COALESCE((
                            SELECT (
                                SELECT COUNT(*) FROM public.execution_requests org_active
                                WHERE org_active.organization_id=request.organization_id
                                    AND org_active.data_source_id IS NOT NULL
                                    AND org_active.state IN ('running','ambiguous')
                                    AND org_active.lease_expires_at + INTERVAL '10 seconds'>clock_timestamp()
                            ) < MIN(NULLIF(live.org_connector_limit,0))
                            FROM public.execution_requests live
                            WHERE live.organization_id=request.organization_id AND (
                                    (live.state='pending' AND live.cancel_requested_at IS NULL
                                        AND live.deadline_at>clock_timestamp()
                                        AND (live.queue_deadline_at IS NULL OR live.queue_deadline_at>clock_timestamp()))
                                    OR (live.state IN ('running','ambiguous')
                                        AND live.lease_expires_at + INTERVAL '10 seconds'>clock_timestamp()))
                        ),TRUE)
                        AND (NOT request.file_transfer OR EXISTS (
                            SELECT 1 FROM public.file_transfer_scopes own_scope
                            WHERE own_scope.scope_id=request.file_transfer_scope_id
                                AND own_scope.organization_id=request.organization_id
                                AND own_scope.released_at IS NULL AND own_scope.deadline_at>clock_timestamp()
                                AND (own_scope.claimed_at IS NOT NULL OR (
                                    SELECT COUNT(*) < LEAST(request.org_file_limit,MIN(active_scope.concurrency_limit),(
                                        SELECT MIN(NULLIF(live.org_file_limit,0)) FROM public.execution_requests live
                                        WHERE live.organization_id=request.organization_id AND (
                                    (live.state='pending' AND live.cancel_requested_at IS NULL
                                        AND live.deadline_at>clock_timestamp()
                                        AND (live.queue_deadline_at IS NULL OR live.queue_deadline_at>clock_timestamp()))
                                    OR (live.state IN ('running','ambiguous')
                                        AND live.lease_expires_at + INTERVAL '10 seconds'>clock_timestamp()))
                                    ))
                                    FROM public.file_transfer_scopes active_scope
                                    WHERE active_scope.organization_id=request.organization_id AND active_scope.claimed_at IS NOT NULL
                                        AND ((active_scope.released_at IS NULL AND active_scope.deadline_at>clock_timestamp())
                                            OR EXISTS (SELECT 1 FROM public.execution_requests running
                                                WHERE running.file_transfer_scope_id=active_scope.scope_id
                                                    AND running.organization_id=request.organization_id
                                                    AND running.state IN ('running','ambiguous')
                                                    AND running.lease_expires_at + INTERVAL '10 seconds'>clock_timestamp()))
                                ))
                        ))
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
        BEGIN
            RETURN QUERY
            SELECT * FROM public.claim_execution_request(candidate_id);
            IF FOUND THEN RETURN; END IF;
            -- A denied candidate must not retain request/org locks while this
            -- poller tries another candidate that a competing poller holds.
            RAISE EXCEPTION USING ERRCODE='BP001', MESSAGE='execution admission candidate declined';
        EXCEPTION WHEN SQLSTATE 'BP001' THEN
            NULL;
        END;
    END LOOP;
END;
$$;

REVOKE ALL ON FUNCTION claim_next_execution_request() FROM PUBLIC;
