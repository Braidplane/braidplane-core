(() => {
    const scheduleRenderFactory = (render) => {
        let frameId = 0;
        return () => {
            if (frameId !== 0) {
                window.cancelAnimationFrame(frameId);
            }
            frameId = window.requestAnimationFrame(() => {
                frameId = 0;
                render();
            });
        };
    };

    const isTypingContext = (eventTarget) => {
        if (!(eventTarget instanceof HTMLElement)) {
            return false;
        }
        if (eventTarget.isContentEditable) {
            return true;
        }
        return eventTarget.closest('input, textarea, select, [contenteditable="true"]') instanceof HTMLElement;
    };

    window.braidplaneBuilderCanvasInteractions = {
        scheduleRenderFactory,
        isTypingContext,
    };
})();
