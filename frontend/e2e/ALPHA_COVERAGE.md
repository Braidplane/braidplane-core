# Historical Alpha browser coverage inventory

For current core selection, use the [Community coverage policy](README.md#community-coverage-policy).
The measurements below describe earlier Alpha and combined product/website runs;
they do not qualify the current core revision.

Baseline: fresh `8306adac2`, canonical `task test-e2e`, 2026-10-09 UTC.
The baseline discovered 220 logical cases / 660 executions: 605 passed, 55
skipped, three engines, four workers. Public landing, analytics and docs
accounted for 75 executions inside that product default; the product-only
comparison denominator is 585 executions / 195 logical cases.

The product baseline replacement kept seven product journeys / 17 executions: Chromium ran
all seven; Firefox and WebKit run query, Git deploy, successful run inspection,
Proof discovery and Reconnect. Sign-in and persisted builder mutation run once
because of the production subject throttle and shared seeded draft. There are
no product viewport/provider loops. Public coverage is separately six cases /
18 executions: three journeys at desktop and phone widths in all three engines.

## Browser reliability checkpoint

The original 2026-10-27 checkpoint had 64 logical product cases / 178 executions
and 14 public cases / 42 executions. Main added one Chromium fixture lifecycle
check, so the reliability-only selection is now 65/179 product and 14/42 public.
This slice preserves that selection so coverage placement and cost can be
reviewed separately. The later addition notes are historical snapshots.

[CI run 40563262194](https://github.com/Braidplane/braidplane-core/actions/runs/40563262194)
provides a failing baseline: 175 product executions passed, one failed and two
were flaky with one configured retry. The query summary returned a parallel
acknowledgement journey's query, acknowledgement reload raced navigation, and
signup exceeded its 30-second teardown hook twice. The responsible browser step
was marked successful by `continue-on-error`; the outcome checker failed later.

| Baseline component | Observed wall time |
| --- | ---: |
| Go job queue/admission (created to started) | 609s |
| Preceding Go test command | 763s |
| Runtime job queue/admission (created to started) | 40s |
| Runtime setup before product browser command | 116s |
| Product browser command, including setup and cleanup | 537s |
| Public browser command | 44s |
| Bruno command | 151s |
| CLI command | 64s |
| Runtime job total | 931s |

The public, Bruno and CLI commands consumed another 259 seconds after known
product failure. This is observed serial work, not a prediction of healthy-run
savings. Queue/admission is GitHub job timing and does not isolate host admission
from scheduling. Browser fixture setup is not separately measured in this log.
The baseline retained 26 product PNGs (3,163,526 bytes), two product traces
(10,542,184 bytes), and 87 public PNGs (35,870,948 bytes); no video was written.
Local results and CI results must remain separate.

## Coverage placement and budget

The executable [manifest](alpha-inventory.json) records every retained title,
profile, cross-browser decision and reason. Run
`bash scripts/node-toolchain.sh node scripts/check-browser-inventory.cjs` from
the repository root to collect the core product tests in full and critical
profiles, compare their selections and enforce the product caps below. The
website entries describe the earlier combined inventory and are historical:

| Suite/profile | Logical cases | Executions | Workers | Runner wall cap |
| --- | ---: | ---: | ---: | ---: |
| Product critical (local default; every CI route) | 15 | 25 | CI 2 / local 4 | 240s |
| Product full (explicit broader validation) | 64 | 174 | CI 2 / local 4 | 660s |
| Public critical | 6 | 18 | 3 | 60s |
| Public full (explicit broader validation) | 14 | 42 | 3 | 120s |

Routine CI and manual dispatches use the critical profile regardless of changed
paths. Product and public runner deadlines total 300 seconds; setup/builds and
runtime readiness are accounted separately. Full coverage remains available
explicitly, so extended regressions are deferred until a broader run. This
replaces automatic full selection that passed all 174 tests in 9.4–9.8 minutes
but then failed its eight-minute measurement gate on the CI host.

Product critical expands to Chromium 15, Firefox 5 and WebKit 5; full expands
to 64/55/55. Both public profiles use three engines and desktop/compact widths.
Runner caps allow cold/host variance above measured local elapsed times; CI
measurement steps enforce them without retries. Recalibrate from representative
CI evidence rather than raising them to hide a failing or incomplete suite.

The original product baseline was 7/17 and public 6/18. Later features added
Google consent, password recovery, HTTP OAuth, Slack setup, completion/connected
runs, execution progress and Builder/mapping authoring. Those additions contain
real browser handoffs. Selection drift came from placing all declarations in
retained spec files into the default three-engine selection, including state,
provider and viewport loops. Public growth added hero animation, pricing,
solutions media and a second documentation source at both widths. Existing
addition notes below are historical snapshots, not current totals.

History pointers include `342c4db3c` (completion), `ac12bfe59` (public site),
`bc2541632` (run history), `188cf4575` and `28ba205af` (mapping), `225688bdd`
(Google), `aa19fc761` (connected runs), `78202989a` (Builder/Git), and
`b762f0e8c` (progress). Current inventory comes from collected source, not the
historical prose totals.

### Retained, moved and retired assertions

The grouped rows cover every manifest case. Broader means the browser assertion
runs only when broader validation is explicitly requested. Lower-layer
tests listed beside it do not prove its CSS, focus or browser navigation.

| Area (retained logical cases) | Decision and proving layer |
| --- | --- |
| Sign-in/recovery (2), Google consent/lifecycle (3) | Consent/auth are critical Chromium. The upstream idle-preconnection lifecycle regression remains broader Chromium. Preserve actual session/link and cross-site consent handoffs. Auth, authorization, CSRF and throttle contracts remain in Go/Bruno. |
| HTTP OAuth (3), Slack (2), Proof (3) | Canonical OAuth, Slack, Proof discovery and Reconnect are critical. OAuth focus/no-JS feedback, notification evidence navigation and remaining Proof authorization navigation stay broader. `ui_utils.test.js`, notification handlers, `views/proof_receipt_test.go` and service/HTTP tests prove state and permissions. |
| Owned query (1), HTTP acknowledgement/headers and MySQL/HubSpot setup (4) | Owned SQL preview/save, acknowledgement editing and secret-header/pagination authoring are critical. MySQL password retention and HubSpot setup stay broader. UI save/reopen, invalid-value recovery and compact acknowledgement layout remain browser exceptions. `TestHTTPAcknowledgementFormValidation` and `TestHTTPAcknowledgementFormRoundTrip` prove scalar/serialization permutations. |
| Acknowledgement execution outcomes inside the authoring journey | Moved all five CLI outcomes to `cmd/braidplane/e2e/cli_e2e_test.go:SavedHTTPQueryAcknowledgementOutcomes`. It builds once, uses saved login, creates/updates through CLI and verifies accepted, rejected, recursive-error, missing and malformed responses, exit status/stdout/stderr and private-body redaction. Retire repeated browser-driven `go run` and three UI outcome permutations; keep visible success/rejection feedback in the browser. API error mapping remains in `transport/http/queries_test.go:TestExecuteQuery`. |
| Builder (3), Git/Builder integration (21) | Real persisted draft and canonical Git import/bind/protected exact-version deploy are critical. Broader cases retain contract edits, cached schema, target preflight, bootstrap/backfill, logical refs, repository setup/consent/retry, new-pipeline routes, recency and mapping interactions. `builder_canvas*.test.js`, `builder_field_mapping.test.js`, `handlers_builder*_test.go` and Git handlers prove serialization/validation. Their passes do not prove browser focus, responsive layout or multi-page handoffs. |
| First-pipeline creation permission (removed case) | Moved rendered denial/hidden proposal form to new `views/pipeline_creation_test.go:TestQueryPipelineSetupHidesProposalWithoutCreatePermission`. Existing `handlers_pipeline_creation_test.go:TestQueryPipelineSetup/restricted` proves 403 and zero proposal writes. Retire the equivalent request/render-only browser case. |
| Run inspection (21) | Successful run and pending-poll navigation race are critical. Keep active refresh, definition/contract inspection, failure/recovery, lineage, completion links, historical run navigation, S3 review, connected graph keyboard/layout/polling and admitted-version transitions broader. Relevant `views/run_*_test.go`, `views/connected_runs_test.go`, run handlers and `run_lineage.test.js` prove states/links/graph calculations. They do not prove rendered geometry or an in-flight HTMX response crossing navigation. |
| Completion state permutations (removed case) | New `views/run_completion_test.go:TestCompletionStatePermutationsDoNotInventChildren` proves waiting, closure mismatch, changed/refused decisions, restricted/empty subscriptions and absent child links. Existing exact-evidence and refresh tests remain. Retire the state/reload browser loop; retain actual completion navigation and polling in other broader cases. |
| Landing core (2), docs from `/` (2), analytics (2) | Critical across all engines and both widths. Preserve production media/range responses/captions, modal focus/keyboard, real navigation and analytics wiring. `public_analytics.test.js` proves consent/expiry/storage/cross-tab permutations; landing Go tests prove passive pages and CTA URLs. |
| Hero animation (2), solutions (2), pricing (2), docs from `/solutions` (2) | Broader. Keep reduced-motion/layout timing, responsive galleries, keyboard disclosures and secondary navigation. No exact lower replacement proves all these browser assertions, so none are retired. |
| Successful screenshots | Retired from routine execution as evidence writes, not assertions. All screenshot assertions remain; captures require `PLAYWRIGHT_E2E_EVIDENCE=1` and Chromium. Failure capture stays enabled. |

Two logical browser cases / six executions are retired only after replacement
proof. Rebase retained main's new idle-preconnection case, adding one full-profile
Chromium execution (63/173 at that point). All other original
cases remain critical or broader. Bruno remains required for API workflows, tenant isolation, authorization and destructive
actions; the built CLI suite remains required for executable/auth/output
behavior. These browser fixtures prove local UI integration, not live providers.

### Selection and remaining cost

`internal/prworkflow/ci.go` chooses critical coverage for routine CI and manual
dispatch. The exact CI plan records `browser_profile`; the toolchain forwards it
explicitly. Full coverage remains available through the explicit commands above.
Extended regressions can wait until an explicit full run. All supported engines
still exercise the five critical product flows and all critical public flows.

Bruno and CLI run before browsers for earlier API/executable failures. Keep Go
and runtime serial: disposable fixtures and project ports isolate dependencies,
but the documented host has one runner service and no qualified concurrent-job
capacity. Removing `needs: test` would only reorder queued work. Queue/admission,
the 763s baseline Go command and runtime fixture leasing remain separate costs.
No worker increase, shard or live runner change is part of this slice.

Growth requires an inventory reason and explicit cap change; the CI collection
check rejects unrecorded titles and omitted smoke executions. JSON measurements
retain setup, startup/shutdown interval unions, runner wall time, test-duration
sums and current artifact bytes. Command wall time remains in `suites.tsv`.
Playwright deadlines follow the manifest runner caps: product full 660 seconds,
product critical 240 seconds, and public full/critical 120/60 seconds. Outer task
and CI step ceilings also cover fixture setup and cleanup. CI results and queue
times must be reported separately from local runs.

### Comparable local profile measurements (2026-10-27)

Before the final rebase, sequential full/critical runs used the same worktree,
cached builds and workers;
each canonical command leased and cleaned its own connector fixtures. Product
used two workers, matching CI; public used three. All runs passed with zero
retries, flakes or skips. Three browser projects ran in each profile.

| Component | Product full | Product critical | Public full | Public critical |
| --- | ---: | ---: | ---: | ---: |
| Logical/executed | 62/172 | 15/25 | 14/42 | 6/18 |
| Command wall, including fixture setup/cleanup | 204s | 85s | 31s | 15s |
| Playwright runner wall | 170.811s | 56.164s | 23.894s | 8.991s |
| Setup wall | 6.871s | 6.784s | 6.503s | 5.536s |
| SSR fixture startup interval union | 9.671s | 12.950s | — | — |
| SSR fixture shutdown interval union | 1.860s | 2.764s | — | — |
| Test-duration sum (parallel; not wall) | 303.945s | 60.392s | 63.873s | 19.216s |
| PNG/trace/video count and bytes | 0/0 | 0/0 | 0/0 | 0/0 |

Product setup is Playwright global setup inside runner time; public setup builds
and starts the isolated site before the runner. Fixture unions overlap tests,
so these rows must not be summed. Product runner time fell 114.647s (67%);
command wall fell 119s (58%). Public command wall fell 16s (52%). These are
single paired local samples, not CI savings estimates. Connector leasing,
runtime recovery and cleanup remain outside the measured browser setup field.
The larger critical startup union shows why fixture cost must stay visible.

The earlier full reliability run used four workers and measured 178 executions,
120.636s runner / 150s command, 24 PNGs / 3,592,228 bytes. Its public run wrote
87 PNGs / 40,360,500 bytes. Those figures establish screenshot drift; different
worker counts prevent using them as the paired runtime comparison above.
New CI profile timing, queue/admission and preceding Go timing remain unverified.
Required PR CI owns final candidate acceptance; local PR receipts still cover
affected hermetic checks, not remote runtime acceptance.

## HTTP OAuth addition (historical)

HTTP OAuth adds one canonical setup/reconnect journey across the three retained
engines: draft machine test, pending creation, machine connect, app replacement
with a public PKCE configuration, consent navigation and reconnect. Together with the API-key setup journey below, the current
product suite is nine journeys / 23 executions. State permutations remain in
Go and Vitest. The SSR fixture uses synthetic API/consent replies and does not
prove encryption, OAuth protocol, provider access or persistence; PostgreSQL and
TLS-provider service tests own those assertions. Optional evidence captures a
compact state during the same journey without adding a viewport matrix.

## HTTP query drafting addition

The HTTP journey also selects a supported cached OpenAPI operation, opens its parameterized query draft, checks compact overflow and saves it through the normal query form. An unsupported multipart operation displays a diagnostic. Synthetic API metadata isolates SSR authoring; connector Go tests cover generation, unsafe shapes and credential omission, and HTTP tests cover tenant-scoped draft access. This does not establish hosted/provider acceptance.

## Notification connections addition

`notification-connections.spec.cjs` adds a bounded setup journey: explicit Slack
provider, synthetic channel consent, safe workspace/channel metadata, destination
binding and asynchronous test acceptance, then disconnect. It captures desktop
and compact states in evidence mode. The production SSR runs against an isolated
API fixture; this proves UI integration, not Slack installation, message delivery,
credential protection or persistence. Provider and PostgreSQL tests cover protocol,
tenant isolation, policy routing, retries and disconnect. Live Slack acceptance
remains separate.

## Password recovery addition

`auth.spec.cjs` adds one Chromium journey using a disposable account: request a
reset email, follow its `/auth/reset-password` link while signed out, set a new
password and sign in. Mailpit captures local delivery. Traces and screenshots
are disabled for auth tests to keep recovery links and credentials out of
artifacts. Go tests cover invalid, expired and consumed tokens.

## Run recency addition

One bounded recency journey in `pipeline-git.spec.cjs` covers create, reload,
edit, disable and historical episode inspection through the real SSR forms.
It checks current versus recorded version identities, recovery-run navigation,
capability-disabled and read-only presentation. Evidence mode retains local
synthetic desktop and compact captures. Go API/CLI tests cover exact scope,
revision conflicts and access denial; real PostgreSQL tests cover episode
persistence, tenant isolation and history after deployment changes. The browser
fixture does not establish observer evaluation, hosted behavior or delivery.

## Run inspection addition

Run inspection covers shared Overview/Steps/Attempts progress, quiet work,
stale reporting, disconnect/reconnect and terminal failure while retaining
selected step, graph view and focus. Queued intake reveals its immutable version
when admission records it, then retains the expanded identity. Navigation away
from a pending poll retains the newly selected step. Running, completed and
pending steps have distinct text/icon cues in desktop and compact views;
selection preserves running/failed styling and reduced motion stops activity
animation. The same journeys run in
Chromium, Firefox and WebKit; evidence mode captures synthetic desktop/compact
states. PostgreSQL and runtime tests own ordered writes and worker-loss fencing.
The built CLI start/watch workflow is independent. Hosted paired UI/CLI and
worker-loss acceptance remains required for run inspection and execution recovery.

## Google signup addition

Two Chromium journeys cover Google signup with consent before authorization and
Google login followed by signup consent. Each uses a disposable PostgreSQL
fixture under `transport/http/e2e/google-signup-fixture`, production auth and OIDC
verification, and a signed local issuer on a different site. They assert one
authorization/exchange, persisted presented agreement version and timestamp,
one agreement email job, and the authenticated workspace screen. Each test owns
its fixture, including when workers are limited to one. Real Google and hosted
acceptance remain separate.

A fixture lifecycle regression holds an unanswered TCP preconnection open while
stopping the server. It verifies shutdown under the existing test timeout and
does not add a product journey or change signup assertions.

## Assertion decisions

`Kept` means a successful browser integration assertion remains. `Moved` means
the named cheaper test proves the contract. `Retired` means an Alpha browser
assertion is deliberately removed; a backend test is not claimed to prove CSS,
responsive layout or every rendered error. The exact baseline title index below
expands each family, including skipped cases; skipped cases were never passing
coverage. Existing Go/Bruno gates remain required and no legacy browser matrix
is available through default task or CI selection.

| Baseline assertions | Decision and destination |
| --- | --- |
| Login heading/fields, seeded sign-in, dashboard project/environment and Pipelines navigation | Kept together in `auth.spec.cjs`. Invalid alert positioning and signup/onboarding browser flows retired. Redirect/return_to moved to `frontend/security_test.go:TestProtectedDashboardRedirectPreservesReturnTo`; CSRF and throttling remain in that file; API signup/auth/verification remain in Bruno `authentication/`. |
| Dashboard scope, quick look/full page/builder route, shell links and responsive navigation | Desktop context/navigation kept in auth, query and Git journeys. Scope field behavior remains in `ui_utils.test.js:DisablesInactiveProjectAndEnvironmentScopeFields`; partial navigation in `shell.test.js`. Compact widths, status permutations and empty aggregate presentation retired. |
| Data-source and query list selection, quick look, full detail, persisted edit/reload and successful result | Kept for SQL in `query-workspace.spec.cjs`, including a real saved record and marker output. HTTP/Git/FTP/provider and width repetitions retired. Query validation/execute/delete/restore/purge remain in `transport/http/queries_test.go`, `service/queriessvc/service_test.go` and Bruno `queries/`. |
| Connector credential/auth fields, connection feedback swaps, tabs/keyboard/unsaved definition | Moved to `ui_utils.test.js`, including new `KeepsConnectorFieldsScopedAfterConnectionFeedbackSwap` and `ReinitializesSwappedTabsWithoutLosingDirtyDefinition`. Existing Snowflake/BigQuery/HubSpot/Git form tests remain. Provider onboarding, credential rotation and safe failures remain in `transport/http/datasources_test.go`, `service/datasourcessvc/service_test.go` and Bruno `provider-connectors/`; their browser matrices retired. |
| Data-source workspace schema/settings/repository preview and healthy/untested/failed/pending/restricted states | One active SQL detail kept in query journey. Provider/state/viewport layout inventory retired. Permission, repository, deletion and schema contracts remain in HTTP datasource tests and datasource services; generic entity layout in `frontend/views/entity_layout_test.go`. |
| Google Workspace spreadsheet selection, first actual worksheet, unavailable saved worksheet, Drive create/update fields, empty versus denied discovery | Moved to new `workspace_connector.test.js` against the actual module. Pagination, compact tab layout and account-wide copy browser assertions retired. Explicit query/operation validation and unsafe writes remain in `service/queriessvc/service_test.go`, Workspace connector tests and builder serialization tests; mocked DOM coverage does not prove live Google access. |
| HTMX immediate event, unrelated response filtering, response failure, bounded missing event and cleanup | Moved to new `htmx_settle.test.js` against the shared helper. |
| Builder graph load, validated changed draft, saved status and server reload | Kept as one real mutation in `pipeline-builder.spec.cjs`; reload clears local cache and checks server draft. Example/import/export/request-only cases moved to existing builder JS and HTTP/service contracts. Binding edges, validation, serialization, dirty/save controls and conflict resolution remain in `builder_*.test.js`, `transport/http/pipelines_test.go`, `service/pipelinesvc/{validation,service_crud}_test.go`. Browser invalid/conflict modes and schema-control inventory retired. |
| Git preview/apply, compatible source binding with protected confirmation and exact-version deploy | Kept in `pipeline-git.spec.cjs` using production SSR with an isolated API fixture. Source-change conflict, missing/restricted/unavailable/failed workspace permutations retired from browsers. Reconcile/deploy authorization, confirmation, version and persistence remain in pipeline HTTP/service tests and Bruno `projects/pipeline-lifecycle/`. |
| Run quick look, full successful outcome, Outputs and return context | Kept in `run-inspection.spec.cjs` with successful timeline evidence. Queued/running/failed/missing/restricted/empty/unavailable, retry diagnostics and pagination browser permutations retired. Start/execute/state semantics remain in `service/pipelinesvc/{service,service_execute_runtime_boundary}_test.go`; authorization and cross-org access remain in HTTP/Bruno. |
| Proof discovery, receipt overview, Claims, Evidence, Technical and history return | Kept in `proof-receipts.spec.cjs` using a passing metadata-only receipt. Mismatch, partial/stale/superseded/no-evidence/verdict pagination rendering remains in `frontend/views/proof_receipt_test.go`; permission/config/evidence lifecycle remains in `transport/http/proof_{receipts,configuration}_test.go` and `service/{proofreceiptsvc,proofconfigsvc}/`. Responsive/provider combinations retired. Metadata-only evidence is not deployment authority. |
| Compact GitHub Reconnect under self-only form CSP, outbound navigation and callback cookie flags | Moved to the retained **desktop** Reconnect browser journey across all three engines; unique navigation/CSP assertion preserved before compact removal. OAuth authority/callback contracts remain in datasource service and GitHub handler tests. |
| Failed Proof save retains selected repository, target path and editable draft | Moved to `frontend/handlers_managed_git_test.go:TestFailedProofSavePreservesConfirmedRepositoryAndDraft` for 400/409 replies. Shared organization/source configuration remains in existing managed Git handlers and Proof configuration tests. Provider recovery browser states retired. |
| Sole-owner organization deletion, explicit destructive confirmation, signed-out account removal receipt | Browser flows retired; destructive/security contracts retained in Bruno `account-data-exit/`, `transport/http/organizations_test.go`, `service/accountclosuresvc/service_test.go` and JS `security.test.js`. |
| Account/PAT/password/billing/retention/API-key/invitation forms, organization membership, environment colors/variables/context, alerts/policies/notifications and audit/history navigation | Administrative browser journeys retired. HTTP/service and Bruno account, PAT/API-key, member, runtime-environment, alert/notification and multi-tenant collections retain behavior/authorization. UI control switching remains in `ui_utils.test.js`/`security.test.js`; no claim of exhaustive replacement for presentation. |
| Generic entity rows, quick-look round trips, empty/unavailable shells, builder content bounds and widths | Selected desktop quick-look transitions kept in query/run journeys. Shared row/template assertions remain in `frontend/views/entity_layout_test.go`; responsive geometry and state matrices retired. |
| Public heading, genuine demo load/play/pause, contact link and overflow | Kept in `landing.spec.cjs` at 1440/390 in all engines, isolated from the product suite. Fullscreen, large-display matrix, hover/loading geometry, FAQ variants and subpage browser permutations retired; landing rendering/links and player logic remain in Go and `landing*.test.js`. |
| Documentation configured URL and keyboard navigation | Kept public-only in `documentation-navigation.spec.cjs`, desktop/phone; intercepted destination proves navigation to configuration, while documentation build proves content. |
| Consent-gated vendor load/pageview, sanitized acquisition tags and preferences disable | Kept public-only in `public-analytics.spec.cjs`, desktop/phone with isolated vendor replies. Expiry, cross-tab synchronization, blocked storage/vendor and hidden prompt cases remain in `public_analytics.test.js`; no request goes to a live analytics property. |

The query workspace suite also covers static HTTP API-key setup through the real
form, same-origin authenticated schema import, and a complete numbered-page read
against an ephemeral synthetic provider. It checks blank rotation fields and
redacted read responses. Local provider and database tests own auth combinations,
rotation, tenant isolation, retries, cancellation, and limit permutations.

Security deletion guard: tenant isolation remains in Bruno `multi-tenant-isolation/`
and `postgres/tenantboundaries/e2e/`; datasource/query/pipeline/Proof `Can(...)`
tests remain in HTTP packages. No product/service/API behavior, credential
lifecycle or authorization check was removed by this change. Vitest remains
jsdom, with no Browser Mode matrix.

## Measured comparison

Measurements use the same macOS worktree, three installed browser engines and
canonical tasks. Baseline and final product runs used four workers. Public runs
used three workers. Final runs were sequential; runtime/toolchain caches were
warm by the final run, so setup savings are not a controlled causal benchmark.
Execution reduction is independent of that cache difference.

| Local product default | Before | After |
| --- | ---: | ---: |
| Discovered executions (including public duplication) | 660 | 17 |
| Product executions | 585 | 17 (97.1% fewer) |
| Passed / skipped / unexpected / flaky | 605 / 55 / 0 / 0 | 17 / 0 / 0 / 0 |
| Runner setup to first executed case | 31.16 s | 13.95 s |
| First test start to final test completion | 389.87 s | 13.44 s |
| Playwright runner wall | 422.27 s | 27.84 s |
| Canonical command wall, including fixtures/teardown | 454.28 s | 64.10 s |
| Successful PNGs / PNG bytes | 567 / 175,606,543 | 0 / 0 |

The final standalone public run passed all 18 executions without skips,
failures or retries: runner 11.35 s, canonical command 17.17 s, zero PNGs.
CI candidate evidence is recorded in the PR validation packet. The standalone public baseline had 69 executions (landing
and analytics), 67 passed / 2 skipped, runner 50.56 s, command 56.24 s and
6 PNGs / 3,611,902 bytes. The final public suite also includes docs navigation;
its six logical cases replace the broader public matrices rather than claim
identical assertions.

Historical CI baseline: [run 37966110363](https://github.com/Braidplane/braidplane-core/actions/runs/37966110363),
head `c3747e9`, 2026-10-09: 660 executions, 605 passed / 55 skipped,
three engines, two workers. Browser step 809 s; npm invocation to scheduling
24.94 s; scheduled execution span 753.20 s. Passing media counts/sizes were not
retained by that workflow, so those historical CI fields are unavailable rather
than inferred from local results. The changed CI runs product and public suites
separately, preserves suite command wall times, built-in JSON reports and actual
PNG/trace/video counts and bytes. Video is disabled; passing screenshots are
opt-in. Final candidate CI evidence belongs to the pull request validation packet.

## Exact baseline title index

Each title below inherits its assertion decisions above. Width/provider/state
expansions are printed exactly once per logical case, rather than once per engine.

### `account-removal.spec.cjs`

- sole member must explicitly remove the organization at 1440px
- account removal produces a signed-out receipt at 1440px
- sole member must explicitly remove the organization at 390px
- account removal produces a signed-out receipt at 390px

### `alerts-support.spec.cjs`

- alerts page manages destinations and policies
- notifications page shows alert delivery diagnostics

### `auth.spec.cjs`

- login page renders
- login errors remain viewport-level alerts
- protected dashboard redirects to login with return_to
- signup page is account-only
- email signup chooses organization after verification

### `dashboard-navigation.spec.cjs`

- dashboard scope and pipeline journey at 1280px
- dashboard scope and pipeline journey at 1440px
- dashboard scope and pipeline journey at 390px
- dashboard scope and pipeline journey at 320px
- dashboard recorded state succeeded-after-failure at 1280px
- dashboard recorded state failed at 1280px
- dashboard recorded state unavailable at 1280px
- dashboard recorded state restricted at 1280px
- dashboard recorded state succeeded-after-failure at 390px
- dashboard recorded state failed at 390px
- dashboard recorded state unavailable at 390px
- dashboard recorded state restricted at 390px
- dashboard recorded state succeeded-after-failure at 320px
- dashboard recorded state failed at 320px
- dashboard recorded state unavailable at 320px
- dashboard recorded state restricted at 320px

### `data-source-workspace.spec.cjs`

- data-source workspace postgres healthy at 1280px
- data-source workspace http untested at 1280px
- data-source workspace s3 failed at 1280px
- data-source workspace gitlab pending at 1280px
- data-source workspace gitlab repository at 1280px
- data-source workspace postgres restricted at 1280px
- data-source workspace postgres healthy at 390px
- data-source workspace http untested at 390px
- data-source workspace s3 failed at 390px
- data-source workspace gitlab pending at 390px
- data-source workspace gitlab repository at 390px
- data-source workspace postgres restricted at 390px

### `data-sources-queries.spec.cjs`

- Snowflake WIF source onboarding and disable at 1365px
- Snowflake WIF sink onboarding and disable at 1365px
- Snowflake WIF source onboarding and disable at 390px
- Snowflake WIF sink onboarding and disable at 390px
- http query detail editor renders http-specific fields
- mongodb query detail editor renders mongo-specific fields
- postgres query detail editor renders SQL fields
- mysql query detail editor renders SQL fields
- ftp query detail editor renders path field
- ftp query save and reload persists statement fields
- git query detail editor renders path field
- git query save and reload persists statement fields
- data sources page renders list heading
- data source create form keeps connector fields scoped after connection test
- Snowflake create form switches between workload identity and password at 1365px
- Snowflake create form switches between workload identity and password at 390px
- GitLab creation routes setup to the shared organization connection
- BigQuery form is OAuth only
- HubSpot OAuth onboarding shows missing name feedback
- HubSpot OAuth onboarding recovers from denial and activates the data source
- data source git-family connection test renders form feedback
- data source git-family connection test renders safe backend failure
- data source detail page renders schema browser section
- data source credential rotation is disabled until credentials change
- queries page renders data-source selector
- events page lists durable data source events with metadata expansion
- query recovery UI restores and purges a soft-deleted query

### `documentation-navigation.spec.cjs`

- documentation navigation at 1440px
- documentation navigation at 390px

### `entity-layouts.spec.cjs`

- pipeline quick-look round trip at 1280px
- data-source quick-look round trip at 1280px
- pipeline quick-look round trip at 1440px
- data-source quick-look round trip at 1440px
- pipeline quick-look round trip at 390px
- data-source quick-look round trip at 390px
- pipeline quick-look round trip at 320px
- data-source quick-look round trip at 320px
- entity empty and unavailable states at 1280px
- entity empty and unavailable states at 390px
- builder shares the product content bounds at 1440px
- builder shares the product content bounds at 390px

### `events.spec.cjs`

- switches between generic history and the security audit contract

### `google-workspace.spec.cjs`

- account-wide connection and file browsing at 1280px
- file pagination and local worksheet metadata at 1280px
- account-wide connection and file browsing at 390px
- file pagination and local worksheet metadata at 390px
- new Sheet selects its first real tab; saved missing tabs remain explicit
- Drive create and update hide irrelevant controls
- builder preserves explicit spreadsheet IDs and unsafe Drive creates
- Workspace detail tabs share identity and own their controls at 1280px
- Workspace detail tabs share identity and own their controls at 390px
- Workspace Operations distinguishes an empty library from a failed load

### `htmx-settle.spec.cjs`

- HTMX listener observes an immediate action event and ignores unrelated responses
- HTMX response failures are reported and listeners removed
- HTMX missing settle event has a bounded failure and cleanup

### `landing.spec.cjs`

- business landing page at 1440px
- landing FAQ keyboard navigation at 1440px
- landing recording playback and layout at 1440px
- landing direct section links at 1440px
- business landing page at 1280px
- landing FAQ keyboard navigation at 1280px
- landing recording playback and layout at 1280px
- landing direct section links at 1280px
- business landing page at 390px
- landing FAQ keyboard navigation at 390px
- landing recording playback and layout at 390px
- landing direct section links at 390px
- public actions keep their bounds while loading, hovering and focusing
- public subpages at 1440px
- public subpages at 1280px
- public subpages at 390px
- real recording fullscreen at 1920x1080
- real recording fullscreen at 2560x1440

### `organization-account.spec.cjs`

- account page renders profile, invitations, security, and personal token forms
- personal access token form creates disposable token metadata
- local password user can update account password and sign in again
- personal billing stays contact-led
- organizations list page renders heading and org card
- free trial organization shows plan status and limits
- organization retention form renders required controls at desktop and compact widths
- organization retention policy saves and persists all windows
- organization billing page offers a contact-led upgrade
- organization security page creates and rotates API keys without blanking
- members page sends an email invitation
- member can accept a pending invitation from account

### `pipeline-builder.spec.cjs`

- trigger quick looks open separate configuration and history views
- user can sign in and open builder page
- builder loads example graph summary
- builder visual projection exposes current schema controls
- builder validation flags missing named transform bindings
- builder shift click repairs missing transform binding
- builder port drag creates transform connections
- builder export endpoint returns yaml for canonical payload
- builder_import_accepts_transform_pipeline_spec
- builder_import_accepts_transform_csv_contract_pipeline_spec
- lifecycle path: load example, sync source yaml, and re-load
- validation summary renders for preloaded invalid canvas state
- invalid-state mode surfaces validation summary and blocked-save reason
- returns builder state JSON for the seeded Git-materialized pipeline
- returns 404 for a non-existent pipeline
- deep-linked seeded pipeline loads cleanly and source sync succeeds
- deep-linked seeded pipeline does not save unchanged state or open a conflict modal
- discarding a stale builder draft reloads the builder instead of surfacing html
- overwriting a stale draft queues the Git proposal
- manually merging a stale draft saves the resolved draft

### `pipeline-git.spec.cjs`

- imports Git definitions and deploys an exact version after fixing bindings at 1512px
- imports Git definitions and deploys an exact version after fixing bindings at 390px
- rejects stale preview and offers a fresh preview
- pipeline workspace healthy at 1280px
- pipeline workspace failed at 1280px
- pipeline workspace unconfigured at 1280px
- pipeline workspace missing-binding at 1280px
- pipeline workspace unavailable at 1280px
- pipeline workspace restricted at 1280px
- pipeline workspace healthy at 390px
- pipeline workspace failed at 390px
- pipeline workspace unconfigured at 390px
- pipeline workspace missing-binding at 390px
- pipeline workspace unavailable at 390px
- pipeline workspace restricted at 390px

### `projects-environments.spec.cjs`

- organization runtime environment color persists separately from risk copy
- environment inventories use shared rows and preserve target context at 1280px
- environment inventories use shared rows and preserve target context at 390px
- environment inventories use shared rows and preserve target context at 320px
- environment variable saves retain their tab and scoped Back link
- project quick look and full-page setup preserve context

### `proof-receipts.spec.cjs`

- Connect GitHub navigates to GitHub under same-origin form CSP
- Reconnect navigates to GitHub under same-origin form CSP
- shows shared provider access and GitLab authorization at 1512px
- manages the same github Proof configuration from organization and source at 1512px
- manages the same gitlab Proof configuration from organization and source at 1512px
- shows shared provider access and GitLab authorization at 1280px
- manages the same github Proof configuration from organization and source at 1280px
- manages the same gitlab Proof configuration from organization and source at 1280px
- shows shared provider access and GitLab authorization at 390px
- manages the same github Proof configuration from organization and source at 390px
- manages the same gitlab Proof configuration from organization and source at 390px
- failed Proof save preserves the repository identity the user confirmed
- summarizes recorded metadata evidence without deployment authority
- summarizes recorded live evidence without deployment authority
- summarizes recorded mixed evidence without deployment authority
- keeps superseded warning on Overview
- GitLab setup retry preserves failed configuration and exposes its controls
- discovers metadata-only receipt from project navigation at 1512px
- discovers metadata-only receipt from project navigation at 1280px
- discovers metadata-only receipt from project navigation at 390px
- history preserves scope and distinguishes empty, denied and unauthenticated states
- renders mismatch, partial provider evidence, and responsive lifecycle links
- keeps the overview concise and stale remediation in claims
- shows explicit no-evidence state
- redirects unauthenticated access and protects denied receipt details

### `public-analytics.spec.cjs`

- public analytics consent and navigation at 1440px
- public analytics consent and navigation at 390px
- navigation works when the analytics vendor is blocked
- consent expires, synchronizes between tabs, and works with storage blocked
- privacy controls work when the floating prompt is hidden

### `query-workspace.spec.cjs`

- query quick look and workspace for acme-postgres at 1280px
- query quick look and workspace for fake-http-20261009-221926-560900000 at 1280px
- query quick look and workspace for acme-git at 1280px
- query quick look and workspace for acme-postgres at 390px
- query quick look and workspace for fake-http-20261009-221926-560900000 at 390px
- query quick look and workspace for acme-git at 390px
- query quick look and workspace for acme-postgres at 320px
- query quick look and workspace for fake-http-20261009-221926-560900000 at 320px
- query quick look and workspace for acme-git at 320px
- execution feedback preserves unsaved definition and handles empty rows and errors
- file sink quick look warns and editing preserves its write definition

### `run-inspection.spec.cjs`

- run inspection round trip at 1440px
- run inspection round trip at 1280px
- run inspection round trip at 390px
- run inspection round trip at 320px
- run diagnostics queued
- run diagnostics running
- run diagnostics succeeded
- run diagnostics missing
- run diagnostics restricted
- run diagnostics empty
- run diagnostics unavailable

### `runs-triage.spec.cjs`

- sign in with seeded user and dashboard loads
- shell navigation links are visible after sign-in
- dashboard aggregate renders explicit empty organization state

## Complete CI timing calibration — 28 October 2026

[Run 40739003437, runtime job 123038352642](https://github.com/Braidplane/braidplane-core/actions/runs/40739003437/job/123038352642)
at `55ae0093468e996573e6870bbec6b41004ff9ed7` completed all 64 logical cases /
174 executions on two workers, with zero skips, flakes or retries. Its archived
`browser-measurements` artifact (12845511359) contains the complete product report.
The job failed afterward because the measured runner duration exceeded the old
480-second cap; the browser assertions passed.

| Observed component | Seconds |
| --- | ---: |
| Playwright runner, including global setup | 580.195 |
| Global setup, included in runner time | 25.774 |
| Summed test durations, overlapping across two workers | 1077.063 |
| Playwright command, including outer fixture work and cleanup | 610 |

At the recorded test durations, even perfect two-worker scheduling needs at least
538.532 seconds. WebKit contributes 591.692 seconds of summed test duration;
there is no single failed or hanging test to discard. These overlapping rows must
not be added together, and the local 2026-10-27 measurements above are not CI timings.

Calibrate the full product runner cap to 660 seconds, about 14 percent above this
complete CI observation. After integrating the browser reliability changes, Playwright deadlines
follow these manifest caps directly; critical retains main's 240-second cap and
critical-only routine CI, and public retains its 60/120-second caps. The earlier
720-second full / 600-second critical containment proposal is superseded. Keep
two CI workers, all cases,
zero retries, and the completeness/over-budget rejection checks. This is a
measured CI budget correction, not an optimization or hosted-capacity claim.
Further growth needs new representative evidence rather than an automatic bump.
