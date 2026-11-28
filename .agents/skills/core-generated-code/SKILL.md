---
name: core-generated-code
description: Change or review Braidplane Core's generated API, CLI, SQLC, Templ, enum, or logging code from its owning source.
---

# Core generated code

Work from the Core repository root. First identify the owning input and
the consumers whose contracts change. For changes to the public API, read
the [contribution rules](../../../CONTRIBUTING.md#implementation-and-checks).
Use the [quality gates](../../../doc/QUALITY_GATES.md) to select checks.

| Change | Regenerate and inspect |
| --- | --- |
| Public API | Edit `api-schema/schema.yml` first and run `task generate-api`. Review server and frontend client types plus the CLI schema and client under `cli/api/`. Exercise affected handlers and run `task test-cli` when the CLI contract changes. |
| SQL queries or schema | Run `task generate-sqlc` with `DATABASE_URL` set to an owned, disposable Core-only database. The task generates and vets queries. Review generated query signatures and row types with the affected persistence callers. |
| Templ | Edit `.templ` sources and run `task generate-templ`. Check the affected views and handlers. |
| Enums and logging wrappers | Find the owning `go:generate` directive and run it with `bash scripts/go-toolchain.sh generate` for that package. Check the affected callers. |

Review the generated diff before adapting consumers. If the output is
unexpected, correct the owning input and regenerate. Keep intended source and
generated changes together and flag any generation or checks that could not run.
