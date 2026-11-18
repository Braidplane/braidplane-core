const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("connected runs expose outcomes and recorded attempts without using the graph", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/retry_success`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=connected&lineage_step=load-orders`,
        );
        const list = page.getByRole("list", { name: "Connected logical runs and outcomes" });
        const graph = page.getByRole("region", { name: "Connected historical runs graph" });
        await expect(list).toBeVisible();
        const parent = list.getByRole("listitem").filter({ hasText: "run-inspection" });
        const child = list.getByRole("listitem").filter({ hasText: "completion-child" });
        await expect(parent).toContainText("Closed · succeeded");
        await expect(parent).toContainText("Recorded attempts: 2");
        await expect(child).toContainText("Closed · failed");
        await expect(child).toContainText("Recorded attempts: 1");
        await expect(parent.getByRole("link")).toHaveAttribute("aria-current", "true");
        await expect(list.getByRole("link")).toHaveCount(2);
        const target = child.getByRole("link");
        if (await graph.isVisible()) {
            const skip = page.getByRole("link", { name: "Skip graph to connected runs list", exact: true });
            await tabTo(page, skip);
            await page.keyboard.press("Enter");
            await expect(list).toBeFocused();
            await page.keyboard.press("Tab");
            await expect(target).toBeFocused();
            const hrefs = (loc) => loc.evaluateAll((links) => links.map((link) => link.getAttribute("href")));
            expect(await hrefs(list.getByRole("link"))).toEqual(await hrefs(graph.locator("a[data-entity-focus]")));
        } else {
            await tabTo(page, target);
        }
        await page.keyboard.press("Enter");
        const evidence = page.getByRole("complementary", { name: "Selected connected run evidence" });
        await expect(evidence).toBeFocused();
        await expect(evidence).toContainText("Closed · failed");
        await expect(evidence.getByRole("link", { name: "1 · completion-child" })).toBeVisible();
        await expect(page).toHaveURL(/#connected-run-evidence$/);
        await expect(page).toHaveURL(/lineage_step=load-orders/);
        await expect(page.getByRole("region", { name: "Connected dispatch decisions" })).toContainText(
            "From daily-sync to normalize-orders",
        );
        await assess(page, testInfo, "connected-run-list-selected");

        const singleRun = evidence.getByRole("link", { name: "Open single-run steps" });
        await tabTo(page, singleRun);
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/runs\/completion-child\?.*section=steps/);
    });
});

test("current subscriptions expose direction and trigger links without using the graph", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/changed`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync?environment_slug=dev&section=connections`,
        );
        const graph = page.getByRole("region", { name: "Current pipeline connections graph" });
        const list = page.getByRole("list", { name: "Current completion subscriptions" });
        const subscription = list.getByRole("listitem");
        await expect(subscription).toContainText(/From\s+daily-sync\s+to\s+normalize-orders/i);
        await expect(subscription).toContainText("Disabled");
        await expect(subscription).toContainText("Success");
        await expect(subscription).toContainText("Revision 9");
        await expect(subscription.getByRole("link")).toHaveCount(3);
        const trigger = subscription.getByRole("link", { name: "after-upstream", exact: true });
        if (await graph.isVisible()) {
            const skip = page.getByRole("link", { name: "Skip graph to subscription list", exact: true });
            await tabTo(page, skip);
            await page.keyboard.press("Enter");
            await expect(list).toBeFocused();
            expect(await trigger.getAttribute("href")).toBe(
                await graph.getByRole("link", { name: /Trigger after-upstream/ }).getAttribute("href"),
            );
        }
        await tabTo(page, trigger);
        await assess(page, testInfo, "current-subscription-list");
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/schedules\/after-upstream/);
    });
});

test("recorded attempt details can be scrolled by keyboard", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/retry_success`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=completion`,
        );
        const details = page.getByRole("region", { name: "Recorded attempt details" });
        const attempts = details.getByRole("table", { name: "Recorded attempts" });
        await expect(attempts.getByRole("row")).toHaveCount(3);
        await tabTo(page, details);
        if (page.viewportSize().width === 320) {
            expect(await details.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true);
            await page.keyboard.press("ArrowRight");
            await expect.poll(() => details.evaluate((element) => element.scrollLeft)).toBeGreaterThan(0);
        }
        await assess(page, testInfo, "recorded-attempt-details");
        await page.keyboard.press("Tab");
        await expect(attempts.getByRole("link").first()).toBeFocused();
    });
});

test("connected run refresh keeps focus on an existing list link", async ({ page }) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/waiting`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=connected`,
        );
        const list = page.getByRole("list", { name: "Connected logical runs and outcomes" });
        const root = list.getByRole("listitem").filter({ hasText: "run-inspection" });
        await expect(root).toContainText("Retry waiting");
        const link = root.getByRole("link");
        const skip = page.getByRole("link", { name: "Skip graph to connected runs list" });
        if (await skip.isVisible()) {
            await tabTo(page, skip);
            await page.keyboard.press("Enter");
        }
        await tabTo(page, link);
        expect((await page.request.post(`${fixture.url}/_test/completion/success`)).ok()).toBe(true);
        await expect(root).toContainText("Closed · succeeded", { timeout: 10000 });
        await expect(link).toBeFocused();
    });
});
