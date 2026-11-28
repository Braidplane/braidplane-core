---
name: braidplane-design
description: Use EGM to plan and review Braidplane changes before implementation.
---

# Braidplane design review

Before implementation, check the proposed approach against the issue, owning
code, and observable acceptance criteria. A routine change needs a short
preflight: state the expected behavior, assumptions and check for a regression.
For a substantial design, such as a change spanning components or altering an
execution or authorization boundary, use the full Elephant–Goldfish Model
(EGM) workflow before coding.

Use the EGM revision pinned in the [project marketplace](../../plugins/marketplace.json).
Install it through Codex Plugins when needed, or register the same upstream
revision with `codex plugin marketplace add google/elephant-goldfish-model --ref 46933754e63c0424daa9c270d7e0734245d79560`
and `codex plugin add egm@egm`. Use its discovered skills in a fresh session.
Use `egm-brainstorm` when the concept is unsettled,
`egm-draft` to turn it into a design and implementation plan, and `egm-review`
for the full independent review. The bundle includes the personas and rubrics.
Follow the applicable repository instructions for source, tools, and approvals.

`egm-draft` keeps its interactive section approvals. For unattended work,
draft from the agreed issue scope and flag decisions that still need owner
approval. Run the full `egm-review` in either case before implementation.

For Goldfish stages, use native reviewers that do not inherit parent turns.
Give them the design, persona, rubric and selected repository sources. Run
reviewers directly when nesting is unavailable. Verify their completion and
resolve material findings before handoff. Report any skipped or blocked stage.
A same-session pass can improve the design but does not count as a Goldfish
review. Record the outcome on the owning issue when authorized.
