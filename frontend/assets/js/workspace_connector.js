(() => {
    const jsonRequest = async (url, body) => {
        const response = await fetch(url, {
            credentials: "same-origin",
            ...(body
                ? {
                      method: "POST",
                      headers: {
                          "Content-Type": "application/json",
                          "X-CSRF-Token": document.querySelector('meta[name="csrf-token"]')?.content || "",
                      },
                      body: JSON.stringify(body),
                  }
                : {}),
        });
        if (!response.ok) {
            throw new Error(
                response.status === 409
                    ? "Source changed. Reload and select again."
                    : response.status === 403
                      ? "Use the connected Google account and check file access."
                      : "Workspace files unavailable. Check the connection and try again.",
            );
        }
        return response.json();
    };
    const fileScope = "https://www.googleapis.com/auth/drive.file";
    let pickerLibraries;
    const loadPickerLibraries = () => {
        if (window.google?.picker && window.google?.accounts?.oauth2) return Promise.resolve();
        if (!pickerLibraries) {
            const script = (src) =>
                new Promise((resolve, reject) => {
                    const element = document.createElement("script");
                    element.src = src;
                    element.onload = resolve;
                    element.onerror = () =>
                        reject(new Error("Google file selector could not load. Reload and try again."));
                    document.head.append(element);
                });
            pickerLibraries = Promise.all([
                script("https://accounts.google.com/gsi/client"),
                script("https://apis.google.com/js/api.js").then(
                    () =>
                        new Promise((resolve, reject) => {
                            window.gapi.load("picker", {
                                callback: resolve,
                                onerror: reject,
                                timeout: 10000,
                                ontimeout: reject,
                            });
                        }),
                ),
            ]).catch((error) => {
                pickerLibraries = undefined;
                throw error;
            });
        }
        return pickerLibraries;
    };
    const init = (root = document) => {
        root.querySelectorAll("[data-workspace-operation]").forEach((panel) => {
            if (panel.dataset.bound) return;
            panel.dataset.bound = "true";
            const base = `/orgs/${encodeURIComponent(panel.dataset.org)}/data-sources/${encodeURIComponent(panel.dataset.source)}/google_workspace`;
            const feedback = panel.querySelector("[data-workspace-feedback]");
            const choose = (file) => {
                if (!file) return;
                const key =
                    file.mime_type === "application/vnd.google-apps.folder"
                        ? "workspace_folder_id"
                        : file.mime_type === "application/vnd.google-apps.spreadsheet"
                          ? "workspace_spreadsheet_id"
                          : "workspace_file_id";
                const input = panel.querySelector(`[name="${key}"]`);
                if (input) input.value = file.file_id;
                if (key !== "workspace_folder_id") {
                    const resource = panel.querySelector('[name="workspace_resource"]');
                    resource.value =
                        key === "workspace_spreadsheet_id"
                            ? "spreadsheet"
                            : file.mime_type === "application/vnd.google-apps.document"
                              ? "document"
                              : file.mime_type === "application/vnd.google-apps.presentation"
                                ? "presentation"
                                : "file";
                    panel.dataset.targetResource = resource.value;
                    panel.querySelectorAll("[data-workspace-target-name]").forEach((label) => {
                        label.textContent = file.name;
                    });
                    panel.querySelectorAll("[data-workspace-target-id]").forEach((label) => {
                        label.textContent = file.file_id;
                    });
                }
                input?.dispatchEvent(new Event("change", { bubbles: true }));
            };
            const pickButton = panel.querySelector("[data-workspace-pick]");
            if (pickButton) {
                pickButton.disabled = true;
                Promise.all([jsonRequest(`${base}/picker`), loadPickerLibraries()])
                    .then(([config]) => {
                        if (!panel.isConnected) return;
                        let browserAuthorized = false;
                        const finishSelection = (message) => {
                            if (!panel.isConnected) return;
                            pickButton.disabled = false;
                            feedback.textContent = message;
                            if (document.activeElement === document.body) pickButton.focus();
                        };
                        const tokenClient = window.google.accounts.oauth2.initTokenClient({
                            client_id: config.client_id,
                            scope: fileScope,
                            include_granted_scopes: false,
                            login_hint: config.account_email,
                            error_callback: () =>
                                finishSelection("Google selection was cancelled or could not open. Try again."),
                            callback: (response) => {
                                const scopes = (response.scope || "").split(/\s+/).filter(Boolean);
                                if (
                                    response.error ||
                                    !response.access_token ||
                                    scopes.length !== 1 ||
                                    scopes[0] !== fileScope
                                ) {
                                    finishSelection(
                                        "Reconnect Google with selected-file access before choosing files.",
                                    );
                                    return;
                                }
                                browserAuthorized = true;
                                // This browser-only token never comes from the stored backend grant.
                                const picker = new window.google.picker.PickerBuilder()
                                    .setDeveloperKey(config.developer_key)
                                    .setAppId(config.app_id)
                                    .setOAuthToken(response.access_token)
                                    .setOrigin(window.location.origin)
                                    .addView(
                                        new window.google.picker.DocsView()
                                            .setIncludeFolders(true)
                                            .setSelectFolderEnabled(true),
                                    )
                                    .addView(
                                        new window.google.picker.DocsView()
                                            .setEnableDrives(true)
                                            .setIncludeFolders(true)
                                            .setSelectFolderEnabled(true),
                                    )
                                    .setCallback(async (selection) => {
                                        if (
                                            ![
                                                window.google.picker.Action.PICKED,
                                                window.google.picker.Action.CANCEL,
                                            ].includes(selection.action)
                                        )
                                            return;
                                        picker.dispose();
                                        if (selection.action === window.google.picker.Action.CANCEL) {
                                            finishSelection("Selection cancelled. The operation is unchanged.");
                                            return;
                                        }
                                        try {
                                            const id = selection.docs?.[0]?.id;
                                            if (!id) throw new Error("Choose a file or folder.");
                                            const verified = await jsonRequest(`${base}/files/verify`, {
                                                file_ids: [id],
                                                expected_revision: config.expected_revision,
                                            });
                                            if (!panel.isConnected) return;
                                            choose(verified.files[0]);
                                            finishSelection("File access verified for the connected account.");
                                        } catch (error) {
                                            finishSelection(error.message);
                                        }
                                    })
                                    .build();
                                picker.setVisible(true);
                            },
                        });
                        pickButton.disabled = false;
                        pickButton.addEventListener("click", () => {
                            pickButton.disabled = true;
                            feedback.textContent = `Select using ${config.account_email}.`;
                            tokenClient.requestAccessToken({ prompt: browserAuthorized ? "" : "consent" });
                        });
                    })
                    .catch(() => {
                        feedback.textContent =
                            "File selection unavailable. Check your connection and permission, then reload.";
                    });
            }
            let listed = [];
            let next = "";
            let discoveryRequest = 0;
            const browseButton = panel.querySelector("[data-workspace-discover]");
            const nextButton = panel.querySelector("[data-workspace-next]");
            const files = panel.querySelector("[data-workspace-files]");
            const discover = async (pageToken = "") => {
                const request = ++discoveryRequest;
                try {
                    const page = await jsonRequest(
                        `${base}/files${pageToken ? `?page_token=${encodeURIComponent(pageToken)}` : ""}`,
                    );
                    if (!panel.isConnected || request !== discoveryRequest) return;
                    listed = page.files;
                    files.replaceChildren(
                        new Option("Choose a target…", ""),
                        ...page.files.map((file) => new Option(`${file.name} (${file.mime_type})`, file.file_id)),
                    );
                    files.classList.remove("hidden");
                    next = page.next_page_token || "";
                    const moveFocus = !next && document.activeElement === nextButton;
                    nextButton.classList.toggle("hidden", !next);
                    const count = listed.length;
                    feedback.textContent = count
                        ? `${count} accessible ${count === 1 ? "file" : "files"} on this page.`
                        : "No accessible files on this page.";
                    if (moveFocus) (count ? files : browseButton).focus();
                } catch (error) {
                    if (panel.isConnected && request === discoveryRequest) feedback.textContent = error.message;
                }
            };
            browseButton?.addEventListener("click", () => void discover());
            nextButton?.addEventListener("click", () => void discover(next));
            files?.addEventListener("change", (event) =>
                choose(listed.find((file) => file.file_id === event.target.value)),
            );
        });
        root.querySelectorAll("[data-workspace-operation]").forEach((fields) => {
            if (fields.dataset.controlsBound) return;
            fields.dataset.controlsBound = "true";
            const match = fields.querySelector('[name="workspace_match_text"]');
            // HTML textarea parsing drops an initial line break.
            if (match?.hasAttribute("data-workspace-initial-match-text")) {
                match.value = match.getAttribute("data-workspace-initial-match-text");
            }
            const resource = fields.querySelector('[name="workspace_resource"]');
            const operation = fields.querySelector('[name="workspace_operation"]');
            const format = fields.querySelector('[name="workspace_format"]');
            fields.dataset.targetResource = resource.value;
            const update = (event) => {
                if (event?.target === resource) fields.dataset.targetResource = resource.value;
                const native = ["document", "presentation"].includes(fields.dataset.targetResource);
                resource.value =
                    operation.value === "list" || (operation.value === "create" && !native)
                        ? "file"
                        : fields.dataset.targetResource;
                const exportOption = operation.querySelector('option[value="export"]');
                if (exportOption) exportOption.disabled = !native;
                if (operation.value === "export" && !native) operation.value = "read";
                const sheet = resource.value === "spreadsheet";
                const hasSheet = sheet && !!fields.querySelector('[name="workspace_spreadsheet_id"]').value;
                const current = operation.value;
                const visible = {
                    sheet: hasSheet,
                    "sheet-update": hasSheet && current === "update",
                    "file-target": !sheet && ["read", "update", "export"].includes(current),
                    folder: !sheet && ["list", "create"].includes(current),
                    list: !sheet && current === "list",
                    create: !sheet && current === "create",
                    mime: !sheet && !native && current !== "read",
                    format: !sheet && !native && current !== "list",
                    csv: !sheet && !native && current !== "list" && format.value === "csv",
                    "file-write": !sheet && (native ? current === "update" : ["create", "update"].includes(current)),
                    "native-update": native && current === "update",
                    native,
                };
                fields.querySelectorAll("[data-workspace-fields]").forEach((group) => {
                    group.classList.toggle("hidden", !visible[group.dataset.workspaceFields]);
                    group.querySelectorAll("input,select,textarea").forEach((input) => {
                        input.disabled = !visible[group.dataset.workspaceFields];
                    });
                });
            };
            fields.addEventListener("change", update, true);
            update();
            const target = fields.querySelector('[name="workspace_spreadsheet_id"]');
            const worksheet = fields.querySelector('[name="workspace_worksheet_id"]');
            const status = fields.querySelector("[data-workspace-worksheet-status]");
            const retry = fields.querySelector("[data-workspace-worksheet-retry]");
            let previousTarget = target.value;
            let worksheetRequest = 0;
            const worksheetMetadata = () => {
                fields.querySelector("[data-workspace-worksheet-id]").textContent = worksheet.value;
            };
            worksheet.addEventListener("change", worksheetMetadata);
            const loadWorksheets = async () => {
                const spreadsheetID = target.value;
                const request = ++worksheetRequest;
                if (!spreadsheetID) return;
                const selected = spreadsheetID === previousTarget ? worksheet.value : "";
                previousTarget = spreadsheetID;
                worksheet.replaceChildren(new Option("Loading worksheets…", selected));
                worksheetMetadata();
                status.textContent = "Loading worksheets…";
                retry.setAttribute("aria-disabled", "true");
                const isCurrent = () =>
                    fields.isConnected && request === worksheetRequest && target.value === spreadsheetID;
                try {
                    const schema = await jsonRequest(
                        `/orgs/${encodeURIComponent(fields.dataset.org)}/data-sources/${encodeURIComponent(fields.dataset.source)}/google_workspace/worksheets?spreadsheet_id=${encodeURIComponent(spreadsheetID)}`,
                    );
                    if (!isCurrent()) return;
                    const worksheets = schema.worksheets || [];
                    worksheet.replaceChildren(
                        ...worksheets.map((sheet) => new Option(sheet.title, String(sheet.worksheet_id))),
                    );
                    if (!selected && !worksheets.length) worksheet.add(new Option("No worksheets available", ""));
                    if (selected && ![...worksheet.options].some((option) => option.value === selected))
                        worksheet.add(new Option("Unavailable worksheet", selected));
                    if (selected) worksheet.value = selected;
                    worksheetMetadata();
                    if (worksheet.value !== selected) worksheet.dispatchEvent(new Event("change", { bubbles: true }));
                    status.textContent = worksheets.length
                        ? `${worksheets.length} ${worksheets.length === 1 ? "worksheet" : "worksheets"} loaded.`
                        : "No worksheets available.";
                    if (document.activeElement === retry) worksheet.focus();
                    retry.hidden = true;
                } catch (error) {
                    if (!isCurrent()) return;
                    worksheet.replaceChildren(
                        new Option(selected ? "Selected worksheet (unverified)" : "Worksheets unavailable", selected),
                    );
                    status.textContent = `Could not load worksheets. ${error.message}`;
                    retry.hidden = false;
                } finally {
                    if (isCurrent()) retry.removeAttribute("aria-disabled");
                }
            };
            retry.addEventListener("click", () => {
                if (retry.getAttribute("aria-disabled") !== "true") void loadWorksheets();
            });
            target.addEventListener("change", loadWorksheets);
            if (target.value) void loadWorksheets();
        });
    };
    window.BraidplaneWorkspace = { init };
    document.addEventListener("DOMContentLoaded", () => init());
    document.addEventListener("htmx:afterSwap", () => init());
})();
