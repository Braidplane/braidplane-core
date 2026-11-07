const { test, expect, captureE2EEvidenceScreenshot } = require("./support/smoke-fixtures.cjs");
const { withSSRFixture } = require("./support/ssr-fixture.cjs");

// Each SSR fixture owns its session store; application cookies belong to a different server.
test.use({ storageState: { cookies: [], origins: [] } });

test("connects Slack, configures and tests an alert destination, then disconnects", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/notifications`)).ok()).toBe(true);
        await page.goto(`${fixture.url}/orgs/acme/alerts/connections`);
        const create = page.locator('form[action="/orgs/acme/alerts/connections"]');
        await create.locator('[name="name"]').fill("Ops Slack");
        await expect(create.locator("details")).not.toHaveAttribute("open", "");
        await create.getByText("Customize identifier", { exact: true }).click();
        await create.locator('[name="slug"]').fill("INVALID");
        await create.getByRole("button", { name: "Add connection" }).click();
        await expect(create.locator('[name="name"]')).toHaveValue("Ops Slack");
        await expect(create.locator('[name="slug"]')).toBeVisible();
        await expect(create.locator('[name="slug"]')).toHaveValue("INVALID");
        await create.locator('[name="slug"]').fill("");
        await expect(create.locator('[name="provider"]')).toHaveValue("slack");
        await create.getByRole("button", { name: "Add connection" }).click();
        await page.getByRole("button", { name: "Connect", exact: true }).click();
        await expect(page.getByRole("heading", { name: "Synthetic Slack channel picker" })).toBeVisible();
        expect(new URL(page.url()).origin).not.toBe(new URL(fixture.url).origin);
        await page.getByRole("button", { name: "Allow access" }).click();
        await expect(page.getByText("Synthetic workspace · #operations", { exact: true })).toBeVisible();
        await page.getByRole("link", { name: "Back to alert destinations" }).click();
        const destination = page.locator('form[action="/orgs/acme/alerts/destinations"]');
        await destination.locator('[name="name"]').fill("Ops alerts");
        await expect(destination.locator("details")).not.toHaveAttribute("open", "");
        await destination.locator('[name="type"]').selectOption("slack");
        await expect(destination.locator('[name="to"]')).toBeDisabled();
        await destination.locator('[name="connection_slug"]').selectOption("ops-slack");
        await destination.getByRole("button", { name: "Create destination" }).click();
        await expect(
            page
                .getByRole("paragraph")
                .filter({ hasText: /^Ops alerts$/ })
                .first(),
        ).toBeVisible();
        await page.getByRole("button", { name: "Test", exact: true }).click();
        await expect(page.getByText(/Alert destination test accepted for/)).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "slack-alert-destination-desktop.png", { fullPage: true });
        await page.getByRole("tab", { name: /^Policies/ }).click();
        const policy = page.locator('form[action="/orgs/acme/alerts/policies"]');
        await policy.locator('[name="name"]').fill("Completed runs");
        await expect(policy.locator("details")).not.toHaveAttribute("open", "");
        await policy.locator('[name="notification_type"]').selectOption("pipeline.run_succeeded");
        await expect(policy.locator('[name="notification_type"]')).toHaveValue("pipeline.run_succeeded");
        await policy.locator('[name="destination_slug"]').check();
        await captureE2EEvidenceScreenshot(page, testInfo, "successful-run-policy-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "successful-run-policy-compact.png", { fullPage: true });
        await page.setViewportSize({ width: 1440, height: 1000 });
        await policy.getByRole("button", { name: "Create policy", exact: true }).click();
        await expect(page.getByText("Completed runs", { exact: true }).first()).toBeVisible();
        await expect(
            page
                .getByRole("paragraph")
                .filter({ hasText: /^Pipeline run succeeded$/ })
                .first(),
        ).toBeVisible();
        await policy.locator('[name="name"]').fill("Contract failures");
        await policy.getByText("Customize identifier", { exact: true }).click();
        await policy.locator('[name="slug"]').fill("contract-failures");
        await policy.locator('[name="notification_type"]').selectOption("pipeline.contract_failure");
        await expect(policy.locator('[name="notification_type"]')).toHaveValue("pipeline.contract_failure");
        await policy.locator('[name="source_kind"]').selectOption("pipeline");
        await policy.locator('[name="source_action"]').selectOption("validate");
        await expect(policy.locator('[name="source_action"]')).toHaveValue("validate");
        await policy.locator('[name="destination_slug"]').check();
        await policy.getByRole("button", { name: "Create policy", exact: true }).click();
        await expect(page.getByText("Contract failures", { exact: true }).first()).toBeVisible();
        await expect(
            page
                .getByRole("paragraph")
                .filter({ hasText: /^Pipeline contract failure$/ })
                .first(),
        ).toBeVisible();
        await captureE2EEvidenceScreenshot(page, testInfo, "contract-failure-policy-desktop.png", { fullPage: true });
        for (const [kind, name] of [
            ["pipeline.run_failed", "Terminal failures"],
            ["pipeline.scheduled_run_delayed", "Delayed schedules"],
            ["pipeline.scheduled_run_missing", "Missing schedules"],
        ]) {
            await policy.locator('[name="name"]').fill(name);
            await policy.getByText("Customize identifier", { exact: true }).click();
            await policy.locator('[name="slug"]').fill(kind.replace(/[._]/g, "-"));
            await policy.locator('[name="notification_type"]').selectOption(kind);
            await expect(policy.locator('[name="notification_type"]')).toHaveValue(kind);
            await expect(policy.locator('option[value="pipeline.freshness_sla_breached"]')).toHaveCount(0);
            await policy.locator('[name="destination_slug"]').check();
            await policy.getByRole("button", { name: "Create policy", exact: true }).click();
            await expect(page.getByText(name, { exact: true }).first()).toBeVisible();
        }
        await captureE2EEvidenceScreenshot(page, testInfo, "pipeline-failure-schedule-policies-desktop.png", {
            fullPage: true,
        });
        await page.setViewportSize({ width: 390, height: 844 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "pipeline-failure-schedule-policies-compact.png", {
            fullPage: true,
        });
        await page.setViewportSize({ width: 1440, height: 1000 });
        await page.getByRole("tab", { name: /^Destinations/ }).click();
        await page.getByRole("link", { name: "Manage notification connections" }).click();
        await page.getByRole("button", { name: "Reconnect / change target", exact: true }).click();
        await expect(page.getByRole("heading", { name: "Synthetic Slack channel picker" })).toBeVisible();
        await page.getByRole("button", { name: "Allow access" }).click();
        await expect(page.getByText("Synthetic workspace · #operations", { exact: true })).toBeVisible();
        await page.setViewportSize({ width: 390, height: 844 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        expect((await page.locator('[name="name"]').boundingBox()).width).toBeGreaterThan(200);
        await page.getByText("Customize identifier", { exact: true }).click();
        expect((await page.locator('[name="slug"]').boundingBox()).width).toBeGreaterThan(200);
        await captureE2EEvidenceScreenshot(page, testInfo, "slack-notification-connection-compact.png", {
            fullPage: true,
        });
        await page.getByRole("button", { name: "Disconnect", exact: true }).click();
        await expect(page.getByText("Connection disconnected in Braidplane.")).toBeVisible();
        await expect(page.getByRole("button", { name: "Connect", exact: true })).toBeVisible();
    });
});

test("follows notification source, recorded routing and delivery evidence without sending", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/attention/admin`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/run/failed`)).ok()).toBe(true);
        const inbox = `${fixture.url}/orgs/acme/notifications?severity=warning`;
        await page.goto(inbox);
        const notice = page.locator("#notification-retry-notice");
        await expect(notice.getByText("Provider attempt 2", { exact: true })).toBeVisible();
        await expect(notice.getByText("Synthetic provider failure", { exact: true })).toBeVisible();
        await notice.getByRole("link", { name: "Run detail", exact: true }).click();
        await expect(page).toHaveURL(/runs\/run-inspection\?.*environment_slug=dev/);
        await page.getByRole("link", { name: "Back to notification", exact: true }).click();
        await expect(page).toHaveURL(/notifications\?severity=warning#notification-retry-notice$/);
        const recordedPolicy = notice.getByRole("link", { name: "Recorded policy: retry-policy", exact: true });
        await recordedPolicy.focus();
        await page.keyboard.press("Enter");
        await expect(page.getByText("Recorded retry policy", { exact: true }).first()).toBeVisible();
        await page.getByRole("button", { name: "Edit", exact: true }).click();
        const editPolicy = page.locator('form[action="/orgs/acme/alerts/policies/retry-policy/update"]');
        await expect(editPolicy.locator('[name="severity"]')).toHaveValue("warning");
        await editPolicy.getByRole("checkbox", { name: "Enabled", exact: true }).uncheck();
        expect(await editPolicy.evaluate((form) => new FormData(form).get("severity"))).toBe("warning");
        await expect(editPolicy.locator("[data-alert-policy-review]")).toContainText("with severity Warning");
        await captureE2EEvidenceScreenshot(page, testInfo, "attention-policy-edit-desktop.png", { fullPage: true });
        await editPolicy.getByRole("checkbox", { name: "Enabled", exact: true }).check();
        await page.getByRole("link", { name: "Destination: ops-email", exact: true }).click();
        await expect(page.getByText("synthetic@example.com", { exact: true })).toBeVisible();
        await page.getByRole("link", { name: "Back to notification", exact: true }).last().click();
        await notice.getByRole("link", { name: "Configure alerts", exact: true }).click();
        const policy = page.locator('form[action="/orgs/acme/alerts/policies"]');
        await expect(policy.locator('[name="project_slug"]')).toHaveValue("ops");
        await expect(policy.locator('[name="environment_slug"]')).toHaveValue("dev");
        await expect(policy.locator('[name="notification_type"]')).toHaveValue("pipeline.retry_exhausted");
        await policy.locator('[name="destination_slug"]').check();
        await expect(policy.locator("[data-alert-policy-review]")).toContainText("Any lifecycle");
        await expect(policy.locator("[data-alert-policy-review]")).toContainText("Operations email (Enabled)");
        await captureE2EEvidenceScreenshot(page, testInfo, "attention-policy-desktop.png", { fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "attention-policy-compact.png", { fullPage: true });
        await page.getByRole("link", { name: "Back to notification", exact: true }).last().click();
        expect((await page.request.post(`${fixture.url}/_test/attention/restricted`)).ok()).toBe(true);
        await page.reload();
        await expect(notice.getByRole("link", { name: "Configure alerts", exact: true })).toHaveCount(0);
        await expect(notice.getByRole("link", { name: "Run detail", exact: true })).toHaveCount(0);
        await expect(notice.getByRole("link", { name: /Recorded (policy|destination):/ })).toHaveCount(0);
        await expect(notice.getByText("Synthetic provider failure", { exact: true })).toBeVisible();
        await expect(page.getByText("synthetic@example.com", { exact: true })).toHaveCount(0);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await captureE2EEvidenceScreenshot(page, testInfo, "attention-restricted-compact.png", { fullPage: true });
        await page.getByRole("link", { name: "Resolved", exact: true }).click();
        await expect(page.getByRole("heading", { name: "No notifications match these filters" })).toBeVisible();
        await page.getByRole("link", { name: "Clear filters", exact: true }).click();
        await expect(page.getByRole("heading", { name: "No resolved notifications" })).toBeVisible();
        expect(await (await page.request.get(`${fixture.url}/_test/attention-stats`)).json()).toEqual({ writes: 0 });
    });
});
