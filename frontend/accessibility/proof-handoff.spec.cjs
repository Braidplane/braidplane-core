const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

async function activate(page, control) {
    await tabTo(page, control);
    await page.keyboard.press("Enter");
}

test("Proof recorded-attempt navigation stays usable after retention removes the run", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const receipt = `${fixture.url}/orgs/acme/projects/ops/environments/prod/proof-receipts/operational-available`;
        await visit(page, receipt);
        const attempt = page.getByRole("link", { name: "Inspect recorded attempt", exact: true });
        await activate(page, attempt);
        await expect(page).toHaveURL(/runs\/recorded-run\?environment_slug=prod$/);
        await expect(page.locator("#run-recorded-status")).toHaveText("succeeded");
        await expect(page.getByText("attempt 2", { exact: true })).toBeVisible();
        await activate(page, page.getByRole("link", { name: "operational-available", exact: true }));
        await expect(page).toHaveURL(receipt);

        expect((await page.request.post(`${fixture.url}/_test/proof/handoff?missing_run=true`)).ok()).toBe(true);
        await activate(page, attempt);
        const alert = page.getByRole("alert").filter({ hasText: "The recorded run is no longer retained." });
        await expect(alert).toHaveCount(1);
        await expect(alert).toBeFocused();
        await expect(alert).toBeInViewport({ ratio: 1 });
        await expect(page.getByRole("link", { name: "Back to runs", exact: true })).toBeVisible();
        await assess(page, testInfo, "proof-linked-run-pruned");
        await page.goBack();
        await expect(page).toHaveURL(receipt);
        await tabTo(page, attempt);
    });
});

test("Proof names the current deployment and keeps past evidence distinct", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const receipt = `${fixture.url}/orgs/acme/projects/ops/environments/prod/proof-receipts/mismatch-partial`;
        const technical = `${receipt}?section=technical`;
        await visit(page, technical);
        const lifecycle = page.getByRole("region", { name: "Deployment and run evidence" });
        const current = lifecycle.getByRole("link", { name: "View current deployment for orders", exact: true });
        await expect(lifecycle).toContainText("orders:old");
        await expect(lifecycle).toContainText("version mismatch");
        await activate(page, current);
        await expect(page).toHaveURL(/pipelines\/orders\/deployment\?environment_slug=prod$/);
        await expect(page.getByRole("heading", { name: "Pointer metadata" })).toBeVisible();
        await expect(page.getByText(`sha256:${"d".repeat(64)}`, { exact: true })).toBeVisible();
        await activate(page, page.getByRole("link", { name: "Proof receipt", exact: true }));
        await expect(page).toHaveURL(receipt);

        await visit(page, technical);
        expect((await page.request.post(`${fixture.url}/_test/proof/handoff?missing_deployment=true`)).ok()).toBe(true);
        await activate(page, current);
        const alert = page.getByRole("alert").filter({ hasText: "The current deployment is unavailable." });
        await expect(alert).toBeFocused();
        await expect(alert).toBeInViewport({ ratio: 1 });
        await expect(page.getByRole("link", { name: "Open pipeline", exact: true })).toBeVisible();
        await assess(page, testInfo, "proof-linked-current-deployment-unavailable");
        await page.goBack();
        await expect(page).toHaveURL(technical);
        await tabTo(page, current);
    });
});
