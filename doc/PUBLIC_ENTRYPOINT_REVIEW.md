# Public entrypoint preparation review

## Reference projects

Reviewed on 4 November 2026. These are structural references; Braidplane's
contacts, rights decision and support capacity come from its own owner.
No upstream support promise or policy text is adopted wholesale.

- [Kestra contribution guide](https://github.com/kestra-io/kestra/blob/develop/.github/CONTRIBUTING.md):
  separate bug/proposal intake, private security route, rights confirmation and setup guidance.
- [Go contribution guide](https://github.com/golang/go/blob/master/CONTRIBUTING.md):
  bug reports identify version, operating system, steps, expected and actual behavior.
- [Prefect security policy](https://github.com/PrefectHQ/prefect/blob/main/SECURITY.md):
  explicit version coverage, private intake and coordinated patch/advisory handling.

## Candidate review

README links build/setup, the existing synthetic PostgreSQL-to-S3 example,
contribution, security, support, compatibility, changelog and license guidance.
CONTRIBUTING documents GitHub intake, focused checks, generation, review and
Apache-2.0 incoming contributions without a separate CLA or DCO sign-off.
The owner selected contact@braidplane.com for initial private security reporting
and confirmed the incoming contribution rule during preparation. The owner also
confirmed responsibility for monitoring security and conduct reports there.
SUPPORT identifies the maintainer, review limits, conduct and custom-fork policy.
Issue forms collect synthetic reproduction or proposal context; blank issues
remain available for questions. Existing PR guidance already requests the problem,
behavior, checks, generated output and security-boundary impact.

Read-only GitHub API checks on 4 November found Issues enabled and the core
repository private. The private-vulnerability-reporting endpoint returned 404;
that does not prove a usable reporting control. Email is the selected route.
No messages were sent and no repository settings were changed.

## Value-free routing rehearsal

Prepare a draft addressed to contact@braidplane.com with subject “Braidplane
security report — routing check” and body “Synthetic routing check; no
vulnerability or sensitive data”. Verify the recipient and named responder.
Sending requires separate authorization; confirm receipt privately afterward.
No message has been sent, so delivery remains unverified.

The paid-support draft now carries the issue's verified owner-approved policy
intake: business calendar, response/action-plan/update deadlines, incident
criteria, version baseline and responsibilities. These remain conditional on
reviewed terms and qualified operations, rather than an active offer.

## Local validation

The ten changed Markdown files passed a check of 78 relative targets, including
Markdown anchors.
All three issue-template YAML files parsed successfully with Ruby's YAML parser;
both forms contain three required text areas and private-security routing.
The repository's hash-pinned Python 3.12 documentation environment built
`mkdocs.public.yml` successfully with `--strict`, using staging and reserved
example.test site/OpenAPI URLs as in CI. The generated docs homepage contains
the contribution, support, issue and private-reporting links.
`git diff --check` passed. These checks validate local targets, anchors, YAML
syntax and documentation generation, not external URL availability, rendered
GitHub forms or runtime behavior.
The five notice-generator unit tests passed after replacing first-party attribution
with the owner-selected GitHub identity, kerak19. A repository documentation
search, including hidden paths, found no remaining full-name mentions.
No application procedure changed, so runtime suites were not repeated.
Read-only issue checks found installation packaging and independent qualification
still in Backlog; their missing acceptance cannot be inferred from these docs.

## Outstanding acceptance

- Arrange an absence responder. No backup or independent conduct appeal handler
  is currently designated; the owner confirmed current mailbox responsibility.
- With separate authorization, send the value-free routing check described above and confirm receipt. No delivery test was performed here.
- Verify forms, question intake and private reporting links from an outside
  account against the exact published candidate. Private API readback is not
  public submission evidence. Qualify contribution execution and branch controls
  before admitting untrusted CI workloads.
- Supply the supported installation bundle, version/platform matrix, retained
  upgrade instructions and independent synthetic workflow evidence. Existing
  developer setup and tutorial assets do not satisfy installation acceptance.
- Match the candidate to final release, connector, compatibility, rights and
  artifact-verification evidence.
- Run the final candidate documentation/navigation gate and independent
  no-private-access walkthrough after those inputs are available.

This preparation is not release acceptance or publication authorization.
