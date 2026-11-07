const path = require("node:path");
const assert = require("node:assert/strict");
const { createInterface } = require("node:readline");
const root = path.resolve(__dirname, "../..");
const { startSSRFixture, stopSSRFixture } = require(path.join(root, "frontend/e2e/support/ssr-fixture.cjs"));
(async () => {
    let fixture;
    let reader;
    let stopping = false;
    const stop = () => {
        stopping = true;
        reader?.close();
    };
    process.on("SIGINT", stop);
    process.on("SIGTERM", stop);
    try {
        fixture = await startSSRFixture(path.join(root, "frontend/e2e/.auth/pipeline-git-fixture"));
        if (stopping) return;
        const start = `${fixture.url}/orgs/acme/projects/ops/pipelines?environment_slug=dev&pipeline_slug=daily-sync`;
        const cases = { a: ["healthy", "succeeded"], b: ["failed", "failed"], c: ["unconfigured", "no runs"] };
        const response = await fetch(`${fixture.url}/_test/session`);
        assert.equal(response.status, 200);
        const cookie = response.headers
            .getSetCookie()
            .map((value) => value.split(";", 1)[0])
            .join("; ");
        const prepare = async (key) => {
            assert.ok(cases[key], "Choose A, B, or C");
            const [state, outcome] = cases[key];
            assert.equal((await fetch(`${fixture.url}/_test/moderation/${state}`, { method: "POST" })).status, 204);
            const page = await fetch(start, { headers: { cookie } });
            assert.equal(page.status, 200);
            const html = await page.text();
            assert.ok(html.includes("data-entity-preview"));
            assert.ok(html.includes(outcome));
            process.stdout.write(`Case ${key.toUpperCase()} ready: ${start}\n`);
        };
        if (process.argv.includes("--preflight")) {
            for (const key of ["a", "b", "c"]) await prepare(key);
        } else {
            await prepare("a");
            if (stopping) return;
            process.stdout.write(
                "In the dedicated local browser, open " +
                    fixture.url +
                    "/_test/session once, then the case URL above.\nEnter A/B/C to prepare the next case, Q to stop. Keep this terminal running.\n",
            );
            reader = createInterface({ input: process.stdin, output: process.stdout });
            for await (const line of reader) {
                const key = line.trim().toLowerCase();
                if (key === "q") break;
                if (!cases[key]) {
                    process.stdout.write("Choose A, B, C, or Q.\n");
                    continue;
                }
                await prepare(key);
            }
        }
    } finally {
        reader?.close();
        process.removeListener("SIGINT", stop);
        process.removeListener("SIGTERM", stop);
        if (fixture) await stopSSRFixture(fixture);
    }
})().catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
});
