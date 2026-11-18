const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("expired MFA can be restarted by keyboard without losing the destination", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        const returnTo = "/organizations/acme/projects";
        const login = `${fixture.url}/auth/login?return_to=${encodeURIComponent(returnTo)}`;
        const email = page.getByRole("textbox", { name: "Email", exact: true });
        const password = page.getByLabel("Password", { exact: true });
        const code = page.getByRole("textbox", { name: "Code", exact: true });
        const signIn = async () => {
            await tabTo(page, email);
            await page.keyboard.insertText("mfa@example.test");
            await tabTo(page, password);
            await page.keyboard.insertText("Synthetic-MFA-123!");
            await page.keyboard.press("Enter");
            await expect(page.getByRole("heading", { name: "Check your email", exact: true })).toBeVisible();
        };
        const submitCode = async () => {
            await tabTo(page, code);
            await page.keyboard.insertText("123456");
            await page.keyboard.press("Enter");
        };
        expect((await page.request.post(`${fixture.url}/_test/auth/mfa`)).ok()).toBe(true);
        await visit(page, login);
        await signIn();
        expect((await page.request.post(`${fixture.url}/_test/auth/mfa/expire`)).ok()).toBe(true);
        await submitCode();
        const error = page.getByRole("alert");
        await expect(error).toContainText("Invalid or expired code.");
        await expect(error).toBeFocused();
        await expect(code).toHaveValue("");
        await assess(page, testInfo, "mfa-expired-code");

        await tabTo(page, page.getByRole("link", { name: "Sign in again for a new code", exact: true }));
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(login);
        await signIn();
        await submitCode();
        await expect(page).toHaveURL(`${fixture.url}${returnTo}`);
    });
});
