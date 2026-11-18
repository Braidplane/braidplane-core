const { test, expect } = require("@playwright/test");
const { uniqueE2ESlug } = require("../e2e/support/smoke-fixtures.cjs");
const { isolateE2EAuthClient } = require("../e2e/support/auth-throttle-client.cjs");
const { assess, tabTo } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("verification resend supports keyboard correction after an invalid code", async ({ page }, testInfo) => {
    const address = `${uniqueE2ESlug("a11y-resend", testInfo)}@example.test`;
    await isolateE2EAuthClient(page, address);
    await page.goto("/verify-email");
    const code = page.getByLabel("Verification code");
    await tabTo(page, code);
    await page.keyboard.insertText("0".repeat(64));
    await page.keyboard.press("Enter");
    const alert = page.getByRole("alert");
    const email = page.getByLabel("Account email", { exact: true });
    await expect(alert).toHaveText("Invalid or missing verification token.");
    await expect(alert).toBeFocused();
    await assess(page, testInfo, "verification-missing-token");

    await tabTo(page, email);
    await page.keyboard.insertText("a@b");
    await page.keyboard.press("Enter");
    await expect(alert).toHaveText("Email must be a valid email address.");
    await expect(alert).toBeFocused();
    await expect(page.locator("main")).toHaveCount(1);
    await expect(email).toHaveValue("a@b");
    await assess(page, testInfo, "verification-resend-invalid-email");

    await tabTo(page, email);
    await page.keyboard.press("ControlOrMeta+a");
    await page.keyboard.insertText(address);
    await page.keyboard.press("Enter");
    const status = page.getByRole("status").filter({ hasText: "If this address has an unverified account" });
    await expect(status).toBeFocused();
    await expect(page.locator("main")).toHaveCount(1);
    await expect(page.getByText("We sent a verification code to your inbox.")).toHaveCount(0);
    await assess(page, testInfo, "verification-resend-requested");
});
