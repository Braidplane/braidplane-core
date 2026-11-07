const {
    test,
    expect,
    assertSeededAuth,
    e2eOrgSlug,
    e2eEnvironmentSlug,
    uniqueE2ESlug,
    createE2EQuery,
    saveE2EQueryDefinition,
    deleteE2EQuery,
    queryDetailPath,
    csrfHeaders,
    captureE2EEvidenceScreenshot,
} = require("./support/smoke-fixtures.cjs");

test.beforeAll(assertSeededAuth);

test("authors, reopens, changes and removes HTTP acknowledgements", async ({ page }, testInfo) => {
    test.setTimeout(120_000);
    page.setDefaultTimeout(10_000);
    const { createServer } = require("node:http");
    const source = uniqueE2ESlug("http-ack", testInfo);
    const query = uniqueE2ESlug("ack-write", testInfo);
    const sourceAPI = `/api/organizations/${e2eOrgSlug}/data-sources/${source}`;
    let mode = "accepted";
    const successCode = "OK";
    const provider = createServer((request, response) => {
        response.setHeader("Content-Type", "application/json");
        if (request.url !== "/records") return response.end("{}");
        const bodies = {
            accepted: `{"status":{"code":"${successCode}"},"number":1.0,"valid":true,"records":[{"errors":[]}]}`,
            rejected: `{"status":{"code":"ERROR"},"number":1.0,"valid":true}`,
        };
        response.end(bodies[mode]);
    });
    await new Promise((resolve, reject) => {
        provider.once("error", reject);
        provider.listen(0, "127.0.0.1", resolve);
    });
    const save = async () => {
        await saveE2EQueryDefinition(page, query);
        await expect(page).toHaveURL((url) => url.pathname === queryDetailPath(e2eOrgSlug, source, query));
        await page.reload({ waitUntil: "load" });
    };
    try {
        const created = await page.request.post(`/api/organizations/${e2eOrgSlug}/data-sources`, {
            headers: await csrfHeaders(page),
            data: {
                name: "Synthetic acknowledgements",
                slug: source,
                type: "http",
                config: { base_url: `http://127.0.0.1:${provider.address().port}`, allow_private_hosts: true },
                credentials: {},
            },
        });
        expect(created.status()).toBe(201);
        await page.goto(`/orgs/${e2eOrgSlug}/queries?ds=${source}`);
        await page.getByRole("button", { name: "New query", exact: true }).click();
        const create = page.locator("#create-query-form");
        await create.locator('[name="name"]').fill("Acknowledged write");
        await create.getByText("Customize identifier", { exact: true }).click();
        await create.locator('[name="slug"]').fill(query);
        await create.locator('[name="http_method"]').selectOption("POST");
        await create.locator('[name="http_path"]').fill("/records");
        const summary = create.getByText("Response acknowledgement (optional)", { exact: true });
        await summary.focus();
        await page.keyboard.press("Enter");
        await expect(create.getByLabel("Require response acknowledgement", { exact: true })).toBeVisible();
        await create.getByLabel("Require response acknowledgement", { exact: true }).check();
        await create.getByLabel("Accepted HTTP status codes", { exact: true }).fill("200, 201");
        await create.getByLabel("Success path", { exact: true }).fill("status.code");
        await create.getByLabel("Equals (JSON scalar)", { exact: true }).fill("OK");
        await create.getByRole("button", { name: "Save query", exact: true }).click();
        await expect(page.locator("#queries-workspace")).toContainText("enter a JSON string (in double quotes)");
        await expect(create.getByLabel("Success path", { exact: true })).toHaveValue("status.code");
        await create.getByLabel("Equals (JSON scalar)", { exact: true }).fill('"OK"');
        for (const [path, value] of [
            ["number", "1.0"],
            ["valid", "true"],
        ]) {
            await create.getByRole("button", { name: "Add success check", exact: true }).click();
            await create.getByLabel("Success path", { exact: true }).last().fill(path);
            await create.getByLabel("Equals (JSON scalar)", { exact: true }).last().fill(value);
        }
        await create.getByRole("button", { name: "Add empty field", exact: true }).click();
        await create.getByLabel("Empty field name", { exact: true }).fill("errors");
        await create.getByRole("button", { name: "Save query", exact: true }).click();
        await expect(create).toBeHidden();
        await page.goto(queryDetailPath(e2eOrgSlug, source, query));
        const edit = page.locator("#query-update-form");
        await expect(edit.getByLabel("Require response acknowledgement", { exact: true })).toBeChecked();
        await expect(edit.getByLabel("Equals (JSON scalar)", { exact: true }).nth(1)).toHaveValue("1.0");
        await expect(edit.getByLabel("Equals (JSON scalar)", { exact: true }).nth(2)).toHaveValue("true");
        const persisted = await page.request.get(`${sourceAPI}/queries/${query}`);
        expect(await persisted.text()).toContain('"equals":1.0');
        const panel = edit.locator("details").filter({ hasText: "Response acknowledgement (optional)" });
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
            await captureE2EEvidenceScreenshot(panel, testInfo, `http-acknowledgement-${width}.png`);
        }
        await page.setViewportSize({ width: 1440, height: 1000 });
        for (const [outcome, code] of [
            ["accepted", ""],
            ["rejected", "HTTP_ACKNOWLEDGEMENT_REJECTED"],
        ]) {
            mode = outcome;
            await page.getByRole("tab", { name: "Definition", exact: true }).click();
            await Promise.all([
                page.waitForResponse((response) =>
                    new URL(response.url()).pathname.endsWith(`/queries/${query}/execute`),
                ),
                page.getByRole("button", { name: "Run query", exact: true }).click(),
            ]);
            const feedback = page.locator("#query-execution-feedback-slot");
            await expect(feedback).toContainText(code || '"number": 1');
            await expect(feedback).not.toContainText("synthetic-private-body");
        }
        await page.getByRole("tab", { name: "Definition", exact: true }).click();
        await edit.getByLabel("Equals (JSON scalar)", { exact: true }).first().fill('"DONE"');
        await save();
        await expect(edit.getByLabel("Equals (JSON scalar)", { exact: true }).first()).toHaveValue('"DONE"');
        await edit.getByRole("button", { name: "Remove success check", exact: true }).last().click();
        await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
        await save();
        await expect(edit.getByLabel("Success path", { exact: true })).toHaveCount(2);
        await edit.getByLabel("Require response acknowledgement", { exact: true }).uncheck();
        await save();
        const removed = await (await page.request.get(`${sourceAPI}/queries/${query}`)).json();
        expect(removed.query_statement.acknowledgement).toBeUndefined();
        expect(removed.query_statement.method).toBe("POST");
        expect(removed.query_statement.path).toBe("/records");
    } finally {
        try {
            await deleteE2EQuery(page, source, query);
            const deleted = await page.request.delete(sourceAPI, { headers: await csrfHeaders(page) });
            expect([204, 404]).toContain(deleted.status());
        } finally {
            provider.closeAllConnections();
            await new Promise((resolve) => provider.close(resolve));
        }
    }
});

test("tests and creates a MySQL source without re-entering its password", async ({ page }, testInfo) => {
    const original = await page.request.get(
        `/api/organizations/${e2eOrgSlug}/data-sources/${process.env.E2E_MYSQL_DATA_SOURCE_SLUG}`,
    );
    expect(original.ok()).toBe(true);
    const { config } = await original.json();
    const source = uniqueE2ESlug("mysql-draft", testInfo);
    const sourceAPI = `/api/organizations/${e2eOrgSlug}/data-sources/${source}`;
    try {
        await page.goto(`/orgs/${e2eOrgSlug}/data-sources`);
        await page.getByRole("button", { name: "Add Data Source", exact: true }).click();
        const create = page.locator("#create-data-source");
        await create.locator('select[name="type"]').selectOption("mysql");
        await create.locator('input[name="name"]').fill("Synthetic MySQL draft");
        await create.getByText("Customize identifier", { exact: true }).click();
        await create.locator('input[name="slug"]').fill(source);
        await create.locator('input[name="host"]').fill(config.host);
        await create.locator('input[name="port"]').fill(String(config.port));
        await create.locator('input[name="database"]').fill(config.database);
        await create.locator('input[name="username"]').filter({ visible: true }).fill("braidplane");
        const password = create.locator('input[name="password"]').filter({ visible: true });
        await password.fill("braidplanepass");
        await create.locator('input[name="database_allow_private_hosts"]').check();
        await create.getByRole("button", { name: "Test connection", exact: true }).click();
        await expect(create).toContainText("Connection successful.");
        await expect(password).toHaveValue("braidplanepass");
        await create.getByRole("button", { name: "Create data source", exact: true }).click();
        await expect(create).not.toBeVisible();
        const stored = await page.request.get(sourceAPI);
        expect(stored.status()).toBe(200);
        expect(await stored.text()).not.toContain("braidplanepass");
        const recheck = await page.request.post(`${sourceAPI}/test-connection`, { headers: await csrfHeaders(page) });
        expect(recheck.ok()).toBe(true);
    } finally {
        const deleted = await page.request.delete(sourceAPI, { headers: await csrfHeaders(page) });
        expect([204, 404]).toContain(deleted.status());
    }
});

test("tests a HubSpot token, creates its source, and reads mock CRM contacts", async ({ page }, testInfo) => {
    const provider = new URL(process.env.HUBSPOT_BASE_URL);
    expect(["localhost", "127.0.0.1"]).toContain(provider.hostname);
    const source = uniqueE2ESlug("hubspot-draft", testInfo);
    const query = uniqueE2ESlug("hubspot-contacts", testInfo);
    const email = `${source}@example.test`;
    const sourceAPI = `/api/organizations/${e2eOrgSlug}/data-sources/${source}`;
    let queryCreated = false;
    try {
        const seeded = await page.request.post(new URL("/seed", provider).href, {
            data: { contacts: [{ properties: { email, firstname: "Synthetic", lastname: "Contact" } }] },
        });
        expect(seeded.ok()).toBe(true);
        await page.goto(`/orgs/${e2eOrgSlug}/data-sources`);
        await page.getByRole("button", { name: "Add Data Source", exact: true }).click();
        const create = page.locator("#create-data-source");
        await create.locator('select[name="type"]').selectOption("hubspot");
        await create.locator('select[name="hubspot_auth_type"]').selectOption("private_app");
        await create.locator('input[name="name"]').fill("Synthetic CRM source");
        await create.getByText("Customize identifier", { exact: true }).click();
        await create.locator('input[name="slug"]').fill(source);
        const token = create.locator('input[name="private_app_token"]');
        await token.fill("pat-na1-test-token");
        await create.getByRole("button", { name: "Test connection", exact: true }).click();
        await expect(create).toContainText("Connection successful.");
        await expect(token).toHaveValue("pat-na1-test-token");
        await create.getByRole("button", { name: "Create data source", exact: true }).click();
        await expect(create).not.toBeVisible();
        const stored = await page.request.get(sourceAPI);
        expect(stored.status()).toBe(200);
        expect(await stored.text()).not.toContain("pat-na1-test-token");
        await createE2EQuery(page, source, {
            slug: query,
            name: "Synthetic CRM contacts",
            parameters: [],
            query_statement: {
                type: "hubspot",
                object_type: "contacts",
                operation: "list",
                properties: ["email", "firstname", "lastname"],
                limit: 100,
            },
            row_limit: 100,
            timeout_seconds: 30,
        });
        queryCreated = true;
        await page.goto(queryDetailPath(e2eOrgSlug, source, query));
        await page.getByRole("button", { name: "Run query", exact: true }).click();
        await expect(page.locator("#query-execution-feedback-slot")).toContainText(email);
        await captureE2EEvidenceScreenshot(page, testInfo, "hubspot-contact-result.png", { fullPage: true });
    } finally {
        if (queryCreated) await deleteE2EQuery(page, source, query);
        const deleted = await page.request.delete(sourceAPI, { headers: await csrfHeaders(page) });
        expect([204, 404]).toContain(deleted.status());
    }
});

// Static API credentials need a browser journey because the form carries write-only secrets.
test("sets up three secret HTTP headers and reads every numbered page", async ({ page }, testInfo) => {
    const http = require("node:http");
    let key = "synthetic-browser-api-key";
    const pages = [];
    let schemaReads = 0;
    let releaseSchema;
    const schemaGate = new Promise((resolve) => {
        releaseSchema = resolve;
    });
    const provider = http.createServer((request, response) => {
        if (
            request.headers["x-api-key"] !== key ||
            request.headers["x-custom-secret"] !== "synthetic-custom" ||
            request.headers["x-api-token"] !== "synthetic-token"
        ) {
            response.writeHead(401).end();
            return;
        }
        response.setHeader("Content-Type", "application/json");
        const url = new URL(request.url, "http://fixture.invalid");
        if (url.pathname === "/openapi.json") {
            schemaReads++;
            schemaGate.then(() =>
                response.end(
                    JSON.stringify({
                        openapi: "3.0.3",
                        info: { title: "Synthetic API", version: "1" },
                        paths: {
                            "/items": { get: { operationId: "listItems", responses: { 200: { description: "OK" } } } },
                        },
                    }),
                ),
            );
        } else if (url.pathname === "/items") {
            const number = Number(url.searchParams.get("page"));
            pages.push(number);
            if (url.searchParams.get("pageSize") !== "1" || number < 1 || number > 3) {
                response.writeHead(400).end();
                return;
            }
            response.end(
                JSON.stringify({
                    items: [{ id: number }],
                    totalPages: 3,
                    page: number,
                    next: number < 3 ? `?page=${number + 1}&pageSize=1` : null,
                }),
            );
        } else {
            response.end("{}");
        }
    });
    await new Promise((resolve, reject) => {
        provider.once("error", reject);
        provider.listen(0, "127.0.0.1", resolve);
    });
    const providerURL = `http://127.0.0.1:${provider.address().port}`;
    const source = uniqueE2ESlug("http-key", testInfo);
    const query = uniqueE2ESlug("http-pages", testInfo);
    const sourceAPI = `/api/organizations/${e2eOrgSlug}/data-sources/${source}`;
    try {
        await page.goto(`/orgs/${e2eOrgSlug}/data-sources?type=http`);
        await page.getByRole("button", { name: "Add Data Source", exact: true }).click();
        const create = page.locator("#create-data-source");
        await create.locator('select[name="type"]').selectOption("http");
        await expect(create.locator("[data-connector-support]")).toHaveCount(0);
        await expect(create.locator('input[name="secret_header_value_0"]')).toHaveAttribute("type", "password");
        for (const viewport of [
            { width: 1440, height: 1000 },
            { width: 390, height: 844 },
        ]) {
            await page.setViewportSize(viewport);
            await expect(create.locator('input[name="secret_header_name_0"]')).toBeVisible();
            await expect
                .poll(() =>
                    create.locator('input[name="name"]').evaluate((input) => {
                        const label = input.closest("label").querySelector(".label");
                        return input.getBoundingClientRect().top - label.getBoundingClientRect().bottom;
                    }),
                )
                .toBeGreaterThanOrEqual(6);
            await expect(create.locator('[data-http-auth-fields="authorization_code"]')).toBeHidden();
            await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `http-key-create-${viewport.width}.png`, {
                fullPage: true,
            });
        }
        await page.setViewportSize({ width: 1440, height: 1000 });
        await create.locator('input[name="name"]').fill("Synthetic HTTP API");
        await create.getByText("Customize identifier", { exact: true }).click();
        await create.locator('input[name="slug"]').fill(source);
        await create.locator('input[name="base_url"]').fill(providerURL);
        await create.locator('input[name="openapi_url"]').fill(`${providerURL}/openapi.json`);
        await create.locator('input[name="http_allow_private_hosts"]').check();
        await create.locator('input[name="secret_header_name_0"]').fill("X-Api-Key");
        await create.locator('input[name="secret_header_value_0"]').fill(key);
        await page.getByText("More secret headers (up to 8)", { exact: true }).click();
        await create.locator('input[name="secret_header_name_1"]').fill("X-Custom-Secret");
        await create.locator('input[name="secret_header_value_1"]').fill("synthetic-custom");
        await create.locator('input[name="secret_header_name_2"]').fill("X-Api-Token");
        await create.locator('input[name="secret_header_value_2"]').fill("synthetic-token");

        await create.getByRole("button", { name: "Test connection", exact: true }).click();
        await expect(create).toContainText("Connection successful.");
        await expect(create.locator('input[name="secret_header_value_0"]')).toHaveValue(key);
        await expect(create.locator('input[name="secret_header_value_1"]')).toHaveValue("synthetic-custom");
        await expect(create.locator('input[name="secret_header_value_2"]')).toHaveValue("synthetic-token");
        await create.getByRole("button", { name: "Create data source", exact: true }).click();
        await expect(create).not.toBeVisible();
        const sourceResponse = await page.request.get(sourceAPI);
        expect(sourceResponse.status()).toBe(200);
        expect(await sourceResponse.text()).not.toContain(key);
        await page.goto(
            `/orgs/${e2eOrgSlug}/data-sources/${source}?section=schema&environment_slug=${e2eEnvironmentSlug}`,
        );
        const schemaCard = page.locator("#data-source-schema-browser");
        await schemaCard.getByRole("button", { name: "Import schema", exact: true }).click();
        await expect(schemaCard.getByRole("button", { name: "Importing…", exact: true })).toBeDisabled();
        await expect(schemaCard).toContainText("This panel updates automatically.");
        await expect(schemaCard).not.toContainText("No tables discovered.");
        if (process.env.PLAYWRIGHT_E2E_EVIDENCE === "1")
            await captureE2EEvidenceScreenshot(schemaCard, testInfo, "schema-import-pending.png");
        const schemaViewport = page.viewportSize();
        await page.setViewportSize({ width: 390, height: 844 });
        await schemaCard.scrollIntoViewIfNeeded();
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        if (process.env.PLAYWRIGHT_E2E_EVIDENCE === "1")
            await captureE2EEvidenceScreenshot(schemaCard, testInfo, "schema-import-pending-compact.png");
        await page.setViewportSize(schemaViewport);
        releaseSchema();
        await expect(schemaCard.getByText("ready", { exact: true })).toBeVisible({ timeout: 15000 });
        await expect(schemaCard).toContainText("listItems");
        await expect(schemaCard).not.toHaveAttribute("hx-get");
        await expect(schemaCard.getByRole("button", { name: "Refresh schema", exact: true })).toBeEnabled();
        if (process.env.PLAYWRIGHT_E2E_EVIDENCE === "1")
            await captureE2EEvidenceScreenshot(schemaCard, testInfo, "schema-import-ready.png");
        expect(schemaReads).toBe(1);
        await createE2EQuery(page, source, {
            slug: query,
            name: "All HTTP pages",
            parameters: [],
            row_limit: 3,
            timeout_seconds: 30,
            query_statement: {
                type: "http",
                method: "GET",
                path: "/items",
                pagination: {
                    page_param: "page",
                    page_size_param: "pageSize",
                    page_size: 1,
                    records_path: "items",
                    total_pages_path: "totalPages",
                    page_number_path: "page",
                    max_pages: 3,
                    timeout_seconds: 5,
                },
            },
        });
        await page.goto(queryDetailPath(e2eOrgSlug, source, query));
        await page.getByRole("button", { name: "Run query", exact: true }).click();
        await expect(page.locator("#query-execution-feedback-slot")).toContainText('"id": 3');
        expect(pages).toEqual([1, 2, 3]);
        await page.getByRole("tab", { name: "Definition", exact: true }).click();
        const editPolicy = page.locator("#query-update-form");
        await editPolicy.getByText("Pagination and write retries", { exact: true }).click();
        const nextLinkPolicy = { next_link_path: "next", records_path: "items", max_pages: 3, timeout_seconds: 5 };
        await editPolicy.locator('[name="http_pagination"]').fill(JSON.stringify(nextLinkPolicy));
        await editPolicy.locator('[name="http_path"]').fill("/items?page=1&pageSize=1");
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await expect(editPolicy.getByText("Pagination (JSON, optional)", { exact: true })).toBeVisible();
            await expect(editPolicy.getByText("Write retry policy (JSON, optional)", { exact: true })).toBeVisible();
            await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `http-retry-policy-${width}.png`, { fullPage: true });
        }
        await page.setViewportSize({ width: 1440, height: 1000 });
        await saveE2EQueryDefinition(page, query);
        await page.reload();
        await expect
            .poll(async () => JSON.parse(await editPolicy.locator('[name="http_pagination"]').inputValue()))
            .toEqual(nextLinkPolicy);
        await page.getByRole("button", { name: "Run query", exact: true }).click();
        await expect(page.locator("#query-execution-feedback-slot")).toContainText('"id": 3');
        expect(pages).toEqual([1, 2, 3, 1, 2, 3]);

        expect(await page.locator("body").innerText()).not.toContain(key);
        await page.goto(`/orgs/${e2eOrgSlug}/data-sources/${source}?section=settings`);
        const rotate = page.locator('form[data-dirty-form="data-source-credentials"]');
        await expect(rotate.locator('input[name="secret_header_value_0"]')).toHaveValue("");
        await expect(rotate.locator('input[name="secret_header_name_0"]')).toHaveValue("");
        await captureE2EEvidenceScreenshot(rotate, testInfo, "http-key-rotation.png");
        key = "synthetic-replaced-api-key";
        await rotate.locator('input[name="secret_header_name_0"]').fill("X-Api-Key");
        await rotate.locator('input[name="secret_header_value_0"]').fill(key);
        await rotate.getByText("More secret headers (up to 8)", { exact: true }).click();
        await rotate.locator('input[name="secret_header_name_1"]').fill("X-Custom-Secret");
        await rotate.locator('input[name="secret_header_value_1"]').fill("synthetic-custom");
        await rotate.locator('input[name="secret_header_name_2"]').fill("X-Api-Token");
        await rotate.locator('input[name="secret_header_value_2"]').fill("synthetic-token");
        page.once("dialog", (dialog) => dialog.accept());
        const rotation = page.waitForResponse(
            (response) =>
                new URL(response.url()).pathname.endsWith("/credentials/update") &&
                response.request().method() === "POST",
        );
        await rotate.getByRole("button", { name: "Rotate credentials", exact: true }).click();
        expect((await rotation).status()).toBe(200);
        await expect(rotate.locator('input[name="secret_header_value_0"]')).toHaveValue("");
        const tested = await page.request.post(`${sourceAPI}/test-connection`, { headers: await csrfHeaders(page) });
        expect(tested.ok()).toBe(true);
        expect(await (await page.request.get(sourceAPI)).text()).not.toContain(key);
    } finally {
        try {
            try {
                await deleteE2EQuery(page, source, query);
            } finally {
                const deleted = await page.request.delete(sourceAPI, { headers: await csrfHeaders(page) });
                expect([204, 404]).toContain(deleted.status());
            }
        } finally {
            releaseSchema();
            provider.closeAllConnections();
            await new Promise((resolve) => provider.close(resolve));
        }
    }
});
