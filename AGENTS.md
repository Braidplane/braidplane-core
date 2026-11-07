# Braidplane core

This repository owns Community core and the public CLI. Core changes belong
here.

Use rg and the pinned bash scripts/go-toolchain.sh wrapper. Keep Go output exact.
Use doc/PACKAGE_BOUNDARIES.md for layer changes and the matching scoped guide.
Preserve tenant scoping, HTTP authorization, typed xerror, immutable applied
migrations, and source-plus-generated-output changes. Tests must use synthetic
fixtures. Never commit credentials or runtime data.

The Go module is github.com/Braidplane/braidplane-core. Shared packages live
under pkg/.

## Start here

- Read `CONTRIBUTING.md` for contribution rules, `COMMUNITY_BUILD.md` for
  setup and `doc/QUALITY_GATES.md` for checks matched to the change.
- `Taskfile.yml` lists build, test, lint and generation commands. Go and Node
  tasks use pinned toolchain wrappers; the CLI lives in `cli/` within the root Go module.
- Check for a more specific `AGENTS.md` in the directories being changed.
  `doc/PACKAGE_BOUNDARIES.md` maps ownership and permitted dependencies.
  For pipeline changes, start with `pkg/pipeline/AGENTS.md`, including changes
  in services, the CLI and pipeline UI.
- `task worktree-create name=my-feature` creates a sibling checkout and branch
  from the current HEAD, or reuses an existing local branch. It does not start
  services or copy local configuration. See `COMMUNITY_BUILD.md` for local setup.
- For tests or acceptance that need the local application, run `task dev` and
  follow `COMMUNITY_BUILD.md#run-locally` for ports, retained data and keys.
  Use distinct Compose project names and ports for concurrent worktrees.
- For generated code, change the source and run the appropriate generator;
  review and include its output. Use disposable core-only databases for
  migration and SQL generation checks.

Complete requested local changes and relevant checks without repeated approval
requests. Reuse passing evidence when its inputs are unchanged. Source
publication, deployment, secret/provider changes and merges need their own task
authority. Preserve unrelated work.
