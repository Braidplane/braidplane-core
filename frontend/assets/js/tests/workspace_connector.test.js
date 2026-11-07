import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

await import("../workspace_connector.js");

const response = (body, status = 200) => ({
    ok: status >= 200 && status < 300,
    status,
    json: vi.fn().mockResolvedValue(body),
});

const workspacePanel = ({ spreadsheetID = "", worksheetID = "", operation = "read" } = {}) => `
    <div data-workspace-operation data-org="acme" data-source="workspace">
        <input name="workspace_resource" value="file" />
        <select name="workspace_operation">
            <option value="read" ${operation === "read" ? "selected" : ""}>Read</option>
            <option value="create" ${operation === "create" ? "selected" : ""}>Create</option>
            <option value="update" ${operation === "update" ? "selected" : ""}>Update</option>
        </select>
        <select name="workspace_format"><option value="json" selected>JSON</option></select>
        <button type="button" data-workspace-discover>Browse</button>
        <button type="button" class="hidden" data-workspace-next>Next</button>
        <select data-workspace-files></select>
        <p data-workspace-feedback></p>
        <div data-workspace-fields="sheet"><input name="workspace_spreadsheet_id" value="${spreadsheetID}" /></div>
        <div data-workspace-fields="sheet"><select name="workspace_worksheet_id"><option value="${worksheetID}" selected>Loading worksheets…</option></select><span data-workspace-worksheet-id></span></div>
        <div data-workspace-fields="sheet-update"><input name="workspace_columns" /></div>
        <div data-workspace-fields="file-target"><input name="workspace_file_id" /></div>
        <div data-workspace-fields="folder"><input name="workspace_folder_id" /></div>
        <div data-workspace-fields="list"><input name="workspace_drive_id" /></div>
        <div data-workspace-fields="create"><input name="workspace_name" /></div>
        <div data-workspace-fields="mime"><input name="workspace_mime_type" /></div>
        <div data-workspace-fields="format"><select name="workspace_format_copy"><option>JSON</option></select></div>
        <div data-workspace-fields="csv"><input name="workspace_delimiter" /></div>
        <div data-workspace-fields="file-write"><textarea name="workspace_content"></textarea></div>
    </div>
`;

beforeEach(() => {
    document.body.innerHTML = "";
});

afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
});

describe("workspace connector", () => {
    it("Preserves literal match text and leading line breaks without resetting edits", () => {
        document.body.innerHTML = workspacePanel({ operation: "update" });
        const panel = document.querySelector("[data-workspace-operation]");
        panel.querySelector('[name="workspace_resource"]').value = "document";
        const match = document.createElement("textarea");
        match.name = "workspace_match_text";
        match.setAttribute("data-workspace-initial-match-text", "\n Customer \n");
        match.textContent = "Customer";
        panel.append(match);
        window.BraidplaneWorkspace.init(document);
        expect(match.value).toBe("\n Customer \n");
        match.value = " edited ";
        window.BraidplaneWorkspace.init(document);
        expect(match.value).toBe(" edited ");
    });

    it.each(["document", "presentation"])(
        "Keeps native %s targets and hides file serialization controls",
        async (resource) => {
            const mime =
                resource === "document"
                    ? "application/vnd.google-apps.document"
                    : "application/vnd.google-apps.presentation";
            vi.stubGlobal(
                "fetch",
                vi
                    .fn()
                    .mockResolvedValue(response({ files: [{ file_id: "native", name: "Native", mime_type: mime }] })),
            );
            document.body.innerHTML = workspacePanel({ operation: "update" });
            window.BraidplaneWorkspace.init(document);
            document.querySelector("[data-workspace-discover]").click();
            const files = document.querySelector("[data-workspace-files]");
            await vi.waitFor(() => expect(files.options).toHaveLength(2));
            files.value = "native";
            files.dispatchEvent(new Event("change", { bubbles: true }));
            expect(document.querySelector('[name="workspace_resource"]').value).toBe(resource);
            expect(document.querySelector('[name="workspace_file_id"]').value).toBe("native");
            expect(document.querySelector('[name="workspace_content"]').disabled).toBe(false);
            expect(document.querySelector('[data-workspace-fields="format"]').classList.contains("hidden")).toBe(true);
            const operation = document.querySelector('[name="workspace_operation"]');
            operation.value = "create";
            operation.dispatchEvent(new Event("change", { bubbles: true }));
            expect(document.querySelector('[name="workspace_resource"]').value).toBe(resource);
            expect(document.querySelector('[name="workspace_content"]').disabled).toBe(true);
            expect(document.querySelector('[name="workspace_folder_id"]').disabled).toBe(false);
        },
    );

    it("LoadsFirstActualWorksheetWhenSelectingSpreadsheetFile", async () => {
        const fetchMock = vi
            .fn()
            .mockResolvedValueOnce(
                response({
                    files: [
                        {
                            file_id: "sheet-1",
                            name: "Orders",
                            mime_type: "application/vnd.google-apps.spreadsheet",
                        },
                    ],
                }),
            )
            .mockResolvedValueOnce(
                response({
                    worksheets: [
                        { worksheet_id: 12, title: "Orders" },
                        { worksheet_id: 19, title: "Archive" },
                    ],
                }),
            );
        vi.stubGlobal("fetch", fetchMock);
        document.body.innerHTML = workspacePanel();

        window.BraidplaneWorkspace.init(document);
        document.querySelector("[data-workspace-discover]").click();

        const files = document.querySelector("[data-workspace-files]");
        await vi.waitFor(() => expect(files.options).toHaveLength(2));
        files.value = "sheet-1";
        files.dispatchEvent(new Event("change", { bubbles: true }));

        const worksheet = document.querySelector('[name="workspace_worksheet_id"]');
        await vi.waitFor(() => expect(worksheet.options).toHaveLength(2));
        expect([...worksheet.options].map((option) => [option.value, option.text])).toEqual([
            ["12", "Orders"],
            ["19", "Archive"],
        ]);
        expect(worksheet.value).toBe("12");
        expect(document.querySelector("[data-workspace-worksheet-id]").textContent).toBe("12");
        expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
            "/orgs/acme/data-sources/workspace/google_workspace/files",
            "/orgs/acme/data-sources/workspace/google_workspace/worksheets?spreadsheet_id=sheet-1",
        ]);
    });

    it("KeepsUnavailableSavedWorksheetExplicit", async () => {
        vi.stubGlobal(
            "fetch",
            vi.fn().mockResolvedValue(response({ worksheets: [{ worksheet_id: 12, title: "Orders" }] })),
        );
        document.body.innerHTML = workspacePanel({ spreadsheetID: "sheet-1", worksheetID: "99" });

        window.BraidplaneWorkspace.init(document);

        const worksheet = document.querySelector('[name="workspace_worksheet_id"]');
        await vi.waitFor(() => expect(worksheet.options).toHaveLength(2));
        expect([...worksheet.options].map((option) => [option.value, option.text])).toEqual([
            ["12", "Orders"],
            ["99", "Unavailable worksheet"],
        ]);
        expect(worksheet.value).toBe("99");
        expect(document.querySelector("[data-workspace-worksheet-id]").textContent).toBe("99");
    });

    it("ShowsDriveCreateAndUpdateFieldsForTheirOperations", () => {
        document.body.innerHTML = workspacePanel({ operation: "create" });
        window.BraidplaneWorkspace.init(document);

        const panel = document.querySelector("[data-workspace-operation]");
        const fields = (name) => panel.querySelector(`[data-workspace-fields="${name}"]`);
        expect(fields("create").classList.contains("hidden")).toBe(false);
        expect(fields("folder").classList.contains("hidden")).toBe(false);
        expect(fields("file-target").classList.contains("hidden")).toBe(true);

        const operation = panel.querySelector('[name="workspace_operation"]');
        operation.value = "update";
        operation.dispatchEvent(new Event("change", { bubbles: true }));
        expect(fields("create").classList.contains("hidden")).toBe(true);
        expect(fields("folder").classList.contains("hidden")).toBe(true);
        expect(fields("file-target").classList.contains("hidden")).toBe(false);
        expect(fields("file-write").classList.contains("hidden")).toBe(false);
    });

    it("DistinguishesEmptyDiscoveryFromUnavailableFiles", async () => {
        const fetchMock = vi
            .fn()
            .mockResolvedValueOnce(response({ files: [] }))
            .mockResolvedValueOnce(response({}, 403));
        vi.stubGlobal("fetch", fetchMock);
        document.body.innerHTML = `${workspacePanel()}${workspacePanel()}`;
        const panels = document.querySelectorAll("[data-workspace-operation]");
        window.BraidplaneWorkspace.init(document);

        panels[0].querySelector("[data-workspace-discover]").click();
        await vi.waitFor(() =>
            expect(panels[0].querySelector("[data-workspace-feedback]").textContent).toBe(
                "No accessible files on this page.",
            ),
        );
        expect(panels[0].querySelector("[data-workspace-files]").classList.contains("hidden")).toBe(false);

        panels[1].querySelector("[data-workspace-discover]").click();
        await vi.waitFor(() =>
            expect(panels[1].querySelector("[data-workspace-feedback]").textContent).toBe(
                "Use the connected Google account and check file access.",
            ),
        );
    });
});

describe("selected-file authorization", () => {
    const settings = {
        client_id: "client",
        developer_key: "public-key",
        app_id: "123",
        account_email: "connected@example.com",
        expected_revision: 7,
    };
    const scope = "https://www.googleapis.com/auth/drive.file";
    const setup = (verifyStatus = 200) => {
        let tokenConfig;
        let pickerCallback;
        const dispose = vi.fn();
        const tokenRequest = vi.fn();
        const builder = {
            setDeveloperKey: vi.fn().mockReturnThis(),
            setAppId: vi.fn().mockReturnThis(),
            setOAuthToken: vi.fn().mockReturnThis(),
            setOrigin: vi.fn().mockReturnThis(),
            addView: vi.fn().mockReturnThis(),
            setCallback: vi.fn((callback) => {
                pickerCallback = callback;
                return builder;
            }),
            build: () => ({ setVisible: vi.fn(), dispose }),
        };
        vi.stubGlobal("google", {
            accounts: {
                oauth2: {
                    initTokenClient: vi.fn((config) => {
                        tokenConfig = config;
                        return { requestAccessToken: tokenRequest };
                    }),
                },
            },
            picker: {
                Action: { PICKED: "picked", CANCEL: "cancel" },
                // biome-ignore lint/complexity/useArrowFunction: Google calls this API with new.
                PickerBuilder: function () {
                    return builder;
                },
                // biome-ignore lint/complexity/useArrowFunction: Google calls this API with new.
                DocsView: function () {
                    return {
                        setEnableDrives: vi.fn().mockReturnThis(),
                        setIncludeFolders() {
                            return this;
                        },
                        setSelectFolderEnabled() {
                            return this;
                        },
                    };
                },
            },
        });
        const fetchMock = vi.fn().mockImplementation((url) =>
            Promise.resolve(
                url.endsWith("/picker")
                    ? response(settings)
                    : response(
                          {
                              files: [
                                  {
                                      file_id: "authorized-file",
                                      name: "Verified name",
                                      mime_type: "application/json",
                                  },
                              ],
                          },
                          verifyStatus,
                      ),
            ),
        );
        vi.stubGlobal("fetch", fetchMock);
        document.head.innerHTML = '<meta name="csrf-token" content="csrf-test" />';
        document.body.innerHTML = workspacePanel().replace(
            '<button type="button" data-workspace-discover>',
            '<button type="button" data-workspace-pick>Select</button><button type="button" data-workspace-discover>',
        );
        window.BraidplaneWorkspace.init(document);
        return {
            fetchMock,
            builder,
            dispose,
            tokenRequest,
            tokenConfig: () => tokenConfig,
            callback: (data) => pickerCallback(data),
        };
    };
    it("authorizes per-file browser access and uses backend-verified metadata", async () => {
        const fixture = setup();
        await vi.waitFor(() => expect(fixture.tokenConfig()).toBeDefined());
        const button = document.querySelector("[data-workspace-pick]");
        button.click();
        expect(fixture.tokenConfig().scope).toBe(scope);
        expect(fixture.tokenConfig().include_granted_scopes).toBe(false);
        expect(fixture.tokenConfig().login_hint).toBe(settings.account_email);
        expect(fixture.tokenRequest).toHaveBeenCalledExactlyOnceWith({ prompt: "consent" });
        fixture.tokenConfig().callback({ scope, access_token: "browser-only-token" });
        expect(fixture.builder.addView).toHaveBeenCalledTimes(2);
        expect(fixture.builder.addView.mock.calls[0][0].setEnableDrives).not.toHaveBeenCalled();
        expect(fixture.builder.addView.mock.calls[1][0].setEnableDrives).toHaveBeenCalledWith(true);
        const verification = fixture.callback({
            action: "picked",
            docs: [{ id: "authorized-file", name: "Untrusted name" }],
        });
        expect(button.disabled).toBe(true);
        await verification;
        expect(button.disabled).toBe(false);
        expect(document.querySelector('[name="workspace_file_id"]').value).toBe("authorized-file");
        expect(fixture.dispose).toHaveBeenCalledOnce();
        button.click();
        expect(fixture.tokenRequest).toHaveBeenLastCalledWith({ prompt: "" });
        const [url, options] = fixture.fetchMock.mock.calls[1];
        expect(url).toContain("/files/verify");
        expect(JSON.parse(options.body)).toEqual({ file_ids: ["authorized-file"], expected_revision: 7 });
        expect(options.headers["X-CSRF-Token"]).toBe("csrf-test");
        expect(JSON.stringify(fixture.fetchMock.mock.calls)).not.toContain("browser-only-token");
    });
    it("retries library loading for a later panel after a failure", async () => {
        vi.stubGlobal("google", undefined);
        vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(settings)));
        const append = vi.spyOn(document.head, "append").mockImplementation((element) => element.onerror());
        for (const attempts of [2, 4]) {
            document.body.innerHTML = workspacePanel().replace(
                '<button type="button" data-workspace-discover>',
                '<button type="button" data-workspace-pick>Select</button><button type="button" data-workspace-discover>',
            );
            window.BraidplaneWorkspace.init(document);
            await vi.waitFor(() =>
                expect(document.querySelector("[data-workspace-feedback]").textContent).toContain(
                    "File selection unavailable",
                ),
            );
            expect(append).toHaveBeenCalledTimes(attempts);
        }
    });
    it("rejects broad browser scopes before opening Picker", async () => {
        const fixture = setup();
        await vi.waitFor(() => expect(fixture.tokenConfig()).toBeDefined());
        fixture
            .tokenConfig()
            .callback({ scope: `${scope} https://www.googleapis.com/auth/drive`, access_token: "broad-token" });
        expect(fixture.builder.setOAuthToken).not.toHaveBeenCalled();
        expect(document.querySelector('[name="workspace_file_id"]').value).toBe("");
    });
    it("keeps the operation unchanged after revision conflict or cancellation", async () => {
        const fixture = setup(409);
        await vi.waitFor(() => expect(fixture.tokenConfig()).toBeDefined());
        fixture.tokenConfig().callback({ scope, access_token: "browser-only-token" });
        await fixture.callback({ action: "picked", docs: [{ id: "stale-file" }] });
        expect(document.querySelector('[name="workspace_file_id"]').value).toBe("");
        expect(document.querySelector("[data-workspace-feedback]").textContent).toContain("Source changed");
        fixture.tokenConfig().callback({ scope, access_token: "browser-only-token" });
        await fixture.callback({ action: "cancel" });
        expect(fixture.fetchMock).toHaveBeenCalledTimes(2);
        expect(document.querySelector("[data-workspace-feedback]").textContent).toContain("cancelled");
    });
});
