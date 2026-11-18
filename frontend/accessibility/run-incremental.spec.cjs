const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("run checkpoint diagnostics identify each source and keep long evidence readable", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const run = `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev`;
        const sourceSlug = "fetch-partitioned-customer-orders-from-primary-warehouse";
        const checkpoint = "synthetic".repeat(8);
        const setRun = async (state) =>
            expect((await page.request.post(`${fixture.url}/_test/run/failed?checkpoint=${state}`)).ok()).toBe(true);
        await setRun("recorded");
        await visit(page, run);
        const attempts = page.getByRole("link", { name: "Attempts & recovery", exact: true });
        await tabTo(page, attempts);
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/section=attempts/);
        const region = page.getByRole("region", { name: "Incremental diagnostics", exact: true });
        const checkpointText = page.getByText(checkpoint, { exact: true });
        const textFits = async (target) =>
            target.evaluate((element) => {
                const range = document.createRange();
                range.selectNodeContents(element);
                const limit = Math.min(element.closest("li, .rounded-lg").getBoundingClientRect().right, innerWidth);
                return Math.max(...Array.from(range.getClientRects(), (rect) => rect.right)) <= limit + 1;
            });
        expect(await textFits(checkpointText)).toBe(true);
        expect(await textFits(page.getByText(sourceSlug, { exact: true }))).toBe(true);
        await expect(region.getByRole("heading", { level: 3, name: "Incremental diagnostics" })).toBeVisible();
        await expect(region.getByText("Replay blocked: Verify the provider outcome before retrying.")).toBeVisible();
        const sources = region.getByRole("listitem");
        await expect(sources).toHaveCount(2);
        const warehouse = sources.filter({ has: page.getByRole("heading", { level: 4, name: sourceSlug }) });
        await expect(warehouse).toContainText("Blocked by store: Sink acknowledgement is unavailable.");
        await expect(warehouse.getByRole("term")).toHaveText([
            "Previous checkpoint",
            "Source window",
            "Observed checkpoint",
        ]);
        await expect(warehouse.getByRole("definition")).toHaveText([
            "2026-10-01",
            "2026-10-01 to 2026-10-02",
            checkpoint,
        ]);
        const cursor = sources.filter({ has: page.getByRole("heading", { level: 4, name: "fetch-updates" }) });
        await expect(cursor).toContainText("redacted cursor");
        await expect(cursor).toContainText("not observed");
        await assess(page, testInfo, "run-checkpoint-sources-recorded");

        await setRun("empty");
        await visit(page, `${run}&section=attempts`);
        await expect(region).toContainText("No incremental source diagnostics recorded.");
        await expect(region.getByRole("listitem")).toHaveCount(0);
        await assess(page, testInfo, "run-checkpoint-sources-unrecorded");

        await setRun("");
        await visit(page, `${run}&section=attempts`);
        await expect(region).toHaveCount(0);
        await expect(page.getByText("Incremental diagnostics", { exact: true })).toHaveCount(0);
    });
});
