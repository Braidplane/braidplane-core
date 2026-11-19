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

        expect((await page.request.post(fixture.url + "/_test/deployment-access?read_only=true&multiple_targets=true")).ok()).toBe(true);
        await visit(page, overview);
        await expect(page.getByRole("heading", { name: "dev ready" })).toBeVisible();
        await expect(page.getByRole("heading", { name: "staging blocked" })).toBeVisible();
        await assess(page, testInfo, "project-readiness-mixed");

        for (const name of ["Missing bindings (1)", "Missing organization variables (1)", "daily-sync"]) {
            const link = page.getByRole("main").getByRole("link", { name, exact: true });
            await tabTo(page, link);
            const destination = new URL(await link.getAttribute("href"), overview).href;
            await page.keyboard.press("Enter");
            await expect(page).toHaveURL(destination);
            await visit(page, overview);
        }
    });
});
