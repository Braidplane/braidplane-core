(() => {
    const element = (tag, text, className = "") => {
        const node = document.createElement(tag);
        node.textContent = text;
        node.className = className;
        return node;
    };
    const quoteIdentifier = (value) => `\`${value.replaceAll("`", "``")}\``;
    const quoteConstant = (value) => `'${value.replaceAll("\\", "\\\\").replaceAll("'", "''")}'`;
    const references = new WeakMap();
    const contractRequest = (context, source, destination) => ({
        org: context.org,
        project: context.project,
        ...(references.has(source) ? { source_ref: references.get(source) } : { source }),
        ...(references.has(destination) ? { destination_ref: references.get(destination) } : { destination }),
    });
    const requestJSON = async (url, options, fallback) => {
        try {
            const response = await fetch(url, options);
            const result = await response.json();
            if (!result || typeof result !== "object" || Array.isArray(result)) throw new Error(fallback);
            return { response, result };
        } catch {
            throw new Error(fallback);
        }
    };
    const post = async (body) => {
        const fallback = "Mapping unavailable. Retry after checking your session.";
        const { response, result } = await requestJSON(
            "/builder/mapping",
            {
                method: "POST",
                headers: { "Content-Type": "application/json", Accept: "application/json" },
                cache: "no-store",
                redirect: "error",
                body: JSON.stringify(body),
            },
            fallback,
        );
        if (!response.ok)
            throw new Error(
                (Array.isArray(result.diagnostics) ? result.diagnostics : [])
                    .map(
                        (item) =>
                            `${item.step ? `${item.step}: ` : ""}${item.field ? `${item.field}: ` : ""}${item.code}: ${item.message}`,
                    )
                    .join("\n") || (typeof result.error === "string" ? result.error : fallback),
            );
        return result;
    };
    const resolveShape = async (context, declaration, direction, label) => {
        const reference = declaration?.[`${direction}_contract_ref`];
        if (!reference) return declaration?.[`${direction}_contract`] || null;
        if (!context.org || !context.project || !reference.revision_slug || reference.revision_slug === "latest")
            throw new Error(`${label}: select an exact ODCS revision and project.`);
        const path = `/api/organizations/${encodeURIComponent(context.org)}/projects/${encodeURIComponent(context.project)}/contracts/${encodeURIComponent(reference.contract_slug)}/revisions/${encodeURIComponent(reference.revision_slug)}/inspection`;
        const unavailable = `${label}: contract unavailable. Check project access and the exact revision.`;
        const { response, result } = await requestJSON(
            `${path}?${new URLSearchParams({ schema_object_id: reference.schema_object_id })}`,
            {
                headers: { Accept: "application/json" },
                cache: "no-store",
                redirect: "error",
            },
            unavailable,
        );
        if (!response.ok) throw new Error(unavailable);
        if (
            result.revision?.slug !== reference.revision_slug ||
            result.schema_object_id !== reference.schema_object_id ||
            !result.revision?.digest
        )
            throw new Error(`${label}: exact contract identity could not be confirmed.`);
        if (!result.document_valid || !result.projection_available || !result.shape)
            throw new Error(
                `${label}: no supported flat shape. ${result.diagnostics?.map((item) => `${item.code}: ${item.message}`).join(" ") || "Keep advanced editing and inspect contract coverage."}`,
            );
        context.evidence.push(
            `${label}: ${reference.contract_slug} / ${reference.revision_slug} / ${reference.schema_object_id} · ${result.revision.digest} · ${result.compiler_id}`,
        );
        const shape = {
            ...result.shape,
            ...(reference.representation ? { representation: reference.representation } : {}),
            ...(reference.root_shape ? { root_shape: reference.root_shape } : {}),
        };
        references.set(shape, reference);
        return shape;
    };
    const attachment = ({ slug, onOpen, onLayout }) => {
        const root = element("div", "", "builder-mapping-attachment");
        const header = element("div", "", "builder-mapping-attachment-header");
        const open = element("button", "", "builder-mapping-open");
        open.type = "button";
        open.setAttribute("aria-expanded", "false");
        open.setAttribute("aria-controls", "builder-mapping-workspace");
        open.setAttribute("aria-label", `Open field mapping for ${slug}`);
        const title = element("span", "Field mapping");
        const status = element("small", "Reading SQL…");
        open.append(title, status);
        open.addEventListener("click", () => onOpen(open));
        const toggle = element("button", "", "btn btn-ghost btn-xs builder-mapping-toggle");
        toggle.type = "button";
        toggle.setAttribute("aria-expanded", "false");
        toggle.setAttribute("aria-label", `Expand field mapping for ${slug}`);
        const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
        icon.setAttribute("viewBox", "0 0 16 16");
        icon.setAttribute("aria-hidden", "true");
        const path = document.createElementNS(icon.namespaceURI, "path");
        path.setAttribute("d", "m4 6 4 4 4-4");
        icon.append(path);
        toggle.append(icon);
        const details = element("div", "", "builder-mapping-attachment-detail");
        details.id = `builder-mapping-detail-${slug}`;
        details.hidden = true;
        toggle.setAttribute("aria-controls", details.id);
        header.append(open, toggle);
        root.append(header, details);
        // These controls must not trigger node selection, connection gestures or canvas shortcuts.
        for (const event of ["click", "pointerdown", "keydown"])
            root.addEventListener(event, (event) => event.stopPropagation());
        let expanded = false;
        let identity = "";
        const setExpanded = (value) => {
            expanded = value;
            details.hidden = !expanded;
            toggle.setAttribute("aria-expanded", String(expanded));
            toggle.setAttribute("aria-label", `${expanded ? "Collapse" : "Expand"} field mapping for ${slug}`);
            onLayout();
        };
        toggle.addEventListener("click", () => setExpanded(!expanded));
        const update = async (context) => {
            const nextIdentity = JSON.stringify([
                context.org,
                context.project,
                context.environment,
                context.step.transform,
                context.steps.filter((step) => step.sink?.input === context.step.slug).map((step) => step.sink),
            ]);
            if (identity === nextIdentity) return;
            identity = nextIdentity;
            const current = () => root.isConnected && identity === nextIdentity;
            status.textContent = "Reading SQL…";
            root.classList.remove("builder-mapping-warning");
            details.replaceChildren();
            const message = (text) => details.append(element("p", text, "text-xs break-words"));
            try {
                if (Object.keys(context.step.transform.inputs || {}).length !== 1)
                    throw new Error(
                        Object.keys(context.step.transform.inputs || {}).length === 0
                            ? "Connect an upstream step first. Choose Edit mapping to set up the input."
                            : "Guided mapping needs one input relation. Keep advanced SQL for multi-input transforms.",
                    );
                const result = await post({ action: "inspect", sql: context.step.transform.sql });
                if (!current()) return;
                for (const field of result.mapping.fields) {
                    const pair = element("div", "", "builder-mapping-pair");
                    pair.append(element("code", field.expression), element("span", "→"), element("code", field.name));
                    details.append(pair);
                }
                status.textContent = `${result.mapping.fields.length} fields · checks not run`;
                const shapes = [];
                const own = await resolveShape(context, context.step.transform, "output", "Transform output");
                if (own) shapes.push(own);
                for (const step of context.steps.filter((step) => step.sink?.input === context.step.slug)) {
                    const shape = await resolveShape(context, step.sink, "input", `Destination ${step.slug}`);
                    if (shape) shapes.push(shape);
                }
                if (!current()) return;
                const missing = [
                    ...new Set(
                        shapes.flatMap((shape) =>
                            (shape.fields || [])
                                .filter(
                                    (field) =>
                                        field.required &&
                                        !/[.[\]]/.test(field.name) &&
                                        !result.mapping.fields.some((mapped) => mapped.name === field.name),
                                )
                                .map((field) => field.name),
                        ),
                    ),
                ];
                if (missing.length) {
                    status.textContent = `${result.mapping.fields.length} fields · ${missing.length} required missing`;
                    root.classList.add("builder-mapping-warning");
                    message(`Required output fields missing: ${missing.join(", ")}.`);
                } else if (!shapes.length) message("No output requirements declared. Compatibility is unchecked.");
                message("Assignments read from SQL. Types, values and delivery are unchecked.");
            } catch (error) {
                if (!current()) return;
                status.textContent =
                    Object.keys(context.step.transform.inputs || {}).length === 0
                        ? "Connect an input to start mapping"
                        : "Inspection unavailable · open editor";
                root.classList.add("builder-mapping-warning");
                message(error.message);
            }
            if (current()) {
                const edit = element("button", "Edit mapping", "btn btn-xs btn-outline w-full mt-2");
                edit.type = "button";
                edit.addEventListener("click", () => onOpen(edit, { edit: true }));
                details.append(edit);
                onLayout();
            }
        };
        const renderSummary = (container) => {
            container.replaceChildren(
                ...Array.from(details.children)
                    .filter((child) => child.tagName !== "BUTTON")
                    .map((child) => child.cloneNode(true)),
            );
            if (!container.childElementCount) container.append(element("p", status.textContent, "text-xs"));
        };
        return { element: root, update, setExpanded, isExpanded: () => expanded, renderSummary };
    };

    const mount = (container, context) => {
        container.replaceChildren();
        container.hidden = !context;
        if (!context) return;
        const heading = element("h4", "Field mapping", "builder-inspector-section-title");
        const help = element(
            "p",
            "Select, rename and order fields. Applying a mapping changes SQL only; contract references stay pinned. Publication and execution are separate.",
            "mt-2 text-xs text-base-content/70",
        );
        const status = element("p", "", "mt-2 whitespace-pre-wrap break-words text-xs");
        status.setAttribute("role", "status");
        const actions = element("div", "", "mt-2 flex flex-wrap gap-2");
        const editor = element("div", "", "mt-3 space-y-3");
        container.append(heading, help, actions, status, editor);
        let mapping = null;
        let source = null;
        let sources = [];
        let targets = [];
        let replacing = false;
        let fixtureJSON = "";
        let revision = 0;
        let pendingRevision = null;
        let busy = false;
        let refreshRequirements;
        const current = () =>
            editor.isConnected && context.isCurrent() && (pendingRevision === null || revision === pendingRevision);
        const changed = () => {
            revision++;
            status.textContent = "Mapping or fixture changed. Check SQL and test the current values.";
            editor.querySelectorAll("pre").forEach((preview) => {
                preview.textContent = "";
            });
            refreshRequirements?.();
        };
        editor.addEventListener("input", changed);
        editor.addEventListener("change", changed);
        const button = (label, callback, parent = actions) => {
            const control = element("button", label, "btn btn-xs btn-outline");
            control.type = "button";
            control.addEventListener("click", async () => {
                if (busy) return;
                if (!current()) {
                    status.textContent =
                        "Selection, SQL or environment changed. Reopen the mapping from the current transform.";
                    return;
                }
                const controls = Array.from(container.querySelectorAll("button"), (button) => [
                    button,
                    button.disabled,
                ]);
                for (const [button] of controls) button.disabled = true;
                busy = true;
                pendingRevision = revision;
                container.setAttribute("aria-busy", "true");
                try {
                    await callback();
                } catch (error) {
                    if (current()) status.textContent = error.message;
                } finally {
                    for (const [button, disabled] of controls) button.disabled = disabled;
                    busy = false;
                    pendingRevision = null;
                    if (editor.isConnected) container.setAttribute("aria-busy", "false");
                }
            });
            parent.appendChild(control);
            return control;
        };
        const load = async () => {
            const transform = context.step.transform;
            const inputs = Object.keys(transform.inputs || {});
            if (inputs.length !== 1)
                throw new Error(
                    "Guided mapping needs one input relation. Keep advanced SQL for multi-input transforms.",
                );
            const input = inputs[0];
            context.evidence = [];
            const upstream = context.steps.find((step) => step.slug === transform.inputs[input]);
            const producer = upstream?.query || upstream?.transform || upstream?.input;
            const attachment = transform.input_contracts?.[input];
            const producerShape = await resolveShape(context, producer, "output", "Source agreement");
            const inputShape = attachment
                ? await resolveShape(
                      context,
                      { input_contract: attachment.contract, input_contract_ref: attachment.contract_ref },
                      "input",
                      `Input ${input}`,
                  )
                : await resolveShape(context, transform, "input", "Input agreement");
            sources = [producerShape, inputShape].filter(Boolean);
            source = sources[0] || null;
            targets = [];
            const ownTarget = await resolveShape(context, transform, "output", "Transform output");
            if (ownTarget) targets.push({ label: "Transform output", shape: ownTarget });
            for (const step of context.steps.filter((step) => step.sink?.input === context.step.slug)) {
                if (!current()) return null;
                const shape = await resolveShape(context, step.sink, "input", `Destination ${step.slug}`);
                if (shape) targets.push({ label: `Destination ${step.slug}`, shape });
            }
            if (!current()) return null;
            return input;
        };
        const fieldLabel = (text, control) => {
            const label = element("label", "", "form-control min-w-0");
            label.append(element("span", text, "label-text text-xs"), control);
            return label;
        };
        const render = () => {
            editor.dataset.fieldMappingDraft = "";
            editor.replaceChildren();
            const evidence = element(
                "p",
                context.evidence.join("\n"),
                "whitespace-pre-wrap break-all text-xs text-base-content/60",
            );
            if (context.evidence.length) {
                const pins = element("details", "", "text-xs");
                pins.append(element("summary", "Pinned contract revisions", "cursor-pointer"), evidence);
                editor.appendChild(pins);
            }
            editor.appendChild(
                element(
                    "p",
                    `Input: ${mapping.input} · Environment: ${context.environment || "not selected"}`,
                    "text-xs",
                ),
            );
            for (const step of context.steps.filter((step) => step.sink?.input === context.step.slug)) {
                const sink = step.sink;
                const resolution = context.resolveSource?.(sink.data_source_ref);
                const operation = element("details", "", "text-xs");
                operation.appendChild(
                    element(
                        "summary",
                        `Destination ${step.slug} · ${sink.operation_type || "operation unselected"} · ${sink.data_source_ref || "unselected"}`,
                        "cursor-pointer",
                    ),
                );
                operation.appendChild(
                    element(
                        "p",
                        `Destination ${step.slug}: ${sink.data_source_ref || "unselected"} · ${sink.operation_type || "operation unselected"} · ${sink.operation_ref || JSON.stringify(sink.operation_inline || sink.config || {})}. Replay class: ${sink.idempotency_class || "undeclared"}. ${resolution?.hint || "Inspect the destination's environment binding and connector capabilities before running."} Review this operation's write and duplicate behavior.`,
                        "break-words text-xs text-base-content/70",
                    ),
                );
                editor.appendChild(operation);
            }
            const fields = source?.fields || [];
            if (!fields.length)
                editor.appendChild(
                    element(
                        "p",
                        "Source shape unavailable. Declare an input/output agreement, or select authorized cached fields on the source node. Manual SQL expressions remain available.",
                        "text-xs text-warning",
                    ),
                );
            if (!targets.length)
                editor.appendChild(
                    element(
                        "p",
                        "No destination requirement declared. Add one in Contracts to check required fields and types.",
                        "text-xs text-warning",
                    ),
                );
            const requirements = element("div", "", "space-y-1");
            editor.appendChild(requirements);
            refreshRequirements = () => {
                requirements.replaceChildren();
                for (const target of targets) {
                    const missing = (target.shape.fields || []).filter(
                        (field) =>
                            field.required &&
                            !/[.[\]]/.test(field.name) &&
                            !mapping.fields.some((mapped) => mapped.name === field.name),
                    );
                    requirements.appendChild(
                        element(
                            "p",
                            `${target.label} required fields: ${
                                (target.shape.fields || [])
                                    .filter((field) => field.required)
                                    .map(
                                        (field) =>
                                            `${field.name} (${field.type}${field.nullable ? ", nullable" : ", non-null"})`,
                                    )
                                    .join(", ") || "none"
                            }.${missing.length ? ` Unmapped: ${missing.map((field) => field.name).join(", ")}.` : ""}`,
                            `text-xs ${missing.length ? "text-warning" : "text-base-content/70"}`,
                        ),
                    );
                }
            };
            refreshRequirements();
            mapping.fields.forEach((field, index) => {
                const row = element("fieldset", "", "space-y-2 border-t border-base-300 pt-3");
                row.appendChild(element("legend", `Field ${index + 1}`, "text-xs font-medium"));
                const name = element("input", "", "input input-bordered input-sm w-full");
                name.value = field.name;
                name.addEventListener("input", () => {
                    field.name = name.value;
                });
                row.appendChild(fieldLabel("Output name", name));
                const picker = element("select", "", "select select-bordered select-sm w-full");
                const placeholder = element("option", "Choose a source field");
                placeholder.value = "";
                picker.appendChild(placeholder);
                fields.forEach((sourceField) => {
                    const option = element("option", `${sourceField.name} · ${sourceField.type}`);
                    option.value = `field:${sourceField.name}`;
                    picker.appendChild(option);
                });
                const constantOption = element("option", "Text constant");
                constantOption.value = "__constant__";
                const expressionOption = element("option", "SQL expression / current value");
                expressionOption.value = "__expression__";
                picker.append(constantOption, expressionOption);
                const selected = fields.find(
                    (item) => field.expression === quoteIdentifier(item.name) || field.expression === item.name,
                );
                picker.value = selected ? `field:${selected.name}` : field.expression ? "__expression__" : "";
                const expression = element("input", "", "input input-bordered input-sm w-full font-mono text-xs");
                expression.value = field.expression;
                const expressionDetails = element("details", "", "mt-2");
                const expressionSummary = element(
                    "summary",
                    `SQL value: ${field.expression || "not selected"}`,
                    "cursor-pointer break-all text-xs text-base-content/70",
                );
                expressionDetails.append(
                    expressionSummary,
                    fieldLabel("Value expression (supported transform SQL)", expression),
                );
                expression.addEventListener("input", () => {
                    field.expression = expression.value;
                    expressionSummary.textContent = `SQL value: ${field.expression}`;
                });
                picker.addEventListener("change", () => {
                    constantGroup.classList.toggle("hidden", picker.value !== "__constant__");
                    const selectedField = fields.find((item) => `field:${item.name}` === picker.value);
                    if (selectedField) {
                        expression.value = field.expression = quoteIdentifier(selectedField.name);
                        expressionSummary.textContent = `SQL value: ${field.expression}`;
                    }
                    expressionDetails.open = picker.value === "__expression__";
                });
                row.appendChild(fieldLabel("Source field", picker));
                const constant = element("input", "", "input input-bordered input-sm w-full");
                constant.placeholder = "e.g. PLN";
                const constantGroup = element("div", "", "hidden space-y-2");
                constantGroup.appendChild(fieldLabel("Constant text", constant));
                row.appendChild(constantGroup);
                button(
                    "Use constant",
                    () => {
                        expression.value = field.expression = quoteConstant(constant.value);
                        expressionSummary.textContent = `SQL value: ${field.expression}`;
                        changed();
                    },
                    constantGroup,
                );
                const conversion = element("select", "", "select select-bordered select-sm w-full");
                for (const [value, label] of [
                    ["", "Apply conversion"],
                    ["DECIMAL(18,2)", "Decimal (18,2), half-even rounding"],
                    ["CHAR", "Text"],
                    ["SIGNED", "Integer"],
                    ["TRIM", "Trim whitespace"],
                    ["LOWER", "Lowercase"],
                    ["UPPER", "Uppercase"],
                ]) {
                    const option = element("option", label);
                    option.value = value;
                    conversion.appendChild(option);
                }
                conversion.addEventListener("change", () => {
                    if (!conversion.value || !expression.value.trim()) return;
                    field.expression = ["TRIM", "LOWER", "UPPER"].includes(conversion.value)
                        ? `${conversion.value}(${expression.value})`
                        : `CAST(${expression.value} AS ${conversion.value})`;
                    expression.value = field.expression;
                    expressionSummary.textContent = `SQL value: ${field.expression}`;
                    conversion.value = "";
                });
                row.append(fieldLabel("Conversion", conversion), expressionDetails);
                const ordering = element("div", "", "flex flex-wrap gap-2");
                const move = (delta) => {
                    const next = index + delta;
                    [mapping.fields[index], mapping.fields[next]] = [mapping.fields[next], mapping.fields[index]];
                    changed();
                    render();
                    editor.querySelectorAll("fieldset")[next]?.querySelector("input")?.focus();
                };
                button("Move up", () => move(-1), ordering).disabled = index === 0;
                button("Move down", () => move(1), ordering).disabled = index === mapping.fields.length - 1;
                button(
                    "Remove",
                    () => {
                        mapping.fields.splice(index, 1);
                        changed();
                        render();
                        const next = Math.min(index, mapping.fields.length - 1);
                        (next >= 0
                            ? editor.querySelectorAll("fieldset")[next]?.querySelector("input")
                            : editor.querySelector("button")
                        )?.focus();
                    },
                    ordering,
                );
                row.appendChild(ordering);
                editor.appendChild(row);
            });
            const footer = element("div", "", "flex flex-wrap gap-2");
            button(
                "Add field",
                () => {
                    mapping.fields.push({ name: "", expression: "" });
                    changed();
                    render();
                    editor.querySelectorAll("fieldset")[mapping.fields.length - 1]?.querySelector("input")?.focus();
                },
                footer,
            );
            button(
                "Suggest fields",
                async () => {
                    if (!source || !targets.length)
                        throw new Error("Select source and destination contract shapes first.");
                    const result = await post({
                        action: "suggest",
                        ...contractRequest(context, source, targets[targets.length - 1].shape),
                        mapping,
                    });
                    if (!current()) return;
                    for (const field of result.mapping.fields)
                        if (!mapping.fields.some((existing) => existing.name === field.name))
                            mapping.fields.push(field);
                    render();
                    changed();
                    status.textContent =
                        "Suggestions added for review. Matching names and types do not establish business meaning or non-null values.";
                },
                footer,
            );
            const preview = element("pre", "", "max-h-48 overflow-auto whitespace-pre-wrap break-all text-xs");
            button(
                "Check SQL",
                async () => {
                    const result = await post({ action: "render", mapping });
                    if (!current()) return;
                    preview.textContent = result.sql;
                    if (!source || !targets.length)
                        throw new Error(
                            "SQL generated. Source or destination shape is unavailable; compatibility remains unchecked.",
                        );
                    for (const target of targets)
                        for (const inputShape of sources) {
                            if (!current()) return;
                            await post({
                                action: "check",
                                sql: result.sql,
                                output_kind: context.step.transform.output_kind || "rows",
                                ...contractRequest(context, inputShape, target.shape),
                            });
                        }
                    if (current())
                        status.textContent =
                            "Static shape checks passed. Fixture values, runtime nullability and destination delivery remain separate checks.";
                },
                footer,
            );
            button(
                replacing ? "Replace SQL with mapping" : "Apply mapping to SQL",
                async () => {
                    const result = await post({ action: "render", mapping });
                    if (!current()) return;
                    context.applySQL(result.sql);
                },
                footer,
            );
            editor.append(footer, preview);
            const fixture = element("textarea", "", "textarea textarea-bordered mt-2 w-full font-mono text-xs");
            fixture.rows = 7;
            fixture.value = fixtureJSON;
            fixture.placeholder = JSON.stringify(
                { inputs: { [mapping.input]: [{ field: "sample" }] }, expected: [] },
                null,
                2,
            );
            fixture.addEventListener("input", () => {
                fixtureJSON = fixture.value;
            });
            const fixtureHelp = element(
                "p",
                "Offline fixture: supply synthetic inputs matching their contracts (row arrays, JSON objects/arrays, or CSV text as a JSON string) and optional expected output. No providers are called or samples retained. Successful output is shown here. Empty inputs check no row values; fixtures do not prove live delivery.",
                "mt-2 text-xs text-base-content/70",
            );
            const fixtureOutput = element(
                "pre",
                "",
                "mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs",
            );
            editor.append(fixtureHelp, fieldLabel("Fixture JSON (inputs and optional expected)", fixture));
            button(
                "Test fixture",
                async () => {
                    fixtureOutput.textContent = "";
                    try {
                        if (!source || !targets.length)
                            throw new Error("Select source and destination shapes before testing fixture contracts.");
                        const rendered = await post({ action: "render", mapping });
                        let result;
                        for (const target of targets)
                            for (const inputShape of sources) {
                                if (!current()) return;
                                result = await post({
                                    action: "fixture",
                                    sql: rendered.sql,
                                    step: context.step.slug,
                                    output_kind: context.step.transform.output_kind || "rows",
                                    fixture_json: fixtureJSON,
                                    ...contractRequest(context, inputShape, target.shape),
                                });
                            }
                        if (!current()) return;
                        fixtureOutput.textContent = result.output_json;
                        status.textContent = result.asserted
                            ? "Fixture contracts and expected output passed. This does not establish destination delivery."
                            : "Fixture contracts passed and output is shown. No expected-output assertion was supplied.";
                    } catch (error) {
                        if (current()) fixtureOutput.textContent = error.message;
                        throw error;
                    }
                },
                editor,
            );
            editor.appendChild(fixtureOutput);
        };
        const openButton = button("Open mapping", async () => {
            status.textContent = "Loading mapping and exact contract shapes…";
            const input = await load();
            if (!input) return;
            const result = await post({ action: "inspect", sql: context.step.transform.sql });
            if (!current()) return;
            mapping = result.mapping;
            replacing = false;
            render();
            status.textContent = "Changes stay local until you apply the mapping to SQL.";
        });
        const newButton = button("New mapping", async () => {
            const input = await load();
            if (!input) return;
            mapping = { input, fields: [] };
            replacing = true;
            render();
            status.textContent = "Current SQL is preserved. Applying this new mapping explicitly replaces it.";
        });
        const inputCount = Object.keys(context.step.transform?.inputs || {}).length;
        if (inputCount !== 1 && context.isCurrent()) {
            openButton.disabled = newButton.disabled = true;
            const setup = element("div", "", "mt-3 space-y-3");
            editor.append(setup);
            if (inputCount === 0) {
                status.textContent =
                    "Connect an input to start mapping. Open mapping and New mapping become available after one upstream step is connected.";
                const candidates = context.steps.filter(
                    (step) => step.slug !== context.step.slug && (step.query || step.input || step.transform),
                );
                if (candidates.length && context.connectInput) {
                    const picker = element("select", "", "select select-bordered select-sm w-full");
                    const placeholder = element("option", "Select upstream step");
                    placeholder.value = "";
                    picker.append(placeholder);
                    for (const step of candidates) {
                        const option = element("option", step.name || step.slug);
                        option.value = step.slug;
                        picker.append(option);
                    }
                    setup.append(fieldLabel("Mapping input", picker));
                    const connect = button("Connect input", () => context.connectInput(picker.value), setup);
                    connect.disabled = true;
                    picker.addEventListener("change", () => {
                        connect.disabled = !picker.value;
                    });
                } else {
                    setup.append(
                        element(
                            "p",
                            "Add an Event Input or Query Step in Canonical Steps, then connect it to this transform.",
                            "text-xs text-base-content/70",
                        ),
                    );
                }
                if (context.chooseSource) button("Choose source step", context.chooseSource, setup);
            } else {
                status.textContent =
                    "This transform has multiple inputs. Keep advanced SQL, or configure one input relation before using guided mapping.";
            }
        }
        return {
            open: () => {
                if (!mapping && inputCount === 1) openButton.click();
            },
        };
    };
    window.braidplaneFieldMapping = {
        mount,
        attachment,
        resolveShape,
        quoteIdentifier,
        quoteConstant,
        contractRequest,
    };
})();
