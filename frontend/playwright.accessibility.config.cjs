const { defineConfig, devices } = require("@playwright/test");
const { resolve } = require("node:path");
const { resolvePlaywrightBaseURL } = require("./assets/js/playwright_base_url.cjs");

module.exports = defineConfig({
    testDir: "./accessibility",
    globalSetup: ["./e2e/global-setup.cjs"],
    timeout: 60_000,
    workers: 1,
    retries: 0,
    forbidOnly: true,
    outputDir: "../.worktree/accessibility/results",
    reporter: [["list"], ["json", { outputFile: "../.worktree/accessibility/results.json" }]],
    use: {
        baseURL: resolvePlaywrightBaseURL({ env: process.env, envPath: resolve(__dirname, "..", ".worktree-env") }),
        storageState: resolve(__dirname, "e2e/.auth/e2e-user-chromium.json"),
        headless: true,
        trace: "off",
        screenshot: "off",
        video: "off",
    },
    projects: [
        { name: "chromium", use: { ...devices["Desktop Chrome"] } },
        {
            name: "chromium-compact",
            // Each auth lifecycle checks both widths without duplicating throttled sign-ins.
            grepInvert:
                /keyboard sign-in recovers from an error and signs out|password recovery preserves|organization and project creation/,
            use: { ...devices["Desktop Chrome"], viewport: { width: 320, height: 900 } },
        },
    ],
});
