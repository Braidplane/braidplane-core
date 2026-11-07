import { describe, expect, it } from "vitest";

// Side-effect import: runs the IIFE and sets window.braidplaneQueryParamEditor.
await import("../query_param_editor.js");

const t = window.braidplaneQueryParamEditor._testing;

function replaceBodyFromHTML(html) {
    const parsed = new DOMParser().parseFromString(html, "text/html");
    document.body.replaceChildren(...Array.from(parsed.body.childNodes));
}

describe("parsePathQueryPairs", () => {
    it("EmptyString_ReturnsEmpty", () => {
        expect(t.parsePathQueryPairs("")).toEqual([]);
    });

    it("NoQueryString_ReturnsEmpty", () => {
        expect(t.parsePathQueryPairs("/api/users")).toEqual([]);
    });

    it("BareQuestionMark_ReturnsEmpty", () => {
        expect(t.parsePathQueryPairs("/api/users?")).toEqual([]);
    });

    it("SinglePair_ParsedCorrectly", () => {
        expect(t.parsePathQueryPairs("/api/users?status=active")).toEqual([{ key: "status", value: "active" }]);
    });

    it("MultiplePairs_AllParsed", () => {
        expect(t.parsePathQueryPairs("/api/users?a=1&b=2")).toEqual([
            { key: "a", value: "1" },
            { key: "b", value: "2" },
        ]);
    });

    it("HashFragment_StopsBeforeHash", () => {
        expect(t.parsePathQueryPairs("/api?q=hello#section")).toEqual([{ key: "q", value: "hello" }]);
    });

    it("TemplatePlaceholders_BracesNotEncoded", () => {
        // {{userId}} must stay readable — not %7B%7BuserId%7D%7D.
        expect(t.parsePathQueryPairs("/api?id={{userId}}")).toEqual([{ key: "id", value: "{{userId}}" }]);
    });

    it("NullInput_ReturnsEmpty", () => {
        expect(t.parsePathQueryPairs(null)).toEqual([]);
    });
});

describe("splitPathAndQuery", () => {
    it("NullInput_ReturnsEmptyPathAndNoPairs", () => {
        expect(t.splitPathAndQuery(null)).toEqual({ pathOnly: "", queryPairs: [] });
    });

    it("NoQueryString_ReturnsFullPathAndNoPairs", () => {
        expect(t.splitPathAndQuery("/api/users")).toEqual({
            pathOnly: "/api/users",
            queryPairs: [],
        });
    });

    it("PathWithQuery_SplitsCorrectly", () => {
        expect(t.splitPathAndQuery("/api/users?status=active")).toEqual({
            pathOnly: "/api/users",
            queryPairs: [{ key: "status", value: "active" }],
        });
    });

    it("EmptyString_ReturnsEmptyPathAndNoPairs", () => {
        expect(t.splitPathAndQuery("")).toEqual({ pathOnly: "", queryPairs: [] });
    });

    it("PathWithMultiplePairs_SplitsAll", () => {
        const result = t.splitPathAndQuery("/search?q=foo&page=2");
        expect(result.pathOnly).toBe("/search");
        expect(result.queryPairs).toEqual([
            { key: "q", value: "foo" },
            { key: "page", value: "2" },
        ]);
    });
});

describe("extractParams", () => {
    it("NullInput_ReturnsEmpty", () => {
        expect(t.extractParams(null)).toEqual([]);
    });

    it("EmptyString_ReturnsEmpty", () => {
        expect(t.extractParams("")).toEqual([]);
    });

    it("NoPlaceholders_ReturnsEmpty", () => {
        expect(t.extractParams("SELECT * FROM users")).toEqual([]);
    });

    it("SinglePlaceholder_ExtractedByName", () => {
        expect(t.extractParams("SELECT * FROM t WHERE id = {{id}}")).toEqual(["id"]);
    });

    it("MultiplePlaceholders_AllExtracted", () => {
        expect(t.extractParams("{{a}} AND {{b}}")).toEqual(["a", "b"]);
    });

    it("DuplicatePlaceholders_Deduplicated", () => {
        expect(t.extractParams("{{x}} OR {{x}} AND {{y}}")).toEqual(["x", "y"]);
    });

    it("UnderscoreAndDigitsAllowed", () => {
        expect(t.extractParams("WHERE col1 = {{col_1}}")).toEqual(["col_1"]);
    });
});

describe("HubSpot upsert field visibility", () => {
    it("OperationChange_TogglesUpsertFields", () => {
        replaceBodyFromHTML(
            [
                "<form>",
                '<select data-hubspot-operation-select><option value="search" selected>search</option><option value="upsert">upsert</option></select>',
                "<div data-hubspot-upsert-fields>Upsert fields</div>",
                "</form>",
            ].join(""),
        );

        window.braidplaneQueryParamEditor.initAll(document);
        const select = document.querySelector("[data-hubspot-operation-select]");
        const fields = document.querySelector("[data-hubspot-upsert-fields]");
        expect(fields.classList.contains("hidden")).toBe(true);

        select.value = "upsert";
        select.dispatchEvent(new Event("change", { bubbles: true }));
        expect(fields.classList.contains("hidden")).toBe(false);

        select.value = "search";
        select.dispatchEvent(new Event("change", { bubbles: true }));
        expect(fields.classList.contains("hidden")).toBe(true);
    });
});

describe("param editor initialization", () => {
    it("ValidationSwap_ReinitializesRowControls", async () => {
        await import("../ui-utils.js");
        replaceBodyFromHTML(`<div id="queries-workspace"><form>
            <div data-kv-editor>
                <button type="button" data-add-kv>Add</button>
                <div data-kv-rows></div>
                <template data-kv-row-template><div data-kv-row><input name="field"><button type="button" data-remove-kv>Remove</button></div></template>
            </div>
        </form></div>`);
        document
            .querySelector("#queries-workspace")
            .dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true }));
        document.querySelector("[data-add-kv]").click();
        expect(document.querySelectorAll('[name="field"]')).toHaveLength(1);
        document.querySelector("[data-remove-kv]").click();
        expect(document.querySelectorAll('[name="field"]')).toHaveLength(0);
    });

    it("Enter_LeavesDisclosureKeyboardAccessWhilePreventingImplicitSubmit", () => {
        replaceBodyFromHTML(
            `<form><input data-http-path-input><details><summary>Acknowledgement</summary></details></form>`,
        );
        window.braidplaneQueryParamEditor.initAll(document);
        const disclosureEnter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
        document.querySelector("summary").dispatchEvent(disclosureEnter);
        expect(disclosureEnter.defaultPrevented).toBe(false);
        const inputEnter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
        document.querySelector("input").dispatchEvent(inputEnter);
        expect(inputEnter.defaultPrevented).toBe(true);
    });

    it("KeyValueRows_NotifyDirtyFormWhenAddedOrRemoved", async () => {
        await import("../ui-utils.js");
        replaceBodyFromHTML(`<form data-dirty-form>
            <div data-kv-editor>
                <button type="button" data-add-kv>Add</button>
                <div data-kv-rows><div data-kv-row><input name="path" value="code"><button type="button" data-remove-kv>Remove</button></div></div>
                <template data-kv-row-template><div data-kv-row><input name="path"><button type="button" data-remove-kv>Remove</button></div></template>
            </div>
            <button data-dirty-submit disabled>Save</button>
        </form>`);
        window.braidplaneQueryParamEditor.initAll(document);
        window.braidplaneInitDirtyForms(document);
        const save = document.querySelector("[data-dirty-submit]");
        expect(save.disabled).toBe(true);
        document.querySelector("[data-remove-kv]").click();
        expect(save.disabled).toBe(false);
        document.querySelector("[data-add-kv]").click();
        const path = document.querySelector('[name="path"]');
        path.value = "code";
        path.dispatchEvent(new Event("input", { bubbles: true }));
        expect(save.disabled).toBe(true);
        document.querySelector("[data-add-kv]").click();
        expect(save.disabled).toBe(false);
    });

    it("RepeatedInit_BindsAddParamOnce", () => {
        replaceBodyFromHTML(
            [
                "<form>",
                "<textarea data-param-source-input>{{customer_id}}</textarea>",
                "<div data-param-editor>",
                '<button type="button" data-add-param>Add param</button>',
                '<button type="button" data-sync-params>Sync from statement</button>',
                "<div data-param-rows></div>",
                "<template data-param-row-template>",
                "<div data-param-row>",
                '<input name="param_name" value="" />',
                '<select name="param_type"><option value="text">text</option></select>',
                '<select name="param_required"><option value="false">optional</option></select>',
                '<input name="param_default" value="" />',
                '<input name="param_description" value="" />',
                '<input type="hidden" name="param_source" value="manual" data-param-source />',
                "<span data-param-source-badge>manual</span>",
                '<button type="button" data-remove-param>Remove</button>',
                "</div>",
                "</template>",
                "</div>",
                "</form>",
            ].join(""),
        );

        window.braidplaneQueryParamEditor.initAll(document);
        window.braidplaneQueryParamEditor.initAll(document);

        document.querySelector("[data-add-param]").click();

        expect(document.querySelectorAll("[data-param-row]")).toHaveLength(1);
    });

    it("SyncFromStatement_CreatesDiscoveredParam", () => {
        replaceBodyFromHTML(
            [
                "<form>",
                "<textarea data-param-source-input>select * from users where id = {{user_id}}</textarea>",
                "<div data-param-editor>",
                '<button type="button" data-add-param>Add param</button>',
                '<button type="button" data-sync-params>Sync from statement</button>',
                "<div data-param-rows></div>",
                "<template data-param-row-template>",
                "<div data-param-row>",
                '<input name="param_name" value="" />',
                '<select name="param_type"><option value="text">text</option></select>',
                '<select name="param_required"><option value="false">optional</option></select>',
                '<input name="param_default" value="" />',
                '<input name="param_description" value="" />',
                '<input type="hidden" name="param_source" value="manual" data-param-source />',
                "<span data-param-source-badge>manual</span>",
                '<button type="button" data-remove-param>Remove</button>',
                "</div>",
                "</template>",
                "</div>",
                "</form>",
            ].join(""),
        );

        window.braidplaneQueryParamEditor.initAll(document);

        document.querySelector("[data-sync-params]").click();

        expect(document.querySelector('input[name="param_name"]').value).toBe("user_id");
        expect(document.querySelector("[data-param-source]").value).toBe("auto");
    });
});
