const { test, expect, captureE2EEvidenceScreenshot } = require("./support/smoke-fixtures.cjs");
const { startSSRFixture, stopSSRFixture } = require("./support/ssr-fixture.cjs");

// Each SSR fixture owns its session store; application cookies belong to a different server.
test.use({ storageState: { cookies: [], origins: [] } });

let fixture;
test.beforeAll(async () => {
    fixture = await startSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN);
});
test.afterAll(async () => stopSSRFixture(fixture));

test("discovers a Proof receipt and inspects its recorded claims", async ({ page }, testInfo) => {
    expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
    await page.goto(`${fixture.url}/organizations/acme/projects`);
    await page.getByRole("link", { name: "Proof", exact: true }).filter({ visible: true }).click();
    await expect(page).toHaveURL(/\/projects\/ops\/proof-receipts\?environment_slug=prod$/);
    for (const [environment, width] of [
        ["empty", 1280],
        ["prod", 390],
    ]) {
        await page.setViewportSize({ width, height: 844 });
        const switcher = page.getByRole("combobox", { name: "Switch project runtime target", exact: true });
        await switcher.selectOption({ label: environment === "empty" ? "Target: Empty" : "Target: Production" });
        await expect(page).toHaveURL(new RegExp(`/proof-receipts\\?environment_slug=${environment}$`));
        await expect(
            page.getByRole("heading", { name: environment === "empty" ? "No Proof receipts" : "Proof", exact: true }),
        ).toBeVisible();
        for (const name of ["Primary", "Mobile primary"]) {
            const navigation = page.getByRole("navigation", { name, exact: true, includeHidden: true });
            for (const destination of ["Pipelines", "Proof", "Schedules", "Runs"]) {
                await expect(
                    navigation.getByRole("link", { name: destination, exact: true, includeHidden: true }),
                ).toHaveAttribute("href", new RegExp(`environment_slug=${environment}$`));
            }
        }
        if (width === 390) await page.getByRole("button", { name: "Open navigation menu", exact: true }).press("Enter");
        const navigation = page.getByRole("navigation", {
            name: width === 390 ? "Mobile primary" : "Primary",
            exact: true,
        });
        await expect(navigation).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, `runtime-target-${width}.png`, { animations: "disabled" });
        await navigation.getByRole("link", { name: "Proof", exact: true }).click();
        await expect(page).toHaveURL(new RegExp(`/proof-receipts\\?environment_slug=${environment}$`));
    }
    await page.setViewportSize({ width: 1280, height: 800 });
    const history = page.getByRole("list", { name: "Proof receipt history" });
    await history.getByRole("link").first().click();
    await expect(page.getByRole("heading", { name: "Evaluated candidate" })).toBeVisible();
    // The fixture records successful static evidence, without provider/deployment authority.
    await page.getByRole("link", { name: "Claims and evidence", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Promise clauses" })).toBeVisible();
    await page.getByText("Evidence details", { exact: true }).click();
    await expect(page.getByText("fields_types", { exact: false })).toBeVisible();
    await page.getByRole("link", { name: "Technical details", exact: true }).click();
    await captureE2EEvidenceScreenshot(page, testInfo, "alpha-proof.png", { fullPage: true });
    await page.getByRole("link", { name: "Receipt history", exact: true }).click();
    await expect(history).toBeVisible();
});

test("keeps a captured operational verdict distinct from pruned supporting evidence", async ({ page }, testInfo) => {
    expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
    await page.goto(`${fixture.url}/orgs/acme/projects/ops/environments/prod/proof-receipts/operational-pruned`);
    const evidence = page.getByRole("region", { name: "Operational evidence", exact: true });
    await expect(evidence).toContainText("Supporting evidence missing or pruned");
    await expect(evidence.getByText("Captured verdict", { exact: true }).locator("..").locator("dd")).toHaveText(
        "passed",
    );
    await expect(
        evidence.getByText("Current supporting verdict", { exact: true }).locator("..").locator("dd"),
    ).toHaveText("unverified");
    await expect(evidence).toContainText("sha256:recorded-plan");
    await expect(evidence.getByRole("link", { name: "Inspect recorded attempt" })).toHaveAttribute(
        "href",
        /recorded-run\?environment_slug=prod$/,
    );
    await captureE2EEvidenceScreenshot(page, testInfo, "operational-proof-desktop.png", { fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await evidence.scrollIntoViewIfNeeded();
    expect(await evidence.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    await captureE2EEvidenceScreenshot(page, testInfo, "operational-proof-compact.png", { fullPage: true });
});

test("Reconnect navigates to GitHub under same-origin form CSP on desktop", async ({ page }) => {
    const destination = "https://github.com/apps/fixture/installations/new?state=fixture";
    await page.route(destination, (route) => route.fulfill({ contentType: "text/html", body: "GitHub installation" }));
    expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
    const response = await page.goto(`${fixture.url}/organizations/connected/github`);
    expect(response.headers()["content-security-policy"]).toBe("form-action 'self'");
    await page.getByRole("button", { name: "Reconnect", exact: true }).click();
    await expect(page).toHaveURL(destination);
    expect(await page.context().cookies(`${fixture.url}/github/callback`)).toEqual(
        expect.arrayContaining([
            expect.objectContaining({
                name: "github_install_organization",
                value: "connected",
                httpOnly: true,
                sameSite: "Lax",
            }),
        ]),
    );
});
