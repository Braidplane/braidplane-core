const { test, expect, captureE2EEvidenceScreenshot } = require("./support/smoke-fixtures.cjs");
const { withSSRFixture } = require("./support/ssr-fixture.cjs");

// Each SSR fixture owns its session store; application cookies belong to a different server.
test.use({ storageState: { cookies: [], origins: [] } });

test("refreshes contract evidence and recorded status during an active run", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/running`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev`);
        await expect(page.locator("#run-recorded-status")).toHaveText("running");
        expect((await page.request.post(`${fixture.url}/_test/run/running?contract_failure=true`)).ok()).toBe(true);
        await expect(page.locator("#run-outcome")).toContainText("Field product_code · required.");
        await expect(page.locator("#run-recorded-status")).toHaveText("running");
        await captureE2EEvidenceScreenshot(page, testInfo, "contract-failure-active-desktop.png", { fullPage: true });
        expect((await page.request.post(`${fixture.url}/_test/run/failed?contract_failure=true`)).ok()).toBe(true);
        await expect(page.locator("#run-recorded-status")).toHaveText("failed");
        await expect(page.locator("#run-metadata")).toContainText("2026-10-01T10:00:02Z");
        await expect(page.locator("#run-metadata")).not.toContainText("Manual retry");
        await expect(page.locator("#run-outcome")).not.toHaveAttribute("hx-trigger");
        await page.setViewportSize({ width: 390, height: 844 });
        await expect(page.locator("#run-outcome")).toContainText("product_code");
        expect(
            await page.locator("#run-outcome").evaluate((element) => element.scrollWidth <= element.clientWidth),
        ).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "contract-failure-terminal-compact.png", { fullPage: true });
    });
});

test("inspects a successful run and returns to its scoped list", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/succeeded`)).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/orgs/acme/projects/ops/runs?environment_slug=dev&status=succeeded&preview=closed`,
        );
        await page.locator('[id^="run-opener-"]').first().click();
        const preview = page.locator("[data-entity-preview]");
        await expect(preview).toContainText("succeeded");
        const selectedURL = page.url();
        await preview.getByRole("link", { name: "Open full page" }).click();
        await expect(page.locator("#run-outcome")).toContainText("succeeded");
        await expect(page.getByRole("region", { name: "Sink delivery outcomes" })).toContainText(
            "Acknowledged by sink",
        );
        await page
            .getByRole("navigation", { name: "Run sections" })
            .getByRole("link", { name: "Steps", exact: true })
            .click();
        await expect(page.locator("#run-outputs")).toBeVisible();
        const evidence = page.getByRole("region", { name: "Contract evidence", exact: true });
        await expect(evidence).toContainText("historical-v1");
        await expect(evidence).toContainText("input:orders");
        await expect(evidence).toContainText("original-warehouse");
        await expect(evidence).toContainText("old-operation");
        await evidence.locator("summary").first().click();
        await expect(evidence).toContainText("sha256:executed-plan");
        await expect(evidence).toContainText("Quality and freshness remain unverified");
        await expect(evidence).toContainText("Resource scope: s3/relative-key/v1 · sink · customers / v1 / row · rows");
        await captureE2EEvidenceScreenshot(page, testInfo, "alpha-run.png", { fullPage: true });
        const desktopViewport = page.viewportSize();
        await page.setViewportSize({ width: 390, height: 844 });
        await evidence.locator("summary").first().scrollIntoViewIfNeeded();
        await expect(evidence.locator("summary").first()).toBeInViewport();
        expect(await evidence.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "alpha-run-compact.png", { fullPage: true });
        await page.setViewportSize(desktopViewport);
        await page.getByRole("link", { name: "Back to runs", exact: true }).click();
        await expect(page).toHaveURL(selectedURL);
        await expect(preview).toBeVisible();
        await expect(page.locator('select[name="status"]')).toHaveValue("succeeded");
    });
});

test("compares exact retained contract projections without losing named input context", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/succeeded`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/contracts?contract=orders&revision=v1&section=impact`);
        await page.getByLabel("Compare with revision").fill("v2");
        await page.getByText("Select retained executable projections", { exact: true }).click();
        await page.getByLabel("Source pipeline", { exact: true }).fill("daily-sync");
        await page.getByLabel("Target pipeline", { exact: true }).fill("daily-sync");
        await page.getByLabel("Source plan digest", { exact: true }).fill(`sha256:${"a".repeat(64)}`);
        await page.getByLabel("Target plan digest", { exact: true }).fill(`sha256:${"b".repeat(64)}`);
        await page.getByLabel("Source plan digest", { exact: true }).fill("invalid-plan-digest");
        await page.getByRole("button", { name: "Compare", exact: true }).click();
        await expect(page.getByRole("alert")).toContainText("Provide both pipeline slugs");
        await page.getByText("Select retained executable projections", { exact: true }).click();
        await page.getByLabel("Source plan digest", { exact: true }).fill(`sha256:${"a".repeat(64)}`);
        await page.getByRole("button", { name: "Compare", exact: true }).click();
        const comparison = page.getByRole("region", { name: "Compare revisions", exact: true });
        await expect(comparison).toContainText("selected_projection");
        await expect(comparison).toContainText("structural_data_path_changed");
        await expect(comparison).toContainText("input:orders");
        await expect(comparison).toContainText("External consumers are unknown");
        await captureE2EEvidenceScreenshot(page, testInfo, "contract-impact-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        await comparison.scrollIntoViewIfNeeded();
        expect(await comparison.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "contract-impact-compact.png", { fullPage: true });
    });
});

test("inspects exact authored definitions with shared keyboard and compact selection", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/succeeded`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev`);
        const initialViewport = page.viewportSize();
        await page.setViewportSize({ width: 320, height: 800 });
        const menuButton = page.getByRole("button", { name: "Open navigation menu", exact: true });
        const menu = page.getByRole("navigation", { name: "Mobile primary", exact: true });
        await menuButton.focus();
        await page.keyboard.press("Enter");
        await expect(menu).toBeVisible();
        await expect(menu).toBeInViewport();
        const popover = page.locator("#shell-mobile-navigation");
        await expect(popover).toHaveCSS("opacity", "1");
        expect(await popover.evaluate((element) => getComputedStyle(element).backgroundColor)).not.toBe(
            "rgba(0, 0, 0, 0)",
        );
        await captureE2EEvidenceScreenshot(page, testInfo, "shell-compact-navigation.png", { fullPage: false });
        await page.keyboard.press("Enter");
        await expect(menu).toBeHidden();
        await page.keyboard.press("Enter");
        await expect(menu).toBeVisible();
        await menu.getByRole("link", { name: "Queries", exact: true }).focus();
        await page.keyboard.press("Escape");
        await expect(menu).toBeHidden();
        await expect(menuButton).toBeFocused();
        await page.keyboard.press("Enter");
        await expect(menu).toBeVisible();
        await page.getByRole("heading", { level: 1 }).click();
        await expect(menu).toBeHidden();
        await page.setViewportSize(initialViewport);
        await page.getByRole("link", { name: "Definition", exact: true }).click();
        const inspector = page.locator("[data-definition-inspector]");
        await expect(inspector).toHaveCount(1);
        await expect(inspector).toContainText("Executed version");
        await expect(page.getByRole("link", { name: "Definition", exact: true })).toHaveAttribute(
            "aria-current",
            "page",
        );
        const step = inspector
            .getByRole("navigation", { name: "Definition steps" })
            .getByRole("link", { name: "2. Normalize customers", exact: true });
        await step.focus();
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/lineage_step=normalize/);
        await expect(inspector).toContainText("Selected step: normalize");
        await expect(inspector).toContainText("SELECT email FROM input");
        await inspector.getByRole("link", { name: "Serialized", exact: true }).click();
        const serialized = inspector.getByLabel("Serialized definition");
        await expect(serialized).toContainText('"slug": "normalize"');
        await expect(serialized).not.toContainText('"slug": "store"');
        const selectedURL = page.url();
        await inspector.getByRole("link", { name: "Whole definition", exact: true }).click();
        await expect(serialized).toContainText('"slug": "store"');
        await page.goBack();
        await expect(page).toHaveURL(selectedURL);
        await expect(serialized).not.toContainText('"slug": "store"');
        await page.setViewportSize({ width: 1280, height: 900 });
        await captureE2EEvidenceScreenshot(page, testInfo, "definition-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 375, height: 812 });
        await inspector.getByRole("link", { name: "Structured", exact: true }).click();
        await expect(inspector).toContainText("Selected step: normalize");
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "definition-compact.png", { fullPage: true });
        await page
            .getByRole("navigation", { name: "Run sections" })
            .getByRole("link", { name: "Steps", exact: true })
            .click();
        await page.getByRole("link", { name: "store", exact: true }).click();
        await expect(inspector).toContainText("Selected step: store");
        await page.getByRole("link", { name: "Open pipeline", exact: true }).click();
        await page.getByRole("link", { name: "Inspect definition", exact: true }).click();
        await expect(inspector).toContainText("Current materialization");
        await expect(inspector).toContainText("Whole authored definition");
        await inspector.getByRole("link", { name: "Serialized", exact: true }).click();
        await expect(inspector.getByLabel("Serialized definition")).toContainText('"slug": "daily-sync"');
    });
});

test("diagnoses the recorded failure and retains context after recovery refusal", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        const expectNamedControls = async () => {
            for (const role of ["textbox", "combobox", "checkbox", "button", "link"]) {
                await expect(page.getByRole(role, { name: "", exact: true })).toHaveCount(0);
            }
        };
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/failed`)).ok()).toBe(true);
        const runURL = `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev`;
        await page.goto(runURL);
        await expect(page.locator("#run-outcome")).toContainText("Failed at step store");
        await expect(page.locator("#run-outcome")).toContainText("sink_delivery_unknown");
        await expectNamedControls();
        await expect(page.getByRole("region", { name: "Sink delivery outcomes" })).toContainText("Unknown delivery");
        await expect(page.locator("main")).toContainText("Root run-root / retry of run-root");
        await page.getByRole("link", { name: "Inspect failing step", exact: true }).click();
        const inspector = page.locator("[data-definition-inspector]");
        await expect(inspector).toContainText("Selected step: store");
        await expect(inspector.getByRole("heading", { name: "Inspect definition", exact: true })).toBeFocused();
        await page
            .getByRole("navigation", { name: "Run sections" })
            .getByRole("link", { name: "Steps", exact: true })
            .click();
        const graph = page.getByRole("region", { name: "Run lineage graph", exact: true });
        await expectNamedControls();
        await expect(page.getByRole("region", { name: "Sink delivery outcomes" })).toContainText(
            "provider may have applied",
        );
        await captureE2EEvidenceScreenshot(page, testInfo, "unknown-write-steps-desktop.png", { fullPage: true });
        await graph.getByRole("link", { name: /Transform step Normalize customers/ }).click();
        await page
            .getByRole("link", { name: /Inspect executed step & contract|Compare executed output contract/ })
            .click();
        await expect(inspector).toContainText("Selected step: normalize");
        await expect(inspector).toContainText("executed V1");
        await page
            .getByRole("navigation", { name: "Run sections" })
            .getByRole("link", { name: "Steps", exact: true })
            .click();
        await expect(page.getByRole("complementary", { name: "Selected step evidence", exact: true })).toContainText(
            "Started; no completion recorded",
        );
        const timeline = page.locator(".timeline");
        await expect(timeline.getByRole("link", { name: "normalize", exact: true })).toHaveAttribute(
            "aria-current",
            "true",
        );
        await timeline.getByRole("link", { name: "store", exact: true }).click();
        await expect(inspector).toContainText("Selected step: store");
        await page.setViewportSize({ width: 390, height: 844 });
        const definitionStep = inspector.getByRole("link", { name: "2. Normalize customers", exact: true });
        await definitionStep.focus();
        await page.keyboard.press("Enter");
        await expect(inspector).toContainText("Selected step: normalize");
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "run-failure-compact.png", { fullPage: true });
        await page.getByRole("link", { name: "Attempts & recovery", exact: true }).click();
        await expect(page).toHaveURL(/lineage_step=normalize/);
        await expect(page.locator("#run-attempts")).toContainText("Failed at step store");
        await expectNamedControls();
        await page.getByRole("button", { name: "Recover sink delivery", exact: true }).click();
        await expect(page).toHaveURL(/section=attempts/);
        await expect(page.getByRole("alert")).toContainText("Retained inputs were pruned; recovery refused.");
        await expect(page.locator("#run-attempts")).toContainText("Provider acknowledgement unavailable");
        await page.setViewportSize({ width: 1280, height: 900 });
        await expectNamedControls();
        await captureE2EEvidenceScreenshot(page, testInfo, "run-failure-recovery-desktop.png", { fullPage: true });
        await page.goto(`${runURL}#run-lineage`);
        await expect(page.locator("#run-lineage")).toBeVisible();
        await expect(page).toHaveURL(/section=steps/);
    });
});

test("navigates the complete 22-step lineage while preserving the selected view", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        await page.setViewportSize({ width: 1440, height: 1000 });
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/succeeded?topology=large`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`);
        const graph = page.getByRole("region", { name: "Run lineage graph", exact: true });
        const canvas = graph.locator("[data-lineage-canvas]");
        const controls = page.getByRole("group", { name: "Lineage view controls" });
        const nodes = canvas.locator('a[data-entity-focus="lineage-step-evidence"]');
        await expect(nodes).toHaveCount(22);
        const fits = () =>
            graph.evaluate((viewport) => {
                const box = viewport.getBoundingClientRect();
                const canvas = viewport.querySelector("[data-lineage-canvas]");
                const items = [canvas.querySelector("svg"), ...canvas.querySelectorAll("a")];
                return (
                    viewport.scrollWidth <= viewport.clientWidth &&
                    viewport.scrollHeight <= viewport.clientHeight &&
                    items.every((item) => {
                        const rect = item.getBoundingClientRect();
                        return (
                            rect.left >= box.left &&
                            rect.top >= box.top &&
                            rect.right <= box.right &&
                            rect.bottom <= box.bottom
                        );
                    })
                );
            });
        await expect.poll(fits).toBe(true);
        await graph.scrollIntoViewIfNeeded();
        await captureE2EEvidenceScreenshot(page, testInfo, "lineage-22-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 1100, height: 850 });
        await expect.poll(fits).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "lineage-22-laptop.png", { fullPage: true });
        await controls.getByRole("button", { name: "100%", exact: true }).click();
        await expect(controls.locator("output")).toHaveText("100%");
        await graph.focus();
        const initialTop = await graph.evaluate((element) => element.scrollTop);
        await page.keyboard.press("ArrowDown");
        await expect.poll(() => graph.evaluate((element) => element.scrollTop)).toBeGreaterThan(initialTop);
        const box = await graph.boundingBox();
        const beforePan = await graph.evaluate((element) => element.scrollTop);
        await page.mouse.move(box.x + 8, box.y + 150);
        await page.mouse.down();
        await page.mouse.move(box.x + 8, box.y + 80);
        await page.mouse.up();
        await expect.poll(() => graph.evaluate((element) => element.scrollTop)).toBeGreaterThan(beforePan + 50);
        await nodes.first().scrollIntoViewIfNeeded();
        const position = await graph.evaluate((element) => ({ left: element.scrollLeft, top: element.scrollTop }));
        await nodes.first().click();
        await expect(page.getByRole("complementary", { name: "Selected step evidence", exact: true })).toContainText(
            "Read supplier 1",
        );
        await expect(controls.locator("output")).toHaveText("100%");
        await expect
            .poll(() => graph.evaluate((element) => ({ left: element.scrollLeft, top: element.scrollTop })))
            .toEqual(position);

        await page
            .getByRole("navigation", { name: "Run sections" })
            .getByRole("link", { name: "Definition", exact: true })
            .click();
        await expect(page.locator("[data-definition-inspector]")).toContainText("Read supplier 1");
        await page
            .getByRole("navigation", { name: "Run sections" })
            .getByRole("link", { name: "Steps", exact: true })
            .click();
        await expect(controls.locator("output")).toHaveText("100%");
        await expect
            .poll(() => graph.evaluate((element) => ({ left: element.scrollLeft, top: element.scrollTop })))
            .toEqual(position);
        await controls.getByRole("button", { name: "Fit", exact: true }).click();
        await expect.poll(fits).toBe(true);
        await controls.getByRole("button", { name: "Zoom in", exact: true }).click();
        await expect(controls.getByRole("button", { name: "Fit", exact: true })).toHaveAttribute(
            "aria-pressed",
            "false",
        );
        await controls.getByRole("button", { name: "Fit", exact: true }).click();
        await expect.poll(fits).toBe(true);
        await page.setViewportSize({ width: 390, height: 844 });
        const compact = page.getByRole("list", { name: "Run lineage steps", exact: true });
        await expect(compact).toBeVisible();
        await expect(compact.locator("li")).toHaveCount(22);
        expect(await compact.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "lineage-22-compact.png", { fullPage: true });
    });
});

test("keeps lineage full width above step inspection", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/succeeded`)).ok()).toBe(true);
        await page.setViewportSize({ width: 1440, height: 1000 });
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`);
        const workbench = page.locator("#pipeline-run-lineage-workbench");
        const graph = page.getByRole("region", { name: "Run lineage graph", exact: true });
        const evidence = page.getByRole("complementary", { name: "Selected step evidence", exact: true });
        const inspector = page.locator("[data-definition-inspector]");
        await expect(graph).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "lineage-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        await captureE2EEvidenceScreenshot(page, testInfo, "lineage-compact.png", { fullPage: true });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await page.setViewportSize({ width: 1440, height: 1000 });
        const graphBox = await graph.boundingBox();
        const workbenchBox = await workbench.boundingBox();
        const inspectorBox = await evidence.boundingBox();
        expect(graphBox.width).toBeGreaterThan(workbenchBox.width - 4);
        expect(inspectorBox.y).toBeGreaterThanOrEqual(graphBox.y + graphBox.height);
        for (const kind of ["Source", "Transform", "Sink"]) {
            const node = graph.getByRole("link", { name: new RegExp(`${kind} step`) }).first();
            await node.scrollIntoViewIfNeeded();
            await expect(node).toBeInViewport();
        }
        await graph.getByRole("link", { name: /Transform step Normalize customers/ }).click();
        await expect(evidence).toContainText("Normalize customers");
        await expect(evidence).toBeVisible();
        await evidence
            .getByRole("link", { name: /Inspect executed step & contract|Compare executed output contract/ })
            .click();
        await expect(inspector).toContainText("Selected step: normalize");
        await expect(inspector).toContainText("SELECT email FROM input");
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await page.setViewportSize({ width: 390, height: 844 });
        await inspector.getByRole("link", { name: "3. Store customers", exact: true }).click();
        await expect(inspector).toContainText("Selected step: store");
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    });
});

test("inspects completion roots and follows exact parent and child links", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/retry_success`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev`);
        const navigation = page.getByRole("navigation", { name: "Run sections" });
        await navigation.getByRole("link", { name: "Completion", exact: true }).focus();
        await page.keyboard.press("Enter");
        const panel = page.locator("#run-completion");
        await expect(panel).toContainText("Closed · succeeded");
        await expect(panel).toContainText("immutable-selected-v2");
        await expect(panel).toContainText("revision 7");
        await expect(panel.getByRole("table")).toContainText("failed");
        await expect(panel.getByRole("table")).toContainText("succeeded");
        await page.setViewportSize({ width: 1440, height: 1000 });
        await captureE2EEvidenceScreenshot(page, testInfo, "completion-parent-desktop.png", { fullPage: true });
        await panel.getByRole("link", { name: "completion-child", exact: true }).focus();
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/\/runs\/completion-child\?.*section=completion/);
        await expect(panel).toContainText("Exact parent");
        await panel.getByRole("link", { name: "parent-final", exact: true }).click();
        await expect(page).toHaveURL(/\/runs\/parent-final\?.*section=completion/);
        await expect(panel).toContainText("immutable-upstream-v1");
        await page.setViewportSize({ width: 375, height: 812 });
        expect(await panel.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "completion-parent-compact.png", { fullPage: true });
        await panel.getByRole("link", { name: "after-upstream", exact: true }).click();
        const config = page.getByRole("region", { name: "Completion configuration" });
        await expect(config).toContainText("waiting for the next matching logical closure");
        await expect(config).toContainText("Current configuration preview");
        await expect(config).toContainText("Trigger revision 9");
        await captureE2EEvidenceScreenshot(page, testInfo, "completion-configuration-compact.png", { fullPage: true });
    });
});

test("selects environment run history on the pipeline and opens exact run step evidence", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/succeeded?history=true`)).ok()).toBe(true);
        const pipeline = `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync?environment_slug=dev`;
        await page.goto(pipeline);
        const preview = page.locator("[data-pipeline-run-preview]");
        await expect(preview.getByRole("heading", { name: "Latest run in dev" })).toBeVisible();
        await expect(preview).toContainText("run-inspection");
        await expect(preview).toContainText("Manual run");
        await expect(preview.getByRole("region", { name: "Run lineage graph" })).toBeVisible();
        await expect(preview.getByRole("button", { name: "Fit", exact: true })).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "pipeline-latest-run-desktop.png", { fullPage: true });
        expect(
            (await page.request.post(`${fixture.url}/_test/run/succeeded?history=true&deployment=changed`)).ok(),
        ).toBe(true);
        await page.reload();
        await expect(preview).toContainText("deployed now bbbbbbbbbbbb");
        await expect(preview.locator("[data-lineage-view]")).toHaveAttribute("data-lineage-view", /\|a{64}$/);

        await page
            .getByRole("navigation", { name: "Pipeline sections" })
            .getByRole("link", { name: "Runs", exact: true })
            .click();
        await preview
            .getByRole("navigation", { name: "Recent run selection" })
            .getByRole("link", { name: "failed · run-older", exact: true })
            .click();
        await expect(page).toHaveURL(/run_slug=run-older/);
        await expect(preview.getByRole("heading", { name: "Selected historical run in dev" })).toBeVisible();
        await expect(preview).toContainText("Historical failure");
        await expect(preview).toContainText("attempt 2");
        await expect(preview.getByRole("link", { name: "Open run", exact: true }).first()).toHaveAttribute(
            "href",
            /runs\/run-older\?environment_slug=dev/,
        );
        await page.setViewportSize({ width: 390, height: 844 });
        await expect(preview.getByRole("region", { name: "Run lineage graph" })).toBeHidden();
        expect(await preview.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "pipeline-selected-run-compact.png", { fullPage: true });
        await preview.locator('a[href*="lineage_step=fetch"]:visible').first().click();
        await expect(page).toHaveURL(/runs\/run-older\?/);
        expect(new URL(page.url()).searchParams.get("environment_slug")).toBe("dev");
        expect(new URL(page.url()).searchParams.get("lineage_step")).toBe("fetch");
        await expect(page.locator("#run-outputs")).toBeVisible();
    });
});

test("pinpoints source and named-input failures with historical definition navigation", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        for (const failure of ["source", "transform"]) {
            expect(
                (
                    await page.request.post(
                        `${fixture.url}/_test/run/failed?failure=${failure}&timezone=Europe%2FWarsaw`,
                    )
                ).ok(),
            ).toBe(true);
            await page.setViewportSize({ width: 1440, height: 1000 });
            await page.goto(
                `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`,
            );
            const graph = page.getByRole("region", { name: "Run lineage graph", exact: true });
            const selected = page.getByRole("complementary", { name: "Selected step evidence" });
            const step = failure === "source" ? "fetch" : "normalize";
            const port = failure === "source" ? "output" : "input";
            const field = failure === "source" ? "customer_id" : "amount_cents";
            await expect(graph.getByRole("img", { name: `Failed ${port} boundary at ${step}` })).toBeVisible();
            await expect(graph.locator("svg path.text-error")).toHaveCount(failure === "source" ? 0 : 1);
            if (failure === "transform") {
                const label = await graph.getByRole("link", { name: /Failed input boundary:/ }).boundingBox();
                const source = await graph.getByRole("link", { name: /Source step Fetch customers/ }).boundingBox();
                const target = await graph
                    .getByRole("link", { name: /Transform step Normalize customers/ })
                    .boundingBox();
                expect(label.x).toBeGreaterThanOrEqual(source.x + source.width);
                expect(label.x + label.width).toBeLessThanOrEqual(target.x);
            }
            await expect(selected).toContainText(field);
            await expect(selected.getByText(/wrapped diagnostic/)).toBeHidden();
            await selected.getByText("Technical diagnostic", { exact: true }).click();
            await expect(selected.getByText(/wrapped diagnostic/)).toBeVisible();
            await selected.getByText("Technical diagnostic", { exact: true }).click();
            await expect(page.getByRole("region", { name: "Sink delivery outcomes" })).toContainText("Not executed");
            await expect(page.locator("time").first()).toHaveText("25 Oct 2026, 02:59:59.125 CEST (UTC+02:00)");
            await expect(page.locator("time").last()).toHaveText("25 Oct 2026, 02:00:00.125 CET (UTC+01:00)");
            await expect(page.locator("time").last()).toHaveAttribute("title", "2026-10-25T01:00:00.125Z");
            await expect(page.getByText("Failure triage", { exact: true })).toHaveCount(0);
            await expect(page.getByText("Execution timeline", { exact: true })).toHaveCount(1);
            await captureE2EEvidenceScreenshot(page, testInfo, `${failure}-failure-desktop.png`, { fullPage: true });
            await selected
                .getByRole("link", { name: /Inspect executed step & contract|Compare executed output contract/ })
                .click();
            await expect(page).toHaveURL(new RegExp(`lineage_step=${step}`));
            await expect(page).toHaveURL(/section=definition/);
            await expect(page.locator("[data-definition-inspector]")).toContainText("Executed version");
            await expect(page.locator("#run-lineage")).toHaveCount(0);
            await expect(
                page
                    .getByRole("navigation", { name: "Run sections" })
                    .getByRole("link", { name: "Definition", exact: true }),
            ).toHaveAttribute("aria-current", "page");
            await page.goBack();
            await expect(selected).toContainText(field);
            await page.setViewportSize({ width: 390, height: 844 });
            await expect(page.getByRole("list", { name: "Run lineage steps", exact: true })).toBeVisible();
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `${failure}-failure-compact.png`, { fullPage: true });
        }
    });
});

test("compares Builder and lineage for the same multi-input topology", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/failed?failure=transform`)).ok()).toBe(true);
        await page.setViewportSize({ width: 1440, height: 1000 });
        await page.goto(
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        const builderNode = page.locator('.builder-node[data-node-slug="normalize"]');
        await expect(page.locator("#builder-grid .builder-node")).toHaveCount(4);
        await builderNode.focus();
        await expect(builderNode).toBeFocused();
        const builderSize = await builderNode.evaluate((node) => ({
            width: node.offsetWidth,
            height: node.offsetHeight,
        }));
        await captureE2EEvidenceScreenshot(page, testInfo, "builder-multi-input-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        await captureE2EEvidenceScreenshot(page, testInfo, "builder-multi-input-compact.png", { fullPage: true });
        await page.setViewportSize({ width: 1440, height: 1000 });
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`);
        const node = page
            .getByRole("region", { name: "Run lineage graph", exact: true })
            .getByRole("link", { name: /Transform step Normalize customers/ });
        await node.focus();
        await expect(node).toBeFocused();
        const runSize = await node.evaluate((element) => ({
            width: element.offsetWidth,
            height: element.offsetHeight,
        }));
        expect(runSize).toEqual(builderSize);
        await captureE2EEvidenceScreenshot(page, testInfo, "lineage-multi-input-focus-desktop.png", { fullPage: true });
        await page.keyboard.press("Enter");
        await expect(page.getByRole("complementary", { name: "Selected step evidence" })).toContainText("amount_cents");
    });
});

test("reviews and saves the complete S3 query from a failed sink", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/failed?failure=s3-provider`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`);
        await page.getByRole("link", { name: "Review current saved query", exact: true }).click();
        const statement = page.getByRole("textbox", { name: "Statement", exact: true });
        const original = JSON.parse(await statement.inputValue());
        expect(original).toMatchObject({
            type: "s3",
            content: "{{input}}",
            overwrite: false,
            metadata: { source: "fixture" },
        });
        await statement.fill(JSON.stringify({ ...original, key: "exports/reviewed.csv" }, null, 2));
        await Promise.all([page.waitForEvent("load"), page.getByRole("button", { name: "Save", exact: true }).click()]);
        await page.reload();
        expect(JSON.parse(await statement.inputValue())).toEqual({ ...original, key: "exports/reviewed.csv" });
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await expect(statement).toBeVisible();
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `s3-query-${width}.png`, { fullPage: true });
        }
        await page.getByRole("link", { name: "Back to list", exact: true }).click();
        await expect(page.getByRole("combobox", { name: "Data source", exact: true })).toHaveValue("warehouse");
        await expect(page.getByRole("alert")).toHaveCount(0);
    });
});

test("explains retained failure causes and offers concrete review actions", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        for (const scenario of ["source-type", "s3", "s3-provider"]) {
            expect((await page.request.post(`${fixture.url}/_test/run/failed?failure=${scenario}`)).ok()).toBe(true);
            for (const width of [1440, 390]) {
                await page.setViewportSize({ width, height: 1000 });
                await page.goto(
                    `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`,
                );
                const selected = page.getByRole("complementary", { name: "Selected step evidence" });
                const source = scenario === "source-type";
                const summary = source
                    ? "A source result value has the wrong type. The field and expected/actual types were not recorded."
                    : scenario === "s3"
                      ? "S3 write failed. No provider error code or response was recorded."
                      : "S3 AccessDenied: access to the requested operation was denied.";
                await expect(selected.getByText(summary, { exact: true })).toBeVisible();
                await expect(selected).not.toContainText("The data source returned an unexpected error");
                const action = selected.getByRole("link", {
                    name: source ? "Compare executed output contract" : "Review executed S3 write",
                    exact: true,
                });
                await expect(action).toBeVisible();
                await expect(action).toHaveAttribute("href", /section=definition/);
                if (!source) {
                    await expect(selected).toContainText(
                        "Reconcile the object at the provider before repeating the write",
                    );
                    await expect(page.getByRole("region", { name: "Sink delivery outcomes" })).toContainText(
                        "Unknown delivery",
                    );
                }
                expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
                await captureE2EEvidenceScreenshot(selected, testInfo, `${scenario}-${width}-diagnostic.png`);
                await action.click();
                await expect(page).toHaveURL(/section=definition/);
                await expect(page).toHaveURL(new RegExp(`lineage_step=${source ? "fetch" : "store"}`));
                await expect(page.locator("#run-lineage")).toHaveCount(0);
            }
        }
    });
});

test("inspects connected run outcomes with grouped retries and keyboard selection", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/retry_success`)).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=connected&lineage_step=load-orders`,
        );
        const panel = page.locator("#connected-runs");
        await expect(panel).toContainText("Closed · succeeded");
        await expect(panel).toContainText("Closed · failed");
        await expect(panel.locator("[data-lineage-canvas] > a[data-entity-focus]")).toHaveCount(2);
        const viewport = panel.getByRole("region", { name: "Connected historical runs graph" });
        await viewport.focus();
        await page.keyboard.press("+");
        await expect(panel.locator("[data-lineage-zoom]")).not.toHaveText("100%");
        await page.keyboard.press("0");
        const child = panel.locator('[data-lineage-canvas] > a[href*="connected_run=completion-child"]');
        await child.focus();
        await page.keyboard.press("Enter");
        await expect(page.locator("#connected-run-evidence")).toContainText("completion-child");
        await expect(page).toHaveURL(/lineage_step=load-orders/);
        await expect(panel).toContainText("sha256:selected-v2");
        await panel.locator("[data-lineage-canvas] > a:not([data-entity-focus])").first().click();
        await expect(page).toHaveURL(/#connected-decision-run-inspection-after-upstream-7/);
        await captureE2EEvidenceScreenshot(page, testInfo, "connected-runs-desktop.png", { fullPage: true });
        await page.locator("#connected-run-evidence").getByRole("link", { name: "Open single-run steps" }).click();
        await expect(page).toHaveURL(/runs\/completion-child\?.*section=steps/);
        await page
            .getByRole("navigation", { name: "Run sections" })
            .getByRole("link", { name: "Connected runs", exact: true })
            .click();
        await expect(page.locator("#connected-run-evidence")).toContainText("completion-child");
        await page.setViewportSize({ width: 390, height: 844 });
        await expect(page.getByRole("list", { name: "Connected logical runs and outcomes" })).toBeVisible();
        expect(await panel.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "connected-runs-compact.png", { fullPage: true });
    });
});

test("inspects current connections separately from historical runs", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/changed`)).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync?environment_slug=dev&section=connections`,
        );
        const connections = page.locator("#pipeline-connections");
        await expect(connections).toContainText("Disabled");
        await expect(connections).toContainText("Success");
        await expect(connections).toContainText("Revision 9");
        await expect(connections.locator("[data-lineage-canvas] > a[data-entity-focus]")).toHaveCount(2);
        await connections.getByRole("region", { name: "Current pipeline connections graph" }).focus();
        await page.keyboard.press("+");
        await page.keyboard.press("0");
        await captureE2EEvidenceScreenshot(page, testInfo, "pipeline-connections-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        await expect(connections.getByRole("list", { name: "Current completion subscriptions" }).first()).toBeVisible();
        expect(await connections.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "pipeline-connections-compact.png", { fullPage: true });
    });
});

test("fits and bounds a large connected graph with continuation guidance", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/large`)).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=connected`,
        );
        const panel = page.locator("#connected-runs");
        await expect(panel).toContainText("Read limit reached");
        await expect(panel.locator("[data-lineage-canvas] > a[data-entity-focus]")).toHaveCount(40);
        await expect(panel.locator("[data-lineage-zoom]")).not.toHaveText("100%");
        const viewport = panel.getByRole("region", { name: "Connected historical runs graph" });
        await viewport.focus();
        await page.keyboard.press("+");
        await page.keyboard.press("ArrowRight");
        await expect(panel.locator('[data-lineage-action="fit"]')).toHaveAttribute("aria-pressed", "false");
        await page.keyboard.press("0");
        await expect(panel.locator('[data-lineage-action="fit"]')).toHaveAttribute("aria-pressed", "true");
        await panel.scrollIntoViewIfNeeded();
        await captureE2EEvidenceScreenshot(page, testInfo, "connected-runs-large-desktop.png", { fullPage: false });
    });
});

test("refreshes connected retry closure while keeping the selected context", async ({ page }) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/waiting`)).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=connected&connected_run=run-inspection&lineage_step=load-orders`,
        );
        const panel = page.locator("#connected-runs");
        await expect(panel).toContainText("Retry waiting");
        await expect(panel).toHaveAttribute("hx-trigger", "every 5s");
        expect((await page.request.post(`${fixture.url}/_test/completion/retry_success`)).ok()).toBe(true);
        await expect(panel).toContainText("Closed · succeeded", { timeout: 10_000 });
        await expect(page.locator("#connected-run-evidence")).toContainText("daily-sync · run-inspection");
        await expect(page).toHaveURL(/connected_run=run-inspection/);
        await expect(page).toHaveURL(/lineage_step=load-orders/);
        await expect(panel).not.toHaveAttribute("hx-trigger");
    });
});

test("navigates retained connected attempts when root evidence has expired", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/retained`)).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=connected&lineage_step=load-orders`,
        );
        const panel = page.locator("#connected-runs");
        await expect(panel).toContainText("Root attempt evidence is unavailable");
        const node = panel.locator("[data-lineage-canvas] > a[data-entity-focus]");
        await expect(node).toHaveAttribute("href", /\/runs\/run-inspection\?/);
        await node.focus();
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(/connected_run=expired-root/);
        await expect(page).toHaveURL(/lineage_step=load-orders/);
        await captureE2EEvidenceScreenshot(page, testInfo, "connected-retained-attempt-desktop.png", {
            fullPage: true,
        });
        await page.locator("#connected-run-evidence").getByRole("link", { name: "Open single-run steps" }).click();
        await expect(page).toHaveURL(/runs\/run-inspection\?.*section=steps/);
        await page
            .getByRole("navigation", { name: "Run sections" })
            .getByRole("link", { name: "Connected runs", exact: true })
            .click();
        await page.setViewportSize({ width: 390, height: 844 });
        await expect(panel).toContainText("Root attempt evidence is unavailable");
        await expect(
            panel.getByRole("list", { name: "Connected logical runs and outcomes" }).getByRole("link"),
        ).toHaveAttribute("href", /\/runs\/run-inspection\?/);
        expect(await panel.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "connected-retained-attempt-compact.png", {
            fullPage: true,
        });
    });
});

test("keeps live Steps selection, graph position and focus through quiet work, outages and failure", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/running?phase=fetch`)).ok()).toBe(true);
        await page.setViewportSize({ width: 1280, height: 900 });
        await page.goto(
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps&lineage_step=normalize`,
        );
        const progress = page.getByRole("region", { name: "Live execution progress" });
        await expect(progress).toContainText("Running step fetch");
        await expect(progress).toContainText("0 completed · 0 failed · 2 pending");
        const graph = page.getByRole("region", { name: "Run lineage graph", exact: true });
        const runningSource = graph.getByRole("link", { name: /^Source step Fetch customers, Running\./ });
        const selectedTransform = graph.locator('a[aria-current="true"]');
        await expect(runningSource.getByText("Running", { exact: true })).toBeVisible();
        await expect(selectedTransform.getByText("Pending", { exact: true }).locator("circle")).toBeVisible();
        const runningAppearance = await runningSource.evaluate((element) => {
            const style = getComputedStyle(element);
            return { border: style.borderTopColor, background: style.backgroundColor };
        });
        expect(await selectedTransform.evaluate((element) => getComputedStyle(element).borderTopColor)).not.toBe(
            runningAppearance.border,
        );
        expect(await selectedTransform.evaluate((element) => getComputedStyle(element).backgroundColor)).not.toBe(
            runningAppearance.background,
        );
        const controls = page.getByRole("group", { name: "Lineage view controls" });
        await controls.getByRole("button", { name: "100%", exact: true }).click();
        for (let i = 0; i < 5; i++) await controls.getByRole("button", { name: "Zoom in", exact: true }).click();
        await graph.focus();
        await page.keyboard.press("ArrowRight");
        const position = await graph.evaluate((element) => ({ left: element.scrollLeft, top: element.scrollTop }));
        const zoom = await controls.locator("output").textContent();
        const documentScroll = await page.evaluate(() => window.scrollY);
        await page.evaluate(() => {
            window.liveRunGraph = document.querySelector("[data-lineage-view]");
        });
        expect((await page.request.post(`${fixture.url}/_test/progress/stale`)).ok()).toBe(true);
        await expect(progress).toContainText("reporting overdue; liveness unknown");
        expect(await page.evaluate(() => window.liveRunGraph === document.querySelector("[data-lineage-view]"))).toBe(
            true,
        );
        expect((await page.request.post(`${fixture.url}/_test/progress/disconnected`)).ok()).toBe(true);
        await expect(progress).toContainText("Progress disconnected");
        await expect(graph).toBeFocused();
        expect((await page.request.post(`${fixture.url}/_test/progress/recent`)).ok()).toBe(true);
        const observationStartedAt = Date.now();
        expect((await page.request.post(`${fixture.url}/_test/run/running?phase=normalize`)).ok()).toBe(true);
        await expect(progress).toContainText("Running step normalize");
        const observationDelayMs = Date.now() - observationStartedAt;
        expect(observationDelayMs).toBeLessThanOrEqual(5000);
        await testInfo.attach("live-progress-observation-timing", {
            body: JSON.stringify({
                observationDelayMs,
                pollingIntervalMs: 2000,
                maximumNormalDelayMs: 5000,
                basis: "isolated SSR fixture, Chromium, synthetic execution facts",
            }),
            contentType: "application/json",
        });
        await expect(progress).toContainText("1 completed · 0 failed · 1 pending");
        await expect(controls.locator("output")).toHaveText(zoom);
        await expect
            .poll(() => graph.evaluate((element) => ({ left: element.scrollLeft, top: element.scrollTop })))
            .toEqual(position);
        await expect(graph).toBeFocused();
        await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(documentScroll);
        await expect(page.locator('[data-lineage-view] a[aria-current="true"]')).toContainText("normalize");
        await expect(selectedTransform.getByText("Running", { exact: true })).toBeVisible();
        expect(await selectedTransform.evaluate((element) => getComputedStyle(element).borderTopColor)).toBe(
            runningAppearance.border,
        );
        expect(await selectedTransform.evaluate((element) => getComputedStyle(element).outlineWidth)).toBe("2px");
        await expect(
            graph
                .getByRole("link", { name: /^Source step Fetch customers, Succeeded\./ })
                .getByText("Succeeded", { exact: true })
                .locator("svg"),
        ).toBeVisible();
        await page.emulateMedia({ reducedMotion: "reduce" });
        expect(
            await selectedTransform.locator(".loading").evaluate((element) => getComputedStyle(element).animationName),
        ).toBe("none");
        await page.emulateMedia({ reducedMotion: "no-preference" });
        await captureE2EEvidenceScreenshot(page, testInfo, "live-progress-running-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        const compactSteps = page.getByRole("list", { name: "Run lineage steps", exact: true });
        await expect(compactSteps.getByText("Running", { exact: true })).toBeVisible();
        await expect(compactSteps.getByText("Succeeded", { exact: true }).locator("svg")).toBeVisible();
        await expect(compactSteps.getByText("Pending", { exact: true }).locator("circle")).toBeVisible();
        expect(await compactSteps.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "live-progress-running-compact.png", { fullPage: true });
        await page.setViewportSize({ width: 1280, height: 900 });
        expect((await page.request.post(`${fixture.url}/_test/run/failed?phase=normalize`)).ok()).toBe(true);
        await expect(page.locator("#run-recorded-status")).toHaveText("failed");
        await expect(progress).toContainText("Attempt finished");
        await expect(progress).not.toHaveAttribute("hx-trigger");
        await expect(progress).toContainText("1 completed · 1 failed · 1 pending");
        await expect(selectedTransform.getByText("Failed", { exact: true })).toBeVisible();
        expect(await selectedTransform.evaluate((element) => getComputedStyle(element).borderTopColor)).not.toBe(
            runningAppearance.border,
        );
        await page.setViewportSize({ width: 390, height: 844 });
        expect(await progress.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "live-progress-terminal-compact.png", { fullPage: true });
    });
});

test("keeps the selected step after navigating away from a pending progress poll", async ({ page }) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        let releasePoll;
        try {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            expect((await page.request.post(`${fixture.url}/_test/run/running?phase=fetch`)).ok()).toBe(true);
            await page.goto(
                `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps&lineage_step=normalize`,
            );
            let pollStarted;
            let pollFinished;
            const finished = new Promise((resolve) => {
                pollFinished = resolve;
            });
            const started = new Promise((resolve) => {
                pollStarted = resolve;
            });
            const released = new Promise((resolve) => {
                releasePoll = resolve;
            });
            await page.route(
                /progress_poll=1/,
                async (route) => {
                    pollStarted();
                    await released;
                    if (!route.request().failure()) await route.continue();
                    pollFinished();
                },
                { times: 1 },
            );
            await started;
            expect((await page.request.post(`${fixture.url}/_test/run/running?phase=normalize`)).ok()).toBe(true);
            await page.getByRole("link", { name: /Source step Fetch customers/ }).click();
            await expect(page.locator('[data-lineage-view] a[aria-current="true"]')).toContainText("fetch");
            releasePoll();
            await finished;
            await expect(page.locator('[data-lineage-view] a[aria-current="true"]')).toContainText("fetch");
        } finally {
            releasePoll?.();
        }
    });
});

test("reveals the admitted version after queued intake and retains its expanded identity", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/queued?phase=fetch`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`);
        const version = page.locator("#run-recorded-version");
        await expect(version).toHaveText("Not recorded");
        await captureE2EEvidenceScreenshot(page, testInfo, "live-progress-queued-desktop.png", { fullPage: true });
        expect((await page.request.post(`${fixture.url}/_test/run/running?phase=fetch`)).ok()).toBe(true);
        await expect(page.locator("#run-recorded-status")).toHaveText("running");
        await expect(version).toContainText("aaaaaaaaaaaa");
        await version.locator("summary").click();
        expect((await page.request.post(`${fixture.url}/_test/run/running?phase=normalize`)).ok()).toBe(true);
        await expect(page.getByRole("region", { name: "Live execution progress" })).toContainText(
            "Running step normalize",
        );
        await expect(version).toHaveAttribute("open", "");
        await captureE2EEvidenceScreenshot(page, testInfo, "live-progress-admitted-desktop.png", { fullPage: true });
    });
});
