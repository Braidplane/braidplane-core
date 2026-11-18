const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("query source filtering waits for keyboard activation and keeps focus", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-references?include_archive=true`)).ok()).toBe(
            true,
        );
        const queries = `${fixture.url}/orgs/acme/queries`;
        await visit(page, `${queries}?ds=warehouse`);
        const filter = page.locator("#queries-filter-form");
        const source = filter.getByRole("combobox", { name: "Data source", exact: true });
        await expect(source).toHaveValue("warehouse");
        await tabTo(page, source);
        await page.keyboard.press("Space");
        await page.keyboard.press("End");
        await page.keyboard.press("Enter");
        await expect(source).toHaveValue("warehouse-prod");
        await expect(page).toHaveURL(`${queries}?ds=warehouse`);
        await expect(source).toBeFocused();

        const apply = filter.getByRole("button", { name: "Filter queries", exact: true });
        await page.keyboard.press("Tab");
        await expect(apply).toBeFocused();
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(`${queries}?ds=warehouse-prod`);
        await expect(apply).toBeFocused();
        await expect(source).toHaveValue("warehouse-prod");
        await assess(page, testInfo, "queries-filtered-by-source");

        await visit(page, `${queries}?deleted=true&ds=warehouse`);
        await tabTo(page, source);
        await page.keyboard.press("Space");
        await page.keyboard.press("End");
        await page.keyboard.press("Enter");
        await tabTo(page, apply);
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(
            (url) => url.searchParams.get("ds") === "warehouse-prod" && url.searchParams.get("deleted") === "true",
        );
        await expect(apply).toBeFocused();
        await expect(page.getByRole("link", { name: "Archived customers", exact: true })).toBeVisible();
        await assess(page, testInfo, "deleted-queries-filtered-by-source");
    });
});

test("repository source choice keeps keyboard focus while branches reload", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/repository-setup`)).ok()).toBe(true);
        await visit(page, `${fixture.url}/organizations/acme/projects/ops?section=repository&repository_path=existing`);
        const source = page.getByRole("combobox", { name: "Backend data source", exact: true });
        await expect(source).toHaveAccessibleDescription(
            "Changing the data source reloads the tracked branch choices.",
        );
        await tabTo(page, source);
        await page.keyboard.press("Space");
        await page.keyboard.press("ArrowDown");
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/data_source_slug=git-source/);
        await expect(source).toHaveValue("git-source");
        await expect(source).toBeFocused();

        const branch = page.getByRole("combobox", { name: "Tracked branch", exact: true });
        await expect(branch.getByRole("option", { name: /main/ })).toBeAttached();
        await tabTo(page, branch);
        await expect(branch).toBeFocused();
        await assess(page, testInfo, "repository-branches-ready");
    });
});

test("repository branch failures support keyboard retry and save", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/repository-setup`)).ok()).toBe(true);
        const setUnavailable = (unavailable) =>
            page.request.post(`${fixture.url}/_test/repository-branches?unavailable=${unavailable}`);
        expect((await setUnavailable(true)).ok()).toBe(true);
        await visit(page, `${fixture.url}/organizations/acme/projects/ops?section=repository&repository_path=existing`);
        await page.route("**/pipeline-backend/branches?**", (route) => route.abort("connectionfailed"), { times: 1 });
        const source = page.getByRole("combobox", { name: "Backend data source", exact: true });
        await tabTo(page, source);
        await page.keyboard.press("Space");
        await page.keyboard.press("ArrowDown");
        await page.keyboard.press("Enter");
        const fragment = page.locator("#project-backend-branches");
        const branch = fragment.getByRole("combobox", { name: "Tracked branch", exact: true });
        const retry = fragment.getByRole("button", { name: "Retry branches", exact: true });
        const alert = fragment.getByRole("alert");
        await expect(alert).toContainText("Could not load branches.");
        await expect(source).toBeFocused();
        await expect(branch).toHaveAccessibleDescription(/Could not load branches/);
        await expect(branch.getByRole("option", { name: "No branches available" })).toBeAttached();
        await assess(page, testInfo, "repository-branches-connection-failed");

        await tabTo(page, retry);
        const retryResponse = () =>
            page.waitForResponse(
                (response) => new URL(response.url()).pathname.endsWith("/pipeline-backend/branches"),
                { timeout: 5_000 },
            );
        const previousRetry = await retry.elementHandle();
        const stillUnavailable = retryResponse();
        await page.keyboard.press("Enter");
        expect((await stillUnavailable).ok()).toBe(true);
        await expect.poll(() => previousRetry.evaluate((button) => button.isConnected)).toBe(false);
        await expect(fragment).not.toHaveClass(/htmx-settling/);
        await expect(retry).toBeFocused();
        await expect(alert).toContainText("Branch discovery is temporarily unavailable.");
        await expect(retry).toHaveAccessibleDescription(/Branch discovery is temporarily unavailable/);
        await expect(branch).toBeDisabled();
        await expect(branch).toHaveAccessibleDescription(/Branch discovery is temporarily unavailable/);
        await assess(page, testInfo, "repository-branches-unavailable");

        expect((await setUnavailable(false)).ok()).toBe(true);
        const recovered = retryResponse();
        await page.keyboard.press("Enter");
        const response = await recovered;
        expect(response.ok()).toBe(true);
        expect(new URL(response.url()).searchParams.get("data_source_slug")).toBe("git-source");
        const refresh = fragment.getByRole("button", { name: "Refresh branches", exact: true });
        await expect(refresh).toBeFocused();
        await expect(fragment.getByRole("status")).toHaveText("Branches are loaded from the selected data source.");
        await assess(page, testInfo, "repository-branches-recovered");
        await page.keyboard.press("Tab");
        await expect(branch).toBeFocused();
        await page.keyboard.press("r");
        await expect(branch).toHaveValue("release");
        const save = page.getByRole("button", { name: "Save backend", exact: true });
        await tabTo(page, save);
        await page.keyboard.press("Enter");
        await expect(page.getByText("Current backend repo", { exact: true }).locator("..")).toContainText(
            "https://example.com/pipelines.git",
        );
        await expect(branch).toHaveValue("release");
    });
});
