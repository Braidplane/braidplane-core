(() => {
    const DRAFT_PIPELINE_SLUG = "draft-pipeline";
    const FORMAT_YAML = "yaml";
    const FORMAT_JSON = "json";

    const parseBool = (value) => {
        return typeof value === "string" && value.trim().toLowerCase() === "true";
    };

    const parseOptionalVersionToken = (value) => {
        if (typeof value !== "string") {
            return value == null ? null : String(value);
        }
        const trimmed = value.trim();
        return trimmed === "" ? null : trimmed;
    };

    const stripConflictDiffVersionMetadata = (pipeline) => {
        if (!pipeline || typeof pipeline !== "object" || Array.isArray(pipeline)) {
            return {};
        }

        const normalized = { ...pipeline };
        delete normalized.expected_version_id;
        delete normalized.current_version_id;
        delete normalized.draft_based_on_version_id;
        delete normalized.expected_spec_hash;
        delete normalized.current_version_spec_hash;
        delete normalized.draft_based_on_spec_hash;
        return normalized;
    };

    const savedPipelineVersionContext = (savedPipeline, currentVersionState = {}) => {
        const savedDraftBasedOnVersionId = parseOptionalVersionToken(
            savedPipeline?.draft_based_on_spec_hash ?? savedPipeline?.draft?.based_on_spec_hash,
        );
        const currentExpectedVersionId = parseOptionalVersionToken(currentVersionState.expectedVersionId);
        const currentCurrentVersionId = parseOptionalVersionToken(currentVersionState.currentVersionId);
        const currentDraftBasedOnVersionId = parseOptionalVersionToken(currentVersionState.draftBasedOnVersionId);

        return {
            expectedVersionId: savedDraftBasedOnVersionId ?? currentExpectedVersionId,
            currentVersionId:
                parseOptionalVersionToken(
                    savedPipeline?.current_version_spec_hash ?? savedPipeline?.draft?.current_version_spec_hash,
                ) ?? currentCurrentVersionId,
            draftBasedOnVersionId: savedDraftBasedOnVersionId ?? currentDraftBasedOnVersionId,
        };
    };

    const storageKeyFor = (pipelineSlug, organizationSlug, projectSlug, memberSlug) => {
        if (!organizationSlug || !projectSlug || !memberSlug) return null;
        return `braidplane.builder.canvas.state.v6/${[memberSlug, organizationSlug, projectSlug, pipelineSlug || DRAFT_PIPELINE_SLUG].map(encodeURIComponent).join("/")}`;
    };

    const parseJSONMap = (value) => {
        if (typeof value !== "string" || value.trim() === "") {
            return {};
        }

        try {
            const parsed = JSON.parse(value);
            if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
                return parsed;
            }
        } catch (_) {
            return {};
        }

        return {};
    };

    const safeOperationObject = (value) => {
        if (value && typeof value === "object" && !Array.isArray(value)) {
            return value;
        }
        return {};
    };

    const normalizePipelineFormat = (value, fallback = FORMAT_YAML) => {
        const normalized = typeof value === "string" ? value.trim().toLowerCase() : "";
        if (normalized === FORMAT_JSON) {
            return FORMAT_JSON;
        }
        if (normalized === FORMAT_YAML) {
            return FORMAT_YAML;
        }
        return fallback;
    };

    const validationStatusMessage = (issueCount) => {
        return issueCount === 0
            ? "Validation passed: pipeline can be saved."
            : `Save blocked: resolve ${issueCount} validation issues first.`;
    };

    const builderSaveAvailability = (pipelineSlug, nodes) => {
        if (!pipelineSlug || pipelineSlug === DRAFT_PIPELINE_SLUG) {
            return {
                canPersist: false,
                reason: "Save Draft is available after a pipeline has been materialized from the project Git repository.",
                localSaveMessage: "Saved locally. Select a Git-authored pipeline before saving a server draft.",
            };
        }
        if (!Array.isArray(nodes) || nodes.length === 0) {
            return {
                canPersist: false,
                reason: "Save to Braidplane is unavailable until the canvas has at least one node.",
                localSaveMessage: "Saved locally. Add at least one node before saving to Braidplane.",
            };
        }

        return {
            canPersist: true,
            reason: validationStatusMessage(0),
            localSaveMessage: "Saved locally and to Braidplane.",
        };
    };

    const sortKeysStable = (value) => {
        if (Array.isArray(value)) {
            return value.map(sortKeysStable);
        }
        if (value !== null && typeof value === "object") {
            return Object.fromEntries(
                Object.keys(value)
                    .sort()
                    .map((key) => [key, sortKeysStable(value[key])]),
            );
        }
        return value;
    };

    const stableJSONStringify = (value) => JSON.stringify(sortKeysStable(value));

    const cloneJSONValue = (value) => {
        if (value === undefined) {
            return undefined;
        }
        if (typeof structuredClone === "function") {
            return structuredClone(value);
        }
        return JSON.parse(JSON.stringify(value));
    };

    const escapeHTML = (value) => {
        return String(value)
            .replace(/&/g, "&amp;")
            .replace(/</g, "&lt;")
            .replace(/>/g, "&gt;")
            .replace(/"/g, "&quot;")
            .replace(/'/g, "&#39;");
    };

    const isPlainObject = (value) => value !== null && typeof value === "object" && !Array.isArray(value);

    const valuesAreEqual = (left, right) => stableJSONStringify(left) === stableJSONStringify(right);

    const formatJSONMap = (value) => {
        if (!value || typeof value !== "object" || Array.isArray(value) || Object.keys(value).length === 0) {
            return "";
        }

        try {
            return JSON.stringify(value, null, 2);
        } catch (_) {
            return "";
        }
    };

    const hasObjectEntries = (value) => {
        return Boolean(value) && typeof value === "object" && !Array.isArray(value) && Object.keys(value).length > 0;
    };

    const saveStatusMessageFromPipeline = () => {
        return "Draft saved to Braidplane. Use Propose to send the change to Git.";
    };

    const runStatusMessage = (pipelineSlug, isValid, hasDraft, targetEnvironmentSlug = "", canExecute = false) => {
        if (!canExecute) {
            return "You don't have permission to execute pipelines.";
        }
        if (!isValid) {
            return "Run is unavailable until blocking validation issues are resolved.";
        }
        if (hasDraft) {
            return "A saved draft is active. Review Git settings to merge, sync or discard it before running the deployed version.";
        }
        const runtimeTarget = String(targetEnvironmentSlug || "").trim();
        if (!runtimeTarget) {
            return "Run is unavailable until a project runtime target is selected.";
        }
        if (!pipelineSlug || pipelineSlug === DRAFT_PIPELINE_SLUG) {
            return "Run is unavailable for draft builder state. Load a saved pipeline with a real slug first.";
        }

        return `Run will execute pipeline "${pipelineSlug}" in runtime target "${runtimeTarget}".`;
    };

    const isJSONResponse = (response) => {
        const contentType = (response?.headers?.get?.("Content-Type") || "").toLowerCase();
        return contentType.includes("application/json");
    };

    const saveResponseProblem = (response) => {
        const responseURL = typeof response?.url === "string" ? response.url : "";
        if (response?.redirected || responseURL.includes("/auth/login")) {
            return "Saved locally, but your session expired. Sign in again and retry.";
        }
        if (!isJSONResponse(response)) {
            return "Saved locally, but server save returned an unexpected response.";
        }
        return "";
    };

    window.braidplaneBuilderCanvasShared = {
        DRAFT_PIPELINE_SLUG,
        FORMAT_YAML,
        FORMAT_JSON,
        parseBool,
        parseOptionalVersionToken,
        stripConflictDiffVersionMetadata,
        savedPipelineVersionContext,
        storageKeyFor,
        parseJSONMap,
        safeOperationObject,
        normalizePipelineFormat,
        validationStatusMessage,
        builderSaveAvailability,
        sortKeysStable,
        stableJSONStringify,
        cloneJSONValue,
        escapeHTML,
        isPlainObject,
        valuesAreEqual,
        formatJSONMap,
        hasObjectEntries,
        saveStatusMessageFromPipeline,
        runStatusMessage,
        isJSONResponse,
        saveResponseProblem,
    };
})();
