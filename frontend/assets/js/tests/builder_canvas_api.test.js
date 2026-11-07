import { beforeEach, describe, expect, it, vi } from "vitest";

await import("../builder_canvas_api.js");

const t = window.braidplaneBuilderCanvasAPI;

describe("builder canvas API adapter", () => {
    beforeEach(() => {
        vi.restoreAllMocks();
    });

    it("adds proposed fields without changing existing contract rules", () => {
        const field = { name: "id", type: "integer", required: true, validations: { minimum: 1 } };
        const contract = { representation: "json", root_shape: "array", fields: [field] };
        const proposed = [
            { name: "id", type: "string" },
            { name: "total", type: "decimal", precision: 12, scale: 2, nullable: true },
        ];
        expect(t.addProposedContractFields(contract, proposed)).toEqual({ ...contract, fields: [field, proposed[1]] });
        expect(contract.fields).toEqual([field]);
    });

    it("loads run evidence without caching or following authentication redirects", async () => {
        const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue({
            ok: true,
            headers: { get: () => "text/html" },
            text: async () => "<div data-builder-run-fragment>Recorded</div>",
        });
        await expect(t.loadBuilderRunEvidence({ org_slug: "acme", run_slug: "run-1" })).resolves.toContain("Recorded");
        expect(fetchMock.mock.calls[0][0]).toBe("/builder/run-evidence?org_slug=acme&run_slug=run-1");
        expect(fetchMock.mock.calls[0][1]).toMatchObject({ cache: "no-store", redirect: "error" });
        fetchMock.mockResolvedValue({ ok: false });
        await expect(t.loadBuilderRunEvidence({})).rejects.toThrow("Check access");
    });

    it("targets only exact server diagnostic connections and step paths", () => {
        const state = {
            graph: { edges: [{ from_step: "fetch", to_step: "store" }], bindings: [] },
            pipeline_spec: { steps: [{ slug: "fetch" }] },
        };
        const targets = t.builderValidationTargets(
            {
                error: {
                    validation_errors: [
                        {
                            field: "spec",
                            message:
                                'edge "fetch->store" incompatible contracts: field email expected string, actual integer',
                        },
                        { field: "steps[0].query", message: "Invalid query" },
                        { field: "spec", message: 'edge "unknown->store" incompatible contracts' },
                        { field: "graph.edges[0].from_step", message: "Invalid port" },
                    ],
                },
            },
            state,
        );
        expect(targets[0].edge).toEqual({ from: "fetch", to: "store" });
        expect(targets[0].message).toContain("expected string, actual integer");
        expect(targets[1].step).toBe("fetch");
        expect(targets[2].edge).toBeNull();
        expect(targets[3].edge).toEqual(targets[0].edge);
        expect(t.builderValidationTargets(null, state)).toEqual([]);
    });

    it("proposes read operations with exact identifiers and no invented provider syntax", () => {
        expect(t.proposedSourceOperation("postgres", { name: 'a"; DROP TABLE users;--' })).toEqual({
            sql: 'SELECT * FROM "a""; DROP TABLE users;--"',
        });
        expect(t.proposedSourceOperation("mysql", { name: "a`b" })).toEqual({ sql: "SELECT * FROM `a``b`" });
        expect(t.proposedSourceOperation("postgres", { name: "a.b" })).toEqual({ sql: 'SELECT * FROM "a.b"' });
        expect(t.proposedSourceOperation("mongodb", { name: "orders" })).toEqual({
            collection: "orders",
            operation: "find",
            filter: {},
        });
        expect(t.proposedSourceOperation("ftp", { name: "in/users.csv" })).toEqual({
            type: "ftp",
            operation: "read",
            path: "in/users.csv",
            format: "auto",
        });
        expect(
            t.proposedSourceOperation("s3", { name: "prefix/in/orders.json", source_key: "in/orders.json" }).key,
        ).toBe("in/orders.json");
        expect(t.proposedSourceOperation("s3", { name: "prefix/in/orders.json" })).toBeNull();
        expect(t.proposedSourceOperation("http", { name: "items" })).toBeNull();
        expect(t.proposedSourceOperation("postgres", { name: "bad\0name" })).toBeNull();
    });

    it("reads only cached schema and explicitly posts refresh to the concrete source", async () => {
        const fetchMock = vi
            .spyOn(globalThis, "fetch")
            .mockResolvedValue({ ok: true, json: async () => ({ status: "ready" }) });
        await t.loadDataSourceSchema("acme", "warehouse");
        expect(fetchMock.mock.calls[0][0]).toBe("/builder/schema?org_slug=acme&data_source_slug=warehouse");
        await t.refreshDataSourceSchema("acme", "warehouse");
        expect(fetchMock.mock.calls[1][0]).toBe("/api/organizations/acme/data-sources/warehouse/schema");
        expect(fetchMock.mock.calls[1][1].method).toBe("POST");
        fetchMock.mockResolvedValue({ ok: false });
        await expect(t.loadDataSourceSchema("acme", "warehouse")).rejects.toThrow("access");
        await expect(t.refreshDataSourceSchema("acme", "warehouse")).rejects.toThrow("permissions");
    });

    it("organizationAPIBasePath_EncodesSlug", () => {
        expect(t.organizationAPIBasePath("acme labs")).toBe("/api/organizations/acme%20labs");
    });

    it("builderValidationIssues_PreservesActionableGraphDiagnostics", () => {
        expect(
            t.builderValidationIssues(
                {
                    error: {
                        detailed_error: "builder export failed",
                        validation_errors: [
                            {
                                field: "steps",
                                code: "disconnected_graph",
                                message:
                                    "Independent flows: [fetch, store], [other, archive]. Connect them or split into separate pipelines.",
                            },
                            { message: "Exact ODCS revision is unavailable." },
                        ],
                    },
                },
                400,
            ),
        ).toEqual([
            "steps: Independent flows: [fetch, store], [other, archive]. Connect them or split into separate pipelines.",
            "Exact ODCS revision is unavailable.",
        ]);
        expect(t.builderValidationIssues({ error: { message: "Permission denied" } }, 403)).toEqual([
            "Permission denied",
        ]);
        expect(t.builderValidationIssues(null, 503)).toEqual(["Server preflight failed (status 503)."]);
    });

    it("readAPIErrorMessage_PrefersDetailedAPIError", async () => {
        const response = {
            headers: { get: () => "application/json" },
            json: async () => ({ error: { detailed_error: "duplicate slug", message: "invalid" } }),
            text: async () => "fallback",
        };

        await expect(t.readAPIErrorMessage(response)).resolves.toBe("duplicate slug");
    });

    it("listQueriesForDataSource_CachesByOrganizationAndDataSource", async () => {
        const fetchMock = vi.fn(async () => ({
            ok: true,
            json: async () => ({ queries: [{ slug: "active-users", name: "Active users" }] }),
        }));
        vi.stubGlobal("fetch", fetchMock);

        const cache = new Map();
        await expect(t.listQueriesForDataSource("acme", "postgres-main", cache)).resolves.toEqual([
            { value: "active-users", label: "Active users" },
        ]);
        await expect(t.listQueriesForDataSource("acme", "postgres-main", cache)).resolves.toEqual([
            { value: "active-users", label: "Active users" },
        ]);

        expect(fetchMock).toHaveBeenCalledTimes(1);
    });
});

describe("logical data source configuration", () => {
    const binding = {
        pipeline_slug: "daily-sync",
        data_source_ref_slug: "source",
        data_source_slug: "warehouse",
        data_source_active: true,
    };
    const configuration = {
        bindings: [binding],
        bindingsStatus: "loaded",
        sources: [{ slug: "warehouse", type: "postgres", readOnly: false }],
        sourcesStatus: "loaded",
    };

    it("resolves the same authored ref differently in two environments using only configuration GETs", async () => {
        const fetchMock = vi.fn(async (url, options) => {
            expect(options.method || "GET").toBe("GET");
            const path = new URL(url, "http://localhost");
            const cursor = path.searchParams.get("cursor");
            let payload;
            if (path.pathname.endsWith("/data-source-refs")) {
                payload = cursor
                    ? { data_source_refs: [{ pipeline_slug: "daily-sync", slug: "source" }] }
                    : {
                          data_source_refs: [{ pipeline_slug: "other-pipeline", slug: "other" }],
                          has_more: true,
                          next_cursor: "page two",
                      };
            } else if (path.pathname.endsWith("/data-source-bindings")) {
                payload = {
                    data_source_bindings: [
                        {
                            ...binding,
                            data_source_slug: path.pathname.includes("/prod/") ? "warehouse-prod" : "warehouse",
                        },
                    ],
                };
            } else {
                payload = {
                    data_sources: [
                        { slug: "warehouse", type: "postgres", is_active: true },
                        { slug: "warehouse-prod", type: "postgres", is_active: false },
                    ],
                };
            }
            return { ok: true, json: async () => payload };
        });
        vi.stubGlobal("fetch", fetchMock);
        for (const [environment, physical] of [
            ["dev", "warehouse"],
            ["prod", "warehouse-prod"],
        ]) {
            const loaded = await t.loadDataSourceReferences("acme", "ops", "daily-sync", environment);
            expect(loaded.refs).toEqual([{ pipeline_slug: "daily-sync", slug: "source" }]);
            expect(loaded.sources.map((source) => source.isActive)).toEqual([true, false]);
            expect(t.referenceConfiguration("source", loaded, environment).source.slug).toBe(physical);
            expect(t.referenceConfiguration("source", loaded, environment).hint).toContain(`bound to ${physical}`);
        }
        expect(fetchMock.mock.calls.every(([url]) => !url.includes("provider"))).toBe(true);
    });

    it.each([
        [{ ...configuration, bindings: [] }, "no binding configured"],
        [{ ...configuration, sources: [] }, "unavailable or inaccessible"],
        [{ ...configuration, bindings: [{ ...binding, data_source_active: false }] }, "inactive target"],
        [{ ...configuration, sourcesStatus: "failed" }, "source metadata failed to load"],
        [{ ...configuration, sourcesStatus: "inaccessible" }, "source metadata is inaccessible"],
        [{ ...configuration, bindingsStatus: "failed" }, "binding metadata failed to load"],
        [{ ...configuration, bindingsStatus: "inaccessible" }, "binding configuration is inaccessible"],
        [{ ...configuration, bindingsStatus: "no-environment" }, "select an environment"],
    ])("keeps uncertain configuration separate from the authored ref: %s", (state, expected) => {
        const before = structuredClone(state);
        expect(t.referenceConfiguration("source", state, "dev")).toEqual({ hint: expect.stringContaining(expected) });
        expect(state).toEqual(before);
    });

    it.each([403, 500, "network"])(
        "reports failed configuration reads (%s) without treating them as empty lists",
        async (status) => {
            vi.stubGlobal(
                "fetch",
                vi.fn(async () => {
                    if (status === "network") throw new Error("offline");
                    return { ok: false, status };
                }),
            );
            const loaded = await t.loadDataSourceReferences("acme", "ops", "daily-sync", "dev");
            expect(loaded.bindingsStatus).toBe(status === 403 ? "inaccessible" : "failed");
            expect(loaded.sourcesStatus).toBe(status === 403 ? "inaccessible" : "failed");
        },
    );
});

it("does not cache a failed saved-query metadata read as an empty catalog", async () => {
    const fetchMock = vi
        .fn()
        .mockResolvedValueOnce({ ok: false, status: 503 })
        .mockResolvedValueOnce({ ok: true, json: async () => ({ queries: [{ slug: "customers" }] }) });
    vi.stubGlobal("fetch", fetchMock);
    const cache = new Map();
    await expect(t.listQueriesForDataSource("acme", "warehouse", cache)).rejects.toThrow("metadata failed");
    expect(cache.size).toBe(0);
    await expect(t.listQueriesForDataSource("acme", "warehouse", cache)).resolves.toEqual([
        { value: "customers", label: "customers" },
    ]);
});

describe("pinned ODCS edge previews", () => {
    const ref = { contract_slug: "orders", revision_slug: "v2", schema_object_id: "row" };
    it("loads only the exact project revision and preserves its raw numeric declarations", async () => {
        const raw = '{"slug":"v2","document":{"schema":[{"id":"row","properties":[]}],"wide":9007199254740993}}';
        const fetchMock = vi.fn(async () => ({ ok: true, text: async () => raw }));
        vi.stubGlobal("fetch", fetchMock);
        const preview = await t.loadODCSContractPreview("acme", "ops", ref);
        expect(preview.rawRevision).toBe(raw);
        expect(preview.object.id).toBe("row");
        expect(fetchMock.mock.calls[0][0]).toBe("/api/organizations/acme/projects/ops/contracts/orders/revisions/v2");
    });
    it("rejects a substituted revision or missing schema object", async () => {
        vi.stubGlobal(
            "fetch",
            vi.fn(async () => ({ ok: true, text: async () => '{"slug":"v1","document":{"schema":[{"id":"row"}]}}' })),
        );
        await expect(t.loadODCSContractPreview("acme", "ops", ref)).rejects.toThrow("Pinned schema object unavailable");
        await expect(
            t.loadODCSContractPreview("acme", "ops", { ...ref, revision_slug: "v1", schema_object_id: "missing" }),
        ).rejects.toThrow("Pinned schema object unavailable");
    });
    it("does not resolve latest or hide access failures", async () => {
        vi.stubGlobal(
            "fetch",
            vi.fn(async () => ({ ok: false, status: 403 })),
        );
        await expect(t.loadODCSContractPreview("acme", "ops", { ...ref, revision_slug: "latest" })).rejects.toThrow(
            "Exact revision required",
        );
        await expect(t.loadODCSContractPreview("acme", "ops", ref)).rejects.toThrow("Contract preview unavailable");
    });
});

describe("new pipeline authoring connections", () => {
    it("uses an explicit authoring preview without inventing an existing runtime binding", () => {
        const configuration = {
            authoringNewPipeline: true,
            sourcesStatus: "loaded",
            bindingsStatus: "loaded",
            bindings: [],
            sources: [{ slug: "warehouse", name: "Warehouse", isActive: true }],
        };
        expect(t.referenceConfiguration("warehouse", configuration, "dev").source.slug).toBe("warehouse");
        expect(t.referenceConfiguration("warehouse", configuration, "dev").hint).toContain(
            "configure runtime bindings after merge and sync",
        );
        expect(
            t.referenceConfiguration("warehouse", { ...configuration, authoringNewPipeline: false }, "dev").source,
        ).toBeUndefined();
        configuration.sources[0].isActive = false;
        expect(t.referenceConfiguration("warehouse", configuration, "dev").source).toBeUndefined();
    });
});
