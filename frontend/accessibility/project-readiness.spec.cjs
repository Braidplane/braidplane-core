const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("project readiness exposes environment status and keyboard routes to fixes", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(fixture.url + "/_test/session")).ok()).toBe(true);
        const overview = fixture.url + "/organizations/acme/projects/ops";
        await visit(page, overview);
        await expect(page.getByText("Pipeline readiness is unknown.", { exact: false })).toBeVisible();
        await assess(page, testInfo, "project-readiness-unavailable");

        expect((await page.request.post(fixture.url + "/_test/project-readiness")).ok()).toBe(true);
        await visit(page, overview);
        await expect(page.getByRole("heading", { name: "dev ready" })).toBeVisible();
        await expect(page.getByRole("heading", { name: "staging blocked" })).toBeVisible();
        await expect(page.getByText(/Create the reference with braidplane data-source-ref create/)).toBeVisible();
        await assess(page, testInfo, "project-readiness-mixed");

        for (const [name, destination] of [
            [
                "Missing bindings (1)",
                page.locator("#project-binding-matrix").getByText(/missing project environment bindings/),
            ],
            [
                "Missing organization variables (1)",
                page.getByRole("region", { name: "Runtime variable comparison" }).getByText("region", { exact: true }),
            ],
            [
                "daily-sync",
                page
                    .getByRole("region", { name: "Requirements in staging" })
                    .getByRole("rowheader", { name: /source/ }),
            ],
        ]) {
            const link = page.getByRole("main").getByRole("link", { name, exact: true });
            await tabTo(page, link);
            await page.keyboard.press("Enter");
            await expect(destination).toBeVisible();
            await visit(page, overview);
        }
    });
});
