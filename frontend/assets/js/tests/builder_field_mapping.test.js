import { beforeEach, describe, expect, it, vi } from "vitest";

await import("../builder_field_mapping.js");
const mapping = window.braidplaneFieldMapping;
const jsonResponse = (value, ok = true) => ({ ok, json: async () => value });
const click = async (container, label) => {
    const button = [...container.querySelectorAll("button")].find((node) => node.textContent === label);
    expect(button).toBeTruthy();
    button.click();
    await vi.waitFor(() => expect(button.disabled).toBe(false));
};

describe("field mapping", () => {
    beforeEach(() => {
        vi.restoreAllMocks();
        document.body.replaceChildren();
    });
    it("quotes identifiers and constants", () => {
        expect(mapping.quoteIdentifier("a`b")).toBe("`a``b`");
        expect(mapping.quoteConstant("O'Brien\\notes")).toBe("'O''Brien\\\\notes'");
    });
    it("requires exact authorized ODCS identity and retains reference facets", async () => {
        const context = { org: "acme", project: "catalog", evidence: [] };
        const ref = {
            contract_slug: "products",
            revision_slug: "v1",
            schema_object_id: "product",
            representation: "rows",
        };
        const fetcher = vi.spyOn(globalThis, "fetch").mockResolvedValue(
            jsonResponse({
                revision: { slug: "v1", digest: "sha256:exact" },
                schema_object_id: "product",
                compiler_id: "compiler/v1",
                document_valid: true,
                projection_available: true,
                shape: { name: "product", fields: [{ name: "sku", type: "string", required: true }] },
            }),
        );
        const declaration = { output_contract_ref: ref };
        const shape = await mapping.resolveShape(context, declaration, "output", "Target");
        expect(shape.representation).toBe("rows");
        expect(shape.fields[0].required).toBe(true);
        expect(context.evidence[0]).toContain("sha256:exact");
        expect(fetcher.mock.calls[0][0]).toContain("/products/revisions/v1/inspection?schema_object_id=product");
        expect(fetcher.mock.calls[0][1]).toMatchObject({ cache: "no-store", redirect: "error" });
        expect(declaration.output_contract_ref).toBe(ref);
        const request = mapping.contractRequest(context, { name: "source", fields: [] }, shape);
        expect(request.destination_ref).toBe(ref);
        expect(request).not.toHaveProperty("destination");
        fetcher.mockResolvedValue(jsonResponse({}, false));
        await expect(mapping.resolveShape(context, declaration, "output", "Target")).rejects.toThrow(
            "Check project access",
        );
        fetcher.mockResolvedValue(jsonResponse({ revision: { slug: "v2", digest: "sha256:changed" } }));
        await expect(mapping.resolveShape(context, declaration, "output", "Target")).rejects.toThrow("identity");
        fetcher.mockResolvedValue(
            jsonResponse({
                revision: { slug: "v1", digest: "sha256:exact" },
                schema_object_id: "product",
                document_valid: true,
                projection_available: false,
                diagnostics: [{ code: "unsupported", message: "Keep advanced SQL" }],
            }),
        );
        await expect(mapping.resolveShape(context, declaration, "output", "Target")).rejects.toThrow("unsupported");
    });
    it("keeps changes local and applies only SQL after explicit replacement", async () => {
        const container = document.createElement("section");
        document.body.appendChild(container);
        const shape = {
            name: "products",
            fields: [
                { name: "sku", type: "string", required: true },
                { name: "currency", type: "string", required: true },
            ],
        };
        const step = {
            slug: "map",
            transform: { inputs: { input: "fetch" }, sql: "SELECT * FROM input", output_contract: shape },
        };
        const original = JSON.stringify(step);
        const applySQL = vi.fn();
        const fetcher = vi.spyOn(globalThis, "fetch").mockImplementation(async (_url, options) => {
            const request = JSON.parse(options.body);
            if (request.action === "suggest")
                return jsonResponse({
                    mapping: {
                        input: "input",
                        fields: [
                            { name: "sku", expression: "`sku`" },
                            { name: "currency", expression: "`currency`" },
                        ],
                    },
                });
            return jsonResponse({ sql: "SELECT 'PLN' AS currency, sku FROM input" });
        });
        mapping.mount(container, {
            org: "acme",
            project: "catalog",
            environment: "staging",
            step,
            steps: [{ slug: "fetch", query: { output_contract: shape } }, step],
            isCurrent: () => true,
            applySQL,
        });
        expect(container.querySelector("[data-field-mapping-draft]")).toBeNull();
        await click(container, "New mapping");
        expect(container.querySelector("[data-field-mapping-draft]")).not.toBeNull();
        expect(applySQL).not.toHaveBeenCalled();
        expect(container.textContent).toContain("explicitly replaces");
        expect(container.textContent).toContain("Unmapped: sku, currency");
        await click(container, "Suggest fields");
        expect(container.querySelectorAll("fieldset")).toHaveLength(2);
        const moveDown = [...container.querySelectorAll("button")].find((node) => node.textContent === "Move down");
        moveDown.click();
        await vi.waitFor(() => expect(document.activeElement.value).toBe("sku"));
        expect(container.querySelector("fieldset input").value).toBe("currency");
        const row = container.querySelector("fieldset");
        row.querySelector('input[placeholder="e.g. PLN"]').value = "PLN";
        await click(row, "Use constant");
        await click(container, "Replace SQL with mapping");
        expect(applySQL).toHaveBeenCalledWith("SELECT 'PLN' AS currency, sku FROM input");
        expect(JSON.parse(fetcher.mock.calls.at(-1)[1].body).mapping.fields[0]).toEqual({
            name: "currency",
            expression: "'PLN'",
        });
        expect(JSON.stringify(step)).toBe(original);
    });
    it("checks the producer and narrower transform input agreement", async () => {
        const container = document.createElement("section");
        document.body.appendChild(container);
        const producer = { name: "producer", fields: [{ name: "sku", type: "string" }] };
        const agreement = { name: "agreement", fields: [{ name: "id", type: "string" }] };
        const target = { name: "target", fields: [{ name: "sku", type: "string" }] };
        const step = {
            slug: "map",
            transform: {
                inputs: { input: "fetch" },
                sql: "SELECT sku FROM input",
                input_contract: agreement,
                output_contract: target,
            },
        };
        const fetcher = vi.spyOn(globalThis, "fetch").mockImplementation(async (_url, options) => {
            const request = JSON.parse(options.body);
            if (request.action === "inspect")
                return jsonResponse({ mapping: { input: "input", fields: [{ name: "sku", expression: "sku" }] } });
            if (request.action === "render") return jsonResponse({ sql: step.transform.sql });
            if (request.source?.name === "agreement")
                return jsonResponse({ error: "sku is absent from input agreement" }, false);
            return jsonResponse({ valid: true });
        });
        mapping.mount(container, {
            step,
            steps: [{ slug: "fetch", query: { output_contract: producer } }, step],
            isCurrent: () => true,
        });
        await click(container, "Open mapping");
        await click(container, "Check SQL");
        const checks = fetcher.mock.calls
            .map(([, options]) => JSON.parse(options.body))
            .filter((request) => request.action === "check");
        expect(checks.map((request) => request.source.name)).toEqual(["producer", "agreement"]);
        expect(container.textContent).toContain("sku is absent from input agreement");
        expect(container.textContent).not.toContain("Static shape checks passed");
    });
    it("rejects stale selection and renders diagnostic text safely", async () => {
        const container = document.createElement("section");
        document.body.appendChild(container);
        const fetcher = vi.spyOn(globalThis, "fetch");
        mapping.mount(container, { step: {}, steps: [], isCurrent: () => false });
        await click(container, "Open mapping");
        expect(container.textContent).toContain("environment changed");
        expect(fetcher).not.toHaveBeenCalled();
        mapping.mount(container, {
            step: { transform: { inputs: { input: "fetch" }, sql: "SELECT id FROM input" } },
            steps: [],
            isCurrent: () => true,
        });
        fetcher.mockResolvedValue(
            jsonResponse({ diagnostics: [{ code: "unsupported", message: "<img src=x onerror=alert(1)>" }] }, false),
        );
        await click(container, "Open mapping");
        expect(container.textContent).toContain("<img src=x");
        expect(container.querySelector("img")).toBeNull();
    });
    it.each(["edit", "remount"])("does not apply stale SQL after %s", async (action) => {
        const container = document.createElement("section");
        document.body.appendChild(container);
        const shape = { name: "products", fields: [{ name: "sku", type: "string", required: true }] };
        const step = {
            slug: "map",
            transform: { inputs: { input: "fetch" }, sql: "SELECT sku FROM input", output_contract: shape },
        };
        const context = {
            step,
            steps: [{ slug: "fetch", query: { output_contract: shape } }, step],
            isCurrent: () => true,
            applySQL: vi.fn(),
        };
        let release;
        const pending = new Promise((resolve) => {
            release = resolve;
        });
        const fetcher = vi.spyOn(globalThis, "fetch").mockImplementation(async (_url, options) => {
            if (JSON.parse(options.body).action === "render") return pending;
            return jsonResponse({ mapping: { input: "input", fields: [{ name: "sku", expression: "`sku`" }] } });
        });
        mapping.mount(container, context);
        await click(container, "Open mapping");
        const apply = [...container.querySelectorAll("button")].find(
            (node) => node.textContent === "Apply mapping to SQL",
        );
        apply.click();
        await vi.waitFor(() =>
            expect(fetcher.mock.calls.some(([, options]) => JSON.parse(options.body).action === "render")).toBe(true),
        );
        if (action === "edit") {
            const input = container.querySelector("fieldset input");
            input.value = "new_sku";
            input.dispatchEvent(new Event("input", { bubbles: true }));
        } else {
            mapping.mount(container, context);
        }
        release(jsonResponse({ sql: "SELECT sku AS old_sku FROM input" }));
        await vi.waitFor(() => expect(apply.disabled).toBe(false));
        expect(context.applySQL).not.toHaveBeenCalled();
    });
    it.each(["network", "html", "null"])("explains %s failures without raw browser errors", async (failure) => {
        const container = document.createElement("section");
        document.body.appendChild(container);
        const fetcher = vi.spyOn(globalThis, "fetch");
        if (failure === "network") fetcher.mockRejectedValue(new TypeError("raw browser failure"));
        else if (failure === "html")
            fetcher.mockResolvedValue({
                ok: false,
                json: async () => {
                    throw new SyntaxError("raw HTML failure");
                },
            });
        else fetcher.mockResolvedValue(jsonResponse(null, false));
        mapping.mount(container, {
            step: { slug: "map", transform: { inputs: { input: "fetch" }, sql: "SELECT id FROM input" } },
            steps: [],
            isCurrent: () => true,
        });
        await click(container, "Open mapping");
        expect(container.textContent).toContain("Mapping unavailable. Retry after checking your session.");
        expect(container.textContent).not.toContain("raw browser failure");
        expect(container.textContent).not.toContain("raw HTML failure");
    });
});

describe("canvas mapping attachments", () => {
    beforeEach(() => {
        vi.restoreAllMocks();
        document.body.replaceChildren();
    });
    const context = (sql = "SELECT sku FROM input") => {
        const step = {
            slug: "normalize",
            transform: {
                inputs: { input: "fetch" },
                sql,
                output_contract: { name: "catalog", fields: [{ name: "currency", required: true }] },
            },
        };
        return { org: "acme", project: "catalog", environment: "dev", step, steps: [step], evidence: [] };
    };
    it("discloses canonical assignments and missing requirements without mutating the pipeline", async () => {
        const c = context();
        const original = JSON.stringify(c.steps);
        const fetcher = vi.spyOn(globalThis, "fetch").mockResolvedValue(
            jsonResponse({
                mapping: { input: "input", fields: [{ name: "sku", expression: "TRIM(item_code)" }] },
            }),
        );
        const onOpen = vi.fn();
        const onLayout = vi.fn();
        const attachment = mapping.attachment({ slug: "normalize", onOpen, onLayout });
        document.body.append(attachment.element);
        await attachment.update(c);
        expect(attachment.element.textContent).toContain("1 fields · 1 required missing");
        expect(attachment.element.textContent).toContain("TRIM(item_code)→sku");
        const summary = document.createElement("div");
        attachment.renderSummary(summary);
        expect(summary.textContent).toContain("TRIM(item_code)→sku");
        expect(summary.querySelector("button")).toBeNull();
        expect(summary.querySelectorAll(".builder-mapping-pair")).toHaveLength(1);
        expect(attachment.element.textContent).toContain("Types, values and delivery are unchecked");
        const toggle = attachment.element.querySelector(".builder-mapping-toggle");
        expect(toggle.getAttribute("aria-expanded")).toBe("false");
        toggle.click();
        expect(toggle.getAttribute("aria-expanded")).toBe("true");
        expect(attachment.isExpanded()).toBe(true);
        expect(document.getElementById(toggle.getAttribute("aria-controls")).hidden).toBe(false);
        attachment.element.querySelector(".builder-mapping-open").click();
        expect(onOpen).toHaveBeenCalled();
        await attachment.update(c);
        expect(fetcher).toHaveBeenCalledTimes(1);
        attachment.setExpanded(false);
        expect(onLayout).toHaveBeenCalled();
        expect(JSON.stringify(c.steps)).toBe(original);
    });
    it("ignores late inspection results and preserves advanced SQL on failure", async () => {
        let complete;
        const fetcher = vi
            .spyOn(globalThis, "fetch")
            .mockImplementationOnce(
                () =>
                    new Promise((resolve) => {
                        complete = resolve;
                    }),
            )
            .mockResolvedValue(
                jsonResponse({ diagnostics: [{ code: "unsupported", message: "Keep advanced SQL" }] }, false),
            );
        const attachment = mapping.attachment({ slug: "normalize", onOpen: vi.fn(), onLayout: vi.fn() });
        document.body.append(attachment.element);
        const first = attachment.update(context());
        const next = context("SELECT * FROM input WHERE sku IS NOT NULL");
        const sql = next.step.transform.sql;
        await attachment.update(next);
        expect(attachment.element.textContent).toContain("Inspection unavailable");
        expect(attachment.element.textContent).toContain("Keep advanced SQL");
        complete(jsonResponse({ mapping: { fields: [{ name: "obsolete", expression: "sku" }] } }));
        await first;
        expect(attachment.element.textContent).not.toContain("obsolete");
        expect(next.step.transform.sql).toBe(sql);
        expect(fetcher).toHaveBeenCalledTimes(2);
    });
});

describe("mapping input setup and loading", () => {
    beforeEach(() => {
        vi.restoreAllMocks();
        document.body.replaceChildren();
    });
    const mount = (steps, callbacks = {}) => {
        const container = document.createElement("section");
        document.body.append(container);
        const controller = mapping.mount(container, {
            step: steps[steps.length - 1],
            steps,
            isCurrent: () => true,
            applySQL: vi.fn(),
            ...callbacks,
        });
        return { container, controller };
    };
    const find = (container, name) =>
        [...container.querySelectorAll("button")].find((button) => button.textContent === name);
    it("explains missing inputs before accepting mapping actions and connects an explicit source", async () => {
        const fetcher = vi.spyOn(globalThis, "fetch");
        const connectInput = vi.fn();
        const step = { slug: "map", transform: { sql: "SELECT sku FROM source", inputs: {} } };
        const original = JSON.stringify(step);
        const { container, controller } = mount([{ slug: "source", query: {} }, step], { connectInput });
        controller.open();
        expect(find(container, "Open mapping").disabled).toBe(true);
        expect(find(container, "New mapping").disabled).toBe(true);
        expect(container.textContent).toContain("Connect an input to start mapping");
        const picker = container.querySelector("select");
        picker.value = "source";
        picker.dispatchEvent(new Event("change", { bubbles: true }));
        await click(container, "Connect input");
        expect(connectInput).toHaveBeenCalledWith("source");
        expect(fetcher).not.toHaveBeenCalled();
        expect(JSON.stringify(step)).toBe(original);
    });
    it("offers palette recovery when no source exists and preserves multi-input SQL", async () => {
        const chooseSource = vi.fn();
        const { container } = mount([{ slug: "map", transform: { inputs: {}, sql: "SELECT * FROM input_rows" } }], {
            chooseSource,
        });
        expect(container.textContent).toContain("Add an Event Input or Query Step");
        await click(container, "Choose source step");
        expect(chooseSource).toHaveBeenCalledOnce();
        const multi = {
            slug: "join",
            transform: { inputs: { a: "first", b: "second" }, sql: "SELECT * FROM a JOIN b" },
        };
        const { container: advanced } = mount([multi]);
        expect(advanced.textContent).toContain("multiple inputs");
        expect(find(advanced, "New mapping").disabled).toBe(true);
        expect(multi.transform.sql).toBe("SELECT * FROM a JOIN b");
    });
    it("disables both actions during loading and restores them after failure", async () => {
        let finish;
        vi.spyOn(globalThis, "fetch").mockImplementation(
            () =>
                new Promise((resolve) => {
                    finish = resolve;
                }),
        );
        const { container } = mount([
            { slug: "source", query: {} },
            { slug: "map", transform: { inputs: { source: "source" }, sql: "SELECT sku FROM source" } },
        ]);
        find(container, "Open mapping").click();
        await vi.waitFor(() => expect(finish).toBeTruthy());
        expect(find(container, "Open mapping").disabled).toBe(true);
        expect(find(container, "New mapping").disabled).toBe(true);
        finish(jsonResponse({ error: "inspect failed" }, false));
        await vi.waitFor(() => expect(find(container, "New mapping").disabled).toBe(false));
        expect(find(container, "Open mapping").disabled).toBe(false);
        expect(container.textContent).toContain("inspect failed");
    });
});
