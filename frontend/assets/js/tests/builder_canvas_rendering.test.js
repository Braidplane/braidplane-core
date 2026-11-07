import { beforeEach, describe, expect, it } from "vitest";

await import("../builder_canvas_rendering.js");

const t = window.braidplaneBuilderCanvasRendering;

beforeEach(() => {
    document.body.innerHTML = "";
});

describe("builder canvas rendering helpers", () => {
    it("clearChildren_RemovesExistingNodes", () => {
        const element = document.createElement("div");
        element.innerHTML = "<span>one</span><span>two</span>";

        t.clearChildren(element);

        expect(element.childElementCount).toBe(0);
    });

    it("renderPayloadDiagnostic_PaintsToneAndMessage", () => {
        const element = document.createElement("p");

        t.renderPayloadDiagnostic(element, { tone: "error", message: "Payload mismatch" });

        expect(element.textContent).toBe("Payload mismatch");
        expect(element.className).toBe("mt-2 text-[11px] text-error");
    });

    it("resetConflictDialogDOMState_ClearsStaleResolverContent", () => {
        document.body.innerHTML = `
            <div id="builder-conflict-modal-status" class="text-error">old</div>
            <div id="builder-conflict-modal-overview" class="hidden"></div>
            <div id="builder-conflict-resolver"></div>
            <div id="builder-conflict-units-list"><button type="button">old</button></div>
            <div id="builder-conflict-resolver-inspector">old</div>
            <span id="builder-conflict-resolver-unresolved-count">4 unresolved</span>
            <span id="builder-conflict-resolver-validation-count">2 invalid</span>
        `;

        t.resetConflictDialogDOMState();

        expect(document.getElementById("builder-conflict-modal-overview")?.classList.contains("hidden")).toBe(false);
        expect(document.getElementById("builder-conflict-resolver")?.classList.contains("hidden")).toBe(true);
        expect(document.getElementById("builder-conflict-units-list")?.textContent).toBe("");
        expect(document.getElementById("builder-conflict-resolver-inspector")?.textContent).toBe("");
    });
});

describe("edge contract disclosures", () => {
    it("limits legacy combined contracts to the selected input relation", () => {
        const element = document.createElement("div");
        const producer = { schema: { fields: [{ name: "id", type: "string" }] } };
        const contract = {
            fields: [
                { name: "orders.id", type: "integer" },
                { name: "accounts.email", type: "string" },
            ],
        };
        t.renderContractComparison(element, producer, { schema: contract, fieldPrefix: "orders." });
        expect(element.textContent).toContain("Type differs");
        expect(element.textContent).not.toContain("accounts.email");
        expect(element.textContent).not.toContain("orders.id");
        expect(contract.fields[0].name).toBe("orders.id");
        t.renderContractComparison(element, producer, { schema: contract, fieldPrefix: "missing." });
        expect(element.textContent).toContain("No consumer fields are declared for this input relation");
    });

    it("compares declarations without equating differences with incompatibility", () => {
        const element = document.createElement("div");
        t.renderContractComparison(
            element,
            {
                schema: {
                    fields: [
                        { name: "id", type: "integer", required: true, nullable: true },
                        { name: "extra", type: "string" },
                    ],
                },
                representation: "rows",
            },
            {
                schema: {
                    properties: [
                        { name: "id", logicalType: "string", required: false, nullable: false },
                        { name: "email", logicalType: "string", required: true },
                    ],
                },
                odcs: true,
                representation: "json",
                rootShape: "array",
            },
        );
        expect([...element.querySelectorAll("th")].map((cell) => cell.textContent)).toEqual([
            "Field",
            "Producer",
            "Consumer",
        ]);
        expect(element.textContent).toContain("Type differs");
        expect(element.textContent).toContain("Required declaration differs");
        expect(element.textContent).toContain("Nullable declaration differs");
        expect(element.textContent).toContain("Consumer-only field · required by consumer");
        expect(element.textContent).toContain("Producer-only field");
        expect(element.textContent).not.toContain("incompatible");
        expect(element.querySelectorAll('[data-contract-difference="true"]')).toHaveLength(5);
    });

    it("keeps identical declarations quiet and authored markup inert", () => {
        const element = document.createElement("div");
        const declaration = {
            schema: { fields: [{ name: "<img src=x onerror=alert(1)>", type: "object", nullable: true }] },
        };
        t.renderContractComparison(element, declaration, declaration);
        expect(element.querySelectorAll('[data-contract-difference="true"]')).toHaveLength(0);
        expect(element.querySelector("img")).toBeNull();
        expect(element.textContent).toContain("<img");
        t.renderContractComparison(element, null, declaration);
        expect(element.textContent).toContain("Both ports need");
        t.renderContractComparison(element, { schema: { fields: [{ name: "id" }, { name: "id" }] } }, declaration);
        expect(element.textContent).toContain("missing or repeated");
    });

    it("shows field summaries and preserves complete revision text without executing authored content", () => {
        const element = document.createElement("div");
        const raw = '{"document":{"wide":9007199254740993,"description":"<img src=x onerror=alert(1)>"}}';
        t.renderEdgeContractPreview(
            element,
            { properties: [{ name: "id", logicalType: "integer", required: true, description: "Stable identity" }] },
            raw,
            true,
        );
        expect(element.querySelector("summary").textContent).toBe("id · integer · required");
        expect(element.querySelector("summary").title).toBe("Stable identity");
        expect(element.querySelector("summary code").textContent).toBe("id");
        expect(element.querySelector("details p").textContent).toBe("Stable identity");

        expect(element.lastElementChild.querySelector("pre").textContent).toBe(raw);
        expect(element.querySelector("img")).toBeNull();
    });
    it("keeps the complete inline agreement available", () => {
        const element = document.createElement("div");
        const contract = { representation: "json", fields: [{ name: "payload", type: "object", nullable: true }] };
        t.renderEdgeContractPreview(element, contract, contract);
        expect(element.textContent).toContain("payload · object · nullable");
        expect(element.textContent).toContain("Full inline contract");
        expect(element.lastElementChild.querySelector("pre").textContent).toBe(JSON.stringify(contract, null, 2));
    });
});
