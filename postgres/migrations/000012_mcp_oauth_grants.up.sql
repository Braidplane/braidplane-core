CREATE TABLE mcp_oauth_machine_clients (
    issuer text NOT NULL,
    audience text NOT NULL,
    client_id text NOT NULL,
    client_secret_hash text NOT NULL CHECK (length(client_secret_hash) = 64),
    api_key_id bigint NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    api_key_token_hash text NOT NULL,
    operation_scope_keys text[] NOT NULL DEFAULT '{}',
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    PRIMARY KEY (issuer, audience, client_id)
);

CREATE INDEX mcp_oauth_machine_clients_key_idx ON mcp_oauth_machine_clients(api_key_id);
CREATE INDEX mcp_oauth_machine_clients_expiry_idx ON mcp_oauth_machine_clients(expires_at);

CREATE TABLE mcp_oauth_grants (
    grant_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issuer text NOT NULL,
    audience text NOT NULL,
    client_id text NOT NULL,
    user_id integer REFERENCES users(id) ON DELETE CASCADE,
    member_id integer REFERENCES organization_members(id) ON DELETE CASCADE,
    machine_client_id text,
    authentication_version bigint,
    operation_scope_keys text[] NOT NULL DEFAULT '{}',
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    FOREIGN KEY (issuer, audience, machine_client_id)
        REFERENCES mcp_oauth_machine_clients(issuer, audience, client_id) ON DELETE CASCADE,
    CHECK (num_nonnulls(user_id, member_id, machine_client_id) = 1),
    CHECK ((machine_client_id IS NULL AND authentication_version IS NOT NULL) OR
           (machine_client_id IS NOT NULL AND machine_client_id = client_id AND authentication_version IS NULL))
);

CREATE INDEX mcp_oauth_grants_user_idx ON mcp_oauth_grants(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX mcp_oauth_grants_member_idx ON mcp_oauth_grants(member_id) WHERE member_id IS NOT NULL;
CREATE INDEX mcp_oauth_grants_machine_client_idx ON mcp_oauth_grants(issuer, audience, machine_client_id)
    WHERE machine_client_id IS NOT NULL;
CREATE INDEX mcp_oauth_grants_expiry_idx ON mcp_oauth_grants(expires_at);
CREATE INDEX mcp_oauth_grants_revoked_idx ON mcp_oauth_grants(revoked_at) WHERE revoked_at IS NOT NULL;

CREATE TABLE mcp_oauth_credentials (
    credential_hash text PRIMARY KEY CHECK (length(credential_hash) = 64),
    grant_id bigint NOT NULL REFERENCES mcp_oauth_grants(grant_id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('code', 'access', 'refresh')),
    redirect_uri text NOT NULL,
    challenge text NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    CHECK ((kind = 'code' AND redirect_uri <> '' AND challenge <> '') OR
           (kind <> 'code' AND redirect_uri = '' AND challenge = '')),
    CHECK (kind <> 'access' OR used_at IS NULL)
);

CREATE INDEX mcp_oauth_credentials_grant_idx ON mcp_oauth_credentials(grant_id);
CREATE INDEX mcp_oauth_credentials_unused_code_expiry_idx ON mcp_oauth_credentials(expires_at)
    WHERE kind = 'code' AND used_at IS NULL;

CREATE TABLE mcp_oauth_pending_consents (
    nonce_hash text PRIMARY KEY CHECK (length(nonce_hash) = 64),
    grant_id bigint NOT NULL UNIQUE REFERENCES mcp_oauth_grants(grant_id) ON DELETE CASCADE,
    session_hash text NOT NULL CHECK (length(session_hash) = 64),
    redirect_uri text NOT NULL,
    challenge text NOT NULL,
    client_state text NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE INDEX mcp_oauth_pending_consents_expiry_idx ON mcp_oauth_pending_consents(expires_at);

CREATE VIEW active_mcp_oauth_user_grants AS
SELECT g.grant_id, g.issuer, g.audience, g.client_id, u.slug AS user_slug,
    COALESCE(o.slug, '')::text AS organization_slug,
    u.id AS user_id, COALESCE(m.id, 0)::integer AS member_id, g.operation_scope_keys
FROM mcp_oauth_grants g
LEFT JOIN organization_members m ON m.id = g.member_id
LEFT JOIN organizations o ON o.id = m.organization_id
JOIN users u ON u.id = COALESCE(g.user_id, m.user_id)
WHERE g.revoked_at IS NULL AND g.expires_at > clock_timestamp()
    AND u.verified_at IS NOT NULL AND u.deleted_at IS NULL
    AND u.authentication_version = g.authentication_version;

CREATE VIEW active_mcp_oauth_machine_clients AS
SELECT c.issuer, c.audience, c.client_id, c.client_secret_hash, c.api_key_id,
    k.slug AS api_key_slug, o.slug AS organization_slug,
    LEAST(c.expires_at, k.expires_at) AS expires_at, c.operation_scope_keys
FROM mcp_oauth_machine_clients c
JOIN api_keys k ON k.id = c.api_key_id AND k.token_hash = c.api_key_token_hash
JOIN organizations o ON o.id = k.organization_id
JOIN roles r ON r.id = k.role_id AND r.organization_id = k.organization_id
WHERE c.revoked_at IS NULL AND c.expires_at > clock_timestamp()
    AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > clock_timestamp())
    AND r.deleted_at IS NULL;

CREATE VIEW active_mcp_oauth_machine_grants AS
SELECT g.grant_id, g.issuer, g.audience, g.client_id, c.api_key_id, c.api_key_slug, c.organization_slug, g.operation_scope_keys
FROM mcp_oauth_grants g
JOIN active_mcp_oauth_machine_clients c
    ON (c.issuer, c.audience, c.client_id) = (g.issuer, g.audience, g.machine_client_id)
WHERE g.revoked_at IS NULL AND g.expires_at > clock_timestamp();
