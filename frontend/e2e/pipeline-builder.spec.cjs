const {
    test,
    expect,
    assertSeededAuth,
    e2eOrgSlug,
    e2eProjectSlug,
    e2eEnvironmentSlug,
    e2ePipelineSlug,
    e2eSQLDataSourceSlug,
    uniqueE2ESlug,
    unsafeRequest,
    captureE2EEvidenceScreenshot,
} = require("./support/smoke-fixtures.cjs");
const { withHTMXSettle } = require("./support/htmx-settle.cjs");

test.beforeAll(assertSeededAuth);

test("saves a materialized pipeline draft and reloads it", async ({ page }, testInfo) => {
    const pipeline = `/api/organizations/${e2eOrgSlug}/projects/${e2eProjectSlug}/pipelines/${e2ePipelineSlug}`;
    const label = uniqueE2ESlug("alpha-builder", testInfo);
    const catalog = `/api/organizations/${e2eOrgSlug}/projects/${e2eProjectSlug}/contracts`;
    const catalogPath = `/orgs/${e2eOrgSlug}/projects/${e2eProjectSlug}/contracts`;
    const contractSlug = uniqueE2ESlug("alpha-odcs", testInfo);
    let contractCreated = false;
    try {
        const original = await page.request.get(pipeline);
        expect(original.ok()).toBe(true);
        const originalPipeline = await original.json();
        const canonical = originalPipeline.spec;
        const fields = canonical.steps.find((step) => step.slug === "fetch-users").query.output_contract.fields;
        const created = await unsafeRequest(page, "POST", catalog, {
            data: { slug: contractSlug, name: contractSlug, odcs_id: contractSlug },
        });
        expect(created.status()).toBe(201);
        contractCreated = true;
        const revision = await unsafeRequest(page, "POST", `${catalog}/${contractSlug}/revisions`, {
            data: {
                slug: "rev-one",
                document: {
                    apiVersion: "v3.2.0",
                    kind: "DataContract",
                    id: contractSlug,
                    version: "1",
                    status: "active",
                    description: { purpose: `<script>window.authoredODCS = true</script> Rich meaning \${UNRESOLVED}` },
                    customProperties: [{ property: "support-notes", value: { purpose: "Retained extension" } }],
                    schema: [
                        {
                            id: "users",
                            name: "Users",
                            properties: fields.map((field) => ({
                                name: field.name,
                                logicalType: field.type,
                                required: field.required,
                            })),
                        },
                    ],
                },
            },
        });
        expect(revision.status()).toBe(201);
        const digest = (await revision.json()).digest;
        await page.goto(`/organizations/${e2eOrgSlug}/projects/${e2eProjectSlug}`);
        await captureE2EEvidenceScreenshot(page, testInfo, "odcs-project-entry.png", { fullPage: true });
        await page.getByRole("link", { name: "Data contracts", exact: true }).click();
        await expect(page).toHaveURL(new RegExp(`${catalogPath}$`));
        await page.goto(
            `/builder?org_slug=${e2eOrgSlug}&project_slug=${e2eProjectSlug}&pipeline_slug=${e2ePipelineSlug}&target_environment_slug=${e2eEnvironmentSlug}`,
        );
        const node = page.locator('.builder-node[data-node-slug="fetch-users"]');
        await node.locator(".builder-node-content").click();
        await expect(page.locator("#builder-parameters-group")).toContainText(
            "{{runtime.previous_success.finished_at}}",
        );
        await expect(page.locator("#builder-parameters-group")).toContainText("{{runtime.run.datetime | offset:-24h}}");
        await expect(page.locator("#builder-parameters-group")).toContainText("month offsets clamp to the last day");
        await page.locator("#builder-parameters-group").scrollIntoViewIfNeeded();
        await expect(page.locator("#builder-parameters-group")).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "runtime-parameters-help.png", { fullPage: true });
        const desktopViewport = page.viewportSize();
        await page.setViewportSize({ width: 390, height: 844 });
        await page.locator("#builder-parameters-group").scrollIntoViewIfNeeded();
        await expect(page.locator("#builder-parameters-group")).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "runtime-parameters-help-compact.png", { fullPage: true });
        await page.setViewportSize(desktopViewport);
        await page.locator("#builder-field-step-name").fill(label);
        await withHTMXSettle(page, { pathname: catalogPath, params: { selector: "1" } }, () =>
            page.getByRole("button", { name: "Browse exact revisions" }).click(),
        );
        await withHTMXSettle(page, { pathname: catalogPath, params: { selector: "1", contract: contractSlug } }, () =>
            page.locator("#odcs-catalog").getByRole("link", { name: contractSlug, exact: true }).click(),
        );
        await withHTMXSettle(
            page,
            { pathname: catalogPath, params: { selector: "1", contract: contractSlug, revision: "rev-one" } },
            () => page.locator("#odcs-catalog").getByRole("link", { name: "rev-one", exact: true }).click(),
        );
        await expect(page).toHaveURL(/\/builder\?/);
        await expect(page.locator("#odcs-catalog").getByRole("table")).toBeVisible();
        await expect(page.locator("#odcs-catalog")).not.toContainText("Quality, SLA, governance");
        await withHTMXSettle(
            page,
            {
                pathname: catalogPath,
                params: { selector: "1", contract: contractSlug, revision: "rev-one", section: "details" },
            },
            () => page.locator("#odcs-catalog").getByRole("link", { name: "Details", exact: true }).click(),
        );
        await expect(page.locator("#odcs-catalog")).toContainText(digest);
        await expect(page.getByRole("button", { name: "Use this object" })).toHaveCount(0);
        await withHTMXSettle(
            page,
            {
                pathname: catalogPath,
                params: { selector: "1", contract: contractSlug, revision: "rev-one", section: "schema" },
            },
            () => page.locator("#odcs-catalog").getByRole("link", { name: "Schema", exact: true }).click(),
        );
        await expect(page).toHaveURL(/\/builder\?/);
        await page.locator("#builder-odcs-representation").selectOption("rows");
        await page.getByRole("button", { name: "Use this object" }).click();
        await expect(page.locator("#builder-odcs-current")).toContainText(`${contractSlug} · rev-one · users`);
        const sinkSlug = canonical.steps.find((step) => step.kind === "sink").slug;
        await page.locator(`.builder-node[data-node-slug="${sinkSlug}"] .builder-node-content`).click();
        await page.getByRole("combobox", { name: "Replay policy", exact: true }).selectOption("deduplicated_by_ledger");
        await page.locator("#builder-odcs-representation").selectOption("rows");
        await page.getByRole("button", { name: "Use this object" }).click();
        await expect(page.locator("#builder-odcs-current")).toContainText(`${contractSlug} · rev-one · users`);
        await page.locator("#builder-validate-btn").click();
        await expect(page.locator("#builder-validation-list")).toContainText("No draft issues found.");
        await expect(page.locator("#builder-server-validation")).toContainText("Server validation passed in");
        await page.locator("#builder-save-btn").click();
        await expect(page.locator("#builder-save-reason")).toContainText("Draft saved to Braidplane");
        const saved = await page.request.get(pipeline);
        expect(saved.ok()).toBe(true);
        const savedSteps = (await saved.json()).draft.spec.steps;
        expect(savedSteps.find((step) => step.slug === "fetch-users").name).toBe(label);
        const selectedRef = {
            contract_slug: contractSlug,
            revision_slug: "rev-one",
            schema_object_id: "users",
            representation: "rows",
        };
        expect(savedSteps.find((step) => step.slug === "fetch-users").query.output_contract_ref).toEqual(selectedRef);
        expect(savedSteps.find((step) => step.slug === sinkSlug).sink.input_contract_ref).toEqual(selectedRef);
        expect(savedSteps.find((step) => step.slug === sinkSlug).sink.idempotency_class).toBe("deduplicated_by_ledger");
        expect(savedSteps.find((step) => step.slug === "fetch-users").query.output_contract).toBeUndefined();
        // Exclude the local draft cache from the server-persistence assertion.
        await page.evaluate(() => localStorage.clear());
        await page.reload();
        await expect(node).toHaveAttribute("data-step-name", label);
        await page.locator(`.builder-node[data-node-slug="${sinkSlug}"] .builder-node-content`).click();
        const replayPolicy = page.getByRole("combobox", { name: "Replay policy", exact: true });
        await expect(replayPolicy).toHaveValue("deduplicated_by_ledger");
        await replayPolicy.scrollIntoViewIfNeeded();
        await captureE2EEvidenceScreenshot(page, testInfo, "sink-replay-policy-desktop.png");
        const replayViewport = page.viewportSize();
        await page.setViewportSize({ width: 390, height: 844 });
        await replayPolicy.scrollIntoViewIfNeeded();
        await expect(replayPolicy).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "sink-replay-policy-compact.png");
        await page.setViewportSize(replayViewport);
        await node.locator(".builder-node-content").click();
        await expect(page.locator("#builder-odcs-current")).toContainText(`${contractSlug} · rev-one · users`);
        await captureE2EEvidenceScreenshot(page, testInfo, "odcs-builder-selected.png", { fullPage: true });
        await page.getByRole("button", { name: "Use inline contract", exact: true }).click();
        await expect(page.locator("#builder-odcs-current")).toContainText("Using an inline agreement.");
        await expect(page.locator("#builder-query-output-contract-editor")).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "alpha-builder.png", { fullPage: true });
        await page.goto(
            `/orgs/${e2eOrgSlug}/projects/${e2eProjectSlug}/contracts?contract=${contractSlug}&revision=rev-one`,
        );
        for (const [name, viewport] of [
            ["desktop", { width: 1440, height: 900 }],
            ["compact", { width: 390, height: 844 }],
        ]) {
            await page.setViewportSize(viewport);
            await expect(page.getByRole("heading", { name: "Data contracts", exact: true })).toBeVisible();
            await expect(page.getByLabel("Product stage", { exact: true })).toHaveText("Alpha");
            for (const section of ["schema", "details", "document", "coverage", "consumers"]) {
                await withHTMXSettle(
                    page,
                    { pathname: catalogPath, params: { contract: contractSlug, revision: "rev-one", section } },
                    () =>
                        page
                            .locator("#odcs-catalog")
                            .getByRole("link", { name: `${section[0].toUpperCase()}${section.slice(1)}`, exact: true })
                            .click(),
                );
                const panel = page.locator("#odcs-catalog");
                if (section === "schema") {
                    await expect(panel.getByRole("table")).toBeVisible();
                    await expect(panel).not.toContainText("Quality, SLA, governance");
                } else if (section === "details") {
                    await expect(panel).toContainText(digest);
                    await expect(panel.getByRole("table")).toHaveCount(0);
                } else if (section === "document") {
                    await expect(panel.getByLabel("Canonical ODCS document")).toContainText(
                        `Rich meaning \${UNRESOLVED}`,
                    );
                    await expect(panel.getByLabel("Canonical ODCS document")).toContainText("Retained extension");
                    expect(await page.evaluate(() => window.authoredODCS)).toBeUndefined();
                } else if (section === "coverage") {
                    expect(await page.evaluate(() => sessionStorage.getItem("htmx-history-cache") || "")).not.toContain(
                        "Retained extension",
                    );
                    await panel.getByRole("textbox", { name: "Schema object ID" }).fill("users");
                    await withHTMXSettle(
                        page,
                        { pathname: catalogPath, params: { section: "coverage", object: "users" } },
                        () => panel.getByRole("button", { name: "Inspect coverage", exact: true }).click(),
                    );
                    await expect(panel).toContainText("braidplane/odcs-flat-port/v2");
                    await expect(panel).toContainText("variable_not_resolved");
                    await expect(panel).toContainText("compiled_runtime_constraint");
                    await expect(panel).toContainText("no execution has passed here");
                } else {
                    await expect(panel).toContainText("fetch-users");
                    await expect(panel).toContainText(sinkSlug);
                }
                expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
                await captureE2EEvidenceScreenshot(page, testInfo, `odcs-${section}-${name}.png`, { fullPage: true });
            }
        }

        const cleared = await unsafeRequest(page, "DELETE", `${pipeline}/draft`);
        expect([204, 404]).toContain(cleared.status());
        const matchingSpec = structuredClone(canonical);
        delete matchingSpec.metadata.slug;
        const matching = await unsafeRequest(page, "PUT", `${pipeline}/draft`, {
            data: { spec: matchingSpec, expected_spec_hash: originalPipeline.current_version_spec_hash },
        });
        expect(matching.status()).toBe(200);
        await page.setViewportSize({ width: 1440, height: 900 });
        await page.goto(
            `/builder?org_slug=${e2eOrgSlug}&project_slug=${e2eProjectSlug}&pipeline_slug=${e2ePipelineSlug}&target_environment_slug=${e2eEnvironmentSlug}`,
        );
        const matchingNotice = page.locator("#builder-matching-draft-notice");
        await expect(matchingNotice).toContainText("matches the current pipeline definition");
        await captureE2EEvidenceScreenshot(matchingNotice, testInfo, "matching-draft-notice.png");
        await page.setViewportSize({ width: 390, height: 844 });
        await matchingNotice.scrollIntoViewIfNeeded();
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(matchingNotice, testInfo, "matching-draft-notice-compact.png");
        await page.setViewportSize({ width: 1440, height: 900 });
        await node.locator(".builder-node-content").click();
        await page.locator("#builder-field-step-name").fill("Local unsaved edit");
        await expect(matchingNotice).toHaveCount(0);
        await page.evaluate(() => localStorage.clear());
        await page.reload();
        await matchingNotice.getByRole("link", { name: "Review saved draft", exact: true }).click();
        await expect(page).toHaveURL(/section=git/);
        page.once("dialog", (dialog) => dialog.dismiss());
        await page.getByRole("button", { name: "Discard draft", exact: true }).click();
        expect((await (await page.request.get(pipeline)).json()).has_draft).toBe(true);
        page.once("dialog", (dialog) => dialog.accept());
        await page.getByRole("button", { name: "Discard draft", exact: true }).click();
        await expect.poll(async () => (await (await page.request.get(pipeline)).json()).has_draft).toBe(false);
        const recovered = await (await page.request.get(pipeline)).json();
        expect(recovered.current_version_spec_hash).toBe(originalPipeline.current_version_spec_hash);
        expect(recovered.spec).toEqual(canonical);
        await page.getByRole("link", { name: `Open Builder in ${e2eEnvironmentSlug}`, exact: true }).click();
        await expect(page.locator("#builder-canvas-root")).toHaveAttribute("data-pipeline-has-draft", "false");
        await expect(matchingNotice).toHaveCount(0);
    } finally {
        const deleted = await unsafeRequest(page, "DELETE", `${pipeline}/draft`);
        expect([204, 404]).toContain(deleted.status());
        if (contractCreated) {
            const archived = await unsafeRequest(page, "PUT", `${catalog}/${contractSlug}`, {
                data: { name: contractSlug, status: "archived" },
            });
            expect(archived.status()).toBe(200);
        }
    }
});

test("previews an unmaterialized definition while keeping runtime preflight separate", async ({ page }, testInfo) => {
    const slug = uniqueE2ESlug("unbound-preview", testInfo);
    await page.goto(
        `/builder?org_slug=${e2eOrgSlug}&project_slug=${e2eProjectSlug}&pipeline_slug=&target_environment_slug=${e2eEnvironmentSlug}`,
    );
    await page.getByRole("textbox", { name: "Pipeline name", exact: true }).fill("Unbound source preview");
    await page.getByRole("textbox", { name: "Pipeline slug", exact: true }).fill(slug);
    await page
        .getByRole("button", {
            name: "Query Step (Execute) Run the referenced query with step parameters.",
            exact: true,
        })
        .click();
    await expect(page.getByRole("combobox", { name: "Add outgoing link", exact: true })).toBeVisible();
    for (const role of ["textbox", "combobox", "checkbox"]) {
        await expect(page.getByRole(role, { name: "", exact: true })).toHaveCount(0);
    }
    const connectionSetup = page
        .locator("#builder-group-source")
        .getByRole("link", { name: "Manage connections (new tab)" });
    await expect(connectionSetup).toHaveAttribute(
        "href",
        `/orgs/${e2eOrgSlug}/data-sources?environment_slug=${e2eEnvironmentSlug}`,
    );
    const setupPagePromise = page.waitForEvent("popup");
    await connectionSetup.click();
    const setupPage = await setupPagePromise;
    await expect(setupPage.getByRole("button", { name: "Add Data Source", exact: true })).toBeVisible();
    await setupPage.close();
    await expect(page.getByRole("textbox", { name: "Pipeline slug", exact: true })).toHaveValue(slug);
    await expect(page.locator('.builder-node[data-kind="query"]')).toHaveCount(1);
    await captureE2EEvidenceScreenshot(page, testInfo, "connection-setup-link.png", { fullPage: true });
    const setupViewport = page.viewportSize();
    await page.setViewportSize({ width: 390, height: 844 });
    await connectionSetup.scrollIntoViewIfNeeded();
    await expect(connectionSetup).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await captureE2EEvidenceScreenshot(page, testInfo, "connection-setup-link-compact.png", { fullPage: true });
    await page.setViewportSize(setupViewport);
    await page.locator("#builder-field-data-source").selectOption(e2eSQLDataSourceSlug);
    await page.locator("#builder-field-inline-sql-toggle").check();
    await page.locator("#builder-field-inline-sql").fill("SELECT 1 AS marker");
    await page.locator("#builder-field-inline-sql").press("Tab");
    const inventoryURL = `**/api/organizations/${e2eOrgSlug}/data-sources?*`;
    await page.route(inventoryURL, (route) => route.fulfill({ status: 503, body: "unavailable" }));
    await page
        .locator("#builder-group-source")
        .getByRole("button", { name: "Refresh connections", exact: true })
        .click();
    await expect(
        page.getByText("Connections could not be refreshed. Check access and try again.", { exact: true }),
    ).toBeVisible();
    await expect(page.locator("#builder-field-data-source")).toHaveValue(e2eSQLDataSourceSlug);
    await expect(page.locator("#builder-field-inline-sql")).toHaveValue("SELECT 1 AS marker");
    await page.unroute(inventoryURL);
    await page
        .locator("#builder-group-source")
        .getByRole("button", { name: "Refresh connections", exact: true })
        .click();
    await expect(page.getByText("Connections refreshed.", { exact: true })).toBeVisible();
    await expect(page.locator("#builder-field-data-source")).toHaveValue(e2eSQLDataSourceSlug);
    await expect(page.locator("#builder-field-inline-sql")).toHaveValue("SELECT 1 AS marker");
    await expect(page.getByRole("textbox", { name: "Pipeline slug", exact: true })).toHaveValue(slug);
    await page.getByRole("button", { name: "Source", exact: true }).click();
    const source = page.getByRole("textbox", { name: "Pipeline source", exact: true });
    await expect(source).toHaveValue(/SELECT 1 AS marker/);
    await expect(source).toHaveValue(new RegExp(slug));
    await expect(page.getByRole("button", { name: new RegExp(`^Validate in ${e2eEnvironmentSlug}$`) })).toBeDisabled();
    await expect(page.locator("#builder-creation-form")).toContainText("before validation and deployment");
    await expect(source).toHaveValue(/SELECT 1 AS marker/);
    await captureE2EEvidenceScreenshot(page, testInfo, "unbound-source-preview.png", { fullPage: true });
    const desktopViewport = page.viewportSize();
    await page.setViewportSize({ width: 390, height: 844 });
    await source.scrollIntoViewIfNeeded();
    await expect(source).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await captureE2EEvidenceScreenshot(page, testInfo, "unbound-source-preview-compact.png", { fullPage: true });
    await page.setViewportSize(desktopViewport);
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    await page.locator("#builder-field-inline-sql").fill("SELECT 2 AS marker");
    await page.locator("#builder-field-inline-sql").press("Tab");
    await page.getByRole("button", { name: "Source", exact: true }).click();
    await expect(source).toHaveValue(/SELECT 2 AS marker/);
    const sourceStepSlug = await page.locator('.builder-node[data-kind="query"]').getAttribute("data-node-slug");
    const editedSource = (await source.inputValue())
        .replace("SELECT 2 AS marker", "SELECT 3 AS marker")
        .replaceAll(sourceStepSlug, "fetch");
    await source.fill(editedSource);
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    await page.getByRole("button", { name: "Source", exact: true }).click();
    await expect(source).toHaveValue(editedSource);
    await page.getByRole("button", { name: "Sync From Canvas", exact: true }).click();
    await expect(source).toHaveValue(/SELECT 2 AS marker/);
    await source.fill(editedSource);
    await page.getByRole("button", { name: "Apply To Canvas", exact: true }).click();
    await expect(page.getByText("Applied source, refreshed graph, and fitted canvas.", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    await page.locator('.builder-node[data-kind="query"] .builder-node-content').click();
    await expect(page.locator("#builder-field-inline-sql")).toHaveValue("SELECT 3 AS marker");
    await page.locator("#builder-field-inline-sql").fill("SELECT 2 AS marker");
    await page.locator("#builder-field-inline-sql").press("Tab");
    await page.getByRole("button", { name: "Source", exact: true }).click();
    await expect(source).toHaveValue(/SELECT 2 AS marker/);
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    const addTransform = page.getByRole("button", {
        name: "Transform Step Apply SQL transform logic to upstream rows.",
        exact: true,
    });
    await addTransform.click();
    for (const role of ["textbox", "combobox", "checkbox"]) {
        await expect(page.getByRole(role, { name: "", exact: true })).toHaveCount(0);
    }
    const queryNode = page.locator('.builder-node[data-kind="query"] .builder-node-content');
    const transforms = page.locator('.builder-node[data-kind="transform"] .builder-node-content');
    await queryNode.click();
    await page.getByRole("textbox", { name: "Step name", exact: true }).fill("Fetch");
    await page.getByRole("textbox", { name: "Step name", exact: true }).press("Tab");
    await transforms.first().click({ modifiers: ["Shift"] });
    const alias = await page.getByRole("textbox", { name: "SQL alias", exact: true }).inputValue();
    expect(alias).toBe("input_fetch");
    await expect(page.locator("#builder-field-transform-sql")).toHaveValue(`SELECT * FROM ${alias}`);
    await captureE2EEvidenceScreenshot(page, testInfo, "transform-default-alias.png");
    const draftIssues = page.locator("#builder-validation-list");
    await expect(draftIssues).toContainText("declare an output contract on");
    await expect(draftIssues).toContainText(`or an input contract for ${alias}`);
    await captureE2EEvidenceScreenshot(page, testInfo, "missing-boundary-contract.png", { fullPage: true });
    const contractViewport = page.viewportSize();
    await page.setViewportSize({ width: 390, height: 844 });
    await draftIssues.scrollIntoViewIfNeeded();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await captureE2EEvidenceScreenshot(draftIssues, testInfo, "missing-boundary-contract-compact.png");
    await page.setViewportSize(contractViewport);
    await queryNode.click();
    await page.locator("#builder-query-output-contract-group").getByText("Raw contract JSON", { exact: true }).click();
    await page.getByRole("textbox", { name: "Source output contract JSON", exact: true }).fill(
        JSON.stringify({
            name: "markers",
            representation: "rows",
            fields: [{ name: "marker", type: "integer", required: true }],
        }),
    );
    await page.getByRole("textbox", { name: "Source output contract JSON", exact: true }).press("Tab");
    await expect(draftIssues).not.toContainText("declare an output contract on");
    await expect(draftIssues).toContainText("No draft issues found.");
    await captureE2EEvidenceScreenshot(page, testInfo, "boundary-contract-recovered.png", { fullPage: true });
    await transforms.first().click();
    await page.getByRole("textbox", { name: "SQL alias", exact: true }).fill("http_items");
    await page.getByRole("textbox", { name: "SQL alias", exact: true }).press("Tab");
    await page.locator("#builder-field-transform-sql").fill("SELECT marker AS marker FROM http_items");
    await page.locator("#builder-field-transform-sql").press("Tab");
    await queryNode.click();
    await transforms.first().click();
    await expect(page.getByRole("textbox", { name: "SQL alias", exact: true })).toHaveValue("http_items");
    await page.getByRole("button", { name: "Source", exact: true }).click();
    await expect(source).toHaveValue(/SELECT marker AS marker FROM http_items/);
    await expect(page.getByText("Source synced from current canvas.", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Preview", exact: true }).click();

    await addTransform.click();
    const authoredSQL = `SELECT marker FROM ${alias}`;
    await page.locator("#builder-field-transform-sql").fill(authoredSQL);
    await page.locator("#builder-field-transform-sql").press("Tab");
    await queryNode.click();
    await transforms.nth(1).click({ modifiers: ["Shift"] });
    await expect(page.locator("#builder-field-transform-sql")).toHaveValue(authoredSQL);
});

test("recovers a new pipeline after reload and retains unfinished contract rows", async ({ page }) => {
    await page.goto(
        `/builder?org_slug=${e2eOrgSlug}&project_slug=${e2eProjectSlug}&target_environment_slug=${e2eEnvironmentSlug}`,
    );
    await page.getByRole("textbox", { name: "Pipeline name", exact: true }).fill("Recovery check");
    await page.getByRole("textbox", { name: "Pipeline slug", exact: true }).fill("recovery-check");
    await page.getByRole("button", { name: /^Query Step \(Execute\)/ }).click();
    const editor = page.locator("#builder-query-output-contract-editor");
    for (let i = 0; i < 3; i++) await editor.getByRole("button", { name: "Add field", exact: true }).click();
    const rows = editor.locator("[data-contract-field-row]");
    await rows.nth(0).locator("[data-contract-field-name]").fill("id");
    await rows.nth(0).locator("[data-contract-field-type]").selectOption("integer");
    await expect(rows).toHaveCount(3);
    await rows.nth(1).locator("[data-contract-field-name]").fill("name");
    await rows.nth(2).locator("[data-contract-field-name]").fill("email");
    await expect(page.locator("#builder-field-query-output-contract")).toHaveValue(/"name": "email"/);
    await page.reload();
    await expect(page.getByRole("textbox", { name: "Pipeline name", exact: true })).toHaveValue("Recovery check");
    await expect(page.getByRole("textbox", { name: "Pipeline slug", exact: true })).toHaveValue("recovery-check");
    await expect(page.locator(".builder-node")).toHaveCount(1);
    await page.locator(".builder-node .builder-node-content").click();
    for (const [index, name] of ["id", "name", "email"].entries()) {
        await expect(editor.locator("[data-contract-field-name]").nth(index)).toHaveValue(name);
    }
});
