const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("./support/ssr-fixture.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("tests a Stripe restricted key and saves a revenue source", async ({ page }) => {
    await withSSRFixture(process.env.E2E_HTTP_OAUTH_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/data-sources?type=stripe`);
        const form = page.locator("#create-data-source");
        await form.getByRole("textbox", { name: "Name", exact: true }).fill("Stripe revenue");
        await form.getByText("Customize identifier", { exact: true }).click();
        await form.locator('[name="slug"]').fill("stripe-revenue");
        const key = form.locator('[name="stripe_api_key"]');
        const mode = form.getByLabel("Stripe mode");
        await expect(key).toHaveAttribute("type", "password");
        await expect(mode).toHaveValue("test");
        await key.fill("rk_test_synthetic");
        await mode.selectOption("live");
        const check = form.getByRole("button", { name: "Test connection", exact: true });
        await check.click();
        await expect(form.getByRole("alert")).toContainText("Check the Stripe restricted key and mode.");
        await expect(mode).toHaveValue("live");
        await mode.selectOption("test");
        await check.click();
        await expect(form.getByRole("status")).toContainText("Connection successful.");
        await form.getByRole("button", { name: "Create data source", exact: true }).click();
        await expect(page.getByRole("link", { name: "Stripe revenue", exact: true })).toBeVisible();
        await page.goto(`${fixture.url}/orgs/acme/data-sources/stripe-revenue?section=settings`);
        await expect(page.getByText("Stripe mode", { exact: true })).toBeVisible();
        await expect(page.locator('input[name="stripe_api_key"]')).toHaveValue("");
        expect(await page.content()).not.toContain("rk_test_synthetic");
    });
});
