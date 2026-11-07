const { spawnSync } = require("node:child_process");
const { mkdirSync, writeFileSync } = require("node:fs");
const { resolve } = require("node:path");
const inventory = require("../frontend/e2e/alpha-inventory.json");

function collectedTests(report) {
    const tests = [];
    function visit(suite) {
        for (const spec of suite.specs || []) {
            for (const test of spec.tests) tests.push({ file: spec.file, title: spec.title, project: test.projectName });
        }
        for (const child of suite.suites || []) visit(child);
    }
    for (const suite of report.suites) visit(suite);
    return tests;
}

function verifySelection(kind, full, critical, manifest = inventory) {
    const suite = manifest.suites[kind];
    const key = (entry) => `${entry.file}:${entry.title}`;
    const execution = (entry) => `${entry.project}:${key(entry)}`;
    const cases = new Map();
    for (const entry of suite.cases) {
        if (cases.has(key(entry)) || !entry.reason?.trim() || !["critical", "extended"].includes(entry.tier))
            throw new Error(`Invalid or unjustified ${kind} inventory entry: ${key(entry)}`);
        if (entry.cross_browser && entry.tier !== "critical") throw new Error("Cross-browser smoke must be critical");
        cases.set(key(entry), entry);
    }
    const discovered = new Set(full.map(key));
    if (discovered.size !== cases.size || [...discovered].some((id) => !cases.has(id)))
        throw new Error(`${kind} cases changed: update the assertion inventory and justify the change`);
    const expected = full.filter((test) => {
        const entry = cases.get(key(test));
        return entry.tier === "critical" && (test.project === "chromium" || entry.cross_browser);
    }).map(execution).sort();
    if (JSON.stringify(expected) !== JSON.stringify(critical.map(execution).sort()))
        throw new Error(`${kind} critical selection omitted or added an execution`);
    const counts = {};
    for (const [profile, tests] of [["full", full], ["critical", critical]]) {
        const logical = new Set(tests.map(key)).size;
        const executions = tests.length;
        const budget = suite.budgets[profile];
        if (![budget.logical, budget.executions, budget.runner_seconds].every((value) => Number.isInteger(value) && value > 0))
            throw new Error(`${kind}/${profile} has an invalid reviewed budget`);
        if (logical > budget.logical || executions > budget.executions)
            throw new Error(`${kind}/${profile} exceeds its reviewed execution budget`);
        counts[profile] = { logical, executions };
    }
    return counts;
}

function main() {
    const root = resolve(__dirname, "..");
    const counts = {};
    for (const kind of ["product"]) {
        const reports = {};
        for (const profile of ["full", "critical"]) {
            const result = spawnSync(process.execPath, ["node_modules/@playwright/test/cli.js", "test", "--list", "--reporter=json", "--config", "playwright.config.cjs"], {
                cwd: resolve(root, "frontend"),
                env: { ...process.env, BRAIDPLANE_BROWSER_PROFILE: profile, PUBLIC_WEBSITE_BASE_URL: "http://127.0.0.1:1", PW_DISABLE_TS_ESM: "1" },
                encoding: "utf8", maxBuffer: 16 * 1024 * 1024, timeout: 30_000,
            });
            if (result.error || result.status !== 0) throw new Error(result.error?.message || result.stderr || result.stdout);
            reports[profile] = collectedTests(JSON.parse(result.stdout));
        }
        counts[kind] = verifySelection(kind, reports.full, reports.critical);
    }
    const directory = resolve(root, ".worktree/playwright");
    mkdirSync(directory, { recursive: true });
    writeFileSync(resolve(directory, "inventory.json"), `${JSON.stringify(counts, null, 2)}\n`);
    console.log(JSON.stringify(counts));
}

if (require.main === module) main();
module.exports = { collectedTests, verifySelection };
