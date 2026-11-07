import { createRequire } from "node:module";
import { describe, expect, it, vi } from "vitest";

const { withHTMXSettle } = createRequire(import.meta.url)("../../../e2e/support/htmx-settle.cjs");

// Run the helper's DOM callbacks in jsdom; browser scheduling belongs to the journeys.
function pageAdapter() {
    return {
        evaluate: async (callback, argument) => callback(argument),
        waitForFunction: vi.fn(async (callback, argument) => {
            if (!callback(argument)) throw new Error("settle event missing");
        }),
    };
}

function emit(type, responseURL, status) {
    document.dispatchEvent(new CustomEvent(type, { detail: { xhr: { responseURL, status } } }));
}

function expectClean() {
    expect(Object.keys(window).filter((key) => key.startsWith("e2e-htmx-"))).toEqual([]);
}

describe("HTMX settlement", () => {
    it("observes immediate matching events and ignores unrelated paths and parameters", async () => {
        const page = pageAdapter();
        await withHTMXSettle(page, { pathname: "/selectors", params: { kind: "pipeline" } }, async () => {
            emit("htmx:responseError", "http://localhost/unrelated?kind=pipeline", 500);
            emit("htmx:responseError", "http://localhost/selectors?kind=query", 500);
            emit("htmx:afterSettle", "http://localhost/selectors?kind=pipeline", 200);
        });
        expectClean();
    });

    it("reports response errors and cleans up listeners", async () => {
        await expect(
            withHTMXSettle(pageAdapter(), { pathname: "/selectors" }, async () => {
                emit("htmx:responseError", "http://localhost/selectors", 500);
            }),
        ).rejects.toThrow("HTMX /selectors did not settle successfully");
        expectClean();
    });

    it("bounds a missing event and cleans up after timeout or action failure", async () => {
        const page = pageAdapter();
        await expect(withHTMXSettle(page, { pathname: "/selectors" }, async () => {})).rejects.toThrow(
            "HTMX /selectors did not settle successfully",
        );
        expect(page.waitForFunction).toHaveBeenCalledWith(expect.any(Function), expect.any(String), { timeout: 5_000 });
        expectClean();
        await expect(
            withHTMXSettle(page, { pathname: "/selectors" }, async () => {
                throw new Error("action failed");
            }),
        ).rejects.toThrow("HTMX /selectors did not settle successfully");
        expectClean();
    });
});
