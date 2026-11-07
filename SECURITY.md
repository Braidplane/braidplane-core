# Security policy

## Report privately

Email [contact@braidplane.com](mailto:contact@braidplane.com) with the subject
“Braidplane security report”. Do not put suspected vulnerabilities, credentials
or exploit details in public issues or pull requests. GitHub private reporting
has not been verified; use email for now.

Include the affected revision/version, component, impact, prerequisites and
minimal synthetic reproduction. Remove tokens, personal information, tenant data
and production logs. Start with a short description; agree a safe transfer method
before sharing sensitive material. Test only systems you own or may assess.

kerak19 is responsible for triage and coordination. Reporting is best
effort, with no guaranteed response time, bounty or round-the-clock monitoring.
If you receive no reply, follow up on the same email thread without posting the
vulnerability publicly. No independent backup responder is currently designated.
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
