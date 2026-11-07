// @vitest-environment node
import { createRequire } from "node:module";
import { describe, expect, it } from "vitest";

const require = createRequire(import.meta.url);
const { browserProfile } = require("../../../e2e/support/alpha-selection.cjs");
const { collectedTests, verifySelection } = require("../../../../scripts/check-browser-inventory.cjs");
const full = [
    { file: "journey.cjs", title: "Save", project: "chromium" },
    { file: "journey.cjs", title: "Save", project: "firefox" },
    { file: "journey.cjs", title: "Recovery", project: "chromium" },
];
const manifest = () => ({
    suites: {
        product: {
            cases: [
                {
                    file: "journey.cjs",
                    title: "Save",
                    tier: "critical",
                    cross_browser: true,
                    reason: "Persisted authoring navigation",
                },
                {
                    file: "journey.cjs",
                    title: "Recovery",
                    tier: "extended",
                    reason: "Distinct browser recovery interaction",
                },
            ],
            budgets: {
                critical: { logical: 1, executions: 2, runner_seconds: 60 },
                full: { logical: 2, executions: 3, runner_seconds: 120 },
            },
        },
    },
});

describe("Alpha browser selection", () => {
    it("keeps the Community critical browser budget within four minutes", () => {
        const inventory = require("../../../e2e/alpha-inventory.json");
        expect(inventory.suites.product.budgets.critical.runner_seconds).toBeLessThanOrEqual(240);
        expect(Object.keys(inventory.suites)).toEqual(["product"]);
    });
    it("requires an explicit valid CI profile", () => {
        expect(browserProfile({})).toBe("critical");
        expect(() => browserProfile({ CI: "1" })).toThrow("required in CI");
        expect(() => browserProfile({ BRAIDPLANE_BROWSER_PROFILE: "typo" })).toThrow();
        expect(browserProfile({ CI: "1", BRAIDPLANE_BROWSER_PROFILE: "full" })).toBe("full");
    });
    it("collects nested projects and proves the complete retained smoke", () => {
        const tests = collectedTests({
            suites: [
                {
                    suites: [
                        {
                            specs: [
                                {
                                    file: "journey.cjs",
                                    title: "Save",
                                    tests: [{ projectName: "chromium" }, { projectName: "firefox" }],
                                },
                            ],
                        },
                    ],
                },
            ],
        });
        expect(verifySelection("product", full, tests, manifest()).critical).toEqual({ logical: 1, executions: 2 });
        expect(() => verifySelection("product", full, tests.slice(0, 1), manifest())).toThrow("omitted");
    });
    it("rejects unrecorded growth, missing justification and budget growth", () => {
        expect(() =>
            verifySelection(
                "product",
                [...full, { file: "new.cjs", title: "New", project: "chromium" }],
                full.slice(0, 2),
                manifest(),
            ),
        ).toThrow("cases changed");
        const missing = manifest();
        missing.suites.product.cases[0].reason = "";
        expect(() => verifySelection("product", full, full.slice(0, 2), missing)).toThrow("unjustified");
        const budget = manifest();
        budget.suites.product.budgets.full.executions = 2;
        expect(() => verifySelection("product", full, full.slice(0, 2), budget)).toThrow("budget");
        budget.suites.product.budgets.full.runner_seconds = 0;
        expect(() => verifySelection("product", full, full.slice(0, 2), budget)).toThrow("invalid reviewed budget");
    });
});
