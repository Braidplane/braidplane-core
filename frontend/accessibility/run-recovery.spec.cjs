const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("run recovery keeps recorded evidence reachable after execute permission is revoked", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/failed?protected_recovery=true`)).ok()).toBe(true);
        const inspection = `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=attempts`;
        await visit(page, inspection);
        const recover = page.getByRole("button", { name: "Recover sink delivery", exact: true });
        const confirmation = page.getByLabel("Environment confirmation (if protected)", { exact: true });
        await tabTo(page, confirmation);
        await page.keyboard.insertText("dev");
        await tabTo(page, recover);
        expect((await page.request.post(`${fixture.url}/_test/deployment-access?read_only=true`)).ok()).toBe(true);
        await page.keyboard.press("Enter");

        const attempts = page.getByRole("region", { name: "Attempts & recovery", exact: true });
        const error = attempts
            .getByRole("alert")
            .filter({ hasText: "you do not have permission to execute pipelines" });
        await expect(error).toBeFocused();
        await expect(error).toBeInViewport({ ratio: 1 });
        await expect(page).toHaveURL(/\/runs\/run-inspection\?environment_slug=dev&section=attempts&recovery_error=/);
        await expect(recover).toHaveCount(0);
        await expect(attempts).toContainText("Provider acknowledgement unavailable");
        await assess(page, testInfo, "run-recovery-permission-revoked");

        const steps = page.getByRole("link", { name: "Steps", exact: true });
        await tabTo(page, steps);
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/\/runs\/run-inspection\?(?=.*section=steps)/);
    });
});

test("run recovery focuses refusal feedback and supports a keyboard retry", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const inspection = `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=attempts`;
        expect((await page.request.post(`${fixture.url}/_test/run/failed`)).ok()).toBe(true);
        await visit(page, inspection);
        const recover = page.getByRole("button", { name: "Recover sink delivery", exact: true });
        const recoveryAlert = page.getByRole("alert");
        await tabTo(page, recover);
        await page.keyboard.press("Enter");
        await expect(recoveryAlert).toContainText("Retained inputs were pruned; recovery refused.");
        await expect(recoveryAlert).toBeFocused();
        const attempts = page.getByRole("region", { name: "Attempts & recovery", exact: true });
        await expect(attempts.getByRole("alert")).toHaveCount(1);
        await expect(attempts).toContainText("Provider acknowledgement unavailable");
        await assess(page, testInfo, "run-recovery-pruned");

        expect((await page.request.post(`${fixture.url}/_test/run/failed?protected_recovery=true`)).ok()).toBe(true);
        await visit(page, inspection);
        await tabTo(page, recover);
        await page.keyboard.press("Enter");
        await expect(attempts.getByRole("alert")).toContainText("Protected environment confirmation required");
        await expect(attempts.getByRole("alert")).toBeFocused();
        await assess(page, testInfo, "run-recovery-confirmation-required");

        const confirmation = page.getByLabel("Environment confirmation (if protected)", { exact: true });
        const confirmationHelp = page.getByText("For a protected environment, enter dev.", { exact: true });
        await expect(confirmationHelp).toBeVisible();
        await expect(confirmation).toHaveAccessibleDescription("For a protected environment, enter dev.");
        await tabTo(page, confirmation);
        await page.keyboard.insertText("not a slug");
        await expect(confirmationHelp).toBeVisible();
        await page.keyboard.press("Enter");
        await expect(attempts.getByRole("alert")).toContainText("Enter a valid environment slug.");
        await expect(attempts.getByRole("alert")).toBeFocused();

        await tabTo(page, confirmation);
        await page.keyboard.insertText("dev");
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/\/runs\/run-recovered\?environment_slug=dev$/);
        await expect(page.locator("#run-recorded-status")).toHaveText("queued");
        await expect(page.getByRole("alert")).toHaveCount(0);
        await assess(page, testInfo, "run-recovery-queued");
    });
});
