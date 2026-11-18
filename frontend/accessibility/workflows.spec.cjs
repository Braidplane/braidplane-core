const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, visit, tabTo } = require("./support.cjs");

test.describe("local application", () => {
    test("authentication entry pages", async ({ browser }, testInfo) => {
        const context = await browser.newContext({
            baseURL: testInfo.project.use.baseURL,
            viewport: testInfo.project.use.viewport,
            storageState: { cookies: [], origins: [] },
        });
        try {
            const page = await context.newPage();
            for (const path of ["/auth/login", "/auth/signup", "/auth/forgot-password"]) {
                await visit(page, path);
                await page.keyboard.press("Tab");
                await expect(page.getByRole("link", { name: "Skip to main content" })).toBeFocused();
                await page.keyboard.press("Enter");
                await expect(page.locator("#main-content")).toBeFocused();
                await assess(page, testInfo, path);
            }
        } finally {
            await context.close();
        }
    });

    test("keyboard sign-in recovers from an error and signs out", async ({ browser }, testInfo) => {
        const context = await browser.newContext({
            baseURL: testInfo.project.use.baseURL,
            viewport: testInfo.project.use.viewport,
            storageState: { cookies: [], origins: [] },
        });
        try {
            const page = await context.newPage();
            await visit(page, "/auth/login");
            const email = page.getByLabel("Email", { exact: true });
            const password = page.getByLabel("Password", { exact: true });
            await expect(email).toHaveAttribute("autocomplete", "username");
            await expect(password).toHaveAttribute("autocomplete", "current-password");
            await tabTo(page, email);
            await page.keyboard.insertText(process.env.E2E_USER_EMAIL);
            await page.keyboard.press("Tab");
            await expect(password).toBeFocused();
            await page.keyboard.insertText("Incorrect-synthetic-password");
            const rejected = page.waitForResponse(
                (response) =>
                    new URL(response.url()).pathname === "/auth/login" && response.request().method() === "POST",
            );
            await page.keyboard.press("Enter");
            expect((await rejected).status()).toBe(401);
            await expect(page.getByRole("alert")).toBeFocused();
            await expect(email).toHaveValue(process.env.E2E_USER_EMAIL);
            expect((await password.inputValue()) === "").toBe(true);
            for (const width of [1280, 320]) {
                await page.setViewportSize({ width, height: 900 });
                await assess(page, testInfo, `sign-in-error-${width}`);
            }
            await tabTo(page, password);
            await page.keyboard.insertText(process.env.E2E_USER_PASSWORD);
            await page.keyboard.press("Enter");
            await expect(page).toHaveURL(/\/dashboard$/);
            for (const width of [1280, 320]) {
                await page.setViewportSize({ width, height: 900 });
                await assess(page, testInfo, `sign-in-recovered-${width}`);
                if (width === 1280) await tabTo(page, page.getByRole("button", { name: "Logout", exact: true }));
            }
            await tabTo(page, page.getByRole("button", { name: "Open navigation menu", exact: true }));
            await page.keyboard.press("Enter");
            await expect(page.getByRole("navigation", { name: "Mobile primary", exact: true })).toBeVisible();
            await assess(page, testInfo, "compact-navigation-open");
            await tabTo(page, page.getByRole("button", { name: "Logout", exact: true }));
            await page.keyboard.press("Space");
            await expect(page).toHaveURL(/\/auth\/login$/);
            for (const width of [1280, 320]) {
                await page.setViewportSize({ width, height: 900 });
                await assess(page, testInfo, `signed-out-${width}`);
            }
        } finally {
            await context.close();
        }
    });

    test("project and source setup, empty authoring and inspection", async ({ page }, testInfo) => {
        const org = process.env.E2E_ORG_SLUG;
        const project = process.env.E2E_PROJECT_SLUG;
        expect(org).toBeTruthy();
        expect(project).toBeTruthy();
        for (const path of [
            "/dashboard",
            `/organizations/${org}/projects`,
            `/organizations/${org}/projects/${project}`,
            `/orgs/${org}/data-sources`,
            `/builder?org_slug=${org}&project_slug=${project}&target_environment_slug=dev`,
            `/orgs/${org}/projects/${project}/pipelines?environment_slug=dev`,
            `/orgs/${org}/projects/${project}/runs?environment_slug=dev`,
            `/orgs/${org}/projects/${project}/proof-receipts?environment_slug=dev`,
        ]) {
            await visit(page, path);
            if (path === "/dashboard") {
                const checklist = page.locator("#dashboard-onboarding");
                const hide = checklist.getByRole("button", { name: "Hide", exact: true });
                const resume = page.getByRole("button", { name: "Resume setup", exact: true });
                await tabTo(page, hide);
                await page.keyboard.press("Enter");
                await expect(checklist).toBeHidden();
                await expect(resume).toBeFocused();
                await page.keyboard.press("Enter");
                await expect(checklist).toBeVisible();
                await expect(hide).toBeFocused();
            }
            await assess(page, testInfo, path);
        }
    });

    test("skip navigation works from the keyboard at desktop and 320 CSS pixels", async ({ page }, testInfo) => {
        for (const width of [1280, 320]) {
            await page.setViewportSize({ width, height: 900 });
            await visit(page, "/dashboard");
            await page.keyboard.press("Tab");
            const skip = page.getByRole("link", { name: /skip to/i });
            await expect(skip).toBeFocused();
            await expect(skip).toBeInViewport();
            await page.keyboard.press("Enter");
            await expect(page.locator("#main-content")).toBeFocused();
            await assess(page, testInfo, `dashboard-${width}`);
        }
    });
});

test.describe("production frontend with isolated API replies", () => {
    test.use({ storageState: { cookies: [], origins: [] } });

    test("populated Builder, workflow, deployment and run states", async ({ page }, testInfo) => {
        await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
            for (const path of [
                "/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev",
                "/orgs/acme/projects/ops/pipelines/daily-sync?environment_slug=dev&section=workflow",
                `/orgs/acme/projects/ops/pipelines/daily-sync/bindings?environment_slug=dev&spec_hash=${"a".repeat(64)}`,
                `/orgs/acme/projects/ops/pipelines/daily-sync/deploy?environment_slug=dev&spec_hash=${"a".repeat(64)}`,
            ]) {
                await visit(page, `${fixture.url}${path}`);
                await assess(page, testInfo, path);
                if (path.includes("section=workflow") && page.viewportSize().width === 320) {
                    const fields = page.getByRole("region", { name: /contract fields$/ }).first();
                    await fields.focus();
                    await page.keyboard.press("ArrowRight");
                    await expect.poll(() => fields.evaluate((el) => el.scrollLeft)).toBeGreaterThan(0);
                }
            }
            for (const status of ["running", "succeeded", "failed"]) {
                expect((await page.request.post(`${fixture.url}/_test/run/${status}`)).ok()).toBe(true);
                await visit(page, `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev`);
                await expect(page.locator("#run-recorded-status")).toHaveText(status);
                await assess(page, testInfo, `run-${status}`);
            }
        });
    });

    test("Builder view mode exposes its selected state and keeps keyboard focus", async ({ page }, testInfo) => {
        await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
            await visit(
                page,
                `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
            );
            const names = ["Preview", "Split", "Source"];
            const modes = page.getByRole("group", { name: "Builder view", exact: true });
            const button = (name) => modes.getByRole("button", { name, exact: true });
            const canvas = page.locator("#builder-canvas-root");
            const source = page.locator("#builder-source-workspace-panel");
            await expect(button("Preview")).toHaveAttribute("aria-pressed", "true");
            for (const selected of ["Split", "Source", "Preview"]) {
                const control = button(selected);
                await tabTo(page, control);
                await page.keyboard.press("Space");
                await expect(control).toBeFocused();
                await expect(control).toBeInViewport({ ratio: 1 });
                for (const name of names) {
                    await expect(button(name)).toHaveAttribute("aria-pressed", String(name === selected));
                }
                await expect(canvas).toBeVisible({ visible: selected !== "Source" });
                await expect(source).toBeVisible({ visible: selected !== "Preview" });
                if (selected !== "Preview") await assess(page, testInfo, `builder-view-${selected.toLowerCase()}`);
                if (selected === "Source") {
                    const editor = source.getByRole("textbox", { name: "Pipeline source", exact: true });
                    await tabTo(page, editor);
                    await expect(editor).toHaveCSS("outline-style", "solid");
                    await expect(editor).toHaveCSS("outline-offset", "-2px");
                    await expect(editor).not.toHaveValue("");
                    const original = await editor.inputValue();
                    const apply = source.getByRole("button", { name: "Apply To Canvas", exact: true });
                    const status = source.getByRole("status");
                    await page.keyboard.press("ControlOrMeta+a");
                    await page.keyboard.press("Backspace");
                    await tabTo(page, apply);
                    await page.keyboard.press("Space");
                    await expect(status).toHaveText("Pipeline source is empty.");
                    await expect(apply).toBeFocused();
                    await tabTo(page, editor);
                    const malformed = "steps: [";
                    await page.keyboard.insertText(malformed);
                    await tabTo(page, apply);
                    await page.keyboard.press("Space");
                    await expect(status).toContainText("Check the YAML or JSON syntax and required pipeline fields.");
                    await expect(apply).toBeFocused();
                    await expect(editor).toHaveValue(malformed);
                    await expect(editor).toHaveAccessibleDescription(/Check the YAML or JSON syntax/);
                    await assess(page, testInfo, "builder-source-import-error");
                    await tabTo(page, editor);
                    await page.keyboard.press("ControlOrMeta+a");
                    const corrected = original.replace("Normalize customers", "Review imported customers");
                    expect(corrected).not.toBe(original);
                    await page.keyboard.insertText(corrected);
                    await tabTo(page, apply);
                    await page.keyboard.press("Space");
                    await expect(status).toHaveText("Applied source, refreshed graph, and fitted canvas.");
                    await expect(apply).toBeFocused();
                    await assess(page, testInfo, "builder-source-import-recovered");
                }
            }
            const step = page.getByRole("button", { name: /Review imported customers/ });
            await expect(step).toBeVisible();
            const save = page.getByRole("button", { name: "Save", exact: true });
            await tabTo(page, save);
            await page.keyboard.press("Enter");
            await expect(page.locator("#builder-save-reason")).toContainText("Draft saved to Braidplane");
            await page.reload();
            await tabTo(page, step);
            await expect(step).toBeFocused();
        });
    });

    test("Builder steps and connections can be reached and inspected using keyboard", async ({ page }, testInfo) => {
        await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
            await visit(
                page,
                `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
            );
            const node = page.locator('.builder-node[data-node-slug="event"]');
            const interceptedZoomKeys = await node.evaluate((element) => {
                const intercepted = [];
                for (const modifier of ["ctrlKey", "metaKey"]) {
                    for (const key of ["+", "=", "-", "0"]) {
                        const event = new KeyboardEvent("keydown", {
                            key,
                            [modifier]: true,
                            bubbles: true,
                            cancelable: true,
                        });
                        element.dispatchEvent(event);
                        if (event.defaultPrevented) intercepted.push(`${modifier} ${key}`);
                    }
                }
                return intercepted;
            });
            expect(interceptedZoomKeys).toEqual([]);
            await tabTo(page, node);
            await expect(node).toHaveAccessibleName(/Event Input.*event.*JSON event payload/i);
            await page.keyboard.press("Enter");
            await expect(page.locator("#builder-field-step-slug")).toHaveValue("event");
            const selectedCard = node.locator(".builder-node-content");
            await expect(selectedCard).toHaveClass(/node-card-selected/);
            await expect(selectedCard).toHaveCSS("outline-style", "solid");
            const renderedOutlineWidth = await selectedCard.evaluate(
                (card) =>
                    (Number.parseFloat(getComputedStyle(card).outlineWidth) * card.getBoundingClientRect().width) /
                    card.offsetWidth,
            );
            expect(renderedOutlineWidth).toBeGreaterThanOrEqual(2);
            const edge = page.getByRole("button", { name: "Inspect connection event to normalize", exact: true });
            await tabTo(page, edge);
            await expect(selectedCard).toHaveClass(/node-card-selected/);
            await expect(selectedCard).toHaveCSS("outline-style", "none");
            await page.keyboard.press("Enter");
            await expect(page.locator("#builder-edge-preview")).toContainText("event");
            await assess(page, testInfo, "builder-keyboard-edge-inspection");
        });
    });

    test("Builder graph can be edited from the keyboard without dragging", async ({ page }, testInfo) => {
        await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
            await visit(
                page,
                `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
            );
            await tabTo(page, page.getByRole("button", { name: /^Transform Step Apply SQL/ }));
            await page.keyboard.press("Enter");
            const name = page.getByRole("textbox", { name: "Step name", exact: true });
            await tabTo(page, name);
            await page.keyboard.insertText("Review customers");
            await page.keyboard.press("Tab");
            const added = page.locator('.builder-node[data-node-slug="review-customers"]');
            await expect(added).toHaveAccessibleName(/review-customers/i);
            const addedCard = added.getByRole("button", { name: /^Transform Step review-customers/ });
            const inputIssue = page.getByRole("button", {
                name: "review-customers: missing transform input binding",
                exact: true,
            });
            await tabTo(page, inputIssue);
            await page.keyboard.press("Enter");
            await expect(addedCard).toBeFocused();

            const view = page.getByRole("group", { name: "Builder view", exact: true });
            await tabTo(page, view.getByRole("button", { name: "Source", exact: true }));
            await page.keyboard.press("Enter");
            const editor = page.getByRole("textbox", { name: "Pipeline source", exact: true });
            await tabTo(page, editor);
            await page.keyboard.press("ControlOrMeta+End");
            await page.keyboard.insertText("\n# Keep this unapplied source");
            const sourceDraft = await editor.inputValue();
            await tabTo(page, inputIssue);
            await page.keyboard.press("Enter");
            await expect(view.getByRole("button", { name: "Split", exact: true })).toHaveAttribute(
                "aria-pressed",
                "true",
            );
            await expect(addedCard).toBeFocused();
            await expect(addedCard).toBeInViewport();
            await expect(editor).toHaveValue(sourceDraft);
            await assess(page, testInfo, "builder-local-diagnostic-from-source");

            await tabTo(page, view.getByRole("button", { name: "Preview", exact: true }));
            await page.keyboard.press("Enter");

            const producer = page.locator('.builder-node[data-node-slug="normalize"]');
            await tabTo(page, producer.getByRole("button", { name: /^Transform Step normalize/ }));
            await page.keyboard.press("Enter");
            const destination = page.getByLabel("Add outgoing link", { exact: true });
            await expect(destination).toHaveAccessibleDescription(
                "Choose a step in Add outgoing link, then select Add.",
            );
            await tabTo(page, destination);
            await page.keyboard.press("r");
            await expect(destination).toHaveValue("review-customers");
            await page.keyboard.press("Tab");
            await expect(page.locator("#builder-connection-outgoing-add-btn")).toBeFocused();
            await page.keyboard.press("Enter");
            const edge = page.getByRole("button", {
                name: /^Inspect (incompatible )?connection normalize to review-customers$/,
            });
            await expect(edge).toBeAttached();
            await expect(destination).toBeFocused();
            await expect(inputIssue).toHaveCount(0);
            await assess(page, testInfo, "builder-keyboard-connected-step");

            const output = page.getByLabel("Transform output contract JSON", { exact: true });
            const rawOutput = page.locator("details").filter({ has: output }).getByText("Raw contract JSON");
            await tabTo(page, rawOutput);
            await page.keyboard.press("Enter");
            await page.keyboard.press("Tab");
            await expect(output).toBeFocused();
            await page.keyboard.insertText(
                JSON.stringify({
                    name: "customers",
                    representation: "rows",
                    fields: [{ name: "email", type: "string", required: true }],
                }),
            );
            await page.keyboard.press("Tab");
            await expect(page.locator("#builder-validation-list")).toHaveText("No draft issues found.");
            const validate = page.getByRole("button", { name: /^Validate/ });
            await tabTo(page, validate);
            await page.keyboard.press("Enter");
            await expect(page.locator("#builder-server-validation")).toContainText("Server validation passed in dev");

            const save = page.getByRole("button", { name: "Save", exact: true });
            await tabTo(page, save);
            const saved = page.waitForResponse(
                (response) => response.request().method() === "POST" && response.url().endsWith("/pipelines/save"),
            );
            await page.keyboard.press("Enter");
            expect((await saved).ok()).toBe(true);
            await page.reload();
            await expect(added).toBeAttached();
            await expect(edge).toBeAttached();
            await tabTo(page, producer.getByRole("button", { name: /^Transform Step normalize/ }));
            await page.keyboard.press("Enter");
            const remove = page.getByRole("button", { name: "Remove connection normalize to review-customers" });
            await tabTo(page, remove);
            await page.keyboard.press("Space");
            await expect(edge).toHaveCount(0);
            await expect(destination).toBeFocused();

            await page.keyboard.press("r");
            await page.keyboard.press("Tab");
            await page.keyboard.press("Enter");
            await tabTo(page, edge);
            await page.keyboard.press("Enter");
            const preview = page.getByRole("button", { name: "Preview", exact: true });
            await tabTo(page, preview);
            await page.keyboard.press("Delete");
            await expect(edge).toBeAttached();
            await expect(preview).toBeFocused();
            await tabTo(page, edge);
            await page.keyboard.press("Delete");
            await expect(edge).toHaveCount(0);
            const properties = page.getByRole("button", { name: "Hide properties", exact: true });
            await expect(properties).toBeFocused();

            await tabTo(page, addedCard);
            await page.keyboard.press("Enter");
            await tabTo(page, page.getByRole("button", { name: "Remove step", exact: true }));
            await page.keyboard.press("Enter");
            await expect(added).toHaveCount(0);
            await expect(properties).toBeFocused();
            await page.keyboard.press("ControlOrMeta+z");
            await expect(added).toBeAttached();
            await tabTo(page, addedCard);
            await page.keyboard.press("Enter");
            await tabTo(page, preview);
            await page.keyboard.press("Backspace");
            await expect(added).toBeAttached();
            await expect(preview).toBeFocused();
            await tabTo(page, addedCard);
            await page.keyboard.press("Delete");
            await expect(added).toHaveCount(0);
            await expect(properties).toBeFocused();
            await expect(producer).toBeAttached();
        });
    });

    test("Builder pointer connections commit on release and can be canceled", async ({ page }) => {
        await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
            await visit(
                page,
                `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
            );
            await tabTo(page, page.getByRole("button", { name: /^Transform Step Apply SQL/ }));
            await page.keyboard.press("Enter");
            await tabTo(page, page.getByRole("textbox", { name: "Step name", exact: true }));
            await page.keyboard.insertText("Review customers");
            await page.keyboard.press("Tab");

            const producer = page.locator('.builder-node[data-node-slug="normalize"]');
            const output = producer.locator(".node-port-out");
            const input = page.locator('.builder-node[data-node-slug="review-customers"] .node-port-in');
            const edge = page.getByRole("button", { name: /connection normalize to review-customers$/i });
            await tabTo(page, producer.getByRole("button", { name: /^Transform Step normalize/ }));
            await page.keyboard.press("Enter");
            await input.hover();
            await page.mouse.down();
            await expect(edge).toHaveCount(0);
            await page.mouse.move(2, 2);
            await page.mouse.up();
            await expect(edge).toHaveCount(0);

            await output.hover();
            await page.mouse.down();
            await expect(page.getByText(/^Connection source selected\. In Outputs/)).toBeVisible();
            await input.hover();
            const preview = page.locator("#builder-connection-preview-line");
            await expect(preview).toBeAttached();
            await page.keyboard.press("Escape");
            await expect(preview).toHaveCount(0);
            await page.mouse.up();
            await expect(edge).toHaveCount(0);

            await producer.getByRole("button", { name: /^Transform Step normalize/ }).click();
            await input.click();
            await expect(edge).toBeAttached();
            await page.keyboard.press("Control+z");
            await expect(edge).toHaveCount(0);

            await output.hover();
            await page.mouse.down();
            await input.hover();
            await expect(preview).toBeAttached();
            await expect(edge).toHaveCount(0);
            await page.mouse.up();
            await expect(edge).toBeAttached();
        });
    });

    test("Proof history, recorded evidence and unavailable receipts support keyboard access", async ({
        page,
    }, testInfo) => {
        await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            const historyURL = `${fixture.url}/orgs/acme/projects/ops/proof-receipts`;
            const receipts = `${fixture.url}/orgs/acme/projects/ops/environments/prod/proof-receipts`;
            const activate = async (control) => {
                await tabTo(page, control);
                await page.keyboard.press("Enter");
            };
            await visit(page, `${historyURL}?environment_slug=empty`);
            await expect(page.getByRole("heading", { name: "No Proof receipts" })).toBeVisible();
            await assess(page, testInfo, "proof-empty-history");
            const environment = page.getByRole("combobox", { name: "Switch project runtime target", exact: true });
            const openProductionHistory = async (steps) => {
                const currentURL = page.url();
                await tabTo(page, environment);
                await page.keyboard.press("Space");
                for (let index = 0; index < steps; index++) await page.keyboard.press("ArrowUp");
                await page.keyboard.press("Enter");
                await expect(
                    environment.getByRole("option", { name: "Target: Production", selected: true }),
                ).toHaveCount(1);
                await expect(page).toHaveURL(currentURL);
                await expect(environment).toBeFocused();
                await activate(page.getByRole("button", { name: "Switch runtime target", exact: true }));
                await expect(page).toHaveURL(`${historyURL}?environment_slug=prod`);
            };
            await openProductionHistory(1);
            await assess(page, testInfo, "proof-history");
            const history = page.getByRole("list", { name: "Proof receipt history" });
            await activate(history.getByRole("link").first());
            await expect(page.getByRole("heading", { name: "Evaluated candidate" })).toBeVisible();
            await assess(page, testInfo, "proof-summary");
            await activate(page.getByRole("link", { name: "Claims and evidence", exact: true }));
            await expect(page.getByRole("heading", { name: "Promise clauses" })).toBeVisible();
            await activate(page.getByText("Evidence details", { exact: true }));
            await expect(page.getByText("fields_types", { exact: false })).toBeVisible();
            await assess(page, testInfo, "proof-claims");
            await activate(page.getByRole("link", { name: "Technical details", exact: true }));
            await expect(page.getByRole("region", { name: "Receipt provenance" })).toBeVisible();
            await assess(page, testInfo, "proof-technical");
            await activate(page.getByRole("link", { name: "Receipt history", exact: true }));
            await expect(history).toBeVisible();

            await activate(page.getByRole("link", { name: "Older receipts", exact: true }));
            const olderPage = `${historyURL}?cursor=older&direction=next&environment_slug=prod`;
            await expect(page).toHaveURL(olderPage);
            const staleReceipt = history.getByRole("link").first();
            await expect(staleReceipt).toHaveAttribute("href", /\/stale\?/);
            await assess(page, testInfo, "proof-history-older");
            await activate(staleReceipt);
            await expect(page.getByRole("heading", { name: "Evaluated candidate" })).toBeVisible();
            await activate(page.getByRole("link", { name: "Receipt history", exact: true }));
            await expect(page).toHaveURL(olderPage);
            await activate(page.getByRole("link", { name: "Newer receipts", exact: true }));
            await expect(page).toHaveURL(`${historyURL}?cursor=newer&direction=prev&environment_slug=prod`);
            await expect(history.getByRole("link").first()).toHaveAttribute("href", /\/metadata\?/);

            await visit(page, `${receipts}/operational-pruned`);
            const operational = page.getByRole("region", { name: "Operational evidence", exact: true });
            await expect(operational).toContainText("Supporting evidence missing or pruned");
            await expect(
                operational.getByText("Captured verdict", { exact: true }).locator("..").locator("dd"),
            ).toHaveText("passed");
            await expect(
                operational.getByText("Current supporting verdict", { exact: true }).locator("..").locator("dd"),
            ).toHaveText("unverified");
            await assess(page, testInfo, "proof-pruned-evidence");

            expect((await page.goto(`${historyURL}?environment_slug=denied`)).status()).toBe(403);
            const alert = page.getByRole("alert");
            await expect(alert).toContainText("You do not have permission to view receipts.");
            await expect(alert).toBeFocused();
            await assess(page, testInfo, "proof-history-denied");
            await openProductionHistory(2);
            await expect(history).toBeVisible();
            expect((await page.goto(`${receipts}/missing`)).status()).toBe(404);
            await expect(alert).toContainText("Proof receipt not found.");
            await expect(alert).toBeFocused();
            await assess(page, testInfo, "proof-receipt-missing");
            await activate(page.getByRole("link", { name: "Receipt history", exact: true }));
            await expect(history).toBeVisible();
        });
    });
});
