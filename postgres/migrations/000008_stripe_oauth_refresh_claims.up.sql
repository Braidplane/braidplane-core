ALTER TABLE credential_oauth_grants
    ADD COLUMN refresh_claim_id text,
    ADD COLUMN refresh_claim_expires_at timestamptz;
