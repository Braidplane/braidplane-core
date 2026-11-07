(() => {
    let active = null;
    let saved = null;
    const storageKey = "braidplane.lineage.view";
    try {
        saved = JSON.parse(sessionStorage.getItem(storageKey));
    } catch (_) {
        // View preservation still works across HTMX swaps when storage is unavailable.
    }

    function init() {
        const root = document.querySelector("[data-lineage-view]");
        if (active?.root === root) return;
        if (active) {
            if (active.root.isConnected) saved = active.read();
            active.observer.disconnect();
            active = null;
        }
        if (!root) return;
        const viewport = root.querySelector("[data-lineage-viewport]");
        const bounds = root.querySelector("[data-lineage-bounds]");
        const canvas = root.querySelector("[data-lineage-canvas]");
        const width = parseFloat(canvas.style.width);
        const height = parseFloat(canvas.style.height);
        const key = root.dataset.lineageView;
        try {
            sessionStorage.removeItem(storageKey);
        } catch (_) {
            // A completed HTMX selection no longer needs the full-navigation fallback.
        }
        let scale = 1;
        let fitting = true;
        let offsetX = 0;
        let offsetY = 0;
        const read = () => ({ key, scale, fitting, left: viewport.scrollLeft, top: viewport.scrollTop });
        const fitScale = () => Math.min(1, viewport.clientWidth / width, viewport.clientHeight / height);
        const minScale = () => Math.min(0.1, fitScale());

        function render() {
            offsetX = Math.max(0, (viewport.clientWidth - width * scale) / 2);
            offsetY = Math.max(0, (viewport.clientHeight - height * scale) / 2);
            bounds.style.width = `${Math.max(viewport.clientWidth, width * scale)}px`;
            bounds.style.height = `${Math.max(viewport.clientHeight, height * scale)}px`;
            canvas.style.transform = `translate(${offsetX}px, ${offsetY}px) scale(${scale})`;
            root.querySelector("[data-lineage-zoom]").textContent = `${Math.round(scale * 100)}%`;
            root.querySelector('[data-lineage-action="out"]').disabled = scale <= minScale();
            root.querySelector('[data-lineage-action="in"]').disabled = scale >= 2.5;
            root.querySelector('[data-lineage-action="fit"]').setAttribute("aria-pressed", String(fitting));
        }

        function fitGraph() {
            if (!viewport.clientWidth || !viewport.clientHeight) return;
            fitting = true;
            scale = fitScale();
            render();
            viewport.scrollLeft = 0;
            viewport.scrollTop = 0;
        }

        function zoom(next, x = viewport.clientWidth / 2, y = viewport.clientHeight / 2) {
            const localX = (viewport.scrollLeft + x - offsetX) / scale;
            const localY = (viewport.scrollTop + y - offsetY) / scale;
            fitting = false;
            scale = Math.max(minScale(), Math.min(2.5, next));
            render();
            viewport.scrollLeft = Math.max(0, localX * scale + offsetX - x);
            viewport.scrollTop = Math.max(0, localY * scale + offsetY - y);
        }

        root.addEventListener("click", (event) => {
            const action = event.target.closest("[data-lineage-action]")?.dataset.lineageAction;
            if (action === "fit") fitGraph();
            if (action === "reset") zoom(1);
            if (action === "in") zoom(scale + 0.1);
            if (action === "out") zoom(scale - 0.1);
            if (event.target.closest("a[data-entity-focus]")) {
                try {
                    saved = read();
                    sessionStorage.setItem(storageKey, JSON.stringify(saved));
                } catch (_) {
                    // Navigation does not depend on browser storage.
                }
            }
        });
        viewport.addEventListener("keydown", (event) => {
            const pan = { ArrowLeft: [-40, 0], ArrowRight: [40, 0], ArrowUp: [0, -40], ArrowDown: [0, 40] }[event.key];
            if (pan) {
                event.preventDefault();
                fitting = false;
                viewport.scrollLeft += pan[0];
                viewport.scrollTop += pan[1];
                render();
            } else if (event.key === "+" || event.key === "=" || event.key === "-") {
                event.preventDefault();
                zoom(scale + (event.key === "-" ? -0.1 : 0.1));
            } else if (event.key === "0") {
                event.preventDefault();
                fitGraph();
            }
        });
        viewport.addEventListener(
            "wheel",
            (event) => {
                if (!event.ctrlKey && !event.metaKey) return;
                event.preventDefault();
                const rect = viewport.getBoundingClientRect();
                const delta = Math.max(0.012, Math.min(0.14, Math.abs(event.deltaY) * 0.004));
                zoom(scale + (event.deltaY > 0 ? -delta : delta), event.clientX - rect.left, event.clientY - rect.top);
            },
            { passive: false },
        );

        let drag = null;
        viewport.addEventListener("pointerdown", (event) => {
            if (event.button !== 0 || event.target.closest("a")) return;
            fitting = false;
            drag = { x: event.clientX, y: event.clientY, left: viewport.scrollLeft, top: viewport.scrollTop };
            viewport.setPointerCapture(event.pointerId);
            viewport.focus({ preventScroll: true });
            event.preventDefault();
            render();
        });
        viewport.addEventListener("pointermove", (event) => {
            if (!drag) return;
            viewport.scrollLeft = drag.left + drag.x - event.clientX;
            viewport.scrollTop = drag.top + drag.y - event.clientY;
        });
        viewport.addEventListener("lostpointercapture", () => {
            drag = null;
        });

        const observer = new ResizeObserver(() => {
            if (fitting) fitGraph();
        });
        observer.observe(viewport);
        active = { root, observer, read };
        if (saved?.key === key && Number.isFinite(saved.scale) && saved.scale > 0 && !saved.fitting) {
            fitting = false;
            scale = saved.scale;
            render();
            viewport.scrollLeft = saved.left;
            viewport.scrollTop = saved.top;
        } else {
            fitGraph();
        }
    }

    let focused = null;
    let detailsState = new Map();
    function beforeRunSwap(event) {
        const target = event.detail?.target || event.target;
        if (!(target instanceof HTMLElement)) return;
        if (active && target.contains(active.root)) saved = active.read();
        if (target.id !== "run-live-content") return;
        const element = document.activeElement;
        focused = target.contains(element)
            ? element.id
                ? { id: element.id }
                : element.matches("[data-lineage-viewport]")
                  ? { viewport: true }
                  : element.matches("a[href]")
                    ? { href: element.getAttribute("href") }
                    : null
            : null;
        detailsState = new Map(
            Array.from(target.querySelectorAll("details[data-run-details-key]"), (element) => [
                element.dataset.runDetailsKey,
                element.open,
            ]),
        );
    }
    document.addEventListener("htmx:beforeSwap", beforeRunSwap);
    document.addEventListener("htmx:oobBeforeSwap", beforeRunSwap);
    document.addEventListener("htmx:afterSettle", () => {
        if (focused) {
            const element = focused.id
                ? document.getElementById(focused.id)
                : focused.viewport
                  ? document.querySelector("[data-lineage-viewport]")
                  : Array.from(document.querySelectorAll("#run-live-content a[href]")).find(
                        (element) => element.getAttribute("href") === focused.href,
                    );
            element?.focus({ preventScroll: true });
            focused = null;
        }
        for (const element of document.querySelectorAll("#run-live-content details[data-run-details-key]")) {
            if (detailsState.has(element.dataset.runDetailsKey))
                element.open = detailsState.get(element.dataset.runDetailsKey);
        }
        detailsState = new Map();
    });
    function progressDisconnected(event) {
        if (!event.detail?.elt?.matches?.("[data-run-progress]")) return;
        const status = document.querySelector("[data-run-progress-connection]");
        if (status)
            status.textContent =
                "Progress disconnected; showing the last durable observation. Reconnecting automatically.";
    }
    document.addEventListener("htmx:responseError", progressDisconnected);
    document.addEventListener("htmx:sendError", progressDisconnected);
    document.addEventListener("htmx:timeout", progressDisconnected);

    window.addEventListener("pagehide", () => {
        if (!active) return;
        try {
            saved = active.read();
            sessionStorage.setItem(storageKey, JSON.stringify(saved));
        } catch (_) {
            // Graph navigation remains available when browser storage is disabled.
        }
    });
    document.addEventListener("DOMContentLoaded", init);
    document.addEventListener("htmx:afterSwap", init);
})();
