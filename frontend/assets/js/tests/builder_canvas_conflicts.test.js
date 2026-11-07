import { describe, expect, it } from "vitest";

await import("../builder_canvas_shared.js");
await import("../builder_canvas_state.js");
await import("../builder_canvas_conflicts.js");

const t = window.braidplaneBuilderCanvasConflicts;

describe("builder canvas conflict helpers", () => {
    it("buildConflictDiffJSONForState_UsesComparableBuilderOrdering", () => {
        expect(
            t.buildConflictDiffJSONForState({
                pipeline_spec: { steps: [{ slug: "sink" }, { slug: "fetch" }] },
                graph: {
                    nodes: [{ step_slug: "sink" }, { step_slug: "fetch" }],
                    edges: [{ from_step: "fetch", to_step: "sink" }],
                    bindings: [],
                },
                context: { expected_spec_hash: "ignored" },
            }),
        ).toBe(
            JSON.stringify(
                {
                    graph: {
                        bindings: [],
                        edges: [{ from_step: "fetch", to_step: "sink" }],
                        nodes: [{ step_slug: "fetch" }, { step_slug: "sink" }],
                    },
                    pipeline_spec: { steps: [{ slug: "fetch" }, { slug: "sink" }] },
                },
                null,
                2,
            ),
        );
    });
});
