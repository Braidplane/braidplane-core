# OWASP ASVS verification matrix

This matrix tracks the 70 requirements in OWASP ASVS 5.0.0 Level 1 for Braidplane Community Core in its documented self-hosted configuration. All rows start as **Pending** for release verification. The local Level 1 assessment is tracked in [#159](https://github.com/Braidplane/braidplane-core/issues/159). Level 2 is tracked in [#168](https://github.com/Braidplane/braidplane-core/issues/168). [OWASP ASVS releases](https://github.com/OWASP/ASVS/releases)

## Release evidence

The Core maintainer updates this matrix when reviewing a release. Use the following statuses.

| Status | Meaning |
| --- | --- |
| Pending | Release verification is open. |
| Verified | The requirement passed for the named release and configuration. |
| Not applicable | The named configuration lacks the feature or condition addressed by the requirement. |

For each Verified or Not applicable row, link to a sanitized release record or a public check that supports the result. Record the release tag, source revision, installer digest, platform, configuration, and verification date in the linked evidence. Review which rows a change affects before carrying their results forward. Reopen affected rows until the new evidence is ready.

Keep findings and sensitive test material in the private assessment record. Use the reporting channels in the [security policy](../SECURITY.md). A Level 1 conformance claim requires passing evidence for every applicable requirement and a documented reason for each Not applicable row.

## Level 1 requirements

| Requirement | Area | Release status | Evidence |
| --- | --- | --- | --- |
| `V1.2.1` | Encoding and sanitization | Pending | — |
| `V1.2.2` | Encoding and sanitization | Pending | — |
| `V1.2.3` | Encoding and sanitization | Pending | — |
| `V1.2.4` | Encoding and sanitization | Pending | — |
| `V1.2.5` | Encoding and sanitization | Pending | — |
| `V1.3.1` | Encoding and sanitization | Pending | — |
| `V1.3.2` | Encoding and sanitization | Pending | — |
| `V1.5.1` | Encoding and sanitization | Pending | — |
| `V2.1.1` | Validation and business logic | Pending | — |
| `V2.2.1` | Validation and business logic | Pending | — |
| `V2.2.2` | Validation and business logic | Pending | — |
| `V2.3.1` | Validation and business logic | Pending | — |
| `V3.2.1` | Web security | Pending | — |
| `V3.2.2` | Web security | Pending | — |
| `V3.3.1` | Web security | Pending | — |
| `V3.4.1` | Web security | Pending | — |
| `V3.4.2` | Web security | Pending | — |
| `V3.5.1` | Web security | Pending | — |
| `V3.5.2` | Web security | Pending | — |
| `V3.5.3` | Web security | Pending | — |
| `V4.1.1` | APIs and web services | Pending | — |
| `V4.4.1` | APIs and web services | Pending | — |
| `V5.2.1` | File handling | Pending | — |
| `V5.2.2` | File handling | Pending | — |
| `V5.3.1` | File handling | Pending | — |
| `V5.3.2` | File handling | Pending | — |
| `V6.1.1` | Authentication | Pending | — |
| `V6.2.1` | Authentication | Pending | — |
| `V6.2.2` | Authentication | Pending | — |
| `V6.2.3` | Authentication | Pending | — |
| `V6.2.4` | Authentication | Pending | — |
| `V6.2.5` | Authentication | Pending | — |
| `V6.2.6` | Authentication | Pending | — |
| `V6.2.7` | Authentication | Pending | — |
| `V6.2.8` | Authentication | Pending | — |
| `V6.3.1` | Authentication | Pending | — |
| `V6.3.2` | Authentication | Pending | — |
| `V6.4.1` | Authentication | Pending | — |
| `V6.4.2` | Authentication | Pending | — |
| `V7.2.1` | Session management | Pending | — |
| `V7.2.2` | Session management | Pending | — |
| `V7.2.3` | Session management | Pending | — |
| `V7.2.4` | Session management | Pending | — |
| `V7.4.1` | Session management | Pending | — |
| `V7.4.2` | Session management | Pending | — |
| `V8.1.1` | Authorization | Pending | — |
| `V8.2.1` | Authorization | Pending | — |
| `V8.2.2` | Authorization | Pending | — |
| `V8.3.1` | Authorization | Pending | — |
| `V9.1.1` | Self-contained tokens | Pending | — |
| `V9.1.2` | Self-contained tokens | Pending | — |
| `V9.1.3` | Self-contained tokens | Pending | — |
| `V9.2.1` | Self-contained tokens | Pending | — |
| `V10.4.1` | OAuth and OpenID Connect | Pending | — |
| `V10.4.2` | OAuth and OpenID Connect | Pending | — |
| `V10.4.3` | OAuth and OpenID Connect | Pending | — |
| `V10.4.4` | OAuth and OpenID Connect | Pending | — |
| `V10.4.5` | OAuth and OpenID Connect | Pending | — |
| `V11.3.1` | Cryptography | Pending | — |
| `V11.3.2` | Cryptography | Pending | — |
| `V11.4.1` | Cryptography | Pending | — |
| `V12.1.1` | Secure communication | Pending | — |
| `V12.2.1` | Secure communication | Pending | — |
| `V12.2.2` | Secure communication | Pending | — |
| `V13.4.1` | Configuration | Pending | — |
| `V14.2.1` | Data protection | Pending | — |
| `V14.3.1` | Data protection | Pending | — |
| `V15.1.1` | Secure coding and architecture | Pending | — |
| `V15.2.1` | Secure coding and architecture | Pending | — |
| `V15.3.1` | Secure coding and architecture | Pending | — |
