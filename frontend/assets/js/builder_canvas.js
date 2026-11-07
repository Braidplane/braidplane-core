(() => {
    const gridColumnCount = 24;
    const nodeWidth = 6;
    const nodeHeight = 3;
    const slugPattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
    const nodeContentClassName =
        "builder-node-content group relative flex h-full w-full flex-col overflow-hidden rounded-xl border bg-base-100 px-3 py-2 text-left shadow-sm transition hover:border-primary/60";

    // Node kind constants
    const KIND_INPUT = "input";
    const KIND_QUERY = "query";
    const KIND_TRANSFORM = "transform";
    const KIND_SINK = "sink";
    const DEFAULT_NODE_TYPE = "query";

    const isStepKind = (kind) => kind === KIND_INPUT || kind === KIND_QUERY || kind === KIND_TRANSFORM;

    const kindLabel = (kind) => {
        switch (kind) {
            case KIND_INPUT:
                return "Event Input";
            case KIND_QUERY:
                return "Query Step";
            case KIND_TRANSFORM:
                return "Transform Step";
            case KIND_SINK:
                return "Sink Step";
            default:
                return kind || "Node";
        }
    };

    // Port constants
    const PORT_ROWS = "rows";
    const PORT_DIR_IN = "in";
    const PORT_DIR_OUT = "out";

    // Misc constants
    const DRAFT_PIPELINE_SLUG = "draft-pipeline";
    const FORMAT_YAML = "yaml";
    const FORMAT_JSON = "json";
    const DEFAULT_SINK_IDEMPOTENCY_CLASS = "idempotent_with_key";
    const ALLOWED_SINK_IDEMPOTENCY_CLASSES = new Set([
        "naturally_idempotent",
        "idempotent_with_key",
        "deduplicated_by_ledger",
        "unsafe",
    ]);
    const builderPaletteExpandedWidthPx = 220;
    const builderRailCollapsedWidthPx = 32;
    const builderPropertiesExpandedWidthPx = 320;
    const builderPaletteColumnMinWidthPx = 860;
    const builderDockedPropertiesMinWidthPx = 920;
    const CONTRACT_REPRESENTATIONS = [
        { value: "", label: "Not declared" },
        { value: "rows", label: "Rows" },
        { value: "json", label: "JSON" },
        { value: "csv", label: "CSV" },
        { value: "file", label: "File" },
        { value: "multipart_form", label: "Multipart form" },
    ];
    const CONTRACT_FIELD_TYPES = ["string", "number", "integer", "boolean", "object", "array", "datetime"];
    const CONTRACT_VALIDATION_FORMATS = [
        { value: "", label: "No format" },
        { value: "email", label: "Email" },
        { value: "uuid", label: "UUID" },
        { value: "url", label: "URL" },
    ];
    const parameterValueTypes = [
        { value: "string", label: "Text" },
        { value: "number", label: "Number" },
        { value: "boolean", label: "Boolean" },
        { value: "json", label: "JSON" },
    ];
    const transformInputNamePattern = /^[a-z_][a-z0-9_]*$/;

    const builderWorkbenchLayoutForWidth = (availableWidth, paletteVisible = true, propertiesVisible = true) => {
        const normalizedWidth = Number.isFinite(Number(availableWidth)) ? Math.max(0, Number(availableWidth)) : 0;
        const canUsePaletteColumn = normalizedWidth >= builderPaletteColumnMinWidthPx;
        const canDockProperties = canUsePaletteColumn && normalizedWidth >= builderDockedPropertiesMinWidthPx;
        const paletteWidth = paletteVisible ? `${builderPaletteExpandedWidthPx}px` : `${builderRailCollapsedWidthPx}px`;
        const propertiesWidth = propertiesVisible
            ? `${builderPropertiesExpandedWidthPx}px`
            : `${builderRailCollapsedWidthPx}px`;

        let gridTemplateColumns = "minmax(0, 1fr)";
        if (canDockProperties) {
            gridTemplateColumns = `${paletteWidth} minmax(0, 1fr) ${propertiesWidth}`;
        } else if (canUsePaletteColumn) {
            gridTemplateColumns = `${paletteWidth} minmax(0, 1fr)`;
        }

        return {
            canUsePaletteColumn,
            canDockProperties,
            paletteWidth,
            propertiesWidth,
            gridTemplateColumns,
            propertiesGridColumn: canDockProperties ? "" : "1 / -1",
        };
    };

    const normalizeSinkIdempotencyClass = (value) => {
        const normalized = typeof value === "string" ? value.trim().toLowerCase() : "";
        return ALLOWED_SINK_IDEMPOTENCY_CLASSES.has(normalized) ? normalized : DEFAULT_SINK_IDEMPOTENCY_CLASS;
    };

    const DEFAULT_QUERY_OPERATION_TYPE = "sql";
    const UNKNOWN_SINK_BACKEND = "unknown";

    const normalizeConnectorString = (value) => (typeof value === "string" ? value.trim().toLowerCase() : "");

    const descriptorStringValue = (value, snakeName, camelName) =>
        normalizeConnectorString(value?.[snakeName] ?? value?.[camelName]);

    const descriptorBoolValue = (value, snakeName, camelName) => Boolean(value?.[snakeName] ?? value?.[camelName]);

    const fallbackQueryOperationTypeFromDataSourceType = (dataSourceType, fallback = DEFAULT_QUERY_OPERATION_TYPE) => {
        switch (normalizeConnectorString(dataSourceType)) {
            case "postgres":
            case "mysql":
                return "sql";
            case "bigquery":
                return "bigquery";
            case "snowflake":
                return "snowflake";
            case "http":
                return "http";
            case "hubspot":
                return "hubspot";
            case "ftp":
                return "ftp";
            case "mongodb":
                return "mongodb";
            case "git":
            case "github":
            case "gitlab":
                return "git";
            case "s3":
                return "s3";
            default:
                return fallback;
        }
    };

    const queryContextForOperationType = (operationType) => {
        if (operationType === "sql") {
            return {
                supportsInlineSQL: true,
                supportsInlineOperationJSON: false,
                queryRefLabel: "Saved query",
                inlineToggleLabel: "Use inline SQL instead of a saved query",
                hint: "SQL sources support saved queries or inline SQL.",
            };
        }

        const base = {
            supportsInlineSQL: false,
            supportsInlineOperationJSON: true,
            queryRefLabel: "Saved query",
            inlineToggleLabel: "Inline SQL is only available for SQL data sources",
            inlineOperationLabel: "Inline operation (JSON)",
        };

        if (operationType !== "") {
            return {
                ...base,
                hint: `${operationType.toUpperCase()} sources support saved queries or inline operation JSON.`,
            };
        }

        return {
            ...base,
            supportsInlineOperationJSON: false,
            hint: "Select a data source to configure a saved query or inline mode.",
        };
    };

    const sinkContextForOperationType = (operationType, sinkBackend, readOnly = false) => {
        if (readOnly) {
            return {
                operationType: "",
                queryRefLabel: "Saved query",
                hint: "This data source is read-only and cannot be used as a pipeline sink.",
            };
        }

        if (operationType !== "" && sinkBackend !== "" && sinkBackend !== UNKNOWN_SINK_BACKEND) {
            return {
                operationType,
                queryRefLabel: "Saved query",
                hint: `${operationType.toUpperCase()} sinks support saved queries or inline operation JSON.`,
            };
        }

        return {
            operationType: "",
            queryRefLabel: "Saved query",
            hint: "Select a writable data source to configure a saved query or inline operation.",
        };
    };

    const legacySchemaFamilyFromDataSourceType = (dataSourceType) => {
        switch (normalizeConnectorString(dataSourceType)) {
            case "postgres":
            case "mysql":
            case "bigquery":
            case "snowflake":
                return "sql";
            case "http":
                return "http";
            case "mongodb":
                return "document";
            case "hubspot":
                return "object_api";
            case "ftp":
            case "git":
            case "github":
            case "gitlab":
                return "file";
            case "s3":
                return "object_store";
            default:
                return "";
        }
    };

    const legacySinkBackendFromDataSourceType = (dataSourceType) => {
        switch (normalizeConnectorString(dataSourceType)) {
            case "postgres":
            case "mysql":
            case "bigquery":
            case "snowflake":
                return "db";
            case "http":
                return "http";
            case "mongodb":
                return "mongo";
            case "ftp":
                return "ftp";
            case "git":
            case "github":
            case "gitlab":
                return "git";
            case "s3":
                return "s3";
            default:
                return UNKNOWN_SINK_BACKEND;
        }
    };

    const normalizeConnectorDescriptor = (value) => {
        if (!value || typeof value !== "object" || Array.isArray(value)) {
            return null;
        }

        const type = descriptorStringValue(value, "type", "type");
        if (type === "") {
            return null;
        }

        const checkpointKinds = Array.isArray(
            value.incremental_source_checkpoint_kinds ?? value.incrementalSourceCheckpointKinds,
        )
            ? (value.incremental_source_checkpoint_kinds ?? value.incrementalSourceCheckpointKinds)
                  .map((kind) => normalizeConnectorString(kind))
                  .filter((kind) => kind !== "")
            : [];

        const replayStrategies = value.supported_sink_replay_strategies ?? value.supportedSinkReplayStrategies;

        return {
            type,
            schemaFamily: descriptorStringValue(value, "schema_family", "schemaFamily"),
            queryOperationType:
                descriptorStringValue(value, "query_operation_type", "queryOperationType") ||
                fallbackQueryOperationTypeFromDataSourceType(type),
            sinkBackend:
                descriptorStringValue(value, "sink_backend", "sinkBackend") ||
                legacySinkBackendFromDataSourceType(type),
            supportsPipelineSource: descriptorBoolValue(value, "supports_pipeline_source", "supportsPipelineSource"),
            supportsPipelineSink: descriptorBoolValue(value, "supports_pipeline_sink", "supportsPipelineSink"),
            incrementalSourceCheckpointKinds: checkpointKinds,
            supportedSinkReplayStrategies: Array.isArray(replayStrategies)
                ? replayStrategies
                      .map((strategy) => normalizeConnectorString(strategy))
                      .filter((strategy) => ALLOWED_SINK_IDEMPOTENCY_CLASSES.has(strategy))
                : [],
            forcedReadOnly: descriptorBoolValue(value, "forced_read_only", "forcedReadOnly"),
        };
    };

    const connectorDescriptorMap = (descriptors) => {
        const descriptorByType = new Map();
        if (!Array.isArray(descriptors)) {
            return descriptorByType;
        }

        descriptors.forEach((descriptor) => {
            const normalized = normalizeConnectorDescriptor(descriptor);
            if (normalized) {
                descriptorByType.set(normalized.type, normalized);
            }
        });
        return descriptorByType;
    };

    const readConnectorDescriptorPayload = (documentRef = document) => {
        const descriptorElement = documentRef?.getElementById?.("builder-connector-descriptors");
        if (!descriptorElement) {
            return new Map();
        }

        const raw =
            typeof descriptorElement.value === "string" ? descriptorElement.value : descriptorElement.textContent || "";
        try {
            return connectorDescriptorMap(JSON.parse(raw));
        } catch (_) {
            return new Map();
        }
    };

    const descriptorForDataSourceType = (dataSourceType, descriptorByType) => {
        if (!(descriptorByType instanceof Map)) {
            return null;
        }
        return descriptorByType.get(normalizeConnectorString(dataSourceType)) || null;
    };

    const enrichDataSourceWithDescriptor = (dataSource, descriptorByType) => {
        if (!dataSource || typeof dataSource !== "object" || Array.isArray(dataSource)) {
            return dataSource;
        }

        const type = normalizeConnectorString(dataSource.type);
        const descriptor = descriptorForDataSourceType(type, descriptorByType);
        const sinkBackend = descriptor?.sinkBackend || legacySinkBackendFromDataSourceType(type);
        const supportsPipelineSink = descriptor
            ? descriptor.supportsPipelineSink && sinkBackend !== UNKNOWN_SINK_BACKEND
            : sinkBackend !== UNKNOWN_SINK_BACKEND;

        return {
            ...dataSource,
            type,
            schemaFamily: descriptor?.schemaFamily || legacySchemaFamilyFromDataSourceType(type),
            queryOperationType: descriptor?.queryOperationType || fallbackQueryOperationTypeFromDataSourceType(type),
            sinkBackend,
            supportsPipelineSource: descriptor ? descriptor.supportsPipelineSource : true,
            supportsPipelineSink,
            incrementalSourceCheckpointKinds: descriptor?.incrementalSourceCheckpointKinds || [],
            forcedReadOnly: Boolean(descriptor?.forcedReadOnly),
            readOnly: Boolean(dataSource.readOnly) || Boolean(descriptor?.forcedReadOnly),
        };
    };

    // Data attribute name constants
    const ATTR_KIND = "data-kind";
    const ATTR_NODE_SLUG = "data-node-slug";
    const ATTR_NODE_TYPE = "data-node-type";
    const ATTR_DATA_SOURCE_SLUG = "data-data-source-slug";
    const ATTR_QUERY_SLUG = "data-query-slug";
    const ATTR_STEP_SLUG = "data-step-slug";
    const ATTR_STEP_NAME = "data-step-name";
    const ATTR_ORIGINAL_STEP = "data-original-step";
    const ATTR_QUERY_OUTPUT_CONTRACT = "data-query-output-contract";
    const ATTR_QUERY_INCREMENTAL = "data-query-incremental";
    const ATTR_TRANSFORM_OUTPUT_KIND = "data-transform-output-kind";
    const ATTR_TRANSFORM_SQL = "data-transform-sql";
    const ATTR_TRANSFORM_INPUTS = "data-transform-inputs";
    const ATTR_TRANSFORM_INPUT_CONTRACT = "data-transform-input-contract";
    const ATTR_TRANSFORM_OUTPUT_CONTRACT = "data-transform-output-contract";
    const ATTR_PARAMETERS = "data-parameters";
    const ATTR_OPERATION_TYPE = "data-operation-type";
    const ATTR_OPERATION_INLINE = "data-operation-inline";
    const ATTR_USE_INLINE_SQL = "data-use-inline-sql";
    const ATTR_USE_INLINE_OPERATION = "data-use-inline-operation";
    const ATTR_INLINE_SQL = "data-inline-sql";
    const ATTR_SINK_BACKEND = "data-sink-backend";
    const ATTR_SINK_DATA_SOURCE_SLUG = "data-sink-data-source-slug";
    const ATTR_SINK_OPERATION_TYPE = "data-sink-operation-type";
    const ATTR_SINK_OPERATION_INLINE = "data-sink-operation-inline";
    const ATTR_SINK_IDEMPOTENCY_CLASS = "data-sink-idempotency-class";
    const ATTR_SINK_INPUT_CONTRACT = "data-sink-input-contract";
    const ATTR_SINK_USE_INLINE_OPERATION = "data-sink-use-inline-operation";
    const ATTR_SINK_QUERY_SLUG = "data-sink-query-slug";
    const ATTR_PORT = "data-port";
    const ATTR_EDGE_FROM = "data-edge-from";
    const ATTR_EDGE_TO = "data-edge-to";
    const ATTR_BINDING_SOURCE_NODE = "data-binding-source-node";
    const ATTR_BINDING_SOURCE_PORT = "data-binding-source-port";

    // Status tone constants
    const TONE_ERROR = "error";
    const TONE_SUCCESS = "success";
    const TONE_NEUTRAL = "neutral";
    const TONE_WARNING = "warning";

    const builderRendering = window.braidplaneBuilderCanvasRendering;
    if (!builderRendering) {
        throw new Error("builder canvas rendering helpers must load before builder_canvas.js");
    }
    const { clearChildren, resetConflictDialogDOMState, bindConflictModalCloseReset } = builderRendering;

    const appendPipelinePanelShell = (paletteBodyElement) => {
        const section = document.createElement("section");
        section.id = "builder-pipeline-list-panel";
        section.className = "mt-5 rounded-lg border border-base-300 bg-base-100 p-3";
        section.innerHTML = `
            <div class="flex items-center justify-between gap-2">
                <p class="text-xs font-semibold uppercase tracking-wide text-base-content/60">Pipelines</p>
                <button class="btn btn-ghost btn-xs" type="button" id="builder-pipeline-list-toggle">Hide</button>
            </div>
            <div id="builder-pipeline-list-body" class="mt-3 space-y-2">
                <input class="input input-bordered input-xs w-full" id="builder-pipeline-search" aria-label="Search pipelines" placeholder="Search pipelines..." type="text"/>
                <div>
                    <select class="select select-bordered select-xs" id="builder-pipeline-draft-filter" aria-label="Filter pipelines by draft status">
                        <option value="">Draft</option>
                        <option value="yes">has draft</option>
                        <option value="no">no draft</option>
                    </select>
                </div>
                <div class="max-h-52 overflow-y-auto space-y-1" id="builder-pipeline-list-items">
                    <p class="text-xs text-base-content/60">Loading...</p>
                </div>
            </div>
        `;
        paletteBodyElement.appendChild(section);
        return section;
    };

    const builderShared = window.braidplaneBuilderCanvasShared;
    if (!builderShared) {
        throw new Error("builder canvas shared helpers must load before builder_canvas.js");
    }

    const {
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
        stableJSONStringify,
        cloneJSONValue,
        escapeHTML,
        isPlainObject,
        valuesAreEqual,
    } = builderShared;
    const builderState = window.braidplaneBuilderCanvasState;
    if (!builderState) {
        throw new Error("builder canvas state helpers must load before builder_canvas.js");
    }
    const { comparableBuilderState, comparableBuilderSnapshot } = builderState;
    const builderSerialization = window.braidplaneBuilderCanvasSerialization;
    if (!builderSerialization) {
        throw new Error("builder canvas serialization helpers must load before builder_canvas.js");
    }
    const builderAPI = window.braidplaneBuilderCanvasAPI;
    if (!builderAPI) {
        throw new Error("builder canvas API helpers must load before builder_canvas.js");
    }
    const builderInteractions = window.braidplaneBuilderCanvasInteractions;
    if (!builderInteractions) {
        throw new Error("builder canvas interaction helpers must load before builder_canvas.js");
    }
    const builderConflicts = window.braidplaneBuilderCanvasConflicts;
    if (!builderConflicts) {
        throw new Error("builder canvas conflict helpers must load before builder_canvas.js");
    }

    const conflictStructuredFieldKeys = new Set([
        "step.query.parameters",
        "step.query.operation_inline",
        "step.query.output_contract",
        "step.query.incremental",
        "step.transform.input_contract",
        "step.transform.output_contract",
        "step.sink.operation_inline",
        "step.sink.input_contract",
    ]);

    const conflictIgnoredFieldKeys = new Set([
        "step.slug",
        "graph.node.step_slug",
        "graph.node.x",
        "graph.node.y",
        "step.sink.input",
    ]);

    const conflictFieldLabelOverrides = {
        "step.kind": "Step kind",
        "step.name": "Name",
        "step.query.data_source_ref": "Data source",
        "step.query.operation_type": "Operation type",
        "step.query.operation_ref": "Operation",
        "step.query.operation_inline": "Inline operation",
        "step.query.parameters": "Parameters",
        "step.query.output_contract": "Output contract",
        "step.query.incremental": "Incremental checkpoint",
        "step.transform.output_kind": "Output kind",
        "step.transform.input_contract": "Input contract",
        "step.transform.output_contract": "Output contract",
        "step.sink.data_source_ref": "Data source",
        "step.sink.operation_type": "Operation type",
        "step.sink.operation_ref": "Operation",
        "step.sink.operation_inline": "Inline operation",
        "step.sink.input_contract": "Input contract",
        "unit.presence": "Presence",
    };

    const conflictFieldLabel = (fieldKey) => {
        if (typeof fieldKey !== "string" || fieldKey.trim() === "") {
            return "Field";
        }
        if (conflictFieldLabelOverrides[fieldKey]) {
            return conflictFieldLabelOverrides[fieldKey];
        }
        const parts = fieldKey.split(".");
        const leaf = parts[parts.length - 1] || "field";
        return leaf
            .split("_")
            .map((part) => (part.length > 0 ? part[0].toUpperCase() + part.slice(1) : part))
            .join(" ");
    };

    const conflictFieldValueType = (fieldKey, value) => {
        if (fieldKey === "unit.presence") {
            return "presence";
        }
        if (conflictStructuredFieldKeys.has(fieldKey) || Array.isArray(value) || isPlainObject(value)) {
            return "json";
        }
        return "text";
    };

    const setNestedValue = (input, pathSegments, value) => {
        if (!Array.isArray(pathSegments) || pathSegments.length === 0 || !isPlainObject(input)) {
            return;
        }
        let cursor = input;
        for (let index = 0; index < pathSegments.length - 1; index += 1) {
            const segment = pathSegments[index];
            if (!isPlainObject(cursor[segment])) {
                cursor[segment] = {};
            }
            cursor = cursor[segment];
        }
        cursor[pathSegments[pathSegments.length - 1]] = cloneJSONValue(value);
    };

    const deleteNestedValue = (input, pathSegments) => {
        if (!Array.isArray(pathSegments) || pathSegments.length === 0 || !isPlainObject(input)) {
            return;
        }
        let cursor = input;
        for (let index = 0; index < pathSegments.length - 1; index += 1) {
            const segment = pathSegments[index];
            if (!isPlainObject(cursor[segment])) {
                return;
            }
            cursor = cursor[segment];
        }
        delete cursor[pathSegments[pathSegments.length - 1]];
    };

    const removeArrayItem = (items, predicate) => {
        if (!Array.isArray(items)) {
            return [];
        }
        return items.filter((item) => !predicate(item));
    };

    const upsertArrayItem = (items, predicate, nextItem) => {
        const filtered = removeArrayItem(items, predicate);
        filtered.push(cloneJSONValue(nextItem));
        return filtered;
    };

    const collectConflictFieldEntries = (canonicalValue, draftValue, path, entries) => {
        if (conflictIgnoredFieldKeys.has(path)) {
            return;
        }
        if (path !== "" && conflictStructuredFieldKeys.has(path)) {
            if (!valuesAreEqual(canonicalValue, draftValue)) {
                entries.push({
                    key: path,
                    label: conflictFieldLabel(path),
                    valueType: "json",
                    canonicalPresent: canonicalValue !== undefined,
                    draftPresent: draftValue !== undefined,
                    canonicalValue: cloneJSONValue(canonicalValue),
                    draftValue: cloneJSONValue(draftValue),
                    choice: null,
                    manualValue: null,
                    manualText:
                        draftValue === undefined
                            ? ""
                            : formatJSONMap(draftValue) || stableJSONStringify(draftValue) || "",
                    manualError: "",
                });
            }
            return;
        }

        const canonicalIsObject = isPlainObject(canonicalValue);
        const draftIsObject = isPlainObject(draftValue);

        if (Array.isArray(canonicalValue) || Array.isArray(draftValue)) {
            if (!valuesAreEqual(canonicalValue, draftValue)) {
                entries.push({
                    key: path,
                    label: conflictFieldLabel(path),
                    valueType: "json",
                    canonicalPresent: canonicalValue !== undefined,
                    draftPresent: draftValue !== undefined,
                    canonicalValue: cloneJSONValue(canonicalValue),
                    draftValue: cloneJSONValue(draftValue),
                    choice: null,
                    manualValue: null,
                    manualText:
                        draftValue === undefined
                            ? ""
                            : formatJSONMap(draftValue) || stableJSONStringify(draftValue) || "",
                    manualError: "",
                });
            }
            return;
        }

        if (canonicalIsObject || draftIsObject) {
            const keys = new Set([
                ...Object.keys(canonicalIsObject ? canonicalValue : {}),
                ...Object.keys(draftIsObject ? draftValue : {}),
            ]);
            Array.from(keys)
                .sort()
                .forEach((key) => {
                    const nextPath = path === "" ? key : `${path}.${key}`;
                    collectConflictFieldEntries(
                        canonicalIsObject ? canonicalValue[key] : undefined,
                        draftIsObject ? draftValue[key] : undefined,
                        nextPath,
                        entries,
                    );
                });
            return;
        }

        if (!valuesAreEqual(canonicalValue, draftValue)) {
            entries.push({
                key: path,
                label: conflictFieldLabel(path),
                valueType: conflictFieldValueType(path, draftValue !== undefined ? draftValue : canonicalValue),
                canonicalPresent: canonicalValue !== undefined,
                draftPresent: draftValue !== undefined,
                canonicalValue: cloneJSONValue(canonicalValue),
                draftValue: cloneJSONValue(draftValue),
                choice: null,
                manualValue: null,
                manualText: draftValue == null ? "" : String(draftValue),
                manualError: "",
            });
        }
    };

    const createConflictPresenceField = (canonicalValue, draftValue) => ({
        key: "unit.presence",
        label: conflictFieldLabel("unit.presence"),
        valueType: "presence",
        canonicalPresent: canonicalValue !== undefined,
        draftPresent: draftValue !== undefined,
        canonicalValue: cloneJSONValue(canonicalValue),
        draftValue: cloneJSONValue(draftValue),
        choice: null,
        manualValue: null,
        manualText: "",
        manualError: "",
    });

    const buildConflictNodeUnit = (stepSlug, canonicalState, draftState) => {
        const canonicalStep = Array.isArray(canonicalState?.pipeline_spec?.steps)
            ? canonicalState.pipeline_spec.steps.find((step) => step?.slug === stepSlug)
            : null;
        const draftStep = Array.isArray(draftState?.pipeline_spec?.steps)
            ? draftState.pipeline_spec.steps.find((step) => step?.slug === stepSlug)
            : null;
        const canonicalNode = Array.isArray(canonicalState?.graph?.nodes)
            ? canonicalState.graph.nodes.find((node) => node?.step_slug === stepSlug)
            : null;
        const draftNode = Array.isArray(draftState?.graph?.nodes)
            ? draftState.graph.nodes.find((node) => node?.step_slug === stepSlug)
            : null;

        if (!canonicalStep && !draftStep) {
            return null;
        }

        const name = draftStep?.name || canonicalStep?.name || stepSlug;
        const kind = draftStep?.kind || canonicalStep?.kind || KIND_QUERY;

        if (!canonicalStep || !draftStep) {
            return {
                key: `node:${stepSlug}`,
                kind: "node",
                stepSlug,
                stepKind: kind,
                title: name,
                subtitle: kindLabel(kind),
                changeKind: canonicalStep ? "remove" : "add",
                fields: [
                    createConflictPresenceField(
                        canonicalStep ? { step: canonicalStep, node: canonicalNode } : undefined,
                        draftStep ? { step: draftStep, node: draftNode } : undefined,
                    ),
                ],
            };
        }

        const fields = [];
        collectConflictFieldEntries(canonicalStep, draftStep, "step", fields);
        if (fields.length === 0) {
            return null;
        }

        return {
            key: `node:${stepSlug}`,
            kind: "node",
            stepSlug,
            stepKind: kind,
            title: name,
            subtitle: kindLabel(kind),
            changeKind: "change",
            fields,
        };
    };

    const parseEdgeConflictKey = (value) => {
        const normalized = typeof value === "string" ? value.trim() : "";
        const prefix = "edge:";
        if (!normalized.startsWith(prefix)) {
            return { fromStep: "", toStep: "" };
        }
        const body = normalized.slice(prefix.length);
        const separator = body.indexOf("->");
        if (separator === -1) {
            return { fromStep: "", toStep: "" };
        }
        return {
            fromStep: body.slice(0, separator),
            toStep: body.slice(separator + 2),
        };
    };

    const parseBindingConflictKey = (value) => {
        const normalized = typeof value === "string" ? value.trim() : "";
        const prefix = "binding:";
        if (!normalized.startsWith(prefix)) {
            return { fromStep: "", fromPort: "", toStep: "", toPort: "" };
        }
        const body = normalized.slice(prefix.length);
        const separator = body.indexOf("->");
        if (separator === -1) {
            return { fromStep: "", fromPort: "", toStep: "", toPort: "" };
        }
        const left = body.slice(0, separator).split(":");
        const right = body.slice(separator + 2).split(":");
        return {
            fromStep: left[0] || "",
            fromPort: left[1] || "",
            toStep: right[0] || "",
            toPort: right[1] || "",
        };
    };

    const buildConflictEdgeUnit = (edgeKey, canonicalState, draftState) => {
        const { fromStep, toStep } = parseEdgeConflictKey(edgeKey);
        const canonicalEdge = Array.isArray(canonicalState?.graph?.edges)
            ? canonicalState.graph.edges.find((edge) => edge?.from_step === fromStep && edge?.to_step === toStep)
            : null;
        const draftEdge = Array.isArray(draftState?.graph?.edges)
            ? draftState.graph.edges.find((edge) => edge?.from_step === fromStep && edge?.to_step === toStep)
            : null;

        if (valuesAreEqual(canonicalEdge, draftEdge)) {
            return null;
        }

        return {
            key: edgeKey,
            kind: "edge",
            changeKind: canonicalEdge ? (draftEdge ? "change" : "remove") : "add",
            title: `${fromStep} -> ${toStep}`,
            subtitle: "Step dependency",
            fromStep,
            toStep,
            fields: [createConflictPresenceField(canonicalEdge || undefined, draftEdge || undefined)],
        };
    };

    const buildConflictBindingUnit = (bindingKey, canonicalState, draftState) => {
        const { fromStep, fromPort, toStep, toPort } = parseBindingConflictKey(bindingKey);
        const canonicalBinding = Array.isArray(canonicalState?.graph?.bindings)
            ? canonicalState.graph.bindings.find(
                  (binding) =>
                      binding?.from_step === fromStep &&
                      binding?.from_port === fromPort &&
                      binding?.to_step === toStep &&
                      binding?.to_port === toPort,
              )
            : null;
        const draftBinding = Array.isArray(draftState?.graph?.bindings)
            ? draftState.graph.bindings.find(
                  (binding) =>
                      binding?.from_step === fromStep &&
                      binding?.from_port === fromPort &&
                      binding?.to_step === toStep &&
                      binding?.to_port === toPort,
              )
            : null;

        if (valuesAreEqual(canonicalBinding, draftBinding)) {
            return null;
        }

        return {
            key: bindingKey,
            kind: "binding",
            changeKind: canonicalBinding ? (draftBinding ? "change" : "remove") : "add",
            title: `${fromStep}.${fromPort} -> ${toStep}.${toPort}`,
            subtitle: "Payload binding",
            fromStep,
            fromPort,
            toStep,
            toPort,
            fields: [createConflictPresenceField(canonicalBinding || undefined, draftBinding || undefined)],
        };
    };

    const sortConflictResolutionStateArrays = (state) => {
        if (!state || typeof state !== "object" || Array.isArray(state)) {
            return state;
        }
        if (Array.isArray(state?.graph?.nodes)) {
            state.graph.nodes = state.graph.nodes.slice().sort((left, right) => {
                const leftSlug = typeof left?.step_slug === "string" ? left.step_slug : "";
                const rightSlug = typeof right?.step_slug === "string" ? right.step_slug : "";
                return leftSlug.localeCompare(rightSlug);
            });
        }
        if (Array.isArray(state?.graph?.edges)) {
            state.graph.edges = state.graph.edges.slice().sort((left, right) => {
                const leftKey = `${left?.from_step || ""}->${left?.to_step || ""}`;
                const rightKey = `${right?.from_step || ""}->${right?.to_step || ""}`;
                return leftKey.localeCompare(rightKey);
            });
        }
        if (Array.isArray(state?.graph?.bindings)) {
            state.graph.bindings = state.graph.bindings.slice().sort((left, right) => {
                const leftKey = `${left?.from_step || ""}:${left?.from_port || ""}->${left?.to_step || ""}:${left?.to_port || ""}`;
                const rightKey = `${right?.from_step || ""}:${right?.from_port || ""}->${right?.to_step || ""}:${right?.to_port || ""}`;
                return leftKey.localeCompare(rightKey);
            });
        }
        return state;
    };

    const resolveConflictFieldSelection = (field) => {
        if (!field || typeof field !== "object") {
            return { present: false, value: undefined, manualError: "" };
        }
        if (field.choice === "canonical") {
            return {
                present: Boolean(field.canonicalPresent),
                value: cloneJSONValue(field.canonicalValue),
                manualError: "",
            };
        }
        if (field.choice === "manual") {
            return {
                present: true,
                value: cloneJSONValue(field.manualValue),
                manualError: field.manualError || "",
            };
        }
        return {
            present: Boolean(field.draftPresent),
            value: cloneJSONValue(field.draftValue),
            manualError: "",
        };
    };

    const applyConflictNodePresence = (state, unit, field) => {
        const resolved = resolveConflictFieldSelection(field);
        const slugValue = unit?.stepSlug || "";
        if (slugValue === "") {
            return;
        }

        if (!resolved.present || !resolved.value?.step) {
            state.pipeline_spec.steps = removeArrayItem(state.pipeline_spec.steps, (step) => step?.slug === slugValue);
            state.graph.nodes = removeArrayItem(state.graph.nodes, (node) => node?.step_slug === slugValue);
            return;
        }

        state.pipeline_spec.steps = upsertArrayItem(
            state.pipeline_spec.steps,
            (step) => step?.slug === slugValue,
            resolved.value.step,
        );
        if (resolved.value.node) {
            state.graph.nodes = upsertArrayItem(
                state.graph.nodes,
                (node) => node?.step_slug === slugValue,
                resolved.value.node,
            );
        }
    };

    const applyConflictPresenceUnit = (state, unit, field) => {
        const resolved = resolveConflictFieldSelection(field);
        if (unit.kind === "edge") {
            const edgeKey = `${unit.fromStep}->${unit.toStep}`;
            if (!resolved.present || !resolved.value) {
                state.graph.edges = removeArrayItem(
                    state.graph.edges,
                    (edge) => `${edge?.from_step || ""}->${edge?.to_step || ""}` === edgeKey,
                );
                return;
            }
            state.graph.edges = upsertArrayItem(
                state.graph.edges,
                (edge) => `${edge?.from_step || ""}->${edge?.to_step || ""}` === edgeKey,
                resolved.value,
            );
            return;
        }

        if (unit.kind === "binding") {
            const bindingKey = `${unit.fromStep}:${unit.fromPort}->${unit.toStep}:${unit.toPort}`;
            if (!resolved.present || !resolved.value) {
                state.graph.bindings = removeArrayItem(
                    state.graph.bindings,
                    (binding) =>
                        `${binding?.from_step || ""}:${binding?.from_port || ""}->${binding?.to_step || ""}:${binding?.to_port || ""}` ===
                        bindingKey,
                );
                return;
            }
            state.graph.bindings = upsertArrayItem(
                state.graph.bindings,
                (binding) =>
                    `${binding?.from_step || ""}:${binding?.from_port || ""}->${binding?.to_step || ""}:${binding?.to_port || ""}` ===
                    bindingKey,
                resolved.value,
            );
        }
    };

    const buildConflictResolutionMergedState = (session) => {
        const merged = cloneJSONValue(session?.draftState || {});
        if (!isPlainObject(merged.pipeline_spec)) {
            merged.pipeline_spec = {};
        }
        if (!Array.isArray(merged.pipeline_spec.steps)) {
            merged.pipeline_spec.steps = [];
        }
        if (!isPlainObject(merged.graph)) {
            merged.graph = {};
        }
        if (!Array.isArray(merged.graph.nodes)) {
            merged.graph.nodes = [];
        }
        if (!Array.isArray(merged.graph.edges)) {
            merged.graph.edges = [];
        }
        if (!Array.isArray(merged.graph.bindings)) {
            merged.graph.bindings = [];
        }
        if (!isPlainObject(merged.context)) {
            merged.context = {};
        }

        (session?.units || []).forEach((unit) => {
            if (!unit || !Array.isArray(unit.fields)) {
                return;
            }

            if (unit.kind === "node" && unit.changeKind !== "change") {
                applyConflictNodePresence(merged, unit, unit.fields[0]);
                return;
            }

            if ((unit.kind === "edge" || unit.kind === "binding") && unit.fields[0]) {
                applyConflictPresenceUnit(merged, unit, unit.fields[0]);
                return;
            }

            if (unit.kind !== "node" || unit.stepSlug === "") {
                return;
            }

            const targetStep = merged.pipeline_spec.steps.find((step) => step?.slug === unit.stepSlug);
            if (!isPlainObject(targetStep)) {
                return;
            }

            unit.fields.forEach((field) => {
                if (typeof field?.key !== "string" || !field.key.startsWith("step.")) {
                    return;
                }
                const resolved = resolveConflictFieldSelection(field);
                const nestedPath = field.key.split(".").slice(1);
                if (!resolved.present) {
                    deleteNestedValue(targetStep, nestedPath);
                    return;
                }
                setNestedValue(targetStep, nestedPath, resolved.value);
            });
        });

        return sortConflictResolutionStateArrays(merged);
    };

    const stepInputSourceSlug = (input) => {
        if (typeof input === "string") {
            return input.trim();
        }
        return "";
    };

    const sortedUniqueValues = (values) => Array.from(new Set(values.filter((value) => value !== ""))).sort();

    const formatSourceSet = (values) => {
        if (!Array.isArray(values) || values.length === 0) {
            return "none";
        }
        return values.join(", ");
    };

    const collectTransformInputGraphConsistencyIssues = (state) => {
        const steps = Array.isArray(state?.pipeline_spec?.steps) ? state.pipeline_spec.steps : [];
        const graph = isPlainObject(state?.graph) ? state.graph : {};
        const edges = Array.isArray(graph.edges) ? graph.edges : [];
        const bindings = Array.isArray(graph.bindings) ? graph.bindings : [];
        const stepBySlug = new Map(
            steps
                .map((step) => [typeof step?.slug === "string" ? step.slug : "", step])
                .filter(([stepSlug]) => stepSlug !== ""),
        );
        const edgeSet = new Set(
            edges
                .map(
                    (edge) =>
                        `${typeof edge?.from_step === "string" ? edge.from_step : ""}->${typeof edge?.to_step === "string" ? edge.to_step : ""}`,
                )
                .filter((key) => key !== "->"),
        );
        const graphSourcesByTransform = new Map();

        if (bindings.length > 0) {
            bindings.forEach((binding) => {
                const fromStep = typeof binding?.from_step === "string" ? binding.from_step : "";
                const toStep = typeof binding?.to_step === "string" ? binding.to_step : "";
                const fromPort = typeof binding?.from_port === "string" ? binding.from_port : "";
                const toPort = typeof binding?.to_port === "string" ? binding.to_port : "";
                const targetStep = stepBySlug.get(toStep);
                const sourceStep = stepBySlug.get(fromStep);
                if (
                    !targetStep?.transform ||
                    (!sourceStep?.input && !sourceStep?.query && !sourceStep?.transform) ||
                    fromPort !== PORT_ROWS ||
                    toPort === "" ||
                    !edgeSet.has(`${fromStep}->${toStep}`)
                ) {
                    return;
                }
                graphSourcesByTransform.set(toStep, [...(graphSourcesByTransform.get(toStep) || []), fromStep]);
            });
        } else {
            edges.forEach((edge) => {
                const fromStep = typeof edge?.from_step === "string" ? edge.from_step : "";
                const toStep = typeof edge?.to_step === "string" ? edge.to_step : "";
                const targetStep = stepBySlug.get(toStep);
                const sourceStep = stepBySlug.get(fromStep);
                if (!targetStep?.transform || (!sourceStep?.input && !sourceStep?.query && !sourceStep?.transform)) {
                    return;
                }
                graphSourcesByTransform.set(toStep, [...(graphSourcesByTransform.get(toStep) || []), fromStep]);
            });
        }

        return steps.flatMap((step) => {
            if (!step?.transform) {
                return [];
            }
            const transformInputs = isPlainObject(step.transform.inputs) ? step.transform.inputs : {};
            const stepSlug = typeof step.slug === "string" ? step.slug : "";
            const inputSources = sortedUniqueValues(Object.values(transformInputs).map(stepInputSourceSlug));
            const graphSources = sortedUniqueValues(graphSourcesByTransform.get(stepSlug) || []);
            if (valuesAreEqual(inputSources, graphSources)) {
                return [];
            }
            return [
                {
                    message: `${stepSlug}: transform inputs must match incoming graph sources (inputs: ${formatSourceSet(inputSources)}; graph: ${formatSourceSet(graphSources)})`,
                    nodeSlug: stepSlug,
                },
            ];
        });
    };

    const collectConflictResolutionValidationIssues = (state) => {
        const normalized = normalizeImportedBuilderState(state);
        if (!normalized) {
            return [{ message: "Merged pipeline state is invalid." }];
        }
        return [
            ...validateNodes(normalized.nodes),
            ...validateEdgeTopology(normalized.nodes, normalized.edges),
            ...validateBindings(normalized.nodes, normalized.edges, normalized.bindings),
            ...validateBindingCompleteness(normalized.nodes, normalized.edges, normalized.bindings),
            ...validateBoundaryContractDeclarations(normalized.nodes, normalized.bindings),
            ...collectTransformInputGraphConsistencyIssues(state),
        ];
    };

    const summarizeConflictResolutionSession = (session) => {
        const unresolvedCount = (session?.units || []).reduce((count, unit) => {
            return (
                count +
                unit.fields.reduce((fieldCount, field) => {
                    return fieldCount + (field?.choice ? 0 : 1);
                }, 0)
            );
        }, 0);

        const hasManualErrors = (session?.units || []).some((unit) =>
            unit.fields.some((field) => field?.choice === "manual" && field?.manualError),
        );
        const issues = hasManualErrors
            ? []
            : collectConflictResolutionValidationIssues(buildConflictResolutionMergedState(session));

        return {
            ...session,
            summary: {
                unresolvedCount,
                canApply:
                    session?.units?.length > 0 && unresolvedCount === 0 && !hasManualErrors && issues.length === 0,
                issues,
            },
        };
    };

    const buildConflictResolutionSession = (canonicalState, draftState) => {
        const canonicalComparable = comparableBuilderState(canonicalState);
        const draftComparable = comparableBuilderState(draftState);
        const stepSlugs = new Set([
            ...(Array.isArray(canonicalComparable?.pipeline_spec?.steps)
                ? canonicalComparable.pipeline_spec.steps.map((step) => step?.slug).filter(Boolean)
                : []),
            ...(Array.isArray(draftComparable?.pipeline_spec?.steps)
                ? draftComparable.pipeline_spec.steps.map((step) => step?.slug).filter(Boolean)
                : []),
        ]);
        const edgeKeys = new Set([
            ...(Array.isArray(canonicalComparable?.graph?.edges)
                ? canonicalComparable.graph.edges.map((edge) => `edge:${edge?.from_step || ""}->${edge?.to_step || ""}`)
                : []),
            ...(Array.isArray(draftComparable?.graph?.edges)
                ? draftComparable.graph.edges.map((edge) => `edge:${edge?.from_step || ""}->${edge?.to_step || ""}`)
                : []),
        ]);
        const bindingKeys = new Set([
            ...(Array.isArray(canonicalComparable?.graph?.bindings)
                ? canonicalComparable.graph.bindings.map(
                      (binding) =>
                          `binding:${binding?.from_step || ""}:${binding?.from_port || ""}->${binding?.to_step || ""}:${binding?.to_port || ""}`,
                  )
                : []),
            ...(Array.isArray(draftComparable?.graph?.bindings)
                ? draftComparable.graph.bindings.map(
                      (binding) =>
                          `binding:${binding?.from_step || ""}:${binding?.from_port || ""}->${binding?.to_step || ""}:${binding?.to_port || ""}`,
                  )
                : []),
        ]);

        const units = [];

        Array.from(stepSlugs)
            .sort()
            .forEach((stepSlug) => {
                const unit = buildConflictNodeUnit(stepSlug, canonicalComparable, draftComparable);
                if (unit) {
                    units.push(unit);
                }
            });

        Array.from(edgeKeys)
            .sort()
            .forEach((edgeKey) => {
                const unit = buildConflictEdgeUnit(edgeKey, canonicalComparable, draftComparable);
                if (unit) {
                    units.push(unit);
                }
            });

        Array.from(bindingKeys)
            .sort()
            .forEach((bindingKey) => {
                const unit = buildConflictBindingUnit(bindingKey, canonicalComparable, draftComparable);
                if (unit) {
                    units.push(unit);
                }
            });

        return summarizeConflictResolutionSession({
            canonicalState: cloneJSONValue(canonicalState || {}),
            draftState: cloneJSONValue(draftState || {}),
            units,
            selectedUnitKey: units[0]?.key || "",
            summary: { unresolvedCount: 0, canApply: false, issues: [] },
        });
    };

    const updateConflictResolutionSession = (session, updater) => {
        const nextSession = cloneJSONValue(session || {});
        if (typeof updater === "function") {
            updater(nextSession);
        }
        return summarizeConflictResolutionSession(nextSession);
    };

    const selectConflictResolutionUnit = (session, unitKey) =>
        updateConflictResolutionSession(session, (nextSession) => {
            nextSession.selectedUnitKey = unitKey;
        });

    const applyConflictResolutionChoice = (session, { unitKey, fieldKey, choice, manualValue }) =>
        updateConflictResolutionSession(session, (nextSession) => {
            const unit = Array.isArray(nextSession.units)
                ? nextSession.units.find((entry) => entry.key === unitKey)
                : null;
            const field = unit?.fields?.find((entry) => entry.key === fieldKey);
            if (!field) {
                return;
            }
            field.choice = choice;
            if (choice === "manual") {
                field.manualValue =
                    manualValue !== undefined
                        ? cloneJSONValue(manualValue)
                        : cloneJSONValue(field.draftPresent ? field.draftValue : field.canonicalValue);
                field.manualError = "";
                if (field.valueType === "json") {
                    field.manualText = formatJSONMap(field.manualValue) || stableJSONStringify(field.manualValue) || "";
                } else {
                    field.manualText = field.manualValue == null ? "" : String(field.manualValue);
                }
            }
        });

    const applyConflictResolutionUnitChoice = (session, { unitKey, choice }) =>
        updateConflictResolutionSession(session, (nextSession) => {
            const unit = Array.isArray(nextSession.units)
                ? nextSession.units.find((entry) => entry.key === unitKey)
                : null;
            if (!unit || !Array.isArray(unit.fields)) {
                return;
            }
            unit.fields.forEach((field) => {
                field.choice = choice;
                if (choice === "manual") {
                    field.manualValue = cloneJSONValue(field.draftPresent ? field.draftValue : field.canonicalValue);
                    field.manualError = "";
                }
            });
            nextSession.selectedUnitKey = unitKey;
        });

    const updateConflictResolutionManualValue = (session, { unitKey, fieldKey, manualText }) =>
        updateConflictResolutionSession(session, (nextSession) => {
            const unit = Array.isArray(nextSession.units)
                ? nextSession.units.find((entry) => entry.key === unitKey)
                : null;
            const field = unit?.fields?.find((entry) => entry.key === fieldKey);
            if (!field) {
                return;
            }
            field.choice = "manual";
            field.manualText = manualText;
            if (field.valueType === "json") {
                const trimmed = typeof manualText === "string" ? manualText.trim() : "";
                try {
                    field.manualValue = trimmed === "" ? {} : JSON.parse(trimmed);
                    field.manualError = "";
                } catch (_) {
                    field.manualError = "Manual JSON must be valid.";
                }
                return;
            }
            field.manualValue = manualText;
            field.manualError = "";
        });

    const rewriteConflictResolutionBaseline = (state, canonicalVersionId, fallbackContext = {}) => {
        const rewritten = cloneJSONValue(state || {});
        if (!isPlainObject(rewritten.context)) {
            rewritten.context = {};
        }
        rewritten.context.expected_spec_hash = canonicalVersionId;
        rewritten.context.current_version_spec_hash = canonicalVersionId;
        rewritten.context.draft_based_on_spec_hash = canonicalVersionId;
        if (!rewritten.context.source_format && fallbackContext?.source_format) {
            rewritten.context.source_format = fallbackContext.source_format;
        }
        if (!rewritten.context.export_format && fallbackContext?.export_format) {
            rewritten.context.export_format = fallbackContext.export_format;
        }
        return rewritten;
    };

    const prepareConflictResolutionSaveState = (mergedState, exportedState, fallbackContext = {}) => {
        const sourceFormat = normalizePipelineFormat(
            mergedState?.context?.source_format ?? exportedState?.context?.source_format,
            fallbackContext.source_format || FORMAT_YAML,
        );
        const exportFormat = normalizePipelineFormat(
            mergedState?.context?.export_format ?? exportedState?.context?.export_format,
            fallbackContext.export_format || sourceFormat,
        );
        const canonicalVersionId = parseOptionalVersionToken(
            mergedState?.context?.current_version_spec_hash ??
                mergedState?.context?.expected_spec_hash ??
                mergedState?.context?.draft_based_on_spec_hash,
        );

        const normalizedExportState =
            canonicalVersionId == null
                ? cloneJSONValue(exportedState || {})
                : rewriteConflictResolutionBaseline(exportedState, canonicalVersionId, {
                      source_format: sourceFormat,
                      export_format: exportFormat,
                  });

        if (!isPlainObject(normalizedExportState.context)) {
            normalizedExportState.context = {};
        }
        normalizedExportState.context.source_format = sourceFormat;
        normalizedExportState.context.export_format = exportFormat;
        return normalizedExportState;
    };

    const { saveStatusMessageFromPipeline, runStatusMessage, saveResponseProblem } = builderShared;

    const setPrimaryActionDisabledState = (
        saveButton,
        runButton,
        publishButton,
        isValid,
        canPersist,
        hasUnpersistedChanges,
        pipelineSlug,
        hasDraft,
        targetEnvironmentSlug = "",
    ) => {
        if (saveButton instanceof HTMLButtonElement) {
            saveButton.disabled = !isValid || !canPersist || !hasUnpersistedChanges;
            saveButton.title = !isValid
                ? "Resolve validation issues before saving."
                : !canPersist
                  ? "Add at least one node before saving to Braidplane."
                  : !hasUnpersistedChanges
                    ? "No unsaved changes."
                    : "";
        }
        if (publishButton instanceof HTMLButtonElement) {
            publishButton.disabled =
                !hasDraft || !pipelineSlug || pipelineSlug === DRAFT_PIPELINE_SLUG || hasUnpersistedChanges;
            publishButton.title = hasUnpersistedChanges
                ? "Save your changes before publishing."
                : !hasDraft
                  ? "Propose is unavailable until a draft exists on the server."
                  : !pipelineSlug || pipelineSlug === DRAFT_PIPELINE_SLUG
                    ? "Propose is unavailable until this draft has a real pipeline slug."
                    : "";
        }
        if (runButton instanceof HTMLButtonElement) {
            const runtimeTarget = String(targetEnvironmentSlug || "").trim();
            const canExecute = runButton.dataset.canExecute === "true";
            runButton.disabled =
                !canExecute ||
                !isValid ||
                hasDraft ||
                !pipelineSlug ||
                pipelineSlug === DRAFT_PIPELINE_SLUG ||
                !runtimeTarget;
            runButton.title = runStatusMessage(pipelineSlug, isValid, hasDraft, runtimeTarget, canExecute);
        }
    };

    const { formatJSONMap, hasObjectEntries } = builderShared;

    const contractForExport = (value, fallbackName = "") => {
        if (!hasObjectEntries(value)) {
            return {};
        }
        const output = { ...value };
        if (typeof output.name === "string" && output.name.trim() === "") {
            delete output.name;
        }
        if (!("name" in output) && fallbackName.trim() !== "") {
            output.name = fallbackName.trim();
        }
        return output;
    };

    const { normalizeStateForGridColumns } = builderState;

    const inputPortsForKind = (kind) => {
        if (kind === KIND_TRANSFORM) {
            return [PORT_ROWS];
        }
        if (kind === KIND_SINK) {
            return ["payload"];
        }
        return [];
    };

    const outputPortsForKind = (kind) => {
        if (isStepKind(kind)) {
            return [PORT_ROWS];
        }
        return [];
    };

    const bindingSourceNodeValue = (nodeKind, incomingSlugs, binding) => {
        if (isStepKind(nodeKind) || nodeKind === KIND_SINK) {
            return incomingSlugs[0] || "";
        }

        return binding?.fromNode || "";
    };

    const normalizeContractRepresentation = (value) => {
        const normalized = typeof value === "string" ? value.trim().toLowerCase() : "";
        if (
            normalized === "rows" ||
            normalized === "json" ||
            normalized === "csv" ||
            normalized === "file" ||
            normalized === "multipart_form" ||
            normalized === "unknown"
        ) {
            return normalized;
        }

        return "";
    };

    const representationDisplayLabel = (representation) => {
        switch (normalizeContractRepresentation(representation)) {
            case "rows":
                return "rows";
            case "json":
                return "JSON";
            case "csv":
                return "CSV";
            case "file":
                return "file";
            case "multipart_form":
                return "multipart form";
            default:
                return "unknown";
        }
    };

    const sinkExpectedRepresentation = (node) => {
        const referenced = normalizeContractRepresentation(
            node?.originalStep?.sink?.input_contract_ref?.representation,
        );
        if (referenced && referenced !== "unknown") return referenced;
        const contract = node?.sinkInputContract;
        if (!contract || typeof contract !== "object" || Array.isArray(contract)) {
            return "";
        }

        const representation = normalizeContractRepresentation(contract.representation);
        if (representation === "unknown") {
            return "";
        }

        return representation;
    };

    const queryPayloadRepresentation = (node, dataSourceMetadata) => {
        if (node?.kind !== KIND_QUERY) {
            return "";
        }

        const declaredRepresentation = normalizeContractRepresentation(node.queryOutputContract?.representation);
        if (declaredRepresentation && declaredRepresentation !== "unknown") {
            return declaredRepresentation;
        }

        const explicitDataSourceType = normalizeConnectorString(node.dataSourceType);
        const dataSourceSlug = typeof node.dataSourceSlug === "string" ? node.dataSourceSlug.trim() : "";
        const metadata = dataSourceMetadata instanceof Map ? dataSourceMetadata.get(dataSourceSlug) : null;
        const resolvedDataSourceType = explicitDataSourceType || normalizeConnectorString(metadata?.type);
        const schemaFamily =
            normalizeConnectorString(metadata?.schemaFamily) ||
            legacySchemaFamilyFromDataSourceType(resolvedDataSourceType);

        switch (schemaFamily) {
            case "sql":
            case "object_api":
                return "rows";
            case "http":
            case "document":
                return "json";
            case "file":
            case "object_store":
                if (
                    String(node.operationInline?.format || "")
                        .trim()
                        .toLowerCase() === "file"
                ) {
                    return "file";
                }
                return "rows";
            default:
                return "";
        }
    };

    const upstreamPayloadRepresentation = (node, dataSourceMetadata) => {
        if (!node || typeof node !== "object") {
            return "";
        }

        const referenced = normalizeContractRepresentation(
            node.originalStep?.[node.kind]?.output_contract_ref?.representation,
        );
        if (referenced && referenced !== "unknown") return referenced;
        if (node.kind === KIND_INPUT) return "json";

        if (node.kind === KIND_QUERY) {
            return queryPayloadRepresentation(node, dataSourceMetadata);
        }

        if (node.kind !== KIND_TRANSFORM) {
            return "";
        }

        const outputKind =
            typeof node.transformOutputKind === "string" ? node.transformOutputKind.trim().toLowerCase() : "";
        if (outputKind === "rows") {
            return "rows";
        }
        if (outputKind === "json" || outputKind === "document") {
            return "json";
        }
        if (outputKind === "csv") {
            return "csv";
        }

        return "";
    };

    const payloadDiagnosticForBinding = (node, binding, nodeBySlug, dataSourceMetadata) => {
        if (node?.kind !== KIND_SINK) {
            return null;
        }

        const expectedRepresentation = sinkExpectedRepresentation(node);
        if (!binding || typeof binding !== "object") {
            if (!expectedRepresentation) {
                return null;
            }

            return {
                tone: TONE_NEUTRAL,
                message: `Sink expects ${representationDisplayLabel(expectedRepresentation)} payload once an upstream node is bound.`,
            };
        }

        const sourceNode = nodeBySlug instanceof Map ? nodeBySlug.get(binding.fromNode || "") : null;
        const sourceRepresentation = upstreamPayloadRepresentation(sourceNode, dataSourceMetadata);

        if (!expectedRepresentation) {
            if (!sourceRepresentation) {
                return null;
            }

            return {
                tone: TONE_NEUTRAL,
                message: `Upstream provides ${representationDisplayLabel(sourceRepresentation)} payload. Sink does not declare an expected representation.`,
            };
        }

        if (!sourceRepresentation) {
            const sourceLabel = sourceNode?.slug || binding.fromNode || "Bound upstream";
            return {
                tone: TONE_NEUTRAL,
                message: `Sink expects ${representationDisplayLabel(expectedRepresentation)} payload. ${sourceLabel} representation is unavailable.`,
            };
        }

        if (sourceRepresentation === expectedRepresentation) {
            return {
                tone: TONE_SUCCESS,
                message: `Payload representation is compatible: upstream ${representationDisplayLabel(sourceRepresentation)}, sink expects ${representationDisplayLabel(expectedRepresentation)}.`,
            };
        }

        return {
            tone: TONE_ERROR,
            message: `Payload representation mismatch: upstream ${representationDisplayLabel(sourceRepresentation)}, sink expects ${representationDisplayLabel(expectedRepresentation)}.`,
        };
    };

    const contractFieldCount = (contract) =>
        contract && typeof contract === "object" && Array.isArray(contract.fields) ? contract.fields.length : 0;

    const hasFieldValidations = (field) => {
        if (!field || typeof field !== "object" || !field.validations || typeof field.validations !== "object") {
            return false;
        }
        return Object.values(field.validations).some((value) => {
            if (value === null || value === undefined || value === false || value === "") {
                return false;
            }
            return !Array.isArray(value) || value.length > 0;
        });
    };

    const contractValidationCount = (contract) =>
        contract && typeof contract === "object" && Array.isArray(contract.fields)
            ? contract.fields.filter(hasFieldValidations).length
            : 0;

    const compactSQLLine = (value) => {
        const line =
            typeof value === "string"
                ? value
                      .trim()
                      .split(/\r?\n/)
                      .map((item) => item.trim())
                      .find((item) => item !== "")
                : "";
        if (!line) {
            return "not set";
        }
        return line.length > 92 ? `${line.slice(0, 89)}...` : line;
    };

    const contractSummary = (contract) => {
        const representation =
            contract && typeof contract === "object" && typeof contract.representation === "string"
                ? contract.representation
                : "";
        const fields = contractFieldCount(contract);
        const validations = contractValidationCount(contract);
        const parts = [];
        if (representation) {
            parts.push(representationDisplayLabel(representation));
        }
        if (fields > 0) {
            parts.push(`${fields} field${fields === 1 ? "" : "s"}`);
        }
        if (validations > 0) {
            parts.push(`${validations} validation${validations === 1 ? "" : "s"}`);
        }
        return parts.length > 0 ? parts.join(", ") : "not declared";
    };

    const addBadge = (badges, label, tone = TONE_NEUTRAL) => {
        if (label && !badges.some((badge) => badge.label === label)) {
            badges.push({ label, tone });
        }
    };

    const validationBadgeLabel = (count) => `${count} check${count === 1 ? "" : "s"}`;

    const nodeConfigBadges = (node, context = {}) => {
        const badges = [];
        const issueCount = Number.isFinite(context.issueCount) ? context.issueCount : 0;
        if (issueCount > 0) {
            addBadge(badges, `${issueCount} issue${issueCount === 1 ? "" : "s"}`, TONE_ERROR);
        }
        const authored = node.originalStep?.[node.kind] || {};
        const referenceCount =
            Number(Boolean(authored.input_contract_ref)) +
            Number(Boolean(authored.output_contract_ref)) +
            Object.values(authored.input_contracts || {}).filter((attachment) => attachment?.contract_ref).length;
        if (referenceCount) addBadge(badges, `${referenceCount} ODCS`, TONE_NEUTRAL);
        if (node.kind === KIND_INPUT) addBadge(badges, "JSON event");

        if (node.kind === KIND_QUERY) {
            addBadge(badges, node.operationType || "sql");
            if (hasObjectEntries(node.queryIncremental)) {
                addBadge(badges, "checkpoint", TONE_SUCCESS);
            }
            if (node.useInlineSQL || node.useInlineOperation || hasObjectEntries(node.operationInline)) {
                addBadge(badges, "inline", TONE_WARNING);
            }
            const fields = contractFieldCount(node.queryOutputContract);
            const validations = contractValidationCount(node.queryOutputContract);
            if (validations > 0) {
                addBadge(badges, validationBadgeLabel(validations), TONE_SUCCESS);
            } else if (fields > 0) {
                addBadge(badges, `${fields} fields`);
            }
        }

        if (node.kind === KIND_TRANSFORM) {
            const inputCount = Number.isFinite(context.inputCount) ? context.inputCount : 0;
            if (inputCount > 0) {
                addBadge(badges, `${inputCount} input${inputCount === 1 ? "" : "s"}`, TONE_SUCCESS);
            }
            const outputCount = Number.isFinite(context.outputCount) ? context.outputCount : 0;
            if (outputCount > 1) {
                addBadge(badges, `fan-out ${outputCount}`);
            }
            const validations =
                contractValidationCount(node.transformInputContract) +
                contractValidationCount(node.transformOutputContract);
            if (validations > 0) {
                addBadge(badges, validationBadgeLabel(validations), TONE_SUCCESS);
            }
            if ((node.transformSQL || "").trim() !== "") {
                addBadge(badges, "sql");
            }
        }

        if (node.kind === KIND_SINK) {
            addBadge(badges, node.sinkOperationType || node.sinkBackend || "sink");
            if (node.sinkUseInlineOperation || hasObjectEntries(node.sinkOperationInline)) {
                addBadge(badges, "inline", TONE_WARNING);
            }
            const validations = contractValidationCount(node.sinkInputContract);
            if (validations > 0) {
                addBadge(badges, validationBadgeLabel(validations), TONE_SUCCESS);
            }
            if (node.sinkIdempotencyClass) {
                addBadge(badges, node.sinkIdempotencyClass.replace(/_/g, " "));
            }
            if (context.payloadTone === TONE_ERROR) {
                addBadge(badges, "payload mismatch", TONE_ERROR);
            } else if (context.payloadTone === TONE_SUCCESS) {
                addBadge(badges, "payload ok", TONE_SUCCESS);
            }
        }

        if (badges.length > 3) {
            return [...badges.slice(0, 2), { label: `+${badges.length - 2}`, tone: TONE_NEUTRAL }];
        }
        return badges;
    };

    const renderNodeBadges = (content, badges) => {
        const existing = content.querySelector(".builder-node-badges");
        if (existing instanceof HTMLElement) {
            existing.remove();
        }
        if (!Array.isArray(badges) || badges.length === 0) {
            return;
        }

        const list = document.createElement("div");
        list.className = "builder-node-badges";
        list.setAttribute("aria-hidden", "true");
        badges.forEach((badge) => {
            const item = document.createElement("span");
            item.className = "builder-node-badge";
            item.dataset.tone = badge.tone || TONE_NEUTRAL;
            item.textContent = badge.label;
            list.appendChild(item);
        });
        content.appendChild(list);
    };

    const normalizeDisclosureContext = (node, nodes, edges, bindings, issues, dataSourceMetadata) => {
        const normalizedBindings = normalizeBindings(bindings);
        const nodeBySlug = new Map(nodes.map((item) => [item.slug, item]));
        const incomingBindings = normalizedBindings.filter((binding) => binding.toNode === node.slug);
        const outgoingBindings = normalizedBindings.filter((binding) => binding.fromNode === node.slug);
        const incomingEdges = edges.filter((edge) => edge.to === node.slug);
        const outgoingEdges = edges.filter((edge) => edge.from === node.slug);
        const currentBinding =
            node.kind === KIND_SINK ? bindingForTarget(normalizedBindings, node.slug, "payload") : null;
        const payloadDiagnostic = payloadDiagnosticForBinding(node, currentBinding, nodeBySlug, dataSourceMetadata);

        return {
            incomingBindings,
            outgoingBindings,
            incomingEdges,
            outgoingEdges,
            issueCount: issues.filter((issue) => issue.nodeSlug === node.slug).length,
            inputCount: incomingBindings.length || incomingEdges.length,
            outputCount: outgoingEdges.length,
            payloadTone: payloadDiagnostic?.tone || "",
            payloadMessage: payloadDiagnostic?.message || "",
        };
    };

    const buildNodeDisclosureRows = (node, context = {}) => {
        const rows = [];
        const addRow = (label, value) => rows.push({ label, value: value || "not set" });

        if (node.kind === KIND_INPUT) {
            const ref = node.originalStep?.input?.output_contract_ref;
            addRow(
                "Output",
                ref
                    ? `${ref.contract_slug} / ${ref.revision_slug} / ${ref.schema_object_id}`
                    : contractSummary(node.originalStep?.input?.output_contract),
            );
        } else if (node.kind === KIND_QUERY) {
            const hasInline = node.useInlineSQL || node.useInlineOperation || hasObjectEntries(node.operationInline);
            addRow(
                "Source",
                `${node.dataSourceSlug || "-"} / ${
                    hasInline ? `inline ${(node.operationType || "operation").toLowerCase()}` : node.querySlug || "-"
                }`,
            );
            if (hasObjectEntries(node.queryIncremental)) {
                const incremental = node.queryIncremental;
                const mode = incremental.checkpoint_kind || "checkpoint";
                const field = incremental.field || incremental.cursor || "";
                const lookback = incremental.lookback_window ? `, lookback ${incremental.lookback_window}` : "";
                addRow("Checkpoint", `${mode}${field ? ` ${field}` : ""}${lookback}`);
            }
            addRow("Output", contractSummary(node.queryOutputContract));
            if (context.outgoingBindings?.length > 0) {
                addRow("Links", context.outgoingBindings.map((binding) => binding.toNode).join(", "));
            }
        } else if (node.kind === KIND_TRANSFORM) {
            if (context.incomingBindings?.length > 0) {
                addRow(
                    "Inputs",
                    context.incomingBindings.map((binding) => `${binding.toPort} <- ${binding.fromNode}`).join(", "),
                );
            }
            addRow("SQL", compactSQLLine(node.transformSQL));
            addRow(
                "Contracts",
                `input ${contractSummary(node.transformInputContract)}; output ${contractSummary(node.transformOutputContract)}`,
            );
            if (context.outgoingBindings?.length > 0) {
                addRow("Links", context.outgoingBindings.map((binding) => binding.toNode).join(", "));
            }
        } else if (node.kind === KIND_SINK) {
            const hasInline = node.sinkUseInlineOperation || hasObjectEntries(node.sinkOperationInline);
            addRow(
                "Target",
                `${node.sinkDataSourceSlug || node.sinkBackend || "-"} / ${
                    hasInline
                        ? `inline ${(node.sinkOperationType || "operation").toLowerCase()}`
                        : node.sinkQuerySlug || "-"
                }`,
            );
            const binding = context.incomingBindings?.find((item) => item.toPort === "payload");
            if (binding) {
                addRow("Input", binding.fromNode);
            }
            addRow("Contract", contractSummary(node.sinkInputContract));
            addRow("Idempotency", (node.sinkIdempotencyClass || "").replace(/_/g, " "));
            if (context.payloadMessage) {
                addRow("Payload", context.payloadMessage);
            }
        }

        if (context.issueCount > 0) {
            addRow("Diagnostics", `${context.issueCount} blocking issue${context.issueCount === 1 ? "" : "s"}`);
        }

        return rows;
    };

    const nodeDisclosureTitle = (node) => `${kindLabel(node.kind)}: ${node.slug || "node"}`;

    const renderNodeDisclosureContent = (node, context = {}) => {
        const rows = buildNodeDisclosureRows(node, context);
        const title = escapeHTML(nodeDisclosureTitle(node));
        const rowHTML = rows
            .map(
                (row) =>
                    `<dt>${escapeHTML(row.label)}</dt><dd title="${escapeHTML(row.value)}">${escapeHTML(row.value)}</dd>`,
            )
            .join("");
        return `<p class="mb-2 text-[11px] font-semibold uppercase tracking-wide text-base-content/60">${title}</p><dl>${rowHTML}</dl>`;
    };

    const { renderPayloadDiagnostic } = builderRendering;

    const {
        normalizeBindings,
        serializeBindingsForExport,
        bindingsForTarget,
        bindingForTarget,
        replaceBindingsForTarget,
        replaceBindingSlug,
        validateBindings,
    } = builderSerialization;

    const parseBindingData = (bindingsDataElement) => {
        if (!(bindingsDataElement instanceof HTMLElement)) {
            return [];
        }

        return normalizeBindings(
            Array.from(bindingsDataElement.querySelectorAll("[data-binding-from-node][data-binding-to-node]")).map(
                (binding) => ({
                    fromNode: binding.getAttribute("data-binding-from-node") || "",
                    fromPort: binding.getAttribute("data-binding-from-port") || "",
                    toNode: binding.getAttribute("data-binding-to-node") || "",
                    toPort: binding.getAttribute("data-binding-to-port") || "",
                }),
            ),
        );
    };

    const issueMessage = (issue) => {
        if (issue && typeof issue === "object" && typeof issue.message === "string") {
            return issue.message;
        }
        return typeof issue === "string" ? issue : "";
    };

    const transformInputNameFromSourceSlug = (sourceSlug) => {
        const normalized = (typeof sourceSlug === "string" ? sourceSlug : "")
            .trim()
            .toLowerCase()
            .replace(/[^a-z0-9_]+/g, "_")
            .replace(/^_+|_+$/g, "");
        if (normalized === "") {
            return "input_rows";
        }
        // Prefix generated aliases so source slugs such as fetch cannot become SQL keywords.
        return `input_${normalized}`;
    };

    const nextTransformInputNameForConnection = (sourceSlug, bindings, targetSlug) => {
        const baseName = transformInputNameFromSourceSlug(sourceSlug);
        const usedNames = new Set(
            normalizeBindings(bindings)
                .filter((binding) => binding.toNode === targetSlug)
                .map((binding) => binding.toPort)
                .filter((toPort) => toPort !== ""),
        );
        if (!usedNames.has(baseName)) {
            return baseName;
        }
        for (let suffix = 2; ; suffix += 1) {
            const candidate = `${baseName}_${suffix}`;
            if (!usedNames.has(candidate)) {
                return candidate;
            }
        }
    };

    const declaredTransformInputNameForConnection = (sourceSlug, targetNode, bindings = []) => {
        const targetSlug = typeof targetNode?.slug === "string" ? targetNode.slug : "";
        const inputs =
            targetNode?.transformInputs &&
            typeof targetNode.transformInputs === "object" &&
            !Array.isArray(targetNode.transformInputs)
                ? targetNode.transformInputs
                : {};
        if (targetSlug === "" || Object.keys(inputs).length === 0) {
            return "";
        }
        const usedNames = new Set(
            normalizeBindings(bindings)
                .filter((binding) => binding.toNode === targetSlug)
                .map((binding) => binding.toPort)
                .filter((toPort) => toPort !== ""),
        );
        const matches = Object.entries(inputs)
            .filter(([, input]) => stepInputSourceSlug(input) === sourceSlug)
            .map(([name]) => name)
            .filter((name) => !usedNames.has(name))
            .sort();

        return matches[0] || "";
    };

    const defaultBindingForConnection = (sourceNode, targetNode, bindings = []) => {
        if (!sourceNode || !targetNode || !outputPortsForKind(sourceNode.kind).includes(PORT_ROWS)) {
            return null;
        }
        if (targetNode.kind === KIND_TRANSFORM) {
            const declaredInputName = declaredTransformInputNameForConnection(sourceNode.slug, targetNode, bindings);
            return {
                fromNode: sourceNode.slug,
                fromPort: PORT_ROWS,
                toNode: targetNode.slug,
                toPort:
                    declaredInputName ||
                    nextTransformInputNameForConnection(sourceNode.slug, bindings, targetNode.slug),
            };
        }
        if (targetNode.kind === KIND_SINK) {
            return {
                fromNode: sourceNode.slug,
                fromPort: PORT_ROWS,
                toNode: targetNode.slug,
                toPort: "payload",
            };
        }
        return null;
    };

    const removeBindingsForConnections = (bindings, connections) => {
        const pairs = new Set(
            connections
                .filter((connection) => connection?.from && connection?.to)
                .map((connection) => `${connection.from}->${connection.to}`),
        );
        if (pairs.size === 0) {
            return normalizeBindings(bindings);
        }
        return normalizeBindings(bindings).filter((binding) => !pairs.has(`${binding.fromNode}->${binding.toNode}`));
    };

    const parseEdgeData = (edgesDataElement) => {
        if (!(edgesDataElement instanceof HTMLElement)) {
            return [];
        }

        return Array.from(edgesDataElement.querySelectorAll("[data-edge-from][data-edge-to]"))
            .map((edge) => ({
                from: edge.getAttribute(ATTR_EDGE_FROM) || "",
                to: edge.getAttribute(ATTR_EDGE_TO) || "",
            }))
            .filter((edge) => edge.from !== "" && edge.to !== "");
    };

    const writeEdgesData = (edgesDataElement, edges) => {
        clearChildren(edgesDataElement);
        edges.forEach((edge) => {
            const item = document.createElement("div");
            item.setAttribute(ATTR_EDGE_FROM, edge.from);
            item.setAttribute(ATTR_EDGE_TO, edge.to);
            edgesDataElement.appendChild(item);
        });
    };

    const readNodeDataFromElement = (item) => {
        const rawParams = item.getAttribute(ATTR_PARAMETERS) || "";
        const kind = item.getAttribute(ATTR_KIND) || "";
        const rawGridX = Number.parseInt(item.getAttribute("data-grid-x") || "0", 10);
        const rawGridY = Number.parseInt(item.getAttribute("data-grid-y") || "0", 10);
        const data = {
            slug: item.getAttribute(ATTR_NODE_SLUG) || "",
            kind,
            nodeType: item.getAttribute(ATTR_NODE_TYPE) || DEFAULT_NODE_TYPE,
            x: Number.isInteger(rawGridX) ? rawGridX : 0,
            y: Number.isInteger(rawGridY) ? rawGridY : 0,
            dataSourceSlug: item.getAttribute(ATTR_DATA_SOURCE_SLUG) || "",
            operationType: item.getAttribute(ATTR_OPERATION_TYPE) || "sql",
            operationInline: parseJSONMap(item.getAttribute(ATTR_OPERATION_INLINE) || ""),
            queryOutputContract: parseJSONMap(item.getAttribute(ATTR_QUERY_OUTPUT_CONTRACT) || ""),
            queryIncremental: parseJSONMap(item.getAttribute(ATTR_QUERY_INCREMENTAL) || ""),
            querySlug: item.getAttribute(ATTR_QUERY_SLUG) || "",
            useInlineSQL: item.getAttribute(ATTR_USE_INLINE_SQL) === "true",
            useInlineOperation: item.getAttribute(ATTR_USE_INLINE_OPERATION) === "true",
            inlineSQL: item.getAttribute(ATTR_INLINE_SQL) || "",
            stepSlug: item.getAttribute(ATTR_STEP_SLUG) || "",
            stepName: item.getAttribute(ATTR_STEP_NAME) || "",
            originalStep: parseJSONMap(item.getAttribute(ATTR_ORIGINAL_STEP) || ""),
            parameters: parseJSONMap(rawParams),
            sinkBackend: item.getAttribute(ATTR_SINK_BACKEND) || "",
            sinkDataSourceSlug: item.getAttribute(ATTR_SINK_DATA_SOURCE_SLUG) || "",
            sinkOperationType: item.getAttribute(ATTR_SINK_OPERATION_TYPE) || "",
            sinkOperationInline: parseJSONMap(item.getAttribute(ATTR_SINK_OPERATION_INLINE) || ""),
            sinkIdempotencyClass:
                kind === KIND_SINK ? normalizeSinkIdempotencyClass(item.getAttribute(ATTR_SINK_IDEMPOTENCY_CLASS)) : "",
            sinkInputContract: parseJSONMap(item.getAttribute(ATTR_SINK_INPUT_CONTRACT) || ""),
            sinkUseInlineOperation: item.getAttribute(ATTR_SINK_USE_INLINE_OPERATION) === "true",
            sinkQuerySlug: item.getAttribute(ATTR_SINK_QUERY_SLUG) || "",
        };

        if (kind === KIND_TRANSFORM) {
            data.transformOutputKind = item.getAttribute(ATTR_TRANSFORM_OUTPUT_KIND) || "";
            data.transformSQL = item.getAttribute(ATTR_TRANSFORM_SQL) || "";
            data.transformInputs = parseJSONMap(item.getAttribute(ATTR_TRANSFORM_INPUTS) || "");
            data.transformInputContract = parseJSONMap(item.getAttribute(ATTR_TRANSFORM_INPUT_CONTRACT) || "");
            data.transformOutputContract = parseJSONMap(item.getAttribute(ATTR_TRANSFORM_OUTPUT_CONTRACT) || "");
        }

        return data;
    };

    const syncPortsForNode = (item, kind) => {
        if (!(item instanceof HTMLElement)) {
            return;
        }

        const normalizedKind = kind || item.getAttribute(ATTR_KIND) || "";
        const wantsInputPort = normalizedKind === KIND_TRANSFORM || normalizedKind === KIND_SINK;
        const wantsOutputPort = isStepKind(normalizedKind);

        const existingInPort = item.querySelector('[data-port="in"]');
        const existingOutPort = item.querySelector('[data-port="out"]');

        if (wantsInputPort) {
            if (!(existingInPort instanceof HTMLElement)) {
                const inPort = document.createElement("div");
                inPort.className = "node-port node-port-in";
                inPort.setAttribute(ATTR_PORT, PORT_DIR_IN);
                inPort.title = "Input";
                inPort.textContent = "\u25c0";
                item.appendChild(inPort);
            }
        } else if (existingInPort instanceof HTMLElement) {
            existingInPort.remove();
        }

        if (wantsOutputPort) {
            if (!(existingOutPort instanceof HTMLElement)) {
                const outPort = document.createElement("div");
                outPort.className = "node-port node-port-out";
                outPort.setAttribute(ATTR_PORT, PORT_DIR_OUT);
                outPort.title = "Output";
                outPort.textContent = "\u25b6";
                item.appendChild(outPort);
            }
        } else if (existingOutPort instanceof HTMLElement) {
            existingOutPort.remove();
        }
    };

    const applyNodeDataToElement = (item, data) => {
        item.setAttribute(ATTR_NODE_SLUG, data.slug || "");
        item.setAttribute(ATTR_KIND, data.kind || "");
        item.setAttribute(ATTR_NODE_TYPE, data.nodeType || DEFAULT_NODE_TYPE);
        item.setAttribute(ATTR_DATA_SOURCE_SLUG, data.dataSourceSlug || "");
        item.setAttribute(ATTR_OPERATION_TYPE, data.operationType || "sql");
        item.setAttribute(ATTR_OPERATION_INLINE, formatJSONMap(data.operationInline));
        item.setAttribute(ATTR_QUERY_SLUG, data.querySlug || "");
        item.setAttribute(ATTR_USE_INLINE_SQL, data.useInlineSQL ? "true" : "false");
        item.setAttribute(ATTR_USE_INLINE_OPERATION, data.useInlineOperation ? "true" : "false");
        item.setAttribute(ATTR_INLINE_SQL, data.inlineSQL || "");
        item.setAttribute(ATTR_STEP_SLUG, data.stepSlug || "");
        item.setAttribute(ATTR_STEP_NAME, data.stepName || "");
        item.setAttribute(ATTR_ORIGINAL_STEP, formatJSONMap(data.originalStep));
        item.setAttribute(ATTR_QUERY_OUTPUT_CONTRACT, formatJSONMap(data.queryOutputContract));
        item.setAttribute(ATTR_QUERY_INCREMENTAL, formatJSONMap(data.queryIncremental));
        item.setAttribute(ATTR_TRANSFORM_OUTPUT_KIND, data.transformOutputKind || "");
        item.setAttribute(ATTR_TRANSFORM_SQL, data.transformSQL || "");
        item.setAttribute(ATTR_TRANSFORM_INPUTS, formatJSONMap(data.transformInputs));
        item.setAttribute(ATTR_TRANSFORM_INPUT_CONTRACT, formatJSONMap(data.transformInputContract));
        item.setAttribute(ATTR_TRANSFORM_OUTPUT_CONTRACT, formatJSONMap(data.transformOutputContract));
        item.setAttribute(ATTR_PARAMETERS, formatJSONMap(data.parameters));
        item.setAttribute(ATTR_SINK_BACKEND, data.sinkBackend || "");
        item.setAttribute(ATTR_SINK_DATA_SOURCE_SLUG, data.sinkDataSourceSlug || "");
        item.setAttribute(ATTR_SINK_OPERATION_TYPE, data.sinkOperationType || "");
        item.setAttribute(ATTR_SINK_OPERATION_INLINE, formatJSONMap(data.sinkOperationInline));
        item.setAttribute(
            ATTR_SINK_IDEMPOTENCY_CLASS,
            data.kind === KIND_SINK ? normalizeSinkIdempotencyClass(data.sinkIdempotencyClass) : "",
        );
        item.setAttribute(ATTR_SINK_INPUT_CONTRACT, formatJSONMap(data.sinkInputContract));
        item.setAttribute(ATTR_SINK_USE_INLINE_OPERATION, data.sinkUseInlineOperation ? "true" : "false");
        item.setAttribute(ATTR_SINK_QUERY_SLUG, data.sinkQuerySlug || "");
        item.tabIndex = 0;
        item.setAttribute("role", "button");
        item.setAttribute("aria-label", nodeDisclosureTitle(data));
        syncPortsForNode(item, data.kind || "");

        const content = item.querySelector(".builder-node-content");
        if (!(content instanceof HTMLElement)) {
            return;
        }
        content.className = nodeContentClassName;
        if (data.kind === KIND_TRANSFORM) {
            item.tabIndex = -1;
            item.setAttribute("role", "group");
            content.tabIndex = 0;
            content.setAttribute("role", "button");
            content.setAttribute("aria-label", nodeDisclosureTitle(data));
        }

        const title = kindLabel(data.kind);
        const subtitle = data.slug;
        const hasInlineOperation =
            data.operationInline &&
            typeof data.operationInline === "object" &&
            Object.keys(data.operationInline).length > 0;
        const hasSinkInlineOperation =
            data.sinkOperationInline &&
            typeof data.sinkOperationInline === "object" &&
            Object.keys(data.sinkOperationInline).length > 0;
        const queryDetails =
            data.useInlineSQL || ((data.operationType || "sql") !== "sql" && hasInlineOperation)
                ? `${data.dataSourceSlug || "-"} / inline ${(data.operationType || "operation").toLowerCase()}`
                : `${data.dataSourceSlug || "-"} / ${data.querySlug || "-"}`;
        const sinkDetails =
            data.sinkDataSourceSlug && (data.sinkUseInlineOperation || hasSinkInlineOperation || data.sinkQuerySlug)
                ? data.sinkUseInlineOperation || hasSinkInlineOperation
                    ? `${data.sinkDataSourceSlug || "-"} / inline ${(data.sinkOperationType || "operation").toLowerCase()}`
                    : `${data.sinkDataSourceSlug || "-"} / ${data.sinkQuerySlug || "-"}`
                : `${data.sinkBackend || "unknown"}`;
        const details =
            data.kind === KIND_INPUT
                ? "JSON event payload"
                : data.kind === KIND_QUERY
                  ? queryDetails
                  : data.kind === KIND_TRANSFORM
                    ? `${data.stepSlug || "-"}${data.stepName ? ` (${data.stepName})` : ""}`
                    : data.kind === KIND_SINK
                      ? sinkDetails
                      : data.nodeType || DEFAULT_NODE_TYPE;

        clearChildren(content);
        const titleElement = document.createElement("p");
        titleElement.className = "text-[10px] font-semibold uppercase tracking-wide text-base-content/50";
        titleElement.textContent = title;
        const subtitleElement = document.createElement("p");
        subtitleElement.className = "mt-2 truncate text-sm font-semibold text-base-content";
        subtitleElement.textContent = subtitle;
        const detailsElement = document.createElement("p");
        detailsElement.className = "mt-0.5 truncate text-[11px] text-base-content/70";
        detailsElement.textContent = details;
        content.append(titleElement, subtitleElement, detailsElement);
        renderNodeBadges(content, nodeConfigBadges(data));
    };

    const readNodes = (gridElement) => {
        const items = Array.from(gridElement.querySelectorAll(".builder-node[data-node-slug]"));
        return items.map((item) => readNodeDataFromElement(item)).filter((node) => node.slug !== "");
    };

    const saveState = (storageKey, gridElement, edgesDataElement, bindings, pipelineMeta) => {
        if (!storageKey) return;
        try {
            const normalizedSourceFormat = normalizePipelineFormat(pipelineMeta?.sourceFormat, FORMAT_YAML);
            const normalizedExportFormat = normalizePipelineFormat(pipelineMeta?.exportFormat, normalizedSourceFormat);
            const { pipeline_spec, graph } = buildBuilderStatePayloadFromLiveNodes(
                readNodes(gridElement),
                parseEdgeData(edgesDataElement),
                bindings,
                pipelineMeta,
            );
            const state = {
                pipeline_spec,
                graph,
                context: {
                    expected_spec_hash: null,
                    current_version_spec_hash: null,
                    draft_based_on_spec_hash: null,
                    source_format: normalizedSourceFormat,
                    export_format: normalizedExportFormat,
                },
            };
            window.localStorage.setItem(storageKey, JSON.stringify(state));
        } catch (_) {
            // Ignore storage failures (private mode, quota, etc.).
        }
    };

    const loadState = (storageKey) => {
        if (!storageKey) return null;
        try {
            const raw = window.localStorage.getItem(storageKey);
            if (!raw) {
                return null;
            }
            const parsed = JSON.parse(raw);
            const normalized = normalizeImportedBuilderState(parsed);
            if (!normalized) {
                return null;
            }
            return normalizeStateForGridColumns(normalized);
        } catch (_) {
            return null;
        }
    };

    const normalizeImportedBuilderState = (rawState) => {
        if (!rawState || typeof rawState !== "object") {
            return null;
        }

        const isSplitState =
            rawState.pipeline_spec &&
            typeof rawState.pipeline_spec === "object" &&
            rawState.graph &&
            typeof rawState.graph === "object";

        const context =
            isSplitState && rawState.context && typeof rawState.context === "object" ? rawState.context : {};
        const pipeline = isSplitState
            ? rawState.pipeline_spec && typeof rawState.pipeline_spec === "object"
                ? rawState.pipeline_spec
                : {}
            : rawState.pipeline && typeof rawState.pipeline === "object"
              ? rawState.pipeline
              : {};
        const graph =
            isSplitState && rawState.graph && typeof rawState.graph === "object"
                ? rawState.graph
                : { nodes: rawState.nodes, edges: rawState.edges, bindings: rawState.bindings };
        const metadata =
            pipeline.metadata && typeof pipeline.metadata === "object" && !Array.isArray(pipeline.metadata)
                ? pipeline.metadata
                : {};
        const sourceFormat = normalizePipelineFormat(
            context.source_format || context.sourceFormat || pipeline.source_format || pipeline.sourceFormat,
            FORMAT_YAML,
        );
        const exportFormat = normalizePipelineFormat(
            context.export_format || context.exportFormat || pipeline.export_format || pipeline.exportFormat,
            sourceFormat,
        );
        const stepSpecs = Array.isArray(pipeline.steps) ? pipeline.steps : [];
        const stepBySlug = new Map();
        stepSpecs.forEach((step) => {
            const stepSlug = typeof step?.slug === "string" ? step.slug.trim() : "";
            if (stepSlug !== "") {
                stepBySlug.set(stepSlug, step);
            }
        });

        const normalizedNodes = (Array.isArray(graph.nodes) ? graph.nodes : [])
            .map((node) => {
                const stepSlug =
                    typeof node?.step_slug === "string"
                        ? node.step_slug
                        : typeof node?.stepSlug === "string"
                          ? node.stepSlug
                          : typeof node?.slug === "string"
                            ? node.slug
                            : "";
                const step = stepBySlug.get(stepSlug) || {};
                const query = step?.query && typeof step.query === "object" ? step.query : {};
                const transform = step?.transform && typeof step.transform === "object" ? step.transform : {};
                const sink = step?.sink && typeof step.sink === "object" ? step.sink : {};
                const queryInline =
                    query.operation_inline &&
                    typeof query.operation_inline === "object" &&
                    !Array.isArray(query.operation_inline)
                        ? query.operation_inline
                        : {};
                const sinkInline =
                    sink.operation_inline &&
                    typeof sink.operation_inline === "object" &&
                    !Array.isArray(sink.operation_inline)
                        ? sink.operation_inline
                        : {};
                const inlineSQL = typeof queryInline.sql === "string" ? queryInline.sql : "";
                const kind =
                    typeof step?.kind === "string"
                        ? step.kind
                        : typeof node?.kind === "string"
                          ? node.kind
                          : KIND_QUERY;

                return {
                    slug: stepSlug,
                    kind,
                    nodeType: DEFAULT_NODE_TYPE,
                    dataSourceSlug:
                        typeof query?.data_source_ref === "string"
                            ? query.data_source_ref
                            : typeof query?.data_source_slug === "string"
                              ? query.data_source_slug
                              : "",
                    operationType:
                        typeof query?.operation_type === "string" && query.operation_type.trim() !== ""
                            ? query.operation_type
                            : "sql",
                    operationInline: queryInline,
                    queryOutputContract:
                        query.output_contract &&
                        typeof query.output_contract === "object" &&
                        !Array.isArray(query.output_contract)
                            ? query.output_contract
                            : {},
                    queryIncremental:
                        query.incremental && typeof query.incremental === "object" && !Array.isArray(query.incremental)
                            ? query.incremental
                            : {},
                    querySlug: typeof query?.operation_ref === "string" ? query.operation_ref : "",
                    useInlineSQL: inlineSQL.trim() !== "",
                    useInlineOperation: Object.keys(queryInline).length > 0 && inlineSQL.trim() === "",
                    inlineSQL,
                    stepSlug,
                    stepName: typeof step?.name === "string" ? step.name : "",
                    originalStep: cloneJSONValue(step),
                    parameters:
                        query?.parameters && typeof query.parameters === "object" && !Array.isArray(query.parameters)
                            ? query.parameters
                            : {},
                    transformOutputKind: typeof transform?.output_kind === "string" ? transform.output_kind : "",
                    transformSQL: typeof transform?.sql === "string" ? transform.sql : "",
                    transformInputs:
                        transform.inputs && typeof transform.inputs === "object" && !Array.isArray(transform.inputs)
                            ? transform.inputs
                            : {},
                    transformInputContract:
                        transform.input_contract &&
                        typeof transform.input_contract === "object" &&
                        !Array.isArray(transform.input_contract)
                            ? transform.input_contract
                            : {},
                    transformOutputContract:
                        transform.output_contract &&
                        typeof transform.output_contract === "object" &&
                        !Array.isArray(transform.output_contract)
                            ? transform.output_contract
                            : {},
                    sinkBackend:
                        typeof sink?.operation_type === "string"
                            ? sink.operation_type
                            : typeof sink?.backend === "string"
                              ? sink.backend
                              : "",
                    sinkDataSourceSlug:
                        typeof sink?.data_source_ref === "string"
                            ? sink.data_source_ref
                            : typeof sink?.data_source_slug === "string"
                              ? sink.data_source_slug
                              : "",
                    sinkOperationType: typeof sink?.operation_type === "string" ? sink.operation_type : "",
                    sinkOperationInline: sinkInline,
                    sinkIdempotencyClass:
                        kind === KIND_SINK ? normalizeSinkIdempotencyClass(sink?.idempotency_class) : "",
                    sinkInputContract:
                        sink?.input_contract &&
                        typeof sink.input_contract === "object" &&
                        !Array.isArray(sink.input_contract)
                            ? sink.input_contract
                            : {},
                    sinkUseInlineOperation:
                        Object.hasOwn(sink, "operation_inline") || Object.keys(sinkInline).length > 0,
                    sinkQuerySlug: typeof sink?.operation_ref === "string" ? sink.operation_ref : "",
                };
            })
            .filter((node) => node.slug !== "");

        const normalizedEdges = (Array.isArray(graph.edges) ? graph.edges : [])
            .map((edge) => ({
                from:
                    typeof edge?.from === "string"
                        ? edge.from
                        : typeof edge?.from_step === "string"
                          ? edge.from_step
                          : "",
                to: typeof edge?.to === "string" ? edge.to : typeof edge?.to_step === "string" ? edge.to_step : "",
            }))
            .filter((edge) => edge.from !== "" && edge.to !== "");

        return {
            gridColumns: gridColumnCount,
            pipeline: {
                slug:
                    typeof metadata.slug === "string"
                        ? metadata.slug
                        : typeof pipeline.slug === "string"
                          ? pipeline.slug
                          : "",
                name:
                    typeof metadata.name === "string"
                        ? metadata.name
                        : typeof pipeline.name === "string"
                          ? pipeline.name
                          : "",
                source_format: sourceFormat,
                export_format: exportFormat,
            },
            nodes: normalizedNodes,
            edges: normalizedEdges,
            bindings: normalizeBindings(graph.bindings),
        };
    };

    const hasPath = (edges, start, target) => {
        const visited = new Set();
        const stack = [start];
        while (stack.length > 0) {
            const current = stack.pop();
            if (current === target) {
                return true;
            }
            if (visited.has(current)) {
                continue;
            }
            visited.add(current);
            edges.forEach((edge) => {
                if (edge.from === current && !visited.has(edge.to)) {
                    stack.push(edge.to);
                }
            });
        }
        return false;
    };

    const validateEdgeConnection = (nodes, edges, fromSlug, toSlug) => {
        if (!fromSlug || !toSlug) {
            return "Connection is incomplete.";
        }
        if (fromSlug === toSlug) {
            return "Cannot connect a node to itself.";
        }

        const fromNode = nodes.find((node) => node.slug === fromSlug) || null;
        const toNode = nodes.find((node) => node.slug === toSlug) || null;
        if (!fromNode || !toNode) {
            return "Cannot connect nodes that are no longer on the canvas.";
        }
        if (fromNode.kind === KIND_SINK) {
            return "Sink nodes cannot have outgoing dependencies.";
        }
        if (toNode.kind === KIND_INPUT) {
            return "Event inputs cannot have incoming dependencies.";
        }
        if (toNode.kind === KIND_QUERY) {
            return "Query steps cannot have incoming dependencies.";
        }
        if (toNode.kind === KIND_SINK && edges.some((edge) => edge.to === toSlug && edge.from !== fromSlug)) {
            return "Nodes can only have one incoming dependency.";
        }
        if (edges.some((edge) => edge.from === fromSlug && edge.to === toSlug)) {
            return "Connection already exists.";
        }
        if (hasPath(edges, toSlug, fromSlug)) {
            return "Cannot connect nodes: this would create a cycle.";
        }
        return "";
    };

    // Returns the CSS layout offset of element relative to ancestor, traversing offsetParent chain.
    // Layout coordinates are stable during CSS transforms and scrolling, making them safe to use
    // directly as SVG viewBox coordinates.
    const layoutOffsetFrom = (element, ancestor) => {
        let x = 0;
        let y = 0;
        let el = element;
        while (el && el !== ancestor) {
            x += el.offsetLeft || 0;
            y += el.offsetTop || 0;
            el = el.offsetParent;
        }
        return { x, y, w: element.offsetWidth || 0, h: element.offsetHeight || 0 };
    };

    const builderCanvasViewBox = (rects, viewport, requestedScale) => {
        if (rects.length === 0) {
            return {
                width: viewport.width,
                height: viewport.height,
                fitScale: 1,
                scale: 1,
                offsetX: 0,
                offsetY: 0,
                boundsWidth: viewport.width,
                boundsHeight: viewport.height,
            };
        }
        const width = Math.max(320, ...rects.map((rect) => rect.x + rect.w + 40));
        const height = Math.max(240, ...rects.map((rect) => rect.y + rect.h + 40));
        const fitScale = Math.min(1, viewport.width / width, viewport.height / height);
        const scale = requestedScale ?? fitScale;
        return {
            width,
            height,
            fitScale,
            scale,
            offsetX: Math.max(0, (viewport.width - width * scale) / 2),
            offsetY: Math.max(0, (viewport.height - height * scale) / 2),
            boundsWidth: Math.max(viewport.width, width * scale),
            boundsHeight: Math.max(viewport.height, height * scale),
        };
    };

    // Same-column dependencies travel through the row gap instead of crossing both cards.
    const builderEdgePath = (from, to) => {
        const x1 = from.x + from.w;
        const y1 = from.y + from.h / 2;
        const x2 = to.x;
        const y2 = to.y + to.h / 2;
        if (x2 <= x1) {
            const gapY =
                to.y > from.y
                    ? ((from.attachmentBottom ?? from.y + from.h) + to.y) / 2
                    : ((to.attachmentBottom ?? to.y + to.h) + from.y) / 2;
            return `M ${x1} ${y1} H ${x1 + 20} V ${gapY} H ${x2 - 20} V ${y2} H ${x2}`;
        }
        const bend = (x2 - x1) / 2;
        return `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`;
    };

    const drawEdges = (rootElement, edgesLayerElement, edgesDataElement, selectedEdge, _scale, diagnostics = []) => {
        if (!(rootElement instanceof HTMLElement) || !(edgesLayerElement instanceof SVGSVGElement)) {
            return;
        }

        const focusedPath = edgesLayerElement.contains(document.activeElement) ? document.activeElement : null;
        const focusedFrom = focusedPath?.getAttribute(ATTR_EDGE_FROM);
        const focusedTo = focusedPath?.getAttribute(ATTR_EDGE_TO);
        clearChildren(edgesLayerElement);
        // Use client dimensions only; scrollWidth can exceed the SVG's rendered size when content
        // extends beyond the current visible area, which causes the viewBox coordinate system to
        // compress and ALL edges to shift.
        const width = Math.ceil(rootElement.clientWidth || 0);
        const height = Math.ceil(rootElement.clientHeight || 0);
        edgesLayerElement.setAttribute("width", String(width));
        edgesLayerElement.setAttribute("height", String(height));
        edgesLayerElement.setAttribute("viewBox", `0 0 ${width} ${height}`);

        const edges = parseEdgeData(edgesDataElement);
        edges.forEach((edge) => {
            const fromElement = rootElement.querySelector(`.builder-node[data-node-slug="${edge.from}"]`);
            const toElement = rootElement.querySelector(`.builder-node[data-node-slug="${edge.to}"]`);
            if (!(fromElement instanceof HTMLElement) || !(toElement instanceof HTMLElement)) {
                return;
            }

            // Use CSS layout offsets relative to the surface element. Unlike getBoundingClientRect(),
            // offsetLeft/offsetTop are not affected by the stage's scroll position, viewport changes,
            // or CSS transforms applied to the surface, so edges stay anchored during zoom and scrolling.
            const from = layoutOffsetFrom(fromElement, rootElement);
            const to = layoutOffsetFrom(toElement, rootElement);
            for (const [item, rect] of [
                [fromElement, from],
                [toElement, to],
            ]) {
                const attachment = item.querySelector(".builder-mapping-attachment");
                if (attachment) rect.attachmentBottom = rect.y + rect.h + 12 + attachment.offsetHeight;
            }
            const path = builderEdgePath(from, to);
            const line = document.createElementNS("http://www.w3.org/2000/svg", "path");
            const hitLine = document.createElementNS("http://www.w3.org/2000/svg", "path");
            const isSelected = selectedEdge && selectedEdge.from === edge.from && selectedEdge.to === edge.to;
            const errors = diagnostics.filter((entry) => entry.edge?.from === edge.from && entry.edge?.to === edge.to);
            hitLine.setAttribute("d", path);
            hitLine.setAttribute("fill", "none");
            hitLine.setAttribute("stroke", "rgba(0,0,0,0)");
            hitLine.setAttribute("stroke-width", "16");
            hitLine.setAttribute("role", "button");
            hitLine.setAttribute("tabindex", "0");
            hitLine.setAttribute("aria-label", `Inspect connection ${edge.from} to ${edge.to}`);
            hitLine.setAttribute("aria-pressed", String(Boolean(isSelected)));
            hitLine.setAttribute("stroke-linecap", "round");
            hitLine.setAttribute(ATTR_EDGE_FROM, edge.from);
            hitLine.setAttribute(ATTR_EDGE_TO, edge.to);
            hitLine.setAttribute("style", "pointer-events: stroke; cursor: pointer;");

            line.setAttribute("d", path);
            line.setAttribute("fill", "none");
            line.setAttribute("stroke-linejoin", "round");
            line.setAttribute(
                "stroke",
                errors.length ? "var(--color-error)" : isSelected ? "var(--color-info)" : "var(--color-primary)",
            );
            hitLine.setAttribute("data-validation-error", String(errors.length > 0));
            if (errors.length) {
                const title = document.createElementNS("http://www.w3.org/2000/svg", "title");
                title.textContent = errors.map((entry) => entry.message).join("\n");
                hitLine.appendChild(title);
                hitLine.setAttribute("aria-label", `Inspect incompatible connection ${edge.from} to ${edge.to}`);
            }
            line.setAttribute("stroke-width", "2");
            line.setAttribute("stroke-linecap", "round");
            line.setAttribute("style", "pointer-events: none;");
            edgesLayerElement.appendChild(hitLine);
            edgesLayerElement.appendChild(line);
            if (focusedFrom === edge.from && focusedTo === edge.to) hitLine.focus({ preventScroll: true });
        });
    };

    const { scheduleRenderFactory } = builderInteractions;

    const applySelection = (gridElement, selectedSlug) => {
        Array.from(gridElement.querySelectorAll(".builder-node[data-node-slug]")).forEach((item) => {
            if (!(item instanceof HTMLElement)) {
                return;
            }
            const slug = item.getAttribute(ATTR_NODE_SLUG) || "";
            const content = item.querySelector(".builder-node-content");
            if (!(content instanceof HTMLElement)) {
                return;
            }

            content.classList.toggle("node-card-selected", slug === selectedSlug);
        });
    };

    const removeConnectedEdges = (edgesDataElement, slug) => {
        const edges = parseEdgeData(edgesDataElement).filter((edge) => edge.from !== slug && edge.to !== slug);
        writeEdgesData(edgesDataElement, edges);
    };

    const restoreNodeLayout = (grid, gridElement, savedState) => {
        if (!savedState || !Array.isArray(savedState.nodes)) {
            return;
        }

        // Deterministic column X per kind — mirrors Go builder_layout.go constants:
        // firstColX=2, levelGapX=7 → query level 0 (X=2), transform level 1 (X=9), sink level 2 (X=16).
        const kindToX = { [KIND_QUERY]: 2, [KIND_TRANSFORM]: 9, [KIND_SINK]: 16 };
        // Track next Y per kind column: firstRowY=1, rowGapY=3.
        const nextYByKind = new Map();
        const firstRowY = 1;
        const rowGapY = 3;

        savedState.nodes.forEach((savedNode) => {
            if (!savedNode || typeof savedNode.slug !== "string") {
                return;
            }

            const kind = savedNode.kind || KIND_QUERY;
            const x = kindToX[kind] ?? 2;
            const y = nextYByKind.has(kind) ? nextYByKind.get(kind) : firstRowY;
            nextYByKind.set(kind, y + rowGapY);
            let item = gridElement.querySelector(`.builder-node[data-node-slug="${savedNode.slug}"]`);
            if (!(item instanceof HTMLElement)) {
                const created = grid.addWidget({
                    x,
                    y,
                    w: nodeWidth,
                    h: nodeHeight,
                    content:
                        '<div class="builder-node-content rounded-lg border border-primary/30 bg-base-100 px-3 py-2 shadow-sm"></div>',
                });
                if (!(created instanceof HTMLElement)) {
                    return;
                }
                item = created;
            }

            item.setAttribute(ATTR_KIND, savedNode.kind || KIND_QUERY);
            item.setAttribute(ATTR_NODE_SLUG, savedNode.slug);
            item.setAttribute(ATTR_NODE_TYPE, savedNode.nodeType || DEFAULT_NODE_TYPE);
            item.setAttribute(ATTR_DATA_SOURCE_SLUG, savedNode.dataSourceSlug || "");
            item.setAttribute(ATTR_QUERY_SLUG, savedNode.querySlug || "");
            item.setAttribute(ATTR_STEP_SLUG, savedNode.stepSlug || "");
            item.setAttribute(ATTR_STEP_NAME, savedNode.stepName || "");
            item.setAttribute(ATTR_PARAMETERS, formatJSONMap(savedNode.parameters));

            if (!(item.querySelector('[data-port="in"]') instanceof HTMLElement)) {
                const inPort = document.createElement("div");
                inPort.className = "node-port node-port-in";
                inPort.setAttribute(ATTR_PORT, PORT_DIR_IN);
                inPort.title = "Input";
                inPort.textContent = "\u25c0";
                item.appendChild(inPort);
            }

            if (!(item.querySelector('[data-port="out"]') instanceof HTMLElement)) {
                const outPort = document.createElement("div");
                outPort.className = "node-port node-port-out";
                outPort.setAttribute(ATTR_PORT, PORT_DIR_OUT);
                outPort.title = "Output";
                outPort.textContent = "\u25b6";
                item.appendChild(outPort);
            }

            grid.update(item, { x, y });
            applyNodeDataToElement(item, savedNode);
        });
    };

    const normalizeNodeForExport = (node) => {
        const output = {
            slug: node.slug,
            kind: node.kind,
        };
        if (hasObjectEntries(node.originalStep)) {
            output.original_step = node.originalStep;
        }
        if (node.kind === KIND_QUERY) {
            output.data_source_slug = node.dataSourceSlug || "";
            output.operation_type = node.operationType || "sql";
            output.query_slug = node.querySlug || "";
            output.operation_inline =
                node.operationInline && typeof node.operationInline === "object" && !Array.isArray(node.operationInline)
                    ? node.operationInline
                    : {};
            output.use_inline_sql = Boolean(node.useInlineSQL);
            output.use_inline_operation = Boolean(node.useInlineOperation);
            output.inline_sql = node.inlineSQL || "";
            if (hasObjectEntries(node.queryOutputContract)) {
                output.query_output_contract = node.queryOutputContract;
            }
            if (hasObjectEntries(node.queryIncremental)) {
                output.query_incremental = node.queryIncremental;
            }
        }
        if (isStepKind(node.kind) || node.kind === KIND_SINK) {
            output.step_slug = node.stepSlug || "";
            output.step_name = node.stepName || "";
        }
        if (isStepKind(node.kind)) {
            output.parameters = node.parameters || {};
        }
        if (node.kind === KIND_TRANSFORM) {
            if (typeof node.transformSQL === "string" && node.transformSQL.trim() !== "") {
                output.transform_sql = node.transformSQL;
            }
            const transformOutputKind =
                typeof node.transformOutputKind === "string" ? node.transformOutputKind.trim() : "";
            if (transformOutputKind !== "") {
                output.transform_output_kind = transformOutputKind;
            }
            if (hasObjectEntries(node.transformInputContract)) {
                output.transform_input_contract = node.transformInputContract;
            }
            if (hasObjectEntries(node.transformOutputContract)) {
                output.transform_output_contract = node.transformOutputContract;
            }
        }
        if (node.kind === KIND_SINK) {
            output.sink_backend = node.sinkBackend || "";
            output.sink_data_source_slug = node.sinkDataSourceSlug || "";
            output.sink_operation_type = node.sinkOperationType || "";
            output.sink_operation_inline =
                node.sinkOperationInline &&
                typeof node.sinkOperationInline === "object" &&
                !Array.isArray(node.sinkOperationInline)
                    ? node.sinkOperationInline
                    : {};
            output.sink_idempotency_class = normalizeSinkIdempotencyClass(node.sinkIdempotencyClass);
            if (
                node.sinkInputContract &&
                typeof node.sinkInputContract === "object" &&
                !Array.isArray(node.sinkInputContract) &&
                Object.keys(node.sinkInputContract).length > 0
            ) {
                output.sink_input_contract = node.sinkInputContract;
            }
            output.sink_use_inline_operation = Boolean(node.sinkUseInlineOperation);
            output.sink_query_slug = node.sinkQuerySlug || "";
        }
        return output;
    };

    const buildBuilderStepInputMaps = (nodes, edges, bindings) => {
        const nodeByStep = new Map();
        nodes.forEach((node) => {
            if (typeof node?.step_slug === "string" && node.step_slug.trim() !== "") {
                nodeByStep.set(node.step_slug, node);
            }
        });

        const inputNameForSource = (sourceStep, usedNames) => {
            const base =
                String(sourceStep || "input")
                    .trim()
                    .toLowerCase()
                    .replace(/[^a-z0-9_]+/g, "_")
                    .replace(/^_+|_+$/g, "") || "input";
            let candidate = base;
            for (let index = 2; usedNames.has(candidate); index += 1) {
                candidate = `${base}_${index}`;
            }
            usedNames.add(candidate);
            return candidate;
        };

        const edgeSet = new Set();
        edges.forEach((edge) => {
            const fromStep = typeof edge?.from === "string" ? edge.from : "";
            const toStep = typeof edge?.to === "string" ? edge.to : "";
            if (fromStep !== "" && toStep !== "") {
                edgeSet.add(`${fromStep}->${toStep}`);
            }
        });

        const transformInputsByStep = new Map();
        const sinkInputByStep = new Map();

        if (Array.isArray(bindings) && bindings.length > 0) {
            bindings.forEach((binding) => {
                const fromStep = typeof binding?.fromNode === "string" ? binding.fromNode : "";
                const toStep = typeof binding?.toNode === "string" ? binding.toNode : "";
                const fromPort = typeof binding?.fromPort === "string" ? binding.fromPort : "";
                const toPort = typeof binding?.toPort === "string" ? binding.toPort : "";
                const toNode = nodeByStep.get(toStep);
                const fromNode = nodeByStep.get(fromStep);
                if (!toNode || !fromNode || !edgeSet.has(`${fromStep}->${toStep}`)) {
                    return;
                }

                const fromIsStep = isStepKind(fromNode.kind);
                if (!fromIsStep) {
                    return;
                }

                if (toNode.kind === KIND_TRANSFORM && fromPort === "rows" && toPort !== "") {
                    const currentInputs = transformInputsByStep.get(toStep) || {};
                    currentInputs[toPort] = fromStep;
                    transformInputsByStep.set(toStep, currentInputs);
                    return;
                }

                if (toNode.kind === KIND_SINK && fromPort === "rows" && toPort === "payload") {
                    sinkInputByStep.set(toStep, fromStep);
                }
            });
        } else {
            const usedInputNamesByStep = new Map();
            edges.forEach((edge) => {
                const fromStep = typeof edge?.from === "string" ? edge.from : "";
                const toStep = typeof edge?.to === "string" ? edge.to : "";
                const toNode = nodeByStep.get(toStep);
                const fromNode = nodeByStep.get(fromStep);
                if (!toNode || !fromNode) {
                    return;
                }

                const fromIsStep = isStepKind(fromNode.kind);
                if (!fromIsStep) {
                    return;
                }

                if (toNode.kind === KIND_TRANSFORM) {
                    const currentInputs = transformInputsByStep.get(toStep) || {};
                    const usedNames = usedInputNamesByStep.get(toStep) || new Set(Object.keys(currentInputs));
                    const inputName = inputNameForSource(fromStep, usedNames);
                    usedInputNamesByStep.set(toStep, usedNames);
                    currentInputs[inputName] = fromStep;
                    transformInputsByStep.set(toStep, currentInputs);
                    return;
                }

                if (toNode.kind === KIND_SINK) {
                    sinkInputByStep.set(toStep, fromStep);
                }
            });
        }

        return { transformInputsByStep, sinkInputByStep };
    };

    const odcsPortFields = {
        "input.output": ["input", "output", "", "", ""],
        "query.output": [
            "query",
            "output",
            "queryOutputContract",
            "builder-query-output-contract-editor",
            "builder-field-query-output-contract",
        ],
        "transform.input": [
            "transform",
            "input",
            "transformInputContract",
            "builder-transform-input-contract-editor",
            "builder-field-transform-input-contract",
        ],
        "transform.output": [
            "transform",
            "output",
            "transformOutputContract",
            "builder-transform-output-contract-editor",
            "builder-field-transform-output-contract",
        ],
        "sink.input": [
            "sink",
            "input",
            "sinkInputContract",
            "builder-sink-input-contract-editor",
            "builder-field-sink-input-contract",
        ],
    };

    const renameTransformInputAgreement = (node, from, to) => {
        const attachments = node.originalStep?.transform?.input_contracts;
        if (from === to || !attachments?.[from]) return true;
        if (!transformInputNamePattern.test(to) || Object.hasOwn(attachments, to)) return false;
        attachments[to] = attachments[from];
        delete attachments[from];
        return true;
    };

    const namedODCSInput = (port) => (port.startsWith("transform.input.") ? port.slice("transform.input.".length) : "");

    const odcsPortReference = (node, port) => {
        const name = namedODCSInput(port);
        if (name) return node.originalStep?.transform?.input_contracts?.[name]?.contract_ref;
        const fields = odcsPortFields[port];
        return fields && node.originalStep?.[fields[0]]?.[`${fields[1]}_contract_ref`];
    };

    const odcsPortInlineContract = (node, port) => {
        const name = namedODCSInput(port);
        if (name) return node.originalStep?.transform?.input_contracts?.[name]?.contract;
        return port === "input.output" ? node.originalStep?.input?.output_contract : undefined;
    };

    const edgePreviewAgreements = (source, target, bindings) => {
        const readAgreement = (node, port, label) => {
            const ref = odcsPortReference(node, port);
            const contract = ref ? null : odcsPortInlineContract(node, port) || node[odcsPortFields[port]?.[2]];
            return { label, ref: ref || null, contract: contract || null };
        };
        const connectionBindings = normalizeBindings(bindings).filter(
            (binding) => binding.fromNode === source.slug && binding.toNode === target.slug,
        );
        const agreements = [readAgreement(source, `${source.kind}.output`, "Producer output")];
        if (target.kind === KIND_SINK) {
            agreements.push(readAgreement(target, "sink.input", "Destination requirement"));
        } else if (target.kind === KIND_TRANSFORM) {
            const inputCount = normalizeBindings(bindings).filter((binding) => binding.toNode === target.slug).length;
            connectionBindings.forEach((binding) => {
                let agreement = readAgreement(target, `transform.input.${binding.toPort}`, `Input: ${binding.toPort}`);
                if (!agreement.ref && !agreement.contract) {
                    agreement = readAgreement(target, "transform.input", agreement.label);
                    if (inputCount > 1 && (agreement.ref || agreement.contract))
                        agreement.fieldPrefix = `${binding.toPort}.`;
                }
                agreements.push(agreement);
            });
        }
        return { bindings: connectionBindings, agreements };
    };

    const validateBoundaryContractDeclarations = (nodes, bindings) => {
        const issues = [];
        const nodeBySlug = new Map(nodes.map((node) => [node.slug, node]));
        normalizeBindings(bindings).forEach((binding) => {
            const source = nodeBySlug.get(binding.fromNode);
            const target = nodeBySlug.get(binding.toNode);
            if (!source || !target || !isStepKind(source.kind) || ![KIND_TRANSFORM, KIND_SINK].includes(target.kind))
                return;
            const { agreements } = edgePreviewAgreements(source, target, [binding]);
            if (agreements.some(({ ref, contract }) => hasObjectEntries(ref) || hasObjectEntries(contract))) return;
            issues.push({
                message: `${target.slug}: declare an output contract on ${source.slug} or an input contract for ${binding.toPort}`,
                nodeSlug: target.slug,
            });
        });
        return issues;
    };

    const setODCSPortInlineContract = (node, port, contract) => {
        if (port !== "input.output" && !namedODCSInput(port)) return false;
        const name = namedODCSInput(port);
        if (
            name &&
            (hasObjectEntries(node.transformInputContract) ||
                node.originalStep?.transform?.input_contract ||
                node.originalStep?.transform?.input_contract_ref)
        )
            return false;
        if (!setODCSPortRef(node, port, null)) return false;
        if (name) {
            const transform = node.originalStep.transform;
            if (hasObjectEntries(contract)) {
                transform.input_contracts ||= {};
                transform.input_contracts[name] = { contract: cloneJSONValue(contract) };
            } else if (transform.input_contracts) {
                delete transform.input_contracts[name];
                if (!Object.keys(transform.input_contracts).length) delete transform.input_contracts;
            }
        } else if (hasObjectEntries(contract)) {
            node.originalStep.input.output_contract = cloneJSONValue(contract);
        } else {
            delete node.originalStep.input.output_contract;
        }
        return true;
    };

    const setODCSPortRef = (node, port, reference, inputCount = 1) => {
        const name = namedODCSInput(port);
        if (name) {
            if (node.kind !== KIND_TRANSFORM || !transformInputNamePattern.test(name)) return false;
            if (
                reference &&
                (hasObjectEntries(node.transformInputContract) ||
                    node.originalStep?.transform?.input_contract ||
                    node.originalStep?.transform?.input_contract_ref)
            )
                return false;
            node.originalStep = cloneJSONValue(node.originalStep || {});
            node.originalStep.transform ||= {};
            node.originalStep.transform.input_contracts ||= {};
            const attachment = node.originalStep.transform.input_contracts[name] || {};
            if (reference) {
                attachment.contract_ref = cloneJSONValue(reference);
                delete attachment.contract;
            } else {
                delete attachment.contract_ref;
            }
            if (Object.keys(attachment).length) node.originalStep.transform.input_contracts[name] = attachment;
            else delete node.originalStep.transform.input_contracts[name];
            if (!Object.keys(node.originalStep.transform.input_contracts).length)
                delete node.originalStep.transform.input_contracts;
            return true;
        }
        const fields = odcsPortFields[port];
        if (!fields || fields[0] !== node.kind || (reference && port === "transform.input" && inputCount !== 1)) {
            return false;
        }
        if (reference && port === "transform.input" && hasObjectEntries(node.originalStep?.transform?.input_contracts))
            return false;
        if (reference && node.kind === KIND_INPUT && reference.representation !== "json") return false;
        const [kind, direction, inlineKey] = fields;
        node.originalStep = cloneJSONValue(node.originalStep || {});
        node.originalStep[kind] = node.originalStep[kind] || {};
        if (reference) {
            node.originalStep[kind][`${direction}_contract_ref`] = cloneJSONValue(reference);
            delete node.originalStep[kind][`${direction}_contract`];
            node[inlineKey] = {};
        } else {
            delete node.originalStep[kind][`${direction}_contract_ref`];
        }
        return true;
    };

    const setODCSPortShape = (node, port, key, value) => {
        const ref = odcsPortReference(node, port);
        const fullInline = port === "input.output" || Boolean(namedODCSInput(port));
        const field = odcsPortFields[port]?.[2];
        const contract = cloneJSONValue(ref || odcsPortInlineContract(node, port) || node[field] || {});
        if (value) contract[key] = value;
        else delete contract[key];
        if (ref) return setODCSPortRef(node, port, contract);
        if (fullInline) return setODCSPortInlineContract(node, port, contract);
        if (!field) return false;
        node[field] = contract;
        return true;
    };

    const buildStepSpecForExportNode = (node, inputMaps = {}) => {
        const transformInputsByStep =
            inputMaps.transformInputsByStep instanceof Map ? inputMaps.transformInputsByStep : new Map();
        const sinkInputByStep = inputMaps.sinkInputByStep instanceof Map ? inputMaps.sinkInputByStep : new Map();
        const stepSlug =
            typeof node?.step_slug === "string" && node.step_slug.trim() !== "" ? node.step_slug.trim() : node.slug;
        const originalStep =
            node?.original_step && typeof node.original_step === "object" && !Array.isArray(node.original_step)
                ? cloneJSONValue(node.original_step)
                : {};
        const step = {
            ...originalStep,
            kind: node.kind,
            slug: stepSlug,
        };
        const authoredStepName = typeof node?.step_name === "string" ? node.step_name.trim() : "";
        if (authoredStepName !== "") {
            step.name = authoredStepName;
        } else if (typeof originalStep.name !== "string" || originalStep.name.trim() === "") {
            delete step.name;
        }

        if (node.kind === KIND_QUERY) {
            const query = {
                ...(originalStep.query && typeof originalStep.query === "object" ? originalStep.query : {}),
                data_source_ref: node.data_source_slug || "",
                operation_type: node.operation_type || "sql",
            };
            delete step.transform;
            delete step.sink;
            delete query.data_source_slug;
            delete query.operation_ref;
            delete query.operation_inline;
            if (node.query_slug) {
                query.operation_ref = node.query_slug;
            } else if ((node.inline_sql || "").trim() !== "") {
                query.operation_inline = { sql: node.inline_sql };
            } else if (hasObjectEntries(node.operation_inline)) {
                query.operation_inline = node.operation_inline;
            }
            if (hasObjectEntries(node.parameters)) {
                query.parameters = node.parameters;
            } else {
                delete query.parameters;
            }
            if (!query.output_contract_ref && hasObjectEntries(node.query_output_contract)) {
                query.output_contract = hasObjectEntries(originalStep?.query?.output_contract)
                    ? node.query_output_contract
                    : contractForExport(node.query_output_contract, `${stepSlug || "query"}_output`);
            } else {
                delete query.output_contract;
            }
            if (hasObjectEntries(node.query_incremental)) {
                query.incremental = node.query_incremental;
            } else {
                delete query.incremental;
            }
            step.query = query;
        } else if (node.kind === KIND_TRANSFORM) {
            delete step.query;
            delete step.sink;
            step.transform = {
                ...(originalStep.transform && typeof originalStep.transform === "object" ? originalStep.transform : {}),
            };
            const inputs = transformInputsByStep.get(stepSlug);
            if (inputs && typeof inputs === "object" && Object.keys(inputs).length > 0) {
                step.transform.inputs = Object.fromEntries(
                    Object.entries(inputs).filter(([, sourceStep]) => {
                        return typeof sourceStep === "string" && sourceStep.trim() !== "";
                    }),
                );
            } else {
                delete step.transform.inputs;
            }
            if (typeof node.transform_sql === "string" && node.transform_sql.trim() !== "") {
                step.transform.sql = node.transform_sql;
            } else {
                delete step.transform.sql;
            }
            if (typeof node.transform_output_kind === "string" && node.transform_output_kind.trim() !== "") {
                step.transform.output_kind = node.transform_output_kind.trim();
            } else {
                delete step.transform.output_kind;
            }
            if (!step.transform.input_contract_ref && hasObjectEntries(node.transform_input_contract)) {
                step.transform.input_contract = hasObjectEntries(originalStep?.transform?.input_contract)
                    ? node.transform_input_contract
                    : contractForExport(node.transform_input_contract, `${stepSlug || "transform"}_input`);
            } else {
                delete step.transform.input_contract;
            }
            if (!step.transform.output_contract_ref && hasObjectEntries(node.transform_output_contract)) {
                step.transform.output_contract = hasObjectEntries(originalStep?.transform?.output_contract)
                    ? node.transform_output_contract
                    : contractForExport(node.transform_output_contract, `${stepSlug || "transform"}_output`);
            } else {
                delete step.transform.output_contract;
            }
        } else if (node.kind === KIND_SINK) {
            delete step.query;
            delete step.transform;
            const sink = {
                ...(originalStep.sink && typeof originalStep.sink === "object" ? originalStep.sink : {}),
            };
            delete sink.backend;
            delete sink.data_source_slug;
            const sourceStep = sinkInputByStep.get(stepSlug);
            if (typeof sourceStep === "string" && sourceStep.trim() !== "") {
                sink.input = sourceStep;
            } else {
                delete sink.input;
            }
            if (node.sink_data_source_slug) {
                sink.data_source_ref = node.sink_data_source_slug;
            } else {
                delete sink.data_source_ref;
            }
            if (node.sink_operation_type) {
                sink.operation_type = node.sink_operation_type;
            } else {
                delete sink.operation_type;
            }
            if (node.sink_query_slug) {
                delete sink.operation_inline;
                sink.operation_ref = node.sink_query_slug;
            } else if (node.sink_use_inline_operation || hasObjectEntries(node.sink_operation_inline)) {
                delete sink.operation_ref;
                sink.operation_inline = node.sink_operation_inline || {};
            } else {
                delete sink.operation_ref;
                delete sink.operation_inline;
            }
            if (!sink.input_contract_ref && hasObjectEntries(node.sink_input_contract)) {
                sink.input_contract = hasObjectEntries(originalStep?.sink?.input_contract)
                    ? node.sink_input_contract
                    : contractForExport(node.sink_input_contract, `${stepSlug || "sink"}_input`);
            } else {
                delete sink.input_contract;
            }
            sink.idempotency_class = normalizeSinkIdempotencyClass(node.sink_idempotency_class);
            step.sink = sink;
        }

        return step;
    };

    const buildBuilderStatePayloadFromLiveNodes = (nodes, edges, bindings, pipelineMeta = {}) => {
        const normalizedNodes = nodes.map((node) => normalizeNodeForExport(node));
        const stepInputMaps = buildBuilderStepInputMaps(normalizedNodes, edges, bindings);
        return {
            pipeline_spec: {
                metadata: {
                    slug: pipelineMeta.slug || DRAFT_PIPELINE_SLUG,
                    name: pipelineMeta.name || "Draft Pipeline",
                },
                steps: normalizedNodes.map((node) => buildStepSpecForExportNode(node, stepInputMaps)),
            },
            graph: {
                nodes: normalizedNodes.map((node) => ({
                    step_slug:
                        typeof node.step_slug === "string" && node.step_slug.trim() !== "" ? node.step_slug : node.slug,
                })),
                edges: edges.map((edge) => ({
                    from_step: edge.from,
                    to_step: edge.to,
                })),
                bindings: serializeBindingsForExport(bindings),
            },
            context: {
                expected_spec_hash: pipelineMeta.expectedVersionId ?? null,
                current_version_spec_hash: pipelineMeta.currentVersionId ?? null,
                draft_based_on_spec_hash: pipelineMeta.draftBasedOnVersionId ?? null,
                source_format: pipelineMeta.sourceFormat || FORMAT_YAML,
                export_format: pipelineMeta.exportFormat || pipelineMeta.sourceFormat || FORMAT_YAML,
                target_org_slug: pipelineMeta.targetOrgSlug || "",
                target_project_slug: pipelineMeta.targetProjectSlug || "",
                target_environment_slug: pipelineMeta.targetEnvironmentSlug || "",
            },
        };
    };

    const validateEdgeTopology = (nodes, edges) => {
        const issues = [];
        const nodeBySlug = new Map(nodes.map((node) => [node.slug, node]));
        const incomingCounts = new Map();

        edges.forEach((edge) => {
            const fromNode = nodeBySlug.get(edge.from);
            const toNode = nodeBySlug.get(edge.to);

            if (!fromNode || !toNode) {
                return;
            }
            if (fromNode.kind === KIND_SINK) {
                issues.push({
                    message: `${edge.from}: sink node cannot have outgoing edges`,
                    nodeSlug: edge.from,
                });
            }
            if (toNode.kind === KIND_INPUT) {
                issues.push({ message: `${edge.to}: event input cannot have incoming edges`, nodeSlug: edge.to });
            }
            if (toNode.kind === KIND_QUERY) {
                issues.push({
                    message: `${edge.to}: query step cannot have incoming edges`,
                    nodeSlug: edge.to,
                });
            }

            const nextIncomingCount = (incomingCounts.get(edge.to) || 0) + 1;
            incomingCounts.set(edge.to, nextIncomingCount);
            if (toNode.kind === KIND_SINK && nextIncomingCount > 1) {
                issues.push({ message: `${edge.to}: only one incoming edge allowed`, nodeSlug: edge.to });
            }
        });

        return issues;
    };

    const validateNodes = (nodes, dataSourceMetadata = null, descriptorByType = null) => {
        const issues = [];
        nodes.forEach((node) => {
            if (!slugPattern.test(node.slug || "")) {
                issues.push({
                    message: `${node.kind} node has invalid slug: ${node.slug || "(empty)"}`,
                    nodeSlug: null,
                });
            }
            if (node.kind === KIND_QUERY) {
                if (!slugPattern.test(node.dataSourceSlug || "")) {
                    issues.push({ message: `${node.slug}: missing data source`, nodeSlug: node.slug });
                }
                const hasInlineOperation = hasObjectEntries(node.operationInline);
                const usesInlineSQL =
                    (node.operationType || "sql") === "sql" &&
                    (Boolean(node.useInlineSQL) || (node.inlineSQL || "").trim() !== "" || hasInlineOperation);
                const usesInlineOperation =
                    (node.operationType || "sql") !== "sql" && (Boolean(node.useInlineOperation) || hasInlineOperation);
                if (
                    !usesInlineSQL &&
                    !usesInlineOperation &&
                    !slugPattern.test(node.querySlug || "") &&
                    !hasInlineOperation
                ) {
                    issues.push({
                        message:
                            (node.operationType || "sql") === "sql"
                                ? `${node.slug}: missing saved query or inline SQL`
                                : `${node.slug}: missing saved query or inline operation`,
                        nodeSlug: node.slug,
                    });
                }
                if (usesInlineSQL && (node.inlineSQL || "").trim() === "" && !hasInlineOperation) {
                    issues.push({ message: `${node.slug}: missing inline SQL`, nodeSlug: node.slug });
                }
                if (usesInlineOperation && !hasInlineOperation) {
                    issues.push({ message: `${node.slug}: missing inline operation`, nodeSlug: node.slug });
                }
            }
            if (
                node.kind === KIND_INPUT &&
                !hasObjectEntries(node.originalStep?.input?.output_contract) &&
                !node.originalStep?.input?.output_contract_ref
            ) {
                issues.push({
                    message: `${node.slug}: declare a JSON output contract or select an exact ODCS revision`,
                    nodeSlug: node.slug,
                });
            }
            if (node.kind === KIND_TRANSFORM && (node.transformSQL || "").trim() === "") {
                issues.push({ message: `${node.slug}: missing transform SQL`, nodeSlug: node.slug });
            }
            if (node.kind === KIND_SINK) {
                const hasSinkInlineOperation =
                    node.sinkOperationInline &&
                    typeof node.sinkOperationInline === "object" &&
                    !Array.isArray(node.sinkOperationInline) &&
                    Object.keys(node.sinkOperationInline).length > 0;
                if (!slugPattern.test(node.sinkDataSourceSlug || "")) {
                    issues.push({ message: `${node.slug}: missing sink data source`, nodeSlug: node.slug });
                }
                const source = dataSourceMetadata?.get(node.sinkDataSourceSlug);
                const descriptor = descriptorForDataSourceType(source?.type, descriptorByType);
                if (descriptor && !descriptor.supportedSinkReplayStrategies.includes(node.sinkIdempotencyClass)) {
                    issues.push({
                        message: `${node.slug}: select a supported replay policy for ${source.type}`,
                        nodeSlug: node.slug,
                    });
                }
                const deliveryKey = node.originalStep?.sink?.config?.key;
                if (node.sinkBackend === "s3" && (typeof deliveryKey !== "string" || !deliveryKey.trim())) {
                    issues.push({ message: `${node.slug}: enter an S3 output object key`, nodeSlug: node.slug });
                }
                if ((node.sinkOperationType || "").trim() === "") {
                    issues.push({ message: `${node.slug}: missing sink operation type`, nodeSlug: node.slug });
                }
                if (
                    !node.sinkUseInlineOperation &&
                    !hasSinkInlineOperation &&
                    !slugPattern.test(node.sinkQuerySlug || "")
                ) {
                    issues.push({
                        message: `${node.slug}: missing saved query or inline operation`,
                        nodeSlug: node.slug,
                    });
                }
                if (node.sinkUseInlineOperation && !hasSinkInlineOperation) {
                    issues.push({ message: `${node.slug}: missing sink inline operation`, nodeSlug: node.slug });
                }
            }
            if (isStepKind(node.kind)) {
                if (!slugPattern.test(node.stepSlug || "")) {
                    issues.push({ message: `${node.slug}: missing step slug`, nodeSlug: node.slug });
                }
            }
        });
        return issues;
    };

    const validateBindingCompleteness = (nodes, edges, bindings) => {
        const issues = [];
        const normalized = normalizeBindings(bindings);
        const nodeBySlug = new Map(nodes.map((n) => [n.slug, n]));
        nodes.forEach((node) => {
            if (node.kind === KIND_TRANSFORM) {
                const transformBindings = normalized.filter((b) => b.toNode === node.slug && b.fromPort === PORT_ROWS);
                const incomingStepEdges = edges.filter((e) => {
                    if (e.to !== node.slug) {
                        return false;
                    }
                    const fromNode = nodeBySlug.get(e.from);
                    return fromNode !== undefined && isStepKind(fromNode.kind);
                });
                const missingSources = incomingStepEdges.filter(
                    (edge) => !transformBindings.some((binding) => binding.fromNode === edge.from),
                );
                if (transformBindings.length === 0) {
                    issues.push({
                        message: `${node.slug}: missing transform input binding`,
                        nodeSlug: node.slug,
                    });
                } else if (missingSources.length > 0) {
                    issues.push({
                        message: `${node.slug}: missing transform input binding for ${missingSources
                            .map((edge) => edge.from)
                            .join(", ")}`,
                        nodeSlug: node.slug,
                    });
                }
            }
            if (node.kind === KIND_SINK) {
                const hasPayloadBinding = normalized.some((b) => b.toNode === node.slug && b.toPort === "payload");
                if (!hasPayloadBinding) {
                    issues.push({
                        message: `${node.slug}: missing sink binding (payload)`,
                        nodeSlug: node.slug,
                    });
                }
                const hasIncomingStepEdge = edges.some((e) => {
                    if (e.to !== node.slug) {
                        return false;
                    }
                    const fromNode = nodeBySlug.get(e.from);
                    return fromNode !== undefined && isStepKind(fromNode.kind);
                });
                if (!hasIncomingStepEdge) {
                    issues.push({
                        message: `${node.slug}: sink has no incoming step edge`,
                        nodeSlug: node.slug,
                    });
                }
            }
        });
        return issues;
    };

    const init = (options) => {
        const mainGridElement = document.getElementById(options.mainGridId || "builder-main-grid");
        const mainSectionElement = document.getElementById("builder-main-section");
        const paletteAsideElement = document.getElementById(options.paletteAsideId || "builder-palette-aside");
        const propertiesAsideElement = document.getElementById(options.propertiesAsideId || "builder-properties-aside");
        const workspaceElement = document.getElementById(options.workspaceId || "builder-workspace");
        const sourcePanelElement = document.getElementById(options.sourcePanelId || "builder-source-workspace-panel");
        const emptyCanvasHintElement = document.getElementById(
            options.emptyCanvasHintId || "builder-empty-canvas-hint",
        );
        const previewModeButton = document.getElementById(options.previewModeButtonId || "builder-mode-preview-btn");
        const splitModeButton = document.getElementById(options.splitModeButtonId || "builder-mode-split-btn");
        const sourceModeButton = document.getElementById(options.sourceModeButtonId || "builder-mode-source-btn");
        const togglePaletteButton = document.getElementById(
            options.togglePaletteButtonId || "builder-toggle-palette-btn",
        );
        const togglePropertiesButton = document.getElementById(
            options.togglePropertiesButtonId || "builder-toggle-properties-btn",
        );
        const paletteBodyElement = document.getElementById(options.paletteBodyId || "builder-palette-body");
        const paletteTitleElement = document.getElementById(options.paletteTitleId || "builder-palette-title");
        const paletteRailLabelElement = document.getElementById(
            options.paletteRailLabelId || "builder-palette-rail-label",
        );
        const propertiesBodyElement = document.getElementById(options.propertiesBodyId || "builder-properties-body");
        const propertiesTitleElement = document.getElementById(options.propertiesTitleId || "builder-properties-title");
        const propertiesRailLabelElement = document.getElementById(
            options.propertiesRailLabelId || "builder-properties-rail-label",
        );
        const gridElement = document.getElementById(options.gridId);
        const rootElement = document.getElementById(options.rootId);
        const stageElement = document.getElementById(options.stageId || options.rootId);
        const boundsElement = document.getElementById(options.boundsId || options.stageId || options.rootId);
        const surfaceElement = document.getElementById(options.surfaceId || options.stageId || options.rootId);
        const viewportElement = document.getElementById(options.viewportId || "builder-canvas-viewport");
        const edgesLayerElement = document.getElementById(options.edgesLayerId);
        const edgesDataElement = document.getElementById(options.edgesDataId);
        const paletteSearch = document.getElementById(options.paletteSearchId);
        const validationList = document.getElementById(options.validationSummaryListId);
        const saveReason = document.getElementById(options.saveReasonId);
        const runReason = document.getElementById(options.runReasonId || "builder-run-reason");
        const validateButton = document.getElementById(options.validateButtonId || "builder-validate-btn");
        const publishButton = document.getElementById(options.publishButtonId || "builder-publish-btn");
        const proposalRefreshButton = document.getElementById("builder-proposal-refresh-btn");
        const saveButton = document.getElementById(options.saveButtonId);
        const runButton = document.getElementById(options.runButtonId);
        const nodeDisclosurePopover = document.getElementById("builder-node-disclosure-popover");
        const property = options.property || {};

        if (!options.pipeline || typeof options.pipeline !== "object") {
            options.pipeline = {};
        }

        let pipelineHasDraft = Boolean(options.pipeline.hasDraft);
        if (!pipelineHasDraft && rootElement instanceof HTMLElement) {
            pipelineHasDraft = parseBool(rootElement.dataset.pipelineHasDraft || "false");
        }
        options.pipeline.hasDraft = pipelineHasDraft;
        options.pipeline.expectedVersionId = parseOptionalVersionToken(
            options.pipeline.expectedVersionId || rootElement?.dataset?.pipelineExpectedVersionId || "",
        );
        options.pipeline.currentVersionId = parseOptionalVersionToken(
            options.pipeline.currentVersionId || rootElement?.dataset?.pipelineCurrentVersionId || "",
        );
        options.pipeline.draftBasedOnVersionId = parseOptionalVersionToken(
            options.pipeline.draftBasedOnVersionId || rootElement?.dataset?.pipelineDraftBasedOnVersionId || "",
        );
        if (
            !(gridElement instanceof HTMLElement) ||
            !(rootElement instanceof HTMLElement) ||
            !(stageElement instanceof HTMLElement) ||
            !(boundsElement instanceof HTMLElement) ||
            !(surfaceElement instanceof HTMLElement)
        ) {
            return;
        }
        if (!(edgesLayerElement instanceof SVGSVGElement) || !(edgesDataElement instanceof HTMLElement)) {
            return;
        }
        if (gridElement.dataset.initialized === "true") {
            return;
        }

        gridElement.dataset.initialized = "true";
        let storageKey;
        const isExampleMode = typeof options?.pipeline?.example === "string" && options.pipeline.example.trim() !== "";

        let pipelineSourceFormat = normalizePipelineFormat(options?.pipeline?.sourceFormat, FORMAT_YAML);
        let pipelineExportFormat = normalizePipelineFormat(options?.pipeline?.exportFormat, pipelineSourceFormat);

        const getActiveOrganizationSlug = () => {
            const orgSwitcher = document.querySelector("[data-org-switcher]");
            if (orgSwitcher instanceof HTMLSelectElement) {
                return (orgSwitcher.value || "").trim();
            }
            const fromQuery = new URLSearchParams(window.location.search).get("org_slug") || "";
            return fromQuery.trim();
        };

        const getActiveProjectSlug = () => {
            const fromOptions =
                typeof options?.pipeline?.projectSlug === "string" ? options.pipeline.projectSlug.trim() : "";
            if (fromOptions) {
                return fromOptions;
            }
            const fromRoot = rootElement instanceof HTMLElement ? (rootElement.dataset.projectSlug || "").trim() : "";
            if (fromRoot) {
                return fromRoot;
            }
            const fromQuery = new URLSearchParams(window.location.search).get("project_slug") || "";
            return fromQuery.trim();
        };

        storageKey = storageKeyFor(
            options.pipeline?.slug,
            getActiveOrganizationSlug(),
            getActiveProjectSlug(),
            rootElement.dataset.memberSlug,
        );

        const getActiveTargetEnvironmentSlug = () => {
            const fromOptions =
                typeof options?.pipeline?.targetEnvironmentSlug === "string"
                    ? options.pipeline.targetEnvironmentSlug.trim()
                    : "";
            if (fromOptions) {
                return fromOptions;
            }
            const fromRoot =
                rootElement instanceof HTMLElement ? (rootElement.dataset.targetEnvironmentSlug || "").trim() : "";
            if (fromRoot) {
                return fromRoot;
            }
            const params = new URLSearchParams(window.location.search);
            const fromTargetQuery = params.get("target_environment_slug") || "";
            if (fromTargetQuery.trim()) {
                return fromTargetQuery.trim();
            }
            return (params.get("environment_slug") || "").trim();
        };

        const hasRestoredWorkspaceMode = false;

        const applyPipelineVersionContext = (versionState = {}) => {
            const expectedVersionId = parseOptionalVersionToken(versionState.expectedVersionId);
            const currentVersionId = parseOptionalVersionToken(versionState.currentVersionId);
            const draftBasedOnVersionId = parseOptionalVersionToken(versionState.draftBasedOnVersionId);

            options.pipeline.expectedVersionId = expectedVersionId;
            options.pipeline.currentVersionId = currentVersionId;
            options.pipeline.draftBasedOnVersionId = draftBasedOnVersionId;

            if (rootElement instanceof HTMLElement) {
                rootElement.dataset.pipelineExpectedVersionId =
                    expectedVersionId == null ? "" : String(expectedVersionId);
                rootElement.dataset.pipelineCurrentVersionId = currentVersionId == null ? "" : String(currentVersionId);
                rootElement.dataset.pipelineDraftBasedOnVersionId =
                    draftBasedOnVersionId == null ? "" : String(draftBasedOnVersionId);
            }
        };

        const setActivePipelineContext = (
            organizationSlug,
            projectSlug,
            pipelineSlug,
            pipelineName,
            hasDraft,
            versionState = {},
        ) => {
            const normalizedOrgSlug = (organizationSlug || "").trim();
            const normalizedProjectSlug = (projectSlug || "").trim();
            const normalizedPipelineSlug = (pipelineSlug || "").trim();
            if (!normalizedOrgSlug || !normalizedProjectSlug || !normalizedPipelineSlug) {
                return;
            }

            const normalizedPipelineName =
                typeof pipelineName === "string" && pipelineName.trim() !== ""
                    ? pipelineName.trim()
                    : normalizedPipelineSlug;
            const normalizedHasDraft = Boolean(hasDraft);

            const creationForm = document.getElementById("builder-creation-form");
            if (creationForm) creationForm.hidden = true;

            options.pipeline.projectSlug = normalizedProjectSlug;
            options.pipeline.slug = normalizedPipelineSlug;
            options.pipeline.name = normalizedPipelineName;
            options.pipeline.hasDraft = normalizedHasDraft;
            pipelineHasDraft = normalizedHasDraft;
            storageKey = storageKeyFor(
                normalizedPipelineSlug,
                normalizedOrgSlug,
                normalizedProjectSlug,
                rootElement.dataset.memberSlug,
            );
            applyPipelineVersionContext(versionState);

            if (rootElement instanceof HTMLElement) {
                rootElement.dataset.projectSlug = normalizedProjectSlug;
                rootElement.dataset.pipelineSlug = normalizedPipelineSlug;
                rootElement.dataset.pipelineName = normalizedPipelineName;
                rootElement.dataset.pipelineHasDraft = normalizedHasDraft ? "true" : "false";
            }

            window.history.replaceState(
                {},
                "",
                `/builder?org_slug=${encodeURIComponent(normalizedOrgSlug)}&project_slug=${encodeURIComponent(normalizedProjectSlug)}&pipeline_slug=${encodeURIComponent(normalizedPipelineSlug)}&target_environment_slug=${encodeURIComponent(getActiveTargetEnvironmentSlug())}`,
            );
        };

        const reloadBuilderFromServer = (organizationSlug, projectSlug, pipelineSlug) => {
            window.location.assign(
                `/builder?org_slug=${encodeURIComponent(organizationSlug)}&project_slug=${encodeURIComponent(projectSlug)}&pipeline_slug=${encodeURIComponent(pipelineSlug)}&target_environment_slug=${encodeURIComponent(getActiveTargetEnvironmentSlug())}`,
            );
        };

        const discardDraftAndReload = async (organizationSlug, projectSlug, pipelineSlug) => {
            const response = await fetch(
                `/orgs/${encodeURIComponent(organizationSlug)}/projects/${encodeURIComponent(projectSlug)}/pipelines/${encodeURIComponent(pipelineSlug)}/draft/discard`,
                {
                    method: "POST",
                    headers: {
                        Accept: "application/json",
                    },
                },
            );
            if (!response.ok) {
                const payload = await readAPIJSON(response);
                const errorCode =
                    typeof payload?.error?.code === "string" ? payload.error.code.trim().toUpperCase() : "";
                // If draft was already deleted, treat it as success (discard is idempotent)
                if (response.status === 404 && errorCode === "PIPELINE_DRAFT_NOT_FOUND") {
                    reloadBuilderFromServer(organizationSlug, projectSlug, pipelineSlug);
                    return;
                }
                const reason = (await readAPIErrorMessage(response)) || "unknown error";
                throw new Error(reason);
            }
            reloadBuilderFromServer(organizationSlug, projectSlug, pipelineSlug);
        };

        const fetchCanonicalBuilderState = async (organizationSlug, projectSlug, pipelineSlug) => {
            const response = await fetch(
                `/orgs/${encodeURIComponent(organizationSlug)}/projects/${encodeURIComponent(projectSlug)}/pipelines/${encodeURIComponent(pipelineSlug)}/canonical-state`,
                { headers: { Accept: "application/json" } },
            );
            if (!response.ok) {
                const reason = (await readAPIErrorMessage(response)) || "Could not load canonical spec for comparison.";
                throw new Error(reason);
            }
            return response.json();
        };

        const propertyEmpty = document.getElementById(property.emptyId);
        const propertyForm = document.getElementById(property.formId);
        const propertyKind = document.getElementById(property.kindId);
        const propertyType = document.getElementById(property.nodeTypeId);
        const fieldSlug = document.getElementById(property.slugId);
        const _fieldDataSource = document.getElementById(property.dataSourceId);
        const _fieldQuery = document.getElementById(property.queryId);
        const fieldStepSlug = document.getElementById(property.stepSlugId);
        const fieldStepName = document.getElementById(property.stepNameId);
        const fieldParameters = document.getElementById(property.parametersId);
        const parameterRows = document.getElementById(property.parameterRowsId || "builder-parameter-rows");
        const fieldQueryOutputContract = document.getElementById(property.queryOutputContractId);
        const queryOutputContractEditor = document.getElementById(
            property.queryOutputContractEditorId || "builder-query-output-contract-editor",
        );
        const fieldQueryIncrementalKind = document.getElementById(property.queryIncrementalKindId);
        const fieldQueryIncrementalField = document.getElementById(property.queryIncrementalFieldId);
        const fieldQueryIncrementalCursor = document.getElementById(property.queryIncrementalCursorId);
        const fieldQueryIncrementalLookback = document.getElementById(property.queryIncrementalLookbackId);
        const fieldQueryIncrementalOrdering = document.getElementById(property.queryIncrementalOrderingId);
        const fieldQueryIncrementalDedupe = document.getElementById(property.queryIncrementalDedupeId);
        const queryIncrementalFieldGroup = document.getElementById(property.queryIncrementalFieldGroupId);
        const queryIncrementalCursorGroup = document.getElementById(property.queryIncrementalCursorGroupId);
        const fieldTransformSQL = document.getElementById(property.transformSQLId);
        const fieldTransformInputContract = document.getElementById(property.transformInputContractId);
        const transformInputContractEditor = document.getElementById(
            property.transformInputContractEditorId || "builder-transform-input-contract-editor",
        );
        const fieldTransformOutputContract = document.getElementById(property.transformOutputContractId);
        const transformOutputContractEditor = document.getElementById(
            property.transformOutputContractEditorId || "builder-transform-output-contract-editor",
        );
        const fieldSinkInputContract = document.getElementById(property.sinkInputContractId);
        const sinkInputContractEditor = document.getElementById(
            property.sinkInputContractEditorId || "builder-sink-input-contract-editor",
        );
        const parametersGroup = document.getElementById("builder-parameters-group");
        const stepNameGroup = document.getElementById("builder-step-name-group");
        const transformSQLGroup = document.getElementById(
            property.transformSQLGroupId || "builder-transform-sql-group",
        );
        const transformContractsSection = document.getElementById("builder-transform-contracts-section");
        const sourceGroup = document.getElementById(property.sourceGroupId);
        const transformGroup = document.getElementById(property.transformGroupId);
        const sinkGroup = document.getElementById(property.sinkGroupId);
        const fieldSinkBackend = document.getElementById(property.sinkDataSourceId || property.sinkBackendId);
        const bindingSection = document.getElementById(property.bindingSectionId);
        const bindingStatus = document.getElementById(property.bindingStatusId);
        const bindingHelp = document.getElementById(property.bindingHelpId);
        const bindingRows = document.getElementById(property.bindingRowsId);
        const bindingEdgeHint = document.getElementById(property.edgeHintId);
        const bindingPayloadDiagnostic = document.getElementById(property.payloadDiagnosticId);
        const bindingError = document.getElementById(property.bindingErrorId);
        const connectionSection = document.getElementById(property.connectionSectionId);
        const connectionStatus = document.getElementById(property.connectionStatusId);
        const connectionHelp = document.getElementById(property.connectionHelpId);
        const connectionEditor = document.getElementById(property.connectionEditorId);
        const connectionError = document.getElementById(property.connectionErrorId);
        const errorSlug = document.getElementById(property.errorSlugId);
        const errorDataSource = document.getElementById(property.errorDataSourceId);
        const errorQuery = document.getElementById(property.errorQueryId);
        const errorStepSlug = document.getElementById(property.errorStepSlugId);
        const errorParameters = document.getElementById(property.errorParametersId);
        const errorTransformSQL = document.getElementById(property.errorTransformSQLId);
        const errorQueryOutputContract = document.getElementById(property.errorQueryOutputContractId);
        const errorQueryIncremental = document.getElementById(property.errorQueryIncrementalId);
        const errorTransformInputContract = document.getElementById(property.errorTransformInputContractId);
        const errorTransformOutputContract = document.getElementById(property.errorTransformOutputContractId);
        const errorSinkInputContract = document.getElementById(property.errorSinkInputContractId);
        const bindingsDataElement = document.getElementById(options.bindingsDataId);
        const initialSourceElement = document.getElementById(options.initialSourceId || "builder-initial-source");
        const sourceEditor = options.sourceEditor || {};
        const sourceEditorField = document.getElementById(sourceEditor.textareaId);
        let sourceEditorCanvasSource = "";
        const sourceHighlightElement = document.getElementById(sourceEditor.highlightId);
        const sourceLineNumbersElement = document.getElementById(
            sourceEditor.lineNumbersId || "builder-source-line-numbers",
        );
        const sourceFormatHintElement = document.getElementById(sourceEditor.formatHintId);
        const sourceEditorSyncButton = document.getElementById(sourceEditor.syncButtonId);
        const sourceEditorApplyButton = document.getElementById(sourceEditor.applyButtonId);
        const sourceEditorStatus = document.getElementById(sourceEditor.statusId);

        const propertyPanel = document.getElementById("builder-property-panel");
        const initialPipelineSource =
            initialSourceElement instanceof HTMLTextAreaElement ? initialSourceElement.value : "";
        let isPaletteVisible = true;
        let isPropertiesVisible = true;
        let workspaceViewMode = "preview";

        const getDataSourceField = () => document.getElementById(property.dataSourceId);
        const getQueryField = () => document.getElementById(property.queryId);
        const getSinkDataSourceField = () =>
            document.getElementById(
                property.sinkDataSourceId || property.sinkBackendId || "builder-field-sink-backend",
            );
        const getSinkQueryField = () => document.getElementById(property.sinkQueryId || "builder-field-sink-query");
        const getSinkSavedQueryGroup = () =>
            document.getElementById(property.sinkSavedQueryGroupId || "builder-sink-saved-query-group");
        const getSinkInlineOperationToggleField = () =>
            document.getElementById(
                property.sinkInlineOperationToggleId || "builder-field-sink-inline-operation-toggle",
            );
        const getSinkInlineOperationField = () =>
            document.getElementById(property.sinkOperationInlineId || "builder-field-sink-operation-inline");
        const getSourceInlineOperationJSONField = () =>
            document.getElementById("builder-source-inline-operation-json-field");
        const getSinkInlineOperationJSONField = () =>
            document.getElementById("builder-sink-inline-operation-json-field");
        const getSourceInlineOperationGuided = () => document.getElementById("builder-source-inline-operation-guided");
        const getSinkInlineOperationGuided = () => document.getElementById("builder-sink-inline-operation-guided");
        const getSinkInlineOperationToggleGroup = () =>
            document.getElementById(
                property.sinkInlineOperationToggleGroupId || "builder-sink-inline-operation-toggle-group",
            );
        const getSinkInlineOperationGroup = () =>
            document.getElementById(property.sinkInlineOperationGroupId || "builder-sink-inline-operation-group");
        const getSinkQueryRefLabel = () => document.getElementById("builder-sink-query-ref-label");
        const getSinkConfigHint = () => document.getElementById("builder-sink-config-hint");
        const getSinkQueryError = () =>
            document.getElementById(property.errorSinkQueryId || "builder-error-sink-query");
        const getSinkDataSourceError = () =>
            document.getElementById(property.errorSinkDataSourceId || "builder-error-sink-data-source");
        const getSinkOperationInlineError = () =>
            document.getElementById(property.errorSinkOperationInlineId || "builder-error-sink-operation-inline");
        const getInlineSQLToggleField = () =>
            document.getElementById(property.inlineSQLToggleId || "builder-field-inline-sql-toggle");
        const getInlineSQLField = () => document.getElementById(property.inlineSQLId || "builder-field-inline-sql");
        const getInlineOperationToggleField = () =>
            document.getElementById(property.inlineOperationToggleId || "builder-field-inline-operation-toggle");
        const getInlineOperationField = () =>
            document.getElementById(property.operationInlineId || "builder-field-operation-inline");
        const getSavedQueryGroup = () =>
            document.getElementById(property.sourceSavedQueryGroupId || "builder-source-saved-query-group");
        const getInlineSQLGroup = () =>
            document.getElementById(property.sourceInlineSQLGroupId || "builder-source-inline-sql-group");
        const getInlineOperationGroup = () =>
            document.getElementById(property.sourceInlineOperationGroupId || "builder-source-inline-operation-group");
        const getQueryRefLabel = () => document.getElementById("builder-query-ref-label");
        const getInlineSQLToggleGroup = () =>
            document.getElementById(property.sourceInlineSQLToggleGroupId || "builder-inline-sql-toggle-group") ||
            getInlineSQLToggleField()?.closest("label.form-control");
        const getInlineOperationToggleGroup = () =>
            document.getElementById(
                property.sourceInlineOperationToggleGroupId || "builder-inline-operation-toggle-group",
            );
        const getInlineSQLToggleLabel = () => document.getElementById("builder-inline-sql-toggle-label");
        const getInlineOperationToggleLabel = () => document.getElementById("builder-inline-operation-toggle-label");
        const getSourceConfigHint = () => document.getElementById("builder-source-config-hint");

        const ensureInlineSQLControls = () => {
            if (!(sourceGroup instanceof HTMLElement)) {
                return;
            }

            const existingToggle = getInlineSQLToggleField();
            const existingInlineField = getInlineSQLField();
            const queryField = getQueryField();
            if (existingToggle instanceof HTMLElement && existingInlineField instanceof HTMLElement) {
                return;
            }
            if (!(queryField instanceof HTMLElement)) {
                return;
            }

            let savedQueryGroup = getSavedQueryGroup();
            if (!(savedQueryGroup instanceof HTMLElement)) {
                savedQueryGroup = document.createElement("div");
                savedQueryGroup.id = property.sourceSavedQueryGroupId || "builder-source-saved-query-group";
                const queryLabel = queryField.closest("label.form-control");
                if (queryLabel instanceof HTMLElement) {
                    queryLabel.parentNode?.insertBefore(savedQueryGroup, queryLabel);
                    savedQueryGroup.appendChild(queryLabel);
                }
            }

            if (!(getInlineSQLToggleField() instanceof HTMLElement)) {
                const toggleLabel = document.createElement("label");
                toggleLabel.className = "form-control mt-2";
                toggleLabel.id = "builder-inline-sql-toggle-group";
                toggleLabel.innerHTML =
                    '<span class="label-text text-xs">Inline SQL</span>' +
                    '<label class="label cursor-pointer justify-start gap-2">' +
                    `<input class="checkbox checkbox-sm" id="${property.inlineSQLToggleId || "builder-field-inline-sql-toggle"}" type="checkbox"/>` +
                    '<span class="label-text text-xs" id="builder-inline-sql-toggle-label">Use inline SQL instead of a saved query</span>' +
                    "</label>";
                sourceGroup.insertBefore(
                    toggleLabel,
                    savedQueryGroup instanceof HTMLElement ? savedQueryGroup : queryField,
                );
            }

            if (!(getInlineSQLGroup() instanceof HTMLElement)) {
                const inlineGroup = document.createElement("div");
                inlineGroup.id = property.sourceInlineSQLGroupId || "builder-source-inline-sql-group";
                inlineGroup.className = "hidden";
                inlineGroup.innerHTML =
                    '<label class="form-control mt-2">' +
                    '<span class="label-text text-xs">Inline SQL <span class="text-error">*</span></span>' +
                    `<textarea class="textarea textarea-bordered textarea-sm" id="${property.inlineSQLId || "builder-field-inline-sql"}" rows="5"></textarea>` +
                    '<span class="label-text-alt text-error"></span>' +
                    "</label>";
                if (savedQueryGroup instanceof HTMLElement && savedQueryGroup.nextSibling) {
                    sourceGroup.insertBefore(inlineGroup, savedQueryGroup.nextSibling);
                } else {
                    sourceGroup.appendChild(inlineGroup);
                }
            }

            if (!(getSourceConfigHint() instanceof HTMLElement)) {
                const sourceHint = document.createElement("p");
                sourceHint.className = "mt-2 text-[11px] text-base-content/60";
                sourceHint.id = "builder-source-config-hint";
                sourceGroup.insertBefore(sourceHint, getInlineSQLToggleGroup() || savedQueryGroup || queryField);
            }
        };

        ensureInlineSQLControls();

        if (propertyPanel instanceof HTMLElement) {
            propertyPanel.style.minHeight = "340px";
        }
        if (fieldSlug instanceof HTMLInputElement) {
            fieldSlug.readOnly = true;
            fieldSlug.classList.add("bg-base-200", "cursor-not-allowed");
        }
        if (fieldStepSlug instanceof HTMLInputElement) {
            fieldStepSlug.readOnly = true;
            fieldStepSlug.classList.add("bg-base-200", "cursor-not-allowed");
        }

        const updatePanelButtonState = (buttonElement, visible) => {
            if (!(buttonElement instanceof HTMLButtonElement)) {
                return;
            }

            buttonElement.classList.toggle("btn-outline", visible);
            buttonElement.classList.toggle("btn-ghost", !visible);
            buttonElement.classList.toggle("btn-primary", visible);
        };

        const applyMainGridLayout = () => {
            if (!(mainGridElement instanceof HTMLElement)) {
                return;
            }

            const availableWidth = mainGridElement.getBoundingClientRect().width;
            const layout = builderWorkbenchLayoutForWidth(availableWidth, isPaletteVisible, isPropertiesVisible);
            const canUseTwoColumns = layout.canUsePaletteColumn;
            const canDockProperties = layout.canDockProperties;

            if (paletteAsideElement instanceof HTMLElement) {
                paletteAsideElement.style.padding = isPaletteVisible ? "1rem" : "0.5rem 0.25rem";
                paletteAsideElement.style.overflow = "visible";
                paletteAsideElement.style.borderBottom = canUseTwoColumns ? "0" : "1px solid var(--color-base-300)";
                paletteAsideElement.style.borderRight = canUseTwoColumns ? "1px solid var(--color-base-300)" : "0";
            }
            if (propertiesAsideElement instanceof HTMLElement) {
                propertiesAsideElement.style.position = "relative";
                propertiesAsideElement.style.gridColumn = layout.propertiesGridColumn;
                propertiesAsideElement.style.top = "auto";
                propertiesAsideElement.style.right = "auto";
                propertiesAsideElement.style.bottom = "auto";
                propertiesAsideElement.style.height = "auto";
                propertiesAsideElement.style.zIndex = "1";
                propertiesAsideElement.style.width = canDockProperties ? "auto" : "100%";
                propertiesAsideElement.style.padding = isPropertiesVisible ? "1rem" : "0";
                propertiesAsideElement.style.overflow = "visible";
                propertiesAsideElement.style.borderLeft = canDockProperties ? "1px solid var(--color-base-300)" : "0";
                propertiesAsideElement.style.borderTop =
                    isPropertiesVisible && !canDockProperties ? "1px solid var(--color-base-300)" : "0";
                propertiesAsideElement.style.boxShadow = "none";
                propertiesAsideElement.style.background = isPropertiesVisible || canDockProperties ? "" : "transparent";
                propertiesAsideElement.style.pointerEvents = isPropertiesVisible || canDockProperties ? "auto" : "none";
                propertiesAsideElement.style.minHeight = isPropertiesVisible && !canDockProperties ? "360px" : "";
            }
            if (paletteBodyElement instanceof HTMLElement) {
                paletteBodyElement.classList.toggle("hidden", !isPaletteVisible);
            }
            if (propertiesBodyElement instanceof HTMLElement) {
                propertiesBodyElement.classList.toggle("hidden", !isPropertiesVisible);
            }
            for (const body of [paletteBodyElement, propertiesBodyElement]) {
                if (body instanceof HTMLElement) {
                    body.style.maxHeight = canDockProperties ? "max(28rem, calc(100dvh - 12rem))" : "";
                    body.style.overflowY = canDockProperties ? "auto" : "visible";
                    body.style.scrollbarGutter = canDockProperties ? "stable" : "";
                }
            }
            if (paletteTitleElement instanceof HTMLElement) {
                paletteTitleElement.classList.toggle("hidden", !isPaletteVisible);
            }
            if (paletteRailLabelElement instanceof HTMLElement) {
                paletteRailLabelElement.classList.toggle("hidden", isPaletteVisible);
            }
            if (propertiesTitleElement instanceof HTMLElement) {
                propertiesTitleElement.classList.toggle("hidden", !isPropertiesVisible);
            }
            if (propertiesRailLabelElement instanceof HTMLElement) {
                propertiesRailLabelElement.classList.toggle("hidden", isPropertiesVisible);
            }
            if (togglePaletteButton instanceof HTMLButtonElement) {
                togglePaletteButton.title = isPaletteVisible ? "Hide palette" : "Show palette";
                togglePaletteButton.setAttribute("aria-label", isPaletteVisible ? "Hide palette" : "Show palette");
                const icon = togglePaletteButton.querySelector("svg");
                if (icon instanceof SVGElement) {
                    icon.style.transform = isPaletteVisible ? "rotate(0deg)" : "rotate(180deg)";
                }
            }
            if (togglePropertiesButton instanceof HTMLButtonElement) {
                togglePropertiesButton.title = isPropertiesVisible ? "Hide properties" : "Show properties";
                togglePropertiesButton.setAttribute(
                    "aria-label",
                    isPropertiesVisible ? "Hide properties" : "Show properties",
                );
                togglePropertiesButton.style.pointerEvents = "auto";
                togglePropertiesButton.style.left = canDockProperties ? "-0.75rem" : "auto";
                togglePropertiesButton.style.right = canDockProperties ? "auto" : "1rem";
                togglePropertiesButton.style.top = canDockProperties ? "1.25rem" : "-0.75rem";
                togglePropertiesButton.style.zIndex = "30";
                const icon = togglePropertiesButton.querySelector("svg");
                if (icon instanceof SVGElement) {
                    icon.style.transform = isPropertiesVisible ? "rotate(180deg)" : "rotate(0deg)";
                }
            }

            mainGridElement.style.display = "grid";
            mainGridElement.style.gap = "0";
            mainGridElement.style.position = "relative";
            mainGridElement.style.gridTemplateColumns = layout.gridTemplateColumns;

            if (mainSectionElement instanceof HTMLElement) {
                mainSectionElement.style.paddingRight = "1rem";
            }

            updatePanelButtonState(togglePaletteButton, isPaletteVisible);
            updatePanelButtonState(togglePropertiesButton, isPropertiesVisible);
        };

        const updateModeButtonState = (buttonElement, active) => {
            if (!(buttonElement instanceof HTMLButtonElement)) {
                return;
            }

            buttonElement.classList.toggle("btn-primary", active);
            buttonElement.classList.toggle("btn-outline", !active);
        };

        const applyWorkspaceViewMode = () => {
            if (workspaceViewMode === "source") closeMappingWorkspace({ restoreFocus: false });
            if (!(workspaceElement instanceof HTMLElement) || !(sourcePanelElement instanceof HTMLElement)) {
                return;
            }

            workspaceElement.style.display = "grid";
            workspaceElement.style.gap = "1rem";
            workspaceElement.style.gridTemplateColumns = "minmax(0, 1fr)";
            rootElement.classList.remove("hidden");
            sourcePanelElement.classList.remove("hidden");

            const availableWidth = workspaceElement.getBoundingClientRect().width;
            const canUseSplitColumns = availableWidth >= 1100;

            if (workspaceViewMode === "preview") {
                sourcePanelElement.classList.add("hidden");
            } else if (workspaceViewMode === "split" && canUseSplitColumns) {
                workspaceElement.style.gridTemplateColumns = "minmax(0, 1fr) minmax(340px, 38%)";
            } else if (workspaceViewMode === "source") {
                rootElement.classList.add("hidden");
            }

            updateModeButtonState(previewModeButton, workspaceViewMode === "preview");
            updateModeButtonState(splitModeButton, workspaceViewMode === "split");
            updateModeButtonState(sourceModeButton, workspaceViewMode === "source");
        };

        const shouldSyncSourceOnOpen = () => {
            if (!(sourceEditorField instanceof HTMLTextAreaElement)) {
                return false;
            }
            if (readNodes(gridElement).length === 0) {
                return false;
            }
            return sourceEditorField.value.trim() === "" || sourceEditorField.value === sourceEditorCanvasSource;
        };

        const highlightJSON = (source) => {
            let escaped = escapeHTML(source);
            escaped = escaped.replace(
                /(&quot;(?:[^&]|&(?!quot;))+&quot;)(\s*:)/g,
                '<span class="text-info">$1</span>$2',
            );
            escaped = escaped.replace(
                /:\s*(&quot;(?:[^&]|&(?!quot;))+&quot;)/g,
                ': <span class="text-success">$1</span>',
            );
            escaped = escaped.replace(
                /:\s*(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g,
                ': <span class="text-warning">$1</span>',
            );
            escaped = escaped.replace(/:\s*(true|false|null)/g, ': <span class="text-primary">$1</span>');
            return escaped;
        };

        const highlightYAML = (source) => {
            let escaped = escapeHTML(source);
            escaped = escaped.replace(/^(\s*[A-Za-z0-9_-]+)(\s*:)/gm, '<span class="text-info">$1</span>$2');
            escaped = escaped.replace(/(#.*)$/gm, '<span class="text-base-content/50">$1</span>');
            escaped = escaped.replace(/:\s*("[^"]*"|'[^']*')/g, ': <span class="text-success">$1</span>');
            escaped = escaped.replace(/:\s*(-?\d+(?:\.\d+)?)/g, ': <span class="text-warning">$1</span>');
            escaped = escaped.replace(/:\s*(true|false|null)/g, ': <span class="text-primary">$1</span>');
            return escaped;
        };

        const syncSourceOverlayScroll = () => {
            if (!(sourceEditorField instanceof HTMLTextAreaElement)) {
                return;
            }
            const scrollLeft = sourceEditorField.scrollLeft;
            const scrollTop = sourceEditorField.scrollTop;
            if (sourceHighlightElement instanceof HTMLElement) {
                sourceHighlightElement.style.transform = `translate(${-scrollLeft}px, ${-scrollTop}px)`;
            }
            if (sourceLineNumbersElement instanceof HTMLElement) {
                sourceLineNumbersElement.style.transform = "none";
                sourceLineNumbersElement.scrollTop = scrollTop;
            }
        };

        let sourceScrollSyncFrameId = null;
        let sourceScrollSyncLastTop = -1;
        let sourceScrollSyncStableFrames = 0;
        const stopSourceScrollSyncLoop = () => {
            if (sourceScrollSyncFrameId !== null) {
                window.cancelAnimationFrame(sourceScrollSyncFrameId);
                sourceScrollSyncFrameId = null;
            }
        };

        const startSourceScrollSyncLoop = () => {
            if (!(sourceEditorField instanceof HTMLTextAreaElement) || sourceScrollSyncFrameId !== null) {
                return;
            }

            const tick = () => {
                if (!(sourceEditorField instanceof HTMLTextAreaElement)) {
                    stopSourceScrollSyncLoop();
                    return;
                }

                syncSourceOverlayScroll();

                const currentTop = sourceEditorField.scrollTop;
                if (Math.abs(currentTop - sourceScrollSyncLastTop) < 0.5) {
                    sourceScrollSyncStableFrames += 1;
                } else {
                    sourceScrollSyncStableFrames = 0;
                    sourceScrollSyncLastTop = currentTop;
                }

                // Keep syncing through inertial scrolling, then stop when stable.
                if (sourceScrollSyncStableFrames >= 6) {
                    stopSourceScrollSyncLoop();
                    return;
                }

                sourceScrollSyncFrameId = window.requestAnimationFrame(tick);
            };

            sourceScrollSyncLastTop = sourceEditorField.scrollTop;
            sourceScrollSyncStableFrames = 0;
            sourceScrollSyncFrameId = window.requestAnimationFrame(tick);
        };

        const resetSourceEditorViewport = () => {
            if (!(sourceEditorField instanceof HTMLTextAreaElement)) {
                return;
            }
            sourceEditorField.scrollTop = 0;
            sourceEditorField.scrollLeft = 0;
            syncSourceOverlayScroll();
        };

        const renderSourceHighlight = () => {
            if (
                !(sourceHighlightElement instanceof HTMLElement) ||
                !(sourceEditorField instanceof HTMLTextAreaElement)
            ) {
                return;
            }

            const source = sourceEditorField.value || "";
            const highlighted = pipelineExportFormat === FORMAT_JSON ? highlightJSON(source) : highlightYAML(source);
            sourceHighlightElement.innerHTML = highlighted;
            if (sourceLineNumbersElement instanceof HTMLElement) {
                const lineCount = source === "" ? 1 : source.split("\n").length;
                sourceLineNumbersElement.textContent = Array.from(
                    { length: lineCount },
                    (_, index) => `${index + 1}`,
                ).join("\n");
            }
            if (sourceFormatHintElement instanceof HTMLElement) {
                sourceFormatHintElement.textContent = pipelineExportFormat.toUpperCase();
            }
            syncSourceOverlayScroll();
        };

        const savedState = isExampleMode ? null : loadState(storageKey);
        if (savedState?.pipeline && typeof savedState.pipeline === "object") {
            pipelineSourceFormat = normalizePipelineFormat(savedState.pipeline.source_format, pipelineSourceFormat);
            pipelineExportFormat = pipelineSourceFormat;
        }
        if (savedState && Array.isArray(savedState.edges)) {
            writeEdgesData(edgesDataElement, savedState.edges);
        }
        let bindingsState = savedState ? normalizeBindings(savedState.bindings) : parseBindingData(bindingsDataElement);

        let selectedSlug = null;
        let selectedEdge = null;
        let serverDiagnostics = [];
        let copiedNode = null;
        let canvasScale = 1;
        let canvasFitting = options.fitToContentOnInit !== false;
        let canvasOffsetX = 0;
        let canvasOffsetY = 0;
        const historyLimit = 120;
        const historyStates = [];
        let historyIndex = -1;
        let applyingHistory = false;

        const readCssPxVar = (element, varName, fallbackPx) => {
            if (!(element instanceof HTMLElement)) {
                return fallbackPx;
            }
            const rawValue = window.getComputedStyle(element).getPropertyValue(varName).trim();
            if (rawValue === "") {
                return fallbackPx;
            }
            const parsed = Number.parseFloat(rawValue);
            return Number.isFinite(parsed) && parsed > 0 ? parsed : fallbackPx;
        };

        const gridUnitXPx = readCssPxVar(surfaceElement, "--builder-grid-unit-x", 34);
        const gridUnitYPx = readCssPxVar(surfaceElement, "--builder-grid-unit-y", 43);
        const nodeCardWidthPx = readCssPxVar(surfaceElement, "--builder-node-width", 180);
        const nodeCardHeightPx = readCssPxVar(surfaceElement, "--builder-node-height", 100);

        const setNodePosition = (item, x, y) => {
            const normalizedX = Number.isInteger(x) ? x : 0;
            const normalizedY = Number.isInteger(y) ? y : 0;
            item.setAttribute("data-grid-x", String(normalizedX));
            item.setAttribute("data-grid-y", String(normalizedY));
            item.style.left = `${normalizedX * gridUnitXPx}px`;
            item.style.top = `${normalizedY * gridUnitYPx}px`;
            item.style.width = `${nodeCardWidthPx}px`;
            item.style.height = `${nodeCardHeightPx}px`;
        };

        const grid = {
            addWidget(config = {}) {
                const item = document.createElement("div");
                item.className = "builder-node absolute";
                if (typeof config.content === "string" && config.content !== "") {
                    item.innerHTML = config.content;
                } else {
                    item.innerHTML =
                        '<div class="builder-node-content rounded-lg border border-primary/30 bg-base-100 px-3 py-2 shadow-sm h-full"></div>';
                }
                setNodePosition(item, config.x, config.y);
                gridElement.appendChild(item);
                return item;
            },
            removeWidget(item) {
                if (item instanceof HTMLElement) {
                    item.remove();
                }
            },
            update(item, updates = {}) {
                if (!(item instanceof HTMLElement)) {
                    return;
                }
                const currentX = Number.parseInt(item.getAttribute("data-grid-x") || "0", 10);
                const currentY = Number.parseInt(item.getAttribute("data-grid-y") || "0", 10);
                setNodePosition(
                    item,
                    Number.isInteger(updates.x) ? updates.x : currentX,
                    Number.isInteger(updates.y) ? updates.y : currentY,
                );
            },
        };

        const setStatusMessage = (message, tone) => {
            if (!(saveReason instanceof HTMLElement)) {
                return;
            }

            saveReason.textContent = message;
            saveReason.classList.remove("text-base-content/70", "text-success", "text-error");
            if (tone === TONE_ERROR) {
                saveReason.classList.add("text-error");
                return;
            }
            if (tone === TONE_SUCCESS) {
                saveReason.classList.add("text-success");
                return;
            }
            saveReason.classList.add("text-base-content/70");
        };

        let activeConnectionDrag = null;

        const mappingAttachments = new Map();
        const toggleMappingsButton = document.getElementById("builder-toggle-mappings-btn");
        const refreshMappingAttachments = () => {
            if (!window.braidplaneFieldMapping?.attachment) return;
            const nodes = readNodes(gridElement);
            const steps = buildBuilderStatePayloadFromLiveNodes(nodes, parseEdgeData(edgesDataElement), bindingsState)
                .pipeline_spec.steps;
            for (const [item] of mappingAttachments) {
                if (!gridElement.contains(item)) mappingAttachments.delete(item);
            }
            for (const item of gridElement.querySelectorAll(".builder-node")) {
                const node = readNodeDataFromElement(item);
                const step = steps.find((step) => step.slug === node.stepSlug && step.transform);
                if (node.kind !== KIND_TRANSFORM || !step) {
                    mappingAttachments.get(item)?.element.remove();
                    mappingAttachments.delete(item);
                    continue;
                }
                let attachment = mappingAttachments.get(item);
                if (!attachment) {
                    attachment = window.braidplaneFieldMapping.attachment({
                        slug: node.slug,
                        onOpen: (button, options) => openMappingWorkspace(item, button, options),
                        onLayout: () => scheduleRender(),
                    });
                    attachment.element.addEventListener("click", () => {
                        canvasFitting = false;
                    });
                    item.append(attachment.element);
                    mappingAttachments.set(item, attachment);
                }
                attachment.update({
                    org: getActiveOrganizationSlug(),
                    project: getActiveProjectSlug(),
                    environment: getActiveTargetEnvironmentSlug(),
                    step,
                    steps,
                    evidence: [],
                });
            }
            const columns = new Map();
            for (const item of gridElement.querySelectorAll(".builder-node")) {
                const x = item.getAttribute("data-grid-x");
                if (!columns.has(x)) columns.set(x, []);
                columns.get(x).push(item);
            }
            // Reserve attachment space in the view only; canonical coordinates and SQL are unchanged.
            for (const column of columns.values()) {
                column.sort((a, b) => Number(a.getAttribute("data-grid-y")) - Number(b.getAttribute("data-grid-y")));
                let bottom = 0;
                for (const item of column) {
                    const y = Number(item.getAttribute("data-grid-y")) * gridUnitYPx;
                    const top = Math.max(y, bottom);
                    item.style.top = `${top}px`;
                    bottom =
                        top +
                        nodeCardHeightPx +
                        (mappingAttachments.has(item) ? 12 + mappingAttachments.get(item).element.offsetHeight : 0) +
                        32;
                }
            }
            if (toggleMappingsButton) {
                const allExpanded =
                    mappingAttachments.size > 0 &&
                    [...mappingAttachments.values()].every((attachment) => attachment.isExpanded());
                toggleMappingsButton.disabled = mappingAttachments.size === 0;
                toggleMappingsButton.setAttribute("aria-expanded", String(allExpanded));
                toggleMappingsButton.textContent = allExpanded ? "Collapse mappings" : "Expand mappings";
            }
        };
        toggleMappingsButton?.addEventListener("click", () => {
            const expanded = toggleMappingsButton.getAttribute("aria-expanded") !== "true";
            canvasFitting = false;
            for (const attachment of mappingAttachments.values()) attachment.setExpanded(expanded);
            scheduleRender();
        });
        const render = () => {
            refreshMappingAttachments();
            const isEmpty = readNodes(gridElement).length === 0;
            surfaceElement.classList.toggle("invisible", isEmpty);
            for (const id of ["builder-zoom-in-btn", "builder-zoom-out-btn", "builder-fit-btn"]) {
                const button = document.getElementById(id);
                if (button) button.disabled = isEmpty;
            }
            if (isEmpty) {
                canvasScale = 1;
                canvasFitting = true;
            }
            if (canvasFitting) canvasScale = canvasViewBox().fitScale;
            applyCanvasScale();
            if (emptyCanvasHintElement instanceof HTMLElement) {
                emptyCanvasHintElement.classList.toggle("hidden", readNodes(gridElement).length > 0);
            }
            drawEdges(
                surfaceElement,
                edgesLayerElement,
                edgesDataElement,
                selectedEdge,
                canvasScale,
                serverDiagnostics,
            );
            applySelection(gridElement, selectedSlug);
            if (mappingWorkspaceIsOpen()) {
                if (mappingWorkspace.dataset.editing !== "true") refreshMappingPreview();
                positionMappingWorkspace();
            }
            if (activeConnectionDrag?.moved && activeConnectionDrag.lastPoint) {
                renderConnectionDragPreview(activeConnectionDrag.lastPoint);
            }
        };
        const scheduleRender = scheduleRenderFactory(render);

        const { isTypingContext } = builderInteractions;

        const canvasViewBox = (scale) =>
            builderCanvasViewBox(
                Array.from(gridElement.querySelectorAll(".builder-node"), (item) => ({
                    x: item.offsetLeft,
                    y: item.offsetTop,
                    w: nodeCardWidthPx,
                    h:
                        nodeCardHeightPx +
                        (mappingAttachments.has(item) ? 12 + mappingAttachments.get(item).element.offsetHeight : 0),
                })),
                { width: Math.max(1, stageElement.clientWidth), height: Math.max(1, stageElement.clientHeight) },
                scale,
            );

        const syncCanvasSurfaceSize = () => {
            const view = canvasViewBox(canvasScale);
            canvasOffsetX = view.offsetX;
            canvasOffsetY = view.offsetY;
            surfaceElement.style.width = `${view.width}px`;
            surfaceElement.style.height = `${view.height}px`;
            surfaceElement.style.transform = `translate(${canvasOffsetX}px, ${canvasOffsetY}px) scale(${canvasScale})`;
            boundsElement.style.width = `${view.boundsWidth}px`;
            boundsElement.style.height = `${view.boundsHeight}px`;
            boundsElement.style.minHeight = "0";
        };

        const clampScale = (value) => {
            return Math.max(Math.min(0.4, canvasViewBox().fitScale), Math.min(2.5, value));
        };

        const zoomStep = Number.isFinite(Number(options.zoomStep)) ? Number(options.zoomStep) : 0.05;
        const wheelZoomSpeed = Number.isFinite(Number(options.wheelZoomSpeed)) ? Number(options.wheelZoomSpeed) : 0.004;

        canvasScale = clampScale(Number(options.defaultScale || 1));

        const applyCanvasScale = () => {
            const zoomLevel = document.getElementById("builder-zoom-level");
            if (zoomLevel instanceof HTMLElement) zoomLevel.textContent = `${Math.round(canvasScale * 100)}%`;
            syncCanvasSurfaceSize();
        };

        const defaultZoomAnchor = () => {
            const stageRect = stageElement.getBoundingClientRect();
            return {
                clientX: stageRect.left + stageRect.width / 2,
                clientY: stageRect.top + stageRect.height / 2,
            };
        };

        const adjustCanvasScale = (delta, anchorClientX, anchorClientY) => {
            const nextScale = clampScale(Number((canvasScale + delta).toFixed(2)));
            if (nextScale === canvasScale) {
                return;
            }

            const previousScale = canvasScale;
            const fallbackAnchor = defaultZoomAnchor();
            const pointerClientX = Number.isFinite(anchorClientX) ? anchorClientX : fallbackAnchor.clientX;
            const pointerClientY = Number.isFinite(anchorClientY) ? anchorClientY : fallbackAnchor.clientY;
            const stageRectBefore = stageElement.getBoundingClientRect();
            const localCanvasX =
                (pointerClientX - stageRectBefore.left + stageElement.scrollLeft - canvasOffsetX) / previousScale;
            const localCanvasY =
                (pointerClientY - stageRectBefore.top + stageElement.scrollTop - canvasOffsetY) / previousScale;

            canvasFitting = false;
            canvasScale = nextScale;
            applyCanvasScale();
            scheduleRender();

            window.requestAnimationFrame(() => {
                stageElement.scrollLeft = Math.max(
                    0,
                    localCanvasX * nextScale + canvasOffsetX - (pointerClientX - stageRectBefore.left),
                );
                stageElement.scrollTop = Math.max(
                    0,
                    localCanvasY * nextScale + canvasOffsetY - (pointerClientY - stageRectBefore.top),
                );
                scheduleRender();
            });
        };

        const _resetCanvasScale = (anchorClientX, anchorClientY) => {
            const delta = 1 - canvasScale;
            if (delta === 0) {
                return;
            }
            adjustCanvasScale(delta, anchorClientX, anchorClientY);
        };

        const fitCanvasToNodes = () => {
            canvasFitting = true;
            canvasScale = canvasViewBox().fitScale;
            applyCanvasScale();
            stageElement.scrollLeft = 0;
            stageElement.scrollTop = 0;
            scheduleRender();
        };

        const slugExists = (slug, exceptItem = null) => {
            const item = gridElement.querySelector(`.builder-node[data-node-slug="${slug}"]`);
            return item instanceof HTMLElement && item !== exceptItem;
        };

        const uniqueSlugFrom = (baseSlug, exceptItem = null) => {
            const sanitized =
                (baseSlug || "node")
                    .toLowerCase()
                    .replace(/[^a-z0-9-]/g, "-")
                    .replace(/-+/g, "-")
                    .replace(/^-|-$/g, "") || "node";
            if (!slugExists(sanitized, exceptItem)) {
                return sanitized;
            }
            for (let i = 1; i <= 999; i += 1) {
                const candidate = `${sanitized}-${i}`;
                if (!slugExists(candidate, exceptItem)) {
                    return candidate;
                }
            }
            return `${sanitized}-${Date.now().toString(36).slice(-4)}`;
        };

        const replaceEdgeSlug = (edges, oldSlug, newSlug) => {
            if (!oldSlug || !newSlug || oldSlug === newSlug) {
                return edges;
            }
            return edges.map((edge) => ({
                from: edge.from === oldSlug ? newSlug : edge.from,
                to: edge.to === oldSlug ? newSlug : edge.to,
            }));
        };

        const resetFieldErrors = () => {
            const errorOperationInline = document.getElementById(
                property.errorOperationInlineId || "builder-error-operation-inline",
            );
            const errorSinkDataSource = getSinkDataSourceError();
            const errorSinkQuery = getSinkQueryError();
            const errorSinkOperationInline = getSinkOperationInlineError();
            [
                errorSlug,
                errorDataSource,
                errorQuery,
                errorOperationInline,
                errorSinkDataSource,
                errorSinkQuery,
                errorSinkOperationInline,
                errorStepSlug,
                errorParameters,
                errorTransformSQL,
                errorQueryOutputContract,
                errorQueryIncremental,
                errorTransformInputContract,
                errorTransformOutputContract,
                errorSinkInputContract,
            ].forEach((element) => {
                if (element instanceof HTMLElement) {
                    element.textContent = "";
                }
            });

            if (bindingError instanceof HTMLElement) {
                bindingError.textContent = "";
            }
        };

        const readJSONObjectField = (fieldElement, errorElement, emptyValue = {}) => {
            if (!(fieldElement instanceof HTMLTextAreaElement)) {
                return { ok: true, value: emptyValue };
            }

            const raw = fieldElement.value.trim();
            if (raw === "") {
                return { ok: true, value: emptyValue };
            }

            try {
                const parsed = JSON.parse(raw);
                if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
                    return { ok: true, value: parsed };
                }
            } catch (_) {
                // Fall through to the shared error path below.
            }

            if (errorElement instanceof HTMLElement) {
                errorElement.textContent = "Must be a JSON object.";
            }
            return { ok: false, value: emptyValue };
        };

        const writeJSONObjectField = (fieldElement, value) => {
            if (fieldElement instanceof HTMLTextAreaElement) {
                fieldElement.value = formatJSONMap(value);
            }
        };

        const appendSelectOption = (selectElement, value, label, selectedValue) => {
            const option = document.createElement("option");
            option.value = value;
            option.textContent = label;
            option.selected = value === selectedValue;
            selectElement.appendChild(option);
        };

        const parameterEditorTypeForValue = (value) => {
            if (typeof value === "number") {
                return "number";
            }
            if (typeof value === "boolean") {
                return "boolean";
            }
            if (value && typeof value === "object") {
                return "json";
            }
            return "string";
        };

        const parameterEditorValueText = (value, valueType) => {
            if (valueType === "json") {
                return stableJSONStringify(value) || "";
            }
            return value === undefined || value === null ? "" : String(value);
        };

        const parseParameterEditorValue = (rawValue, valueType) => {
            const value = typeof rawValue === "string" ? rawValue : "";
            switch (valueType) {
                case "number": {
                    if (value.trim() === "") {
                        return { ok: true, value: 0 };
                    }
                    const parsed = Number(value);
                    return Number.isFinite(parsed)
                        ? { ok: true, value: parsed }
                        : { ok: false, error: "Parameter number values must be finite." };
                }
                case "boolean":
                    return { ok: true, value: value === "true" };
                case "json": {
                    if (value.trim() === "") {
                        return { ok: true, value: null };
                    }
                    try {
                        return { ok: true, value: JSON.parse(value) };
                    } catch (_) {
                        return { ok: false, error: "Parameter JSON values must parse." };
                    }
                }
                default:
                    return { ok: true, value };
            }
        };

        const createParameterRow = (key = "", value = "") => {
            const valueType = parameterEditorTypeForValue(value);
            const row = document.createElement("div");
            row.className = "rounded-md border border-base-300 bg-base-100 p-2";
            row.setAttribute("data-parameter-row", "true");

            const grid = document.createElement("div");
            grid.className = "grid grid-cols-[1fr_92px] gap-2";

            const keyLabel = document.createElement("label");
            keyLabel.className = "form-control";
            const keyText = document.createElement("span");
            keyText.className = "label-text text-xs";
            keyText.textContent = "Name";
            const keyInput = document.createElement("input");
            keyInput.className = "input input-bordered input-xs font-mono";
            keyInput.setAttribute("data-parameter-key", "true");
            keyInput.value = key;
            keyLabel.append(keyText, keyInput);

            const typeLabel = document.createElement("label");
            typeLabel.className = "form-control";
            const typeText = document.createElement("span");
            typeText.className = "label-text text-xs";
            typeText.textContent = "Type";
            const typeSelect = document.createElement("select");
            typeSelect.className = "select select-bordered select-xs";
            typeSelect.setAttribute("data-parameter-type", "true");
            parameterValueTypes.forEach((option) => {
                appendSelectOption(typeSelect, option.value, option.label, valueType);
            });
            typeLabel.append(typeText, typeSelect);

            const valueLabel = document.createElement("label");
            valueLabel.className = "form-control mt-2";
            const valueText = document.createElement("span");
            valueText.className = "label-text text-xs";
            valueText.textContent = "Value";
            const valueInput = document.createElement("input");
            valueInput.className = "input input-bordered input-xs font-mono";
            valueInput.setAttribute("data-parameter-value", "true");
            valueInput.value = parameterEditorValueText(value, valueType);
            valueLabel.append(valueText, valueInput);

            const actions = document.createElement("div");
            actions.className = "mt-2 flex justify-end";
            const removeButton = document.createElement("button");
            removeButton.className = "btn btn-ghost btn-xs";
            removeButton.type = "button";
            removeButton.textContent = "Remove";
            removeButton.setAttribute("data-remove-parameter-row", "true");
            actions.appendChild(removeButton);

            grid.append(keyLabel, typeLabel);
            row.append(grid, valueLabel, actions);
            return row;
        };

        const renderParameterEditor = (parameters) => {
            if (!(parameterRows instanceof HTMLElement)) {
                return;
            }
            clearChildren(parameterRows);
            const entries =
                parameters && typeof parameters === "object" && !Array.isArray(parameters)
                    ? Object.entries(parameters)
                    : [];
            if (entries.length === 0) {
                const empty = document.createElement("p");
                empty.className = "text-xs text-base-content/60";
                empty.textContent = "No parameters declared.";
                parameterRows.appendChild(empty);
                return;
            }
            entries.forEach(([key, value]) => {
                parameterRows.appendChild(createParameterRow(key, value));
            });
        };

        const readParameterEditor = () => {
            if (!(parameterRows instanceof HTMLElement)) {
                return { ok: true, value: {} };
            }
            const output = {};
            const seenKeys = new Set();
            for (const row of Array.from(parameterRows.querySelectorAll("[data-parameter-row]"))) {
                const keyInput = row.querySelector("[data-parameter-key]");
                const typeSelect = row.querySelector("[data-parameter-type]");
                const valueInput = row.querySelector("[data-parameter-value]");
                const key = keyInput instanceof HTMLInputElement ? keyInput.value.trim() : "";
                if (key === "") {
                    continue;
                }
                if (seenKeys.has(key)) {
                    return { ok: false, error: `Duplicate parameter "${key}".` };
                }
                seenKeys.add(key);
                const valueType = typeSelect instanceof HTMLSelectElement ? typeSelect.value : "string";
                const rawValue = valueInput instanceof HTMLInputElement ? valueInput.value : "";
                const parsed = parseParameterEditorValue(rawValue, valueType);
                if (!parsed.ok) {
                    return { ok: false, error: parsed.error };
                }
                output[key] = parsed.value;
            }
            return { ok: true, value: output };
        };

        const syncParameterEditorToField = () => {
            const result = readParameterEditor();
            if (!result.ok) {
                if (errorParameters instanceof HTMLElement) {
                    errorParameters.textContent = result.error || "Invalid parameter.";
                }
                return false;
            }
            writeJSONObjectField(fieldParameters, result.value);
            if (errorParameters instanceof HTMLElement) {
                errorParameters.textContent = "";
            }
            return true;
        };

        const validationValueText = (value) => {
            if (Array.isArray(value)) {
                return value.map((entry) => validationValueText(entry)).join(", ");
            }
            if (value === undefined || value === null) {
                return "";
            }
            return String(value);
        };

        const parseValidationLiteral = (rawValue) => {
            const value = rawValue.trim();
            if (value === "true") {
                return true;
            }
            if (value === "false") {
                return false;
            }
            if (value !== "" && Number.isFinite(Number(value))) {
                return Number(value);
            }
            return value;
        };

        const parseValidationBound = (rawValue) => {
            const value = rawValue.trim();
            if (value === "") {
                return "";
            }
            return Number.isFinite(Number(value)) ? Number(value) : value;
        };

        const createContractFieldRow = (field = {}) => {
            const validations = field?.validations && typeof field.validations === "object" ? field.validations : {};
            const row = document.createElement("div");
            row.className = "builder-contract-field-editor";
            row.setAttribute("data-contract-field-row", "true");

            const grid = document.createElement("div");
            grid.className = "grid grid-cols-[1fr_104px] gap-2";

            const nameLabel = document.createElement("label");
            nameLabel.className = "form-control";
            const nameText = document.createElement("span");
            nameText.className = "label-text text-xs";
            nameText.textContent = "Field";
            const nameInput = document.createElement("input");
            nameInput.className = "input input-bordered input-xs font-mono";
            nameInput.setAttribute("data-contract-field-name", "true");
            nameInput.value = typeof field.name === "string" ? field.name : "";
            nameLabel.append(nameText, nameInput);

            const typeLabel = document.createElement("label");
            typeLabel.className = "form-control";
            const typeText = document.createElement("span");
            typeText.className = "label-text text-xs";
            typeText.textContent = "Type";
            const typeSelect = document.createElement("select");
            typeSelect.className = "select select-bordered select-xs";
            typeSelect.setAttribute("data-contract-field-type", "true");
            const selectedType = CONTRACT_FIELD_TYPES.includes(field.type) ? field.type : "string";
            CONTRACT_FIELD_TYPES.forEach((type) => {
                appendSelectOption(typeSelect, type, type, selectedType);
            });
            typeLabel.append(typeText, typeSelect);

            const toggles = document.createElement("div");
            toggles.className = "mt-2 grid grid-cols-2 gap-2";
            const requiredLabel = document.createElement("label");
            requiredLabel.className = "label cursor-pointer justify-start gap-2 py-0";
            const requiredInput = document.createElement("input");
            requiredInput.className = "checkbox checkbox-xs";
            requiredInput.type = "checkbox";
            requiredInput.checked = field.required !== false;
            requiredInput.setAttribute("data-contract-field-required", "true");
            const requiredText = document.createElement("span");
            requiredText.className = "label-text text-xs";
            requiredText.textContent = "Required";
            requiredLabel.append(requiredInput, requiredText);

            const nullableLabel = document.createElement("label");
            nullableLabel.className = "label cursor-pointer justify-start gap-2 py-0";
            const nullableInput = document.createElement("input");
            nullableInput.className = "checkbox checkbox-xs";
            nullableInput.type = "checkbox";
            nullableInput.checked = Boolean(field.nullable);
            nullableInput.setAttribute("data-contract-field-nullable", "true");
            const nullableText = document.createElement("span");
            nullableText.className = "label-text text-xs";
            nullableText.textContent = "Nullable";
            nullableLabel.append(nullableInput, nullableText);
            toggles.append(requiredLabel, nullableLabel);

            const validationGrid = document.createElement("div");
            validationGrid.className = "mt-2 grid grid-cols-2 gap-2";

            const formatLabel = document.createElement("label");
            formatLabel.className = "form-control";
            const formatText = document.createElement("span");
            formatText.className = "label-text text-xs";
            formatText.textContent = "Format";
            const formatSelect = document.createElement("select");
            formatSelect.className = "select select-bordered select-xs";
            formatSelect.setAttribute("data-contract-validation-format", "true");
            const selectedFormat =
                typeof validations.format === "string" &&
                CONTRACT_VALIDATION_FORMATS.some((option) => option.value === validations.format)
                    ? validations.format
                    : "";
            CONTRACT_VALIDATION_FORMATS.forEach((option) => {
                appendSelectOption(formatSelect, option.value, option.label, selectedFormat);
            });
            formatLabel.append(formatText, formatSelect);

            const enumLabel = document.createElement("label");
            enumLabel.className = "form-control";
            const enumText = document.createElement("span");
            enumText.className = "label-text text-xs";
            enumText.textContent = "Enum";
            const enumInput = document.createElement("input");
            enumInput.className = "input input-bordered input-xs font-mono";
            enumInput.placeholder = "active, paused";
            enumInput.setAttribute("data-contract-validation-enum", "true");
            enumInput.value = validationValueText(validations.enum);
            enumLabel.append(enumText, enumInput);

            const minLabel = document.createElement("label");
            minLabel.className = "form-control";
            const minText = document.createElement("span");
            minText.className = "label-text text-xs";
            minText.textContent = "Min";
            const minInput = document.createElement("input");
            minInput.className = "input input-bordered input-xs font-mono";
            minInput.setAttribute("data-contract-validation-min", "true");
            minInput.value = validationValueText(validations.min);
            minLabel.append(minText, minInput);

            const maxLabel = document.createElement("label");
            maxLabel.className = "form-control";
            const maxText = document.createElement("span");
            maxText.className = "label-text text-xs";
            maxText.textContent = "Max";
            const maxInput = document.createElement("input");
            maxInput.className = "input input-bordered input-xs font-mono";
            maxInput.setAttribute("data-contract-validation-max", "true");
            maxInput.value = validationValueText(validations.max);
            maxLabel.append(maxText, maxInput);

            const minLengthLabel = document.createElement("label");
            minLengthLabel.className = "form-control";
            const minLengthText = document.createElement("span");
            minLengthText.className = "label-text text-xs";
            minLengthText.textContent = "Min length";
            const minLengthInput = document.createElement("input");
            minLengthInput.className = "input input-bordered input-xs font-mono";
            minLengthInput.type = "number";
            minLengthInput.min = "0";
            minLengthInput.setAttribute("data-contract-validation-min-length", "true");
            minLengthInput.value = validationValueText(validations.min_length);
            minLengthLabel.append(minLengthText, minLengthInput);

            const maxLengthLabel = document.createElement("label");
            maxLengthLabel.className = "form-control";
            const maxLengthText = document.createElement("span");
            maxLengthText.className = "label-text text-xs";
            maxLengthText.textContent = "Max length";
            const maxLengthInput = document.createElement("input");
            maxLengthInput.className = "input input-bordered input-xs font-mono";
            maxLengthInput.type = "number";
            maxLengthInput.min = "0";
            maxLengthInput.setAttribute("data-contract-validation-max-length", "true");
            maxLengthInput.value = validationValueText(validations.max_length);
            maxLengthLabel.append(maxLengthText, maxLengthInput);

            validationGrid.append(formatLabel, enumLabel, minLabel, maxLabel, minLengthLabel, maxLengthLabel);

            const bottom = document.createElement("div");
            bottom.className = "mt-2 grid grid-cols-[1fr_auto] items-end gap-2";
            const descriptionLabel = document.createElement("label");
            descriptionLabel.className = "form-control";
            const descriptionText = document.createElement("span");
            descriptionText.className = "label-text text-xs";
            descriptionText.textContent = "Description";
            const descriptionInput = document.createElement("input");
            descriptionInput.className = "input input-bordered input-xs";
            descriptionInput.setAttribute("data-contract-field-description", "true");
            descriptionInput.value = typeof field.description === "string" ? field.description : "";
            descriptionLabel.append(descriptionText, descriptionInput);

            const uniqueLabel = document.createElement("label");
            uniqueLabel.className = "label cursor-pointer justify-start gap-2 py-0";
            const uniqueInput = document.createElement("input");
            uniqueInput.className = "checkbox checkbox-xs";
            uniqueInput.type = "checkbox";
            uniqueInput.checked = Boolean(validations.unique);
            uniqueInput.setAttribute("data-contract-validation-unique", "true");
            const uniqueText = document.createElement("span");
            uniqueText.className = "label-text text-xs";
            uniqueText.textContent = "Unique";
            uniqueLabel.append(uniqueInput, uniqueText);
            bottom.append(descriptionLabel);

            const actions = document.createElement("div");
            actions.className = "mt-2 flex justify-end";
            const removeButton = document.createElement("button");
            removeButton.className = "btn btn-ghost btn-xs";
            removeButton.type = "button";
            removeButton.textContent = "Remove field";
            removeButton.setAttribute("data-contract-remove-field", "true");
            actions.appendChild(removeButton);

            grid.append(nameLabel, typeLabel);
            const rules = document.createElement("details");
            rules.className = "builder-contract-rules";
            rules.open = Object.keys(validations).length > 0;
            const rulesSummary = document.createElement("summary");
            rulesSummary.textContent = "Validation rules";
            bottom.className = "mt-3";
            validationGrid.appendChild(uniqueLabel);
            rules.append(rulesSummary, validationGrid);
            row.append(grid, toggles, bottom, rules, actions);
            return row;
        };

        const renderContractEditor = (editorElement, contract = {}) => {
            if (!(editorElement instanceof HTMLElement)) {
                return;
            }
            clearChildren(editorElement);
            const value = contract && typeof contract === "object" && !Array.isArray(contract) ? contract : {};

            const shell = document.createElement("div");
            shell.className = "space-y-2";

            const topGrid = document.createElement("div");
            topGrid.className = "grid grid-cols-[1fr_112px] gap-2";

            const nameLabel = document.createElement("label");
            nameLabel.className = "form-control";
            const nameText = document.createElement("span");
            nameText.className = "label-text text-xs";
            nameText.textContent = "Contract name";
            const nameInput = document.createElement("input");
            nameInput.className = "input input-bordered input-xs font-mono";
            nameInput.placeholder = "auto on export";
            nameInput.setAttribute("data-contract-name", "true");
            nameInput.value = typeof value.name === "string" ? value.name : "";
            nameLabel.append(nameText, nameInput);

            const representationLabel = document.createElement("label");
            representationLabel.className = "form-control";
            const representationText = document.createElement("span");
            representationText.className = "label-text text-xs";
            representationText.textContent = "Shape";
            const representationSelect = document.createElement("select");
            representationSelect.className = "select select-bordered select-xs";
            representationSelect.setAttribute("data-contract-representation", "true");
            const selectedRepresentation =
                typeof value.representation === "string" ? value.representation.trim().toLowerCase() : "";
            CONTRACT_REPRESENTATIONS.forEach((option) => {
                appendSelectOption(representationSelect, option.value, option.label, selectedRepresentation);
            });
            representationLabel.append(representationText, representationSelect);
            topGrid.append(nameLabel, representationLabel);

            const rootShapeLabel = document.createElement("label");
            rootShapeLabel.className = "form-control";
            rootShapeLabel.classList.toggle("hidden", selectedRepresentation !== "json");
            const rootShapeText = document.createElement("span");
            rootShapeText.className = "label-text text-xs";
            rootShapeText.textContent = "JSON root";
            const rootShapeSelect = document.createElement("select");
            rootShapeSelect.className = "select select-bordered select-xs";
            rootShapeSelect.setAttribute("data-contract-root-shape", "true");
            const selectedRootShape = typeof value.root_shape === "string" ? value.root_shape : "";
            [
                { value: "", label: "Any" },
                { value: "array", label: "Array" },
                { value: "object", label: "Object" },
            ].forEach((option) => {
                appendSelectOption(rootShapeSelect, option.value, option.label, selectedRootShape);
            });
            rootShapeLabel.append(rootShapeText, rootShapeSelect);

            const fieldsHeader = document.createElement("div");
            fieldsHeader.className = "mt-2 flex items-center justify-between gap-2";
            const fieldsTitle = document.createElement("p");
            fieldsTitle.className = "text-xs font-semibold text-base-content";
            fieldsTitle.textContent = "Fields and validations";
            const addFieldButton = document.createElement("button");
            addFieldButton.className = "btn btn-xs btn-outline";
            addFieldButton.type = "button";
            addFieldButton.textContent = "Add field";
            addFieldButton.setAttribute("data-contract-add-field", "true");
            fieldsHeader.append(fieldsTitle, addFieldButton);

            const rows = document.createElement("div");
            rows.className = "mt-2 space-y-2";
            rows.setAttribute("data-contract-field-rows", "true");
            const fields = Array.isArray(value.fields) ? value.fields : [];
            if (fields.length === 0) {
                const empty = document.createElement("p");
                empty.className = "text-xs text-base-content/60";
                empty.textContent = "No fields declared.";
                rows.appendChild(empty);
            } else {
                fields.forEach((field) => {
                    rows.appendChild(createContractFieldRow(field));
                });
            }

            shell.append(topGrid, rootShapeLabel, fieldsHeader, rows);
            editorElement.appendChild(shell);
        };

        const readContractEditor = (editorElement) => {
            if (!(editorElement instanceof HTMLElement)) {
                return { ok: true, value: {} };
            }
            const nameInput = editorElement.querySelector("[data-contract-name]");
            const representationSelect = editorElement.querySelector("[data-contract-representation]");
            const rootShapeSelect = editorElement.querySelector("[data-contract-root-shape]");
            const name = nameInput instanceof HTMLInputElement ? nameInput.value.trim() : "";
            const representation =
                representationSelect instanceof HTMLSelectElement ? representationSelect.value.trim() : "";
            const rootShape = rootShapeSelect instanceof HTMLSelectElement ? rootShapeSelect.value.trim() : "";
            const fields = [];

            for (const row of Array.from(editorElement.querySelectorAll("[data-contract-field-row]"))) {
                const fieldNameInput = row.querySelector("[data-contract-field-name]");
                const fieldTypeSelect = row.querySelector("[data-contract-field-type]");
                const requiredInput = row.querySelector("[data-contract-field-required]");
                const nullableInput = row.querySelector("[data-contract-field-nullable]");
                const descriptionInput = row.querySelector("[data-contract-field-description]");
                const formatSelect = row.querySelector("[data-contract-validation-format]");
                const enumInput = row.querySelector("[data-contract-validation-enum]");
                const minInput = row.querySelector("[data-contract-validation-min]");
                const maxInput = row.querySelector("[data-contract-validation-max]");
                const minLengthInput = row.querySelector("[data-contract-validation-min-length]");
                const maxLengthInput = row.querySelector("[data-contract-validation-max-length]");
                const uniqueInput = row.querySelector("[data-contract-validation-unique]");

                const fieldName = fieldNameInput instanceof HTMLInputElement ? fieldNameInput.value.trim() : "";
                const fieldType =
                    fieldTypeSelect instanceof HTMLSelectElement && CONTRACT_FIELD_TYPES.includes(fieldTypeSelect.value)
                        ? fieldTypeSelect.value
                        : "string";
                const hasValidationValues = [
                    formatSelect instanceof HTMLSelectElement ? formatSelect.value.trim() : "",
                    enumInput instanceof HTMLInputElement ? enumInput.value.trim() : "",
                    minInput instanceof HTMLInputElement ? minInput.value.trim() : "",
                    maxInput instanceof HTMLInputElement ? maxInput.value.trim() : "",
                    minLengthInput instanceof HTMLInputElement ? minLengthInput.value.trim() : "",
                    maxLengthInput instanceof HTMLInputElement ? maxLengthInput.value.trim() : "",
                    uniqueInput instanceof HTMLInputElement && uniqueInput.checked ? "unique" : "",
                ].some((entry) => entry !== "");
                const description = descriptionInput instanceof HTMLInputElement ? descriptionInput.value.trim() : "";

                if (fieldName === "" && !hasValidationValues && description === "") {
                    continue;
                }
                if (fieldName === "") {
                    return { ok: false, error: "Contract field name is required." };
                }

                const field = {
                    name: fieldName,
                    type: fieldType,
                    required: !(requiredInput instanceof HTMLInputElement) || requiredInput.checked,
                };
                if (nullableInput instanceof HTMLInputElement && nullableInput.checked) {
                    field.nullable = true;
                }
                if (description !== "") {
                    field.description = description;
                }

                const validations = {};
                if (formatSelect instanceof HTMLSelectElement && formatSelect.value.trim() !== "") {
                    validations.format = formatSelect.value.trim();
                }
                if (enumInput instanceof HTMLInputElement && enumInput.value.trim() !== "") {
                    validations.enum = enumInput.value
                        .split(",")
                        .map((entry) => entry.trim())
                        .filter((entry) => entry !== "")
                        .map(parseValidationLiteral);
                    if (validations.enum.length === 0) {
                        delete validations.enum;
                    }
                }
                if (minInput instanceof HTMLInputElement && minInput.value.trim() !== "") {
                    validations.min = parseValidationBound(minInput.value);
                }
                if (maxInput instanceof HTMLInputElement && maxInput.value.trim() !== "") {
                    validations.max = parseValidationBound(maxInput.value);
                }
                if (minLengthInput instanceof HTMLInputElement && minLengthInput.value.trim() !== "") {
                    const parsed = Number.parseInt(minLengthInput.value, 10);
                    if (!Number.isInteger(parsed) || parsed < 0) {
                        return { ok: false, error: "Minimum length must be zero or greater." };
                    }
                    validations.min_length = parsed;
                }
                if (maxLengthInput instanceof HTMLInputElement && maxLengthInput.value.trim() !== "") {
                    const parsed = Number.parseInt(maxLengthInput.value, 10);
                    if (!Number.isInteger(parsed) || parsed < 0) {
                        return { ok: false, error: "Maximum length must be zero or greater." };
                    }
                    validations.max_length = parsed;
                }
                if (uniqueInput instanceof HTMLInputElement && uniqueInput.checked) {
                    validations.unique = true;
                }
                if (Object.keys(validations).length > 0) {
                    field.validations = validations;
                }

                fields.push(field);
            }

            if (name === "" && representation === "" && rootShape === "" && fields.length === 0) {
                return { ok: true, value: {} };
            }

            const contract = {};
            if (name !== "") {
                contract.name = name;
            }
            if (representation !== "") {
                contract.representation = representation;
            } else if (fields.length > 0) {
                contract.representation = "rows";
            }
            if (rootShape !== "" && contract.representation === "json") {
                contract.root_shape = rootShape;
            }
            if (fields.length > 0) {
                contract.fields = fields;
            }
            return { ok: true, value: contract };
        };

        const syncContractEditorToField = (editorElement, fieldElement, errorElement) => {
            const result = readContractEditor(editorElement);
            if (!result.ok) {
                if (errorElement instanceof HTMLElement) {
                    errorElement.textContent = result.error || "Invalid contract.";
                }
                return false;
            }
            writeJSONObjectField(fieldElement, result.value);
            if (errorElement instanceof HTMLElement) {
                errorElement.textContent = "";
            }
            return true;
        };

        const syncIncrementalModeUI = (kind) => {
            const normalized = typeof kind === "string" ? kind.trim().toLowerCase() : "";
            const hasIncrementalMode = normalized === "watermark" || normalized === "cursor";
            if (queryIncrementalFieldGroup instanceof HTMLElement) {
                queryIncrementalFieldGroup.classList.toggle("hidden", normalized !== "watermark");
            }
            if (queryIncrementalCursorGroup instanceof HTMLElement) {
                queryIncrementalCursorGroup.classList.toggle("hidden", normalized !== "cursor");
            }
            [fieldQueryIncrementalLookback, fieldQueryIncrementalOrdering, fieldQueryIncrementalDedupe].forEach(
                (field) => {
                    const group = field instanceof HTMLElement ? field.closest("label.form-control") : null;
                    if (group instanceof HTMLElement) {
                        group.classList.toggle("hidden", !hasIncrementalMode);
                    }
                    if (field instanceof HTMLInputElement) {
                        field.disabled = !hasIncrementalMode;
                    }
                },
            );
        };

        const writeIncrementalEditor = (incremental) => {
            const value =
                incremental && typeof incremental === "object" && !Array.isArray(incremental) ? incremental : {};
            const declaredKind =
                typeof value.checkpoint_kind === "string" ? value.checkpoint_kind.trim().toLowerCase() : "";
            const kind =
                declaredKind === "watermark" || declaredKind === "cursor"
                    ? declaredKind
                    : typeof value.field === "string" && value.field.trim() !== ""
                      ? "watermark"
                      : typeof value.cursor === "string" && value.cursor.trim() !== ""
                        ? "cursor"
                        : "";
            if (fieldQueryIncrementalKind instanceof HTMLSelectElement) {
                fieldQueryIncrementalKind.value = kind === "watermark" || kind === "cursor" ? kind : "";
            }
            if (fieldQueryIncrementalField instanceof HTMLInputElement) {
                fieldQueryIncrementalField.value = typeof value.field === "string" ? value.field : "";
            }
            if (fieldQueryIncrementalCursor instanceof HTMLInputElement) {
                fieldQueryIncrementalCursor.value = typeof value.cursor === "string" ? value.cursor : "";
            }
            if (fieldQueryIncrementalLookback instanceof HTMLInputElement) {
                fieldQueryIncrementalLookback.value =
                    typeof value.lookback_window === "string" ? value.lookback_window : "";
            }
            if (fieldQueryIncrementalOrdering instanceof HTMLInputElement) {
                fieldQueryIncrementalOrdering.value = Array.isArray(value.ordering)
                    ? value.ordering.filter((entry) => typeof entry === "string" && entry.trim() !== "").join(", ")
                    : "";
            }
            if (fieldQueryIncrementalDedupe instanceof HTMLInputElement) {
                fieldQueryIncrementalDedupe.value = typeof value.dedupe_key === "string" ? value.dedupe_key : "";
            }
            if (errorQueryIncremental instanceof HTMLElement) {
                const hasCheckpointOptions = Boolean(
                    value.lookback_window ||
                        value.dedupe_key ||
                        (Array.isArray(value.ordering) && value.ordering.length > 0),
                );
                errorQueryIncremental.textContent =
                    declaredKind === "" && kind === "" && hasCheckpointOptions
                        ? "Select watermark or cursor mode to use the existing checkpoint options."
                        : "";
            }
            syncIncrementalModeUI(kind);
        };

        const readIncrementalEditor = () => {
            const kind =
                fieldQueryIncrementalKind instanceof HTMLSelectElement
                    ? fieldQueryIncrementalKind.value.trim().toLowerCase()
                    : "";
            if (kind === "") {
                syncIncrementalModeUI(kind);
                return { ok: true, value: {} };
            }
            if (kind !== "watermark" && kind !== "cursor") {
                if (errorQueryIncremental instanceof HTMLElement) {
                    errorQueryIncremental.textContent = "Checkpoint mode must be watermark or cursor.";
                }
                syncIncrementalModeUI(kind);
                return { ok: false, value: {} };
            }

            const value = { checkpoint_kind: kind };
            const field =
                fieldQueryIncrementalField instanceof HTMLInputElement ? fieldQueryIncrementalField.value.trim() : "";
            const cursor =
                fieldQueryIncrementalCursor instanceof HTMLInputElement ? fieldQueryIncrementalCursor.value.trim() : "";
            if (kind === "watermark") {
                if (field === "") {
                    if (errorQueryIncremental instanceof HTMLElement) {
                        errorQueryIncremental.textContent = "Watermark checkpoints require a field.";
                    }
                    syncIncrementalModeUI(kind);
                    return { ok: false, value: {} };
                }
                value.field = field;
            }
            if (kind === "cursor") {
                if (cursor === "") {
                    if (errorQueryIncremental instanceof HTMLElement) {
                        errorQueryIncremental.textContent = "Cursor checkpoints require a cursor name.";
                    }
                    syncIncrementalModeUI(kind);
                    return { ok: false, value: {} };
                }
                value.cursor = cursor;
            }

            const lookback =
                fieldQueryIncrementalLookback instanceof HTMLInputElement
                    ? fieldQueryIncrementalLookback.value.trim()
                    : "";
            const ordering =
                fieldQueryIncrementalOrdering instanceof HTMLInputElement
                    ? fieldQueryIncrementalOrdering.value
                          .split(/[,\n]/)
                          .map((entry) => entry.trim())
                          .filter(Boolean)
                    : [];
            const dedupe =
                fieldQueryIncrementalDedupe instanceof HTMLInputElement ? fieldQueryIncrementalDedupe.value.trim() : "";

            if (lookback !== "") {
                value.lookback_window = lookback;
            }
            if (ordering.length > 0) {
                value.ordering = ordering;
            }
            if (dedupe !== "") {
                value.dedupe_key = dedupe;
            }

            syncIncrementalModeUI(kind);
            return { ok: true, value };
        };

        const ensureSelectHasOption = (selectElement, selectedValue, labelSuffix = "referenced") => {
            if (!(selectElement instanceof HTMLSelectElement)) {
                return;
            }
            const normalizedSelected = typeof selectedValue === "string" ? selectedValue.trim() : "";
            if (normalizedSelected === "") {
                return;
            }
            const hasSelectedOption = Array.from(selectElement.options).some(
                (option) => option.value === normalizedSelected,
            );
            if (hasSelectedOption) {
                return;
            }
            const option = document.createElement("option");
            option.value = normalizedSelected;
            option.textContent = `${normalizedSelected} (${labelSuffix})`;
            selectElement.appendChild(option);
        };

        const setSelectOptions = (
            selectElement,
            options,
            placeholder,
            selectedValue,
            missingLabelSuffix = "not in current options",
        ) => {
            if (!(selectElement instanceof HTMLSelectElement)) {
                return;
            }
            const normalizedSelected = typeof selectedValue === "string" ? selectedValue.trim() : "";

            clearChildren(selectElement);
            const placeholderOption = document.createElement("option");
            placeholderOption.value = "";
            placeholderOption.textContent = placeholder;
            selectElement.appendChild(placeholderOption);

            options.forEach((option) => {
                const element = document.createElement("option");
                element.value = option.value;
                element.textContent = option.label;
                selectElement.appendChild(element);
            });

            ensureSelectHasOption(selectElement, normalizedSelected, missingLabelSuffix);
            selectElement.value =
                normalizedSelected !== "" &&
                Array.from(selectElement.options).some((option) => option.value === normalizedSelected)
                    ? normalizedSelected
                    : "";
        };

        const syncQuerySourceModeUI = (useInlineSQL) => {
            const sourceSavedQueryGroup = getSavedQueryGroup();
            const sourceInlineSQLGroup = getInlineSQLGroup();
            const fieldInlineSQLToggle = getInlineSQLToggleField();
            if (sourceSavedQueryGroup instanceof HTMLElement) {
                sourceSavedQueryGroup.classList.toggle("hidden", useInlineSQL);
            }
            if (sourceInlineSQLGroup instanceof HTMLElement) {
                sourceInlineSQLGroup.classList.toggle("hidden", !useInlineSQL);
            }
            if (fieldInlineSQLToggle instanceof HTMLInputElement) {
                fieldInlineSQLToggle.checked = useInlineSQL;
            }
        };

        const syncOperationSourceModeUI = (useInlineOperation) => {
            const sourceSavedQueryGroup = getSavedQueryGroup();
            const inlineOperationGroup = getInlineOperationGroup();
            const fieldInlineOperationToggle = getInlineOperationToggleField();
            if (sourceSavedQueryGroup instanceof HTMLElement) {
                sourceSavedQueryGroup.classList.toggle("hidden", useInlineOperation);
            }
            if (inlineOperationGroup instanceof HTMLElement) {
                inlineOperationGroup.classList.toggle("hidden", !useInlineOperation);
            }
            if (fieldInlineOperationToggle instanceof HTMLInputElement) {
                fieldInlineOperationToggle.checked = useInlineOperation;
            }
        };

        const syncSinkOperationModeUI = (useInlineOperation) => {
            const sinkSavedQueryGroup = getSinkSavedQueryGroup();
            const sinkInlineOperationGroup = getSinkInlineOperationGroup();
            const sinkInlineOperationToggle = getSinkInlineOperationToggleField();
            if (sinkSavedQueryGroup instanceof HTMLElement) {
                sinkSavedQueryGroup.classList.toggle("hidden", useInlineOperation);
            }
            if (sinkInlineOperationGroup instanceof HTMLElement) {
                sinkInlineOperationGroup.classList.toggle("hidden", !useInlineOperation);
            }
            if (sinkInlineOperationToggle instanceof HTMLInputElement) {
                sinkInlineOperationToggle.checked = useInlineOperation;
            }
        };

        let dataSourceOptions = [];
        let sinkDataSourceOptions = [];
        // Keys remain authored logical refs; physical slugs are configuration metadata only.
        const dataSourceMetaBySlug = new Map();
        let dataSourceConfiguration = null;
        const referenceConfiguration = (ref) =>
            builderAPI.referenceConfiguration(ref, dataSourceConfiguration, getActiveTargetEnvironmentSlug());
        const queryOptionsByDataSource = new Map();
        const optionDescriptorMap = connectorDescriptorMap(
            Array.isArray(options?.connectorDescriptors) ? options.connectorDescriptors : [],
        );
        const connectorDescriptorByType =
            optionDescriptorMap.size > 0 ? optionDescriptorMap : readConnectorDescriptorPayload(document);

        const queryOperationTypeFromDataSourceType = (dataSourceType) => {
            return (
                descriptorForDataSourceType(dataSourceType, connectorDescriptorByType)?.queryOperationType ||
                fallbackQueryOperationTypeFromDataSourceType(dataSourceType)
            );
        };

        const guidedOperationTypeFromDataSourceType = (dataSourceType) => {
            if (normalizeConnectorString(dataSourceType) === "google_workspace") return "google_workspace";
            const descriptor = descriptorForDataSourceType(dataSourceType, connectorDescriptorByType);
            const schemaFamily = descriptor?.schemaFamily || legacySchemaFamilyFromDataSourceType(dataSourceType);
            if (schemaFamily === "file" || schemaFamily === "object_store") {
                return (
                    descriptor?.queryOperationType || fallbackQueryOperationTypeFromDataSourceType(dataSourceType, "")
                );
            }
            return "";
        };

        const boolInputChecked = (container, name, fallback = false) => {
            const input = container.querySelector(`[data-guided-field="${name}"]`);
            if (input instanceof HTMLInputElement) {
                return input.checked;
            }
            return fallback;
        };

        const stringInputValue = (container, name) => {
            const input = container.querySelector(`[data-guided-field="${name}"]`);
            if (
                input instanceof HTMLInputElement ||
                input instanceof HTMLSelectElement ||
                input instanceof HTMLTextAreaElement
            ) {
                return input.value.trim();
            }
            return "";
        };

        const buildGuidedInlineOperation = (container, operationType, editorMode) => {
            if (!(container instanceof HTMLElement)) {
                return {};
            }

            const isSource = editorMode === "source";

            if (operationType === "google_workspace") {
                const resource = stringInputValue(container, "resource") || "spreadsheet";
                const op = stringInputValue(container, "operation") || (isSource ? "read" : "update");
                const operation = { type: "google_workspace", resource, operation: op };
                if (resource === "spreadsheet") {
                    const raw = stringInputValue(container, "worksheet_id"),
                        id = Number(raw);
                    if (!raw || !Number.isSafeInteger(id) || id < 0)
                        throw new RangeError("Choose a non-negative worksheet ID.");
                    operation.spreadsheet_id = stringInputValue(container, "spreadsheet_id");
                    operation.worksheet_id = id;
                    operation.range = stringInputValue(container, "range");
                    if (op === "update") {
                        operation.columns = JSON.parse(stringInputValue(container, "columns") || "[]");
                        if (
                            !Array.isArray(operation.columns) ||
                            !operation.columns.every((column) => typeof column === "string")
                        ) {
                            throw new TypeError("Columns must be a JSON array of strings.");
                        }
                        const rows = stringInputValue(container, "rows");
                        operation.rows = rows === "{{input}}" ? rows : JSON.parse(rows || "[]");
                        if (operation.rows !== "{{input}}" && !Array.isArray(operation.rows)) {
                            throw new TypeError("Rows must be an array or {{input}}.");
                        }
                    }
                } else if (["document", "presentation"].includes(resource)) {
                    if (op === "create") {
                        operation.folder_id = stringInputValue(container, "folder_id");
                        operation.name = stringInputValue(container, "name");
                    } else {
                        operation.file_id = stringInputValue(container, "file_id");
                        if (op === "update") {
                            const match = container.querySelector('[data-guided-field="match_text"]');
                            operation.match_text = match instanceof HTMLTextAreaElement ? match.value : "";
                            const content = stringInputValue(container, "content");
                            try {
                                operation.content = content === "{{input}}" ? content : JSON.parse(content || '""');
                            } catch {
                                throw new RangeError("Native replacement must be a JSON string or {{input}}.");
                            }
                            if (typeof operation.content !== "string")
                                throw new RangeError("Native replacement must be a JSON string or {{input}}.");
                        }
                    }
                } else {
                    if (op === "list") {
                        for (const key of ["folder_id", "drive_id", "mime_type", "page_token"]) {
                            const value = stringInputValue(container, key);
                            if (value) operation[key] = value;
                        }
                    } else {
                        operation.format = stringInputValue(container, "format") || "json";
                        if (op === "create") {
                            operation.folder_id = stringInputValue(container, "folder_id");
                            operation.name = stringInputValue(container, "name");
                        } else {
                            operation.file_id = stringInputValue(container, "file_id");
                        }
                        if (op !== "read") {
                            const content = stringInputValue(container, "content");
                            operation.content = content === "{{input}}" ? content : JSON.parse(content);
                            const mime = stringInputValue(container, "mime_type");
                            if (mime) operation.mime_type = mime;
                        }
                        if (operation.format === "csv") {
                            const delimiter = stringInputValue(container, "delimiter");
                            if (delimiter) operation.delimiter = delimiter;
                            const header = stringInputValue(container, "has_header");
                            if (header) operation.has_header = header === "true";
                        }
                    }
                }
                return operation;
            }
            if (operationType === "ftp") {
                const operation = {
                    type: "ftp",
                    operation: isSource ? "read" : "write",
                };
                const path = stringInputValue(container, "path");
                const delimiter = stringInputValue(container, "delimiter");
                const overwrite = boolInputChecked(container, "overwrite", true);
                if (path !== "") {
                    operation.path = path;
                }
                if (isSource && delimiter !== "") {
                    operation.delimiter = delimiter;
                }
                if (isSource) {
                    operation.has_header = boolInputChecked(container, "has_header", false);
                }
                if (!isSource) {
                    operation.overwrite = overwrite;
                }

                return operation;
            }

            if (operationType === "git") {
                const operation = {
                    type: "git",
                    operation: isSource ? "read" : "write",
                };
                const path = stringInputValue(container, "path");
                const delimiter = stringInputValue(container, "delimiter");
                const branch = stringInputValue(container, "branch");
                const message = stringInputValue(container, "message");
                const overwrite = boolInputChecked(container, "overwrite", true);
                if (path !== "") {
                    operation.path = path;
                }
                if (isSource && delimiter !== "") {
                    operation.delimiter = delimiter;
                }
                if (isSource) {
                    operation.has_header = boolInputChecked(container, "has_header", false);
                }
                if (branch !== "") {
                    operation.branch = branch;
                }
                if (message !== "") {
                    operation.message = message;
                }
                if (!isSource) {
                    operation.overwrite = overwrite;
                }
                return operation;
            }

            if (operationType === "s3") {
                const operation = {
                    type: "s3",
                    operation: isSource ? "read" : "write",
                };
                const key = stringInputValue(container, "key");
                const format = stringInputValue(container, "format");
                const overwrite = boolInputChecked(container, "overwrite", true);
                if (key !== "") {
                    operation.key = key;
                }
                if (format !== "") {
                    operation.format = format;
                }
                if (!isSource) {
                    operation.overwrite = overwrite;
                }
                return operation;
            }

            return {};
        };

        const renderGuidedInlineOperationEditor = (
            container,
            jsonFieldGroup,
            inlineOperationField,
            operationType,
            value,
            editorMode,
            onChange,
        ) => {
            if (!(container instanceof HTMLElement)) {
                return;
            }

            const isSource = editorMode === "source";

            if (
                operationType !== "ftp" &&
                operationType !== "git" &&
                operationType !== "s3" &&
                operationType !== "google_workspace"
            ) {
                container.classList.add("hidden");
                container.innerHTML = "";
                if (jsonFieldGroup instanceof HTMLElement) {
                    jsonFieldGroup.classList.remove("hidden");
                }
                return;
            }

            if (jsonFieldGroup instanceof HTMLElement) {
                jsonFieldGroup.classList.add("hidden");
            }
            container.classList.remove("hidden");

            container
                .querySelector("[data-workspace-operation]")
                ?.dispatchEvent(new Event("htmx:beforeCleanupElement"));
            const operation = safeOperationObject(value);
            if (operationType === "google_workspace") {
                const input = (key, label, group) =>
                    `<label class="form-control" data-workspace-fields="${group}"><span class="label-text text-xs">${label}</span><input class="input input-bordered input-sm" name="workspace_${key}" data-guided-field="${key}" value="${escapeHTML(String(operation[key] ?? ""))}"/></label>`;
                const text = (key, label, group) => {
                    const value =
                        key === "match_text"
                            ? String(operation[key] ?? "")
                            : operation[key] === undefined
                              ? "{{input}}"
                              : operation[key];
                    const encoded =
                        key === "content" &&
                        ["document", "presentation"].includes(operation.resource) &&
                        value !== "{{input}}"
                            ? JSON.stringify(value)
                            : typeof value === "string"
                              ? value
                              : JSON.stringify(value);
                    const initial =
                        key === "match_text" ? ` data-workspace-initial-match-text="${escapeHTML(String(value))}"` : "";
                    return `<label class="form-control" data-workspace-fields="${group}"><span class="label-text text-xs">${label}</span><textarea class="textarea textarea-bordered text-xs" name="workspace_${key}" data-guided-field="${key}"${initial}>${escapeHTML(encoded)}</textarea></label>`;
                };
                const select = (key, label, values, defaultValue) =>
                    `<label class="form-control"><span class="label-text text-xs">${label}</span><select class="select select-bordered select-sm" aria-label="${escapeHTML(label)}" name="workspace_${key}" data-guided-field="${key}">${values.map((value) => `<option value="${escapeHTML(value)}" ${value === String(operation[key] ?? defaultValue) ? "selected" : ""}>${escapeHTML(value || "Default")}</option>`).join("")}</select></label>`;
                const sourceSlug =
                    editorMode === "source" ? getDataSourceField()?.value : getSinkDataSourceField()?.value;
                container.innerHTML =
                    `<div class="space-y-3 min-w-0" data-workspace-operation data-org="${escapeHTML(getActiveOrganizationSlug())}" data-source="${escapeHTML(sourceSlug || "")}">` +
                    select("resource", "Resource", ["spreadsheet", "file", "document", "presentation"], "spreadsheet") +
                    select(
                        "operation",
                        "Operation",
                        isSource ? ["read", "list", "export"] : ["update", "create"],
                        isSource ? "read" : "update",
                    ) +
                    `<div data-workspace-fields="sheet"><input type="hidden" name="workspace_spreadsheet_id" data-guided-field="spreadsheet_id" value="${escapeHTML(operation.spreadsheet_id || "")}"/><p class="text-xs">Spreadsheet: <span data-workspace-target-name>${escapeHTML(operation.spreadsheet_id || "")}</span></p><p class="text-xs text-base-content/70 break-all">File ID: <span data-workspace-target-id>${escapeHTML(operation.spreadsheet_id || "")}</span></p></div>` +
                    `<div data-workspace-fields="sheet">${select("worksheet_id", "Worksheet", [String(operation.worksheet_id ?? "0")], String(operation.worksheet_id ?? "0"))}<p class="text-xs text-base-content/70 break-all">Worksheet ID: <span data-workspace-worksheet-id>${escapeHTML(String(operation.worksheet_id ?? "0"))}</span></p></div>` +
                    input("range", "Bounded A1 range", "sheet") +
                    (isSource
                        ? ""
                        : input("columns", "Ordered columns (JSON array)", "sheet-update") +
                          text("rows", "Rows or {{input}}", "sheet-update")) +
                    `<div data-workspace-fields="file-target"><input type="hidden" name="workspace_file_id" data-guided-field="file_id" value="${escapeHTML(operation.file_id || "")}"/><p class="text-xs">File: <span data-workspace-target-name>${escapeHTML(operation.file_id || "")}</span></p><p class="text-xs text-base-content/70 break-all">File ID: <span data-workspace-target-id>${escapeHTML(operation.file_id || "")}</span></p></div>` +
                    input("folder_id", "Folder ID", "folder") +
                    input("drive_id", "Shared drive ID (optional)", "list") +
                    input("page_token", "Continuation token (optional)", "list") +
                    input("name", "Filename", "create") +
                    input("mime_type", "MIME type (optional)", "mime") +
                    `<div data-workspace-fields="format">${select("format", "Format", ["json", "csv", "file"], "json")}</div>` +
                    input("delimiter", "CSV delimiter (optional)", "csv") +
                    `<div data-workspace-fields="csv">${select("has_header", "CSV header", ["", "true", "false"], "")}</div>` +
                    (isSource
                        ? ""
                        : text("match_text", "Exact text to replace (case-sensitive)", "native-update") +
                          text("content", "Content (native: JSON string) or {{input}}", "file-write")) +
                    `<p class="text-xs" data-workspace-fields="native">Native reads retain structure. Create makes a blank file. Update replaces exact matches and requires unsafe replay. Export produces PDF.</p>` +
                    `<p class="text-xs" data-workspace-fields="create">Create needs unsafe replay policy and always creates a new file.</p><div class="flex flex-wrap gap-2"><button type="button" class="btn btn-outline btn-sm" data-workspace-discover>Browse accessible files</button><button type="button" class="btn btn-outline btn-sm hidden" data-workspace-next>Next page</button></div><select class="select select-bordered select-sm min-w-0 w-full hidden" aria-label="Accessible Workspace files" data-workspace-files></select><p class="text-sm" role="status" data-workspace-feedback></p><p class="text-xs text-error" data-workspace-error></p></div>`;
            }
            if (operationType === "ftp") {
                const path = (operation.path || "").toString();
                const delimiter = (operation.delimiter || "").toString();
                const hasHeader = Boolean(operation.has_header);
                const overwrite = operation.overwrite === undefined ? true : Boolean(operation.overwrite);
                container.innerHTML = `
                    <label class="form-control">
                        <span class="label-text text-xs">Path</span>
                        <input class="input input-bordered input-sm font-mono" type="text" data-guided-field="path" value="${escapeHTML(path)}" placeholder="/path/to/file.csv"/>
                    </label>
                    <p class="text-[11px] text-base-content/60">Operation: <strong>${isSource ? "read" : "write"}</strong></p>
                    ${
                        isSource
                            ? `<label class="form-control">
                            <span class="label-text text-xs">Delimiter (optional)</span>
                            <input class="input input-bordered input-sm font-mono" type="text" maxlength="1" data-guided-field="delimiter" value="${escapeHTML(delimiter)}" placeholder=","/>
                        </label>`
                            : ""
                    }
                    <div class="flex flex-col gap-2 pb-1">
                        ${
                            isSource
                                ? `<label class="label cursor-pointer justify-start gap-2">
                                <input class="checkbox checkbox-sm" type="checkbox" data-guided-field="has_header" ${hasHeader ? "checked" : ""}/>
                                <span class="label-text text-xs">Has header row</span>
                            </label>`
                                : ""
                        }
                        ${
                            isSource
                                ? ""
                                : `<label class="label cursor-pointer justify-start gap-2">
                            <input class="checkbox checkbox-sm" type="checkbox" data-guided-field="overwrite" ${overwrite ? "checked" : ""}/>
                            <span class="label-text text-xs">Overwrite on write</span>
                        </label>`
                        }
                    </div>
                `;
            }

            if (operationType === "git") {
                const path = (operation.path || "").toString();
                const delimiter = (operation.delimiter || "").toString();
                const branch = (operation.branch || "").toString();
                const message = (operation.message || "").toString();
                const hasHeader = Boolean(operation.has_header);
                const overwrite = operation.overwrite === undefined ? true : Boolean(operation.overwrite);
                container.innerHTML = `
                    <label class="form-control">
                        <span class="label-text text-xs">Path</span>
                        <input class="input input-bordered input-sm font-mono" type="text" data-guided-field="path" value="${escapeHTML(path)}" placeholder="data/file.csv"/>
                    </label>
                    <p class="text-[11px] text-base-content/60">Operation: <strong>${isSource ? "read" : "write"}</strong></p>
                    <label class="form-control">
                        <span class="label-text text-xs">Branch / ref (optional)</span>
                        <input class="input input-bordered input-sm font-mono" type="text" data-guided-field="branch" value="${escapeHTML(branch)}" placeholder="main"/>
                    </label>
                    ${
                        isSource
                            ? ""
                            : `<label class="form-control">
                        <span class="label-text text-xs">Commit message (optional)</span>
                        <input class="input input-bordered input-sm" type="text" data-guided-field="message" value="${escapeHTML(message)}" placeholder="Update file"/>
                    </label>`
                    }
                    ${
                        isSource
                            ? `<label class="form-control">
                        <span class="label-text text-xs">Delimiter (optional)</span>
                        <input class="input input-bordered input-sm font-mono" type="text" maxlength="1" data-guided-field="delimiter" value="${escapeHTML(delimiter)}" placeholder=","/>
                    </label>`
                            : ""
                    }
                    <div class="flex flex-col gap-2 pb-1">
                        ${
                            isSource
                                ? `<label class="label cursor-pointer justify-start gap-2">
                            <input class="checkbox checkbox-sm" type="checkbox" data-guided-field="has_header" ${hasHeader ? "checked" : ""}/>
                            <span class="label-text text-xs">Has header row</span>
                        </label>`
                                : ""
                        }
                        ${
                            isSource
                                ? ""
                                : `<label class="label cursor-pointer justify-start gap-2">
                            <input class="checkbox checkbox-sm" type="checkbox" data-guided-field="overwrite" ${overwrite ? "checked" : ""}/>
                            <span class="label-text text-xs">Overwrite on write</span>
                        </label>`
                        }
                    </div>
                `;
            }

            if (operationType === "s3") {
                const key = (operation.key || operation.path || "").toString();
                const format = (operation.format || "").toString();
                const overwrite = operation.overwrite === undefined ? true : Boolean(operation.overwrite);
                container.innerHTML = `
                    ${
                        isSource
                            ? `<label class="form-control">
                        <span class="label-text text-xs">Object key</span>
                        <input class="input input-bordered input-sm font-mono" type="text" data-guided-field="key" value="${escapeHTML(key)}" placeholder="exports/users.csv"/>
                    </label>`
                            : ""
                    }
                    <p class="text-[11px] text-base-content/60">Operation: <strong>${isSource ? "read" : "write"}</strong></p>
                    ${
                        isSource
                            ? `<label class="form-control">
                        <span class="label-text text-xs">Format</span>
                        <input class="input input-bordered input-sm font-mono" type="text" data-guided-field="format" value="${escapeHTML(format)}" placeholder="csv"/>
                    </label>`
                            : ""
                    }
                    ${
                        isSource
                            ? ""
                            : `<label class="label cursor-pointer justify-start gap-2">
                            <input class="checkbox checkbox-sm" type="checkbox" data-guided-field="overwrite" ${overwrite ? "checked" : ""}/>
                            <span class="label-text text-xs">Overwrite on write</span>
                        </label>`
                    }
                `;
            }

            const handleGuidedInput = () => {
                const existing = safeOperationObject(inlineOperationField.value);
                let next;
                try {
                    next = buildGuidedInlineOperation(container, operationType, editorMode);
                } catch (cause) {
                    const error = container.querySelector("[data-workspace-error]");
                    if (error) {
                        error.textContent =
                            cause instanceof RangeError ? cause.message : "Use valid JSON columns and rows.";
                    }
                    return;
                }
                const workspaceError = container.querySelector("[data-workspace-error]");
                if (workspaceError) workspaceError.textContent = "";
                const merged = operationType === "google_workspace" ? next : Object.assign({}, existing, next);
                if (operationType === "google_workspace" && isSource) {
                    delete merged.columns;
                    delete merged.rows;
                }
                if (inlineOperationField instanceof HTMLTextAreaElement) {
                    inlineOperationField.value = formatJSONMap(merged);
                }
                if (typeof onChange === "function") {
                    onChange();
                }
            };

            container.querySelectorAll("[data-guided-field]").forEach((element) => {
                element.addEventListener("input", handleGuidedInput);
                element.addEventListener("change", handleGuidedInput);
            });
            if (operationType === "google_workspace") window.BraidplaneWorkspace?.init();
        };

        const renderSourceGuidedInlineEditor = (node, onChange) => {
            const dataSourceSlug = (node?.dataSourceSlug || "").trim();
            const meta = dataSourceMetaBySlug.get(dataSourceSlug);
            const operationType = guidedOperationTypeFromDataSourceType(meta?.type || "");
            renderGuidedInlineOperationEditor(
                getSourceInlineOperationGuided(),
                getSourceInlineOperationJSONField(),
                getInlineOperationField(),
                operationType,
                node?.operationInline,
                "source",
                onChange,
            );
        };

        const renderSinkGuidedInlineEditor = (node, onChange) => {
            const dataSourceSlug = (node?.sinkDataSourceSlug || "").trim();
            const meta = dataSourceMetaBySlug.get(dataSourceSlug);
            const operationType = guidedOperationTypeFromDataSourceType(meta?.type || "");
            renderGuidedInlineOperationEditor(
                getSinkInlineOperationGuided(),
                getSinkInlineOperationJSONField(),
                getSinkInlineOperationField(),
                operationType,
                node?.sinkOperationInline,
                "sink",
                onChange,
            );
        };

        const queryContextForDataSourceType = (dataSourceType) => {
            const normalized = normalizeConnectorString(dataSourceType);
            const descriptor = descriptorForDataSourceType(normalized, connectorDescriptorByType);
            const operationType =
                descriptor?.queryOperationType || fallbackQueryOperationTypeFromDataSourceType(normalized, "");
            if (descriptor && !descriptor.supportsPipelineSource) {
                return {
                    supportsInlineSQL: false,
                    supportsInlineOperationJSON: false,
                    queryRefLabel: "Saved query",
                    inlineToggleLabel: "Inline SQL is unavailable for this data source",
                    inlineOperationLabel: "inline operation",
                    hint: "This data source is not available as a pipeline source.",
                };
            }
            return queryContextForOperationType(operationType);
        };

        const applyQueryDataSourceContext = (dataSourceSlug, node) => {
            const selectedDataSourceSlug = (dataSourceSlug || "").trim();
            const meta = dataSourceMetaBySlug.get(selectedDataSourceSlug);
            const metadataDataSourceType = meta?.type || "";
            const declaredOperationType =
                node && typeof node.operationType === "string" ? normalizeConnectorString(node.operationType) : "";
            const effectiveOperationType =
                declaredOperationType ||
                (metadataDataSourceType ? queryOperationTypeFromDataSourceType(metadataDataSourceType) : "");
            const context = declaredOperationType
                ? queryContextForOperationType(declaredOperationType)
                : queryContextForDataSourceType(metadataDataSourceType);
            const queryRefLabel = getQueryRefLabel();
            const queryField = getQueryField();
            const inlineSQLToggleGroup = getInlineSQLToggleGroup();
            const inlineOperationToggleGroup = getInlineOperationToggleGroup();
            const inlineSQLToggleLabel = getInlineSQLToggleLabel();
            const inlineOperationToggleLabel = getInlineOperationToggleLabel();
            const sourceConfigHint = getSourceConfigHint();
            const inlineOperationGroup = getInlineOperationGroup();
            const inlineOperationField = getInlineOperationField();

            if (queryRefLabel instanceof HTMLElement) {
                queryRefLabel.innerHTML = selectedDataSourceSlug
                    ? `${escapeHTML(context.queryRefLabel)} <span class="text-error">*</span>`
                    : escapeHTML(context.queryRefLabel);
            }
            if (queryField instanceof HTMLInputElement || queryField instanceof HTMLSelectElement) {
                queryField.disabled = selectedDataSourceSlug === "";
            }
            if (inlineSQLToggleLabel instanceof HTMLElement) {
                inlineSQLToggleLabel.textContent = context.inlineToggleLabel;
            }
            if (inlineSQLToggleGroup instanceof HTMLElement) {
                inlineSQLToggleGroup.classList.toggle("hidden", !context.supportsInlineSQL);
            }
            if (inlineOperationToggleGroup instanceof HTMLElement) {
                inlineOperationToggleGroup.classList.toggle("hidden", !context.supportsInlineOperationJSON);
            }
            if (inlineOperationToggleLabel instanceof HTMLElement) {
                inlineOperationToggleLabel.textContent = `Use ${context.inlineOperationLabel || "inline operation"} instead of a saved query`;
            }
            if (sourceConfigHint instanceof HTMLElement) {
                sourceConfigHint.textContent = selectedDataSourceSlug
                    ? `${referenceConfiguration(selectedDataSourceSlug).hint} ${context.hint}`
                    : "Select an authored logical data source ref.";
            }
            if (inlineOperationGroup instanceof HTMLElement) {
                inlineOperationGroup.classList.toggle("hidden", !context.supportsInlineOperationJSON);
            }

            if (node && !node.operationType && effectiveOperationType) node.operationType = effectiveOperationType;
            if (inlineOperationField instanceof HTMLTextAreaElement) {
                // Only clear the field if it's empty AND node doesn't have inline operation
                // Don't clear if user has already typed content into the field
                if (
                    !(node?.operationInline && Object.keys(node.operationInline).length > 0) &&
                    inlineOperationField.value.trim() === ""
                ) {
                    inlineOperationField.value = "";
                }
            }

            syncQuerySourceModeUI(context.supportsInlineSQL && Boolean(node?.useInlineSQL));
            if (context.supportsInlineOperationJSON) {
                syncOperationSourceModeUI(Boolean(node?.useInlineOperation));
            }
            if (effectiveOperationType !== "" && !context.supportsInlineSQL) {
                const sourceSavedQueryGroup = getSavedQueryGroup();
                const sourceInlineSQLGroup = getInlineSQLGroup();
                if (sourceSavedQueryGroup instanceof HTMLElement) {
                    sourceSavedQueryGroup.classList.remove("hidden");
                }
                if (sourceInlineSQLGroup instanceof HTMLElement) {
                    sourceInlineSQLGroup.classList.add("hidden");
                }
            }
        };

        const sinkContextForDataSourceType = (dataSourceType, dataSourceMeta = null) => {
            const meta = dataSourceMeta && typeof dataSourceMeta === "object" ? dataSourceMeta : null;
            const normalized = normalizeConnectorString(meta?.type || dataSourceType);
            const descriptor = descriptorForDataSourceType(normalized, connectorDescriptorByType);
            const sinkBackend =
                meta?.sinkBackend || descriptor?.sinkBackend || legacySinkBackendFromDataSourceType(normalized);
            const supportsPipelineSink =
                meta && "supportsPipelineSink" in meta
                    ? meta.supportsPipelineSink !== false
                    : descriptor
                      ? descriptor.supportsPipelineSink
                      : sinkBackend !== UNKNOWN_SINK_BACKEND;
            const operationType =
                meta?.queryOperationType || (normalized ? queryOperationTypeFromDataSourceType(normalized) : "");
            return sinkContextForOperationType(
                operationType,
                sinkBackend,
                Boolean(meta?.readOnly) || !supportsPipelineSink,
            );
        };

        const applySinkDataSourceContext = (dataSourceSlug, node) => {
            const selectedDataSourceSlug = (dataSourceSlug || "").trim();
            const meta = dataSourceMetaBySlug.get(selectedDataSourceSlug);
            const metadataDataSourceType = meta?.type || "";
            const declaredOperationType =
                node && typeof node.sinkOperationType === "string"
                    ? normalizeConnectorString(node.sinkOperationType)
                    : "";
            const context = declaredOperationType
                ? sinkContextForOperationType(declaredOperationType, normalizeConnectorString(node?.sinkBackend || ""))
                : sinkContextForDataSourceType(metadataDataSourceType, meta);
            const sinkQueryRefLabel = getSinkQueryRefLabel();
            const sinkQueryField = getSinkQueryField();
            const sinkConfigHint = getSinkConfigHint();
            const sinkInlineOperationToggleGroup = getSinkInlineOperationToggleGroup();

            if (sinkQueryRefLabel instanceof HTMLElement) {
                sinkQueryRefLabel.innerHTML = selectedDataSourceSlug
                    ? `${escapeHTML(context.queryRefLabel)} <span class="text-error">*</span>`
                    : escapeHTML(context.queryRefLabel);
            }
            if (sinkQueryField instanceof HTMLInputElement || sinkQueryField instanceof HTMLSelectElement) {
                sinkQueryField.disabled = selectedDataSourceSlug === "" || context.operationType === "";
            }
            if (sinkConfigHint instanceof HTMLElement) {
                sinkConfigHint.textContent = selectedDataSourceSlug
                    ? `${referenceConfiguration(selectedDataSourceSlug).hint} ${context.hint}`
                    : "Select an authored logical data source ref.";
            }
            if (sinkInlineOperationToggleGroup instanceof HTMLElement) {
                sinkInlineOperationToggleGroup.classList.toggle("hidden", context.operationType === "");
            }

            if (node && !node.sinkOperationType && context.operationType)
                node.sinkOperationType = context.operationType;
        };

        const sinkBackendFromDataSourceType = (dataSourceType) => {
            const descriptor = descriptorForDataSourceType(dataSourceType, connectorDescriptorByType);
            if (descriptor) {
                return descriptor.supportsPipelineSink ? descriptor.sinkBackend : UNKNOWN_SINK_BACKEND;
            }
            return legacySinkBackendFromDataSourceType(dataSourceType);
        };

        const organizationAPIBasePath = builderAPI.organizationAPIBasePath;

        const ensureSelectField = (fieldElement, placeholder) => {
            if (fieldElement instanceof HTMLSelectElement) {
                return fieldElement;
            }
            if (!(fieldElement instanceof HTMLInputElement)) {
                return null;
            }

            const select = document.createElement("select");
            select.id = fieldElement.id;
            select.className = fieldElement.className;
            fieldElement.parentNode?.replaceChild(select, fieldElement);
            setSelectOptions(select, [], placeholder, "");
            return select;
        };

        const listQueriesForDataSource = async (organizationSlug, dataSourceSlug) => {
            const source = referenceConfiguration(dataSourceSlug).source;
            if (!source) return [];
            try {
                return await builderAPI.listQueriesForDataSource(
                    organizationSlug,
                    source.slug,
                    queryOptionsByDataSource,
                );
            } catch (_) {
                setStatusMessage("Saved-query metadata failed to load. Authored references were kept.", TONE_ERROR);
                return [];
            }
        };

        const renderPipelinePanel = async () => {
            if (!(paletteBodyElement instanceof HTMLElement)) {
                return;
            }
            const organizationSlug = getActiveOrganizationSlug();
            const projectSlug = getActiveProjectSlug();
            if (!organizationSlug || !projectSlug) {
                return;
            }

            const section = appendPipelinePanelShell(paletteBodyElement);

            const body = section.querySelector("#builder-pipeline-list-body");
            const toggle = section.querySelector("#builder-pipeline-list-toggle");
            const search = section.querySelector("#builder-pipeline-search");
            const draft = section.querySelector("#builder-pipeline-draft-filter");
            const list = section.querySelector("#builder-pipeline-list-items");

            if (toggle instanceof HTMLButtonElement && body instanceof HTMLElement) {
                toggle.addEventListener("click", () => {
                    const hidden = body.classList.toggle("hidden");
                    toggle.textContent = hidden ? "Show" : "Hide";
                });
            }

            let pipelines = [];
            try {
                const response = await fetch(
                    `${organizationAPIBasePath(organizationSlug)}/projects/${encodeURIComponent(projectSlug)}/pipelines`,
                    {
                        headers: { Accept: "application/json" },
                    },
                );
                if (response.ok) {
                    const payload = await response.json();
                    pipelines = Array.isArray(payload?.pipelines) ? payload.pipelines : [];
                }
            } catch (_) {
                pipelines = [];
            }

            const rerender = () => {
                if (!(list instanceof HTMLElement)) {
                    return;
                }
                clearChildren(list);

                const searchTerm = search instanceof HTMLInputElement ? search.value.trim().toLowerCase() : "";
                const draftFilter = draft instanceof HTMLSelectElement ? draft.value.trim() : "";

                const filtered = pipelines.filter((pipeline) => {
                    const slugValue = typeof pipeline?.slug === "string" ? pipeline.slug : "";
                    const nameValue = typeof pipeline?.name === "string" ? pipeline.name : slugValue;
                    const hasDraft = Boolean(pipeline?.has_draft);

                    if (searchTerm && !`${nameValue} ${slugValue}`.toLowerCase().includes(searchTerm)) {
                        return false;
                    }
                    if (draftFilter === "yes" && !hasDraft) {
                        return false;
                    }
                    if (draftFilter === "no" && hasDraft) {
                        return false;
                    }

                    return true;
                });

                if (filtered.length === 0) {
                    const empty = document.createElement("p");
                    empty.className = "text-xs text-base-content/60";
                    empty.textContent = "No pipelines match the current filters.";
                    list.appendChild(empty);
                    return;
                }

                filtered.forEach((pipeline) => {
                    const slugValue = typeof pipeline?.slug === "string" ? pipeline.slug : "";
                    if (!slugValue) {
                        return;
                    }
                    const nameValue =
                        typeof pipeline?.name === "string" && pipeline.name.trim() !== "" ? pipeline.name : slugValue;
                    const hasDraft = Boolean(pipeline?.has_draft);
                    const itemButton = document.createElement("button");
                    itemButton.type = "button";
                    itemButton.className =
                        "block w-full rounded border border-base-300 bg-base-200/40 px-2 py-1.5 text-left hover:border-primary/40 hover:bg-base-100";
                    itemButton.innerHTML = `
                        <span class="block truncate text-xs font-medium">${escapeHTML(nameValue)}</span>
                        <span class="block truncate text-[11px] text-base-content/60">${escapeHTML(slugValue)}${hasDraft ? " • draft" : ""}</span>
                    `;
                    itemButton.addEventListener("click", async () => {
                        if (itemButton.disabled) {
                            return;
                        }
                        if ((options?.pipeline?.slug || "").trim() === slugValue) {
                            return;
                        }

                        itemButton.disabled = true;
                        setStatusMessage(`Loading pipeline "${nameValue}"...`, TONE_NEUTRAL);

                        try {
                            const pipelineResponse = await fetch(
                                `${organizationAPIBasePath(organizationSlug)}/projects/${encodeURIComponent(projectSlug)}/pipelines/${encodeURIComponent(slugValue)}`,
                                { headers: { Accept: "application/json" } },
                            );
                            if (!pipelineResponse.ok) {
                                const message = await readAPIErrorMessage(pipelineResponse);
                                setStatusMessage(message || "Unable to load selected pipeline.", TONE_ERROR);
                                return;
                            }

                            const payload = await pipelineResponse.json();
                            const loadedPipeline = payload?.pipeline || payload;
                            const loadedSpec = loadedPipeline?.spec;
                            if (!loadedSpec || typeof loadedSpec !== "object") {
                                setStatusMessage("Unable to load selected pipeline: missing spec.", TONE_ERROR);
                                return;
                            }

                            const importResponse = await fetch(options.importUrl || "/builder/import", {
                                method: "POST",
                                headers: {
                                    "Content-Type": "application/json",
                                },
                                body: JSON.stringify({
                                    format: "json",
                                    source: JSON.stringify(loadedSpec, null, 2),
                                }),
                            });
                            if (!importResponse.ok) {
                                const message = await readAPIErrorMessage(importResponse);
                                setStatusMessage(
                                    message || "Unable to map selected pipeline to builder state.",
                                    TONE_ERROR,
                                );
                                return;
                            }

                            const importedState = await importResponse.json();
                            applyImportedStateToCanvas(importedState);

                            const loadedPipelineName =
                                typeof loadedPipeline?.name === "string" && loadedPipeline.name.trim() !== ""
                                    ? loadedPipeline.name.trim()
                                    : slugValue;
                            const loadedHasDraft = Boolean(loadedPipeline?.draft);
                            setActivePipelineContext(
                                organizationSlug,
                                projectSlug,
                                slugValue,
                                loadedPipelineName,
                                loadedHasDraft,
                                {},
                            );
                            await enhanceQueryFields();
                            saveState(storageKey, gridElement, edgesDataElement, bindingsState, {
                                sourceFormat: pipelineSourceFormat,
                                exportFormat: pipelineExportFormat,
                            });
                            markActionStatePersisted();
                            refreshValidation();
                            setStatusMessage(`Loaded pipeline "${loadedPipelineName}".`, TONE_SUCCESS);
                        } catch (_) {
                            setStatusMessage("Unable to load selected pipeline.", TONE_ERROR);
                        } finally {
                            itemButton.disabled = false;
                        }
                    });

                    list.appendChild(itemButton);
                });
            };

            [search, draft].forEach((element) => {
                if (!(element instanceof HTMLElement)) {
                    return;
                }
                element.addEventListener("input", rerender);
                element.addEventListener("change", rerender);
            });

            rerender();
        };

        const enhanceQueryFields = async () => {
            const organizationSlug = getActiveOrganizationSlug();
            if (!organizationSlug) {
                return;
            }

            const dsSelect = ensureSelectField(getDataSourceField(), "Select data source");
            const querySelect = ensureSelectField(getQueryField(), "Select data source first");
            const sinkDataSourceSelect = ensureSelectField(getSinkDataSourceField(), "Select writable data source");
            const sinkQuerySelect = ensureSelectField(getSinkQueryField(), "Select data source first");
            if (!(dsSelect instanceof HTMLSelectElement) || !(querySelect instanceof HTMLSelectElement)) {
                return;
            }

            const pipelineSlug = options.pipeline.slug;
            dataSourceConfiguration = null;
            dataSourceMetaBySlug.clear();
            const configuration = await builderAPI.loadDataSourceReferences(
                organizationSlug,
                getActiveProjectSlug(),
                pipelineSlug,
                getActiveTargetEnvironmentSlug(),
            );
            if (options.pipeline.slug !== pipelineSlug) return;
            dataSourceConfiguration = configuration;
            const creationForm = document.getElementById("builder-creation-form");
            configuration.authoringNewPipeline = Boolean(creationForm && !creationForm.hidden);
            const refs = new Map(dataSourceConfiguration.refs.map((ref) => [ref.slug, ref]));
            if (configuration.authoringNewPipeline) {
                refs.clear();
                for (const source of configuration.sources.filter((item) => item.isActive)) {
                    const descriptor = descriptorForDataSourceType(source.type, connectorDescriptorByType);
                    const capabilities = [];
                    if (descriptor?.supportsPipelineSource) capabilities.push("pipeline_source");
                    if (descriptor?.supportsPipelineSink && !source.readOnly) capabilities.push("pipeline_sink");
                    refs.set(source.slug, {
                        slug: source.slug,
                        name: source.name,
                        required_capabilities: capabilities,
                    });
                }
            }
            readNodes(gridElement).forEach((node) => {
                const ref = node.kind === KIND_QUERY ? node.dataSourceSlug : node.sinkDataSourceSlug;
                if (ref && !refs.has(ref)) refs.set(ref, { slug: ref, name: ref });
            });
            for (const ref of refs.values()) {
                const source = referenceConfiguration(ref.slug).source;
                if (source)
                    dataSourceMetaBySlug.set(
                        ref.slug,
                        enrichDataSourceWithDescriptor(source, connectorDescriptorByType),
                    );
            }
            const refOptions = (capability) =>
                Array.from(refs.values())
                    .filter((ref) => !ref.required_capabilities || ref.required_capabilities.includes(capability))
                    .map((ref) => ({
                        value: ref.slug,
                        label: configuration.authoringNewPipeline ? ref.name || ref.slug : ref.slug,
                    }));
            dataSourceOptions = refOptions("pipeline_source");
            sinkDataSourceOptions = refOptions("pipeline_sink");

            setSelectOptions(
                dsSelect,
                dataSourceOptions,
                "Select data source",
                dsSelect.value || "",
                "authored logical ref",
            );

            if (sinkDataSourceSelect instanceof HTMLSelectElement) {
                setSelectOptions(
                    sinkDataSourceSelect,
                    sinkDataSourceOptions,
                    "Select writable data source",
                    sinkDataSourceSelect.value || "",
                    "authored logical ref",
                );
            }

            const loadQueryOptions = async (dataSourceSlug, selectedQuery) => {
                if (!dataSourceSlug) {
                    setSelectOptions(querySelect, [], "Select data source first", "");
                    return;
                }

                const queryOptions = await listQueriesForDataSource(organizationSlug, dataSourceSlug);
                setSelectOptions(
                    querySelect,
                    queryOptions,
                    "Select query",
                    selectedQuery || "",
                    "authored saved query",
                );
            };

            const loadSinkQueryOptions = async (dataSourceSlug, selectedQuery) => {
                if (!(sinkQuerySelect instanceof HTMLSelectElement)) {
                    return;
                }

                if (!dataSourceSlug) {
                    setSelectOptions(sinkQuerySelect, [], "Select data source first", "");
                    return;
                }

                const queryOptions = await listQueriesForDataSource(organizationSlug, dataSourceSlug);
                setSelectOptions(
                    sinkQuerySelect,
                    queryOptions,
                    "Select query",
                    selectedQuery || "",
                    "authored saved query",
                );
            };

            if (dsSelect.dataset.referenceListeners !== "true") {
                dsSelect.dataset.referenceListeners = "true";
                dsSelect.addEventListener("change", async () => {
                    applyQueryDataSourceContext(dsSelect.value.trim());
                    await loadQueryOptions(dsSelect.value.trim(), "");
                    updateSelectedNode();
                });

                querySelect.addEventListener("change", () => {
                    updateSelectedNode();
                });

                if (sinkDataSourceSelect instanceof HTMLSelectElement) {
                    sinkDataSourceSelect.addEventListener("change", async () => {
                        applySinkDataSourceContext(sinkDataSourceSelect.value.trim());
                        await loadSinkQueryOptions(sinkDataSourceSelect.value.trim(), "");
                        updateSelectedNode();
                    });
                }

                if (sinkQuerySelect instanceof HTMLSelectElement) {
                    sinkQuerySelect.addEventListener("change", () => {
                        updateSelectedNode();
                    });
                }
            }

            const currentDataSource = dsSelect.value.trim();
            if (currentDataSource !== "") {
                applyQueryDataSourceContext(currentDataSource);
                await loadQueryOptions(currentDataSource, querySelect.value.trim());
            } else {
                applyQueryDataSourceContext("");
                setSelectOptions(querySelect, [], "Select data source first", "");
            }

            if (sinkDataSourceSelect instanceof HTMLSelectElement) {
                const currentSinkDataSource = sinkDataSourceSelect.value.trim();
                if (currentSinkDataSource !== "") {
                    applySinkDataSourceContext(currentSinkDataSource);
                    await loadSinkQueryOptions(currentSinkDataSource, sinkQuerySelect?.value?.trim?.() || "");
                } else {
                    applySinkDataSourceContext("");
                    if (sinkQuerySelect instanceof HTMLSelectElement) {
                        setSelectOptions(sinkQuerySelect, [], "Select data source first", "");
                    }
                }
            }
            if (selectedItem()) showSelection(selectedItem());
        };

        const collectBindingsFromEditor = (node, targetPort, allowMultiple) => {
            if (!(bindingRows instanceof HTMLElement)) {
                return [];
            }

            const nextBindings = Array.from(bindingRows.querySelectorAll("[data-binding-row]"))
                .map((row) => {
                    if (!(row instanceof HTMLElement)) {
                        return null;
                    }

                    const sourceNodeSelect = row.querySelector("[data-binding-source-node]");
                    const sourcePortSelect = row.querySelector("[data-binding-source-port]");
                    const sourceNode =
                        sourceNodeSelect instanceof HTMLSelectElement || sourceNodeSelect instanceof HTMLInputElement
                            ? sourceNodeSelect.value.trim()
                            : "";
                    if (sourceNode === "") {
                        return null;
                    }

                    const sourceItem = gridElement.querySelector(`.builder-node[data-node-slug="${sourceNode}"]`);
                    const sourceKind =
                        sourceItem instanceof HTMLElement ? sourceItem.getAttribute(ATTR_KIND) || "" : "";
                    const availablePorts = outputPortsForKind(sourceKind);
                    const sourcePort =
                        sourcePortSelect instanceof HTMLSelectElement && sourcePortSelect.value.trim() !== ""
                            ? sourcePortSelect.value.trim()
                            : availablePorts[0] || "";
                    if (sourcePort === "") {
                        return null;
                    }
                    const targetPortField = row.querySelector("[data-binding-target-port]");
                    const rowTargetPort =
                        targetPortField instanceof HTMLInputElement || targetPortField instanceof HTMLSelectElement
                            ? targetPortField.value.trim()
                            : targetPort;

                    return {
                        fromNode: sourceNode,
                        fromPort: sourcePort,
                        toNode: node.slug,
                        toPort: rowTargetPort,
                    };
                })
                .filter(Boolean);

            return allowMultiple ? nextBindings : nextBindings.slice(0, 1);
        };

        const syncBindingsFromEditor = () => {
            const item = selectedItem();
            if (!(item instanceof HTMLElement)) {
                return;
            }

            const node = readNodeDataFromElement(item);
            const targetPort = inputPortsForKind(node.kind)[0] || "";
            if (!targetPort) {
                return;
            }

            if (node.kind === KIND_TRANSFORM) {
                const nextBindings = collectBindingsFromEditor(node, "", true);
                const seenAliases = new Set();
                const aliasIssue = nextBindings.find((binding) => {
                    if (!transformInputNamePattern.test(binding.toPort)) {
                        return true;
                    }
                    if (seenAliases.has(binding.toPort)) {
                        return true;
                    }
                    seenAliases.add(binding.toPort);
                    return false;
                });
                if (aliasIssue) {
                    if (bindingError instanceof HTMLElement) {
                        bindingError.textContent =
                            "Transform input aliases must be unique SQL identifiers like orders or input_rows.";
                    }
                    return;
                }
                for (const row of bindingRows.querySelectorAll("[data-binding-row]")) {
                    const from = row.getAttribute("data-binding-original-target-port") || "";
                    const to = row.querySelector("[data-binding-target-port]")?.value?.trim() || "";
                    if (!renameTransformInputAgreement(node, from, to)) {
                        if (bindingError)
                            bindingError.textContent =
                                "This alias already has an agreement. Choose another alias or clear that agreement first.";
                        return;
                    }
                }
                const remaining = normalizeBindings(bindingsState).filter(
                    (binding) => !(binding.toNode === node.slug && binding.fromPort === PORT_ROWS),
                );
                bindingsState = normalizeBindings([...remaining, ...nextBindings]);
                node.transformInputs = Object.fromEntries(
                    nextBindings.map((binding) => [binding.toPort, binding.fromNode]),
                );
                applyNodeDataToElement(item, node);
                renderBindingsEditor(node);
                syncODCSSelection(node);
                saveAndValidate();
                return;
            }

            if (node.kind === KIND_SINK) {
                const nextBindings = collectBindingsFromEditor(node, targetPort, false);
                const nextBinding = nextBindings[0] || null;
                const incomingEdges = parseEdgeData(edgesDataElement).filter((edge) => edge.to === node.slug);
                const nodes = readNodes(gridElement);
                if (!nextBinding) {
                    removeCanonicalConnections(incomingEdges);
                    renderBindingsEditor(node);
                    renderConnectionsEditor(node);
                    saveAndValidate();
                    return;
                }
                const sourceNode = nodes.find((candidate) => candidate.slug === nextBinding.fromNode) || null;
                const result = ensureCanonicalConnection(sourceNode, node, { replaceIncoming: true });
                if (!result.ok) {
                    if (bindingError instanceof HTMLElement) {
                        bindingError.textContent = result.error;
                    }
                    return;
                }
                renderBindingsEditor(node);
                renderConnectionsEditor(node);
                saveAndValidate();
                return;
            }

            const allowMultiple = false;
            const nextBindings = collectBindingsFromEditor(node, targetPort, allowMultiple);
            bindingsState = replaceBindingsForTarget(bindingsState, node.slug, targetPort, nextBindings, allowMultiple);
            renderBindingsEditor(node);
            saveAndValidate();
        };

        const renderBindingsEditor = (node) => {
            if (!(bindingSection instanceof HTMLElement) || !(bindingRows instanceof HTMLElement)) {
                return;
            }

            const effectiveTargetPort = inputPortsForKind(node.kind)[0] || "";
            const allowMultiple = false;
            bindingSection.classList.toggle("hidden", effectiveTargetPort === "");
            if (effectiveTargetPort === "") {
                clearChildren(bindingRows);
                renderPayloadDiagnostic(bindingPayloadDiagnostic, null);
                return;
            }

            const edges = parseEdgeData(edgesDataElement);
            const nodes = readNodes(gridElement);
            const nodeBySlug = new Map(nodes.map((entry) => [entry.slug, entry]));
            const incomingSlugs = Array.from(
                new Set(edges.filter((edge) => edge.to === node.slug).map((edge) => edge.from)),
            );
            if (node.kind === KIND_TRANSFORM) {
                const currentBindings = normalizeBindings(bindingsState).filter((binding) => {
                    return binding.toNode === node.slug && binding.fromPort === PORT_ROWS;
                });
                const incomingEdges = edges.filter((edge) => edge.to === node.slug);
                const directBindings = currentBindings.filter((binding) =>
                    incomingEdges.some((edge) => edge.from === binding.fromNode && edge.to === binding.toNode),
                );
                const missingBindings = incomingEdges
                    .filter((edge) => !directBindings.some((binding) => binding.fromNode === edge.from))
                    .map((edge) => ({
                        fromNode: edge.from,
                        fromPort: PORT_ROWS,
                        toNode: node.slug,
                        toPort: nextTransformInputNameForConnection(edge.from, currentBindings, node.slug),
                    }));
                const orphanBindings = currentBindings.filter(
                    (binding) =>
                        !incomingEdges.some((edge) => edge.from === binding.fromNode && edge.to === binding.toNode),
                );
                const inputRows = [...directBindings, ...missingBindings, ...orphanBindings];
                if (bindingStatus instanceof HTMLElement) {
                    bindingStatus.textContent = `${inputRows.length} named input${inputRows.length === 1 ? "" : "s"}`;
                }
                if (bindingHelp instanceof HTMLElement) {
                    bindingHelp.textContent = "Each incoming source gets a SQL alias written to transform.inputs.";
                }
                if (bindingEdgeHint instanceof HTMLElement) {
                    bindingEdgeHint.textContent =
                        incomingSlugs.length > 0
                            ? `Incoming dependencies: ${incomingSlugs.join(", ")}`
                            : "No direct incoming dependencies yet.";
                }
                renderPayloadDiagnostic(bindingPayloadDiagnostic, null);
                clearChildren(bindingRows);
                if (inputRows.length === 0) {
                    const empty = document.createElement("p");
                    empty.className = "text-xs text-base-content/60";
                    empty.textContent = "Connect an upstream source to declare transform inputs.";
                    bindingRows.appendChild(empty);
                } else {
                    inputRows.forEach((binding) => {
                        const isOrphan = !incomingEdges.some(
                            (edge) => edge.from === binding.fromNode && edge.to === binding.toNode,
                        );
                        const rowElement = document.createElement("div");
                        rowElement.className = "rounded-md border border-base-300 bg-base-100 p-3";
                        rowElement.setAttribute("data-binding-row", "true");
                        rowElement.setAttribute("data-binding-original-target-port", binding.toPort || "");

                        const header = document.createElement("div");
                        header.className = "flex items-center justify-between gap-2";
                        const label = document.createElement("p");
                        label.className = "text-[11px] font-medium text-base-content/80";
                        label.textContent = `Source ${binding.fromNode}`;
                        const status = document.createElement("span");
                        status.className = isOrphan
                            ? "badge badge-xs badge-error badge-outline"
                            : "badge badge-xs badge-success badge-outline";
                        status.textContent = isOrphan ? "missing edge" : "linked";
                        header.append(label, status);

                        const sourceInput = document.createElement("input");
                        sourceInput.type = "hidden";
                        sourceInput.value = binding.fromNode;
                        sourceInput.setAttribute(ATTR_BINDING_SOURCE_NODE, "true");

                        const fields = document.createElement("div");
                        fields.className = "mt-3 space-y-2";
                        const aliasLabel = document.createElement("label");
                        aliasLabel.className = "form-control";
                        const aliasText = document.createElement("span");
                        aliasText.className = "label-text text-xs";
                        aliasText.textContent = "SQL alias";
                        const aliasInput = document.createElement("input");
                        aliasInput.className = "input input-bordered input-sm font-mono";
                        aliasInput.value = binding.toPort || transformInputNameFromSourceSlug(binding.fromNode);
                        aliasInput.setAttribute("data-binding-target-port", "true");
                        aliasInput.placeholder = "orders";
                        aliasLabel.append(aliasText, aliasInput);

                        const detail = document.createElement("p");
                        detail.className = "text-[11px] text-base-content/60";
                        detail.textContent = "Use this alias in transform SQL, for example SELECT * FROM orders.";

                        fields.append(aliasLabel, detail);
                        rowElement.append(header, sourceInput, fields);
                        bindingRows.appendChild(rowElement);
                    });
                }
                if (bindingError instanceof HTMLElement) {
                    const issue = validateBindings(nodes, edges, currentBindings).find((entry) => {
                        const message = issueMessage(entry);
                        return message.startsWith(`${node.slug}:`) || message.includes(` ${node.slug} `);
                    });
                    bindingError.textContent = issueMessage(issue);
                }
                return;
            }
            const currentBindings = [bindingForTarget(bindingsState, node.slug, effectiveTargetPort)].filter(Boolean);
            const currentBinding = currentBindings[0] || null;
            let selectableSourceSlugs = Array.from(
                new Set([...incomingSlugs, ...currentBindings.map((binding) => binding.fromNode)]),
            );
            if (node.kind === KIND_SINK) {
                const edgeWithoutIncoming = edges.filter((edge) => edge.to !== node.slug);
                selectableSourceSlugs = Array.from(
                    new Set([
                        ...selectableSourceSlugs,
                        ...nodes
                            .filter((candidate) => candidate.slug !== node.slug)
                            .filter((candidate) => outputPortsForKind(candidate.kind).length > 0)
                            .filter((candidate) => {
                                const error = validateEdgeConnection(
                                    nodes,
                                    edgeWithoutIncoming,
                                    candidate.slug,
                                    node.slug,
                                );
                                return error === "" || candidate.slug === currentBinding?.fromNode;
                            })
                            .map((candidate) => candidate.slug),
                    ]),
                );
            }
            const sourceNodeOptions = selectableSourceSlugs
                .map((slug) => nodeBySlug.get(slug))
                .filter((entry) => entry && outputPortsForKind(entry.kind).length > 0)
                .map((entry) => ({ value: entry.slug, label: `${entry.slug} (${entry.kind})` }));

            if (bindingStatus instanceof HTMLElement) {
                bindingStatus.textContent = currentBinding ? `from ${currentBinding.fromNode}` : "Missing input";
            }
            if (bindingHelp instanceof HTMLElement) {
                bindingHelp.textContent =
                    incomingSlugs.length > 0
                        ? "Sink input follows the direct upstream step."
                        : "Choose the upstream step that provides this sink payload.";
            }
            if (bindingEdgeHint instanceof HTMLElement) {
                bindingEdgeHint.textContent =
                    incomingSlugs.length > 0
                        ? `Incoming dependencies: ${incomingSlugs.join(", ")}`
                        : "No direct incoming dependencies yet.";
            }
            renderPayloadDiagnostic(
                bindingPayloadDiagnostic,
                payloadDiagnosticForBinding(node, currentBinding, nodeBySlug, dataSourceMetaBySlug),
            );

            clearChildren(bindingRows);
            const rows = [...currentBindings];
            if (rows.length === 0) {
                rows.push(null);
            }
            rows.forEach((binding) => {
                const currentSourceNode = bindingSourceNodeValue(node.kind, incomingSlugs, binding);
                const rowSourceNodeOptions = sourceNodeOptions;
                const sourceNode = currentSourceNode ? nodeBySlug.get(currentSourceNode) : null;
                const sourcePortOptions = (sourceNode ? outputPortsForKind(sourceNode.kind) : []).map((port) => ({
                    value: port,
                    label: port,
                }));
                const rowElement = document.createElement("div");
                rowElement.className = "rounded-md border border-base-300 bg-base-100 p-3";
                rowElement.setAttribute("data-binding-row", "true");
                const title = node.kind === KIND_SINK ? "Sink input" : `Input ${effectiveTargetPort}`;

                const header = document.createElement("div");
                header.className = "flex items-center justify-between gap-2";
                const label = document.createElement("p");
                label.className = "text-[11px] font-medium text-base-content/80";
                label.textContent = title;
                const clearButton = document.createElement("button");
                clearButton.className = "btn btn-ghost btn-xs";
                clearButton.type = "button";
                clearButton.textContent = "Clear";
                clearButton.setAttribute("data-binding-clear-row", "true");
                clearButton.disabled = currentSourceNode === "";
                header.appendChild(label);
                header.appendChild(clearButton);

                const fields = document.createElement("div");
                fields.className = "mt-3 space-y-2";

                const nodeLabel = document.createElement("label");
                nodeLabel.className = "form-control";
                const nodeText = document.createElement("span");
                nodeText.className = "label-text text-xs";
                nodeText.textContent = "Source node";
                nodeLabel.appendChild(nodeText);

                if (isStepKind(node.kind)) {
                    const nodeValue = document.createElement("div");
                    nodeValue.className =
                        "rounded-md border border-base-300 bg-base-200 px-3 py-2 text-sm text-base-content";
                    nodeValue.textContent = currentSourceNode || "No source node set";
                    const nodeInput = document.createElement("input");
                    nodeInput.type = "hidden";
                    nodeInput.value = currentSourceNode;
                    nodeInput.setAttribute(ATTR_BINDING_SOURCE_NODE, "true");
                    nodeLabel.appendChild(nodeValue);
                    nodeLabel.appendChild(nodeInput);
                } else {
                    const nodeSelect = document.createElement("select");
                    nodeSelect.className = "select select-bordered select-sm";
                    nodeSelect.setAttribute(ATTR_BINDING_SOURCE_NODE, "true");
                    setSelectOptions(
                        nodeSelect,
                        rowSourceNodeOptions,
                        allowMultiple ? "Add upstream node" : "Select upstream node",
                        currentSourceNode,
                    );
                    nodeSelect.disabled = rowSourceNodeOptions.length === 0;
                    nodeLabel.appendChild(nodeSelect);
                }

                fields.appendChild(nodeLabel);
                if (node.kind !== KIND_SINK) {
                    const portLabel = document.createElement("label");
                    portLabel.className = "form-control";
                    const portText = document.createElement("span");
                    portText.className = "label-text text-xs";
                    portText.textContent = "Source output";
                    const portSelect = document.createElement("select");
                    portSelect.className = "select select-bordered select-sm";
                    portSelect.setAttribute(ATTR_BINDING_SOURCE_PORT, "true");
                    setSelectOptions(portSelect, sourcePortOptions, "Select source output", binding?.fromPort || "");
                    portSelect.disabled = sourcePortOptions.length === 0 || currentSourceNode === "";
                    portLabel.appendChild(portText);
                    portLabel.appendChild(portSelect);
                    fields.appendChild(portLabel);
                } else {
                    const detail = document.createElement("p");
                    detail.className = "text-[11px] text-base-content/60";
                    detail.textContent = currentSourceNode
                        ? `Payload comes from ${currentSourceNode}.`
                        : "Select one upstream step; the sink receives its payload directly.";
                    fields.appendChild(detail);
                }

                if (binding) {
                    const rowIssues = validateBindings(nodes, edges, [binding]).filter((issue) => {
                        const message = issueMessage(issue);
                        return (
                            message.startsWith(`${node.slug}:`) ||
                            message.includes(` ${node.slug} `) ||
                            message.startsWith("binding ")
                        );
                    });
                    if (rowIssues.length > 0) {
                        const rowError = document.createElement("p");
                        rowError.className = "mt-2 text-[11px] text-error";
                        rowError.textContent = issueMessage(rowIssues[0]);
                        fields.appendChild(rowError);
                    }
                }

                rowElement.appendChild(header);
                rowElement.appendChild(fields);
                bindingRows.appendChild(rowElement);
            });

            if (bindingError instanceof HTMLElement) {
                const issue = validateBindings(nodes, edges, currentBindings).find((entry) => {
                    const message = issueMessage(entry);
                    return (
                        message.startsWith(`${node.slug}:`) ||
                        message.includes(` ${node.slug} `) ||
                        message.startsWith("binding ")
                    );
                });
                bindingError.textContent = issueMessage(issue);
            }
        };

        const setConnectionError = (message) => {
            if (!(connectionError instanceof HTMLElement)) {
                return;
            }
            connectionError.textContent = message || "";
        };

        const canHaveIncomingConnection = (kind) => kind === KIND_TRANSFORM || kind === KIND_SINK;
        const canHaveOutgoingConnection = (kind) => isStepKind(kind);

        const ensureCanonicalConnection = (sourceNode, targetNode, { replaceIncoming = false } = {}) => {
            if (!sourceNode || !targetNode) {
                return { ok: false, error: "Connection is incomplete." };
            }

            const nodes = readNodes(gridElement);
            const edges = parseEdgeData(edgesDataElement);
            const removedIncoming = replaceIncoming ? edges.filter((edge) => edge.to === targetNode.slug) : [];
            const scopedEdges = replaceIncoming ? edges.filter((edge) => edge.to !== targetNode.slug) : edges;
            const existingEdge = scopedEdges.some(
                (edge) => edge.from === sourceNode.slug && edge.to === targetNode.slug,
            );
            const validationEdges = existingEdge
                ? scopedEdges.filter((edge) => !(edge.from === sourceNode.slug && edge.to === targetNode.slug))
                : scopedEdges;
            const error = validateEdgeConnection(nodes, validationEdges, sourceNode.slug, targetNode.slug);
            if (error !== "") {
                return { ok: false, error };
            }

            const currentBindings = normalizeBindings(bindingsState);
            const connectionHasBinding = currentBindings.some(
                (binding) => binding.fromNode === sourceNode.slug && binding.toNode === targetNode.slug,
            );
            if (existingEdge && connectionHasBinding && removedIncoming.length === 0) {
                return { ok: true, binding: null, existingEdge: true, changed: false };
            }

            const nextEdges = existingEdge
                ? scopedEdges
                : [...scopedEdges, { from: sourceNode.slug, to: targetNode.slug }];
            const connectionsToClean = connectionHasBinding
                ? removedIncoming
                : [
                      ...removedIncoming,
                      {
                          from: sourceNode.slug,
                          to: targetNode.slug,
                      },
                  ];
            let nextBindings = removeBindingsForConnections(currentBindings, connectionsToClean);
            const shouldCreateBinding = !connectionHasBinding || removedIncoming.length > 0;
            const binding = shouldCreateBinding
                ? defaultBindingForConnection(sourceNode, targetNode, nextBindings)
                : null;
            if (binding) {
                if (targetNode.kind === KIND_SINK) {
                    nextBindings = replaceBindingsForTarget(
                        nextBindings,
                        targetNode.slug,
                        binding.toPort,
                        [binding],
                        false,
                    );
                } else {
                    nextBindings = normalizeBindings([...nextBindings, binding]);
                }
            }

            if (
                binding &&
                targetNode.kind === KIND_TRANSFORM &&
                targetNode.transformSQL === "SELECT * FROM input_rows" &&
                !targetNode.originalStep?.transform &&
                !currentBindings.some((entry) => entry.toNode === targetNode.slug)
            ) {
                targetNode.transformSQL = `SELECT * FROM ${binding.toPort}`;
                const targetItem = gridElement.querySelector(`.builder-node[data-node-slug="${targetNode.slug}"]`);
                if (targetItem) applyNodeDataToElement(targetItem, targetNode);
            }

            writeEdgesData(edgesDataElement, nextEdges);
            bindingsState = normalizeBindings(nextBindings);
            setConnectionError("");
            return { ok: true, binding, existingEdge, changed: true };
        };

        const removeCanonicalConnections = (connections) => {
            const pairs = new Set(
                connections
                    .filter((connection) => connection?.from && connection?.to)
                    .map((connection) => `${connection.from}->${connection.to}`),
            );
            if (pairs.size === 0) {
                return false;
            }

            const edges = parseEdgeData(edgesDataElement).filter((edge) => !pairs.has(`${edge.from}->${edge.to}`));
            writeEdgesData(edgesDataElement, edges);
            bindingsState = removeBindingsForConnections(bindingsState, connections);
            setConnectionError("");
            return true;
        };

        const selectNodeItem = (item, { openProperties = true, renderSelection = true } = {}) => {
            if (!(item instanceof HTMLElement)) {
                return null;
            }
            const slug = item.getAttribute(ATTR_NODE_SLUG) || null;
            if (!slug) {
                return null;
            }
            const changedSelection = selectedSlug !== slug;
            selectedSlug = slug;
            selectedEdge = null;
            if (openProperties) {
                isPropertiesVisible = true;
                applyMainGridLayout();
            }
            showSelection(item);
            if (changedSelection) {
                if (propertyPanel instanceof HTMLElement) propertyPanel.scrollTop = 0;
                if (propertiesBodyElement instanceof HTMLElement) propertiesBodyElement.scrollTop = 0;
            }
            if (renderSelection) {
                scheduleRender();
            } else {
                applySelection(gridElement, selectedSlug);
            }
            return readNodeDataFromElement(item);
        };

        const connectNodeItems = (sourceItem, targetItem, { selectTarget = true } = {}) => {
            if (!(sourceItem instanceof HTMLElement) || !(targetItem instanceof HTMLElement)) {
                setStatusMessage("Select a producer first, then choose a downstream target.", TONE_NEUTRAL);
                return false;
            }
            if (sourceItem === targetItem) {
                setStatusMessage("Cannot connect a node to itself.", TONE_ERROR);
                return false;
            }

            const sourceNode = readNodeDataFromElement(sourceItem);
            const targetNode = readNodeDataFromElement(targetItem);
            const result = ensureCanonicalConnection(sourceNode, targetNode);
            if (!result.ok) {
                setConnectionError(result.error);
                setStatusMessage(result.error, TONE_ERROR);
                return false;
            }

            if (selectTarget) {
                selectedSlug = targetNode.slug;
                selectedEdge = null;
                showSelection(targetItem);
            } else {
                const item = selectedItem();
                if (item instanceof HTMLElement) {
                    showSelection(item);
                }
            }
            scheduleRender();
            if (!result.changed) {
                setStatusMessage("Connection already exists.", TONE_NEUTRAL);
                return true;
            }
            saveAndValidate();
            setStatusMessage(`Connected ${sourceNode.slug} -> ${targetNode.slug}.`, TONE_SUCCESS);
            return true;
        };

        const connectionPointForPort = (nodeItem, portType) => {
            const offset = layoutOffsetFrom(nodeItem, surfaceElement);
            return {
                x: portType === PORT_DIR_OUT ? offset.x + offset.w : offset.x,
                y: offset.y + offset.h / 2,
            };
        };

        const pointerPointForSurface = (event) => {
            const rect = surfaceElement.getBoundingClientRect();
            const scale = Number.isFinite(canvasScale) && canvasScale > 0 ? canvasScale : 1;
            return {
                x: (event.clientX - rect.left) / scale,
                y: (event.clientY - rect.top) / scale,
            };
        };

        const setPortDropTarget = (port) => {
            if (activeConnectionDrag?.targetPort instanceof HTMLElement && activeConnectionDrag.targetPort !== port) {
                activeConnectionDrag.targetPort.classList.remove("node-port-connect-target");
            }
            if (port instanceof HTMLElement) {
                port.classList.add("node-port-connect-target");
            }
            if (activeConnectionDrag) {
                activeConnectionDrag.targetPort = port instanceof HTMLElement ? port : null;
            }
        };

        const inputPortAtPointer = (event) => {
            const target = document.elementFromPoint(event.clientX, event.clientY);
            const port = target instanceof HTMLElement ? target.closest("[data-port]") : null;
            if (!(port instanceof HTMLElement) || port.getAttribute(ATTR_PORT) !== PORT_DIR_IN) {
                const inputPorts = Array.from(
                    gridElement.querySelectorAll(`.node-port-in[${ATTR_PORT}="${PORT_DIR_IN}"]`),
                );
                return (
                    inputPorts.find((candidate) => {
                        if (!(candidate instanceof HTMLElement)) {
                            return false;
                        }
                        const rect = candidate.getBoundingClientRect();
                        const hitSlop = 4;
                        return (
                            event.clientX >= rect.left - hitSlop &&
                            event.clientX <= rect.right + hitSlop &&
                            event.clientY >= rect.top - hitSlop &&
                            event.clientY <= rect.bottom + hitSlop
                        );
                    }) || null
                );
            }
            return port;
        };

        const connectionTargetAtPointer = (event, sourceItem) => {
            const port = inputPortAtPointer(event);
            const portItem = port instanceof HTMLElement ? port.closest(".builder-node") : null;
            if (portItem instanceof HTMLElement && portItem !== sourceItem) {
                return { item: portItem, port };
            }

            const candidates = Array.from(gridElement.querySelectorAll(".builder-node[data-node-slug]"));
            for (const candidate of candidates) {
                if (!(candidate instanceof HTMLElement) || candidate === sourceItem) {
                    continue;
                }
                const node = readNodeDataFromElement(candidate);
                if (!canHaveIncomingConnection(node.kind)) {
                    continue;
                }
                const rect = candidate.getBoundingClientRect();
                const edgeSlop = 18;
                const leftBandWidth = Math.min(48, rect.width * 0.3);
                const isNearInputEdge =
                    event.clientX >= rect.left - edgeSlop &&
                    event.clientX <= rect.left + leftBandWidth &&
                    event.clientY >= rect.top - edgeSlop &&
                    event.clientY <= rect.bottom + edgeSlop;
                if (!isNearInputEdge) {
                    continue;
                }
                const candidatePort = candidate.querySelector(`.node-port-in[${ATTR_PORT}="${PORT_DIR_IN}"]`);
                return {
                    item: candidate,
                    port: candidatePort instanceof HTMLElement ? candidatePort : null,
                };
            }

            return null;
        };

        const renderConnectionDragPreview = (endPoint) => {
            if (!(edgesLayerElement instanceof SVGSVGElement) || !activeConnectionDrag) {
                return;
            }

            const width = Math.ceil(surfaceElement.clientWidth || 0);
            const height = Math.ceil(surfaceElement.clientHeight || 0);
            edgesLayerElement.setAttribute("width", String(width));
            edgesLayerElement.setAttribute("height", String(height));
            edgesLayerElement.setAttribute("viewBox", `0 0 ${width} ${height}`);

            let line = edgesLayerElement.querySelector("#builder-connection-preview-line");
            if (!(line instanceof SVGLineElement)) {
                line = document.createElementNS("http://www.w3.org/2000/svg", "line");
                line.id = "builder-connection-preview-line";
                line.setAttribute("stroke", "var(--color-info)");
                line.setAttribute("stroke-width", "3");
                line.setAttribute("stroke-linecap", "round");
                line.setAttribute("stroke-dasharray", "6 5");
                line.setAttribute("style", "pointer-events: none;");
                edgesLayerElement.appendChild(line);
            }

            line.setAttribute("x1", String(Math.round(activeConnectionDrag.startPoint.x)));
            line.setAttribute("y1", String(Math.round(activeConnectionDrag.startPoint.y)));
            line.setAttribute("x2", String(Math.round(endPoint.x)));
            line.setAttribute("y2", String(Math.round(endPoint.y)));
        };

        const clearConnectionDragPreview = () => {
            const previewLine = edgesLayerElement.querySelector("#builder-connection-preview-line");
            if (previewLine) {
                previewLine.remove();
            }
            if (activeConnectionDrag?.targetPort instanceof HTMLElement) {
                activeConnectionDrag.targetPort.classList.remove("node-port-connect-target");
            }
        };

        const finishConnectionDrag = (event) => {
            if (!activeConnectionDrag) {
                return;
            }

            const drag = activeConnectionDrag;
            const target = drag.moved ? connectionTargetAtPointer(event, drag.sourceItem) : null;
            const targetItem =
                target?.item instanceof HTMLElement
                    ? target.item
                    : drag.targetPort instanceof HTMLElement
                      ? drag.targetPort.closest(".builder-node")
                      : null;

            window.removeEventListener("mousemove", handleConnectionDragMove);
            window.removeEventListener("mouseup", finishConnectionDrag);
            window.removeEventListener("blur", cancelConnectionDrag);
            clearConnectionDragPreview();
            activeConnectionDrag = null;

            if (targetItem instanceof HTMLElement && targetItem !== drag.sourceItem) {
                connectNodeItems(drag.sourceItem, targetItem);
                return;
            }

            if (drag.moved) {
                setStatusMessage("Connection canceled.", TONE_NEUTRAL);
                return;
            }

            setStatusMessage(
                "Connection source selected. Drag to a downstream input handle, click an input handle, or Shift+click a downstream node.",
                TONE_NEUTRAL,
            );
        };

        const cancelConnectionDrag = () => {
            if (!activeConnectionDrag) {
                return;
            }
            window.removeEventListener("mousemove", handleConnectionDragMove);
            window.removeEventListener("mouseup", finishConnectionDrag);
            window.removeEventListener("blur", cancelConnectionDrag);
            clearConnectionDragPreview();
            activeConnectionDrag = null;
            setStatusMessage("Connection canceled.", TONE_NEUTRAL);
        };

        function handleConnectionDragMove(event) {
            if (!activeConnectionDrag) {
                return;
            }

            const dx = event.clientX - activeConnectionDrag.startClientX;
            const dy = event.clientY - activeConnectionDrag.startClientY;
            if (!activeConnectionDrag.moved && Math.hypot(dx, dy) < 4) {
                return;
            }

            activeConnectionDrag.moved = true;
            event.preventDefault();
            const endPoint = pointerPointForSurface(event);
            activeConnectionDrag.lastPoint = endPoint;
            renderConnectionDragPreview(endPoint);

            const target = connectionTargetAtPointer(event, activeConnectionDrag.sourceItem);
            setPortDropTarget(target?.port instanceof HTMLElement ? target.port : null);
        }

        const beginConnectionDrag = (sourceItem, event) => {
            if (!(sourceItem instanceof HTMLElement)) {
                return;
            }

            activeConnectionDrag = {
                sourceItem,
                startClientX: event.clientX,
                startClientY: event.clientY,
                startPoint: connectionPointForPort(sourceItem, PORT_DIR_OUT),
                moved: false,
                lastPoint: null,
                targetPort: null,
            };
            window.addEventListener("mousemove", handleConnectionDragMove);
            window.addEventListener("mouseup", finishConnectionDrag);
            window.addEventListener("blur", cancelConnectionDrag);
        };

        const renderConnectionsEditor = (node) => {
            if (!(connectionSection instanceof HTMLElement) || !(connectionEditor instanceof HTMLElement)) {
                return;
            }

            setConnectionError("");
            clearChildren(connectionEditor);

            const nodes = readNodes(gridElement);
            const edges = parseEdgeData(edgesDataElement);
            const outgoingEdges = edges.filter((edge) => edge.from === node.slug);
            connectionSection.classList.toggle("hidden", !canHaveOutgoingConnection(node.kind));
            if (!canHaveOutgoingConnection(node.kind)) {
                return;
            }

            if (connectionStatus instanceof HTMLElement) {
                connectionStatus.textContent = `${outgoingEdges.length} downstream`;
            }
            if (connectionHelp instanceof HTMLElement) {
                connectionHelp.textContent =
                    "Choose downstream steps here, drag from an output handle, or select a producer and Shift+click a target.";
            }

            if (canHaveOutgoingConnection(node.kind)) {
                const outgoingGroup = document.createElement("div");
                outgoingGroup.className = "space-y-2";
                const outgoingLabel = document.createElement("label");
                outgoingLabel.className = "block text-xs text-base-content/70";
                outgoingLabel.htmlFor = "builder-connection-outgoing-select";
                outgoingLabel.textContent = "Add outgoing link";

                const outgoingControls = document.createElement("div");
                outgoingControls.className = "flex items-center gap-2";
                const outgoingSelect = document.createElement("select");
                outgoingSelect.className = "select select-bordered select-sm w-full";
                outgoingSelect.id = "builder-connection-outgoing-select";
                const outgoingPlaceholder = document.createElement("option");
                outgoingPlaceholder.value = "";
                outgoingPlaceholder.textContent = "Select target node";
                outgoingSelect.appendChild(outgoingPlaceholder);

                nodes
                    .filter((candidate) => candidate.slug !== node.slug)
                    .forEach((candidate) => {
                        const alreadyLinked = outgoingEdges.some((edge) => edge.to === candidate.slug);
                        if (alreadyLinked) {
                            return;
                        }
                        const error = validateEdgeConnection(nodes, edges, node.slug, candidate.slug);
                        if (error !== "") {
                            return;
                        }
                        const option = document.createElement("option");
                        option.value = candidate.slug;
                        option.textContent = `${candidate.slug} (${candidate.kind})`;
                        outgoingSelect.appendChild(option);
                    });

                const addOutgoingButton = document.createElement("button");
                addOutgoingButton.type = "button";
                addOutgoingButton.className = "btn btn-xs btn-outline";
                addOutgoingButton.id = "builder-connection-outgoing-add-btn";
                addOutgoingButton.textContent = "Add";

                outgoingControls.appendChild(outgoingSelect);
                outgoingControls.appendChild(addOutgoingButton);
                outgoingGroup.appendChild(outgoingLabel);
                outgoingGroup.appendChild(outgoingControls);

                const outgoingList = document.createElement("div");
                outgoingList.className = "space-y-1";
                outgoingList.id = "builder-connection-outgoing-list";
                if (outgoingEdges.length === 0) {
                    const empty = document.createElement("p");
                    empty.className = "text-[11px] text-base-content/60";
                    empty.textContent = "No outgoing links.";
                    outgoingList.appendChild(empty);
                } else {
                    outgoingEdges.forEach((edge) => {
                        const row = document.createElement("div");
                        row.className =
                            "flex items-center justify-between gap-2 rounded border border-base-300 bg-base-100 px-2 py-1";
                        const label = document.createElement("span");
                        label.className = "text-xs";
                        label.textContent = `${edge.from} -> ${edge.to}`;
                        const removeButton = document.createElement("button");
                        removeButton.type = "button";
                        removeButton.className = "btn btn-ghost btn-xs";
                        removeButton.textContent = "Remove";
                        removeButton.setAttribute("data-remove-outgoing-to", edge.to);
                        row.appendChild(label);
                        row.appendChild(removeButton);
                        outgoingList.appendChild(row);
                    });
                }

                outgoingGroup.appendChild(outgoingList);
                connectionEditor.appendChild(outgoingGroup);
            }
        };

        const odcsPort = document.getElementById("builder-odcs-port");
        const odcsCurrent = document.getElementById("builder-odcs-current");
        const odcsSection = document.getElementById("builder-odcs-section");
        const odcsInlineJSON = document.getElementById("builder-odcs-inline-json");
        const odcsInlineError = document.getElementById("builder-odcs-inline-error");
        const syncODCSSelection = (node) => {
            if (!(odcsPort instanceof HTMLSelectElement)) return;
            const previousPort = odcsPort.value;
            odcsPort.replaceChildren();
            Object.keys(odcsPortFields)
                .filter((port) => port.startsWith(`${node.kind}.`))
                .filter(
                    (port) =>
                        port !== "transform.input" || !hasObjectEntries(node.originalStep?.transform?.input_contracts),
                )
                .forEach((port) => {
                    const option = document.createElement("option");
                    option.value = port;
                    option.textContent = port.endsWith("input") ? "Input contract" : "Output contract";
                    odcsPort.appendChild(option);
                });
            if (node.kind === KIND_TRANSFORM) {
                const inputs =
                    buildBuilderStepInputMaps(
                        readNodes(gridElement).map(normalizeNodeForExport),
                        parseEdgeData(edgesDataElement),
                        bindingsState,
                    ).transformInputsByStep.get(node.stepSlug) || {};
                Object.keys({ ...inputs, ...node.originalStep?.transform?.input_contracts })
                    .sort()
                    .forEach((name) => {
                        const option = document.createElement("option");
                        option.value = `transform.input.${name}`;
                        option.textContent = `Input: ${name}`;
                        odcsPort.appendChild(option);
                    });
            }
            if (Array.from(odcsPort.options).some((option) => option.value === previousPort))
                odcsPort.value = previousPort;
            else if (hasObjectEntries(node.originalStep?.transform?.input_contracts)) {
                const name = Object.keys(node.originalStep.transform.input_contracts).sort()[0];
                odcsPort.value = `transform.input.${name}`;
            }
            Object.entries(odcsPortFields).forEach(([, [kind, direction, , editorID, fieldID]]) => {
                const referenced = Boolean(node.originalStep?.[kind]?.[`${direction}_contract_ref`]);
                document.getElementById(editorID)?.classList.toggle("hidden", referenced);
                document.getElementById(fieldID)?.closest("details")?.classList.toggle("hidden", referenced);
            });
            const fields = odcsPortFields[odcsPort.value];
            const ref = odcsPortReference(node, odcsPort.value);
            const hasFullInlineEditor = odcsPort.value === "input.output" || Boolean(namedODCSInput(odcsPort.value));
            document
                .getElementById("builder-odcs-inline-agreement")
                ?.classList.toggle("hidden", !hasFullInlineEditor || Boolean(ref));
            if (odcsInlineJSON instanceof HTMLTextAreaElement)
                odcsInlineJSON.value = formatJSONMap(odcsPortInlineContract(node, odcsPort.value));
            if (odcsInlineJSON instanceof HTMLTextAreaElement) odcsInlineJSON.setCustomValidity("");
            if (odcsInlineError) odcsInlineError.textContent = "";
            for (const [id, key] of [
                ["builder-odcs-representation", "representation"],
                ["builder-odcs-root-shape", "root_shape"],
            ]) {
                const select = document.getElementById(id);
                if (select instanceof HTMLSelectElement) {
                    select.value =
                        ref?.[key] ||
                        odcsPortInlineContract(node, odcsPort.value)?.[key] ||
                        (fields && node[fields[2]]?.[key]) ||
                        (node.kind === KIND_INPUT && key === "representation" ? "json" : "");
                    select.disabled = node.kind === KIND_INPUT && key === "representation";
                }
            }
            if (odcsCurrent instanceof HTMLElement) {
                odcsCurrent.textContent = ref
                    ? `${ref.contract_slug} · ${ref.revision_slug} · ${ref.schema_object_id}. Exact reference; inspect the revision for its digest.`
                    : "Using an inline agreement. Choose a catalog revision to reuse a contract.";
            }
            document.getElementById("builder-event-input-help")?.classList.toggle("hidden", node.kind !== KIND_INPUT);
            document.getElementById("builder-odcs-inline")?.classList.toggle("hidden", !ref);
            document
                .getElementById("builder-transform-legacy-input-contract-group")
                ?.classList.toggle("hidden", hasObjectEntries(node.originalStep?.transform?.input_contracts));
        };
        odcsInlineJSON?.addEventListener("change", () => {
            const item = selectedItem();
            if (!item || !(odcsInlineJSON instanceof HTMLTextAreaElement) || !(odcsPort instanceof HTMLSelectElement))
                return;
            let contract;
            try {
                contract = odcsInlineJSON.value.trim() ? JSON.parse(odcsInlineJSON.value) : {};
                if (!isPlainObject(contract)) throw new Error("object required");
            } catch (_) {
                if (odcsInlineError) odcsInlineError.textContent = "Enter a JSON object for this agreement.";
                odcsInlineJSON.setCustomValidity("Enter a JSON object for this agreement.");
                refreshValidation();
                return;
            }
            const node = readNodeDataFromElement(item);
            if (!setODCSPortInlineContract(node, odcsPort.value, contract)) {
                if (odcsInlineError)
                    odcsInlineError.textContent = "Clear the legacy input agreement before adding named requirements.";
                odcsInlineJSON.setCustomValidity("Clear the legacy input agreement before adding named requirements.");
                refreshValidation();
                return;
            }
            if (odcsInlineError) odcsInlineError.textContent = "";
            odcsInlineJSON.setCustomValidity("");
            applyNodeDataToElement(item, node);
            syncODCSSelection(node);
            saveAndValidate();
        });
        if (odcsPort instanceof HTMLSelectElement) {
            odcsPort.addEventListener("change", () => {
                const item = selectedItem();
                if (item) syncODCSSelection(readNodeDataFromElement(item));
            });
        }
        for (const [id, key] of [
            ["builder-odcs-representation", "representation"],
            ["builder-odcs-root-shape", "root_shape"],
        ]) {
            document.getElementById(id)?.addEventListener("change", (event) => {
                const item = selectedItem();
                if (!item || !(odcsPort instanceof HTMLSelectElement)) return;
                const node = readNodeDataFromElement(item);
                if (!setODCSPortShape(node, odcsPort.value, key, event.target.value)) return;
                applyNodeDataToElement(item, node);
                syncODCSSelection(node);
                saveAndValidate();
            });
        }
        if (odcsSection instanceof HTMLElement) {
            odcsSection.addEventListener("click", (event) => {
                const button =
                    event.target instanceof HTMLElement
                        ? event.target.closest("[data-odcs-select], #builder-odcs-inline")
                        : null;
                const item = selectedItem();
                if (!button || !item || !(odcsPort instanceof HTMLSelectElement)) return;
                const node = readNodeDataFromElement(item);
                const inputMaps = buildBuilderStepInputMaps(
                    readNodes(gridElement).map(normalizeNodeForExport),
                    parseEdgeData(edgesDataElement),
                    bindingsState,
                );
                const inputCount = Object.keys(inputMaps.transformInputsByStep.get(node.stepSlug) || {}).length;
                const reference = button.hasAttribute("data-odcs-select")
                    ? {
                          contract_slug: button.dataset.contract,
                          revision_slug: button.dataset.revision,
                          schema_object_id: button.dataset.object,
                      }
                    : null;
                if (reference) {
                    for (const [id, key] of [
                        ["builder-odcs-representation", "representation"],
                        ["builder-odcs-root-shape", "root_shape"],
                    ]) {
                        const value = document.getElementById(id)?.value;
                        if (value) reference[key] = value;
                    }
                }
                if (!setODCSPortRef(node, odcsPort.value, reference, inputCount)) {
                    if (odcsCurrent)
                        odcsCurrent.textContent =
                            "Use named input agreements or one legacy input agreement. Clear the other mode before switching; legacy input requires exactly one relation.";
                    return;
                }
                applyNodeDataToElement(item, node);
                showSelection(item);
                scheduleRender();
                saveAndValidate();
            });
        }

        const edgePreview = document.getElementById("builder-edge-preview");
        const renderEdgeDiagnostics = () => {
            edgePreview?.querySelectorAll("[data-server-diagnostic]").forEach((element) => {
                element.remove();
            });
            if (!selectedEdge) return;
            const messages = serverDiagnostics.filter(
                (entry) => entry.edge?.from === selectedEdge.from && entry.edge?.to === selectedEdge.to,
            );
            const elements = messages.map((entry) => {
                const element = document.createElement("p");
                element.className = "text-sm text-error";
                element.dataset.serverDiagnostic = "";
                element.textContent = entry.message;
                return element;
            });
            edgePreview?.querySelector(".builder-inspector-identifier")?.after(...elements);
        };
        let selectedEvidenceRun = "";
        let selectedEvidenceScope = "";
        const attachRunEvidence = (container, from, to) => {
            const status = document.createElement("p");
            status.className = "text-xs text-base-content/70";
            status.setAttribute("role", "status");
            const body = document.createElement("div");
            container.append(status, body);
            const context = () => ({
                org_slug: getActiveOrganizationSlug(),
                project_slug: getActiveProjectSlug(),
                environment_slug: getActiveTargetEnvironmentSlug(),
                pipeline_slug: options?.pipeline?.slug || "",
            });
            const scope = context();
            if (selectedEvidenceScope !== JSON.stringify(scope)) {
                selectedEvidenceRun = "";
                selectedEvidenceScope = JSON.stringify(scope);
            }
            if (
                !scope.org_slug ||
                !scope.project_slug ||
                !scope.environment_slug ||
                !scope.pipeline_slug ||
                scope.pipeline_slug === DRAFT_PIPELINE_SLUG
            ) {
                status.textContent = "Select a saved pipeline and runtime environment to inspect recorded runs.";
                return;
            }
            let cursor = "";
            let generation = 0;
            const retry = (label) => {
                const button = document.createElement("button");
                button.type = "button";
                button.className = "btn btn-xs btn-outline";
                button.dataset.builderRunReload = "";
                button.textContent = label;
                body.replaceChildren(button);
            };
            const load = async (nextCursor = cursor) => {
                cursor = nextCursor;
                const request = ++generation;
                status.textContent = "Loading recorded run evidence…";
                body.setAttribute("aria-busy", "true");
                const current = () =>
                    request === generation &&
                    container.isConnected &&
                    selectedEdge?.from === from &&
                    selectedEdge?.to === to &&
                    JSON.stringify(context()) === JSON.stringify(scope);
                try {
                    const html = await builderAPI.loadBuilderRunEvidence({
                        ...scope,
                        from_step: from,
                        to_step: to,
                        run_slug: selectedEvidenceRun,
                        cursor,
                    });
                    if (!current()) return;
                    const fragment = new DOMParser()
                        .parseFromString(html, "text/html")
                        .querySelector("[data-builder-run-fragment]");
                    if (!fragment) throw new Error("Run evidence unavailable. Sign in and retry.");
                    body.replaceChildren(fragment);
                    status.textContent = "";
                } catch (error) {
                    if (!current()) return;
                    status.textContent = error.message;
                    retry("Retry loading runs");
                } finally {
                    if (current()) body.removeAttribute("aria-busy");
                }
            };
            body.addEventListener("change", (event) => {
                if (!event.target.matches("[data-builder-run-select]")) return;
                selectedEvidenceRun = event.target.value;
                load();
            });
            body.addEventListener("click", (event) => {
                const button = event.target.closest("[data-builder-run-cursor], [data-builder-run-reload]");
                if (!button) return;
                load(button.hasAttribute("data-builder-run-cursor") ? button.dataset.builderRunCursor : cursor);
            });
            retry("Choose recorded run");
            if (selectedEvidenceRun) load();
        };
        const schemaPanel = document.getElementById("builder-schema-panel");
        const schemaStatus = document.getElementById("builder-schema-status");
        const schemaLines = document.getElementById("builder-schema-lines");
        const schemaDataset = document.getElementById("builder-schema-dataset");
        const schemaHint = document.getElementById("builder-schema-hint");
        const schemaApply = document.getElementById("builder-schema-apply");
        const schemaRefresh = document.getElementById("builder-schema-refresh");
        let schemaIdentity = "";
        let schemaDatasets = [];
        let schemaTimer;
        let schemaGeneration = 0;
        const schemaContext = () => {
            const item = selectedItem();
            if (!item) return null;
            const node = readNodeDataFromElement(item);
            if (![KIND_QUERY, KIND_SINK].includes(node.kind)) return null;
            const ref = node.kind === KIND_SINK ? node.sinkDataSourceSlug : node.dataSourceSlug;
            const resolution = referenceConfiguration(ref);
            const org = getActiveOrganizationSlug();
            return {
                item,
                node,
                org,
                source: resolution.source,
                hint: resolution.hint,
                key: [org, getActiveTargetEnvironmentSlug(), node.slug, ref, resolution.source?.slug].join(":"),
            };
        };
        const schemaOperationPanel = document.getElementById("builder-schema-operation");
        let schemaOperation = null;
        const updateSchemaProposal = () => {
            const context = schemaContext();
            const dataset = schemaDataset?.value === "" ? null : schemaDatasets[Number(schemaDataset?.value)];
            const port = context?.node.kind === KIND_SINK ? "sink.input" : "query.output";
            const pinned = context && odcsPortReference(context.node, port);
            schemaOperation =
                context?.node.kind === KIND_QUERY
                    ? builderAPI.proposedSourceOperation(context.source?.type, dataset)
                    : null;
            schemaOperationPanel?.classList.toggle("hidden", !schemaOperation);
            const operationPreview = document.getElementById("builder-schema-operation-preview");
            if (operationPreview)
                operationPreview.textContent = schemaOperation ? JSON.stringify(schemaOperation, null, 2) : "";
            schemaApply.disabled = !dataset?.fields?.length || Boolean(pinned);
            schemaHint.textContent = pinned
                ? "This port uses a pinned ODCS reference. Switch to an inline contract before adding fields."
                : dataset?.warning ||
                  (dataset?.fields?.length
                      ? `${dataset.fields.length} fields available. Existing fields keep their definitions.`
                      : "No typed field proposal is available. Inspect the cached metadata above.");
        };
        const loadSchema = async (refresh = false, remainingPolls = 30) => {
            const context = schemaContext();
            clearTimeout(schemaTimer);
            const generation = ++schemaGeneration;
            schemaDatasets = [];
            schemaLines.textContent = "";
            schemaDataset.replaceChildren();
            updateSchemaProposal();
            if (!context?.source) {
                schemaStatus.textContent = context?.hint || "Select a bound source or sink.";
                if (schemaRefresh) schemaRefresh.disabled = true;
                return;
            }
            if (schemaRefresh) schemaRefresh.disabled = true;
            schemaStatus.textContent = refresh ? "Requesting schema refresh…" : "Loading cached schema…";
            const current = () => generation === schemaGeneration && schemaContext()?.key === context.key;
            try {
                if (refresh) await builderAPI.refreshDataSourceSchema(context.org, context.source.slug);
                const schema = await builderAPI.loadDataSourceSchema(context.org, context.source.slug);
                if (!current()) return;
                schemaStatus.textContent =
                    schema.error ||
                    {
                        not_imported: "No cached schema yet. Refresh schema to import it.",
                        importing: "Schema import in progress…",
                        failed: "Schema import failed. Refresh schema to retry.",
                        ready: `Cached schema · ${context.source.name || context.source.slug}`,
                    }[schema.status] ||
                    "Schema status unavailable.";
                schemaLines.textContent = (schema.lines || []).join("\n");
                schemaDatasets = Array.isArray(schema.datasets) ? schema.datasets : [];
                const placeholder = document.createElement("option");
                placeholder.value = "";
                placeholder.textContent = "Select a dataset";
                schemaDataset.append(placeholder);
                schemaDataset.disabled = schemaDatasets.length === 0;
                schemaDatasets.forEach((dataset, index) => {
                    const option = document.createElement("option");
                    option.value = String(index);
                    option.textContent = dataset.name;
                    schemaDataset.append(option);
                });
                updateSchemaProposal();
                if (schema.status === "importing" && remainingPolls > 0 && schemaPanel.open)
                    schemaTimer = setTimeout(() => loadSchema(false, remainingPolls - 1), 1000);
                else if (schema.status === "importing")
                    schemaStatus.textContent = "Schema import is still running. Reload cache to check progress.";
            } catch (error) {
                if (current()) schemaStatus.textContent = error.message;
            } finally {
                if (current() && schemaRefresh) schemaRefresh.disabled = false;
            }
        };
        schemaPanel?.addEventListener("toggle", () => {
            if (schemaPanel.open && !schemaPanel.classList.contains("hidden")) loadSchema();
            else {
                clearTimeout(schemaTimer);
                schemaGeneration++;
            }
        });
        document.getElementById("builder-schema-reload")?.addEventListener("click", () => loadSchema());
        schemaRefresh?.addEventListener("click", () => loadSchema(true));
        schemaDataset?.addEventListener("change", updateSchemaProposal);
        schemaApply?.addEventListener("click", () => {
            const context = schemaContext();
            const dataset = schemaDataset.value === "" ? null : schemaDatasets[Number(schemaDataset.value)];
            if (!context || context.key !== schemaIdentity || !dataset?.fields?.length) return;
            const port = context.node.kind === KIND_SINK ? "sink.input" : "query.output";
            if (odcsPortReference(context.node, port)) return;
            const field = odcsPortFields[port][2];
            context.node[field] = builderAPI.addProposedContractFields(context.node[field], dataset.fields);
            applyNodeDataToElement(context.item, context.node);
            showSelection(context.item);
            saveAndValidate();
        });
        document.getElementById("builder-schema-operation-apply")?.addEventListener("click", () => {
            const context = schemaContext();
            if (!context || context.key !== schemaIdentity || !schemaOperation) return;
            const operation = cloneJSONValue(schemaOperation);
            context.node.querySlug = "";
            context.node.operationInline = operation;
            context.node.useInlineSQL = typeof operation.sql === "string";
            context.node.inlineSQL = operation.sql || "";
            context.node.useInlineOperation = !context.node.useInlineSQL;
            context.node.operationType = queryOperationTypeFromDataSourceType(context.source.type);
            applyNodeDataToElement(context.item, context.node);
            showSelection(context.item);
            scheduleRender();
            saveAndValidate();
            setStatusMessage(
                "Source operation replaced in this draft. Review the contract and validate before running.",
                TONE_NEUTRAL,
            );
        });
        const syncSchemaSelection = (item) => {
            if (!schemaPanel) return;
            const context = item instanceof HTMLElement ? schemaContext() : null;
            schemaPanel.classList.toggle("hidden", !context);
            const identity = context?.key || "";
            if (identity === schemaIdentity) {
                updateSchemaProposal();
                return;
            }
            schemaIdentity = identity;
            clearTimeout(schemaTimer);
            schemaGeneration++;
            schemaDatasets = [];
            updateSchemaProposal();
            if (context && schemaPanel.open) loadSchema();
        };

        let mappingSelectionKey = "";
        let mappingMountedIdentity = "";
        let mappingController = null;
        let mappingOpener = null;
        const mappingWorkspace = document.getElementById("builder-mapping-workspace");
        const mappingWorkspaceIsOpen = () => mappingWorkspace?.matches(":popover-open");
        const refreshMappingPreview = () => {
            const preview = document.getElementById("builder-mapping-preview");
            const attachment = mappingAttachments.get(selectedItem());
            if (preview && attachment) attachment.renderSummary(preview);
        };
        const positionMappingWorkspace = () => {
            if (!mappingWorkspaceIsOpen()) return;
            const anchor = selectedItem()?.querySelector(".builder-mapping-attachment");
            if (!anchor) return;
            const rect = anchor.getBoundingClientRect();
            const canvas = stageElement.getBoundingClientRect();
            const minY = Math.max(12, Math.min(canvas.top + 8, innerHeight - 240));
            mappingWorkspace.style.maxHeight = `${Math.max(
                200,
                Math.min(mappingWorkspace.dataset.editing === "true" ? 680 : 520, innerHeight - minY - 12),
            )}px`;
            const panel = mappingWorkspace.getBoundingClientRect();
            const right = rect.right + 12;
            const leftSide = rect.left - panel.width - 12;
            const left =
                right + panel.width <= innerWidth - 12
                    ? right
                    : leftSide >= 12
                      ? leftSide
                      : Math.max(12, Math.min(rect.left, innerWidth - panel.width - 12));
            const top = Math.max(minY, Math.min(rect.top, innerHeight - panel.height - 12));
            mappingWorkspace.style.left = `${Math.round(left)}px`;
            mappingWorkspace.style.top = `${Math.round(top)}px`;
        };
        const closeMappingWorkspace = ({ restoreFocus = true } = {}) => {
            if (mappingWorkspaceIsOpen()) mappingWorkspace.hidePopover();
            mappingOpener?.setAttribute("aria-expanded", "false");
            if (restoreFocus && mappingOpener?.isConnected) mappingOpener.focus({ preventScroll: true });
        };
        const setMappingEditing = (editing) => {
            if (!mappingWorkspace) return;
            mappingWorkspace.dataset.editing = String(editing);
            for (const id of [
                "builder-mapping-preview",
                "builder-mapping-preview-help",
                "builder-mapping-preview-actions",
            ])
                document.getElementById(id)?.classList.toggle("hidden", editing);
            document.getElementById("builder-field-mapping")?.classList.toggle("hidden", !editing);
            document.getElementById("builder-preview-mapping-btn")?.classList.toggle("hidden", !editing);
            if (editing) mappingController?.open();
            else refreshMappingPreview();
            positionMappingWorkspace();
            document.getElementById("builder-mapping-workspace-title")?.focus({ preventScroll: true });
        };
        const openMappingWorkspace = (item, opener, { edit = false } = {}) => {
            if (!(mappingWorkspace instanceof HTMLElement) || !(item instanceof HTMLElement)) return;
            canvasFitting = false;
            mappingWorkspace.dataset.editing = String(edit);
            selectNodeItem(item, { openProperties: false });
            mappingOpener?.setAttribute("aria-expanded", "false");
            mappingOpener = opener;
            mappingOpener.setAttribute("aria-expanded", "true");
            if (!mappingWorkspaceIsOpen()) mappingWorkspace.showPopover();
            setMappingEditing(edit);
        };
        document.getElementById("builder-close-mapping-btn")?.addEventListener("click", () => closeMappingWorkspace());
        document.getElementById("builder-edit-mapping-btn")?.addEventListener("click", () => setMappingEditing(true));
        document
            .getElementById("builder-preview-mapping-btn")
            ?.addEventListener("click", () => setMappingEditing(false));
        document
            .getElementById("builder-open-mapping-btn")
            ?.addEventListener("click", (event) => openMappingWorkspace(selectedItem(), event.currentTarget));
        mappingWorkspace?.addEventListener("toggle", () => {
            if (!mappingWorkspaceIsOpen()) mappingOpener?.setAttribute("aria-expanded", "false");
        });
        mappingWorkspace?.addEventListener("keydown", (event) => {
            event.stopPropagation();
            if (event.key === "Escape") {
                event.preventDefault();
                closeMappingWorkspace();
            }
        });
        document.addEventListener("scroll", positionMappingWorkspace, { capture: true, passive: true });
        window.addEventListener("resize", positionMappingWorkspace);
        const showSelection = (item, preserveContractEditor = null) => {
            const isTransform = item instanceof HTMLElement && item.getAttribute(ATTR_KIND) === KIND_TRANSFORM;
            document.getElementById("builder-mapping-properties")?.classList.toggle("hidden", !isTransform);
            if (!isTransform) closeMappingWorkspace({ restoreFocus: false });
            const mappingParent = document.getElementById("builder-mapping-workspace-parent");
            if (mappingParent && isTransform)
                mappingParent.textContent = `Attached to ${item.getAttribute(ATTR_NODE_SLUG)}`;
            syncSchemaSelection(item);
            if (edgePreview instanceof HTMLElement) edgePreview.classList.add("hidden");
            if (!(item instanceof HTMLElement)) {
                if (propertyEmpty instanceof HTMLElement) {
                    propertyEmpty.classList.remove("hidden");
                }
                if (propertyForm instanceof HTMLElement) {
                    propertyForm.classList.add("hidden");
                }
                if (bindingSection instanceof HTMLElement) {
                    bindingSection.classList.add("hidden");
                }
                if (connectionSection instanceof HTMLElement) {
                    connectionSection.classList.add("hidden");
                }
                return;
            }

            const node = readNodeDataFromElement(item);
            if (propertyEmpty instanceof HTMLElement) {
                propertyEmpty.classList.add("hidden");
            }
            if (propertyForm instanceof HTMLElement) {
                propertyForm.classList.remove("hidden");
            }
            if (propertyKind instanceof HTMLElement) {
                propertyKind.textContent = node.stepName || node.slug;
            }
            if (propertyType instanceof HTMLElement) {
                if (node.kind === KIND_SINK) {
                    propertyType.textContent = node.sinkOperationType || "sink operation";
                } else if (node.kind === KIND_TRANSFORM) {
                    propertyType.textContent = "transform";
                } else {
                    propertyType.textContent = node.operationType || node.nodeType || "query";
                }
            }
            if (propertyType instanceof HTMLElement)
                propertyType.textContent = `${kindLabel(node.kind)} · ${propertyType.textContent}`;
            const propertyIdentifier = document.getElementById("builder-property-identifier");
            if (propertyIdentifier) propertyIdentifier.textContent = node.slug;
            if (fieldSlug instanceof HTMLInputElement) {
                fieldSlug.value = node.slug;
            }
            const dataSourceField = getDataSourceField();
            const queryField = getQueryField();
            const dataSourceSlug = node.dataSourceSlug || "";
            const querySlug = node.querySlug || "";
            if (dataSourceField instanceof HTMLSelectElement) {
                ensureSelectHasOption(dataSourceField, dataSourceSlug, "authored logical ref");
                dataSourceField.value = dataSourceSlug;
            } else if (dataSourceField instanceof HTMLInputElement) {
                dataSourceField.value = dataSourceSlug;
            }
            if (queryField instanceof HTMLSelectElement) {
                ensureSelectHasOption(queryField, querySlug, "authored saved query");
                queryField.value = querySlug;
            } else if (queryField instanceof HTMLInputElement) {
                queryField.value = querySlug;
            }
            const inlineSQLField = getInlineSQLField();
            if (inlineSQLField instanceof HTMLTextAreaElement) {
                inlineSQLField.value = node.inlineSQL || "";
            }
            const inlineOperationField = getInlineOperationField();
            if (inlineOperationField instanceof HTMLTextAreaElement) {
                inlineOperationField.value = formatJSONMap(node.operationInline);
            }
            syncQuerySourceModeUI(Boolean(node.useInlineSQL));
            if (
                node.kind === KIND_QUERY &&
                dataSourceField instanceof HTMLSelectElement &&
                queryField instanceof HTMLSelectElement
            ) {
                const organizationSlug = getActiveOrganizationSlug();
                const dataSourceSlug = dataSourceField.value.trim();
                applyQueryDataSourceContext(dataSourceSlug, node);
                renderSourceGuidedInlineEditor(node, updateSelectedNode);
                // For non-SQL types, sync the operation inline mode
                const meta = dataSourceMetaBySlug.get(dataSourceSlug.trim());
                const dataSourceType = meta?.type || "";
                const context = queryContextForDataSourceType(dataSourceType);
                if (context.supportsInlineOperationJSON) {
                    syncOperationSourceModeUI(Boolean(node.useInlineOperation));
                }
                if (organizationSlug && dataSourceSlug) {
                    listQueriesForDataSource(organizationSlug, dataSourceSlug).then((queryOptions) => {
                        setSelectOptions(queryField, queryOptions, "Select query", node.querySlug);
                    });
                }
            }
            if (fieldStepSlug instanceof HTMLInputElement) {
                fieldStepSlug.value = node.slug;
            }
            if (fieldStepName instanceof HTMLInputElement) {
                fieldStepName.value = node.stepName;
            }
            if (fieldParameters instanceof HTMLTextAreaElement) {
                fieldParameters.value = formatJSONMap(node.parameters);
            }
            renderParameterEditor(node.parameters);
            if (fieldTransformSQL instanceof HTMLTextAreaElement) {
                fieldTransformSQL.value = node.transformSQL || "";
            }
            writeJSONObjectField(fieldQueryOutputContract, node.queryOutputContract);
            writeIncrementalEditor(node.queryIncremental);
            writeJSONObjectField(fieldTransformInputContract, node.transformInputContract);
            writeJSONObjectField(fieldTransformOutputContract, node.transformOutputContract);
            writeJSONObjectField(fieldSinkInputContract, node.sinkInputContract);
            if (preserveContractEditor !== queryOutputContractEditor)
                renderContractEditor(queryOutputContractEditor, node.queryOutputContract);
            if (preserveContractEditor !== transformInputContractEditor)
                renderContractEditor(transformInputContractEditor, node.transformInputContract);
            if (preserveContractEditor !== transformOutputContractEditor)
                renderContractEditor(transformOutputContractEditor, node.transformOutputContract);
            if (preserveContractEditor !== sinkInputContractEditor)
                renderContractEditor(sinkInputContractEditor, node.sinkInputContract);
            const sinkDataSourceField = getSinkDataSourceField();
            const sinkQueryField = getSinkQueryField();
            const sinkInlineOperationField = getSinkInlineOperationField();
            const s3Output = document.getElementById("builder-s3-output");
            const isS3Sink = node.kind === KIND_SINK && node.sinkBackend === "s3";
            s3Output?.classList.toggle("hidden", !isS3Sink);
            if (isS3Sink && s3Output) {
                document.getElementById("builder-s3-key").value = node.originalStep?.sink?.config?.key || "";
                const format = node.originalStep?.sink?.config?.format || "";
                const field = document.getElementById("builder-s3-format");
                ensureSelectHasOption(field, format, "authored format");
                field.value = format;
            }
            const replayPolicyField = document.getElementById("builder-field-sink-replay-policy");
            if (node.kind === KIND_SINK && replayPolicyField instanceof HTMLSelectElement) {
                const source = dataSourceMetaBySlug.get(node.sinkDataSourceSlug);
                const descriptor = descriptorForDataSourceType(source?.type, connectorDescriptorByType);
                const labels = {
                    naturally_idempotent: "Operation is safe to repeat",
                    idempotent_with_key: "Use an idempotency key",
                    deduplicated_by_ledger: "Skip confirmed deliveries",
                    unsafe: "No replay guarantee",
                };
                setSelectOptions(
                    replayPolicyField,
                    (descriptor?.supportedSinkReplayStrategies || []).map((value) => ({ value, label: labels[value] })),
                    "Select replay policy",
                    node.sinkIdempotencyClass,
                    "unsupported by this connection",
                );
                replayPolicyField.options[0].disabled = true;
                replayPolicyField.disabled = !descriptor;
                const help = document.getElementById("builder-sink-replay-help");
                if (help) {
                    const explanations = {
                        naturally_idempotent:
                            "Use only when repeating this operation has the same effect, such as overwriting a stable object key.",
                        idempotent_with_key:
                            "The destination must enforce the same idempotency key across repeated attempts.",
                        deduplicated_by_ledger:
                            "Skip deliveries already confirmed by Braidplane. Investigate uncertain delivery before replaying.",
                        unsafe: "This operation has no replay guarantee. Review possible duplicate writes before retrying.",
                    };
                    help.textContent =
                        explanations[node.sinkIdempotencyClass] ||
                        "Choose a replay policy supported by this connection.";
                }
            }
            const sinkHasInlineOperation =
                node.sinkOperationInline &&
                typeof node.sinkOperationInline === "object" &&
                Object.keys(node.sinkOperationInline).length > 0;
            if (sinkDataSourceField instanceof HTMLInputElement || sinkDataSourceField instanceof HTMLSelectElement) {
                const sinkDataSourceSlug = node.sinkDataSourceSlug || "";
                if (sinkDataSourceField instanceof HTMLSelectElement) {
                    ensureSelectHasOption(sinkDataSourceField, sinkDataSourceSlug, "authored logical ref");
                }
                sinkDataSourceField.value = sinkDataSourceSlug;
            }
            if (sinkQueryField instanceof HTMLInputElement || sinkQueryField instanceof HTMLSelectElement) {
                const sinkQuerySlug = node.sinkQuerySlug || "";
                if (sinkQueryField instanceof HTMLSelectElement) {
                    ensureSelectHasOption(sinkQueryField, sinkQuerySlug, "authored saved query");
                }
                sinkQueryField.value = sinkQuerySlug;
            }
            if (sinkInlineOperationField instanceof HTMLTextAreaElement) {
                sinkInlineOperationField.value = formatJSONMap(node.sinkOperationInline);
            }
            if (sourceGroup instanceof HTMLElement) {
                sourceGroup.classList.toggle("hidden", node.kind !== KIND_QUERY);
            }
            if (transformGroup instanceof HTMLElement) {
                transformGroup.classList.toggle("hidden", !isStepKind(node.kind));
            }
            if (stepNameGroup instanceof HTMLElement) {
                stepNameGroup.classList.toggle("hidden", !isStepKind(node.kind));
            }
            if (parametersGroup instanceof HTMLElement) {
                const isFTPOrGitQuery =
                    node.kind === KIND_QUERY && (node.operationType === "ftp" || node.operationType === "git");
                parametersGroup.classList.toggle("hidden", isFTPOrGitQuery || node.kind !== KIND_QUERY);
            }
            if (transformSQLGroup instanceof HTMLElement) {
                transformSQLGroup.classList.toggle("hidden", node.kind !== KIND_TRANSFORM);
            }
            if (transformContractsSection instanceof HTMLElement) {
                transformContractsSection.classList.toggle("hidden", node.kind !== KIND_TRANSFORM);
            }
            if (sinkGroup instanceof HTMLElement) {
                sinkGroup.classList.toggle("hidden", node.kind !== KIND_SINK);
            }
            if (node.kind === KIND_SINK) {
                const organizationSlug = getActiveOrganizationSlug();
                const dataSourceSlug =
                    sinkDataSourceField instanceof HTMLInputElement || sinkDataSourceField instanceof HTMLSelectElement
                        ? sinkDataSourceField.value.trim()
                        : "";
                applySinkDataSourceContext(dataSourceSlug, node);
                renderSinkGuidedInlineEditor(node, updateSelectedNode);
                syncSinkOperationModeUI(Boolean(node.sinkUseInlineOperation) || sinkHasInlineOperation);
                if (organizationSlug && dataSourceSlug && sinkQueryField instanceof HTMLSelectElement) {
                    listQueriesForDataSource(organizationSlug, dataSourceSlug).then((queryOptions) => {
                        setSelectOptions(sinkQueryField, queryOptions, "Select query", node.sinkQuerySlug);
                    });
                }
            }
            renderBindingsEditor(node);
            renderConnectionsEditor(node);
            syncODCSSelection(node);
            const mappingContainer = document.getElementById("builder-field-mapping");
            if (mappingContainer && window.braidplaneFieldMapping) {
                const mappingSteps = () =>
                    buildBuilderStatePayloadFromLiveNodes(
                        readNodes(gridElement),
                        parseEdgeData(edgesDataElement),
                        bindingsState,
                    ).pipeline_spec.steps;
                const steps = mappingSteps();
                const mappingIdentity = () =>
                    JSON.stringify([
                        getActiveOrganizationSlug(),
                        getActiveProjectSlug(),
                        getActiveTargetEnvironmentSlug(),
                        mappingSteps().map((step) => ({ ...step, name: undefined })),
                    ]);
                const identity = mappingIdentity();
                const selectionKey =
                    node.kind === KIND_TRANSFORM
                        ? JSON.stringify([
                              getActiveOrganizationSlug(),
                              getActiveProjectSlug(),
                              getActiveTargetEnvironmentSlug(),
                              node.slug,
                              node.transformSQL,
                          ])
                        : "";
                if (
                    mappingSelectionKey === selectionKey &&
                    mappingContainer.childElementCount > 0 &&
                    (identity === mappingMountedIdentity ||
                        mappingContainer.querySelector("[data-field-mapping-draft]"))
                ) {
                    const warning = mappingContainer.querySelector("[data-field-mapping-context-warning]");
                    if (identity !== mappingMountedIdentity && !warning) {
                        const notice = document.createElement("div");
                        notice.dataset.fieldMappingContextWarning = "";
                        notice.className = "mb-2 space-y-2 text-xs text-warning";
                        notice.setAttribute("role", "alert");
                        const message = document.createElement("p");
                        message.textContent =
                            "Source or destination context changed. Mapping edits are retained, but checks are stale. Reloading discards unapplied edits and fixture text.";
                        const reload = document.createElement("button");
                        reload.type = "button";
                        reload.className = "btn btn-xs btn-outline";
                        reload.textContent = "Discard edits and reload";
                        reload.addEventListener("click", () => {
                            if (selectedItem() !== item) return;
                            mappingSelectionKey = "";
                            showSelection(item);
                        });
                        notice.append(message, reload);
                        mappingContainer.prepend(notice);
                    } else if (identity === mappingMountedIdentity && warning) {
                        warning.remove();
                    }
                    return;
                }
                mappingSelectionKey = selectionKey;
                mappingMountedIdentity = identity;
                mappingController = window.braidplaneFieldMapping.mount(
                    mappingContainer,
                    node.kind === KIND_TRANSFORM
                        ? {
                              org: getActiveOrganizationSlug(),
                              project: getActiveProjectSlug(),
                              environment: getActiveTargetEnvironmentSlug(),
                              resolveSource: referenceConfiguration,
                              step: steps.find((step) => step.slug === node.stepSlug),
                              steps,
                              isCurrent: () => selectedItem() === item && identity === mappingIdentity(),
                              connectInput: (slug) => {
                                  const source = Array.from(gridElement.querySelectorAll(".builder-node")).find(
                                      (candidate) => candidate.getAttribute(ATTR_NODE_SLUG) === slug,
                                  );
                                  mappingSelectionKey = "";
                                  if (!connectNodeItems(source, item))
                                      throw new Error(
                                          "Could not connect that input. Check the connection diagnostic in Properties.",
                                      );
                              },
                              chooseSource: () => {
                                  closeMappingWorkspace({ restoreFocus: false });
                                  isPaletteVisible = true;
                                  applyMainGridLayout();
                                  const source = paletteAsideElement?.querySelector('[data-add-node-kind="query"]');
                                  source?.scrollIntoView({ block: "nearest" });
                                  source?.focus();
                              },
                              applySQL: (sql) => {
                                  fieldTransformSQL.value = sql;
                                  updateSelectedNode();
                              },
                          }
                        : null,
                );
                if (mappingWorkspaceIsOpen() && mappingWorkspace.dataset.editing === "true") mappingController?.open();
            }
        };

        const sortKeysForActionState = (value) => {
            if (Array.isArray(value)) {
                return value.map(sortKeysForActionState);
            }
            if (value !== null && typeof value === "object") {
                return Object.fromEntries(
                    Object.keys(value)
                        .sort()
                        .map((key) => [key, sortKeysForActionState(value[key])]),
                );
            }
            return value;
        };

        const comparableBuilderState = (state) => {
            if (!state || typeof state !== "object" || Array.isArray(state)) {
                return state;
            }

            const pipelineSpec =
                state.pipeline_spec && typeof state.pipeline_spec === "object" && !Array.isArray(state.pipeline_spec)
                    ? { ...state.pipeline_spec }
                    : {};
            const graph =
                state.graph && typeof state.graph === "object" && !Array.isArray(state.graph) ? { ...state.graph } : {};

            if (!Array.isArray(pipelineSpec.steps)) {
                pipelineSpec.steps = [];
            }
            if (!Array.isArray(graph.nodes)) {
                graph.nodes = [];
            }
            if (!Array.isArray(graph.edges)) {
                graph.edges = [];
            }
            if (!Array.isArray(graph.bindings)) {
                graph.bindings = [];
            }

            pipelineSpec.steps = pipelineSpec.steps.slice().sort((left, right) => {
                const leftSlug = typeof left?.slug === "string" ? left.slug : "";
                const rightSlug = typeof right?.slug === "string" ? right.slug : "";
                return leftSlug.localeCompare(rightSlug);
            });
            graph.nodes = graph.nodes.slice().sort((left, right) => {
                const leftSlug = typeof left?.step_slug === "string" ? left.step_slug : "";
                const rightSlug = typeof right?.step_slug === "string" ? right.step_slug : "";
                return leftSlug.localeCompare(rightSlug);
            });
            graph.edges = graph.edges.slice().sort((left, right) => {
                const leftFrom = typeof left?.from_step === "string" ? left.from_step : "";
                const rightFrom = typeof right?.from_step === "string" ? right.from_step : "";
                if (leftFrom !== rightFrom) {
                    return leftFrom.localeCompare(rightFrom);
                }
                const leftTo = typeof left?.to_step === "string" ? left.to_step : "";
                const rightTo = typeof right?.to_step === "string" ? right.to_step : "";
                return leftTo.localeCompare(rightTo);
            });
            graph.bindings = graph.bindings.slice().sort((left, right) => {
                const leftFrom = typeof left?.from_step === "string" ? left.from_step : "";
                const rightFrom = typeof right?.from_step === "string" ? right.from_step : "";
                if (leftFrom !== rightFrom) {
                    return leftFrom.localeCompare(rightFrom);
                }
                const leftFromPort = typeof left?.from_port === "string" ? left.from_port : "";
                const rightFromPort = typeof right?.from_port === "string" ? right.from_port : "";
                if (leftFromPort !== rightFromPort) {
                    return leftFromPort.localeCompare(rightFromPort);
                }
                const leftTo = typeof left?.to_step === "string" ? left.to_step : "";
                const rightTo = typeof right?.to_step === "string" ? right.to_step : "";
                if (leftTo !== rightTo) {
                    return leftTo.localeCompare(rightTo);
                }
                const leftToPort = typeof left?.to_port === "string" ? left.to_port : "";
                const rightToPort = typeof right?.to_port === "string" ? right.to_port : "";
                return leftToPort.localeCompare(rightToPort);
            });

            return sortKeysForActionState({
                pipeline_spec: pipelineSpec,
                graph,
            });
        };

        const normalizeExportBodyForActionState = (state) => {
            return comparableBuilderState(state);
        };

        let persistedActionStateJSON = "";
        let serverValidationSnapshot = "";

        const buildActionStateJSON = () => {
            return JSON.stringify(normalizeExportBodyForActionState(buildExportBody()));
        };

        const hasUnpersistedActionChanges = () => {
            if (persistedActionStateJSON === "") {
                return false;
            }
            return buildActionStateJSON() !== persistedActionStateJSON;
        };

        const markActionStatePersisted = () => {
            persistedActionStateJSON = buildActionStateJSON();
        };

        const refreshValidation = () => {
            if (serverValidationSnapshot && serverValidationSnapshot !== JSON.stringify(buildExportBody())) {
                document.getElementById("builder-server-validation")?.classList.add("hidden");
                serverValidationSnapshot = "";
                serverDiagnostics = [];
                renderEdgeDiagnostics();
                scheduleRender();
            }
            const nodes = readNodes(gridElement);
            const edges = parseEdgeData(edgesDataElement);
            const issues = [
                ...validateNodes(nodes, dataSourceMetaBySlug, connectorDescriptorByType),
                ...validateEdgeTopology(nodes, edges),
                ...validateBindings(nodes, edges, bindingsState),
                ...validateBindingCompleteness(nodes, edges, bindingsState),
                ...validateBoundaryContractDeclarations(nodes, bindingsState),
                ...collectTransformInputGraphConsistencyIssues(
                    buildBuilderStatePayloadFromLiveNodes(nodes, edges, bindingsState),
                ),
            ];
            if (odcsInlineJSON instanceof HTMLTextAreaElement && !odcsInlineJSON.validity.valid) {
                issues.push({ message: odcsInlineJSON.validationMessage, nodeSlug: null });
            }
            const isValid = issues.length === 0;
            const activeCreationForm = document.getElementById("builder-creation-form");
            if (
                validateButton instanceof HTMLButtonElement &&
                activeCreationForm &&
                !validateButton.hasAttribute("aria-busy")
            ) {
                validateButton.disabled = !activeCreationForm.hidden;
            }

            // Apply per-node error badges and border styling.
            if (gridElement instanceof HTMLElement) {
                const errorCounts = new Map();
                issues.forEach((issue) => {
                    if (issue.nodeSlug) {
                        errorCounts.set(issue.nodeSlug, (errorCounts.get(issue.nodeSlug) || 0) + 1);
                    }
                });
                gridElement.querySelectorAll(`.builder-node[${ATTR_NODE_SLUG}]`).forEach((item) => {
                    const slug = item.getAttribute(ATTR_NODE_SLUG) || "";
                    const content = item.querySelector(".builder-node-content");
                    if (!(content instanceof HTMLElement)) {
                        return;
                    }
                    const node = nodes.find((candidate) => candidate.slug === slug);
                    if (node) {
                        const context = normalizeDisclosureContext(
                            node,
                            nodes,
                            edges,
                            bindingsState,
                            issues,
                            dataSourceMetaBySlug,
                        );
                        renderNodeBadges(content, nodeConfigBadges(node, context));
                    }
                    const count = errorCounts.get(slug) || 0;
                    content.classList.toggle("node-card-has-error", count > 0);
                    let badge = item.querySelector(":scope > .node-error-badge");
                    if (count > 0) {
                        if (!badge) {
                            badge = document.createElement("div");
                            badge.className = "node-error-badge";
                            item.appendChild(badge);
                        }
                        badge.textContent = String(count);
                    } else if (badge) {
                        badge.remove();
                    }
                });
            }

            // Toggle validation summary panel styling.
            const validationSummary = validationList instanceof HTMLElement ? validationList.parentElement : null;
            if (validationSummary instanceof HTMLElement) {
                const summaryHeader = validationSummary.querySelector("p");
                validationSummary.classList.toggle("border-error/30", !isValid);
                validationSummary.classList.toggle("bg-error/10", !isValid);
                validationSummary.classList.toggle("border-success/30", isValid && nodes.length > 0);
                validationSummary.classList.toggle("bg-success/10", isValid && nodes.length > 0);
                if (summaryHeader instanceof HTMLElement) {
                    summaryHeader.classList.toggle("text-error", !isValid);
                    summaryHeader.classList.toggle("text-success", isValid && nodes.length > 0);
                }
            }

            if (validationList instanceof HTMLElement) {
                clearChildren(validationList);
                if (issues.length === 0) {
                    const item = document.createElement("li");
                    item.textContent =
                        nodes.length === 0
                            ? "Add a source step to begin. Draft checks run as you build."
                            : "No draft issues found.";
                    validationList.appendChild(item);
                } else {
                    issues.forEach((issue) => {
                        const item = document.createElement("li");
                        item.textContent = issue.message;
                        if (issue.nodeSlug) {
                            item.style.cursor = "pointer";
                            item.title = `Click to select ${issue.nodeSlug}`;
                            item.classList.add("hover:underline");
                            item.addEventListener("click", () => {
                                const target = gridElement.querySelector(
                                    `.builder-node[${ATTR_NODE_SLUG}="${issue.nodeSlug}"]`,
                                );
                                if (!(target instanceof HTMLElement)) {
                                    return;
                                }
                                selectedSlug = issue.nodeSlug;
                                selectedEdge = null;
                                showSelection(target);
                                applySelection(gridElement, selectedSlug);
                                scheduleRender();
                            });
                        }
                        validationList.appendChild(item);
                    });
                }
            }
            const saveAvailability = builderSaveAvailability(options?.pipeline?.slug || "", nodes);
            if (!storageKey) {
                saveAvailability.canPersist = false;
                saveAvailability.reason = "Choose an organization and project before saving this draft.";
            }
            const hasChanges = hasUnpersistedActionChanges();

            if (hasChanges) document.getElementById("builder-matching-draft-notice")?.remove();

            setPrimaryActionDisabledState(
                saveButton,
                runButton,
                publishButton,
                isValid,
                saveAvailability.canPersist,
                hasChanges,
                options?.pipeline?.slug || "",
                pipelineHasDraft,
                getActiveTargetEnvironmentSlug(),
            );
            const newPipelineForm = document.getElementById("builder-creation-form");
            for (const button of [saveButton, publishButton, runButton]) {
                if (button) button.hidden = Boolean(newPipelineForm && !newPipelineForm.hidden);
            }
            if (newPipelineForm && !newPipelineForm.hidden) {
                if (saveButton) saveButton.disabled = true;
                if (publishButton) publishButton.disabled = true;
                if (runButton) runButton.disabled = true;
            }
            setStatusMessage(
                isValid
                    ? saveAvailability.canPersist
                        ? hasChanges
                            ? validationStatusMessage(0)
                            : "No unsaved changes."
                        : saveAvailability.reason
                    : validationStatusMessage(issues.length),
                isValid
                    ? saveAvailability.canPersist
                        ? hasChanges
                            ? TONE_SUCCESS
                            : TONE_NEUTRAL
                        : TONE_NEUTRAL
                    : TONE_ERROR,
            );
            if (newPipelineForm && !newPipelineForm.hidden && isValid) {
                setStatusMessage("Add steps, then use Propose new pipeline above the canvas.", TONE_NEUTRAL);
            }
            if (runReason instanceof HTMLElement) {
                runReason.hidden = Boolean(newPipelineForm && !newPipelineForm.hidden);
                runReason.textContent = runStatusMessage(
                    options?.pipeline?.slug || "",
                    isValid,
                    pipelineHasDraft,
                    getActiveTargetEnvironmentSlug(),
                    runButton?.dataset.canExecute === "true",
                );
            }

            const draftBadge = document.getElementById("builder-draft-badge");
            if (draftBadge instanceof HTMLElement) {
                draftBadge.classList.toggle("hidden", !pipelineHasDraft);
            }

            const draftStatus = document.getElementById("builder-draft-status");
            if (draftStatus instanceof HTMLElement) {
                draftStatus.classList.toggle("hidden", !pipelineHasDraft);
            }

            const statusBadge = document.querySelector(".badge.badge-outline.uppercase");
            if (statusBadge instanceof HTMLElement) {
                statusBadge.classList.remove("badge-success", "badge-warning", "badge-neutral");
                if (nodes.length === 0) {
                    statusBadge.textContent = "Unsaved Canvas";
                    statusBadge.classList.add("badge-neutral");
                } else if (isValid) {
                    statusBadge.textContent = "Draft checks passed";
                    statusBadge.classList.add("badge-success");
                } else {
                    statusBadge.textContent = "Unsaved Changes";
                    statusBadge.classList.add("badge-warning");
                }
            }

            return isValid;
        };

        const saveAndValidate = () => {
            const form = document.getElementById("builder-creation-form");
            const fields = form && !form.hidden ? new FormData(form) : null;
            saveState(storageKey, gridElement, edgesDataElement, bindingsState, {
                slug: fields?.get("pipeline_slug") || options.pipeline?.slug,
                name: fields?.get("name") || options.pipeline?.name,
                sourceFormat: pipelineSourceFormat,
                exportFormat: pipelineExportFormat,
            });
            refreshValidation();
            pushHistorySnapshot();
        };

        let disclosureHoverTimer = null;
        let disclosureNode = null;

        const hideNodeDisclosure = (item = null) => {
            if (disclosureHoverTimer !== null) {
                window.clearTimeout(disclosureHoverTimer);
                disclosureHoverTimer = null;
            }
            if (item instanceof HTMLElement && disclosureNode instanceof HTMLElement && disclosureNode !== item) {
                return;
            }
            if (disclosureNode instanceof HTMLElement) {
                disclosureNode.removeAttribute("aria-describedby");
            }
            disclosureNode = null;
            if (nodeDisclosurePopover instanceof HTMLElement) {
                nodeDisclosurePopover.classList.add("hidden");
                nodeDisclosurePopover.innerHTML = "";
            }
        };

        const currentDisclosureIssues = (nodes, edges) => [
            ...validateNodes(nodes, dataSourceMetaBySlug, connectorDescriptorByType),
            ...validateEdgeTopology(nodes, edges),
            ...validateBindings(nodes, edges, bindingsState),
            ...validateBindingCompleteness(nodes, edges, bindingsState),
            ...validateBoundaryContractDeclarations(nodes, bindingsState),
        ];

        const positionNodeDisclosure = (item) => {
            if (!(nodeDisclosurePopover instanceof HTMLElement)) {
                return;
            }

            const rootRect = rootElement.getBoundingClientRect();
            const nodeRect = item.getBoundingClientRect();
            const popoverRect = nodeDisclosurePopover.getBoundingClientRect();
            const gap = 12;
            const min = 12;
            let left = nodeRect.right - rootRect.left + gap;
            if (left + popoverRect.width > rootRect.width - min) {
                left = nodeRect.left - rootRect.left - popoverRect.width - gap;
            }
            left = Math.max(min, Math.min(left, Math.max(min, rootRect.width - popoverRect.width - min)));

            let top = nodeRect.top - rootRect.top;
            top = Math.max(min, Math.min(top, Math.max(min, rootRect.height - popoverRect.height - min)));
            nodeDisclosurePopover.style.left = `${Math.round(left)}px`;
            nodeDisclosurePopover.style.top = `${Math.round(top)}px`;
        };

        const showNodeDisclosure = (item) => {
            const attachment = item?.querySelector(".builder-mapping-attachment");
            if (attachment && (attachment.matches(":hover") || attachment.contains(document.activeElement))) return;
            if (!(item instanceof HTMLElement) || !(nodeDisclosurePopover instanceof HTMLElement)) {
                return;
            }
            if (disclosureHoverTimer !== null) {
                window.clearTimeout(disclosureHoverTimer);
                disclosureHoverTimer = null;
            }
            if (disclosureNode instanceof HTMLElement && disclosureNode !== item) {
                disclosureNode.removeAttribute("aria-describedby");
            }
            const node = readNodeDataFromElement(item);
            if (!node.slug) {
                return;
            }
            const nodes = readNodes(gridElement);
            const edges = parseEdgeData(edgesDataElement);
            const issues = currentDisclosureIssues(nodes, edges);
            const context = normalizeDisclosureContext(node, nodes, edges, bindingsState, issues, dataSourceMetaBySlug);

            nodeDisclosurePopover.innerHTML = renderNodeDisclosureContent(node, context);
            nodeDisclosurePopover.classList.remove("hidden");
            item.setAttribute("aria-describedby", nodeDisclosurePopover.id);
            disclosureNode = item;
            positionNodeDisclosure(item);
        };

        const scheduleNodeDisclosure = (item) => {
            if (disclosureHoverTimer !== null) {
                window.clearTimeout(disclosureHoverTimer);
            }
            disclosureHoverTimer = window.setTimeout(() => {
                disclosureHoverTimer = null;
                showNodeDisclosure(item);
            }, 180);
        };

        const handleNodeDisclosureScroll = () => {
            if (
                disclosureNode instanceof HTMLElement &&
                nodeDisclosurePopover instanceof HTMLElement &&
                !nodeDisclosurePopover.classList.contains("hidden")
            ) {
                positionNodeDisclosure(disclosureNode);
                return;
            }
            hideNodeDisclosure();
        };

        const bindNodeSelection = (item) => {
            if (!(item instanceof HTMLElement)) {
                return;
            }
            if (item.dataset.bound === "true") {
                return;
            }
            item.dataset.bound = "true";

            item.addEventListener("click", (event) => {
                const target = event.target;
                if (target instanceof HTMLElement && target.closest("[data-port]")) {
                    return;
                }
                event.preventDefault();
                event.stopPropagation();

                hideNodeDisclosure();

                const sourceBeforeClick = selectedItem();
                if (event.shiftKey && sourceBeforeClick instanceof HTMLElement && sourceBeforeClick !== item) {
                    connectNodeItems(sourceBeforeClick, item);
                    return;
                }

                selectNodeItem(item);
            });

            item.addEventListener("mouseenter", () => scheduleNodeDisclosure(item));
            item.addEventListener("pointerenter", () => scheduleNodeDisclosure(item));
            item.addEventListener("mousemove", () => {
                if (
                    nodeDisclosurePopover instanceof HTMLElement &&
                    nodeDisclosurePopover.classList.contains("hidden")
                ) {
                    scheduleNodeDisclosure(item);
                    return;
                }
                if (disclosureNode === item) {
                    positionNodeDisclosure(item);
                }
            });
            item.addEventListener("mouseleave", () => hideNodeDisclosure(item));
            item.addEventListener("pointerleave", () => hideNodeDisclosure(item));
            item.addEventListener("focus", () => showNodeDisclosure(item));
            item.addEventListener("focusin", () => showNodeDisclosure(item));
            item.addEventListener("blur", () => hideNodeDisclosure(item));
            item.addEventListener("focusout", () => hideNodeDisclosure(item));
            item.addEventListener("keydown", (event) => {
                if (event.key === "Escape") {
                    hideNodeDisclosure();
                    return;
                }
                if (event.key !== "Enter" && event.key !== " ") {
                    return;
                }
                event.preventDefault();
                hideNodeDisclosure();

                const selectedNodeSlug = item.getAttribute(ATTR_NODE_SLUG) || null;
                if (!selectedNodeSlug) {
                    return;
                }
                selectNodeItem(item);
            });

            item.addEventListener("dblclick", (event) => {
                const target = event.target;
                if (target instanceof HTMLElement && target.closest("[data-port]")) {
                    return;
                }
                event.preventDefault();
                event.stopPropagation();

                const clickedSlug = item.getAttribute(ATTR_NODE_SLUG) || null;
                if (!clickedSlug) {
                    return;
                }

                selectNodeItem(item, { openProperties: true });
            });
        };

        stageElement.addEventListener("scroll", handleNodeDisclosureScroll, { passive: true });
        window.addEventListener("resize", hideNodeDisclosure);

        if (savedState && Array.isArray(savedState.nodes) && savedState.nodes.length > 0) {
            Array.from(gridElement.querySelectorAll(".builder-node")).forEach((item) => {
                if (item instanceof HTMLElement) {
                    grid.removeWidget(item);
                }
            });
            restoreNodeLayout(grid, gridElement, savedState);
        }

        Array.from(gridElement.querySelectorAll(".builder-node[data-node-slug]")).forEach((item) => {
            if (item instanceof HTMLElement) {
                applyNodeDataToElement(item, readNodeDataFromElement(item));
            }
            bindNodeSelection(item);
        });

        const addPorts = (item) => {
            syncPortsForNode(item, item instanceof HTMLElement ? item.getAttribute(ATTR_KIND) || "" : "");
        };

        const captureCanvasState = () => {
            return {
                nodes: readNodes(gridElement).map((node) => ({
                    ...node,
                    parameters: node.parameters || {},
                })),
                edges: parseEdgeData(edgesDataElement),
                bindings: normalizeBindings(bindingsState),
            };
        };

        const snapshotKey = (snapshot) => {
            try {
                return JSON.stringify(snapshot);
            } catch (_) {
                return "";
            }
        };

        const pushHistorySnapshot = () => {
            if (applyingHistory) {
                return;
            }

            const snapshot = captureCanvasState();
            const key = snapshotKey(snapshot);
            if (key === "") {
                return;
            }

            if (historyIndex >= 0) {
                const currentKey = snapshotKey(historyStates[historyIndex]);
                if (currentKey === key) {
                    return;
                }
            }

            historyStates.splice(historyIndex + 1);
            historyStates.push(snapshot);
            if (historyStates.length > historyLimit) {
                historyStates.shift();
            }
            historyIndex = historyStates.length - 1;
        };

        const applyHistorySnapshot = (snapshot, message) => {
            if (!snapshot || !Array.isArray(snapshot.nodes) || !Array.isArray(snapshot.edges)) {
                return false;
            }

            applyingHistory = true;
            selectedSlug = null;
            selectedEdge = null;

            Array.from(gridElement.querySelectorAll(".builder-node")).forEach((item) => {
                if (item instanceof HTMLElement) {
                    grid.removeWidget(item);
                }
            });

            snapshot.nodes.forEach((node) => {
                const item = grid.addWidget({
                    x: Number.isInteger(node.x) ? node.x : 0,
                    y: Number.isInteger(node.y) ? node.y : 0,
                    w: nodeWidth,
                    h: nodeHeight,
                    content:
                        '<div class="builder-node-content rounded-lg border border-primary/30 bg-base-100 px-3 py-2 shadow-sm"></div>',
                });
                if (!(item instanceof HTMLElement)) {
                    return;
                }

                item.setAttribute(ATTR_KIND, node.kind || KIND_QUERY);
                item.setAttribute(ATTR_NODE_SLUG, node.slug || `node-${Date.now().toString(36)}`);
                const content = item.querySelector(".builder-node-content");
                if (content instanceof HTMLElement) {
                    content.className = nodeContentClassName;
                }

                addPorts(item);
                applyNodeDataToElement(item, node);
                bindNodeSelection(item);
            });

            writeEdgesData(edgesDataElement, snapshot.edges);
            bindingsState = normalizeBindings(snapshot.bindings);
            showSelection(null);

            scheduleRender();
            saveState(storageKey, gridElement, edgesDataElement, bindingsState, {
                sourceFormat: pipelineSourceFormat,
                exportFormat: pipelineExportFormat,
            });
            refreshValidation();
            setStatusMessage(message, TONE_NEUTRAL);
            applyingHistory = false;
            return true;
        };

        const undoCanvas = () => {
            if (historyIndex <= 0) {
                return false;
            }
            historyIndex -= 1;
            return applyHistorySnapshot(historyStates[historyIndex], "Undo applied.");
        };

        const redoCanvas = () => {
            if (historyIndex < 0 || historyIndex >= historyStates.length - 1) {
                return false;
            }
            historyIndex += 1;
            return applyHistorySnapshot(historyStates[historyIndex], "Redo applied.");
        };

        gridElement.addEventListener(
            "mousedown",
            (event) => {
                const target = event.target;
                if (!(target instanceof HTMLElement)) {
                    return;
                }
                const portEl = target.closest("[data-port]");
                if (!(portEl instanceof HTMLElement)) {
                    return;
                }

                const nodeItem = portEl.closest(".builder-node");
                if (!(nodeItem instanceof HTMLElement)) {
                    return;
                }

                const slug = nodeItem.getAttribute(ATTR_NODE_SLUG) || "";
                const portType = portEl.getAttribute(ATTR_PORT) || "";
                if (!slug || (portType !== PORT_DIR_IN && portType !== PORT_DIR_OUT)) {
                    return;
                }

                event.preventDefault();
                event.stopPropagation();

                hideNodeDisclosure();

                if (portType === PORT_DIR_IN) {
                    const sourceItem = selectedItem();
                    if (sourceItem instanceof HTMLElement && sourceItem !== nodeItem) {
                        connectNodeItems(sourceItem, nodeItem);
                        return;
                    }
                }

                selectNodeItem(nodeItem, { renderSelection: portType !== PORT_DIR_OUT });
                if (portType === PORT_DIR_OUT) {
                    beginConnectionDrag(nodeItem, event);
                    setStatusMessage(
                        "Connection source selected. Drag to a downstream input handle, click an input handle, or Shift+click a downstream node.",
                        TONE_NEUTRAL,
                    );
                } else {
                    setStatusMessage("Select a producer first, then click this input handle.", TONE_NEUTRAL);
                }
            },
            true,
        );

        // Mirror the Go layout constants (builder_layout.go): firstColX=2, levelGapX=7.
        // Query → level 0 (X=2), Transform → level 1 (X=9), Sink → level 2 (X=16).
        const defaultXForKind = (kind) => {
            if (kind === KIND_QUERY || kind === KIND_INPUT) {
                return 2;
            }
            if (kind === KIND_TRANSFORM) {
                return 9;
            }
            // KIND_SINK and anything else
            return 16;
        };

        // Returns the Y for a new node of the given kind. Fills gaps in the column sequence
        // before appending to the end. Mirror Go's firstRowY=1, rowGapY=3.
        const nextNodeYForKind = (kind) => {
            const firstRowY = 1;
            const rowGapY = 3;
            const expectedX = defaultXForKind(kind);

            const nodes = readNodes(gridElement);
            // Reserve row slots by target column (X), not only by kind. This avoids overlaps
            // when legacy/imported nodes have stale or missing kind metadata.
            const sameColumn = nodes.filter((n) => n.x === expectedX);

            if (sameColumn.length === 0) {
                return firstRowY;
            }

            // Normalize existing Y values to logical row slots so legacy/non-canonical Y values
            // still reserve the intended slot and cannot be overwritten.
            const occupiedRows = new Set(
                sameColumn
                    .map((n) => {
                        const y = Number.isFinite(n.y) ? n.y : firstRowY;
                        return Math.max(0, Math.round((y - firstRowY) / rowGapY));
                    })
                    .filter((row) => Number.isInteger(row)),
            );

            // Find the first available row slot (0, 1, 2, ...) then convert back to Y.
            for (let row = 0; row <= 1000; row++) {
                if (!occupiedRows.has(row)) {
                    return firstRowY + row * rowGapY;
                }
            }

            // Fallback: append after the highest known slot.
            const maxRow = Math.max(...occupiedRows);
            return firstRowY + (maxRow + 1) * rowGapY;
        };

        const newNodeData = (kind, nodeType, slug) => {
            if (isStepKind(kind)) {
                const data = {
                    kind,
                    nodeType,
                    slug,
                    dataSourceSlug: "",
                    operationType: "sql",
                    operationInline: {},
                    queryOutputContract: {},
                    queryIncremental: {},
                    querySlug: "",
                    useInlineSQL: false,
                    useInlineOperation: false,
                    inlineSQL: "",
                    stepSlug: slug,
                    stepName: "",
                    originalStep: {},
                    parameters: {},
                    sinkBackend: "",
                    sinkDataSourceSlug: "",
                    sinkOperationType: "",
                    sinkOperationInline: {},
                    sinkIdempotencyClass: "",
                    sinkInputContract: {},
                    sinkUseInlineOperation: false,
                    sinkQuerySlug: "",
                };

                if (kind === KIND_TRANSFORM) {
                    data.transformOutputKind = "rows";
                    data.transformSQL = "SELECT * FROM input_rows";
                    data.transformInputContract = {};
                    data.transformOutputContract = {};
                }

                return data;
            }

            return {
                kind,
                nodeType,
                slug,
                dataSourceSlug: "",
                operationType: "sql",
                operationInline: {},
                queryOutputContract: {},
                queryIncremental: {},
                querySlug: "",
                useInlineSQL: false,
                useInlineOperation: false,
                inlineSQL: "",
                stepSlug: "",
                stepName: "",
                originalStep: {},
                parameters: {},
                sinkBackend: kind === KIND_SINK ? "unknown" : "",
                sinkDataSourceSlug: "",
                sinkOperationType: "",
                sinkOperationInline: {},
                sinkIdempotencyClass: kind === KIND_SINK ? DEFAULT_SINK_IDEMPOTENCY_CLASS : "",
                sinkInputContract: {},
                sinkUseInlineOperation: false,
                sinkQuerySlug: "",
            };
        };

        const createNodeFromPalette = (kind, nodeType, x, y) => {
            const slug = `${kind}-${Date.now().toString(36).slice(-6)}`;
            const uniqueSlug = uniqueSlugFrom(slug);
            const item = grid.addWidget({
                x,
                y,
                w: nodeWidth,
                h: nodeHeight,
                content:
                    '<div class="builder-node-content rounded-lg border border-primary/30 bg-base-100 px-3 py-2 shadow-sm"></div>',
            });

            if (!(item instanceof HTMLElement)) {
                return;
            }

            item.setAttribute(ATTR_KIND, kind);
            item.setAttribute(ATTR_NODE_SLUG, uniqueSlug);
            const content = item.querySelector(".builder-node-content");
            if (content instanceof HTMLElement) {
                content.className = nodeContentClassName;
            }

            addPorts(item);
            applyNodeDataToElement(item, newNodeData(kind, nodeType, uniqueSlug));
            bindNodeSelection(item);
            selectedSlug = uniqueSlug;
            selectedEdge = null;
            showSelection(item);
            scheduleRender();
            saveAndValidate();
        };

        const createNodeFromData = (nodeData, x, y) => {
            const uniqueSlug = uniqueSlugFrom(nodeData.slug || `${nodeData.kind}-node`);
            const item = grid.addWidget({
                x,
                y,
                w: nodeWidth,
                h: nodeHeight,
                content:
                    '<div class="builder-node-content rounded-lg border border-primary/30 bg-base-100 px-3 py-2 shadow-sm"></div>',
            });

            if (!(item instanceof HTMLElement)) {
                return;
            }

            const payload = {
                kind: nodeData.kind || KIND_QUERY,
                nodeType: nodeData.nodeType || DEFAULT_NODE_TYPE,
                slug: uniqueSlug,
                dataSourceSlug: nodeData.dataSourceSlug || "",
                operationType: nodeData.operationType || "sql",
                operationInline:
                    nodeData.operationInline &&
                    typeof nodeData.operationInline === "object" &&
                    !Array.isArray(nodeData.operationInline)
                        ? nodeData.operationInline
                        : {},
                queryOutputContract:
                    nodeData.queryOutputContract &&
                    typeof nodeData.queryOutputContract === "object" &&
                    !Array.isArray(nodeData.queryOutputContract)
                        ? nodeData.queryOutputContract
                        : {},
                queryIncremental:
                    nodeData.queryIncremental &&
                    typeof nodeData.queryIncremental === "object" &&
                    !Array.isArray(nodeData.queryIncremental)
                        ? nodeData.queryIncremental
                        : {},
                querySlug: nodeData.querySlug || "",
                useInlineSQL: Boolean(nodeData.useInlineSQL),
                useInlineOperation: Boolean(nodeData.useInlineOperation),
                inlineSQL: nodeData.inlineSQL || "",
                stepSlug: nodeData.stepSlug || uniqueSlug,
                stepName: nodeData.stepName || "",
                originalStep:
                    nodeData.originalStep &&
                    typeof nodeData.originalStep === "object" &&
                    !Array.isArray(nodeData.originalStep)
                        ? nodeData.originalStep
                        : {},
                parameters: nodeData.parameters || {},
                sinkBackend: nodeData.sinkBackend || (nodeData.kind === KIND_SINK ? "unknown" : ""),
                sinkDataSourceSlug: nodeData.sinkDataSourceSlug || "",
                sinkOperationType: nodeData.sinkOperationType || "",
                sinkOperationInline:
                    nodeData.sinkOperationInline &&
                    typeof nodeData.sinkOperationInline === "object" &&
                    !Array.isArray(nodeData.sinkOperationInline)
                        ? nodeData.sinkOperationInline
                        : {},
                sinkIdempotencyClass:
                    nodeData.kind === KIND_SINK ? normalizeSinkIdempotencyClass(nodeData.sinkIdempotencyClass) : "",
                sinkInputContract:
                    nodeData.sinkInputContract &&
                    typeof nodeData.sinkInputContract === "object" &&
                    !Array.isArray(nodeData.sinkInputContract)
                        ? nodeData.sinkInputContract
                        : {},
                sinkUseInlineOperation: Boolean(nodeData.sinkUseInlineOperation),
                sinkQuerySlug: nodeData.sinkQuerySlug || "",
            };

            if (payload.kind === KIND_TRANSFORM) {
                payload.transformOutputKind =
                    typeof nodeData.transformOutputKind === "string"
                        ? nodeData.transformOutputKind
                        : typeof nodeData.transform_output_kind === "string"
                          ? nodeData.transform_output_kind
                          : "rows";
                payload.transformSQL =
                    typeof nodeData.transformSQL === "string"
                        ? nodeData.transformSQL
                        : typeof nodeData.transform_sql === "string"
                          ? nodeData.transform_sql
                          : "SELECT * FROM input_rows";
                payload.transformInputContract =
                    nodeData.transformInputContract &&
                    typeof nodeData.transformInputContract === "object" &&
                    !Array.isArray(nodeData.transformInputContract)
                        ? nodeData.transformInputContract
                        : {};
                payload.transformOutputContract =
                    nodeData.transformOutputContract &&
                    typeof nodeData.transformOutputContract === "object" &&
                    !Array.isArray(nodeData.transformOutputContract)
                        ? nodeData.transformOutputContract
                        : {};
            }

            item.setAttribute(ATTR_KIND, payload.kind);
            item.setAttribute(ATTR_NODE_SLUG, payload.slug);
            const content = item.querySelector(".builder-node-content");
            if (content instanceof HTMLElement) {
                content.className = nodeContentClassName;
            }

            addPorts(item);
            applyNodeDataToElement(item, payload);
            bindNodeSelection(item);
            selectedSlug = payload.slug;
            selectedEdge = null;
            showSelection(item);
            scheduleRender();
            saveAndValidate();
        };

        const addNodeButtons = document.querySelectorAll("[data-add-node-kind]");
        addNodeButtons.forEach((item) => {
            if (!(item instanceof HTMLElement)) {
                return;
            }

            item.addEventListener("click", () => {
                const kind = (item.getAttribute("data-add-node-kind") || KIND_QUERY).trim();
                const nodeType = (item.getAttribute("data-add-node-type") || DEFAULT_NODE_TYPE).trim();
                createNodeFromPalette(kind, nodeType, defaultXForKind(kind), nextNodeYForKind(kind));
            });
        });

        if (paletteSearch instanceof HTMLInputElement) {
            paletteSearch.addEventListener("input", () => {
                const searchValue = paletteSearch.value.trim().toLowerCase();
                document.querySelectorAll('[data-palette-item="true"]').forEach((item) => {
                    if (!(item instanceof HTMLElement)) {
                        return;
                    }
                    const text = item.textContent ? item.textContent.toLowerCase() : "";
                    const visible = searchValue === "" || text.includes(searchValue);
                    item.classList.toggle("hidden", !visible);
                });
            });
        }

        stageElement.addEventListener(
            "wheel",
            (event) => {
                if (!event.ctrlKey && !event.metaKey) {
                    return;
                }
                event.preventDefault();
                const direction = event.deltaY > 0 ? -1 : 1;
                const magnitude = Math.max(0.012, Math.min(0.14, Math.abs(event.deltaY) * wheelZoomSpeed));
                adjustCanvasScale(direction * magnitude, event.clientX, event.clientY);
            },
            { passive: false },
        );

        const selectedItem = () => {
            if (!selectedSlug) {
                return null;
            }
            const item = gridElement.querySelector(`.builder-node[data-node-slug="${selectedSlug}"]`);
            return item instanceof HTMLElement ? item : null;
        };

        const updateSelectedNode = (preserveContractEditor = null) => {
            const item = selectedItem();
            if (!(item instanceof HTMLElement)) {
                return;
            }

            resetFieldErrors();
            const node = readNodeDataFromElement(item);
            const previousSlug = node.slug;
            let hasFieldError = false;

            node.slug = previousSlug;

            if (node.kind === KIND_QUERY) {
                const dataSourceField = getDataSourceField();
                const queryField = getQueryField();
                const fieldInlineSQLToggle = getInlineSQLToggleField();
                const fieldInlineSQL = getInlineSQLField();
                const fieldInlineOperation = getInlineOperationField();
                const fieldInlineOperationToggle = getInlineOperationToggleField();
                if (dataSourceField instanceof HTMLInputElement || dataSourceField instanceof HTMLSelectElement) {
                    const nextRef = dataSourceField.value.trim();
                    if (nextRef !== node.dataSourceSlug) {
                        const source = dataSourceMetaBySlug.get(nextRef);
                        if (source) node.operationType = queryOperationTypeFromDataSourceType(source.type);
                    }
                    node.dataSourceSlug = nextRef;
                }
                if (queryField instanceof HTMLInputElement || queryField instanceof HTMLSelectElement) {
                    node.querySlug = queryField.value.trim();
                }
                if (fieldInlineSQLToggle instanceof HTMLInputElement) {
                    node.useInlineSQL = fieldInlineSQLToggle.checked;
                }
                if (fieldInlineOperationToggle instanceof HTMLInputElement) {
                    node.useInlineOperation = fieldInlineOperationToggle.checked;
                }
                if (fieldInlineSQL instanceof HTMLTextAreaElement) {
                    node.inlineSQL = fieldInlineSQL.value;
                }

                applyQueryDataSourceContext(node.dataSourceSlug, node);
                renderSourceGuidedInlineEditor(node, updateSelectedNode);

                const isSQLOperation = (node.operationType || "sql") === "sql";
                const rawInlineOperation =
                    fieldInlineOperation instanceof HTMLTextAreaElement ? fieldInlineOperation.value.trim() : "";

                // Inline mode takes precedence over a saved query.
                if (isSQLOperation && node.useInlineSQL) {
                    node.querySlug = "";
                    if (queryField instanceof HTMLInputElement || queryField instanceof HTMLSelectElement) {
                        queryField.value = "";
                    }
                    if ((node.inlineSQL || "").trim() !== "") {
                        node.operationInline = { sql: node.inlineSQL };
                    } else {
                        node.operationInline = {};
                    }
                } else if (
                    !isSQLOperation &&
                    node.useInlineOperation &&
                    fieldInlineOperation instanceof HTMLTextAreaElement
                ) {
                    node.querySlug = "";
                    if (queryField instanceof HTMLInputElement || queryField instanceof HTMLSelectElement) {
                        queryField.value = "";
                    }
                    if (rawInlineOperation === "") {
                        node.operationInline = {};
                    } else {
                        try {
                            const parsed = JSON.parse(rawInlineOperation);
                            if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
                                node.operationInline = parsed;
                            } else {
                                throw new Error("not-object");
                            }
                        } catch (_) {
                            hasFieldError = true;
                            setStatusMessage("Inline operation must be a JSON object.", TONE_ERROR);
                        }
                    }
                } else if (node.querySlug && node.querySlug !== "") {
                    node.operationInline = {};
                } else {
                    node.operationInline = {};
                }

                const queryOutputContractResult = readJSONObjectField(
                    fieldQueryOutputContract,
                    errorQueryOutputContract,
                );
                if (queryOutputContractResult.ok) {
                    node.queryOutputContract = queryOutputContractResult.value;
                } else {
                    hasFieldError = true;
                }

                const queryIncrementalResult = readIncrementalEditor();
                if (queryIncrementalResult.ok) {
                    node.queryIncremental = queryIncrementalResult.value;
                } else {
                    hasFieldError = true;
                }

                syncQuerySourceModeUI(isSQLOperation && Boolean(node.useInlineSQL));
                if (!isSQLOperation) {
                    syncOperationSourceModeUI(Boolean(node.useInlineOperation));
                }
            }

            if (isStepKind(node.kind)) {
                if (fieldStepName instanceof HTMLInputElement) {
                    node.stepName = fieldStepName.value.trim();
                }
                const nextSlugBase = node.stepName || previousSlug;
                node.slug = hasObjectEntries(node.originalStep) ? previousSlug : uniqueSlugFrom(nextSlugBase, item);
                node.stepSlug = node.slug;
                if (fieldParameters instanceof HTMLTextAreaElement) {
                    const raw = fieldParameters.value.trim();
                    if (raw === "") {
                        node.parameters = {};
                    } else {
                        try {
                            const parsed = JSON.parse(raw);
                            if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
                                node.parameters = parsed;
                            } else {
                                throw new Error("not-object");
                            }
                        } catch (_) {
                            hasFieldError = true;
                            if (errorParameters instanceof HTMLElement) {
                                errorParameters.textContent = "Must be a JSON object.";
                            }
                        }
                    }
                }
                if (fieldStepSlug instanceof HTMLInputElement) {
                    fieldStepSlug.value = node.stepSlug;
                }
            }

            if (node.kind === KIND_TRANSFORM) {
                if (fieldTransformSQL instanceof HTMLTextAreaElement) {
                    const sql = fieldTransformSQL.value;
                    node.transformSQL = sql;
                    if (sql.trim() === "") {
                        hasFieldError = true;
                        if (errorTransformSQL instanceof HTMLElement) {
                            errorTransformSQL.textContent = "Transform SQL is required.";
                        }
                    }
                }

                const transformInputContractResult = readJSONObjectField(
                    fieldTransformInputContract,
                    errorTransformInputContract,
                );
                if (transformInputContractResult.ok) {
                    node.transformInputContract = transformInputContractResult.value;
                } else {
                    hasFieldError = true;
                }

                const transformOutputContractResult = readJSONObjectField(
                    fieldTransformOutputContract,
                    errorTransformOutputContract,
                );
                if (transformOutputContractResult.ok) {
                    node.transformOutputContract = transformOutputContractResult.value;
                } else {
                    hasFieldError = true;
                }
            }

            if (node.kind === KIND_SINK) {
                const replayPolicyField = document.getElementById("builder-field-sink-replay-policy");
                if (replayPolicyField instanceof HTMLSelectElement && replayPolicyField.value) {
                    node.sinkIdempotencyClass = replayPolicyField.value;
                }
                const sinkDataSourceField = getSinkDataSourceField();
                const sinkQueryField = getSinkQueryField();
                const sinkInlineOperationToggle = getSinkInlineOperationToggleField();
                const sinkInlineOperationField = getSinkInlineOperationField();
                if (
                    sinkDataSourceField instanceof HTMLInputElement ||
                    sinkDataSourceField instanceof HTMLSelectElement
                ) {
                    const selectedDataSourceSlug = sinkDataSourceField.value.trim();
                    const refChanged = node.sinkDataSourceSlug !== selectedDataSourceSlug;
                    node.sinkDataSourceSlug = selectedDataSourceSlug;
                    const selectedDataSource = dataSourceMetaBySlug.get(selectedDataSourceSlug);
                    if (selectedDataSource && (refChanged || !node.sinkBackend)) {
                        if (refChanged)
                            node.sinkOperationType = queryOperationTypeFromDataSourceType(selectedDataSource.type);
                        node.sinkBackend =
                            selectedDataSource.sinkBackend || sinkBackendFromDataSourceType(selectedDataSource.type);
                    } else if (selectedDataSourceSlug === "") {
                        node.sinkBackend = "";
                    }
                    applySinkDataSourceContext(selectedDataSourceSlug, node);
                }
                renderSinkGuidedInlineEditor(node, updateSelectedNode);
                if (sinkQueryField instanceof HTMLInputElement || sinkQueryField instanceof HTMLSelectElement) {
                    node.sinkQuerySlug = sinkQueryField.value.trim();
                }
                if (sinkInlineOperationToggle instanceof HTMLInputElement) {
                    node.sinkUseInlineOperation = sinkInlineOperationToggle.checked;
                }

                const rawSinkInlineOperation =
                    sinkInlineOperationField instanceof HTMLTextAreaElement
                        ? sinkInlineOperationField.value.trim()
                        : "";
                if (node.sinkUseInlineOperation) {
                    node.sinkQuerySlug = "";
                    if (sinkQueryField instanceof HTMLInputElement || sinkQueryField instanceof HTMLSelectElement) {
                        sinkQueryField.value = "";
                    }
                    if (rawSinkInlineOperation === "") {
                        node.sinkOperationInline = {};
                    } else {
                        try {
                            const parsed = JSON.parse(rawSinkInlineOperation);
                            if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
                                node.sinkOperationInline = parsed;
                            } else {
                                throw new Error("not-object");
                            }
                        } catch (_) {
                            hasFieldError = true;
                            setStatusMessage("Inline operation must be a JSON object.", TONE_ERROR);
                        }
                    }
                } else if (node.sinkQuerySlug && node.sinkQuerySlug !== "") {
                    node.sinkOperationInline = {};
                } else {
                    node.sinkOperationInline = {};
                }

                if (node.sinkBackend === "s3" && document.getElementById("builder-s3-output")) {
                    node.originalStep ||= {};
                    node.originalStep.sink ||= {};
                    const config = { ...node.originalStep.sink.config };
                    for (const key of ["key", "format"]) {
                        const value = document.getElementById(`builder-s3-${key}`)?.value;
                        if (value) config[key] = value;
                        else delete config[key];
                    }
                    node.originalStep.sink.config = config;
                    if (node.sinkUseInlineOperation && !hasObjectEntries(node.sinkOperationInline)) {
                        node.sinkOperationInline = buildGuidedInlineOperation(
                            getSinkInlineOperationGuided(),
                            "s3",
                            "sink",
                        );
                    }
                }
                const sinkInputContractResult = readJSONObjectField(fieldSinkInputContract, errorSinkInputContract);
                if (sinkInputContractResult.ok) {
                    node.sinkInputContract = sinkInputContractResult.value;
                } else {
                    hasFieldError = true;
                }

                syncSinkOperationModeUI(Boolean(node.sinkUseInlineOperation));
            }

            if (node.slug !== previousSlug) {
                bindingsState = replaceBindingSlug(bindingsState, previousSlug, node.slug);
                writeEdgesData(
                    edgesDataElement,
                    replaceEdgeSlug(parseEdgeData(edgesDataElement), previousSlug, node.slug),
                );
            }
            applyNodeDataToElement(item, node);
            selectedSlug = node.slug;
            if (!hasFieldError) {
                showSelection(item, preserveContractEditor);
            }
            scheduleRender();
            saveAndValidate();
        };

        [
            getDataSourceField(),
            getQueryField(),
            fieldStepName,
            fieldParameters,
            fieldQueryOutputContract,
            fieldQueryIncrementalKind,
            fieldQueryIncrementalField,
            fieldQueryIncrementalCursor,
            fieldQueryIncrementalLookback,
            fieldQueryIncrementalOrdering,
            fieldQueryIncrementalDedupe,
            fieldTransformSQL,
            fieldTransformInputContract,
            fieldTransformOutputContract,
            fieldSinkBackend,
            getSinkQueryField(),
            fieldSinkInputContract,
            getInlineSQLField(),
            getInlineOperationField(),
            getSinkInlineOperationField(),
            document.getElementById("builder-s3-key"),
            document.getElementById("builder-s3-format"),
            document.getElementById("builder-field-sink-replay-policy"),
        ].forEach((field) => {
            if (!(field instanceof HTMLElement)) {
                return;
            }
            field.addEventListener("input", updateSelectedNode);
            field.addEventListener("change", updateSelectedNode);
        });

        if (parametersGroup instanceof HTMLElement) {
            parametersGroup.addEventListener("change", (event) => {
                const target = event.target;
                if (!(target instanceof HTMLElement) || !target.closest("[data-parameter-row]")) {
                    return;
                }
                if (syncParameterEditorToField()) {
                    updateSelectedNode();
                }
            });

            parametersGroup.addEventListener("click", (event) => {
                const target = event.target;
                if (!(target instanceof HTMLElement)) {
                    return;
                }
                if (target.id === "builder-add-parameter-btn") {
                    if (parameterRows instanceof HTMLElement) {
                        const empty = parameterRows.querySelector("p");
                        if (empty instanceof HTMLElement) {
                            empty.remove();
                        }
                        const row = createParameterRow();
                        parameterRows.appendChild(row);
                        const keyInput = row.querySelector("[data-parameter-key]");
                        if (keyInput instanceof HTMLInputElement) {
                            keyInput.focus();
                        }
                    }
                    return;
                }
                const removeButton = target.closest("[data-remove-parameter-row]");
                if (!(removeButton instanceof HTMLButtonElement)) {
                    return;
                }
                removeButton.closest("[data-parameter-row]")?.remove();
                if (parameterRows instanceof HTMLElement && !parameterRows.querySelector("[data-parameter-row]")) {
                    renderParameterEditor({});
                }
                if (syncParameterEditorToField()) {
                    updateSelectedNode();
                }
            });
        }

        const contractEditorDefinitions = () => [
            {
                editor: queryOutputContractEditor,
                field: fieldQueryOutputContract,
                error: errorQueryOutputContract,
            },
            {
                editor: transformInputContractEditor,
                field: fieldTransformInputContract,
                error: errorTransformInputContract,
            },
            {
                editor: transformOutputContractEditor,
                field: fieldTransformOutputContract,
                error: errorTransformOutputContract,
            },
            {
                editor: sinkInputContractEditor,
                field: fieldSinkInputContract,
                error: errorSinkInputContract,
            },
        ];
        const contractDefinitionForTarget = (target) =>
            contractEditorDefinitions().find(
                (definition) => definition.editor instanceof HTMLElement && definition.editor.contains(target),
            ) || null;
        const syncContractDefinition = (definition) => {
            if (!definition) {
                return false;
            }
            return syncContractEditorToField(definition.editor, definition.field, definition.error);
        };

        contractEditorDefinitions().forEach((definition) => {
            const editor = definition.editor;
            if (!(editor instanceof HTMLElement)) {
                return;
            }
            const syncEditor = (event) => {
                const target = event.target;
                if (!(target instanceof HTMLElement)) {
                    return;
                }
                const activeDefinition = contractDefinitionForTarget(target);
                if (syncContractDefinition(activeDefinition)) {
                    updateSelectedNode(editor);
                }
            };
            editor.addEventListener("input", syncEditor);
            editor.addEventListener("change", syncEditor);
            editor.addEventListener("click", (event) => {
                const target = event.target;
                if (!(target instanceof HTMLElement)) {
                    return;
                }
                const activeDefinition = contractDefinitionForTarget(target);
                const addButton = target.closest("[data-contract-add-field]");
                if (addButton instanceof HTMLButtonElement) {
                    const rows = editor.querySelector("[data-contract-field-rows]");
                    if (rows instanceof HTMLElement) {
                        const empty = rows.querySelector("p");
                        if (empty instanceof HTMLElement) {
                            empty.remove();
                        }
                        const row = createContractFieldRow();
                        rows.appendChild(row);
                        const nameInput = row.querySelector("[data-contract-field-name]");
                        if (nameInput instanceof HTMLInputElement) {
                            nameInput.focus();
                        }
                    }
                    return;
                }
                const removeButton = target.closest("[data-contract-remove-field]");
                if (!(removeButton instanceof HTMLButtonElement)) {
                    return;
                }
                removeButton.closest("[data-contract-field-row]")?.remove();
                const rows = editor.querySelector("[data-contract-field-rows]");
                if (rows instanceof HTMLElement && !rows.querySelector("[data-contract-field-row]")) {
                    const empty = document.createElement("p");
                    empty.className = "text-xs text-base-content/60";
                    empty.textContent = "No fields declared.";
                    rows.appendChild(empty);
                }
                if (syncContractDefinition(activeDefinition)) {
                    updateSelectedNode(editor);
                }
            });
        });

        const fieldInlineSQLToggle = getInlineSQLToggleField();
        if (fieldInlineSQLToggle instanceof HTMLInputElement) {
            fieldInlineSQLToggle.addEventListener("change", updateSelectedNode);
        }
        const fieldInlineOperationToggle = getInlineOperationToggleField();
        if (fieldInlineOperationToggle instanceof HTMLInputElement) {
            fieldInlineOperationToggle.addEventListener("change", updateSelectedNode);
        }
        const fieldSinkInlineOperationToggle = getSinkInlineOperationToggleField();
        if (fieldSinkInlineOperationToggle instanceof HTMLInputElement) {
            fieldSinkInlineOperationToggle.addEventListener("change", updateSelectedNode);
        }

        if (bindingSection instanceof HTMLElement) {
            bindingSection.addEventListener("change", (event) => {
                const target = event.target;
                if (!(target instanceof HTMLElement)) {
                    return;
                }
                if (!(target instanceof HTMLSelectElement) && !(target instanceof HTMLInputElement)) {
                    return;
                }
                if (!target.closest("[data-binding-row]")) {
                    return;
                }
                if (
                    !target.hasAttribute(ATTR_BINDING_SOURCE_NODE) &&
                    !target.hasAttribute(ATTR_BINDING_SOURCE_PORT) &&
                    !target.hasAttribute("data-binding-target-port")
                ) {
                    return;
                }

                syncBindingsFromEditor();
            });

            bindingSection.addEventListener("click", (event) => {
                const target = event.target;
                if (!(target instanceof HTMLElement)) {
                    return;
                }
                const clearButton = target.closest("[data-binding-clear-row]");
                if (!(clearButton instanceof HTMLButtonElement)) {
                    return;
                }

                const row = clearButton.closest("[data-binding-row]");
                if (!(row instanceof HTMLElement)) {
                    return;
                }

                const sourceNodeSelect = row.querySelector("[data-binding-source-node]");
                const sourcePortSelect = row.querySelector("[data-binding-source-port]");
                if (sourceNodeSelect instanceof HTMLSelectElement) {
                    sourceNodeSelect.value = "";
                }
                if (sourcePortSelect instanceof HTMLSelectElement) {
                    sourcePortSelect.value = "";
                }

                syncBindingsFromEditor();
            });
        }

        if (connectionSection instanceof HTMLElement) {
            connectionSection.addEventListener("click", (event) => {
                const target = event.target;
                if (!(target instanceof HTMLElement)) {
                    return;
                }

                const selectedNodeItem = selectedItem();
                if (!(selectedNodeItem instanceof HTMLElement)) {
                    return;
                }
                const node = readNodeDataFromElement(selectedNodeItem);

                if (target.id === "builder-connection-outgoing-add-btn") {
                    const outgoingSelect = connectionSection.querySelector("#builder-connection-outgoing-select");
                    if (!(outgoingSelect instanceof HTMLSelectElement)) {
                        return;
                    }
                    const targetSlug = outgoingSelect.value.trim();
                    if (targetSlug === "") {
                        return;
                    }

                    const nodes = readNodes(gridElement);
                    const targetNode = nodes.find((candidate) => candidate.slug === targetSlug) || null;
                    const result = ensureCanonicalConnection(node, targetNode);
                    if (!result.ok) {
                        setConnectionError(result.error);
                        setStatusMessage(result.error, TONE_ERROR);
                        renderConnectionsEditor(node);
                        return;
                    }

                    setConnectionError("");
                    showSelection(selectedNodeItem);
                    scheduleRender();
                    saveAndValidate();
                    setStatusMessage(`Connected ${node.slug} -> ${targetSlug}.`, TONE_SUCCESS);
                    return;
                }

                const removeButton = target.closest("[data-remove-outgoing-to]");
                if (!(removeButton instanceof HTMLButtonElement)) {
                    return;
                }
                const removeTo = (removeButton.getAttribute("data-remove-outgoing-to") || "").trim();
                if (removeTo === "") {
                    return;
                }

                removeCanonicalConnections([{ from: node.slug, to: removeTo }]);
                setConnectionError("");
                showSelection(selectedNodeItem);
                scheduleRender();
                saveAndValidate();
                setStatusMessage(`Removed ${node.slug} -> ${removeTo}.`, TONE_SUCCESS);
            });
        }

        const inspectEdge = (target) => {
            if (!(target instanceof SVGPathElement)) return;
            const from = target.getAttribute(ATTR_EDGE_FROM) || "";
            const to = target.getAttribute(ATTR_EDGE_TO) || "";
            const sourceItem = gridElement.querySelector(`.builder-node[data-node-slug="${from}"]`);
            const targetItem = gridElement.querySelector(`.builder-node[data-node-slug="${to}"]`);
            if (!(sourceItem instanceof HTMLElement) || !(targetItem instanceof HTMLElement)) return;
            selectedSlug = null;
            selectedEdge = { from, to };
            isPropertiesVisible = true;
            applyMainGridLayout();
            showSelection(null);
            if (propertyEmpty instanceof HTMLElement) propertyEmpty.classList.add("hidden");
            if (edgePreview instanceof HTMLElement) {
                clearChildren(edgePreview);
                edgePreview.classList.remove("hidden");
                const appendText = (tag, text, className = "text-xs text-base-content/75", parent = edgePreview) => {
                    const element = document.createElement(tag);
                    element.className = className;
                    element.textContent = text;
                    parent.appendChild(element);
                    return element;
                };
                appendText("h3", "Connection");
                appendText("p", `${from} → ${to}`, "builder-inspector-identifier font-mono");
                renderEdgeDiagnostics();
                const preview = edgePreviewAgreements(
                    readNodeDataFromElement(sourceItem),
                    readNodeDataFromElement(targetItem),
                    bindingsState,
                );
                appendText(
                    "p",
                    preview.bindings.length
                        ? preview.bindings.map((binding) => `${binding.fromPort} → ${binding.toPort}`).join(", ")
                        : "Dependency only. No payload binding declared.",
                    "builder-inspector-subtitle",
                );
                const revisionPreviews = new Map();
                const loadRevision = (ref) => {
                    const key = `${ref.contract_slug}/${ref.revision_slug}/${ref.schema_object_id}`;
                    if (!revisionPreviews.has(key))
                        revisionPreviews.set(
                            key,
                            builderAPI.loadODCSContractPreview(
                                getActiveOrganizationSlug(),
                                getActiveProjectSlug(),
                                ref,
                            ),
                        );
                    return revisionPreviews.get(key);
                };
                const comparison = appendText("section", "", "builder-inspector-section");
                comparison.setAttribute("aria-label", "Draft contract comparison");
                appendText("h4", "Draft contract comparison", "", comparison);
                appendText(
                    "p",
                    "Top-level declarations only. Differences are not a compatibility verdict; use Validate for nested structure and rules.",
                    "builder-inspector-note",
                    comparison,
                );
                const resolveDeclaration = async (agreement) => {
                    if (!agreement) return null;
                    const { ref, contract } = agreement;
                    const schema = ref ? (await loadRevision(ref)).object : contract;
                    return {
                        schema,
                        odcs: Boolean(ref),
                        representation: ref?.representation || contract?.representation,
                        rootShape: ref?.root_shape || contract?.root_shape,
                        fieldPrefix: agreement.fieldPrefix,
                    };
                };
                const producer = resolveDeclaration(preview.agreements[0]);
                // A single producer promise is shared across all named consumer inputs.
                producer.catch(() => {});
                if (preview.agreements.length < 2)
                    appendText(
                        "p",
                        "No consumer contract is declared for this connection.",
                        "text-xs text-base-content/70",
                        comparison,
                    );
                preview.agreements.slice(1).forEach((agreement) => {
                    appendText("p", agreement.label, "text-xs font-medium mt-3", comparison);
                    const table = appendText(
                        "div",
                        "Loading declarations…",
                        "text-xs text-base-content/70",
                        comparison,
                    );
                    Promise.all([producer, resolveDeclaration(agreement)])
                        .then(([source, destination]) => {
                            if (table.isConnected)
                                builderRendering.renderContractComparison(table, source, destination);
                        })
                        .catch(() => {
                            if (table.isConnected)
                                table.textContent =
                                    "Comparison unavailable. Check access to the exact pinned revisions and reopen this connection.";
                        });
                });
                const runEvidence = appendText("section", "", "builder-inspector-section");
                runEvidence.setAttribute("aria-label", "Recorded run evidence");
                appendText("h4", "Recorded run evidence", "", runEvidence);
                attachRunEvidence(runEvidence, from, to);
                preview.agreements.forEach(({ label, ref, contract, fieldPrefix }) => {
                    const section = appendText("section", "", "builder-inspector-section");
                    section.setAttribute("aria-label", label);
                    appendText("h4", label, "", section);
                    if (fieldPrefix)
                        appendText(
                            "p",
                            "Full combined declaration. The comparison above selects only this input relation’s fields.",
                            "builder-inspector-note",
                            section,
                        );
                    if (ref) {
                        const metadata = appendText("dl", "", "builder-inspector-metadata", section);
                        for (const [name, value] of [
                            ["Contract", ref.contract_slug],
                            ["Revision", ref.revision_slug],
                            ["Object", ref.schema_object_id],
                            ["Representation", ref.representation || "not declared"],
                        ]) {
                            appendText("dt", name, "", metadata);
                            appendText("dd", value, "font-mono", metadata);
                        }
                        const link = appendText(
                            "a",
                            "Inspect pinned ODCS revision",
                            "link link-primary text-xs",
                            section,
                        );
                        const root = document.getElementById("builder-canvas-root");
                        link.href = `/orgs/${encodeURIComponent(root.dataset.orgSlug)}/projects/${encodeURIComponent(root.dataset.projectSlug)}/contracts?${new URLSearchParams({ contract: ref.contract_slug, revision: ref.revision_slug })}`;
                        const schemaPreview = appendText(
                            "div",
                            "Loading pinned contract…",
                            "mt-4 space-y-2 text-xs",
                            section,
                        );
                        schemaPreview.setAttribute("aria-label", `${label} contract preview`);
                        loadRevision(ref)
                            .then(({ rawRevision, object }) => {
                                if (!schemaPreview.isConnected) return;
                                builderRendering.renderEdgeContractPreview(schemaPreview, object, rawRevision, true);
                            })
                            .catch(() => {
                                if (!schemaPreview.isConnected) return;
                                schemaPreview.textContent =
                                    "Contract preview unavailable. Check project access, then reopen this connection to retry.";
                            });
                    } else {
                        appendText("p", contractSummary(contract), "builder-inspector-subtitle", section);
                        if (!contract) return;
                        const schemaPreview = appendText("div", "", "mt-4 space-y-2 text-xs", section);
                        schemaPreview.setAttribute("aria-label", `${label} contract preview`);
                        builderRendering.renderEdgeContractPreview(schemaPreview, contract, contract);
                    }
                });
                const actions = appendText("div", "", "builder-inspector-section flex flex-wrap gap-2");
                for (const [label, item] of [
                    ["Inspect source", sourceItem],
                    ["Inspect destination", targetItem],
                ]) {
                    const button = document.createElement("button");
                    button.type = "button";
                    button.className = "btn btn-outline btn-sm";
                    button.textContent = label;
                    button.addEventListener("click", () => selectNodeItem(item));
                    actions.appendChild(button);
                }
                appendText(
                    "p",
                    "Port compatibility: use Validate in the selected runtime target to check producer and destination contracts. Matching representations alone do not prove compatibility.",
                    "builder-inspector-note",
                );
                appendText(
                    "p",
                    "Draft definitions and recorded run evidence are separate. Opening a preview does not execute the pipeline.",
                    "builder-inspector-note",
                );
            }
            if (propertyPanel instanceof HTMLElement) propertyPanel.scrollTop = 0;
            if (propertiesBodyElement instanceof HTMLElement) propertiesBodyElement.scrollTop = 0;
            scheduleRender();
        };
        edgesLayerElement.addEventListener("click", (event) => inspectEdge(event.target));
        edgesLayerElement.addEventListener("keydown", (event) => {
            if (event.key !== "Enter" && event.key !== " ") return;
            event.preventDefault();
            inspectEdge(event.target);
        });

        rootElement.addEventListener("click", (event) => {
            if (!(event.target instanceof HTMLElement)) {
                return;
            }
            if (event.target.closest(".builder-node")) {
                return;
            }
            if (
                (mainGridElement instanceof HTMLElement && mainGridElement.contains(event.target)) ||
                (propertiesAsideElement instanceof HTMLElement && propertiesAsideElement.contains(event.target))
            ) {
                return;
            }
            selectedSlug = null;
            selectedEdge = null;
            showSelection(null);
            scheduleRender();
        });

        document.addEventListener("keydown", (event) => {
            if (isTypingContext(event.target)) {
                return;
            }

            if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === "z") {
                event.preventDefault();
                if (event.shiftKey) {
                    redoCanvas();
                } else {
                    undoCanvas();
                }
                return;
            }

            if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === "y") {
                event.preventDefault();
                redoCanvas();
                return;
            }

            if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "c") {
                const current = selectedItem();
                if (current instanceof HTMLElement) {
                    copiedNode = readNodeDataFromElement(current);
                }
                return;
            }

            if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "v") {
                if (!copiedNode) {
                    return;
                }
                event.preventDefault();
                const nodes = readNodes(gridElement);
                const maxY = nodes.reduce((acc, node) => Math.max(acc, node.y), 0);
                const nextY = Math.max(maxY + 1, (copiedNode.y || 0) + 1);
                const nextX = Math.max(0, Math.min(9, (copiedNode.x || 0) + 1));
                const pasted = {
                    ...copiedNode,
                    slug: `${copiedNode.slug || "node"}-copy`,
                    stepSlug: isStepKind(copiedNode.kind)
                        ? `${copiedNode.stepSlug || copiedNode.slug || "step"}-copy`
                        : copiedNode.stepSlug,
                };
                createNodeFromData(pasted, nextX, nextY);
                return;
            }

            if ((event.metaKey || event.ctrlKey) && (event.key === "+" || event.key === "=")) {
                event.preventDefault();
                const anchor = defaultZoomAnchor();
                adjustCanvasScale(zoomStep, anchor.clientX, anchor.clientY);
                return;
            }

            if ((event.metaKey || event.ctrlKey) && event.key === "-") {
                event.preventDefault();
                const anchor = defaultZoomAnchor();
                adjustCanvasScale(-zoomStep, anchor.clientX, anchor.clientY);
                return;
            }

            if ((event.metaKey || event.ctrlKey) && event.key === "0") {
                event.preventDefault();
                fitCanvasToNodes();
                return;
            }

            if (event.key === "Escape") {
                selectedSlug = null;
                selectedEdge = null;
                showSelection(null);
                scheduleRender();
                return;
            }

            if (event.key !== "Delete" && event.key !== "Backspace") {
                return;
            }

            if (!selectedSlug) {
                if (selectedEdge) {
                    const edges = parseEdgeData(edgesDataElement).filter((edge) => {
                        return !(edge.from === selectedEdge.from && edge.to === selectedEdge.to);
                    });
                    writeEdgesData(edgesDataElement, edges);
                    selectedEdge = null;
                    scheduleRender();
                    saveAndValidate();
                }
                return;
            }

            const target = selectedItem();
            if (!(target instanceof HTMLElement)) {
                selectedSlug = null;
                showSelection(null);
                applySelection(gridElement, selectedSlug);
                return;
            }

            target.remove();
            removeConnectedEdges(edgesDataElement, selectedSlug);
            selectedSlug = null;
            selectedEdge = null;
            showSelection(null);
            scheduleRender();
            saveAndValidate();
        });

        window.addEventListener("resize", () => {
            applyMainGridLayout();
            applyWorkspaceViewMode();
            scheduleRender();
        });

        if (typeof ResizeObserver === "function") {
            const resizeObserver = new ResizeObserver(() => {
                scheduleRender();
            });
            resizeObserver.observe(stageElement);
            resizeObserver.observe(boundsElement);
            resizeObserver.observe(surfaceElement);
            resizeObserver.observe(gridElement);
            if (viewportElement instanceof HTMLElement) {
                resizeObserver.observe(viewportElement);
            }
        }

        applyCanvasScale();
        syncCanvasSurfaceSize();

        document.getElementById("builder-fit-btn")?.addEventListener("click", fitCanvasToNodes);
        for (const [id, direction] of [
            ["builder-zoom-in-btn", 1],
            ["builder-zoom-out-btn", -1],
        ]) {
            document.getElementById(id)?.addEventListener("click", () => {
                const anchor = defaultZoomAnchor();
                adjustCanvasScale(zoomStep * direction, anchor.clientX, anchor.clientY);
            });
        }
        if (options.fitToContentOnInit !== false) {
            const runInitialFit = () => {
                fitCanvasToNodes();
            };

            // Layout finalization is handled by the deterministic layout engine.
            window.requestAnimationFrame(() => {
                runInitialFit();
                window.requestAnimationFrame(() => {
                    runInitialFit();
                });
            });

            window.setTimeout(() => {
                runInitialFit();
            }, 120);
        }

        const buildBuilderStatePayload = (nodes, edges, bindings) => {
            const form = document.getElementById("builder-creation-form");
            const fields = form && !form.hidden ? new FormData(form) : null;
            return buildBuilderStatePayloadFromLiveNodes(nodes, edges, bindings, {
                slug: String(fields?.get("pipeline_slug") || options.pipeline?.slug || DRAFT_PIPELINE_SLUG).trim(),
                name: String(fields?.get("name") || options.pipeline?.name || "Draft Pipeline").trim(),
                expectedVersionId: parseOptionalVersionToken(options.pipeline?.expectedVersionId),
                currentVersionId: parseOptionalVersionToken(options.pipeline?.currentVersionId),
                draftBasedOnVersionId: parseOptionalVersionToken(options.pipeline?.draftBasedOnVersionId),
                sourceFormat: pipelineSourceFormat,
                exportFormat: pipelineExportFormat,
                targetOrgSlug: getActiveOrganizationSlug(),
                targetProjectSlug: getActiveProjectSlug(),
                targetEnvironmentSlug: getActiveTargetEnvironmentSlug(),
            });
        };

        const buildExportBody = () => {
            const nodes = readNodes(gridElement);
            const edges = parseEdgeData(edgesDataElement);
            return buildBuilderStatePayload(nodes, edges, bindingsState);
        };

        const creationForm = document.getElementById("builder-creation-form");
        if (creationForm && savedState?.nodes?.length) {
            creationForm.elements.name.value = savedState.pipeline?.name || "";
            creationForm.elements.pipeline_slug.value = savedState.pipeline?.slug || "";
        }
        creationForm?.addEventListener("input", () => {
            document.getElementById("builder-creation-status").textContent = "";
            saveAndValidate();
        });
        creationForm?.addEventListener("submit", async (event) => {
            event.preventDefault();
            const button = creationForm.querySelector("button[type=submit]");
            if (button.disabled || creationForm.hidden) return;
            const status = document.getElementById("builder-creation-status");
            const payload = buildExportBody();
            button.disabled = true;
            status.textContent = "Queuing Git proposal…";
            status.classList.remove("text-error");
            try {
                const response = await fetch(
                    `/orgs/${encodeURIComponent(getActiveOrganizationSlug())}/projects/${encodeURIComponent(getActiveProjectSlug())}/pipeline-creations?environment_slug=${encodeURIComponent(getActiveTargetEnvironmentSlug())}`,
                    {
                        method: "POST",
                        headers: { Accept: "application/json", "Content-Type": "application/json" },
                        redirect: "error",
                        body: JSON.stringify(payload),
                    },
                );
                const result = await response.json();
                if (!response.ok) throw new Error(result.message || "The pipeline could not be proposed.");
                if (typeof result.progress_url !== "string" || !result.progress_url.startsWith("/orgs/")) {
                    throw new Error("The proposal returned an unexpected response. Retry the same definition.");
                }
                try {
                    window.localStorage.removeItem(storageKey);
                } catch (_) {
                    // A queued proposal must remain reachable when local storage is unavailable.
                }
                window.location.assign(result.progress_url);
            } catch (error) {
                status.textContent = error.message || "The proposal outcome is unavailable. Retry the same definition.";
                status.classList.add("text-error");
                button.disabled = false;
            }
        });

        const { buildConflictDiffJSONForState } = builderConflicts;

        const buildConflictDiffStateJSON = () => {
            return buildConflictDiffJSONForState(buildExportBody());
        };

        const { readAPIJSON, readAPIErrorMessage } = builderAPI;

        const buildConflictSummary = (conflict) => {
            const expectedVersionId = parseOptionalVersionToken(conflict?.expected_spec_hash);
            const currentVersionId = parseOptionalVersionToken(conflict?.current_spec_hash);
            if (expectedVersionId != null && currentVersionId != null) {
                return `Your draft expected version ${expectedVersionId}, but the canonical pipeline is now at version ${currentVersionId}.`;
            }
            if (currentVersionId != null) {
                return `The canonical pipeline changed and is now at version ${currentVersionId}.`;
            }
            return "The canonical pipeline changed since you started editing.";
        };

        let activeConflictResolutionSession = null;
        let activeConflictDialogConfig = null;

        const setConflictModalStatus = (message, tone = TONE_NEUTRAL) => {
            const statusElement = document.getElementById("builder-conflict-modal-status");
            if (!(statusElement instanceof HTMLElement)) {
                return;
            }
            statusElement.textContent = message;
            statusElement.classList.remove("text-error", "text-success", "text-base-content/60");
            if (tone === TONE_ERROR) {
                statusElement.classList.add("text-error");
                return;
            }
            if (tone === TONE_SUCCESS) {
                statusElement.classList.add("text-success");
                return;
            }
            statusElement.classList.add("text-base-content/60");
        };

        const setConflictModalMode = (mode) => {
            const overview = document.getElementById("builder-conflict-modal-overview");
            const resolver = document.getElementById("builder-conflict-resolver");
            if (overview instanceof HTMLElement) {
                overview.classList.toggle("hidden", mode === "resolver");
            }
            if (resolver instanceof HTMLElement) {
                resolver.classList.toggle("hidden", mode !== "resolver");
            }
        };

        const resetConflictDialogState = () => {
            activeConflictResolutionSession = null;
            activeConflictDialogConfig = null;
            resetConflictDialogDOMState();
        };

        const conflictChangeKindLabel = (changeKind) => {
            switch (changeKind) {
                case "add":
                    return "Added";
                case "remove":
                    return "Removed";
                default:
                    return "Changed";
            }
        };

        const conflictChangeKindClass = (changeKind) => {
            switch (changeKind) {
                case "add":
                    return "badge badge-success badge-outline";
                case "remove":
                    return "badge badge-error badge-outline";
                default:
                    return "badge badge-warning badge-outline";
            }
        };

        const conflictUnitResolvedCount = (unit) => {
            if (!unit || !Array.isArray(unit.fields)) {
                return 0;
            }
            return unit.fields.filter((field) => Boolean(field?.choice)).length;
        };

        const conflictFieldCurrentChoiceLabel = (field) => {
            switch (field?.choice) {
                case "canonical":
                    return "Canonical";
                case "draft":
                    return "Draft";
                case "manual":
                    return "Manual";
                default:
                    return "Unresolved";
            }
        };

        const renderConflictFieldValue = (value, valueType, present) => {
            if (!present) {
                return '<span class="text-base-content/50">(absent)</span>';
            }
            if (valueType === "presence") {
                return '<span class="text-base-content/70">Present</span>';
            }
            if (valueType === "json") {
                const formatted = formatJSONMap(value) || stableJSONStringify(value) || "{}";
                return `<pre class="overflow-auto whitespace-pre-wrap break-all text-[11px] font-mono text-base-content/80">${escapeHTML(formatted)}</pre>`;
            }
            if (value == null || value === "") {
                return '<span class="text-base-content/50">(empty)</span>';
            }
            return `<span class="block break-all font-mono text-[11px] text-base-content/80">${escapeHTML(String(value))}</span>`;
        };

        const buildConflictUnitListHTML = (session) => {
            if (!session || !Array.isArray(session.units) || session.units.length === 0) {
                return '<p class="text-sm text-base-content/60">No resolvable units.</p>';
            }

            return session.units
                .map((unit) => {
                    const isSelected = unit.key === session.selectedUnitKey;
                    const resolvedCount = conflictUnitResolvedCount(unit);
                    const unresolvedCount = Math.max(unit.fields.length - resolvedCount, 0);
                    const summaryLabel = unresolvedCount === 0 ? "Resolved" : `${unresolvedCount} unresolved`;
                    return `
                        <div class="mb-3 rounded-lg border ${isSelected ? "border-primary bg-primary/5 shadow-sm" : "border-base-300 bg-base-100"}">
                            <button
                                type="button"
                                class="flex w-full items-start justify-between gap-3 px-3 py-3 text-left"
                                data-conflict-select-unit="${escapeHTML(unit.key)}"
                            >
                                <div class="min-w-0 flex-1">
                                    <p class="truncate text-sm font-semibold text-base-content">${escapeHTML(unit.title || unit.key)}</p>
                                    <p class="mt-1 text-xs text-base-content/60">${escapeHTML(unit.subtitle || "")}</p>
                                    <p class="mt-2 text-[11px] ${unresolvedCount === 0 ? "text-success" : "text-base-content/60"}">${escapeHTML(summaryLabel)}</p>
                                </div>
                                <div class="flex flex-col items-end gap-2">
                                    <span class="${conflictChangeKindClass(unit.changeKind)}">${escapeHTML(conflictChangeKindLabel(unit.changeKind))}</span>
                                    <span class="text-[11px] text-base-content/50">${resolvedCount}/${unit.fields.length} resolved</span>
                                </div>
                            </button>
                        </div>
                    `;
                })
                .join("");
        };

        const buildConflictInspectorHTML = (session, unit) => {
            if (!session || !unit) {
                return '<p class="text-sm text-base-content/60">Select a changed unit to resolve its fields.</p>';
            }

            const unitResolvedCount = conflictUnitResolvedCount(unit);
            const allCanonical =
                Array.isArray(unit.fields) &&
                unit.fields.length > 0 &&
                unit.fields.every((field) => field.choice === "canonical");
            const allDraft =
                Array.isArray(unit.fields) &&
                unit.fields.length > 0 &&
                unit.fields.every((field) => field.choice === "draft");
            const issueList =
                Array.isArray(session.summary?.issues) && session.summary.issues.length > 0
                    ? `
                        <div class="mt-4 rounded-lg border border-error/30 bg-error/10 px-3 py-3">
                            <p class="text-xs font-semibold uppercase tracking-wide text-error">Merged validation issues</p>
                            <ul class="mt-2 space-y-1">
                                ${session.summary.issues
                                    .map(
                                        (issue) =>
                                            `<li class="text-xs text-base-content/80">${escapeHTML(issue.message)}</li>`,
                                    )
                                    .join("")}
                            </ul>
                        </div>
                    `
                    : "";

            return `
                <div class="space-y-3" data-conflict-unit-key="${escapeHTML(unit.key)}">
                    <div class="flex flex-wrap items-start justify-between gap-3">
                        <div>
                            <p class="text-sm font-semibold text-base-content">${escapeHTML(unit.title || unit.key)}</p>
                            <p class="mt-1 text-xs text-base-content/60">${escapeHTML(unit.subtitle || "")}</p>
                        </div>
                        <span class="${conflictChangeKindClass(unit.changeKind)}">${escapeHTML(conflictChangeKindLabel(unit.changeKind))}</span>
                    </div>
                    <div class="rounded-lg border border-base-300 bg-base-200/30 px-3 py-3" data-conflict-unit-actions="${escapeHTML(unit.key)}">
                        <div class="flex flex-wrap items-center justify-between gap-2">
                            <div>
                                <p class="text-xs font-semibold uppercase tracking-wide text-base-content/50">Apply to this unit</p>
                                <p class="mt-1 text-[11px] text-base-content/60">Set every field in this unit in one action.</p>
                            </div>
                            <span class="text-[11px] text-base-content/50">${escapeHTML(`${unitResolvedCount}/${unit.fields.length} resolved`)}</span>
                        </div>
                        <div class="mt-3 flex flex-wrap items-center gap-2">
                            <button
                                type="button"
                                class="btn btn-sm ${allCanonical ? "btn-primary" : "btn-outline"}"
                                data-conflict-apply-unit="${escapeHTML(unit.key)}"
                                data-conflict-choice="canonical"
                            >Accept canonical</button>
                            <button
                                type="button"
                                class="btn btn-sm ${allDraft ? "btn-primary" : "btn-outline"}"
                                data-conflict-apply-unit="${escapeHTML(unit.key)}"
                                data-conflict-choice="draft"
                            >Keep draft</button>
                        </div>
                    </div>
                    ${unit.fields
                        .map((field) => {
                            const isManual = field.choice === "manual";
                            const currentChoice = conflictFieldCurrentChoiceLabel(field);
                            const editorValue =
                                typeof field.manualText === "string"
                                    ? field.manualText
                                    : field.valueType === "json"
                                      ? formatJSONMap(field.draftValue) || ""
                                      : field.draftValue == null
                                        ? ""
                                        : String(field.draftValue);
                            return `
                                <div class="rounded-lg border border-base-300 bg-base-100 p-3" data-conflict-field-key="${escapeHTML(field.key)}">
                                    <div class="flex flex-wrap items-start justify-between gap-3">
                                        <div>
                                            <p class="text-sm font-semibold text-base-content">${escapeHTML(field.label)}</p>
                                            <p class="mt-1 text-[11px] text-base-content/60">${escapeHTML(currentChoice)}</p>
                                        </div>
                                        ${field.manualError ? `<span class="text-[11px] text-error">${escapeHTML(field.manualError)}</span>` : ""}
                                    </div>
                                    <div class="mt-3 flex flex-wrap items-center gap-2" data-conflict-field-actions="${escapeHTML(field.key)}">
                                        <button type="button" class="btn btn-sm ${field.choice === "canonical" ? "btn-primary" : "btn-outline"}" data-conflict-field-choice="canonical" data-conflict-unit="${escapeHTML(unit.key)}" data-conflict-field="${escapeHTML(field.key)}">Accept canonical</button>
                                        <button type="button" class="btn btn-sm ${field.choice === "draft" ? "btn-primary" : "btn-outline"}" data-conflict-field-choice="draft" data-conflict-unit="${escapeHTML(unit.key)}" data-conflict-field="${escapeHTML(field.key)}">Keep draft</button>
                                        ${
                                            field.valueType !== "presence"
                                                ? `<button type="button" class="btn btn-sm ${isManual ? "btn-primary" : "btn-outline"}" data-conflict-field-choice="manual" data-conflict-unit="${escapeHTML(unit.key)}" data-conflict-field="${escapeHTML(field.key)}">Manual</button>`
                                                : ""
                                        }
                                    </div>
                                    ${
                                        field.valueType !== "presence"
                                            ? `
                                                <div class="mt-3 ${isManual ? "" : "hidden"}">
                                                    ${
                                                        field.valueType === "json"
                                                            ? `<textarea class="textarea textarea-bordered h-32 w-full font-mono text-xs" data-conflict-manual-input="${escapeHTML(unit.key)}" data-conflict-manual-field="${escapeHTML(field.key)}">${escapeHTML(editorValue)}</textarea>`
                                                            : `<input class="input input-bordered w-full text-sm" type="text" data-conflict-manual-input="${escapeHTML(unit.key)}" data-conflict-manual-field="${escapeHTML(field.key)}" value="${escapeHTML(editorValue)}"/>`
                                                    }
                                                </div>
                                            `
                                            : ""
                                    }
                                    <div class="mt-3 grid gap-3 lg:grid-cols-2">
                                        <div class="rounded border border-base-300 bg-base-200/40 px-3 py-2">
                                            <p class="text-[10px] font-semibold uppercase tracking-wide text-base-content/50">Canonical</p>
                                            <div class="mt-1">${renderConflictFieldValue(field.canonicalValue, field.valueType, field.canonicalPresent)}</div>
                                        </div>
                                        <div class="rounded border border-base-300 bg-base-200/40 px-3 py-2">
                                            <p class="text-[10px] font-semibold uppercase tracking-wide text-base-content/50">Draft</p>
                                            <div class="mt-1">${renderConflictFieldValue(field.draftValue, field.valueType, field.draftPresent)}</div>
                                        </div>
                                    </div>
                                </div>
                            `;
                        })
                        .join("")}
                    ${issueList}
                </div>
            `;
        };

        const renderConflictDialogActions = () => {
            const modalActions = document.getElementById("builder-conflict-modal-actions");
            const conflictModal = document.getElementById("builder-conflict-modal");
            if (
                !(modalActions instanceof HTMLElement) ||
                !(conflictModal instanceof HTMLDialogElement) ||
                !activeConflictDialogConfig
            ) {
                return;
            }

            clearChildren(modalActions);
            const resolverMode = Boolean(activeConflictResolutionSession);
            const appendButton = (label, className, onClick, disabled = false) => {
                const button = document.createElement("button");
                button.type = "button";
                button.className = className;
                button.textContent = label;
                button.disabled = disabled;
                button.addEventListener("click", onClick);
                modalActions.appendChild(button);
            };

            if (resolverMode) {
                appendButton("Back to diff", "btn btn-sm btn-outline", () => {
                    activeConflictResolutionSession = null;
                    setConflictModalMode("overview");
                    setConflictModalStatus("Resolver closed. The line diff is still available below.");
                    renderConflictDialogActions();
                });
            } else {
                appendButton("Use canonical", "btn btn-sm btn-outline", () => {
                    conflictModal.close();
                    discardDraftAndReload(
                        activeConflictDialogConfig.organizationSlug,
                        activeConflictDialogConfig.projectSlug,
                        activeConflictDialogConfig.pipelineSlug,
                    ).catch((error) => {
                        const message = error instanceof Error ? error.message : "unknown error";
                        setStatusMessage(`Discard failed: ${message}.`, TONE_ERROR);
                    });
                });

                if (
                    activeConflictDialogConfig.conflict?.can_overwrite &&
                    typeof activeConflictDialogConfig.onOverwrite === "function"
                ) {
                    appendButton(
                        activeConflictDialogConfig.overwriteLabel || "Use my draft",
                        "btn btn-sm btn-warning",
                        () => {
                            conflictModal.close();
                            activeConflictDialogConfig.onOverwrite();
                        },
                    );
                }

                appendButton("Merge manually", "btn btn-sm btn-primary", async () => {
                    if (!activeConflictDialogConfig) {
                        return;
                    }
                    setConflictModalStatus("Loading canonical state for the merge resolver...");
                    try {
                        const canonicalState = await fetchCanonicalBuilderState(
                            activeConflictDialogConfig.organizationSlug,
                            activeConflictDialogConfig.projectSlug,
                            activeConflictDialogConfig.pipelineSlug,
                        );
                        const session = buildConflictResolutionSession(
                            canonicalState,
                            activeConflictDialogConfig.draftState,
                        );
                        if (!Array.isArray(session.units) || session.units.length === 0) {
                            setConflictModalStatus(
                                "No field-level semantic conflicts were found. Choose a side from the conflict actions instead.",
                                TONE_NEUTRAL,
                            );
                            return;
                        }
                        activeConflictResolutionSession = session;
                        setConflictModalMode("resolver");
                        setConflictModalStatus("Resolve each changed unit, then apply the merged draft.");
                        renderConflictDialogActions();
                        renderConflictResolutionSession();
                    } catch (error) {
                        const message =
                            error instanceof Error
                                ? error.message
                                : "Could not load canonical state for merge resolution.";
                        setConflictModalStatus(message, TONE_ERROR);
                    }
                });
            }

            if (resolverMode) {
                appendButton(
                    "Apply merge",
                    "btn btn-sm btn-primary",
                    async () => {
                        if (
                            !activeConflictResolutionSession?.summary?.canApply ||
                            typeof activeConflictDialogConfig?.onApplyMerge !== "function"
                        ) {
                            return;
                        }
                        const canonicalVersionId = parseOptionalVersionToken(
                            activeConflictDialogConfig.conflict?.current_spec_hash ??
                                options?.pipeline?.currentVersionId,
                        );
                        if (canonicalVersionId == null) {
                            setConflictModalStatus(
                                "Canonical version is missing; cannot save the merged draft.",
                                TONE_ERROR,
                            );
                            return;
                        }
                        const mergedState = rewriteConflictResolutionBaseline(
                            buildConflictResolutionMergedState(activeConflictResolutionSession),
                            canonicalVersionId,
                            {
                                source_format: pipelineSourceFormat,
                                export_format: pipelineExportFormat,
                            },
                        );
                        setConflictModalStatus("Applying merged draft...");
                        const saved = await activeConflictDialogConfig.onApplyMerge(
                            mergedState,
                            activeConflictDialogConfig.conflict,
                        );
                        if (saved) {
                            conflictModal.close();
                            return;
                        }
                        setConflictModalStatus(
                            "Saving the merged draft failed. Review the error and retry.",
                            TONE_ERROR,
                        );
                    },
                    !activeConflictResolutionSession.summary?.canApply,
                );
            }
        };

        const renderConflictResolutionSession = () => {
            const session = activeConflictResolutionSession;
            if (!session) {
                return;
            }

            const unitsList = document.getElementById("builder-conflict-units-list");
            const inspector = document.getElementById("builder-conflict-resolver-inspector");
            const unresolvedCount = document.getElementById("builder-conflict-resolver-unresolved-count");
            const validationCount = document.getElementById("builder-conflict-resolver-validation-count");

            if (!(unitsList instanceof HTMLElement) || !(inspector instanceof HTMLElement)) {
                return;
            }

            const selectedUnit =
                session.units.find((unit) => unit.key === session.selectedUnitKey) || session.units[0] || null;

            unitsList.innerHTML = buildConflictUnitListHTML(session);
            inspector.innerHTML = buildConflictInspectorHTML(session, selectedUnit);

            if (unresolvedCount instanceof HTMLElement) {
                unresolvedCount.textContent = `${session.summary.unresolvedCount} unresolved`;
            }
            if (validationCount instanceof HTMLElement) {
                validationCount.textContent =
                    session.summary.issues.length === 0
                        ? "No validation issues."
                        : `${session.summary.issues.length} validation issue${session.summary.issues.length === 1 ? "" : "s"}.`;
            }
            setConflictModalStatus(
                session.summary.canApply
                    ? "Merged draft is ready to apply."
                    : session.summary.unresolvedCount > 0
                      ? "Resolve every changed field before applying the merged draft."
                      : "Merged draft still has validation issues.",
                session.summary.canApply ? TONE_SUCCESS : TONE_NEUTRAL,
            );
            renderConflictDialogActions();

            const attachChoiceHandlers = (root) => {
                root.querySelectorAll("[data-conflict-select-unit]").forEach((button) => {
                    button.addEventListener("click", () => {
                        const unitKey = button.getAttribute("data-conflict-select-unit") || "";
                        activeConflictResolutionSession = selectConflictResolutionUnit(
                            activeConflictResolutionSession,
                            unitKey,
                        );
                        renderConflictResolutionSession();
                    });
                });

                root.querySelectorAll("[data-conflict-apply-unit][data-conflict-choice]").forEach((button) => {
                    button.addEventListener("click", () => {
                        const unitKey = button.getAttribute("data-conflict-apply-unit") || "";
                        const choice = button.getAttribute("data-conflict-choice") || "";
                        activeConflictResolutionSession = applyConflictResolutionUnitChoice(
                            activeConflictResolutionSession,
                            { unitKey, choice },
                        );
                        renderConflictResolutionSession();
                    });
                });

                root.querySelectorAll("[data-conflict-field-choice][data-conflict-unit][data-conflict-field]").forEach(
                    (button) => {
                        button.addEventListener("click", () => {
                            const unitKey = button.getAttribute("data-conflict-unit") || "";
                            const fieldKey = button.getAttribute("data-conflict-field") || "";
                            const choice = button.getAttribute("data-conflict-field-choice") || "";
                            activeConflictResolutionSession = applyConflictResolutionChoice(
                                activeConflictResolutionSession,
                                { unitKey, fieldKey, choice },
                            );
                            renderConflictResolutionSession();
                        });
                    },
                );

                root.querySelectorAll("[data-conflict-manual-input][data-conflict-manual-field]").forEach((input) => {
                    input.addEventListener("input", () => {
                        const unitKey = input.getAttribute("data-conflict-manual-input") || "";
                        const fieldKey = input.getAttribute("data-conflict-manual-field") || "";
                        const manualText =
                            input instanceof HTMLTextAreaElement || input instanceof HTMLInputElement
                                ? input.value
                                : "";
                        activeConflictResolutionSession = updateConflictResolutionManualValue(
                            activeConflictResolutionSession,
                            { unitKey, fieldKey, manualText },
                        );
                        renderConflictResolutionSession();
                    });
                });
            };

            attachChoiceHandlers(unitsList);
            attachChoiceHandlers(inspector);
        };

        const populateConflictDiffView = ({
            conflict,
            draftStateJson,
            organizationSlug,
            projectSlug,
            pipelineSlug,
        }) => {
            const diffPre = document.getElementById("builder-conflict-modal-diff");
            if (!(diffPre instanceof HTMLElement)) {
                return;
            }
            diffPre.textContent = "Loading diff…";

            const unifiedDiff = conflict?.conflict_diff?.comparable_unified_diff;
            if (typeof unifiedDiff === "string" && unifiedDiff.trim() !== "") {
                diffPre.textContent = unifiedDiff.trim();
                return;
            }

            if (!(typeof draftStateJson === "string" && organizationSlug && projectSlug && pipelineSlug)) {
                diffPre.textContent = "Diff not available.";
                return;
            }

            fetchCanonicalBuilderState(organizationSlug, projectSlug, pipelineSlug)
                .then((canonicalState) => {
                    const canonicalJson = buildConflictDiffJSONForState(canonicalState);
                    let normalizedDraftJson = draftStateJson;
                    try {
                        normalizedDraftJson = buildConflictDiffJSONForState(JSON.parse(draftStateJson));
                    } catch (_) {
                        // Keep fallback draft JSON when parsing fails.
                    }
                    const hunks = window.braidplaneComputeLineDiff(canonicalJson, normalizedDraftJson);
                    const hasVisibleChanges = Array.isArray(hunks)
                        ? hunks.some((hunk) => hunk.kind === "add" || hunk.kind === "remove")
                        : false;
                    if (!hasVisibleChanges) {
                        diffPre.textContent =
                            "No content differences were found between canonical and your draft. This conflict is version-based.";
                        return;
                    }
                    diffPre.innerHTML = window.braidplaneRenderDiff(hunks);
                })
                .catch((error) => {
                    const message =
                        error instanceof Error ? error.message : "Could not load canonical spec for comparison.";
                    diffPre.textContent = message;
                });
        };

        const showDraftConflictDialog = ({
            conflict,
            organizationSlug,
            projectSlug,
            pipelineSlug,
            overwriteLabel,
            onOverwrite,
            draftStateJson,
            draftState,
            onApplyMerge,
        }) => {
            const conflictModal = document.getElementById("builder-conflict-modal");
            if (!(conflictModal instanceof HTMLDialogElement)) {
                // Fallback to prompt if modal element is not found.
                const summary = buildConflictSummary(conflict);
                const detailParts = [];
                if (conflict?.draft_preserved) {
                    detailParts.push("Your server draft was preserved.");
                }
                if (conflict?.can_overwrite) {
                    detailParts.push("You can keep your draft and overwrite canonical.");
                }
                const detailText = detailParts.length > 0 ? `\n\n${detailParts.join(" ")}` : "";
                const options = conflict?.can_overwrite ? "1, 2, or 3" : "1 or 2";
                const overwriteOption = conflict?.can_overwrite ? `\n3. ${overwriteLabel || "Use my draft"}` : "";
                const selection = window.prompt(
                    `${summary}${detailText}\n\n1. Use canonical\n2. Reload latest${overwriteOption}`,
                    options,
                );
                if (selection === "1") {
                    discardDraftAndReload(organizationSlug, projectSlug, pipelineSlug).catch((error) => {
                        const message = error instanceof Error ? error.message : "unknown error";
                        setStatusMessage(`Discard failed: ${message}.`, TONE_ERROR);
                    });
                } else if (selection === "2") {
                    reloadBuilderFromServer(organizationSlug, projectSlug, pipelineSlug);
                } else if (selection === "3" && conflict?.can_overwrite && typeof onOverwrite === "function") {
                    onOverwrite();
                }
                return;
            }

            resetConflictDialogState();
            bindConflictModalCloseReset(resetConflictDialogState);
            activeConflictDialogConfig = {
                conflict,
                organizationSlug,
                projectSlug,
                pipelineSlug,
                overwriteLabel,
                onOverwrite,
                draftStateJson,
                draftState: cloneJSONValue(draftState || {}),
                onApplyMerge,
            };

            const modalTitle = document.getElementById("builder-conflict-modal-title");
            const modalSummary = document.getElementById("builder-conflict-modal-summary");
            const modalDetail = document.getElementById("builder-conflict-modal-detail");

            if (modalTitle instanceof HTMLElement) {
                modalTitle.textContent = "Version conflict";
            }
            if (modalSummary instanceof HTMLElement) {
                modalSummary.textContent = buildConflictSummary(conflict);
            }
            if (modalDetail instanceof HTMLElement) {
                const detailParts = [];
                if (conflict?.draft_preserved) {
                    detailParts.push("Your draft changes have been preserved on the server.");
                }
                if (parseOptionalVersionToken(conflict?.draft_based_on_spec_hash) != null) {
                    detailParts.push(`Your draft was based on version ${conflict.draft_based_on_spec_hash}.`);
                }
                modalDetail.textContent = detailParts.join(" ");
            }
            setConflictModalMode("overview");
            renderConflictDialogActions();
            setConflictModalStatus("Use canonical, use your draft, or merge manually before saving again.");
            if (!conflictModal.open) {
                conflictModal.showModal();
            }
            populateConflictDiffView({ conflict, draftStateJson, organizationSlug, projectSlug, pipelineSlug });
        };

        bindConflictModalCloseReset(resetConflictDialogState);

        const setSourceEditorStatus = (message, tone) => {
            if (!(sourceEditorStatus instanceof HTMLElement)) {
                return;
            }

            sourceEditorStatus.textContent = message;
            sourceEditorStatus.classList.remove("text-success", "text-error", "text-base-content/70");
            if (tone === TONE_SUCCESS) {
                sourceEditorStatus.classList.add("text-success");
                return;
            }
            if (tone === TONE_ERROR) {
                sourceEditorStatus.classList.add("text-error");
                return;
            }
            sourceEditorStatus.classList.add("text-base-content/70");
        };

        const _applyLaneLayout = () => {
            const nodes = readNodes(gridElement);
            if (nodes.length === 0) {
                return;
            }

            const rowGap = 3;
            const topOffset = 1;
            const transformX = 9;
            const sinkX = 17;

            const nodeBySlug = new Map(nodes.map((node) => [node.slug, node]));
            const edges = parseEdgeData(edgesDataElement);

            const transforms = nodes
                .filter((node) => isStepKind(node.kind))
                .slice()
                .sort((a, b) => a.slug.localeCompare(b.slug));
            const sinks = nodes
                .filter((node) => node.kind === KIND_SINK)
                .slice()
                .sort((a, b) => a.slug.localeCompare(b.slug));

            const transformRowBySlug = new Map();
            const transformPreferredOrder = [];

            transforms.forEach((transformNode) => {
                if (!transformPreferredOrder.includes(transformNode.slug)) {
                    transformPreferredOrder.push(transformNode.slug);
                }
            });

            transformPreferredOrder.forEach((slugValue, index) => {
                transformRowBySlug.set(slugValue, topOffset + index * rowGap);
            });

            transforms.forEach((node, index) => {
                const item = gridElement.querySelector(`.builder-node[data-node-slug="${node.slug}"]`);
                if (item instanceof HTMLElement) {
                    grid.update(item, {
                        x: transformX,
                        y: transformRowBySlug.get(node.slug) || topOffset + index * rowGap,
                    });
                }
            });

            sinks.forEach((node, index) => {
                const upstreamRows = edges
                    .filter((edge) => edge.to === node.slug && isStepKind(nodeBySlug.get(edge.from)?.kind || ""))
                    .map((edge) => transformRowBySlug.get(edge.from))
                    .filter((row) => Number.isInteger(row));

                let sinkRow = topOffset + index * rowGap;
                if (upstreamRows.length === 1) {
                    sinkRow = upstreamRows[0];
                } else if (upstreamRows.length > 1) {
                    sinkRow = Math.round(upstreamRows.reduce((acc, row) => acc + row, 0) / upstreamRows.length);
                }

                const item = gridElement.querySelector(`.builder-node[data-node-slug="${node.slug}"]`);
                if (item instanceof HTMLElement) {
                    grid.update(item, { x: sinkX, y: sinkRow });
                }
            });
        };

        const applyImportedStateToCanvas = (importedState) => {
            const normalizedState = normalizeImportedBuilderState(importedState);
            if (!normalizedState) {
                setSourceEditorStatus("Source apply failed: invalid state payload.", TONE_ERROR);
                return;
            }

            clearChildren(edgesLayerElement);

            Array.from(gridElement.querySelectorAll(".builder-node")).forEach((item) => {
                if (item instanceof HTMLElement) {
                    grid.removeWidget(item);
                }
            });

            clearChildren(edgesDataElement);
            restoreNodeLayout(grid, gridElement, normalizedState);
            writeEdgesData(edgesDataElement, normalizedState.edges);
            bindingsState = normalizeBindings(normalizedState.bindings);

            pipelineSourceFormat = normalizePipelineFormat(normalizedState.pipeline.source_format, FORMAT_YAML);
            pipelineExportFormat = pipelineSourceFormat;

            Array.from(gridElement.querySelectorAll(".builder-node[data-node-slug]")).forEach((item) => {
                bindNodeSelection(item);
            });

            selectedSlug = null;
            selectedEdge = null;
            showSelection(null);
            scheduleRender();
            saveAndValidate();
            fitCanvasToNodes();
            setStatusMessage("Pipeline source applied to canvas.", TONE_SUCCESS);
            setSourceEditorStatus("Applied source, refreshed graph, and fitted canvas.", TONE_SUCCESS);
            if (workspaceViewMode !== "source" && mainGridElement instanceof HTMLElement) {
                mainGridElement.scrollIntoView({ block: "start", inline: "nearest" });
            }
        };

        const applySavedPipelineResult = (savedPipeline) => {
            const savedPipelineSlug = savedPipeline?.draft?.pipeline_slug || savedPipeline?.slug || "";
            if (typeof savedPipelineSlug !== "string" || savedPipelineSlug.trim() === "") {
                return;
            }

            const versionContext = savedPipelineVersionContext(savedPipeline, {
                expectedVersionId: options?.pipeline?.expectedVersionId,
                currentVersionId: options?.pipeline?.currentVersionId,
                draftBasedOnVersionId: options?.pipeline?.draftBasedOnVersionId,
            });

            setActivePipelineContext(
                getActiveOrganizationSlug(),
                getActiveProjectSlug(),
                savedPipelineSlug.trim(),
                options?.pipeline?.name || savedPipelineSlug.trim(),
                true,
                versionContext,
            );
            markActionStatePersisted();
            refreshValidation();
        };

        const persistResolvedConflictDraft = async ({
            mergedState,
            organizationSlug,
            projectSlug,
            pipelineSlug,
            overwriteLabel,
            onOverwrite,
            flow,
        }) => {
            applyImportedStateToCanvas(mergedState);
            const savePayload = prepareConflictResolutionSaveState(mergedState, buildExportBody(), {
                source_format: pipelineSourceFormat,
                export_format: pipelineExportFormat,
            });

            if (typeof options.saveUrl !== "string" || options.saveUrl.trim() === "") {
                setStatusMessage(
                    "Conflict resolved in the editor, but the draft save endpoint is unavailable.",
                    TONE_ERROR,
                );
                return false;
            }

            const response = await fetch(options.saveUrl, {
                method: "POST",
                headers: {
                    Accept: "application/json",
                    "Content-Type": "application/json",
                },
                body: JSON.stringify(savePayload),
            });

            if (!response.ok) {
                const payload = await readAPIJSON(response);
                if (response.status === 409 && payload?.error) {
                    showDraftConflictDialog({
                        conflict: payload,
                        organizationSlug,
                        projectSlug,
                        pipelineSlug,
                        overwriteLabel,
                        onOverwrite,
                        draftStateJson: buildConflictDiffJSONForState(savePayload),
                        draftState: savePayload,
                        onApplyMerge: async (nextMergedState) =>
                            persistResolvedConflictDraft({
                                mergedState: nextMergedState,
                                organizationSlug,
                                projectSlug,
                                pipelineSlug,
                                overwriteLabel,
                                onOverwrite,
                                flow,
                            }),
                    });
                    setStatusMessage(
                        "Another canonical update happened while saving the merged draft. Review the new conflict and retry.",
                        TONE_ERROR,
                    );
                    return false;
                }

                const reason = payload?.error?.detailed_error || payload?.error?.message || "unknown error";
                setStatusMessage(`Saving the merged draft failed: ${reason}.`, TONE_ERROR);
                return false;
            }

            const responseProblem = saveResponseProblem(response);
            if (responseProblem !== "") {
                setStatusMessage(`Saving the merged draft failed: ${responseProblem}`, TONE_ERROR);
                return false;
            }

            const savedPipeline = await readAPIJSON(response);
            if (!savedPipeline) {
                setStatusMessage("Saving the merged draft failed: unexpected server response.", TONE_ERROR);
                return false;
            }

            applySavedPipelineResult(savedPipeline);
            setStatusMessage(
                flow === "publish"
                    ? "Conflict resolved and merged draft saved. Publish again when ready."
                    : saveStatusMessageFromPipeline(),
                TONE_SUCCESS,
            );
            return true;
        };

        const syncSourceEditorFromCanvas = async () => {
            if (!(sourceEditorField instanceof HTMLTextAreaElement)) {
                return;
            }

            // Avoid export API calls for empty canvases during initial load.
            if (readNodes(gridElement).length === 0) {
                sourceEditorField.value = "";
                resetSourceEditorViewport();
                setSourceEditorStatus("Canvas is empty. Add nodes to generate source.", TONE_NEUTRAL);
                renderSourceHighlight();
                return;
            }

            try {
                const response = await fetch(options.exportUrl || "/builder/export", {
                    method: "POST",
                    headers: {
                        "Content-Type": "application/json",
                    },
                    body: JSON.stringify(buildExportBody()),
                });

                if (!response.ok) {
                    const message = await readAPIErrorMessage(response);
                    setSourceEditorStatus(message || "Unable to sync source from canvas.", TONE_ERROR);
                    return;
                }

                sourceEditorField.value = await response.text();
                sourceEditorCanvasSource = sourceEditorField.value;
                resetSourceEditorViewport();
                setSourceEditorStatus("Source synced from current canvas.", TONE_SUCCESS);
                renderSourceHighlight();
            } catch (_) {
                setSourceEditorStatus("Unable to sync source from canvas.", TONE_ERROR);
            }
        };

        if (sourceEditorField instanceof HTMLTextAreaElement) {
            sourceEditorField.addEventListener("focus", () => {
                sourceEditorField.style.caretColor = "var(--color-primary)";
            });
            sourceEditorField.addEventListener("input", () => {
                renderSourceHighlight();
            });
            sourceEditorField.addEventListener("scroll", () => {
                syncSourceOverlayScroll();
                startSourceScrollSyncLoop();
            });
            sourceEditorField.addEventListener(
                "wheel",
                (event) => {
                    event.stopPropagation();
                    startSourceScrollSyncLoop();
                },
                { passive: true },
            );
            sourceEditorField.addEventListener("pointerdown", () => {
                startSourceScrollSyncLoop();
            });
            sourceEditorField.addEventListener(
                "touchstart",
                () => {
                    startSourceScrollSyncLoop();
                },
                { passive: true },
            );
        }

        if (previewModeButton instanceof HTMLButtonElement) {
            previewModeButton.addEventListener("click", () => {
                workspaceViewMode = "preview";
                applyWorkspaceViewMode();
            });
        }

        if (splitModeButton instanceof HTMLButtonElement) {
            splitModeButton.addEventListener("click", () => {
                workspaceViewMode = "split";
                applyWorkspaceViewMode();
            });
        }

        if (sourceModeButton instanceof HTMLButtonElement) {
            sourceModeButton.addEventListener("click", () => {
                workspaceViewMode = "source";
                applyWorkspaceViewMode();
                if (shouldSyncSourceOnOpen()) {
                    setSourceEditorStatus("Syncing source from current canvas...", TONE_NEUTRAL);
                    syncSourceEditorFromCanvas();
                }
            });
        }

        if (togglePaletteButton instanceof HTMLButtonElement) {
            togglePaletteButton.addEventListener("click", () => {
                isPaletteVisible = !isPaletteVisible;
                applyMainGridLayout();
            });
        }

        if (togglePropertiesButton instanceof HTMLButtonElement) {
            togglePropertiesButton.addEventListener("click", () => {
                isPropertiesVisible = !isPropertiesVisible;
                applyMainGridLayout();
            });
        }

        if (sourceEditorSyncButton instanceof HTMLButtonElement) {
            sourceEditorSyncButton.addEventListener("click", async () => {
                await syncSourceEditorFromCanvas();
            });
        }

        if (sourceEditorApplyButton instanceof HTMLButtonElement) {
            sourceEditorApplyButton.addEventListener("click", async () => {
                if (!(sourceEditorField instanceof HTMLTextAreaElement)) {
                    return;
                }

                const source = sourceEditorField.value.trim();
                if (source === "") {
                    setSourceEditorStatus("Pipeline source is empty.", TONE_ERROR);
                    return;
                }

                try {
                    const response = await fetch(options.importUrl || "/builder/import", {
                        method: "POST",
                        headers: {
                            "Content-Type": "application/json",
                        },
                        body: JSON.stringify({
                            format: "",
                            source,
                        }),
                    });

                    if (!response.ok) {
                        const message = await readAPIErrorMessage(response);
                        setSourceEditorStatus(message || "Unable to apply pipeline source.", TONE_ERROR);
                        return;
                    }

                    const importedState = await response.json();
                    applyImportedStateToCanvas(importedState);
                    sourceEditorCanvasSource = sourceEditorField.value;
                } catch (_) {
                    setSourceEditorStatus("Unable to apply pipeline source.", TONE_ERROR);
                }
            });
        }

        if (validateButton instanceof HTMLButtonElement) {
            validateButton.addEventListener("click", async () => {
                const isValid = refreshValidation();
                if (!isValid) {
                    setStatusMessage("Draft issues found. Resolve them before server preflight.", TONE_ERROR);
                    return;
                }
                if (!getActiveOrganizationSlug() || !getActiveProjectSlug() || !getActiveTargetEnvironmentSlug()) {
                    setStatusMessage(
                        "Draft checks passed. Select a project and runtime target for server preflight.",
                        TONE_NEUTRAL,
                    );
                    return;
                }
                const snapshot = JSON.stringify(buildExportBody());
                const panel = document.getElementById("builder-server-validation");
                const list = document.getElementById("builder-server-validation-list");
                panel?.classList.add("hidden");
                serverValidationSnapshot = "";
                serverDiagnostics = [];
                renderEdgeDiagnostics();
                scheduleRender();
                validateButton.disabled = true;
                validateButton.setAttribute("aria-busy", "true");
                setStatusMessage("Checking this draft against the selected runtime target...", TONE_NEUTRAL);
                try {
                    const response = await fetch(options.validationUrl || "/builder/validate", {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify(buildExportBody()),
                    });
                    const validationPayload = response.ok ? null : await readAPIJSON(response);
                    const diagnostics = response.ok
                        ? []
                        : builderAPI.builderValidationTargets(validationPayload, JSON.parse(snapshot));
                    const messages = response.ok
                        ? [`Server validation passed in ${getActiveTargetEnvironmentSlug()} for this draft.`]
                        : builderAPI.builderValidationIssues(validationPayload, response.status);
                    if (snapshot !== JSON.stringify(buildExportBody())) {
                        setStatusMessage(
                            "The draft changed during validation. Validate the current draft again.",
                            TONE_NEUTRAL,
                        );
                        return;
                    }
                    if (list instanceof HTMLElement) {
                        list.replaceChildren(
                            ...messages.map((message, index) => {
                                const item = document.createElement("li");
                                const diagnostic = diagnostics[index];
                                if (diagnostic?.edge || diagnostic?.step) {
                                    const button = document.createElement("button");
                                    button.type = "button";
                                    button.className = "link text-left whitespace-normal";
                                    button.textContent = message;
                                    button.addEventListener("click", () => {
                                        if (diagnostic.edge) {
                                            const path = Array.from(
                                                edgesLayerElement.querySelectorAll('[role="button"]'),
                                            ).find(
                                                (entry) =>
                                                    entry.getAttribute(ATTR_EDGE_FROM) === diagnostic.edge.from &&
                                                    entry.getAttribute(ATTR_EDGE_TO) === diagnostic.edge.to,
                                            );
                                            inspectEdge(path);
                                            path?.focus({ preventScroll: true });
                                        } else {
                                            const node = Array.from(gridElement.querySelectorAll(".builder-node")).find(
                                                (entry) => entry.getAttribute(ATTR_NODE_SLUG) === diagnostic.step,
                                            );
                                            if (node) {
                                                selectedSlug = diagnostic.step;
                                                selectedEdge = null;
                                                isPropertiesVisible = true;
                                                applyMainGridLayout();
                                                showSelection(node);
                                                scheduleRender();
                                            }
                                        }
                                    });
                                    item.appendChild(button);
                                } else item.textContent = message;
                                return item;
                            }),
                        );
                    }
                    panel?.classList.remove("hidden");
                    panel?.classList.toggle("text-error", !response.ok);
                    panel?.classList.toggle("text-success", response.ok);
                    serverValidationSnapshot = snapshot;
                    serverDiagnostics = diagnostics;
                    renderEdgeDiagnostics();
                    scheduleRender();
                    setStatusMessage(
                        response.ok
                            ? messages[0]
                            : "Server preflight failed. Review its diagnostics; the draft is kept.",
                        response.ok ? TONE_SUCCESS : TONE_ERROR,
                    );
                } catch (_) {
                    setStatusMessage("Server preflight is unavailable. Retry validation.", TONE_ERROR);
                } finally {
                    validateButton.disabled = false;
                    validateButton.removeAttribute("aria-busy");
                }
            });
        }

        if (proposalRefreshButton instanceof HTMLButtonElement) {
            proposalRefreshButton.addEventListener("click", async () => {
                const organizationSlug = getActiveOrganizationSlug();
                const projectSlug = getActiveProjectSlug();
                const pipelineSlug = (options?.pipeline?.slug || "").trim();
                if (!organizationSlug || !projectSlug || !pipelineSlug || pipelineSlug === DRAFT_PIPELINE_SLUG) {
                    setStatusMessage(
                        "Proposal refresh is unavailable until this draft has a real pipeline slug.",
                        TONE_ERROR,
                    );
                    return;
                }
                proposalRefreshButton.disabled = true;
                setStatusMessage("Queuing proposal status refresh...", TONE_NEUTRAL);
                try {
                    const response = await fetch(
                        `/orgs/${encodeURIComponent(organizationSlug)}/projects/${encodeURIComponent(projectSlug)}/pipelines/${encodeURIComponent(pipelineSlug)}/proposal-refresh`,
                        {
                            method: "POST",
                            headers: { Accept: "application/json" },
                        },
                    );
                    if (!response.ok) {
                        setStatusMessage(
                            (await readAPIErrorMessage(response)) || "Unable to refresh proposal status.",
                            TONE_ERROR,
                        );
                        return;
                    }
                    setStatusMessage(
                        "Proposal status refresh queued. Reload shortly to see the provider result.",
                        TONE_SUCCESS,
                    );
                } catch (_) {
                    setStatusMessage("Unable to refresh proposal status.", TONE_ERROR);
                } finally {
                    proposalRefreshButton.disabled = false;
                }
            });
        }

        if (publishButton instanceof HTMLButtonElement) {
            const publishDraft = async (force = false, conflict = null) => {
                const organizationSlug = getActiveOrganizationSlug();
                const projectSlug = getActiveProjectSlug();
                const pipelineSlug = (options?.pipeline?.slug || "").trim();
                if (!organizationSlug || !projectSlug || !pipelineSlug || pipelineSlug === DRAFT_PIPELINE_SLUG) {
                    setStatusMessage("Propose is unavailable until this draft has a real pipeline slug.", TONE_ERROR);
                    return;
                }

                setStatusMessage(
                    force ? "Queuing Git proposal from stale draft..." : "Queuing Git proposal from draft...",
                    TONE_NEUTRAL,
                );
                try {
                    const response = await fetch(
                        `/orgs/${encodeURIComponent(organizationSlug)}/projects/${encodeURIComponent(projectSlug)}/pipelines/${encodeURIComponent(pipelineSlug)}/publish`,
                        {
                            method: "POST",
                            headers: {
                                Accept: "application/json",
                                "Content-Type": "application/json",
                            },
                            body: JSON.stringify({
                                expected_spec_hash: parseOptionalVersionToken(
                                    conflict?.expected_spec_hash ?? options?.pipeline?.expectedVersionId,
                                ),
                                force,
                            }),
                        },
                    );
                    if (!response.ok) {
                        const payload = await readAPIJSON(response);
                        if (response.status === 409 && payload?.error) {
                            const draftState = buildExportBody();
                            showDraftConflictDialog({
                                conflict: payload,
                                organizationSlug,
                                projectSlug,
                                pipelineSlug,
                                draftStateJson: buildConflictDiffStateJSON(),
                                draftState,
                                onOverwrite: () => {
                                    publishDraft(true, payload).catch(() => {
                                        setStatusMessage("Publish failed: network or server error.", TONE_ERROR);
                                    });
                                },
                                onApplyMerge: async (mergedState, latestConflict) =>
                                    persistResolvedConflictDraft({
                                        mergedState,
                                        organizationSlug,
                                        projectSlug,
                                        pipelineSlug,
                                        onOverwrite: () => {
                                            publishDraft(true, latestConflict).catch(() => {
                                                setStatusMessage(
                                                    "Publish failed: network or server error.",
                                                    TONE_ERROR,
                                                );
                                            });
                                        },
                                        flow: "publish",
                                    }),
                            });
                            setStatusMessage("Publish blocked by a newer canonical version.", TONE_ERROR);
                            return;
                        }

                        const errorCode =
                            typeof payload?.error?.code === "string" ? payload.error.code.trim().toUpperCase() : "";
                        if (response.status === 404 && errorCode === "PIPELINE_DRAFT_NOT_FOUND") {
                            pipelineHasDraft = false;
                            options.pipeline.hasDraft = false;
                            if (rootElement instanceof HTMLElement) {
                                rootElement.dataset.pipelineHasDraft = "false";
                            }
                            refreshValidation();
                            setStatusMessage(
                                "Draft no longer exists (likely published in another tab). Save current changes to create a new draft, then publish again.",
                                TONE_ERROR,
                            );
                            return;
                        }

                        const reason = payload?.error?.detailed_error || payload?.error?.message || "unknown error";
                        setStatusMessage(`Publish failed: ${reason}.`, TONE_ERROR);
                        return;
                    }

                    const publishedPipeline = await readAPIJSON(response);
                    pipelineHasDraft = Boolean(publishedPipeline?.draft);
                    options.pipeline.hasDraft = pipelineHasDraft;
                    applyPipelineVersionContext({
                        expectedVersionId: parseOptionalVersionToken(publishedPipeline?.current_version_spec_hash),
                        currentVersionId: parseOptionalVersionToken(publishedPipeline?.current_version_spec_hash),
                        draftBasedOnVersionId: parseOptionalVersionToken(publishedPipeline?.draft?.based_on_spec_hash),
                    });
                    if (rootElement instanceof HTMLElement) {
                        rootElement.dataset.pipelineHasDraft = pipelineHasDraft ? "true" : "false";
                    }

                    markActionStatePersisted();
                    refreshValidation();
                    setStatusMessage(
                        force
                            ? "Git proposal queued from overwritten draft. Open Propose to check progress."
                            : "Git proposal queued. Open Propose to check progress and review in Git.",
                        TONE_SUCCESS,
                    );
                } catch (_) {
                    setStatusMessage("Publish failed: network or server error.", TONE_ERROR);
                }
            };

            const proposalReviewModal = document.getElementById("builder-proposal-review-modal");
            const proposalReviewFacts = document.getElementById("builder-proposal-review-facts");
            const proposalReviewState = document.getElementById("builder-proposal-review-state");
            const proposalStructuralDiff = document.getElementById("builder-proposal-structural-diff");
            const proposalSourceDiff = document.getElementById("builder-proposal-source-diff");
            const proposalReviewError = document.getElementById("builder-proposal-review-error");
            const proposalReviewAttribution = document.getElementById("builder-proposal-review-attribution");
            const proposalReviewLink = document.getElementById("builder-proposal-review-link");
            const proposalReviewConfirm = document.getElementById("builder-proposal-review-confirm");
            const proposalReviewCancel = document.getElementById("builder-proposal-review-cancel");
            let proposalReviewCanPublish = false;
            let proposalPublishPending = false;

            const proposalFact = (label, value) => {
                const item = document.createElement("div");
                item.className = "min-w-0 rounded border border-base-300 bg-base-100 px-3 py-2";
                const name = document.createElement("p");
                name.className = "font-semibold uppercase tracking-wide text-base-content/50";
                name.textContent = label;
                const content = document.createElement("p");
                content.className = "mt-1 break-all font-mono text-base-content/80";
                content.textContent = value || "Not available";
                item.append(name, content);
                return item;
            };

            const openProposalReview = async () => {
                const organizationSlug = getActiveOrganizationSlug();
                const projectSlug = getActiveProjectSlug();
                const pipelineSlug = (options?.pipeline?.slug || "").trim();
                if (!organizationSlug || !projectSlug || !pipelineSlug || pipelineSlug === DRAFT_PIPELINE_SLUG) {
                    setStatusMessage(
                        "Proposal review is unavailable until this draft has a real pipeline slug.",
                        TONE_ERROR,
                    );
                    return;
                }
                if (!(proposalReviewModal instanceof HTMLDialogElement)) {
                    setStatusMessage("Proposal review is unavailable.", TONE_ERROR);
                    return;
                }
                if (proposalReviewError instanceof HTMLElement) {
                    proposalReviewError.textContent = "";
                }
                if (proposalReviewLink instanceof HTMLAnchorElement) {
                    proposalReviewLink.classList.add("hidden");
                    proposalReviewLink.removeAttribute("href");
                }
                if (proposalReviewConfirm instanceof HTMLButtonElement) {
                    proposalReviewConfirm.disabled = true;
                }
                proposalReviewCanPublish = false;
                proposalReviewModal.showModal();

                try {
                    const response = await fetch(
                        `/orgs/${encodeURIComponent(organizationSlug)}/projects/${encodeURIComponent(projectSlug)}/pipelines/${encodeURIComponent(pipelineSlug)}/proposal-review`,
                        { headers: { Accept: "application/json" } },
                    );
                    const review = await readAPIJSON(response);
                    if (!response.ok) {
                        throw new Error(
                            review?.error?.detailed_error || review?.error?.message || `status ${response.status}`,
                        );
                    }

                    if (proposalReviewFacts instanceof HTMLElement) {
                        proposalReviewFacts.replaceChildren(
                            proposalFact("Repository", review.repository),
                            proposalFact("Initiating member", review.initiating_member),
                            proposalFact(
                                review.publication_mode === "repository_credential"
                                    ? "Git connection"
                                    : "Provider actor",
                                review.provider_actor,
                            ),
                            proposalFact("Authority source", review.authority_source),
                            proposalFact("Publication mode", review.publication_mode),
                            proposalFact("Source path", review.source_path),
                            proposalFact("Base ref", review.base_ref),
                            proposalFact("Base commit", review.base_commit_sha),
                            proposalFact("Base spec hash", review.base_spec_hash),
                            proposalFact("Draft spec hash", review.draft_spec_hash),
                            proposalFact("Proposal branch", review.branch_name),
                            proposalFact("Last known head", review.branch_head_sha),
                            proposalFact("Provider", review.proposal_provider),
                            proposalFact("Proposal ID", review.proposal_id),
                            proposalFact("Last proposed hash", review.last_proposed_spec_hash),
                            proposalFact("Status observed", review.status_observed_at),
                        );
                    }
                    if (proposalReviewAttribution instanceof HTMLElement) {
                        proposalReviewAttribution.textContent = review.attribution || "";
                    }
                    if (
                        proposalReviewLink instanceof HTMLAnchorElement &&
                        /^https?:\/\//.test(review.proposal_url || "")
                    ) {
                        proposalReviewLink.href = review.proposal_url;
                        proposalReviewLink.classList.remove("hidden");
                    }
                    if (proposalReviewState instanceof HTMLElement) {
                        proposalReviewState.textContent =
                            review.draft_state === "publish_failed"
                                ? "Publish failed — retry available"
                                : review.draft_state === "publish_queued"
                                  ? "Publish queued"
                                  : review.proposal_is_stale
                                    ? "Proposal behind draft"
                                    : review.proposal_state
                                      ? `Last known: ${review.proposal_state}`
                                      : review.draft_state || "Draft";
                    }
                    if (proposalStructuralDiff instanceof HTMLElement) {
                        proposalStructuralDiff.textContent = review.structural_diff || "No structural differences.";
                    }
                    if (proposalSourceDiff instanceof HTMLElement) {
                        proposalSourceDiff.textContent =
                            review.source_diff || review.proposed_source || "No source differences.";
                    }
                    if (proposalReviewConfirm instanceof HTMLButtonElement) {
                        proposalReviewCanPublish = Boolean(review.provider_supported);
                        proposalReviewConfirm.disabled = !proposalReviewCanPublish;
                    }
                    if (!review.provider_supported && proposalReviewError instanceof HTMLElement) {
                        proposalReviewError.textContent =
                            "Proposal publication is unavailable. Check the project repository connection and publishing access.";
                    }
                } catch (error) {
                    if (proposalReviewError instanceof HTMLElement) {
                        proposalReviewError.textContent = `Unable to load proposal review: ${error.message || "unknown error"}.`;
                    }
                }
            };

            if (proposalReviewCancel instanceof HTMLButtonElement) {
                proposalReviewCancel.addEventListener("click", () => {
                    if (proposalReviewModal instanceof HTMLDialogElement) {
                        proposalReviewModal.close();
                    }
                });
            }
            if (proposalReviewConfirm instanceof HTMLButtonElement) {
                proposalReviewConfirm.addEventListener("click", async () => {
                    if (proposalPublishPending) {
                        return;
                    }
                    proposalPublishPending = true;
                    proposalReviewConfirm.disabled = true;
                    if (proposalReviewModal instanceof HTMLDialogElement) {
                        proposalReviewModal.close();
                    }
                    try {
                        await publishDraft(false, null);
                    } finally {
                        proposalPublishPending = false;
                        proposalReviewConfirm.disabled = !proposalReviewCanPublish;
                    }
                });
            }
            publishButton.addEventListener("click", openProposalReview);
        }

        if (saveButton instanceof HTMLButtonElement) {
            saveButton.addEventListener("click", async () => {
                if (!storageKey) {
                    setStatusMessage("Choose an organization and project before saving this draft.", TONE_ERROR);
                    return;
                }
                refreshValidation();

                saveState(storageKey, gridElement, edgesDataElement, bindingsState, {
                    sourceFormat: pipelineSourceFormat,
                    exportFormat: pipelineExportFormat,
                });

                const now = new Date();
                const timestamp = now.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });

                const exportBody = buildExportBody();
                const saveAvailability = builderSaveAvailability(options?.pipeline?.slug || "", readNodes(gridElement));
                if (!saveAvailability.canPersist) {
                    setStatusMessage(`${saveAvailability.localSaveMessage} (${timestamp}).`, TONE_NEUTRAL);
                    return;
                }
                if (!hasUnpersistedActionChanges()) {
                    setStatusMessage(`No draft changes to save (${timestamp}).`, TONE_NEUTRAL);
                    return;
                }

                if (typeof options.saveUrl === "string" && options.saveUrl.trim() !== "") {
                    setStatusMessage("Saving to Braidplane...", TONE_NEUTRAL);
                    try {
                        const response = await fetch(options.saveUrl, {
                            method: "POST",
                            headers: {
                                Accept: "application/json",
                                "Content-Type": "application/json",
                            },
                            body: JSON.stringify(exportBody),
                        });
                        if (!response.ok) {
                            const payload = await readAPIJSON(response);
                            if (response.status === 409 && payload?.error) {
                                const organizationSlug = getActiveOrganizationSlug();
                                const projectSlug = getActiveProjectSlug();
                                const pipelineSlug = (options?.pipeline?.slug || "").trim();
                                if (
                                    organizationSlug &&
                                    projectSlug &&
                                    pipelineSlug &&
                                    pipelineSlug !== DRAFT_PIPELINE_SLUG
                                ) {
                                    showDraftConflictDialog({
                                        conflict: payload,
                                        organizationSlug,
                                        projectSlug,
                                        pipelineSlug,
                                        draftStateJson: buildConflictDiffStateJSON(),
                                        draftState: exportBody,
                                        onApplyMerge: async (mergedState) =>
                                            persistResolvedConflictDraft({
                                                mergedState,
                                                organizationSlug,
                                                projectSlug,
                                                pipelineSlug,
                                                flow: "save",
                                            }),
                                    });
                                    setStatusMessage(
                                        `Save blocked by a newer canonical version (${timestamp}).`,
                                        TONE_ERROR,
                                    );
                                    return;
                                }
                            }
                            const reason = payload?.error?.detailed_error || payload?.error?.message || "unknown error";
                            setStatusMessage(
                                `Save to Braidplane failed: ${reason}. Local draft kept (${timestamp}).`,
                                TONE_ERROR,
                            );
                            return;
                        }

                        const responseProblem = saveResponseProblem(response);
                        if (responseProblem !== "") {
                            setStatusMessage(
                                `Save to Braidplane failed: ${responseProblem} Local draft kept (${timestamp}).`,
                                TONE_ERROR,
                            );
                            return;
                        }

                        const savedPipeline = await readAPIJSON(response);
                        if (!savedPipeline) {
                            setStatusMessage(
                                `Save to Braidplane failed: unexpected server response. Local draft kept (${timestamp}).`,
                                TONE_ERROR,
                            );
                            return;
                        }

                        applySavedPipelineResult(savedPipeline);

                        setStatusMessage(saveStatusMessageFromPipeline(), TONE_SUCCESS);
                    } catch (_) {
                        setStatusMessage(
                            `Save to Braidplane failed: network or server error. Local draft kept (${timestamp}).`,
                            TONE_ERROR,
                        );
                    }
                    return;
                }

                setStatusMessage(
                    `Save to Braidplane failed: save endpoint unavailable. Local draft kept (${timestamp}).`,
                    TONE_ERROR,
                );
            });
        }

        document.querySelectorAll("[data-builder-refresh-connections]").forEach((button) => {
            button.addEventListener("click", async () => {
                button.disabled = true;
                try {
                    await enhanceQueryFields();
                    const loaded = dataSourceConfiguration?.sourcesStatus === "loaded";
                    setStatusMessage(
                        loaded
                            ? "Connections refreshed."
                            : "Connections could not be refreshed. Check access and try again.",
                        loaded ? TONE_SUCCESS : TONE_ERROR,
                    );
                } catch (_) {
                    setStatusMessage("Connections could not be refreshed. Try again.", TONE_ERROR);
                } finally {
                    button.disabled = false;
                }
            });
        });

        renderPipelinePanel();
        enhanceQueryFields();

        if (readNodes(gridElement).length === 0 && !hasRestoredWorkspaceMode) {
            workspaceViewMode = "preview";
        }

        applyMainGridLayout();
        applyWorkspaceViewMode();
        markActionStatePersisted();
        scheduleRender();
        showSelection(null);
        saveAndValidate();
        if (sourceEditorField instanceof HTMLTextAreaElement) {
            if (initialPipelineSource.trim() !== "") {
                sourceEditorField.value = initialPipelineSource;
                sourceEditorCanvasSource = initialPipelineSource;
                resetSourceEditorViewport();
                setSourceEditorStatus(
                    "Loaded source from pipeline example. Canvas and source are now aligned.",
                    TONE_NEUTRAL,
                );
                renderSourceHighlight();
            } else {
                const isGraphValid = refreshValidation();
                if (!isGraphValid) {
                    setSourceEditorStatus("Source sync skipped: resolve validation issues first.", TONE_NEUTRAL);
                } else {
                    setSourceEditorStatus("Loading source from current canvas...", TONE_NEUTRAL);
                    syncSourceEditorFromCanvas();
                }
            }
        }

        // New pipelines have no server draft to recover after reload.
        window.addEventListener("pagehide", () => {
            if (creationForm && !creationForm.hidden) return;
            try {
                window.localStorage.removeItem(storageKey);
            } catch (_) {
                // Ignore storage failures.
            }
        });
    };

    window.braidplaneBuilderCanvas = {
        init,
        _testing: {
            parseJSONMap,
            formatJSONMap,
            safeOperationObject,
            normalizeConnectorDescriptor,
            connectorDescriptorMap,
            readConnectorDescriptorPayload,
            enrichDataSourceWithDescriptor,
            fallbackQueryOperationTypeFromDataSourceType,
            queryContextForOperationType,
            sinkContextForOperationType,
            legacySinkBackendFromDataSourceType,
            storageKeyFor,
            normalizePipelineFormat,
            validationStatusMessage,
            builderSaveAvailability,
            builderWorkbenchLayoutForWidth,
            builderEdgePath,
            builderCanvasViewBox,
            edgePreviewAgreements,
            saveStatusMessageFromPipeline,
            saveResponseProblem,
            setPrimaryActionDisabledState,
            saveState,
            loadState,
            normalizeBindings,
            serializeBindingsForExport,
            bindingsForTarget,
            validateBindings,
            validateBindingCompleteness,
            validateBoundaryContractDeclarations,
            validateEdgeTopology,
            normalizeStateForGridColumns,
            parseEdgeData,
            parseBindingData,
            writeEdgesData,
            hasPath,
            validateEdgeConnection,
            normalizeNodeForExport,
            validateNodes,
            bindingSourceNodeValue,
            payloadDiagnosticForBinding,
            upstreamPayloadRepresentation,
            contractSummary,
            transformInputNameFromSourceSlug,
            nextTransformInputNameForConnection,
            defaultBindingForConnection,
            removeBindingsForConnections,
            nodeConfigBadges,
            buildNodeDisclosureRows,
            renderNodeDisclosureContent,
            stripConflictDiffVersionMetadata,
            savedPipelineVersionContext,
            comparableBuilderState,
            comparableBuilderSnapshot,
            buildConflictResolutionSession,
            applyConflictResolutionChoice,
            buildConflictResolutionMergedState,
            rewriteConflictResolutionBaseline,
            prepareConflictResolutionSaveState,
            normalizeImportedBuilderState,
            setODCSPortRef,
            renameTransformInputAgreement,
            setODCSPortInlineContract,
            setODCSPortShape,
            buildBuilderStepInputMaps,
            buildStepSpecForExportNode,
            buildBuilderStatePayload: buildBuilderStatePayloadFromLiveNodes,
            renderBuilderNodeContent: applyNodeDataToElement,
            renderPipelinePanel: async (paletteBodyElement) => appendPipelinePanelShell(paletteBodyElement),
            bindConflictModalCloseReset: () => bindConflictModalCloseReset(() => resetConflictDialogDOMState()),
        },
    };
})();
