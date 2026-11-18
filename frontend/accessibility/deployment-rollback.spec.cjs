const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

for (const [boundary, roleChange, denialText] of [
    ["before submission", "read_only=true", "You need permission to update pipelines"],
    ["during submission", "backend_denied=true", "You do not have permission to update pipelines"],
]) {
    test(`rollback keeps its selection when update permission is revoked ${boundary}`, async ({ page }, testInfo) => {
        await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            const access = (query) => page.request.post(`${fixture.url}/_test/deployment-access?${query}`);
            expect((await access("rollback=true")).ok()).toBe(true);
            const deployment = `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync/deploy?environment_slug=dev`;
            const version = "a".repeat(64);
            const digest = `sha256:${"d".repeat(64)}`;
            const activate = async (control) => {
                await tabTo(page, control);
                await page.keyboard.press("Enter");
            };
            await visit(page, `${deployment}&spec_hash=${"c".repeat(64)}`);
            await activate(page.getByRole("link", { name: "Review previous resolution for rollback" }));
            const selected = page.getByRole("region", { name: "Rollback selection", exact: true });
            await expect(selected).toContainText(digest);
            const confirmation = page.getByRole("textbox", { name: /^Confirm protected environment/ });
            await tabTo(page, confirmation);
            await page.keyboard.insertText("dev");
            const rollback = page.getByRole("button", { name: "Roll back in dev", exact: true });
            await tabTo(page, rollback);
            expect((await access(roleChange)).ok()).toBe(true);
            const denied = page.waitForResponse(
                (response) =>
                    response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/deploy"),
            );
            await page.keyboard.press("Enter");
            expect((await denied).status()).toBe(403);
            const error = page.getByRole("alert").filter({ hasText: denialText });
            await expect(error).toBeFocused();
            await expect(error).toBeInViewport({ ratio: 1 });
            await expect(selected).toContainText(digest);
            await expect(page.getByText(version, { exact: true })).toBeVisible();
            await expect(rollback).toHaveCount(0);
            await expect(confirmation).toHaveCount(0);
            await expect(page.getByRole("button", { name: "Compile selected version", exact: true })).toHaveCount(0);
            await assess(page, testInfo, `rollback-update-denied-${boundary.replace(" ", "-")}`);

            const review = page.getByRole("link", { name: "Review current deployment", exact: true });
            await page.keyboard.press("Tab");
            await expect(review).toBeFocused();
            await page.keyboard.press("Enter");
            await expect(selected.getByRole("heading")).toBeFocused();
            await expect(selected).toContainText(digest);
            await expect(rollback).toHaveCount(0);

            expect((await access("read_only=false")).ok()).toBe(true);
            await page.reload();
            await expect(page.getByText("Expected deployment revision: 7", { exact: true })).toBeVisible();
            await expect(rollback).toBeVisible();
            await expect(confirmation).toHaveValue("");
        });
    });
}

test("rollback reviews the previous resolution and retries after a deployment revision conflict", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/deployment-access?rollback=true`)).ok()).toBe(true);
        const deployment = `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync/deploy?environment_slug=dev`;
        const previous = "a".repeat(64);
        const digest = `sha256:${"d".repeat(64)}`;
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        await visit(page, `${deployment}&spec_hash=${"c".repeat(64)}`);
        await activate(page.getByRole("link", { name: "Review previous resolution for rollback", exact: true }));
        const selected = page.getByRole("region", { name: "Rollback selection", exact: true });
        const selectionHeading = selected.getByRole("heading");
        await expect(selectionHeading).toBeFocused();
        await expect(selected).toContainText(digest);
        await expect(page.getByText("Expected deployment revision:", { exact: false })).toHaveText(
            "Expected deployment revision: 7",
        );
        await assess(page, testInfo, "rollback-selection");

        await activate(page.getByRole("link", { name: "Resolve selected version bindings", exact: true }));
        await activate(page.getByRole("link", { name: "Return to selected pipeline", exact: true }));
        await expect(selectionHeading).toBeFocused();
        await expect(selected).toContainText(digest);

        const compiled = page.getByRole("textbox", { name: "Exact compiled plan digest", exact: true });
        const inspect = page.getByRole("button", { name: "Inspect selection", exact: true });
        const error = page.getByRole("alert");
        await tabTo(page, compiled);
        await page.keyboard.insertText(`sha256:${"b".repeat(64)}`);
        await activate(inspect);
        await expect(error).toContainText("Select a compiled plan or a rollback resolution, one at a time.");
        await expect(error).toBeFocused();
        await expect(page.getByRole("button", { name: "Roll back in dev", exact: true })).toHaveCount(0);
        await assess(page, testInfo, "rollback-ambiguous-selection");
        await tabTo(page, compiled);
        await page.keyboard.press("ControlOrMeta+a");
        await page.keyboard.press("Backspace");
        await activate(inspect);
        await expect(selectionHeading).toBeFocused();

        const confirmation = page.getByRole("textbox", { name: /^Confirm protected environment/ });
        const rollback = page.getByRole("button", { name: "Roll back in dev", exact: true });
        const submit = async () => {
            await tabTo(page, confirmation);
            await page.keyboard.insertText("dev");
            await activate(rollback);
        };
        await submit();
        const conflict = page.getByRole("alert");
        await expect(conflict).toContainText("Deployment changed.");
        await expect(conflict).toBeFocused();
        await expect(rollback).toHaveCount(0);
        await assess(page, testInfo, "rollback-revision-conflict");

        await expect(conflict).toBeFocused();
        await page.keyboard.press("Tab");
        await expect(page.getByRole("link", { name: "Review current deployment", exact: true })).toBeFocused();
        await page.keyboard.press("Enter");
        await expect(selectionHeading).toBeFocused();
        await expect(selected).toContainText(digest);
        await expect(page.getByText("Expected deployment revision:", { exact: false })).toHaveText(
            "Expected deployment revision: 8",
        );
        await submit();
        const success = page.getByRole("status").filter({ hasText: `Deployed ${previous} to dev.` });
        await expect(success).toBeFocused();
        await assess(page, testInfo, "rollback-complete");
    });
});

test("deployment target review resets another environment's rollback selection", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect(
            (
                await page.request.post(`${fixture.url}/_test/deployment-access?rollback=true&multiple_targets=true`)
            ).ok(),
        ).toBe(true);
        const deployment = `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync/deploy`;
        const current = `environment_slug=dev&spec_hash=${"c".repeat(64)}`;
        const rollback = page.getByRole("region", { name: "Rollback selection", exact: true });
        const review = page.getByRole("button", { name: "Review target", exact: true });
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        for (const [label, submit, source] of [
            ["Runtime environment", review, "deployment-form"],
            [
                "Switch project runtime target",
                page.getByRole("button", { name: "Switch runtime target", exact: true }),
                "shell",
            ],
        ]) {
            await visit(page, `${deployment}?${current}`);
            await activate(page.getByRole("link", { name: "Review previous resolution for rollback" }));
            await expect(rollback).toContainText(`sha256:${"d".repeat(64)}`);
            await activate(review);
            await expect(page.getByRole("button", { name: "Roll back in dev", exact: true })).toBeVisible();
            await expect(rollback).toContainText(`sha256:${"d".repeat(64)}`);

            const target = page.getByRole("combobox", { name: label, exact: true });
            const beforeReview = page.url();
            await tabTo(page, target);
            await page.keyboard.press("Space");
            await page.keyboard.press("End");
            await page.keyboard.press("Enter");
            await expect(target).toBeFocused();
            await expect(page).toHaveURL(beforeReview);
            await activate(submit);
            await expect(page).toHaveURL(
                (url) =>
                    url.searchParams.get("environment_slug") === "staging" &&
                    !url.searchParams.has("resolution_digest"),
            );
            const targetHeading = page.getByRole("heading", { name: "Target: staging", exact: true });
            await expect(targetHeading).toBeFocused();
            await expect(targetHeading).toHaveAccessibleDescription(
                /rollback selection belongs to another environment/,
            );
            await expect(rollback).toHaveCount(0);
            await expect(page.getByRole("button", { name: "Deploy to staging", exact: true })).toBeVisible();
            await expect(page.getByText("Expected deployment revision: 3", { exact: true })).toBeVisible();
            const binding = page.getByRole("link", { name: "Resolve selected version bindings" });
            await expect(binding).toHaveAttribute("href", /environment_slug=staging/);
            await assess(page, testInfo, `deployment-staging-target-${source}`);
            await activate(binding);
            await expect(page.getByRole("combobox", { name: "Data source for source in staging" })).toHaveValue("");
            await activate(page.getByRole("link", { name: "Return to selected pipeline", exact: true }));
            await expect(page).toHaveURL(
                (url) =>
                    url.searchParams.get("environment_slug") === "staging" &&
                    !url.searchParams.has("resolution_digest"),
            );
            await expect(rollback).toHaveCount(0);
        }
    });
});
