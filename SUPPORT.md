# Community support and maintenance

## Ask for help

Use [GitHub Issues](https://github.com/Braidplane/braidplane-core/issues/new/choose)
for bugs, questions and feature or connector proposals. Search existing issues
first. Include the exact revision, platform, deployment layout, connector and
provider version, expected result and a synthetic reproduction. Strip credentials,
tenant data and private URLs. Use [SECURITY.md](SECURITY.md) for vulnerabilities.

kerak19 maintains this repository. Community help and review are best
effort, with no guaranteed response, resolution, uptime or SLA. There is no
separate community forum or public roadmap requirement. Maintainers explain
triage decisions in the public issue and synchronize internal work as needed.

Paid onboarding, upgrade assistance or troubleshooting require a separate
agreement covering scope, supported versions, responsibilities and any response
commitments. Contact [contact@braidplane.com](mailto:contact@braidplane.com) to
discuss it. Existing coverage follows your signed agreement.

## Maintenance and compatibility

This is Alpha source, with no qualified supported release or production
installation matrix yet. [Local setup](COMMUNITY_BUILD.md) and container build
commands describe development inputs, not production support promises. Use the
[Alpha API policy](docs-public/api/alpha.md) and [changelog](CHANGELOG.md): keep
application and CLI versions aligned, expect breaking changes, and preserve the
database and original encryption keys before upgrades. Migration rollback needs
a tested restore, not an application downgrade.

[Connector classifications](docs-public/connectors/support-levels.md) identify
preview and experimental combinations and their evidence. No connector is
currently classified production-ready. Provider permissions and operation
support vary; a connection test does not qualify every operation. Request
additional official support through a GitHub proposal with the exact combination.
Custom forks and unlisted combinations carry no compatibility or maintenance
promise; reproduce on unmodified source for upstream triage.

Release acceptance requires an exact source revision, platform/artifact manifest,
rights and notice review, distribution and upgrade instructions, and independent
installation/workflow evidence. Until those exist, there is no promised release
date or historical-version/backport window. Maintainers review and validate
changes before selecting a release; publication requires separate authorization.

## Conduct

Be respectful, discuss the work, and avoid harassment, threats, discrimination,
spam and disclosure of private information. This applies to issues, reviews and
other project participation. Maintainers may edit or remove harmful content,
lock a discussion or restrict participation, explaining the reason when safe.

For public moderation needs, flag the thread without repeating harmful content.
For sensitive conduct concerns, request a private conversation at
[contact@braidplane.com](mailto:contact@braidplane.com); include only the minimum
needed context. kerak19 handles moderation. There is currently no separate
appeal reviewer or backup moderator; a concern involving the maintainer has no
independent project escalation route. Response is best effort.
