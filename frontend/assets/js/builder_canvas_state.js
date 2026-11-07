(() => {
    const shared = window.braidplaneBuilderCanvasShared;
    if (!shared) {
        throw new Error("builder canvas shared helpers must load before builder_canvas_state.js");
    }

    const gridColumnCount = 24;
    const { sortKeysStable, stableJSONStringify } = shared;

    const comparableBuilderState = (state) => {
        if (!state || typeof state !== "object" || Array.isArray(state)) {
            return state;
        }

        const pipelineSpec =
            state.pipeline_spec && typeof state.pipeline_spec === "object" && !Array.isArray(state.pipeline_spec)
                ? { ...state.pipeline_spec }
                : {};
        const graph =
            state.graph && typeof state.graph === "object" && !Array.isArray(state.graph) ? { ...state.graph } : {};

        if (!Array.isArray(pipelineSpec.steps)) {
            pipelineSpec.steps = [];
        }
        if (!Array.isArray(graph.nodes)) {
            graph.nodes = [];
        }
        if (!Array.isArray(graph.edges)) {
            graph.edges = [];
        }
        if (!Array.isArray(graph.bindings)) {
            graph.bindings = [];
        }

        pipelineSpec.steps = pipelineSpec.steps.slice().sort((left, right) => {
            const leftSlug = typeof left?.slug === "string" ? left.slug : "";
            const rightSlug = typeof right?.slug === "string" ? right.slug : "";
            return leftSlug.localeCompare(rightSlug);
        });
        graph.nodes = graph.nodes.slice().sort((left, right) => {
            const leftSlug = typeof left?.step_slug === "string" ? left.step_slug : "";
            const rightSlug = typeof right?.step_slug === "string" ? right.step_slug : "";
            return leftSlug.localeCompare(rightSlug);
        });
        graph.edges = graph.edges.slice().sort((left, right) => {
            const leftFrom = typeof left?.from_step === "string" ? left.from_step : "";
            const rightFrom = typeof right?.from_step === "string" ? right.from_step : "";
            if (leftFrom !== rightFrom) {
                return leftFrom.localeCompare(rightFrom);
            }
            const leftTo = typeof left?.to_step === "string" ? left.to_step : "";
            const rightTo = typeof right?.to_step === "string" ? right.to_step : "";
            return leftTo.localeCompare(rightTo);
        });
        graph.bindings = graph.bindings.slice().sort((left, right) => {
            const leftFrom = typeof left?.from_step === "string" ? left.from_step : "";
            const rightFrom = typeof right?.from_step === "string" ? right.from_step : "";
            if (leftFrom !== rightFrom) {
                return leftFrom.localeCompare(rightFrom);
            }
            const leftFromPort = typeof left?.from_port === "string" ? left.from_port : "";
            const rightFromPort = typeof right?.from_port === "string" ? right.from_port : "";
            if (leftFromPort !== rightFromPort) {
                return leftFromPort.localeCompare(rightFromPort);
            }
            const leftTo = typeof left?.to_step === "string" ? left.to_step : "";
            const rightTo = typeof right?.to_step === "string" ? right.to_step : "";
            if (leftTo !== rightTo) {
                return leftTo.localeCompare(rightTo);
            }
            const leftToPort = typeof left?.to_port === "string" ? left.to_port : "";
            const rightToPort = typeof right?.to_port === "string" ? right.to_port : "";
            return leftToPort.localeCompare(rightToPort);
        });

        return sortKeysStable({
            pipeline_spec: pipelineSpec,
            graph,
        });
    };

    const comparableBuilderSnapshot = (state) => stableJSONStringify(comparableBuilderState(state));

    const normalizeStateForGridColumns = (state) => {
        if (!state || !Array.isArray(state.nodes)) {
            return state;
        }

        return {
            ...state,
            gridColumns: gridColumnCount,
        };
    };

    window.braidplaneBuilderCanvasState = {
        comparableBuilderState,
        comparableBuilderSnapshot,
        normalizeStateForGridColumns,
        storageKeyFor: shared.storageKeyFor,
        savedPipelineVersionContext: shared.savedPipelineVersionContext,
    };
})();
