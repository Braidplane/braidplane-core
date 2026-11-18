const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { assess, tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

async function assessDialog(page, testInfo, dialog, state) {
    await dialog.evaluate((element) =>
        Promise.allSettled(element.getAnimations({ subtree: true }).map((animation) => animation.finished)),
    );
    await assess(page, testInfo, state);
}

test("Builder proposal and conflict dialogs support keyboard review and dismissal", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        const step = page.getByRole("button", { name: /^Transform Step normalize/ });
        const stepName = page.getByRole("textbox", { name: "Step name", exact: true });
        const editName = async (name) => {
            await tabTo(page, step);
            await page.keyboard.press("Enter");
            await tabTo(page, stepName);
            await page.keyboard.press("ControlOrMeta+a");
            await page.keyboard.insertText(name);
            await page.keyboard.press("Tab");
        };
        const save = page.getByRole("button", { name: "Save", exact: true });
        await editName("Normalize inventory");
        await tabTo(page, save);
        const saved = page.waitForResponse(
            (response) => response.request().method() === "POST" && response.url().endsWith("/pipelines/save"),
        );
        await page.keyboard.press("Enter");
        expect((await saved).ok()).toBe(true);

        const diff = Array.from({ length: 60 }, (_, line) => `+ Review line ${line + 1}`).join("\n");
        let reviewUnavailable = true;
        await page.route("**/proposal-review", (route) =>
            reviewUnavailable
                ? route.fulfill({ status: 503, json: { error: { message: "Review temporarily unavailable" } } })
                : route.fulfill({
                      json: { draft_state: "editing", provider_supported: true, structural_diff: diff },
                  }),
        );
        const propose = page.getByRole("button", { name: "Propose", exact: true });
        await expect(propose).toBeEnabled();
        await tabTo(page, propose);
        await page.keyboard.press("Enter");
        const proposal = page.getByRole("dialog", { name: "Review Git proposal", exact: true });
        await expect(proposal.getByRole("heading", { name: "Review Git proposal" })).toBeFocused();
        await expect(proposal.getByRole("alert")).toContainText("Review temporarily unavailable");
        await expect(proposal.getByRole("status")).toHaveText("Unavailable");
        await assessDialog(page, testInfo, proposal, "builder-proposal-review-unavailable");
        await page.keyboard.press("Escape");
        await expect(proposal).toBeHidden();
        await expect(propose).toBeFocused();

        reviewUnavailable = false;
        await page.keyboard.press("Enter");
        const structuralDiff = proposal.getByRole("region", { name: "Structural diff", exact: true });
        await expect(structuralDiff).toContainText("Review line 60");
        await tabTo(page, structuralDiff);
        await page.keyboard.press("ArrowDown");
        await expect.poll(() => structuralDiff.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
        await assessDialog(page, testInfo, proposal, "builder-proposal-review");
        await tabTo(page, proposal.getByRole("button", { name: "Cancel", exact: true }));
        await page.keyboard.press("Enter");
        await expect(proposal).toBeHidden();
        await expect(propose).toBeFocused();

        let publicationUnavailable = true;
        const publishPath = "**/pipelines/daily-sync/publish";
        await page.route(publishPath, (route) =>
            publicationUnavailable
                ? route.fulfill({ status: 503, json: { error: { message: "Publication temporarily unavailable" } } })
                : route.fulfill({ status: 202, json: { draft: {} } }),
        );
        const confirmProposal = proposal.getByRole("button", { name: "Create or update proposal", exact: true });
        const feedback = page.getByRole("status").and(page.locator("#builder-save-reason"));
        await page.keyboard.press("Enter");
        await expect(confirmProposal).toBeEnabled();
        await tabTo(page, confirmProposal);
        await page.keyboard.press("Enter");
        await expect(proposal).toBeHidden();
        await expect(propose).toBeFocused();
        await expect(feedback).toContainText("Publish failed: Publication temporarily unavailable");

        publicationUnavailable = false;
        await page.keyboard.press("Enter");
        await expect(confirmProposal).toBeEnabled();
        await tabTo(page, confirmProposal);
        await page.keyboard.press("Space");
        await expect(proposal).toBeHidden();
        await expect(propose).toBeFocused();
        await expect(feedback).toContainText("Git proposal queued");

        const canonicalHash = "a".repeat(64);
        let acceptMergedDraft = false;
        await page.route("**/pipelines/save", (route) =>
            acceptMergedDraft
                ? route.fallback()
                : route.fulfill({
                      status: 409,
                      json: {
                          error: { message: "Version conflict" },
                          draft_preserved: true,
                          current_spec_hash: canonicalHash,
                          conflict_diff: { comparable_unified_diff: diff },
                      },
                  }),
        );
        await editName("Normalize retained inventory");
        await tabTo(page, save);
        await page.keyboard.press("Enter");
        const conflict = page.getByRole("dialog", { name: "Version conflict", exact: true });
        await expect(conflict.getByRole("heading", { name: "Version conflict" })).toBeFocused();
        const conflictDiff = conflict.getByRole("region", {
            name: "Show diff (canonical vs. your draft)",
            exact: true,
        });
        await expect(conflictDiff).toContainText("Review line 60");
        await tabTo(page, conflictDiff);
        await page.keyboard.press("ArrowDown");
        await expect.poll(() => conflictDiff.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
        await assessDialog(page, testInfo, conflict, "builder-version-conflict");
        await tabTo(page, conflict.getByRole("button", { name: "Cancel", exact: true }));
        await page.keyboard.press("Enter");
        await expect(conflict).toBeHidden();
        await expect(save).toBeFocused();
        await tabTo(page, step);
        await page.keyboard.press("Enter");
        await expect(stepName).toHaveValue("Normalize retained inventory");

        await tabTo(page, save);
        await page.keyboard.press("Enter");
        const merge = conflict.getByRole("button", { name: "Merge manually", exact: true });
        await tabTo(page, merge);
        await page.keyboard.press("Enter");
        const unit = conflict.getByRole("button", { name: /^Normalize retained inventory/ });
        await expect(unit).toBeFocused();
        await expect(unit).toHaveAttribute("aria-pressed", "true");
        await page.keyboard.press("Space");
        await expect(unit).toBeFocused();
        const apply = conflict.getByRole("button", { name: "Apply merge", exact: true });
        await expect(apply).toBeDisabled();
        const manual = conflict.getByRole("button", { name: "Manual", exact: true });
        const editor = conflict.getByRole("textbox", { name: "Name", exact: true });
        await tabTo(page, manual);
        await page.keyboard.press("Enter");
        await expect(editor).toBeFocused();
        await page.keyboard.press("ControlOrMeta+a");
        await page.keyboard.type("Merged inventry");
        await page.keyboard.press("ArrowLeft");
        await page.keyboard.press("ArrowLeft");
        await editor.evaluate((element) => {
            const caret = element.selectionStart;
            element.setRangeText("o", caret, caret, "end");
            element.dispatchEvent(new InputEvent("input", { bubbles: true, isComposing: true }));
            element.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "o" }));
        });
        await expect(editor).toBeFocused();
        await expect(editor).toHaveValue("Merged inventory");
        await expect(apply).toBeEnabled();
        await assessDialog(page, testInfo, conflict, "builder-manual-merge");

        acceptMergedDraft = true;
        await tabTo(page, apply);
        const mergedSave = page.waitForResponse(
            (response) => response.request().method() === "POST" && response.url().endsWith("/pipelines/save"),
        );
        await page.keyboard.press("Enter");
        const merged = await mergedSave;
        expect(merged.ok()).toBe(true);
        expect(merged.request().postDataJSON().context.expected_spec_hash).toBe(canonicalHash);
        await expect(conflict).toBeHidden();
        await expect(feedback).toBeFocused();
        await expect(feedback).toContainText("Draft saved to Braidplane");
        await page.reload();
        await tabTo(page, step);
        await page.keyboard.press("Enter");
        await expect(stepName).toHaveValue("Merged inventory");
    });
});
