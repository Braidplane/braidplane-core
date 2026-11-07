import { describe, expect, it } from "vitest";

await import("../builder_canvas_serialization.js");

const t = window.braidplaneBuilderCanvasSerialization;

describe("builder canvas serialization helpers", () => {
    it("normalizeBindings_AcceptsLegacyAndCanonicalShapes", () => {
        expect(
            t.normalizeBindings([
                { from_step: "fetch-users", from_port: "rows", to_step: "write-users", to_port: "payload" },
                { sourceNodeSlug: "fetch-users", sourceField: "rows", nodeSlug: "write-users", field: "payload" },
                { fromNode: "", fromPort: "", toNode: "", toPort: "" },
            ]),
        ).toEqual([{ fromNode: "fetch-users", fromPort: "rows", toNode: "write-users", toPort: "payload" }]);
    });

    it("replaceBindingsForTarget_UsesSingleBindingByDefault", () => {
        expect(
            t.replaceBindingsForTarget(
                [{ fromNode: "old", fromPort: "rows", toNode: "sink", toPort: "payload" }],
                "sink",
                "payload",
                [
                    { fromNode: "first", fromPort: "rows", toNode: "sink", toPort: "payload" },
                    { fromNode: "second", fromPort: "rows", toNode: "sink", toPort: "payload" },
                ],
                false,
            ),
        ).toEqual([{ fromNode: "first", fromPort: "rows", toNode: "sink", toPort: "payload" }]);
    });

    it("validateBindings_ReportsMissingDirectEdge", () => {
        expect(
            t.validateBindings(
                [
                    { slug: "fetch-users", kind: "query" },
                    { slug: "write-users", kind: "sink" },
                ],
                [],
                [{ fromNode: "fetch-users", fromPort: "rows", toNode: "write-users", toPort: "payload" }],
            ),
        ).toEqual([
            {
                message: "write-users: binding source fetch-users must have a direct incoming edge",
                nodeSlug: "write-users",
            },
        ]);
    });
});
