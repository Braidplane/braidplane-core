const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

for (const [provider, outcome, message] of [
    ["github", "declined", "GitHub authorization could not be completed."],
    ["gitlab", "declined", "GitLab authorization was declined."],
    ["github", "expired", "GitHub authorization expired."],
]) {
    test(`${provider} repository setup resumes the same saved target after ${outcome} authorization`, async ({
        page,
    }, testInfo) => {
        await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
            expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
            const setup = `${fixture.url}/_test/repository-setup?gitlab_consent=required&refuse_timed_start=true&github_consent=${outcome}`;
            expect((await page.request.post(setup)).ok()).toBe(true);
            const github = provider === "github";
            const authorization = github
                ? "https://github.com/apps/braidplane-fixture/installations/new?*"
                : "https://gitlab.com/oauth/authorize?*";
            const callback = github
                ? `${fixture.url}/github/callback?state=${"f".repeat(32)}&error=access_denied`
                : `${fixture.url}/_test/gitlab-consent/callback?result=denied`;
            await page.route(authorization, (route) =>
                route.fulfill({
                    contentType: "text/html",
                    body: `<title>Synthetic consent</title><a href="${callback}">Decline consent</a>`,
                }),
            );
            await visit(
                page,
                `${fixture.url}/organizations/acme/projects/ops?section=repository&repository_path=create`,
            );

            const providerField = page.getByRole("combobox", { name: "Provider", exact: true });
            if (!github) {
                await tabTo(page, providerField);
                await page.keyboard.press("Space");
                await page.keyboard.press("End");
                await page.keyboard.press("Enter");
                await expect(providerField).toHaveValue(provider);
            }
            const owner = page.getByRole("textbox", { name: "Owner, organization or group", exact: true });
            const repository = page.getByRole("textbox", { name: "Repository name", exact: true });
            for (const [field, value] of [
                [owner, "customer-team"],
                [repository, "data-pipelines"],
            ]) {
                await tabTo(page, field);
                await page.keyboard.insertText(value);
            }
            const consent = page.getByRole("checkbox", {
                name: "Create this private repository and add the Braidplane template.",
            });
            const moreTime = page.getByRole("checkbox", { name: /^Allow 100 minutes/ });
            const activate = async (control, key = "Enter") => {
                await tabTo(page, control);
                await page.keyboard.press(key);
            };
            await expect(moreTime).not.toBeChecked();
            await activate(consent, "Space");
            await activate(page.getByRole("button", { name: "Create and connect", exact: true }));
            await expect(page).toHaveTitle("Synthetic consent");
            if (outcome === "expired") {
                await page.context().clearCookies({ name: /^github_install_/ });
            }
            await activate(page.getByRole("link", { name: "Decline consent", exact: true }));
            const feedback = page.getByRole("alert").filter({ hasText: message });
            await expect(feedback).toBeFocused();
            await expect(feedback).toBeInViewport({ ratio: 1 });
            await expect(owner).toHaveValue("customer-team");
            await expect(repository).toHaveValue("data-pipelines");
            await expect(repository).toHaveAttribute("readonly", "");
            if (outcome === "expired") {
                await assess(page, testInfo, "github-repository-expired");
            }

            const resume = page.getByRole("button", { name: "Resume setup", exact: true });
            await activate(consent, "Space");
            await activate(moreTime, "Space");
            await expect(moreTime).toBeChecked();
            await activate(resume);
            const refused = page.getByRole("alert").filter({ hasText: "Authorization could not start." });
            await expect(refused).toBeFocused();
            await expect(refused).toBeInViewport({ ratio: 1 });
            await expect(moreTime).toBeChecked();
            await expect(repository).toHaveValue("data-pipelines");
            await assess(page, testInfo, `${provider}-repository-timing-refused`);

            if (!(await consent.isChecked())) {
                await activate(consent, "Space");
            }
            await activate(resume);
            await expect(page).toHaveTitle("Synthetic consent");
        });
    });
}
