# OWASP ASVS verification matrix

This matrix tracks the 70 requirements in OWASP ASVS 5.0.0 Level 1 for Braidplane Community Core in its documented self-hosted configuration. The dispositions below apply to the named installation. The local Level 1 assessment is tracked in [#159](https://github.com/Braidplane/braidplane-core/issues/159). Level 2 is tracked in [#168](https://github.com/Braidplane/braidplane-core/issues/168). [OWASP ASVS releases](https://github.com/OWASP/ASVS/releases)

## Release evidence

The Core maintainer updates this matrix when reviewing a release. Use the following statuses.

| Status | Meaning |
| --- | --- |
| Pending | Release verification is open. |
| Verified | The requirement passed for the named release and configuration. |
| Not applicable | The named configuration lacks the feature or condition addressed by the requirement. |

For each Verified or Not applicable row, link to a sanitized release record or a public check that supports the result. Record the release tag, source revision, installer digest, platform, configuration, and verification date in the linked evidence. Review which rows a change affects before carrying their results forward. Reopen affected rows until the new evidence is ready.

Keep findings and sensitive test material in the private assessment record. Use the reporting channels in the [security policy](../SECURITY.md). A Level 1 conformance claim requires passing evidence for every applicable requirement and a documented reason for each Not applicable row.

## Candidate verification record

Of the 70 requirements, 18 are Verified, 8 are Not applicable and 44 are Pending. Level 1 conformance remains unverified.

On 2026-11-19 the assessment used **v0.2.0-rc.16-linux-arm64**, source `4ad19d2c69edbb40e55357cdb65e671d73065930`, on a Linux ARM64 Docker daemon (Engine 29.8.2, Compose 5.5.0). The merge commits for [#175](https://github.com/Braidplane/braidplane-core/pull/175) and [#182](https://github.com/Braidplane/braidplane-core/pull/182) are ancestors of this source. The downloaded `installation.tar.gz` had SHA-256 `8e1511b721b00e2623cdef544c2eb1cf9ece95ccb2dd2de9d4a36223e1dab621`. The repository's attestation verifier matched the archive, candidate revision, signer revision and release workflow run. [Release assets](https://github.com/Braidplane/braidplane-core/releases/tag/v0.2.0-rc.16-linux-arm64) · [Signing run](https://github.com/Braidplane/braidplane-core/actions/runs/44530568747)

The installer prepared the full single-host self-hosted profile with its pinned images, TLS edge at `app.customer.test` on loopback TCP 443, synthetic CA, verified database TLS, closed registration and separate service identities. The SMTP address was a fixture placeholder. No external identity provider was configured. Preflight passed. The account journey used a fresh database, operator bootstrap, and synthetic credentials. It checked the authenticated identity through the profile API before and after the password change.

The last column records the method and result for each supported row, or the specific retest for a Pending row. Source references refer to the pinned revision. Its unit, browser, Bruno and four Go E2E jobs passed. CI supports individual controls only where the named checks exercise them. Earlier assessment counts describe a different revision. [Candidate source](https://github.com/Braidplane/braidplane-core/tree/4ad19d2c69edbb40e55357cdb65e671d73065930) · [Candidate CI](https://github.com/Braidplane/braidplane-core/actions/runs/44527892067) · [Release verification issue](https://github.com/Braidplane/braidplane-core/issues/183)

## Level 1 requirements

| Requirement | Area | Release status | Evidence or required retest |
| --- | --- | --- | --- |
| `V1.2.1` | Encoding and sanitization | Pending | Compare HTML, attribute and response-header encoding tests with the current output inventory. |
| `V1.2.2` | Encoding and sanitization | Pending | Review generated links across the UI and verify scheme and component handling for each URL source. |
| `V1.2.3` | Encoding and sanitization | Pending | Check dynamic JSON and JavaScript serialization in the browser build shipped with this release. |
| `V1.2.4` | Encoding and sanitization | Pending | Map SQL and document-query bindings across the supported connectors and check their typed parameter contracts. |
| `V1.2.5` | Encoding and sanitization | Pending | Review process argument boundaries across Git, SSH and other subprocess callers in the shipped images. |
| `V1.3.1` | Encoding and sanitization | Not applicable | [Candidate record](#candidate-verification-record) · The browser package and Builder views provide plain-text source editing. The contenteditable check in builder_canvas_interactions.js guards keyboard handling. The UI has no rich HTML editor or equivalent untrusted HTML authoring feature. |
| `V1.3.2` | Encoding and sanitization | Pending | Review the enabled expression evaluators and connector scripting controls with representative configurations. |
| `V1.5.1` | Encoding and sanitization | Pending | Check the parser settings and dependency behavior of each XML ingestion path, including spreadsheets. |
| `V2.1.1` | Validation and business logic | Pending | Reconcile documented input constraints with the current API and pipeline schemas. |
| `V2.2.1` | Validation and business logic | Pending | Check boundary values across security-relevant account, pipeline and connector inputs. |
| `V2.2.2` | Validation and business logic | Pending | Trace those input rules to service entry points for both API and browser submissions. |
| `V2.3.1` | Validation and business logic | Pending | Exercise the ordered steps of the enabled multistep flows with synthetic provider fixtures. |
| `V3.2.1` | Web security | Pending | Inventory response-context controls for UI, API, static assets and generated downloads. |
| `V3.2.2` | Web security | Pending | Review browser DOM text sinks against the shipped bundles and their rendering tests. |
| `V3.3.1` | Web security | Verified | [Candidate record](#candidate-verification-record) · The installed login, account, password-change and logout responses used Secure cookies with __Host- or __Secure- names. Both session stores select the name through pkg/httpsecurity. The installer enables secure cookies on both. |
| `V3.4.1` | Web security | Verified | [Candidate record](#candidate-verification-record) · The packaged Caddyfile sets one-year HSTS on proxied responses and edge errors. The installed edge returned max-age=31536000 on HTML, redirects and an unauthenticated API response. |
| `V3.4.2` | Web security | Verified | [Candidate record](#candidate-verification-record) · The installed config allowlists only the application HTTPS origin. The same API profile request returned that exact origin for the allowed origin and omitted Access-Control-Allow-Origin for a second synthetic origin. |
| `V3.5.1` | Web security | Pending | Verify origin or transaction binding across cookie-authenticated write routes and provider callbacks. |
| `V3.5.2` | Web security | Pending | Classify sensitive routes by reliance on preflight and verify their request-header requirements. |
| `V3.5.3` | Web security | Pending | Compare the registered methods and callback contracts with the inventory of state changes. |
| `V4.1.1` | APIs and web services | Pending | HTML and JSON returned matching media types and charsets. Complete the inventory for streams, downloads, static files and errors. |
| `V4.4.1` | APIs and web services | Not applicable | [Candidate record](#candidate-verification-record) · The route inventory and browser client contain no WebSocket endpoint or client. The installer exposes only the HTTPS frontend and proxied API. |
| `V5.2.1` | File handling | Pending | Verify size limits for uploads, connector transfers, archive processing and Git input under documented resource limits. |
| `V5.2.2` | File handling | Pending | Map file formats used in security or business decisions and validate extension and content handling for each. |
| `V5.3.1` | File handling | Not applicable | [Candidate record](#candidate-verification-record) · The static handlers in frontend/server/main.go serve packaged icons and assets. The frontend root is read-only and the installation mounts no user-writable content into those directories. |
| `V5.3.2` | File handling | Pending | Review filename and path handling across local retention and remote transfer adapters. |
| `V6.1.1` | Authentication | Verified | [Candidate record](#candidate-verification-record) · The abuse controls section of CONFIG.md defines the shared client budget, expiry, trusted proxies and retry behavior. It explains how the client budget prevents another client from locking the subject out. |
| `V6.2.1` | Authentication | Verified | [Candidate record](#candidate-verification-record) · The account creation and change models enforce an eight-character minimum. Both bootstrap input modes pass through Bootstrapper.Bootstrap, which validates model.NewUser before hashing. The installed login form also declares the minimum. |
| `V6.2.2` | Authentication | Verified | [Candidate record](#candidate-verification-record) · A bootstrapped user changed their password through the installed account security form, signed in with the replacement and read their authenticated API profile. |
| `V6.2.3` | Authentication | Verified | [Candidate record](#candidate-verification-record) · The installed change form accepted the current and new passwords. The candidate's TestChangePassword covers both successful verification and refusal to update when the current password is wrong. |
| `V6.2.4` | Authentication | Verified | [Candidate record](#candidate-verification-record) · The account service checks the embedded 3,000-entry list at registration, reset and change. The ranked source and length filter are documented in pkg/passwordpolicy/DATA_SOURCE.txt. Candidate unit coverage checks list cardinality and rejection before persistence. |
| `V6.2.5` | Authentication | Verified | [Candidate record](#candidate-verification-record) · Source review found length and common-password checks, with raw UTF-8 bootstrap input available. A lowercase passphrase with spaces worked through installed bootstrap, change and sign-in. |
| `V6.2.6` | Authentication | Verified | [Candidate record](#candidate-verification-record) · The installed login rendered a masked password field. The login, new-password, confirmation and account-change templates use password inputs throughout. |
| `V6.2.7` | Authentication | Verified | [Candidate record](#candidate-verification-record) · Reviewed the account password templates and browser handlers. Inputs permit paste and declare current-password or new-password autocomplete. The installed sign-in page served current-password. |
| `V6.2.8` | Authentication | Pending | Verify exact preservation of whitespace, case and Unicode through all account creation, change and authentication paths. |
| `V6.3.1` | Authentication | Pending | Confirm documented client and subject budgets behind the installed proxy chain using distinct synthetic clients. |
| `V6.3.2` | Authentication | Pending | Inventory application accounts immediately after migration and before the first operator bootstrap. |
| `V6.4.1` | Authentication | Pending | Check generation, lifetime and single use for every invitation, activation and temporary credential flow. |
| `V6.4.2` | Authentication | Verified | [Candidate record](#candidate-verification-record) · Reviewed account models, API schema, sign-in and recovery views. The local account flows provide email recovery and contain no password hint or knowledge-based challenge. |
| `V7.2.1` | Session management | Pending | Trace frontend and API session checks for every principal type, including revoked accounts. |
| `V7.2.2` | Session management | Verified | [Candidate record](#candidate-verification-record) · Both services use the Fiber session store with its default key generator. Two independent HTTPS sign-ins received distinct session identifiers and each authorized a read of the same synthetic profile. [Fiber v3.5.0 session configuration](https://github.com/gofiber/fiber/blob/v3.5.0/middleware/session/config.go) |
| `V7.2.3` | Session management | Verified | [Candidate record](#candidate-verification-record) · Both session configurations use the pinned SecureToken generator. It draws 32 bytes from crypto/rand and base64url-encodes all 256 bits. Independent sign-ins returned distinct identifiers. [Generator implementation](https://github.com/gofiber/utils/blob/v2.4.1/common.go) |
| `V7.2.4` | Session management | Pending | Verify rotation and backend retirement of the previous token at sign-in and reauthentication. |
| `V7.4.1` | Session management | Pending | Logout redirected the client to sign-in. Verify backend invalidation and expiry for each session path. |
| `V7.4.2` | Session management | Pending | Verify all independent sessions terminate after account disablement or deletion on both frontend and API. |
| `V8.1.1` | Authorization | Pending | Reconcile the documented operation, principal and resource rules with the complete protected route inventory. |
| `V8.2.1` | Authorization | Pending | Compare the approved function-permission inventory with service enforcement and role fixtures. |
| `V8.2.2` | Authorization | Pending | Complete the resource and cross-organization authorization checks across the installed routes. |
| `V8.3.1` | Authorization | Pending | Trace trusted authorization enforcement for each route and non-browser entry point. |
| `V9.1.1` | Self-contained tokens | Pending | Inventory token consumers and verify the signature or MAC requirement of each enabled verifier. |
| `V9.1.2` | Self-contained tokens | Pending | Check algorithm allowlists across all self-contained token consumers and issuer configurations. |
| `V9.1.3` | Self-contained tokens | Pending | Verify key selection and issuer trust for each applicable token consumer. |
| `V9.2.1` | Self-contained tokens | Pending | Verify time-claim enforcement across the configured token consumers. |
| `V10.4.1` | OAuth and OpenID Connect | Not applicable | [Candidate record](#candidate-verification-record) · Core has no authorization server that registers client redirect URIs. The OIDC and connector code consume an external provider. |
| `V10.4.2` | OAuth and OpenID Connect | Not applicable | [Candidate record](#candidate-verification-record) · The installed Core services do not issue or redeem their own OAuth authorization codes as an authorization server. |
| `V10.4.3` | OAuth and OpenID Connect | Not applicable | [Candidate record](#candidate-verification-record) · An external provider owns authorization-code issuance and lifetime. The installation has no authorization-server role. |
| `V10.4.4` | OAuth and OpenID Connect | Not applicable | [Candidate record](#candidate-verification-record) · The client selects grants for outbound provider flows. The installation exposes no authorization server that grants them to clients. |
| `V10.4.5` | OAuth and OpenID Connect | Not applicable | [Candidate record](#candidate-verification-record) · The installation has no authorization server that issues refresh tokens to public clients. |
| `V11.3.1` | Cryptography | Pending | Review block modes and padding across application and enabled connector cryptography. |
| `V11.3.2` | Cryptography | Pending | Inventory ciphers and modes across credential storage, payload retention and configured integrations. |
| `V11.4.1` | Cryptography | Pending | Classify hash uses in application and connector code by purpose and verify permitted algorithms. |
| `V12.1.1` | Secure communication | Verified | [Candidate record](#candidate-verification-record) · The edge negotiated TLS 1.3 by default and pg_stat_ssl reported TLS 1.3 for active database TLS connections. On both listeners, explicit TLS 1.2 and 1.3 handshakes succeeded and TLS 1.0 and 1.1 received protocol-version alerts. |
| `V12.2.1` | Secure communication | Verified | [Candidate record](#candidate-verification-record) · The installed Compose configuration publishes only loopback edge TCP 443. The client validated the fixture CA and hostname for login, account changes and API reads over HTTPS. |
| `V12.2.2` | Secure communication | Pending | The client-facing HTTPS edge used a fixture CA. Retest with the operator’s public certificate chain and an unmodified public trust store to establish public trust. |
| `V13.4.1` | Configuration | Verified | [Candidate record](#candidate-verification-record) · Inspected exported root filesystem entries for the running edge, frontend, API, worker, executor and database. None contained .git or .svn. Mounts were fixture configuration, keys and fresh service volumes. |
| `V14.2.1` | Data protection | Pending | Review URLs, redirects and message delivery across verification, recovery and provider flows. |
| `V14.3.1` | Data protection | Pending | Check client data removal across logout, expiration, browser history and offline storage. |
| `V15.1.1` | Secure coding and architecture | Verified | [Candidate record](#candidate-verification-record) · The candidate SECURITY.md assigns weekly advisory review, a 90-day routine update cycle and risk-specific remediation deadlines. It defines the clock start, exception approval and expiry. |
| `V15.2.1` | Secure coding and architecture | Pending | Compare all six pinned images and bundled dependencies against dated advisories, routine update deadlines and approved exceptions. |
| `V15.3.1` | Secure coding and architecture | Pending | Inventory response projections for each principal and compare their fields with the operation contract. |
