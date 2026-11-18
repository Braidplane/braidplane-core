const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("GitHub existing installation supports keyboard validation and authorization retry", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        const authorization = "https://github.com/login/oauth/authorize?state=fixture";
        await page.route(
            (url) => url.origin !== fixture.url,
            (route) => {
                if (route.request().url() === authorization) {
                    return route.fulfill({ contentType: "text/html", body: "<title>Synthetic authorization</title>" });
                }
                return route.abort();
            },
        );
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const settings = `${fixture.url}/organizations/disconnected/github`;
        await visit(page, settings);

        const account = page.getByRole("textbox", { name: /GitHub account or organization/ });
        const connect = page.getByRole("button", { name: "Connect existing installation" });
        const moreTime = connect.locator("..").getByRole("checkbox", { name: /^Allow 100 minutes/ });
        await expect(moreTime).not.toBeChecked();
        await tabTo(page, moreTime);
        await page.keyboard.press("Space");
        await expect(account).toHaveAccessibleDescription(/use letters, numbers and hyphens/i);
        const enterAccount = async (value) => {
            await tabTo(page, account);
            await page.keyboard.press("ControlOrMeta+a");
            await page.keyboard.insertText(value);
            await tabTo(page, connect);
            await page.keyboard.press("Enter");
        };

        await enterAccount("@invalid");
        expect(await account.evaluate((input) => input.validity.patternMismatch)).toBe(true);
        await expect(account).toBeFocused();

        await enterAccount("synthetic-missing");
        const error = page.getByRole("alert").filter({ hasText: "GitHub could not authorize the installation" });
        await expect(error).toBeFocused();
        await expect(error).toBeInViewport({ ratio: 1 });
        await expect(account).toHaveValue("synthetic-missing");
        await expect(moreTime).toBeChecked();
        await expect(page).toHaveURL(settings);
        await assess(page, testInfo, "github-existing-install-refused");

        await enterAccount("synthetic-valid");
        await expect(page).toHaveTitle("Synthetic authorization");
        await expect(page).toHaveURL(authorization);
    });
});

test("GitHub keeps the connection visible after a refused disconnect", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const settings = `${fixture.url}/organizations/acme/github`;
        await visit(page, `${settings}?connected=true`);
        await expect(page.getByRole("status").filter({ hasText: "GitHub connected." })).toBeFocused();
        const disconnect = page.getByRole("button", { name: "Disconnect", exact: true });
        await tabTo(page, disconnect);
        const dialog = page.waitForEvent("dialog");
        const submitted = page.keyboard.press("Enter");
        await (await dialog).accept();
        await submitted;

        const error = page.getByRole("alert").filter({ hasText: "Could not disconnect GitHub." });
        await expect(error).toBeFocused();
        await expect(error).toBeInViewport({ ratio: 1 });
        await expect(page.getByText("acme/pipelines", { exact: true })).toBeVisible();
        await expect(disconnect).toBeEnabled();
        await assess(page, testInfo, "github-disconnect-refused");
    });
});

test("GitHub install and reconnect both let keyboard users request more time", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        const authorization = "https://github.com/apps/fixture/installations/new?state=fixture";
        await page.route(authorization, (route) =>
            route.fulfill({ contentType: "text/html", body: "<title>Synthetic installation</title>" }),
        );
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        for (const [organization, action] of [
            ["disconnected", "Connect GitHub"],
            ["connected", "Reconnect"],
        ]) {
            await visit(page, `${fixture.url}/organizations/${organization}/github`);
            const connect = page.getByRole("button", { name: action, exact: true });
            const moreTime = connect.locator("..").getByRole("checkbox", { name: /^Allow 100 minutes/ });
            await tabTo(page, moreTime);
            await expect(moreTime).toBeInViewport({ ratio: 1 });
            await page.keyboard.press("Space");
            await expect(moreTime).toBeChecked();
            await assess(page, testInfo, `github-install-timing-${organization}`);
            const submitted = page.waitForRequest((request) => request.url().endsWith("/github/connect"));
            await tabTo(page, connect);
            await page.keyboard.press("Enter");
            expect(new URLSearchParams((await submitted).postData()).get("more_time")).toBe("true");
            await expect(page).toHaveTitle("Synthetic installation");
        }
    });
});
