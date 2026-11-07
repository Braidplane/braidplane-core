const {
    test,
    expect,
    assertSeededAuth,
    uniqueE2ESlug,
    signUpAndVerify,
    signInWithCredentials,
    deleteE2EOrganization,
    deleteE2EAccount,
    createE2EProject,
    deleteE2EProject,
    createE2EQuery,
    deleteE2EQuery,
    csrfHeaders,
    queryDetailPath,
    captureE2EEvidenceScreenshot,
    saveE2EQueryDefinition,
} = require("./support/smoke-fixtures.cjs");

// Recovery links and disposable credentials must stay out of browser artifacts.
test.use({ storageState: { cookies: [], origins: [] }, trace: "off", screenshot: "off" });
test.beforeAll(assertSeededAuth);

// Latest-execution and onboarding assertions need an organization and session no other journey writes.
test.describe("owned query workspace", () => {
    const ownedTest = test.extend({
        queryWorkspace: async ({ page }, use, testInfo) => {
            const org = uniqueE2ESlug("query-org", testInfo);
            const email = `${org}@example.test`;
            const password = "Synthetic-query-password-123!";
            const source = "query-source";
            const slug = uniqueE2ESlug("alpha-query", testInfo);
            const request = async (path, data) => {
                const response = await page.request.post(path, { headers: await csrfHeaders(page), data });
                expect(response.status(), response.ok() ? path : await response.text()).toBe(201);
                return response.json();
            };
            const errors = [];
            try {
                await signUpAndVerify(page, email, password, "Query Test");
                await signInWithCredentials(page, email, password, /\/workspace/);
                await request("/api/organizations", { name: "Query Test", slug: org });
                await createE2EProject(page, "query-test", org);
                await request(`/api/organizations/${org}/data-sources`, {
                    name: "Query source",
                    slug: source,
                    type: "postgres",
                    config: {
                        host: "localhost",
                        port: Number(process.env.POSTGRES_CONNECTOR_PORT),
                        database: "braidplane",
                        ssl_mode: "disable",
                        allow_private_hosts: true,
                    },
                    credentials: { username: "postgres", password: "postgres" },
                });
                await use({ page, org, source, slug });
            } catch (error) {
                errors.push(error);
            }
            // Fixture teardown retains the original test failure as well as cleanup errors.
            for (const cleanup of [
                () => deleteE2EQuery(page, source, slug, org),
                async () => {
                    const response = await page.request.delete(`/api/organizations/${org}/data-sources/${source}`, {
                        headers: await csrfHeaders(page),
                    });
                    expect([204, 404]).toContain(response.status());
                },
                () => deleteE2EProject(page, "query-test", org),
                () => deleteE2EOrganization(page, org),
                () => deleteE2EAccount(page, email),
            ]) {
                try {
                    await cleanup();
                } catch (error) {
                    errors.push(error);
                }
            }
            if (errors.length)
                throw new AggregateError(errors, "Owned query workspace setup/cleanup failed", { cause: errors[0] });
        },
    });
    ownedTest(
        "opens a data source, previews a query, and saves its definition",
        async ({ queryWorkspace }, testInfo) => {
            const { page, org, source, slug } = queryWorkspace;
            expect(
                (
                    await page.request.post(`/api/organizations/${org}/data-sources/${source}/test-connection`, {
                        headers: await csrfHeaders(page),
                    })
                ).status(),
            ).toBe(204);
            await createE2EQuery(
                page,
                source,
                {
                    slug,
                    name: "Alpha query",
                    parameters: [],
                    query_statement: { type: "postgres", sql: "SELECT 1 AS marker" },
                    row_limit: 10,
                    timeout_seconds: 30,
                },
                org,
            );
            await page.goto(`/orgs/${org}/data-sources?selected=${source}`);
            const preview = page.locator("[data-entity-preview]");
            await expect(preview).toBeVisible();
            await preview.getByRole("link", { name: "Open full page" }).click();
            await expect(page.getByRole("navigation", { name: "Data source sections" })).toBeVisible();
            await page.goto(`/orgs/${org}/queries?ds=${source}`);
            await page.locator(`#query-opener-${slug}`).click();
            await expect(preview).toBeVisible();
            await preview.getByRole("link", { name: "Open full page" }).click();
            await expect(page).toHaveURL(new RegExp(queryDetailPath(org, source, slug)));
            const name = page.locator('#query-update-form input[name="name"]');
            await name.fill("Saved Alpha query");
            await saveE2EQueryDefinition(page, slug);
            await page.reload();
            await expect(name).toHaveValue("Saved Alpha query");
            const executionStartedAt = Date.now();
            await page.getByRole("button", { name: "Run query", exact: true }).click();
            await expect(page.getByRole("tab", { name: "Results", exact: true })).toHaveAttribute(
                "aria-selected",
                "true",
            );
            await expect(page.locator("#query-execution-feedback-slot")).toContainText("marker");
            await captureE2EEvidenceScreenshot(page, testInfo, "alpha-query.png", { fullPage: true });
            const summary = await page.request.get(`/api/organizations/${org}/query-execution-summary`);
            expect(summary.status()).toBe(200);
            const recorded = (await summary.json()).successful_execution;
            expect(recorded).toEqual({
                data_source_slug: source,
                executed_at: expect.any(String),
                execution_slug: expect.any(String),
                query_slug: slug,
            });
            expect(Date.parse(recorded.executed_at)).toBeGreaterThanOrEqual(executionStartedAt - 5_000);
            await page.goto(`/dashboard?project_slug=query-test&environment_slug=dev`);
            const onboarding = page.locator("[data-dashboard-onboarding]");
            await expect(onboarding).toBeVisible();
            await onboarding.getByText("Setup checklist", { exact: true }).click();
            await expect(onboarding.getByRole("listitem").filter({ hasText: "Get a result" })).toContainText("Done");
            for (const width of [1440, 390]) {
                await page.setViewportSize({ width, height: 900 });
                await expect
                    .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
                    .toBe(true);
                await captureE2EEvidenceScreenshot(page, testInfo, `onboarding-result-${width}.png`, {
                    fullPage: true,
                });
            }
        },
    );
});
