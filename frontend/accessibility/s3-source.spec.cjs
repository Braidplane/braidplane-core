const { once } = require("node:events");
const { createServer } = require("node:http");
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

const sourceSlug = "s3-fixture";
const querySlug = "fixture-object-preview";
const objectName = "orders".repeat(24);
const objectKey = `imports/${objectName}.json`;
const rotatedKey = "rotated-fixture-s3-key";

const test = base.extend({
    s3Endpoint: async ({ baseURL }, use) => {
        const server = createServer((request, response) => {
            const url = new URL(request.url, baseURL);
            if (request.method === "HEAD" && url.pathname === "/fixture-bucket") {
                response.writeHead(200).end();
            } else if (request.method === "GET" && url.pathname === `/fixture-bucket/${objectKey}`) {
                if (request.headers.authorization?.includes(`Credential=${rotatedKey}/`)) {
                    response
                        .writeHead(200, { "Content-Type": "application/json" })
                        .end('[{"order_name":"Fixture order"}]');
                } else {
                    response.writeHead(403).end();
                }
            } else if (
                request.method === "GET" &&
                url.pathname === "/fixture-bucket" &&
                url.searchParams.get("list-type") === "2"
            ) {
                response
                    .writeHead(200, { "Content-Type": "application/xml" })
                    .end(
                        '<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>fixture-bucket</Name><IsTruncated>false</IsTruncated></ListBucketResult>',
                    );
            } else {
                const code = request.method === "HEAD" ? "NoSuchBucket" : "NoSuchKey";
                response
                    .writeHead(404, { "Content-Type": "application/xml" })
                    .end(`<Error><Code>${code}</Code></Error>`);
            }
        });
        server.listen(0, "127.0.0.1");
        await once(server, "listening");
        try {
            await use(`http://127.0.0.1:${server.address().port}`);
        } finally {
            await new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
        }
    },
    ownedOrg: [
        async ({ baseURL, page }, use, testInfo) => {
            expect(["127.0.0.1", "localhost", "[::1]"]).toContain(new URL(baseURL).hostname);
            const org = uniqueE2ESlug("a11y-s3", testInfo);
            const email = `${org}@example.test`;
            const password = "Synthetic-source-password-123!";
            await signUpAndVerify(page, email, password, "S3 Accessibility Fixture");
            await signInWithCredentials(page, email, password, /\/workspace/);
            const errors = [];
            try {
                const organization = await page.request.post("/api/organizations", {
                    headers: await csrfHeaders(page),
                    data: { name: "S3 Accessibility Fixture", slug: org },
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
            if (errors.length) throw new AggregateError(errors, "S3 fixture cleanup failed");
        },
        { timeout: 45_000 },
    ],
});

test.use({ storageState: { cookies: [], origins: [] }, trace: "off", screenshot: "off" });

test("S3 source and object preview recover with the keyboard", async ({
    page,
    ownedOrg: org,
    s3Endpoint,
}, testInfo) => {
    const activate = async (control) => {
        await tabTo(page, control);
        await page.keyboard.press("Enter");
    };
    const typeText = async (control, value) => {
        await tabTo(page, control);
        await page.keyboard.press("ControlOrMeta+a");
        await page.keyboard.insertText(value);
    };
    await visit(page, `/orgs/${org}/data-sources`);
    await activate(page.getByRole("button", { name: "Add Data Source", exact: true }));
    const create = page.locator("#create-data-source");
    const type = create.getByRole("combobox", { name: "Type", exact: true });
    await tabTo(page, type);
    await page.keyboard.press("s");
    await page.keyboard.press("3");
    await expect(type).toHaveValue("s3");

    const name = create.getByRole("textbox", { name: "Name", exact: true });
    const endpoint = create.getByRole("textbox", { name: "Endpoint (optional)", exact: true });
    const bucket = create.getByRole("textbox", { name: "Bucket", exact: true });
    const secret = create.locator("#data-source-create-s3-secret-access-key");
    const fixtureSecret = "fixture-only-s3-secret";
    await typeText(name, "S3 Fixture");
    await typeText(endpoint, s3Endpoint);
    await typeText(create.getByRole("textbox", { name: "Region", exact: true }), "us-east-1");
    await typeText(bucket, "missing-bucket");
    await typeText(create.getByRole("textbox", { name: "Access key ID", exact: true }), "fixture-s3-key");
    await expect(secret).toHaveAccessibleName("Secret access key");
    await typeText(secret, fixtureSecret);
    const pathStyle = create.getByRole("checkbox", { name: "Force path-style addressing" });
    const privateEndpoint = create.getByRole("checkbox", { name: "Allow private endpoint" });
    for (const checkbox of [pathStyle, privateEndpoint]) {
        await tabTo(page, checkbox);
        await page.keyboard.press("Space");
        await expect(checkbox).toBeChecked();
    }
    const connect = create.getByRole("button", { name: "Test connection", exact: true });
    await activate(connect);
    const connectionFailure = create.getByRole("alert");
    await expect(connectionFailure).toBeFocused();
    await expect(connectionFailure).toContainText(/bucket|connection/i);
    await expect(name).toHaveValue("S3 Fixture");
    await expect(endpoint).toHaveValue(s3Endpoint);
    await expect(bucket).toHaveValue("missing-bucket");
    await expect(pathStyle).toBeChecked();
    await expect(privateEndpoint).toBeChecked();
    expect((await secret.inputValue()) === fixtureSecret).toBe(true);
    await assess(page, testInfo, "s3-connection-refused");

    await typeText(bucket, "fixture-bucket");
    await activate(connect);
    await expect(create.getByRole("status").filter({ hasText: "Connection successful." })).toBeFocused();
    await assess(page, testInfo, "s3-connection-ready");
    await activate(create.getByRole("button", { name: "Create data source", exact: true }));
    await expect(create).toBeHidden();
    await activate(page.getByRole("link", { name: "S3 Fixture", exact: true }));
    await activate(page.getByRole("link", { name: "Settings", exact: true }));
    const settings = page.locator('form[data-dirty-form="data-source-update"]');
    const prefix = settings.getByRole("textbox", { name: "Prefix", exact: true });
    const region = settings.getByRole("textbox", { name: "Region", exact: true });
    const saveSettings = settings.getByRole("button", { name: "Save connection settings" });
    await typeText(prefix, "imports/");
    await typeText(region, " ");
    await activate(saveSettings);
    const settingsError = settings.getByRole("alert");
    await expect(settingsError).toBeFocused();
    await expect(settingsError).toContainText("region and bucket are required");
    await expect(prefix).toHaveValue("imports/");
    await assess(page, testInfo, "s3-settings-refused");

    await typeText(region, "us-east-1");
    await activate(saveSettings);
    await expect(page.getByRole("status").filter({ hasText: "Connection settings saved." })).toBeFocused();
    await expect(prefix).toHaveValue("imports/");
    await assess(page, testInfo, "s3-settings-saved");

    const rotation = page.locator('form[data-dirty-form="data-source-credentials"]');
    const key = rotation.getByLabel("Access key ID", { exact: true });
    const newSecret = rotation.getByLabel("Secret access key", { exact: true });
    const rotate = rotation.getByRole("button", { name: "Rotate credentials", exact: true });
    const rotationRequests = [];
    page.on("request", (request) => {
        if (new URL(request.url()).pathname.endsWith("/credentials/update")) {
            rotationRequests.push(request.method());
        }
    });
    const confirmRotation = async (answer) => {
        const prompt = page.waitForEvent("dialog");
        const activation = activate(rotate);
        const dialog = await prompt;
        await dialog[answer]();
        await activation;
        expect(dialog.type()).toBe("confirm");
        expect(dialog.message()).toContain("S3 Fixture");
    };
    await expect(key).toHaveValue("");
    await expect(newSecret).toHaveValue("");
    await typeText(key, " ");
    await typeText(newSecret, fixtureSecret);
    await confirmRotation("dismiss");
    await expect(rotate).toBeFocused();
    await expect(key).toHaveValue(" ");
    await expect(newSecret).not.toHaveValue("");
    expect(rotationRequests).toEqual([]);

    await confirmRotation("accept");
    const rotationError = page
        .getByRole("alert")
        .filter({ hasText: "access key ID and secret access key are required" });
    await expect(rotationError).toBeFocused();
    await expect(rotationError).toBeInViewport({ ratio: 1 });
    await expect(key).toHaveValue(" ");
    await expect(newSecret).not.toHaveValue("");
    await expect(prefix).toHaveValue("imports/");
    await assess(page, testInfo, "s3-rotation-refused");

    await typeText(key, rotatedKey);
    await confirmRotation("accept");
    const rotationSaved = page.getByRole("status").filter({ hasText: "Credentials updated." });
    await expect(rotationSaved).toBeFocused();
    await expect(rotationSaved).toBeInViewport({ ratio: 1 });
    await expect(key).toHaveValue("");
    await expect(newSecret).toHaveValue("");
    await expect(prefix).toHaveValue("imports/");
    expect(rotationRequests).toEqual(["POST", "POST"]);
    await assess(page, testInfo, "s3-rotation-saved");

    await activate(page.getByRole("link", { name: "Operations", exact: true }));
    await activate(page.getByRole("link", { name: "Open queries", exact: true }));
    await activate(page.getByRole("button", { name: "New query", exact: true }));
    const draft = page.locator("#create-query-form");
    await typeText(draft.getByRole("textbox", { name: "Name", exact: true }), "Fixture Object Preview");
    await typeText(draft.getByRole("textbox", { name: "Object key", exact: true }), "{{object}}.json");
    await activate(draft.getByRole("button", { name: "Sync from statement", exact: true }));
    await activate(draft.getByRole("button", { name: "Save query", exact: true }));
    await activate(page.getByRole("link", { name: "Fixture Object Preview", exact: true }));
    const object = page.getByRole("textbox", { name: "object (text)", exact: true });
    const run = page.getByRole("button", { name: "Run query", exact: true });
    const results = page.getByRole("tabpanel", { name: "Results", exact: true });
    await typeText(object, "missing");
    await activate(run);
    await expect(results.getByRole("alert")).toContainText(/not found|does not exist/i);
    await expect(page.locator("#query-execution-feedback")).toBeFocused();
    await assess(page, testInfo, "s3-object-missing");
    await tabTo(page, page.getByRole("tab", { name: "Results", exact: true }));
    await page.keyboard.press("ArrowLeft");
    await expect(page.getByRole("tab", { name: "Definition", exact: true })).toBeFocused();
    await expect(object).toHaveValue("missing");
    await typeText(object, objectName);
    await activate(run);
    await expect(results.getByText("Row count: 1", { exact: true })).toBeVisible();
    const displayedKey = results.getByText(`Key: ${objectKey}`, { exact: true });
    await expect(displayedKey).toBeVisible();
    const keyOverflow = await displayedKey.evaluate((element) => {
        const range = document.createRange();
        range.selectNodeContents(element);
        const right = Math.max(...Array.from(range.getClientRects(), (rect) => rect.right));
        const result = element.closest("#query-execution-feedback");
        return right - Math.min(result.getBoundingClientRect().right, window.innerWidth);
    });
    expect(keyOverflow, "key text fits within the result panel and viewport").toBeLessThanOrEqual(1);
    await expect(results.locator("pre")).toContainText('"order_name": "Fixture order"');
    await expect(results.getByRole("group", { name: "Result", exact: true })).toBeFocused();
    await assess(page, testInfo, "s3-object-preview");
});
