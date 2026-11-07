const {
    test,
    expect,
    e2eOrgSlug: org,
    e2eProjectSlug: project,
    e2eEnvironmentSlug: env,
    e2eSQLDataSourceSlug: source,
} = require("../e2e/support/smoke-fixtures.cjs");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");

async function capture(page, info, name) {
    await page.screenshot({ path: info.outputPath(`${name}.png`), fullPage: true, animations: "disabled" });
}

test("dashboard breakpoints, disclosures and explicit no-project selection", async ({ page }, info) => {
    await page.goto(`/dashboard?project_slug=${project}&environment_slug=${env}`);
    const overflow = [];
    for (const width of [1280, 1440, 390, 320]) {
        await page.setViewportSize({ width, height: 800 });
        await expect(page.getByRole("region", { name: "Dashboard scope" })).toBeVisible();
        const bounds = await page.evaluate(() => ({
            width: innerWidth,
            documentWidth: document.documentElement.scrollWidth,
            offenders: Array.from(document.querySelectorAll("body *"))
                .filter(
                    (e) =>
                        e.getBoundingClientRect().right > innerWidth + 1 && getComputedStyle(e).position !== "absolute",
                )
                .slice(0, 8)
                .map((e) => ({
                    tag: e.tagName,
                    cls: e.className,
                    text: e.textContent.trim().slice(0, 65),
                    right: e.getBoundingClientRect().right,
                })),
        }));
        if (bounds.documentWidth > bounds.width) overflow.push(bounds);
        const inventory = page.locator("summary").filter({ hasText: "Inventory and setup details" });
        await inventory.press("Enter");
        await expect(inventory.locator("..")).toHaveAttribute("open", "");
        await inventory.press("Enter");
        await capture(page, info, `dashboard-${width}`);
    }
    await page
        .getByRole("combobox", { name: "Choose dashboard project scope", exact: true })
        .selectOption({ label: "No project selected" });
    await expect(
        page.getByRole("combobox", { name: "Dashboard runtime target unavailable", exact: true }),
    ).toBeDisabled();
    await page.reload();
    await expect(page.getByRole("combobox", { name: "Choose dashboard project scope", exact: true })).toHaveValue("");
    await expect(
        page.getByRole("combobox", { name: "Dashboard runtime target unavailable", exact: true }),
    ).toBeDisabled();
    await capture(page, info, "no-project");
    await info.attach("overflow", { body: JSON.stringify(overflow, null, 2), contentType: "application/json" });
    expect(overflow).toEqual([]);
});

test.describe("canonical navigation without JavaScript", () => {
    test.use({ javaScriptEnabled: false });
    test("data-source, query, project and environment round trips", async ({ page }, info) => {
        for (const [name, url, opener, back] of [
            ["source", `/orgs/${org}/data-sources?selected=${source}&environment_slug=${env}`, null, "Back to list"],
            ["query", `/orgs/${org}/queries?ds=${source}`, '[id^="query-opener-"]', "Back to list"],
            ["project", `/organizations/${org}/projects`, `#project-opener-${project}`, "Back to project list"],
            ["environment", `/organizations/${org}/environments`, 'a[id^="environment-"]', "Back to environments"],
        ]) {
            await page.goto(url);
            if (opener) await page.locator(opener).first().click();
            const preview = page.getByRole("complementary", { name: "Quick look", exact: true });
            await expect(preview.getByRole("link", { name: "Open full page", exact: true })).toBeVisible();
            const selected = page.url();
            await preview.getByRole("link", { name: "Open full page", exact: true }).click();
            await expect(page.getByRole("link", { name: back, exact: true })).toBeVisible();
            await page.getByRole("link", { name: back, exact: true }).click();
            await expect(page).toHaveURL((current) => {
                const previous = new URL(selected);
                return (
                    current.pathname === previous.pathname &&
                    JSON.stringify([...current.searchParams].sort()) ===
                        JSON.stringify([...previous.searchParams].sort())
                );
            });
            await capture(page, info, `no-js-${name}`);
        }
    });
});

test("Proof empty, restricted, missing, mismatch and configuration states", async ({ page }, info) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        for (const [name, url, text, status] of [
            ["empty", "/orgs/acme/projects/ops/proof-receipts?environment_slug=empty", "No Proof receipts", 200],
            [
                "restricted",
                "/orgs/acme/projects/ops/proof-receipts?environment_slug=denied",
                "You do not have permission to view receipts.",
                403,
            ],
            [
                "missing",
                "/orgs/acme/projects/ops/environments/prod/proof-receipts/missing-record",
                "Proof receipt not found.",
                404,
            ],
            ["mismatch", "/orgs/acme/projects/ops/environments/prod/proof-receipts/mismatch-partial", /mismatch/i, 200],
            ["configuration", "/orgs/acme/data-sources/github/proof", "Proof configuration", 200],
        ]) {
            const response = await page.goto(fixture.url + url);
            expect(response.status()).toBe(status);
            await expect(page.locator("body")).toContainText(text);
            if (name === "restricted" || name === "missing")
                await expect(page.getByRole("heading", { name: "No Proof receipts", exact: true })).toHaveCount(0);
            for (const width of [1440, 390, 320]) {
                await page.setViewportSize({ width, height: 800 });
                expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
                await capture(page, info, `proof-${name}-${width}`);
            }
        }
    });
});

test("explicit full-page Back retains a deliberately scrolled source catalog", async ({ page }, info) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(`/orgs/${org}/data-sources?environment_slug=${env}`);
    const card = page.locator("[data-ds-card]").last();
    const detail = card.locator("a").first();
    await detail.scrollIntoViewIfNeeded();
    const before = await page.evaluate(() => scrollY);
    expect(before).toBeGreaterThan(100);
    await detail.click();
    await page.getByRole("link", { name: "Back to list", exact: true }).click();
    await expect(
        page
            .getByRole("complementary", { name: "Quick look" })
            .getByRole("link", { name: "Open full page", exact: true }),
    ).toBeVisible();
    const after = await page.evaluate(() => scrollY);
    info.annotations.push({ type: "scroll", description: JSON.stringify({ before, after }) });
    await capture(page, info, "explicit-back-scroll");
    expect(Math.abs(after - before)).toBeLessThan(100);
});

test("moderation fixture has three repeatable read-only cases", async ({ page }, info) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        for (const [state, outcome] of [
            ["healthy", "succeeded"],
            ["failed", "failed"],
            ["unconfigured", "no runs"],
        ]) {
            expect((await page.request.post(`${fixture.url}/_test/moderation/${state}`)).status()).toBe(204);
            await page.goto(
                `${fixture.url}/orgs/acme/projects/ops/pipelines?environment_slug=dev&pipeline_slug=daily-sync`,
            );
            const preview = page.getByRole("complementary", { name: "Quick look", exact: true });
            await expect(preview).toContainText(outcome);
            await expect(preview).toContainText(state === "unconfigured" ? "not deployed" : "dev:aaaaaaaaaaaa");
            await expect(preview.locator("form,textarea,input,button")).toHaveCount(0);
            await capture(page, info, `moderation-${state}`);
            await preview.getByRole("link", { name: "Open full page", exact: true }).click();
            await expect(page.getByRole("button", { name: /^(Run|Deploy|Revalidate)/ })).toHaveCount(0);
            if (state === "unconfigured")
                await expect(
                    page.getByRole("link", { name: "Review deployment and bindings", exact: true }),
                ).toBeVisible();
            else {
                await page
                    .getByRole("navigation", { name: "Pipeline sections" })
                    .getByRole("link", { name: "Runs", exact: true })
                    .click();
                await expect(page.locator("[data-pipeline-run-preview]")).toContainText(outcome);
            }
        }
    });
});

test("populated schedule quick look, configuration, history and Back", async ({ page }, info) => {
    await page.goto(`/orgs/${org}/projects/${project}/schedules?environment_slug=${env}`);
    await page.locator('a[id^="trigger-opener-"]').first().press("Enter");
    const preview = page.getByRole("complementary", { name: "Quick look", exact: true });
    await expect(preview.getByRole("link", { name: "Open full page", exact: true })).toBeVisible();
    await expect(page).toHaveURL(/trigger_slug=/);
    const selected = page.url();
    await preview.getByRole("link", { name: "Open full page", exact: true }).press("Enter");
    await page.getByRole("link", { name: "History", exact: true }).press("Enter");
    expect(new URL(page.url()).searchParams.get("section")).toBe("history");
    for (const width of [1280, 390, 320]) {
        await page.setViewportSize({ width, height: 800 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        await capture(page, info, `schedule-history-${width}`);
    }
    await page.getByRole("link", { name: "Back to schedules", exact: true }).press("Enter");
    await expect(page).toHaveURL((current) => {
        const previous = new URL(selected);
        return (
            current.pathname === previous.pathname &&
            JSON.stringify([...current.searchParams].sort()) === JSON.stringify([...previous.searchParams].sort())
        );
    });
});

test("newly seeded empty workspace shows setup without invented success", async ({ page }, info) => {
    test.skip(
        info.project.name !== "chromium",
        "Repository auth lifecycle policy is single-engine because seeded sign-in is throttled.",
    );
    const support = require("../e2e/support/smoke-fixtures.cjs");
    await support.signInWithCredentials(
        page,
        support.e2eEmail,
        support.e2ePassword,
        /\/dashboard/,
        `acceptance-empty-${info.project.name}`,
    );
    await support.unsafeRequest(page, "POST", `/organizations/${process.env.E2E_EMPTY_ORG_SLUG}/switch`, { form: {} });
    await page.goto("/dashboard");
    await expect(
        page.getByRole("combobox", { name: "Dashboard runtime target unavailable", exact: true }),
    ).toBeDisabled();
    await expect(page.getByRole("region", { name: "Recent activity", exact: true })).toContainText("Choose a project");
    await capture(page, info, "empty-workspace");
});
