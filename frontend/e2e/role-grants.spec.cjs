const {
    test,
    expect,
    assertSeededAuth,
    deleteE2EOrganization,
    deleteE2EProject,
    e2eProjectSlug,
    uniqueE2ESlug,
    unsafeRequest,
} = require("./support/smoke-fixtures.cjs");

test.use({ actionTimeout: 5000 });
test.beforeAll(assertSeededAuth);

test("edits role permissions and grants access at a project", async ({ page }, testInfo) => {
    const org = uniqueE2ESlug("grant-org", testInfo);
    const project = "local-project";
    const role = "scoped-reviewer";
    const settings = `/organizations/${org}`;
    const orgAPI = `/api${settings}`;

    expect(
        (
            await unsafeRequest(page, "POST", "/api/organizations", { data: { name: "Role grants", slug: org } })
        ).status(),
    ).toBe(201);
    const errors = [];
    try {
        expect(
            (
                await unsafeRequest(page, "POST", `${orgAPI}/projects`, {
                    data: { name: "Local project", slug: project },
                })
            ).status(),
        ).toBe(201);
        const members = await page.request.get(`${orgAPI}/members`);
        expect(members.ok()).toBe(true);
        const member = (await members.json()).members[0];
        expect(member.role).toBe("admin");

        await page.goto(`${settings}/roles`);
        const create = page.locator(`form[action="${settings}/roles"]`);
        await create.getByLabel("Name", { exact: true }).fill("Scoped reviewer");
        await create.getByRole("button", { name: "Create role" }).click();
        const edit = page.locator(`#organization-role-${role}-edit`);
        await expect(edit).toBeAttached();
        const getRole = async () => (await page.request.get(`${orgAPI}/roles/${role}`)).json();
        expect((await getRole()).permissions).toEqual([]);
        const unsupported = await unsafeRequest(page, "PUT", `${orgAPI}/roles/${role}`, {
            data: { name: "Invalid role", permissions: [{ resource: "roles", action: "execute" }] },
        });
        expect(unsupported.status()).toBe(400);
        expect((await getRole()).permissions).toEqual([]);

        await page.getByRole("button", { name: "Edit", exact: true }).click();
        await edit.getByText("Permissions", { exact: true }).click();
        const allPermissions = edit.getByRole("checkbox", { name: "Select all permissions", exact: true });
        const allPipelines = edit.getByRole("checkbox", { name: "Select all Pipelines permissions", exact: true });
        await expect(allPermissions).not.toBeChecked();
        await expect(allPermissions).toHaveJSProperty("indeterminate", false);
        await allPipelines.check();
        await expect(edit.locator('input[value="pipelines:execute"]')).toBeChecked();
        await expect(allPermissions).toHaveJSProperty("indeterminate", true);
        await allPermissions.check();
        await expect(allPermissions).toHaveJSProperty("indeterminate", false);
        await expect(edit.locator('input[value="queries:purge"]')).toBeChecked();
        await allPermissions.uncheck();
        await expect(allPipelines).not.toBeChecked();
        await expect(edit.getByRole("button", { name: "Save", exact: true })).toBeDisabled();

        const permissions = ["events:read", "pipelines:execute", "projects:read", "queries:purge"];
        for (const permission of permissions) {
            await edit.locator(`input[value="${permission}"]`).check();
        }
        await expect(allPipelines).toHaveJSProperty("indeterminate", true);
        const dryRunName = "x".repeat(256);
        await edit.getByLabel("Name", { exact: true }).fill(dryRunName);
        await edit.getByRole("button", { name: "Save", exact: true }).click();
        await expect(edit.getByRole("alert").filter({ visible: true })).toBeFocused();
        await expect(edit.getByLabel("Name", { exact: true })).toHaveValue(dryRunName);
        await expect(edit.locator("[data-role-permission-choice]:checked")).toHaveCount(4);
        await expect(allPipelines).toHaveJSProperty("indeterminate", true);
        await edit.getByLabel("Name", { exact: true }).fill("Updated scoped reviewer");
        await edit.getByRole("button", { name: "Save", exact: true }).click();
        await expect(
            page.getByText("Updated scoped reviewer", { exact: true }).filter({ visible: true }),
        ).toBeVisible();
        expect((await getRole()).permissions.map(({ resource, action }) => `${resource}:${action}`).sort()).toEqual(
            permissions,
        );

        await page.setViewportSize({ width: 390, height: 844 });
        await page.goto(`${settings}/members`);
        const membersList = page.locator("#members-list");
        await expect(membersList.getByText("admin", { exact: true }).first()).toBeVisible();
        await membersList.getByRole("link", { name: "Grants" }).click();
        const grantPath = `${settings}/grants`;
        const grantForm = page.locator(`form[action^="${grantPath}"]`);
        await expect(grantForm.getByLabel("Member or API key slug")).toHaveValue(member.user_slug);
        await grantForm.getByLabel("Role slug").fill(role);
        await grantForm.getByRole("combobox", { name: "Scope", exact: true }).selectOption("project");
        await grantForm.getByLabel("Target slug or key").fill(e2eProjectSlug);
        await grantForm.getByRole("button", { name: "Grant role" }).click();
        await expect(grantForm.getByRole("alert").filter({ visible: true })).toContainText(
            "could not be found in this organization",
        );
        await expect(grantForm.getByLabel("Target slug or key")).toHaveValue(e2eProjectSlug);
        await expect(page.getByText("No explicit grants.")).toBeVisible();

        expect(
            (
                await page.request.post(grantPath, {
                    form: {
                        subject_kind: "member",
                        subject_slug: member.user_slug,
                        role_slug: role,
                        scope: "project",
                        target_slug: project,
                    },
                })
            ).status(),
        ).toBe(403);
        await grantForm.getByLabel("Target slug or key").fill(project);
        await grantForm.getByRole("button", { name: "Grant role" }).click();
        const row = page.getByRole("row").filter({ hasText: project });
        await expect(row).toContainText(role);
        const grantList = await (await page.request.get(`${orgAPI}/authorization/grants`)).json();
        expect(grantList.grants).toEqual([
            expect.objectContaining({
                subject_kind: "member",
                subject_slug: member.user_slug,
                role_slug: role,
                scope: "project",
                target_slug: project,
            }),
        ]);

        page.once("dialog", async (dialog) => {
            expect(dialog.message()).toContain("keeps access from its primary role and any other grants");
            await dialog.accept();
        });
        await row.getByRole("button", { name: /Revoke grant/ }).click();
        await expect(page.getByText("No explicit grants.")).toBeVisible();
        expect((await (await page.request.get(`${orgAPI}/members`)).json()).members[0].role).toBe("admin");

        await page.goto(`${settings}/roles`);
        await page.getByRole("button", { name: "Edit", exact: true }).click();
        await expect(allPermissions).toHaveJSProperty("indeterminate", true);
        await allPermissions.check();
        await allPermissions.uncheck();
        await edit.getByRole("button", { name: "Save", exact: true }).click();
        await expect(edit).toBeHidden();
        expect((await getRole()).permissions).toEqual([]);
    } catch (error) {
        errors.push(error);
    }
    for (const cleanup of [() => deleteE2EProject(page, project, org), () => deleteE2EOrganization(page, org)]) {
        try {
            await cleanup();
        } catch (error) {
            errors.push(error);
        }
    }
    if (errors.length) throw new AggregateError(errors, "Role grant workflow or fixture cleanup failed");
});
