window.braidplaneToggleSection = (btn) => {
    const id = btn.getAttribute("data-toggle-target");
    const target = id ? document.getElementById(id) : null;
    if (!target) return;
    const nowHidden = target.classList.toggle("hidden");
    btn.setAttribute("aria-expanded", String(!nowHidden));
};

const braidplaneInteractiveFieldTypes = new Set([
    "checkbox",
    "color",
    "date",
    "datetime-local",
    "email",
    "month",
    "number",
    "password",
    "radio",
    "range",
    "search",
    "tel",
    "text",
    "time",
    "url",
    "week",
]);

const braidplaneFormFields = (form) =>
    Array.from(form.elements).filter((field) => {
        if (
            !(field instanceof HTMLInputElement) &&
            !(field instanceof HTMLSelectElement) &&
            !(field instanceof HTMLTextAreaElement)
        ) {
            return false;
        }
        if (field.type === "hidden" || field.type === "submit" || field.type === "button") {
            return false;
        }
        if (field instanceof HTMLInputElement && !braidplaneInteractiveFieldTypes.has(field.type)) {
            return false;
        }
        return !field.disabled;
    });

const braidplaneFormSnapshot = (form) =>
    JSON.stringify(
        braidplaneFormFields(form).map((field) => {
            if (field instanceof HTMLInputElement && (field.type === "checkbox" || field.type === "radio")) {
                return [field.name, field.checked];
            }
            return [field.name, field.value];
        }),
    );

const braidplaneDirtySubmitButtons = (form) => {
    const buttons = Array.from(form.querySelectorAll("[data-dirty-submit]")).filter(
        (button) => button instanceof HTMLButtonElement,
    );
    if (form.id) {
        document.querySelectorAll("[data-dirty-submit][form]").forEach((button) => {
            if (button instanceof HTMLButtonElement && button.getAttribute("form") === form.id) {
                buttons.push(button);
            }
        });
    }
    return buttons;
};

const braidplaneSelectWithRoot = (root, selector) => {
    const scope = root instanceof HTMLElement || root instanceof Document ? root : document;
    const matches = [];
    if (scope instanceof Element && scope.matches(selector)) {
        matches.push(scope);
    }
    scope.querySelectorAll(selector).forEach((node) => {
        matches.push(node);
    });
    return matches;
};

window.braidplaneInitDirtyForms = (root) => {
    braidplaneSelectWithRoot(root, "[data-dirty-form]").forEach((form) => {
        if (!(form instanceof HTMLFormElement) || form.dataset.dirtyReady === "true") {
            return;
        }

        form.dataset.dirtyReady = "true";
        // Use rendered defaults so typing before initialization remains an edit.
        const baseline = document.createElement("form");
        braidplaneFormFields(form).forEach((field) => {
            const copy = field.cloneNode(true);
            copy.removeAttribute("form");
            baseline.append(copy);
        });
        baseline.reset();
        const initial = form.getAttribute("data-dirty-baseline") || braidplaneFormSnapshot(baseline);
        const sync = () => {
            const isPristine = !form.hasAttribute("data-dirty-unsaved") && braidplaneFormSnapshot(form) === initial;
            braidplaneDirtySubmitButtons(form).forEach((button) => {
                button.disabled = isPristine || button.dataset.braidplanePendingDisabled !== undefined;
                button.setAttribute("aria-disabled", String(button.disabled));
            });
        };

        form.addEventListener("input", sync);
        form.addEventListener("change", sync);
        form.addEventListener("braidplane:form-mutated", sync);
        sync();
    });
};

const braidplaneTypeSet = (value) =>
    (value || "")
        .split(",")
        .map((item) => item.trim())
        .filter((item) => item.length > 0);

const braidplaneSetFormControlEnabled = (container, enabled) => {
    container.querySelectorAll("input, select, textarea, button").forEach((field) => {
        if (
            field instanceof HTMLInputElement ||
            field instanceof HTMLSelectElement ||
            field instanceof HTMLTextAreaElement ||
            field instanceof HTMLButtonElement
        ) {
            field.disabled = !enabled;
        }
    });
};

const braidplaneUpdateGitAuthVisibility = (form) => {
    const authSelect = form.querySelector("[data-git-auth-select]");
    if (!(authSelect instanceof HTMLSelectElement)) {
        return;
    }
    const selectedAuth = authSelect.value || "https";
    const typeSelect = form.querySelector("[data-ds-type-select]");
    const selectedType = typeSelect instanceof HTMLSelectElement ? typeSelect.value : "";

    form.querySelectorAll("[data-git-auth-fields]").forEach((group) => {
        const authVisible = braidplaneTypeSet(group.getAttribute("data-git-auth-fields")).includes(selectedAuth);
        const dataSourceTypes = braidplaneTypeSet(group.getAttribute("data-ds-fields"));
        const typeVisible = dataSourceTypes.length === 0 || dataSourceTypes.includes(selectedType);
        const visible = authVisible && typeVisible;
        group.classList.toggle("hidden", !visible);
        braidplaneSetFormControlEnabled(group, visible);
    });
};

window.braidplaneDsInitGitAuthForms = (root) => {
    braidplaneSelectWithRoot(root, "[data-git-auth-form]").forEach((form) => {
        if (!(form instanceof HTMLFormElement)) {
            return;
        }
        const authSelect = form.querySelector("[data-git-auth-select]");
        if (authSelect instanceof HTMLSelectElement && authSelect.dataset.gitAuthBound !== "true") {
            authSelect.dataset.gitAuthBound = "true";
            authSelect.addEventListener("change", () => braidplaneUpdateGitAuthVisibility(form));
        }
        braidplaneUpdateGitAuthVisibility(form);
    });
};

window.braidplaneDsInitHTTPAuthFields = (root) => {
    braidplaneSelectWithRoot(root, "[data-http-auth]").forEach((section) => {
        const select = section.querySelector("[data-http-auth-select]");
        if (!(select instanceof HTMLSelectElement)) return;
        const update = () => {
            const type = section.closest("form")?.querySelector('[name="type"]');
            const active = !type || type.value === "http";
            select.disabled = !active;
            section.querySelectorAll("[data-http-auth-fields]").forEach((group) => {
                const visible = active && braidplaneTypeSet(group.dataset.httpAuthFields).includes(select.value);
                group.classList.toggle("hidden", !visible);
                braidplaneSetFormControlEnabled(group, visible);
            });
            section.querySelectorAll("[data-http-required]").forEach((field) => {
                field.required =
                    !field.disabled && braidplaneTypeSet(field.dataset.httpRequired).includes(select.value);
            });
        };
        if (select.dataset.httpAuthBound !== "true") {
            select.dataset.httpAuthBound = "true";
            select.addEventListener("change", update);
        }
        update();
    });
};

const braidplaneUpdateProviderAuthVisibility = (form) => {
    const typeSelect = form.querySelector("[data-ds-type-select]");
    const selectedType = typeSelect instanceof HTMLSelectElement ? typeSelect.value : "";
    const authSelect = form.querySelector(`[data-provider-auth-select="${selectedType}"]`);
    const selectedAuth = authSelect instanceof HTMLSelectElement ? authSelect.value || "oauth2" : "";

    form.querySelectorAll("[data-provider-auth-fields]").forEach((group) => {
        const provider = group.getAttribute("data-provider") || "";
        const visible =
            provider === selectedType &&
            braidplaneTypeSet(group.getAttribute("data-provider-auth-fields")).includes(selectedAuth);
        group.classList.toggle("hidden", !visible);
        braidplaneSetFormControlEnabled(group, visible);

        group.querySelectorAll("[data-required-for]").forEach((field) => {
            if (
                field instanceof HTMLInputElement ||
                field instanceof HTMLSelectElement ||
                field instanceof HTMLTextAreaElement
            ) {
                field.required =
                    visible && braidplaneTypeSet(field.getAttribute("data-required-for")).includes(selectedType);
            }
        });
    });

    form.querySelectorAll("[data-static-data-source-actions]").forEach((group) => {
        const visible =
            selectedType !== "" &&
            selectedType !== "bigquery" &&
            selectedType !== "google_workspace" &&
            !(selectedType === "github" && form.dataset.githubConnectionAvailable !== "true") &&
            !(selectedType === "gitlab" && form.dataset.gitlabConnectionAvailable !== "true") &&
            (!(authSelect instanceof HTMLSelectElement) || selectedAuth !== "oauth2");
        group.classList.toggle("hidden", !visible);
        braidplaneSetFormControlEnabled(group, visible);
        group.querySelectorAll("[data-ds-fields]").forEach((action) => {
            const actionVisible =
                visible && braidplaneTypeSet(action.getAttribute("data-ds-fields")).includes(selectedType);
            action.classList.toggle("hidden", !actionVisible);
            braidplaneSetFormControlEnabled(action, actionVisible);
            if (action instanceof HTMLButtonElement) {
                action.disabled = !actionVisible;
            }
        });
    });

    window.braidplaneDsInitHTTPAuthFields(form);

    if (selectedType !== "gitlab" || selectedAuth !== "oauth2") {
        braidplaneUpdateGitAuthVisibility(form);
    }
};

window.braidplaneDsInitProviderAuthForms = (root) => {
    braidplaneSelectWithRoot(root, "[data-provider-auth-form]").forEach((form) => {
        if (!(form instanceof HTMLFormElement)) {
            return;
        }
        form.querySelectorAll("[data-provider-auth-select]").forEach((authSelect) => {
            if (authSelect instanceof HTMLSelectElement && authSelect.dataset.providerAuthBound !== "true") {
                authSelect.dataset.providerAuthBound = "true";
                authSelect.addEventListener("change", () => braidplaneUpdateProviderAuthVisibility(form));
            }
        });
        braidplaneUpdateProviderAuthVisibility(form);
    });
};

const braidplaneUpdateSnowflakeAuthVisibility = (form) => {
    const authSelect = form.querySelector("[data-snowflake-auth-select]");
    if (!(authSelect instanceof HTMLSelectElement)) {
        return;
    }
    const selectedAuth = authSelect.value || "workload_identity";
    const typeSelect = form.querySelector("[data-ds-type-select]");
    const selectedType = typeSelect instanceof HTMLSelectElement ? typeSelect.value : "snowflake";
    const isSnowflake = selectedType === "snowflake";

    form.querySelectorAll("[data-snowflake-auth-fields]").forEach((group) => {
        const dataSourceTypes = braidplaneTypeSet(group.getAttribute("data-ds-fields"));
        const supportsSelectedType = dataSourceTypes.length === 0 || dataSourceTypes.includes(selectedType);
        const visible =
            supportsSelectedType &&
            (!isSnowflake ||
                braidplaneTypeSet(group.getAttribute("data-snowflake-auth-fields")).includes(selectedAuth));
        group.classList.toggle("hidden", !visible);
        braidplaneSetFormControlEnabled(group, visible);
        group.querySelectorAll("[data-required-for]").forEach((field) => {
            if (
                field instanceof HTMLInputElement ||
                field instanceof HTMLSelectElement ||
                field instanceof HTMLTextAreaElement
            ) {
                field.required =
                    visible && braidplaneTypeSet(field.getAttribute("data-required-for")).includes(selectedType);
            }
        });
    });
};

window.braidplaneDsInitSnowflakeAuthForms = (root) => {
    braidplaneSelectWithRoot(root, "[data-snowflake-auth-form]").forEach((form) => {
        if (!(form instanceof HTMLFormElement)) {
            return;
        }
        const authSelect = form.querySelector("[data-snowflake-auth-select]");
        if (authSelect instanceof HTMLSelectElement && authSelect.dataset.snowflakeAuthBound !== "true") {
            authSelect.dataset.snowflakeAuthBound = "true";
            authSelect.addEventListener("change", () => braidplaneUpdateSnowflakeAuthVisibility(form));
        }
        braidplaneUpdateSnowflakeAuthVisibility(form);
    });
};

window.braidplaneDsInitCreateForms = (root) => {
    braidplaneSelectWithRoot(root, "[data-data-source-create-form]").forEach((form) => {
        if (!(form instanceof HTMLFormElement)) {
            return;
        }

        const typeSelect = form.querySelector("[data-ds-type-select]");
        if (!(typeSelect instanceof HTMLSelectElement)) {
            return;
        }

        const parseURIInput = form.querySelector("#data-source-uri");
        const parseURIToggleButton = form.querySelector("#data-source-uri-toggle-btn");
        const parseURIPanel = form.querySelector("#data-source-uri-panel");
        const parseURIMessage = form.querySelector("#data-source-uri-parse-message");

        const updateVisibility = () => {
            const selectedType = typeSelect.value;
            const hasType = selectedType !== "";

            form.querySelectorAll("[data-ds-requires-type]").forEach((group) => {
                group.classList.toggle("hidden", !hasType);
                braidplaneSetFormControlEnabled(group, hasType);
            });

            form.querySelectorAll("[data-ds-fields]").forEach((group) => {
                const visible =
                    hasType && braidplaneTypeSet(group.getAttribute("data-ds-fields")).includes(selectedType);
                group.classList.toggle("hidden", !visible);
                braidplaneSetFormControlEnabled(group, visible);

                group.querySelectorAll("input, select, textarea").forEach((field) => {
                    if (
                        !(field instanceof HTMLInputElement) &&
                        !(field instanceof HTMLSelectElement) &&
                        !(field instanceof HTMLTextAreaElement)
                    ) {
                        return;
                    }
                    if (!visible) {
                        field.required = false;
                        return;
                    }

                    const requiredFor = field.getAttribute("data-required-for") || "";
                    if (requiredFor !== "") {
                        field.required = braidplaneTypeSet(requiredFor).includes(selectedType);
                    }
                });
            });

            braidplaneUpdateGitAuthVisibility(form);
            braidplaneUpdateProviderAuthVisibility(form);
            braidplaneUpdateSnowflakeAuthVisibility(form);
        };

        const setParseMessage = (message, success) => {
            if (!(parseURIMessage instanceof HTMLElement)) {
                return;
            }

            if (!message) {
                parseURIMessage.textContent = "";
                parseURIMessage.classList.add("hidden");
                parseURIMessage.classList.remove("text-success", "text-error");
                return;
            }

            parseURIMessage.textContent = message;
            parseURIMessage.classList.remove("hidden");
            parseURIMessage.classList.toggle("text-success", !!success);
            parseURIMessage.classList.toggle("text-error", !success);
        };

        const setFieldValue = (name, value) => {
            const normalizedValue = value == null ? "" : String(value);
            const target = form.querySelector(`[name="${name}"]:not(:disabled)`);
            if (
                !(target instanceof HTMLInputElement) &&
                !(target instanceof HTMLTextAreaElement) &&
                !(target instanceof HTMLSelectElement)
            ) {
                return;
            }
            target.value = normalizedValue;
        };

        const applyParsedFields = (payload) => {
            if (!payload || typeof payload !== "object") {
                return;
            }

            if (typeof payload.type === "string" && payload.type !== "") {
                typeSelect.value = payload.type;
                updateVisibility();
            }

            const fields = payload.fields;
            if (!fields || typeof fields !== "object") {
                return;
            }

            setFieldValue("host", fields.host || "");
            setFieldValue("port", fields.port || "");
            setFieldValue("database", fields.database || "");
            setFieldValue("sslmode", fields.sslmode || "");
            setFieldValue("username", fields.username || "");
            setFieldValue("password", fields.password || "");
            setFieldValue("auth_database", fields.auth_database || "");
            setFieldValue("base_url", fields.base_url || "");
            setFieldValue("health_path", fields.health_path || "");
            setFieldValue("bearer_token", fields.bearer_token || "");
        };

        const parseURI = async () => {
            if (!(parseURIInput instanceof HTMLInputElement)) {
                return;
            }

            const body = new URLSearchParams();
            body.set("type", typeSelect.value);
            body.set("uri", parseURIInput.value);

            const headers = {
                "Content-Type": "application/x-www-form-urlencoded",
                "X-Requested-With": "XMLHttpRequest",
            };
            const csrfToken =
                form.querySelector('input[name="_csrf"], input[name="csrf_token"]')?.value ||
                document.querySelector('meta[name="csrf-token"]')?.getAttribute("content") ||
                "";
            if (csrfToken !== "") {
                headers["X-CSRF-Token"] = csrfToken;
            }

            try {
                const response = await fetch("data-sources/parse-uri", {
                    method: "POST",
                    headers,
                    body: body.toString(),
                    credentials: "same-origin",
                });

                const payload = await response.json();
                applyParsedFields(payload);
                setParseMessage(payload.message || "", payload.success === true);
            } catch (_) {
                setParseMessage("Could not parse URI right now. Try again.", false);
            }
        };

        if (typeSelect.dataset.dsTypeBound !== "true") {
            typeSelect.dataset.dsTypeBound = "true";
            typeSelect.addEventListener("change", updateVisibility);
        }

        if (
            parseURIToggleButton instanceof HTMLButtonElement &&
            parseURIToggleButton.dataset.uriToggleBound !== "true"
        ) {
            parseURIToggleButton.dataset.uriToggleBound = "true";
            parseURIToggleButton.addEventListener("click", () => {
                if (!(parseURIPanel instanceof HTMLElement)) {
                    return;
                }
                const isHidden = parseURIPanel.classList.contains("hidden");
                parseURIPanel.classList.toggle("hidden", !isHidden);
                parseURIToggleButton.classList.toggle("hidden", isHidden);
                if (isHidden && parseURIInput instanceof HTMLInputElement) {
                    parseURIInput.focus();
                }
            });
        }

        const parseURIButton = form.querySelector("#data-source-uri-parse-btn");
        if (parseURIButton instanceof HTMLButtonElement && parseURIButton.dataset.uriParseBound !== "true") {
            parseURIButton.dataset.uriParseBound = "true";
            parseURIButton.addEventListener("click", () => void parseURI());
        }

        updateVisibility();
    });
};

const braidplaneUpdateAlertPolicyScopeVisibility = (form) => {
    const scopeSelect = form.querySelector("[data-alert-policy-scope-select]");
    if (!(scopeSelect instanceof HTMLSelectElement)) {
        return;
    }
    const selectedScope = scopeSelect.value || "organization";

    form.querySelectorAll("[data-alert-policy-scope-fields]").forEach((group) => {
        const visible = braidplaneTypeSet(group.getAttribute("data-alert-policy-scope-fields")).includes(selectedScope);
        group.classList.toggle("hidden", !visible);
        braidplaneSetFormControlEnabled(group, visible);
    });
};

window.braidplaneInitAlertDestinationForms = (root) => {
    braidplaneSelectWithRoot(root, "[data-alert-destination-form]").forEach((form) => {
        const select = form.querySelector("[data-alert-destination-type]");
        if (!(select instanceof HTMLSelectElement)) return;
        const update = () => {
            form.querySelectorAll("[data-alert-destination-fields]").forEach((group) => {
                const active = group.getAttribute("data-alert-destination-fields") === select.value;
                group.classList.toggle("hidden", !active);
                braidplaneSetFormControlEnabled(group, active);
                group.querySelectorAll("[name='connection_slug']").forEach((field) => {
                    field.required = active;
                });
            });
        };
        if (!select.dataset.alertDestinationBound) {
            select.dataset.alertDestinationBound = "true";
            select.addEventListener("change", update);
        }
        update();
    });
};

const braidplaneUpdateAlertPolicyReview = (form) => {
    const review = form.querySelector("[data-alert-policy-review]");
    if (!review) return;
    const value = (name) => {
        const field = form.elements.namedItem(name);
        if (!field || field.disabled) return "";
        return field instanceof HTMLSelectElement
            ? field.selectedOptions[0]?.textContent.trim() || ""
            : field.value || "";
    };
    const scope = form.elements.namedItem("scope")?.value || "organization";
    const target =
        scope === "organization"
            ? "all projects and environments in this organization"
            : `project ${value("project_slug") || "(select project)"}${scope === "project_environment" ? ` in ${value("environment_slug") || "(select environment)"}` : " in all environments"}`;
    const destinations = Array.from(form.querySelectorAll('[name="destination_slug"]:checked')).map(
        (field) =>
            `${field.dataset.destinationLabel || field.value} (${field.dataset.destinationEnabled || "availability unknown"})`,
    );
    const source = [value("source_kind"), value("source_action"), value("source_ref")].filter(Boolean).join("; ");
    const enabled = form.elements.namedItem("enabled");
    const state = enabled instanceof HTMLInputElement && !enabled.checked ? "Disabled" : "Enabled";
    const severity =
        value("notification_type") && form.elements.namedItem("severity")?.value
            ? ` with severity ${value("severity")}`
            : "";
    review.textContent = `${state}: when ${value("notification_type") || value("severity") || "notifications match the source"}${severity} occurs for ${target}, while attention is ${value("lifecycle_state") || "Any lifecycle"}, route to ${destinations.join(", ") || "(select destinations)"}. Source restrictions: ${source || "Any source"}.`;
};

window.braidplaneInitAlertPolicyForms = (root) => {
    const forms = Array.from(braidplaneSelectWithRoot(root, "[data-alert-policy-form]"));
    const enclosing = root instanceof Element ? root.closest("[data-alert-policy-form]") : null;
    if (enclosing && !forms.includes(enclosing)) forms.push(enclosing);
    forms.forEach((form) => {
        if (!(form instanceof HTMLFormElement)) return;
        if (form.dataset.alertPolicyBound !== "true") {
            form.dataset.alertPolicyBound = "true";
            form.addEventListener("change", () => {
                braidplaneUpdateAlertPolicyScopeVisibility(form);
                braidplaneUpdateAlertPolicyReview(form);
            });
        }
        braidplaneUpdateAlertPolicyScopeVisibility(form);
        braidplaneUpdateAlertPolicyReview(form);
    });
};

const braidplaneSyncProtectedActionModal = (dialog) => {
    const input = dialog.querySelector("[data-protected-confirm-input]");
    const submitButtons = dialog.querySelectorAll("[data-protected-confirm-submit]");
    const expectedSlug = input?.getAttribute("data-confirm-slug") || dialog.getAttribute("data-confirm-slug") || "";
    const confirmed = input instanceof HTMLInputElement && input.value.trim() === expectedSlug;

    submitButtons.forEach((button) => {
        if (!(button instanceof HTMLButtonElement)) {
            return;
        }
        button.disabled = !confirmed;
        button.setAttribute("aria-disabled", String(!confirmed));
    });
};

window.braidplaneOpenProtectedActionModal = (modalID) => {
    const dialog = document.getElementById(modalID || "");
    if (!(dialog instanceof HTMLDialogElement)) {
        return;
    }

    const input = dialog.querySelector("[data-protected-confirm-input]");
    if (input instanceof HTMLInputElement) {
        input.value = "";
    }
    braidplaneSyncProtectedActionModal(dialog);
    dialog.showModal();
    if (input instanceof HTMLInputElement) {
        input.focus();
    }
};

window.braidplaneInitProtectedActionModals = (root) => {
    braidplaneSelectWithRoot(root, "[data-protected-action-modal]").forEach((dialog) => {
        if (!(dialog instanceof HTMLDialogElement) || dialog.dataset.protectedActionReady === "true") {
            return;
        }

        dialog.dataset.protectedActionReady = "true";
        const input = dialog.querySelector("[data-protected-confirm-input]");
        if (input instanceof HTMLInputElement) {
            input.addEventListener("input", () => braidplaneSyncProtectedActionModal(dialog));
        }
        dialog.addEventListener("close", () => {
            if (input instanceof HTMLInputElement) {
                input.value = "";
            }
            braidplaneSyncProtectedActionModal(dialog);
        });
        braidplaneSyncProtectedActionModal(dialog);
    });

    braidplaneSelectWithRoot(root, "[data-protected-action-trigger]").forEach((trigger) => {
        if (!(trigger instanceof HTMLElement) || trigger.dataset.protectedActionTriggerReady === "true") {
            return;
        }

        trigger.dataset.protectedActionTriggerReady = "true";
        trigger.addEventListener("click", (event) => {
            event.preventDefault();
            window.braidplaneOpenProtectedActionModal(trigger.getAttribute("data-protected-action-trigger"));
        });
    });

    braidplaneSelectWithRoot(root, "[data-protected-action-close]").forEach((trigger) => {
        if (!(trigger instanceof HTMLElement) || trigger.dataset.protectedActionCloseReady === "true") {
            return;
        }

        trigger.dataset.protectedActionCloseReady = "true";
        trigger.addEventListener("click", (event) => {
            event.preventDefault();
            const dialog = document.getElementById(trigger.getAttribute("data-protected-action-close") || "");
            if (dialog instanceof HTMLDialogElement) {
                dialog.close();
            }
        });
    });
};

window.braidplaneInitProtectedSelectForms = (root) => {
    braidplaneSelectWithRoot(root, "[data-protected-select-form]").forEach((form) => {
        if (!(form instanceof HTMLFormElement) || form.dataset.protectedSelectReady === "true") {
            return;
        }

        form.dataset.protectedSelectReady = "true";
        form.addEventListener(
            "submit",
            (event) => {
                const select = form.querySelector("[data-protected-select]");
                if (!(select instanceof HTMLSelectElement)) {
                    return;
                }

                const option = select.selectedOptions[0];
                if (!(option instanceof HTMLOptionElement) || option.dataset.protected !== "true") {
                    return;
                }

                const modalID = option.getAttribute("data-protected-modal") || "";
                if (modalID === "") {
                    return;
                }

                event.preventDefault();
                event.stopImmediatePropagation();
                window.braidplaneOpenProtectedActionModal(modalID);
            },
            { capture: true },
        );
    });
};

window.braidplaneInitSuccessPopups = (root) => {
    braidplaneSelectWithRoot(root, "[data-success-popup][data-auto-dismiss-ms]").forEach((popup) => {
        if (!(popup instanceof HTMLElement) || popup.dataset.autoDismissBound === "true") {
            return;
        }

        popup.dataset.autoDismissBound = "true";
        const delay = Number(popup.dataset.autoDismissMs || "5000");
        window.setTimeout(() => {
            if (popup.isConnected) {
                popup.remove();
            }
        }, delay);
    });
};

window.braidplaneDismissPopup = (trigger) => {
    if (!(trigger instanceof Element)) {
        return;
    }
    const selector = trigger.getAttribute("data-popup-dismiss");
    const popup = selector ? trigger.closest(selector) : trigger.closest("[data-error-popup], [data-success-popup]");
    if (popup instanceof HTMLElement) {
        popup.remove();
    }
};

window.braidplaneDsSelectCard = (card) => {
    if (!(card instanceof HTMLElement)) {
        return;
    }
    document.querySelectorAll("[data-ds-card][data-selected]").forEach((current) => {
        current.removeAttribute("data-selected");
    });
    card.setAttribute("data-selected", "true");
};

window.braidplaneQuerySelectCard = (card) => {
    if (!(card instanceof HTMLElement)) {
        return;
    }
    document.querySelectorAll("[data-query-card][data-selected]").forEach((current) => {
        current.removeAttribute("data-selected");
    });
    card.setAttribute("data-selected", "true");
};

const braidplaneSetQueryRunButtonLoading = (isLoading) => {
    const runButton = document.getElementById("query-run-btn");
    if (!(runButton instanceof HTMLButtonElement)) {
        return;
    }

    const idleLabel = runButton.dataset.idleLabel || "Run";
    const loadingLabel = runButton.dataset.loadingLabel || "Running...";
    runButton.disabled = isLoading;
    runButton.textContent = isLoading ? loadingLabel : idleLabel;
};

const braidplaneSelectQueryTab = (detail, panelID) => {
    detail.querySelectorAll("[data-query-tab]").forEach((tab) => {
        const selected = tab.dataset.queryTab === panelID;
        tab.classList.toggle("btn-active", selected);
        tab.setAttribute("aria-selected", String(selected));
        tab.tabIndex = selected ? 0 : -1;
        const panel = detail.querySelector(`#${tab.dataset.queryTab}`);
        if (panel) panel.hidden = !selected;
    });
};

window.braidplaneFocusQueryExecutionFeedback = (target) => {
    if (!(target instanceof HTMLElement)) {
        return;
    }

    const nextFeedback = target.querySelector('[data-query-execution-feedback="true"]');
    if (!(nextFeedback instanceof HTMLElement)) {
        return;
    }

    const detail = target.closest("[data-query-detail]");
    if (detail) braidplaneSelectQueryTab(detail, "query-execution-feedback-slot");
    requestAnimationFrame(() => {
        nextFeedback.scrollIntoView({ behavior: "smooth", block: "start" });
        nextFeedback.focus({ preventScroll: true });
    });
};

const braidplaneQueryDetailScrollKey = () => `query-detail-scroll:${window.location.pathname}`;

window.braidplanePersistQueryDetailScroll = () => {
    try {
        sessionStorage.setItem(braidplaneQueryDetailScrollKey(), String(window.scrollY));
    } catch {
        // Scroll preservation is best-effort and should not block query updates.
    }
};

const braidplaneRestoreQueryDetailScroll = (callback) => {
    let raw = null;
    try {
        raw = sessionStorage.getItem(braidplaneQueryDetailScrollKey());
    } catch {
        raw = null;
    }
    if (raw === null) {
        if (callback) callback(false);
        return;
    }
    try {
        sessionStorage.removeItem(braidplaneQueryDetailScrollKey());
    } catch {
        // The in-page restore can still proceed if storage cleanup is blocked.
    }
    const y = Number(raw);
    if (!Number.isFinite(y)) {
        if (callback) callback(false);
        return;
    }
    requestAnimationFrame(() => {
        window.scrollTo({ top: y, behavior: "auto" });
        if (callback) callback(true);
    });
};

window.braidplaneInitQueryDetail = (root) => {
    const scope = root instanceof HTMLElement || root instanceof Document ? root : document;
    const detailRoot =
        scope.querySelector("[data-query-detail]") ||
        (scope instanceof Element && scope.matches("[data-query-detail]") ? scope : null);
    if (!(detailRoot instanceof HTMLElement) || detailRoot.dataset.queryDetailReady === "true") {
        return;
    }
    detailRoot.dataset.queryDetailReady = "true";

    const tabs = [...detailRoot.querySelectorAll("[data-query-tab]")];
    braidplaneSelectQueryTab(
        detailRoot,
        detailRoot.querySelector('[data-query-execution-feedback="true"]')
            ? "query-execution-feedback-slot"
            : "query-definition",
    );
    tabs.forEach((tab, index) => {
        tab.addEventListener("click", () => braidplaneSelectQueryTab(detailRoot, tab.dataset.queryTab));
        tab.addEventListener("keydown", (event) => {
            let next;
            if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
            else if (event.key === "ArrowLeft") next = (index + tabs.length - 1) % tabs.length;
            else if (event.key === "Home") next = 0;
            else if (event.key === "End") next = tabs.length - 1;
            else return;
            event.preventDefault();
            tabs[next].focus();
            braidplaneSelectQueryTab(detailRoot, tabs[next].dataset.queryTab);
        });
        tab.disabled = false;
    });

    detailRoot.querySelector('[form="query-update-form"]')?.addEventListener("click", () => {
        // Reveal definition fields before native constraint validation tries to focus them.
        braidplaneSelectQueryTab(detailRoot, "query-definition");
    });

    window.braidplaneQueryParamEditor?.initAll(detailRoot);

    braidplaneRestoreQueryDetailScroll((wasRestored) => {
        const executionFeedback = detailRoot.querySelector('[data-query-execution-feedback="true"]');
        if (executionFeedback instanceof HTMLElement && !wasRestored) {
            requestAnimationFrame(() => {
                executionFeedback.scrollIntoView({ behavior: "smooth", block: "start" });
                executionFeedback.focus({ preventScroll: true });
            });
        }
    });
};

window.braidplaneInitTransientQueryFeedback = (root) => {
    const queryParameters = new Set();
    braidplaneSelectWithRoot(root, "[data-consume-query-param]").forEach((feedback) => {
        if (!(feedback instanceof HTMLElement) || feedback.dataset.transientQueryFeedbackConsumed === "true") {
            return;
        }
        feedback.dataset.transientQueryFeedbackConsumed = "true";
        const parameter = (feedback.getAttribute("data-consume-query-param") || "").trim();
        if (parameter !== "") {
            queryParameters.add(parameter);
        }
    });

    if (queryParameters.size === 0) {
        return;
    }
    const currentURL = new URL(window.location.href);
    let changed = false;
    queryParameters.forEach((parameter) => {
        if (currentURL.searchParams.has(parameter)) {
            currentURL.searchParams.delete(parameter);
            changed = true;
        }
    });
    if (changed) {
        window.history.replaceState(
            window.history.state,
            "",
            `${currentURL.pathname}${currentURL.search}${currentURL.hash}`,
        );
    }
};

const braidplaneInitSharedForms = (root) => {
    window.braidplaneDsInitCreateForms(root);
    window.braidplaneDsInitGitAuthForms(root);
    window.braidplaneDsInitProviderAuthForms(root);
    window.braidplaneDsInitSnowflakeAuthForms(root);
    window.braidplaneDsInitHTTPAuthFields(root);
    window.braidplaneInitAlertPolicyForms(root);
    window.braidplaneInitAlertDestinationForms(root);
    window.braidplaneQueryParamEditor?.initAll(root);
    window.braidplaneInitDirtyForms(root);
    window.braidplaneInitProtectedActionModals(root);
    window.braidplaneInitProtectedSelectForms(root);
    window.braidplaneInitSuccessPopups(root);
    window.braidplaneInitQueryDetail(root);
    window.braidplaneInitTransientQueryFeedback(root);
    braidplaneSelectWithRoot(root, "[data-form-feedback]").some((form) => {
        const target = form.querySelector("[data-form-error-focus]:not(:disabled)");
        if (!(target instanceof HTMLElement)) return false;
        target.removeAttribute("data-form-error-focus");
        target.focus();
        return true;
    });
};

if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", () => braidplaneInitSharedForms(document));
} else {
    braidplaneInitSharedForms(document);
}

document.body?.addEventListener("htmx:afterSwap", (event) => {
    braidplaneInitSharedForms(event.target);
    if (event.target instanceof Element) {
        const snowflakeForm = event.target.closest("[data-snowflake-auth-form]");
        if (snowflakeForm instanceof HTMLFormElement) {
            window.braidplaneDsInitSnowflakeAuthForms(snowflakeForm);
        }
    }
});

document.addEventListener("click", (event) => {
    const target = event.target;
    if (!(target instanceof Element)) {
        return;
    }

    const popupDismiss = target.closest("[data-popup-dismiss]");
    if (popupDismiss instanceof HTMLElement) {
        window.braidplaneDismissPopup(popupDismiss);
        return;
    }

    const toggle = target.closest("[data-toggle-target]");
    if (toggle instanceof HTMLElement) {
        window.braidplaneToggleSection(toggle);
    }

    const dataSourceCard = target.closest("[data-ds-card]");
    if (dataSourceCard instanceof HTMLElement) {
        window.braidplaneDsSelectCard(dataSourceCard);
    }

    const queryCard = target.closest("[data-query-card]");
    if (queryCard instanceof HTMLElement) {
        window.braidplaneQuerySelectCard(queryCard);
    }
});

document.addEventListener("change", (event) => {
    const target = event.target;
    if (target instanceof HTMLSelectElement && target.matches("[data-auto-submit-form]")) {
        target.form?.requestSubmit();
    }
});

document.addEventListener("submit", (event) => {
    const form = event.target;
    if (form instanceof HTMLFormElement && form.matches("[data-query-detail-scroll-form]")) {
        window.braidplanePersistQueryDetailScroll();
    }
});

document.addEventListener("htmx:beforeRequest", (event) => {
    const source = event.detail?.elt;
    if (source instanceof HTMLFormElement && source.id === "query-execute-form") {
        braidplaneSetQueryRunButtonLoading(true);
    }
});

document.addEventListener("htmx:afterRequest", (event) => {
    const source = event.detail?.elt;
    if (source instanceof HTMLFormElement && source.id === "query-execute-form") {
        braidplaneSetQueryRunButtonLoading(false);
    }
});

document.addEventListener("htmx:responseError", (event) => {
    const source = event.detail?.elt;
    if (source instanceof HTMLFormElement && source.id === "query-execute-form") {
        braidplaneSetQueryRunButtonLoading(false);
    }
});

document.addEventListener("htmx:afterSwap", (event) => {
    const target = event.target;
    if (target instanceof HTMLElement && target.id === "query-execution-feedback-slot") {
        window.braidplaneFocusQueryExecutionFeedback(target);
    }
});

const BRAIDPLANE_PRESERVED_SCROLL_KEY = "braidplane:preserved-scroll-y";
let braidplanePreservedScrollY = null;

const braidplanePreserveScrollTrigger = (event) => {
    const candidates = [event.detail?.elt, event.target];
    for (const candidate of candidates) {
        if (!(candidate instanceof HTMLElement)) {
            continue;
        }
        if (candidate.matches("[data-preserve-scroll]")) {
            return candidate;
        }
        const parent = candidate.closest("[data-preserve-scroll]");
        if (parent instanceof HTMLElement) {
            return parent;
        }
    }
    return null;
};

const braidplaneStorePreservedScroll = () => {
    braidplanePreservedScrollY = window.scrollY;
    try {
        sessionStorage.setItem(BRAIDPLANE_PRESERVED_SCROLL_KEY, String(braidplanePreservedScrollY));
    } catch {
        // Session storage is only a fallback for full navigation; in-memory restore still works.
    }
};

const braidplaneReadPreservedScroll = () => {
    if (braidplanePreservedScrollY !== null) {
        return braidplanePreservedScrollY;
    }
    try {
        const raw = sessionStorage.getItem(BRAIDPLANE_PRESERVED_SCROLL_KEY);
        if (raw === null) {
            return null;
        }
        const parsed = Number(raw);
        return Number.isFinite(parsed) ? parsed : null;
    } catch {
        return null;
    }
};

const braidplaneClearPreservedScroll = () => {
    braidplanePreservedScrollY = null;
    try {
        sessionStorage.removeItem(BRAIDPLANE_PRESERVED_SCROLL_KEY);
    } catch {
        // Ignore storage failures; the in-memory state has already been cleared.
    }
};

const braidplaneRestorePreservedScroll = () => {
    const restoredY = braidplaneReadPreservedScroll();
    if (restoredY === null) {
        return;
    }
    braidplaneClearPreservedScroll();
    // Deferred restores can move the next link between pointer down and click.
    window.scrollTo(window.scrollX, restoredY);
};

document.addEventListener(
    "click",
    (event) => {
        if (braidplanePreserveScrollTrigger(event)) {
            braidplaneStorePreservedScroll();
        }
    },
    true,
);

const braidplaneHandlePreserveScrollRequest = (event) => {
    if (braidplanePreserveScrollTrigger(event)) {
        braidplaneStorePreservedScroll();
    }
};

document.addEventListener("htmx:beforeRequest", braidplaneHandlePreserveScrollRequest);
document.addEventListener("htmx:afterSwap", braidplaneRestorePreservedScroll);
document.addEventListener("htmx:afterSettle", braidplaneRestorePreservedScroll);
document.body?.addEventListener("htmx:beforeRequest", braidplaneHandlePreserveScrollRequest);
document.body?.addEventListener("htmx:afterSwap", braidplaneRestorePreservedScroll);
document.body?.addEventListener("htmx:afterSettle", braidplaneRestorePreservedScroll);
window.addEventListener("load", braidplaneRestorePreservedScroll);

const BRAIDPLANE_ENTITY_LIST_SCROLL_KEY = "braidplane:entity-list-scroll";
const braidplaneScrollURL = (url) => {
    url.searchParams.sort();
    return url.pathname + url.search;
};

document.addEventListener(
    "click",
    (event) => {
        const anchor = event.target instanceof Element ? event.target.closest("a[href]") : null;
        if (
            !(anchor instanceof HTMLAnchorElement) ||
            event.button !== 0 ||
            event.metaKey ||
            event.ctrlKey ||
            event.shiftKey ||
            event.altKey ||
            (anchor.target && anchor.target !== "_self")
        )
            return;
        try {
            const detail = new URL(anchor.href);
            const listReturn = detail.searchParams.get("list_return");
            if (detail.origin !== window.location.origin || !listReturn) return;
            const list = new URL(listReturn, window.location.origin);
            if (list.origin !== window.location.origin || list.pathname !== window.location.pathname) return;
            // Detail sections must not consume the position needed by explicit Back.
            sessionStorage.setItem(
                BRAIDPLANE_ENTITY_LIST_SCROLL_KEY,
                JSON.stringify({ url: braidplaneScrollURL(list), y: window.scrollY }),
            );
        } catch {
            // Navigation remains usable when session storage is unavailable.
        }
    },
    true,
);

window.addEventListener("load", () => {
    try {
        const saved = JSON.parse(sessionStorage.getItem(BRAIDPLANE_ENTITY_LIST_SCROLL_KEY));
        if (
            !saved ||
            saved.url !== braidplaneScrollURL(new URL(window.location.href)) ||
            !Number.isFinite(saved.y) ||
            saved.y < 0
        )
            return;
        sessionStorage.removeItem(BRAIDPLANE_ENTITY_LIST_SCROLL_KEY);
        window.scrollTo(window.scrollX, saved.y);
    } catch {
        // A malformed or unavailable stored position must not block the page.
    }
});

const BRAIDPLANE_DIFF_MAX_LINES = 400;
const BRAIDPLANE_DIFF_MAX_CELLS = 120000;

const braidplaneBuildDiffFallback = (oldLineCount, newLineCount) => [
    {
        kind: "equal",
        text: "[Diff omitted because the compared documents are too large for the in-browser line diff.]",
    },
    { kind: "remove", text: `[Original document: ${oldLineCount} lines]` },
    { kind: "add", text: `[Updated document: ${newLineCount} lines]` },
];

/**
 * Compute a line-level unified diff between two text strings.
 * Returns an array of objects: { kind: 'equal' | 'add' | 'remove', text: string }.
 */
window.braidplaneComputeLineDiff = (oldText, newText) => {
    const oldLines = oldText.split("\n");
    const newLines = newText.split("\n");

    if (
        oldLines.length > BRAIDPLANE_DIFF_MAX_LINES ||
        newLines.length > BRAIDPLANE_DIFF_MAX_LINES ||
        oldLines.length * newLines.length > BRAIDPLANE_DIFF_MAX_CELLS
    ) {
        return braidplaneBuildDiffFallback(oldLines.length, newLines.length);
    }

    // Build LCS table
    const m = oldLines.length;
    const n = newLines.length;
    const dp = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0));
    for (let i = m - 1; i >= 0; i--) {
        for (let j = n - 1; j >= 0; j--) {
            if (oldLines[i] === newLines[j]) {
                dp[i][j] = dp[i + 1][j + 1] + 1;
            } else {
                dp[i][j] = Math.max(dp[i + 1][j], dp[i][j + 1]);
            }
        }
    }

    // Trace back through LCS table to produce diff hunks
    const result = [];
    let i = 0;
    let j = 0;
    while (i < m || j < n) {
        if (i < m && j < n && oldLines[i] === newLines[j]) {
            result.push({ kind: "equal", text: oldLines[i] });
            i++;
            j++;
        } else if (j < n && (i >= m || dp[i + 1][j] <= dp[i][j + 1])) {
            result.push({ kind: "add", text: newLines[j] });
            j++;
        } else {
            result.push({ kind: "remove", text: oldLines[i] });
            i++;
        }
    }
    return result;
};

/**
 * Render a diff produced by braidplaneComputeLineDiff into an HTML string suitable
 * for insertion into a <pre> element. Added lines are prefixed with "+",
 * removed lines with "-", and equal lines with " ".
 */
window.braidplaneRenderDiff = (hunks) => {
    return hunks
        .map((hunk) => {
            let prefix = " ";
            let cssClass = "";
            if (hunk.kind === "add") {
                prefix = "+";
                cssClass = "text-success";
            } else if (hunk.kind === "remove") {
                prefix = "-";
                cssClass = "text-error";
            }
            const escaped = hunk.text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
            if (cssClass) {
                return `<span class="${cssClass}">${prefix}${escaped}</span>`;
            }
            return ` ${escaped}`;
        })
        .join("\n");
};

// Only explicit quick-look navigation moves focus; polling and unrelated swaps do not.
document.addEventListener("htmx:afterSettle", (event) => {
    const source = event.detail?.requestConfig?.elt;
    const id = source?.dataset?.entityFocus;
    if (!id) return;
    const target = document.getElementById(id) || document.getElementById(source.id);
    if (!(target instanceof HTMLElement)) return;
    if (event.detail?.requestConfig?.triggeringEvent?.pointerType && target.closest("[data-entity-preview]")) return;
    target.focus({ preventScroll: false });
});

// A quick-look swap leaves its inventory intact; update selection after success.
document.addEventListener("htmx:afterSwap", (event) => {
    const source = event.detail?.requestConfig?.elt;
    if (!source?.dataset?.entityFocus) return;
    const layout = event.target?.closest?.("[data-entity-layout]") || source.closest?.("[data-entity-layout]");
    if (!layout) return;
    const selected = source.closest("[data-entity-list-item], [data-ds-card]");
    for (const item of layout.querySelectorAll("[data-entity-list-item], [data-ds-card]")) {
        const current = item === selected;
        item.classList.toggle("entity-selected", current);
        item.toggleAttribute("data-selected", current);
        item.querySelector("a[data-entity-focus]")?.setAttribute("aria-current", String(current));
    }
});
