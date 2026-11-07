(() => {
    const readAPIJSON = async (response) => {
        const contentType = (response?.headers?.get?.("Content-Type") || "").toLowerCase();
        if (!contentType.includes("application/json")) {
            return null;
        }
        try {
            return await response.json();
        } catch (_) {
            return null;
        }
    };

    const readAPIErrorMessage = async (response) => {
        const payload = await readAPIJSON(response);
        const apiError = payload?.error;
        if (apiError && typeof apiError.detailed_error === "string" && apiError.detailed_error.trim() !== "") {
            return apiError.detailed_error.trim();
        }
        if (apiError && typeof apiError.message === "string" && apiError.message.trim() !== "") {
            return apiError.message.trim();
        }

        try {
            return (await response.text()).trim();
        } catch (_) {
            return "";
        }
    };

    const builderValidationIssues = (payload, status) => {
        const diagnostics = Array.isArray(payload?.error?.validation_errors) ? payload.error.validation_errors : [];
        const messages = diagnostics
            .filter((diagnostic) => typeof diagnostic?.message === "string" && diagnostic.message.trim())
            .map((diagnostic) =>
                diagnostic.field ? `${diagnostic.field}: ${diagnostic.message}` : diagnostic.message,
            );
        if (messages.length) return messages;
        const detail = payload?.error?.detailed_error;
        const message = payload?.error?.message;
        return [
            typeof detail === "string" && detail.trim()
                ? detail
                : typeof message === "string" && message.trim()
                  ? message
                  : `Server preflight failed (status ${status}).`,
        ];
    };

    const organizationAPIBasePath = (organizationSlug) => `/api/organizations/${encodeURIComponent(organizationSlug)}`;

    const builderValidationTargets = (payload, state) => {
        const diagnostics = payload?.error?.validation_errors;
        if (!Array.isArray(diagnostics)) return [];
        const edges = state?.graph?.edges || [];
        return diagnostics
            .filter((entry) => typeof entry?.message === "string" && entry.message.trim())
            .map((entry) => {
                const edgeIndex = /^graph\.(?:edges|bindings)\[(\d+)\]/.exec(entry.field || "");
                const collection = entry.field?.startsWith("graph.bindings") ? state.graph.bindings : edges;
                // shortcut: compatibility targets use the canonical edge marker; adopt structured edge fields if that diagnostic format changes.
                const marker = /\bedge "([a-z0-9-]+)->([a-z0-9-]+)"/.exec(entry.message);
                const edge = edgeIndex
                    ? collection?.[Number(edgeIndex[1])]
                    : marker
                      ? edges.find((candidate) => candidate.from_step === marker[1] && candidate.to_step === marker[2])
                      : null;
                const stepIndex = /^steps\[(\d+)\]/.exec(entry.field || "");
                const step = stepIndex ? state?.pipeline_spec?.steps?.[Number(stepIndex[1])]?.slug : null;
                return {
                    message: entry.field ? `${entry.field}: ${entry.message}` : entry.message,
                    code: entry.code || "",
                    edge: edge ? { from: edge.from_step, to: edge.to_step } : null,
                    step,
                };
            });
    };

    const loadODCSContractPreview = async (organizationSlug, projectSlug, reference) => {
        if (!reference?.revision_slug || reference.revision_slug === "latest")
            throw new Error("Exact revision required");
        const path = `${organizationAPIBasePath(organizationSlug)}/projects/${encodeURIComponent(projectSlug)}/contracts/${encodeURIComponent(reference.contract_slug)}/revisions/${encodeURIComponent(reference.revision_slug)}`;
        const response = await fetch(path, { headers: { Accept: "application/json" } });
        if (!response.ok) throw new Error("Contract preview unavailable");
        const rawRevision = await response.text();
        const revision = JSON.parse(rawRevision);
        const objects = Array.isArray(revision?.document?.schema)
            ? revision.document.schema.filter((object) => object.id === reference.schema_object_id)
            : [];
        if (revision?.slug !== reference.revision_slug || objects.length !== 1)
            throw new Error("Pinned schema object unavailable");
        return { revision, object: objects[0], rawRevision };
    };

    const loadDataSourceSchema = async (organizationSlug, dataSourceSlug) => {
        const query = new URLSearchParams({ org_slug: organizationSlug, data_source_slug: dataSourceSlug });
        const response = await fetch(`/builder/schema?${query}`, { headers: { Accept: "application/json" } });
        if (!response.ok) throw new Error("Cached schema unavailable. Check data source access and retry.");
        return response.json();
    };

    const loadBuilderRunEvidence = async (params) => {
        const response = await fetch(`/builder/run-evidence?${new URLSearchParams(params)}`, {
            headers: { Accept: "text/html" },
            cache: "no-store",
            redirect: "error",
        });
        if (!response.ok || !(response.headers.get("Content-Type") || "").includes("text/html"))
            throw new Error("Run evidence unavailable. Check access to this project and environment, then retry.");
        return response.text();
    };

    const refreshDataSourceSchema = async (organizationSlug, dataSourceSlug) => {
        const response = await fetch(
            `${organizationAPIBasePath(organizationSlug)}/data-sources/${encodeURIComponent(dataSourceSlug)}/schema`,
            { method: "POST", headers: { Accept: "application/json" } },
        );
        if (!response.ok) throw new Error("Schema refresh failed. Check data source permissions and retry.");
        return response.json();
    };

    const addProposedContractFields = (contract, fields) => {
        const existing = Array.isArray(contract?.fields) ? contract.fields : [];
        const names = new Set(existing.map((field) => field.name));
        return { ...contract, fields: [...existing, ...fields.filter((field) => !names.has(field.name))] };
    };

    const proposedSourceOperation = (type, dataset) => {
        const name = dataset?.name;
        if (typeof name !== "string" || !name || name.includes("\0")) return null;
        switch (type) {
            case "postgres":
                return { sql: `SELECT * FROM "${name.replaceAll('"', '""')}"` };
            case "mysql":
                return { sql: `SELECT * FROM \`${name.replaceAll("`", "``")}\`` };
            case "mongodb":
                return { collection: name, operation: "find", filter: {} };
            case "ftp":
                return { type: "ftp", operation: "read", path: name, format: "auto" };
            case "s3":
                return dataset.source_key
                    ? { type: "s3", operation: "read", key: dataset.source_key, format: "auto" }
                    : null;
            default:
                return null;
        }
    };

    const listConfiguration = async (path, field) => {
        const items = [];
        let cursor = "";
        do {
            const response = await fetch(`${path}?limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`, {
                headers: { Accept: "application/json" },
            });
            if (!response.ok) throw new Error(response.status === 403 ? "inaccessible" : "failed");
            const payload = await response.json();
            if (
                !Array.isArray(payload?.[field]) ||
                (payload.has_more && (!payload.next_cursor || payload.next_cursor === cursor))
            ) {
                throw new Error("failed");
            }
            items.push(...payload[field]);
            cursor = payload.has_more ? payload.next_cursor : "";
        } while (cursor);
        return items;
    };

    const listDataSourcesForOrg = async (organizationSlug) => {
        const sources = await listConfiguration(
            `${organizationAPIBasePath(organizationSlug)}/data-sources`,
            "data_sources",
        );
        return sources.map((ds) => ({
            slug: ds.slug,
            name: ds.name || ds.slug,
            type: ds.type,
            readOnly: Boolean(ds.read_only),
            isActive: Boolean(ds.is_active),
        }));
    };

    const loadDataSourceReferences = async (organizationSlug, projectSlug, pipelineSlug, environmentSlug) => {
        const projectPath = `${organizationAPIBasePath(organizationSlug)}/projects/${encodeURIComponent(projectSlug)}`;
        const read = async (load) => {
            try {
                return { items: await load(), status: "loaded" };
            } catch (error) {
                return { items: [], status: error.message === "inaccessible" ? "inaccessible" : "failed" };
            }
        };
        const [refs, bindings, sources] = await Promise.all([
            read(() => listConfiguration(`${projectPath}/data-source-refs`, "data_source_refs")),
            environmentSlug
                ? read(() =>
                      listConfiguration(
                          `${projectPath}/environments/${encodeURIComponent(environmentSlug)}/data-source-bindings`,
                          "data_source_bindings",
                      ),
                  )
                : { items: [], status: "no-environment" },
            read(() => listDataSourcesForOrg(organizationSlug)),
        ]);
        return {
            refs: refs.items.filter((ref) => ref.pipeline_slug === pipelineSlug),
            refsStatus: refs.status,
            bindings: bindings.items.filter((binding) => binding.pipeline_slug === pipelineSlug),
            bindingsStatus: bindings.status,
            sources: sources.items,
            sourcesStatus: sources.status,
        };
    };

    const referenceConfiguration = (ref, configuration, environmentSlug) => {
        const binding = configuration?.bindings.find((item) => item.data_source_ref_slug === ref);
        const prefix = `Logical ref ${ref}. ${environmentSlug || "No environment selected"}: `;
        if (!configuration) return { hint: `${prefix}loading configuration.` };
        if (configuration.authoringNewPipeline) {
            const source = configuration.sources.find((item) => item.slug === ref && item.isActive);
            if (configuration.sourcesStatus !== "loaded" || !source) {
                return { hint: `Authoring connection ${ref} is unavailable. Check connection access.` };
            }
            return {
                source,
                hint: `Authoring preview from ${source.name || ref}. The definition uses logical ref ${ref}; configure runtime bindings after merge and sync.`,
            };
        }
        if (configuration.bindingsStatus === "no-environment")
            return { hint: `${prefix}select an environment to see its binding.` };
        if (configuration.bindingsStatus !== "loaded") {
            return {
                hint: `${prefix}${configuration.bindingsStatus === "inaccessible" ? "binding configuration is inaccessible" : "binding metadata failed to load"}.`,
            };
        }
        if (!binding) return { hint: `${prefix}no binding configured.` };
        const source = configuration.sources.find((item) => item.slug === binding.data_source_slug);
        const resolution = `${prefix}bound to ${binding.data_source_slug}`;
        if (!binding.data_source_active) return { hint: `${resolution} (inactive target).` };
        if (configuration.sourcesStatus !== "loaded") {
            return {
                hint: `${resolution}; ${configuration.sourcesStatus === "inaccessible" ? "source metadata is inaccessible" : "source metadata failed to load"}.`,
            };
        }
        if (!source) return { hint: `${resolution}; source is unavailable or inaccessible.` };
        return { source, hint: `${resolution}${source.readOnly ? " (read-only)" : ""}.` };
    };

    const listQueriesForDataSource = async (organizationSlug, dataSourceSlug, queryOptionsByDataSource = new Map()) => {
        if (!organizationSlug || !dataSourceSlug) {
            return [];
        }

        const cacheKey = `${organizationSlug}:${dataSourceSlug}`;
        if (queryOptionsByDataSource.has(cacheKey)) {
            return queryOptionsByDataSource.get(cacheKey) || [];
        }

        const response = await fetch(
            `${organizationAPIBasePath(organizationSlug)}/data-sources/${encodeURIComponent(dataSourceSlug)}/queries?limit=100`,
            { headers: { Accept: "application/json" } },
        );
        if (!response.ok) throw new Error("Saved-query metadata failed to load");

        const payload = await response.json();
        if (!payload || !Array.isArray(payload.queries)) throw new Error("Invalid saved-query metadata");

        const options = payload.queries
            .map((query) => ({
                value: typeof query?.slug === "string" ? query.slug : "",
                label: typeof query?.name === "string" && query.name.trim() !== "" ? query.name : query?.slug || "",
            }))
            .filter((query) => query.value !== "");

        queryOptionsByDataSource.set(cacheKey, options);
        return options;
    };

    window.braidplaneBuilderCanvasAPI = {
        loadODCSContractPreview,
        loadDataSourceSchema,
        loadBuilderRunEvidence,
        refreshDataSourceSchema,
        addProposedContractFields,
        proposedSourceOperation,
        readAPIJSON,
        readAPIErrorMessage,
        builderValidationIssues,
        builderValidationTargets,
        organizationAPIBasePath,
        listDataSourcesForOrg,
        loadDataSourceReferences,
        referenceConfiguration,
        listQueriesForDataSource,
    };
})();
