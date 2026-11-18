const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("Git SSH setup identifies required credentials and supports a keyboard retry", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_HTTP_OAUTH_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        await visit(page, `${fixture.url}/orgs/acme/data-sources`);
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        const enter = async (control, value) => {
            await tabTo(page, control);
            await page.keyboard.insertText(value);
        };
        await activate(page.getByRole("button", { name: "Add Data Source", exact: true }));
        const form = page.locator("#create-data-source");
        const type = form.getByRole("combobox", { name: "Type", exact: true });
        await tabTo(page, type);
        await page.keyboard.press("Space");
        await page.keyboard.press("End");
        await page.keyboard.press("ArrowUp");
        await page.keyboard.press("ArrowUp");
        await page.keyboard.press("Enter");
        await expect(type).toHaveValue("git");
        const auth = form.getByRole("combobox", { name: "Auth type", exact: true });
        await tabTo(page, auth);
        await page.keyboard.press("Space");
        await page.keyboard.press("End");
        await page.keyboard.press("Enter");
        await expect(auth).toHaveValue("ssh");

        const repository = form.getByRole("textbox", { name: "Git repository URL (HTTPS or SSH)", exact: true });
        const key = form.getByRole("textbox", { name: /^SSH private key/ });
        const hosts = form.getByRole("textbox", { name: /^SSH known_hosts/ });
        const connect = form.getByRole("button", { name: "Test connection", exact: true });
        await expect(key).toHaveAttribute("aria-required", "true");
        await expect(hosts).toHaveAttribute("aria-required", "true");
        await expect(hosts).toHaveAccessibleDescription("Paste the trusted known_hosts entry for the repository host.");
        await enter(form.getByRole("textbox", { name: "Name", exact: true }), "Synthetic SSH repository");
        await activate(form.getByText("Customize identifier", { exact: true }));
        await enter(form.locator('[name="slug"]'), "ssh-repository");
        await enter(repository, "ssh://git@repository.example.test/data.git");
        await enter(key, "synthetic-key-line-one\nsynthetic-key-line-two\n");
        await activate(connect);
        const refusal = form.getByRole("alert").filter({ hasText: "SSH known_hosts is required" });
        await expect(refusal).toBeFocused();
        await expect(refusal).toBeInViewport({ ratio: 1 });
        await expect(auth).toHaveValue("ssh");
        await expect(repository).toHaveValue("ssh://git@repository.example.test/data.git");
        await expect(key).toHaveValue("synthetic-key-line-one\nsynthetic-key-line-two\n");
        await assess(page, testInfo, "git-ssh-known-hosts-required");

        await enter(hosts, "repository.example.test ssh-ed25519 synthetic-host-key");
        await activate(connect);
        await expect(form.getByRole("status").filter({ hasText: "Connection successful." })).toBeFocused();
        await expect(hosts).toHaveValue("repository.example.test ssh-ed25519 synthetic-host-key");
        await assess(page, testInfo, "git-ssh-connection-test-succeeded");

        await activate(form.getByRole("button", { name: "Create data source", exact: true }));
        await expect(form).toBeHidden();
        await activate(page.getByRole("link", { name: "Synthetic SSH repository", exact: true }));
        await activate(page.getByRole("link", { name: "Settings", exact: true }));
        const rotation = page.locator('form[data-dirty-form="data-source-credentials"]');
        const newKey = rotation.getByRole("textbox", { name: /^SSH private key/ });
        const newHosts = rotation.getByRole("textbox", { name: /^SSH known_hosts/ });
        const rotate = rotation.getByRole("button", { name: "Rotate credentials", exact: true });
        const confirmRotation = async () => {
            const prompt = page.waitForEvent("dialog");
            const activation = activate(rotate);
            const dialog = await prompt;
            await dialog.accept();
            await activation;
            expect(dialog.type()).toBe("confirm");
            expect(dialog.message()).toContain("Synthetic SSH repository");
        };
        await expect(newKey).toHaveValue("");
        await expect(newHosts).toHaveAccessibleDescription(
            "Paste the trusted known_hosts entry for the repository host.",
        );
        await enter(newKey, "synthetic-replacement-key");
        await confirmRotation();
        const rotationError = rotation.getByRole("alert").filter({ hasText: "SSH known_hosts is required" });
        await expect(rotationError).toBeFocused();
        await expect(rotationError).toBeInViewport({ ratio: 1 });
        await expect(newKey).toHaveValue("synthetic-replacement-key");
        await assess(page, testInfo, "git-ssh-rotation-known-hosts-required");

        await enter(newHosts, "repository.example.test ssh-ed25519 synthetic-replacement-host-key");
        await confirmRotation();
        await expect(page.getByRole("status").filter({ hasText: "Credentials updated." })).toBeFocused();
        await expect(newKey).toHaveValue("");
        await assess(page, testInfo, "git-ssh-rotation-succeeded");
    });
});

test("HTTP source setup supports keyboard recovery and query preview", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_HTTP_OAUTH_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        await visit(page, `${fixture.url}/orgs/acme/data-sources`);
        const primary = page.getByRole("navigation", { name: "Primary" });
        if (await primary.isVisible()) {
            await expect(primary.getByRole("link", { name: "Runs requires an active project" })).toBeDisabled();
        }
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        const typeText = async (control, value) => {
            await tabTo(page, control);
            await page.keyboard.press("ControlOrMeta+a");
            await page.keyboard.insertText(value);
        };
        await activate(page.getByRole("button", { name: "Add Data Source", exact: true }));
        const create = page.locator("#create-data-source");
        const type = create.getByRole("combobox", { name: "Type", exact: true });
        await tabTo(page, type);
        await page.keyboard.press("h");
        await expect(type).toHaveValue("http");
        const name = create.getByRole("textbox", { name: "Name", exact: true });
        const baseURL = create.getByRole("textbox", { name: "HTTP base URL", exact: true });
        const healthPath = create.getByRole("textbox", { name: "HTTP health path", exact: true });
        await typeText(name, "HTTP App");
        await activate(create.getByText("Customize identifier", { exact: true }));
        await typeText(create.locator('[name="slug"]'), "http-app");
        await typeText(baseURL, "https://api.example.test");
        await typeText(healthPath, "/offline");
        const testConnection = create.getByRole("button", { name: "Test connection", exact: true });
        await activate(testConnection);
        const alert = create.getByRole("alert");
        await expect(alert).toContainText("Health endpoint is unavailable.");
        await expect(alert).toBeFocused();
        await expect(name).toHaveValue("HTTP App");
        await expect(baseURL).toHaveValue("https://api.example.test");
        await expect(create.locator('[name="slug"]')).toHaveValue("http-app");
        await assess(page, testInfo, "source-test-refused");

        await typeText(healthPath, "/health");
        await activate(testConnection);
        await expect(create.getByRole("status").filter({ hasText: "Connection successful." })).toBeFocused();
        await assess(page, testInfo, "source-test-succeeded");
        await activate(create.getByRole("button", { name: "Create data source", exact: true }));
        await expect(create).toBeHidden();
        await activate(page.getByRole("link", { name: "HTTP App", exact: true }));
        await activate(page.getByRole("link", { name: "Settings", exact: true }));
        const settings = page.locator('form[data-dirty-form="data-source-update"]');
        const settingsName = settings.getByRole("textbox", { name: "Name", exact: true });
        const settingsHealthPath = settings.getByRole("textbox", { name: "Health path", exact: true });
        const saveSettings = settings.getByRole("button", { name: "Save connection settings" });
        await typeText(settingsName, "HTTP App updated");
        await typeText(settingsHealthPath, "/unavailable");
        await activate(saveSettings);
        const settingsError = settings.getByRole("alert");
        await expect(settingsError).toHaveText("Choose an available health path.");
        await expect(settingsError).toBeFocused();
        await expect(settingsName).toHaveValue("HTTP App updated");
        await expect(settingsHealthPath).toHaveValue("/unavailable");
        await assess(page, testInfo, "source-settings-refused");

        await typeText(settingsHealthPath, "/ready");
        await activate(saveSettings);
        const saved = page.getByRole("status").filter({ hasText: "Connection settings saved." });
        await expect(saved).toBeFocused();
        await page.waitForLoadState("load");
        await expect(saved).toBeInViewport({ ratio: 1 });
        await expect(settingsName).toHaveValue("HTTP App updated");
        await expect(settingsHealthPath).toHaveValue("/ready");
        await expect(saveSettings).toBeDisabled();
        await assess(page, testInfo, "source-settings-saved");

        const setSchemaStatus = async (status) =>
            expect((await page.request.post(`${fixture.url}/_test/schema-status?status=${status}`)).ok()).toBe(true);
        await setSchemaStatus("importing");
        await page.clock.install();
        await activate(page.getByRole("link", { name: "Schema", exact: true }));
        const schema = page.getByRole("region", { name: "Schema browser", exact: true });
        const schemaHeading = schema.getByRole("heading", { name: "Schema browser", exact: true });
        const checkImport = schema.getByRole("button", { name: "Check import status", exact: true });
        await expect(schema.getByRole("status")).toContainText("Schema import is running");
        await assess(page, testInfo, "source-schema-import-pending");
        await activate(checkImport);
        await expect(schemaHeading).toBeFocused();
        await expect(checkImport).toBeVisible();
        await setSchemaStatus("ready");
        await page.clock.fastForward(6000);
        await expect(checkImport).toBeVisible();
        await activate(checkImport);
        await expect(schemaHeading).toBeFocused();
        await expect(schemaHeading).toHaveAccessibleDescription("ready");
        await expect(checkImport).toHaveCount(0);
        await activate(page.getByRole("link", { name: "Create query draft", exact: true }));
        const draft = page.locator("#create-query-form");
        const path = draft.getByRole("textbox", { name: "Path", exact: true });
        const save = draft.getByRole("button", { name: "Save query", exact: true });
        await expect(path).toHaveValue("/users/{{path_id}}");
        await typeText(path, "/users/{{path_id}}?invalid=%");
        await activate(save);
        const invalid = draft.getByRole("alert").filter({ hasText: "HTTP path query string must be valid." });
        await expect(invalid).toBeFocused();
        await expect(invalid).toBeInViewport({ ratio: 1 });
        await expect(path).toHaveValue("/users/{{path_id}}?invalid=%");
        await typeText(path, "/users/{{path_id}}");
        await activate(save);
        await activate(page.getByRole("link", { name: "getUser", exact: true }));
        const editor = page.locator("#query-update-form");
        const savedPath = editor.getByRole("textbox", { name: "Path", exact: true });
        const saveChanges = page.getByRole("button", { name: "Save", exact: true });
        await typeText(savedPath, "/users/{{path_id}}?invalid=%");
        await activate(saveChanges);
        const editError = editor.getByRole("alert").filter({ hasText: "HTTP path query string must be valid." });
        await expect(editError).toBeFocused();
        await expect(editError).toBeInViewport({ ratio: 1 });
        await expect(savedPath).toHaveValue("/users/{{path_id}}?invalid=%");
        await assess(page, testInfo, "source-query-edit-invalid");
        await typeText(savedPath, "/users/{{path_id}}");
        await typeText(editor.locator('[name="description"]'), "Find a customer.");
        await activate(saveChanges);
        await expect(page.getByText("Find a customer.", { exact: true })).toBeVisible();
        await expect(editError).toHaveCount(0);
        await expect(saveChanges).toBeDisabled();
        const results = page.getByRole("tabpanel", { name: "Results", exact: true });
        const returnToDefinition = async () => {
            await tabTo(page, page.getByRole("tab", { name: "Results", exact: true }));
            await page.keyboard.press("ArrowLeft");
            await expect(page.getByRole("tab", { name: "Definition", exact: true })).toBeFocused();
        };
        const parameter = page.getByRole("textbox", { name: "path_id (text)", exact: true });
        const run = page.getByRole("button", { name: "Run query", exact: true });
        await activate(run);
        await expect(results.getByRole("alert")).toContainText("Parameter path_id is required.");
        await expect(results.getByRole("group", { name: "Parameter path_id is required." })).toBeFocused();
        await returnToDefinition();

        await typeText(parameter, "missing");
        await activate(run);
        const failure = results.getByRole("alert");
        await expect(failure).toContainText("The customer was not found.");
        await expect(results.getByRole("group", { name: "The customer was not found." })).toBeFocused();
        await activate(failure.getByText("More details", { exact: true }));
        await expect(failure.getByText(/The synthetic HTTP endpoint returned 404/)).toBeVisible();
        await expect(failure).not.toHaveCSS("position", "fixed");
        await assess(page, testInfo, "source-query-refused");
        await returnToDefinition();
        await expect(parameter).toHaveValue("missing");
        await typeText(parameter, "customer-1");
        await activate(run);
        await expect(results).toContainText("Synthetic customer");
        await expect(page.locator("#query-execution-feedback")).toBeFocused();
        await expect
            .poll(() => results.locator("pre").evaluate((output) => output.scrollWidth - output.clientWidth))
            .toBe(0);
        await assess(page, testInfo, "source-saved-query-result");
    });
});
