const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("partial Git sync reports failed definitions and supports a keyboard retry", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const simulate = `${fixture.url}/_test/git-sync-partial`;
        expect((await page.request.post(simulate)).ok()).toBe(true);
        await visit(page, `${fixture.url}/orgs/acme/projects/ops/pipeline-sync?environment_slug=dev`);
        await expect(page).toHaveTitle("Sync from Git");
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        const preview = page.getByRole("button", { name: "Preview Git changes", exact: true });
        const apply = page.getByRole("button", { name: "Apply Git changes", exact: true });
        const results = page.getByRole("region", { name: "Git sync results", exact: true });
        const failed = results.getByRole("row", { name: /Failed.*pipelines\/broken\.yaml/ });
        await activate(preview);
        await expect(page.getByRole("heading", { name: "Sync preview", exact: true })).toBeFocused();
        await expect(failed).toContainText("Check the committed pipeline definition");
        await assess(page, testInfo, "git-sync-preview-with-failure");
        await activate(apply);
        const warning = page.getByRole("alert").filter({ hasText: "Sync completed with failures." });
        await expect(warning).toBeFocused();
        await expect(warning).toContainText("1 synced");
        await expect(warning).toContainText("1 failed");
        await expect(failed).not.toContainText("synthetic provider detail");
        await expect(failed.getByRole("link")).toHaveCount(0);
        await assess(page, testInfo, "git-sync-partial-apply");
        await page.keyboard.press("Tab");
        await expect(results).toBeFocused();
        if (page.viewportSize().width === 320) {
            await page.keyboard.press("ArrowRight");
            await expect.poll(() => results.evaluate((element) => element.scrollLeft)).toBeGreaterThan(0);
        }
        const deploy = results.getByRole("link", { name: "Deploy version", exact: true });
        await page.keyboard.press("Tab");
        await expect(deploy).toBeFocused();
        await expect(deploy).toHaveAttribute("href", new RegExp(`daily-sync/deploy.*spec_hash=${"a".repeat(64)}`));
        await page.keyboard.press("Tab");
        await expect(results.getByRole("link", { name: "Bindings and details", exact: true })).toBeFocused();
        await page.keyboard.press("Tab");
        const retry = page.getByRole("button", { name: "Preview Git changes again", exact: true });
        await expect(retry).toBeFocused();

        expect((await page.request.post(`${simulate}?corrected=true`)).ok()).toBe(true);
        await page.keyboard.press("Enter");
        await expect(page.getByRole("heading", { name: "Sync preview", exact: true })).toBeFocused();
        await expect(failed).toHaveCount(0);
        await expect(results.getByRole("row", { name: /Synced.*pipelines\/broken\.yaml/ })).toBeVisible();
        await expect(page.locator('input[name="commit"]')).toHaveValue("b".repeat(40));
        await activate(apply);
        await expect(page.getByRole("status").filter({ hasText: "Git sync completed" })).toBeFocused();
        await assess(page, testInfo, "git-sync-partial-recovered");
    });
});

test("deployment without an available version gives keyboard users a way back", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/deployment-access?rollback=true`)).ok()).toBe(true);
        const pipeline = `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync`;
        for (const [hash, message, state] of [
            ["", "Select an immutable version from the pipeline details.", "no-version"],
            ["f".repeat(64), "The selected pipeline version is unavailable.", "missing-version"],
        ]) {
            const response = await page.goto(`${pipeline}/deploy?environment_slug=dev&spec_hash=${hash}`);
            expect(response.status()).toBe(hash ? 200 : 400);
            await expect(page).toHaveTitle("Deploy pipeline version");
            const alert = page.getByRole("alert");
            await expect(alert).toContainText(message);
            await expect(alert).toBeFocused();
            await expect(page.getByRole("button", { name: "Compile selected version" })).toHaveCount(0);
            await expect(page.getByRole("button", { name: "Deploy to dev" })).toHaveCount(0);
            await assess(page, testInfo, `deployment-${state}`);
            await page.keyboard.press("Tab");
            await expect(page.getByRole("link", { name: "Back to pipeline details", exact: true })).toBeFocused();
            await page.keyboard.press("Enter");
            await expect(page).toHaveURL(`${pipeline}?environment_slug=dev`);
        }
    });
});

test("protected binding and deployment support keyboard confirmation and retry", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        await visit(page, `${fixture.url}/orgs/acme/projects/ops/pipelines`);
        await expect(page).toHaveTitle("Pipelines");
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        await activate(page.getByRole("link", { name: "Sync from Git", exact: true }));
        const previewGit = page.getByRole("button", { name: "Preview Git changes" });
        const applyGit = page.getByRole("button", { name: "Apply Git changes" });
        const preview = page.getByRole("heading", { name: "Sync preview" });
        await activate(previewGit);
        await expect(preview).toBeVisible();
        expect((await page.request.post(`${fixture.url}/_test/git-source-changed`)).ok()).toBe(true);
        await activate(applyGit);
        const syncError = page.getByRole("alert").filter({ hasText: "Git source changed since preview." });
        await expect(syncError).toBeFocused();
        await expect(applyGit).toHaveCount(0);
        await assess(page, testInfo, "git-sync-stale-preview");
        await expect(syncError).toBeFocused();
        await page.keyboard.press("Tab");
        await expect(previewGit).toBeFocused();
        await page.keyboard.press("Enter");
        await expect(preview).toBeFocused();
        await page.keyboard.press("Tab");
        const results = page.getByRole("region", { name: "Git sync results", exact: true });
        await expect(results).toBeFocused();
        await assess(page, testInfo, "git-sync-retry-preview");
        if (page.viewportSize().width === 320) {
            await page.keyboard.press("ArrowRight");
            await expect.poll(() => results.evaluate((element) => element.scrollLeft)).toBeGreaterThan(0);
        }
        await activate(applyGit);
        await expect(page.getByRole("status").filter({ hasText: "Git sync completed" })).toBeFocused();
        await assess(page, testInfo, "git-sync-complete");
        await activate(page.getByRole("link", { name: "Deploy version", exact: true }));
        const version = await page.locator('input[name="spec_hash"]').first().inputValue();
        await expect(page).toHaveTitle("Deploy pipeline version");
        await activate(page.getByRole("link", { name: "Resolve selected version bindings" }));
        await expect(page).toHaveTitle("Resolve pipeline bindings");
        const source = page.getByRole("combobox", { name: "Data source for source in dev" });
        await tabTo(page, source);
        await page.keyboard.press("w");
        await expect(source).toHaveValue("warehouse");
        const bind = page.getByRole("button", { name: "Bind", exact: true });
        await activate(bind);
        const dialog = page.getByRole("dialog", { name: "Confirm protected action" });
        const confirmBinding = dialog.getByRole("textbox", { name: "Type dev to confirm" });
        const save = dialog.getByRole("button", { name: "Save binding", exact: true });
        await expect(confirmBinding).toBeFocused();
        await expect(save).toBeDisabled();
        await page.keyboard.insertText("prod");
        await expect(save).toBeDisabled();
        await page.keyboard.press("Escape");
        await expect(dialog).toBeHidden();
        await expect(bind).toBeFocused();

        await page.keyboard.press("Enter");
        await expect(confirmBinding).toHaveValue("");
        await page.keyboard.insertText("dev");
        await expect(save).toBeEnabled();
        await dialog.evaluate((element) =>
            Promise.allSettled(element.getAnimations({ subtree: true }).map((animation) => animation.finished)),
        );
        await assess(page, testInfo, "protected-source-binding-confirmation");
        await activate(save);
        await expect(page.getByRole("button", { name: "Update", exact: true })).toBeVisible();
        await expect(page.locator("#binding-cell-daily-sync-source-dev")).toBeFocused();
        await activate(page.getByRole("link", { name: "Return to selected pipeline" }));
        const compile = page.getByRole("button", { name: "Compile selected version", exact: true });
        const error = page.getByRole("alert");
        for (const [disclosure, label, value, reason, state] of [
            ["Rich projection selection", "Projection selection JSON", "{", "projection selection", "projection"],
            ["Resource scope selection", "Resource scope selection JSON", "[", "resource scope", "resource-scope"],
        ]) {
            await activate(page.getByText(disclosure, { exact: true }));
            const field = page.getByRole("textbox", { name: label, exact: true });
            await tabTo(page, field);
            await page.keyboard.insertText(value);
            await activate(compile);
            await expect(error).toContainText(`Enter one valid ${reason}`);
            await expect(error).toBeFocused();
            await expect(field).toBeVisible();
            await expect(field).toHaveValue(value);
            await expect(field).toHaveAttribute("aria-invalid", "true");
            await expect(field).toHaveAttribute("aria-errormessage", "pipeline-deployment-feedback");
            await assess(page, testInfo, `compile-invalid-${state}`);
            await tabTo(page, field);
            await page.keyboard.press("ControlOrMeta+a");
            await page.keyboard.press("Backspace");
        }

        expect((await page.request.post(`${fixture.url}/_test/deployment-access?reject_compile=true`)).ok()).toBe(true);
        await activate(compile);
        await expect(error).toContainText("Compiler access denied for this version.");
        await expect(error).toBeFocused();
        await expect(page.getByRole("button", { name: "Deploy to dev", exact: true })).toHaveCount(0);
        await assess(page, testInfo, "compile-permission-refusal");

        await activate(compile);
        await expect(page.getByRole("region", { name: "Compiled plan" })).toContainText("available");
        await expect(page.getByRole("heading", { name: "Compiled plan", exact: true })).toBeFocused();

        const confirmation = page.getByRole("textbox", { name: /^Confirm protected environment/ });
        const deploy = page.getByRole("button", { name: "Deploy to dev", exact: true });
        await tabTo(page, confirmation);
        await page.keyboard.insertText("prod");
        await activate(deploy);
        await expect(error).toContainText("Invalid version or protected environment confirmation");
        await expect(error).toBeFocused();
        await assess(page, testInfo, "protected-deployment-refused");

        await tabTo(page, confirmation);
        await page.keyboard.insertText("dev");
        await activate(deploy);
        await expect(page.getByRole("status").filter({ hasText: `Deployed ${version} to dev` })).toBeFocused();
        await assess(page, testInfo, "protected-deployment-complete");
    });
});

test("removed runtime targets give keyboard users a way to recover deployment review", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const simulate = (query) => page.request.post(`${fixture.url}/_test/deployment-access?rollback=true&${query}`);
        expect((await simulate("multiple_targets=true")).ok()).toBe(true);
        const version = "a".repeat(64);
        const deployment = `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync/deploy?spec_hash=${version}&environment_slug=staging`;
        await visit(page, deployment);
        const deploy = page.getByRole("button", { name: "Deploy to staging", exact: true });
        await tabTo(page, deploy);
        expect((await simulate("multiple_targets=false")).ok()).toBe(true);
        await page.keyboard.press("Enter");

        const alert = page.getByRole("alert").filter({ hasText: "Choose an environment enabled for this project." });
        await expect(alert).toBeFocused();
        await expect(alert).toBeInViewport({ ratio: 1 });
        const form = page.locator("#pipeline-git-workflow");
        const target = form.getByRole("combobox", { name: "Runtime environment", exact: true });
        const bind = form.getByRole("link", { name: "Resolve selected version bindings", exact: true });
        const compile = form.getByRole("button", { name: "Compile selected version", exact: true });
        await expect(deploy).toHaveCount(0);
        await expect(bind).toHaveCount(0);
        await expect(compile).toHaveCount(0);
        await expect(target).toHaveValue("");
        await assess(page, testInfo, "deployment-target-disabled");

        await page.keyboard.press("Tab");
        await expect(target).toBeFocused();
        await page.keyboard.press("Space");
        await page.keyboard.press("End");
        await page.keyboard.press("Enter");
        const review = form.getByRole("button", { name: "Review target", exact: true });
        await tabTo(page, review);
        await page.keyboard.press("Enter");
        await expect(form.getByRole("heading", { name: "Target: dev", exact: true })).toBeFocused();
        await expect(bind).toHaveAttribute("href", /environment_slug=dev/);
        await expect(compile).toBeVisible();
        await expect(form.getByText(version, { exact: true }).first()).toBeVisible();
        await expect(form.getByRole("textbox", { name: /^Confirm protected environment/ })).toBeVisible();
        await assess(page, testInfo, "deployment-target-review-recovered");

        expect((await simulate("no_targets=true")).ok()).toBe(true);
        await visit(page, deployment);
        await expect(alert).toBeFocused();
        await expect(bind).toHaveCount(0);
        await expect(compile).toHaveCount(0);
        await expect(review).toHaveCount(0);
        await expect(target).toHaveCount(0);
        await page.keyboard.press("Tab");
        await expect(form.getByRole("link", { name: "Manage enabled environments", exact: true })).toBeFocused();
        await assess(page, testInfo, "deployment-no-enabled-targets");
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(`${fixture.url}/orgs/acme/projects/ops/environments`);
        await expect(page.getByRole("heading", { name: "No environments enabled", exact: true })).toBeVisible();
    });
});

test("read-only deployment can inspect a retained plan by keyboard", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/deployment-access?read_only=true`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync/deploy?environment_slug=dev&spec_hash=${"a".repeat(64)}`,
        );
        await expect(page.getByText("You can inspect retained plans.", { exact: false })).toBeVisible();
        await expect(page.getByRole("button", { name: "Compile selected version", exact: true })).toHaveCount(0);
        await expect(page.getByText("Rich projection selection", { exact: true })).toHaveCount(0);
        await expect(page.getByRole("button", { name: "Deploy to dev", exact: true })).toHaveCount(0);
        const digest = `sha256:${"b".repeat(64)}`;
        const selection = page.getByRole("textbox", { name: "Exact compiled plan digest", exact: true });
        await tabTo(page, selection);
        await page.keyboard.insertText(digest);
        await tabTo(page, page.getByRole("button", { name: "Inspect selection", exact: true }));
        await page.keyboard.press("Enter");
        await expect(page.getByRole("heading", { name: "Compiled plan", exact: true })).toBeFocused();
        await expect(page.getByRole("region", { name: "Compiled plan", exact: true })).toContainText(digest);
        await assess(page, testInfo, "deployment-retained-plan-read-only");
    });
});

test("deployment and Git sync access denials give keyboard users a way back", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        const simulate = (mode) => page.request.post(`${fixture.url}/_test/deployment-access?${mode}`);
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await simulate("read_only=true")).ok()).toBe(true);
        const pipelines = `${fixture.url}/orgs/acme/projects/ops/pipelines`;
        const deployment = `${pipelines}/daily-sync/deploy?environment_slug=dev&spec_hash=${"a".repeat(64)}`;
        const feedback = page.getByRole("alert").filter({ hasText: "You need permission to" });
        const returnTo = async (name, url) => {
            await expect(feedback).toBeFocused();
            await page.keyboard.press("Tab");
            const link = page.getByRole("link", { name, exact: true });
            await expect(link).toBeFocused();
            await page.keyboard.press("Enter");
            await expect(page).toHaveURL(url);
        };

        const sync = await page.goto(`${fixture.url}/orgs/acme/projects/ops/pipeline-sync?environment_slug=dev`);
        expect(sync.status()).toBe(403);
        await expect(feedback).toContainText("update pipelines to sync from Git");
        await expect(feedback).toBeFocused();
        await assess(page, testInfo, "git-sync-access-denied");
        await returnTo("Back to pipelines", pipelines);

        expect((await simulate("no_read=true")).ok()).toBe(true);
        const denied = await page.goto(deployment);
        expect(denied.status()).toBe(403);
        await expect(feedback).toContainText("read pipelines to inspect deployments");
        await expect(page.getByText("Selected immutable version", { exact: true })).toHaveCount(0);
        await expect(feedback).toBeFocused();
        await assess(page, testInfo, "deployment-read-denied");
        await returnTo("Back to organizations", `${fixture.url}/organizations`);

        expect((await simulate("read_only=false&rollback=true")).ok()).toBe(true);
        await visit(page, deployment);
        const confirmation = page.getByRole("textbox", { name: /^Confirm protected environment/ });
        await tabTo(page, confirmation);
        await page.keyboard.insertText("dev");
        await tabTo(page, page.getByRole("button", { name: "Deploy to dev", exact: true }));
        expect((await simulate("read_only=true")).ok()).toBe(true);
        const submit = page.waitForResponse(
            (response) =>
                response.request().method() === "POST" && response.url().startsWith(`${pipelines}/daily-sync/deploy`),
        );
        await page.keyboard.press("Enter");
        expect((await submit).status()).toBe(403);
        await expect(feedback).toContainText("update pipelines to compile or deploy");
        await expect(page.getByRole("button", { name: "Deploy to dev", exact: true })).toHaveCount(0);
        await expect(feedback).toBeFocused();
        await expect(page.getByText("Selected immutable version", { exact: true })).toBeVisible();
        await assess(page, testInfo, "deployment-update-denied");
        await returnTo("Review current deployment", /\/deploy\?(?=.*environment_slug=dev)(?=.*spec_hash=)/);
        await expect(page.getByRole("button", { name: "Deploy to dev", exact: true })).toHaveCount(0);
    });
});

test("deployment binding review stays keyboard accessible when permissions change", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        const permissions = (mode) => page.request.post(`${fixture.url}/_test/deployment-access?${mode}`);
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await permissions("read_only=true")).ok()).toBe(true);
        const version = "a".repeat(64);
        const digest = `sha256:${"b".repeat(64)}`;
        const deployment = new URL("/orgs/acme/projects/ops/pipelines/daily-sync/deploy", fixture.url);
        deployment.search = new URLSearchParams({
            environment_slug: "dev",
            spec_hash: version,
            compiled_plan_digest: digest,
        }).toString();
        await visit(page, deployment.href);
        const activate = async (link) => {
            await tabTo(page, link);
            await page.keyboard.press("Enter");
        };
        const bindings = page.getByRole("link", { name: "Resolve selected version bindings", exact: true });
        await activate(bindings);
        await expect(page.getByText(/Updating bindings requires data-source update permission/)).toBeVisible();
        await expect(page.getByText("Selected immutable version:", { exact: false })).toContainText(version);
        await expect(page.getByRole("combobox", { name: "Data source for source in dev" })).toBeDisabled();
        await expect(page.getByRole("button", { name: "Bind", exact: true })).toBeDisabled();
        await activate(page.getByRole("link", { name: "Review source", exact: true }));
        const binding = page.locator("#binding-cell-daily-sync-source-dev");
        await expect(binding).toBeFocused();
        await expect(binding.getByText("missing", { exact: true })).toBeVisible();
        await assess(page, testInfo, "deployment-read-only-binding-review");

        await activate(page.getByRole("link", { name: "Return to selected pipeline", exact: true }));
        await expect(page.getByRole("heading", { name: "Compiled plan", exact: true })).toBeFocused();
        await expect(page.getByRole("region", { name: "Compiled plan", exact: true })).toContainText(digest);

        await tabTo(page, bindings);
        expect((await permissions("no_read=true")).ok()).toBe(true);
        const denied = page.waitForResponse((response) => new URL(response.url()).pathname.endsWith("/bindings"));
        await page.keyboard.press("Enter");
        expect((await denied).status()).toBe(403);
        const alert = page.getByRole("alert").filter({ hasText: "You need permission to read pipelines" });
        await expect(alert).toBeFocused();
        await expect(alert).toBeInViewport({ ratio: 1 });
        await expect(page.getByText("Selected immutable version:", { exact: false })).toHaveCount(0);
        await assess(page, testInfo, "deployment-binding-access-denied");
        await page.keyboard.press("Tab");
        await expect(page.getByRole("link", { name: "Back to organizations", exact: true })).toBeFocused();
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(`${fixture.url}/organizations`);
    });
});

test("binding save denial focuses the error when edit permission changes", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        const permissions = `${fixture.url}/_test/deployment-access`;
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${permissions}?read_only=true`)).ok()).toBe(true);
        expect((await page.request.post(`${permissions}?read_only=false`)).ok()).toBe(true);
        const version = "a".repeat(64);
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync/deploy?environment_slug=dev&spec_hash=${version}`,
        );
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        await activate(page.getByRole("link", { name: "Resolve selected version bindings", exact: true }));
        const source = page.getByRole("combobox", { name: "Data source for source in dev" });
        await tabTo(page, source);
        await page.keyboard.press("w");
        await expect(source).toHaveValue("warehouse");
        await activate(page.getByRole("button", { name: "Bind", exact: true }));
        const dialog = page.getByRole("dialog", { name: "Confirm protected action" });
        await expect(dialog.getByRole("textbox", { name: "Type dev to confirm" })).toBeFocused();
        await page.keyboard.insertText("dev");
        await tabTo(page, dialog.getByRole("button", { name: "Save binding", exact: true }));
        expect((await page.request.post(`${permissions}?read_only=true`)).ok()).toBe(true);
        await page.keyboard.press("Enter");

        const alert = page
            .getByRole("alert")
            .filter({ hasText: "Updating bindings requires data-source update permission" });
        await expect(alert).toBeFocused();
        await expect(alert).toBeInViewport({ ratio: 1 });
        await expect(dialog).toBeHidden();
        await expect(source).toBeDisabled();
        await expect(page.getByRole("button", { name: "Bind", exact: true })).toBeDisabled();
        await expect(page.getByText("Selected immutable version:", { exact: false })).toContainText(version);
        await assess(page, testInfo, "deployment-binding-save-denied");
        await activate(page.getByRole("link", { name: "Return to selected pipeline", exact: true }));
        await expect(page.getByText("You can inspect retained plans.", { exact: false })).toBeVisible();
    });
});

test("protected binding removal can be canceled and recovered after permission changes", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        const permissions = `${fixture.url}/_test/deployment-access`;
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${permissions}?rollback=true`)).ok()).toBe(true);
        const version = "a".repeat(64);
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync/deploy?environment_slug=dev&spec_hash=${version}`,
        );
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        await activate(page.getByRole("link", { name: "Resolve selected version bindings", exact: true }));
        await expect(page).toHaveTitle("Resolve pipeline bindings");
        const resolver = page.url();
        const binding = page.locator("#binding-cell-daily-sync-source-dev");
        const remove = binding.getByRole("button", {
            name: "Remove source binding in dev for daily-sync",
            exact: true,
        });
        const source = binding.getByRole("combobox", { name: "Data source for source in dev" });
        await expect(source).toHaveValue("warehouse");
        await activate(remove);
        const dialog = page.getByRole("dialog", { name: "Confirm protected action" });
        await expect(dialog).toHaveAccessibleDescription(
            "Remove the source binding from dev. The pipeline will lose this data-source binding in this target.",
        );
        const confirmation = dialog.getByRole("textbox", { name: "Type dev to confirm", exact: true });
        const submit = dialog.getByRole("button", { name: "Remove binding", exact: true });
        await expect(confirmation).toBeFocused();
        await expect(submit).toBeDisabled();
        await page.keyboard.insertText("prod");
        await expect(submit).toBeDisabled();
        await page.keyboard.press("Escape");
        await expect(dialog).toBeHidden();
        await expect(remove).toBeFocused();
        await expect(source).toHaveValue("warehouse");

        await page.keyboard.press("Enter");
        await expect(confirmation).toHaveValue("");
        await page.keyboard.insertText("dev");
        await expect(submit).toBeEnabled();
        await dialog.evaluate((element) =>
            Promise.allSettled(element.getAnimations({ subtree: true }).map((animation) => animation.finished)),
        );
        await assess(page, testInfo, "protected-binding-removal-confirmation");
        expect((await page.request.post(`${permissions}?read_only=true`)).ok()).toBe(true);
        await activate(submit);
        const alert = page
            .getByRole("alert")
            .filter({ hasText: "Removing bindings requires data-source delete permission" });
        await expect(alert).toBeFocused();
        await expect(alert).toBeInViewport({ ratio: 1 });
        await expect(dialog).toBeHidden();
        await expect(source).toHaveValue("warehouse");
        await expect(remove).toBeDisabled();
        await assess(page, testInfo, "protected-binding-removal-refused");

        expect((await page.request.post(permissions)).ok()).toBe(true);
        await visit(page, resolver);
        await activate(remove);
        await expect(confirmation).toBeFocused();
        await page.keyboard.insertText("dev");
        await activate(submit);
        await expect(binding).toBeFocused();
        await expect(remove).toHaveCount(0);
        await expect(binding.getByRole("button", { name: "Bind", exact: true })).toBeVisible();
        await expect(page.getByText("Selected immutable version:", { exact: false })).toContainText(version);
        await assess(page, testInfo, "protected-binding-removed");
    });
});
