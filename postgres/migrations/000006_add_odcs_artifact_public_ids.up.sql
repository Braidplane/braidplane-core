SET search_path = public;

ALTER TABLE odcs_artifacts
    ADD COLUMN public_id TEXT NOT NULL
    DEFAULT translate(encode(uuid_send(gen_random_uuid()), 'base64'), '+/=', '-_');

ALTER TABLE odcs_artifacts
    ADD CONSTRAINT odcs_artifacts_public_id_key UNIQUE (public_id);
