# WCAG 2.2 AA release verification matrix

This matrix tracks the 55 Level A and AA success criteria for Braidplane
Community Core’s web interface. The local assessment for
[#160](https://github.com/Braidplane/braidplane-core/issues/160) covers
authentication, project and source setup, pipeline authoring and validation,
deployment, run inspection and recovery, and Proof. See the
[process inventory](../frontend/accessibility/README.md#intended-process-scope)
and [WCAG 2.2](https://www.w3.org/TR/WCAG22/).

## Release evidence

All rows start as **Pending** for release verification. The
[local assessment](../frontend/accessibility/README.md) and
[workflow regression CSV](../frontend/accessibility/matrix.csv) record the
states tested and their results. Screen-reader testing was deferred for #160.

| Status | Meaning |
| --- | --- |
| Pending | Release evidence is incomplete. |
| Verified | Evidence supports the criterion across its applicable pages and complete processes in the named release scope. |
| Failed | Evidence shows that an applicable page or process fails the criterion. |
| Not applicable | The scoped release has no content or behavior to which the criterion applies. Record why. |

Link each decision to evidence that records the release tag, source revision,
configuration, verification date, pages and complete processes tested, and
browser and assistive technology versions where used. Reopen affected rows
when a change invalidates their evidence. A WCAG 2.2 AA conformance claim
requires verification of every applicable criterion across the claimed scope,
with reasons for each inapplicable criterion. A pass on one workflow check
provides evidence for that state.

## Level A and AA criteria

| Criterion | Level | Area | Release status | Evidence |
| --- | --- | --- | --- | --- |
| `1.1.1` | A | Text alternatives | Pending | — |
| `1.2.1` | A | Time-based media | Pending | — |
| `1.2.2` | A | Time-based media | Pending | — |
| `1.2.3` | A | Time-based media | Pending | — |
| `1.2.4` | AA | Time-based media | Pending | — |
| `1.2.5` | AA | Time-based media | Pending | — |
| `1.3.1` | A | Adaptable content | Pending | — |
| `1.3.2` | A | Adaptable content | Pending | — |
| `1.3.3` | A | Adaptable content | Pending | — |
| `1.3.4` | AA | Adaptable content | Pending | — |
| `1.3.5` | AA | Adaptable content | Pending | — |
| `1.4.1` | A | Distinguishable content | Pending | — |
| `1.4.2` | A | Distinguishable content | Pending | — |
| `1.4.3` | AA | Distinguishable content | Pending | — |
| `1.4.4` | AA | Distinguishable content | Pending | — |
| `1.4.5` | AA | Distinguishable content | Pending | — |
| `1.4.10` | AA | Distinguishable content | Pending | — |
| `1.4.11` | AA | Distinguishable content | Pending | — |
| `1.4.12` | AA | Distinguishable content | Pending | — |
| `1.4.13` | AA | Distinguishable content | Pending | — |
| `2.1.1` | A | Keyboard access | Pending | — |
| `2.1.2` | A | Keyboard access | Pending | — |
| `2.1.4` | A | Keyboard access | Pending | — |
| `2.2.1` | A | Timing | Pending | — |
| `2.2.2` | A | Timing | Pending | — |
| `2.3.1` | A | Flashing | Pending | — |
| `2.4.1` | A | Navigation and focus | Pending | — |
| `2.4.2` | A | Navigation and focus | Pending | — |
| `2.4.3` | A | Navigation and focus | Pending | — |
| `2.4.4` | A | Navigation and focus | Pending | — |
| `2.4.5` | AA | Navigation and focus | Pending | — |
| `2.4.6` | AA | Navigation and focus | Pending | — |
| `2.4.7` | AA | Navigation and focus | Pending | — |
| `2.4.11` | AA | Navigation and focus | Pending | — |
| `2.5.1` | A | Input methods | Pending | — |
| `2.5.2` | A | Input methods | Pending | — |
| `2.5.3` | A | Input methods | Pending | — |
| `2.5.4` | A | Input methods | Pending | — |
| `2.5.7` | AA | Input methods | Pending | — |
| `2.5.8` | AA | Input methods | Pending | — |
| `3.1.1` | A | Language | Pending | — |
| `3.1.2` | AA | Language | Pending | — |
| `3.2.1` | A | Predictable behavior | Pending | — |
| `3.2.2` | A | Predictable behavior | Pending | — |
| `3.2.3` | AA | Predictable behavior | Pending | — |
| `3.2.4` | AA | Predictable behavior | Pending | — |
| `3.2.6` | A | Predictable behavior | Pending | — |
| `3.3.1` | A | Input assistance | Pending | — |
| `3.3.2` | A | Input assistance | Pending | — |
| `3.3.3` | AA | Input assistance | Pending | — |
| `3.3.4` | AA | Input assistance | Pending | — |
| `3.3.7` | A | Input assistance | Pending | — |
| `3.3.8` | AA | Input assistance | Pending | — |
| `4.1.2` | A | Assistive technology compatibility | Pending | — |
| `4.1.3` | AA | Assistive technology compatibility | Pending | — |
