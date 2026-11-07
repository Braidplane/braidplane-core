import { beforeEach, describe, expect, it } from "vitest";

// Side-effect imports: register builder helper namespaces before the canvas IIFE.
await import("../builder_canvas_shared.js");
await import("../builder_canvas_rendering.js");
await import("../builder_canvas_state.js");
await import("../builder_canvas_conflicts.js");
await import("../builder_canvas_serialization.js");
await import("../builder_canvas_api.js");
await import("../builder_canvas_interactions.js");

// Side-effect import: runs the IIFE and sets window.braidplaneBuilderCanvas.
await import("../builder_canvas.js");

const t = window.braidplaneBuilderCanvas._testing;

const issueMessages = (issues) => issues.map((issue) => (typeof issue === "string" ? issue : issue.message));

const resetLocalStorage = () => {
    const store = new Map();
    Object.defineProperty(window, "localStorage", {
        configurable: true,
        value: {
            getItem: (key) => (store.has(String(key)) ? store.get(String(key)) : null),
            setItem: (key, value) => {
                store.set(String(key), String(value));
            },
            removeItem: (key) => {
                store.delete(String(key));
            },
            clear: () => {
                store.clear();
            },
        },
    });
};

beforeEach(() => {
    resetLocalStorage();
    document.body.innerHTML = "";
});

describe("parseJSONMap", () => {
    it("EmptyString_ReturnsEmptyObject", () => {
        expect(t.parseJSONMap("")).toEqual({});
    });

    it("WhitespaceOnly_ReturnsEmptyObject", () => {
        expect(t.parseJSONMap("   ")).toEqual({});
    });

    it("InvalidJSON_ReturnsEmptyObject", () => {
        expect(t.parseJSONMap("not json")).toEqual({});
    });

    it("JSONArray_ReturnsEmptyObject", () => {
        // Only plain objects are valid; arrays are rejected.
        expect(t.parseJSONMap("[1, 2, 3]")).toEqual({});
    });

    it("JSONStringScalar_ReturnsEmptyObject", () => {
        expect(t.parseJSONMap('"just a string"')).toEqual({});
    });

    it("JSONNull_ReturnsEmptyObject", () => {
        expect(t.parseJSONMap("null")).toEqual({});
    });

    it("ValidObject_ReturnsParsed", () => {
        expect(t.parseJSONMap('{"key":"val","n":42}')).toEqual({ key: "val", n: 42 });
    });

    it("NestedObject_ReturnsParsed", () => {
        expect(t.parseJSONMap('{"a":{"b":1}}')).toEqual({ a: { b: 1 } });
    });
});

describe("formatJSONMap", () => {
    it("EmptyObject_ReturnsEmptyString", () => {
        expect(t.formatJSONMap({})).toBe("");
    });

    it("Null_ReturnsEmptyString", () => {
        expect(t.formatJSONMap(null)).toBe("");
    });

    it("Array_ReturnsEmptyString", () => {
        expect(t.formatJSONMap([])).toBe("");
    });

    it("UndefinedValue_ReturnsEmptyString", () => {
        expect(t.formatJSONMap(undefined)).toBe("");
    });

    it("SingleKey_FormattedWithIndentation", () => {
        expect(t.formatJSONMap({ key: "val" })).toBe('{\n  "key": "val"\n}');
    });

    it("MultipleKeys_FormattedInOrder", () => {
        const result = t.formatJSONMap({ a: 1, b: "two" });
        expect(result).toBe('{\n  "a": 1,\n  "b": "two"\n}');
    });
});

describe("storageKeyFor", () => {
    it("ValidSlug_UsesSlugInKey", () => {
        expect(t.storageKeyFor("my-pipeline", "acme", "ops", "alice")).toBe(
            "braidplane.builder.canvas.state.v6/alice/acme/ops/my-pipeline",
        );
    });

    it("EmptyString_FallsBackToDraftPipeline", () => {
        expect(t.storageKeyFor("", "acme", "ops", "alice")).toBe(
            "braidplane.builder.canvas.state.v6/alice/acme/ops/draft-pipeline",
        );
    });

    it("Null_FallsBackToDraftPipeline", () => {
        expect(t.storageKeyFor(null, "acme", "ops", "alice")).toBe(
            "braidplane.builder.canvas.state.v6/alice/acme/ops/draft-pipeline",
        );
    });

    it("Undefined_FallsBackToDraftPipeline", () => {
        expect(t.storageKeyFor(undefined, "acme", "ops", "alice")).toBe(
            "braidplane.builder.canvas.state.v6/alice/acme/ops/draft-pipeline",
        );
    });

    it("SlugWithMultipleSegments_PreservedExactly", () => {
        expect(t.storageKeyFor("org-data-ingest-v2", "acme", "ops", "alice")).toBe(
            "braidplane.builder.canvas.state.v6/alice/acme/ops/org-data-ingest-v2",
        );
    });
    it("separates organizations and projects and refuses missing context", () => {
        const key = t.storageKeyFor("orders", "acme", "ops", "alice");
        expect(t.storageKeyFor("orders", "other", "ops", "alice")).not.toBe(key);
        expect(t.storageKeyFor("orders", "acme", "other", "alice")).not.toBe(key);
        expect(t.storageKeyFor("orders", "", "ops", "alice")).toBeNull();
        expect(t.storageKeyFor("orders", "acme", "", "alice")).toBeNull();
        expect(t.storageKeyFor("orders", "acme", "ops", "")).toBeNull();
        expect(t.storageKeyFor("orders", "acme", "ops", "bob")).not.toBe(key);
        window.localStorage.setItem(
            "braidplane.builder.canvas.state.v5.orders",
            JSON.stringify({
                pipeline_spec: { metadata: { slug: "orders", name: "Orders" }, steps: [] },
                graph: { nodes: [], edges: [], bindings: [] },
            }),
        );
        expect(t.loadState("braidplane.builder.canvas.state.v5.orders")).not.toBeNull();
        expect(t.loadState(key)).toBeNull();
        window.localStorage.clear();
        t.saveState(null, null, null, [], {});
        expect(window.localStorage.getItem(null)).toBeNull();
    });
});

describe("current pipeline compatibility", () => {
    it("ImportedPipeline_PreservesStepOrderAndUneditedCanonicalFields", () => {
        const pipelineSpec = {
            metadata: { slug: "acme-users-sync", name: "ACME Users Sync" },
            steps: [
                {
                    kind: "query",
                    slug: "fetch-users",
                    name: "Fetch users",
                    query: {
                        data_source_ref: "acme-postgres",
                        operation_type: "sql",
                        operation_ref: "list-active-users",
                        output_contract: {
                            name: "",
                            representation: "rows",
                            fields: [{ name: "email", type: "string", required: true }],
                        },
                    },
                },
                {
                    kind: "sink",
                    slug: "acme-users-sync",
                    sink: {
                        input: "fetch-users",
                        input_contract: {
                            name: "",
                            representation: "rows",
                            fields: [{ name: "email", type: "string", required: true }],
                        },
                        data_source_ref: "acme-mysql",
                        operation_type: "sql",
                        operation_ref: "upsert-users",
                        config: { mode: "upsert", table: "users" },
                        idempotency_class: "deduplicated_by_ledger",
                    },
                },
            ],
        };
        const imported = t.normalizeImportedBuilderState({
            pipeline_spec: pipelineSpec,
            graph: {
                nodes: [{ step_slug: "fetch-users" }, { step_slug: "acme-users-sync" }],
                edges: [{ from_step: "fetch-users", to_step: "acme-users-sync" }],
                bindings: [
                    {
                        from_step: "fetch-users",
                        from_port: "rows",
                        to_step: "acme-users-sync",
                        to_port: "payload",
                    },
                ],
            },
        });

        const rebuilt = t.buildBuilderStatePayload(imported.nodes, imported.edges, imported.bindings, {
            slug: pipelineSpec.metadata.slug,
            name: pipelineSpec.metadata.name,
        });

        expect(rebuilt.pipeline_spec).toEqual(pipelineSpec);
    });
});

describe("normalizePipelineFormat", () => {
    it("JSONValue_ReturnsJSON", () => {
        expect(t.normalizePipelineFormat("json")).toBe("json");
    });

    it("UppercaseYAML_ReturnsNormalizedYAML", () => {
        expect(t.normalizePipelineFormat("YAML")).toBe("yaml");
    });

    it("InvalidValue_ReturnsFallback", () => {
        expect(t.normalizePipelineFormat("xml", "json")).toBe("json");
    });

    it("MissingValue_ReturnsDefaultYAML", () => {
        expect(t.normalizePipelineFormat(undefined)).toBe("yaml");
    });
});

describe("connector descriptors", () => {
    it("SnakeCasePayload_NormalizesCapabilityFields", () => {
        expect(
            t.normalizeConnectorDescriptor({
                type: "GitHub",
                schema_family: "file",
                query_operation_type: "git",
                sink_backend: "git",
                supports_pipeline_source: true,
                supports_pipeline_sink: true,
                incremental_source_checkpoint_kinds: ["watermark", ""],
                forced_read_only: false,
            }),
        ).toEqual({
            type: "github",
            schemaFamily: "file",
            queryOperationType: "git",
            sinkBackend: "git",
            supportsPipelineSource: true,
            supportsPipelineSink: true,
            incrementalSourceCheckpointKinds: ["watermark"],
            supportedSinkReplayStrategies: [],
            forcedReadOnly: false,
        });
    });

    it("ReplayPolicies_NormalizeBothSpellingsAndSurviveRoundTrip", () => {
        for (const key of ["supported_sink_replay_strategies", "supportedSinkReplayStrategies"]) {
            const descriptor = t.normalizeConnectorDescriptor({
                type: "s3",
                [key]: [" DEDUPLICATED_BY_LEDGER ", "unsafe", "unsupported", null],
            });
            expect(descriptor.supportedSinkReplayStrategies).toEqual(["deduplicated_by_ledger", "unsafe"]);
            expect(t.normalizeConnectorDescriptor(descriptor).supportedSinkReplayStrategies).toEqual(
                descriptor.supportedSinkReplayStrategies,
            );
        }
        expect(
            t.normalizeConnectorDescriptor({ type: "s3", supportedSinkReplayStrategies: {} })
                .supportedSinkReplayStrategies,
        ).toEqual([]);
    });

    it("DOMPayload_LoadsDescriptorMapFromTextarea", () => {
        document.body.innerHTML = `<textarea id="builder-connector-descriptors">[
            {
                "type": "hubspot",
                "schema_family": "object_api",
                "query_operation_type": "hubspot",
                "sink_backend": "unknown",
                "supports_pipeline_source": true,
                "supports_pipeline_sink": false,
                "forced_read_only": true
            }
        ]</textarea>`;

        expect(t.readConnectorDescriptorPayload().get("hubspot")).toEqual(
            expect.objectContaining({
                queryOperationType: "hubspot",
                supportsPipelineSink: false,
                forcedReadOnly: true,
            }),
        );
    });

    it("EnrichDataSource_UsesDescriptorCapabilitiesForSinkEligibility", () => {
        const descriptors = t.connectorDescriptorMap([
            {
                type: "hubspot",
                schema_family: "object_api",
                query_operation_type: "hubspot",
                sink_backend: "unknown",
                supports_pipeline_source: true,
                supports_pipeline_sink: false,
                forced_read_only: true,
            },
            {
                type: "github",
                schema_family: "file",
                query_operation_type: "git",
                sink_backend: "git",
                supports_pipeline_source: true,
                supports_pipeline_sink: true,
            },
        ]);

        expect(t.enrichDataSourceWithDescriptor({ slug: "hubspot", type: "hubspot" }, descriptors)).toEqual(
            expect.objectContaining({
                queryOperationType: "hubspot",
                supportsPipelineSource: true,
                supportsPipelineSink: false,
                forcedReadOnly: true,
                readOnly: true,
            }),
        );
        expect(t.enrichDataSourceWithDescriptor({ slug: "repo", type: "github" }, descriptors)).toEqual(
            expect.objectContaining({
                queryOperationType: "git",
                sinkBackend: "git",
                supportsPipelineSink: true,
            }),
        );
    });

    it("LegacySinkBackend_AlignsProviderGitTypesToCatalogBackend", () => {
        expect(t.legacySinkBackendFromDataSourceType("github")).toBe("git");
        expect(t.legacySinkBackendFromDataSourceType("gitlab")).toBe("git");
    });

    it("QueryContextForOperationType_PreservesStoredSQLWhenMetadataMissing", () => {
        expect(t.queryContextForOperationType("sql")).toEqual(
            expect.objectContaining({
                supportsInlineSQL: true,
                supportsInlineOperationJSON: false,
            }),
        );
    });

    it("SinkContextForOperationType_HidesWritableOptionsForReadOnlyDataSource", () => {
        expect(t.sinkContextForOperationType("git", "git", true)).toEqual(
            expect.objectContaining({
                operationType: "",
                hint: "This data source is read-only and cannot be used as a pipeline sink.",
            }),
        );
    });

    it("SinkContextForOperationType_KeepsWritableOptionsForKnownSinkBackend", () => {
        expect(t.sinkContextForOperationType("git", "git", false)).toEqual(
            expect.objectContaining({
                operationType: "git",
                hint: "GIT sinks support saved queries or inline operation JSON.",
            }),
        );
    });

    it("SinkContextForOperationType_HidesWritableOptionsForMissingSinkBackend", () => {
        expect(t.sinkContextForOperationType("git", "", false)).toEqual(
            expect.objectContaining({
                operationType: "",
                hint: "Select a writable data source to configure a saved query or inline operation.",
            }),
        );
    });
});

describe("builderWorkbenchLayoutForWidth", () => {
    it("DesktopWidth_DocksPropertiesAsRightRail", () => {
        expect(t.builderWorkbenchLayoutForWidth(1120, true, true)).toEqual(
            expect.objectContaining({
                canUsePaletteColumn: true,
                canDockProperties: true,
                gridTemplateColumns: "220px minmax(0, 1fr) 320px",
                propertiesGridColumn: "",
            }),
        );
    });

    it("CompactWidth_StacksPropertiesBelowWorkbench", () => {
        expect(t.builderWorkbenchLayoutForWidth(840, true, true)).toEqual(
            expect.objectContaining({
                canUsePaletteColumn: false,
                canDockProperties: false,
                gridTemplateColumns: "minmax(0, 1fr)",
                propertiesGridColumn: "1 / -1",
            }),
        );
    });
});

describe("setPrimaryActionDisabledState", () => {
    it("RunButton_RequiresRuntimeTarget", () => {
        const saveButton = document.createElement("button");
        const runButton = document.createElement("button");
        runButton.dataset.canExecute = "true";
        const publishButton = document.createElement("button");

        t.setPrimaryActionDisabledState(
            saveButton,
            runButton,
            publishButton,
            true,
            true,
            false,
            "users-sync",
            false,
            "",
        );

        expect(runButton.disabled).toBe(true);
        expect(runButton.title).toBe("Run is unavailable until a project runtime target is selected.");

        t.setPrimaryActionDisabledState(
            saveButton,
            runButton,
            publishButton,
            true,
            true,
            false,
            "users-sync",
            false,
            "prod",
        );

        expect(runButton.disabled).toBe(false);
        expect(runButton.title).toBe('Run will execute pipeline "users-sync" in runtime target "prod".');
        runButton.dataset.canExecute = "false";
        t.setPrimaryActionDisabledState(
            saveButton,
            runButton,
            publishButton,
            true,
            true,
            false,
            "users-sync",
            false,
            "prod",
        );
        expect(runButton.disabled).toBe(true);
        expect(runButton.title).toBe("You don't have permission to execute pipelines.");
        delete runButton.dataset.canExecute;
        t.setPrimaryActionDisabledState(
            saveButton,
            runButton,
            publishButton,
            true,
            true,
            false,
            "users-sync",
            false,
            "prod",
        );
        expect(runButton.disabled).toBe(true);
    });
});

describe("validationStatusMessage", () => {
    it("ZeroIssues_ReturnsSuccessCopy", () => {
        expect(t.validationStatusMessage(0)).toBe("Validation passed: pipeline can be saved.");
    });

    it("PositiveIssueCount_MentionsSaveBlocking", () => {
        expect(t.validationStatusMessage(3)).toBe("Save blocked: resolve 3 validation issues first.");
    });
});

describe("saveStatusMessageFromPipeline", () => {
    it("MaterializedPipeline_ExplainsProposalPath", () => {
        expect(t.saveStatusMessageFromPipeline()).toBe(
            "Draft saved to Braidplane. Use Propose to send the change to Git.",
        );
    });
});

describe("builderSaveAvailability", () => {
    it("DraftPipelineWithNodes_RequiresMaterializedPipeline", () => {
        expect(t.builderSaveAvailability("draft-pipeline", [{ slug: "fetch-users" }])).toEqual({
            canPersist: false,
            reason: "Save Draft is available after a pipeline has been materialized from the project Git repository.",
            localSaveMessage: "Saved locally. Select a Git-authored pipeline before saving a server draft.",
        });
    });

    it("EmptyCanvas_DisablesServerSave", () => {
        expect(t.builderSaveAvailability("acme-users-sync", [])).toEqual({
            canPersist: false,
            reason: "Save to Braidplane is unavailable until the canvas has at least one node.",
            localSaveMessage: "Saved locally. Add at least one node before saving to Braidplane.",
        });
    });

    it("PersistedPipelineWithNodes_EnablesServerSave", () => {
        expect(t.builderSaveAvailability("acme-users-sync", [{ slug: "fetch-users" }])).toEqual({
            canPersist: true,
            reason: "Validation passed: pipeline can be saved.",
            localSaveMessage: "Saved locally and to Braidplane.",
        });
    });
});

describe("saveResponseProblem", () => {
    const responseLike = ({ contentType = "application/json", redirected = false, url = "" } = {}) => ({
        redirected,
        url,
        headers: {
            get: (name) => (name === "Content-Type" ? contentType : null),
        },
    });

    it("RedirectedResponse_ExplainsSessionExpiry", () => {
        expect(t.saveResponseProblem(responseLike({ redirected: true, url: "/auth/login?return_to=%2Fbuilder" }))).toBe(
            "Saved locally, but your session expired. Sign in again and retry.",
        );
    });

    it("LoginURL_ExplainsSessionExpiry", () => {
        expect(t.saveResponseProblem(responseLike({ url: "http://localhost:3000/auth/login" }))).toBe(
            "Saved locally, but your session expired. Sign in again and retry.",
        );
    });

    it("NonJSONResponse_IsRejected", () => {
        expect(t.saveResponseProblem(responseLike({ contentType: "text/html" }))).toBe(
            "Saved locally, but server save returned an unexpected response.",
        );
    });

    it("JSONResponse_HasNoProblem", () => {
        expect(t.saveResponseProblem(responseLike())).toBe("");
    });
});

describe("comparableBuilderSnapshot", () => {
    it("IgnoresContextFields", () => {
        const snapshot = t.comparableBuilderSnapshot({
            pipeline_spec: {
                metadata: { slug: "acme-users-sync", name: "ACME Users Sync" },
                steps: [{ kind: "query", slug: "fetch-users", name: "Fetch users" }],
            },
            graph: {
                nodes: [{ step_slug: "fetch-users", x: 1, y: 1 }],
                edges: [],
                bindings: [],
            },
            context: {
                expected_version_id: 1,
                current_version_id: 2,
            },
        });

        expect(snapshot).toContain('"pipeline_spec"');
        expect(snapshot).toContain('"graph"');
        expect(snapshot).not.toContain("expected_version_id");
        expect(snapshot).not.toContain("current_version_id");
    });

    it("SortsObjectKeysDeterministically", () => {
        const left = t.comparableBuilderSnapshot({
            graph: { bindings: [], edges: [], nodes: [] },
            pipeline_spec: {
                metadata: { name: "ACME Users Sync", slug: "acme-users-sync" },
                steps: [],
            },
        });
        const right = t.comparableBuilderSnapshot({
            pipeline_spec: {
                steps: [],
                metadata: { slug: "acme-users-sync", name: "ACME Users Sync" },
            },
            graph: { nodes: [], edges: [], bindings: [] },
        });

        expect(left).toBe(right);
    });

    it("SortsComparableArraysByCanonicalKeys", () => {
        const snapshot = JSON.parse(
            t.comparableBuilderSnapshot({
                pipeline_spec: {
                    metadata: { slug: "acme-users-sync", name: "ACME Users Sync" },
                    steps: [
                        { kind: "sink", slug: "sink-b" },
                        { kind: "query", slug: "query-a" },
                    ],
                },
                graph: {
                    nodes: [
                        { step_slug: "sink-b", x: 9, y: 1 },
                        { step_slug: "query-a", x: 2, y: 1 },
                    ],
                    edges: [
                        { from_step: "sink-b", to_step: "query-a" },
                        { from_step: "query-a", to_step: "sink-b" },
                    ],
                    bindings: [
                        { from_step: "sink-b", from_port: "rows", to_step: "query-a", to_port: "payload" },
                        { from_step: "query-a", from_port: "rows", to_step: "sink-b", to_port: "payload" },
                    ],
                },
            }),
        );

        expect(snapshot.pipeline_spec.steps.map((step) => step.slug)).toEqual(["query-a", "sink-b"]);
        expect(snapshot.graph.nodes.map((node) => node.step_slug)).toEqual(["query-a", "sink-b"]);
        expect(snapshot.graph.edges).toEqual([
            { from_step: "query-a", to_step: "sink-b" },
            { from_step: "sink-b", to_step: "query-a" },
        ]);
        expect(snapshot.graph.bindings).toEqual([
            { from_step: "query-a", from_port: "rows", to_step: "sink-b", to_port: "payload" },
            { from_step: "sink-b", from_port: "rows", to_step: "query-a", to_port: "payload" },
        ]);
    });
});

describe("setPrimaryActionDisabledState", () => {
    it("InvalidState_DisablesSaveAndRunButtons", () => {
        const saveButton = document.createElement("button");
        const runButton = document.createElement("button");

        t.setPrimaryActionDisabledState(saveButton, runButton, null, false, true, true, "acme-users-sync", false);

        expect(saveButton.disabled).toBe(true);
        expect(runButton.disabled).toBe(true);
    });

    it("ValidState_EnablesSaveAndRunForPersistedPipeline", () => {
        const saveButton = document.createElement("button");
        const runButton = document.createElement("button");
        runButton.dataset.canExecute = "true";
        const publishButton = document.createElement("button");
        saveButton.disabled = true;
        runButton.disabled = true;
        publishButton.disabled = true;

        t.setPrimaryActionDisabledState(
            saveButton,
            runButton,
            publishButton,
            true,
            true,
            true,
            "acme-users-sync",
            false,
            "prod",
        );

        expect(saveButton.disabled).toBe(false);
        expect(runButton.disabled).toBe(false);
    });

    it("SavedDraftWithoutLocalChanges_EnablesProposalPublish", () => {
        const saveButton = document.createElement("button");
        const runButton = document.createElement("button");
        const publishButton = document.createElement("button");

        t.setPrimaryActionDisabledState(
            saveButton,
            runButton,
            publishButton,
            true,
            true,
            false,
            "acme-users-sync",
            true,
            "prod",
        );

        expect(saveButton.disabled).toBe(true);
        expect(saveButton.title).toBe("No unsaved changes.");
        expect(publishButton.disabled).toBe(false);
    });

    it("ValidDraftState_LeavesRunDisabled", () => {
        const saveButton = document.createElement("button");
        const runButton = document.createElement("button");

        t.setPrimaryActionDisabledState(saveButton, runButton, null, true, true, true, "draft-pipeline", false);

        expect(saveButton.disabled).toBe(false);
        expect(runButton.disabled).toBe(true);
    });
});

describe("saveState and loadState", () => {
    it("SaveState_PreservesEventInputsAndNamedODCSAttachments", () => {
        const inputRef = {
            contract_slug: "events",
            revision_slug: "rev-7",
            schema_object_id: "event",
            representation: "json",
            root_shape: "object",
        };
        const steps = [
            { kind: "input", slug: "event", name: "Incoming event", input: { output_contract_ref: inputRef } },
            {
                kind: "transform",
                slug: "normalize",
                transform: {
                    sql: "select * from events",
                    inputs: { events: "event" },
                    input_contracts: { events: { contract_ref: inputRef } },
                },
            },
        ];
        const state = {
            pipeline_spec: { metadata: { slug: "event-pipeline", name: "Events" }, steps },
            graph: {
                nodes: steps.map((step) => ({ step_slug: step.slug })),
                edges: [{ from_step: "event", to_step: "normalize" }],
                bindings: [{ from_step: "event", from_port: "rows", to_step: "normalize", to_port: "events" }],
            },
        };
        const imported = t.normalizeImportedBuilderState(state);
        const grid = document.createElement("div");
        imported.nodes.forEach((node) => {
            const element = document.createElement("div");
            element.className = "builder-node";
            t.renderBuilderNodeContent(element, node);
            grid.append(element);
        });
        const edges = document.createElement("div");
        t.writeEdgesData(edges, imported.edges);
        t.saveState("event-state", grid, edges, imported.bindings);
        expect(JSON.parse(window.localStorage.getItem("event-state")).pipeline_spec.steps).toEqual(steps);
        const restored = t.loadState("event-state");
        expect(
            t.buildBuilderStatePayload(restored.nodes, restored.edges, restored.bindings).pipeline_spec.steps,
        ).toEqual(steps);
    });

    it("SaveState_PersistsPipelineFormats", () => {
        const gridElement = document.createElement("div");
        const node = document.createElement("div");
        node.className = "builder-node absolute";
        node.setAttribute("data-node-slug", "source-a");
        node.setAttribute("data-kind", "query");
        node.setAttribute("data-node-type", "query");
        node.setAttribute("data-step-slug", "source-a");
        node.setAttribute("data-step-name", "Source A");
        node.setAttribute("data-data-source-slug", "acme-postgres");
        node.setAttribute("data-query-slug", "list-active-users");
        node.setAttribute("data-grid-x", "2");
        node.setAttribute("data-grid-y", "1");
        gridElement.appendChild(node);

        const edgesDataElement = document.createElement("div");
        const edge = document.createElement("div");
        edge.setAttribute("data-edge-from", "source-a");
        edge.setAttribute("data-edge-to", "sink-a");
        edgesDataElement.appendChild(edge);

        t.saveState(
            "braidplane.builder.canvas.test",
            gridElement,
            edgesDataElement,
            [{ fromNode: "source-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" }],
            { sourceFormat: "json", exportFormat: "yaml" },
        );

        expect(JSON.parse(window.localStorage.getItem("braidplane.builder.canvas.test"))).toEqual({
            pipeline_spec: {
                metadata: {
                    slug: "draft-pipeline",
                    name: "Draft Pipeline",
                },
                steps: [
                    {
                        kind: "query",
                        slug: "source-a",
                        name: "Source A",
                        query: {
                            data_source_ref: "acme-postgres",
                            operation_type: "sql",
                            operation_ref: "list-active-users",
                        },
                    },
                ],
            },
            graph: {
                nodes: [
                    {
                        step_slug: "source-a",
                    },
                ],
                edges: [{ from_step: "source-a", to_step: "sink-a" }],
                bindings: [{ from_step: "source-a", from_port: "rows", to_step: "sink-a", to_port: "payload" }],
            },
            context: {
                expected_spec_hash: null,
                current_version_spec_hash: null,
                draft_based_on_spec_hash: null,
                source_format: "json",
                export_format: "yaml",
            },
        });
    });

    it("LoadState_NormalizesPipelineFormats", () => {
        window.localStorage.setItem(
            "braidplane.builder.canvas.test",
            JSON.stringify({
                pipeline_spec: {
                    metadata: {
                        slug: "draft-pipeline",
                        name: "Draft Pipeline",
                    },
                    steps: [],
                },
                graph: {
                    nodes: [],
                    edges: [],
                    bindings: [],
                },
                context: {
                    source_format: "invalid",
                    export_format: "JSON",
                },
            }),
        );

        expect(t.loadState("braidplane.builder.canvas.test")).toEqual({
            gridColumns: 24,
            nodes: [],
            edges: [],
            bindings: [],
            pipeline: {
                slug: "draft-pipeline",
                name: "Draft Pipeline",
                source_format: "yaml",
                export_format: "json",
            },
        });
    });

    it("LoadState_PreservesLegacyPipelineSpecDataSourceSlug", () => {
        window.localStorage.setItem(
            "braidplane.builder.canvas.test",
            JSON.stringify({
                pipeline_spec: {
                    metadata: {
                        slug: "legacy-draft",
                        name: "Legacy Draft",
                    },
                    steps: [
                        {
                            kind: "query",
                            slug: "fetch-users",
                            name: "Fetch users",
                            query: {
                                data_source_slug: "acme-postgres",
                                operation_type: "sql",
                                operation_ref: "list-active-users",
                            },
                        },
                        {
                            kind: "sink",
                            slug: "write-users",
                            name: "Write users",
                            sink: {
                                input: "fetch-users",
                                data_source_slug: "acme-mysql",
                                operation_type: "sql",
                                operation_ref: "upsert-users",
                                idempotency_class: "idempotent_with_key",
                            },
                        },
                    ],
                },
                graph: {
                    nodes: [
                        { step_slug: "fetch-users", x: 2, y: 1 },
                        { step_slug: "write-users", x: 6, y: 1 },
                    ],
                    edges: [],
                    bindings: [],
                },
            }),
        );

        const restored = t.loadState("braidplane.builder.canvas.test");
        const queryNode = restored.nodes.find((node) => node.stepSlug === "fetch-users");
        const sinkNode = restored.nodes.find((node) => node.stepSlug === "write-users");

        expect(queryNode).toEqual(
            expect.objectContaining({
                stepSlug: "fetch-users",
                dataSourceSlug: "acme-postgres",
                operationType: "sql",
                querySlug: "list-active-users",
            }),
        );
        expect(sinkNode).toEqual(
            expect.objectContaining({
                stepSlug: "write-users",
                sinkDataSourceSlug: "acme-mysql",
                sinkOperationType: "sql",
                sinkQuerySlug: "upsert-users",
                sinkIdempotencyClass: "idempotent_with_key",
            }),
        );

        const payload = t.buildBuilderStatePayload(restored.nodes, restored.edges, restored.bindings);
        const queryStep = payload.pipeline_spec.steps.find((step) => step.slug === "fetch-users");
        const sinkStep = payload.pipeline_spec.steps.find((step) => step.slug === "write-users");
        expect(queryStep.query).toEqual({
            data_source_ref: "acme-postgres",
            operation_type: "sql",
            operation_ref: "list-active-users",
        });
        expect(sinkStep.sink).toEqual({
            data_source_ref: "acme-mysql",
            operation_type: "sql",
            operation_ref: "upsert-users",
            idempotency_class: "idempotent_with_key",
        });
    });

    it("SaveState_PersistsSinkWithOperationRefForRestore", () => {
        const gridElement = document.createElement("div");
        const node = document.createElement("div");
        node.className = "builder-node absolute";
        node.setAttribute("data-node-slug", "load-users");
        node.setAttribute("data-kind", "sink");
        node.setAttribute("data-step-slug", "load-users");
        node.setAttribute("data-step-name", "Load users");
        node.setAttribute("data-sink-data-source-slug", "acme-mysql");
        node.setAttribute("data-sink-operation-type", "sql");
        node.setAttribute("data-sink-query-slug", "upsert-users");
        node.setAttribute("data-sink-idempotency-class", "idempotent_with_key");
        node.setAttribute(
            "data-sink-input-contract",
            JSON.stringify({
                name: "users",
                representation: "rows",
                fields: [{ name: "email", type: "string", required: true }],
            }),
        );
        gridElement.appendChild(node);

        t.saveState("braidplane.builder.canvas.test", gridElement, document.createElement("div"), [], {
            sourceFormat: "yaml",
            exportFormat: "yaml",
        });

        const saved = JSON.parse(window.localStorage.getItem("braidplane.builder.canvas.test"));
        expect(saved.pipeline_spec.steps[0].sink).toEqual({
            data_source_ref: "acme-mysql",
            operation_type: "sql",
            operation_ref: "upsert-users",
            idempotency_class: "idempotent_with_key",
            input_contract: {
                name: "users",
                representation: "rows",
                fields: [{ name: "email", type: "string", required: true }],
            },
        });
        expect(saved.pipeline_spec.steps[0].sink).not.toHaveProperty("operation_inline");

        const restored = t.loadState("braidplane.builder.canvas.test");
        expect(restored.nodes[0]).toEqual(
            expect.objectContaining({
                kind: "sink",
                stepSlug: "load-users",
                stepName: "Load users",
                sinkDataSourceSlug: "acme-mysql",
                sinkOperationType: "sql",
                sinkQuerySlug: "upsert-users",
                sinkIdempotencyClass: "idempotent_with_key",
                sinkInputContract: {
                    name: "users",
                    representation: "rows",
                    fields: [{ name: "email", type: "string", required: true }],
                },
            }),
        );
    });

    it("SaveState_PersistsSinkWithOperationInlineForRestore", () => {
        const gridElement = document.createElement("div");
        const inlineNode = document.createElement("div");
        inlineNode.className = "builder-node absolute";
        inlineNode.setAttribute("data-node-slug", "write-users-inline");
        inlineNode.setAttribute("data-kind", "sink");
        inlineNode.setAttribute("data-step-slug", "write-users-inline");
        inlineNode.setAttribute("data-sink-data-source-slug", "acme-http");
        inlineNode.setAttribute("data-sink-operation-type", "http");
        inlineNode.setAttribute("data-sink-operation-inline", JSON.stringify({ method: "POST", path: "/users" }));
        inlineNode.setAttribute("data-sink-use-inline-operation", "true");
        inlineNode.setAttribute("data-sink-idempotency-class", "naturally_idempotent");
        gridElement.appendChild(inlineNode);

        t.saveState("braidplane.builder.canvas.test", gridElement, document.createElement("div"), [], {
            sourceFormat: "yaml",
            exportFormat: "yaml",
        });

        const saved = JSON.parse(window.localStorage.getItem("braidplane.builder.canvas.test"));
        expect(saved.pipeline_spec.steps[0].sink).toEqual({
            data_source_ref: "acme-http",
            operation_type: "http",
            operation_inline: { method: "POST", path: "/users" },
            idempotency_class: "naturally_idempotent",
        });
        expect(saved.pipeline_spec.steps[0].sink).not.toHaveProperty("operation_ref");

        const restored = t.loadState("braidplane.builder.canvas.test");
        expect(restored.nodes[0]).toEqual(
            expect.objectContaining({
                kind: "sink",
                stepSlug: "write-users-inline",
                sinkDataSourceSlug: "acme-http",
                sinkOperationType: "http",
                sinkOperationInline: { method: "POST", path: "/users" },
                sinkUseInlineOperation: true,
                sinkIdempotencyClass: "naturally_idempotent",
            }),
        );
    });

    it("SaveState_PersistsSinkEmptyInlineModeForRestore", () => {
        const gridElement = document.createElement("div");
        const node = document.createElement("div");
        node.className = "builder-node absolute";
        node.setAttribute("data-node-slug", "write-users-inline");
        node.setAttribute("data-kind", "sink");
        node.setAttribute("data-step-slug", "write-users-inline");
        node.setAttribute("data-sink-data-source-slug", "acme-http");
        node.setAttribute("data-sink-operation-type", "http");
        node.setAttribute("data-sink-operation-inline", "{}");
        node.setAttribute("data-sink-use-inline-operation", "true");
        node.setAttribute("data-sink-idempotency-class", "naturally_idempotent");
        gridElement.appendChild(node);

        t.saveState("braidplane.builder.canvas.test", gridElement, document.createElement("div"), [], {
            sourceFormat: "yaml",
            exportFormat: "yaml",
        });

        const saved = JSON.parse(window.localStorage.getItem("braidplane.builder.canvas.test"));
        expect(saved.pipeline_spec.steps[0].sink).toEqual({
            data_source_ref: "acme-http",
            operation_type: "http",
            operation_inline: {},
            idempotency_class: "naturally_idempotent",
        });
        expect(saved.pipeline_spec.steps[0].sink).not.toHaveProperty("operation_ref");

        const restored = t.loadState("braidplane.builder.canvas.test");
        expect(restored.nodes[0]).toEqual(
            expect.objectContaining({
                kind: "sink",
                stepSlug: "write-users-inline",
                sinkDataSourceSlug: "acme-http",
                sinkOperationType: "http",
                sinkOperationInline: {},
                sinkUseInlineOperation: true,
                sinkIdempotencyClass: "naturally_idempotent",
            }),
        );
    });
});

describe("savedPipelineVersionContext", () => {
    it("UsesDraftBaselineWhenSaveResponseIncludesIt", () => {
        expect(
            t.savedPipelineVersionContext(
                {
                    draft: {
                        based_on_spec_hash: "spec-hash-12",
                        current_version_spec_hash: "spec-hash-18",
                    },
                },
                {
                    expectedVersionId: "spec-hash-10",
                    currentVersionId: "spec-hash-17",
                    draftBasedOnVersionId: "spec-hash-10",
                },
            ),
        ).toEqual({
            expectedVersionId: "spec-hash-12",
            currentVersionId: "spec-hash-18",
            draftBasedOnVersionId: "spec-hash-12",
        });
    });

    it("PreservesExistingBaselineWhenSaveResponseOmitsBasedOnVersion", () => {
        expect(
            t.savedPipelineVersionContext(
                {
                    draft: {
                        current_version_spec_hash: "spec-hash-18",
                    },
                },
                {
                    expectedVersionId: "spec-hash-10",
                    currentVersionId: "spec-hash-17",
                    draftBasedOnVersionId: "spec-hash-10",
                },
            ),
        ).toEqual({
            expectedVersionId: "spec-hash-10",
            currentVersionId: "spec-hash-18",
            draftBasedOnVersionId: "spec-hash-10",
        });
    });
});

describe("normalizeBindings", () => {
    it("LegacyBindingShape_MapsToCanonicalFields", () => {
        expect(
            t.normalizeBindings([
                { nodeSlug: "transform-a", field: "rows", sourceNodeSlug: "source-a", sourceField: "rows" },
            ]),
        ).toEqual([{ fromNode: "source-a", fromPort: "rows", toNode: "transform-a", toPort: "rows" }]);
    });

    it("SnakeCaseBindingShape_MapsToCanonicalFields", () => {
        expect(
            t.normalizeBindings([
                { from_node: "transform-a", from_port: "rows", to_node: "sink-a", to_port: "payload" },
            ]),
        ).toEqual([{ fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" }]);
    });

    it("ExactDuplicateBindings_AreCollapsed", () => {
        expect(
            t.normalizeBindings([
                { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
                { from_node: "transform-a", from_port: "rows", to_node: "sink-a", to_port: "payload" },
            ]),
        ).toEqual([{ fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" }]);
    });
});

describe("serializeBindingsForExport", () => {
    it("CanonicalBindings_EmitSnakeCaseContract", () => {
        expect(
            t.serializeBindingsForExport([
                { fromNode: "source-a", fromPort: "rows", toNode: "transform-a", toPort: "rows" },
            ]),
        ).toEqual([{ from_step: "source-a", from_port: "rows", to_step: "transform-a", to_port: "rows" }]);
    });
});

describe("bindingsForTarget", () => {
    it("SinkPayload_ReturnsMultipleBindings", () => {
        expect(
            t.bindingsForTarget(
                [
                    { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
                    { fromNode: "transform-b", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
                    { fromNode: "source-a", fromPort: "rows", toNode: "transform-a", toPort: "rows" },
                ],
                "sink-a",
                "payload",
            ),
        ).toEqual([
            { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
            { fromNode: "transform-b", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
        ]);
    });
});

describe("normalizeStateForGridColumns", () => {
    it("LegacyState_PreservesNodeXAndAnnotatesColumns", () => {
        const state = {
            nodes: [
                { slug: "source-a", x: 0, y: 0 },
                { slug: "transform-a", x: 5, y: 1 },
                { slug: "sink-a", x: 9, y: 2 },
            ],
        };

        expect(t.normalizeStateForGridColumns(state)).toEqual({
            gridColumns: 24,
            nodes: [
                { slug: "source-a", x: 0, y: 0 },
                { slug: "transform-a", x: 5, y: 1 },
                { slug: "sink-a", x: 9, y: 2 },
            ],
        });
    });

    it("AlreadyDenseState_PreservesNodePositions", () => {
        const state = {
            gridColumns: 24,
            nodes: [{ slug: "transform-a", x: 14, y: 3 }],
            edges: [{ from: "source-a", to: "transform-a" }],
        };

        expect(t.normalizeStateForGridColumns(state)).toEqual({
            gridColumns: 24,
            nodes: [{ slug: "transform-a", x: 14, y: 3 }],
            edges: [{ from: "source-a", to: "transform-a" }],
        });
    });
});

describe("buildBuilderStatePayload", () => {
    it("PreservesCanonicalStepFieldsFromLiveNodeShape", () => {
        const payload = t.buildBuilderStatePayload(
            [
                {
                    slug: "fetch-users",
                    kind: "query",
                    x: 2,
                    y: 1,
                    stepSlug: "fetch-users",
                    stepName: "Fetch users",
                    dataSourceSlug: "acme-postgres",
                    operationType: "sql",
                    querySlug: "list-active-users",
                    parameters: { limit: 100 },
                    queryIncremental: {
                        checkpoint_kind: "watermark",
                        field: "updated_at",
                        ordering: ["updated_at", "id"],
                        dedupe_key: "id",
                    },
                    queryOutputContract: {
                        name: "",
                        representation: "rows",
                        fields: [
                            {
                                name: "email",
                                type: "string",
                                required: true,
                                validations: { format: "email", unique: true },
                            },
                        ],
                    },
                },
                {
                    slug: "load-users",
                    kind: "sink",
                    x: 8,
                    y: 1,
                    stepSlug: "load-users",
                    stepName: "Load users",
                    sinkDataSourceSlug: "acme-mysql",
                    sinkOperationType: "sql",
                    sinkQuerySlug: "insert-users",
                },
            ],
            [{ from: "fetch-users", to: "load-users" }],
            [{ fromNode: "fetch-users", fromPort: "rows", toNode: "load-users", toPort: "payload" }],
        );

        expect(payload.pipeline_spec.steps).toEqual([
            {
                kind: "query",
                slug: "fetch-users",
                name: "Fetch users",
                query: {
                    data_source_ref: "acme-postgres",
                    operation_type: "sql",
                    operation_ref: "list-active-users",
                    parameters: { limit: 100 },
                    output_contract: {
                        name: "fetch-users_output",
                        representation: "rows",
                        fields: [
                            {
                                name: "email",
                                type: "string",
                                required: true,
                                validations: { format: "email", unique: true },
                            },
                        ],
                    },
                    incremental: {
                        checkpoint_kind: "watermark",
                        field: "updated_at",
                        ordering: ["updated_at", "id"],
                        dedupe_key: "id",
                    },
                },
            },
            {
                kind: "sink",
                slug: "load-users",
                name: "Load users",
                sink: {
                    idempotency_class: "idempotent_with_key",
                    input: "fetch-users",
                    data_source_ref: "acme-mysql",
                    operation_type: "sql",
                    operation_ref: "insert-users",
                },
            },
        ]);

        expect(payload.graph).toEqual({
            nodes: [{ step_slug: "fetch-users" }, { step_slug: "load-users" }],
            edges: [{ from_step: "fetch-users", to_step: "load-users" }],
            bindings: [{ from_step: "fetch-users", from_port: "rows", to_step: "load-users", to_port: "payload" }],
        });
    });

    it("IncludesTargetEnvironmentContext", () => {
        const payload = t.buildBuilderStatePayload([], [], [], {
            targetOrgSlug: "acme-hq",
            targetProjectSlug: "data-pipelines",
            targetEnvironmentSlug: "prod",
        });

        expect(payload.context).toMatchObject({
            target_org_slug: "acme-hq",
            target_project_slug: "data-pipelines",
            target_environment_slug: "prod",
        });
    });

    it("BuildsNamedTransformInputsFromMultipleBindings", () => {
        const payload = t.buildBuilderStatePayload(
            [
                {
                    slug: "fetch-orders",
                    kind: "query",
                    stepSlug: "fetch-orders",
                    dataSourceSlug: "orders-db",
                    operationType: "sql",
                    querySlug: "list-orders",
                },
                {
                    slug: "fetch-accounts",
                    kind: "query",
                    stepSlug: "fetch-accounts",
                    dataSourceSlug: "crm-api",
                    operationType: "hubspot",
                    querySlug: "list-accounts",
                },
                {
                    slug: "join-orders",
                    kind: "transform",
                    stepSlug: "join-orders",
                    stepName: "Join orders",
                    transformSQL: "SELECT * FROM orders JOIN accounts USING (account_id)",
                    transformOutputKind: "rows",
                },
            ],
            [
                { from: "fetch-orders", to: "join-orders" },
                { from: "fetch-accounts", to: "join-orders" },
            ],
            [
                { fromNode: "fetch-orders", fromPort: "rows", toNode: "join-orders", toPort: "orders" },
                { fromNode: "fetch-accounts", fromPort: "rows", toNode: "join-orders", toPort: "accounts" },
            ],
        );

        expect(payload.pipeline_spec.steps[2].transform.inputs).toEqual({
            orders: "fetch-orders",
            accounts: "fetch-accounts",
        });
        expect(payload.pipeline_spec.steps[2].transform.sql).toBe(
            "SELECT * FROM orders JOIN accounts USING (account_id)",
        );
        expect(payload.graph.bindings).toEqual([
            { from_step: "fetch-orders", from_port: "rows", to_step: "join-orders", to_port: "orders" },
            { from_step: "fetch-accounts", from_port: "rows", to_step: "join-orders", to_port: "accounts" },
        ]);
    });
});

describe("renderBuilderNodeContent", () => {
    it("MaliciousStepText_RendersAsTextWithoutInjectingHTML", () => {
        const item = document.createElement("div");
        item.className = "builder-node";
        item.innerHTML = '<div class="builder-node-content"></div>';

        t.renderBuilderNodeContent(item, {
            slug: "transform-users",
            kind: "transform",
            stepSlug: "normalize-users",
            stepName:
                '<img src=x onerror="window.__builderInjected = true"><script>window.__builderScript = true</script>',
        });

        const content = item.querySelector(".builder-node-content");
        expect(content?.textContent).toContain("<img src=x");
        expect(content?.querySelector("img")).toBeNull();
        expect(content?.querySelector("script")).toBeNull();
        expect(window.__builderInjected).toBeUndefined();
        expect(window.__builderScript).toBeUndefined();
    });

    it("CanonicalSchemaHints_RenderAsReadableBadges", () => {
        const item = document.createElement("div");
        item.className = "builder-node";
        item.innerHTML = '<div class="builder-node-content"></div>';

        t.renderBuilderNodeContent(item, {
            slug: "read-orders",
            kind: "query",
            dataSourceSlug: "orders-postgres",
            operationType: "sql",
            querySlug: "list-orders-incremental",
            queryIncremental: {
                checkpoint_kind: "watermark",
                field: "updated_at",
            },
            queryOutputContract: {
                representation: "rows",
                fields: [
                    { name: "id", type: "string", validations: { format: "uuid" } },
                    { name: "status", type: "string", validations: { enum: ["active", "pending"] } },
                ],
            },
        });

        expect(item.tabIndex).toBe(0);
        expect(item.getAttribute("role")).toBe("button");
        expect(item.getAttribute("aria-label")).toBe("Query Step: read-orders");

        const badges = Array.from(item.querySelectorAll(".builder-node-badge")).map((badge) => ({
            label: badge.textContent,
            tone: badge.getAttribute("data-tone"),
        }));
        expect(badges).toEqual([
            { label: "sql", tone: "neutral" },
            { label: "checkpoint", tone: "success" },
            { label: "2 checks", tone: "success" },
        ]);
    });
});

describe("node disclosure content", () => {
    it("TransformNode_SummarizesInputsSqlContractsAndLinks", () => {
        const rows = t.buildNodeDisclosureRows(
            {
                slug: "join-orders",
                kind: "transform",
                transformSQL:
                    "SELECT orders.id, orders.status FROM orders JOIN accounts ON accounts.id = orders.account_id",
                transformInputContract: {
                    representation: "rows",
                    fields: [
                        { name: "orders.id", type: "string" },
                        { name: "accounts.id", type: "string" },
                    ],
                },
                transformOutputContract: {
                    representation: "rows",
                    fields: [{ name: "id", type: "string", validations: { format: "uuid" } }],
                },
            },
            {
                incomingBindings: [
                    { fromNode: "read-orders", fromPort: "rows", toNode: "join-orders", toPort: "orders" },
                    { fromNode: "read-accounts", fromPort: "rows", toNode: "join-orders", toPort: "accounts" },
                ],
                outgoingBindings: [
                    { fromNode: "join-orders", fromPort: "rows", toNode: "warehouse-upsert", toPort: "payload" },
                ],
            },
        );

        expect(rows).toContainEqual({ label: "Inputs", value: "orders <- read-orders, accounts <- read-accounts" });
        expect(rows).toContainEqual({
            label: "SQL",
            value: "SELECT orders.id, orders.status FROM orders JOIN accounts ON accounts.id = orders.account_id",
        });
        expect(rows).toContainEqual({
            label: "Contracts",
            value: "input rows, 2 fields; output rows, 1 field, 1 validation",
        });
        expect(rows).toContainEqual({ label: "Links", value: "warehouse-upsert" });
    });

    it("CanonicalValues_RenderAsEscapedPopoverHTML", () => {
        const html = t.renderNodeDisclosureContent(
            {
                slug: "unsafe-transform",
                kind: "transform",
                transformSQL: "<script>window.__builderDisclosureInjected = true</script>",
            },
            {},
        );
        const documentFragment = new DOMParser().parseFromString(html, "text/html");

        expect(documentFragment.body.textContent).toContain("<script>");
        expect(documentFragment.querySelector("script")).toBeNull();
        expect(window.__builderDisclosureInjected).toBeUndefined();
    });
});

describe("renderPipelinePanel", () => {
    it("DynamicSearchAndFilterControls_HaveAccessibleNames", async () => {
        const paletteBody = document.createElement("div");

        await t.renderPipelinePanel(paletteBody, { organizationSlug: "acme-hq" });

        expect(paletteBody.querySelector("#builder-pipeline-search")?.getAttribute("aria-label")).toBe(
            "Search pipelines",
        );
        expect(paletteBody.querySelector("#builder-pipeline-draft-filter")?.getAttribute("aria-label")).toBe(
            "Filter pipelines by draft status",
        );
    });
});

describe("bindConflictModalCloseReset", () => {
    it("ModalAddedAfterInit_ResetsStateWhenClosed", () => {
        const modal = document.createElement("dialog");
        modal.id = "builder-conflict-modal";
        modal.innerHTML = `
            <div id="builder-conflict-modal-status">stale status</div>
            <div id="builder-conflict-modal-overview" class="hidden"></div>
            <div id="builder-conflict-resolver"></div>
            <div id="builder-conflict-units-list"><button type="button">stale</button></div>
            <div id="builder-conflict-resolver-inspector">stale</div>
            <span id="builder-conflict-resolver-unresolved-count">4 unresolved</span>
            <span id="builder-conflict-resolver-validation-count">2 invalid</span>
        `;
        document.body.appendChild(modal);

        t.bindConflictModalCloseReset();
        modal.dispatchEvent(new Event("close"));

        expect(document.getElementById("builder-conflict-modal-status")?.textContent).toBe("");
        expect(document.getElementById("builder-conflict-units-list")?.textContent).toBe("");
        expect(document.getElementById("builder-conflict-resolver-inspector")?.textContent).toBe("");
        expect(document.getElementById("builder-conflict-resolver-unresolved-count")?.textContent).toBe("0 unresolved");
        expect(document.getElementById("builder-conflict-resolver-validation-count")?.textContent).toBe("0 invalid");
    });
});

describe("buildStepSpecForExportNode", () => {
    it("PreservesAuthoredSQLWhitespace", () => {
        const sql = "SELECT *\nFROM input\n";

        expect(
            t.buildStepSpecForExportNode({
                slug: "normalize-users",
                kind: "transform",
                step_slug: "normalize-users",
                transform_sql: sql,
            }).transform.sql,
        ).toBe(sql);
        expect(
            t.buildStepSpecForExportNode({
                slug: "fetch-users",
                kind: "query",
                step_slug: "fetch-users",
                data_source_slug: "warehouse",
                operation_type: "sql",
                inline_sql: sql,
            }).query.operation_inline.sql,
        ).toBe(sql);
    });

    it("BuildsStepSpecFromNormalizedNodeShape", () => {
        expect(
            t.buildStepSpecForExportNode({
                slug: "canvas-fetch-users",
                kind: "query",
                step_slug: "fetch-users",
                step_name: "Fetch users",
                data_source_slug: "acme-postgres",
                operation_type: "sql",
                query_slug: "list-active-users",
                operation_inline: {},
                parameters: { limit: 100 },
                query_incremental: { checkpoint_kind: "cursor", cursor: "next_page_token" },
                query_output_contract: {
                    representation: "rows",
                    fields: [{ name: "email", type: "string", required: true }],
                },
            }),
        ).toEqual({
            kind: "query",
            slug: "fetch-users",
            name: "Fetch users",
            query: {
                data_source_ref: "acme-postgres",
                operation_type: "sql",
                operation_ref: "list-active-users",
                parameters: { limit: 100 },
                incremental: { checkpoint_kind: "cursor", cursor: "next_page_token" },
                output_contract: {
                    name: "fetch-users_output",
                    representation: "rows",
                    fields: [{ name: "email", type: "string", required: true }],
                },
            },
        });
    });

    it("BuildsTransformContractsFromNormalizedNodeShape", () => {
        expect(
            t.buildStepSpecForExportNode({
                slug: "normalize-users",
                kind: "transform",
                step_slug: "normalize-users",
                step_name: "Normalize users",
                transform_sql: "SELECT lower(email) AS normalized_email FROM input",
                transform_output_kind: "rows",
                transform_input_contract: {
                    representation: "rows",
                    fields: [{ name: "email", type: "string", required: true }],
                },
                transform_output_contract: {
                    representation: "rows",
                    fields: [{ name: "normalized_email", type: "string", required: true }],
                },
            }),
        ).toEqual({
            kind: "transform",
            slug: "normalize-users",
            name: "Normalize users",
            transform: {
                sql: "SELECT lower(email) AS normalized_email FROM input",
                output_kind: "rows",
                input_contract: {
                    name: "normalize-users_input",
                    representation: "rows",
                    fields: [{ name: "email", type: "string", required: true }],
                },
                output_contract: {
                    name: "normalize-users_output",
                    representation: "rows",
                    fields: [{ name: "normalized_email", type: "string", required: true }],
                },
            },
        });
    });

    it("BuildsSinkInputFromBindingsAndEdges", () => {
        const payload = t.buildBuilderStatePayload(
            [
                {
                    slug: "fetch-users",
                    kind: "query",
                    x: 2,
                    y: 1,
                    stepSlug: "fetch-users",
                    stepName: "Fetch users",
                    dataSourceSlug: "acme-postgres",
                    operationType: "sql",
                    querySlug: "list-active-users",
                },
                {
                    slug: "load-users",
                    kind: "sink",
                    x: 8,
                    y: 1,
                    stepSlug: "load-users",
                    stepName: "Load users",
                    sinkDataSourceSlug: "acme-mysql",
                    sinkOperationType: "sql",
                    sinkQuerySlug: "insert-users",
                },
            ],
            [{ from: "fetch-users", to: "load-users" }],
            [{ fromNode: "fetch-users", fromPort: "rows", toNode: "load-users", toPort: "payload" }],
        );

        expect(payload.pipeline_spec.steps).toContainEqual({
            kind: "sink",
            slug: "load-users",
            name: "Load users",
            sink: {
                idempotency_class: "idempotent_with_key",
                input: "fetch-users",
                data_source_ref: "acme-mysql",
                operation_type: "sql",
                operation_ref: "insert-users",
            },
        });
    });

    it("ClearsInheritedSinkProviderFieldsWhenCanvasValuesAreEmpty", () => {
        const step = t.buildStepSpecForExportNode({
            slug: "load-users",
            kind: "sink",
            step_slug: "load-users",
            sink_data_source_slug: "",
            sink_operation_type: "",
            original_step: {
                kind: "sink",
                slug: "load-users",
                sink: {
                    data_source_ref: "old-source",
                    operation_type: "sql",
                    operation_ref: "old-query",
                },
            },
        });

        expect(step.sink).not.toHaveProperty("data_source_ref");
        expect(step.sink).not.toHaveProperty("operation_type");
    });
});

describe("stripConflictDiffVersionMetadata", () => {
    it("RemovesPipelineVersionFields", () => {
        expect(
            t.stripConflictDiffVersionMetadata({
                slug: "acme-users-sync",
                name: "ACME Users Sync",
                expected_version_id: 3,
                current_version_id: 3,
                draft_based_on_version_id: 3,
                export_format: "json",
            }),
        ).toEqual({
            slug: "acme-users-sync",
            name: "ACME Users Sync",
            export_format: "json",
        });
    });
});

describe("hasPath", () => {
    it("EmptyEdges_ReturnsFalse", () => {
        expect(t.hasPath([], "a", "b")).toBe(false);
    });

    it("DirectEdge_ReturnsTrue", () => {
        const edges = [{ from: "a", to: "b" }];
        expect(t.hasPath(edges, "a", "b")).toBe(true);
    });

    it("IndirectPath_ReturnsTrue", () => {
        const edges = [
            { from: "a", to: "b" },
            { from: "b", to: "c" },
        ];
        expect(t.hasPath(edges, "a", "c")).toBe(true);
    });

    it("NoPath_ReturnsFalse", () => {
        const edges = [
            { from: "a", to: "b" },
            { from: "c", to: "d" },
        ];
        expect(t.hasPath(edges, "a", "d")).toBe(false);
    });

    it("StartEqualsTarget_ReturnsTrue", () => {
        expect(t.hasPath([], "x", "x")).toBe(true);
    });

    it("CycleDoesNotHang_ReturnsFalse", () => {
        const edges = [
            { from: "a", to: "b" },
            { from: "b", to: "a" },
        ];
        expect(t.hasPath(edges, "a", "c")).toBe(false);
    });
});

describe("validateEdgeConnection", () => {
    const nodes = [
        { slug: "source-a", kind: "source" },
        { slug: "transform-a", kind: "transform" },
        { slug: "sink-a", kind: "sink" },
    ];

    it("ValidConnection_ReturnsEmptyString", () => {
        expect(t.validateEdgeConnection(nodes, [], "source-a", "transform-a")).toBe("");
    });

    it("DuplicateConnection_ReturnsMessage", () => {
        const edges = [{ from: "source-a", to: "transform-a" }];
        expect(t.validateEdgeConnection(nodes, edges, "source-a", "transform-a")).toBe("Connection already exists.");
    });

    it("CycleConnection_ReturnsMessage", () => {
        const edges = [{ from: "transform-a", to: "source-a" }];
        expect(t.validateEdgeConnection(nodes, edges, "source-a", "transform-a")).toBe(
            "Cannot connect nodes: this would create a cycle.",
        );
    });

    it("SelfConnection_ReturnsMessage", () => {
        expect(t.validateEdgeConnection(nodes, [], "transform-a", "transform-a")).toBe(
            "Cannot connect a node to itself.",
        );
    });

    it("SinkOutgoing_ReturnsMessage", () => {
        expect(t.validateEdgeConnection(nodes, [], "sink-a", "transform-a")).toBe(
            "Sink nodes cannot have outgoing dependencies.",
        );
    });

    it("SourceIncoming_ReturnsMessage", () => {
        expect(t.validateEdgeConnection(nodes, [], "transform-a", "source-a")).toBe("");
    });

    it("SecondIncomingIntoTransform_ReturnsEmptyString", () => {
        const edges = [{ from: "source-a", to: "transform-a" }];
        expect(
            t.validateEdgeConnection(
                [...nodes, { slug: "source-b", kind: "source" }],
                edges,
                "source-b",
                "transform-a",
            ),
        ).toBe("");
    });

    it("SecondIncomingIntoSink_ReturnsMessage", () => {
        const edges = [{ from: "transform-a", to: "sink-a" }];
        expect(
            t.validateEdgeConnection(
                [...nodes, { slug: "transform-b", kind: "transform" }],
                edges,
                "transform-b",
                "sink-a",
            ),
        ).toBe("Nodes can only have one incoming dependency.");
    });

    it("ProducerFanOutToMultipleSinks_ReturnsEmptyString", () => {
        const fanOutNodes = [...nodes, { slug: "sink-b", kind: "sink" }];
        const edges = [{ from: "transform-a", to: "sink-a" }];

        expect(t.validateEdgeConnection(fanOutNodes, edges, "transform-a", "sink-b")).toBe("");
    });
});

describe("validateEdgeTopology", () => {
    it("MultipleIncomingIntoTransform_ReportsNoIssue", () => {
        const nodes = [
            { slug: "source-a", kind: "source" },
            { slug: "source-b", kind: "source" },
            { slug: "transform-a", kind: "transform" },
        ];
        const edges = [
            { from: "source-a", to: "transform-a" },
            { from: "source-b", to: "transform-a" },
        ];

        expect(t.validateEdgeTopology(nodes, edges)).toEqual([]);
    });

    it("ProducerFanOutToMultipleSinks_ReportsNoIssue", () => {
        const nodes = [
            { slug: "transform-a", kind: "transform" },
            { slug: "sink-a", kind: "sink" },
            { slug: "sink-b", kind: "sink" },
        ];
        const edges = [
            { from: "transform-a", to: "sink-a" },
            { from: "transform-a", to: "sink-b" },
        ];

        expect(t.validateEdgeTopology(nodes, edges)).toEqual([]);
    });
});

describe("validateBindings", () => {
    it("SinkPayload_RejectsMultipleBindings", () => {
        const nodes = [
            { slug: "transform-a", kind: "transform" },
            { slug: "transform-b", kind: "transform" },
            { slug: "sink-a", kind: "sink" },
        ];
        const edges = [
            { from: "transform-a", to: "sink-a" },
            { from: "transform-b", to: "sink-a" },
        ];

        expect(
            issueMessages(
                t.validateBindings(nodes, edges, [
                    { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
                    { fromNode: "transform-b", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
                ]),
            ),
        ).toContain("sink-a: multiple bindings for input port payload are not allowed");
    });

    it("TransformRows_AllowsMultipleNamedBindings", () => {
        const nodes = [
            { slug: "source-a", kind: "query" },
            { slug: "source-b", kind: "query" },
            { slug: "transform-a", kind: "transform" },
        ];
        const edges = [
            { from: "source-a", to: "transform-a" },
            { from: "source-b", to: "transform-a" },
        ];

        expect(
            t.validateBindings(nodes, edges, [
                { fromNode: "source-a", fromPort: "rows", toNode: "transform-a", toPort: "orders" },
                { fromNode: "source-b", fromPort: "rows", toNode: "transform-a", toPort: "accounts" },
            ]),
        ).toEqual([]);
    });
});

describe("bindingSourceNodeValue", () => {
    it("TransformIncoming_ReturnsCanonicalSourceNode", () => {
        expect(t.bindingSourceNodeValue("transform", ["source-a"], null)).toBe("source-a");
    });

    it("TransformWithoutIncoming_ReturnsEmptyString", () => {
        expect(t.bindingSourceNodeValue("transform", [], { fromNode: "stale-source" })).toBe("");
    });

    it("Sink_UsesBindingSourceNode", () => {
        expect(t.bindingSourceNodeValue("sink", ["transform-a"], { fromNode: "transform-b" })).toBe("transform-a");
    });
});

describe("parseEdgeData + writeEdgesData", () => {
    const makeContainer = () => document.createElement("div");

    it("ParseEdgeData_NonElement_ReturnsEmptyArray", () => {
        expect(t.parseEdgeData(null)).toEqual([]);
        expect(t.parseEdgeData("string")).toEqual([]);
    });

    it("ParseEdgeData_EmptyContainer_ReturnsEmptyArray", () => {
        expect(t.parseEdgeData(makeContainer())).toEqual([]);
    });

    it("ParseEdgeData_ReadsAttributes", () => {
        const container = makeContainer();
        const child = document.createElement("div");
        child.setAttribute("data-edge-from", "node-a");
        child.setAttribute("data-edge-to", "node-b");
        container.appendChild(child);
        expect(t.parseEdgeData(container)).toEqual([{ from: "node-a", to: "node-b" }]);
    });

    it("ParseEdgeData_FiltersIncompleteEdges", () => {
        const container = makeContainer();
        const bad = document.createElement("div");
        bad.setAttribute("data-edge-from", "node-a");
        // missing data-edge-to
        container.appendChild(bad);
        expect(t.parseEdgeData(container)).toEqual([]);
    });

    it("WriteEdgesData_RoundTrip", () => {
        const container = makeContainer();
        const edges = [
            { from: "a", to: "b" },
            { from: "b", to: "c" },
        ];
        t.writeEdgesData(container, edges);
        expect(t.parseEdgeData(container)).toEqual(edges);
    });

    it("WriteEdgesData_ClearsExistingChildren", () => {
        const container = makeContainer();
        t.writeEdgesData(container, [{ from: "x", to: "y" }]);
        t.writeEdgesData(container, [{ from: "a", to: "b" }]);
        expect(t.parseEdgeData(container)).toEqual([{ from: "a", to: "b" }]);
    });
});

describe("normalizeNodeForExport", () => {
    it("QueryNode_IncludesDataSourceAndQuerySlugs", () => {
        const node = { slug: "n1", kind: "query", x: 1, y: 2, dataSourceSlug: "ds", querySlug: "q" };
        expect(t.normalizeNodeForExport(node)).toEqual({
            slug: "n1",
            kind: "query",
            data_source_slug: "ds",
            operation_type: "sql",
            query_slug: "q",
            operation_inline: {},
            use_inline_sql: false,
            use_inline_operation: false,
            inline_sql: "",
            step_slug: "",
            step_name: "",
            parameters: {},
        });
    });

    it("QueryStep_IncludesQueryReferenceAndStepFields", () => {
        const node = {
            slug: "fetch-users",
            kind: "query",
            dataSourceSlug: "acme-postgres",
            querySlug: "list-active-users",
            stepSlug: "fetch-users",
            stepName: "Fetch users from Postgres",
            parameters: { is_active: true, limit: 100 },
        };
        expect(t.normalizeNodeForExport(node)).toEqual({
            slug: "fetch-users",
            kind: "query",
            data_source_slug: "acme-postgres",
            operation_type: "sql",
            query_slug: "list-active-users",
            operation_inline: {},
            use_inline_sql: false,
            use_inline_operation: false,
            inline_sql: "",
            step_slug: "fetch-users",
            step_name: "Fetch users from Postgres",
            parameters: { is_active: true, limit: 100 },
        });
    });

    it("TransformNode_IncludesStepFields", () => {
        const node = {
            slug: "n2",
            kind: "transform",
            stepSlug: "step-1",
            stepName: "My Step",
            parameters: { k: "v" },
        };
        expect(t.normalizeNodeForExport(node)).toEqual({
            slug: "n2",
            kind: "transform",
            step_slug: "step-1",
            step_name: "My Step",
            parameters: { k: "v" },
        });
    });

    it("TransformNode_MissingParameters_DefaultsToEmptyObject", () => {
        const node = { slug: "n3", kind: "transform", x: 0, y: 0, stepSlug: "step-1", stepName: "" };
        const result = t.normalizeNodeForExport(node);
        expect(result.parameters).toEqual({});
    });

    it("TransformNode_IncludesTransformOutputKindWhenPresent", () => {
        const node = {
            slug: "n3",
            kind: "transform",
            stepSlug: "step-1",
            stepName: "My Step",
            parameters: { k: "v" },
            transformSQL: "SELECT * FROM input_rows",
            transformOutputKind: "csv",
        };

        expect(t.normalizeNodeForExport(node)).toEqual({
            slug: "n3",
            kind: "transform",
            step_slug: "step-1",
            step_name: "My Step",
            parameters: { k: "v" },
            transform_sql: "SELECT * FROM input_rows",
            transform_output_kind: "csv",
        });
    });

    it("SinkNode_IncludesSinkInputContractWhenPresent", () => {
        const node = {
            slug: "sink-1",
            kind: "sink",
            stepSlug: "load-users",
            stepName: "Load users",
            sinkBackend: "http",
            sinkDataSourceSlug: "acme-http",
            sinkOperationType: "http",
            sinkOperationInline: {},
            sinkIdempotencyClass: "idempotent_with_key",
            sinkInputContract: {
                name: "users_rows",
                fields: [{ name: "id", type: "integer", required: true }],
            },
            sinkUseInlineOperation: false,
            sinkQuerySlug: "post-users",
        };

        expect(t.normalizeNodeForExport(node)).toEqual({
            slug: "sink-1",
            kind: "sink",
            step_slug: "load-users",
            step_name: "Load users",
            sink_backend: "http",
            sink_data_source_slug: "acme-http",
            sink_operation_type: "http",
            sink_operation_inline: {},
            sink_idempotency_class: "idempotent_with_key",
            sink_input_contract: {
                name: "users_rows",
                fields: [{ name: "id", type: "integer", required: true }],
            },
            sink_use_inline_operation: false,
            sink_query_slug: "post-users",
        });
    });

    it("SinkNode_DefaultsInvalidIdempotencyClass", () => {
        const node = {
            slug: "sink-1",
            kind: "sink",
            stepSlug: "load-users",
            stepName: "Load users",
            sinkIdempotencyClass: "unsafe-mode",
        };

        expect(t.normalizeNodeForExport(node)).toEqual(
            expect.objectContaining({
                sink_idempotency_class: "idempotent_with_key",
            }),
        );
    });

    it("SinkNode_PreservesLedgerIdempotencyClass", () => {
        const node = {
            slug: "sink-1",
            kind: "sink",
            stepSlug: "load-users",
            stepName: "Load users",
            sinkIdempotencyClass: "deduplicated_by_ledger",
        };

        expect(t.normalizeNodeForExport(node)).toEqual(
            expect.objectContaining({
                sink_idempotency_class: "deduplicated_by_ledger",
            }),
        );
    });

    it("UnknownKind_OnlyBaseFields", () => {
        const node = { slug: "n4", kind: "unknown", x: 3, y: 4 };
        expect(t.normalizeNodeForExport(node)).toEqual({ slug: "n4", kind: "unknown" });
    });
});

describe("validateNodes", () => {
    it("ValidSourceNode_NoIssues", () => {
        const nodes = [{ slug: "node-a", kind: "source", dataSourceSlug: "ds-1", querySlug: "q-1" }];
        expect(t.validateNodes(nodes)).toEqual([]);
    });

    it("ValidTransformNode_NoIssues", () => {
        const nodes = [{ slug: "node-b", kind: "transform", stepSlug: "step-1", transformSQL: "SELECT * FROM input" }];
        expect(t.validateNodes(nodes)).toEqual([]);
    });

    it("TransformMissingSQL_ReportsIssue", () => {
        const nodes = [{ slug: "node-b", kind: "transform", stepSlug: "step-1" }];
        const issues = t.validateNodes(nodes);
        expect(issueMessages(issues)).toContain("node-b: missing transform SQL");
    });

    it("ValidSinkNode_NoIssues", () => {
        const nodes = [
            {
                slug: "sink-1",
                kind: "sink",
                sinkDataSourceSlug: "warehouse",
                sinkOperationType: "sql",
                sinkQuerySlug: "insert-users",
            },
        ];
        expect(t.validateNodes(nodes)).toEqual([]);
    });

    it("SinkMissingDelivery_ReportsIssues", () => {
        const nodes = [{ slug: "sink-1", kind: "sink" }];
        const issues = t.validateNodes(nodes);

        expect(issueMessages(issues)).toEqual([
            "sink-1: missing sink data source",
            "sink-1: missing sink operation type",
            "sink-1: missing saved query or inline operation",
        ]);
    });

    it("InvalidSlug_ReportsIssue", () => {
        const nodes = [{ slug: "", kind: "source", dataSourceSlug: "ds", querySlug: "q" }];
        const issues = t.validateNodes(nodes);
        expect(issues.length).toBeGreaterThan(0);
        expect(issueMessages(issues)[0]).toContain("invalid slug");
    });

    it("QueryMissingDataSourceSlug_ReportsIssue", () => {
        const nodes = [{ slug: "node-a", kind: "query", dataSourceSlug: "", querySlug: "q-1" }];
        const issues = t.validateNodes(nodes);
        expect(issueMessages(issues)).toContain("node-a: missing data source");
    });

    it("TransformMissingStepSlug_ReportsIssue", () => {
        const nodes = [{ slug: "node-a", kind: "transform", stepSlug: "" }];
        const issues = t.validateNodes(nodes);
        expect(issueMessages(issues)).toContain("node-a: missing step slug");
    });

    it("MultipleErrors_AllReported", () => {
        const nodes = [{ slug: "node-a", kind: "query", dataSourceSlug: "", querySlug: "" }];
        const issues = t.validateNodes(nodes);
        expect(issues.length).toBe(3);
    });
});

describe("payloadDiagnosticForBinding", () => {
    it("SinkRows_FromQueryRows_IsCompatible", () => {
        const sink = { kind: "sink", sinkInputContract: { representation: "rows" } };
        const binding = { fromNode: "query-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" };
        const nodeBySlug = new Map([["query-a", { slug: "query-a", kind: "query", dataSourceSlug: "postgres-a" }]]);
        const dataSourceMetadata = new Map([["postgres-a", { type: "postgres" }]]);

        expect(t.payloadDiagnosticForBinding(sink, binding, nodeBySlug, dataSourceMetadata)).toEqual({
            tone: "success",
            message: "Payload representation is compatible: upstream rows, sink expects rows.",
        });
    });

    it("SinkRows_FromHTTPQuery_ReportsMismatch", () => {
        const sink = { kind: "sink", sinkInputContract: { representation: "rows" } };
        const binding = { fromNode: "query-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" };
        const nodeBySlug = new Map([["query-a", { slug: "query-a", kind: "query", dataSourceSlug: "http-a" }]]);
        const dataSourceMetadata = new Map([["http-a", { type: "http" }]]);

        expect(t.payloadDiagnosticForBinding(sink, binding, nodeBySlug, dataSourceMetadata)).toEqual({
            tone: "error",
            message: "Payload representation mismatch: upstream JSON, sink expects rows.",
        });
    });

    it("SinkJSON_FromMongoQuery_IsCompatible", () => {
        const sink = { kind: "sink", sinkInputContract: { representation: "json" } };
        const binding = { fromNode: "query-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" };
        const nodeBySlug = new Map([["query-a", { slug: "query-a", kind: "query", dataSourceSlug: "mongo-a" }]]);
        const dataSourceMetadata = new Map([["mongo-a", { type: "mongodb" }]]);

        expect(t.payloadDiagnosticForBinding(sink, binding, nodeBySlug, dataSourceMetadata)).toEqual({
            tone: "success",
            message: "Payload representation is compatible: upstream JSON, sink expects JSON.",
        });
    });

    it("SinkRows_FromObjectAPISchemaFamily_IsCompatible", () => {
        const sink = { kind: "sink", sinkInputContract: { representation: "rows" } };
        const binding = { fromNode: "query-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" };
        const nodeBySlug = new Map([["query-a", { slug: "query-a", kind: "query", dataSourceSlug: "hubspot-a" }]]);
        const dataSourceMetadata = new Map([["hubspot-a", { type: "hubspot", schemaFamily: "object_api" }]]);

        expect(t.payloadDiagnosticForBinding(sink, binding, nodeBySlug, dataSourceMetadata)).toEqual({
            tone: "success",
            message: "Payload representation is compatible: upstream rows, sink expects rows.",
        });
    });

    it("SinkFile_FromFTPFileQuery_IsCompatible", () => {
        const sink = { kind: "sink", sinkInputContract: { representation: "file" } };
        const binding = { fromNode: "query-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" };
        const nodeBySlug = new Map([
            [
                "query-a",
                {
                    slug: "query-a",
                    kind: "query",
                    dataSourceSlug: "ftp-a",
                    operationInline: { format: "file" },
                },
            ],
        ]);
        const dataSourceMetadata = new Map([["ftp-a", { type: "ftp" }]]);

        expect(t.payloadDiagnosticForBinding(sink, binding, nodeBySlug, dataSourceMetadata)).toEqual({
            tone: "success",
            message: "Payload representation is compatible: upstream file, sink expects file.",
        });
    });

    it("SinkFile_FromDeclaredFileQuery_IsCompatible", () => {
        const sink = { kind: "sink", sinkInputContract: { representation: "file" } };
        const binding = { fromNode: "query-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" };
        const nodeBySlug = new Map([
            [
                "query-a",
                {
                    slug: "query-a",
                    kind: "query",
                    dataSourceSlug: "ftp-a",
                    queryOutputContract: { representation: "file" },
                },
            ],
        ]);
        const dataSourceMetadata = new Map([["ftp-a", { type: "ftp" }]]);

        expect(t.payloadDiagnosticForBinding(sink, binding, nodeBySlug, dataSourceMetadata)).toEqual({
            tone: "success",
            message: "Payload representation is compatible: upstream file, sink expects file.",
        });
    });

    it("SinkRows_FromTransformCSV_ReportsMismatch", () => {
        const sink = { kind: "sink", sinkInputContract: { representation: "rows" } };
        const binding = { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" };
        const nodeBySlug = new Map([
            ["transform-a", { slug: "transform-a", kind: "transform", transformOutputKind: "csv" }],
        ]);

        expect(t.payloadDiagnosticForBinding(sink, binding, nodeBySlug, new Map())).toEqual({
            tone: "error",
            message: "Payload representation mismatch: upstream CSV, sink expects rows.",
        });
    });

    it("SinkJSON_FromUnknownTransform_IsNeutral", () => {
        const sink = { kind: "sink", sinkInputContract: { representation: "json" } };
        const binding = { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" };
        const nodeBySlug = new Map([["transform-a", { slug: "transform-a", kind: "transform" }]]);

        expect(t.payloadDiagnosticForBinding(sink, binding, nodeBySlug, new Map())).toEqual({
            tone: "neutral",
            message: "Sink expects JSON payload. transform-a representation is unavailable.",
        });
    });

    it("SinkRows_WithoutBinding_ShowsExpectedRepresentation", () => {
        const sink = { kind: "sink", sinkInputContract: { representation: "rows" } };

        expect(t.payloadDiagnosticForBinding(sink, null, new Map(), new Map())).toEqual({
            tone: "neutral",
            message: "Sink expects rows payload once an upstream node is bound.",
        });
    });
});

describe("validateBindings", () => {
    const nodes = [
        { slug: "query-a", kind: "query" },
        { slug: "transform-a", kind: "transform" },
        { slug: "sink-a", kind: "sink" },
    ];

    it("ValidBindings_NoIssues", () => {
        const edges = [
            { from: "query-a", to: "transform-a" },
            { from: "transform-a", to: "sink-a" },
        ];
        const bindings = [
            { fromNode: "query-a", fromPort: "rows", toNode: "transform-a", toPort: "rows" },
            { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
        ];

        expect(t.validateBindings(nodes, edges, bindings)).toEqual([]);
    });

    it("BindingWithoutDirectEdge_ReportsIssue", () => {
        const issues = t.validateBindings(
            nodes,
            [],
            [{ fromNode: "query-a", fromPort: "rows", toNode: "transform-a", toPort: "rows" }],
        );

        expect(issueMessages(issues)).toContain("transform-a: binding source query-a must have a direct incoming edge");
    });

    it("SinkBindingWithoutDirectEdge_ReportsIssue", () => {
        const issues = t.validateBindings(
            nodes,
            [],
            [{ fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" }],
        );

        expect(issueMessages(issues)).toContain("sink-a: binding source transform-a must have a direct incoming edge");
    });

    it("InvalidSourcePort_ReportsIssue", () => {
        const edges = [{ from: "query-a", to: "transform-a" }];
        const issues = t.validateBindings(nodes, edges, [
            { fromNode: "query-a", fromPort: "payload", toNode: "transform-a", toPort: "rows" },
        ]);

        expect(issueMessages(issues)).toContain("query-a: output port payload is not available");
    });

    it("WhitespaceTransformInputName_ReportsIssue", () => {
        const edges = [{ from: "query-a", to: "transform-a" }];
        const issues = t.validateBindings(nodes, edges, [
            { fromNode: "query-a", fromPort: "rows", toNode: "transform-a", toPort: "   " },
        ]);

        expect(issueMessages(issues)).toContain("transform-a: transform input name is required");
    });

    it("DuplicateTransformInputPort_ReportsIssue", () => {
        const duplicateNodes = [...nodes, { slug: "query-b", kind: "query" }];
        const edges = [
            { from: "query-a", to: "transform-a" },
            { from: "query-b", to: "transform-a" },
        ];
        const issues = t.validateBindings(duplicateNodes, edges, [
            { fromNode: "query-a", fromPort: "rows", toNode: "transform-a", toPort: "orders" },
            { fromNode: "query-b", fromPort: "rows", toNode: "transform-a", toPort: "orders" },
        ]);

        expect(issueMessages(issues)).toContain("transform-a: multiple bindings for input port orders are not allowed");
    });

    it("ExactDuplicateBinding_IsCollapsedBeforeValidation", () => {
        const edges = [{ from: "transform-a", to: "sink-a" }];
        const issues = t.validateBindings(nodes, edges, [
            { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
            { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
        ]);

        expect(issues).toEqual([]);
    });
});

describe("validateBindingCompleteness", () => {
    const source = { slug: "source-a", kind: "source" };
    const transform = { slug: "transform-a", kind: "transform" };
    const sink = { slug: "sink-a", kind: "sink" };

    const edgesFull = [
        { from: "source-a", to: "transform-a" },
        { from: "transform-a", to: "sink-a" },
    ];
    const bindingsFull = [
        { fromNode: "source-a", fromPort: "rows", toNode: "transform-a", toPort: "rows" },
        { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
    ];

    it("FullyWiredPipeline_NoIssues", () => {
        // Step node has rows binding; sink has payload binding and incoming step edge.
        expect(t.validateBindingCompleteness([source, transform, sink], edgesFull, bindingsFull)).toEqual([]);
    });

    it("SourceNodeOnly_NoIssues", () => {
        // Source nodes have no required bindings and no required incoming edges.
        expect(t.validateBindingCompleteness([source], [], [])).toEqual([]);
    });

    it("TransformMissingInputBinding_ReportsIssue", () => {
        // Transform node exists and has incoming edge but no binding wired.
        const edges = [{ from: "source-a", to: "transform-a" }];
        const issues = t.validateBindingCompleteness([source, transform], edges, []);

        expect(issueMessages(issues)).toContain("transform-a: missing transform input binding");
    });

    it("TransformWithRowsBinding_NoIssue", () => {
        const edges = [{ from: "source-a", to: "transform-a" }];
        const bindings = [{ fromNode: "source-a", fromPort: "rows", toNode: "transform-a", toPort: "rows" }];

        expect(t.validateBindingCompleteness([source, transform], edges, bindings)).toEqual([]);
    });

    it("TransformWithMissingMultiSourceBinding_ReportsMissingSource", () => {
        const queryA = { slug: "query-a", kind: "query" };
        const queryB = { slug: "query-b", kind: "query" };
        const edges = [
            { from: "query-a", to: "transform-a" },
            { from: "query-b", to: "transform-a" },
        ];
        const bindings = [{ fromNode: "query-a", fromPort: "rows", toNode: "transform-a", toPort: "query_a" }];
        const issues = t.validateBindingCompleteness([queryA, queryB, transform], edges, bindings);

        expect(issueMessages(issues)).toContain("transform-a: missing transform input binding for query-b");
    });

    it("SinkMissingPayloadBinding_ReportsIssue", () => {
        // Sink has the incoming step edge but payload binding is absent.
        const edges = [
            { from: "source-a", to: "transform-a" },
            { from: "transform-a", to: "sink-a" },
        ];
        const bindings = [{ fromNode: "source-a", fromPort: "rows", toNode: "transform-a", toPort: "rows" }];
        const issues = t.validateBindingCompleteness([source, transform, sink], edges, bindings);

        expect(issueMessages(issues)).toContain("sink-a: missing sink binding (payload)");
    });

    it("SinkMissingIncomingStepEdge_ReportsIssue", () => {
        // Sink has a payload binding but no edge from any step node.
        const edges = [{ from: "source-a", to: "transform-a" }];
        const bindings = [
            { fromNode: "source-a", fromPort: "rows", toNode: "transform-a", toPort: "rows" },
            { fromNode: "transform-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" },
        ];
        const issues = t.validateBindingCompleteness([source, transform, sink], edges, bindings);

        expect(issueMessages(issues)).toContain("sink-a: sink has no incoming step edge");
    });

    it("SinkWithSourceEdgeOnly_ReportsNoStepEdgeIssue", () => {
        // Edge from source directly to sink is not a step edge.
        const edges = [{ from: "source-a", to: "sink-a" }];
        const bindings = [{ fromNode: "source-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" }];
        const issues = t.validateBindingCompleteness([source, sink], edges, bindings);

        expect(issueMessages(issues)).toContain("sink-a: sink has no incoming step edge");
    });

    it("SinkFullyWiredDirectly_NoIssues", () => {
        // Pipeline with no transform: source -> sink, wired with payload binding.
        // Sink still requires a step edge so this combination should report one issue.
        const edges = [{ from: "source-a", to: "sink-a" }];
        const bindings = [{ fromNode: "source-a", fromPort: "rows", toNode: "sink-a", toPort: "payload" }];
        const issues = t.validateBindingCompleteness([source, sink], edges, bindings);
        const messages = issueMessages(issues);

        // No payload binding issue, but no step edge issue either if source->sink is acceptable
        // per the architectural contract (source is not a step kind). Step edge is required.
        expect(messages).toContain("sink-a: sink has no incoming step edge");
        expect(messages).not.toContain("sink-a: missing sink binding (payload)");
    });

    it("MultipleIssues_AllReported", () => {
        // Brand-new incomplete pipeline: transform with no binding, sink with no binding and no edge.
        const issues = t.validateBindingCompleteness([transform, sink], [], []);
        const messages = issueMessages(issues);

        expect(messages).toContain("transform-a: missing transform input binding");
        expect(messages).toContain("sink-a: missing sink binding (payload)");
        expect(messages).toContain("sink-a: sink has no incoming step edge");
    });
});

describe("connection binding helpers", () => {
    const source = { slug: "read-orders", kind: "query" };
    const numericSource = { slug: "2026-orders", kind: "query" };
    const transform = { slug: "join-orders", kind: "transform" };
    const sink = { slug: "warehouse-upsert", kind: "sink" };

    it("TransformInputNameFromSourceSlug_ProducesSQLRelationName", () => {
        expect(t.transformInputNameFromSourceSlug("read-orders")).toBe("input_read_orders");
        expect(t.transformInputNameFromSourceSlug("fetch")).toBe("input_fetch");
        expect(t.transformInputNameFromSourceSlug("select")).toBe("input_select");
        expect(t.transformInputNameFromSourceSlug("2026-orders")).toBe("input_2026_orders");
        expect(t.transformInputNameFromSourceSlug("")).toBe("input_rows");
    });

    it("DefaultTransformBinding_UsesUniqueSourceDerivedInput", () => {
        const binding = t.defaultBindingForConnection(source, transform, [
            { fromNode: "read-accounts", fromPort: "rows", toNode: "join-orders", toPort: "input_read_orders" },
        ]);

        expect(binding).toEqual({
            fromNode: "read-orders",
            fromPort: "rows",
            toNode: "join-orders",
            toPort: "input_read_orders_2",
        });
    });

    it("DefaultTransformBinding_UsesDeclaredInputName", () => {
        const binding = t.defaultBindingForConnection(
            source,
            { ...transform, transformInputs: { accounts: "read-accounts", orders: "read-orders" } },
            [],
        );

        expect(binding).toEqual({
            fromNode: "read-orders",
            fromPort: "rows",
            toNode: "join-orders",
            toPort: "orders",
        });
    });

    it("DefaultTransformBinding_PrefixesNumericSourceSlug", () => {
        expect(t.defaultBindingForConnection(numericSource, transform, [])).toEqual({
            fromNode: "2026-orders",
            fromPort: "rows",
            toNode: "join-orders",
            toPort: "input_2026_orders",
        });
    });

    it("DefaultSinkBinding_UsesPayloadPort", () => {
        expect(t.defaultBindingForConnection(transform, sink, [])).toEqual({
            fromNode: "join-orders",
            fromPort: "rows",
            toNode: "warehouse-upsert",
            toPort: "payload",
        });
    });

    it("RemoveBindingsForConnections_DropsBindingsForRemovedEdges", () => {
        const bindings = [
            { fromNode: "read-orders", fromPort: "rows", toNode: "join-orders", toPort: "orders" },
            { fromNode: "read-accounts", fromPort: "rows", toNode: "join-orders", toPort: "accounts" },
            { fromNode: "join-orders", fromPort: "rows", toNode: "warehouse-upsert", toPort: "payload" },
        ];

        expect(t.removeBindingsForConnections(bindings, [{ from: "read-orders", to: "join-orders" }])).toEqual([
            { fromNode: "read-accounts", fromPort: "rows", toNode: "join-orders", toPort: "accounts" },
            { fromNode: "join-orders", fromPort: "rows", toNode: "warehouse-upsert", toPort: "payload" },
        ]);
    });
});

describe("safeOperationObject", () => {
    it("PlainObject_ReturnsSameObject", () => {
        const obj = { path: "/data/file.csv", has_header: true };
        expect(t.safeOperationObject(obj)).toEqual(obj);
    });

    it("NullOrUndefined_ReturnsEmptyObject", () => {
        expect(t.safeOperationObject(null)).toEqual({});
        expect(t.safeOperationObject(undefined)).toEqual({});
    });

    it("Array_ReturnsEmptyObject", () => {
        expect(t.safeOperationObject([1, 2, 3])).toEqual({});
    });

    it("String_ReturnsEmptyObject", () => {
        expect(t.safeOperationObject("not an object")).toEqual({});
    });
});

describe("guided inline operation key preservation", () => {
    it("MergePreservesUnknownKeys", () => {
        // Simulates the Object.assign merge in handleGuidedInput:
        // existing keys not in the guided UI must survive when the user edits a field.
        const existing = t.safeOperationObject({ path: "/old.csv", has_header: true, custom_extra: "keep-me" });
        const guided = { path: "/new.csv", has_header: false };
        const merged = Object.assign({}, existing, guided);

        expect(merged.path).toBe("/new.csv");
        expect(merged.has_header).toBe(false);
        expect(merged.custom_extra).toBe("keep-me");
    });

    it("GuidedValuesOverrideExisting", () => {
        const existing = t.safeOperationObject({ path: "/old.csv", branch: "main", message: "old message" });
        const guided = { path: "/new.csv", branch: "develop" };
        const merged = Object.assign({}, existing, guided);

        expect(merged.path).toBe("/new.csv");
        expect(merged.branch).toBe("develop");
        expect(merged.message).toBe("old message");
    });

    it("EmptyExisting_MergeEqualsGuided", () => {
        const existing = t.safeOperationObject(null);
        const guided = { path: "/file.csv", has_header: true };
        const merged = Object.assign({}, existing, guided);

        expect(merged).toEqual({ path: "/file.csv", has_header: true });
    });
});

describe("conflict resolution session", () => {
    const makeConflictState = ({
        pipelineSlug = "acme-users-sync",
        pipelineName = "ACME Users Sync",
        querySlug = "fetch-users",
        queryName = "Fetch users canonical",
        parameters = { limit: 100 },
        includeEdge = true,
        includeBinding = true,
    } = {}) => ({
        pipeline_spec: {
            metadata: {
                slug: pipelineSlug,
                name: pipelineName,
            },
            steps: [
                {
                    kind: "query",
                    slug: querySlug,
                    name: queryName,
                    query: {
                        data_source_ref: "acme-postgres",
                        operation_type: "sql",
                        operation_ref: "list-active-users",
                        parameters,
                    },
                },
                {
                    kind: "sink",
                    slug: "load-users",
                    name: "Load users",
                    sink: includeEdge
                        ? {
                              input: querySlug,
                              data_source_ref: "analytics-warehouse",
                              operation_type: "sql",
                              operation_ref: "insert-users",
                          }
                        : {
                              data_source_ref: "analytics-warehouse",
                              operation_type: "sql",
                              operation_ref: "insert-users",
                          },
                },
            ],
        },
        graph: {
            nodes: [
                { step_slug: querySlug, x: 2, y: 1 },
                { step_slug: "load-users", x: 8, y: 1 },
            ],
            edges: includeEdge ? [{ from_step: querySlug, to_step: "load-users" }] : [],
            bindings: includeBinding
                ? [{ from_step: querySlug, from_port: "rows", to_step: "load-users", to_port: "payload" }]
                : [],
        },
        context: {
            expected_version_id: 11,
            current_version_id: 12,
            draft_based_on_version_id: 11,
            source_format: "yaml",
            export_format: "yaml",
        },
    });

    it("GroupsChangedNodeFieldsIntoResolvableUnits", () => {
        expect(typeof t.buildConflictResolutionSession).toBe("function");

        const session = t.buildConflictResolutionSession(
            makeConflictState(),
            makeConflictState({
                queryName: "Fetch users draft",
                parameters: { limit: 250, active_only: true },
            }),
        );

        expect(session.summary.canApply).toBe(false);
        expect(session.summary.unresolvedCount).toBeGreaterThan(0);

        const queryUnit = session.units.find((unit) => unit.key === "node:fetch-users");
        expect(queryUnit).toBeTruthy();
        expect(queryUnit.kind).toBe("node");
        expect(queryUnit.fields.map((field) => field.key)).toEqual(
            expect.arrayContaining(["step.name", "step.query.parameters"]),
        );
        expect(queryUnit.fields.map((field) => field.key)).not.toContain("context.current_version_id");
    });

    it("TreatsSlugReplacementAsSeparateRemoveAndAddUnits", () => {
        expect(typeof t.buildConflictResolutionSession).toBe("function");

        const session = t.buildConflictResolutionSession(
            makeConflictState({ querySlug: "fetch-users", queryName: "Fetch users canonical" }),
            makeConflictState({ querySlug: "fetch-customers", queryName: "Fetch customers draft" }),
        );

        expect(session.units.map((unit) => [unit.key, unit.changeKind])).toEqual(
            expect.arrayContaining([
                ["node:fetch-users", "remove"],
                ["node:fetch-customers", "add"],
            ]),
        );
    });

    it("CreatesEdgeAndBindingUnitsForGraphOnlyDifferences", () => {
        expect(typeof t.buildConflictResolutionSession).toBe("function");

        const session = t.buildConflictResolutionSession(
            makeConflictState({ includeEdge: false, includeBinding: false }),
            makeConflictState({ includeEdge: true, includeBinding: true }),
        );

        expect(session.units.map((unit) => unit.key)).toEqual(
            expect.arrayContaining(["edge:fetch-users->load-users", "binding:fetch-users:rows->load-users:payload"]),
        );
    });

    it("SurfacesNamedTransformInputConflicts", () => {
        expect(typeof t.buildConflictResolutionSession).toBe("function");

        const canonicalState = makeConflictState();
        canonicalState.pipeline_spec.steps.splice(1, 0, {
            kind: "transform",
            slug: "join-users",
            name: "Join users",
            transform: {
                output_kind: "records",
                inputs: {
                    orders: "fetch-orders",
                    accounts: "fetch-accounts",
                },
            },
        });

        const draftState = makeConflictState();
        draftState.pipeline_spec.steps.splice(1, 0, {
            kind: "transform",
            slug: "join-users",
            name: "Join users",
            transform: {
                output_kind: "records",
                inputs: {
                    orders: "fetch-orders-v2",
                    customers: "fetch-customers",
                },
            },
        });

        const session = t.buildConflictResolutionSession(canonicalState, draftState);
        const transformUnit = session.units.find((unit) => unit.key === "node:join-users");

        expect(transformUnit).toBeTruthy();
        expect(transformUnit.fields.map((field) => field.key)).toEqual(
            expect.arrayContaining([
                "step.transform.inputs.accounts",
                "step.transform.inputs.customers",
                "step.transform.inputs.orders",
            ]),
        );
    });
});

describe("conflict resolution choices", () => {
    const makeConflictState = ({ queryName = "Fetch users canonical", parameters = { limit: 100 } } = {}) => ({
        pipeline_spec: {
            metadata: {
                slug: "acme-users-sync",
                name: "ACME Users Sync",
            },
            steps: [
                {
                    kind: "query",
                    slug: "fetch-users",
                    name: queryName,
                    query: {
                        data_source_ref: "acme-postgres",
                        operation_type: "sql",
                        operation_ref: "list-active-users",
                        parameters,
                    },
                },
            ],
        },
        graph: {
            nodes: [{ step_slug: "fetch-users", x: 2, y: 1 }],
            edges: [],
            bindings: [],
        },
        context: {
            expected_version_id: 11,
            current_version_id: 12,
            draft_based_on_version_id: 11,
            source_format: "yaml",
            export_format: "yaml",
        },
    });

    it("DraftChoiceUpdatesMergedStateAndClearsUnresolvedCount", () => {
        expect(typeof t.buildConflictResolutionSession).toBe("function");
        expect(typeof t.applyConflictResolutionChoice).toBe("function");
        expect(typeof t.buildConflictResolutionMergedState).toBe("function");

        const session = t.buildConflictResolutionSession(
            makeConflictState({ queryName: "Fetch users canonical" }),
            makeConflictState({ queryName: "Fetch users draft" }),
        );

        const updated = t.applyConflictResolutionChoice(session, {
            unitKey: "node:fetch-users",
            fieldKey: "step.name",
            choice: "draft",
        });

        expect(updated.summary.unresolvedCount).toBe(0);
        expect(updated.summary.canApply).toBe(true);

        const merged = t.buildConflictResolutionMergedState(updated);
        expect(merged.pipeline_spec.steps.find((step) => step.slug === "fetch-users").name).toBe("Fetch users draft");
    });

    it("ManualStructuredChoiceOverridesJSONSubtree", () => {
        expect(typeof t.buildConflictResolutionSession).toBe("function");
        expect(typeof t.applyConflictResolutionChoice).toBe("function");
        expect(typeof t.buildConflictResolutionMergedState).toBe("function");

        const session = t.buildConflictResolutionSession(
            makeConflictState({ parameters: { limit: 100 } }),
            makeConflictState({ parameters: { limit: 250 } }),
        );

        const updated = t.applyConflictResolutionChoice(session, {
            unitKey: "node:fetch-users",
            fieldKey: "step.query.parameters",
            choice: "manual",
            manualValue: {
                limit: 500,
                include_disabled: true,
            },
        });

        const merged = t.buildConflictResolutionMergedState(updated);
        expect(merged.pipeline_spec.steps.find((step) => step.slug === "fetch-users").query.parameters).toEqual({
            limit: 500,
            include_disabled: true,
        });
    });

    it("BlocksApplyWhenTransformInputChoiceDivergesFromGraphChoice", () => {
        expect(typeof t.buildConflictResolutionSession).toBe("function");
        expect(typeof t.applyConflictResolutionChoice).toBe("function");

        const baseState = () => ({
            pipeline_spec: {
                metadata: {
                    slug: "acme-users-sync",
                    name: "ACME Users Sync",
                },
                steps: [
                    {
                        kind: "query",
                        slug: "fetch-users",
                        name: "Fetch users",
                        query: {
                            data_source_ref: "acme-postgres",
                            operation_type: "sql",
                            operation_ref: "list-users",
                        },
                    },
                    {
                        kind: "query",
                        slug: "fetch-accounts",
                        name: "Fetch accounts",
                        query: {
                            data_source_ref: "acme-postgres",
                            operation_type: "sql",
                            operation_ref: "list-accounts",
                        },
                    },
                    {
                        kind: "transform",
                        slug: "join-users",
                        name: "Join users",
                        transform: {
                            output_kind: "records",
                        },
                    },
                ],
            },
            graph: {
                nodes: [
                    { step_slug: "fetch-users", x: 2, y: 1 },
                    { step_slug: "fetch-accounts", x: 2, y: 3 },
                    { step_slug: "join-users", x: 8, y: 2 },
                ],
                edges: [],
                bindings: [],
            },
            context: {
                expected_version_id: 11,
                current_version_id: 12,
                draft_based_on_version_id: 11,
                source_format: "yaml",
                export_format: "yaml",
            },
        });
        const canonicalState = baseState();
        canonicalState.pipeline_spec.steps[2].transform.inputs = { users: "fetch-users" };
        canonicalState.graph.edges = [{ from_step: "fetch-users", to_step: "join-users" }];
        canonicalState.graph.bindings = [
            { from_step: "fetch-users", from_port: "rows", to_step: "join-users", to_port: "users" },
        ];

        const draftState = baseState();
        draftState.pipeline_spec.steps[2].transform.inputs = { users: "fetch-accounts" };
        draftState.graph.edges = [{ from_step: "fetch-accounts", to_step: "join-users" }];
        draftState.graph.bindings = [
            { from_step: "fetch-accounts", from_port: "rows", to_step: "join-users", to_port: "users" },
        ];

        let updated = t.buildConflictResolutionSession(canonicalState, draftState);
        [
            ["node:join-users", "step.transform.inputs.users", "draft"],
            ["edge:fetch-users->join-users", "unit.presence", "canonical"],
            ["edge:fetch-accounts->join-users", "unit.presence", "canonical"],
            ["binding:fetch-users:rows->join-users:users", "unit.presence", "canonical"],
            ["binding:fetch-accounts:rows->join-users:users", "unit.presence", "canonical"],
        ].forEach(([unitKey, fieldKey, choice]) => {
            updated = t.applyConflictResolutionChoice(updated, { unitKey, fieldKey, choice });
        });

        expect(updated.summary.unresolvedCount).toBe(0);
        expect(updated.summary.canApply).toBe(false);
        expect(issueMessages(updated.summary.issues)).toContain(
            "join-users: transform inputs must match incoming graph sources (inputs: fetch-accounts; graph: fetch-users)",
        );
    });

    it("BlocksNonScalarTransformInputValuesInResolvedState", () => {
        expect(typeof t.buildConflictResolutionSession).toBe("function");
        expect(typeof t.applyConflictResolutionChoice).toBe("function");

        const baseState = () => ({
            pipeline_spec: {
                metadata: {
                    slug: "orders-sync",
                    name: "Orders Sync",
                },
                steps: [
                    {
                        kind: "query",
                        slug: "fetch-orders",
                        name: "Fetch orders",
                        query: {
                            data_source_ref: "acme-postgres",
                            operation_type: "sql",
                            operation_ref: "list-orders",
                        },
                    },
                    {
                        kind: "transform",
                        slug: "normalize-orders",
                        name: "Normalize orders",
                        transform: {
                            output_kind: "rows",
                            inputs: { orders: "fetch-orders" },
                            sql: "SELECT id AS id FROM orders",
                        },
                    },
                ],
            },
            graph: {
                nodes: [{ step_slug: "fetch-orders" }, { step_slug: "normalize-orders" }],
                edges: [{ from_step: "fetch-orders", to_step: "normalize-orders" }],
                bindings: [
                    { from_step: "fetch-orders", from_port: "rows", to_step: "normalize-orders", to_port: "orders" },
                ],
            },
            context: {},
        });

        const canonicalState = baseState();
        const draftState = baseState();
        draftState.pipeline_spec.steps[1].transform.inputs = { orders: { from_step: "fetch-orders" } };

        let updated = t.buildConflictResolutionSession(canonicalState, draftState);
        const transformUnit = updated.units.find((unit) => unit.key === "node:normalize-orders");
        expect(transformUnit).toBeTruthy();
        expect(transformUnit.fields).toHaveLength(1);
        updated = t.applyConflictResolutionChoice(updated, {
            unitKey: "node:normalize-orders",
            fieldKey: transformUnit.fields[0].key,
            choice: "draft",
        });

        expect(updated.summary.unresolvedCount).toBe(0);
        expect(updated.summary.canApply).toBe(false);
        expect(issueMessages(updated.summary.issues)).toContain(
            "normalize-orders: transform inputs must match incoming graph sources (inputs: none; graph: fetch-orders)",
        );
    });

    it("RewritesResolvedStateBaselineToCanonicalVersion", () => {
        expect(typeof t.rewriteConflictResolutionBaseline).toBe("function");

        const rewritten = t.rewriteConflictResolutionBaseline(
            makeConflictState({ queryName: "Fetch users draft" }),
            "spec-hash-22",
        );

        expect(rewritten.context).toEqual(
            expect.objectContaining({
                expected_spec_hash: "spec-hash-22",
                current_version_spec_hash: "spec-hash-22",
                draft_based_on_spec_hash: "spec-hash-22",
            }),
        );
        expect(rewritten.pipeline_spec.steps.find((step) => step.slug === "fetch-users").name).toBe(
            "Fetch users draft",
        );
    });

    it("PreparesResolvedSaveStateFromFreshExportedGraphState", () => {
        expect(typeof t.prepareConflictResolutionSaveState).toBe("function");

        const mergedState = {
            pipeline_spec: {
                metadata: {
                    slug: "acme-users-sync",
                    name: "ACME Users Sync",
                },
                steps: [
                    {
                        kind: "query",
                        slug: "fetch-users",
                        name: "Fetch users",
                        query: {
                            data_source_ref: "acme-postgres",
                            operation_type: "sql",
                            operation_ref: "list-active-users",
                        },
                    },
                    {
                        kind: "sink",
                        slug: "load-users",
                        name: "Load users",
                        sink: {
                            input: {
                                from_step: "stale-step",
                            },
                            data_source_ref: "acme-mysql",
                            operation_type: "sql",
                            operation_ref: "insert-users",
                        },
                    },
                ],
            },
            graph: {
                nodes: [{ step_slug: "fetch-users" }, { step_slug: "load-users" }],
                edges: [{ from_step: "fetch-users", to_step: "load-users" }],
                bindings: [{ from_step: "fetch-users", from_port: "rows", to_step: "load-users", to_port: "payload" }],
            },
            context: {
                expected_spec_hash: "spec-hash-22",
                current_version_spec_hash: "spec-hash-22",
                draft_based_on_spec_hash: "spec-hash-22",
                source_format: "json",
                export_format: "json",
            },
        };

        const exportedState = t.buildBuilderStatePayload(
            [
                {
                    slug: "fetch-users",
                    kind: "query",
                    x: 2,
                    y: 1,
                    stepSlug: "fetch-users",
                    stepName: "Fetch users",
                    dataSourceSlug: "acme-postgres",
                    operationType: "sql",
                    querySlug: "list-active-users",
                },
                {
                    slug: "load-users",
                    kind: "sink",
                    x: 8,
                    y: 1,
                    stepSlug: "load-users",
                    stepName: "Load users",
                    sinkDataSourceSlug: "acme-mysql",
                    sinkOperationType: "sql",
                    sinkQuerySlug: "insert-users",
                },
            ],
            [{ from: "fetch-users", to: "load-users" }],
            [{ fromNode: "fetch-users", fromPort: "rows", toNode: "load-users", toPort: "payload" }],
        );

        const prepared = t.prepareConflictResolutionSaveState(mergedState, exportedState, {
            source_format: "yaml",
            export_format: "yaml",
        });

        expect(prepared.pipeline_spec.steps).toContainEqual({
            kind: "sink",
            slug: "load-users",
            name: "Load users",
            sink: {
                idempotency_class: "idempotent_with_key",
                input: "fetch-users",
                data_source_ref: "acme-mysql",
                operation_type: "sql",
                operation_ref: "insert-users",
            },
        });
        expect(prepared.context).toEqual(
            expect.objectContaining({
                expected_spec_hash: "spec-hash-22",
                current_version_spec_hash: "spec-hash-22",
                draft_based_on_spec_hash: "spec-hash-22",
                source_format: "json",
                export_format: "json",
            }),
        );
    });
});

describe("immutable ODCS port selection", () => {
    const reference = {
        contract_slug: "orders",
        revision_slug: "rev-one",
        schema_object_id: "order",
        representation: "rows",
    };
    it("uses effective exported inputs for orphan bindings and unbound graph edges", () => {
        const source = { kind: "query", slug: "source", stepSlug: "source" };
        const other = { kind: "query", slug: "other", stepSlug: "other" };
        const edge = { from: "source", to: "shape" };
        const secondEdge = { from: "other", to: "shape" };
        const binding = { fromNode: "source", toNode: "shape", fromPort: "rows", toPort: "source" };
        const orphan = { fromNode: "other", toNode: "shape", fromPort: "rows", toPort: "other" };
        for (const [edges, bindings, count] of [
            [[edge], [binding, orphan], 1],
            [[edge], [binding, { ...binding, fromPort: "events", toPort: "event" }], 1],
            [[edge], [], 1],
            [[edge, secondEdge], [], 2],
            [[edge, secondEdge], [binding, orphan], 2],
        ]) {
            const node = { kind: "transform", slug: "shape", stepSlug: "shape" };
            const nodes = [source, other, node];
            const maps = t.buildBuilderStepInputMaps(nodes.map(t.normalizeNodeForExport), edges, bindings);
            const inputCount = Object.keys(maps.transformInputsByStep.get(node.stepSlug) || {}).length;
            expect(inputCount).toBe(count);
            expect(t.setODCSPortRef(node, "transform.input", reference, inputCount)).toBe(count === 1);
            const exported = t
                .buildBuilderStatePayload(nodes, edges, bindings)
                .pipeline_spec.steps.find((step) => step.slug === "shape");
            expect(Object.keys(exported.transform.inputs)).toHaveLength(count);
            expect(exported.transform.input_contract_ref).toEqual(count === 1 ? reference : undefined);
        }
    });
    it("selects independent exact revisions for named inputs and event outputs", () => {
        const node = {
            kind: "transform",
            slug: "join",
            originalStep: {
                transform: {
                    input_contracts: { orders: { contract: { fields: [{ name: "id", type: "integer" }] } } },
                },
            },
        };
        expect(t.setODCSPortRef(node, "transform.input.orders", reference, 2)).toBe(true);
        const second = { ...reference, contract_slug: "accounts", revision_slug: "rev-two" };
        expect(t.setODCSPortRef(node, "transform.input.accounts", second, 2)).toBe(true);
        const exported = t.buildStepSpecForExportNode(t.normalizeNodeForExport(node));
        expect(exported.transform.input_contracts).toEqual({
            orders: { contract_ref: reference },
            accounts: { contract_ref: second },
        });
        expect(t.setODCSPortRef(node, "transform.input.orders", null, 2)).toBe(true);
        expect(node.originalStep.transform.input_contracts).toEqual({ accounts: { contract_ref: second } });
        const event = { kind: "input", slug: "event", stepSlug: "event" };
        const eventRef = { ...reference, representation: "json", root_shape: "object" };
        expect(t.setODCSPortRef(event, "input.output", eventRef)).toBe(true);
        expect(t.upstreamPayloadRepresentation(event)).toBe("json");
        expect(
            t.upstreamPayloadRepresentation({
                kind: "query",
                operationType: "sql",
                originalStep: { query: { output_contract_ref: { ...reference, representation: "csv" } } },
            }),
        ).toBe("csv");
        expect(t.buildStepSpecForExportNode(t.normalizeNodeForExport(event)).input.output_contract_ref).toEqual(
            eventRef,
        );
        const transform = { kind: "transform", slug: "join", stepSlug: "join" };
        const edges = [{ from: "event", to: "join" }];
        const bindings = [{ fromNode: "event", fromPort: "rows", toNode: "join", toPort: "events" }];
        expect(t.validateBindings([event, transform], edges, bindings)).toEqual([]);
        expect(t.validateEdgeConnection([event, transform], [], "event", "join")).toBe("");
        expect(t.validateEdgeConnection([event, transform], [], "join", "event")).toContain("Event inputs");
    });

    it("renames a named agreement without replacing a sibling or destination agreement", () => {
        const node = {
            originalStep: {
                transform: {
                    input_contracts: {
                        events: { contract_ref: reference },
                        customers: { contract_ref: { ...reference, revision_slug: "rev-two" } },
                    },
                },
            },
        };
        expect(t.renameTransformInputAgreement(node, "events", "incoming_events")).toBe(true);
        expect(node.originalStep.transform.input_contracts.events).toBeUndefined();
        expect(node.originalStep.transform.input_contracts.incoming_events).toEqual({ contract_ref: reference });
        const before = JSON.stringify(node);
        expect(t.renameTransformInputAgreement(node, "incoming_events", "customers")).toBe(false);
        expect(t.renameTransformInputAgreement(node, "incoming_events", "bad-name")).toBe(false);
        expect(JSON.stringify(node)).toBe(before);
        expect(node.originalStep.transform.input_contracts.customers.contract_ref.revision_slug).toBe("rev-two");
    });

    it("preserves rich inline agreements and refuses mixed legacy and named inputs", () => {
        const rich = {
            name: "events",
            representation: "json",
            root_shape: "object",
            fields: [
                {
                    name: "id",
                    type: "integer",
                    default: 9007199254740991,
                    nullable: false,
                    description: "Stable event identity",
                },
            ],
        };
        for (const [kind, port] of [
            ["input", "input.output"],
            ["transform", "transform.input.events"],
        ]) {
            const node = { kind, slug: "event", stepSlug: "event" };
            expect(t.setODCSPortInlineContract(node, port, rich)).toBe(true);
            const exported = t.buildStepSpecForExportNode(t.normalizeNodeForExport(node));
            expect(
                kind === "input" ? exported.input.output_contract : exported.transform.input_contracts.events.contract,
            ).toEqual(rich);
            expect(t.setODCSPortShape(node, port, "root_shape", "array")).toBe(true);
            const reshaped = t.buildStepSpecForExportNode(t.normalizeNodeForExport(node));
            expect(
                kind === "input" ? reshaped.input.output_contract : reshaped.transform.input_contracts.events.contract,
            ).toEqual({ ...rich, root_shape: "array" });
            expect(rich.root_shape).toBe("object");
            expect(t.setODCSPortInlineContract(node, port, {})).toBe(true);
            const cleared = t.buildStepSpecForExportNode(t.normalizeNodeForExport(node));
            expect(
                kind === "input" ? cleared.input.output_contract : cleared.transform.input_contracts,
            ).toBeUndefined();
        }
        const legacy = { kind: "transform", originalStep: { transform: { input_contract_ref: reference } } };
        const before = JSON.stringify(legacy);
        expect(t.setODCSPortRef(legacy, "transform.input.events", reference)).toBe(false);
        expect(t.setODCSPortInlineContract(legacy, "transform.input.events", rich)).toBe(false);
        expect(JSON.stringify(legacy)).toBe(before);
        expect(issueMessages(t.validateNodes([{ kind: "input", slug: "event", stepSlug: "event" }]))).toContain(
            "event: declare a JSON output contract or select an exact ODCS revision",
        );
    });

    it("updates legacy inline shapes and preserves exact revision identity", () => {
        const node = {
            kind: "query",
            queryOutputContract: { representation: "json", fields: [{ name: "id", type: "integer" }] },
        };
        expect(t.setODCSPortShape(node, "query.output", "root_shape", "array")).toBe(true);
        expect(node.queryOutputContract).toEqual({
            representation: "json",
            root_shape: "array",
            fields: [{ name: "id", type: "integer" }],
        });
        expect(t.setODCSPortRef(node, "query.output", reference)).toBe(true);
        expect(t.setODCSPortShape(node, "query.output", "root_shape", "object")).toBe(true);
        expect(node.originalStep.query.output_contract_ref).toEqual({ ...reference, root_shape: "object" });
    });

    it("reuses an exact object across all eligible ports and exports no inline conflict", () => {
        for (const [kind, port, key] of [
            ["query", "output", "queryOutputContract"],
            ["transform", "input", "transformInputContract"],
            ["transform", "output", "transformOutputContract"],
            ["sink", "input", "sinkInputContract"],
        ]) {
            const node = {
                kind,
                slug: `${kind}-step`,
                [key]: { fields: [{ name: "id", type: "integer" }] },
                originalStep: { [kind]: {} },
            };
            expect(t.setODCSPortRef(node, `${kind}.${port}`, reference, 1)).toBe(true);
            const exported = t.buildStepSpecForExportNode(t.normalizeNodeForExport(node));
            expect(exported[kind][`${port}_contract_ref`]).toEqual(reference);
            expect(exported[kind][`${port}_contract`]).toBeUndefined();
            expect(node[key]).toEqual({});
            expect(t.setODCSPortRef(node, `${kind}.${port}`, null)).toBe(true);
            expect(
                t.buildStepSpecForExportNode(t.normalizeNodeForExport(node))[kind][`${port}_contract_ref`],
            ).toBeUndefined();
        }
    });
    it("rejects multi-input transform references and ineligible ports without changing the node", () => {
        for (const [kind, port, count] of [
            ["transform", "input", 2],
            ["transform", "input", 0],
            ["input", "output", 1],
            ["sink", "output", 1],
        ]) {
            const node = { kind, slug: "step" };
            expect(t.setODCSPortRef(node, `${kind}.${port}`, reference, count)).toBe(false);
            expect(node).toEqual({ kind, slug: "step" });
        }
    });
});

describe("logical reference round trip", () => {
    it("retains source refs and authored extensions in export, save payload, and local reload", () => {
        const original = {
            metadata: { slug: "daily-sync", name: "Daily sync" },
            steps: [
                {
                    kind: "query",
                    slug: "fetch",
                    query: {
                        data_source_ref: "source",
                        operation_type: "sql",
                        operation_ref: "customers",
                        custom: { retained: true },
                    },
                },
                {
                    kind: "sink",
                    slug: "store",
                    sink: {
                        input: "fetch",
                        data_source_ref: "source",
                        operation_type: "sql",
                        operation_ref: "upsert-customers",
                        idempotency_class: "naturally_idempotent",
                        custom: { retained: true },
                    },
                },
            ],
        };
        const state = {
            pipeline_spec: original,
            graph: {
                nodes: [{ step_slug: "fetch" }, { step_slug: "store" }],
                edges: [{ from_step: "fetch", to_step: "store" }],
            },
        };
        const loaded = t.normalizeImportedBuilderState(state);
        const payload = t.buildBuilderStatePayload(loaded.nodes, loaded.edges, loaded.bindings, {
            slug: "daily-sync",
            name: "Daily sync",
            currentVersionId: "current",
            expectedVersionId: "current",
            draftBasedOnVersionId: "older",
        });
        expect(payload.pipeline_spec).toEqual(original);
        expect(payload.context).toMatchObject({
            current_version_spec_hash: "current",
            expected_spec_hash: "current",
            draft_based_on_spec_hash: "older",
        });
        window.localStorage.setItem("logical-ref-round-trip", JSON.stringify(payload));
        const restored = t.loadState("logical-ref-round-trip");
        expect(restored.nodes[0].dataSourceSlug).toBe("source");
        expect(restored.nodes[1].sinkDataSourceSlug).toBe("source");
        expect(
            t.buildBuilderStatePayload(restored.nodes, restored.edges, restored.bindings, {
                slug: "daily-sync",
                name: "Daily sync",
            }).pipeline_spec,
        ).toEqual(original);
    });
});

describe("builderEdgePath", () => {
    it("routes same-column dependencies outside cards through the row gap", () => {
        expect(t.builderEdgePath({ x: 306, y: 43, w: 180, h: 100 }, { x: 306, y: 172, w: 180, h: 100 })).toBe(
            "M 486 93 H 506 V 157.5 H 286 V 222 H 306",
        );
    });
    it("routes below an expanded attachment while keeping node-port endpoints", () => {
        const path = t.builderEdgePath(
            { x: 306, y: 43, w: 180, h: 100, attachmentBottom: 300 },
            { x: 306, y: 340, w: 180, h: 100 },
        );
        expect(path).toBe("M 486 93 H 506 V 320 H 286 V 390 H 306");
    });
    it("anchors forward curves at the output and input ports", () => {
        expect(t.builderEdgePath({ x: 68, y: 43, w: 180, h: 100 }, { x: 306, y: 172, w: 180, h: 100 })).toBe(
            "M 248 93 C 277 93, 277 222, 306 222",
        );
    });
});

describe("edgePreviewAgreements", () => {
    it("scopes legacy combined declarations independently for every relation", () => {
        const contract = {
            representation: "rows",
            fields: [
                { name: "orders.id", type: "integer" },
                { name: "accounts.id", type: "string" },
            ],
        };
        const preview = t.edgePreviewAgreements(
            { slug: "read", kind: "query" },
            { slug: "combine", kind: "transform", transformInputContract: contract },
            [
                { fromNode: "read", fromPort: "rows", toNode: "combine", toPort: "orders" },
                { fromNode: "read", fromPort: "rows", toNode: "combine", toPort: "accounts" },
            ],
        );
        expect(preview.agreements.slice(1).map((agreement) => agreement.fieldPrefix)).toEqual(["orders.", "accounts."]);
        expect(preview.agreements[1].contract).toBe(contract);
        expect(contract.fields[0].name).toBe("orders.id");
    });

    it("isolates a named input pin from sibling requirements", () => {
        const ref = { contract_slug: "events", revision_slug: "v2", schema_object_id: "event", representation: "json" };
        const source = { slug: "event", kind: "input", originalStep: { input: { output_contract_ref: ref } } };
        const target = {
            slug: "normalize",
            kind: "transform",
            originalStep: {
                transform: {
                    input_contracts: {
                        events: { contract_ref: ref },
                        customers: { contract_ref: { ...ref, contract_slug: "customers" } },
                    },
                },
            },
        };
        const preview = t.edgePreviewAgreements(source, target, [
            { fromNode: "event", fromPort: "json", toNode: "normalize", toPort: "events" },
        ]);
        expect(preview.agreements).toEqual([
            { label: "Producer output", ref, contract: null },
            { label: "Input: events", ref, contract: null },
        ]);
    });
    it("shows edited inline declarations and the sink requirement", () => {
        const contract = { representation: "rows", fields: [{ name: "id", type: "integer", required: true }] };
        const preview = t.edgePreviewAgreements(
            { slug: "read", kind: "query", queryOutputContract: contract },
            { slug: "write", kind: "sink", sinkInputContract: contract },
            [{ fromNode: "read", fromPort: "rows", toNode: "write", toPort: "payload" }],
        );
        expect(preview.agreements.every((agreement) => agreement.contract === contract && agreement.ref === null)).toBe(
            true,
        );
    });
    it("does not invent payload bindings for a dependency", () => {
        const preview = t.edgePreviewAgreements(
            { slug: "read", kind: "query" },
            { slug: "normalize", kind: "transform" },
            [],
        );
        expect(preview.bindings).toEqual([]);
        expect(preview.agreements).toEqual([{ label: "Producer output", ref: null, contract: null }]);
    });
});

describe("builderCanvasViewBox", () => {
    it("keeps an empty canvas at viewport size without zoom or offsets", () => {
        const view = t.builderCanvasViewBox([], { width: 240, height: 420 }, 2.5);
        expect(view).toEqual({
            width: 240,
            height: 420,
            fitScale: 1,
            scale: 1,
            offsetX: 0,
            offsetY: 0,
            boundsWidth: 240,
            boundsHeight: 420,
        });
    });
    const rects = [
        { x: 88, y: 50, w: 208, h: 116 },
        { x: 704, y: 350, w: 208, h: 116 },
    ];
    it("centers a graph at natural size when the viewport has room", () => {
        const view = t.builderCanvasViewBox(rects, { width: 1280, height: 800 });
        expect(view).toEqual({
            width: 952,
            height: 506,
            fitScale: 1,
            scale: 1,
            offsetX: 164,
            offsetY: 147,
            boundsWidth: 1280,
            boundsHeight: 800,
        });
    });
    it("fits the graph on a laptop without reserving a wider artboard", () => {
        const view = t.builderCanvasViewBox(rects, { width: 790, height: 518 });
        expect(view.scale).toBeCloseTo(790 / 952);
        expect(view.offsetX).toBeCloseTo(0);
        expect(view.offsetY).toBeGreaterThan(0);
        expect(view.boundsWidth).toBe(790);
        expect(view.boundsHeight).toBe(518);
    });
    it("retains scrollable dimensions at manual zoom", () => {
        const view = t.builderCanvasViewBox(rects, { width: 790, height: 518 }, 2);
        expect(view.scale).toBe(2);
        expect(view.boundsWidth).toBe(1904);
        expect(view.boundsHeight).toBe(1012);
        expect(view.offsetX).toBe(0);
        expect(view.offsetY).toBe(0);
    });
});

describe("sink replay policy validation", () => {
    it("rejects the unsupported S3 default and accepts a supported authored policy", () => {
        const descriptors = t.connectorDescriptorMap([
            {
                type: "s3",
                supported_sink_replay_strategies: ["naturally_idempotent", "deduplicated_by_ledger", "unsafe"],
            },
        ]);
        const sources = new Map([["objects", { type: "s3" }]]);
        const node = {
            slug: "write",
            kind: "sink",
            stepSlug: "write",
            sinkDataSourceSlug: "objects",
            sinkOperationType: "s3",
            sinkQuerySlug: "save",
            sinkIdempotencyClass: "idempotent_with_key",
        };
        expect(issueMessages(t.validateNodes([node], sources, descriptors))).toContain(
            "write: select a supported replay policy for s3",
        );
        node.sinkIdempotencyClass = "deduplicated_by_ledger";
        expect(t.validateNodes([node], sources, descriptors)).toEqual([]);
    });
});

describe("validateBoundaryContractDeclarations", () => {
    const source = { slug: "read", kind: "query" };
    const target = { slug: "normalize", kind: "transform" };
    const binding = { fromNode: "read", fromPort: "rows", toNode: "normalize", toPort: "records" };
    const contract = { representation: "rows", fields: [{ name: "id", type: "integer" }] };
    const ref = { contract_slug: "records", revision_slug: "v1", schema_object_id: "record" };

    it("identifies the destination and undeclared input without requiring both sides", () => {
        expect(t.validateBoundaryContractDeclarations([source, target], [binding])).toEqual([
            {
                message: "normalize: declare an output contract on read or an input contract for records",
                nodeSlug: "normalize",
            },
        ]);
        expect(
            t.validateBoundaryContractDeclarations([{ ...source, queryOutputContract: contract }, target], [binding]),
        ).toEqual([]);
        expect(
            t.validateBoundaryContractDeclarations(
                [source, { ...target, transformInputContract: contract }],
                [binding],
            ),
        ).toEqual([]);
        expect(t.validateBoundaryContractDeclarations([source, target], [])).toEqual([]);
    });

    it("accepts a producer pin and only the matching named consumer agreement", () => {
        expect(
            t.validateBoundaryContractDeclarations(
                [{ ...source, originalStep: { query: { output_contract_ref: ref } } }, target],
                [binding],
            ),
        ).toEqual([]);
        const named = {
            ...target,
            originalStep: { transform: { input_contracts: { records: { contract_ref: ref } } } },
        };
        expect(t.validateBoundaryContractDeclarations([source, named], [binding])).toEqual([]);
        expect(t.validateBoundaryContractDeclarations([source, named], [{ ...binding, toPort: "other" }])).toHaveLength(
            1,
        );
        named.originalStep.transform.input_contracts.records = { contract };
        expect(t.validateBoundaryContractDeclarations([source, named], [binding])).toEqual([]);
    });

    it("accepts a sink requirement or a transform output and rejects empty declarations", () => {
        const transform = { slug: "normalize", kind: "transform", transformOutputContract: contract };
        const sink = { slug: "write", kind: "sink" };
        const payload = { fromNode: "normalize", fromPort: "rows", toNode: "write", toPort: "payload" };
        expect(t.validateBoundaryContractDeclarations([transform, sink], [payload])).toEqual([]);
        delete transform.transformOutputContract;
        expect(
            t.validateBoundaryContractDeclarations([transform, { ...sink, sinkInputContract: contract }], [payload]),
        ).toEqual([]);
        expect(
            t.validateBoundaryContractDeclarations([transform, { ...sink, sinkInputContract: {} }], [payload]),
        ).toHaveLength(1);
    });
});

describe("S3 delivery configuration", () => {
    it("requires the delivery key even when the operation carries a key", () => {
        const sink = {
            slug: "deliver",
            kind: "sink",
            sinkBackend: "s3",
            sinkDataSourceSlug: "objects",
            sinkOperationType: "s3",
            sinkUseInlineOperation: true,
            sinkOperationInline: { type: "s3", operation: "write", key: "operation.csv" },
        };
        expect(issueMessages(t.validateNodes([sink]))).toContain("deliver: enter an S3 output object key");
        sink.originalStep = { sink: { config: { key: "delivery.csv" } } };
        expect(t.validateNodes([sink])).toEqual([]);
    });
});
