# HTTP Layer Guide

This guide applies inside `transport/http/` and supplements the root
`AGENTS.md`.

## Boundaries

- Keep handlers thin: parse and validate requests, enforce permission checks,
  call services, and map results to HTTP responses. Business logic stays in
  services and SSR rendering stays in the frontend layer.
- Start API behavior changes in `api-schema/schema.yml` before editing handlers.
- Parse user-provided slugs with `slug.New(...)` before calling services.
- Enforce protected HTTP operations through `authorization.Service.Can(...)`
  with typed `model.Action` and `model.Resource` values. Do not query permission
  tables directly.
- Reuse existing request parsers, error mappers, and response helpers. Preserve
  route semantics, active organization context, and redirect safety unless the
  task intentionally changes their contract.

## Validation

Run `task generate-api` when the schema changes. Select affected handler, CLI
contract, and browser-flow tests from
`doc/QUALITY_GATES.md`.
