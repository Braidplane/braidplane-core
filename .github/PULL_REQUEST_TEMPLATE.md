## Change

Describe the concrete problem and resulting behavior.

Before merge, the reviewer selects exactly one release label: `release:patch`
for compatible fixes, `release:minor` for features, `release:breaking` for
incompatible changes (a minor bump before v1), or `release:none` for changes
without a distributed behavior change. Add `release:security` only for an
approved public security release. After successful main CI, labels prepare a
v0 RC prerelease and publish it automatically after CI and artifact checks.
Stable releases require an explicit promotion dispatch. Breaking
changes follow the same path and are marked explicitly in the release notes.

## AI assistance

If AI helped substantially with this contribution, name the tool and describe
its role. Otherwise, remove this section.

## How were the changes tested?

Describe how you tested the changes. Include commands and results, and explain
any relevant checks you did not run.

- [ ] Source and generated output are synchronized where applicable.
- [ ] Tenant scope, permissions and package boundaries remain intact.
- [ ] Runtime/browser claims are supported by runtime evidence.
