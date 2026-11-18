const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("expired Google signup moves keyboard focus toward the Google retry", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        const signup = `${fixture.url}/auth/signup?return_to=%2Forganizations%2Facme%2Fprojects`;
        const seed = `${fixture.url}/_test/auth/google-signup`;
        expect((await page.request.post(seed)).ok()).toBe(true);
        await visit(page, signup);
        await expect(page.getByRole("button", { name: "Create account with Google", exact: true })).toBeVisible();
        const moreTime = page.getByRole("button", { name: "Continue with Google (100 minutes)", exact: true });
        await expect(moreTime).toHaveCount(0);
        await assess(page, testInfo, "google-signup-pending");

        expect((await page.request.post(`${seed}?expired=true`)).ok()).toBe(true);
        await visit(page, signup);
        const alert = page.getByRole("alert").filter({ hasText: "Your Google sign-in expired." });
        await expect(alert).toBeFocused();
        await page.keyboard.press("Tab");
        const retry = page.getByRole("button", { name: "Continue with Google", exact: true });
        await expect(retry).toBeFocused();
        await expect(moreTime).toBeVisible();
        await expect(retry.locator("..")).toHaveAttribute(
            "action",
            "/api/auth/google?return_to=%2Forganizations%2Facme%2Fprojects",
        );
        await assess(page, testInfo, "google-signup-expired");

        // The HTTP handler redirects here after an expired confirmation submission clears the session.
        await visit(page, `${signup}&google_signup_expired=true`);
        await expect(alert).toBeFocused();
        await page.keyboard.press("Tab");
        await expect(retry).toBeFocused();
        await expect(retry.locator("..")).toHaveAttribute(
            "action",
            "/api/auth/google?return_to=%2Forganizations%2Facme%2Fprojects",
        );
    });
});
