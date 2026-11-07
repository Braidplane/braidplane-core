const {
    test,
    expect,
    assertSeededAuth,
    signInWithCredentials,
    signUpAndVerify,
    waitForMailpitLink,
    uniqueE2ESlug,
    deleteE2EAccount,
    e2eEmail,
    e2ePassword,
    e2eOrgSlug,
    e2eProjectSlug,
    e2eEnvironmentSlug,
} = require("./support/smoke-fixtures.cjs");

// Recovery URLs and credentials must stay out of browser artifacts.
test.use({ storageState: { cookies: [], origins: [] }, trace: "off", screenshot: "off" });
test.beforeAll(assertSeededAuth);

test("signs in and navigates from dashboard to scoped pipelines", async ({ page }) => {
    await signInWithCredentials(page, e2eEmail, e2ePassword);
    const scopeCookies = ["braidplane_dashboard_project_scope", "braidplane_dashboard_environment_scope"];
    await page.context().addCookies([
        {
            name: scopeCookies[0],
            value: `${e2eOrgSlug}:${e2eProjectSlug}`,
            domain: new URL(page.url()).hostname,
            path: "/dashboard",
        },
        {
            name: scopeCookies[1],
            value: `${e2eOrgSlug}:${e2eProjectSlug}:${e2eEnvironmentSlug}`,
            domain: new URL(page.url()).hostname,
            path: "/dashboard",
        },
    ]);
    await page.goto(`/dashboard?project_slug=${e2eProjectSlug}&environment_slug=${e2eEnvironmentSlug}`);
    await expect(page.getByRole("region", { name: "Dashboard scope" })).toBeVisible();
    await expect(page.getByLabel("Choose dashboard project scope", { exact: true })).toHaveValue(e2eProjectSlug);
    await expect(page.getByLabel("Choose dashboard runtime target", { exact: true })).toHaveValue(e2eEnvironmentSlug);
    const savedScope = (await page.context().cookies()).filter((cookie) => scopeCookies.includes(cookie.name));
    expect(savedScope).toHaveLength(2);
    expect(savedScope.every((cookie) => cookie.path === "/")).toBe(true);
    await page.getByRole("link", { name: "Data Sources", exact: true }).click();
    await page.reload();
    await page
        .getByRole("navigation", { name: "Primary", exact: true })
        .getByRole("link", { name: "Pipelines", exact: true })
        .click();
    await expect(page).toHaveURL(new RegExp(`/orgs/${e2eOrgSlug}/projects/${e2eProjectSlug}/pipelines`));
    expect(new URL(page.url()).searchParams.get("environment_slug")).toBe(e2eEnvironmentSlug);
});

test.describe("password recovery", () => {
    test("resets a signed-out account from its emailed link and signs in", async ({ page }, testInfo) => {
        const email = `${uniqueE2ESlug("password-recovery", testInfo)}@example.com`;
        const password = "Recovery-original-123!";
        const newPassword = "Recovery-replacement-123!";
        await signUpAndVerify(page, email, password, "Recovery Test");
        let currentPassword = password;

        try {
            await page.context().clearCookies();
            await page.goto("/auth/forgot-password");
            await page.locator('input[name="email"]').fill(email);
            await page.getByRole("button", { name: "Send reset link" }).click();
            await expect(
                page.getByText("If an account exists for this email, a reset link has been sent."),
            ).toBeVisible();

            const link = await waitForMailpitLink(page, email, "/auth/reset-password?token=");
            const resetURL = new URL(link);
            expect(resetURL.pathname).toBe("/auth/reset-password");
            expect(Boolean(resetURL.searchParams.get("token"))).toBe(true);
            await page.goto(link);
            await expect(page.getByRole("heading", { name: "Reset password", exact: true })).toBeVisible();
            expect(
                (await page.locator('input[name="token"]').inputValue()) === resetURL.searchParams.get("token"),
            ).toBe(true);
            await page.locator('input[name="password"]').fill(newPassword);
            await page.locator('input[name="confirm_password"]').fill(newPassword);
            await page.getByRole("button", { name: "Reset password", exact: true }).click();
            await expect(
                page.getByText("Password reset successful. You can now sign in with your new password."),
            ).toBeVisible();
            currentPassword = newPassword;
            await signInWithCredentials(page, email, newPassword, /\/workspace/);
        } finally {
            await signInWithCredentials(page, email, currentPassword, /\/workspace/);
            await deleteE2EAccount(page, email);
        }
    });
});
