const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("GitLab authorization start and declined consent support keyboard retry", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/gitlab/refuse-actions`)).ok()).toBe(true);
        const connections = `${fixture.url}/organizations/acme/connections`;
        let consentOutcome = "denied";
        await page.route("https://gitlab.com/oauth/authorize?state=fixture", (route) =>
            route.fulfill({ status: 302, headers: { Location: `${connections}?provider_oauth=${consentOutcome}` } }),
        );
        await visit(page, connections);

        const sections = page.getByRole("navigation", { name: "Organization detail sections" });
        const links = sections.getByRole("link");
        await expect(links).toHaveCount(6);
        await expect(links.filter({ hasText: "Git integrations" })).toHaveAttribute("aria-current", "page");
        await expect(sections.locator('[aria-current="page"]')).toHaveCount(1);
        await tabTo(page, links.first());
        for (const link of await links.all()) {
            await expect(link).toBeFocused();
            await expect(link).toBeInViewport({ ratio: 1 });
            await page.keyboard.press("Tab");
        }

        const includeWrite = page.getByRole("checkbox", { name: /allow git writes/i });
        await expect(includeWrite).toHaveAccessibleDescription(/repository-write scopes/);
        await tabTo(page, includeWrite);
        await page.keyboard.press("Space");
        const moreTime = page.getByRole("checkbox", { name: /^Allow 100 minutes/ });
        await page.keyboard.press("Tab");
        await expect(moreTime).toBeFocused();
        await expect(moreTime).not.toBeChecked();
        await page.keyboard.press("Space");
        const connect = page.getByRole("button", { name: "Connect or reconnect GitLab" });
        await page.keyboard.press("Tab");
        await expect(connect).toBeFocused();
        await page.keyboard.press("Enter");
        const startError = page.getByRole("alert").filter({ hasText: /could not start GitLab authorization/i });
        await expect(startError).toBeFocused();
        await expect(startError).toBeInViewport({ ratio: 1 });
        await expect(includeWrite).toBeChecked();
        await expect(moreTime).toBeChecked();
        await expect(page.getByText("Connection status: active")).toBeVisible();
        await assess(page, testInfo, "gitlab-start-refused");

        await tabTo(page, connect);
        const retry = page.waitForRequest((request) => request.url().endsWith("/gitlab/connect"));
        await page.keyboard.press("Enter");
        const fields = new URLSearchParams((await retry).postData());
        expect(fields.get("include_write_scopes")).toBe("on");
        expect(fields.get("more_time")).toBe("true");
        const declined = page.getByRole("alert").filter({ hasText: /GitLab authorization was declined/ });
        await expect(declined).toBeFocused();
        await expect(declined).toBeInViewport({ ratio: 1 });
        await expect(page.getByText("Connection status: active")).toBeVisible();
        await assess(page, testInfo, "gitlab-consent-declined");

        consentOutcome = "success";
        await tabTo(page, connect);
        await page.keyboard.press("Enter");
        const connected = page.getByRole("status").filter({ hasText: /GitLab connected/ });
        await expect(connected).toBeFocused();
        await expect(connected).toBeInViewport({ ratio: 1 });
        await assess(page, testInfo, "gitlab-consent-completed");
    });
});

test("GitLab disconnection can be canceled and retried by keyboard", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/gitlab/refuse-actions`)).ok()).toBe(true);
        const connections = `${fixture.url}/organizations/acme/connections`;
        await visit(page, connections);
        const disconnect = page.getByRole("button", { name: "Disconnect GitLab" });

        const confirmDisconnect = async (accept) => {
            await tabTo(page, disconnect);
            const dialog = page.waitForEvent("dialog");
            const submitted = page.keyboard.press("Enter");
            const confirmation = await dialog;
            const message = confirmation.message();
            await (accept ? confirmation.accept() : confirmation.dismiss());
            await submitted;
            expect(message).toContain("All attached GitLab sources will lose repository access");
        };

        let disconnectRequests = 0;
        page.on("request", (request) => {
            if (request.url().endsWith("/gitlab/disconnect")) disconnectRequests++;
        });
        await confirmDisconnect(false);
        await expect(disconnect).toBeFocused();
        await expect(page).toHaveURL(connections);
        expect(disconnectRequests).toBe(0);

        await confirmDisconnect(true);
        const refusal = page.getByRole("alert").filter({ hasText: /could not disconnect GitLab/i });
        await expect(refusal).toBeFocused();
        await expect(refusal).toBeInViewport({ ratio: 1 });
        await expect(page.getByText("Connection status: active")).toBeVisible();
        await assess(page, testInfo, "gitlab-disconnect-refused");

        await confirmDisconnect(true);
        const success = page.getByRole("status").filter({ hasText: /GitLab disconnected/ });
        await expect(success).toBeFocused();
        await expect(success).toBeInViewport({ ratio: 1 });
        await expect(page.getByText("Connection status: revoked")).toBeVisible();
        await expect(disconnect).toHaveCount(0);
        expect(disconnectRequests).toBe(2);
        await assess(page, testInfo, "gitlab-disconnected");
    });
});
