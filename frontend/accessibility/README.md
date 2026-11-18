# Core WCAG 2.2 AA assessment

Work for [Core #160](https://github.com/Braidplane/braidplane-core/issues/160).
This assessment records local evidence for the selected Core workflows. A
full WCAG 2.2 AA conformance claim would require additional evidence. The reference is [WCAG 2.2](https://www.w3.org/TR/WCAG22/), including full
pages, complete processes and accessibility-supported use of technology.

Screen-reader testing is deferred beyond the current acceptance of #160 at the
maintainer's request because no screen reader is available. Automated semantics
and keyboard testing remain required. Unverified spoken announcements and
assistive-technology behavior are recorded as limitations, not passing evidence.

## Run the automated assessment

Start `task dev` in a dedicated worktree with distinct Compose names and ports,
following [local setup](../../COMMUNITY_BUILD.md#run-locally). Use only its
synthetic database; the assessment seeds users, organizations and a project.
The #160 runtime uses:

```sh
COMPOSE_PROJECT_NAME=core-160 DB_PORT=36432 ALLOW_PLAINTEXT_CONNECTORS=true \
MAILPIT_SMTP_PORT=32025 MAILPIT_WEB_PORT=39026 \
SERVER_PORT=3101 FRONTEND_SERVER_PORT=3102 COMMUNITY_WORKER_PORT=3103 task dev
```

The PostgreSQL connector check uses the owned loopback database and needs the
local development transport exception shown above. The source also opts
into private hosts using the development runtime's private-host policy.

In another shell at the repository root, load that runtime's keys and supply
its database and frontend addresses:

```sh
source .worktree/community/keys.env
export ENCRYPTION_KEY_HEX EXECUTION_REQUEST_KEY_HEX
export DB_HOST=127.0.0.1 DB_PORT=36432 DB_USER=postgres
export DB_PASSWORD=postgres DB_DATABASE=braidplane DB_SSL_MODE=disable
export E2E_BASE_URL=http://127.0.0.1:3102
export MAILPIT_URL=http://127.0.0.1:39026
task test-accessibility
```

The command uses the pinned Node/Playwright tools and locked axe dependencies.
It reuses browser authentication and SSR fixture provisioning from the existing
E2E setup, with one sign-in per configured session file. The real database
supplies setup and empty states. Isolated API fixtures supply populated source,
Builder, workflow, deployment, run and Proof states through the production
frontend. Those replies do not prove persistence,
execution or provider behavior. The suite is separate from the critical browser
inventory and is not currently a required CI gate.

The JSON report is `.worktree/accessibility/results.json`. Each scan attaches
browser/axe versions, viewport, state, rule results, violation targets and
incomplete checks requiring human judgment. No rules or elements are excluded.
Confirmed rule violations fail the suite; soft assertions allow later states
to be scanned. Navigation, missing fixtures and keyboard assertions fail hard.
Screenshots, traces and input HTML are not retained by this assessment.
Reports contain synthetic identifiers and must stay uncommitted.

Each workflow state is scanned with normal styles and with WCAG text-spacing
overrides: line height 1.5, paragraph spacing 2em, letter spacing .12em and word
spacing .16em. These scans check rules and document overflow; they do not prove
every control remains usable or replace actual browser zoom testing.

### Supplemental rendered text contrast

For axe's unresolved contrast targets, `contrast.cjs` samples rendered backgrounds
with text glyphs temporarily transparent. The screenshots remain in memory;
only selectors, foreground colors, required ratios and measured bounds enter
the report. Text geometry and `currentColor` backgrounds are preserved.

The check composites translucent text against sampled sRGB backgrounds and
calculates relative luminance. Large-text thresholds account for authored scale.
A sample passes only when its lowest contrast meets the threshold; it fails
only when even its highest contrast falls short. Bounds crossing the threshold
require visual review of actual glyph backgrounds. Fully transparent computed
text shadows contribute no visible effect and permit normal sampling. Visible
shadows and unrecognized shadow formats remain review items. Native controls,
unsampled or clipped text, SVG, text strokes, gradient text, rotation, opacity,
filters and blending remain review items. This supplements axe and does not
turn its incomplete results into criterion-level passes.

Synthetic browser regressions cover passing, failing and mixed backgrounds,
alpha compositing, nested targets, transformed text, large bold text and explicit
review outcomes for unsupported rendering. Shadow regressions cover transparent
Color 4 values, multiple transparent shadows, mixed visible/transparent shadows,
faint visible shadows and low contrast beneath transparent shadows.
Top-layer dialog and popover samples ignore clipping and effects from outside
the top layer, while retaining their own and inner clipping/effects. A synthetic
regression covers both layers, scrolling and a nonmodal dialog.

## Intended process scope

All steps below require assessment, including shared navigation and error states.
The coverage table identifies the paths exercised within each process.
Managed-only screens and external provider pages are outside the Core scope.

| Process | Pages and states required before a claim |
| --- | --- |
| Authentication | Sign in, email-code MFA, signup, verification, forgotten/reset password, expired/invalid links, sign out, validation and retry, password manager/paste support, and Core's Google sign-in callback and signup confirmation when Google is enabled |
| Project/source setup | Organization/project selection and creation, source creation, connector-specific fields, connection testing, save, update, failure and recovery, query preview |
| Pipeline authoring/validation | Empty/populated Builder, add/select/edit/remove/connect steps, contracts, properties, import, validation success/failure, navigate error to step/edge, save and reload |
| Deployment | Repository import, runtime/source bindings, validation, confirmation, deployment success/failure, permission/protected-environment feedback |
| Runs/recovery | Filter/list/preview, live and terminal details, recorded timeline and technical diagnostics, contracts/delivery, pagination, retry and recovery confirmations, and retained/pruned evidence |
| Proof | History/filter/empty state, receipt summary, claims, evidence disclosures, technical details, retained/pruned evidence and configuration feedback |

Shared components: authenticated and auth shells; desktop/compact navigation;
organization/runtime switchers; headings, breadcrumbs and entity lists;
forms, field errors and request summaries; popups/status regions; disclosures,
tabs and dialogs; tables, pagination, Builder graph and run timeline.

The following coverage refers to the recorded Core runs. The remaining cases
are examples of gaps within the process scope above.

| Process | Recorded keyboard paths | Remaining checks |
| --- | --- | --- |
| Authentication | Email sign-in and sign-out, native password-manager autofill, password paste on signup and reset, email verification, password reset and retry, MFA invalid code, paste and expiry recovery, Google signup, adjustable callback and pending-confirmation timing, and expiry recovery | Native password generation remains a browser coverage limitation. |
| Project/source setup | Organization and project creation, repository source and branch selection, branch load failure and retry, default create forms for 12 connector types, HTTP, PostgreSQL, MySQL, MongoDB, S3 and HubSpot private-app connection recovery and settings updates, Git SSH setup and credential rotation, S3 and HubSpot private-app credential rotation and retry, Snowflake password creation with connection-test recovery, workload identity status, script inspection, verification retry and identity controls, HubSpot OAuth correction, expiry, denial, retry and reconnection, BigQuery dataset correction and consent retry, Workspace authorized-file discovery, worksheet query setup and worksheet-list retry, HTTP and MongoDB query draft recovery, HTTP, SQL, MongoDB, S3 and FTP query preview, HTTP, SQL, S3 and FTP execution retry, query source filtering | Other connector connection, credential, settings and query variants. |
| Pipeline authoring/validation | Graph edits and dependency inspection, canvas and source views, empty and invalid source recovery, draft and server step/edge validation navigation, contract correction, draft save and reload, proposal and conflict dialogs | Additional diagnostic types and graph states |
| Deployment | Stale preview and partial Git sync recovery, protected binding setup and removal, compile errors and retry, exact-version deployment and rollback, Development-to-Staging target review, recovery when a target is removed, read-only plan and binding inspection, permissions changing during binding and deployment review, access denials and missing-version recovery | Other environment and permission combinations |
| Runs/recovery | List filter and pagination, step list and named dependencies on a branched run, chronological timeline and failure diagnostics, sink outcomes and missing evidence, retained timeline and snapshots when the definition is pruned, incremental source checkpoints, live update pause, recovery permission changes, refusal and retry, retained and pruned evidence | Other lineage shapes and connector-specific evidence |
| Proof | Runtime selection, history pagination and cursor recovery, mixed claims and evidence disclosures, live read-only evidence, available, expired, changed and pruned operational evidence, version mismatch, empty and superseded receipts, denied history and receipt recovery, missing and denied configuration, repository replacement and configuration retry | Other evidence and configuration states beyond the fixture cases |

## Regression matrix

Use the [WCAG 2.2 AA release matrix](../../doc/WCAG_22_AA.md) to track
criterion-level decisions for a named release.

[Download the CSV matrix](matrix.csv) for the selected workflow checks. Each
row records a state, a WCAG 2.2 criterion, a result and its evidence. Evidence
names refer to specs in this directory or local reports under
`.worktree/accessibility/`.

Keep the row set fixed when comparing runs. Report Pass, Fail, Untested and
Not applicable counts separately. Calculate coverage as (Pass + Fail) /
(Pass + Fail + Untested). A pass applies to the listed check and state.
The matrix samples the assessment below and carries no conformance claim.

## Criteria and remaining assessment

The table maps the recorded evidence to criteria and keeps its limits explicit.
Axe rule results and individual workflow checks support only the states tested.
The groups below have partial coverage and carry no criterion-wide pass.
Mark a criterion inapplicable only after inspecting the entire selected scope.

| WCAG 2.2 A/AA criteria | Recorded evidence | Remaining assessment |
| --- | --- | --- |
| 1.1.1 | Builder nodes/connections have names; keyboard inspection exposes steps, dependencies and contracts in properties. Run and Proof fixtures expose text evidence. | Review alternatives for every graph/timeline state and meaningful icon. |
| 1.2.1–1.2.5 | The authored Core source review identified no audio or video elements, embedded media or browser media APIs. Axe's video-caption and autoplay-audio rules were inapplicable in all 858 scans in the latest combined run. | Reassess if an assessed page adds time-based media. |
| 1.3.1–1.3.5 | Axe scans cover semantics in the listed states. Sign-in, recovery and organization tests check autocomplete. Graph inputs and outputs expose named dependencies. The source review found no orientation restriction and the browser runs sample portrait and landscape viewports. Seven page samples verify DOM and bidirectional keyboard order. | Review reading order and instructions in untested states and remaining input-purpose fields. |
| 1.4.1–1.4.5 | Run and Proof verdicts have text. Supplemental text contrast and targeted reviews cover sampled rendering. Actual 200% zoom checks cover selected states in all six workflow areas. No autoplay audio was identified in the sampled Core pages. | Check other color-only indicators, text contrast and 200% text in untested states. |
| 1.4.10–1.4.13 | Every scan checks document overflow at normal/increased spacing. Actual 200% and 400% zoom probes cover selected states in all six workflow areas. Node-summary regression covers hover, focus persistence and Escape. Sampled keyboard focus outlines measured at least 4.71:1. | Review other non-text contrast, further hover content, actual zoom across remaining states and clipped content inside scroll regions. |
| 2.1.1, 2.1.2, 2.1.4 | Keyboard journeys cover authentication/recovery, workspace/project creation, source/query setup, graph edits, deployment, recovery and Proof. Dialog tests verify Escape and return focus. Builder's printable editing shortcuts require a modifier. Run-lineage pan, zoom and fit shortcuts are active on the focused viewport. Browser checks moved from the viewport to a dependency link and then outside the graph to verify that focus scope. | Complete the remaining process steps. |
| 2.2.1, 2.2.2, 2.3.1 | Run progress, completion and connected-run updates can be paused and resumed. Source and Builder schema status checks are user initiated. Shared success feedback and Builder run results stay inline. The running-step icon is static. Authentication CSS has a reduced-motion override. Browser journeys cover MFA and Google OAuth expiry recovery. Users can choose a 100-minute Core callback window for Google sign-in, browser-based data-source authorization, GitHub or GitLab organization connections, and project repository setup. Pending Google signup confirmation honors the same choice. The initial-load motion probe observed only one-time Builder transitions. | Review motion and flashing in untested interactive states. |
| 2.4.1–2.4.7, 2.4.11 | Skip-link tests verify focus transfer. Journeys check error/dialog focus. Brave checks record visible, unobscured outlines on selected controls. Chromium measurements cover 60 keyboard stops in seven states across all six workflow areas. Git sync, deployment and binding setup use distinct browser titles. Dashboard shortcuts and primary navigation reach Data Sources, Queries, Pipelines and Runs by keyboard at both widths. | Review link purpose, navigation to other independent pages and focus across additional states. |
| 2.5.1–2.5.4, 2.5.7, 2.5.8 | Builder add/connect/remove/undo works through keyboard controls. Pointer connections commit on release and can be canceled. Lineage pan preserves Fit on a click and can be canceled before release. Compact mapping controls and query links retain 24px targets. The axe target-size rule passed in all 858 latest combined scans, and measured spacing or label size covers the three small controls found in the focus sample. The source review found no authored device-motion input. Zoom symbol name review is recorded below. | Assess cancellation for other gestures and review remaining targets. |
| 3.1.1, 3.1.2 | Both shells declare English. Document-language checks passed in 834 scans, with 24 open-dialog scans where the rules were inapplicable. A separate probe verified the rendered declaration across all six workflow areas. | Language of arbitrary provider and user content remains unverified. |
| 3.2.1–3.2.4, 3.2.6 | Keyboard journeys use explicit activation for runtime switching, query filtering, creation and execution. The repository picker describes its branch refresh and preserves focus. Documentation appears last in the primary menu on the sampled authenticated pages that expose it. A separate probe checked its position relative to Account and the page heading. | Review other input-triggered updates and any newly introduced shared help mechanisms. |
| 3.3.1–3.3.4, 3.3.7, 3.3.8 | Error/retry tests verify preserved inputs, cleared passwords, focused feedback and protected confirmations. The protected binding removal journey verifies cancellation and permission refusal before a successful retry. Recovery checks autocomplete, keyboard paste in both new-password fields and replacement-password sign-in. Chromium's password manager fills both sign-in fields, and keyboard submission continues through MFA. | Review other validation paths, destructive confirmations, redundant entry and remaining authentication states. |
| 4.1.2, 4.1.3 | Axe/role assertions cover the scanned states. Tests verify status semantics and feedback focus. | Review remaining dynamic updates. Actual assistive-technology announcements are deferred. |

The configured frontend idle timeout and the email verification and password
reset tokens each last 24 hours. This exceeds the 20-hour exception threshold
for WCAG success criterion 2.2.1. The email MFA code expires after 10 minutes.
W3C identifies time-limited second factors as potentially essential. The
Google sign-in and signup pages start with a 10-minute Core authorization
window and offer a 100-minute choice before redirecting. This gives users
ten times the default time to return to Core. Google controls its own
authorization deadlines. Keyboard retry after expiry preserves the
original destination and starts a new OAuth request.
In the standard Core flow, the verified Google callback creates the account
and opens workspace setup. When a callback reaches the separate pending-signup
confirmation, the selected 10- or 100-minute window starts again on arrival.
The HTTP regression exercises a synthetic account service response that enters
this fallback, checks both deadlines and verifies that Core clears the OAuth
state and timing choice. The signed-issuer Core browser flow creates the account
directly from both signup and login.
Core offers a 100-minute callback window alongside the 10-minute default
for HubSpot, BigQuery and Workspace source authorization, HTTP browser OAuth,
and GitHub App and GitLab organization connections. The option is available
before the redirect. The HubSpot and HTTP retries preserve the choice after a
refused start. Core still checks the stored deadline and keeps the provider
state single-use. The provider can impose its own deadline. GitHub and GitLab
project repository setup offer the same choice before authorization. Choosing
a different duration when resuming creates a new authorization for the same
saved target. If GitHub returns after the authorization and return cookies
expire, Core can route the initiating user to the same active project setup
after checking current permissions. The expired intent supplies recovery
context only. A new authorization is required to continue.
[Understanding Timing Adjustable](https://www.w3.org/WAI/WCAG22/Understanding/timing-adjustable.html)

For 2.2.2 and 2.3.1, the authored recurring requests refresh run progress
every two seconds, logical completion every three seconds and connected runs
every five seconds. The run-page control pauses all three. The initial
Snowflake onboarding and repository branch requests use plain-text loading
messages. The list and account loading spinners appear after an explicit
interaction. Chromium recorded CSS animation and transition activity and
checked for visible motion media for at least 5.5 seconds on 28 initial
loads across the six workflow areas at 1280 and 320 CSS pixels. Authentication
was also sampled with reduced motion enabled. Only Builder's one-time 200 ms
palette and properties transitions were observed. The sample covers initial
load and the source review covers authored recurring updates.
`scope-motion-audit.jsonl` and `scope-motion-audit-summary.json` retain the
observations, probe and runtime fingerprints.
[Understanding Pause, Stop, Hide](https://www.w3.org/WAI/WCAG22/Understanding/pause-stop-hide.html)
and [Understanding Three Flashes or Below Threshold](https://www.w3.org/WAI/WCAG22/Understanding/three-flashes-or-below-threshold.html).

For 1.3.2 and 2.4.3, a Chromium probe checked DOM order, Tab and Shift+Tab
across seven representative pages at 1280 and 320 CSS pixels. It verified
60 forward control stops across sign-in, signup, repository setup, Builder
views, deployment review, run recovery and Proof evidence. These are
selected sequences within the workflows. See `meaningful-sequence-audit.jsonl`
and `meaningful-sequence-audit-summary.json`.
[Understanding Meaningful Sequence](https://www.w3.org/WAI/WCAG22/Understanding/meaningful-sequence.html)
and [Understanding Focus Order](https://www.w3.org/WAI/WCAG22/Understanding/focus-order.html).

For 1.3.3, the source search covered 93 authored files and identified six
gesture instructions. The three connection and run graph pan instructions
name keyboard controls. The Builder connection copy now names the Outputs
controls and associates the instructions with the destination select.
The new copy passed focused browser checks after the combined run. See
`sensory-instructions-audit-summary.json` for the search before the wording
change and `builder-connection-instructions/candidate-status.json` and
`builder-connection-instructions/tracked-results.json` for the checks.
[Understanding Sensory Characteristics](https://www.w3.org/WAI/WCAG22/Understanding/sensory-characteristics.html)

For 2.4.5, the dashboard keyboard probe activated the primary menu links and
the dashboard content links with Tab and Enter at 1280 and 320 CSS pixels.
Each pair reached the same inventory path and expected page title.
`dashboard-navigation-paths-evidence.json` fingerprints the runtime and probe.
Form steps and results are evaluated in their containing process under W3C's
exception for these pages. [Understanding Multiple Ways](https://www.w3.org/WAI/WCAG22/Understanding/multiple-ways.html)

For 3.3.8, the tested email sign-in fills both credential fields with Chromium's
password manager. The email-code challenge accepts the complete code from the
clipboard. These are mechanisms W3C describes for avoiding memorization and
transcription. W3C focuses this criterion on authenticating existing accounts.
The test evidence covers autofill and paste during sign-in and recovery.
Browser-native password generation remains unverified.
[Understanding Accessible Authentication (Minimum)](https://www.w3.org/WAI/WCAG22/Understanding/accessible-authentication-minimum.html)

`core-source-applicability-current.json` records the source hashes and
searches for media, device orientation and motion, and key handlers across
93 authored Core templates, JavaScript files and styles. No authored
orientation lock or device-motion control was identified. The source review
covers Core code and the browser runs exercise portrait and landscape widths.

## Manual protocol and evidence

Record OS and browser versions, date, exact revision plus dirty
diff, viewport/zoom, process/state, steps, expected/observed behavior and result.
Screen-reader testing is deferred and is not an acceptance gate for this issue.
An ARIA snapshot or DOM assertion is not a listening test.

1. With keyboard alone, enter each process from its normal navigation. Check
   Tab/Shift+Tab order, skip link, visible/unobscured focus, Enter/Space, Escape,
   arrow navigation where expected, dialogs and restoration to the opener.
2. Add/edit/connect/delete graph nodes and inspect dependencies and validation
   without pointer, position or color. Compare the information and actions in
   any alternative list/table with the graph. Inspect run steps/timing/outcomes
   and recovery in the same way; existence of a table alone is insufficient.
3. Repeat at 200% text and 400% browser zoom (1280px viewport), 320 CSS px,
   increased text spacing and both orientations. Verify content and controls
   remain usable; distinguish permitted two-dimensional diagrams/tables from
   surrounding content that must reflow.
4. Inspect contrast of text, disabled/hover/focus/selected states, graph edges
   and status indicators. Resolve axe `incomplete` items individually. Exercise
   validation, transport failures, dynamic updates and destructive confirmations.

For a future screen-reader pass, record exact tool versions and listen for names,
roles, states, field help/errors, loading, validation, save/deploy outcomes and
polling updates. Inspect reading order and heading/landmark navigation. This
deferred pass is needed before a full conformance claim, not for current issue
acceptance.

## Current local assessment

The rebuilt local suite on 2026-11-18 passed 163 Chromium 153.0.8010.12
executions and 858 axe-core 4.14.1 scans at desktop and 320 CSS px widths.
It used macOS 27.0.1, Node 24.19.0 and Playwright 1.63.0. There were zero
skips, axe violations, page overflow or definite text contrast failures.
All 2,043 flagged text samples have dispositions. The symbolic zoom label
review below covers the other axe review items.

`combined-160-acceptance-input-manifest.json` fingerprints the rebuilt
runtime, fixtures, test inputs and `combined-160-acceptance-results.json`.
`combined-160-acceptance-review.json` records dispositions, and
`combined-160-acceptance-evidence-index.json` maps 72 named journeys
and sampled actual 200% and 400% zoom evidence across all six workflow areas.
`combined-160-acceptance-audit.json` verifies the report, rule outcomes and
supporting fixture evidence. The hermetic Core and CLI tests passed with
local test listener access. All 435 JavaScript tests passed. Go lint,
JavaScript lint, package boundaries and documentation checks passed.

A separate language probe verified the English document declaration in 16
page and viewport combinations. A source check found the declaration in
both shared shells and no authored language overrides in the template and
script sample. Product prose uses English in the tested states. Proper
names and technical terms may inherit the page declaration under the
criterion. Provider and user text can vary and was not assessed for
language changes. See `language-sample-summary.json`.
[Language of Parts](https://www.w3.org/WAI/WCAG22/Understanding/language-of-parts.html)

### Evidence against issue acceptance

| Requirement | Verified local evidence |
| --- | --- |
| Process scope and shared components | The [process inventory](#intended-process-scope) lists the six workflow areas, their steps and the shared shells, forms, feedback, controls and inspection surfaces. |
| Automated and keyboard WCAG assessment | The [criteria table](#criteria-and-remaining-assessment) maps A/AA criteria to the scans, keyboard journeys, source review and sampled manual evidence. The current report records actual versions and results. |
| Focus, labels, forms, reflow, contrast, announcements and dialogs | The evidence index maps automated rule results to forms and retry tests, named modal controls, status regions, text spacing and reflow checks. The supplemental probes verify actual 200% and 400% zoom and rendered focus contrast. |
| Graph and timeline access | [Builder keyboard authoring](workflows.spec.cjs) exercises editing, connections, validation and save. [Run lineage parity](lineage.spec.cjs) compares graph and list destinations, dependencies, outcomes and timing. [Connected outcomes](connected-graphs.spec.cjs) and the [recorded timeline](run-delivery.spec.cjs) have tested routes without traversing the graph. |
| Fixes and verification | The local regression suite and focused checks cover the confirmed gaps described below, including the Workspace worksheet recovery, protected binding removal and pruned-definition evidence. The [manual protocol](#manual-protocol-and-evidence) records how to reproduce the browser observations. |
| Reported limitations | The process and criterion tables retain the untested variants. Status checks cover browser semantics and focus. Screen-reader behavior remains deferred. Live provider and hosted CI behavior were outside the local evidence. |

A focused Workspace probe reproduced and verified a
fix for worksheet-list refusal. The Worksheet selector previously kept its
loading text after the request failed. It now shows the failure and offers
a keyboard-accessible Retry loading worksheets button. Success updates the
status and moves focus to Worksheet when the retry button still has focus.
A declined load preserves the selected spreadsheet and any previously
selected worksheet. A late response from a previous spreadsheet cannot
replace the current state. The browser test retries a refused request,
saves the query, and recovers the same worksheet after another refusal on
reopen. Four Chromium executions at desktop and compact widths passed 32
axe scans, with zero violations, overflow or definite text contrast
failures. All 140 flagged text samples have dispositions. The retry button
and recovered worksheet had visible focus at actual 200% and 400% zoom.
The focused fixture uses synthetic Workspace responses.

See `workspace-worksheet-refusal-baseline-summary.json`,
`workspace-worksheet-retry-summary.json`, `workspace-worksheet-retry-review.json`
and `workspace-worksheet-retry-zoom-results.json`.

The protected binding removal check verified that Escape returns focus to
Remove and keeps the bound source. The button's accessible name now
identifies the pipeline, reference and runtime target. The confirmation
describes the binding that will be removed and the consequence for the
pipeline. Typing a different target leaves Remove binding disabled. A
refusal after permission changes focuses the error and retains the source.
After the permission is restored, successful removal focuses the unbound
cell and keeps the immutable deployment version in view.

The focused deployment and rollback suite passed 26 Chromium executions
and 140 axe scans with no violations, page overflow or definite text
contrast failures. All 224 flagged samples have dispositions. The
confirmation input and refusal message kept visible focus at actual 200%
and 400% zoom. See `binding-removal-summary.json`,
`binding-removal-review.json` and `binding-removal-zoom-results.json`.

A Chromium focus probe checked 14 forward and reverse keyboard sequences
across sign-in, sign-up, repository setup, Builder view selection, deployment
review, run recovery and Proof evidence review at 1280 and 320 CSS px. Each of
the 60 selected Tab stops matched `:focus-visible`, showed a rendered change
around the control when focused, and received all 25 hit-test points inside its
bounds. [Focus Visible](https://www.w3.org/WAI/WCAG22/Understanding/focus-visible.html)
and [Focus Not Obscured (Minimum)](https://www.w3.org/WAI/WCAG22/Understanding/focus-not-obscured-minimum.html)
describe the criteria.

A separate contrast probe measured the rendered outline and adjacent
background in the focused state at 1,120 points across the same 60 stops.
Each outline had samples on all four edges. The lowest applicable
contrast was 4.71:1. These are straight-edge samples at the tested widths.
See `acceptance-focus-outline-contrast-summary.json`.
[Non-text Contrast](https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html)

The axe target-size rule passed in all 858 current combined scans. Three controls in the focus sample had an element
dimension below 24 CSS px. The password-reset link on sign-in was 19 CSS px
high at both widths. Its center was 21.5 CSS px from the nearest other target
and at least 92 CSS px from the center of another undersized target. Clicking
the text of each repository consent and callback timing label toggled its
checkbox. At 320 CSS px the clickable labels were 60 and 40 CSS px high. At
1280 CSS px their centers cleared the nearest target by at least 30 CSS px
and the nearest undersized target center by 41 CSS px.
[Target Size (Minimum)](https://www.w3.org/WAI/WCAG22/Understanding/target-size-minimum.html)

See `focus-visibility-audit.jsonl`, `target-size-sample.jsonl` and
`focus-visibility-audit-summary.json` for the measurements and fingerprints.

The consistent-help probe checked 16 page and viewport combinations in
Chromium at 1280 and 320 CSS px. It used the local sign-in, sign-up and
password recovery pages, plus synthetic repository setup, Builder, deployment,
run recovery and Proof pages with a configured Documentation URL. In the nine
combinations that expose Documentation, it was the final primary navigation
link, before Account in Tab order and before the page heading in document
order. The compact menu opened with Enter and returned focus to its trigger
on Escape. The sampled signed-out pages have no shared Documentation link.
Builder exposes it through the compact menu and hides the sidebar on desktop.
[Consistent Help](https://www.w3.org/WAI/WCAG22/Understanding/consistent-help.html)

See `consistent-help/results.jsonl` and `consistent-help/summary.json`.

In the desktop run lineage graph, pressing the background no longer turns off
Fit before the user pans. Escape, pointer cancellation and unexpected capture
loss restore the starting position. A live content swap keeps the last
completed position. The 22-step Chromium test checks that Escape restores the
view, releasing outside the graph completes an intentional pan, and the
compact step list still exposes all 22 steps. Four focused browser tests
passed. The same 22-step browser test passed in Firefox 155.0 and WebKit
26.6. Vitest also exercises pointer cancellation, capture loss and a swap
during a pan. See `run-lineage-pointer-results.json` and
`run-lineage-pointer-cross-engine-results.json`.
[Pointer Cancellation](https://www.w3.org/WAI/WCAG22/Understanding/pointer-cancellation.html)

The pending Google signup timing fix also has a focused HTTP test. It
failed with the previous fixed deadline and passed with the selected duration. A separate rebuilt signed-issuer fixture passed 12 browser
executions and 24 scans of the standard Google account and expiry journeys.
All 24 flagged text samples have dispositions. See
`google-pending-timing/baseline.log`, `google-pending-timing/final-test.log`,
`google-pending-timing/browser-results.json` and
`google-pending-timing/browser-results-review.json`.

The protected run recovery form now shows the environment slug as persistent
text and gives the confirmation field an accessible description. The keyboard
test checks the text after typing an invalid entry, then retries with the
correct slug. Four executions passed with 16 axe scans, no violations or
document overflow, and dispositions for all 16 flagged text samples. The
instruction and the visible input focus fit together at actual 200% and
400% browser zoom. See `run-recovery-confirmation-baseline.json`,
`run-recovery-confirmation-updated.json`,
`run-recovery-confirmation-updated-review.json` and
`run-recovery-confirmation-zoom-results.json`.

When the exact executed definition is unavailable, Overview and the lineage
notice now link to the recorded timeline. Retained events keep their step
names, and snapshots keep their operation and output evidence. Links to
individual steps are shown when the exact definition was loaded.
Six focused executions after the combined run passed 48 axe scans with no
violations, document overflow or definite text contrast failures. All 69
flagged text samples have dispositions. Timeline and snapshot focus stayed
visible at actual 200% and 400% zoom. See
`run-pruned-definition-baseline-confirmed.json`, `run-pruned-definition-final.json`,
`run-pruned-definition-review.json` and `run-pruned-definition-zoom-results.json`.

Proof receipts name the exact recorded run or the current deployment link.
The recorded deployment identifier remains visible next to the current
deployment link. When the destination becomes unavailable, the page focuses
one inline error and provides a return link. Keyboard tests follow both
destinations and reach the original receipt again after an error. The focused
run passed 18 Chromium executions and 68 scans, with zero axe violations,
document overflow or definite text contrast failures. All 68 flagged samples
have dispositions. Four additional probes verified visible, unobscured focus
at actual 200% and 400% zoom. See `proof-handoff-final-results.json`,
`proof-handoff-final-review.json` and `proof-handoff-zoom-final-results.json`.

Proof history now focuses the scoped receipt summary after pagination. When
an older page is empty or the cursor is invalid, the focused message leads by
Tab to Back to newest receipts for the same project and runtime. Following the
link focuses the history summary and the next Tab reaches the first receipt.
The six synthetic browser executions passed 48 axe scans with no violations,
document overflow or definite text contrast failures. All 48 flagged text
samples have dispositions. Focus stayed visible through recovery at actual
200% and 400% zoom. See `proof-history-recovery-baseline.json`,
`proof-history-recovery-final.json`, `proof-history-recovery-review.json`
and `proof-history-recovery-zoom-results.json`.

The earlier GitLab combined run passed 119 of 123 executions. Its four PostgreSQL
and S3 failures occurred before the accessibility checks because the
loopback runtime was restarted without the documented plaintext connector
setting. After restoring it, the PostgreSQL and S3 journeys passed at both
widths. See `connector-transport-rerun-results.json`. The fingerprinted report
and contrast review from the interrupted assessment are retained as
`combined-160-gitlab-results.json`, `combined-160-gitlab-input-manifest.json`
and `combined-160-gitlab-review.json`.
The four compact-menu contrast review items in that report were reproduced
with the menu settled. Tab brought Organization settings, Documentation,
Account and Logout fully into view. The same text met contrast at 8.33:1
or higher when visible. See `compact-menu-tail-contrast-evidence.json`.

The Builder regression completes graph authoring with the keyboard, and
the pointer regression uses separate clicks to select the source and
connect the destination. Run lineage compares graph and list dependencies
and checks that the chronological timeline exposes the merge outcome and
timing. See the process and criterion tables for the remaining coverage.

Connected runs and current completion subscriptions each have a list at
desktop and compact widths. The desktop skip links take focus directly to
those lists. The connected-run list shows the same outcomes, attempt counts
and evidence links as the graph. Selecting a run focuses its evidence, and
automatic refresh keeps focus on a surviving list link. Subscription and
dispatch details name the upstream and downstream pipelines in words. The
recorded-attempt table keeps its heading visible during keyboard scrolling.
Eight Chromium executions passed across both widths with 12 scans and zero
axe violations, document overflow or definite text contrast failures.
All 22 flagged samples have dispositions, including six offscreen table
samples measured after scrolling and four screen-reader-only badge labels.
Six existing run-completion and connected-graph tests also passed. Focus on
the selected evidence, trigger link and table remained visible at actual
200% and 400% zoom. See `connected-graphs-contrast-complete-results.json`,
`connected-graphs-contrast-review.json`, `connected-graphs-existing-e2e-results.json`
and `connected-graphs-zoom-final-results.json`.

Escape now dismisses Builder's organization menu or a step summary without
clearing the selected step. Run lineage leaves character and directional keys
to the focused link once focus moves off the viewport. The three graph views
describe how to focus the viewport and use its controls. Two focused keyboard
journeys passed in Firefox 155.0 and WebKit 26.6, with Option-Tab for full
keyboard navigation in WebKit on macOS. The focused Chromium run passed 16
executions and 48 scans with zero axe violations, document overflow or
definite text contrast failures. All 100 flagged samples have dispositions.
See `graph-shortcuts-final-results.json`, `graph-shortcuts-final-review.json`
and `graph-shortcuts-final-engines-results.json`.

## Earlier recorded runs

The previous combined checkpoint passed 107 Chromium executions and 674 axe
scans. All 1,639 flagged text samples have dispositions. See
`acceptance-local-results.json`, `acceptance-local-input-manifest.json`,
`acceptance-local-dispositions.json` and `acceptance-local-evidence-index.json`.

The earlier full run in `full-local-assessment-results.json` preceded a
non-text contrast fix in the run-lineage graph.
Rendered pending dependencies measured 2.08–2.17:1 before the change
and 4.50–4.78:1 after it. The successful dependency measured at least
7.17:1 in both probes. The measurements compare rendered stroke pixels
with the same canvas pixels after hiding the stroke and cover the sampled
dependencies at 100% graph scale. Eighteen focused Chromium executions
and 100 scans passed after the color change and fixture cleanup. The
168 flagged text contrast samples have dispositions. See
`lineage-edge-contrast-baseline.log`, `lineage-edge-contrast-final.log`,
`lineage-contrast-final-results.json` and `lineage-contrast-final-review.json`.

A separate browser check sampled 24 page titles across both widths.
Git sync, deployment and binding setup had shared the inventory title
`Pipelines`. Each now uses its task name. Eighteen Chromium deployment
executions passed 108 scans after the change. The focused tests verify
titles for inventory, sync, binding setup and deployment, including
missing-version errors. The scans found zero axe violations, document
overflow or definite text contrast failures. All 164 flagged text
samples have dispositions. On the local dashboard, source inventory and
query inventory, Documentation was last in both primary menus.
See `full-local-shell-navigation.log`, `deployment-titles-baseline-results.json`,
`deployment-titles-results.json` and `deployment-titles-review.json`.

The consolidated local checkpoint on 2026-11-18 passed all 93 Chromium
153.0.8010.12 tests using axe-core 4.14.1. Across 590 scans the run found
zero axe violations, document overflow or definite text contrast failures.
All 1,544 flagged color-contrast samples have recorded dispositions. The
only other axe review item was the zoom symbols' accessible names, covered
by the existing symbol review below. See `consolidated-checkpoint-results.json`,
`consolidated-checkpoint-input-manifest.json` and
`consolidated-checkpoint-review.json`.

A separate actual page zoom probe found that the Builder node summary could
cover the focused destination at 200%. If the summary cannot fit to either
side, it is now positioned vertically away from the card. The replay
verified unobscured destination focus, continued keyboard correction and no
document overflow at actual 200% and 400% Chromium zoom. The hover,
inspection, editing and validation regressions also passed eight desktop
and compact tests with 20 axe scans and 437 contrast dispositions. The
consolidated run preceded this placement change. See
`actual-page-zoom-builder-validation-baseline.log`,
`actual-page-zoom-builder-validation-after.log`,
`builder-zoom-after-results.json` and `builder-zoom-after-review.json`.

The 100-minute Google authorization controls remained fully visible and
keyboard operable at actual 200% and 400% Chromium zoom on sign-in and
signup. At both zoom levels the local issuer returned after a simulated
11-minute delay, and Core preserved the intended destination. See
`actual-page-zoom-google-more-time.log`.

The rebuilt Core runtime and synthetic fixtures passed 89 Chromium executions
on 2026-11-17 in the local timezone. The 550 scans reported zero axe
violations, document overflow, or definite text contrast failures. All 1,198
flagged text contrast samples have recorded dispositions. The input manifest
records the source diff, generated CSS and executables used. See
`current-combined-results.json`, `current-combined-input-manifest.json`,
`current-combined-contrast-continuity.json` and
`current-media-source-review.json`.

The combined report flagged the Builder Inline SQL checkbox's nested labels
in eight scans and the named run lineage action container's missing group
role in 20 scans. The Builder toggles now have one label each, including the
JavaScript fallback. Run lineage names its current configuration actions as
a group. A focused replay of all flagged states passed eight Chromium
executions and 52 scans. Both types of markup review disappeared, and all
332 flagged text samples matched the combined dispositions. See
`a11y-markup-results.json` and `a11y-markup-evidence.json`.

Compact Firefox 155.0 and WebKit 26.6 also passed the Proof claims, run
timeline and sink evidence, and repository branch retry keyboard journeys.
On macOS the WebKit checks used Option-Tab to reach all controls. Firefox
used type-ahead and Enter to commit the repository selection. These are
focused behavior checks. Their extra supplemental contrast samples have not
been classified. See `current-cross-browser-native-keys-results.json` and
`current-firefox-branch-native-results.json` for the successful Firefox
retry after the initial key sequence failed.

The 2026-11-17 run used local source based on `f810440` with the #160 worktree changes.

- macOS 27.0.1, pinned Node 24.19.0 / Playwright 1.63.0, headless Chromium
  153.0.8010.12, @axe-core/playwright 4.14.0 and axe-core 4.14.1.
- The full assessment after the field border change passed all 39 executions
  with zero skips. The 262
  scans found zero axe violations, definite rendered-contrast failures or document-overflow
  failures. Normal and increased-text-spacing states use 1280 × 720 and
  320 × 900 CSS pixels; keyboard checks also use 1280 × 900.
  This run used the standard configuration through the temporary
  `native-style.config.cjs` wrapper, with Core-only seeding. The wrapper keeps
  the same tests, browser projects, rule scope and assertions, and adds native
  rendering metadata without field values or selected text. The manifest records
  its command and fingerprint. `results.json` copies `native-style-results.json`.
- The keyboard sign-in lifecycle runs once and checks both widths, avoiding
  duplicate attempts against the shared account's authentication throttle.
  Anonymous contexts explicitly discard saved sessions. The test checks error
  focus, preserved email, cleared password, autocomplete semantics, successful
  retry, desktop/compact navigation and compact sign-out.
- A later focused run passed both desktop and compact setup/inspection cases
  after fixing dashboard checklist focus. Hide now focuses Resume setup;
  reopening the checklist focuses Hide. The existing Vitest regression failed
  before this fix. All 422 JavaScript tests and JavaScript lint passed afterward.
  `onboarding-focus-browser.json` retains this focused run. The latest full-suite
  checkpoint also includes this fix.
- Browser zoom shortcuts now reach the browser in Builder and lineage.
  Builder no longer intercepts Ctrl/Command with +, =, − or 0 for canvas zoom.
  Lineage handles its unmodified viewport keys while leaving modified keys
  alone. Canvas buttons remain available by keyboard. Regressions failed before
  these changes and passed afterward: 423 JavaScript tests, JavaScript lint and
  both desktop/compact Builder inspection cases. `browser-zoom-before.json` and
  `browser-zoom-after.json` retain the browser regression results. The regression
  checks event cancellation; actual browser zoom across every workflow remains open.
- A later focused email-code MFA case enters sign-in credentials by keyboard,
  recovers from an invalid code and continues to the preserved return path.
  It checks one-time-code autocomplete, numeric input mode, focused inline
  feedback, the code's error description, cleared code and a single main landmark.
  The test pastes the corrected code using Chromium's clipboard and the
  normal keyboard shortcut. Both widths pass. Shared form initialization also
  runs after HTMX settles, and a Vitest regression verifies that subsequent
  settles do not steal focus.
  The API challenge and verification replies are synthetic; delivery, expiry,
  rate limits and provider behavior are outside this fixture. `mfa-before.json`
  retains the original focus failure. `mfa-after.json` retains focused evidence
  after the 33-execution full-suite checkpoint. Authentication handler tests,
  425 JavaScript tests and JavaScript lint passed after these changes.
- Chromium's built-in password manager filled the Core sign-in form from a
  synthetic credential saved in a temporary browser profile with sync
  disabled. At 1280 and 320 CSS pixels, both inputs matched the browser's
  autofill state and submitted the stored values by keyboard. The fixture
  completed email-code MFA and opened the requested return page. The
  autofilled text measured 12.75:1 against its background in both fields at
  both widths. The probe removed its browser profile and retained only
  results and colors. See `password-manager-core-evidence.json` and
  `password-manager-core.cjs`.
- The owned Core and Mailpit lifecycle passed with keyboard paste in both
  password fields on desktop signup and compact reset. It verified email,
  exercised mismatch recovery, reset the password and signed in with the
  replacement. Chromium 153.0.8010.12 recorded 32 scans at both widths and
  spacing settings. No axe violations or document overflow appeared. All
  contrast results matched the earlier classified run, including ten
  flagged samples. See `recovery-paste-results.json` and
  `recovery-paste-evidence.json`.
- Signup and password reset now keep the minimum-length instruction visible
  and expose it as a field description. The owned Core and Mailpit regression
  verified the field name and description, keyboard paste, mismatch recovery
  and replacement-password sign-in. All 36 scans at desktop and 320 CSS px,
  with normal and increased spacing, passed without axe violations or document
  overflow. The 18 flagged contrast samples have dispositions, including new
  password input colors at 17.49:1. At actual 200% and 400% page zoom,
  keyboard focus stayed visible and unobscured on the field. Its help text
  fit within the page on signup, reset and reset after a mismatch. See
  `password-instructions-results.json`, `password-instructions-review.json`
  and `password-instructions-zoom-results.json`.
- The disposable Chromium profile reported that it disabled password
  generation because password sync was off. Firefox 155.0 also did not
  supply a generated password on Core or in a loopback control form with
  generation enabled, using either keyboard or pointer. Native generation
  remains unverified. These probes used
  synthetic form data and removed their browser profiles. W3C identifies
  password-manager entry and paste as ways to reduce reliance on memory
  and transcription during authentication. See
  [Understanding Accessible Authentication](https://www.w3.org/WAI/WCAG22/Understanding/accessible-authentication-minimum.html)
  and `native-password-generation-chromium.json`,
  `native-password-generation-firefox-enabled.json` and
  `native-password-generation-firefox-control.json`.
- The PostgreSQL journey used the owned Core runtime and synthetic loopback
  database. Keyboard users recovered from a refused connection to a missing
  database, saved the source and updated its settings, and ran a saved SQL
  query that returned a named column and its value. The SSL control now
  exposes its help as a description in both creation and settings. The
  read/write badge uses an opaque background because the selected inventory
  card lowered the previous badge's contrast to 4.35:1. Its text passed
  contrast checks on the inventory card and settings page after the change.
  Two Chromium 153.0.8010.12 tests passed 20 scans at desktop and 320 CSS px,
  including increased text spacing. No axe violations or document overflow
  appeared. The 24 closed-select text samples flagged for contrast review
  measured 17.49:1. The test cleaned its synthetic organizations and
  accounts. See `postgres-source-results.json` and
  `postgres-source-evidence.json`.
- The parameterized PostgreSQL follow-up identified two unnamed selects
  in the query editor and a divide-by-zero failure reported as an unavailable
  data source. Parameter rows now have persistent visible labels on create
  and edit. PostgreSQL error feedback explains how to correct the divisor
  without exposing database diagnostics. In the keyboard test, focus moved
  to the error, the Results tab returned to Definition with the entered
  value intact, and editing that value produced a successful result. The
  results table was reachable by Tab and scrolled by arrow key at 320 CSS px.
  Both Chromium widths passed 28 scans with zero axe violations or document
  overflow. The 40 closed-select contrast samples measured 17.49:1. Both
  HTTP fixture journeys also passed 28 scans and matched all 38 contrast
  review samples in the earlier classified run. See
  `postgres-execution-baseline-results.json`, `postgres-execution-results.json`,
  `postgres-execution-evidence.json` and `http-query-parameter-results.json`.
- The owned Core instance and a loopback MongoDB 8.2.12 fixture verified
  keyboard recovery from an invalid authentication database, source settings
  save, and correction of an invalid JSON query filter followed by a successful
  preview. Failed query creation had kept the draft while dropping the error
  in the page update. The query form now shows an inline error, preserves
  submitted values, and brings the focused alert into view. The HTTP
  fixture regression also verifies error focus, visibility and retry.
  Four Chromium journeys passed 48 scans at desktop and 320 CSS px, with
  normal and increased spacing. There were zero axe violations, document
  overflows or definite text contrast failures. All 54 contrast samples have
  dispositions. At actual 200% and 400% Chromium zoom, the MongoDB query
  error stayed visible and unobscured. See
  `mongo-source-before-feedback.json`, `mongo-source-results.json`,
  `mongo-source-review.json`, `mongo-source-zoom-results.json`,
  `http-query-create-feedback-results.json` and
  `http-query-create-feedback-review.json`.
- Saved query edits now preserve the draft on refusal and focus an inline
  alert. The previous viewport position is restored after a successful save,
  while an error brings the alert into view. The HTTP fixture passed keyboard
  rejection, correction, save and execution at desktop and 320 CSS px. The
  saved-query alert stayed visible and unobscured at actual 200% and 400%
  Chromium zoom. The PostgreSQL journey verified that the Row limit input
  has a short label and separate help in both create and edit. Across the
  four Chromium executions, 60 scans reported zero axe violations, document
  overflow or definite text contrast failures. All 90 flagged text samples
  have dispositions. See `http-query-edit-before-feedback.json`,
  `http-query-after-bigquery-results.json`, `http-query-after-bigquery-review.json`,
  `http-query-edit-feedback-zoom-results.json`, `query-row-limit-results.json`
  and `query-row-limit-review.json`.
- In the local Core S3 journey, keyboard users corrected a missing bucket,
  saved a source prefix after a settings validation error, and changed the
  preserved object parameter after a missing-object result. The corrected
  query returned the synthetic JSON data through the loopback S3 fixture.
  A long key previously extended 681 CSS px past the compact result panel
  even though the document itself fit the viewport. Result summary values
  now wrap long identifiers. Successful executions focus the group named
  by the Result heading. The key fit and focus stayed visible at actual
  200% and 400% Chromium zoom. Six S3, PostgreSQL and HTTP test executions
  passed 80 scans across desktop and compact widths. There were no axe
  violations, document overflow or definite text contrast failures. All
  86 text contrast review samples have dispositions. See
  `s3-source-key-clipping-baseline-results.json`,
  `s3-source-and-query-regressions-results.json`,
  `s3-source-and-query-regressions-review.json` and
  `s3-source-zoom-results.json`.
- The S3 credential-rotation check opened the native confirmation using
  Enter. Playwright dismissed and accepted it through the browser dialog API.
  Cancel returned focus to Rotate credentials and sent no request. The
  server-side validation error previously opened an unfocused page alert.
  It now focuses the inline error and keeps the entered values for retry.
  On success, the focused confirmation stays in view, the credential fields
  clear, and the object query uses the rotated access key. See
  `s3-rotation-baseline.json` and `s3-rotation-scroll-baseline.json` for
  the original focus failure and the offscreen success message.
  The S3, PostgreSQL and HTTP checks passed six Chromium executions and
  88 scans at desktop and 320 CSS px, with no axe violations, overflow or
  definite text contrast failures. All 86 text contrast review samples
  have dispositions. At actual 200% and 400% Chromium zoom, both rotation
  feedback messages had visible focus outlines and unobscured centers,
  with no horizontal document overflow. The HTTP OAuth reconnect
  regression also passed at both widths. See
  `rotation-and-query-regressions-results.json`,
  `rotation-and-query-regressions-review.json`,
  `s3-rotation-zoom-results.json` (200%),
  `s3-rotation-zoom-400-results.json` (400%) and
  `rotation-oauth-results.json`. The initial 400% attempt hit the local
  signup rate limit during fixture setup and passed on retry.
- The local MySQL journey used MySQL Community Server 26.7.0 with synthetic
  data on loopback. The browser now exposes Password as the field name and
  associates the blank-password hint as a description only for MySQL.
  An unsupplied SQL placeholder previously yielded a generic validation
  error when its saved parameter was marked optional. The focused error
  now names the missing parameter so the user can enter it and rerun the
  query. See `mysql-source-baseline.json` and
  `mysql-source-parameter-baseline.json`. The corrected journey passed at
  desktop and 320 CSS px. Users corrected a missing database, saved
  settings after an invalid host, and recovered a query to a one-row result.
  Its 32 scans found no axe violations, overflow or definite text contrast
  failures. All 28 contrast review samples have dispositions. Both actual
  zoom checks at 200% and 400% showed an unobscured focus outline on the
  connection error with no horizontal document overflow. See
  `mysql-source-results.json`, `mysql-source-review.json` and
  `mysql-source-zoom-results.json`.
- Missing and denied Proof settings keep their HTTP 404 and 403 statuses
  and render a focused error and a keyboard route back to Git integrations.
  When a repository changes during editing, the focused error preserves
  the draft and offers a reload link. Reloading opens saved settings for
  the replacement repository. The user then explicitly selects targets and
  saves again. Each of the three error states kept an unobscured focus
  outline and fit the document at actual 200% and 400% Chromium zoom.
  The final fixture run passed 14 Proof tests and 60 scans at both widths,
  with no axe violations, overflow or definite text contrast failures.
  The rebuilt local frontend and API also returned 404 for a missing
  source, with focused feedback and keyboard navigation passing at both
  widths across four scans. All contrast review samples have dispositions.
  See `proof-configuration-recovery-baseline.json`,
  `proof-configuration-recovery-evidence.json` and
  `proof-configuration-recovery-zoom-results.json`, plus
  `proof-configuration-neighbor-results.json` and
  `proof-configuration-neighbor-review.json` for the final fixture run and
  `proof-configuration-local-review.json` for the local 404 check.
- The Builder contract validation journey injected an incompatible producer
  and destination diagnostic and a related step diagnostic. The Validate
  button kept focus during preflight and blocked repeat activation while
  the request was pending. The diagnostics moved focus to the connection
  or step, and Inspect destination moved focus from the connection preview
  to its step. The diagnostic buttons grew to a 24 CSS px minimum height.
  After correcting the input contract, keyboard users revalidated and saved
  the draft. The fixture returned the saved draft after browser storage was
  cleared. Two Chromium tests and four adjacent keyboard regressions passed
  at desktop and compact widths. Their 20 scans found no axe violations or
  document overflow. Scroll resampling covered 215 clipped text samples,
  88 closed selects measured 17.49:1, and three runtime-switch samples
  matched the earlier review. The adjacent scans matched the earlier
  contrast dispositions. The zoom-button symbol review below also applies.
  See `builder-validation-baseline-results.json`,
  `builder-validation-results.json` and `builder-validation-evidence.json`.
- Builder now links draft step diagnostics reported as
  `pipeline_spec.steps[n]` to the affected step. Activating a step or
  connection diagnostic from Source switches to Split, keeps unapplied
  source text and scrolls the focused target into view. The keyboard
  journey verified both targets at desktop, 320 CSS px, and actual 200%
  and 400% Chromium zoom. Two executions passed 16 axe scans with no
  violations or document overflow. All text contrast samples have
  dispositions. At 320 CSS px, the diagnostic button's minimum contrast
  rose from 4.05:1 with increased text spacing to 11.42:1 across the
  final samples. See `builder-diagnostic-navigation-results.json`,
  `builder-diagnostic-navigation-dispositions.json`,
  `builder-diagnostic-navigation-text-results.json` and
  `builder-diagnostic-navigation-zoom-results.json`.
- Local draft diagnostics now also reveal Split and focus the affected step
  when activated from Source. The keyboard journey keeps unapplied source
  text while inspecting the step, then connects, validates and saves the
  draft. At compact width, the canvas-scaled inset focus ring had covered
  step labels and badges. The outside outline keeps the label area clear
  and measured at least 13.55:1 against the sampled adjacent colors.
  Four executions passed 24 axe scans with no violations, document overflow
  or definite text contrast failures. All 569 flagged text samples have
  dispositions. The zoom-button symbol name review below still applies.
  Actual 200% and 400% zoom probes verified that the focused card was visible
  after navigation. See `builder-local-diagnostic-baseline.json`,
  `builder-local-diagnostic-final.json`, `builder-local-diagnostic-dispositions.json`,
  `builder-local-diagnostic-outline-results.json` and
  `builder-local-diagnostic-zoom-results.json`.
- When update permission is revoked after deployment review opens,
  the HTTP 403 response keeps the exact version, target and selected
  rollback resolution available in a read-only view. Focus moves to
  the explanation and Tab reaches Review current deployment. Neither
  compile nor deployment runs if the frontend detects the permission
  change. A backend authorization refusal also keeps read-only review
  available. The rollback fixture confirmed the deployed revision stayed
  unchanged and that regaining permission requires a new confirmation.
  At actual 200% and 400% Chromium zoom, both refusal messages stayed
  focused and visible. The deployment, rollback and binding regressions
  passed 22 Chromium executions and 116 scans with no axe violations,
  overflow or definite text contrast failures. All 184 contrast review
  samples have dispositions. See `deployment-permission-review-baseline.json`,
  `deployment-permission-review-results.json`,
  `deployment-permission-review-results-reviewed.json` and
  `deployment-permission-review-zoom-results.json`.
- Deployment target review now focuses the new target after keyboard
  activation. Both the deployment form and the shell switcher clear a
  rollback selection that belongs to the previous environment and explain
  the change beside the focused heading. Switching from Development to
  Staging kept the immutable version, showed Staging's deployment revision,
  and opened Staging's unbound source setup. Returning from bindings kept
  Staging selected. Reviewing Development again preserved its rollback
  selection. The two focused Chromium tests and six existing protected
  deployment, rollback and read-only plan regressions passed 64 scans
  with no axe violations or document overflow. All 108 contrast samples
  flagged for review have dispositions. At actual 200% and 400% page zoom,
  both switchers brought the target heading into view with a visible
  outline. See `deployment-target-baseline-results.json`,
  `deployment-target-results.json`, `deployment-target-regression-results.json`
  and `actual-page-zoom-deployment-target.log`. Deployment mutations were
  exercised only against the synthetic fixture.
- If a selected deployment target is disabled before submission, the
  focused alert now leads to an explicit enabled-target choice. The page
  keeps the immutable version and removes binding, compile and deploy actions
  for the unavailable target. With no targets enabled, the next Tab reaches
  the project environment settings link. Synthetic browser journeys removed
  Staging during review, recovered to protected Development and opened the
  empty environment list. The focused suite covered the neighboring rollback,
  compile, protected-binding and permission states. All 12 executions and
  80 scans passed with no axe violations or document overflow. All 132 flagged
  samples have dispositions. Recovery focus stayed visible at actual 200%
  and 400% zoom. See `deployment-target-removed-baseline.json`,
  `deployment-target-removed-final.json`, `deployment-target-removed-review.json`
  and `deployment-target-removed-zoom-results.json`.
- Read-only binding review preserves the selected version and plan on return
  to deployment. If pipeline read permission is revoked, the binding link
  opens a 403 page that focuses the explanation and offers a keyboard return
  to organizations. If edit permission is revoked while the protected save
  dialog is open, the binding panel refreshes, focuses the refusal and
  leaves the version available for read-only review. At actual 200% and
  400% page zoom, both refusals and the return link retained visible focus
  without document overflow.
  At 320px, increased text spacing previously put missing-binding glyphs
  over the badge edge, where contrast fell to 2.34:1. The badge now grows
  and wraps, and the glyph-background measurement reached 6.45:1 in both
  binding states with and without the spacing override. Four new browser
  tests and ten adjacent deployment regressions passed with 88 scans and
  no axe violations, document overflow or definite contrast failures. All
  132 text contrast samples flagged for review have dispositions. See
  `deployment-binding-errors-baseline-results.json`,
  `deployment-binding-glyphs-baseline-results.json`,
  `deployment-binding-glyphs-results.json`,
  `deployment-binding-badge-results.json`,
  `deployment-binding-permissions-review.json` and
  `actual-page-zoom-deployment-binding-permissions-final.log`.
- In the six-step branched run, keyboard users reached both named inputs to
  the merge from the dependency list. The merge showed the same successful
  status and 1.00 s duration in the graph, step list and selected evidence.
  The timeline sorted all 12 events by timestamp and its merge events
  confirmed the duration. The completion link focused the executed
  definition. Before the fix, `aria-current` marked both merge events as
  current when the merge was selected in lineage. That
  state now stays on the lineage selection.
  Eight Chromium desktop and compact tests passed with 56 scans, zero axe
  violations, document overflow or definite text contrast failures. All
  99 text contrast review samples have dispositions, including 27 remeasured
  inside the step list scroll area. The zoom symbol name review below also
  applies. At actual 200% and 400% zoom, the keyboard path through the
  step link, dependency, timeline and executed definition kept focus visible
  without document overflow. See `lineage-branch-baseline-results.json`,
  `lineage-branch-scroll-clipping-results.json`, `lineage-branch-review.json`
  and `actual-page-zoom-lineage-branch-final.log`.
- Shared input/select/textarea focus outlines now use the opaque primary color
  and sit inside field boundaries. The former translucent outlines measured below
  3:1; scroll panels also clipped their side edges. The rendered regression checks
  both adjacent backgrounds in 16 normal/increased-spacing measurements at both
  configured widths. Its minimum is 5.17:1. Brave Side checks at 1280 × 900 and
  320 × 900 confirmed the textarea's full outline is visible after the change.
  `field-focus-baseline.json`, `field-focus-after.json` and
  `manual-field-focus-review.json` retain this evidence. This covers these field
  focus states, not every component boundary or focus indicator.
- Active input, select and textarea borders now use a lighter color to make
  their boundaries visible against dark surfaces. The previous borders measured
  about 1.22–1.30:1 on the sampled left edges. The regression measures the
  unfocused login email input, Builder runtime select and Builder SQL textarea
  at both widths and spacing settings. All 12 boundaries now meet 3:1 against
  the adjacent exterior, with a minimum of 3.68:1. The 16 focus measurements
  still meet 3:1. The focused evidence is in `field-boundary-baseline.json` and
  `field-boundary-after.json`. See the
  [W3C non-text contrast guidance](https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html).
- Signup now replaces the whole main landmark and focuses inline feedback when
  the passwords do not match. The keyboard regression verifies one main,
  preserved name, email and return path, and cleared passwords at both widths.
  The existing recovery test also passed after creating and verifying an account
  through the updated signup form. `signup-mismatch-baseline.log` records the
  original duplicate landmark and lost focus, and `signup-keyboard-results.json`
  records the three passing executions after rebuilding the frontend. These
  focused results are newer than the full assessment checkpoint.
- The expanded suite passed 43 executions and 278 scans after adding signup
  and verification resend. Verification covers a missing token, correction of
  a server-rejected email address, and a privacy-preserving status after retry.
  Each response retains one main landmark, the error leaves the address
  editable, and feedback receives focus. That test uses a synthetic address
  with no account to check the privacy-preserving response.
  Go handler tests also cover backend throttling and the preserved return path.
  `full-verification-before-label-edge.json` and
  `full-verification-inputs.json` record the run and its inputs before two
  further visual adjustments.
- The recovery journey also signs up by keyboard, requests a resend from the
  pending verification page, and waits for Mailpit to receive a different link.
  Following that second link verifies the disposable account before the test
  proceeds through password reset and account cleanup. It scans the pending and
  resend confirmation pages at both widths. `resend-mailpit-results.json`
  records this local delivery check.
- The next full assessment passed 43 executions and 286 scans after the
  delivery, button and focus stroke changes, with no definite contrast failures,
  axe violations or document overflow. `full-after-mailpit-results.json` and
  `full-after-mailpit-summary.json` preserve that run. All 494 raw text contrast
  review samples have scoped dispositions. `mailpit-contrast-continuity.json`
  records the matches, and `mailpit-native-text-review.json` recalculates the 318
  sampled native values and placeholders.
- The verification resend action now has a shorter label and room to grow as
  text wraps. The old label reached the bottom of the button at 320 CSS pixels
  with increased text spacing. The rebuilt page keeps the shorter label on
  one line in that state and its minimum sampled text contrast is 4.64:1.
  See `resend-button-geometry.log` and `resend-button-final.log`.
- The Builder connection focus stroke is brighter. On the event-to-normalize
  fixture connection, 56 rendered points across both widths measured
  1.42–1.45:1 before the fix and at least 3.75:1 afterward. The probe compares
  points on either side of the center line with the underlying canvas pixels
  and keeps the connection focused throughout. `graph-focus-contrast.json`
  records the measurements. Other connections and zoom levels still need
  assessment.
- The full run lineage offers a keyboard disclosure for named dependencies at
  desktop and compact widths. The keyboard test follows the failed orders input
  from fetch to normalize, verifies focus reaches recorded step evidence,
  opens the technical diagnostic, and follows the executed contract link.
  At desktop width it also compares the list link with the graph link.
  `lineage-keyboard-results.json` records the fixture checks at both widths
  and the MFA clipboard cases. Clipboard checks use a synthetic code and
  do not exercise a password manager.
- Desktop now offers the same lineage step list as the compact view, before
  the graph. Its step destinations match the graph, and the dependency
  disclosure lists each graph dependency with the same name and destination.
  The regression keeps a step link focused through a live update and opens
  the recorded evidence from the list using Enter. The existing large-graph,
  layout and live progress product checks passed. See
  `lineage-list-results.json`, `lineage-affected-e2e-partial-results.json` and
  `lineage-affected-e2e-results.json`.
- At 320 CSS pixels, Tab reaches the executed input contract table and Arrow
  Right scrolls it under both spacing settings. All ten clipped text samples
  from the executed contract view pass after scrolling, with a minimum of
  5.58:1. Four heading samples mixed the browser focus outline into the
  background bounds. Temporarily making just the outline transparent, while
  keeping focus and layout, gives 15.97:1 in all four cases. The outline was
  restored before the test continued. See `lineage-contrast-checkpoint.json`.
- The full run with lineage and clipboard coverage passed 45 executions and
  298 scans. It reported no axe violations, document overflow or definite
  text contrast failures. All 520 raw text contrast review samples have
  scoped dispositions in `lineage-contrast-continuity.json`. The report and
  source fingerprints are in `full-after-lineage-results.json` and
  `full-after-lineage-summary.json`.
- Password and email-code entry both support paste in the isolated Chromium
  MFA journey at both widths. The test submits the pasted password before
  exercising the MFA challenge. The separate Chromium password-manager probe
  later verified autofill.
  The unusable reset link screen focuses an inline error and offers a direct
  link to request another reset. The keyboard checks cover a missing token,
  an actual local API rejection of a synthetic token, and navigation to a
  new request. The valid signup, verification and reset lifecycle also
  passed. See `reset-link-paste-results.json`.
- A separate Chromium 153.0.8010.12 probe used true page zoom in a temporary
  browser profile with a local extension. The extension reports the active
  zoom factor, and the CSS viewport shrank from 1280 × 813 to 640 × 406
  and 320 × 203 without viewport emulation. Seventy-four focused checks covered
  authentication recovery, project creation, the HTTP source form, schema
  status, source settings and query errors, Builder SQL and links, focused
  event and transform cards, the
  selected merge unit, manual editor and Apply merge,
  protected binding confirmation and Escape, the organization popover,
  lineage step selection and executed contracts, run filters and reset,
  run recovery refusal, Proof configuration
  feedback, and Proof history and evidence. None of
  those states produced document-level horizontal overflow. The checks
  exercise selected keyboard routes and focus visibility without covering
  every state in each process. See `actual-page-zoom-checkpoint.json`.
- The full run after the reset-link fix passed 47 executions and 306 scans.
  It reported no axe violations, document overflow or definite text contrast
  failures. The 190 non-native review samples match the preceding run, and the
  330 native samples were recalculated. See `full-after-reset-link-results.json`,
  `full-after-reset-link-summary.json` and `reset-link-contrast-continuity.json`.
- The full checkpoint with schema and manual merge changes passed 47 executions
  and 314 scans. There were no axe violations, document overflow failures or
  definite text contrast failures. Of 525 raw text reviews, 516 matched the
  prior evidence, four source icon samples measured 5.49:1 or higher, and five
  initially clipped conflict/merge samples measured 4.83:1 or higher when
  scrolled into view. See `full-after-builder-merge-summary.json`,
  `full-after-builder-merge-contrast-continuity.json` and
  `merge-label-probe-results.json`. The run-list focus change has separate evidence.
- The next full checkpoint included run-list focus. All 49 executions and
  326 scans passed, with no skips, axe violations, document overflow failures
  or definite text contrast failures. Its 565 raw text reviews have individual
  dispositions. See `full-after-run-list-summary.json` and
  `full-after-run-list-contrast-continuity.json`.
- Repeating the 49-execution suite with the scale-aware Builder focus outline
  passed all 326 scans, with the same disposition coverage for its 565 raw
  text reviews. See `full-after-builder-focus-results.json` and
  `full-after-builder-focus-contrast-continuity.json`.
- The full Proof and Builder feedback checkpoint passed 57 executions and
  354 scans with no skips, axe violations, document overflow or definite text
  contrast failures. The scan used Chromium 153.0.8010.12 and axe-core 4.14.1.
  All 866 raw text reviews have scoped dispositions. The follow-up remeasured
  195 clipped text samples after scrolling, checked 104 additional closed
  select values, and sampled the painted glyph backgrounds for six focused
  Builder run feedback entries. See
  `full-after-proof-builder-feedback-summary.json` and
  `full-after-proof-builder-feedback-contrast-continuity.json`.
- The full source settings and query recovery checkpoint passed 57 executions
  and 366 scans with no skips, axe violations, document overflow or definite
  text contrast failures. Follow-up classified 888 of the 894 raw text
  reviews. The six organization menu entries that lacked readable samples
  were checked with the menu open. Five passed and the Active label measured
  3.51:1 before the fix below. See `full-after-source-recovery-summary.json`,
  `full-after-source-recovery-contrast-continuity.json` and
  `compact-organization-contrast-results.json`.
- The orientation and motion review inspected 95 Templ, JavaScript, CSS and
  manifest inputs. It found no orientation lock, rotate-to-continue rule or
  sensor-based input in those files. The browser runs separately provide layout
  and operation samples at portrait and landscape viewport sizes. See
  `orientation-and-motion-source-review.json`.
- Shared success notices now stay in the page as inline status messages. They
  previously disappeared after five seconds. The seeded retention page probe
  advanced the browser clock six seconds at both widths and confirmed the
  message remained visible in the page flow. The probe did not save settings.
  See `success-feedback-probe.log`.
- The source schema browser leaves the pending state in place until the user
  activates Check import status. The browser check advances the clock past
  the old polling interval after the synthetic backend reports ready. The
  pending display remains until keyboard activation, then focus moves to the
  schema heading, whose accessible description contains the current status.
  The query draft can then be opened. Both widths passed. The pending control
  and updated heading also had visible focus at 200% and 400% page zoom.
  See `schema-icon-contrast-final-results.json` and
  `actual-page-zoom-schema.log`. The separate HTTP provider integration test
  stopped at connection validation before it reached schema import.
- Source settings now swap only the error message after an HTMX save is
  refused. The keyboard test verifies that the proposed name and health path
  stay entered, then corrects the health path and reads the focused save
  confirmation. The save form no longer restores the previous scroll position
  over the confirmation's focus. A plain form submission still uses the
  redirect error path.
- Query validation and provider execution errors now appear in the Results
  panel. The focused result names the error, and detailed diagnostics can be
  opened from the keyboard. The regression checks required-parameter feedback,
  a synthetic provider refusal, the preserved input after returning to
  Definition, and a successful retry. Both source and query cases passed at
  both widths. The focused source error, save confirmation and query error
  were visible at 200% and 400% page zoom. See
  `source-recovery-final-results.json` and
  `actual-page-zoom-source-recovery.log`.
  The targeted contrast follow-up measured the HTTP icon text at 5.49:1 or
  higher and the focused query disclosure text at 14.99:1. See
  `source-recovery-svg-results.json` and `source-query-glyphs-results.json`.
- The FTP query probe provisioned a synthetic source through the API. The
  Core source form offers 12 source types and has no FTP creation option.
  The keyboard journey created a CSV query, focused missing-file feedback,
  returned to Definition with the file parameter preserved, and previewed
  two rows. Both widths passed with 12 scans and zero violations, document
  overflow or definite text contrast failures. All 20 flagged samples have
  dispositions. The FTP glyph measured 8.40:1. Error and result focus
  remained visible at actual 200% and 400% zoom. See `ftp-query-results.json`,
  `ftp-query-review.json`, `ftp-query-svg-results.json` and
  `ftp-query-zoom-results.json`.
- Builder's schema panel uses its existing Reload cache button for progress.
  Both widths kept the importing status in place for six virtual seconds after
  the fixture made the cache ready. Keyboard activation updated the status and
  dataset list while focus stayed on the button. The existing schema proposal
  product test also passed. See `builder-schema-manual-results.json`.
- Builder execution displays one persistent inline result with a keyboard
  disclosure for failure details and a link to the run when a record exists.
  The regression checks focused failure and accepted feedback after six virtual
  seconds, then opens the run using the link. The existing bootstrap and
  bounded backfill test also passed. See `builder-run-feedback-results.json`
  and `builder-affected-e2e-results.json`.
  The six mixed text-contrast samples in the focused feedback and details
  disclosure measured 14.99:1 over the painted text pixels at both widths.
  See `builder-run-glyphs-results.json`.
- Proof settings focus the validation error or saved outcome. A failed
  submission retains entered settings and allows retry. The keyboard cases
  cover selecting a missing assessment path after refusal and retrying a
  simulated GitLab setup failure without re-entering values. They also verify
  that the path field exposes its format instructions. Axe identified an
  undersized publication checkbox in the opened options. Both checkboxes use
  at least 24 CSS pixels after the fix. Validation and saved feedback retained
  visible focus with actual 200% and 400% page zoom. See
  `proof-configuration-results.json` and `actual-page-zoom-proof-configuration.log`.
- Builder Delete and Backspace now require focus on the selected step or
  connection. The previous handler removed a selected step while keyboard focus
  was on Preview. The focused authoring regression checks that both toolbar
  actions leave the graph intact, then verifies that Delete still works on the
  selected items and restores focus. See `builder-delete-scope-baseline.log`
  and `delete-scope-final-results.json`.
- Builder card labels now use block spans rather than paragraphs. Increased
  paragraph spacing previously pushed the badges outside their fixed-height
  cards. Each assessment state now checks that badge rectangles fit their own
  cards, independently of the graph viewport. The compact keyboard authoring
  regression failed before this fix and passed afterward; 425 JavaScript tests,
  affected Go view tests and JavaScript lint also passed. The focused evidence
  is in `badge-spacing-baseline.json` and `badge-spacing-after.json`; this change
  is included in the 39-execution full assessment checkpoint.
- The password recovery test verifies a disposable account through the owned
  Mailpit inbox. It requests a reset link by keyboard at desktop width, then
  corrects a password mismatch and follows the sign-in link by keyboard at 320px.
  It checks autocomplete, status semantics, mismatch focus, empty password fields
  and one main landmark after each form swap. It scans six states at both widths
  with and without increased text spacing, then deletes the disposable account
  after signing in with the replacement password.
- The HTTP source test uses the keyboard to test a connection, correct a refused
  health path, create the source, save a draft from its schema and preview a
  saved query. It checks preserved inputs and focused feedback after failure
  and success. The raw result wraps at 320 CSS pixels. Desktop navigation
  exposes unavailable destinations as disabled links with their scope
  requirement. Connection and result responses come from an isolated API
  fixture. A separate probe of the owned runtime confirmed the inline error
  received focus after HTTP 400. The runtime has plaintext connectors disabled,
  and the synthetic loopback provider received no requests.
- The workspace/project lifecycle signs up and verifies a disposable user,
  then tests a rejected custom workspace identifier and a successful retry.
  It follows Projects from the compact navigation, creates a project after
  correcting another invalid identifier, and opens its detail page. Error
  feedback receives focus and preserves input values. The five scanned states
  cover both widths and text-spacing settings. Cleanup deletes the disposable
  project, organization and account. The real local API supplies these results.
- The same lifecycle opens New organization through the organization switcher,
  checks its organization-name autocomplete purpose, corrects an invalid custom
  identifier and creates a second organization. Inline feedback receives focus
  while name, description and identifier remain entered after validation.
  The created organization becomes the active context. Three additional states
  are scanned at both widths and text-spacing settings, followed by owned
  organization/account cleanup.
- The organization switcher opens on explicit activation. Tab reaches its
  links, and Escape closes it and returns focus to the switcher. The active
  label now measures at least 8.21:1 at both test widths. Short viewports
  center the popover so the first focused link remains fully visible, including
  at 400% page zoom. See `organization-escape-baseline-results.json`,
  `organization-menu-results.json` and `actual-page-zoom-lineage-org.log`.
- Workspace validation now updates the main landmark and displays focused
  inline feedback. Project-create refusals also focus their inline summary
  after the portfolio update.
  Additional organization creation uses the same targeted update and feedback
  pattern, with native required/name-length constraints.
- Tab/Enter reaches the skip link and moves focus to the content at both widths.
  The Builder fixture covers keyboard inspection of steps and connections.
- The keyboard authoring case adds and names a transform, follows a missing-input
  diagnostic back to the step, connects it through Outputs, and declares a
  contract. It validates and saves the draft through the isolated API fixture,
  then reloads the step and connection. It also checks focus after removal of a
  connection or step and undo of the step removal.
- A selected Builder card previously shared the same settled appearance with
  and without keyboard focus. The card now has an inset focus outline sized
  against the canvas scale. The graph regression checks that the outline
  appears and disappears while selection stays in place. The rendered outline
  measured at least 13.05:1 against the adjacent colors at both widths. At true
  200% and 400% zoom, focused event and transform cards remained in view and
  the outline rendered at least 2 CSS pixels thick. The later draft
  diagnostic regression above moved the outline to the outside edge.
  See `builder-selected-focus-before.json`, `builder-card-focus-results.json` and
  `builder-card-outline-results.json`.
- Scanning the connected draft found step-kind labels under 4.5:1 contrast on
  selected and invalid cards. Those labels now use the stronger shared text color.
- The Builder dialog case checks named proposal review and version conflict
  dialogs. It exercises initial focus, keyboard scrolling in diffs, a failed
  review load followed by a successful retry, and return to the opener after
  Escape or Cancel. Canceling a draft conflict preserves the local edit.
  The case also submits a proposal with the keyboard, returns focus to Propose,
  and checks the submission failure and queued retry in the shared status
  region. Proposal replies are synthetic.
- In the manual merge editor, the baseline lost focus on the selected unit and
  after the first typed character. The updated resolver retains focus and the
  text selection when it refreshes, names the field, and exposes selected
  choices. The regression types and corrects a step name, submits the merged
  draft against the canonical version, focuses the save result, and reloads
  the saved name. Both widths passed against the isolated API fixture.
  At true 200% and 400% zoom, the focused unit, editor and Apply merge button
  fit within the visible viewport. See `manual-merge-baseline-results.json`,
  `builder-manual-merge-results.json` and `actual-page-zoom-merge.log`.
- The node-summary case checks persistence as the pointer crosses from a card
  to its summary, dismissal with Escape, and dismissal after leaving both.
  A summary also remains visible while its node retains keyboard focus.
  The summary now receives pointer events and delays hiding across its 12px
  gap from the card. Escape dismisses it from other focused controls.
- The run-recovery case submits by keyboard and checks focus on inline
  refusal feedback in Attempts & recovery. It covers pruned inputs and a
  missing or malformed protected-environment confirmation. A corrected
  confirmation opens the queued recovery run in the fixture.
- If pipeline execute permission is revoked after the recovery form opens,
  Core returns to Attempts & recovery on the same run and environment.
  The focused refusal explains the permission requirement. Recorded
  acknowledgements and the Steps section remain reachable, and the
  frontend does not call the recovery API. Both recovery journeys passed
  at desktop and 320 CSS px across 16 scans with no axe violations,
  overflow or definite text contrast failures. All 16 contrast review
  samples have dispositions. Actual 200% and 400% Chromium zoom checks
  found the focused refusal visible and unobscured. See
  `run-recovery-permissions-baseline.json`,
  `run-recovery-permissions-results.json`,
  `run-recovery-permissions-review.json` and
  `run-recovery-permissions-zoom-results.json`.
- Incremental run diagnostics now identify each source by heading and
  associate previous, source-window and observed checkpoints with their
  values. Long checkpoint summaries wrap within the card. The keyboard
  journey checks a blocked replay, blocked watermark, redacted cursor,
  missing source diagnostics and a historical run without incremental
  metadata. Desktop and 320 CSS px checks passed, and long values fit
  inside their cards at actual 200% and 400% Chromium zoom with no
  horizontal document overflow. The two final focused executions passed
  eight scans with no axe violations or definite text contrast failures.
  The broader run-delivery and recovery regression passed eight executions
  and 52 scans. See `run-incremental-baseline.json`,
  `run-incremental-review.json`, `run-incremental-regressions-review.json`
  and `run-incremental-zoom-results.json`.
- Applying, resetting and clearing run filters now focuses the updated results
  heading. The previous replacement left the document body focused. The
  run-list regression covers matched and empty results, quick look and return
  focus, and pagination between two distinct records while retaining the filter.
  Both widths passed against the isolated API fixture. Actual 200% and 400%
  zoom probes confirmed the focused headings remained visible. See
  `run-list-focus-baseline.json`, `run-list-regression-results.json` and
  `actual-page-zoom-run-list.log`.
- The live-update case pauses by keyboard while a progress request is pending.
  The pending response cannot replace progress or run evidence, and subsequent
  polls are withheld. Resume observes the newer step. On terminal completion,
  the control stays focused, becomes unavailable and announces that automatic
  updates finished. Unit checks cover progress, completion and connected-run poll targets,
  including late swaps and error feedback. The pause lasts for the current
  document. A full navigation or reload starts normal updates.
- Connected evidence can keep polling after the inspected run has finished.
  Its pending state now exposes the same pause control. A focused desktop/compact
  case pauses it, waits beyond its five-second interval and checks that evidence
  stays unchanged. Resume observes closure and disables the focused control.
  `connected-pause-browser.json` records this run; all 424 JavaScript tests,
  affected Go view tests and JavaScript lint passed after the extension.
- Lineage graph nodes derive their accessible names from visible content.
  Dependency names include the visible dependency label and relationship.
  The running state uses a static clock icon beside its text label.
- The desktop lineage graph has a step list with links to the same evidence
  and dependencies. The list keeps the active link focused across live updates.
  The organization menu opens on activation and closes when focus leaves.
  Escape returns focus to its trigger. The active lineage step, the first
  menu link and the next control after the menu were checked at actual 200%
  and 400% browser zoom.
- The deployment case uses the keyboard to preview and apply a fixture Git
  sync, confirm a protected source binding, compile the selected version and
  deploy it. A change to the Git source after preview focuses the refusal and
  lets the user Tab directly to Preview Git changes. The next preview focuses
  its heading, and the compact results table scrolls with ArrowRight. The
  binding dialog restores focus on Escape. The compiled plan, confirmation
  refusal and deployment success each receive visible focus.
  Both widths passed 24 scans in total. All 32 supplemental contrast samples
  were reviewed. At 200% and 400% browser zoom, the six focused Git and
  deployment outcomes were visible with no document overflow.
  See `git-sync-recovery-results.json`, `git-sync-recovery-contrast-review.json`
  and `actual-page-zoom-git-sync-deploy.log`.
- Compilation checks cover malformed projection and resource scope JSON, a
  compiler permission refusal, and a successful retry. The error receives
  focus, the rejected value stays visible, and the field identifies the
  associated error. Read-only users can inspect a retained plan by digest and
  see which permission is needed to compile or deploy.
  The expanded deployment tests passed four Chromium executions and 40 scans
  at both widths, with zero axe violations or document overflow. All 64
  supplemental contrast samples have recorded dispositions. Actual 200% and
  400% zoom probes checked the projection error, permission refusal, compile
  result and read-only retained plan. See `deployment-access-results.json`,
  `deployment-access-contrast-review.json` and `actual-page-zoom-deployment-access.log`.
- Git sync without update permission and deployment inspection without read
  permission return HTTP 403 with focused feedback and a keyboard-reachable
  return link. An in-place deployment submission after update permission is
  revoked also returns 403 and replaces the stale form with the denial.
  At actual 200% and 400% browser zoom, all three errors were visible and the
  next Tab reached the return link. See `actual-page-zoom-deployment-denied.log`.
- The rollback case selects the previous stored resolution by keyboard and
  preserves it through binding review. Ambiguous selections and a concurrent
  revision conflict receive focus. The next Tab after the conflict reaches
  Review current deployment, which reloads the current revision and retains
  the selected resolution for a fresh confirmation. Both widths passed 16
  scans with zero axe violations or document overflow. All 28 closed-select
  samples flagged for contrast review measured 17.49:1. The selection, errors
  and success had visible focus at actual 200% and 400% browser zoom. The
  deployment used synthetic responses. See `deployment-rollback-results.json`
  and `actual-page-zoom-deployment-rollback.log`.
- A partial Git sync focuses a warning with the result counts. Failed rows
  give stage-specific correction guidance without rendering raw provider
  details. Tab reaches the results table, actions for the synced version, and
  Preview Git changes again. The fixture corrects the failed file in a new
  commit and the retry verifies its successful sync. An absent or unavailable
  deployment version focuses an error followed by a link back to pipeline
  details in the same environment. Both cases add 20 scans across both widths
  and text-spacing settings. The combined deployment run passed 10 executions
  and 84 scans with zero axe violations or document overflow. All 128 flagged
  contrast samples have recorded dispositions. The warning, recovery controls
  and outcomes were checked at actual 200% and 400% zoom. See
  `git-partial-results.json`, `git-partial-contrast-review.json` and
  `actual-page-zoom-git-partial.log`.
- Steps links directly to the recorded execution timeline and sink delivery
  outcomes. The links move keyboard focus to visible targets before users
  need to traverse the graph. The timeline test opens and closes the
  disclosure by keyboard and verifies chronological order across the
  daylight-saving transition, including the displayed offsets and failure
  diagnostic. Sink outcomes retain a named region when evidence is
  unavailable. The fixture covers acknowledgement with rows written,
  unknown delivery with no recorded output location, no-op, failure before
  dispatch, and a sink blocked by a failed dependency. Output metadata uses
  terms and values. Six Chromium executions passed 44 scans with zero
  axe violations or document overflow. All 60 flagged text contrast
  samples have recorded dispositions. The timeline and sampled sink
  headings had visible focus at actual 200% and 400% zoom. The web UI
  exposes recorded events and diagnostics. Service logs are outside the
  web assessment. See `run-delivery-final-results.json`,
  `run-delivery-final-contrast-review.json` and
  `actual-page-zoom-run-delivery.log`.
- The Proof case uses the keyboard to return from empty and denied environments
  to permitted history, open a receipt, expand evidence, inspect provenance,
  and return to history. It checks the captured verdict separately from the
  current verdict when supporting evidence was pruned. Denied history and a
  missing receipt receive focused inline feedback. The missing receipt links
  back to history in the same environment.
- Proof disclosures include the clause heading in their accessible name
  and have a minimum 24 CSS-pixel target. A mixed receipt lets the user
  open the metadata and stale evidence independently by keyboard.
  Clauses without evidence references and receipts without clauses explain
  what was recorded. A superseded receipt retains its identity while
  switching sections. Technical details distinguish the candidate and
  selected versions on a mismatch and expose the scoped deployment link.
  Both widths passed 16 scans with zero axe violations or document overflow.
  All 16 closed-select text samples flagged for review measured 17.49:1.
  Both disclosures stayed visible when focused at actual 200% and 400%
  zoom. See `proof-claims-variants-results.json`,
  `proof-claims-variants-contrast-review.json` and
  `actual-page-zoom-proof-claims.log`.
- A denied receipt opened from older history now keeps the original page
  on the Receipt history link. The 403 page focuses the denial and uses
  the same organization, project and environment checks as a successful
  receipt. The live read-only assessment exposes its recorded time and
  sanitized reference through the keyboard disclosure. Operational
  receipts distinguish available, expired and changed support from the
  original captured verdict and expose the link to the recorded attempt.
  Twelve Chromium desktop and compact test executions passed 84 scans.
  They found no axe violations, document overflow or definite text
  contrast failures. All 84 flagged contrast samples have dispositions.
  The live disclosure, recorded-attempt link, denial and history link
  kept visible focus at actual 200% and 400% page zoom. See
  `proof-evidence-baseline-results.json`, `proof-evidence-final-results.json`,
  `proof-evidence-final-review.json` and `actual-page-zoom-proof-evidence.log`.
- Selecting a runtime target leaves the current page and keyboard focus in
  place. The Switch button applies the choice. The Proof test also opens older
  receipts, follows a stale receipt, returns to the same history page and
  moves back to newer receipts. Both browser widths passed. The selector,
  Switch button and pagination links were checked at actual 200% and 400%
  browser zoom. See `runtime-proof-results.json` and
  `actual-page-zoom-runtime-proof.log`.
- Builder connections made with the pointer commit on release. Releasing
  outside the input cancels a click, and Escape cancels an active drag.
  Both widths passed cancellation, intentional click and drag regressions.
  See `builder-pointer-baseline-results.json` and `builder-pointer-results.json`.
- The combined checkpoint with explicit runtime switching, Proof paging and
  Builder pointer cancellation passed 63 Chromium executions with no skips.
  Its 378 scans reported zero axe violations, document-overflow failures or
  definite supplemental text contrast failures. See
  `full-after-runtime-proof-pointer-summary.json` for the report fingerprint
  and `full-after-runtime-proof-pointer-contrast-continuity.json` for the
  dispositions of its 918 supplemental text samples flagged for review. The
  compact menu's shadow dimmed the exposed Preview label to 4.06:1 with
  increased text spacing. Removing the shadow restored 4.77:1.
  See `menu-exposed-final-results.json`.
- Query source changes apply through the Filter queries button. Focus stays
  on the button after the update, and the deleted-query filter is preserved.
  Active and deleted query links have at least 24px of height. The repository
  source picker describes the branch refresh and keeps focus during the update.
  All four desktop and compact executions passed in the focused fixture,
  with zero axe violations in 12 scans. The source controls and branch picker
  were also checked at actual 200% and 400% browser zoom. Closed select text
  measured 17.49:1 in 28 follow-up samples. See `source-choice-results.json`,
  `source-choice-contrast-review.json` and `actual-page-zoom-source-choice.log`.
- A failed branch request keeps focus on the repository source and reports
  the failure beside Retry branches. The retry and branch controls reference
  the inline feedback. A provider refusal keeps the focused retry button at
  the same position. After a successful retry, the next Tab reaches the
  branch picker. The keyboard test chooses and saves the recovered release
  branch. A saved branch remains identifiable as unverified if discovery
  fails. Four Chromium executions passed 16 scans with zero axe violations
  or overflow. All 44 closed-select samples flagged for review measured
  at least 17.49:1 when rounded. The retry and branch controls stayed
  visible when focused at actual 200% and 400% browser zoom. See
  `branch-retry-final-results.json`, `branch-retry-final-contrast-review.json`
  and `actual-page-zoom-branch-retry.log`.
- The combined source picker and menu checkpoint passed 67 Chromium
  executions with no skips. Its 390 scans reported zero axe violations,
  document-overflow failures or definite supplemental text contrast failures.
  The 945 supplemental review samples have recorded dispositions. See
  `full-after-source-choice-menu-summary.json` and
  `full-after-source-choice-menu-contrast-continuity.json`. The Google signup
  changes were tested separately after that run.
- The seeded Google signup fixture covers the pending and expired fallback states.
  Previously Tab from the expiry alert focused the email Create account
  button. The alert is now followed by Continue with Google, with the same
  return path. Both widths passed. At actual 200% and 400% zoom, the alert and
  retry button were fully visible and keyboard reachable when the page loaded
  at the target zoom. With increased spacing, the pending confirmation
  button now grows to fit the wrapped label, which measured 4.52:1 after
  the sizing fix. The fixture seeded the Core session and checked the retry
  action without contacting Google. See `google-signup-expiry-baseline-results.json`,
  `google-signup-final-results.json` and `google-signup-contrast-review.json`.
- The signed-issuer browser test uses the production OAuth handlers, users
  service and disposable PostgreSQL. It expires the first OAuth state before
  the callback reaches validation. The callback rejects that state before
  exchanging the code. Inline error feedback receives focus and the next Tab
  reaches Back to sign in. A second authorization reaches workspace setup
  with the original destination. The two normal Google entry paths also
  passed using the keyboard. The expired state passed four scans across
  both widths and text-spacing settings with zero axe violations or
  document overflow. The focused alert and retry link stayed fully visible
  at actual 200% and 400% browser zoom. See `google-callback-expiry-results.json`
  and `actual-page-zoom-google-callback-expiry.log`.
- A follow-up on 2026-11-18 passed six Chromium 153.0.8010.12 browser tests
  using the same local issuer. Keyboard users chose 100 minutes from both
  login and signup. Each callback succeeded with 11 minutes deducted from
  its Core state deadline and preserved the original destination. The default
  choice expired under the same simulated delay and could be retried. Handler
  tests verified the exact 10- and 100-minute session deadlines for both routes.
  Twelve axe-core 4.14.1 scans covered 1280 and 320 CSS pixels with normal and
  increased text spacing. They found no violations or document overflow.
  The twelve native placeholder contrast samples were at least 4.92:1 when
  rounded to two decimals. See `google-oauth-time-results.json` and
  `google-oauth-time-evidence.json`. Two additional Chromium fixture tests
  verified that the timing choice appears on retry after pending signup
  expires and that Tab from the error still reaches the default retry first.
  An active pending signup presents its existing confirmation action.
  See `google-signup-pending-current-results.json`.
- The MFA fixture rejected an expired challenge using the API's public
  invalid-token response. The error explains how to get a new code. Keyboard
  users can restart sign-in and finish with the original destination intact.
  Both widths passed, and the alert and retry action stayed visible at actual
  200% and 400% zoom. See `mfa-restart-results.json` and
  `actual-page-zoom-mfa-restart.log`.
- Keyboard probes switched between the 12 default connector create forms,
  then exercised nine authentication-mode forms across Git, HTTP, HubSpot
  and Snowflake. The probes checked visible control names, focus after each
  selection, and normal and increased spacing at both widths. All 84 scans
  reported zero axe violations or document overflow. All 156 closed-select
  text samples flagged for supplemental review measured 17.49:1. The
  probes used synthetic forms without contacting providers or saving a
  connection. See `connector-forms-summary.json` and
  `connector-auth-forms-summary.json`.
- Git SSH setup and credential rotation now mark the private key and
  `known_hosts` entry as required. Persistent instructions describe which
  host entry to paste. The production frontend, backed by synthetic API
  replies, verified keyboard connection testing, creation and rotation.
  Missing `known_hosts` focuses the error and preserves the draft private
  key in the browser. A successful rotation clears the field. Two browser
  executions passed 16 axe scans with no violations or document overflow.
  All 24 flagged closed-select values measured 17.49:1. Two actual zoom
  journeys at 200% and 400% verified visible focus and reflow through both
  error and success states. See `git-ssh-source-baseline.json`,
  `git-ssh-source-updated.json`, `git-ssh-source-review.json` and
  `git-ssh-source-zoom-results.json`. The neighboring HTTP journey missed
  the query path field in one desktop keyboard run and passed at both widths
  on recheck. See `git-ssh-source-http-regression.json` and
  `git-ssh-source-http-regression-recheck.json`.
- Snowflake password creation now hides and disables workload-identity table
  inputs. Switching back before submitting restores the table draft. The Role
  field explains which authentication method requires a dedicated role.
  Keyboard tests used a synthetic API to reject a username, focus the alert,
  preserve the password in the browser, retry and create the source. The
  request check also verified that workload-identity grants were absent from
  the password connection test. Eight axe scans reported zero violations or
  document overflow. All 16 flagged closed-select values measured 17.49:1.
  The error and success feedback stayed visible at actual 200% and 400% zoom.
  See `snowflake-password-baseline.json`, `snowflake-password-updated.json`,
  `snowflake-password-review.json` and `snowflake-password-zoom-results.json`.
- Snowflake workload identity refresh previously dropped keyboard focus
  when the panel was replaced. Retry status now keeps visible focus when
  status moves from unavailable to pending and from pending to ready. A
  local SSR fixture verified those transitions, the Verify button state
  and keyboard inspection of the read-only SQL preview. Both widths
  passed 12 scans with no axe violations or document overflow. All 12
  flagged native select samples measured 17.49:1. The Retry status button
  kept visible, unobscured focus at actual 200% and 400% page zoom. See
  `snowflake-onboarding-baseline.json`, `snowflake-onboarding-results.json`,
  `snowflake-onboarding-review.json` and `snowflake-onboarding-zoom-results.json`.
- Snowflake Verify previously redirected to Overview after a refused
  connection test. Verify now returns to Settings and preserves Back to
  list. The refusal focuses the inline alert. A keyboard retry stays in
  Settings and focuses the success message. Disable identity and Enable
  identity also preserve Settings and focus their confirmation messages.
  Those messages and the Verify feedback remained visible and unobscured
  at actual 200% and 400% Chromium zoom. Two synthetic
  journeys passed 28 scans at desktop and 320 CSS px, with normal and
  increased spacing. There were zero axe violations, document overflow
  or definite text contrast failures. All 28 flagged contrast samples
  have dispositions. See `snowflake-verify-baseline.json`,
  `snowflake-verify-results.json`, `snowflake-verify-review.json` and
  `snowflake-verify-zoom-results.json`.
- The BigQuery authorization fixture exercised a missing dataset, a refused
  authorization start, canceled consent and successful consent after a
  keyboard retry from Settings. The missing-dataset error now identifies and
  focuses the field. The authorization result focuses a visible message
  even when the inventory has a saved scroll position. The pending status
  badge now expands with its text. On the compact selected card with
  increased spacing its rendered text contrast rose from 2.14:1 to 5.20:1,
  with the text inside the border. The field and the authorization messages
  kept visible, unobscured focus at actual 200% and 400% Chromium zoom.
  Both Chromium widths passed 16 scans
  with no axe violations, document overflow or definite text contrast
  failures. All 24 flagged contrast samples have dispositions. See
  `bigquery-oauth-results.json`, `bigquery-oauth-before-scroll-fix.json`,
  `bigquery-oauth-current-results.json`, `bigquery-oauth-review.json`,
  `bigquery-oauth-glyphs-baseline.json`, `bigquery-oauth-glyphs-results.json`
  and `bigquery-oauth-zoom-results.json`.
- In the Workspace query editor, the final authorized-files page previously
  hid the focused Next page button and dropped focus. The file selector now
  receives focus if the last page has files and the button still owns
  focus. The status reports how many files were returned. Keyboard users
  recovered from a refused discovery request, paged to the last result,
  selected a worksheet, and reopened the saved query with its selection
  intact. Both Chromium widths passed 12 scans without axe violations,
  document overflow or definite text contrast failures. All 48 flagged
  contrast samples have dispositions. The selector received visible and
  unobscured focus at actual 200% and 400% browser zoom. See
  `workspace-discovery-results.json`, `workspace-discovery-current-results.json`,
  `workspace-discovery-review.json` and `workspace-discovery-zoom-results.json`.
  The fixture used synthetic file and worksheet responses.
- The Workspace Picker fixture reproduced lost focus after cancellation,
  refused verification and successful verification. Core now returns focus
  to Select files with Google when the Picker closes and no other control
  has focus. A regression test also checks that Core preserves focus when
  a user has already moved to Browse authorized files. The browser
  fixture verified that a refused request leaves the target unchanged and
  that a successful request loads the verified worksheet. Both widths
  passed 12 scans with no axe violations, document overflow or definite
  text contrast failures. All 40 flagged contrast samples have dispositions.
  Return focus stayed visible at actual 200% and 400% Chromium zoom.
  See `workspace-picker-focus-baseline-results.json`,
  `workspace-picker-focus-current-results.json`, `workspace-picker-focus-review.json`
  and `workspace-picker-focus-zoom-results.json`. The fixture simulated
  Google Picker and the authorization response.
- The HubSpot OAuth fixture used the production Core frontend with synthetic
  consent and API replies. Keyboard journeys corrected a missing name, retried
  a refused start, and recovered after expiry, a refused retry, and canceled
  consent. Successful authorization opened the saved source. Canceling a
  reconnection left the active grant available. The write-enabled access mode
  was retained throughout. Two Chromium executions passed at desktop and
  320 CSS px, with 28 scans, zero axe violations, zero document overflow,
  zero definite text contrast failures and dispositions for all 44 flagged
  samples. Two additional probes checked visible, unobscured feedback focus
  at actual 200% and 400% zoom. See `hubspot-oauth-results.json`,
  `hubspot-oauth-review.json`, `hubspot-oauth-zoom-results.json`
  and `hubspot-oauth-evidence-manifest.json`.
- Six Chromium executions verified the alternate callback window for HubSpot,
  BigQuery and Workspace source creation and reconnection at desktop and 320
  CSS px. Across 44 scans there were zero axe violations, document overflow
  or definite text contrast failures. All 56 flagged samples have
  dispositions. In BigQuery and Workspace, the new checkbox boundaries
  measured at least 3.75:1 and the focus outlines at least 15.34:1 after the
  Workspace controls were stacked. The credential-service test verified
  acceptance after the default limit and expiry after the selected limit.
  Six separate probes passed at actual 200% and 400% zoom. HubSpot probes
  checked error and result focus. BigQuery and Workspace probes checked
  the visible, unobscured checkbox focus in creation and settings.
  See `provider-oauth-timing-results.json`, `provider-oauth-timing-review.json`,
  `google-source-oauth-timing-results.json`,
  `google-source-oauth-timing-review.json`, both `-zoom-results.json` reports
  and `provider-oauth-timing-evidence-manifest.json`.
- The HubSpot private-app fixture verified keyboard recovery from a refused
  connection test, saved a write-enabled setting, then exercised cancel,
  refusal and retry during credential rotation. The Access mode control
  now has a short name and separate visible permission guidance in create
  and settings. The access selector and both refusal messages kept visible,
  unobscured focus at actual 200% and 400% Chromium zoom. Both widths passed
  20 scans with no axe violations, document overflow or definite text
  contrast failures. All 36 flagged contrast samples have dispositions.
  See `hubspot-private-baseline-results.json`,
  `hubspot-private-current-results.json`, `hubspot-private-review.json` and
  `hubspot-private-zoom-results.json`. The fixture used synthetic tokens
  and responses.
- Attaching a shared organization GitLab connection previously sent users
  to a plain error page on refusal and to Overview on success. Settings
  and Back to list now keep their context. A refusal focuses the accessible
  error and a successful retry focuses the confirmation. The expired-session
  redirect uses Settings as the login return target. Both Chromium widths
  passed eight scans with no axe violations or document overflow. All 182
  text contrast samples passed supplemental checks. Both messages kept
  visible, unobscured focus at actual 200% and 400% zoom. See
  `gitlab-connection-baseline-results.json`,
  `gitlab-connection-current-results.json`, `gitlab-connection-zoom-results.json`
  and `gitlab-connection-evidence.json`. The API responses were synthetic.
- Git integrations now focuses authorization and disconnect errors and
  confirmations. A refused authorization start keeps the selected write
  option in the form after an HTMX submission. The keyboard
  journeys exercised a refused start, simulated consent refusal and success,
  and cancellation, refusal and retry of disconnection. Organization section
  links wrap on compact screens, identify the current page and were fully
  visible as focus moved through them. The Connect label fits inside its
  button with increased text spacing, with a lowest measured contrast
  bound of 4.52:1 at 320 CSS pixels.
  Twelve test executions covered GitLab and adjacent Proof flows across both
  widths, with 44 scans and zero axe violations, document overflow or definite text contrast
  failures. All 44 native select reviews have dispositions. Focus on the
  five GitLab feedback states remained visible at actual 200% and 400% zoom.
  See `gitlab-actions-verified-results.json`, `gitlab-actions-verified-review.json`,
  `gitlab-actions-contrast-verified-results.json` and
  `gitlab-actions-zoom-verified-results.json`.
- The GitHub App page now focuses authorization and disconnect feedback.
  An invalid account name returns focus to the field, and a refused
  existing-installation authorization keeps the entered account name
  in the form for correction. Retrying with a valid account reached the
  synthetic authorization page. A refused disconnect retained the
  repository and retry action. Both errors retained visible focus at actual
  200% and 400% zoom. The Connect button label fits at desktop and compact
  widths and measured at least 4.58:1 in the tested states. See
  `github-connection-zoom-results.json` and
  `github-action-contrast-final-results.json`.
- GitHub App install, existing-install authorization, and reconnect;
  GitLab organization consent and retry; and HTTP browser OAuth use the
  same optional 100-minute Core window. The HTTP retry kept this choice
  after a refused request. The GitHub test completed a stored authorization
  after 11 minutes and verified the extended callback cookie and deadline
  in the frontend and service tests. The 12 keyboard executions produced 44
  scans with no axe violations, overflow or definite text contrast
  failures. All 40 flagged samples have dispositions. Boundaries of the new
  checkboxes measured 3.91:1 and their keyboard focus outlines measured
  15.96:1 or higher. Four separate probes passed at actual 200% and
  400% zoom. The existing HTTP browser tests passed for both grant types.
  See `git-organization-timing-results.json`, `http-oauth-timing-results.json`,
  `git-http-oauth-timing-zoom-results.json` and
  `git-http-oauth-timing-evidence-manifest.json`.
- The GitHub and GitLab project repository probes declined provider consent,
  resumed the saved target by keyboard, and selected the longer callback
  window. Both retained the time choice when Core refused to start the next
  authorization. The project now focuses inline feedback after a declined or
  refused attempt. Removing the managed form's scroll restoration keeps that
  focus visible. Ten Chromium executions passed with 32 scans and no axe
  violations, document overflow or definite text contrast failures. All 68
  flagged samples have dispositions. Four additional probes at actual 200%
  and 400% zoom verified visible, unobscured checkbox and error focus. Five
  existing browser regressions passed for repository creation and callbacks.
  See `repository-oauth-timing-results.json`,
  `repository-oauth-timing-review.json`,
  `repository-oauth-timing-zoom-results.json`,
  `repository-oauth-timing-product-results.json` and
  `repository-oauth-timing-evidence-manifest.json`. The new checkbox boundary
  and focus outline both passed the 3:1 threshold at desktop and compact
  widths. See `repository-oauth-timing-indicators-results.json`.
- The GitHub expiry regression cleared both return cookies before the
  synthetic callback. Core returned to the saved project, focused the expiry
  alert, and let the user resume that target with a fresh authorization.
  Backend tests verify that wrong users, consumed or unknown state, denied
  project access, and missing setups receive no project recovery link. The
  expiry path leaves the intent unused and makes no provider call. Six
  Chromium executions passed with 16 scans and no axe violations, document
  overflow or definite text contrast failures. All 16 flagged samples have
  dispositions. Two probes verified visible, unobscured focus after expiry
  and refusal at actual 200% and 400% zoom. See `github-expiry-recovery-results.json`,
  `github-expiry-recovery-review.json`, `github-expiry-recovery-zoom-results.json`
  and `github-expiry-recovery-evidence-manifest.json`.
- The unchecked checkbox boundary in GitLab and Proof settings measured
  1.82:1 against the form surface. The shared checkbox style now measures
  3.91:1 in those states. The focus outline measured 15.97:1 before and after
  checking the box by keyboard. The focused regressions cover desktop and
  320 CSS pixels. The [W3C non-text contrast criterion](https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html)
  uses a 3:1 threshold for visual information needed to identify controls or
  their states. The combined focused run passed 20 browser tests and 52
  axe scans, with no violations, document overflow or definite text
  contrast failures. The 48 supplemental review items have dispositions.
  See `checkbox-contrast-baseline-results.json`, `github-checkbox-final-results.json`
  and `github-checkbox-final-review.json`.
- The combined checkpoint through the pipeline access-denied changes passed
  75 Chromium executions with no skips and 446 scans with no axe violations
  or document overflow. The 1,044 supplemental text samples flagged for
  review have recorded dispositions. The MFA restart fix and connector
  form probes were tested separately. See
  `full-after-deployment-denied-summary.json` and
  `full-after-deployment-denied-contrast-continuity.json`.
- If the pending Google signup expires before confirmation is submitted, the
  HTTP handler returns to signup with the original destination. A Go handler
  test verifies the redirect, and Chromium tests verify focus on the retry
  message and the next Tab to Continue with Google after that redirect.
- Builder exposes the active Preview, Split or Source mode as a pressed
  button. Text and background colors change together when the mode changes.
  Source and Split passed eight scans at normal and increased spacing.
  Focus outline samples covered all three buttons in both their selected
  and unselected states at both widths, with a minimum of 4.73:1. The source
  editor's previous outer ring was clipped. Its inset replacement measured
  5.28:1. The keyboard test also cleared the source, observed the empty-source
  status, and reapplied the original source while keeping focus on the Apply
  button. Actual 200% and 400% zoom probes checked the mode controls and editor.
  See `builder-view-mode-results.json`, `builder-view-mode-contrast-review.json`,
  `builder-mode-focus-results.json`, `builder-source-focus-results.json` and
  `actual-page-zoom-builder-view-source.log`.
- A malformed YAML import leaves the source and focus on Apply To Canvas.
  The source editor now references the feedback, which gives a correction
  hint for a rejected request. The keyboard test corrects the YAML, applies
  the renamed step, saves the draft and verifies the persisted step can
  still receive focus after reload. Both widths passed 16 scans with zero
  axe violations or document overflow. All 36 supplemental text samples
  have recorded dispositions. At actual 200% and 400% zoom, the feedback
  fit in the viewport beside the focused button. See
  `builder-source-import-results.json`, `builder-source-import-contrast-review.json`
  and `actual-page-zoom-builder-source-import.log`.
- The compact matrix also found undersized mapping controls after canvas
  scaling and a contract table without keyboard scrolling. Mapping controls now
  retain a minimum 24px target with room for both buttons. Contract field tables
  are named focusable regions; the compact test verifies ArrowRight scrolling.
- The baseline found a non-focusable skip target, an undersized project link,
  and Builder names that omitted visible node/mapping text. These were fixed
  and verified in the passing run. Named generic containers also received group
  roles after inspection of axe's manual-review results.
- Treating fully transparent DaisyUI text shadows as invisible exposed a
  compact-navigation contrast failure in the organization avatar initial.
  Its blue text measured at most 4.41:1 against a required 4.5:1. The initial
  now uses the shared base-content color.
- Text-spacing assessment found that the compact auth branding row forced a
  340px document at a 320px viewport. Wrapping that shared row fixes all three
  authentication entry pages. Their skip targets are now focusable, with
  Tab/Enter assertions on sign-in, signup and password recovery.
- Focused recovery handler and view tests passed after template regeneration.
  Focused Builder and authentication view tests and JavaScript lint passed.
  All 422 Vitest tests passed. Three earlier fixture journeys also passed,
  covering preflight rejection, compact navigation Escape and focus return,
  and retention of run context after refused recovery.
  Nine related run-inspection journeys passed after adding live-update
  controls and correcting lineage names, including a 22-step graph, completion,
  live polling, historical failure context and retained selection.

Brave Side 156.1.98.63 on macOS 27.0.1 used actual page zoom on the local sign-in
page. The document fit the 864 × 456 CSS pixel viewport at 200% and the
432 × 228 viewport at 400%. At 400%, Tab showed the skip link and its focus
outline in the viewport. Enter navigated to `#main-content`. This check covers the
sign-in page at those viewport sizes. The automated Chromium profile covers
320 CSS pixels.

The same Brave Side version verified a fixture run refusal at actual 400%
zoom (432 × 228 CSS pixels). Tab reached Recover sink delivery and Enter
submitted the fixture request. The refusal received focus with a visible,
unobscured outline, and the document fit the viewport width.

At 400% zoom (432 × 242 CSS pixels), the protected binding dialog brought the
confirmation input into view with a visible focus outline. Escape closed the
dialog and returned focus to Bind. The fixture page fit the viewport width.

At the same zoom and viewport, the missing Proof receipt message remained
fully visible with a focus outline. Tab and Enter on Receipt history returned
to the permitted history for the same environment. The error page fit the
viewport width.

The Builder composition fixture was checked in Brave Side 156.1.98.63 on
macOS 27.0.1 at actual 400% page zoom, confirmed by the browser toolbar. The
viewport was 432 × 228 CSS pixels and the document width was 432px.
Tab reached the normalize step, Enter opened its properties, and Tab reached
Step name and Add outgoing link with visible, unobscured focus outlines.
The graph auto-fit to 38% canvas scale. This check covers access to the properties
and dependency controls at that zoom, with the graph itself retaining a
two-dimensional layout.

Contrast checks are incomplete in all 262 scans, largely because axe cannot
determine gradient backgrounds. Supplemental measurement passed 9,272 samples
and flagged 482 samples for review across the scanned states. Those include 23
samples whose contrast bounds cross the threshold, 149 without usable sampled
pixels, 306 native-control cases and four opacity cases.

The native rendering diagnostics cover 264 selects and 42 inputs. All sampled
controls are visible and active, with opaque `#080e15` backgrounds and no shadow,
stroke, filter, blending, opacity or transform effects. Value text is `#eef4fa`,
giving 17.49:1 contrast. Thirty samples show translucent placeholders at 4.92:1;
a conservative conversion allowance still gives at least 4.76:1. Select text
uses clipped ellipsis and 28px end padding, which separates it from the 4px
chevron gradients. Selected-option and control text colors agree.
`native-text-contrast-review.json` retains each sampled state and the assertions.
This uses user-agent colors as described by
[Understanding Contrast (Minimum)](https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum.html).
The dispositions cover these sampled closed values/placeholders, not all picker,
hover, boundary or focus states. Raw automated statuses remain review.

The four opacity cases concern one project-validation detail paragraph. A focused
probe confirmed 80% opacity on a transparent text-only leaf, with no shadows,
strokes, filters, blending or ancestor opacity. The sampled background and
foreground match the canonical samples. A conservative opacity calculation gives
at least 9.38:1 in all four viewport/spacing states, above the required 4.5:1.
`project-opacity-review.json` retains the rendering assertions and calculation.
This resolves those four samples without changing the analyzer's general opacity
review rule.

A later diagnostic run checked the formerly clipped text in 14 affected browser
executions. All passed, including badge containment, and all original text-node
identities and workflow state were preserved. After the Builder label fix, none
of the rechecks needed to scroll a hidden-overflow axis: 136 samples met text
contrast after scrolling, 12 were deliberately clipped screen-reader-only labels,
and one exposed structural diff received a visual review in Brave Side at
1280 × 720 with increased text spacing. Complete diff lines used the dark
interior; keyboard scrolling exposed the final line and Escape returned focus to
Propose. The focus outline explains mixed bounds at a partially clipped line.
`clipped-text-dispositions.json` and `manual-exposed-diff-review.json` retain the
scoped results. These supplement the raw report and do not establish a
criterion-wide pass.

`contrast-review-queue.json` maps all 482 canonical raw review samples to scoped
dispositions: 306 native closed values/placeholders, four opacity cases, 23
previously reviewed mixed bounds, 136 exact samples rechecked after scrolling,
12 deliberately hidden labels and one exposed diff reviewed visually. The
cross-run mapping normalizes only the generated synthetic organization identifier;
state, viewport, selector, text-node ordinal and raw sample must otherwise match.
There are no unclassified text samples in this checkpoint. This does not resolve
component boundaries, every focus state or criterion-wide contrast acceptance.
The full report includes the MFA states without adding focused scans again.

Manual contrast inspection in Brave Side at 100% page zoom covered the settled
unavailable-proposal title at 1280 × 720 and 320 × 900 CSS pixels. Its light
text (`#eef4fa`) sits on an opaque `#111923` panel, giving 15.97:1 contrast.
The proposal diff at both widths and the conflict diff at 320 × 900 showed
light glyphs on a uniform dark background during keyboard scrolling. Their
pale focus outlines and scrollbars can enter the sampled text rectangles.
These observations match five of the 23 crossing-bound samples and cover normal
styles. Automated Chromium and Brave both read the foreground as `#eef4fa`.
A follow-up checked the unavailable-proposal title under increased spacing at
both widths and the compact conflict diff under increased spacing. Their
visible glyphs remained on uniform dark backgrounds, covering three more samples.
The remaining 15 samples concern dashboard text behind the open compact menu.
At 320 × 900, the settled opaque menu covered those glyphs under normal and
increased spacing. The mixed samples include menu pixels over occluded targets.
All 23 crossing-bound samples now have scoped manual dispositions. This does
not resolve the other contrast review reasons or establish a criterion-wide pass.
`manual-contrast-review.json` retains observations and fingerprints under
`.worktree/accessibility/`; `manual-navigation-and-spacing-review.json` maps all
23 samples to their dispositions. Raw review counts remain unchanged.
The latest full-suite report contains those same 23 sample records. The four
opacity samples also retain the same foreground and measured bounds. The authored theme changes affect the field border and focus outline. Removing
those changes reproduces the opacity review's source hash. `badge-review-continuity.json`
records the current comparison and points to the retained earlier reports.

The rebuilt run produced `label-content-name-mismatch` review items in 66
Builder and lineage scans. The four selectors represent the two zoom
functions in each surface. Their visible + and − symbols express those
functions, and their accessible names “Zoom in” and “Zoom out” match them.
This resolves the name review using W3C's guidance on symbolic text
characters. [Understanding Label in Name](https://www.w3.org/WAI/WCAG22/Understanding/label-in-name#symbolic-text-characters)
Representative assistive-technology operation remains untested.
Do not interpret these incomplete checks as passes.

The local report and `input-manifest.json` under `.worktree/accessibility/`
retain scan results and source fingerprints. The original failing report is
under `baseline/`. The earlier passing run is in `pre-recovery.json`.
`builder-selected-contrast-baseline.json` records the Builder label failures.
`builder-dialogs-unnamed-baseline.json` and `builder-dialogs-contrast-baseline.json`
record the dialog failures. `run-recovery-unfocused-baseline.json` records
the refusal focus gap before the inline feedback fix.

`deployment-unnamed-dialog-baseline.json` records the protected binding dialog
before it had an accessible name.
`proof-denied-unfocused-baseline.json` records the original Proof focus gap.
`source-test-status-baseline.json` records the missing connection status.
`source-query-result-baseline.json` records the compact result's keyboard
scrolling violation.
`source-disabled-navigation-baseline.json` records the unsupported labels on
disabled navigation entries.
`workspace-validation-baseline.json` records the missing workspace error
feedback before the targeted update and inline focus fix.
`builder-hover-unreachable-baseline.json` records the node summary that could
not receive hover.
`organization-avatar-contrast-baseline.json` records the avatar contrast
failure after transparent shadows became measurable.
`live-lineage-name-baseline.json` records the lineage name mismatch found
when scanning live and terminal step states.
`lineage-stages-role-baseline.json` records the named stages container before
it received group semantics.
`organization-input-purpose-baseline.json` records the additional organization
form's missing organization-name autocomplete purpose.

The process and criterion tables above list the remaining coverage. Native
password generation, connector-specific settings and query paths outside the
recorded fixtures, additional graph and run states, and deployment and Proof
permission combinations need their own evidence. The recorded zoom and contrast
results cover the controls used in each probe. Screen-reader testing remains
deferred. Hosted CI and provider acceptance are outside this run.

During the MFA checks, `npm audit` returned a nonzero exit status and reported
eight high-severity package entries in the frontend development dependency chain.
The raw report is `mfa-npm-audit.json` under `.worktree/accessibility/`.
Dependencies were unchanged; advisory applicability and remediation remain unassessed.
