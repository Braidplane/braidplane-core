// @vitest-environment node
import { createRequire } from "node:module";
import { describe, expect, it, vi } from "vitest";

const { startSSRFixture, stopSSRFixture, withSSRFixture } = createRequire(import.meta.url)(
    "../../../e2e/support/ssr-fixture.cjs",
);

describe("SSR fixture lifecycle", () => {
    it("records credential-free startup and shutdown timing in browser worker output", async () => {
        vi.stubEnv("TEST_WORKER_INDEX", "0");
        const log = vi.spyOn(console, "log").mockImplementation(() => {});
        try {
            const fixture = await startSSRFixture(process.execPath, {
                args: ["-e", 'console.log("http://127.0.0.1:12345"); setInterval(() => {}, 1000);'],
            });
            await stopSSRFixture(fixture);
            const events = log.mock.calls.map((call) => JSON.parse(call[1]));
            expect(events.map((event) => event.phase)).toEqual(["startup", "shutdown"]);
            for (const event of events) {
                expect(event.success).toBe(true);
                expect(event.elapsed_ms).toBeGreaterThanOrEqual(0);
                expect(event.started_at_ms).toBeGreaterThan(0);
                expect(Object.keys(event).sort()).toEqual([
                    "binary",
                    "elapsed_ms",
                    "phase",
                    "started_at_ms",
                    "success",
                ]);
            }
        } finally {
            log.mockRestore();
            vi.unstubAllEnvs();
        }
    });
    it("reads a complete URL across stdout chunks and awaits child shutdown", async () => {
        const fixture = await startSSRFixture(process.execPath, {
            args: [
                "-e",
                'process.stdout.write("http://127.0."); setImmediate(() => process.stdout.write("0.1:12345\\n")); setInterval(() => {}, 1000);',
            ],
        });
        try {
            expect(fixture.url).toBe("http://127.0.0.1:12345");
        } finally {
            await stopSSRFixture(fixture);
        }
        expect(fixture.process.signalCode).toBe("SIGTERM");
        await expect(stopSSRFixture(fixture)).resolves.toBeUndefined();
    });

    it("does not wait for inherited stdout after the child exits", async () => {
        const fixture = await startSSRFixture(process.execPath, {
            args: [
                "-e",
                'const { spawn } = require("node:child_process"); spawn(process.execPath, ["-e", "setTimeout(() => {}, 1000)"], { stdio: ["ignore", "inherit", "ignore"] }).unref(); process.stdout.write("http://127.0.0.1:12345\\n");',
            ],
        });
        await fixture.exited;
        let timer;
        try {
            await expect(
                Promise.race([
                    stopSSRFixture(fixture),
                    new Promise((_, reject) => {
                        timer = setTimeout(() => reject(new Error("waited for inherited stdout")), 250);
                    }),
                ]),
            ).resolves.toBeUndefined();
        } finally {
            clearTimeout(timer);
        }
        expect(fixture.process.exitCode).toBe(0);
    });

    it("reports an early exit instead of waiting for the startup timeout", async () => {
        await expect(startSSRFixture(process.execPath, { args: ["-e", "process.exit(7)"] })).rejects.toThrow(
            "exited before announcing its URL (7)",
        );
    });

    it("reports a missing executable promptly", async () => {
        await expect(startSSRFixture("/nonexistent/braidplane-ssr-fixture")).rejects.toThrow("ENOENT");
    });

    it("bounds startup when a child never announces a URL", async () => {
        await expect(
            startSSRFixture(process.execPath, {
                args: ["-e", "setInterval(() => {}, 1000)"],
                startupTimeout: 100,
            }),
        ).rejects.toThrow("startup timed out");
    });

    it("kills and reaps a child that ignores graceful shutdown", async () => {
        const fixture = await startSSRFixture(process.execPath, {
            args: [
                "-e",
                'process.on("SIGTERM", () => {}); console.log("http://127.0.0.1:12345"); setInterval(() => {}, 1000);',
            ],
            shutdownTimeout: 100,
        });
        await expect(stopSSRFixture(fixture)).rejects.toThrow("did not stop within 100ms");
        expect(fixture.process.signalCode).toBe("SIGKILL");
        await expect(fixture.closed).resolves.toEqual({ code: null, signal: "SIGKILL" });
    });

    it("reports a failing fixture exit after readiness", async () => {
        const fixture = await startSSRFixture(process.execPath, {
            args: [
                "-e",
                'process.on("SIGTERM", () => { console.error("synthetic cleanup failure"); process.exit(7); }); console.log("http://127.0.0.1:12345"); setInterval(() => {}, 1000);',
            ],
        });
        await expect(stopSSRFixture(fixture)).rejects.toThrow(
            "exited with code 7\nhttp://127.0.0.1:12345\nsynthetic cleanup failure",
        );
    });

    it("retains the startup failure when forced cleanup also fails", async () => {
        const error = await startSSRFixture(process.execPath, {
            args: ["-e", 'process.on("SIGTERM", () => {}); console.log("invalid-url"); setInterval(() => {}, 1000);'],
            shutdownTimeout: 100,
        }).catch((error) => error);
        expect(error).toBeInstanceOf(AggregateError);
        expect(error.cause.message).toContain("Invalid URL");
        expect(error.errors[1].message).toContain("did not stop within 100ms");
    });

    it("kills descendants holding output pipes after the fixture parent exits", async () => {
        const fixture = await startSSRFixture(process.execPath, {
            args: [
                "-e",
                `const { spawn } = require("node:child_process");
                const child = spawn(process.execPath, ["-e", 'process.on("SIGTERM", () => {}); process.send("ready"); setInterval(() => {}, 1000);'], { stdio: ["ignore", "inherit", "inherit", "ipc"] });
                child.once("message", () => console.log("http://127.0.0.1:12345"));`,
            ],
            shutdownTimeout: 100,
        });
        await expect(stopSSRFixture(fixture)).rejects.toThrow("did not stop within 100ms");
        await expect(fixture.closed).resolves.toEqual({ code: null, signal: "SIGTERM" });
    });

    it("cleans up a failed journey without replacing its error", async () => {
        const primary = new Error("primary assertion");
        let fixture;
        await expect(
            withSSRFixture(
                process.execPath,
                async (owned) => {
                    fixture = owned;
                    throw primary;
                },
                { args: ["-e", 'console.log("http://127.0.0.1:12345"); setInterval(() => {}, 1000);'] },
            ),
        ).rejects.toBe(primary);
        expect(fixture.process.signalCode).toBe("SIGTERM");
    });

    it("retains both the journey and cleanup failures", async () => {
        const primary = new Error("primary assertion");
        const error = await withSSRFixture(
            process.execPath,
            async () => {
                throw primary;
            },
            {
                args: [
                    "-e",
                    'process.on("SIGTERM", () => process.exit(7)); console.log("http://127.0.0.1:12345"); setInterval(() => {}, 1000);',
                ],
            },
        ).catch((error) => error);
        expect(error).toBeInstanceOf(AggregateError);
        expect(error.cause).toBe(primary);
        expect(error.errors[1].message).toContain("exited with code 7");
    });

    it("fails a successful journey when fixture cleanup fails", async () => {
        await expect(
            withSSRFixture(
                process.execPath,
                async (fixture) => {
                    expect(fixture.url).toBe("http://127.0.0.1:12345");
                },
                {
                    args: [
                        "-e",
                        'process.on("SIGTERM", () => process.exit(7)); console.log("http://127.0.0.1:12345"); setInterval(() => {}, 1000);',
                    ],
                },
            ),
        ).rejects.toThrow("exited with code 7");
    });
});
