const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("Proof claims distinguish repeated evidence disclosures and empty records by keyboard", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const receipts = `${fixture.url}/orgs/acme/projects/ops/environments/prod/proof-receipts`;
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        await visit(page, `${receipts}/mixed`);
        await activate(page.getByRole("link", { name: "Review claims and evidence", exact: true }));
        const claims = page.getByRole("region", { name: "Promise clauses", exact: true });
        const metadata = claims.getByRole("article", { name: "Metadata compatibility", exact: true });
        const stale = claims.getByRole("article", { name: "schema evidence remains current", exact: true });
        await expect(claims.getByRole("article")).toHaveCount(2);
        await expect(metadata.getByRole("heading", { name: "Metadata compatibility", exact: true })).toBeVisible();
        await expect(metadata).toContainText("Static (metadata only)");
        await expect(stale).toContainText("Required evidence");
        await expect(stale).toContainText("Actual evidence");
        const details = (article) => article.getByText("Evidence details", { exact: true });
        const first = details(metadata);
        const second = details(stale);
        await expect(first).toHaveAccessibleName("Evidence details Metadata compatibility");
        await expect(second).toHaveAccessibleName("Evidence details schema evidence remains current");
        await expect(first).toHaveCSS("min-height", "24px");
        await expect(second).toHaveCSS("min-height", "24px");
        await activate(first);
        await expect(first).toBeFocused();
        await expect(metadata).toContainText("No evidence references were recorded for this clause.");
        await expect(second.locator("..")).not.toHaveAttribute("open", "");
        await page.keyboard.press("Tab");
        await expect(second).toBeFocused();
        await page.keyboard.press("Space");
        await expect(stale.getByRole("list", { name: "Sanitized evidence references" })).toContainText("sha256:");
        await expect(stale).toContainText("Schema evidence expired");
        await expect(stale).toContainText("Refresh introspection");
        await assess(page, testInfo, "proof-mixed-claims");

        await visit(page, `${receipts}/mismatch-partial`);
        await activate(page.getByRole("link", { name: "Technical details", exact: true }));
        const lifecycle = page.getByRole("region", { name: "Deployment and run evidence", exact: true });
        await expect(lifecycle).toContainText("version mismatch");
        const recordedVersion = (label) => lifecycle.getByRole("term").filter({ hasText: label }).locator("..");
        await expect(recordedVersion("Candidate version")).toContainText(`sha256:${"a".repeat(64)}`);
        await expect(recordedVersion("Selected version")).toContainText(`sha256:${"d".repeat(64)}`);
        await expect(lifecycle.getByText("orders:old", { exact: true })).toBeVisible();
        const deployment = lifecycle.getByRole("link", { name: "View current deployment for orders", exact: true });
        await tabTo(page, deployment);
        await expect(deployment).toHaveAttribute("href", /\/pipelines\/orders\/deployment\?environment_slug=prod$/);
        await assess(page, testInfo, "proof-version-mismatch");

        for (const id of ["no-evidence", "superseded"]) {
            await visit(page, `${receipts}/${id}`);
            if (id === "superseded") {
                await expect(page.getByText("A later immutable receipt supersedes this result.")).toBeVisible();
            }
            await activate(page.getByRole("link", { name: "Claims and evidence", exact: true }));
            await expect(page).toHaveURL(
                (url) =>
                    url.pathname === new URL(`${receipts}/${id}`).pathname &&
                    url.searchParams.get("section") === "claims",
            );
            await expect(claims.getByRole("status")).toHaveText("No promise clauses were recorded for this receipt.");
            await expect(claims.getByRole("article")).toHaveCount(0);
            await assess(page, testInfo, `proof-${id}-claims`);
        }
    });
});

test("Proof evidence distinguishes live observations from current operational support", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const receipts = `${fixture.url}/orgs/acme/projects/ops/environments/prod/proof-receipts`;
        const termValue = (region, name) => region.getByText(name, { exact: true }).locator("..").locator("dd");
        const activate = async (control) => {
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        await visit(page, `${receipts}/live`);
        await expect(page.getByText("1 Live read only", { exact: true })).toBeVisible();
        await activate(page.getByRole("link", { name: "Review claims and evidence", exact: true }));
        const live = page.getByRole("article", { name: "Read-only connection assessment", exact: true });
        await expect(termValue(live, "Evidence")).toHaveText("Live read only");
        await expect(termValue(live, "Observed")).toHaveText("2026-09-02 00:00:00 UTC");
        const details = live.getByText("Evidence details", { exact: true });
        await expect(details).toHaveAccessibleName("Evidence details Read-only connection assessment");
        await activate(details);
        await expect(details).toBeFocused();
        await expect(live.getByRole("list", { name: "Sanitized evidence references" })).toContainText(
            "sha256:read-only-observation",
        );
        await assess(page, testInfo, "proof-live-evidence");

        for (const [state, support, verdict] of [
            ["available", "Supporting evidence available", "passed"],
            ["expired", "Supporting evidence expired", "stale"],
            ["changed", "Supporting evidence changed or unsupported", "inconclusive"],
        ]) {
            await visit(page, `${receipts}/operational-${state}`);
            const evidence = page.getByRole("region", { name: "Operational evidence", exact: true });
            await expect(termValue(evidence, "Captured verdict")).toHaveText("passed");
            await expect(termValue(evidence, "Current supporting verdict")).toHaveText(verdict);
            await expect(termValue(evidence, "Supporting evidence")).toHaveText(support);
            await expect(termValue(evidence, "Captured attempt")).toHaveText("recorded-run / attempt 2");
            await expect(termValue(evidence, "Evidence expiry")).toHaveText(/UTC$/);
            const attempt = evidence.getByRole("link", { name: "Inspect recorded attempt", exact: true });
            await tabTo(page, attempt);
            await expect(attempt).toBeInViewport({ ratio: 1 });
            await expect(attempt).toHaveAttribute("href", /\/runs\/recorded-run\?environment_slug=prod$/);
            await assess(page, testInfo, `proof-operational-${state}`);
        }
    });
});

test("Proof history can recover from an empty page or invalid cursor by keyboard", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const history = `${fixture.url}/orgs/acme/projects/ops/proof-receipts`;
        const latest = `${history}?environment_slug=prod`;
        const older = page.getByRole("link", { name: "Older receipts", exact: true });
        await visit(page, latest);
        await tabTo(page, older);
        expect((await page.request.post(`${fixture.url}/_test/proof/history/prune-older`)).ok()).toBe(true);
        await page.keyboard.press("Enter");

        const recover = page.getByRole("link", { name: "Back to newest receipts", exact: true });
        const restart = async () => {
            await page.keyboard.press("Tab");
            await expect(recover).toBeFocused();
            await expect(recover).toBeInViewport({ ratio: 1 });
            await page.keyboard.press("Enter");
            await expect(page).toHaveURL(`${latest}#proof-history-context`);
            await expect(page.locator("#proof-history-context")).toBeFocused();
            await page.keyboard.press("Tab");
            await expect(page.getByRole("link", { name: /^Change #42/ })).toBeFocused();
        };

        const empty = page.getByRole("heading", { name: "No Proof receipts on this page", exact: true });
        await expect(empty).toBeFocused();
        await expect(page.getByRole("link", { name: "View project Git setup", exact: true })).toHaveCount(0);
        await assess(page, testInfo, "proof-history-empty-older-page");
        await restart();

        const invalid = await page.goto(`${history}?cursor=invalid&direction=next&environment_slug=prod`);
        expect(invalid.status()).toBe(400);
        const error = page.getByRole("alert").filter({ hasText: "Invalid cursor." });
        await expect(error).toBeFocused();
        await assess(page, testInfo, "proof-history-invalid-cursor");
        await restart();
    });
});

test("Proof unavailable receipt returns to the same history page by keyboard", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const historyURL = `${fixture.url}/orgs/acme/projects/ops/proof-receipts`;
        await visit(page, `${historyURL}?environment_slug=prod`);
        await tabTo(page, page.getByRole("link", { name: "Older receipts", exact: true }));
        await page.keyboard.press("Enter");
        const older = `${historyURL}?cursor=older&direction=next&environment_slug=prod`;
        await expect(page).toHaveURL(older);
        await expect(page.locator("#proof-history-context")).toBeFocused();
        const denied = page
            .getByRole("list", { name: "Proof receipt history" })
            .getByRole("link", { name: /Change #41/ });
        await tabTo(page, denied);
        const response = page.waitForResponse((result) => new URL(result.url()).pathname.endsWith("/denied"));
        await page.keyboard.press("Enter");
        expect((await response).status()).toBe(403);
        const alert = page.getByRole("alert");
        await expect(alert).toHaveText("You do not have permission to view this receipt.");
        await expect(alert).toBeFocused();
        await expect(page.getByRole("navigation", { name: "Proof receipt sections" })).toHaveCount(0);
        const back = page.getByRole("link", { name: "Receipt history", exact: true });
        await tabTo(page, back);
        await assess(page, testInfo, "proof-receipt-denied");
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(older);
        await expect(page.locator("#proof-history-context")).toBeFocused();
        await expect(denied).toBeVisible();
    });
});
