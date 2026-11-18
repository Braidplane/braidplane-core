const { test: base, expect } = require("@playwright/test");
const {
    csrfHeaders,
    deleteE2EAccount,
    deleteE2EOrganization,
    deleteE2EQuery,
    signInWithCredentials,
    signUpAndVerify,
    uniqueE2ESlug,
} = require("../e2e/support/smoke-fixtures.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

const sourceSlug = "postgresql-fixture";
const querySlug = "fixture-sql-preview";
const password = "Synthetic-source-password-123!";

const test = base.extend({
    ownedOrg: [
        async ({ page }, use, testInfo) => {
            const org = uniqueE2ESlug("a11y-postgres", testInfo);
            const email = `${org}@example.test`;
            await signUpAndVerify(page, email, password, "PostgreSQL Accessibility Fixture");
            await signInWithCredentials(page, email, password, /\/workspace/);
            const errors = [];
            try {
                const organization = await page.request.post("/api/organizations", {
                    headers: await csrfHeaders(page),
                    data: { name: "PostgreSQL Accessibility Fixture", slug: org },
                });
                expect(organization.status()).toBe(201);
                await use(org);
            } catch (error) {
                errors.push(error);
            }
            for (const cleanup of [
                () => deleteE2EQuery(page, sourceSlug, querySlug, org),
                async () => {
                    const response = await page.request.delete(`/api/organizations/${org}/data-sources/${sourceSlug}`, {
                        headers: await csrfHeaders(page),
                    });
                    expect([204, 404]).toContain(response.status());
                },
                () => deleteE2EOrganization(page, org),
                () => deleteE2EAccount(page, email),
            ]) {
                try {
                    await cleanup();
                } catch (error) {
                    errors.push(error);
                }
            }
            if (errors.length) throw new AggregateError(errors, "PostgreSQL fixture cleanup failed");
        },
        { timeout: 45_000 },
    ],
});

test.use({ storageState: { cookies: [], origins: [] }, trace: "off", screenshot: "off" });

test("PostgreSQL source connection and query preview recover with the keyboard", async ({
    page,
    ownedOrg: org,
}, testInfo) => {
    const port = process.env.POSTGRES_CONNECTOR_PORT || process.env.DB_PORT;
    expect(port, "Set the port of this runtime's synthetic PostgreSQL instance").toBeTruthy();
    const activate = async (control) => {
        await expect(control).toBeVisible();
        await tabTo(page, control);
        await page.keyboard.press("Enter");
    };
    const typeText = async (control, value) => {
        await expect(control).toBeVisible();
        await tabTo(page, control);
        await page.keyboard.press("ControlOrMeta+a");
        await page.keyboard.insertText(value);
    };
    await visit(page, `/orgs/${org}/data-sources`);
    await activate(page.getByRole("button", { name: "Add Data Source", exact: true }));
    const create = page.locator("#create-data-source");
    const type = create.getByRole("combobox", { name: "Type", exact: true });
    await tabTo(page, type);
    await page.keyboard.press("p");
    await expect(type).toHaveValue("postgres");

    const sourceName = "PostgreSQL Fixture";
    const name = create.getByRole("textbox", { name: "Name", exact: true });
    const host = create.getByRole("textbox", { name: "Host", exact: true });
    const database = create.getByRole("textbox", { name: "Database", exact: true });
    const credential = create.locator("#data-source-create-database-password");
    await expect(credential).toHaveAccessibleName("Password");
    const missingDatabase = "a11y_fixture_nonexistent_database";
    await typeText(name, sourceName);
    await typeText(host, "localhost");
    await typeText(create.getByRole("spinbutton", { name: "Port", exact: true }), port);
    await typeText(database, missingDatabase);
    await typeText(create.getByRole("textbox", { name: "Username", exact: true }), "postgres");
    await typeText(credential, "postgres");
    const sslMode = create.getByRole("combobox", { name: "SSL mode", exact: true });
    const sslHelp = "Modes other than verify-full require the local development transport exception.";
    await expect(sslMode).toHaveAccessibleDescription(sslHelp);
    await tabTo(page, sslMode);
    await page.keyboard.press("Space");
    await page.keyboard.press("End");
    await page.keyboard.press("Enter");
    await expect(sslMode).toHaveValue("disable");
    const privateHosts = create.getByRole("checkbox", { name: "Allow private hosts" });
    await tabTo(page, privateHosts);
    await page.keyboard.press("Space");
    await expect(privateHosts).toBeChecked();
    const connect = create.getByRole("button", { name: "Test connection", exact: true });
    await activate(connect);
    const connectionError = create.getByRole("alert");
    await expect(connectionError).toContainText(/connection|database/i);
    await expect(connectionError).toBeFocused();
    await expect(name).toHaveValue(sourceName);
    await expect(host).toHaveValue("localhost");
    await expect(database).toHaveValue(missingDatabase);
    await expect(sslMode).toHaveValue("disable");
    await expect(privateHosts).toBeChecked();
    expect((await credential.inputValue()) === "postgres").toBe(true);
    await assess(page, testInfo, "postgres-connection-refused");

    await typeText(database, "braidplane");
    await activate(connect);
    await expect(create.getByRole("status").filter({ hasText: "Connection successful." })).toBeFocused();
    await assess(page, testInfo, "postgres-connection-ready");
    await activate(create.getByRole("button", { name: "Create data source", exact: true }));
    await expect(create).toBeHidden();
    const source = page.getByRole("link", { name: sourceName, exact: true });
    await expect(source).toBeVisible();
    await assess(page, testInfo, "postgres-source-saved");
    await activate(source);
    await activate(page.getByRole("link", { name: "Settings", exact: true }));
    const settings = page.locator('form[data-dirty-form="data-source-update"]');
    const settingsName = settings.getByRole("textbox", { name: "Name", exact: true });
    await expect(settings.getByRole("textbox", { name: "Database", exact: true })).toHaveValue("braidplane");
    await expect(settings.getByRole("combobox", { name: "SSL mode", exact: true })).toHaveAccessibleDescription(
        sslHelp,
    );
    await typeText(settingsName, "PostgreSQL Fixture Updated");
    await activate(settings.getByRole("button", { name: "Save connection settings" }));
    const saved = page.getByRole("status").filter({ hasText: "Connection settings saved." });
    await expect(saved).toBeFocused();
    await expect(settingsName).toHaveValue("PostgreSQL Fixture Updated");
    await assess(page, testInfo, "postgres-settings-saved");

    await activate(page.getByRole("link", { name: "Operations", exact: true }));
    await activate(page.getByRole("link", { name: "Open queries", exact: true }));
    await activate(page.getByRole("button", { name: "New query", exact: true }));
    const draft = page.locator("#create-query-form");
    const rowLimitHelp = "0 uses the operator’s row policy.";
    await expect(draft.getByRole("spinbutton", { name: "Row limit", exact: true })).toHaveAccessibleDescription(
        rowLimitHelp,
    );
    await typeText(draft.getByRole("textbox", { name: "Name", exact: true }), "Fixture SQL Preview");
    await typeText(
        draft.getByRole("textbox", { name: "Statement", exact: true }),
        "SELECT 1 / CAST({{divisor}} AS integer) AS a11y_marker",
    );
    await activate(draft.getByRole("button", { name: "Sync from statement", exact: true }));
    const parameters = draft.getByRole("group", { name: "Query parameter", exact: true });
    await expect(parameters.getByRole("textbox", { name: "Name", exact: true })).toHaveValue("divisor");
    const parameterType = parameters.getByRole("combobox", { name: "Type", exact: true });
    await tabTo(page, parameterType);
    await page.keyboard.press("i");
    await expect(parameterType).toHaveValue("integer");
    await assess(page, testInfo, "postgres-query-parameter-draft");
    await activate(draft.getByRole("button", { name: "Save query", exact: true }));
    await activate(page.getByRole("link", { name: "Fixture SQL Preview", exact: true }));
    const editor = page.locator("#query-update-form");
    const savedParameter = editor.getByRole("group", { name: "Query parameter" });
    await expect(savedParameter.getByRole("textbox", { name: "Name", exact: true })).toHaveValue("divisor");
    await expect(savedParameter.getByRole("combobox", { name: "Type", exact: true })).toHaveValue("integer");
    const advancedLimits = editor.getByText("Advanced limits", { exact: true });
    await activate(advancedLimits);
    await expect(editor.getByRole("spinbutton", { name: "Row limit", exact: true })).toHaveAccessibleDescription(
        rowLimitHelp,
    );
    await activate(advancedLimits);
    const divisor = page.getByRole("textbox", { name: "divisor (integer)", exact: true });
    const run = page.getByRole("button", { name: "Run query", exact: true });
    const results = page.getByRole("tabpanel", { name: "Results", exact: true });
    await typeText(divisor, "0");
    await activate(run);
    await expect(results.getByRole("alert")).toContainText(
        "Division by zero. Check values used as divisors and try again.",
    );
    await expect(page.locator("#query-execution-feedback")).toBeFocused();
    await assess(page, testInfo, "postgres-sql-execution-failure");
    await tabTo(page, page.getByRole("tab", { name: "Results", exact: true }));
    await page.keyboard.press("ArrowLeft");
    await expect(page.getByRole("tab", { name: "Definition", exact: true })).toBeFocused();
    await expect(divisor).toHaveValue("0");
    await typeText(divisor, "1");
    await activate(run);
    await expect(results.getByRole("columnheader", { name: "a11y_marker" })).toBeVisible();
    await expect(results.getByRole("cell", { name: "1", exact: true })).toBeVisible();
    await expect(page.locator("#query-execution-feedback")).toBeFocused();
    await assess(page, testInfo, "postgres-sql-preview");
    const table = results.getByRole("region", { name: "Query result table" });
    await tabTo(page, table);
    if (page.viewportSize().width === 320) {
        await page.keyboard.press("ArrowRight");
        await expect.poll(() => table.evaluate((region) => region.scrollLeft)).toBeGreaterThan(0);
    }
});
