const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("Builder node summaries stay visible on hover and dismiss with Escape", async ({ page }) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        const node = page.locator('.builder-node[data-node-slug="normalize"]').getByRole("button").first();
        const summary = page.getByRole("tooltip");
        const validate = page.getByRole("button", { name: "Validate in dev", exact: true });
        await validate.hover();
        await tabTo(page, node);
        await expect(summary).toContainText("Transform Step: normalize");
        await page.waitForTimeout(250);
        await expect(summary).toBeVisible();
        await page.keyboard.press("Escape");
        await expect(summary).toBeHidden();
        await expect(node).toBeFocused();

        // Remove keyboard focus so pointer persistence is tested independently.
        await tabTo(page, page.getByRole("button", { name: "Hide properties", exact: true }));
        await node.hover({ position: { x: 10, y: 10 } });
        await expect(summary).toBeVisible();
        await summary.hover({ timeout: 2000 });
        // The transition crosses a 12px gap and outlasts the delayed leave handler.
        await page.waitForTimeout(250);
        await expect(summary).toBeVisible();
        await page.keyboard.press("Escape");
        await expect(summary).toBeHidden();

        await node.hover({ position: { x: 10, y: 10 } });
        await expect(summary).toBeVisible();
        await summary.hover();
        await validate.hover();
        await expect(summary).toBeHidden();
    });
});

test("dismissing an overlay keeps the selected Builder step", async ({ page }) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        const step = page.getByRole("button", { name: /^Transform Step normalize/ });
        const name = page.getByRole("textbox", { name: "Step name", exact: true });
        await tabTo(page, step);
        await page.keyboard.press("Enter");
        await expect(name).toBeVisible();
        const selected = await name.inputValue();

        const opener = page.getByRole("button", { name: /^Switch organization:/ });
        const menu = page.getByRole("group", { name: "Organization menu" });
        await tabTo(page, opener);
        await page.keyboard.press("Enter");
        await expect(menu).toBeVisible();
        await tabTo(page, menu.getByRole("link").first());
        await expect(name).toBeVisible();
        await page.keyboard.press("Escape");
        await expect(menu).toBeHidden();
        await expect(opener).toBeFocused();
        await expect(name).toHaveValue(selected);

        await tabTo(page, step);
        const summary = page.getByRole("tooltip");
        await expect(summary).toContainText("Transform Step: normalize");
        await page.keyboard.press("Escape");
        await expect(summary).toBeHidden();
        await expect(step).toBeFocused();
        await expect(name).toHaveValue(selected);
    });
});
