# PostgreSQL Layer Guide

This guide applies inside `postgres/` and supplements the root `AGENTS.md`.

## Boundaries

- Keep this layer limited to persistence, SQLC-backed query execution, and
  database error mapping. Services own business transaction boundaries and
  multi-step orchestration.
- Standard CRUD repository methods receive `postgres.DBTX` per call and do not
  store transaction handles. `postgres/checkpoints` is the existing adapter
  exception because it implements the runtime `TimelineStore` interface without
  a DB parameter; do not generalize that shape without an architecture change.
- New repository methods should wrap one SQLC query call. When changing an
  existing multi-query method, do not expand its orchestration; move that work
  into a service-owned `TransactionManager` unit when the task can do so safely.
- Do not add repository or service-style `...WithTx` variants. Context-carried
  `RunInTx` and `RunRead` calls already reuse the active transaction.
- Use named SQL parameters with `sqlc.arg('name')`.
- Organization-scope all tenant-owned data. Public or pre-tenant tables require
  an explicitly documented boundary and must not grant tenant access.
- Prefer simple schemas and full update semantics. Do not add compatibility
  shims, hidden partial updates, database enums, or checks without a concrete
  data-integrity reason.
- Add the next numbered forward-only `.up.sql` migration and update its catalog
  manifest as described in `doc/QUALITY_GATES.md`. Never edit migrations applied
  in a shared or non-local environment. An unpublished migration applied only to
  the task's disposable local database may evolve; rebuild that database and
  rerun migrations before validation. Never edit `postgres/dbgen/` manually.

## Validation

Run `task generate-sqlc` for query or schema work; it generates and vets queries
against the explicitly configured disposable core-only database. Select the
remaining affected tests and gates from `doc/QUALITY_GATES.md`.
