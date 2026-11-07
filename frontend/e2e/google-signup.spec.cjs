const { once } = require("node:events");
const { createConnection } = require("node:net");
const { test: base, expect } = require("@playwright/test");
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
    await googleSignup.getByRole("button", { name: /continue with Google/i }).click();
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
    await page.getByRole("link", { name: "Continue with Google", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`${escapeRegExp(fixture.url)}/workspace`));
    const assertion = await page.request.get(`${fixture.url}/_test/assert?email=google-browser-1@example.test`);
    const state = await assertion.json();
    expect(state.error).toBe("");
    expect(state.authorization).toBe(1);
    expect(state.exchange).toBe(1);
    expect(new Date(state.created_at).getTime()).toBeGreaterThan(0);
});

function escapeRegExp(value) {
    return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
