const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("connected run updates pause after the inspected run has finished", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/completion/waiting`)).ok()).toBe(true);
        let polls = 0;
        await page.route("**/runs/run-inspection?**", (route) => {
            if (route.request().headers()["hx-request"] === "true") polls++;
            return route.continue();
        });
        await visit(
            page,
            `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=connected`,
        );
        const connected = page.locator("#connected-runs");
        await expect(connected).toContainText("Retry waiting");
        const toggle = page.locator("[data-run-updates-toggle]");
        await expect(toggle).toBeVisible();
        await tabTo(page, toggle);
        await page.keyboard.press("Space");
        await expect(toggle).toHaveAccessibleName("Resume live updates");
        const pausedPolls = polls;
        expect((await page.request.post(`${fixture.url}/_test/completion/success`)).ok()).toBe(true);
        // Outlast the connected-runs polling interval.
        await page.waitForTimeout(5500);
        expect(polls).toBe(pausedPolls);
        await expect(connected).toContainText("Retry waiting");
        await expect(toggle).toBeFocused();
        await assess(page, testInfo, "connected-run-updates-paused");
        await page.keyboard.press("Space");
        await expect(connected).not.toContainText("Retry waiting");
        await expect(toggle).toHaveAttribute("aria-disabled", "true");
        await expect(toggle).toBeFocused();
        await expect(page.locator("[data-run-updates-state]")).toHaveText("Automatic updates finished.");
        await assess(page, testInfo, "connected-run-updates-resumed-terminal");
    });
});

test("live run updates pause by keyboard and discard a pending response", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        let release;
        const released = new Promise((resolve) => {
            release = resolve;
        });
        let started;
        const pending = new Promise((resolve) => {
            started = resolve;
        });
        let completed;
        const finished = new Promise((resolve) => {
            completed = resolve;
        });
        let polls = 0;
        try {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            expect((await page.request.post(`${fixture.url}/_test/run/running?phase=fetch`)).ok()).toBe(true);
            await page.route(/progress_poll=1/, async (route) => {
                polls++;
                if (polls === 1) {
                    started();
                    await released;
                }
                const response = await route.fetch();
                await route.fulfill({ response });
                completed();
            });
            await visit(
                page,
                `${fixture.url}/orgs/acme/projects/ops/runs/run-inspection?environment_slug=dev&section=steps`,
            );
            const progress = page.getByRole("region", { name: "Live execution progress" });
            await expect(progress).toContainText("Running step fetch");
            const retainedContent = await page.locator("#run-live-content").textContent();
            await pending;
            const toggle = page.locator("[data-run-updates-toggle]");
            await tabTo(page, toggle);
            await page.keyboard.press("Space");
            await expect(toggle).toHaveAccessibleName("Resume live updates");
            await expect(toggle).toHaveAttribute("aria-pressed", "true");
            expect((await page.request.post(`${fixture.url}/_test/run/running?phase=normalize`)).ok()).toBe(true);
            release();
            await finished;
            // Outlast the polling interval so a paused request would be observed.
            await page.waitForTimeout(2500);
            expect(polls).toBe(1);
            await expect(progress).toContainText("Running step fetch");
            expect(await page.locator("#run-live-content").textContent()).toBe(retainedContent);
            await expect(toggle).toBeFocused();
            await assess(page, testInfo, "run-live-updates-paused");

            await page.keyboard.press("Space");
            await expect(toggle).toHaveAccessibleName("Pause live updates");
            await expect(progress).toContainText("Running step normalize");
            await expect(toggle).toBeFocused();
            expect((await page.request.post(`${fixture.url}/_test/run/succeeded`)).ok()).toBe(true);
            await expect(page.locator("#run-recorded-status")).toHaveText("succeeded");
            await expect(toggle).toHaveAttribute("aria-disabled", "true");
            await expect(toggle).toBeFocused();
            await expect(page.locator("[data-run-updates-state]")).toHaveText("Automatic updates finished.");
            await assess(page, testInfo, "run-live-updates-resumed-terminal");
        } finally {
            release();
            await page.unrouteAll({ behavior: "wait" });
        }
    });
});
