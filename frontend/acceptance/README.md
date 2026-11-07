# Integrated local acceptance

This explicit owner-run supplement checks the approved dashboard/entity acceptance matrix with production SSR, seeded local data and deterministic API fixtures. It is separate from the routine Alpha CI inventory and does not change its case or time budgets.

From the repository root:

```sh
PLAYWRIGHT_E2E_EVIDENCE=1 BRAIDPLANE_BROWSER_PROFILE=full task test-e2e
BRAIDPLANE_BROWSER_PROFILE=full task test-e2e -- --config=playwright.acceptance.config.cjs
```

The first command runs the retained full product profile. The supplement runs seven journeys across Chromium, Firefox and WebKit: dashboard widths/disclosures/no-project; JavaScript-disabled entity round trips; Proof empty/restricted/missing/mismatch/configuration; explicit Back scroll; repeatable moderation states; schedule configuration/history/Back; and a newly seeded empty workspace. Authentication lifecycle is intentionally Chromium-only under the repository throttle policy, so two non-Chromium fresh-workspace cases are skipped. These skips are not acceptance passes.

Reports are `.worktree/playwright/product.json` and `.worktree/playwright/acceptance.json`; supplemental screenshots/traces are in `.worktree/playwright/acceptance-results/`. Archive reports before rerunning. No retries are configured. Fixture replies and automated durations are not hosted/provider qualification or moderated participant results.

The original supplement reproduced scroll resetting from 913px to 0px in all engines, and long organization names widening the 320px dashboard in Firefox/WebKit. Keep those assertions as regression checks. The scroll test starts from a direct catalog link, not only from an already selected quick look.

See [the browser validation guide](../e2e/README.md) for the current core fixture
and test commands. Historical moderation plans and deployment operations belong
to the consumer distribution; they are not runnable core procedures.
