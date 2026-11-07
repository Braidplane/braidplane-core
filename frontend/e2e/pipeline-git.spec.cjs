const { test, expect, captureE2EEvidenceScreenshot } = require("./support/smoke-fixtures.cjs");
const { withSSRFixture } = require("./support/ssr-fixture.cjs");

// Each SSR fixture owns its session store; application cookies belong to a different server.
test.use({ storageState: { cookies: [], origins: [] } });

test("edits Builder event and named ODCS agreements without changing pinned siblings", async ({ page }, testInfo) => {
    test.slow(testInfo.project.name === "webkit", "Full authoring workflow needs more WebKit rendering frames in CI.");
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
        const builderURL = `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`;
        await page.route("**/api/organizations/acme/projects/ops/contracts/events/revisions/rev-event-one", (route) =>
            route.fulfill({
                contentType: "application/json",
                body: '{"slug":"rev-event-one","document":{"schema":[{"id":"event","properties":[{"name":"email","logicalType":"string","required":true,"description":"Customer email"}]}],"wide":9007199254740993}}',
            }),
        );
        await page.goto(builderURL);
        await expect(page.locator("#builder-source-workspace-panel")).toBeHidden();
        await expect(page.getByRole("link", { name: "Back to project", exact: true })).toHaveAttribute(
            "href",
            "/organizations/acme/projects/ops",
        );
        await expect(page.getByRole("complementary", { name: "Workspace navigation" })).toBeHidden();
        await expect(page.locator("#builder-edges-layer path[stroke-width='2']")).toHaveCount(3);
        await expect(page.locator('[data-add-node-kind="input"]')).toBeVisible();
        await page.locator('.builder-node[data-node-slug="event"] .builder-node-content').click();
        await page.getByRole("button", { name: "Hide properties", exact: true }).click();
        await expect(page.locator("#builder-properties-body")).toBeHidden();
        await page.locator('.builder-node[data-node-slug="event"] .builder-node-content').click();
        await expect(page.locator("#builder-properties-body")).toBeVisible();
        await page.getByRole("button", { name: "Fit", exact: true }).click();
        await expect(page.locator('.builder-node[data-node-slug="store"]')).toBeInViewport();
        const stage = page.locator("#builder-canvas-stage");
        await expect
            .poll(() => stage.evaluate((element) => element.scrollWidth - element.clientWidth))
            .toBeLessThanOrEqual(1);
        const zoom = page.getByLabel("Canvas zoom", { exact: true });
        const fittedZoom = await zoom.innerText();
        await page.getByRole("button", { name: "Zoom in", exact: true }).click();
        await expect(zoom).not.toHaveText(fittedZoom);
        await page.getByRole("button", { name: "Zoom out", exact: true }).click();
        await page.getByRole("button", { name: "Fit", exact: true }).click();
        await page.setViewportSize({ width: 2048, height: 1200 });
        await expect(zoom).toHaveText("100%");
        await expect
            .poll(() => stage.evaluate((element) => element.scrollWidth - element.clientWidth))
            .toBeLessThanOrEqual(1);
        await stage.scrollIntoViewIfNeeded();
        const connection = page.getByRole("button", { name: "Inspect connection event to normalize", exact: true });
        // A horizontal SVG path has a zero-height geometry box; click its visible stroke.
        const connectionPoint = await connection.evaluate((path) => {
            const point = path.getPointAtLength(path.getTotalLength() / 2).matrixTransform(path.getScreenCTM());
            return { x: point.x, y: point.y };
        });
        await page.mouse.click(connectionPoint.x, connectionPoint.y);
        await expect(connection).toBeFocused();
        await expect(connection).toHaveCSS("outline-style", "none");
        const preview = page.getByRole("region", { name: "Connection preview", exact: true });
        await expect(preview).toBeVisible();
        await expect(preview).toContainText("event → normalize");
        await expect(preview).toContainText("Input: events");
        await expect(preview).toContainText("rev-event-one");
        await expect(preview).not.toContainText("rev-customers-two");
        const producerContract = preview.locator('[aria-label="Producer output contract preview"]');
        await expect(producerContract).toContainText("email · string · required");
        await producerContract.getByText("email · string · required", { exact: true }).click();
        await expect(producerContract).toContainText("Customer email");
        await producerContract.getByText("Full ODCS contract and revision", { exact: true }).click();
        await expect(producerContract.locator("details").last().locator("pre")).toContainText("9007199254740993");

        await expect(preview.getByRole("link", { name: "Inspect pinned ODCS revision" }).first()).toHaveAttribute(
            "href",
            /revision=rev-event-one/,
        );
        await preview.getByRole("button", { name: "Inspect source", exact: true }).click();
        await expect(preview).toBeHidden();
        await connection.focus();
        await connection.press("Enter");
        await expect(connection).toHaveCSS("outline-style", "none");
        await expect(connection).not.toHaveCSS("stroke", "rgba(0, 0, 0, 0)");
        await expect(preview).toBeVisible();
        await preview.getByRole("button", { name: "Inspect source", exact: true }).click();
        await expect(page.locator("#builder-odcs-port")).toHaveValue("input.output");
        await expect(page.locator("#builder-odcs-representation")).toHaveValue("json");
        await expect(page.locator("#builder-odcs-representation")).toBeDisabled();
        await expect(page.locator("#builder-event-input-help")).toContainText(
            "Event delivery is configured separately",
        );
        await expect(page.locator("#builder-odcs-current")).toContainText("rev-event-one");
        await page.locator('.builder-node[data-node-slug="normalize"] .builder-node-content').click();
        await page.locator("#builder-odcs-port").selectOption("transform.input.customers");
        await expect(page.locator("#builder-odcs-current")).toContainText("rev-customers-two");
        await page.locator("#builder-odcs-port").selectOption("transform.input.events");
        await expect(page.locator("#builder-odcs-current")).toContainText("rev-event-one");
        await expect(page.locator("#builder-transform-legacy-input-contract-group")).toBeHidden();
        await expect(page.locator("#builder-parameters-group")).toBeHidden();
        await page.getByRole("button", { name: "Use inline contract", exact: true }).click();
        const editor = page.getByLabel("Inline agreement (JSON)");
        await editor.fill("[]");
        await editor.blur();
        await expect(page.locator("#builder-odcs-inline-error")).toContainText("Enter a JSON object");
        const agreement = {
            name: "events",
            representation: "json",
            root_shape: "object",
            fields: [{ name: "email", type: "string", required: true, description: "Event customer email" }],
        };
        await editor.fill(JSON.stringify(agreement, null, 2));
        await editor.blur();
        await expect(page.locator("#builder-odcs-inline-error")).toBeEmpty();
        await expect(page.locator("#builder-odcs-root-shape")).toHaveValue("object");
        await page.locator("#builder-odcs-root-shape").selectOption("array");
        await expect(editor).toHaveValue(JSON.stringify({ ...agreement, root_shape: "array" }, null, 2));
        await page.locator("#builder-odcs-root-shape").selectOption("object");
        await expect(editor).toHaveValue(JSON.stringify(agreement, null, 2));
        const alias = page
            .locator("[data-binding-row]")
            .filter({ hasText: "Source event" })
            .locator("[data-binding-target-port]");
        await alias.fill("incoming_events");
        await alias.blur();
        await page.locator("#builder-odcs-port").selectOption("transform.input.incoming_events");
        await expect(editor).toHaveValue(JSON.stringify(agreement, null, 2));
        await page.locator("#builder-save-btn").click();
        await expect(page.locator("#builder-save-reason")).toContainText("Draft saved to Braidplane");
        const result = await page.request.get(
            `${fixture.url}/api/organizations/acme/projects/ops/pipelines/daily-sync`,
        );
        expect(result.ok()).toBe(true);
        const steps = (await result.json()).draft.spec.steps;
        expect(steps.find((step) => step.slug === "event").input.output_contract_ref.revision_slug).toBe(
            "rev-event-one",
        );
        const requirements = steps.find((step) => step.slug === "normalize").transform.input_contracts;
        expect(requirements.events).toBeUndefined();
        expect(requirements.incoming_events).toEqual({ contract: agreement });
        expect(steps.find((step) => step.slug === "normalize").transform.inputs.incoming_events).toBe("event");
        expect(requirements.customers.contract_ref.revision_slug).toBe("rev-customers-two");
        await page.evaluate(() => localStorage.clear());
        await page.reload();
        await page.locator('.builder-node[data-node-slug="normalize"] .builder-node-content').click();
        await page.locator("#builder-odcs-port").selectOption("transform.input.incoming_events");
        await expect(editor).toHaveValue(JSON.stringify(agreement, null, 2));
        for (const [name, viewport] of [
            ["laptop", { width: 1280, height: 900 }],
            ["desktop", { width: 1440, height: 900 }],
            ["compact", { width: 390, height: 844 }],
        ]) {
            await page.setViewportSize(viewport);
            const canvas = page.locator("#builder-main-section");
            const properties = page.locator("#builder-properties-aside");
            if (name !== "compact") {
                await expect
                    .poll(async () => {
                        const main = await canvas.boundingBox();
                        const inspector = await properties.boundingBox();
                        return inspector.x >= main.x + main.width - 1 && Math.abs(inspector.y - main.y) < 1;
                    })
                    .toBe(true);
                const body = page.locator("#builder-properties-body");
                expect((await body.boundingBox()).height).toBeLessThan(viewport.height);
                expect(
                    await page
                        .locator("#builder-property-panel")
                        .evaluate((element) => element.scrollHeight > element.clientHeight),
                ).toBe(true);
                await alias.focus();
                await expect(alias).toBeInViewport();
            } else {
                const main = await canvas.boundingBox();
                const inspector = await properties.boundingBox();
                expect(inspector.y).toBeGreaterThanOrEqual(main.y + main.height - 1);
            }
            await editor.focus();
            await editor.scrollIntoViewIfNeeded();
            await expect(editor).toBeVisible();
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `builder-named-odcs-${name}.png`, { fullPage: true });
        }
        await page.locator("#builder-mode-source-btn").click();
        await expect(page.locator("#builder-source-editor")).toBeVisible();
        await page.locator("#builder-mode-preview-btn").click();
        await expect(page.locator("#builder-source-workspace-panel")).toBeHidden();
    });
});

test("inspects Builder connection declarations and explicit historical run evidence", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-references`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/succeeded`)).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        await expect(page.locator("#builder-source-editor")).toHaveValue(/daily-sync/);
        const original = await page.locator("#builder-source-editor").inputValue();
        const selectEdge = async (from, to) => {
            const path = page.getByRole("button", { name: `Inspect connection ${from} to ${to}`, exact: true });
            await path.focus();
            await page.keyboard.press("Enter");
        };
        await selectEdge("fetch", "normalize");
        const comparison = page.getByRole("region", { name: "Draft contract comparison", exact: true });
        await expect(comparison.getByRole("columnheader", { name: "Producer", exact: true })).toBeVisible();
        await expect(comparison).toContainText("email");
        const requestsBefore = await (await page.request.get(`${fixture.url}/_test/builder-requests`)).json();
        expect(requestsBefore.some((request) => request.endsWith("/runs"))).toBe(false);
        const evidence = page.getByRole("region", { name: "Recorded run evidence", exact: true });
        await evidence.getByRole("button", { name: "Choose recorded run", exact: true }).click();
        await evidence.getByLabel("Run for this connection", { exact: true }).selectOption("run-inspection");
        await expect(evidence).toContainText("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
        await expect(evidence).toContainText("No recorded checks");
        await expect(evidence).toContainText("Raw payload values are not retained");
        await selectEdge("normalize", "store");
        await expect(evidence).toContainText("conformant");
        await expect(evidence.getByLabel("Run for this connection", { exact: true })).toHaveValue("run-inspection");
        await evidence.getByRole("button", { name: "Older runs", exact: true }).click();
        await expect(evidence.getByRole("button", { name: "Newest runs", exact: true })).toBeVisible();
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await evidence.scrollIntoViewIfNeeded();
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `connection-run-${width}.png`, { fullPage: true });
        }
        await page.setViewportSize({ width: 1440, height: 1000 });
        expect((await page.request.post(`${fixture.url}/_test/run/failed`)).ok()).toBe(true);
        await evidence.getByRole("button", { name: "Refresh evidence", exact: true }).click();
        await expect(evidence).toContainText("Failed");
        await expect(evidence).toContainText("Historical evidence from this run");
        await page.route("**/builder/run-evidence?**", (route) => route.fulfill({ status: 403 }));
        await evidence.getByRole("button", { name: "Refresh evidence", exact: true }).click();
        await expect(evidence).toContainText("Check access");
        await page.unroute("**/builder/run-evidence?**");
        await evidence.getByRole("button", { name: "Retry loading runs", exact: true }).click();
        await expect(evidence).toContainText("conformant");

        let release;
        const held = new Promise((resolve) => {
            release = resolve;
        });
        let received;
        const requested = new Promise((resolve) => {
            received = resolve;
        });
        await page.route("**/builder/run-evidence?**", async (route) => {
            if (new URL(route.request().url()).searchParams.get("from_step") !== "normalize") return route.continue();
            const response = await route.fetch();
            received();
            await held;
            await route.fulfill({ response });
        });
        await evidence.getByRole("button", { name: "Refresh evidence", exact: true }).click();
        await requested;
        await selectEdge("fetch", "normalize");
        await expect(evidence).toContainText("No recorded checks");
        release();
        await expect(page.locator("#builder-edge-preview")).toContainText("fetch → normalize");
        await expect(evidence).not.toContainText("conformant");
        expect(await page.locator("#builder-source-editor").inputValue()).toBe(original);
    });
});

test("uses cached Builder schema proposals without replacing agreements", async ({ page }, testInfo) => {
    test.slow(
        testInfo.project.name === "webkit",
        "Schema authoring and responsive captures need more WebKit rendering frames in CI.",
    );
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-references`)).ok()).toBe(true);
        let refreshes = 0;
        await page.route("**/builder/schema?**", (route) => {
            const source = new URL(route.request().url()).searchParams.get("data_source_slug");
            expect(source).toBe("warehouse");
            return route.fulfill({
                contentType: "application/json",
                json: {
                    status: "ready",
                    lines: ["table customers", "  - customer_id (integer)"],
                    datasets: [
                        {
                            name: "customers",
                            fields: [{ name: "customer_id", type: "integer", required: false, nullable: true }],
                        },
                    ],
                },
            });
        });
        await page.route("**/api/organizations/acme/data-sources/warehouse/schema", (route) => {
            expect(route.request().method()).toBe("POST");
            refreshes++;
            return route.fulfill({
                contentType: "application/json",
                json: { schema_status: "importing", enqueued: true },
            });
        });
        await page.goto(
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        await page.locator('.builder-node[data-node-slug="fetch"] .builder-node-content').click();
        const original = await page.locator("#builder-source-editor").inputValue();
        await page.locator("#builder-schema-panel summary").click();
        await expect(page.locator("#builder-source-capabilities")).toHaveCount(0);
        await expect(page.locator("#builder-schema-status")).toContainText("Cached schema");
        await expect(page.locator("#builder-schema-apply")).toBeDisabled();
        expect(await page.locator("#builder-source-editor").inputValue()).toBe(original);
        await page.getByLabel("Schema dataset", { exact: true }).selectOption("0");
        await expect(page.locator("#builder-schema-operation-preview")).toContainText("SELECT * FROM");
        await expect(page.locator("#builder-source-editor")).toHaveValue(original);
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await page.locator("#builder-schema-operation").scrollIntoViewIfNeeded();
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `schema-operation-${width}.png`, {
                fullPage: true,
            });
        }
        await page.setViewportSize({ width: 1440, height: 1000 });
        await page.getByRole("button", { name: "Replace source operation", exact: true }).click();
        await expect(page.locator("#builder-field-inline-sql")).toHaveValue('SELECT * FROM "customers"');
        await page.locator("#builder-mode-source-btn").click();
        await page.locator("#builder-source-sync-btn").click();
        await expect(page.locator("#builder-source-editor")).toHaveValue(/SELECT \* FROM/);
        await expect(page.locator("#builder-source-editor")).not.toHaveValue(/"operation_ref": "customers"/);
        await page.locator("#builder-mode-preview-btn").click();
        await expect(page.locator("#builder-field-query-output-contract")).not.toHaveValue(/customer_id/);
        await page.getByRole("button", { name: "Add proposed fields", exact: true }).click();
        await expect(page.locator("#builder-field-query-output-contract")).toHaveValue(/customer_id/);
        await expect(page.locator("#builder-field-query-output-contract")).toHaveValue(/nullable/);
        const field = page.locator("#builder-query-output-contract-editor [data-contract-field-row]").last();
        const rules = field.locator("details");
        await expect(rules).not.toHaveAttribute("open");
        await rules.getByText("Validation rules", { exact: true }).click();
        await field.locator("[data-contract-validation-min]").fill("1");
        await field.locator("[data-contract-validation-min]").press("Tab");
        await expect(page.locator("#builder-field-query-output-contract")).toHaveValue(/"min": 1/);
        await expect(rules).toHaveAttribute("open");
        await page.locator('.builder-node[data-node-slug="store"] .builder-node-content').click();
        await page.locator('.builder-node[data-node-slug="fetch"] .builder-node-content').click();
        await expect(field.locator("[data-contract-validation-min]")).toHaveValue("1");
        await expect(rules).toHaveAttribute("open");

        await page.getByRole("button", { name: "Refresh schema", exact: true }).click();
        await expect(page.locator("#builder-schema-status")).toContainText("Cached schema");
        expect(refreshes).toBe(1);
        await page.locator('.builder-node[data-node-slug="store"] .builder-node-content').click();
        await expect(page.locator("#builder-schema-panel")).toBeVisible();
        await expect(page.locator("#builder-sink-capabilities")).toHaveCount(0);
    });
});

test("validates Builder drafts on the selected server target and keeps rejected drafts editable", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-references`)).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        const preflight = page.locator("#builder-server-validation");
        await page.locator('.builder-node[data-node-slug="fetch"] .builder-node-content').click();
        await page.locator("#builder-field-step-name").fill("Draft awaiting preflight");
        await page.locator("#builder-validate-btn").click();
        await expect(preflight).toContainText("Server validation passed in dev for this draft.");
        const requests = await (await page.request.get(`${fixture.url}/_test/builder-requests`)).json();
        expect(requests).toContain("POST /api/organizations/acme/projects/ops/environments/dev/pipeline-validation");
        expect((await page.request.post(`${fixture.url}/_test/builder-validation-rejected`)).ok()).toBe(true);
        await page.locator("#builder-validate-btn").click();
        await expect(preflight).toContainText("2 disconnected data-flow groups");
        await expect(page.locator("#builder-validation-summary")).not.toContainText("Server preflight");
        await expect(preflight).toContainText("split independent flows into separate pipelines");
        await expect(page.locator("#builder-save-btn")).toBeEnabled();
        let rejectConnection = true;
        await page.route("**/builder/validate", (route) =>
            rejectConnection
                ? route.fulfill({
                      status: 400,
                      contentType: "application/json",
                      json: {
                          error: {
                              validation_errors: [
                                  {
                                      field: "spec",
                                      code: "contract_mismatch",
                                      message:
                                          'edge "fetch->normalize" incompatible contracts: field "email" expected string, actual integer',
                                  },
                              ],
                          },
                      },
                  })
                : route.fulfill({ status: 200, body: "Validated" }),
        );
        await page.locator("#builder-validate-btn").click();
        const incompatible = page.getByRole("button", {
            name: "Inspect incompatible connection fetch to normalize",
            exact: true,
        });
        await expect(incompatible).toHaveAttribute("data-validation-error", "true");
        await preflight.getByRole("button", { name: /expected string, actual integer/ }).click();
        await expect(page.locator("#builder-edge-preview")).toContainText(
            'field "email" expected string, actual integer',
        );
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await page.locator("#builder-edge-preview").scrollIntoViewIfNeeded();
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `edge-diagnostic-${width}.png`, { fullPage: true });
        }
        await page.setViewportSize({ width: 1440, height: 1000 });
        rejectConnection = false;
        await page.locator("#builder-validate-btn").click();
        await expect(preflight).toContainText("Server validation passed");
        await expect(page.locator("#builder-edge-preview")).not.toContainText("expected string, actual integer");
        rejectConnection = true;
        await page.locator("#builder-validate-btn").click();
        await expect(page.locator("#builder-edge-preview")).toContainText("expected string, actual integer");
        await page.unroute("**/builder/validate");
        await page.locator('.builder-node[data-node-slug="fetch"] .builder-node-content').click();
        await page.locator("#builder-field-step-name").fill("Edited after preflight");
        await expect(preflight).toBeHidden();
        await expect(
            page.getByRole("button", { name: "Inspect connection fetch to normalize", exact: true }),
        ).toHaveAttribute("data-validation-error", "false");
        let release;
        const held = new Promise((resolve) => {
            release = resolve;
        });
        let received;
        const requested = new Promise((resolve) => {
            received = resolve;
        });
        await page.route("**/builder/validate", async (route) => {
            const response = await route.fetch();
            received();
            await held;
            await route.fulfill({ response });
        });
        await page.locator("#builder-validate-btn").click();
        await requested;
        await page.locator("#builder-field-step-name").fill("Changed while validating");
        release();
        await expect(page.locator("#builder-save-reason")).toContainText("draft changed during validation");
        await expect(preflight).toBeHidden();
    });
});

test("imports Git definitions, binds a source, and deploys an exact version", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/pipelines`);
        await page.getByRole("link", { name: "Sync from Git", exact: true }).click();
        await page.getByRole("button", { name: "Preview Git changes" }).click();
        await expect(page.getByRole("heading", { name: "Sync preview" })).toBeVisible();
        await page.getByRole("button", { name: "Apply Git changes" }).click();
        await expect(page.getByRole("status").filter({ hasText: "Git sync completed" })).toBeVisible();
        await page.getByRole("link", { name: "Deploy version", exact: true }).click();
        const deployURL = page.url();
        const version = await page.locator('input[name="spec_hash"]').first().inputValue();
        await page.getByRole("link", { name: "Resolve selected version bindings" }).click();
        await expect(page.getByRole("heading", { name: "Resolve pipeline bindings" })).toBeVisible();
        expect(new URL(page.url()).searchParams.get("spec_hash")).toBe(version);
        await expect(page.getByText("Selected immutable version:")).toContainText(version);
        const review = page.getByRole("link", { name: "Review source", exact: true });
        await review.focus();
        await page.keyboard.press("Enter");
        await expect(page.locator("#binding-cell-daily-sync-source-dev")).toBeFocused();
        const sourceChoice = page.getByRole("combobox", { name: "Data source for source in dev" });
        const measureChoice = () =>
            sourceChoice.evaluate((select) => {
                const style = getComputedStyle(select);
                const canvas = document.createElement("canvas");
                const context = canvas.getContext("2d");
                context.font = style.font;
                return {
                    width: select.getBoundingClientRect().width,
                    content: context.measureText(select.selectedOptions[0].textContent).width,
                    padding: parseFloat(style.paddingLeft) + parseFloat(style.paddingRight),
                    parent: select.parentElement.getBoundingClientRect().width,
                };
            });
        const shortChoice = await measureChoice();
        expect(shortChoice.width).toBeLessThanOrEqual(shortChoice.content + shortChoice.padding + 24);
        await sourceChoice.evaluate((select) => {
            select.add(
                new Option(
                    "Long warehouse connection for customer enrichment and reporting ".repeat(4),
                    "sizing-probe",
                ),
            );
            select.value = "sizing-probe";
        });
        const longChoice = await measureChoice();
        expect(longChoice.width).toBeGreaterThan(shortChoice.width);
        expect(longChoice.width).toBeLessThanOrEqual(longChoice.parent + 1);
        await sourceChoice.evaluate((select) => {
            select.querySelector('option[value="sizing-probe"]').remove();
            select.selectedIndex = 0;
            select.disabled = true;
        });
        expect((await measureChoice()).width).toBeCloseTo(shortChoice.width, 0);
        await sourceChoice.evaluate((select) => {
            select.options[0].textContent = "Loading…";
        });
        const loadingChoice = await measureChoice();
        expect(loadingChoice.width).toBeLessThan(shortChoice.width);
        await sourceChoice.evaluate((select) => {
            select.options[0].textContent = "Select data source";
            select.disabled = false;
        });
        await captureE2EEvidenceScreenshot(page, testInfo, "binding-setup-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 768, height: 1024 });
        await expect(page.getByRole("link", { name: "Return to selected pipeline" })).toBeVisible();
        await sourceChoice.scrollIntoViewIfNeeded();
        await expect(sourceChoice).toBeInViewport();
        await captureE2EEvidenceScreenshot(page, testInfo, "binding-setup-compact.png", { fullPage: true });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        const binding = page.locator('form[action*="data-source-bindings/source"]');
        await binding.locator('select[name="data_source_slug"]').selectOption("warehouse");
        await page.getByRole("button", { name: "Bind", exact: true }).click();
        const dialog = page.locator("dialog[open]");
        await dialog.locator("[data-protected-confirm-input]").fill("dev");
        await dialog.getByRole("button", { name: "Save binding", exact: true }).click();
        await expect(page.getByRole("button", { name: "Update", exact: true })).toBeVisible();
        await expect(page.locator("#binding-cell-daily-sync-source-dev")).toBeFocused();
        await page.getByRole("link", { name: "Return to selected pipeline" }).click();
        await expect(page).toHaveURL(deployURL);
        await page.setViewportSize({ width: 1440, height: 1000 });
        await page.getByText("Rich projection selection", { exact: true }).click();
        await page.getByRole("textbox", { name: "Projection selection JSON", exact: true }).fill(
            JSON.stringify({
                inputs: [
                    {
                        contract_slug: "orders",
                        revision_slug: "v1",
                        schema_object_id: "row",
                        digest: `sha256:${"d".repeat(64)}`,
                        mapping: {
                            id: "fixture/json/v1",
                            representation: "json",
                            root_shape: "object",
                            types: ["object", "integer"],
                        },
                    },
                ],
            }),
        );
        await page.getByText("Resource scope selection", { exact: true }).click();
        await page.getByRole("textbox", { name: "Resource scope selection JSON", exact: true }).fill(
            JSON.stringify([
                {
                    step: "fetch",
                    role: "query",
                    scope: {
                        policy_id: "s3/relative-key/v1",
                        data_source: "source",
                        operation: "orders",
                        contract: "orders",
                        revision: "v1",
                        object_id: "row",
                        representation: "rows",
                        s3: {
                            endpoint: "https://storage.example",
                            region: "test",
                            bucket: "fixed-bucket",
                            prefix: "imports",
                            dynamic_key: true,
                            force_path_style: false,
                        },
                    },
                },
            ]),
        );
        await captureE2EEvidenceScreenshot(page, testInfo, "resource-scope-selection-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 768, height: 1024 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "resource-scope-selection-compact.png", { fullPage: true });
        await page.setViewportSize({ width: 1440, height: 1000 });
        await page.getByRole("button", { name: "Compile selected version", exact: true }).click();
        await expect(page.getByRole("region", { name: "Compiled plan" })).toContainText("Retained projection inputs");
        await expect(page.getByRole("region", { name: "Compiled plan" })).toContainText("Input 0 · orders / v1 / row");
        await expect(page.getByRole("region", { name: "Compiled plan" })).toContainText("fixture/json/v1");
        await expect(page.getByRole("region", { name: "Compiled plan" })).toContainText("available");
        await expect(page.getByRole("region", { name: "Compiled plan" })).toContainText("pipelines/daily-sync.yaml");
        await expect(page.getByRole("region", { name: "Compiled plan" })).toContainText(
            "Resource scope: s3/relative-key/v1 · source · orders / v1 / row · rows",
        );
        const compiledDigest = await page
            .getByRole("textbox", { name: "Exact compiled plan digest", exact: true })
            .inputValue();
        await page.getByRole("button", { name: "Review target", exact: true }).click();
        await expect(page.getByRole("textbox", { name: "Exact compiled plan digest", exact: true })).toHaveValue(
            compiledDigest,
        );
        await expect(page.getByRole("region", { name: "Compiled plan" })).toContainText(
            "Resource scope: s3/relative-key/v1",
        );
        await captureE2EEvidenceScreenshot(page, testInfo, "compiled-plan-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 768, height: 1024 });
        await expect(page.locator("body")).toHaveJSProperty("scrollWidth", 768);
        await captureE2EEvidenceScreenshot(page, testInfo, "compiled-plan-compact.png", { fullPage: true });

        await page.getByRole("textbox", { name: "Confirm protected environment" }).fill("dev");
        await page.getByRole("button", { name: "Deploy to dev", exact: true }).click();
        await expect(page.getByRole("status").filter({ hasText: `Deployed ${version} to dev` })).toBeVisible();
        await expect(page.getByRole("link", { name: "Open pipeline to run" })).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "alpha-deployment.png", { fullPage: true });
        await page.getByRole("link", { name: "Open pipeline to run" }).click();
        await expect(page).toHaveURL(
            `${fixture.url}/builder?org_slug=acme&pipeline_slug=daily-sync&project_slug=ops&target_environment_slug=dev`,
        );
        await expect(page.getByRole("button", { name: "Run in dev", exact: true })).toBeEnabled();
        await captureE2EEvidenceScreenshot(page, testInfo, "deployment-run-handoff.png", { fullPage: true });
    });
});

test("bootstraps initial progress and requests a bounded backfill", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/incremental`)).ok()).toBe(true);
        await page.clock.install();
        await page.goto(
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        await page.getByText("Incremental controls", { exact: true }).click();
        const controls = page.locator("#builder-backfill-form");
        await expect(controls.getByRole("button", { name: "Start backfill in dev", exact: true })).toBeEnabled();
        await controls.getByLabel("From", { exact: true }).fill("a@example.test");
        await controls.getByLabel("To", { exact: true }).fill("z@example.test");
        await captureE2EEvidenceScreenshot(page, testInfo, "incremental-backfill-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 768, height: 1024 });
        await captureE2EEvidenceScreenshot(page, testInfo, "incremental-backfill-compact.png", { fullPage: true });
        await controls.getByRole("combobox", { name: "Action", exact: true }).selectOption("bootstrap");
        await controls.getByLabel("Request slug", { exact: true }).fill("bootstrap-ui-001");
        await expect(controls.getByLabel("To", { exact: true })).toBeHidden();
        const bootstrapResponse = page.waitForResponse(
            (response) => response.url().includes("/bootstrap-checkpoints") && response.status() === 204,
        );
        page.once("dialog", (dialog) => dialog.accept("dev"));
        await controls.getByRole("button", { name: "Bootstrap checkpoints in dev", exact: true }).click();
        await bootstrapResponse;
        await expect(page.getByText("Initial checkpoints bootstrapped in dev", { exact: true })).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "incremental-bootstrap-compact.png", { fullPage: true });
        await controls.getByRole("combobox", { name: "Action", exact: true }).selectOption("backfill");
        await controls.getByLabel("From", { exact: true }).fill("9007199254740993");
        await controls.getByLabel("To", { exact: true }).fill("9007199254740995");
        await page.clock.pauseAt(new Date(Date.now() + 60_000));
        const requested = page.waitForRequest(
            (request) => request.url().includes("/execute?") && request.method() === "POST",
        );
        await controls.getByRole("button", { name: "Start backfill in dev", exact: true }).click();
        expect((await requested).postData()).toBe(
            '{"backfill":{"source_windows":{"fetch":{"lower_bound":{"value":9007199254740993},"upper_bound":{"value":9007199254740995}}}}}',
        );
        const notification = page.locator(".alert-info").filter({ hasText: "Run requested in dev" });
        await expect(notification).toBeVisible();
        await expect(notification).not.toContainText("executed");
        await expect(notification.getByRole("link")).toHaveCount(0);
        await captureE2EEvidenceScreenshot(notification, testInfo, "run-request-accepted.png");
        await page.setViewportSize({ width: 390, height: 844 });
        const notificationBox = await notification.boundingBox();
        expect(notificationBox.x).toBeGreaterThanOrEqual(0);
        expect(notificationBox.x + notificationBox.width).toBeLessThanOrEqual(390);
        await captureE2EEvidenceScreenshot(notification, testInfo, "run-request-accepted-compact.png");
        await page.clock.runFor(300);
        await expect(page).toHaveURL(`${fixture.url}/orgs/acme/projects/ops/runs/backfill-ui-001?environment_slug=dev`);
    });
});

test("keeps Builder logical refs through environment inspection, draft save, and reload", async ({
    page,
}, testInfo) => {
    test.slow(
        testInfo.project.name === "webkit",
        "Binding inspection reloads and edits the graph across multiple states.",
    );
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        const writes = [];
        let bindingState = "bound";
        let sourceStatus = 200;

        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-references`)).ok()).toBe(true);
        await page.route("**/api/organizations/acme/data-sources?*", (route) =>
            route.fulfill({
                status: sourceStatus,
                json: {
                    data_sources: ["warehouse", "warehouse-prod"].map((slug) => ({
                        slug,
                        name: slug,
                        type: "postgres",
                        read_only: false,
                    })),
                },
            }),
        );
        await page.route("**/environments/*/data-source-bindings?*", (route) => {
            if (bindingState === "failed") return route.fulfill({ status: 503, json: {} });
            if (bindingState === "missing") return route.fulfill({ json: { data_source_bindings: [] } });
            const prod = route.request().url().includes("/prod/");
            return route.fulfill({
                json: {
                    data_source_bindings: [
                        {
                            pipeline_slug: "daily-sync",
                            data_source_ref_slug: "source",
                            data_source_slug: prod ? "warehouse-prod" : "warehouse",
                            data_source_active: bindingState !== "inactive",
                        },
                    ],
                },
            });
        });
        page.on("request", (request) => {
            const path = new URL(request.url()).pathname;
            // Serialization, target validation and attachment inspection are read-only POSTs.
            const readOnlyPost =
                request.method() === "POST" &&
                (path === "/builder/export" ||
                    path === "/builder/validate" ||
                    (path === "/builder/mapping" && request.postDataJSON()?.action === "inspect"));
            if (request.method() !== "GET" && !readOnlyPost) writes.push(request);
        });
        const open = async (environment) => {
            await page.goto(
                `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=${environment}`,
            );
            await page.locator('.builder-node[data-node-slug="fetch"] .builder-node-content').click();
            await expect(page.locator("#builder-source-config-hint")).toContainText(
                `${environment}: bound to warehouse${environment === "prod" ? "-prod" : ""}`,
            );
            await expect(page.locator("#builder-field-data-source")).toHaveValue("source");
            await expect(page.locator('#builder-field-data-source option[value^="warehouse"]')).toHaveCount(0);
            await page.locator('.builder-node[data-node-slug="store"] .builder-node-content').click();
            await expect(page.locator("#builder-sink-config-hint")).toContainText(
                `${environment}: bound to warehouse${environment === "prod" ? "-prod" : ""}`,
            );
            await expect(page.locator("#builder-field-sink-backend")).toHaveValue("source");
        };
        await open("dev");
        await expect(page.locator("#builder-source-editor")).toHaveValue(/"data_source_ref": "source"/);
        await expect(page.locator("#builder-source-editor")).not.toHaveValue(/warehouse/);
        await captureE2EEvidenceScreenshot(page, testInfo, "logical-refs-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 768, height: 1024 });
        await open("prod");
        const select = page.locator("#builder-field-sink-backend");
        await select.focus();
        await select.press("End");
        await expect(select).toHaveValue("source");
        await select.press("Escape");
        await select.press("Tab");
        await expect(select).not.toBeFocused();
        await captureE2EEvidenceScreenshot(page, testInfo, "logical-refs-compact.png", { fullPage: true });
        expect(writes).toHaveLength(0);
        await page.locator("#builder-validate-btn").click();
        await expect(page.locator("#builder-server-validation")).toContainText("Server validation passed in prod");
        const reads = await (await page.request.get(`${fixture.url}/_test/builder-requests`)).json();
        expect(reads.filter((request) => !request.startsWith("GET "))).toEqual(
            expect.arrayContaining([expect.stringContaining("/pipeline-validation")]),
        );
        expect(reads.every((request) => request.startsWith("GET ") || request.endsWith("/pipeline-validation"))).toBe(
            true,
        );
        await page.locator('.builder-node[data-node-slug="fetch"] .builder-node-content').click();
        await page.locator("#builder-field-step-name").fill("Fetch customers edited");
        const saved = page.waitForResponse(
            (response) =>
                response.request().method() === "POST" && response.url().endsWith("/pipelines/save") && response.ok(),
        );
        await page.locator("#builder-save-btn").click();
        await saved;
        const submitted = writes.at(-1).postDataJSON();
        expect(submitted.pipeline_spec.steps.find((step) => step.kind === "query").query.data_source_ref).toBe(
            "source",
        );
        expect(submitted.pipeline_spec.steps.find((step) => step.kind === "sink").sink.data_source_ref).toBe("source");
        expect(submitted.context.current_version_spec_hash).toContain("aaaa");
        await page.reload();
        await expect(page).toHaveURL(/target_environment_slug=prod/);
        await page.locator('.builder-node[data-node-slug="store"] .builder-node-content').click();
        await expect(page.locator("#builder-sink-config-hint")).toContainText("prod: bound to warehouse-prod");
        await expect(page.locator("#builder-field-sink-backend")).toHaveValue("source");
        await page.locator('.builder-node[data-node-slug="fetch"] .builder-node-content').click();
        await expect(page.locator("#builder-field-data-source")).toHaveValue("source");
        await expect(page.locator("#builder-field-step-name")).toHaveValue("Fetch customers edited");
        for (const [state, status, hint] of [
            ["missing", 200, "no binding configured"],
            ["inactive", 200, "inactive target"],
            ["failed", 200, "binding metadata failed to load"],
            ["bound", 403, "source metadata is inaccessible"],
        ]) {
            bindingState = state;
            sourceStatus = status;
            await page.reload();
            await page.locator('.builder-node[data-node-slug="fetch"] .builder-node-content').click();
            await expect(page.locator("#builder-source-config-hint")).toContainText(hint);
            await expect(page.locator("#builder-field-data-source")).toHaveValue("source");
            await expect(page.locator("#builder-field-step-name")).toHaveValue("Fetch customers edited");
            await page.locator('.builder-node[data-node-slug="store"] .builder-node-content').click();
            await expect(page.locator("#builder-sink-config-hint")).toContainText(hint);
            await expect(page.locator("#builder-field-sink-backend")).toHaveValue("source");
        }
        expect(writes).toHaveLength(1);
    });
});

test("keeps repository setup choices and advanced fields clear on desktop and compact screens", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/repository-setup`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/organizations/acme/projects/ops?section=repository`);
        const choices = page.getByRole("group", { name: "Repository setup path", exact: true });
        const existing = choices.getByRole("link", { name: "Use existing repository", exact: true });
        const create = choices.getByRole("link", { name: "Create repository", exact: true });
        await expect(page.locator('form input[name="mode"]')).toHaveCount(0);
        await create.click();
        await expect(create).toHaveAttribute("aria-current", "page");
        await expect(existing).toHaveAttribute("aria-current", "false");
        await expect(page.locator('input[name="mode"]')).toHaveValue("managed_create");
        await expect(page.getByRole("button", { name: "Save backend", exact: true })).toHaveCount(0);
        await page.getByText("Advanced settings", { exact: true }).click();
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            for (const role of ["textbox", "combobox", "checkbox", "button"]) {
                await expect(page.getByRole(role, { name: "", exact: true })).toHaveCount(0);
            }
            const branch = page.getByRole("textbox", { name: "Default branch", exact: true });
            const path = page.getByRole("textbox", { name: "Pipeline path", exact: true });
            await expect(branch).toBeVisible();
            await expect(path).toBeVisible();
            const layout = await branch.evaluate((input) => {
                const form = input.closest("form");
                const advanced = input.closest("details");
                return {
                    formGap: parseFloat(getComputedStyle(form).rowGap),
                    formWidth: form.getBoundingClientRect().width,
                    advancedWidth: advanced.getBoundingClientRect().width,
                    overflow: document.documentElement.scrollWidth > window.innerWidth,
                };
            });
            expect(layout.formGap).toBeGreaterThanOrEqual(20);
            expect(layout.advancedWidth).toBeCloseTo(layout.formWidth, 0);
            expect(layout.overflow).toBe(false);
            const status = page.locator(".badge").filter({ hasText: "no Git backend" });
            expect(await status.evaluate((badge) => badge.scrollHeight <= badge.clientHeight)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `repository-setup-${width}.png`, { fullPage: true });
        }
        await page.getByRole("link", { name: "Cancel", exact: true }).click();
        await expect(page.locator('form input[name="mode"]')).toHaveCount(0);
        await existing.focus();
        await page.keyboard.press("Enter");
        await expect(existing).toHaveAttribute("aria-current", "page");
        await expect(page.locator('input[name="mode"]')).toHaveValue("adopt_existing");
        await expect(page.getByRole("button", { name: "Create and connect", exact: true })).toHaveCount(0);
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            for (const role of ["textbox", "combobox", "checkbox", "button"]) {
                await expect(page.getByRole(role, { name: "", exact: true })).toHaveCount(0);
            }
        }
    });
});

test("keeps repository selection parameters safe and clears stale fields after saving", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/repository-setup`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/organizations/acme/projects/ops?section=repository&repository_path=existing`);
        const form = page.locator('form[data-dirty-form="project-pipeline-backend"]');
        await expect(form.locator('input[name="_csrf"]')).toHaveCount(1);
        const selectionRequest = page.waitForRequest(
            (request) =>
                request.method() === "GET" &&
                new URL(request.url()).searchParams.get("data_source_slug") === "git-source",
        );
        let releaseBranches;
        const branchesReady = new Promise((resolve) => {
            releaseBranches = resolve;
        });
        await page.route("**/settings/pipeline-backend/branches*", async (route) => {
            await branchesReady;
            await route.continue();
        });
        await page.getByRole("combobox", { name: /Backend data source/ }).selectOption("git-source");
        const selectedURL = new URL((await selectionRequest).url());
        expect(selectedURL.searchParams.has("_csrf")).toBe(false);
        expect(selectedURL.searchParams.has("mode")).toBe(false);
        await expect(page.getByRole("status").filter({ hasText: "Loading branches" })).toBeVisible();
        await expect(page.getByRole("combobox", { name: /Tracked branch/ })).toHaveValue("");
        await expect(page.getByText("Setting up repository…", { exact: true })).toBeHidden();
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await captureE2EEvidenceScreenshot(page, testInfo, `branches-loading-${width}.png`, { fullPage: true });
        }
        releaseBranches();
        await expect(page.getByRole("combobox", { name: /Tracked branch/ })).toBeEnabled();
        await form.locator("summary").filter({ hasText: "Pipeline path" }).click();
        await page.getByRole("textbox", { name: /Pipeline path/ }).fill("new-pipelines");
        await page.getByRole("button", { name: "Save backend", exact: true }).click();
        await expect(page).toHaveURL(
            `${fixture.url}/organizations/acme/projects/ops?repository_path=existing&section=repository`,
        );
        await page.reload();
        await expect(page.getByRole("textbox", { name: /Pipeline path/ })).toHaveValue("new-pipelines");
    });
});

test("resumes managed GitLab creation after partial failure without reentering the target", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/repository-setup`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/organizations/acme/projects/ops?section=repository&repository_path=create`);
        await page.getByRole("combobox", { name: "Provider", exact: true }).selectOption("gitlab");
        await page.getByRole("textbox", { name: "Owner, organization or group", exact: true }).fill("customer-team");
        await page.getByRole("textbox", { name: "Repository name", exact: true }).fill("data-pipelines");
        await page.getByRole("button", { name: "Create and connect", exact: true }).click();
        await expect(page.locator('input[name="create_private_repository"]')).not.toBeChecked();
        await page
            .getByRole("checkbox", {
                name: "Create this private repository and add the Braidplane template.",
                exact: true,
            })
            .check();
        await page.getByRole("button", { name: "Create and connect", exact: true }).click();
        await expect(page.getByRole("status").filter({ hasText: "Resume repository setup" })).toBeVisible();
        await page.reload();
        await expect(page.getByRole("textbox", { name: "Repository name", exact: true })).toHaveValue("data-pipelines");
        await expect(page.getByRole("textbox", { name: "Repository name", exact: true })).toHaveAttribute(
            "readonly",
            "",
        );
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await expect(page.getByRole("button", { name: "Resume setup", exact: true })).toBeVisible();
            expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false);
            await captureE2EEvidenceScreenshot(page, testInfo, `managed-repository-retry-${width}.png`, {
                fullPage: true,
            });
        }
        await page
            .getByRole("checkbox", {
                name: "Create this private repository and add the Braidplane template.",
                exact: true,
            })
            .check();
        await page.getByRole("button", { name: "Resume setup", exact: true }).click();
        await expect(
            page.getByText("Private repository created and connected. You can now create a pipeline.", { exact: true }),
        ).toBeVisible();
        await expect(page.getByText("Repository already connected", { exact: true })).toBeVisible();
    });
});

test("changes a repository target while preserving the previous attempt", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/repository-setup`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/organizations/acme/projects/ops?section=repository&repository_path=create`);
        await page.getByRole("combobox", { name: "Provider", exact: true }).selectOption("gitlab");
        await page.getByRole("textbox", { name: "Owner, organization or group", exact: true }).fill("customer-team");
        await page.getByRole("textbox", { name: "Repository name", exact: true }).fill("data-pipelines");
        await page
            .getByRole("checkbox", {
                name: "Create this private repository and add the Braidplane template.",
                exact: true,
            })
            .check();
        await page.getByRole("button", { name: "Create and connect", exact: true }).click();
        await expect(page.getByRole("button", { name: "Resume setup", exact: true })).toBeVisible();
        await expect(page.locator("[data-error-popup]")).toHaveCount(0);
        await page.getByText("Choose a different target", { exact: true }).click();
        await page.getByRole("button", { name: "Change target", exact: true }).click();
        await expect(page).not.toHaveURL(/previous_setup=/);
        await page
            .getByRole("checkbox", {
                name: "Keep any existing repository and connection from this attempt.",
                exact: true,
            })
            .check();
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false);
            await captureE2EEvidenceScreenshot(page, testInfo, `repository-change-target-${width}.png`, {
                fullPage: true,
            });
        }
        await page.getByRole("button", { name: "Change target", exact: true }).click();
        await expect(page).toHaveURL(/previous_setup=repo-fixture/);
        await expect(page.getByText("Previous target kept", { exact: true })).toBeVisible();
        await expect(page.getByRole("combobox", { name: "Provider", exact: true })).toHaveValue("gitlab");
        await expect(page.getByRole("textbox", { name: "Owner, organization or group", exact: true })).toHaveValue(
            "customer-team",
        );
        await expect(page.getByRole("link", { name: "Open previous repository", exact: true })).toHaveAttribute(
            "href",
            "https://gitlab.com/customer-team/data-pipelines.git",
        );
        const name = page.getByRole("textbox", { name: "Repository name", exact: true });
        await expect(name).toHaveValue("");
        await expect(name).toBeEditable();
        await name.fill("another-target");
        await page.reload();
        await expect(page.getByText("Previous target kept", { exact: true })).toBeVisible();
        await expect(name).toHaveValue("");
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false);
            await captureE2EEvidenceScreenshot(page, testInfo, `repository-previous-target-${width}.png`, {
                fullPage: true,
            });
        }
    });
});

test("resumes declined GitLab consent and returns approval to the originating project", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/repository-setup?gitlab_consent=required`)).ok()).toBe(
            true,
        );
        await page.route("https://gitlab.com/oauth/authorize?*", (route) =>
            route.fulfill({
                contentType: "text/html",
                body: `<h1>Isolated GitLab consent fixture</h1><a href="${fixture.url}/_test/gitlab-consent/callback?result=denied">Decline consent</a><a href="${fixture.url}/_test/gitlab-consent/callback?result=success">Approve consent</a>`,
            }),
        );
        await page.goto(`${fixture.url}/organizations/acme/projects/ops?section=repository&repository_path=create`);
        await page.getByRole("combobox", { name: "Provider", exact: true }).selectOption("gitlab");
        await page.getByRole("textbox", { name: "Owner, organization or group", exact: true }).fill("customer-team");
        await page.getByRole("textbox", { name: "Repository name", exact: true }).fill("data-pipelines");
        await page
            .getByRole("checkbox", {
                name: "Create this private repository and add the Braidplane template.",
                exact: true,
            })
            .check();
        await page.getByRole("button", { name: "Create and connect", exact: true }).click();
        await expect(page.getByRole("heading", { name: "Isolated GitLab consent fixture", exact: true })).toBeVisible();
        await page.getByRole("link", { name: "Decline consent", exact: true }).click();
        await expect(page.getByRole("alert")).toContainText("GitLab authorization was declined.");
        await expect(
            page.getByText(
                "Authorization was declined. Resume this saved setup to open a fresh authorization request.",
                { exact: true },
            ),
        ).toBeVisible();
        await expect(page.getByRole("textbox", { name: "Repository name", exact: true })).toHaveValue("data-pipelines");
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await expect(page.getByRole("button", { name: "Resume setup", exact: true })).toBeVisible();
            expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false);
            await captureE2EEvidenceScreenshot(page, testInfo, `gitlab-consent-declined-${width}.png`, {
                fullPage: true,
            });
        }
        await page
            .getByRole("checkbox", {
                name: "Create this private repository and add the Braidplane template.",
                exact: true,
            })
            .check();
        await page.getByRole("button", { name: "Resume setup", exact: true }).click();
        await page.getByRole("link", { name: "Approve consent", exact: true }).click();
        await expect(page).toHaveURL(/gitlab_setup=success/);
        await expect(page.getByText("Git backend attached; validation unavailable", { exact: true })).toBeVisible();
    });
});

test("explains a saved repository connection that needs retesting", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/repository-setup?source_test=required`)).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/organizations/acme/projects/ops?section=repository&repository_path=create&environment_slug=dev`,
        );
        await expect(
            page.getByText("Test the saved repository connection successfully, then resume setup.", { exact: true }),
        ).toBeVisible();
        await expect(page.getByRole("link", { name: "Review connection", exact: true })).toHaveAttribute(
            "href",
            "/orgs/acme/data-sources/repo-fixture?environment_slug=dev",
        );
        await expect(
            page.getByText("Ask an organization admin to test this connection.", { exact: true }),
        ).toBeVisible();
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false);
            await captureE2EEvidenceScreenshot(page, testInfo, `repository-connection-retest-${width}.png`, {
                fullPage: true,
            });
        }
    });
});

test("returns declined and approved GitHub consent to the originating project", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/repository-setup`)).ok()).toBe(true);
        await page.route("https://github.com/apps/braidplane-fixture/installations/new?*", (route) =>
            route.fulfill({
                contentType: "text/html",
                body: `<h1>Isolated provider consent fixture</h1><a href="${fixture.url}/github/callback?state=${"f".repeat(32)}&error=access_denied">Decline consent</a><a href="${fixture.url}/github/callback?installation_id=9&state=${"f".repeat(32)}&code=isolated-code">Confirm provider consent</a>`,
            }),
        );
        await page.goto(`${fixture.url}/organizations/acme/projects/ops?section=repository&repository_path=create`);
        await page.getByRole("textbox", { name: "Owner, organization or group", exact: true }).fill("customer-team");
        await page.getByRole("textbox", { name: "Repository name", exact: true }).fill("data-pipelines");
        await page
            .getByRole("checkbox", {
                name: "Create this private repository and add the Braidplane template.",
                exact: true,
            })
            .check();
        await page.getByRole("button", { name: "Create and connect", exact: true }).click();
        await expect(
            page.getByRole("heading", { name: "Isolated provider consent fixture", exact: true }),
        ).toBeVisible();
        await page.getByRole("link", { name: "Decline consent", exact: true }).click();
        await expect(
            page.getByText(
                "Authorization was declined. Resume this saved setup to open a fresh authorization request.",
                { exact: true },
            ),
        ).toBeVisible();
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await expect(page.getByRole("button", { name: "Resume setup", exact: true })).toBeVisible();
            expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false);
            await captureE2EEvidenceScreenshot(page, testInfo, `github-consent-declined-${width}.png`, {
                fullPage: true,
            });
        }
        await page
            .getByRole("checkbox", {
                name: "Create this private repository and add the Braidplane template.",
                exact: true,
            })
            .check();
        await page.getByRole("button", { name: "Resume setup", exact: true }).click();
        await page.getByRole("link", { name: "Confirm provider consent", exact: true }).click();
        await expect(page).toHaveURL(
            `${fixture.url}/organizations/acme/projects/ops?section=repository&repository_setup=complete`,
        );
        await expect(page.getByText("Git backend attached; validation unavailable", { exact: true })).toBeVisible();
    });
});

test("proposes a first pipeline from a saved query and resumes its immutable definition", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/first-pipeline/start`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/data-sources/warehouse/queries/orders`);
        await page.getByRole("link", { name: "Build pipeline", exact: true }).click();
        await page.getByLabel("Project", { exact: true }).selectOption("ops");
        await page.getByRole("button", { name: "Check readiness" }).click();
        await expect(page.getByLabel("Pipeline name", { exact: true })).toHaveValue("Recent orders pipeline");
        await page.getByLabel("count (integer)", { exact: true }).fill("42");
        for (const [name, viewport] of [
            ["desktop", { width: 1440, height: 900 }],
            ["compact", { width: 390, height: 844 }],
        ]) {
            await page.setViewportSize(viewport);
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `first-pipeline-setup-${name}.png`, { fullPage: true });
        }
        await page.getByRole("button", { name: "Propose pipeline", exact: true }).click();
        await expect(page).toHaveURL(/\/pipeline-creations\/creation-one$/);
        await expect(page.getByText("Preparing the proposal.", { exact: false })).toBeVisible();
        await expect(page.getByRole("link", { name: "Deploy this version" })).toHaveCount(0);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/pipelines`);
        await page.getByRole("link", { name: "Creation proposals", exact: true }).click();
        for (const [name, viewport] of [
            ["desktop", { width: 1440, height: 900 }],
            ["compact", { width: 390, height: 844 }],
        ]) {
            await page.setViewportSize(viewport);
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `first-pipeline-list-${name}.png`, { fullPage: true });
        }
        await page.getByRole("link", { name: "Resume proposal for recent-orders-pipeline", exact: true }).click();
        await expect(page).toHaveURL(/\/pipeline-creations\/creation-one/);
        expect((await page.request.post(`${fixture.url}/_test/first-pipeline/failed`)).ok()).toBe(true);
        await page.reload();
        const before = await (await page.request.get(`${fixture.url}/_test/first-pipeline-requests`)).json();
        await page.getByRole("button", { name: "Retry this proposal" }).click();
        await expect(page).toHaveURL(/\/pipeline-creations\/creation-one/);
        await expect(
            page.getByText("Preparing the proposal. Refresh progress to check the recorded outcome.", { exact: true }),
        ).toBeVisible();
        await expect(page.getByRole("button", { name: "Retry this proposal" })).toHaveCount(0);
        const after = await (await page.request.get(`${fixture.url}/_test/first-pipeline-requests`)).json();
        expect(after.filter((request) => request.includes("/queries/"))).toEqual(
            before.filter((request) => request.includes("/queries/")),
        );
        expect((await page.request.post(`${fixture.url}/_test/first-pipeline/open`)).ok()).toBe(true);
        await page.reload();
        await expect(page.getByRole("link", { name: "Review Git proposal" })).toBeVisible();
        await expect(page.getByRole("link", { name: "Sync merged pipeline" })).toBeVisible();
        expect((await page.request.post(`${fixture.url}/_test/first-pipeline/imported`)).ok()).toBe(true);
        await page.reload();
        await expect(page.getByRole("link", { name: "Configure source bindings" })).toHaveAttribute(
            "href",
            /spec_hash=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/,
        );
        await page.getByText("Inspect proposed definition", { exact: true }).click();
        await expect(page.locator("#definition-authority")).toContainText("Recorded creation proposal");
        await page.getByRole("link", { name: "Serialized", exact: true }).click();
        await expect(page.getByLabel("Serialized definition", { exact: true })).toContainText('"count": 42');
        for (const [name, viewport] of [
            ["desktop", { width: 1440, height: 900 }],
            ["compact", { width: 390, height: 844 }],
        ]) {
            await page.setViewportSize(viewport);
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `first-pipeline-progress-${name}.png`, {
                fullPage: true,
            });
        }
    });
});

test("creates a new pipeline from the Builder palette without an existing pipeline", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/first-pipeline/start`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/pipelines?environment_slug=dev`);
        await page.getByRole("link", { name: "Create pipeline", exact: true }).click();
        const form = page.locator("#builder-creation-form");
        await expect(form).toBeVisible();
        await expect(page.getByRole("heading", { name: "Add your first step" })).toBeVisible();
        await expect(page.locator("#builder-canvas-surface")).not.toBeVisible();
        await expect(page.getByRole("button", { name: "Zoom in", exact: true })).toBeDisabled();
        await expect(page.locator("#builder-validation-summary")).not.toHaveClass(/bg-success/);
        await expect(page.locator("#builder-validation-list")).toContainText("Add a source step");
        await form.getByLabel("Pipeline name", { exact: true }).fill("Order events");
        await form.getByLabel("Pipeline slug", { exact: true }).fill("order-events");
        await form.getByRole("button", { name: "Propose new pipeline", exact: true }).click();
        await expect(page.locator("#builder-creation-status")).toContainText("validation");
        await expect(form.getByLabel("Pipeline name", { exact: true })).toHaveValue("Order events");
        await page.locator('[data-add-node-kind="input"]').click();
        await expect(page.locator("#builder-empty-canvas-hint")).not.toBeVisible();
        await expect(page.locator("#builder-canvas-surface")).toBeVisible();
        await expect(page.getByRole("button", { name: "Zoom in", exact: true })).toBeEnabled();
        await page.locator(".builder-node .builder-node-content").click();
        const editor = page.getByLabel("Inline agreement (JSON)");
        await editor.fill(
            JSON.stringify({
                name: "events",
                representation: "json",
                root_shape: "object",
                fields: [{ name: "order_id", type: "string", required: true }],
            }),
        );
        await editor.blur();
        for (const [name, viewport] of [
            ["desktop", { width: 1440, height: 900 }],
            ["compact", { width: 390, height: 844 }],
        ]) {
            await page.setViewportSize(viewport);
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `builder-creation-${name}.png`, { fullPage: true });
        }
        await expect(page.locator("#builder-save-btn")).toBeDisabled();
        await form.getByRole("button", { name: "Propose new pipeline", exact: true }).click();
        await expect(page).toHaveURL(/\/pipeline-creations\/creation-one\?environment_slug=dev$/);
        await expect(page.getByText("Preparing the proposal.", { exact: false })).toBeVisible();
        await page.getByText("Inspect proposed definition", { exact: true }).click();
        await page.getByRole("link", { name: "Serialized", exact: true }).click();
        await expect(page.getByLabel("Serialized definition", { exact: true })).toContainText("order-events");
    });
});

test("builds a new SQL pipeline using an authoring connection before runtime bindings exist", async ({ page }) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/first-pipeline/start`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/builder?org_slug=acme&project_slug=ops&target_environment_slug=dev`);
        const form = page.locator("#builder-creation-form");
        await form.getByLabel("Pipeline name", { exact: true }).fill("New SQL orders");
        await form.getByLabel("Pipeline slug", { exact: true }).fill("new-sql-orders");
        await page.locator('[data-add-node-kind="query"]').first().click();
        await page.locator(".builder-node .builder-node-content").click();
        await page.locator("#builder-field-data-source").selectOption("warehouse");
        await expect(page.locator("#builder-source-config-hint")).toContainText(
            "configure runtime bindings after merge and sync",
        );
        await page.locator("#builder-field-query").selectOption("orders");
        await expect
            .poll(() =>
                page.evaluate(() =>
                    localStorage.getItem("braidplane.builder.canvas.state.v6/test-user/acme/ops/draft-pipeline"),
                ),
            )
            .not.toBeNull();
        await page.locator("#builder-field-inline-sql-toggle").check();
        await page.locator("#builder-field-inline-sql").fill("SELECT 42 AS order_id");
        await page.locator("#builder-field-inline-sql").blur();
        await form.getByRole("button", { name: "Propose new pipeline", exact: true }).click();
        await expect(page).toHaveURL(/\/pipeline-creations\/creation-one/);
        expect((await page.request.post(`${fixture.url}/_test/first-pipeline/branch`)).ok()).toBe(true);
        await page.reload();
        await expect(page.getByText("Branch pushed", { exact: true })).toBeVisible();
        await expect(page.getByText("braidplane/create/creation-one", { exact: true })).toBeVisible();
        await expect(page.getByRole("link", { name: "Review Git proposal", exact: true })).toHaveCount(0);
        await expect(page.getByRole("link", { name: "Sync merged pipeline", exact: true })).toBeVisible();
        await expect(page.getByRole("link", { name: "Deploy this version", exact: true })).toHaveCount(0);
        await page.getByText("Inspect proposed definition", { exact: true }).click();
        await page.getByRole("link", { name: "Serialized", exact: true }).click();
        const definition = page.getByLabel("Serialized definition", { exact: true });
        await expect(definition).toContainText('"data_source_ref": "warehouse"');
        await expect(definition).toContainText("SELECT 42 AS order_id");
    });
});

test("configures successful-run recency and inspects historical evidence", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        await page.request.get(`${fixture.url}/_test/session`);
        await page.request.post(`${fixture.url}/_test/recency/new`);
        const href = `${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync/environments/dev/recency`;
        await page.goto(`${fixture.url}/orgs/acme/projects/ops/pipelines/daily-sync?environment_slug=dev`);
        await page.getByRole("link", { name: "Successful-run recency", exact: true }).click();
        await expect(page).toHaveURL(href);
        await expect(page.getByRole("heading", { name: "Current state", exact: true })).toBeVisible();
        const form = page.locator('form[action$="/recency"]');
        await form.locator('[name="maximum_age_seconds"]').fill("3600");
        await form.getByRole("button", { name: "Save expectation" }).click();
        await expect(form.locator('[name="expected_revision"]')).toHaveValue("1");
        await expect(page.locator("[data-recency-outcome]")).toHaveText("grace");
        await page.reload();
        await expect(form.locator('[name="maximum_age_seconds"]')).toHaveValue("3600");
        await form.locator('[name="maximum_age_seconds"]').fill("7200");
        await form.getByRole("button", { name: "Save expectation" }).click();
        await expect(form.locator('[name="expected_revision"]')).toHaveValue("2");
        await form.locator('[name="enabled"]').selectOption("false");
        await form.getByRole("button", { name: "Save expectation" }).click();
        await expect(page.getByText("This expectation is disabled.", { exact: true })).toBeVisible();
        await page.request.post(`${fixture.url}/_test/recency-evidence`);
        await page.reload();
        await page.getByText(/recorded-recency-episode · rule/).click();
        await expect(page.getByText("Recovered after a qualifying success", { exact: true })).toBeVisible();
        await expect(page.getByText("b".repeat(64), { exact: true })).toBeVisible();
        await expect(page.getByRole("link", { name: "recency-recovery", exact: true })).toHaveCount(1);
        await expect(page.getByRole("link", { name: "current-success", exact: true })).toHaveCount(1);
        await expect(page.getByText("This expectation is disabled.", { exact: true })).toHaveCount(0);
        const desktopViewport = page.viewportSize();
        await page.goto(
            `${fixture.url}/orgs/acme/notifications?state=resolved&notification_type=pipeline.freshness_sla_breached`,
        );
        await expect(page.locator('select[name="notification_type"]')).toHaveValue("pipeline.freshness_sla_breached");
        await expect(
            page.getByText("This historical condition is not currently monitored by notifications."),
        ).toHaveCount(0);
        await expect(page.getByRole("link", { name: "Configure alerts", exact: true })).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "local-recency-notifications-desktop.png", {
            fullPage: true,
        });
        await page.setViewportSize({ width: 390, height: 844 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "local-recency-notifications-compact.png", {
            fullPage: true,
        });
        await page.getByRole("link", { name: "Recency", exact: true }).click();
        await expect(page).toHaveURL(new RegExp(`${href}\\?notification_return=`));
        await expect(page.getByText(/recorded-recency-episode · rule/)).toBeVisible();
        await page.setViewportSize(desktopViewport);
        await captureE2EEvidenceScreenshot(page, testInfo, "local-recency-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "local-recency-compact.png", { fullPage: true });
        await page.getByRole("link", { name: "Configure recency alerts", exact: true }).click();
        const policy = page.locator('form[action="/orgs/acme/alerts/policies"]');
        await expect(policy.locator('[name="notification_type"]')).toHaveValue("pipeline.freshness_sla_breached");
        await expect(policy.locator('[name="scope"]')).toHaveValue("project_environment");
        await expect(policy.locator('[name="project_slug"]')).toHaveValue("ops");
        await expect(policy.locator('[name="environment_slug"]')).toHaveValue("dev");
        await expect(policy.locator('[name="source_ref"]')).toHaveValue("daily-sync");
        await page.goto(href);
        await page.request.post(`${fixture.url}/_test/recency/unavailable`);
        await page.reload();
        await expect(page.getByRole("button", { name: "Save expectation" })).toHaveCount(0);
        await expect(page.getByRole("link", { name: "Configure recency alerts" })).toHaveCount(0);
        await expect(page.getByText(/recorded-recency-episode · rule/)).toBeVisible();
        await page.request.post(`${fixture.url}/_test/recency/restricted`);
        await page.reload();
        await expect(page.getByRole("button", { name: "Save expectation" })).toHaveCount(0);
        await expect(page.getByRole("heading", { name: "Current state", exact: true })).toBeVisible();
    });
});

test("authors supplier field mapping and catches a fixture regression before saving", async ({ page }, testInfo) => {
    test.slow(testInfo.project.name === "webkit", "Mapping authoring checks fixtures, viewports and draft reload.");
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const reference = {
            contract_slug: "products",
            revision_slug: "v1",
            schema_object_id: "product",
            representation: "rows",
        };
        const steps = [
            {
                kind: "query",
                slug: "fetch",
                query: {
                    data_source_ref: "source",
                    operation_type: "sql",
                    operation_inline: { sql: "SELECT item_code, description, price FROM supplier_products" },
                    output_contract: {
                        name: "supplier",
                        representation: "rows",
                        fields: ["item_code", "description", "price"].map((name) => ({
                            name,
                            type: "string",
                            required: true,
                        })),
                    },
                },
            },
            {
                kind: "transform",
                slug: "normalize",
                transform: {
                    inputs: { input: "fetch" },
                    sql: "SELECT * FROM input",
                    output_kind: "rows",
                    output_contract_ref: reference,
                },
            },
            {
                kind: "sink",
                slug: "store",
                sink: {
                    input: "normalize",
                    data_source_ref: "source",
                    operation_type: "sql",
                    operation_inline: {
                        sql: "INSERT INTO products (sku, name, unit_price, currency) SELECT item->>'sku', item->>'name', (item->>'unit_price')::numeric(18,2), item->>'currency' FROM jsonb_array_elements({{input}}::jsonb) AS item",
                    },
                    idempotency_class: "unsafe",
                    input_contract_ref: reference,
                },
            },
        ];
        expect((await page.request.post(`${fixture.url}/_test/builder-composition`, { data: steps })).ok()).toBe(true);
        const url = `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`;
        await page.goto(url);
        await expect(page.locator('.builder-node[data-node-slug="normalize"]')).toBeVisible();
        await page.locator('.builder-node[data-node-slug="normalize"] .builder-node-content').click();
        await page.getByRole("button", { name: "Open field mapping for normalize", exact: true }).click();
        await page.locator("#builder-edit-mapping-btn").click();
        const editor = page.getByRole("region", { name: "Field mapping", exact: true });
        await expect(editor).not.toHaveAttribute("aria-busy", "true");
        await editor.getByRole("button", { name: "New mapping", exact: true }).click();
        await expect(editor).toContainText("sha256:products-v1");
        await expect(editor).toContainText("Unmapped: sku, name, unit_price, currency");
        await expect(page.locator("#builder-field-transform-sql")).toHaveValue("SELECT * FROM input");
        for (const [name, source] of [
            ["sku", "item_code"],
            ["name", "description"],
            ["unit_price", "price"],
            ["currency", ""],
        ]) {
            await editor.getByRole("button", { name: "Add field", exact: true }).click();
            const row = editor.locator("fieldset").last();
            await row.getByLabel("Output name", { exact: true }).fill(name);
            if (source)
                await row.getByRole("combobox", { name: "Source field", exact: true }).selectOption(`field:${source}`);
            if (name === "unit_price")
                await row.getByRole("combobox", { name: "Conversion", exact: true }).selectOption("DECIMAL(18,2)");
            if (name === "currency") {
                await row.getByRole("combobox", { name: "Source field", exact: true }).selectOption("__constant__");
                await row.getByLabel("Constant text", { exact: true }).fill("PLN");
                await row.getByRole("button", { name: "Use constant", exact: true }).click();
            }
        }
        await editor.getByRole("button", { name: "Check SQL", exact: true }).click();
        await expect(editor).toContainText("Static shape checks passed");
        await page.locator("#builder-field-step-name").fill("Map supplier products");
        await expect(editor.locator("fieldset")).toHaveCount(4);
        await expect(editor.locator("fieldset").first().getByLabel("Output name", { exact: true })).toHaveValue("sku");
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await editor.locator("fieldset").first().scrollIntoViewIfNeeded();
            await captureE2EEvidenceScreenshot(page, testInfo, `field-mapping-editor-${width}.png`, { fullPage: true });
        }
        const sample = {
            inputs: { input: [{ item_code: "CHAIR-42", description: "Oak chair", price: "249.90" }] },
            expected: [{ sku: "CHAIR-42", name: "Oak chair", unit_price: 249.9, currency: "PLN" }],
        };
        const sampleEditor = editor.getByLabel("Fixture JSON (inputs and optional expected)", { exact: true });
        await sampleEditor.fill(JSON.stringify(sample));
        await editor.getByRole("button", { name: "Test fixture", exact: true }).click();
        await expect(editor).toContainText("Fixture contracts and expected output passed");
        await expect(editor).toContainText('"unit_price":249.90');
        sample.expected[0].currency = "EUR";
        await sampleEditor.fill(JSON.stringify(sample));
        await editor.getByRole("button", { name: "Test fixture", exact: true }).click();
        await expect(editor).toContainText("TRANSFORM_FIXTURE_ASSERTION_FAILED");
        await expect(editor).not.toContainText('"unit_price":249.90');
        for (const width of [1440, 390]) {
            await page.setViewportSize({ width, height: 1000 });
            await sampleEditor.scrollIntoViewIfNeeded();
            await sampleEditor.focus();
            await expect(sampleEditor).toBeFocused();
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
            await captureE2EEvidenceScreenshot(page, testInfo, `field-mapping-${width}.png`, { fullPage: true });
        }
        await editor.getByRole("button", { name: "Replace SQL with mapping", exact: true }).focus();
        await editor.getByRole("button", { name: "Replace SQL with mapping", exact: true }).press("Enter");
        await expect(page.locator("#builder-field-transform-sql")).toHaveValue(/CAST/);
        await page.locator("#builder-save-btn").click();
        await expect(page.locator("#builder-save-reason")).toContainText("Draft saved to Braidplane");
        const draft = await page.request.get(`${fixture.url}/api/organizations/acme/projects/ops/pipelines/daily-sync`);
        const saved = (await draft.json()).draft.spec.steps.find((step) => step.slug === "normalize").transform;
        expect(saved.output_contract_ref).toEqual(reference);
        expect(saved).not.toHaveProperty("output_contract");
        expect(saved.sql).toMatch(/DECIMAL\(18,\s*2\)/);
        await page.evaluate(() => localStorage.clear());
        await page.goto(url);
        await page.locator('.builder-node[data-node-slug="normalize"] .builder-node-content').click();
        await expect(page.locator("#builder-field-transform-sql")).toHaveValue(saved.sql);
        await page.getByRole("button", { name: "Open field mapping for normalize", exact: true }).click();
        await page.locator("#builder-edit-mapping-btn").click();
        await expect(editor).not.toHaveAttribute("aria-busy", "true");
        await editor.getByRole("button", { name: "Open mapping", exact: true }).click();
        await expect(editor.locator("fieldset")).toHaveCount(4);
        await expect(editor.locator("fieldset").first().getByLabel("Output name", { exact: true })).toHaveValue("sku");
        sample.expected[0].currency = "PLN";
        await sampleEditor.fill(JSON.stringify(sample));
        await editor.getByRole("button", { name: "Test fixture", exact: true }).click();
        await expect(editor).toContainText("Fixture contracts and expected output passed");
        await page.locator("#builder-transform-legacy-input-contract-group details summary").click();
        await page.locator("#builder-field-transform-input-contract").fill(
            JSON.stringify({
                name: "supplier-input",
                representation: "rows",
                fields: [{ name: "item_code", type: "string", required: true }],
            }),
        );
        await page.getByRole("button", { name: "Open field mapping for normalize", exact: true }).click();
        await page.locator("#builder-edit-mapping-btn").click();
        await expect(editor).toContainText("Mapping edits are retained, but checks are stale");
        await expect(editor.locator("fieldset")).toHaveCount(4);
        await expect(sampleEditor).toHaveValue(JSON.stringify(sample));
        await editor.getByRole("button", { name: "Discard edits and reload", exact: true }).click();
        await expect(editor.locator("fieldset")).toHaveCount(4);
        await expect(sampleEditor).toHaveValue("");
    });
});

test("discloses three transform mappings and returns from the workspace without changing the canvas", async ({
    page,
}, testInfo) => {
    test.slow(testInfo.project.name === "webkit", "Three mapping panels exercise editing, dismissal and draft save.");
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const reference = {
            contract_slug: "products",
            revision_slug: "v1",
            schema_object_id: "product",
            representation: "rows",
        };
        const names = ["normalize", "price", "catalog"];
        const sql = "SELECT sku AS sku, name AS name, unit_price AS unit_price, currency AS currency FROM input";
        const steps = [
            { kind: "input", slug: "event", input: { output_contract_ref: reference } },
            ...names.map((slug, index) => ({
                kind: "transform",
                slug,
                transform: {
                    inputs: { input: index === 0 ? "event" : names[index - 1] },
                    sql,
                    output_kind: "rows",
                    input_contracts: { input: { contract_ref: reference } },
                    output_contract_ref: reference,
                },
            })),
            {
                kind: "sink",
                slug: "store",
                sink: {
                    input: "catalog",
                    data_source_ref: "source",
                    operation_type: "sql",
                    operation_inline: { sql: "SELECT {{input}}" },
                    idempotency_class: "unsafe",
                    input_contract_ref: reference,
                },
            },
        ];
        expect((await page.request.post(`${fixture.url}/_test/builder-composition`, { data: steps })).ok()).toBe(true);
        await page.setViewportSize({ width: 1440, height: 1000 });
        await page.goto(
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        const attachments = page.locator(".builder-mapping-attachment");
        await expect(attachments).toHaveCount(3);
        for (const attachment of await attachments.all())
            await expect(attachment).toContainText("4 fields · checks not run");
        await expect(page.locator(".builder-mapping-attachment-detail:visible")).toHaveCount(0);
        await expect
            .poll(() =>
                page.locator("#builder-canvas-stage").evaluate((stage) => {
                    const viewport = stage.getBoundingClientRect();
                    return [...stage.querySelectorAll(".builder-node, .builder-mapping-attachment")].every((node) => {
                        const rect = node.getBoundingClientRect();
                        return (
                            rect.left >= viewport.left &&
                            rect.right <= viewport.right &&
                            rect.top >= viewport.top &&
                            rect.bottom <= viewport.bottom
                        );
                    });
                }),
            )
            .toBe(true);
        await expect(page.locator("#builder-edges-layer path[stroke-width='2']")).toHaveCount(4);
        await captureE2EEvidenceScreenshot(page, testInfo, "mapping-attachments-collapsed.png", { fullPage: true });
        const inspect = page.getByRole("button", { name: "Open field mapping for normalize", exact: true });
        await inspect.click();
        await expect(page.locator("#builder-mapping-preview .builder-mapping-pair")).toHaveCount(4);
        await expect(page.getByRole("region", { name: "Field mapping", exact: true })).toBeHidden();
        await captureE2EEvidenceScreenshot(page, testInfo, "mapping-enlarged-default.png", { fullPage: true });
        await page.keyboard.press("Escape");
        await expect(inspect).toBeFocused();
        const toggle = page.getByRole("button", { name: "Expand field mapping for normalize", exact: true });
        await toggle.focus();
        await toggle.press("Enter");
        await expect(page.locator("#builder-mapping-detail-normalize")).toBeVisible();
        await expect(
            page.getByRole("button", { name: "Collapse field mapping for normalize", exact: true }),
        ).toBeFocused();
        await page.locator("#builder-toggle-mappings-btn").click();
        await expect(page.locator(".builder-mapping-attachment-detail:visible")).toHaveCount(3);
        await expect
            .poll(() =>
                page.locator('.builder-node[data-kind="transform"]').evaluateAll((nodes) => {
                    const bounds = nodes
                        .map((node) => ({
                            top: node.offsetTop,
                            bottom:
                                node.offsetTop +
                                node.offsetHeight +
                                12 +
                                node.querySelector(".builder-mapping-attachment").offsetHeight,
                        }))
                        .sort((a, b) => a.top - b.top);
                    return bounds.every((node, index) => index === 0 || node.top > bounds[index - 1].bottom);
                }),
            )
            .toBe(true);
        await page.locator("#builder-fit-btn").click();
        await captureE2EEvidenceScreenshot(page, testInfo, "mapping-attachments-expanded.png", { fullPage: true });
        const stage = page.locator("#builder-canvas-stage");
        for (const name of names) {
            const open = page.getByRole("button", { name: `Open field mapping for ${name}`, exact: true });
            await open.scrollIntoViewIfNeeded();
            const zoom = await page.locator("#builder-zoom-level").innerText();
            const position = await stage.evaluate((element) => ({ left: element.scrollLeft, top: element.scrollTop }));
            await open.click();
            const editor = page.getByRole("region", { name: "Field mapping", exact: true });
            await expect(page.locator("#builder-mapping-workspace-parent")).toHaveText(`Attached to ${name}`);
            await expect(page.locator("#builder-mapping-preview .builder-mapping-pair")).toHaveCount(4);
            await expect(editor).toBeHidden();
            expect(await page.locator("#builder-workspace").evaluate((element) => element.inert)).toBe(false);
            const panelBounds = await page.locator("#builder-mapping-workspace").boundingBox();
            const canvasBounds = await page.locator("#builder-canvas-stage").boundingBox();
            expect(panelBounds.width).toBeLessThan(canvasBounds.width * 0.85);
            expect(panelBounds.height).toBeLessThan(800);
            if (name === "normalize")
                await captureE2EEvidenceScreenshot(page, testInfo, "mapping-enlarged-inspection.png", {
                    fullPage: true,
                });
            await page.locator("#builder-edit-mapping-btn").click();
            await expect(editor.locator("fieldset")).toHaveCount(4);
            if (name === "normalize") {
                await editor
                    .locator("fieldset")
                    .first()
                    .getByLabel("Output name", { exact: true })
                    .fill("reviewed_sku");
                await page.locator("#builder-preview-mapping-btn").click();
                await expect(page.locator("#builder-mapping-preview")).not.toContainText("reviewed_sku");
                await page.locator("#builder-edit-mapping-btn").click();
                await expect(editor.locator("fieldset").first().getByLabel("Output name", { exact: true })).toHaveValue(
                    "reviewed_sku",
                );
                await captureE2EEvidenceScreenshot(page, testInfo, "mapping-workspace-desktop.png", { fullPage: true });
            }
            await page.keyboard.press("Escape");
            await expect(page.locator("#builder-mapping-workspace")).toBeHidden();
            await expect(open).toBeFocused();
            await expect(page.locator("#builder-zoom-level")).toHaveText(zoom);
            expect(await stage.evaluate((element) => ({ left: element.scrollLeft, top: element.scrollTop }))).toEqual(
                position,
            );
            await expect(page.locator(".builder-mapping-attachment-detail:visible")).toHaveCount(3);
            if (name === "normalize") {
                await open.click();
                await page.locator("#builder-edit-mapping-btn").click();
                await expect(editor.locator("fieldset").first().getByLabel("Output name", { exact: true })).toHaveValue(
                    "reviewed_sku",
                );
                await page.getByRole("button", { name: "Close mapping", exact: true }).click();
            }
        }
        await page.getByRole("button", { name: "Open field mapping for catalog", exact: true }).click();
        await page.locator("#builder-zoom-in-btn").click();
        await expect(page.locator("#builder-mapping-workspace")).toBeHidden();
        await page.locator("#builder-toggle-mappings-btn").click();
        await expect(page.locator(".builder-mapping-attachment-detail:visible")).toHaveCount(0);
        await expect(page.locator("#builder-save-btn")).toBeDisabled();
        await page.locator("#builder-field-step-name").fill("Catalog review");
        await page.locator("#builder-save-btn").click();
        await expect(page.locator("#builder-save-reason")).toContainText("Draft saved to Braidplane");
        const saved = await (
            await page.request.get(`${fixture.url}/api/organizations/acme/projects/ops/pipelines/daily-sync`)
        ).json();
        for (const name of names) {
            const transform = saved.draft.spec.steps.find((step) => step.slug === name).transform;
            expect(transform.sql).toBe(sql);
            expect(transform.output_contract_ref).toEqual(reference);
        }
        await page.setViewportSize({ width: 390, height: 900 });
        await page.getByRole("button", { name: "Open field mapping for catalog", exact: true }).click();
        await expect(page.locator("#builder-mapping-preview .builder-mapping-pair")).toHaveCount(4);
        await captureE2EEvidenceScreenshot(page, testInfo, "mapping-enlarged-compact.png", { fullPage: true });
        await page.locator("#builder-edit-mapping-btn").click();
        await expect(page.getByRole("region", { name: "Field mapping", exact: true }).locator("fieldset")).toHaveCount(
            4,
        );
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "mapping-workspace-compact.png", { fullPage: true });
        await page.getByRole("button", { name: "Close mapping", exact: true }).click();
        await page.locator("#builder-mode-source-btn").click();
        await expect(page.locator("#builder-mapping-workspace")).toBeHidden();
        await expect(page.locator("#builder-source-workspace-panel")).toBeVisible();
    });
});

test("connects a missing mapping input before enabling Open and New mapping", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const shape = {
            name: "products",
            representation: "rows",
            fields: [{ name: "sku", type: "string", required: true }],
        };
        const sql = "SELECT sku AS sku FROM event";
        const steps = [
            { kind: "input", slug: "event", input: { output_contract: shape } },
            {
                kind: "transform",
                slug: "map",
                transform: { inputs: {}, sql, output_kind: "rows", output_contract: shape },
            },
        ];
        expect((await page.request.post(`${fixture.url}/_test/builder-composition`, { data: steps })).ok()).toBe(true);
        await page.goto(
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        await page.getByRole("button", { name: "Open field mapping for map", exact: true }).click();
        await page.locator("#builder-edit-mapping-btn").click();
        const editor = page.getByRole("region", { name: "Field mapping", exact: true });
        await expect(editor).toContainText("Connect an input to start mapping");
        await expect(editor.getByRole("button", { name: "Open mapping", exact: true })).toBeDisabled();
        await expect(editor.getByRole("button", { name: "New mapping", exact: true })).toBeDisabled();
        await captureE2EEvidenceScreenshot(page, testInfo, "mapping-input-required.png", { fullPage: true });
        await editor.getByRole("combobox", { name: "Mapping input", exact: true }).selectOption("event");
        await editor.getByRole("button", { name: "Connect input", exact: true }).click();
        await expect(editor.locator("fieldset")).toHaveCount(1);
        await expect(editor.getByRole("button", { name: "Open mapping", exact: true })).toBeEnabled();
        await editor.getByRole("button", { name: "New mapping", exact: true }).click();
        await expect(editor).toContainText("Current SQL is preserved");
        await expect(editor.locator("fieldset")).toHaveCount(0);
        await editor.getByRole("button", { name: "Open mapping", exact: true }).click();
        await expect(editor.locator("fieldset")).toHaveCount(1);
        await expect(editor.getByLabel("Output name", { exact: true })).toHaveValue("sku");
        await expect(page.locator("#builder-field-transform-sql")).toHaveValue(sql);
        await expect(page.locator('#builder-edges-layer path[stroke-width="2"]')).toHaveCount(1);
        await captureE2EEvidenceScreenshot(page, testInfo, "mapping-input-recovered.png", { fullPage: true });
    });
});
