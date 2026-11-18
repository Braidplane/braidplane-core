const { once } = require("node:events");
const { createConnection } = require("node:net");
const { test: base, expect } = require("@playwright/test");
const { assess, tabTo } = require("../accessibility/support.cjs");
const { startSSRFixture, stopSSRFixture } = require("./support/ssr-fixture.cjs");

const startupTimeout = 60_000;
const shutdownTimeout = 75_000;
const test = base.extend({
    fixture: [
        async ({ browserName: _browserName }, use) => {
            const fixture = await startSSRFixture(process.env.E2E_GOOGLE_SIGNUP_FIXTURE_BIN, {
                args: ["-test.run=^TestFixtureServer$"],
                startupTimeout,
                // Container cleanup owns a one-minute deadline after server shutdown.
                shutdownTimeout,
            });
            try {
                await use(fixture);
            } finally {
                await stopSSRFixture(fixture);
            }
        },
        // Playwright charges setup and teardown to the same fixture slot.
        { timeout: startupTimeout + shutdownTimeout + 15_000 },
    ],
});
test.use({ storageState: { cookies: [], origins: [] }, trace: "off", screenshot: "off" });

test("stops its fixture with an unanswered browser preconnection", async ({ fixture }) => {
    const url = new URL(fixture.url);
    const connection = createConnection({ host: url.hostname, port: Number(url.port) });
    try {
        await once(connection, "connect");
        // The fixture may reset this deliberately incomplete HTTP connection during shutdown.
        connection.on("error", () => {});
        // A completed second request ensures the listener has accepted the idle connection.
        const response = await fetch(`${fixture.url}/auth/login`);
        expect(response.status).toBe(200);
        await response.text();
        await stopSSRFixture(fixture);
    } finally {
        connection.destroy();
    }
});

async function completeGoogleSignup(page, fixture, entryPath, email) {
    await page.goto(`${fixture.url}${entryPath}`);
    const googleSignup = page.locator('form[action^="/api/auth/google"]').first();
    await tabTo(page, googleSignup.getByRole("button", { name: "Continue with Google", exact: true }));
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(new RegExp(`${escapeRegExp(fixture.url)}/workspace`));
    await expect(page.getByRole("heading", { name: "Set up your workspace" })).toBeVisible();
    const assertion = await page.request.get(`${fixture.url}/_test/assert?email=${encodeURIComponent(email)}`);
    expect(assertion.ok()).toBe(true);
    const state = await assertion.json();
    expect(state.error).toBe("");
    expect(state.authorization).toBe(1);
    expect(state.exchange).toBe(1);
}

test("creates a Google account through a separate local issuer", async ({ page, fixture }) => {
    await completeGoogleSignup(page, fixture, "/auth/signup", "google-browser-1@example.test");
});

test("creates a Google account from the login entry point when registration is open", async ({ page, fixture }) => {
    await page.goto(`${fixture.url}/auth/login`);
    await tabTo(page, page.getByRole("link", { name: "Continue with Google", exact: true }));
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(new RegExp(`${escapeRegExp(fixture.url)}/workspace`));
    const assertion = await page.request.get(`${fixture.url}/_test/assert?email=google-browser-1@example.test`);
    const state = await assertion.json();
    expect(state.error).toBe("");
    expect(state.authorization).toBe(1);
    expect(state.exchange).toBe(1);
    expect(new Date(state.created_at).getTime()).toBeGreaterThan(0);
});

for (const [entry, role] of [
    ["login", "link"],
    ["signup", "button"],
]) {
    test(`allows more time for Google authorization from ${entry}`, async ({ page, fixture }, testInfo) => {
        const returnTo = "/organizations?section=projects";
        await page.goto(`${fixture.url}/auth/${entry}?${new URLSearchParams({ return_to: returnTo })}`);
        const delay = await page.request.post(`${fixture.url}/_test/delay-next-oauth-callback`);
        expect(delay.status()).toBe(204);
        const moreTime = page.getByRole(role, { name: "Continue with Google (100 minutes)", exact: true });
        for (const width of [1280, 320]) {
            await page.setViewportSize({ width, height: 900 });
            await assess(page, testInfo, `google-more-time-${entry}-${width}`);
        }
        await tabTo(page, moreTime);
        await page.keyboard.press("Enter");
        await expect(page.getByRole("heading", { name: "Set up your workspace" })).toBeVisible();
        expect(new URL(page.url()).searchParams.get("return_to")).toBe(returnTo);
        const assertion = await page.request.get(`${fixture.url}/_test/assert?email=google-browser-1@example.test`);
        expect(assertion.ok()).toBe(true);
        const state = await assertion.json();
        expect(state.error).toBe("");
        expect(state.authorization).toBe(1);
        expect(state.exchange).toBe(1);
    });
}

test("recovers from an expired Google callback with the keyboard", async ({ page, fixture }, testInfo) => {
    const returnTo = "/organizations";
    const query = new URLSearchParams({ return_to: returnTo });
    await page.goto(`${fixture.url}/auth/login?${query}`);
    const delay = await page.request.post(`${fixture.url}/_test/delay-next-oauth-callback`);
    expect(delay.status()).toBe(204);
    const google = page.getByRole("link", { name: "Continue with Google", exact: true });
    await tabTo(page, google);
    await page.keyboard.press("Enter");
    const error = page.getByRole("alert");
    await expect(error).toContainText("Sign-in session expired or became invalid.");
    await expect(error).toBeFocused();
    await expect(error).toHaveCSS("display", "grid");
    for (const width of [1280, 320]) {
        await page.setViewportSize({ width, height: 900 });
        await assess(page, testInfo, `google-callback-expired-${width}`);
        await expect(error).toBeFocused();
    }
    await page.keyboard.press("Tab");
    await expect(page.getByRole("link", { name: "Back to sign in", exact: true })).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(google).toHaveAttribute("href", `/api/auth/google?${query}`);
    await tabTo(page, google);
    await page.keyboard.press("Enter");
    await expect(page.getByRole("heading", { name: "Set up your workspace" })).toBeVisible();
    expect(new URL(page.url()).searchParams.get("return_to")).toBe(returnTo);
    const assertion = await page.request.get(`${fixture.url}/_test/assert?email=google-browser-1@example.test`);
    expect(assertion.ok()).toBe(true);
    const state = await assertion.json();
    expect(state.authorization).toBe(2);
    expect(state.exchange).toBe(1);
    expect(state.error).toBe("");
});

function escapeRegExp(value) {
    return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
