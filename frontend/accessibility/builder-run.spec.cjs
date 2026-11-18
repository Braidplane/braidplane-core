const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("Builder run feedback stays until the user opens run details", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/incremental`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        await page.clock.install();
        let failed = true;
        await page.route("**/pipelines/daily-sync/execute?**", (route) =>
            route.fulfill({
                status: failed ? 422 : 202,
                json: {
                    metadata: { run_slug: "backfill-ui-001", status: failed ? "failed" : "queued" },
                    ...(failed
                        ? {
                              timeline: {
                                  failure: {
                                      kind: "data_source",
                                      side: "source",
                                      data_source_slug: "warehouse",
                                      error: "Connection refused",
                                      action_hint: "Check the data-source binding.",
                                  },
                              },
                          }
                        : {}),
                },
            }),
        );
        const run = page.getByRole("button", { name: "Run in dev", exact: true });
        const result = page.locator("#builder-run-feedback");
        await tabTo(page, run);
        await page.keyboard.press("Enter");
        const failure = result.getByRole("alert");
        await expect(failure).toContainText("Data source error");
        await expect(failure).toBeFocused();
        await page.clock.fastForward(6000);
        await expect(page).toHaveURL(/\/builder\?/);
        await expect(failure).toBeFocused();
        await page.keyboard.press("Tab");
        const details = result.getByText("Show details", { exact: true });
        await expect(details).toBeFocused();
        await page.keyboard.press("Space");
        await expect(result.getByText(/Check the data-source binding/)).toBeVisible();
        await assess(page, testInfo, "builder-run-refused");

        failed = false;
        await tabTo(page, run);
        await page.keyboard.press("Enter");
        const accepted = result.getByRole("status");
        await expect(accepted).toContainText("Run requested in dev");
        await expect(accepted).toBeFocused();
        await expect(failure).toHaveCount(0);
        await page.clock.fastForward(6000);
        await expect(page).toHaveURL(/\/builder\?/);
        await assess(page, testInfo, "builder-run-accepted");
        await page.keyboard.press("Tab");
        await expect(result.getByRole("link", { name: "Open run details" })).toBeFocused();
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/\/runs\/backfill-ui-001\?environment_slug=dev$/);
    });
});
