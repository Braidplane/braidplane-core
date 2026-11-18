const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("run steps keep recorded evidence reachable after the executed definition is pruned", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const seed = await page.request.post(`${fixture.url}/_test/run/failed?definition=pruned`);
        expect(seed.ok()).toBe(true);
        const run = `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev`;
        await visit(page, `${run}&section=steps`);
        const lineage = page.getByRole("region", { name: "Run lineage", exact: true });
        await expect(lineage).toContainText("Execution topology unavailable");
        const recorded = lineage.getByRole("link", { name: "Review recorded timeline", exact: true });
        await expect(recorded).toBeVisible();
        await tabTo(page, recorded);
        await page.keyboard.press("Enter");
        const timeline = page.locator("#run-execution-timeline");
        await expect(timeline).toBeFocused();
        await expect(timeline).toBeInViewport({ ratio: 1 });
        const events = timeline.locator("..").getByRole("list");
        await expect(events).toContainText("store");
        await expect(events).toContainText("Provider acknowledgement unavailable");
        await expect(events.getByRole("link", { name: "store", exact: true })).toHaveCount(0);
        await assess(page, testInfo, "run-pruned-definition-timeline");

        const snapshot = page.getByText("Step store", { exact: true });
        await tabTo(page, snapshot);
        await page.keyboard.press("Space");
        await expect(snapshot).toBeFocused();
        await expect(snapshot.locator("..")).toContainText("upsert-customers");
        await expect(snapshot.locator("..").getByRole("link", { name: "Inspect this step" })).toHaveCount(0);
        await assess(page, testInfo, "run-pruned-definition-snapshot");

        await visit(page, run);
        await expect(page.getByRole("link", { name: "Inspect failing step", exact: true })).toHaveCount(0);
        const overview = page.getByRole("link", { name: "Review recorded timeline", exact: true });
        await tabTo(page, overview);
        await page.keyboard.press("Enter");
        await expect(timeline).toBeFocused();
        await expect(timeline).toBeInViewport({ ratio: 1 });
    });
});

test("run timeline and sink delivery are reachable without tabbing through the graph", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const seed = async (state) =>
            expect((await page.request.post(`${fixture.url}/_test/run/${state}`)).ok()).toBe(true);
        const details = `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev`;
        const steps = `${details}&section=steps`;
        const activate = async (control) => {
            await expect(control).toBeVisible();
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };

        await seed("failed?failure=transform");
        await visit(page, details);
        await activate(page.getByRole("navigation", { name: "Run sections" }).getByRole("link", { name: "Steps" }));
        const timeline = page.getByText("Execution timeline", { exact: true });
        await activate(page.getByRole("link", { name: "Go to execution timeline", exact: true }));
        await expect(timeline).toBeFocused();
        await expect(timeline).toBeInViewport({ ratio: 1 });
        await page.keyboard.press("Space");
        const eventList = timeline.locator("..").getByRole("list");
        await expect(eventList).toBeHidden();
        await page.keyboard.press("Enter");
        await expect(eventList).toContainText("02:59:59.125 CEST (UTC+02:00)");
        await expect(eventList).toContainText("02:00:00.125 CET (UTC+01:00)");
        const times = await eventList
            .locator("time")
            .evaluateAll((elements) => elements.map((el) => Date.parse(el.dateTime)));
        expect(times).toHaveLength(5);
        expect(times).toEqual([...times].sort((first, second) => first - second));
        await expect(eventList).toContainText("amount_cents");
        await assess(page, testInfo, "run-keyboard-timeline");

        for (const [state, label, explanation] of [
            ["failed?failure=transform", "Not executed", "Blocked by a failed dependency"],
            ["succeeded?delivery=acknowledged", "Acknowledged by sink", "Rows written"],
            ["succeeded?delivery=no_op", "No-op: empty input", "No provider write was made."],
            ["failed?delivery=failed", "Failed before dispatch", "No dispatch began for this attempt."],
            ["failed?failure=s3", "Unknown delivery", "Output location was not captured for this run."],
            ["succeeded?delivery=unavailable", "Sink effects unavailable", "No events recorded."],
        ]) {
            await seed(state);
            await visit(page, steps);
            await activate(page.getByRole("link", { name: "Go to sink delivery", exact: true }));
            const delivery = page.getByRole("region", { name: "Sink delivery outcomes", exact: true });
            const heading = delivery.getByRole("heading", { name: "Sink delivery outcomes" });
            await expect(heading).toBeFocused();
            await expect(heading).toBeInViewport({ ratio: 1 });
            await expect(delivery).toContainText(label);
            if (label === "Sink effects unavailable") {
                await expect(timeline.locator("..")).toContainText(explanation);
            } else {
                const sink = delivery.getByRole("listitem");
                await expect(sink).toContainText("store");
                await expect(sink).toContainText(explanation);
                if (label === "Acknowledged by sink") {
                    await expect(sink.getByRole("term").filter({ hasText: "Rows written" })).toBeVisible();
                    await expect(sink).toContainText("A sink acknowledgement is not independent delivery readback.");
                }
            }
            await assess(page, testInfo, `run-sink-${state}`);
        }
    });
});
