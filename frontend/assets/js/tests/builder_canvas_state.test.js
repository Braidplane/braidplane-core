import { describe, expect, it } from "vitest";

await import("../builder_canvas_shared.js");
await import("../builder_canvas_state.js");

const t = window.braidplaneBuilderCanvasState;

describe("builder canvas state helpers", () => {
    it("comparableBuilderSnapshot_SortsGraphAndSpecCollections", () => {
        const snapshot = t.comparableBuilderSnapshot({
            graph: {
                bindings: [
                    { from_step: "b", from_port: "rows", to_step: "c", to_port: "rows" },
                    { from_step: "a", from_port: "rows", to_step: "c", to_port: "rows" },
                ],
                edges: [
                    { from_step: "b", to_step: "c" },
                    { from_step: "a", to_step: "c" },
                ],
                nodes: [{ step_slug: "c" }, { step_slug: "a" }],
            },
            pipeline_spec: {
                steps: [{ slug: "c" }, { slug: "a" }],
            },
            context: {
                expected_spec_hash: "ignored",
            },
        });

        expect(JSON.parse(snapshot)).toEqual({
            graph: {
                bindings: [
                    { from_port: "rows", from_step: "a", to_port: "rows", to_step: "c" },
                    { from_port: "rows", from_step: "b", to_port: "rows", to_step: "c" },
                ],
                edges: [
                    { from_step: "a", to_step: "c" },
                    { from_step: "b", to_step: "c" },
                ],
                nodes: [{ step_slug: "a" }, { step_slug: "c" }],
            },
            pipeline_spec: {
                steps: [{ slug: "a" }, { slug: "c" }],
            },
        });
    });

    it("normalizeStateForGridColumns_AddsCurrentGridWidthOnlyWhenNodesExist", () => {
        expect(t.normalizeStateForGridColumns({ nodes: [] })).toEqual({ nodes: [], gridColumns: 24 });
        expect(t.normalizeStateForGridColumns({ graph: {} })).toEqual({ graph: {} });
    });
});
