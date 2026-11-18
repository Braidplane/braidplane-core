const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("Snowflake password setup shows relevant fields and supports a keyboard retry", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_HTTP_OAUTH_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        await visit(page, `${fixture.url}/orgs/acme/data-sources`);

        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        const enter = async (control, text) => {
            await tabTo(page, control);
            await page.keyboard.press("ControlOrMeta+a");
            await page.keyboard.insertText(text);
        };

        await activate(page.getByRole("button", { name: "Add Data Source", exact: true }));
        const form = page.locator("#create-data-source");
        const type = form.getByRole("combobox", { name: "Type", exact: true });
        await tabTo(page, type);
        await page.keyboard.press("Space");
        await page.keyboard.press("Home");
        for (let step = 0; step < 5; step++) await page.keyboard.press("ArrowDown");
        await page.keyboard.press("Enter");
        await expect(type).toHaveValue("snowflake");

        const auth = form.getByRole("combobox", { name: "Authentication", exact: true });
        const tables = form.getByRole("textbox", { name: "Source tables (SELECT)", exact: true });
        const username = form.getByRole("textbox", { name: "Username", exact: true });
        const selectAuth = async (key) => {
            await tabTo(page, auth);
            await page.keyboard.press("Space");
            await page.keyboard.press(key);
            await page.keyboard.press("Enter");
            await expect(auth).toBeFocused();
        };
        await expect(auth).toHaveValue("workload_identity");
        await expect(tables).toBeVisible();
        await enter(tables, "ORDERS");
        await selectAuth("End");
        await expect(auth).toHaveValue("password");
        await expect(tables).toHaveCount(0);
        await expect(form.locator('[name="snowflake_select_tables"]')).toBeDisabled();
        await expect(username).toBeVisible();
        await expect(form.getByRole("textbox", { name: "Role", exact: true })).toHaveAccessibleDescription(
            "Optional for password authentication. Workload identity requires a dedicated role.",
        );
        await selectAuth("Home");
        await expect(tables).toHaveValue("ORDERS");
        await expect(username).toHaveCount(0);
        await selectAuth("End");

        const password = form.getByLabel("Password", { exact: true });
        for (const [field, text] of [
            ["Name", "Synthetic Snowflake source"],
            ["Account", "synthetic-account"],
            ["Warehouse", "WAREHOUSE"],
            ["Database", "ANALYTICS"],
            ["Schema", "PUBLIC"],
            ["Username", "synthetic-refused"],
        ]) {
            await enter(form.getByRole("textbox", { name: field, exact: true }), text);
        }
        await enter(password, "synthetic-snowflake-password");
        await activate(form.getByText("Customize identifier", { exact: true }));
        await enter(form.locator('[name="slug"]'), "snowflake-password");
        const connect = form.getByRole("button", { name: "Test connection", exact: true });
        await activate(connect);
        const alert = form.getByRole("alert").filter({ hasText: "Check the Snowflake username and password." });
        await expect(alert).toBeFocused();
        await expect(alert).toBeInViewport({ ratio: 1 });
        await expect(auth).toHaveValue("password");
        await expect(form.getByRole("textbox", { name: "Account", exact: true })).toHaveValue("synthetic-account");
        await expect(password).toHaveValue("synthetic-snowflake-password");
        await assess(page, testInfo, "snowflake-password-connection-refused");

        await enter(username, "synthetic-reader");
        await activate(connect);
        await expect(form.getByRole("status").filter({ hasText: "Connection successful." })).toBeFocused();
        await expect(password).toHaveValue("synthetic-snowflake-password");
        await assess(page, testInfo, "snowflake-password-connection-succeeded");

        await activate(form.getByRole("button", { name: "Create data source", exact: true }));
        await expect(form).toBeHidden();
        await expect(page.getByRole("link", { name: "Synthetic Snowflake source", exact: true })).toBeVisible();
    });
});
