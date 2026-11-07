(() => {
    const paramRegex = /\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}/g;

    const extractParams = (sql) => {
        if (!sql) {
            return [];
        }

        const names = [];
        const seen = new Set();
        let match;
        while (
            // biome-ignore lint/suspicious/noAssignInExpressions: intentional regex iteration pattern
            (match = paramRegex.exec(sql)) !== null
        ) {
            const name = match[1];
            if (!seen.has(name)) {
                seen.add(name);
                names.push(name);
            }
        }
        paramRegex.lastIndex = 0;
        return names;
    };

    const setRowSource = (row, source) => {
        const sourceInput = row.querySelector("[data-param-source]");
        const badge = row.querySelector("[data-param-source-badge]");
        if (sourceInput) {
            sourceInput.value = source;
        }
        if (badge) {
            badge.textContent = source;
            badge.classList.remove("badge-warning");
            if (source === "unused") {
                badge.classList.add("badge-warning");
            }
        }
    };

    const extractParamsFromInputs = (inputs) => {
        const discovered = [];
        const seen = new Set();

        inputs.forEach((input) => {
            const value = input.value || "";
            extractParams(value).forEach((name) => {
                if (!seen.has(name)) {
                    seen.add(name);
                    discovered.push(name);
                }
            });
        });

        return discovered;
    };

    const initParamEditor = (editor) => {
        if (editor.dataset.paramEditorReady === "true") {
            return;
        }

        const form = editor.closest("form");
        const rows = editor.querySelector("[data-param-rows]");
        const template = editor.querySelector("template[data-param-row-template]");
        const addBtn = editor.querySelector("[data-add-param]");
        const syncBtn = editor.querySelector("[data-sync-params]");

        if (!rows || !template || !form) {
            return;
        }
        editor.dataset.paramEditorReady = "true";

        const notifyFormMutated = () => {
            form.dispatchEvent(new CustomEvent("braidplane:form-mutated", { bubbles: true }));
        };

        const getStatementInputs = () => Array.from(form.querySelectorAll("[data-param-source-input]"));

        const bindRemoveButtons = () => {
            rows.querySelectorAll("[data-remove-param]").forEach((btn) => {
                if (btn.dataset.bound === "true") {
                    return;
                }
                btn.dataset.bound = "true";
                btn.addEventListener("click", () => {
                    const row = btn.closest("[data-param-row]");
                    if (row) {
                        row.remove();
                        notifyFormMutated();
                    }
                });
            });
        };

        const createRow = (name, source) => {
            const fragment = template.content.cloneNode(true);
            const row = fragment.querySelector("[data-param-row]");
            if (!row) {
                return;
            }

            const nameInput = row.querySelector('input[name="param_name"]');
            if (nameInput) {
                nameInput.value = name || "";
            }

            setRowSource(row, source || "manual");
            rows.appendChild(row);
            bindRemoveButtons();
            notifyFormMutated();
        };

        const syncFromStatement = () => {
            const statementInputs = getStatementInputs();
            const discovered = extractParamsFromInputs(statementInputs);
            const existing = new Map();

            rows.querySelectorAll("[data-param-row]").forEach((row) => {
                const nameInput = row.querySelector('input[name="param_name"]');
                if (!nameInput) {
                    return;
                }
                const key = nameInput.value.trim();
                if (key !== "") {
                    existing.set(key, row);
                }
            });

            discovered.forEach((name) => {
                const row = existing.get(name);
                if (row) {
                    const currentSource = row.querySelector("[data-param-source]")?.value || "manual";
                    if (currentSource === "auto" || currentSource === "unused") {
                        setRowSource(row, "auto");
                    }
                    return;
                }
                createRow(name, "auto");
            });

            rows.querySelectorAll("[data-param-row]").forEach((row) => {
                const name = row.querySelector('input[name="param_name"]')?.value.trim() || "";
                const source = row.querySelector("[data-param-source]")?.value || "manual";
                if (name !== "" && source === "auto" && !discovered.includes(name)) {
                    setRowSource(row, "unused");
                }
            });
            notifyFormMutated();
        };

        if (addBtn) {
            addBtn.addEventListener("click", () => createRow("", "manual"));
        }
        if (syncBtn) {
            syncBtn.addEventListener("click", syncFromStatement);
        }

        bindRemoveButtons();
    };

    const setHTTPPane = (container, activePane) => {
        const tabs = container.querySelectorAll("[data-http-tab]");
        const panes = container.querySelectorAll("[data-http-pane]");

        tabs.forEach((tab) => {
            if (tab.dataset.httpTab === activePane) {
                tab.classList.add("tab-active");
            } else {
                tab.classList.remove("tab-active");
            }
        });

        panes.forEach((pane) => {
            if (pane.dataset.httpPane === activePane) {
                pane.classList.remove("hidden");
            } else {
                pane.classList.add("hidden");
            }
        });
    };

    const initHTTPTabs = (root) => {
        root.querySelectorAll("[data-http-tabs]").forEach((container) => {
            const tabs = container.querySelectorAll("[data-http-tab]");
            if (tabs.length === 0) {
                return;
            }

            tabs.forEach((tab) => {
                if (tab.dataset.bound === "true") {
                    return;
                }
                tab.dataset.bound = "true";
                tab.addEventListener("click", () => {
                    setHTTPPane(container, tab.dataset.httpTab || "body");
                });
            });

            // Body is the default active tab, matching wireframes.
            setHTTPPane(container, "body");
        });
    };

    const initKeyValueEditor = (editor) => {
        const rows = editor.querySelector("[data-kv-rows]");
        const template = editor.querySelector("template[data-kv-row-template]");
        const addBtn = editor.querySelector("[data-add-kv]");

        if (!rows || !template) {
            return;
        }

        const bindRemoveButtons = () => {
            rows.querySelectorAll("[data-remove-kv]").forEach((btn) => {
                if (btn.dataset.bound === "true") {
                    return;
                }
                btn.dataset.bound = "true";
                btn.addEventListener("click", () => {
                    const row = btn.closest("[data-kv-row]");
                    if (row) {
                        row.remove();
                        editor.dispatchEvent(new CustomEvent("braidplane:form-mutated", { bubbles: true }));
                    }
                });
            });
        };

        const addRow = () => {
            const fragment = template.content.cloneNode(true);
            rows.appendChild(fragment);
            bindRemoveButtons();
            editor.dispatchEvent(new CustomEvent("braidplane:form-mutated", { bubbles: true }));
        };

        if (addBtn && addBtn.dataset.bound !== "true") {
            addBtn.dataset.bound = "true";
            addBtn.addEventListener("click", addRow);
        }

        bindRemoveButtons();
    };

    const parsePathQueryPairs = (pathValue) => {
        const trimmed = (pathValue || "").trim();
        const queryIndex = trimmed.indexOf("?");
        if (queryIndex === -1) {
            return [];
        }

        const hashIndex = trimmed.indexOf("#", queryIndex + 1);
        const queryText = hashIndex === -1 ? trimmed.slice(queryIndex + 1) : trimmed.slice(queryIndex + 1, hashIndex);
        if (queryText === "") {
            return [];
        }

        return Array.from(new URLSearchParams(queryText).entries()).map(([key, value]) => ({
            key,
            value,
        }));
    };

    const splitPathAndQuery = (pathValue) => {
        const trimmed = (pathValue || "").trim();
        const queryIndex = trimmed.indexOf("?");
        if (queryIndex === -1) {
            return {
                pathOnly: trimmed,
                queryPairs: [],
            };
        }

        return {
            pathOnly: trimmed.slice(0, queryIndex).trim(),
            queryPairs: parsePathQueryPairs(trimmed),
        };
    };

    const buildQueryStringFromRows = (rowsContainer) => {
        if (!rowsContainer) {
            return "";
        }

        const queryParts = [];
        const encodeForPathDisplay = (text) => encodeURIComponent(text).replace(/%7B/gi, "{").replace(/%7D/gi, "}");

        rowsContainer.querySelectorAll("[data-kv-row]").forEach((row) => {
            const key = row.querySelector('input[name="http_query_param_key"]')?.value.trim() || "";
            const value = row.querySelector('input[name="http_query_param_value"]')?.value.trim() || "";
            if (key === "" && value === "") {
                return;
            }
            if (key !== "") {
                queryParts.push(`${encodeForPathDisplay(key)}=${encodeForPathDisplay(value)}`);
            }
        });

        return queryParts.join("&");
    };

    const initPreventEnterSubmit = (root) => {
        root.querySelectorAll("form").forEach((form) => {
            if (!form.querySelector("[data-param-editor]") && !form.querySelector("[data-http-path-input]")) {
                return;
            }

            if (form.dataset.preventEnterSubmitBound === "true") {
                return;
            }

            form.dataset.preventEnterSubmitBound = "true";
            form.addEventListener("keydown", (event) => {
                if (event.key !== "Enter") {
                    return;
                }
                if (event.shiftKey || event.ctrlKey || event.metaKey || event.altKey) {
                    return;
                }

                const target = event.target;
                if (!(target instanceof Element)) {
                    return;
                }

                const tagName = target.tagName.toLowerCase();
                if (tagName === "textarea" || tagName === "button" || tagName === "summary") {
                    return;
                }

                event.preventDefault();
            });
        });
    };

    const initHubSpotUpsertVisibility = (root) => {
        root.querySelectorAll("[data-hubspot-operation-select]").forEach((operationSelect) => {
            const form = operationSelect.closest("form");
            const upsertFields = form?.querySelector("[data-hubspot-upsert-fields]");
            if (!upsertFields) {
                return;
            }

            const sync = () => {
                upsertFields.classList.toggle("hidden", operationSelect.value !== "upsert");
            };
            if (operationSelect.dataset.hubspotVisibilityBound !== "true") {
                operationSelect.dataset.hubspotVisibilityBound = "true";
                operationSelect.addEventListener("change", sync);
            }
            sync();
        });
    };

    const replaceQueryRows = (queryEditor, pairs) => {
        const rows = queryEditor.querySelector("[data-kv-rows]");
        const template = queryEditor.querySelector("template[data-kv-row-template]");
        if (!rows || !template) {
            return;
        }

        rows.innerHTML = "";

        const rowsToRender = pairs.length > 0 ? pairs : [{ key: "", value: "" }];
        rowsToRender.forEach((pair) => {
            const fragment = template.content.cloneNode(true);
            const keyInput = fragment.querySelector('input[name="http_query_param_key"]');
            const valueInput = fragment.querySelector('input[name="http_query_param_value"]');
            if (keyInput) {
                keyInput.value = pair.key || "";
            }
            if (valueInput) {
                valueInput.value = pair.value || "";
            }
            rows.appendChild(fragment);
        });

        initKeyValueEditor(queryEditor);
    };

    const initHTTPPathQuerySync = (root) => {
        root.querySelectorAll("form").forEach((form) => {
            const pathInput = form.querySelector("[data-http-path-input]");
            const queryEditor = form.querySelector("[data-http-query-editor]");
            if (!pathInput || !queryEditor) {
                return;
            }

            const queryRows = queryEditor.querySelector("[data-kv-rows]");
            if (!queryRows) {
                return;
            }

            const syncRowsFromPath = () => {
                const parsed = splitPathAndQuery(pathInput.value);
                replaceQueryRows(queryEditor, parsed.queryPairs);
            };

            const syncPathFromRows = () => {
                const parsed = splitPathAndQuery(pathInput.value);
                const queryString = buildQueryStringFromRows(queryRows);
                pathInput.value = queryString === "" ? parsed.pathOnly : `${parsed.pathOnly}?${queryString}`;
            };

            if (pathInput.dataset.pathQuerySyncBound !== "true") {
                pathInput.dataset.pathQuerySyncBound = "true";
                pathInput.addEventListener("input", syncRowsFromPath);
                pathInput.addEventListener("change", syncRowsFromPath);
                pathInput.addEventListener("blur", syncRowsFromPath);
            }

            if (queryRows.dataset.pathQuerySyncBound !== "true") {
                queryRows.dataset.pathQuerySyncBound = "true";
                queryRows.addEventListener("input", (event) => {
                    const target = event.target;
                    if (!(target instanceof Element)) {
                        return;
                    }
                    if (
                        target.matches('input[name="http_query_param_key"]') ||
                        target.matches('input[name="http_query_param_value"]')
                    ) {
                        syncPathFromRows();
                    }
                });
                queryRows.addEventListener("click", (event) => {
                    const target = event.target;
                    if (!(target instanceof Element)) {
                        return;
                    }
                    if (target.matches("[data-remove-kv]")) {
                        requestAnimationFrame(syncPathFromRows);
                    }
                });
            }

            const addBtn = queryEditor.querySelector("[data-add-kv]");
            if (addBtn && addBtn.dataset.pathQuerySyncBound !== "true") {
                addBtn.dataset.pathQuerySyncBound = "true";
                addBtn.addEventListener("click", () => {
                    requestAnimationFrame(syncPathFromRows);
                });
            }

            // Normalize initial state so query string and key/value rows stay aligned.
            if (pathInput.value.includes("?")) {
                syncRowsFromPath();
            } else {
                syncPathFromRows();
            }
        });
    };

    const initAll = (root) => {
        const scope = root || document;
        scope.querySelectorAll("[data-param-editor]").forEach((editor) => {
            initParamEditor(editor);
        });
        initHTTPTabs(scope);
        scope.querySelectorAll("[data-kv-editor]").forEach((editor) => {
            initKeyValueEditor(editor);
        });
        initHTTPPathQuerySync(scope);
        initPreventEnterSubmit(scope);
        initHubSpotUpsertVisibility(scope);
    };

    window.braidplaneQueryParamEditor = {
        initAll,
        _testing: {
            parsePathQueryPairs,
            splitPathAndQuery,
            extractParams,
        },
    };

    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", () => initAll(document));
        return;
    }

    initAll(document);
})();
