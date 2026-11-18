const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("Builder schema cache updates only after a keyboard reload", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-references`)).ok()).toBe(true);
        let reads = 0;
        let ready = false;
        await page.route("**/builder/schema?**", (route) => {
            expect(new URL(route.request().url()).searchParams.get("data_source_slug")).toBe("warehouse");
            reads++;
            return route.fulfill({
                json: ready
                    ? { status: "ready", lines: ["table customers"], datasets: [{ name: "customers", fields: [] }] }
                    : { status: "importing" },
            });
        });
        await visit(
            page,
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        await page.clock.install();
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        await activate(page.locator('.builder-node[data-node-slug="fetch"]'));
        const inlineSQL = page.getByRole("checkbox", { name: "Use inline SQL instead of a saved query", exact: true });
        expect(await inlineSQL.evaluate((input) => input.labels.length)).toBe(1);
        await activate(page.getByText("Data source schema", { exact: true }));
        const panel = page.locator("#builder-schema-panel");
        const status = panel.getByRole("status");
        await expect(status).toContainText("Schema import");
        expect(reads).toBe(1);
        ready = true;
        await page.clock.fastForward(6000);
        await expect(status).toContainText("Schema import", { timeout: 1500 });
        expect(reads).toBe(1);
        await assess(page, testInfo, "builder-schema-importing");

        const reload = panel.getByRole("button", { name: "Reload cache", exact: true });
        await activate(reload);
        await expect(status).toContainText("Cached schema");
        await expect(reload).toBeFocused();
        await expect(panel.getByRole("combobox", { name: "Schema dataset" })).toBeEnabled();
        expect(reads).toBe(2);
        await assess(page, testInfo, "builder-schema-ready");
    });
});
