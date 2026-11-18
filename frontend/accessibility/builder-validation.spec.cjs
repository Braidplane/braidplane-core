const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

test("Builder contract validation leads the keyboard to the affected connection and step", async ({
    page,
}, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-references`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        const activate = async (control) => {
            await expect(control).toBeVisible();
            await tabTo(page, control);
            await page.keyboard.press("Enter");
        };
        const normalize = page.getByRole("button", { name: /^Transform Step normalize/ });
        const inputType = page
            .locator("#builder-transform-input-contract-editor")
            .getByRole("combobox", { name: "Type", exact: true });
        const validate = page.getByRole("button", { name: "Validate in dev", exact: true });
        const diagnostics = page.locator("#builder-server-validation");
        const mismatch =
            'edge "fetch->normalize" incompatible contracts: field "email" expected integer, actual string';
        const stepMessage = 'Use the incoming "email" field type in the transform input contract.';
        let rejections = 0;
        let corrections = 0;
        const rejection = Promise.withResolvers();
        await page.route("**/builder/validate", async (route) => {
            const steps = route.request().postDataJSON().pipeline_spec.steps;
            const index = steps.findIndex((step) => step.slug === "normalize");
            const field = steps[index].transform.input_contract.fields.find(({ name }) => name === "email");
            if (field.type === "string") {
                corrections++;
                await route.continue();
                return;
            }
            expect(field.type).toBe("integer");
            rejections++;
            await rejection.promise;
            await route.fulfill({
                status: 400,
                json: {
                    error: {
                        validation_errors: [
                            { field: "spec", code: "contract_mismatch", message: mismatch },
                            {
                                field: `pipeline_spec.steps[${index}].transform.input_contract`,
                                code: "contract_mismatch",
                                message: stepMessage,
                            },
                        ],
                    },
                },
            });
        });

        await activate(normalize);
        await expect(inputType).toHaveValue("string");
        await tabTo(page, inputType);
        await page.keyboard.press("i");
        await expect(inputType).toHaveValue("integer");
        await activate(validate);
        try {
            await expect(validate).toHaveAttribute("aria-busy", "true");
            await expect(validate).toBeFocused();
            await page.keyboard.press("Enter");
        } finally {
            rejection.resolve();
        }
        const failedEdge = page.getByRole("button", { name: "Inspect incompatible connection fetch to normalize" });
        await expect(failedEdge).toHaveAttribute("data-validation-error", "true");
        await expect(validate).toBeFocused();
        await assess(page, testInfo, "builder-contract-validation-rejected");

        const modes = page.getByRole("group", { name: "Builder view", exact: true });
        const sourceMode = modes.getByRole("button", { name: "Source", exact: true });
        const splitMode = modes.getByRole("button", { name: "Split", exact: true });
        const editor = page.getByRole("textbox", { name: "Pipeline source", exact: true });
        await activate(sourceMode);
        await expect(editor).not.toHaveValue("");
        await tabTo(page, editor);
        await page.keyboard.press("ControlOrMeta+End");
        await page.keyboard.insertText("\n# Source draft for keyboard inspection");
        const unappliedSource = await editor.inputValue();
        expect(unappliedSource).toContain("# Source draft for keyboard inspection");
        const stepDiagnostic = diagnostics.getByRole("button", { name: /use the incoming/i });
        await expect(stepDiagnostic).toBeVisible();
        await activate(stepDiagnostic);
        await expect(normalize).toBeFocused();
        await expect(normalize).toBeInViewport();
        await expect(splitMode).toHaveAttribute("aria-pressed", "true");
        await expect(editor).toHaveValue(unappliedSource);
        await expect(inputType).toHaveValue("integer");
        await assess(page, testInfo, "builder-contract-validation-step-from-source");

        await activate(sourceMode);
        await activate(diagnostics.getByRole("button", { name: /expected integer, actual string/ }));
        await expect(failedEdge).toBeFocused();
        await expect(failedEdge).toBeInViewport();
        await expect(splitMode).toHaveAttribute("aria-pressed", "true");
        await expect(editor).toHaveValue(unappliedSource);
        const preview = page.locator("#builder-edge-preview");
        await expect(preview.getByText(mismatch, { exact: false })).toBeVisible();
        await assess(page, testInfo, "builder-contract-validation-edge");

        await activate(preview.getByRole("button", { name: "Inspect destination" }));
        await expect(normalize).toBeFocused();
        await expect(normalize).toBeInViewport();
        await expect
            .poll(() =>
                normalize.evaluate((card) => {
                    const bounds = card.getBoundingClientRect();
                    const x = (Math.max(0, bounds.left) + Math.min(innerWidth, bounds.right)) / 2;
                    const y = (Math.max(0, bounds.top) + Math.min(innerHeight, bounds.bottom)) / 2;
                    return card.contains(document.elementFromPoint(x, y));
                }),
            )
            .toBe(true);
        await tabTo(page, inputType);
        await page.keyboard.press("s");
        await expect(inputType).toHaveValue("string");
        const name = page.getByRole("textbox", { name: "Step name", exact: true });
        await tabTo(page, name);
        await page.keyboard.press("ControlOrMeta+a");
        await page.keyboard.insertText("Normalize verified customers");
        await page.keyboard.press("Tab");
        await expect(diagnostics).toBeHidden();
        await activate(validate);
        await expect(diagnostics).toContainText("Server validation passed in dev");
        await expect(page.getByRole("button", { name: "Inspect connection fetch to normalize" })).toHaveAttribute(
            "data-validation-error",
            "false",
        );
        expect(rejections).toBe(1);
        expect(corrections).toBe(1);
        await assess(page, testInfo, "builder-contract-validation-repaired");

        const save = page.getByRole("button", { name: "Save", exact: true });
        const submission = page.waitForResponse(
            (response) => response.request().method() === "POST" && response.url().endsWith("/pipelines/save"),
        );
        await activate(save);
        expect((await submission).ok()).toBe(true);
        await expect(save).toBeDisabled();
        await page.evaluate(() => localStorage.clear());
        await page.reload();
        await activate(normalize);
        await expect(inputType).toHaveValue("string");
        await expect(name).toHaveValue("Normalize verified customers");
    });
});
