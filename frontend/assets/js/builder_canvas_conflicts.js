(() => {
    const state = window.braidplaneBuilderCanvasState;
    if (!state) {
        throw new Error("builder canvas state helpers must load before builder_canvas_conflicts.js");
    }

    const normalizeStateForConflictDiff = (value) => state.comparableBuilderState(value);

    const buildConflictDiffJSONForState = (value) => {
        return JSON.stringify(normalizeStateForConflictDiff(value), null, 2);
    };

    window.braidplaneBuilderCanvasConflicts = {
        normalizeStateForConflictDiff,
        buildConflictDiffJSONForState,
    };
})();
