const { test, expect, captureE2EEvidenceScreenshot } = require("./support/smoke-fixtures.cjs");
const { startSSRFixture, stopSSRFixture } = require("./support/ssr-fixture.cjs");
let fixture;
test.beforeAll(async () => {
    fixture = await startSSRFixture(process.env.E2E_HTTP_OAUTH_FIXTURE_BIN);
});
test.afterAll(async () => stopSSRFixture(fixture));

// Each SSR fixture owns its session store; application cookies belong to a different server.
test.use({ storageState: { cookies: [], origins: [] } });

test("preserves sign-in feedback and email with JavaScript disabled", async ({ browser }) => {
    const context = await browser.newContext({ javaScriptEnabled: false });
    try {
        const page = await context.newPage();
        await page.goto(`${fixture.url}/auth/login`);
        await page.getByLabel("Email", { exact: true }).fill("operator@example.com");
        await page.getByLabel("Password", { exact: true }).fill("synthetic-password");
        await page.getByRole("button", { name: "Sign in", exact: true }).click();
        await expect(page.locator("#login-feedback")).toContainText("Sign in failed");
        await expect(page.locator("#login-feedback")).toBeFocused();
        await expect(page.getByLabel("Email", { exact: true })).toHaveValue("operator@example.com");
        await expect(page.getByLabel("Password", { exact: true })).toHaveValue("");
    } finally {
        await context.close();
    }
});

// Keep this validation/redirect regression at the browser layer.
test("keeps OAuth validation visible and focuses the invalid field before redirecting", async ({ page }, testInfo) => {
    await page.request.get(`${fixture.url}/_test/session`);
    await page.goto(`${fixture.url}/orgs/acme/data-sources`);
    await page.getByRole("button", { name: "Add Data Source", exact: true }).click();
    const create = page.locator("#create-data-source");
    await create.locator('[name="type"]').selectOption("hubspot");
    await create.getByText("Customize identifier", { exact: true }).click();
    await create.locator('[name="slug"]').fill("hubspot-crm");
    await create.getByRole("button", { name: "Connect HubSpot", exact: true }).click();
    const name = create.locator('[name="name"]');
    await expect(create.locator("#data-source-create-feedback")).toContainText("name is required");
    await expect(name).toBeFocused();
    await expect(name).toHaveAttribute("aria-errormessage", "data-source-create-feedback");
    await expect(create.locator('[name="slug"]')).toHaveValue("hubspot-crm");
    await expect(create.getByRole("button", { name: "Connect HubSpot", exact: true })).toBeEnabled();
    await captureE2EEvidenceScreenshot(page, testInfo, "form-validation-desktop.png", { fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await captureE2EEvidenceScreenshot(page, testInfo, "form-validation-compact.png", { fullPage: true });
    await name.fill("HubSpot CRM");
    const timeout = create.locator('[name="timeout_seconds"]:enabled');
    await timeout.fill("0");
    await create.getByRole("button", { name: "Connect HubSpot", exact: true }).click();
    await expect(create.locator("#data-source-create-feedback")).toContainText("timeout seconds");
    await expect(timeout).toBeFocused();
    await expect(timeout).toHaveAttribute("aria-errormessage", "data-source-create-feedback");
    await expect(name).toHaveValue("HubSpot CRM");
    await timeout.fill("15");
    await create.getByRole("button", { name: "Connect HubSpot", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Synthetic OAuth consent" })).toBeVisible();
});

test("sets up HTTP OAuth and reconnects after replacing the app grant type", async ({ page }, testInfo) => {
    expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
    await page.goto(`${fixture.url}/orgs/acme/data-sources?type=http`);
    await page.getByRole("button", { name: "Add Data Source", exact: true }).click();
    const create = page.locator("#create-data-source");
    await create.locator('[name="type"]').selectOption("http");
    await create.locator('[name="name"]').fill("HTTP OAuth app");
    await create.getByText("Customize identifier", { exact: true }).click();
    await create.locator('[name="slug"]').fill("http-app");
    await create.locator('[name="base_url"]').fill("https://api.example.com/v1");
    await create.locator('[name="http_auth_type"]').selectOption("client_credentials");
    await create.locator('[name="http_oauth_issuer"]').fill("https://auth.example.com");
    await create.locator('[name="http_oauth_token_endpoint"]').fill("https://auth.example.com/token");
    await create.locator('[name="http_oauth_client_id"]').fill("synthetic-app");
    await expect(create.locator('[name="bearer_token"]')).toBeDisabled();
    await expect(create.locator('[name="username"]').filter({ visible: true })).toHaveCount(0);
    await expect(create.locator('[name="password"]').filter({ visible: true })).toHaveCount(0);
    await captureE2EEvidenceScreenshot(page, testInfo, "http-oauth-setup-desktop.png", { fullPage: true });
    await create.locator('[name="http_oauth_client_secret"]').fill("synthetic-client-secret");
    await create.getByRole("button", { name: "Test connection", exact: true }).click();
    await expect(create).toContainText("Connection successful");
    await expect(create.locator('[name="http_oauth_client_secret"]')).toHaveValue("synthetic-client-secret");
    await create.getByRole("button", { name: "Create data source", exact: true }).click();
    await expect(page.getByRole("button", { name: "Connect HTTP API", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Connect HTTP API", exact: true }).click();
    await expect(page.getByRole("button", { name: "Reconnect HTTP API", exact: true })).toBeVisible();
    const rotate = page.locator('form[data-dirty-form="data-source-credentials"]');
    await rotate.locator('[name="http_auth_type"]').selectOption("authorization_code");
    await rotate.locator('[name="http_oauth_issuer"]').fill("https://auth.example.com");
    await rotate.locator('[name="http_oauth_authorization_endpoint"]').fill("https://auth.example.com/authorize");
    await rotate.locator('[name="http_oauth_token_endpoint"]').fill("https://auth.example.com/token");
    await rotate.locator('[name="http_oauth_client_id"]').fill("synthetic-public-pkce-app");
    await expect(rotate.locator('[name="http_oauth_client_secret"]')).not.toHaveAttribute("required");
    page.once("dialog", (dialog) => dialog.accept());
    await rotate.getByRole("button", { name: "Rotate credentials", exact: true }).click();
    await page.getByRole("button", { name: "Connect HTTP API", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Synthetic OAuth consent" })).toBeVisible();
    await page.getByRole("button", { name: "Allow access", exact: true }).click();
    await expect(page.getByRole("button", { name: "Reconnect HTTP API", exact: true })).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await captureE2EEvidenceScreenshot(page, testInfo, "http-oauth-connected-compact.png", { fullPage: true });
    await page.getByRole("button", { name: "Reconnect HTTP API", exact: true }).click();
    await page.getByRole("button", { name: "Allow access", exact: true }).click();
    await expect(page.getByRole("button", { name: "Reconnect HTTP API", exact: true })).toBeVisible();
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`${fixture.url}/orgs/acme/data-sources/http-app?section=schema`);
    await expect(page.getByText("Multipart request bodies require manual authoring.", { exact: true })).toBeVisible();
    await captureE2EEvidenceScreenshot(page, testInfo, "http-openapi-schema-desktop.png", { fullPage: true });
    await page.getByRole("link", { name: "Create query draft", exact: true }).click();
    const draft = page.locator("#create-query-form");
    await expect(draft).toBeVisible();
    await expect(draft.locator('[name="http_path"]')).toHaveValue("/users/{{path_id}}");
    await expect(draft.locator('[name="param_name"]')).toHaveValue("path_id");
    await captureE2EEvidenceScreenshot(page, testInfo, "http-openapi-draft-desktop.png", { fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await captureE2EEvidenceScreenshot(page, testInfo, "http-openapi-draft-compact.png", { fullPage: true });
    await draft.getByRole("button", { name: "Save query", exact: true }).click();
    await expect(page.getByText("getUser", { exact: true }).first()).toBeVisible();
});
