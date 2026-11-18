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
    it("keeps graph shortcuts from handling keys on a dependency link", () => {
        const { viewport, canvas } = mount();
        const dependency = document.createElement("a");
        dependency.href = "#dependency";
        canvas.appendChild(dependency);
        action("reset");
        for (const key of ["+", "=", "-", "0", "ArrowDown"]) {
            const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
            dependency.dispatchEvent(event);
            expect(event.defaultPrevented, key).toBe(false);
            expect(percent()).toBe("100%");
        }
        const fit = new KeyboardEvent("keydown", { key: "0", bubbles: true, cancelable: true });
        viewport.dispatchEvent(fit);
        expect(fit.defaultPrevented).toBe(true);
        expect(percent()).toBe("31%");
    });

    it("leaves browser zoom shortcuts to the browser", () => {
        const { viewport } = mount();
        const initialZoom = percent();
        for (const modifier of ["ctrlKey", "metaKey"]) {
            for (const key of ["+", "=", "-", "0"]) {
                const event = new KeyboardEvent("keydown", { key, [modifier]: true, bubbles: true, cancelable: true });
                viewport.dispatchEvent(event);
                expect(event.defaultPrevented, `${modifier} ${key}`).toBe(false);
                expect(percent()).toBe(initialZoom);
            }
        }
    });

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
        pointer("pointermove", { pointerId: 1, clientX: 150, clientY: 100 });
        expect(viewport.scrollLeft).toBe(350);
        expect(viewport.scrollTop).toBe(350);
        pointer("pointerup", { pointerId: 1 });
        pointer("lostpointercapture", { pointerId: 1 });
        pointer("pointermove", { pointerId: 1, clientX: 100, clientY: 100 });
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

    it("keeps Fit active when pressing and releasing without a pan", () => {
        const { viewport } = mount();
        const fit = document.querySelector('[data-lineage-action="fit"]');
        viewport.dispatchEvent(new PointerEvent("pointerdown", { button: 0, pointerId: 1 }));
        expect(fit.getAttribute("aria-pressed")).toBe("true");
        viewport.dispatchEvent(new PointerEvent("pointerup", { pointerId: 1 }));
        expect(fit.getAttribute("aria-pressed")).toBe("true");
    });

    it("preserves the committed view if a live swap interrupts a pan", () => {
        const { viewport, key } = mount({ width: 1200, height: 1000 });
        action("reset");
        viewport.dispatchEvent(new PointerEvent("pointerdown", { button: 0, pointerId: 1, clientY: 200 }));
        viewport.dispatchEvent(new PointerEvent("pointermove", { pointerId: 1, clientY: 100 }));
        expect(viewport.scrollTop).toBe(350);
        const updated = mount({ width: 1200, height: 1000, key });
        expect(updated.viewport.scrollTop).toBe(250);
    });

    it.each(["pointercancel", "lostpointercapture", "Escape"])("restores the pre-pan position on %s", (cancel) => {
        const { viewport } = mount({ width: 1200, height: 1000 });
        viewport.hasPointerCapture = () => true;
        viewport.releasePointerCapture = vi.fn();
        action("reset");
        const pointer = (type, pointerId, clientX, clientY) =>
            viewport.dispatchEvent(new PointerEvent(type, { button: 0, pointerId, clientX, clientY }));
        pointer("pointerdown", 1, 200, 200);
        pointer("pointermove", 2, 150, 100);
        expect(viewport.scrollLeft).toBe(300);
        expect(viewport.scrollTop).toBe(250);
        pointer("pointermove", 1, 150, 100);
        expect(viewport.scrollLeft).toBe(350);
        expect(viewport.scrollTop).toBe(350);

        if (cancel === "Escape") {
            viewport.dispatchEvent(new KeyboardEvent("keydown", { key: cancel, cancelable: true }));
            expect(viewport.releasePointerCapture).toHaveBeenCalledWith(1);
        } else {
            pointer(cancel, 1);
        }
        pointer("pointermove", 1, 50, 50);
        expect(viewport.scrollLeft).toBe(300);
        expect(viewport.scrollTop).toBe(250);
    });
});

describe("live update controls", () => {
    it.each(["run-progress-live", "run-completion", "connected-runs"])(
        "pauses requests and late swaps for %s",
        (id) => {
            document.body.innerHTML = `<div data-run-updates="run-pause" hidden>
            <button data-run-updates-toggle aria-pressed="false"></button><span data-run-updates-state></span>
            </div><section id="${id}" hx-get="/run"><span data-run-progress-connection>Last observation</span></section>`;
            const controls = document.querySelector("[data-run-updates]");
            const button = controls.querySelector("button");
            const poll = document.getElementById(id);
            poll.toggleAttribute("data-run-progress", id === "run-progress-live");
            document.dispatchEvent(new Event("htmx:afterSwap"));
            expect(controls.hidden).toBe(false);
            expect(button.textContent).toBe("Pause live updates");
            button.click();
            expect(button.getAttribute("aria-pressed")).toBe("true");
            expect(controls.textContent).toContain("Showing the last observation");
            for (const [type, detail] of [
                ["htmx:beforeRequest", { elt: poll }],
                ["htmx:beforeSwap", { target: poll }],
            ]) {
                const event = new CustomEvent(type, { bubbles: true, cancelable: true, detail });
                poll.dispatchEvent(event);
                expect(event.defaultPrevented).toBe(true);
            }
            const manual = new CustomEvent("htmx:beforeRequest", {
                bubbles: true,
                cancelable: true,
                detail: { elt: button },
            });
            button.dispatchEvent(manual);
            expect(manual.defaultPrevented).toBe(false);
            poll.dispatchEvent(new CustomEvent("htmx:responseError", { bubbles: true, detail: { elt: poll } }));
            expect(poll.textContent).toBe("Last observation");
            document.dispatchEvent(new Event("htmx:afterSwap"));
            expect(button.textContent).toBe("Resume live updates");
            button.click();
            const resumed = new CustomEvent("htmx:beforeRequest", {
                bubbles: true,
                cancelable: true,
                detail: { elt: poll },
            });
            poll.dispatchEvent(resumed);
            expect(resumed.defaultPrevented).toBe(false);
            poll.removeAttribute("hx-get");
            document.dispatchEvent(new Event("htmx:afterSwap"));
            expect(controls.hidden).toBe(false);
            expect(button.getAttribute("aria-disabled")).toBe("true");
            expect(controls.textContent).toContain("Automatic updates finished");
            button.click();
            expect(button.getAttribute("aria-pressed")).toBe("false");
        },
    );
});
