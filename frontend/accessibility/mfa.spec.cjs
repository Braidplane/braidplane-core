const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("email-code sign-in supports keyboard error recovery and clipboard paste", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        await page.context().grantPermissions(["clipboard-read", "clipboard-write"], { origin: fixture.url });
        expect((await page.request.post(`${fixture.url}/_test/auth/mfa`)).ok()).toBe(true);
        const returnTo = "/organizations/acme/projects";
        await visit(page, `${fixture.url}/auth/login?return_to=${encodeURIComponent(returnTo)}`);
        await tabTo(page, page.getByRole("textbox", { name: "Email", exact: true }));
        await page.keyboard.insertText("mfa@example.test");
        await page.keyboard.press("Tab");
        const password = page.getByLabel("Password", { exact: true });
        await expect(password).toBeFocused();
        await expect(password).toHaveAttribute("autocomplete", "current-password");
        await page.evaluate(() => navigator.clipboard.writeText("Synthetic-MFA-123!"));
        await page.keyboard.press("ControlOrMeta+v");
        expect((await password.inputValue()) === "Synthetic-MFA-123!").toBe(true);
        await page.keyboard.press("Enter");
        await expect(page.getByRole("heading", { name: "Check your email", exact: true })).toBeVisible();
        const code = page.getByRole("textbox", { name: "Code", exact: true });
        await expect(code).toHaveAttribute("autocomplete", "one-time-code");
        await expect(code).toHaveAttribute("inputmode", "numeric");
        await tabTo(page, code);
        await page.keyboard.insertText("000000");
        await page.keyboard.press("Enter");
        const error = page.getByRole("alert");
        await expect(error).toContainText("Invalid or expired code.");
        await expect(error).toBeFocused();
        await expect(page.getByRole("main")).toHaveCount(1);
        await expect(code).toHaveValue("");
        await expect(code).toHaveAttribute("aria-invalid", "true");
        await expect(code).toHaveAttribute("aria-describedby", "mfa-feedback");
        await assess(page, testInfo, "mfa-invalid-code");
        await tabTo(page, code);
        await page.evaluate(() => navigator.clipboard.writeText("123456"));
        await page.keyboard.press("ControlOrMeta+v");
        await expect(code).toHaveValue("123456");
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(`${fixture.url}${returnTo}`);
        await expect(page.getByRole("main")).toHaveCount(1);
        await assess(page, testInfo, "mfa-sign-in-complete");
    });
});
