const { test, expect } = require("@playwright/test");
const {
    signUpAndVerify,
    signInWithCredentials,
    deleteE2EAccount,
    deleteE2EOrganization,
    deleteE2EProject,
    uniqueE2ESlug,
} = require("../e2e/support/smoke-fixtures.cjs");
const { assess, tabTo } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("organization and project creation support keyboard validation and retry", async ({ page }, testInfo) => {
    const org = uniqueE2ESlug("a11y-project", testInfo);
    const additionalOrg = uniqueE2ESlug("a11y-second", testInfo);
    const email = `${org}@example.test`;
    const password = "Synthetic-workspace-123!";
    const workspace = "Accessibility Operations";
    const project = "Accessibility Pipeline";
    let submittedOrganization = false;
    let submittedAdditionalOrganization = false;
    const scan = async (state) => {
        const viewport = page.viewportSize();
        for (const width of [1280, 320]) {
            await page.setViewportSize({ width, height: 900 });
            await assess(page, testInfo, `${state}-${width}`);
        }
        await page.setViewportSize(viewport);
    };

    const activate = async (target) => {
        await expect(target).toBeVisible();
        await tabTo(page, target);
        await page.keyboard.press("Enter");
    };
    const typeText = async (target, value) => {
        await expect(target).toBeVisible();
        await tabTo(page, target);
        await page.keyboard.press("ControlOrMeta+a");
        await page.keyboard.insertText(value);
    };

    await signUpAndVerify(page, email, password, "Accessibility Fixture");
    await signInWithCredentials(page, email, password, /\/workspace/);
    try {
        const main = page.getByRole("main");
        await activate(main.getByRole("link", { name: /^Organization/ }));
        const orgName = main.getByRole("textbox", { name: "Organization workspace name" });
        const orgId = main.getByRole("textbox", { name: /^Identifier/ });
        const createOrg = main.getByRole("button", { name: "Create workspace", exact: true });
        await typeText(orgName, workspace);
        await activate(main.getByText("Customize identifier", { exact: true }));
        await typeText(orgId, "Bad slug!");
        await scan("organization-create-form");
        await activate(createOrg);
        const workspaceError = main.getByRole("alert");
        await expect(workspaceError).toContainText("workspace slug must use lowercase letters");
        await expect(workspaceError).toBeFocused();
        await expect(workspaceError).toBeInViewport();
        await expect(main).toHaveCount(1);
        await expect(orgName).toHaveValue(workspace);
        await expect(orgId).toHaveValue("Bad slug!");
        await scan("organization-create-invalid-identifier");

        await page.setViewportSize({ width: 320, height: 900 });
        await typeText(orgId, org);
        submittedOrganization = true;
        await activate(createOrg);
        await expect(page).toHaveURL(/\/dashboard/);
        const menu = page.getByRole("button", { name: "Open navigation menu", exact: true });
        const compact = await menu.isVisible();
        if (compact) await activate(menu);
        const navigation = page.getByRole("navigation", { name: compact ? "Mobile primary" : "Primary", exact: true });
        await activate(navigation.getByRole("link", { name: "Projects", exact: true }));
        await expect(page).toHaveURL(new RegExp(`/organizations/${org}/projects$`));
        await expect(main.getByRole("heading", { name: "No projects found" })).toBeVisible();

        const create = main.locator("#create-project");
        await activate(main.locator('[aria-controls="create-project"]'));
        const projectName = create.getByRole("textbox", { name: "Name", exact: true });
        const projectId = create.getByRole("textbox", { name: /^Identifier/ });
        const createProject = create.getByRole("button", { name: "Create project", exact: true });
        await typeText(projectName, project);
        await activate(create.getByText("Customize identifier", { exact: true }));
        await typeText(projectId, "Bad slug!");
        await scan("project-create-form");
        await activate(createProject);
        const projectError = main.getByRole("alert");
        await expect(projectError).toContainText("Project slug must use lowercase letters");
        await expect(projectError).toBeFocused();
        await expect(projectError).toBeInViewport();
        await expect(projectName).toHaveValue(project);
        await expect(projectId).toHaveValue("Bad slug!");
        await scan("project-create-invalid-identifier");

        await typeText(projectId, "accessibility-pipeline");
        await activate(createProject);
        await expect(page).toHaveURL(/project_slug=accessibility-pipeline/);
        const created = main.getByRole("link", { name: project, exact: true });
        await activate(created);
        await expect(page).toHaveURL((url) => url.pathname === `/organizations/${org}/projects/accessibility-pipeline`);
        await scan("project-created-detail");

        await activate(page.getByRole("button", { name: /^Switch organization:/ }));
        await activate(
            page
                .getByRole("group", { name: "Organization menu", exact: true })
                .getByRole("link", { name: "New organization", exact: true }),
        );
        await expect(page).toHaveURL(/\/organizations\/new$/);
        const organizationForm = main.locator('form[action="/organizations"]');
        const additionalName = organizationForm.getByRole("textbox", { name: "Organization name", exact: true });
        const description = organizationForm.getByRole("textbox", { name: "Description (optional)", exact: true });
        const additionalId = organizationForm.getByRole("textbox", { name: /^Identifier/ });
        await expect(additionalName).toHaveAttribute("autocomplete", "organization");
        await typeText(additionalName, "Accessibility Secondary");
        await typeText(description, "Synthetic workspace context");
        await activate(organizationForm.getByText("Customize identifier", { exact: true }));
        await typeText(additionalId, "Bad slug!");
        await scan("additional-organization-form");
        await activate(organizationForm.getByRole("button", { name: "Create Organization", exact: true }));
        const additionalError = main.getByRole("alert");
        await expect(additionalError).toContainText("organization slug must use lowercase letters");
        await expect(additionalError).toBeFocused();
        await expect(additionalError).toBeInViewport();
        await expect(main).toHaveCount(1);
        await expect(additionalName).toHaveValue("Accessibility Secondary");
        await expect(description).toHaveValue("Synthetic workspace context");
        await expect(additionalId).toHaveValue("Bad slug!");
        await scan("additional-organization-invalid-identifier");

        await typeText(additionalId, additionalOrg);
        submittedAdditionalOrganization = true;
        await activate(organizationForm.getByRole("button", { name: "Create Organization", exact: true }));
        await expect(page).toHaveURL(/\/organizations$/);
        await expect(main.getByRole("heading", { name: "Accessibility Secondary", exact: true })).toBeVisible();
        await expect(
            page.getByRole("button", { name: "Switch organization: Accessibility Secondary", exact: true }),
        ).toBeVisible();
        await scan("additional-organization-created");
    } finally {
        if (submittedAdditionalOrganization) await deleteE2EOrganization(page, additionalOrg);
        if (submittedOrganization) {
            await deleteE2EProject(page, "accessibility-pipeline", org);
            await deleteE2EOrganization(page, org);
        }
        await deleteE2EAccount(page, email);
    }
});
