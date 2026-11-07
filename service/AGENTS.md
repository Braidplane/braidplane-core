# Service Layer Guide

This guide applies inside `service/` and supplements the root `AGENTS.md`.

## Boundaries

- Services own business logic, non-slug input validation, and orchestration. Do
  not move HTTP parsing, response mapping, or rendering into this layer.
- Services accept already-validated external identifiers and never expose
  database IDs or UUIDs as API identities.
- Services may use functional options (`ServiceOption`, `WithX`) to install
  optional dependencies or modules. Define the behavior when each option is
  omitted and when it is supplied; core should not need a placeholder service
  for a module it does not use.
- Constructors or composition must validate dependencies required by enabled
  features. Optional modules must not bypass tenant isolation, authorization or
  other requirements of protected operations. Test the supported configurations
  with and without the option, and use fakes for external dependencies.
- Use `postgres.TransactionManager` for atomic units of work and separate
  one-query repository calls inside them. Do not add `MethodWithTx`, `...WithTx`,
  or similar service APIs to pass transactions between services.
- Permission checks normally stay in the HTTP layer. The service exceptions are
  pipeline validation history and queued proposal-publish reauthorization
  (`doc/PROPOSAL_AUTHORITY.md`). Other exceptions
  require an accepted architecture change.
- Wrap dependency failures with the existing typed `xerror` constructors and
  reuse existing services and `internal/model` contracts before adding abstractions.

## Validation

For behavior changes, add the smallest meaningful service coverage, including
relevant failure paths. Dependency-backed coverage belongs in the corresponding
`e2e` package. Use `doc/QUALITY_GATES.md` to select the remaining gates.
