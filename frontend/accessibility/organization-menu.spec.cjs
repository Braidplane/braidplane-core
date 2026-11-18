const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");
const { inspectTextContrast } = require("./contrast.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("organization switcher opens and closes explicitly by keyboard", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        const opener = page.getByRole("button", { name: /^Switch organization:/ });
        const menu = page.getByRole("group", { name: "Organization menu", exact: true });
        await tabTo(page, opener);
        await expect(menu).toBeHidden();
        await page.keyboard.press("Enter");
        await expect(menu).toBeVisible();
        await page.keyboard.press("Tab");
        await expect(menu.getByRole("link", { name: "See all organizations", exact: true })).toBeFocused();
        await menu.evaluate((element) =>
            Promise.allSettled(element.getAnimations({ subtree: true }).map((animation) => animation.finished)),
        );
        const active = '#shell-organization-menu [aria-current="true"] > span:last-child';
        const [label] = await inspectTextContrast(page, [active]);
        await testInfo.attach("active-organization-contrast", {
            body: JSON.stringify({ viewport: page.viewportSize(), label }),
            contentType: "application/json",
        });
        expect(label.status).toBe("pass");
        expect(label.minimum).toBeGreaterThanOrEqual(4.5);
        await assess(page, testInfo, "organization-menu-open");
        await page.keyboard.press("Escape");
        await expect(menu).toBeHidden();
        await expect(opener).toBeFocused();
        await page.keyboard.press("Tab");
        await expect(menu).toBeHidden();
        await expect(opener).not.toBeFocused();

        await tabTo(page, opener);
        await page.keyboard.press("Enter");
        await tabTo(page, menu.getByRole("link").last());
        await page.keyboard.press("Tab");
        await expect(menu).toBeHidden();
        await expect(page.getByRole("combobox", { name: "Switch project runtime target", exact: true })).toBeFocused();
    });
});
