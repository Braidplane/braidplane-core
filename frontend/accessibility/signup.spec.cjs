const { test, expect } = require("@playwright/test");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("signup password mismatch preserves the return path and focuses the error", async ({ page }, testInfo) => {
    await visit(page, "/auth/signup?return_to=%2Fdashboard");
    const name = page.getByLabel("Full name", { exact: true });
    const email = page.getByLabel("Email", { exact: true });
    const password = page.locator('[name="password"]');
    const confirmation = page.getByLabel("Confirm password", { exact: true });
    for (const [field, value] of [
        [name, "Accessibility Fixture"],
        [email, "signup-mismatch@example.test"],
        [password, "Synthetic-first-123!"],
        [confirmation, "Synthetic-other-123!"],
    ]) {
        await tabTo(page, field);
        await page.keyboard.insertText(value);
    }
    await page.keyboard.press("Enter");
    const alert = page.getByRole("alert").filter({ hasText: "Passwords do not match." });
    await expect(alert).toBeFocused();
    await expect(page.locator("main")).toHaveCount(1);
    await expect(name).toHaveValue("Accessibility Fixture");
    await expect(email).toHaveValue("signup-mismatch@example.test");
    expect((await password.inputValue()) === "" && (await confirmation.inputValue()) === "").toBe(true);
    await expect(page.locator('form[action="/auth/signup"] [name="return_to"]')).toHaveValue("/dashboard");
    await assess(page, testInfo, "signup-password-mismatch");
});
