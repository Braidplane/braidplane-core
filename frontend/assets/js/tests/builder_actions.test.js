import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

await import("../builder_actions.js");

const t = window.braidplaneBuilderActions._testing;

beforeEach(() => {
    vi.useFakeTimers();
    document.body.innerHTML = "";
    window.history.replaceState({}, "", "/");
});

afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
});

const fetchResponse = (status, body) => ({
    ok: status >= 200 && status < 300,
    status,
    text: vi.fn().mockResolvedValue(body),
    json: vi.fn().mockResolvedValue(body ? JSON.parse(body) : {}),
});

describe("showNotification", () => {
    it("ErrorWithDetails_PausesDismissWhileDetailsOpen", () => {
        const notification = t.showNotification(
            "Failed to execute pipeline: duplicate key",
            "error",
            "Failed to execute pipeline: Duplicate entry '1' for key 'users.PRIMARY'",
        );

        const details = notification.querySelector("details");
        expect(details).not.toBeNull();

        details.open = true;
        details.dispatchEvent(new Event("toggle"));

        vi.advanceTimersByTime(5000);

        expect(document.body.contains(notification)).toBe(true);
    });

    it("ClosedDetails_AutoDismissesAfterTimeout", () => {
        const notification = t.showNotification(
            "Failed to execute pipeline: duplicate key",
            "error",
            "Failed to execute pipeline: Duplicate entry '1' for key 'users.PRIMARY'",
        );

        const details = notification.querySelector("details");
        expect(details).not.toBeNull();

        details.open = true;
        details.dispatchEvent(new Event("toggle"));
        details.open = false;
        details.dispatchEvent(new Event("toggle"));

        vi.advanceTimersByTime(5000);

        expect(document.body.contains(notification)).toBe(false);
    });

    it("ErrorNotification_UsesOpaqueAlertColors", () => {
        const notification = t.showNotification("Pipeline failed", "error", "duplicate key");

        expect(notification.style.opacity).toBe("1");
        expect(notification.style.backgroundColor).toBe("var(--color-error)");
        expect(notification.style.color).toBe("var(--color-error-content)");
    });
});

describe("handleRunPipeline", () => {
    it("SavedDraftBlocksExecutionAndExplainsPublishOrDiscardRecovery", async () => {
        document.body.innerHTML = `
            <div id="builder-canvas-root" data-org-slug="acme" data-project-slug="operations"
                data-target-environment-slug="dev" data-pipeline-slug="users-sync" data-pipeline-has-draft="true"></div>
            <button id="builder-run-btn">Run in dev</button>
        `;
        vi.stubGlobal("fetch", vi.fn());
        await t.handleRunPipeline(new Event("click"));
        expect(fetch).not.toHaveBeenCalled();
        expect(document.body.textContent).toContain("Propose, merge and sync the saved draft");
        expect(document.body.textContent).toContain("discard it in Git settings");
    });

    it("BindsLateBuilderActionsOnceAfterNavigation", async () => {
        document.body.innerHTML = `
            <div id="builder-canvas-root" data-org-slug="acme" data-project-slug="operations"
                data-target-environment-slug="dev" data-pipeline-slug="users-sync"></div>
            <button id="builder-run-btn">Run in dev</button>
        `;
        vi.stubGlobal("fetch", vi.fn().mockResolvedValue(fetchResponse(202, JSON.stringify({ metadata: {} }))));
        await import("../builder_actions.js?late-load");
        document.dispatchEvent(new Event("htmx:afterSwap"));
        document.dispatchEvent(new Event("htmx:afterSwap"));
        document.getElementById("builder-run-btn").click();
        await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
        await vi.waitFor(() => expect(document.body.textContent).toContain("Run requested in dev"));
    });

    it("ProtectedRetryRunFailure_PreservesRunRecordDetails", async () => {
        document.body.innerHTML = `
            <select data-org-switcher>
                <option value="acme" selected>ACME</option>
            </select>
            <div
                id="builder-canvas-root"
                data-project-slug="operations"
                data-target-environment-slug="prod"
                data-pipeline-slug="users-sync"
                data-pipeline-has-draft="false"
            ></div>
            <button id="builder-run-btn">Run</button>
        `;

        vi.stubGlobal(
            "fetch",
            vi
                .fn()
                .mockResolvedValueOnce(
                    fetchResponse(
                        400,
                        JSON.stringify({
                            error: {
                                code: "PROTECTED_ENVIRONMENT_CONFIRMATION_REQUIRED",
                                message: "protected environment confirmation required",
                            },
                        }),
                    ),
                )
                .mockResolvedValueOnce(
                    fetchResponse(
                        422,
                        JSON.stringify({
                            metadata: {
                                run_slug: "run-failed",
                            },
                            timeline: {
                                failure: {
                                    kind: "data_source",
                                    side: "source",
                                    data_source_slug: "orders-db",
                                    action_hint: "Check the data-source binding.",
                                    error: "connection refused",
                                },
                            },
                        }),
                    ),
                ),
        );
        const promptSpy = vi.spyOn(window, "prompt").mockReturnValue("prod");

        await t.handleRunPipeline(new Event("click"));

        expect(fetch).toHaveBeenCalledTimes(2);
        expect(fetch).toHaveBeenNthCalledWith(
            1,
            "/orgs/acme/projects/operations/pipelines/users-sync/execute?environment_slug=prod",
            expect.objectContaining({ method: "POST" }),
        );
        expect(promptSpy).toHaveBeenCalledWith("Type prod to confirm running this protected runtime target.");
        expect(document.body.textContent).toContain("Data source error (source step): orders-db");
        expect(document.body.textContent).toContain("Check the data-source binding.");
        expect(document.body.textContent).toContain("connection refused");
    });

    it("AcceptedRunNamesTargetWithoutClaimingCompletion", async () => {
        document.body.innerHTML = `
            <div data-org-switcher><button>ACME</button></div>
            <div
                id="builder-canvas-root"
                data-org-slug="acme"
                data-project-slug="operations"
                data-target-environment-slug="prod"
                data-pipeline-slug="users-sync"
                data-pipeline-has-draft="false"
            ></div>
            <button id="builder-run-btn">Run in prod</button>
        `;

        vi.stubGlobal(
            "fetch",
            vi.fn().mockResolvedValue(
                fetchResponse(
                    202,
                    JSON.stringify({
                        metadata: {
                            run_slug: "run-001",
                            status: "queued",
                        },
                    }),
                ),
            ),
        );

        await t.handleRunPipeline(new Event("click"));

        expect(fetch).toHaveBeenCalledWith(
            "/orgs/acme/projects/operations/pipelines/users-sync/execute?environment_slug=prod",
            expect.objectContaining({ method: "POST" }),
        );
        expect(document.body.textContent).toContain("Run requested in prod (run: run-001)");
        expect(document.body.textContent).not.toContain("executed");
        expect(document.querySelector(".alert a")).toBeNull();
        expect(document.querySelector(".alert").classList.contains("alert-info")).toBe(true);
        expect(document.querySelector(".alert").style.maxWidth).toBe("calc(100vw - 2rem)");
        expect(document.getElementById("builder-run-btn").disabled).toBe(false);
        fetch.mockResolvedValueOnce(fetchResponse(202, JSON.stringify({ metadata: { status: "queued" } })));
        document.querySelector(".alert").remove();
        await t.handleRunPipeline(new Event("click"));
        expect(document.body.textContent).toContain("Run requested in prod");
        expect(document.querySelector(".alert a")).toBeNull();
    });
});

describe("backfillFromForm", () => {
    it("builds watermark and cursor envelopes without exposing cursor values elsewhere", () => {
        document.body.innerHTML = `
          <fieldset data-backfill-source="orders" data-checkpoint-kind="watermark">
            <input name="lower_bound" value="1"><input name="upper_bound" value="10">
          </fieldset>
          <fieldset data-backfill-source="contacts" data-checkpoint-kind="cursor">
            <input name="lower_bound" value="opaque-a"><input name="upper_bound" value="opaque-b">
          </fieldset>`;
        expect(JSON.parse(JSON.stringify(t.backfillFromForm()))).toEqual({
            source_windows: {
                orders: { lower_bound: { value: 1 }, upper_bound: { value: 10 } },
                contacts: { lower_bound: { cursor: "opaque-a" }, upper_bound: { cursor: "opaque-b" } },
            },
        });
        document.querySelector('[name="lower_bound"]').value = "9007199254740993";
        expect(JSON.stringify(t.backfillFromForm())).toContain('"value":9007199254740993');
        document.querySelector('[name="lower_bound"]').value = "0.12345678901234567890123456789";
        expect(JSON.stringify(t.backfillFromForm())).toContain('"value":0.12345678901234567890123456789');
    });
    it("rejects absent sources and missing bounds", () => {
        expect(() => t.backfillFromForm()).toThrow("no incremental sources");
        document.body.innerHTML = `<fieldset data-backfill-source="source" data-checkpoint-kind="watermark"><input name="lower_bound" value=""><input name="upper_bound" value="10"></fieldset>`;
        expect(() => t.backfillFromForm()).toThrow("both bounds");
    });
});

it("bootstraps initial checkpoints through the environment-scoped route", async () => {
    document.body.innerHTML = `
      <div id="builder-canvas-root" data-org-slug="acme" data-project-slug="operations" data-target-environment-slug="prod" data-pipeline-slug="users-sync"></div>
      <button id="builder-backfill-btn">Bootstrap checkpoints</button>
      <input id="builder-bootstrap-request" value="bootstrap-001">
      <fieldset data-backfill-source="source" data-checkpoint-kind="cursor"><input name="lower_bound" value="opaque-a"></fieldset>`;
    window.history.replaceState({}, "", "/builder?org_slug=acme&project_slug=operations&pipeline_slug=users-sync");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(fetchResponse(204, "")));
    await t.handleRunPipeline({ preventDefault() {} }, "bootstrap");
    expect(fetch).toHaveBeenCalledWith(
        "/orgs/acme/projects/operations/pipelines/users-sync/bootstrap-checkpoints?environment_slug=prod",
        expect.objectContaining({
            method: "POST",
            body: JSON.stringify({ source_seeds: { source: { cursor: "opaque-a" } }, request_slug: "bootstrap-001" }),
        }),
    );
    expect(document.body.textContent).toContain("Initial checkpoints bootstrapped in prod");
});
