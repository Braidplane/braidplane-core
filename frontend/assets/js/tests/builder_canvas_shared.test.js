import { describe, expect, it } from "vitest";

await import("../builder_canvas_shared.js");

const t = window.braidplaneBuilderCanvasShared;

describe("builder canvas shared helpers", () => {
    it("parseOptionalVersionToken_NormalizesEmptyValues", () => {
        expect(t.parseOptionalVersionToken("")).toBeNull();
        expect(t.parseOptionalVersionToken("  ")).toBeNull();
        expect(t.parseOptionalVersionToken(null)).toBeNull();
        expect(t.parseOptionalVersionToken(42)).toBe("42");
    });

    it("stripConflictDiffVersionMetadata_RemovesVersionOnlyFields", () => {
        expect(
            t.stripConflictDiffVersionMetadata({
                slug: "sync-users",
                expected_version_id: 1,
                current_version_id: 2,
                draft_based_on_version_id: 3,
                expected_spec_hash: "a",
                current_version_spec_hash: "b",
                draft_based_on_spec_hash: "c",
            }),
        ).toEqual({ slug: "sync-users" });
    });

    it("stableJSONStringify_SortsNestedKeys", () => {
        expect(t.stableJSONStringify({ z: 1, a: { y: 2, b: 3 } })).toBe('{"a":{"b":3,"y":2},"z":1}');
    });

    it("saveResponseProblem_RejectsLoginRedirectsAndNonJSON", () => {
        const response = {
            redirected: true,
            url: "/auth/login",
            headers: { get: () => "text/html" },
        };

        expect(t.saveResponseProblem(response)).toBe(
            "Saved locally, but your session expired. Sign in again and retry.",
        );
    });

    it("runStatusMessage_RefusesUnknownOrDeniedExecutePermission", () => {
        expect(t.runStatusMessage("users-sync", true, false, "prod")).toBe(
            "You don't have permission to execute pipelines.",
        );
        expect(t.runStatusMessage("users-sync", true, false, "prod", false)).toBe(
            "You don't have permission to execute pipelines.",
        );
    });

    it("runStatusMessage_RequiresAndNamesRuntimeTarget", () => {
        expect(t.runStatusMessage("users-sync", true, true, "prod", true)).toContain("merge, sync or discard");
        expect(t.runStatusMessage("users-sync", true, false, "", true)).toBe(
            "Run is unavailable until a project runtime target is selected.",
        );
        expect(t.runStatusMessage("users-sync", true, false, "prod", true)).toBe(
            'Run will execute pipeline "users-sync" in runtime target "prod".',
        );
    });
});
