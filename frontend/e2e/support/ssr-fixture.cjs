const { spawn } = require("node:child_process");
const { once } = require("node:events");
const { resolve, basename } = require("node:path");
const { kill } = require("node:process");
const { createInterface } = require("node:readline");

async function startSSRFixture(binary, { args = [], startupTimeout = 10_000, shutdownTimeout = 5_000 } = {}) {
    const started = performance.now();
    if (!binary) throw new Error("SSR fixture binary is required");
    const process = spawn(binary, args, {
        cwd: resolve(__dirname, "../../.."),
        stdio: ["ignore", "pipe", "pipe"],
        detached: true,
    });
    const exited = new Promise((resolve) => process.once("exit", (code, signal) => resolve({ code, signal })));
    const closed = new Promise((resolve) => process.once("close", (code, signal) => resolve({ code, signal })));
    const fixture = { process, exited, closed, shutdownTimeout, binary: basename(binary) };
    let output = "";
    for (const stream of [process.stdout, process.stderr]) {
        stream.on("data", (chunk) => {
            output = (output + chunk.toString()).slice(-8_192);
        });
    }
    fixture.diagnostics = () => output;
    const lines = createInterface({ input: process.stdout });
    const controller = new AbortController();
    const options = { signal: controller.signal };
    let timer;
    try {
        fixture.url = await Promise.race([
            once(lines, "line", options).then(([line]) => {
                const url = new URL(line.trim());
                if (url.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(url.hostname)) {
                    throw new Error("SSR fixture must announce a loopback HTTP URL");
                }
                return url.origin;
            }),
            once(process, "exit", options).then(([code, signal]) => {
                throw new Error(`SSR fixture exited before announcing its URL (${code ?? signal})`);
            }),
            new Promise((_, reject) => {
                timer = setTimeout(() => reject(new Error("SSR fixture URL startup timed out")), startupTimeout);
            }),
        ]);
        recordFixtureTiming(fixture, "startup", started, true);
        return fixture;
    } catch (error) {
        recordFixtureTiming(fixture, "startup", started, false);
        try {
            await stopSSRFixture(fixture);
        } catch (cleanupError) {
            throw new AggregateError([error, cleanupError], error.message, { cause: error });
        }
        throw error;
    } finally {
        clearTimeout(timer);
        controller.abort();
        lines.close();
        process.stdout.resume();
    }
}

async function stopSSRFixture(fixture) {
    if (!fixture) return;
    const started = performance.now();
    let success = false;
    const { process, closed } = fixture;
    // A reaped parent can leave owned descendants holding its output pipes.
    if (
        (process.exitCode === null && process.signalCode === null) ||
        !process.stdout.destroyed ||
        !process.stderr.destroyed
    )
        signalFixture(process, "SIGTERM");
    let timer;
    try {
        const result = await Promise.race([
            closed,
            new Promise((_, reject) => {
                timer = setTimeout(() => {
                    signalFixture(process, "SIGKILL");
                    reject(new Error(`SSR fixture did not stop within ${fixture.shutdownTimeout}ms`));
                }, fixture.shutdownTimeout);
            }),
        ]);
        if (result.code !== null && result.code !== 0) {
            throw new Error(`SSR fixture exited with code ${result.code}\n${fixture.diagnostics()}`);
        }
        if (result.signal && result.signal !== "SIGTERM") {
            throw new Error(`SSR fixture exited with signal ${result.signal}`);
        }
        success = true;
    } finally {
        clearTimeout(timer);
        // Even a failed graceful shutdown must reap the owned child.
        await closed;
        recordFixtureTiming(fixture, "shutdown", started, success);
    }
}

function recordFixtureTiming(fixture, phase, started, success) {
    if (process.env.TEST_WORKER_INDEX === undefined) return;
    console.log(
        "BRAIDPLANE_FIXTURE_TIMING",
        JSON.stringify({
            binary: fixture.binary,
            phase,
            success,
            started_at_ms: performance.timeOrigin + started,
            elapsed_ms: performance.now() - started,
        }),
    );
}

function signalFixture(process, signal) {
    if (!process.pid) return;
    try {
        // The detached group contains only this fixture and its setup/cleanup children.
        kill(-process.pid, signal);
    } catch (error) {
        if (error.code !== "ESRCH") throw error;
    }
}

async function withSSRFixture(binary, body, options) {
    const fixture = await startSSRFixture(binary, options);
    const errors = [];
    try {
        await body(fixture);
    } catch (error) {
        errors.push(error);
    }
    try {
        await stopSSRFixture(fixture);
    } catch (error) {
        errors.push(error);
    }
    if (errors.length === 1) throw errors[0];
    if (errors.length > 1) throw new AggregateError(errors, "SSR journey and cleanup failed", { cause: errors[0] });
}

module.exports = { startSSRFixture, stopSSRFixture, withSSRFixture };
