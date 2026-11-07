import { beforeEach, describe, expect, it, vi } from "vitest";

const observers = [];
vi.stubGlobal(
    "ResizeObserver",
    class {
        constructor(callback) {
            this.callback = callback;
            observers.push(this);
        }
        observe() {}
        disconnect = vi.fn();
    },
);
await import("../run-lineage.js");
let sequence = 0;

function mount({ width = 1200, height = 1600, key = `run-${++sequence}|version` } = {}) {
    document.body.dispatchEvent(
        new CustomEvent("htmx:beforeSwap", { bubbles: true, detail: { target: document.body } }),
    );
    document.body.innerHTML = `<div data-lineage-view="${key}">
        <button data-lineage-action="out"></button><output data-lineage-zoom></output>
        <button data-lineage-action="in"></button><button data-lineage-action="fit"></button>
        <button data-lineage-action="reset"></button>
        <div data-lineage-viewport><div data-lineage-bounds>
        <div data-lineage-canvas style="width:${width}px;height:${height}px"><a data-entity-focus href="#definition-inspector">Step</a></div>
        </div></div></div>`;
    const viewport = document.querySelector("[data-lineage-viewport]");
    Object.defineProperties(viewport, {
        clientWidth: { configurable: true, value: 600 },
        clientHeight: { value: 500 },
    });
    viewport.getBoundingClientRect = () => ({ left: 10, top: 20 });
    viewport.setPointerCapture = vi.fn();
    document.dispatchEvent(new Event("htmx:afterSwap"));
    return { viewport, canvas: document.querySelector("[data-lineage-canvas]"), observer: observers.at(-1), key };
}
const action = (name) => document.querySelector(`[data-lineage-action="${name}"]`).click();
const percent = () => document.querySelector("output").textContent;
beforeEach(() => {
    document.body.innerHTML = "";
});

describe("lineage viewport", () => {
    it.each([
        { width: 1200, height: 1600, zoom: "31%" },
        { width: 2400, height: 300, zoom: "25%" },
        { width: 200, height: 200, zoom: "100%" },
    ])("fits the complete $width × $height canvas", ({ width, height, zoom }) => {
        const { viewport, canvas } = mount({ width, height });
        expect(percent()).toBe(zoom);
        expect(canvas.style.transform).toContain("scale(");
        expect(parseFloat(document.querySelector("[data-lineage-bounds]").style.width)).toBeLessThanOrEqual(
            viewport.clientWidth,
        );
        expect(parseFloat(document.querySelector("[data-lineage-bounds]").style.height)).toBeLessThanOrEqual(
            viewport.clientHeight,
        );
        expect(viewport.scrollLeft).toBe(0);
        expect(viewport.scrollTop).toBe(0);
    });

    it("anchors native-size reset at the viewport center and enforces zoom limits", () => {
        const { viewport } = mount({ width: 1200, height: 1000 });
        action("reset");
        expect(percent()).toBe("100%");
        expect(viewport.scrollLeft).toBe(300);
        expect(viewport.scrollTop).toBe(250);
        for (let i = 0; i < 40; i++) action("in");
        expect(percent()).toBe("250%");
        expect(document.querySelector('[data-lineage-action="in"]').disabled).toBe(true);
        for (let i = 0; i < 40; i++) action("out");
        expect(percent()).toBe("10%");
        expect(document.querySelector('[data-lineage-action="out"]').disabled).toBe(true);
        action("fit");
        expect(percent()).toBe("50%");
        expect(viewport.scrollLeft).toBe(0);
        expect(viewport.scrollTop).toBe(0);
    });

    it("fits below 10% when necessary and never clips a very tall graph", () => {
        mount({ height: 20000 });
        expect(percent()).toBe("3%");
        action("out");
        expect(percent()).toBe("3%");
    });

    it("refits on resize only in Fit mode", () => {
        const { viewport, observer } = mount({ width: 1200, height: 300 });
        Object.defineProperty(viewport, "clientWidth", { value: 300 });
        observer.callback();
        expect(percent()).toBe("25%");
        action("reset");
        viewport.scrollLeft = 120;
        Object.defineProperty(viewport, "clientWidth", { value: 400 });
        observer.callback();
        expect(percent()).toBe("100%");
        expect(viewport.scrollLeft).toBe(120);
    });

    it("preserves view on same-run swaps, fits a new version and initializes once", () => {
        const { viewport, key, observer } = mount();
        action("reset");
        viewport.scrollLeft = 175;
        viewport.scrollTop = 250;
        document.dispatchEvent(new Event("htmx:afterSwap"));
        action("in");
        expect(percent()).toBe("110%");
        document.querySelector("a").click();
        expect(sessionStorage.getItem("braidplane.lineage.view")).not.toBeNull();
        const replacement = mount({ key });
        expect(sessionStorage.getItem("braidplane.lineage.view")).toBeNull();
        expect(observer.disconnect).toHaveBeenCalledOnce();
        expect(percent()).toBe("110%");
        expect(replacement.viewport.scrollTop).toBeGreaterThan(250);
        mount({ key: `${key}-new-version` });
        expect(percent()).toBe("31%");
    });

    it("retains the graph view while visiting a section without a graph", () => {
        const { viewport, key } = mount();
        action("reset");
        viewport.scrollLeft = 180;
        viewport.scrollTop = 240;
        window.dispatchEvent(new Event("pagehide"));
        const saved = JSON.parse(sessionStorage.getItem("braidplane.lineage.view"));
        expect(saved).toMatchObject({ key, scale: 1, left: 180, top: 240 });
        document.body.innerHTML = "<section>Executed definition</section>";
        document.dispatchEvent(new Event("htmx:afterSwap"));
        window.dispatchEvent(new Event("pagehide"));
        expect(JSON.parse(sessionStorage.getItem("braidplane.lineage.view"))).toEqual(saved);
        const restored = mount({ key });
        expect(percent()).toBe("100%");
        expect(restored.viewport.scrollLeft).toBe(180);
        expect(restored.viewport.scrollTop).toBe(240);
    });

    it("supports pointer panning, keyboard zoom and pointer-anchored wheel zoom", () => {
        const { viewport } = mount({ width: 1200, height: 1000 });
        action("reset");
        viewport.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
        expect(viewport.scrollTop).toBe(290);
        viewport.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }));
        const pointer = (type, options) =>
            viewport.dispatchEvent(Object.assign(new Event(type, { bubbles: true, cancelable: true }), options));
        pointer("pointerdown", { button: 0, clientX: 200, clientY: 200, pointerId: 1 });
        pointer("pointermove", { clientX: 150, clientY: 100 });
        expect(viewport.scrollLeft).toBe(350);
        expect(viewport.scrollTop).toBe(350);
        pointer("lostpointercapture", {});
        pointer("pointermove", { clientX: 100, clientY: 100 });
        expect(viewport.scrollLeft).toBe(350);
        viewport.dispatchEvent(new WheelEvent("wheel", { ctrlKey: true, deltaY: -25, clientX: 110, clientY: 120 }));
        expect(percent()).toBe("110%");
        expect(viewport.scrollLeft).toBeCloseTo(395);
        expect(viewport.scrollTop).toBeCloseTo(395);
        viewport.dispatchEvent(new KeyboardEvent("keydown", { key: "0", bubbles: true }));
        expect(percent()).toBe("50%");
        viewport.dispatchEvent(new KeyboardEvent("keydown", { key: "+", bubbles: true }));
        expect(percent()).toBe("60%");
    });
});
