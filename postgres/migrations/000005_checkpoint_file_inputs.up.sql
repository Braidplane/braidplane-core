SET search_path = public;

-- Private associations preserve original bytes without exposing capabilities in run JSON.
CREATE TABLE pipeline_checkpoint_files (
    checkpoint_id BIGINT NOT NULL REFERENCES pipeline_checkpoint(id) ON DELETE CASCADE,
    step_slug TEXT NOT NULL,
    request_id UUID NOT NULL REFERENCES retained_file_streams(request_id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (checkpoint_id, step_slug)
);
CREATE INDEX pipeline_checkpoint_files_request_idx ON pipeline_checkpoint_files(request_id, expires_at);

-- The reader still needs its own live claim. A checkpoint extends only the
-- original sealed input's retention, never a dead executor's ownership.
CREATE OR REPLACE FUNCTION read_retained_file(p_request_id UUID, p_claim_token UUID, p_org_slug TEXT, source_request_id UUID)
RETURNS TABLE(size_bytes BIGINT, chunk_count INTEGER, sha256 TEXT, expires_at TIMESTAMPTZ)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE tenant_id INTEGER;
BEGIN
    tenant_id := public.retained_file_claim(p_request_id, p_claim_token, p_org_slug);
    IF tenant_id IS NULL THEN RETURN; END IF;
    RETURN QUERY SELECT stream.size_bytes, stream.chunk_count, stream.sha256,
        GREATEST(stream.expires_at, COALESCE((SELECT max(pin.expires_at) FROM public.pipeline_checkpoint_files pin WHERE pin.request_id=stream.request_id), stream.expires_at))
    FROM public.retained_file_streams stream
    WHERE stream.request_id = source_request_id AND stream.organization_id = tenant_id
        AND stream.sealed AND (stream.expires_at > clock_timestamp() OR EXISTS (
            SELECT 1 FROM public.pipeline_checkpoint_files pin WHERE pin.request_id=stream.request_id AND pin.expires_at > clock_timestamp()));
END;
$$;

CREATE OR REPLACE FUNCTION read_retained_file_chunk(p_request_id UUID, p_claim_token UUID, p_org_slug TEXT, source_request_id UUID, chunk_sequence INTEGER)
RETURNS BYTEA LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE tenant_id INTEGER; body BYTEA;
BEGIN
    tenant_id := public.retained_file_claim(p_request_id, p_claim_token, p_org_slug);
    IF tenant_id IS NULL THEN RETURN NULL; END IF;
    SELECT chunk.ciphertext INTO body FROM public.retained_file_chunks chunk
    JOIN public.retained_file_streams stream ON stream.request_id = chunk.request_id
    WHERE stream.request_id = source_request_id AND stream.organization_id = tenant_id
        AND stream.sealed AND (stream.expires_at > clock_timestamp() OR EXISTS (
            SELECT 1 FROM public.pipeline_checkpoint_files pin WHERE pin.request_id=stream.request_id AND pin.expires_at > clock_timestamp()))
        AND chunk.sequence = chunk_sequence;
    RETURN body;
END;
$$;
