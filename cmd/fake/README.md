# Development fixture seeder

`task fake-seed` seeds synthetic identities, organizations, projects and
connector/query/pipeline fixtures through core services. Use only an authorized
isolated localhost database and the documented local connector fixtures.
The command prints synthetic login credentials and one API key for that local
fixture; keep that output out of normal logs and handoffs.

`--core-only` seeds identities/organizations/projects without connectors.
`--git-only` prepares local Git fixtures; `--git-fixture-state` selects the
base/deleted/restored pipeline state. `--schedule-fire-seed` consumes the existing
JSON schedule-fire fixture payload on stdin. These modes are mutually exclusive.
Backend config flags retain normal TOML/environment/flag precedence.

The shared parser/runner lives in `internal/fake/command`; this entrypoint has no
private demo profile. Use `task fake-seed -- --help` to inspect flags without connecting.
