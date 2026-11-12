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
