const path = require("node:path");
const base = require("./playwright.config.cjs");

// Explicit owner-run acceptance; the routine CI inventory stays unchanged.
module.exports = {
    ...base,
    testDir: "./acceptance",
    testMatch: "acceptance.spec.cjs",
    outputDir: path.resolve(__dirname, "../.worktree/playwright/acceptance-results"),
    reporter: [["list"], ["json", { outputFile: path.resolve(__dirname, "../.worktree/playwright/acceptance.json") }]],
    projects: base.projects.map((project) => ({ ...project, grep: undefined, testIgnore: undefined })),
    workers: 3,
    maxFailures: 20,
    globalTimeout: 240000,
};
