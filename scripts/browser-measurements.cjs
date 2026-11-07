const { readFileSync, readdirSync, statSync } = require("node:fs");
const { join } = require("node:path");
const inventory = require("../frontend/e2e/alpha-inventory.json");

function unionMilliseconds(intervals) {
    let total = 0;
    let end = -Infinity;
    for (const [start, finish] of intervals.sort((a, b) => a[0] - b[0])) {
        total += Math.max(0, finish - Math.max(start, end));
        end = Math.max(end, finish);
    }
    return total;
}

function browserMeasurements(kind, report) {
    const profile = report.config.metadata.profile;
    const budget = inventory.suites[kind].budgets[profile];
    const tests = [];
    function visit(suite) {
        for (const spec of suite.specs || []) {
            for (const test of spec.tests) tests.push({ spec, test });
        }
        for (const child of suite.suites || []) visit(child);
    }
    for (const suite of report.suites) visit(suite);
    const logical = new Set(tests.map(({ spec }) => `${spec.file}:${spec.title}`)).size;
    if (report.errors?.length || report.stats.unexpected || report.stats.skipped || report.stats.flaky || tests.length !== budget.executions || logical !== budget.logical)
        throw new Error("Incomplete or unsuccessful browser profile cannot be acceptance evidence");
    if (!Number.isFinite(budget.runner_seconds) || budget.runner_seconds <= 0 || !Number.isFinite(report.stats.duration) || report.stats.duration < 0 || report.stats.duration > budget.runner_seconds * 1000)
        throw new Error(`${kind}/${profile} browser runner exceeds its reviewed wall-time budget: ${report.stats.duration}ms (limit ${budget.runner_seconds * 1000}ms)`);
    const phases = { startup: [], shutdown: [] };
    let testDurationSum = 0;
    for (const { test } of tests) {
        if (test.results.length !== 1 || test.results[0].status !== "passed")
            throw new Error("Retried or unexecuted browser test cannot be acceptance evidence");
        const result = test.results[0];
        testDurationSum += result.duration;
        for (const output of result.stdout || []) {
            for (const line of (output.text || "").split("\n")) {
                if (!line.startsWith("BRAIDPLANE_FIXTURE_TIMING ")) continue;
                const event = JSON.parse(line.slice("BRAIDPLANE_FIXTURE_TIMING ".length));
                if (!event.success || !phases[event.phase] || !Number.isFinite(event.started_at_ms) || !Number.isFinite(event.elapsed_ms) || event.elapsed_ms < 0)
                    throw new Error("Invalid fixture timing evidence");
                phases[event.phase].push([event.started_at_ms, event.started_at_ms + event.elapsed_ms]);
            }
        }
    }
    const artifacts = { png: { count: 0, bytes: 0 }, zip: { count: 0, bytes: 0 }, webm: { count: 0, bytes: 0 } };
    function scan(directory) {
        for (const entry of readdirSync(directory, { withFileTypes: true })) {
            const path = join(directory, entry.name);
            if (entry.isDirectory()) scan(path);
            else if (entry.isFile()) {
                const type = entry.name.split(".").at(-1);
                if (artifacts[type]) { artifacts[type].count++; artifacts[type].bytes += statSync(path).size; }
            }
        }
    }
    for (const directory of new Set(report.config.projects.map((project) => project.outputDir))) scan(directory);
    const setup = report.config.metadata.setup_wall_ms;
    if (!Number.isFinite(setup) || setup < 0) throw new Error("Missing setup timing evidence");
    return {
        kind, profile, logical,
        executions: tests.length, projects: report.config.projects.map((project) => project.name), workers: report.config.workers,
        setup_wall_ms: setup, runner_wall_ms: report.stats.duration,
        fixture_startup_union_ms: unionMilliseconds(phases.startup), fixture_shutdown_union_ms: unionMilliseconds(phases.shutdown),
        test_duration_sum_ms: testDurationSum, artifacts,
    };
}

if (require.main === module) {
    const [kind, path] = process.argv.slice(2);
    console.log(JSON.stringify(browserMeasurements(kind, JSON.parse(readFileSync(path, "utf8"))), null, 2));
}
module.exports = { browserMeasurements, unionMilliseconds };
