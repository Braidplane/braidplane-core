# HTTP OAuth reference

For the user walkthrough, see [HTTP OAuth setup](HTTP.md#oauth-setup).

## App settings

Supply `credentials.oauth`; these settings are encrypted per organization/source,
write-only, and mutually exclusive with static bearer/basic credentials.

| Field | Required | Notes |
|---|---|---|
| `grant_type` | yes | `authorization_code` or `client_credentials` |
| `issuer` | yes | Exact authorization-server issuer URL |
| `token_endpoint` | yes | HTTPS token endpoint |
| `client_id` | yes | ID of the app registered with the provider |
| `client_secret` | for client credentials | Also used by confidential authorization-code apps; public PKCE apps can omit it |
| `authorization_endpoint` | for authorization code | HTTPS consent endpoint |
| `scopes` | no | Array of provider scope names; include offline access when required for refresh tokens |
| `audience` | no | Sent as the OAuth `audience` request parameter |
| `revocation_endpoint` | no | HTTPS endpoint used when revoking the current grant |

The operator configures the API callback in
[HTTP OAuth configuration](../../CONFIG.md#customer-http-oauth). Register that
exact URL on the provider app; it is separate from the frontend URL and cannot
be supplied in customer credentials.

## API and CLI setup

This CLI request file creates a client-credentials source:

```json
{
  "name": "Customer API",
  "slug": "customer-api",
  "type": "http",
  "config": {
    "base_url": "https://api.example.com/v1",
    "health_path": "health"
  },
  "credentials": {
    "oauth": {
      "issuer": "https://auth.example.com",
      "token_endpoint": "https://auth.example.com/token",
      "client_id": "customer-app",
      "client_secret_file": "secrets/client-secret",
      "grant_type": "client_credentials",
      "scopes": ["records.read"]
    }
  }
}
```

The CLI resolves `client_secret_file` relative to the request file; the API uses
`client_secret` instead. For authorization code, change `grant_type`, add
`authorization_endpoint`, and omit the secret for public PKCE apps.

```bash
data-source create http --file source.json
data-source provider connect-http customer-api
```

The connect API is
`POST /organizations/{slug}/data-sources/{ds_slug}/http-oauth/connect`. It returns
`data_source` and, for consent, `install.authorization_url`. Open that URL while
signed into Braidplane.

Replace settings with `data-source credentials update --file credentials.json`,
including `type`, `slug` and the complete `credentials.oauth` object, then connect
again. Use `connect-http` for either grant; generic provider `rotate` starts
browser authorization only.

```bash
data-source provider grant refresh customer-api
data-source provider grant revoke customer-api
```

Refresh forces renewal. Revoke disables the local grant and attempts provider
revocation if configured. Otherwise tokens remain valid remotely. Replacement
does not revoke old provider tokens: revoke before replacing, or with the provider
afterward. Refresh/revocation retain the issuing app settings; changing the source
URL cannot redirect existing tokens.

## Provider compatibility and security

- API and OAuth URLs must use HTTPS. Endpoints, scopes and audience are explicit;
  there is no discovery from the API URL or OpenAPI document.
- Authorization code requires PKCE S256 and a callback `iss` matching the issuer.
  One-time state and source-revision checks reject replay and stale consent.
- Confidential apps use `client_secret_basic`; public code clients send
  `client_id`. Tokens must be Bearer with positive `expires_in`; returned scopes
  must cover requested access.
- `audience` is supported; alternative resource parameters, discovery, dynamic
  registration, device/implicit/password flows, JWT assertions and custom token
  authentication are unsupported.
- Tokens are bound to the HTTPS origin, port and base path, including redirects.
  API/OAuth requests enforce host/DNS checks; private access needs deployment
  permission. OpenAPI imports do not send OAuth tokens, so protected specs cannot
  be imported through this path.
