import { describe, expect, it, vi } from "vitest";

await import("../ui-utils.js");

describe("server form error focus", () => {
    it("KeepsAFailedDraftSaveableWithoutAnotherEdit", () => {
        document.body.innerHTML =
            '<form data-dirty-form data-dirty-unsaved><input name="name" value="Unsaved draft"><button type="submit" data-dirty-submit disabled>Save</button></form>';
        window.braidplaneInitDirtyForms(document);
        expect(document.querySelector("button").disabled).toBe(false);
    });

    it("FocusesTheInvalidControlAfterASwapAndLeavesUnrelatedSwapsAlone", () => {
        document.body.innerHTML =
            '<form data-form-feedback><input name="name" aria-invalid="true" data-form-error-focus><p id="feedback" role="alert">Name is required.</p></form><section id="other"></section>';
        const form = document.querySelector("form");
        form.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true }));
        expect(document.activeElement).toBe(form.querySelector("input"));
        document.activeElement.blur();
        document.querySelector("#other").dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true }));
        expect(document.activeElement).toBe(document.body);
    });

    it("FocusesTheSummaryForAnErrorWithoutAField", () => {
        document.body.innerHTML =
            '<form data-form-feedback><div role="alert" tabindex="-1" data-form-error-focus>Save failed.</div></form>';
        document.querySelector("form").dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true }));
        expect(document.activeElement).toBe(document.querySelector('[role="alert"]'));
    });
});

describe("transient query feedback", () => {
    it("RemovesValidatedProviderOAuthFeedbackFromBrowserHistory", () => {
        window.history.replaceState(
            {},
            "",
            "/orgs/acme-hq/data-sources?environment_slug=dev&provider_oauth=success&selected=hubspot-crm#sources",
        );
        document.body.innerHTML = '<div data-consume-query-param="provider_oauth"></div>';

        window.braidplaneInitTransientQueryFeedback(document);

        expect(window.location.pathname).toBe("/orgs/acme-hq/data-sources");
        expect(window.location.search).toBe("?environment_slug=dev&selected=hubspot-crm");
        expect(window.location.hash).toBe("#sources");
    });

    it("KeepsQueryParametersWhenNoValidatedFeedbackWasRendered", () => {
        window.history.replaceState({}, "", "/orgs/acme-hq/data-sources?provider_oauth=success&selected=hubspot-crm");
        document.body.innerHTML = "<main></main>";

        window.braidplaneInitTransientQueryFeedback(document);

        expect(window.location.search).toBe("?provider_oauth=success&selected=hubspot-crm");
    });
});

describe("braidplaneComputeLineDiff", () => {
    it("ReturnsLineDiffForSmallInputs", () => {
        expect(window.braidplaneComputeLineDiff("a\nb", "a\nc")).toEqual([
            { kind: "equal", text: "a" },
            { kind: "add", text: "c" },
            { kind: "remove", text: "b" },
        ]);
    });

    it("FallsBackWhenInputsAreTooLargeForQuadraticDiff", () => {
        const oldText = Array.from({ length: 401 }, (_, index) => `old-${index}`).join("\n");
        const newText = Array.from({ length: 401 }, (_, index) => `new-${index}`).join("\n");

        expect(window.braidplaneComputeLineDiff(oldText, newText)).toEqual([
            {
                kind: "equal",
                text: "[Diff omitted because the compared documents are too large for the in-browser line diff.]",
            },
            { kind: "remove", text: "[Original document: 401 lines]" },
            { kind: "add", text: "[Updated document: 401 lines]" },
        ]);
    });
});

describe("dirty forms", () => {
    it("EnablesSaveAfterEditableFieldChanges", () => {
        document.body.innerHTML = `
            <form id="settings" data-dirty-form>
                <input name="name" value="Original" />
            </form>
            <button type="submit" form="settings" data-dirty-submit>Save</button>
        `;

        window.braidplaneInitDirtyForms(document);

        const input = document.querySelector('input[name="name"]');
        const button = document.querySelector("[data-dirty-submit]");
        expect(button.disabled).toBe(true);

        input.value = "Changed";
        input.dispatchEvent(new Event("input", { bubbles: true }));

        expect(button.disabled).toBe(false);
    });

    it("KeepsEditsMadeBeforeDirtyFormInitializationSaveable", () => {
        document.body.innerHTML = `
            <form data-dirty-form>
                <input name="name" value="Original" />
                <button type="submit" data-dirty-submit disabled>Save</button>
            </form>
        `;
        const input = document.querySelector("input");
        input.value = "Early edit";
        window.braidplaneInitDirtyForms(document);
        expect(document.querySelector("button").disabled).toBe(false);
        expect(input.value).toBe("Early edit");
    });

    it("KeepsUnchangedExternallyAssociatedControlsPristine", () => {
        document.body.innerHTML = `
            <input name="confirmation" form="settings" value="" />
            <form id="settings" data-dirty-form>
                <input name="name" value="Original" />
                <button type="submit" data-dirty-submit disabled>Save</button>
            </form>
        `;
        window.braidplaneInitDirtyForms(document);
        const button = document.querySelector("button");
        expect(button.disabled).toBe(true);
        const input = document.querySelector('[name="name"]');
        input.value = "Changed";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        expect(button.disabled).toBe(false);
        input.value = "Original";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        expect(button.disabled).toBe(true);
    });

    it("InitializesDirtyFormWhenRootIsTheForm", () => {
        document.body.innerHTML = `
            <form id="credentials" data-dirty-form>
                <input name="password" type="password" value="" />
                <button type="submit" data-dirty-submit>Rotate credentials</button>
            </form>
        `;

        const form = document.querySelector("form");
        window.braidplaneInitDirtyForms(form);

        const input = document.querySelector('input[name="password"]');
        const button = document.querySelector("[data-dirty-submit]");
        expect(button.disabled).toBe(true);

        input.value = "secret";
        input.dispatchEvent(new Event("input", { bubbles: true }));

        expect(button.disabled).toBe(false);
    });

    it("UsesServerProvidedDirtyBaselineAfterPartialReloads", () => {
        document.body.innerHTML = `
            <form data-dirty-form data-dirty-baseline='[["data_source_slug",""]]'>
                <select name="data_source_slug">
                    <option value="">Select Git data source</option>
                    <option value="pipelines-git" selected>Pipeline Git</option>
                </select>
                <select name="tracked_branch">
                    <option value="main" selected>main</option>
                </select>
                <button type="submit" data-dirty-submit disabled>Save backend</button>
            </form>
        `;

        window.braidplaneInitDirtyForms(document);

        const button = document.querySelector("[data-dirty-submit]");
        expect(button.disabled).toBe(false);
    });
});

describe("delegated UI actions", () => {
    it("HandlesToggleButtonsPopupDismissalAndAutoSubmitSelects", () => {
        document.body.innerHTML = `
            <button type="button" data-toggle-target="advanced" aria-expanded="false">Toggle</button>
            <section id="advanced" class="hidden"></section>
            <div data-error-popup>
                <button type="button" data-popup-dismiss="[data-error-popup]">Dismiss</button>
            </div>
            <form id="filters">
                <select name="scope" data-auto-submit-form>
                    <option value="all">All</option>
                    <option value="active">Active</option>
                </select>
            </form>
        `;

        const form = document.getElementById("filters");
        form.requestSubmit = vi.fn();

        document.querySelector("[data-toggle-target]").click();
        expect(document.getElementById("advanced").classList.contains("hidden")).toBe(false);
        expect(document.querySelector("[data-toggle-target]").getAttribute("aria-expanded")).toBe("true");

        document.querySelector("[data-popup-dismiss]").click();
        expect(document.querySelector("[data-error-popup]")).toBeNull();

        document.querySelector("[data-auto-submit-form]").dispatchEvent(new Event("change", { bubbles: true }));
        expect(form.requestSubmit).toHaveBeenCalledTimes(1);
    });

    it("MarksClickedDataAndQueryCardsAsSelected", () => {
        document.body.innerHTML = `
            <a data-ds-card data-selected="true" id="old-ds"></a>
            <a data-ds-card id="new-ds"></a>
            <a data-query-card data-selected="true" id="old-query"></a>
            <a data-query-card id="new-query"></a>
        `;

        document.getElementById("new-ds").click();
        document.getElementById("new-query").click();

        expect(document.getElementById("old-ds").hasAttribute("data-selected")).toBe(false);
        expect(document.getElementById("new-ds").getAttribute("data-selected")).toBe("true");
        expect(document.getElementById("old-query").hasAttribute("data-selected")).toBe(false);
        expect(document.getElementById("new-query").getAttribute("data-selected")).toBe("true");
    });
});

describe("query detail initialization", () => {
    it("ReinitializesSwappedTabsWithoutLosingDirtyDefinition", () => {
        document.body.innerHTML = `<main><section data-query-detail>
            <button data-query-tab="query-definition">Definition</button>
            <button data-query-tab="query-execution-feedback-slot">Results</button>
            <div id="query-definition"><input value="Saved" /></div>
            <div id="query-execution-feedback-slot"></div>
        </section></main>`;
        const main = document.querySelector("main");
        main.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true }));
        document.querySelector("input").value = "Unsaved";
        const tabs = document.querySelectorAll("[data-query-tab]");
        // Repeated swaps must not install duplicate keyboard handlers.
        main.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true }));
        tabs[0].dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
        expect(tabs[1].getAttribute("aria-selected")).toBe("true");
        expect(document.getElementById("query-definition").hidden).toBe(true);
        tabs[1].dispatchEvent(new KeyboardEvent("keydown", { key: "Home" }));
        expect(document.getElementById("query-execution-feedback-slot").hidden).toBe(true);
        expect(document.querySelector("input").value).toBe("Unsaved");
    });
    it("SwitchesPanelsWithKeyboardAndPreservesEdits", () => {
        document.body.innerHTML = `
            <section data-query-detail>
                <button disabled data-query-tab="query-definition">Definition</button>
                <button disabled data-query-tab="query-execution-feedback-slot">Results</button>
                <div id="query-definition"><input value="Saved" /></div>
                <div id="query-execution-feedback-slot"></div>
            </section>`;
        const tabs = document.querySelectorAll("[data-query-tab]");
        expect([...tabs].every((tab) => tab.disabled)).toBe(true);
        window.braidplaneInitQueryDetail(document);
        expect([...tabs].every((tab) => !tab.disabled)).toBe(true);
        document.querySelector("input").value = "Unsaved";
        tabs[0].dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
        expect(tabs[1].getAttribute("aria-selected")).toBe("true");
        expect(document.getElementById("query-definition").hidden).toBe(true);
        expect(document.getElementById("query-execution-feedback-slot").hidden).toBe(false);
        tabs[0].click();
        expect(document.querySelector("input").value).toBe("Unsaved");
        expect(tabs[0].tabIndex).toBe(0);
        expect(tabs[1].tabIndex).toBe(-1);
    });

    it("InitializesParameterEditorRowsWithoutInlineScript", () => {
        const initAll = vi.fn();
        window.braidplaneQueryParamEditor = { initAll };
        document.body.innerHTML = `
            <section data-query-detail>
                <div data-param-rows>
                    <div data-param-row>
                        <select name="param_type"><option value="string">string</option></select>
                        <select name="param_required"><option value="false" selected>false</option></select>
                        <input name="param_default" value="" />
                    </div>
                </div>
            </section>
        `;

        window.braidplaneInitQueryDetail(document);

        const detail = document.querySelector("[data-query-detail]");
        expect(detail.dataset.queryDetailReady).toBe("true");
        expect(initAll).toHaveBeenCalledWith(detail);

        delete window.braidplaneQueryParamEditor;
    });
});

describe("alert policy scope forms", () => {
    it("DisablesInactiveProjectAndEnvironmentScopeFields", () => {
        document.body.innerHTML = `
            <form data-alert-policy-form>
                <select name="scope" data-alert-policy-scope-select>
                    <option value="organization" selected>Organization</option>
                    <option value="project">Project</option>
                    <option value="project_environment">Project environment</option>
                </select>
                <label data-alert-policy-scope-fields="project,project_environment">
                    <input name="project_slug" value="ops" />
                </label>
                <label data-alert-policy-scope-fields="project_environment">
                    <input name="environment_slug" value="prod" />
                </label>
            </form>
        `;

        window.braidplaneInitAlertPolicyForms(document);

        const scope = document.querySelector("[data-alert-policy-scope-select]");
        const projectGroup = document.querySelector('[data-alert-policy-scope-fields="project,project_environment"]');
        const environmentGroup = document.querySelector('[data-alert-policy-scope-fields="project_environment"]');
        const projectInput = document.querySelector('input[name="project_slug"]');
        const environmentInput = document.querySelector('input[name="environment_slug"]');

        expect(projectGroup.classList.contains("hidden")).toBe(true);
        expect(environmentGroup.classList.contains("hidden")).toBe(true);
        expect(projectInput.disabled).toBe(true);
        expect(environmentInput.disabled).toBe(true);

        scope.value = "project";
        scope.dispatchEvent(new Event("change", { bubbles: true }));

        expect(projectGroup.classList.contains("hidden")).toBe(false);
        expect(environmentGroup.classList.contains("hidden")).toBe(true);
        expect(projectInput.disabled).toBe(false);
        expect(environmentInput.disabled).toBe(true);

        scope.value = "project_environment";
        scope.dispatchEvent(new Event("change", { bubbles: true }));

        expect(projectGroup.classList.contains("hidden")).toBe(false);
        expect(environmentGroup.classList.contains("hidden")).toBe(false);
        expect(projectInput.disabled).toBe(false);
        expect(environmentInput.disabled).toBe(false);
    });
});

describe("HTMX scroll preservation", () => {
    it("RestoresScrollWithoutOverridingTheNextInteraction", () => {
        vi.useFakeTimers();
        const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
        try {
            document.body.innerHTML = `<a href="/next" data-preserve-scroll><span>Next</span></a>`;
            Object.defineProperty(window, "scrollX", { value: 12, configurable: true });
            Object.defineProperty(window, "scrollY", { value: 345, configurable: true });
            const trigger = document.querySelector("span");
            document.body.dispatchEvent(
                new CustomEvent("htmx:beforeRequest", { bubbles: true, detail: { elt: trigger } }),
            );
            document.body.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true }));
            expect(scrollTo).toHaveBeenCalledWith(12, 345);
            scrollTo.mockClear();
            document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }));
            Object.defineProperty(window, "scrollY", { value: 100, configurable: true });
            vi.advanceTimersByTime(350);
            expect(scrollTo).not.toHaveBeenCalled();
        } finally {
            scrollTo.mockRestore();
            vi.useRealTimers();
        }
    });
});

describe("entity list return scroll", () => {
    const key = "braidplane:entity-list-scroll";

    it("keeps the position through detail sections and restores only the matching list once", () => {
        const original = window.location.href;
        const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
        const list = "/orgs/acme/data-sources?environment_slug=dev&filter=ready&selected=warehouse";
        const detail = `/orgs/acme/data-sources/warehouse?list_return=${encodeURIComponent(list)}`;
        try {
            window.history.replaceState({}, "", "/orgs/acme/data-sources?filter=ready&environment_slug=dev");
            Object.defineProperty(window, "scrollY", { value: 913, configurable: true });
            document.body.innerHTML = `<a href="${detail}"><span>Warehouse</span></a>`;
            document.querySelector("a").addEventListener("click", (event) => event.preventDefault());
            document.querySelector("span").dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
            expect(JSON.parse(sessionStorage.getItem(key))).toEqual({ url: list, y: 913 });

            for (const path of [
                detail,
                `${detail}&section=schema`,
                "/orgs/other/data-sources?environment_slug=dev&filter=ready&selected=warehouse",
                "/orgs/acme/data-sources?environment_slug=prod&filter=ready&selected=warehouse",
            ]) {
                window.history.replaceState({}, "", path);
                window.dispatchEvent(new Event("load"));
                expect(scrollTo).not.toHaveBeenCalled();
            }
            window.history.replaceState(
                {},
                "",
                "/orgs/acme/data-sources?selected=warehouse&filter=ready&environment_slug=dev",
            );
            window.dispatchEvent(new Event("load"));
            expect(scrollTo).toHaveBeenCalledWith(window.scrollX, 913);
            expect(sessionStorage.getItem(key)).toBeNull();
            scrollTo.mockClear();
            window.dispatchEvent(new Event("load"));
            expect(scrollTo).not.toHaveBeenCalled();
        } finally {
            sessionStorage.removeItem(key);
            scrollTo.mockRestore();
            window.history.replaceState({}, "", original);
        }
    });

    it("ignores invalid stored positions and modified-click navigation", () => {
        const original = window.location.href;
        const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
        try {
            window.history.replaceState({}, "", "/orgs/acme/data-sources");
            for (const saved of [
                "invalid JSON",
                JSON.stringify({ url: window.location.pathname, y: -1 }),
                JSON.stringify({ url: window.location.pathname, y: "913" }),
            ]) {
                sessionStorage.setItem(key, saved);
                window.dispatchEvent(new Event("load"));
                expect(scrollTo).not.toHaveBeenCalled();
            }
            sessionStorage.removeItem(key);
            document.body.innerHTML =
                '<a href="/orgs/acme/data-sources/warehouse?list_return=%2Forgs%2Facme%2Fdata-sources">Warehouse</a>';
            document.querySelector("a").addEventListener("click", (event) => event.preventDefault());
            document
                .querySelector("a")
                .dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ctrlKey: true }));
            expect(sessionStorage.getItem(key)).toBeNull();
        } finally {
            sessionStorage.removeItem(key);
            scrollTo.mockRestore();
            window.history.replaceState({}, "", original);
        }
    });
});

describe("success popups", () => {
    it("AutoDismissesPopupsInsertedByPartialSwaps", () => {
        vi.useFakeTimers();
        try {
            document.body.innerHTML = `<div id="success" data-success-popup data-auto-dismiss-ms="250">Saved</div>`;

            window.braidplaneInitSuccessPopups(document);

            expect(document.getElementById("success")).not.toBeNull();
            vi.advanceTimersByTime(250);
            expect(document.getElementById("success")).toBeNull();
        } finally {
            vi.useRealTimers();
        }
    });
});

describe("protected action modals", () => {
    it("EnablesSubmitOnlyWhenConfirmationMatchesSlug", () => {
        document.body.innerHTML = `
            <button type="button" data-protected-action-trigger="prod-disable">Disable</button>
            <dialog id="prod-disable" data-protected-action-modal data-confirm-slug="prod">
                <form>
                    <input name="protected_environment_confirmation" data-protected-confirm-input data-confirm-slug="prod" />
                    <button type="submit" data-protected-confirm-submit disabled aria-disabled="true">Disable</button>
                </form>
            </dialog>
        `;

        const dialog = document.getElementById("prod-disable");
        dialog.showModal = vi.fn();
        const input = document.querySelector("[data-protected-confirm-input]");
        const submit = document.querySelector("[data-protected-confirm-submit]");

        window.braidplaneInitProtectedActionModals(document);
        expect(submit.disabled).toBe(true);

        input.value = "pro";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        expect(submit.disabled).toBe(true);

        input.value = "prod";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        expect(submit.disabled).toBe(false);
        expect(submit.getAttribute("aria-disabled")).toBe("false");
    });

    it("OpeningModalClearsStaleConfirmation", () => {
        document.body.innerHTML = `
            <button type="button" data-protected-action-trigger="prod-default">Set default</button>
            <dialog id="prod-default" data-protected-action-modal data-confirm-slug="prod">
                <form>
                    <input name="protected_environment_confirmation" data-protected-confirm-input data-confirm-slug="prod" />
                    <button type="submit" data-protected-confirm-submit>Set default</button>
                </form>
            </dialog>
        `;

        const dialog = document.getElementById("prod-default");
        dialog.showModal = vi.fn();
        const input = document.querySelector("[data-protected-confirm-input]");
        const submit = document.querySelector("[data-protected-confirm-submit]");

        window.braidplaneInitProtectedActionModals(document);
        input.value = "prod";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        expect(submit.disabled).toBe(false);

        document.querySelector("[data-protected-action-trigger]").click();

        expect(dialog.showModal).toHaveBeenCalledTimes(1);
        expect(input.value).toBe("");
        expect(submit.disabled).toBe(true);
    });
});

describe("protected select forms", () => {
    it("OpensModalInsteadOfSubmittingProtectedSelection", () => {
        document.body.innerHTML = `
            <form data-protected-select-form>
                <select name="environment_slug" data-protected-select>
                    <option value="dev">Development</option>
                    <option value="prod" data-protected="true" data-protected-modal="prod-enable" selected>
                        Production
                    </option>
                </select>
            </form>
        `;
        const openModal = vi.spyOn(window, "braidplaneOpenProtectedActionModal").mockImplementation(() => {});

        window.braidplaneInitProtectedSelectForms(document);

        const form = document.querySelector("form");
        const propagatedSubmit = vi.fn();
        form.addEventListener("submit", propagatedSubmit);
        const allowed = form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

        expect(allowed).toBe(false);
        expect(propagatedSubmit).not.toHaveBeenCalled();
        expect(openModal).toHaveBeenCalledWith("prod-enable");
        openModal.mockRestore();
    });

    it("AllowsUnprotectedSelectionToSubmitNormally", () => {
        document.body.innerHTML = `
            <form data-protected-select-form>
                <select name="environment_slug" data-protected-select>
                    <option value="dev" selected>Development</option>
                    <option value="prod" data-protected="true" data-protected-modal="prod-enable">
                        Production
                    </option>
                </select>
            </form>
        `;
        const openModal = vi.spyOn(window, "braidplaneOpenProtectedActionModal").mockImplementation(() => {});

        window.braidplaneInitProtectedSelectForms(document);

        const form = document.querySelector("form");
        const allowed = form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

        expect(allowed).toBe(true);
        expect(openModal).not.toHaveBeenCalled();
        openModal.mockRestore();
    });
});

describe("data source Git auth forms", () => {
    it("SwitchesCredentialFieldsByAuthType", () => {
        document.body.innerHTML = `
            <form data-git-auth-form>
                <select name="git_auth_type" data-git-auth-select>
                    <option value="https" selected>HTTPS</option>
                    <option value="ssh">SSH</option>
                </select>
                <label data-git-auth-fields="https"><input name="git_token" /></label>
                <label data-git-auth-fields="ssh"><textarea name="git_ssh_private_key"></textarea></label>
            </form>
        `;

        window.braidplaneDsInitGitAuthForms(document);

        const select = document.querySelector("[data-git-auth-select]");
        const httpsFields = document.querySelector('[data-git-auth-fields="https"]');
        const sshFields = document.querySelector('[data-git-auth-fields="ssh"]');
        expect(httpsFields.classList.contains("hidden")).toBe(false);
        expect(sshFields.classList.contains("hidden")).toBe(true);

        select.value = "ssh";
        select.dispatchEvent(new Event("change", { bubbles: true }));

        expect(httpsFields.classList.contains("hidden")).toBe(true);
        expect(sshFields.classList.contains("hidden")).toBe(false);
        expect(document.querySelector('input[name="git_token"]').disabled).toBe(true);
        expect(document.querySelector('textarea[name="git_ssh_private_key"]').disabled).toBe(false);
    });

    it("KeepsGitCredentialsHiddenForNonGitCreateType", () => {
        document.body.innerHTML = `
            <form data-data-source-create-form data-git-auth-form>
                <select name="type" data-ds-type-select>
                    <option value="postgres" selected>Postgres</option>
                    <option value="git">Git</option>
                </select>
                <label data-ds-requires-type>
                    <input name="name" />
                </label>
                <label data-ds-fields="postgres">
                    <input name="host" />
                </label>
                <label data-ds-fields="git" data-git-auth-fields="https">
                    <input name="git_token" />
                </label>
                <select name="git_auth_type" data-git-auth-select>
                    <option value="https" selected>HTTPS</option>
                    <option value="ssh">SSH</option>
                </select>
            </form>
        `;

        window.braidplaneDsInitCreateForms(document);
        window.braidplaneDsInitGitAuthForms(document);

        const gitFields = document.querySelector('[data-git-auth-fields="https"]');
        expect(gitFields.classList.contains("hidden")).toBe(true);
        expect(document.querySelector('input[name="git_token"]').disabled).toBe(true);
        expect(document.querySelector('input[name="host"]').disabled).toBe(false);
    });

    it("InitializesCreateFormWhenRootIsTheForm", () => {
        document.body.innerHTML = `
            <form data-data-source-create-form data-git-auth-form>
                <select name="type" data-ds-type-select>
                    <option value="postgres" selected>Postgres</option>
                    <option value="http">HTTP</option>
                </select>
                <label data-ds-requires-type>
                    <input name="name" />
                </label>
                <label data-ds-fields="postgres">
                    <input name="host" />
                </label>
                <label data-ds-fields="http">
                    <input name="base_url" />
                </label>
            </form>
        `;

        const form = document.querySelector("form");
        window.braidplaneDsInitCreateForms(form);

        const postgresFields = document.querySelector('[data-ds-fields="postgres"]');
        const httpFields = document.querySelector('[data-ds-fields="http"]');
        expect(postgresFields.classList.contains("hidden")).toBe(false);
        expect(httpFields.classList.contains("hidden")).toBe(true);
        expect(document.querySelector('input[name="base_url"]').disabled).toBe(true);
    });

    it("KeepsConnectorFieldsScopedAfterConnectionFeedbackSwap", () => {
        document.body.innerHTML = `<form data-data-source-create-form>
            <select data-ds-type-select><option value="postgres">Postgres</option><option value="http" selected>HTTP</option></select>
            <label data-ds-fields="postgres"><input name="host" /></label>
            <label data-ds-fields="http"><input name="base_url" value="https://example.test" /></label>
            <p role="status">Connection tested</p>
        </form>`;
        document.querySelector("form").dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true }));
        expect(document.querySelector("select").value).toBe("http");
        expect(document.querySelector('[data-ds-fields="postgres"]').classList.contains("hidden")).toBe(true);
        expect(document.querySelector('input[name="host"]').disabled).toBe(true);
        expect(document.querySelector('input[name="base_url"]').disabled).toBe(false);
        expect(document.querySelector('input[name="base_url"]').value).toBe("https://example.test");
    });

    it("DefaultsHubSpotCreateToOAuthAndKeepsPrivateTokenAsAlternative", () => {
        document.body.innerHTML = `
            <form data-data-source-create-form data-provider-auth-form>
                <select name="type" data-ds-type-select>
                    <option value="hubspot" selected>HubSpot</option>
                    <option value="postgres">Postgres</option>
                </select>
                <select name="hubspot_auth_type" data-provider-auth-select="hubspot">
                    <option value="oauth2" selected>OAuth 2.0</option>
                    <option value="private_app">Private app token</option>
                </select>
                <div data-ds-fields="hubspot" data-provider="hubspot" data-provider-auth-fields="oauth2">
                    <button name="connect_hubspot">Connect HubSpot</button>
                </div>
                <label data-ds-fields="hubspot" data-provider="hubspot" data-provider-auth-fields="private_app">
                    <input name="private_app_token" data-required-for="hubspot" />
                </label>
                <div data-ds-requires-type data-static-data-source-actions>
                    <button name="test_connection">Test connection</button>
                </div>
            </form>
        `;

        window.braidplaneDsInitCreateForms(document);
        window.braidplaneDsInitProviderAuthForms(document);

        const authSelect = document.querySelector('[data-provider-auth-select="hubspot"]');
        const oauthFields = document.querySelector('[data-provider="hubspot"][data-provider-auth-fields="oauth2"]');
        const privateFields = document.querySelector(
            '[data-provider="hubspot"][data-provider-auth-fields="private_app"]',
        );
        const privateToken = document.querySelector('input[name="private_app_token"]');
        const staticActions = document.querySelector("[data-static-data-source-actions]");

        expect(oauthFields.classList.contains("hidden")).toBe(false);
        expect(privateFields.classList.contains("hidden")).toBe(true);
        expect(privateToken.disabled).toBe(true);
        expect(privateToken.required).toBe(false);
        expect(staticActions.classList.contains("hidden")).toBe(true);

        authSelect.value = "private_app";
        authSelect.dispatchEvent(new Event("change", { bubbles: true }));

        expect(oauthFields.classList.contains("hidden")).toBe(true);
        expect(privateFields.classList.contains("hidden")).toBe(false);
        expect(privateToken.disabled).toBe(false);
        expect(privateToken.required).toBe(true);
        expect(staticActions.classList.contains("hidden")).toBe(false);
    });

    it("KeepsBigQueryOAuthOnlyAndHidesStaticActions", () => {
        document.body.innerHTML = `
            <form data-data-source-create-form data-provider-auth-form>
                <select name="type" data-ds-type-select>
                    <option value="bigquery" selected>BigQuery</option>
                    <option value="postgres">Postgres</option>
                </select>
                <div data-ds-fields="bigquery" data-provider="bigquery">
                    <button name="connect_bigquery">Connect with Google</button>
                </div>
                <div data-ds-requires-type data-static-data-source-actions>
                    <button name="test_connection" data-ds-fields="postgres">Test connection</button>
                </div>
            </form>
        `;

        window.braidplaneDsInitCreateForms(document);
        window.braidplaneDsInitProviderAuthForms(document);

        expect(document.querySelector('[name="connect_bigquery"]').disabled).toBe(false);
        expect(document.querySelector("[data-static-data-source-actions]").classList.contains("hidden")).toBe(true);
    });

    it.each([true, false])("RequiresSharedGitLabConnectionBeforeCreate (%s)", (connected) => {
        document.body.innerHTML = `
            <form data-data-source-create-form data-provider-auth-form data-gitlab-connection-available="${connected}">
                <select name="type" data-ds-type-select>
                    <option value="gitlab" selected>GitLab</option>
                    <option value="git">Generic Git</option>
                </select>
                <label data-ds-fields="gitlab"><select name="gitlab_project_id" data-required-for="gitlab"><option value="42">acme/data</option></select></label>
                <div data-ds-requires-type data-static-data-source-actions>
                    <button name="create">Create data source</button>
                </div>
            </form>`;
        window.braidplaneDsInitCreateForms(document);
        window.braidplaneDsInitProviderAuthForms(document);
        expect(document.querySelector('button[name="create"]').disabled).toBe(!connected);
        expect(document.querySelector('[name="gitlab_project_id"]').required).toBe(true);
        const type = document.querySelector("[data-ds-type-select]");
        type.value = "git";
        type.dispatchEvent(new Event("change", { bubbles: true }));
        expect(document.querySelector('button[name="create"]').disabled).toBe(false);
        expect(document.querySelector('[name="gitlab_project_id"]').disabled).toBe(true);
    });

    it("KeepsStaticActionsDisabledUntilADataSourceTypeIsSelected", () => {
        document.body.innerHTML = `
            <form data-data-source-create-form data-provider-auth-form>
                <select name="type" data-ds-type-select>
                    <option value="" selected>Select a type</option>
                    <option value="gitlab">GitLab</option>
                </select>
                <div data-ds-requires-type data-static-data-source-actions>
                    <button name="test_connection">Test connection</button>
                </div>
            </form>
        `;

        window.braidplaneDsInitCreateForms(document);
        window.braidplaneDsInitProviderAuthForms(document);

        const staticActions = document.querySelector("[data-static-data-source-actions]");
        expect(staticActions.classList.contains("hidden")).toBe(true);
        expect(document.querySelector('button[name="test_connection"]').disabled).toBe(true);
    });

    it("DefaultsSnowflakeToWorkloadIdentityAndRequiresPasswordFieldsOnlyWhenSelected", () => {
        document.body.innerHTML = `
            <form data-data-source-create-form data-snowflake-auth-form>
                <select name="type" data-ds-type-select>
                    <option value="snowflake" selected>Snowflake</option>
                    <option value="postgres">Postgres</option>
                </select>
                <select name="snowflake_auth_type" data-snowflake-auth-select>
                    <option value="workload_identity" selected>Workload identity</option>
                    <option value="password">Password</option>
                </select>
                <div data-snowflake-auth-fields="workload_identity">WIF</div>
                <label data-snowflake-auth-fields="password">
                    <input name="username" data-required-for="postgres,snowflake" />
                </label>
            </form>
        `;

        window.braidplaneDsInitCreateForms(document);
        window.braidplaneDsInitSnowflakeAuthForms(document);

        const authSelect = document.querySelector("[data-snowflake-auth-select]");
        const wifFields = document.querySelector('[data-snowflake-auth-fields="workload_identity"]');
        const passwordFields = document.querySelector('[data-snowflake-auth-fields="password"]');
        const username = document.querySelector('input[name="username"]');
        expect(wifFields.classList.contains("hidden")).toBe(false);
        expect(passwordFields.classList.contains("hidden")).toBe(true);
        expect(username.disabled).toBe(true);
        expect(username.required).toBe(false);

        authSelect.value = "password";
        authSelect.dispatchEvent(new Event("change", { bubbles: true }));
        expect(wifFields.classList.contains("hidden")).toBe(true);
        expect(passwordFields.classList.contains("hidden")).toBe(false);
        expect(username.disabled).toBe(false);
        expect(username.required).toBe(true);

        const typeSelect = document.querySelector("[data-ds-type-select]");
        typeSelect.value = "postgres";
        typeSelect.dispatchEvent(new Event("change", { bubbles: true }));
        expect(passwordFields.classList.contains("hidden")).toBe(false);
        expect(username.disabled).toBe(false);
        expect(username.required).toBe(true);
    });

    it("InitializesSnowflakeAuthAfterInnerFieldsAreSwapped", () => {
        document.body.innerHTML = `
            <form data-snowflake-auth-form>
                <div id="ds-fields">
                    <select name="snowflake_auth_type" data-snowflake-auth-select>
                        <option value="workload_identity" selected>Workload identity</option>
                        <option value="password">Password</option>
                    </select>
                    <div data-snowflake-auth-fields="workload_identity">WIF</div>
                    <label data-snowflake-auth-fields="password">
                        <input name="username" data-required-for="snowflake" />
                    </label>
                </div>
            </form>
        `;

        const fields = document.querySelector("#ds-fields");
        fields.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true }));

        const authSelect = document.querySelector("[data-snowflake-auth-select]");
        const passwordFields = document.querySelector('[data-snowflake-auth-fields="password"]');
        const username = document.querySelector('input[name="username"]');
        expect(passwordFields.classList.contains("hidden")).toBe(true);
        expect(username.disabled).toBe(true);
        expect(username.required).toBe(false);

        authSelect.value = "password";
        authSelect.dispatchEvent(new Event("change", { bubbles: true }));
        expect(passwordFields.classList.contains("hidden")).toBe(false);
        expect(username.disabled).toBe(false);
        expect(username.required).toBe(true);
    });

    it("SendsCSRFHeaderWhenParsingConnectionURI", async () => {
        document.head.innerHTML = '<meta name="csrf-token" content="csrf-token">';
        document.body.innerHTML = `
            <form data-data-source-create-form data-parse-uri-url="/orgs/acme-hq/data-sources/parse-uri">
                <select name="type" data-ds-type-select>
                    <option value="postgres" selected>Postgres</option>
                </select>
                <input id="data-source-uri" value="postgres://user:secret@db.example.com:5432/app" />
                <button id="data-source-uri-parse-btn" type="button">Parse URI</button>
                <p id="data-source-uri-parse-message"></p>
                <label data-ds-requires-type>
                    <input name="name" />
                </label>
                <label data-ds-fields="postgres">
                    <input name="host" />
                </label>
            </form>
        `;

        const originalFetch = globalThis.fetch;
        const fetchMock = vi.fn().mockResolvedValue({
            json: () =>
                Promise.resolve({
                    success: true,
                    message: "URI parsed.",
                    fields: { host: "db.example.com" },
                }),
        });
        globalThis.fetch = fetchMock;

        try {
            window.braidplaneDsInitCreateForms(document);
            document.getElementById("data-source-uri-parse-btn").click();

            await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
            const [url, options] = fetchMock.mock.calls[0];
            expect(url).toBe("data-sources/parse-uri");
            expect(options.headers["X-CSRF-Token"]).toBe("csrf-token");
            expect(options.headers["X-Requested-With"]).toBe("XMLHttpRequest");
            expect(options.body).toContain("type=postgres");
        } finally {
            globalThis.fetch = originalFetch;
        }
    });
});

describe("entity quick-look focus", () => {
    it("FocusesTheRequestedHeadingOrOpenerWithoutAffectingPolling", () => {
        document.body.innerHTML = '<a id="opener" href="#">Quick look</a><h2 id="preview" tabindex="-1">Selected</h2>';
        const opener = document.getElementById("opener");
        opener.dataset.entityFocus = "preview";
        document.getElementById("preview").setAttribute("data-entity-preview", "");
        opener.focus();
        document.dispatchEvent(
            new CustomEvent("htmx:afterSettle", {
                detail: { requestConfig: { elt: opener, triggeringEvent: { detail: 1, pointerType: "mouse" } } },
            }),
        );
        expect(document.activeElement.id).toBe("opener");
        document.dispatchEvent(
            new CustomEvent("htmx:afterSettle", {
                detail: { requestConfig: { elt: opener, triggeringEvent: { detail: 1, pointerType: "" } } },
            }),
        );
        expect(document.activeElement.id).toBe("preview");
        document.dispatchEvent(new CustomEvent("htmx:afterSettle", { detail: {} }));
        expect(document.activeElement.id).toBe("preview");
        const close = document.createElement("a");
        close.dataset.entityFocus = "opener";
        document.dispatchEvent(new CustomEvent("htmx:afterSettle", { detail: { requestConfig: { elt: close } } }));
        expect(document.activeElement.id).toBe("opener");
        document.getElementById("preview").remove();
        document.dispatchEvent(new CustomEvent("htmx:afterSettle", { detail: { requestConfig: { elt: opener } } }));
        expect(document.activeElement.id).toBe("opener");
    });
});

describe("quick-look partial selection", () => {
    it("updates the retained inventory and clears selection on close", () => {
        document.body.innerHTML = `<div data-entity-layout><div>
            <div data-entity-list-item data-selected class="entity-selected"><a id="first" data-entity-focus="heading" aria-current="true"></a></div>
            <div data-entity-list-item><a id="second" data-entity-focus="heading" aria-current="false"></a></div>
        </div><aside><div id="panel"><a id="close" data-entity-focus="second"></a></div></aside></div>`;
        const second = document.getElementById("second");
        const panel = document.getElementById("panel");
        document.dispatchEvent(
            new CustomEvent("htmx:afterSwap", { detail: { target: panel, requestConfig: { elt: second } } }),
        );
        expect(second.getAttribute("aria-current")).toBe("true");
        expect(second.parentElement.hasAttribute("data-selected")).toBe(true);
        expect(document.getElementById("first").getAttribute("aria-current")).toBe("false");
        const close = document.getElementById("close");
        const replacement = document.createElement("div");
        replacement.textContent = "Select an item";
        panel.replaceWith(replacement);
        replacement.dispatchEvent(
            new CustomEvent("htmx:afterSwap", {
                bubbles: true,
                detail: { target: panel, requestConfig: { elt: close } },
            }),
        );
        expect(document.querySelectorAll("[data-selected]").length).toBe(0);
    });
});

describe("HTTP OAuth authentication fields", () => {
    it("EnablesOnlyTheSelectedGrantAndDisablesStaleStaticCredentials", () => {
        document.body.innerHTML = `<form><select name="type"><option value="http">HTTP</option><option value="postgres">Postgres</option></select>
          <div data-http-auth><select data-http-auth-select><option value="static">Static</option><option value="client_credentials">Machine</option><option value="authorization_code">Code</option></select>
          <div data-http-auth-fields="static"><input name="bearer_token" value="stale-token"></div>
          <div data-http-auth-fields="client_credentials,authorization_code"><input name="http_oauth_client_secret" data-http-required="client_credentials"><div data-http-auth-fields="authorization_code"><input name="http_oauth_authorization_endpoint" data-http-required="authorization_code"></div></div></div></form>`;
        const mode = document.querySelector("[data-http-auth-select]");
        const bearer = document.querySelector('[name="bearer_token"]');
        const secret = document.querySelector('[name="http_oauth_client_secret"]');
        const endpoint = document.querySelector('[name="http_oauth_authorization_endpoint"]');
        window.braidplaneDsInitHTTPAuthFields(document);
        expect(bearer.disabled).toBe(false);
        expect(secret.disabled).toBe(true);
        mode.value = "client_credentials";
        mode.dispatchEvent(new Event("change"));
        expect(bearer.disabled).toBe(true);
        expect(secret.required).toBe(true);
        expect(endpoint.disabled).toBe(true);
        mode.value = "authorization_code";
        mode.dispatchEvent(new Event("change"));
        expect(secret.required).toBe(false);
        expect(endpoint.disabled).toBe(false);
        expect(endpoint.required).toBe(true);
        document.querySelector('[name="type"]').value = "postgres";
        window.braidplaneDsInitHTTPAuthFields(document);
        expect(mode.disabled).toBe(true);
        expect(secret.disabled).toBe(true);
        expect(endpoint.required).toBe(false);
    });
});

describe("HTTP static URI credentials", () => {
    it("PopulatesEnabledHTTPCredentialsInsteadOfHiddenDatabaseFields", async () => {
        document.body.innerHTML = `<form data-data-source-create-form data-parse-uri-url="/orgs/acme/data-sources/parse-uri">
          <select name="type" data-ds-type-select><option value="http" selected>HTTP</option></select>
          <input id="data-source-uri" value="https://user:synthetic@api.example.com" />
          <button id="data-source-uri-parse-btn" type="button">Parse URI</button><p id="data-source-uri-parse-message"></p>
          <div data-ds-fields="postgres"><input name="username" id="database-user"/><input name="password"/></div>
          <div data-ds-fields="http"><input name="username" id="http-user"/><input name="password" id="http-password"/></div></form>`;
        const originalFetch = globalThis.fetch;
        globalThis.fetch = vi.fn().mockResolvedValue({
            json: async () => ({
                success: true,
                type: "http",
                fields: { username: "user", password: "synthetic" },
            }),
        });
        try {
            window.braidplaneDsInitCreateForms(document);
            document.getElementById("data-source-uri-parse-btn").click();
            await vi.waitFor(() => expect(document.getElementById("http-user").value).toBe("user"));
            expect(document.getElementById("http-password").value).toBe("synthetic");
            expect(document.getElementById("database-user").value).toBe("");
        } finally {
            globalThis.fetch = originalFetch;
        }
    });
});

describe("alert destination configuration", () => {
    it("submits only the selected adapter fields after type changes and HTMX initialization", () => {
        document.body.innerHTML = `<form data-alert-destination-form>
          <select data-alert-destination-type><option value="email">Email</option><option value="slack">Slack</option></select>
          <div data-alert-destination-fields="email"><input name="to" value="ops@example.com"></div>
          <div data-alert-destination-fields="slack"><select name="connection_slug"><option value="ops">Operations Slack</option></select></div>
        </form>`;
        const form = document.querySelector("form");
        const type = form.querySelector("[data-alert-destination-type]");
        window.braidplaneInitAlertDestinationForms(form);
        expect(form.querySelector("[name=to]").disabled).toBe(false);
        expect(form.querySelector("[name=connection_slug]").disabled).toBe(true);
        type.value = "slack";
        type.dispatchEvent(new Event("change"));
        expect(form.querySelector("[name=to]").disabled).toBe(true);
        expect(form.querySelector("[name=connection_slug]").required).toBe(true);
        expect(Object.fromEntries(new FormData(form))).toEqual({ connection_slug: "ops" });
        window.braidplaneInitAlertDestinationForms(form);
        type.value = "email";
        type.dispatchEvent(new Event("change"));
        expect(Object.fromEntries(new FormData(form))).toEqual({ to: "ops@example.com" });
    });
});

describe("alert policy review", () => {
    it("describes Any lifecycle, organization breadth, source restrictions and disabled destinations without implying delivery", () => {
        document.body.innerHTML = `
            <form data-alert-policy-form>
                <select name="scope" data-alert-policy-scope-select><option value="organization">Organization</option></select>
                <select name="notification_type"><option value="pipeline.retry_exhausted">Pipeline retry exhausted</option></select>
                <select name="severity"><option value="">Any severity</option><option value="warning" selected>Warning</option></select>
                <select name="lifecycle_state"><option value="">Any lifecycle</option><option value="resolved">Resolved</option></select>
                <select name="source_kind"><option value="pipeline_run">Pipeline run</option></select>
                <select name="source_action"><option value="execute">Execute</option></select>
                <select name="source_ref"><option value="run-original">run-original</option></select>
                <input name="destination_slug" type="checkbox" value="ops" data-destination-label="Operations" data-destination-enabled="Disabled" checked>
                <input name="enabled" type="checkbox" checked>
                <p data-alert-policy-review></p>
            </form>`;
        window.braidplaneInitAlertPolicyForms(document);
        const review = document.querySelector("[data-alert-policy-review]");
        expect(review.textContent).toContain("all projects and environments");
        expect(review.textContent).toContain("Any lifecycle");
        expect(review.textContent).toContain("Operations (Disabled)");
        expect(review.textContent).toContain("Pipeline run; Execute; run-original");
        expect(review.textContent).toContain("with severity Warning");
        const severity = document.querySelector('[name="severity"]');
        severity.value = "";
        severity.dispatchEvent(new Event("change", { bubbles: true }));
        expect(review.textContent).not.toContain("with severity");
        const enabled = document.querySelector('[name="enabled"]');
        enabled.checked = false;
        enabled.dispatchEvent(new Event("change", { bubbles: true }));
        expect(review.textContent).toMatch(/^Disabled:/);
        // Reinitializing after a selector swap updates the enclosing form's review.
        const selector = document.querySelector('[name="lifecycle_state"]');
        selector.value = "resolved";
        window.braidplaneInitAlertPolicyForms(selector);
        expect(review.textContent).toContain("Resolved");
    });
});
