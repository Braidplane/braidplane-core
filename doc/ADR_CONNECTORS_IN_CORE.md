# ADR: Keep connector adapters in Core

**Status** Accepted · [Core issue #19](https://github.com/Braidplane/braidplane-core/issues/19)

The Core registry, capability catalog, provider adapters and typed Proof
contracts evolve with the Core execution runtime. Core can build and test
those packages from its own checkout with the pinned toolchain and public
dependencies. Separate packaging would introduce another release and support
boundary without an identified consumer requiring independent versions.

Keep the adapters, client packages, factory and registry in
`github.com/Braidplane/braidplane-core`. Core maintainers own changes,
compatibility, verification, support and versioning as part of the Core
release. The [connector inventory](CONNECTOR_PACKAGE_INVENTORY.md) identifies
the in-tree provider code and its direct SDK or protocol dependencies.

In-tree connector code ships under the repository's [Apache-2.0 license](../LICENSE)
and [notice](../NOTICE). Resolve imported Go SDKs and dependencies from
the pinned [go.mod](../go.mod) and [go.sum](../go.sum).
The [third-party notices](../THIRD_PARTY_NOTICES.txt) preserve dependency
attributions, including their own licenses. Git and OpenSSH used by the
container follow its [source and notice delivery](RELEASE_RIGHTS.md) process.
Core maintainers review provenance when connector code or dependencies change
and keep release notices aligned with what ships.

Revisit independent packaging when a concrete consumer needs independent
releases and an identified maintainer can own compatibility, provenance and
support for that distribution.
