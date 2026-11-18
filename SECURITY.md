# Security policy

The [OWASP ASVS verification matrix](doc/OWASP_ASVS.md) tracks release evidence for Community Core.

## Report privately

Email [contact@braidplane.com](mailto:contact@braidplane.com) with the subject
“Braidplane security report”. Do not put suspected vulnerabilities, credentials
or exploit details in public issues or pull requests. You can also use
[GitHub private vulnerability reporting](https://github.com/Braidplane/braidplane-core/security/advisories/new)
for this repository. Both routes are handled by the maintainer.

Include the affected revision/version, component, impact, prerequisites and
minimal synthetic reproduction. Remove tokens, personal information, tenant data
and production logs. Start with a short description; agree a safe transfer method
before sharing sensitive material. Test only systems you own or may assess.

kerak19 is responsible for triage and coordination. Reporting is best
effort, with no guaranteed response time, bounty or round-the-clock monitoring.
If you receive no reply to an email report, follow up in the same thread. For a
GitHub report, email [contact@braidplane.com](mailto:contact@braidplane.com) with a
link to the private report. Do not post the vulnerability publicly.
No independent backup responder is currently designated.
Before an absence, the maintainer must arrange a named responder with mailbox
access or state that intake is unattended; do not assume continuous coverage.

## Scope and supported versions

Reports may concern the Community backend, frontend, CLI, API, connectors,
pipeline execution, authentication, authorization or bundled dependencies.
For upstream defects, include how Braidplane is affected so maintainers can
coordinate a dependency update. Custom forks should identify their changes and,
where possible, reproduce against the unmodified revision.

The source is Alpha and CHANGELOG.md is unreleased. There is currently no
published supported-version or security-backport window. Report against an
exact revision; do not infer historical-version maintenance. A future release
must name its supported versions and upgrade instructions before claiming support.
See [maintenance](SUPPORT.md#maintenance-and-compatibility).

## Handling a report

The maintainer confirms affected revisions and impact, reproduces privately,
and agrees disclosure timing with the reporter. Develop and test the fix in
private, then prepare the patch release, upgrade or mitigation instructions and
security advisory. Coordinate a CVE when appropriate. Publish affected/fixed
versions and credit the reporter only with their agreement. Avoid disclosing
exploit details before a mitigation is available. No fixed disclosure deadline
or guaranteed patch date is promised.

The maintainer monitors contact@braidplane.com for security reports. Mailbox
delivery has not yet been tested as part of release qualification.

## Dependency updates

The Core maintainer owns dependency triage and the release review. Check security
advisories weekly and before publishing or promoting a candidate. Review and apply
compatible routine updates at least every 90 calendar days. For an incompatible
update or an end-of-support dependency, record a migration or removal plan with
an owner and review date during that cycle.

Assess each advisory against the installed version, supported platform, enabled
features and exposure. Use vendor severity as a starting point and record any
change in priority with evidence. Count calendar days from the earliest public
advisory or private confirmation that identifies the affected version. Later
inventory or triage does not restart the clock.

| Risk in Core | Remediation target |
| --- | --- |
| Known exploitation that applies to the deployment | Contain within 72 hours and remediate within 7 days |
| Critical | 7 days |
| High | 30 days |
| Medium | 90 days |
| Low | 180 days |

Remediation means shipping an updated component, removing it, or verifying that
the affected functionality is disabled in the supported deployment. Record any
temporary mitigation, missing upstream fix or review exception with the affected
artifact, rationale, owner, approving maintainer and expiry date. Reassess when
exploit evidence or a fix becomes available. An exception leaves the original
deadline and any overdue status visible.

Review the exact release artifacts and their installed inventories before
approval. Include linked Go modules and standard library, shipped browser assets,
operating-system packages and installer images. Retain the source revision, image
digests and platform, SBOM or inventory, dated advisory results, and disposition
of every applicable advisory. Record unresolved matches or gaps as unverified.
Resolve overdue items or document an explicit maintainer exception before
approval. Recheck advisory results at promotion against the same digests.
