// @vitest-environment node

import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const require = createRequire(import.meta.url);
const { unionMilliseconds, browserMeasurements } = require("../../../../scripts/browser-measurements.cjs");
const {
    logical,
    executions,
    runner_seconds: runnerSeconds,
} = require("../../../e2e/alpha-inventory.json").suites.product.budgets.critical;

describe("browser measurement evidence", () => {
    it("records successful counts, setup, fixture spans and artifact bytes", () => {
        const directory = mkdtempSync(join(tmpdir(), "browser-measurements-"));
        try {
            writeFileSync(join(directory, "evidence.png"), "image");
            const report = {
                config: {
                    metadata: { profile: "critical", setup_wall_ms: 12 },
                    workers: 4,
                    projects: [{ name: "chromium", outputDir: directory }],
                },
                stats: { duration: 40 },
                suites: [
                    {
                        specs: Array.from({ length: executions }, (_, i) => ({
                            file: "journey.cjs",
                            title: `case-${i % logical}`,
                            tests: [
                                {
                                    results: [
                                        {
                                            status: "passed",
                                            duration: 2,
                                            stdout: [
                                                {
                                                    text: 'BRAIDPLANE_FIXTURE_TIMING {"phase":"startup","success":true,"started_at_ms":10,"elapsed_ms":5}\n',
                                                },
                                            ],
                                        },
                                    ],
                                },
                            ],
                        })),
                    },
                ],
            };
            expect(browserMeasurements("product", report)).toMatchObject({
                logical,
                executions,
                setup_wall_ms: 12,
                runner_wall_ms: 40,
                fixture_startup_union_ms: 5,
                fixture_shutdown_union_ms: 0,
                test_duration_sum_ms: 2 * executions,
                artifacts: { png: { count: 1, bytes: 5 }, zip: { count: 0, bytes: 0 } },
            });
            const limit = runnerSeconds * 1000;
            report.stats.duration = limit;
            expect(browserMeasurements("product", report).runner_wall_ms).toBe(limit);
            report.stats.duration++;
            expect(() => browserMeasurements("product", report)).toThrow(
                "product/critical browser runner exceeds its reviewed wall-time budget",
            );
            report.stats.duration = 40;
            report.suites[0].specs[0].tests[0].results[0].stdout[0].text =
                'BRAIDPLANE_FIXTURE_TIMING {"phase":"startup","success":false,"started_at_ms":10,"elapsed_ms":5}';
            expect(() => browserMeasurements("product", report)).toThrow("Invalid fixture timing");
        } finally {
            rmSync(directory, { recursive: true });
        }
    });
    it("counts overlapping fixture intervals as elapsed spans rather than summed durations", () => {
        expect(
            unionMilliseconds([
                [10, 30],
                [0, 20],
                [40, 45],
            ]),
        ).toBe(35);
        expect(unionMilliseconds([])).toBe(0);
    });
    it("rejects failed, skipped, flaky and unexecuted profiles before reading artifacts", () => {
        for (const stats of [{ unexpected: 1 }, { skipped: 1 }, { flaky: 1 }, {}]) {
            expect(() =>
                browserMeasurements("product", { config: { metadata: { profile: "critical" } }, suites: [], stats }),
            ).toThrow("cannot be acceptance");
        }
    });
});
