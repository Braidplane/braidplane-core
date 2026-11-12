# Documentation contract

Run `task docs-check` from a checkout or the exact exported source bundle.
CI calls the same `scripts/docs-check.sh` implementation. Python 3.12 and public
locked wheels are required; Git metadata and private tooling are not.

## Writing for users

Lead with what Braidplane does and the task the reader wants to complete. Use
direct instructions, concrete examples and expected results. Include warnings
where they change an action, such as preserving encryption keys or checking a
destination before retrying a write. Keep support and compatibility policies in
their reference guides, and release qualification checklists in engineering
guides. Avoid repeating those policies as disclaimers throughout the README and
tutorials.

## Navigation and isolation

`mkdocs.public.yml` serves only `docs-public/`. Every Markdown page there must
appear once in its navigation. Local links and downloads must remain within
that root. Engineering, operator, security and architecture guides live under
`doc/` and appear once in [the engineering index](index.md). Add future ADRs
there too. No ADR directory is currently present in core; private distribution
documentation stays with its owner rather than being copied into this site.

`docker/docs/inventory.json` lists every contextual Markdown file outside those
roots, with a reason for exclusion from site navigation. These files still have
their local links and Markdown anchors checked, including README, CONTRIBUTING,
SECURITY, SUPPORT, CHANGELOG and source-adjacent fixture/contributor guidance.
Missing files, stale exclusions, unclassified Markdown, duplicate navigation
targets and orphaned pages fail validation.

## Generated and archived material

The inventory's ignored directory names cover Git metadata, virtual environments,
dependency trees, caches and build output. They must not contain authored active
documentation. There are no archived documentation exclusions today. Retain
future authored archives with an explicit contextual inventory entry and reason;
do not silently exclude a new active directory. Generated non-Markdown examples,
images and downloads are checked when linked. Source code is not parsed as prose.

## Links and release evidence

The gate renders Markdown with the public site's extensions before checking
inline, reference, image and HTML links. Code examples are not links. Targets
must exist inside the checked source tree; Markdown fragments must match rendered IDs.
The OpenAPI build placeholder uses the same reserved staging URL as MkDocs.
Strict MkDocs warnings also reject missing anchors and unlisted pages.

This is deterministic local validation. Remote HTTP availability, GitHub intake,
public downloads and absence of private-access requirements need the separate
post-public acceptance receipt. Verify hosted CI against the exact source
revision and workflow run. Do not infer those results from a passing local build.

For a release, run this command inside its exact retained source/bundle, including
the entrypoints, changelog and selected version's install/upgrade/recovery inputs.
Keep version instructions in immutable tagged Git content or retained bundles;
never substitute the latest guide for the selected release. Release packaging
must supply those inputs before their independent walkthrough can pass. This
gate cannot establish retention or qualify a distribution that has not shipped.
