const { test, expect } = require("@playwright/test");
const { assess, tabTo, visit } = require("./support.cjs");
const { isolateE2EAuthClient } = require("../e2e/support/auth-throttle-client.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("unusable reset codes lead by keyboard to a new request", async ({ page }, testInfo) => {
    await isolateE2EAuthClient(page, `reset-link-${testInfo.project.name}`);
    const link = page.getByRole("link", { name: "Request a new reset code", exact: true });
    const alert = page.getByRole("alert").filter({ hasText: "This password reset code is invalid or expired." });
    const checkUnavailable = async () => {
        await expect(alert).toBeFocused();
        await expect(page.getByRole("main")).toHaveCount(1);
        await expect(link).toBeVisible();
    };

    await visit(page, "/auth/reset-password");
    const code = page.getByLabel("Reset code");
    await tabTo(page, code);
    await page.keyboard.insertText("0".repeat(64));
    await tabTo(page, page.getByLabel("New password", { exact: true }));
    await page.keyboard.insertText("Synthetic-unused-123!");
    await page.keyboard.press("Tab");
    await expect(page.getByLabel("Confirm password", { exact: true })).toBeFocused();
    await page.keyboard.insertText("Synthetic-unused-123!");
    await page.keyboard.press("Enter");
    await checkUnavailable();
    await assess(page, testInfo, "reset-link-rejected");

    await tabTo(page, link);
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/auth\/forgot-password$/);
    await expect(page.getByLabel("Email", { exact: true })).toBeVisible();
});
