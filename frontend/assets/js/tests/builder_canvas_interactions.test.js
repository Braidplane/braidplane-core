import { beforeEach, describe, expect, it, vi } from "vitest";

await import("../builder_canvas_interactions.js");

const t = window.braidplaneBuilderCanvasInteractions;

beforeEach(() => {
    document.body.innerHTML = "";
    vi.restoreAllMocks();
});

describe("builder canvas interaction helpers", () => {
    it("isTypingContext_DetectsNestedFormControls", () => {
        document.body.innerHTML =
            '<label><input id="name"/></label><div id="editable" contenteditable="true"><span>Text</span></div><div id="plain"></div>';

        expect(t.isTypingContext(document.getElementById("name"))).toBe(true);
        expect(t.isTypingContext(document.querySelector("#editable span"))).toBe(true);
        expect(t.isTypingContext(document.getElementById("plain"))).toBe(false);
    });

    it("scheduleRenderFactory_CancelsPendingFrameBeforeSchedulingNext", () => {
        const render = vi.fn();
        const cancel = vi.spyOn(window, "cancelAnimationFrame").mockImplementation(() => {});
        const callbacks = [];
        vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
            callbacks.push(callback);
            return callbacks.length;
        });

        const scheduleRender = t.scheduleRenderFactory(render);
        scheduleRender();
        scheduleRender();
        callbacks[1]();

        expect(cancel).toHaveBeenCalledWith(1);
        expect(render).toHaveBeenCalledTimes(1);
    });
});
