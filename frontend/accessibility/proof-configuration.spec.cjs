const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("Proof configuration supports keyboard error recovery and saved feedback", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        await visit(page, `${fixture.url}/orgs/acme/data-sources/github/proof`);
        const save = page.getByRole("button", { name: "Save Proof configuration", exact: true });
        const enabled = page.getByRole("checkbox", { name: "Enable Proof assessments", exact: true });
        await expect(save).toBeDisabled();
        await tabTo(page, enabled);
        await page.keyboard.press("Space");
        const publish = page.getByRole("checkbox", { name: "Publish informational GitHub checks", exact: true });
        await tabTo(page, page.getByText("Publication options", { exact: true }));
        await page.keyboard.press("Enter");
        await tabTo(page, publish);
        await page.keyboard.press("Space");
        await tabTo(page, save);
        await page.keyboard.press("Enter");

        const error = page.getByRole("alert").filter({ hasText: "Select an explicit assessment target" });
        await expect(error).toBeFocused();
        await expect(enabled).toBeChecked();
        await expect(publish).toBeChecked();
        await expect(save).toBeEnabled();
        await assess(page, testInfo, "proof-configuration-refused");

        const pipelinePath = page.getByRole("textbox", { name: "ops / prod", exact: true });
        await expect(pipelinePath).toHaveAccessibleDescription(/one repository-relative pipeline path per line/);
        await tabTo(page, pipelinePath);
        await page.keyboard.insertText("pipelines/orders.yaml");
        await tabTo(page, save);
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/proof\?saved=true$/);
        await expect(page.getByRole("status").filter({ hasText: "Proof configuration saved." })).toBeFocused();
        await expect(enabled).toBeChecked();
        await expect(save).toBeDisabled();
        const targets = page.getByText("Assessment targets (1)", { exact: true });
        await tabTo(page, targets);
        await page.keyboard.press("Enter");
        await expect(pipelinePath).toHaveValue("pipelines/orders.yaml");
        await assess(page, testInfo, "proof-configuration-saved");
    });
});

test("Proof settings can be retried after a GitLab webhook refusal without re-entering values", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect(
            (await page.request.post(`${fixture.url}/_test/proof/reset?provider=gitlab&setup_failure=true`)).ok(),
        ).toBe(true);
        await visit(page, `${fixture.url}/orgs/acme/data-sources/gitlab/proof`);
        const retry = page.getByRole("button", { name: "Retry GitLab webhook setup", exact: true });
        await tabTo(page, retry);
        await page.keyboard.press("Enter");
        const error = page.getByRole("alert").filter({ hasText: "GitLab webhook setup failed. Retry setup." });
        await expect(error).toBeFocused();
        const pipelinePath = page.getByRole("textbox", { name: "ops / prod", exact: true });
        await expect(pipelinePath).toHaveValue("pipelines/orders.yaml");
        await assess(page, testInfo, "proof-webhook-retry");

        const save = page.getByRole("button", { name: "Save Proof configuration", exact: true });
        await expect(save).toBeEnabled();
        await tabTo(page, save);
        await page.keyboard.press("Space");
        await expect(page).toHaveURL(/proof\?saved=true$/);
        await expect(page.getByRole("status").filter({ hasText: "Proof configuration saved." })).toBeFocused();
        await expect(retry).toHaveCount(0);
    });
});

test("unavailable Proof settings keep a keyboard route back to Git integrations", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const response = await page.goto(`${fixture.url}/orgs/acme/data-sources/missing/proof`);
        expect(response.status()).toBe(404);
        await expect(page.getByRole("heading", { name: "Proof configuration", exact: true })).toBeVisible();
        await expect(page.getByRole("alert").filter({ hasText: "Proof configuration is unavailable" })).toBeFocused();
        await expect(page.getByRole("button", { name: "Save Proof configuration" })).toHaveCount(0);
        const back = page.getByRole("link", { name: "Git integrations", exact: true });
        await expect(back).toHaveAttribute("href", "/organizations/acme/connections");
        await assess(page, testInfo, "proof-configuration-unavailable");
        const denied = await page.goto(`${fixture.url}/orgs/acme/data-sources/restricted/proof`);
        expect(denied.status()).toBe(403);
        await expect(page.getByRole("alert").filter({ hasText: "You do not have permission" })).toBeFocused();
        await expect(page.getByRole("button", { name: "Save Proof configuration" })).toHaveCount(0);
        await assess(page, testInfo, "proof-configuration-denied");
        await tabTo(page, back);
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/\/organizations\/acme\/connections$/);
    });
});

test("repository replacement gives Proof users a keyboard way to review fresh settings", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const location = `${fixture.url}/orgs/acme/data-sources/github/proof`;
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        await visit(page, location);
        const enabled = page.getByRole("checkbox", { name: "Enable Proof assessments" });
        const save = page.getByRole("button", { name: "Save Proof configuration" });
        const path = page.getByRole("textbox", { name: "ops / prod", exact: true });
        await tabTo(page, enabled);
        await page.keyboard.press("Space");
        await activate(page.getByText("Assessment targets (0)", { exact: true }));
        await tabTo(page, path);
        await page.keyboard.insertText("pipelines/orders.yaml");
        expect((await page.request.post(`${fixture.url}/_test/proof/replace`)).ok()).toBe(true);
        await activate(save);
        await expect(page.getByRole("alert").filter({ hasText: "Repository changed" })).toBeFocused();
        await expect(path).toHaveValue("pipelines/orders.yaml");
        const reload = page.getByRole("link", { name: "Reload Proof configuration", exact: true });
        await expect(reload).toHaveAttribute("href", "/orgs/acme/data-sources/github/proof");
        await assess(page, testInfo, "proof-configuration-stale-repository");
        await activate(reload);
        await expect(enabled).not.toBeChecked();
        await expect(save).toBeDisabled();
        await expect(page.locator('[name="expected_repository_id"]')).toHaveValue("456");
        await tabTo(page, enabled);
        await page.keyboard.press("Space");
        await activate(page.getByText("Assessment targets (0)", { exact: true }));
        await tabTo(page, path);
        await page.keyboard.insertText("pipelines/orders.yaml");
        await activate(save);
        await expect(page.getByRole("status").filter({ hasText: "Proof configuration saved." })).toBeFocused();
    });
});
