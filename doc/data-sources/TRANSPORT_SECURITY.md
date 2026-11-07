# Connector transport encryption

Connectors require authenticated encrypted transport by default. HTTP and S3
endpoints, HTTP OpenAPI imports, and operator HubSpot, GitLab OAuth, and BigQuery endpoints
require HTTPS. HTTP, OpenAPI, and S3 redirects cannot downgrade HTTPS. HubSpot,
Google, and Git provider API clients refuse redirects. Git remotes require HTTPS
or SSH. PostgreSQL defaults to and requires `ssl_mode: verify-full`; MySQL and
MongoDB use certificate-verified TLS, including selected-resource metadata observations. FTP uses explicit FTPS for the control and
passive data channels, with no fallback to raw FTP. Snowflake uses its driver's
verified HTTPS transport. Certificates must chain to the runtime trust store
and match the configured hostname; customer certificate bypasses are unavailable.

Local fixture deployments can explicitly enable the backend-only
[`security.allow_plaintext_connectors`](../../CONFIG.md#security-configuration)
policy. It permits initial HTTP endpoints, raw FTP and local database fixtures;
it never permits an HTTPS redirect downgrade. Staging and production startup
reject this setting. The checked-in configuration keeps it disabled; generated
worktree fixture environments enable it explicitly for their local mock services.
`allow_private_hosts` controls network reachability only
and never relaxes transport encryption. Existing insecure saved configurations
must be updated before connection tests, schema imports, or execution can succeed.
