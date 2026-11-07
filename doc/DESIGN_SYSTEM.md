# Community product design

This guide carries the shared product rules into the canonical core. Production
UI uses server-rendered Go/Templ, HTMX, DaisyUI and Tailwind. Existing components,
CSS and rendered behavior define the implementation; private design archives or
agent skills are not prerequisites for contributions.

## Theme and components

The authenticated shell uses `data-theme="control-room"`. Theme colors, font
stacks and geometry live in
[app-custom.css](../frontend/assets/css/app-custom.css); shared form primitives
live in [app.tailwind.css](../frontend/assets/css/app.tailwind.css).
[package-lock.json](../frontend/package-lock.json) owns frontend versions.
Run `task build-css` after style changes.

Reuse DaisyUI variables and existing Templ components before adding local
classes or tokens. Use `btn-primary` for the main commit action, `btn-outline`
for secondary actions and `btn-ghost` for utility actions. Use compact badges
for metadata and semantic colors for actual status. Keep protection/risk
indicators separate from environment identity or health.

Operational forms use compact labels, `input-sm` and `select-sm`. Tables remain
dense with contained horizontal overflow. Long slugs, paths and connection
values must wrap or scroll within their region. Set readable text colors for
warning, empty and error states in the dark shell.

## Responsive policy

Desktop and laptop workflows are primary. Preserve the compact-screen fallback
for the shared shell, popups and forms. Local controls must remain reachable,
readable and keyboard accessible; do not invent a second mobile interaction
model unless the task calls for it. Check both desktop/laptop and compact widths
against rendered output.

## Shell and navigation

Reuse `AuthenticatedShell` and its shared components. Active navigation uses
`aria-current="page"`. Keep primary navigation and the signed-in account in the
sidebar; compact navigation uses the accessible topbar menu. Collapsed navigation
retains distinct icons and the account affordance.

Preserve active organization, project, runtime target and safe return paths.
The topbar owns organization and runtime target switching; dashboard scope
controls own dashboard target selection. Do not duplicate shell context in body
headings, badges or breadcrumbs. A breadcrumb adds navigable parent context,
not another copy of the current route title.

Runtime-changing links and forms carry explicit target parameters and name the
target in visible copy. Use shared URL helpers and `url.Values` to preserve
filters and encode query parameters. Keep environment identity colors separate
from permission and destructive-action warnings.

## Inventories and detail workspaces

Use shared entity presentation components: a selectable list, compact read-only
quick look and separate full workspace. Keep create actions discoverable before
and after selection. Show counts once. Quick looks summarize the selected record;
they must not embed the entire editable detail page.

Selection uses canonical routes, stable HTMX regions and shareable URLs. Local
selection, filtering and form swaps preserve document scroll with
`data-preserve-scroll` and `focus-scroll:false`; avoid body swaps for ordinary
list operations. Let the document own vertical scrolling instead of nesting
independent scrolling detail panes. Use native disclosures for advanced content.

## SSR form behavior

Forms remain server-rendered. Small JavaScript helpers may track dirty state,
selected connector types, parameter rows and tabs. Server validation and
authorization remain authoritative.

- Persisted edits use `data-dirty-form` and `data-dirty-submit`; save is visibly
  disabled until an editable value changes, including after HTMX swaps.
- Connector-specific hidden sections remain disabled so inactive controls cannot
  submit stale values. Pre-save actions preserve the selected type and non-secret
  values in the same form region.
- Never render stored or draft credentials into HTML. Draft secrets may remain
  client-side for a pre-save connection test, but responses and persisted edit
  forms render only non-secret identity and configuration.
- Every mutation has a real POST method and action. Alternate actions mirror
  `hx-post` in `formaction`. Native required/types/bounds help users; handlers
  validate both plain and HTMX submissions before persistence or redirects.
- On failure, preserve submitted non-secret values and keep the failed panel
  open. Use `data-dirty-unsaved` when retry must remain possible without another
  edit. Only API-marked sanitized public error detail may appear in form alerts.
- Use `FormError` and `FormFieldError` with unique IDs and existing help IDs.
  Associate invalid controls through ARIA and focus the first invalid control or
  request summary. Use `data-form-feedback` for focus restoration after swaps;
  unrelated updates must not steal focus.
- Swap the form or its containing region on validation errors. Use
  `setHTMLResponseStatus` for plain error status and swappable HTMX HTML.
  Successful redirects use `redirectHTMXOrSeeOther`, or show explicit inline
  success for actions that remain on the page.
- Include `FormRequestError` for retryable transport/session failures. Shared
  request handling restores each submitter's previous disabled state after
  completion, errors, timeout or network failure.
- Simple destructive actions use `DestructiveConfirmation` and mirror its copy
  in `hx-confirm` and form-level `data-destructive-confirm`, preserving keyboard
  and plain-submit protection. Existing high-impact actions retain their typed
  confirmation modal and explicit affected slug.

## Icons and validation

Use the canonical marks in `brand/icons/` served under `/assets/icons/`.
Transparent marks are visible logos; opaque tiles are app icons/favicons.
Use existing provider assets and their reuse policies; never redraw vendor
marks. Decorative inline SVG uses `aria-hidden="true"`.

Follow [quality gates](QUALITY_GATES.md) and the
[browser coverage policy](../frontend/e2e/README.md#community-coverage-policy).
Regenerate Templ from source and inspect relevant rendered desktop and compact
states. Capture useful masked evidence without credentials. HTML reports are
appropriate for substantial redesigns or when requested, not every small edit.
