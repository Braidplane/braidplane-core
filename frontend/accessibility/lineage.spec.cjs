const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("run lineage steps remain available without using the graph", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const setRunningStep = async (step) =>
            expect((await page.request.post(`${fixture.url}/_test/run/running?phase=${step}`)).ok()).toBe(true);
        await setRunningStep("fetch");
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`,
        );

        const list = page.getByRole("list", { name: "Run lineage steps", exact: true });
        const toggle = page.getByText("Lineage steps", { exact: true });
        if (await toggle.isVisible()) {
            await tabTo(page, toggle);
            await page.keyboard.press("Enter");
        }
        await expect(list).toBeVisible();
        const hrefs = (links) =>
            links.evaluateAll((elements) => elements.map((element) => element.getAttribute("href")));
        const graphSteps = page.locator('a[id^="lineage-graph-step-"]');
        await expect(graphSteps).toHaveCount(3);
        expect(await hrefs(list.getByRole("link"))).toEqual(await hrefs(graphSteps));
        await expect(list).toContainText("Running");
        await expect(list).toContainText("Pending");
        await expect(list).toContainText("From");
        const step = list.getByRole("link", { name: /normalize/i });
        await tabTo(page, step);

        await setRunningStep("normalize");
        const progress = page.getByRole("region", { name: "Live execution progress" });
        await expect(progress).toContainText("Running step normalize");
        await expect(step).toBeFocused();
        await expect(step.locator("xpath=ancestor::li")).toContainText("Running");
        await assess(page, testInfo, "run-lineage-step-list");

        await page.keyboard.press("Enter");
        const evidence = page.getByRole("complementary", { name: "Selected step evidence" });
        await expect(evidence).toBeFocused();
        await expect(page).toHaveURL(/lineage_step=normalize/);
        await expect(evidence).toContainText("Duration");
    });
});

test("run lineage exposes failed dependencies and recorded evidence by keyboard", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect(
            (
                await page.request.post(`${fixture.url}/_test/run/failed?failure=transform&timezone=Europe%2FWarsaw`)
            ).ok(),
        ).toBe(true);
        await visit(page, `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev`);
        const sections = page.getByRole("navigation", { name: "Run sections" });
        await tabTo(page, sections.getByRole("link", { name: "Steps", exact: true }));
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/section=steps/);

        const dependencies = page
            .locator("details")
            .filter({ has: page.getByText("Lineage dependencies", { exact: true }) });
        await tabTo(page, dependencies.getByText("Lineage dependencies", { exact: true }));
        await page.keyboard.press("Enter");
        const failed = dependencies.getByRole("link", { name: /Failed input boundary: fetch to normalize via/ });
        await expect(failed).toBeVisible();
        const graph = page.getByRole("region", { name: "Run lineage graph" });
        if (await graph.isVisible()) {
            const graphLink = graph.getByRole("link", { name: await failed.textContent(), exact: true });
            await expect(graphLink).toHaveAttribute("href", await failed.getAttribute("href"));
        }
        const getLinkDetails = (links) =>
            links.evaluateAll((elements) =>
                elements.map((link) => [
                    link.getAttribute("href"),
                    link.getAttribute("aria-label") || link.textContent,
                ]),
            );
        expect(await getLinkDetails(dependencies.getByRole("link"))).toEqual(
            await getLinkDetails(page.locator('a[id^="lineage-graph-dependency-"]')),
        );
        await assess(page, testInfo, "run-lineage-dependency-list");

        await tabTo(page, failed);
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/lineage_step=normalize/);
        const evidence = page.getByRole("complementary", { name: "Selected step evidence" });
        await expect(evidence).toBeFocused();
        await expect(evidence).toContainText("amount_cents");
        await expect(evidence).toContainText("From customers, fetch");
        const configuration = evidence.getByRole("group", { name: "Current configuration", exact: true });
        await expect(configuration.getByRole("link", { name: "Edit current pipeline in Builder" })).toBeVisible();
        const diagnostic = evidence.getByText("Technical diagnostic", { exact: true });
        await tabTo(page, diagnostic);
        await page.keyboard.press("Space");
        await expect(evidence.getByText(/wrapped diagnostic/)).toBeVisible();
        await assess(page, testInfo, "run-lineage-failed-dependency-evidence");

        const contract = evidence.getByRole("link", { name: "Inspect executed step & contract", exact: true });
        await tabTo(page, contract);
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/section=definition/);
        const inspector = page.locator("[data-definition-inspector]");
        await expect(inspector).toContainText("Selected step: normalize");
        await expect(inspector).toContainText("Executed version");
        await assess(page, testInfo, "run-lineage-executed-contract");
    });
});

test("branched run lineage exposes dependencies, outcomes and timing by keyboard", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/succeeded?topology=branched`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`,
        );
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        const stepToggle = page.getByText("Lineage steps", { exact: true });
        if (await stepToggle.isVisible()) await activate(stepToggle);

        const steps = page.getByRole("list", { name: "Run lineage steps" });
        const graphSteps = page.locator('a[id^="lineage-graph-step-"]');
        const hrefs = (links) => links.evaluateAll((elements) => elements.map((link) => link.getAttribute("href")));
        await expect(steps.getByRole("listitem")).toHaveCount(6);
        expect(await hrefs(steps.getByRole("link"))).toEqual(await hrefs(graphSteps));
        const combine = steps.getByRole("listitem").filter({ hasText: "Combine supplier offers" });
        for (const item of [combine, page.locator("#lineage-graph-step-combine")]) {
            await expect(item).toContainText("Succeeded");
            await expect(item).toContainText("1.00 s");
        }
        await expect(combine).toContainText("From normalize-0, normalize-1");

        const dependencyToggle = page.getByText("Lineage dependencies", { exact: true });
        await activate(dependencyToggle);
        const dependencies = dependencyToggle.locator("..").getByRole("link");
        const graphDependencies = page.locator('a[id^="lineage-graph-dependency-"]');
        await expect(dependencies).toHaveCount(5);
        expect(await hrefs(dependencies)).toEqual(await hrefs(graphDependencies));
        const input = dependencyToggle.locator("..").getByRole("link", { name: /normalize-1 to combine.*supplier_1/ });
        await expect(
            dependencyToggle.locator("..").getByRole("link", { name: /normalize-0 to combine.*supplier_0/ }),
        ).toBeVisible();
        await assess(page, testInfo, "branched-lineage-lists");
        await activate(input);

        const evidence = page.getByRole("complementary", { name: "Selected step evidence" });
        await expect(evidence).toBeFocused();
        await expect(evidence).toContainText("From normalize-0, normalize-1");
        await expect(evidence).toContainText("1.00 s");
        await activate(evidence.getByText("Executed configuration", { exact: true }));
        await expect(evidence).toContainText("supplier_0 ← normalize-0");
        await expect(evidence).toContainText("supplier_1 ← normalize-1");
        await assess(page, testInfo, "branched-lineage-combine-evidence");

        const timeline = page.getByText("Execution timeline", { exact: true });
        await activate(page.getByRole("link", { name: "Go to execution timeline", exact: true }));
        await expect(timeline).toBeFocused();
        await expect(timeline).toBeInViewport({ ratio: 1 });
        const events = timeline.locator("..").getByRole("listitem");
        const times = (items) =>
            items.locator("time").evaluateAll((elements) => elements.map((time) => Date.parse(time.dateTime)));
        const timestamps = await times(events);
        expect(timestamps).toHaveLength(12);
        expect(timestamps).toEqual([...timestamps].sort((first, second) => first - second));
        const combineEvents = events.filter({ has: page.getByRole("link", { name: "combine", exact: true }) });
        await expect(combineEvents).toHaveCount(2);
        await expect(combineEvents.first()).toContainText(/step started/i);
        await expect(combineEvents.last()).toContainText(/step succeeded/i);
        const [started, finished] = await times(combineEvents);
        expect(finished - started).toBe(1000);
        await expect(combineEvents.locator('a[aria-current="true"]')).toHaveCount(0);
        await assess(page, testInfo, "branched-lineage-timeline");

        await activate(combineEvents.last().getByRole("link", { name: "combine", exact: true }));
        await expect(page.getByRole("heading", { name: "Inspect definition" })).toBeFocused();
        const inspector = page.locator("[data-definition-inspector]");
        await expect(inspector).toContainText("Selected step: combine");
        await expect(inspector).toContainText("Executed version");
    });
});
