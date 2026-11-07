(() => {
    const TONE_ERROR = "error";
    const TONE_SUCCESS = "success";

    const clearChildren = (element) => {
        while (element.firstChild) {
            element.removeChild(element.firstChild);
        }
    };

    const resetConflictDialogDOMState = (statusMessage = "") => {
        const overview = document.getElementById("builder-conflict-modal-overview");
        const resolver = document.getElementById("builder-conflict-resolver");
        if (overview instanceof HTMLElement) {
            overview.classList.remove("hidden");
        }
        if (resolver instanceof HTMLElement) {
            resolver.classList.add("hidden");
        }
        ["builder-conflict-units-list", "builder-conflict-resolver-inspector"].forEach((id) => {
            const element = document.getElementById(id);
            if (element instanceof HTMLElement) {
                clearChildren(element);
            }
        });
        const unresolvedCount = document.getElementById("builder-conflict-resolver-unresolved-count");
        if (unresolvedCount instanceof HTMLElement) {
            unresolvedCount.textContent = "0 unresolved";
        }
        const validationCount = document.getElementById("builder-conflict-resolver-validation-count");
        if (validationCount instanceof HTMLElement) {
            validationCount.textContent = "0 invalid";
        }
        const statusElement = document.getElementById("builder-conflict-modal-status");
        if (statusElement instanceof HTMLElement) {
            statusElement.textContent = statusMessage;
            statusElement.classList.remove("text-error", "text-success", "text-base-content/60");
            statusElement.classList.add("text-base-content/60");
        }
    };

    const bindConflictModalCloseReset = (resetHandler = resetConflictDialogDOMState) => {
        const conflictModalElement = document.getElementById("builder-conflict-modal");
        if (!(conflictModalElement instanceof HTMLDialogElement) || conflictModalElement.dataset.bound === "true") {
            return;
        }
        conflictModalElement.dataset.bound = "true";
        conflictModalElement.addEventListener("close", () => {
            resetHandler();
        });
    };

    const renderPayloadDiagnostic = (element, diagnostic) => {
        if (!(element instanceof HTMLElement)) {
            return;
        }

        if (!diagnostic || typeof diagnostic.message !== "string" || diagnostic.message.trim() === "") {
            element.textContent = "";
            element.className = "hidden mt-2 text-[11px]";
            return;
        }

        let toneClass = "text-base-content/60";
        if (diagnostic.tone === TONE_SUCCESS) {
            toneClass = "text-success";
        } else if (diagnostic.tone === TONE_ERROR) {
            toneClass = "text-error";
        }

        element.textContent = diagnostic.message;
        element.className = `mt-2 text-[11px] ${toneClass}`;
    };

    const renderEdgeContractPreview = (element, schema, canonicalDocument, odcs = false) => {
        clearChildren(element);
        const fields = odcs ? schema.properties : schema.fields;
        const list = document.createElement("div");
        list.className = "max-h-80 overflow-auto";
        for (const field of Array.isArray(fields) ? fields : []) {
            const detail = document.createElement("details");
            detail.className = "border-b border-base-300";
            const summary = document.createElement("summary");
            summary.className = "builder-contract-field-summary";
            const name = document.createElement("code");
            name.className = "builder-contract-field-name font-mono";
            name.textContent = field.name || field.id || "Unnamed field";
            const type = document.createElement("code");
            type.className = "builder-contract-field-type font-mono";
            type.textContent = field.logicalType || field.type || "type not declared";
            summary.append(name, " · ", type);
            if (field.required) summary.append(" · required");
            if (field.nullable) summary.append(" · nullable");
            if (typeof field.description === "string") summary.title = field.description;
            const declaration = document.createElement(odcs ? "p" : "pre");
            declaration.className = odcs
                ? "builder-contract-description"
                : "my-3 overflow-auto whitespace-pre-wrap break-words font-mono text-xs";
            declaration.textContent = odcs
                ? field.description || "Expand the full contract to inspect all declarations for this field."
                : JSON.stringify(field, null, 2);
            detail.append(summary, declaration);
            list.appendChild(detail);
        }
        if (!list.childElementCount) {
            list.textContent = "No top-level fields declared. Expand the contract for its complete declaration.";
            list.className = "text-xs text-base-content/70";
        }
        const full = document.createElement("details");
        full.className = "mt-3 border-t border-base-300 pt-3";
        const summary = document.createElement("summary");
        summary.className = "cursor-pointer text-xs font-medium";
        summary.textContent = odcs ? "Full ODCS contract and revision" : "Full inline contract";
        const declaration = document.createElement("pre");
        declaration.className =
            "mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-base-200 p-3 font-mono text-xs";
        declaration.tabIndex = 0;
        // Preserve the original API text so wide numeric declarations are never rounded by JSON.parse.
        declaration.textContent =
            typeof canonicalDocument === "string" ? canonicalDocument : JSON.stringify(canonicalDocument, null, 2);
        full.append(summary, declaration);
        element.append(list, full);
    };

    const renderContractComparison = (element, producer, consumer) => {
        clearChildren(element);
        const fields = (declaration) => {
            const values = declaration?.odcs ? declaration.schema?.properties : declaration?.schema?.fields;
            const declared = Array.isArray(values) ? values : [];
            if (!declaration?.fieldPrefix) return declared;
            return declared
                .filter(
                    (field) =>
                        typeof (field.name || field.id) === "string" &&
                        (field.name || field.id).startsWith(declaration.fieldPrefix),
                )
                .map((field) => ({ ...field, name: (field.name || field.id).slice(declaration.fieldPrefix.length) }));
        };
        const producerFields = fields(producer);
        const consumerFields = fields(consumer);
        if (consumer?.fieldPrefix && consumerFields.length === 0) {
            element.textContent =
                "No consumer fields are declared for this input relation. Use Validate for compatibility checks.";
            return;
        }
        if (!producer?.schema || !consumer?.schema) {
            element.textContent = "Both ports need a contract declaration for comparison.";
            return;
        }
        const left = new Map(producerFields.map((field) => [field.name || field.id, field]));
        const right = new Map(consumerFields.map((field) => [field.name || field.id, field]));
        if (
            left.size !== producerFields.length ||
            right.size !== consumerFields.length ||
            left.has(undefined) ||
            right.has(undefined)
        ) {
            element.textContent = "Field names are missing or repeated. Validate the declarations before comparing.";
            return;
        }
        const table = document.createElement("table");
        table.className = "w-full table-fixed text-xs";
        const head = document.createElement("thead");
        head.className = "sticky top-0 bg-base-100";
        const heading = document.createElement("tr");
        for (const name of ["Field", "Producer", "Consumer"]) {
            const cell = document.createElement("th");
            cell.scope = "col";
            cell.className = "text-left font-semibold pb-2 pr-2";
            cell.textContent = name;
            heading.appendChild(cell);
        }
        head.appendChild(heading);
        const body = document.createElement("tbody");
        const appendRow = (name, produced, expected, notes = []) => {
            const row = document.createElement("tr");
            row.className = "align-top border-t border-base-300";
            row.dataset.contractDifference = String(notes.length > 0);
            for (const value of [name, produced, expected]) {
                const cell = document.createElement("td");
                cell.className = "py-2 pr-2 whitespace-pre-wrap break-words [overflow-wrap:anywhere]";
                cell.textContent = value;
                row.appendChild(cell);
            }
            body.appendChild(row);
            if (notes.length) {
                const noteRow = document.createElement("tr");
                const cell = document.createElement("td");
                cell.colSpan = 3;
                cell.className = "pb-2 text-warning";
                cell.textContent = notes.join(" · ");
                noteRow.appendChild(cell);
                body.appendChild(noteRow);
            }
        };
        for (const [name, key] of [
            ["Representation", "representation"],
            ["Root shape", "rootShape"],
        ]) {
            const a = producer[key] || "Not declared";
            const b = consumer[key] || "Not declared";
            appendRow(name, a, b, a === b ? [] : ["Declaration differs"]);
        }
        const flag = (value) => (typeof value === "boolean" ? (value ? "yes" : "no") : "not declared");
        const type = (field) => field.logicalType || field.type || "Type not declared";
        const describe = (field) =>
            field
                ? `${type(field)}\nRequired: ${flag(field.required)}\nNullable: ${flag(field.nullable)}`
                : "Not declared";
        for (const name of new Set([...left.keys(), ...right.keys()])) {
            const a = left.get(name);
            const b = right.get(name);
            const notes = [];
            if (!a) notes.push(b.required ? "Consumer-only field · required by consumer" : "Consumer-only field");
            else if (!b) notes.push("Producer-only field");
            else {
                if (type(a) !== type(b)) notes.push("Type differs");
                if (flag(a.required) !== flag(b.required)) notes.push("Required declaration differs");
                if (flag(a.nullable) !== flag(b.nullable)) notes.push("Nullable declaration differs");
            }
            appendRow(name, describe(a), describe(b), notes);
        }
        table.append(head, body);
        element.className = "max-h-96 overflow-auto text-xs";
        element.tabIndex = 0;
        element.appendChild(table);
    };

    window.braidplaneBuilderCanvasRendering = {
        clearChildren,
        resetConflictDialogDOMState,
        bindConflictModalCloseReset,
        renderPayloadDiagnostic,
        renderEdgeContractPreview,
        renderContractComparison,
    };
})();
