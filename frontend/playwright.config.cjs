const { defineConfig, devices } = require("@playwright/test");
const { resolve } = require("node:path");

const { resolvePlaywrightBaseURL } = require("./assets/js/playwright_base_url.cjs");
const { browserProfile, browserGrep } = require("./e2e/support/alpha-selection.cjs");
const profile = browserProfile();
const inventory = require("./e2e/alpha-inventory.json");

const configDir = __dirname;
const authStateDir = resolve(configDir, "e2e", ".auth");
const baseURL = resolvePlaywrightBaseURL({
    env: process.env,
    envPath: resolve(configDir, "..", ".worktree-env"),
});

module.exports = defineConfig({
    metadata: { profile },
    testDir: "./e2e",
    testMatch: [
        "auth.spec.cjs",
        "google-signup.spec.cjs",
        "query-workspace.spec.cjs",
        "query-workspace-owned.spec.cjs",
        "pipeline-builder.spec.cjs",
        "pipeline-git.spec.cjs",
        "run-inspection.spec.cjs",
        "proof-receipts.spec.cjs",
        "http-oauth.spec.cjs",
        "notification-connections.spec.cjs",
    ],
    globalSetup: ["./e2e/firefox-app-data.cjs", "./e2e/global-setup.cjs"],
    timeout: 30_000,
    maxFailures: 5,
    globalTimeout: inventory.suites.product.budgets[profile].runner_seconds * 1000,
    expect: {
        timeout: 5_000,
    },
    fullyParallel: true,
    // Bound browser startup and recording contention across the matrix.
    workers: process.env.CI ? 2 : 4,
    forbidOnly: Boolean(process.env.CI),
    retries: 0,
    reporter: process.env.CI
        ? [["github"], ["html", { open: "never" }], ["json", { outputFile: "../.worktree/playwright/product.json" }]]
        : [["list"], ["html", { open: "never" }], ["json", { outputFile: "../.worktree/playwright/product.json" }]],
    use: {
        baseURL,
        headless: true,
        trace: "retain-on-failure",
        screenshot: "only-on-failure",
        video: "off",
    },
    projects: [
        {
            name: "chromium",
            grep: browserGrep("product", "chromium", profile),
            use: { ...devices["Desktop Chrome"], storageState: resolve(authStateDir, "e2e-user-chromium.json") },
        },
        {
            name: "firefox",
            grep: browserGrep("product", "firefox", profile),
            testIgnore: ["**/auth.spec.cjs", "**/google-signup.spec.cjs", "**/pipeline-builder.spec.cjs"],
            use: { ...devices["Desktop Firefox"], storageState: resolve(authStateDir, "e2e-user-firefox.json") },
        },
        {
            name: "webkit",
            grep: browserGrep("product", "webkit", profile),
            testIgnore: ["**/auth.spec.cjs", "**/google-signup.spec.cjs", "**/pipeline-builder.spec.cjs"],
            use: { ...devices["Desktop Safari"], storageState: resolve(authStateDir, "e2e-user-webkit.json") },
        },
    ],
});
