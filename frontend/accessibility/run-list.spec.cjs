const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { tabTo, visit, assess } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("run list filters, preview and pagination by keyboard", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const runs = `${fixture.url}/orgs/acme/projects/ops/runs?environment_slug=dev`;
        await visit(page, runs);
        const status = page.getByRole("combobox", { name: "Status", exact: true });
        const apply = page.getByRole("button", { name: "Apply", exact: true });
        const reset = page.getByRole("link", { name: "Reset", exact: true });
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        const results = page.locator("#run-results-heading");
        await expect(results).toHaveText("No project runs yet");
        await tabTo(page, status);
        await page.keyboard.press("f");
        await activate(apply);
        await expect(results).toHaveText("No runs match these filters");
        await expect(results).toBeFocused();
        await assess(page, testInfo, "run-list-no-matches");
        await activate(page.getByRole("link", { name: "Clear filters", exact: true }));
        await expect(results).toHaveText("No project runs yet");
        await expect(results).toBeFocused();

        expect((await page.request.post(`${fixture.url}/_test/run/failed`)).ok()).toBe(true);
        await visit(page, runs);
        await expect(results).toHaveText("Recent runs");
        await tabTo(page, status);
        await page.keyboard.press("f");
        await expect(status).toHaveValue("failed");
        await activate(apply);
        await expect(results).toHaveText("Failed runs");
        await expect(page).toHaveURL(/[?&]status=failed/);
        await expect(results).toBeFocused();
        await assess(page, testInfo, "run-list-filtered");
        const run = page.getByRole("link", { name: "Quick look: run-inspection", exact: true });
        await activate(run);
        await expect(page.locator("#run-preview-heading")).toBeFocused();
        await expect(run).toHaveAttribute("aria-current", "true");
        await expect(page).toHaveURL(/[?&]run_slug=run-inspection/);
        await assess(page, testInfo, "run-list-preview");
        await activate(
            page
                .getByRole("region", { name: "daily-sync", exact: true })
                .getByRole("link", { name: "Close", exact: true }),
        );
        await expect(run).toBeFocused();
        await expect(run).toHaveAttribute("aria-current", "false");

        await activate(page.getByRole("link", { name: "Next", exact: true }));
        await expect(page).toHaveURL(/[?&]cursor=page-2/);
        await expect(page.getByRole("link", { name: "Quick look: run-older", exact: true })).toBeVisible();
        await expect(page.getByRole("button", { name: "Next", exact: true })).toBeDisabled();
        await expect(results).toHaveText("Failed runs");
        await activate(page.getByRole("link", { name: "Previous", exact: true }));
        await expect(page).toHaveURL(/[?&]cursor=page-1/);
        await expect(run).toBeVisible();
        await expect(page.getByRole("button", { name: "Previous", exact: true })).toBeDisabled();
        await expect(results).toHaveText("Failed runs");
        await activate(reset);
        await expect(results).toHaveText("Recent runs");
        await expect(status).toHaveValue("");
        await expect(results).toBeFocused();
    });
});
