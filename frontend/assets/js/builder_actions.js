(() => {
    const lastErrorSegment = (input) => {
        if (typeof input !== "string") {
            return "";
        }

        const segments = input
            .split(":")
            .map((segment) => segment.trim())
            .filter((segment) => segment !== "");

        if (segments.length === 0) {
            return input.trim();
        }

        return segments[segments.length - 1];
    };

    const apiErrorMessageFrom = (payload) => {
        if (!payload || typeof payload !== "object") {
            return "";
        }

        const detailed =
            payload.error && typeof payload.error === "object" && typeof payload.error.detailed_error === "string"
                ? payload.error.detailed_error.trim()
                : "";

        const mergeMessageAndDetail = (message) => {
            const base = typeof message === "string" ? message.trim() : "";
            if (!base) {
                return detailed;
            }
            if (!detailed) {
                return base;
            }
            return `${base}: ${detailed}`;
        };

        if (typeof payload.message === "string" && payload.message.trim() !== "") {
            return mergeMessageAndDetail(payload.message.trim());
        }

        if (typeof payload.error === "string" && payload.error.trim() !== "") {
            return mergeMessageAndDetail(payload.error.trim());
        }

        if (payload.error && typeof payload.error === "object") {
            if (typeof payload.error.message === "string" && payload.error.message.trim() !== "") {
                return mergeMessageAndDetail(payload.error.message.trim());
            }
            if (typeof payload.error.code === "string" && payload.error.code.trim() !== "") {
                return mergeMessageAndDetail(payload.error.code.trim());
            }
        }

        return detailed;
    };

    // Extract organization and pipeline slugs from shell and builder DOM state.
    const getContextSlugs = () => {
        const orgSwitcher = document.querySelector("[data-org-switcher]");
        const builderRoot = document.getElementById("builder-canvas-root");

        let orgSlug = builderRoot instanceof HTMLElement ? builderRoot.dataset.orgSlug || "" : "";
        if (!orgSlug && orgSwitcher instanceof HTMLSelectElement) orgSlug = orgSwitcher.value;
        if (!orgSlug) {
            const match = window.location.pathname.match(/\/orgs\/([^/]+)/);
            orgSlug = match ? decodeURIComponent(match[1]) : "";
        }
        const pipelineSlug = builderRoot instanceof HTMLElement ? builderRoot.dataset.pipelineSlug || "" : "";
        const projectSlug = builderRoot instanceof HTMLElement ? builderRoot.dataset.projectSlug || "" : "";
        const environmentSlug =
            builderRoot instanceof HTMLElement ? builderRoot.dataset.targetEnvironmentSlug || "" : "";
        const pipelineHasDraft =
            builderRoot instanceof HTMLElement ? (builderRoot.dataset.pipelineHasDraft || "false") === "true" : false;

        if (orgSlug && projectSlug && environmentSlug && pipelineSlug) {
            return { orgSlug, projectSlug, environmentSlug, pipelineSlug, pipelineHasDraft };
        }

        return null;
    };

    const showNotification = (message, type = "info", details = "") => {
        // Create a simple notification (can be replaced with DaisyUI toast later)
        const notification = document.createElement("div");
        notification.className = `alert alert-${type} fixed bottom-4 right-4 z-50 w-96 shadow-lg`;
        notification.style.opacity = "1";
        notification.style.maxWidth = "calc(100vw - 2rem)";
        notification.style.backgroundColor = `var(--color-${type})`;
        notification.style.color = `var(--color-${type}-content)`;

        let removeTimeoutId = 0;

        const clearRemovalTimer = () => {
            if (removeTimeoutId) {
                window.clearTimeout(removeTimeoutId);
                removeTimeoutId = 0;
            }
        };

        const removeNotification = () => {
            clearRemovalTimer();
            notification.remove();
        };

        const scheduleRemoval = (detailsEl) => {
            clearRemovalTimer();
            removeTimeoutId = window.setTimeout(() => {
                if (detailsEl instanceof HTMLDetailsElement && detailsEl.open) {
                    return;
                }
                notification.remove();
            }, 5000);
        };

        const content = document.createElement("div");
        content.className = "flex min-w-0 flex-col gap-2 break-words";

        const summary = document.createElement("span");
        summary.textContent = message;
        content.appendChild(summary);

        let detailsEl = null;

        if (typeof details === "string" && details.trim() !== "" && details.trim() !== message.trim()) {
            detailsEl = document.createElement("details");
            detailsEl.className = "text-xs opacity-90";

            const detailsSummary = document.createElement("summary");
            detailsSummary.className = "cursor-pointer";
            detailsSummary.textContent = "Show details";
            detailsEl.appendChild(detailsSummary);

            const pre = document.createElement("pre");
            pre.className = "whitespace-pre-wrap break-words mt-1";
            pre.textContent = details;
            detailsEl.appendChild(pre);

            detailsEl.addEventListener("toggle", () => {
                if (detailsEl.open) {
                    clearRemovalTimer();
                    return;
                }
                scheduleRemoval(detailsEl);
            });

            content.appendChild(detailsEl);
        }

        const close = document.createElement("button");
        close.className = "btn btn-sm btn-ghost";
        close.textContent = "✕";
        close.style.color = "inherit";
        close.setAttribute("aria-label", "Dismiss notification");
        close.addEventListener("click", () => {
            removeNotification();
        });

        notification.appendChild(content);
        notification.appendChild(close);
        document.body.appendChild(notification);

        // Auto-hide after 5 seconds
        scheduleRemoval(detailsEl);

        return notification;
    };

    const failureSideLabel = (side) => {
        switch (side) {
            case "source":
                return "source step";
            case "sink":
                return "sink step";
            case "transform":
                return "transform step";
            case "connector":
                return "connector";
            default:
                return side;
        }
    };

    const backfillFromForm = (mode = "backfill") => {
        const source_windows = {};
        for (const fieldset of document.querySelectorAll("[data-backfill-source]")) {
            const bound = (name) => {
                const text = fieldset.querySelector(`[name="${name}"]`).value;
                if (!text.trim()) throw new Error("Enter both bounds for every source");
                if (fieldset.dataset.checkpointKind === "cursor") return { cursor: text };
                let value = text;
                if (/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/.test(text)) {
                    value = JSON.rawJSON(text);
                }
                return { value };
            };
            source_windows[fieldset.dataset.backfillSource] =
                mode === "bootstrap"
                    ? bound("lower_bound")
                    : {
                          lower_bound: bound("lower_bound"),
                          upper_bound: bound("upper_bound"),
                      };
        }
        if (!Object.keys(source_windows).length) throw new Error("This pipeline has no incremental sources");
        return mode === "bootstrap" ? { source_seeds: source_windows } : { source_windows };
    };

    const handleRunPipeline = async (e, mode = "run") => {
        const backfill = mode === "backfill";
        const bootstrap = mode === "bootstrap";
        e.preventDefault();

        const context = getContextSlugs();
        if (!context?.orgSlug || !context?.projectSlug || !context?.environmentSlug || !context?.pipelineSlug) {
            showNotification("Unable to determine organization, project, runtime target, or pipeline slug", "error");
            return;
        }
        const { orgSlug, projectSlug, environmentSlug, pipelineSlug, pipelineHasDraft } = context;

        if (pipelineHasDraft) {
            showNotification(
                "Propose, merge and sync the saved draft, or discard it in Git settings before running the deployment.",
                "error",
            );
            return;
        }

        const runBtn = document.getElementById(backfill || bootstrap ? "builder-backfill-btn" : "builder-run-btn");
        const originalText = runBtn.textContent;
        const originalDisabled = runBtn.disabled;

        let backfillRequest;
        if (backfill || bootstrap) {
            try {
                backfillRequest = backfillFromForm(mode);
                if (bootstrap) {
                    backfillRequest.request_slug = document.getElementById("builder-bootstrap-request").value.trim();
                    if (!backfillRequest.request_slug) throw new Error("Enter a bootstrap request slug");
                }
            } catch (error) {
                showNotification(error.message, "error");
                return;
            }
        }
        const submitRun = (confirmation) => {
            const body = bootstrap ? { ...backfillRequest } : backfillRequest ? { backfill: backfillRequest } : {};
            if (typeof confirmation === "string" && confirmation.trim() !== "") {
                body.protected_environment_confirmation = confirmation.trim();
            }
            return fetch(
                `/orgs/${encodeURIComponent(orgSlug)}/projects/${encodeURIComponent(projectSlug)}/pipelines/${encodeURIComponent(pipelineSlug)}/${bootstrap ? "bootstrap-checkpoints" : "execute"}?environment_slug=${encodeURIComponent(environmentSlug)}`,
                {
                    method: "POST",
                    headers: {
                        "Content-Type": "application/json",
                    },
                    body: JSON.stringify(body),
                },
            );
        };

        const handlePipelineRunFailureResponse = async (response) => {
            // 422 means the pipeline ran but failed (e.g. data source error).
            // The body is a PipelineRunRecord with full failure context.
            const rawBody = await response.text();
            let runRecord = null;
            try {
                runRecord = rawBody ? JSON.parse(rawBody) : null;
            } catch (_) {
                // not a run record - treat as generic error below
            }

            const failure = runRecord?.timeline?.failure;
            if (failure) {
                const kind = failure.kind;
                const side = failure.side;
                const dsSlug = failure.data_source_slug;
                const actionHint = failure.action_hint;
                const runSlug = runRecord?.metadata?.run_slug;

                const kindLabel = kind === "data_source" ? "Data source error" : "Platform error";
                let primaryMsg = kindLabel;
                if (side) primaryMsg += ` (${failureSideLabel(side)})`;
                if (dsSlug) primaryMsg += `: ${dsSlug}`;

                const detailParts = [];
                if (actionHint) detailParts.push(actionHint);
                if (failure.error) detailParts.push(failure.error);
                const detailText = detailParts.join("\n\n");

                showNotification(primaryMsg, "error", detailText);

                if (runSlug) {
                    setTimeout(() => {
                        window.location.href = `/orgs/${encodeURIComponent(orgSlug)}/projects/${encodeURIComponent(projectSlug)}/runs/${encodeURIComponent(runSlug)}?environment_slug=${encodeURIComponent(environmentSlug)}`;
                    }, 3000);
                }
                return true;
            }

            // Body was consumed but wasn't a valid run record - parse as error response.
            let errorData = {};
            try {
                errorData = rawBody ? JSON.parse(rawBody) : {};
            } catch (_) {
                errorData = {};
            }
            const fullErrorMessage =
                apiErrorMessageFrom(errorData) || (rawBody.trim() !== "" ? rawBody.trim() : "") || "HTTP 422";
            const err = new Error(lastErrorSegment(fullErrorMessage) || fullErrorMessage);
            err.fullDetails = fullErrorMessage;
            throw err;
        };

        try {
            // Set button to loading state
            runBtn.disabled = true;
            runBtn.classList.add("loading");
            runBtn.textContent = "Running...";

            // Submit run request via frontend route.
            let response = await submitRun("");

            if (response.status === 422) {
                if (await handlePipelineRunFailureResponse(response)) {
                    return;
                }
            }

            if (!response.ok) {
                const rawBody = await response.text();
                let errorData = {};
                try {
                    errorData = rawBody ? JSON.parse(rawBody) : {};
                } catch (_) {
                    errorData = {};
                }
                if (errorData?.error?.code === "PROTECTED_ENVIRONMENT_CONFIRMATION_REQUIRED") {
                    const confirmation = window.prompt(
                        `Type ${environmentSlug} to confirm ${bootstrap ? "bootstrapping checkpoints in" : "running"} this protected runtime target.`,
                    );
                    if (confirmation === null) {
                        return;
                    }
                    response = await submitRun(confirmation);
                    if (response.ok) {
                        // Continue through the common success path below.
                    } else if (response.status === 422) {
                        if (await handlePipelineRunFailureResponse(response)) {
                            return;
                        }
                    } else {
                        const retryRawBody = await response.text();
                        try {
                            errorData = retryRawBody ? JSON.parse(retryRawBody) : {};
                        } catch (_) {
                            errorData = {};
                        }
                        const retryErrorMessage =
                            apiErrorMessageFrom(errorData) ||
                            (typeof retryRawBody === "string" && retryRawBody.trim() !== ""
                                ? retryRawBody.trim()
                                : "") ||
                            `HTTP ${response.status}`;
                        const err = new Error(lastErrorSegment(retryErrorMessage) || retryErrorMessage);
                        err.fullDetails = retryErrorMessage;
                        throw err;
                    }
                }
                if (!response.ok) {
                    const fullErrorMessage =
                        apiErrorMessageFrom(errorData) ||
                        (typeof rawBody === "string" && rawBody.trim() !== "" ? rawBody.trim() : "") ||
                        `HTTP ${response.status}`;
                    const err = new Error(lastErrorSegment(fullErrorMessage) || fullErrorMessage);
                    err.fullDetails = fullErrorMessage;
                    throw err;
                }
            }

            if (bootstrap) {
                showNotification(`Initial checkpoints bootstrapped in ${environmentSlug}`, "success");
                return;
            }
            const result = await response.json();
            const runSlug = result.metadata?.run_slug;

            showNotification(
                runSlug
                    ? `Run requested in ${environmentSlug} (run: ${runSlug})`
                    : `Run requested in ${environmentSlug}`,
                "info",
            );

            if (runSlug) {
                const runHref = `/orgs/${encodeURIComponent(orgSlug)}/projects/${encodeURIComponent(projectSlug)}/runs/${encodeURIComponent(runSlug)}?environment_slug=${encodeURIComponent(environmentSlug)}`;
                setTimeout(() => {
                    window.location.href = runHref;
                }, 250);
            }
        } catch (error) {
            const details = error && typeof error.fullDetails === "string" ? error.fullDetails : "";
            showNotification(`Failed to execute pipeline: ${error.message}`, "error", details);
            console.error("Pipeline execution error:", error);
        } finally {
            // Restore button state
            runBtn.disabled = originalDisabled;
            runBtn.classList.remove("loading");
            runBtn.textContent = originalText;
        }
    };

    const init = () => {
        const runBtn = document.getElementById("builder-run-btn");
        if (runBtn && runBtn.dataset.builderActionsReady !== "true") {
            runBtn.dataset.builderActionsReady = "true";
            runBtn.addEventListener("click", (event) => handleRunPipeline(event));
        }
        const backfillForm = document.getElementById("builder-backfill-form");
        if (backfillForm && backfillForm.dataset.builderActionsReady !== "true") {
            backfillForm.dataset.builderActionsReady = "true";
            backfillForm.addEventListener("submit", (event) =>
                handleRunPipeline(event, document.getElementById("builder-incremental-action").value),
            );
        }
        const incrementalAction = document.getElementById("builder-incremental-action");
        if (!incrementalAction || incrementalAction.dataset.builderActionsReady === "true") return;
        incrementalAction.dataset.builderActionsReady = "true";
        incrementalAction.addEventListener("change", (event) => {
            const bootstrap = event.target.value === "bootstrap";
            const request = document.getElementById("builder-bootstrap-request");
            request.disabled = !bootstrap;
            request.required = bootstrap;
            const field = document.getElementById("builder-bootstrap-request-field");
            field.classList.toggle("hidden", !bootstrap);
            field.classList.toggle("flex", bootstrap);
            document.querySelectorAll("[data-backfill-upper]").forEach((label) => {
                label.classList.toggle("hidden", bootstrap);
                label.querySelector("input").disabled = bootstrap;
            });
            document.querySelectorAll("[data-incremental-lower-label]").forEach((label) => {
                label.textContent = bootstrap ? "Initial checkpoint" : "From";
            });
            const environment = getContextSlugs()?.environmentSlug;
            const action = bootstrap ? "Bootstrap checkpoints" : "Start backfill";
            document.getElementById("builder-backfill-btn").textContent = environment
                ? `${action} in ${environment}`
                : action;
        });
    };

    document.addEventListener("DOMContentLoaded", init);
    document.addEventListener("htmx:afterSwap", init);
    if (document.readyState !== "loading") init();

    window.braidplaneBuilderActions = {
        _testing: {
            handleRunPipeline,
            backfillFromForm,
            showNotification,
        },
    };
})();
