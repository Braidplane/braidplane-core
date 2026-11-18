const { test, expect } = require("@playwright/test");
const {
    signInWithCredentials,
    waitForMailpitCode,
    uniqueE2ESlug,
    deleteE2EAccount,
} = require("../e2e/support/smoke-fixtures.cjs");
const { isolateE2EAuthClient } = require("../e2e/support/auth-throttle-client.cjs");
const { assess, visit, tabTo } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("password recovery preserves keyboard access, errors and status across the complete process", async ({
    page,
}, testInfo) => {
    expect(process.env.MAILPIT_URL, "Set MAILPIT_URL to this runtime's owned synthetic inbox").toBeTruthy();
    const email = `${uniqueE2ESlug("a11y-recovery", testInfo)}@example.test`;
    const password = "Synthetic-original-123!";
    const replacement = "Synthetic-replacement-123!";
    const pastePassword = async (field, value) => {
        await expect(field).toHaveAttribute("autocomplete", "new-password");
        await tabTo(page, field);
        await page.evaluate((text) => navigator.clipboard.writeText(text), value);
        try {
            await page.keyboard.press("ControlOrMeta+v");
            expect((await field.inputValue()) === value).toBe(true);
        } finally {
            await page.evaluate(() => navigator.clipboard.writeText(""));
        }
    };
    const scan = async (state) => {
        const viewport = page.viewportSize();
        for (const width of [1280, 320]) {
            await page.setViewportSize({ width, height: 900 });
            await assess(page, testInfo, `${state}-${width}`);
        }
        await page.setViewportSize(viewport);
    };
    await isolateE2EAuthClient(page, email);
    await visit(page, "/auth/signup");
    const signupPassword = page.locator('[name="password"]');
    await expect(signupPassword).toHaveAccessibleName("Password");
    await expect(signupPassword).toHaveAccessibleDescription("Use at least 8 characters.");
    await expect(page.getByText("Use at least 8 characters.", { exact: true })).toBeVisible();
    await scan("signup-password-form");
    await page
        .context()
        .grantPermissions(["clipboard-read", "clipboard-write"], { origin: new URL(page.url()).origin });
    for (const [field, value] of [
        [page.getByLabel("Full name", { exact: true }), "Accessibility Fixture"],
        [page.getByLabel("Email", { exact: true }), email],
    ]) {
        await tabTo(page, field);
        await page.keyboard.insertText(value);
    }
    await pastePassword(signupPassword, password);
    await expect(signupPassword).toHaveAccessibleDescription("Use at least 8 characters.");
    await pastePassword(page.getByLabel("Confirm password", { exact: true }), password);
    await page.keyboard.press("Enter");
    await expect(page.getByRole("heading", { name: "Verify your email" })).toBeVisible();
    const verificationPath = "/verify-email";
    const firstVerification = await waitForMailpitCode(page, email, verificationPath);
    let verification = firstVerification;
    const completeVerification = async () => {
        await page.goto(verification.link);
        const code = page.getByLabel("Verification code");
        await tabTo(page, code);
        await page.keyboard.insertText(verification.code);
        await page.keyboard.press("Enter");
    };
    let verified = false;
    let currentPassword = password;
    let signedIn = false;
    try {
        await scan("signup-verification-pending");
        const resend = page.getByRole("button", { name: "Resend verification code", exact: true });
        await tabTo(page, resend);
        await page.keyboard.press("Enter");
        verification = await waitForMailpitCode(page, email, verificationPath, firstVerification.code);
        await expect(
            page.getByRole("status").filter({ hasText: "If this address has an unverified account" }),
        ).toBeFocused();
        await expect(page.locator("main")).toHaveCount(1);
        await scan("signup-verification-resent");
        await completeVerification();
        await expect(page.getByRole("heading", { name: "Email verified", exact: true })).toBeVisible();
        verified = true;
        await scan("email-verified");
        await visit(page, "/auth/forgot-password");
        const emailField = page.getByLabel("Email", { exact: true });
        await expect(emailField).toHaveAttribute("autocomplete", "email");
        await tabTo(page, emailField);
        await page.keyboard.insertText(email);
        await page.keyboard.press("Enter");
        await expect(page.getByRole("status")).toContainText("a reset code has been sent");
        await expect(page.locator("main")).toHaveCount(1);
        await scan("reset-link-requested");
        const reset = await waitForMailpitCode(page, email, "/auth/reset-password");
        await page.setViewportSize({ width: 320, height: 900 });
        await page.goto(reset.link);
        const resetCode = page.getByLabel("Reset code");
        await tabTo(page, resetCode);
        await page.keyboard.insertText(reset.code);
        const next = page.getByLabel("New password", { exact: true });
        const confirm = page.getByLabel("Confirm password", { exact: true });
        await expect(next).toHaveAttribute("autocomplete", "new-password");
        await expect(next).toHaveAccessibleDescription("Use at least 8 characters.");
        await expect(page.getByText("Use at least 8 characters.", { exact: true })).toBeVisible();
        await expect(confirm).toHaveAttribute("autocomplete", "new-password");
        await scan("reset-password-form");
        await tabTo(page, next);
        await page.keyboard.insertText(replacement);
        await page.keyboard.press("Tab");
        await expect(confirm).toBeFocused();
        await page.keyboard.insertText("Synthetic-mismatch-123!");
        await page.keyboard.press("Enter");
        await expect(page.getByRole("alert")).toContainText("Passwords do not match");
        await expect(page.getByRole("alert")).toBeFocused();
        await expect(page.locator("main")).toHaveCount(1);
        expect((await next.inputValue()) === "" && (await confirm.inputValue()) === "").toBe(true);
        await scan("reset-password-mismatch");
        await tabTo(page, resetCode);
        await page.keyboard.insertText(reset.code);
        await pastePassword(next, replacement);
        await expect(next).toHaveAccessibleDescription("Use at least 8 characters.");
        await pastePassword(confirm, replacement);
        await page.keyboard.press("Enter");
        await expect(
            page.getByText("Password reset successful. You can now sign in with your new password."),
        ).toBeVisible();
        currentPassword = replacement;
        await expect(page.getByRole("status")).toContainText("Password reset successful");
        await expect(page.locator("main")).toHaveCount(1);
        await scan("reset-password-success");
        await tabTo(page, page.getByRole("link", { name: "Go to sign in", exact: true }));
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/\/auth\/login$/);
        await signInWithCredentials(page, email, replacement, /\/workspace/);
        signedIn = true;
        await scan("reset-password-sign-in");
    } finally {
        if (!verified) {
            await completeVerification();
        }
        if (!signedIn) await signInWithCredentials(page, email, currentPassword, /\/workspace/);
        await deleteE2EAccount(page, email);
    }
});
