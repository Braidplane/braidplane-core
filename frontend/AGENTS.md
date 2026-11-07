# Frontend Layer Guide

This guide applies inside `frontend/` and supplements the root `AGENTS.md`.

## Boundaries

- The frontend is server-rendered Go and Templ with HTMX, Tailwind, and DaisyUI.
  A client framework or hydration architecture requires an accepted architecture
  change, not only a feature-level request.
- Use `doc/DESIGN_SYSTEM.md` as the visual source of truth and inspect existing
  components before changing design or UX.
- Reuse existing Templ components, partials, route patterns, and design tokens.
  Preserve active organization and shell semantics.
- Design dense workflows for desktop and laptop first while preserving the
  documented compact-screen fallback. Keep loading, empty, success, and error
  states explicit, and keep copy concise and operational.

## Validation

Run `task generate-templ` when `.templ` sources change. Use
`doc/QUALITY_GATES.md` for affected tests. Prefer Vitest for UI logic and
Playwright for critical paths, following [the Community coverage policy](e2e/README.md#community-coverage-policy).
Use manual inspection when requested or when automated checks cannot verify the
change. HTML review reports are reserved for explicit requests.
