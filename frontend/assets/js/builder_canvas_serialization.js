(() => {
    const KIND_QUERY = "query";
    const KIND_TRANSFORM = "transform";
    const KIND_SINK = "sink";
    const PORT_ROWS = "rows";

    const isStepKind = (kind) => kind === "input" || kind === KIND_QUERY || kind === KIND_TRANSFORM;

    const inputPortsForKind = (kind) => {
        if (kind === KIND_TRANSFORM) {
            return [PORT_ROWS];
        }
        if (kind === KIND_SINK) {
            return ["payload"];
        }
        return [];
    };

    const outputPortsForKind = (kind) => {
        if (isStepKind(kind)) {
            return [PORT_ROWS];
        }
        return [];
    };

    const normalizeBindings = (value) => {
        if (!Array.isArray(value)) {
            return [];
        }

        const seenBindings = new Set();

        return value
            .map((binding) => ({
                fromNode:
                    typeof binding?.fromNode === "string"
                        ? binding.fromNode
                        : typeof binding?.from_node === "string"
                          ? binding.from_node
                          : typeof binding?.from_step === "string"
                            ? binding.from_step
                            : typeof binding?.sourceNodeSlug === "string"
                              ? binding.sourceNodeSlug
                              : "",
                fromPort:
                    typeof binding?.fromPort === "string"
                        ? binding.fromPort
                        : typeof binding?.from_port === "string"
                          ? binding.from_port
                          : typeof binding?.sourceField === "string"
                            ? binding.sourceField
                            : "",
                toNode:
                    typeof binding?.toNode === "string"
                        ? binding.toNode
                        : typeof binding?.to_node === "string"
                          ? binding.to_node
                          : typeof binding?.to_step === "string"
                            ? binding.to_step
                            : typeof binding?.nodeSlug === "string"
                              ? binding.nodeSlug
                              : "",
                toPort:
                    typeof binding?.toPort === "string"
                        ? binding.toPort
                        : typeof binding?.to_port === "string"
                          ? binding.to_port
                          : typeof binding?.field === "string"
                            ? binding.field
                            : "",
            }))
            .filter((binding) => {
                const hasValues =
                    binding.fromNode !== "" ||
                    binding.fromPort !== "" ||
                    binding.toNode !== "" ||
                    binding.toPort !== "";
                if (!hasValues) {
                    return false;
                }

                const key = `${binding.fromNode}:${binding.fromPort}:${binding.toNode}:${binding.toPort}`;
                if (seenBindings.has(key)) {
                    return false;
                }
                seenBindings.add(key);
                return true;
            });
    };

    const serializeBindingsForExport = (bindings) => {
        return normalizeBindings(bindings).map((binding) => ({
            from_step: binding.fromNode,
            from_port: binding.fromPort,
            to_step: binding.toNode,
            to_port: binding.toPort,
        }));
    };

    const bindingsForTarget = (bindings, toNode, toPort) => {
        return normalizeBindings(bindings).filter((binding) => binding.toNode === toNode && binding.toPort === toPort);
    };

    const bindingForTarget = (bindings, toNode, toPort) => {
        return bindingsForTarget(bindings, toNode, toPort)[0] || null;
    };

    const replaceBindingsForTarget = (bindings, toNode, toPort, nextBindings, allowMultiple) => {
        const remaining = normalizeBindings(bindings).filter(
            (binding) => !(binding.toNode === toNode && binding.toPort === toPort),
        );
        const scopedBindings = normalizeBindings(nextBindings).filter(
            (binding) => binding.toNode === toNode && binding.toPort === toPort,
        );
        const nextTargetBindings = allowMultiple ? scopedBindings : scopedBindings.slice(0, 1);
        return normalizeBindings([...remaining, ...nextTargetBindings]);
    };

    const replaceBindingSlug = (bindings, oldSlug, newSlug) => {
        if (!oldSlug || !newSlug || oldSlug === newSlug) {
            return normalizeBindings(bindings);
        }

        return normalizeBindings(bindings).map((binding) => ({
            fromNode: binding.fromNode === oldSlug ? newSlug : binding.fromNode,
            fromPort: binding.fromPort,
            toNode: binding.toNode === oldSlug ? newSlug : binding.toNode,
            toPort: binding.toPort,
        }));
    };

    const validateBindings = (nodes, edges, bindings) => {
        const issues = [];
        const nodeBySlug = new Map(nodes.map((node) => [node.slug, node]));
        const seenBindings = new Set();
        const seenSingleTargetPorts = new Set();

        normalizeBindings(bindings).forEach((binding) => {
            const bindingKey = `${binding.fromNode}:${binding.fromPort}:${binding.toNode}:${binding.toPort}`;
            const sourceNode = nodeBySlug.get(binding.fromNode);
            const targetNode = nodeBySlug.get(binding.toNode);

            if (seenBindings.has(bindingKey)) {
                issues.push({
                    message: `duplicate binding ${binding.fromNode}.${binding.fromPort} -> ${binding.toNode}.${binding.toPort}`,
                    nodeSlug: binding.fromNode,
                });
            } else {
                seenBindings.add(bindingKey);
            }

            if (!sourceNode) {
                issues.push({
                    message: `binding source node not found: ${binding.fromNode || "(empty)"}`,
                    nodeSlug: null,
                });
                return;
            }
            if (!targetNode) {
                issues.push({
                    message: `binding target node not found: ${binding.toNode || "(empty)"}`,
                    nodeSlug: null,
                });
                return;
            }
            if (!outputPortsForKind(sourceNode.kind).includes(binding.fromPort)) {
                issues.push({
                    message: `${binding.fromNode}: output port ${binding.fromPort || "(empty)"} is not available`,
                    nodeSlug: binding.fromNode,
                });
            }
            if (targetNode.kind !== KIND_TRANSFORM && !inputPortsForKind(targetNode.kind).includes(binding.toPort)) {
                issues.push({
                    message: `${binding.toNode}: input port ${binding.toPort || "(empty)"} is not available`,
                    nodeSlug: binding.toNode,
                });
            }
            const normalizedTargetPort = typeof binding.toPort === "string" ? binding.toPort.trim() : "";
            if (targetNode.kind === KIND_TRANSFORM && normalizedTargetPort === "") {
                issues.push({
                    message: `${binding.toNode}: transform input name is required`,
                    nodeSlug: binding.toNode,
                });
            }
            const singleTargetKey = `${binding.toNode}:${normalizedTargetPort}`;
            if (seenSingleTargetPorts.has(singleTargetKey)) {
                issues.push({
                    message: `${binding.toNode}: multiple bindings for input port ${normalizedTargetPort} are not allowed`,
                    nodeSlug: binding.toNode,
                });
            } else {
                seenSingleTargetPorts.add(singleTargetKey);
            }
            if (!edges.some((edge) => edge.from === binding.fromNode && edge.to === binding.toNode)) {
                issues.push({
                    message: `${binding.toNode}: binding source ${binding.fromNode} must have a direct incoming edge`,
                    nodeSlug: binding.toNode,
                });
            }
        });

        return issues;
    };

    window.braidplaneBuilderCanvasSerialization = {
        normalizeBindings,
        serializeBindingsForExport,
        bindingsForTarget,
        bindingForTarget,
        replaceBindingsForTarget,
        replaceBindingSlug,
        validateBindings,
    };
})();
