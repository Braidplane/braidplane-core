import { beforeEach, describe, expect, it, vi } from "vitest";

await import("../security.js");
await import("../ui-utils.js");

describe("browser session cleanup", () => {
    it("clears authenticated data before the logout form is sent and keeps its CSRF field", () => {
        const draftKey = "braidplane.builder.canvas.state.v6/user/org/project/draft-pipeline";
        window.localStorage.setItem(draftKey, "synthetic draft");
        window.localStorage.setItem("braidplane.sidebar.collapsed", "1");
        window.sessionStorage.setItem("braidplane.lineage.view", "synthetic lineage");
        window.sessionStorage.setItem("htmx-history-cache", "synthetic history");
        document.body.innerHTML = `
            <main>Authenticated workspace</main>
            <form method="post" action="/auth/logout"><input name="_csrf" value="fixture"></form>
        `;
        const logout = document.querySelector("form");
        try {
            expect(logout.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }))).toBe(true);
            expect(window.localStorage.getItem(draftKey)).toBeNull();
            expect(window.localStorage.getItem("braidplane.sidebar.collapsed")).toBe("1");
            expect(window.sessionStorage.getItem("braidplane.lineage.view")).toBeNull();
            expect(window.sessionStorage.getItem("htmx-history-cache")).toBeNull();
            expect(document.querySelector("main")).toBeNull();
            expect(logout.isConnected).toBe(true);
            expect(new FormData(logout).get("_csrf")).toBe("fixture");
        } finally {
            window.localStorage.clear();
            window.sessionStorage.clear();
            delete document.documentElement.dataset.braidplaneSessionEnded;
        }
    });
});

describe("security CSRF helpers", () => {
    beforeEach(() => {
        document.head.innerHTML = '<meta name="csrf-token" content="csrf-token">';
        document.body.innerHTML = "";
        delete window.htmx;
    });

    it("AddsCSRFHeaderForUnsafeSameOriginHTMXRequests", () => {
        const headers = {};

        document.dispatchEvent(
            new CustomEvent("htmx:configRequest", {
                detail: {
                    headers,
                    path: "/organizations",
                    verb: "post",
                },
            }),
        );

        expect(headers["X-CSRF-Token"]).toBe("csrf-token");
    });

    it("SkipsCSRFHeaderForSafeHTMXRequests", () => {
        const headers = {};

        document.dispatchEvent(
            new CustomEvent("htmx:configRequest", {
                detail: {
                    headers,
                    path: "/organizations",
                    verb: "get",
                },
            }),
        );

        expect(headers["X-CSRF-Token"]).toBeUndefined();
    });

    it("SkipsCSRFHeaderForCrossOriginHTMXRequests", () => {
        const headers = {};

        document.dispatchEvent(
            new CustomEvent("htmx:configRequest", {
                detail: {
                    headers,
                    path: "https://example.invalid/organizations",
                    verb: "post",
                },
            }),
        );

        expect(headers["X-CSRF-Token"]).toBeUndefined();
    });

    it("DisablesMutatingHTMXFormSubmittersWhileRequestIsPending", () => {
        document.body.innerHTML = `
            <form id="create-form" hx-post="/organizations">
                <button type="submit">Create</button>
                <button type="button">Cancel</button>
            </form>
        `;
        const form = document.getElementById("create-form");
        const submit = form.querySelector('button[type="submit"]');
        const cancel = form.querySelector('button[type="button"]');

        document.dispatchEvent(
            new CustomEvent("htmx:beforeRequest", {
                detail: { elt: form, requestConfig: { verb: "post" } },
            }),
        );

        expect(submit.disabled).toBe(true);
        expect(submit.getAttribute("aria-busy")).toBe("true");
        expect(cancel.disabled).toBe(false);

        document.dispatchEvent(
            new CustomEvent("htmx:afterRequest", {
                detail: { elt: form, requestConfig: { verb: "post" } },
            }),
        );

        expect(submit.disabled).toBe(false);
        expect(submit.hasAttribute("aria-busy")).toBe(false);
    });

    it("ShowsRateLimitFeedbackAndClearsItBeforeRetry", () => {
        document.body.innerHTML =
            '<form method="post"><button type="submit">Request</button><p data-request-error hidden></p></form>';
        const form = document.querySelector("form");
        const feedback = form.querySelector("[data-request-error]");
        document.dispatchEvent(new CustomEvent("htmx:responseError", { detail: { elt: form, xhr: { status: 429 } } }));
        expect(feedback.hidden).toBe(false);
        expect(feedback.textContent).toContain("Too many requests");
        document.dispatchEvent(new CustomEvent("htmx:beforeRequest", { detail: { elt: form } }));
        expect(feedback.hidden).toBe(true);
        document.dispatchEvent(new CustomEvent("htmx:sendError", { detail: { elt: form } }));
        expect(feedback.hidden).toBe(false);
        expect(feedback.textContent).toContain("Request failed");
        expect(form.querySelector("button").disabled).toBe(false);
    });

    it("KeepsDirtySaveDisabledDuringARequestAndRecalculatesItAfterFailure", () => {
        document.body.innerHTML =
            '<form method="post" data-dirty-form><input name="name" value="Original"><button data-dirty-submit disabled>Save</button></form>';
        const form = document.querySelector("form");
        const input = form.querySelector("input");
        const button = form.querySelector("button");
        window.braidplaneInitDirtyForms(document);
        input.value = "Draft";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        expect(button.disabled).toBe(false);
        form.dispatchEvent(new CustomEvent("htmx:beforeRequest", { bubbles: true, detail: { elt: form } }));
        input.value = "Changed while saving";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        expect(button.disabled).toBe(true);
        input.value = "Original";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        form.dispatchEvent(new CustomEvent("htmx:sendError", { bubbles: true, detail: { elt: form } }));
        expect(button.disabled).toBe(true);
        input.value = "Retry draft";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        expect(button.disabled).toBe(false);
    });

    it("RestoresSubmittersAfterOverlappingPendingEventsWithoutEnablingPreexistingDisabledButtons", () => {
        document.body.innerHTML =
            '<form method="post"><button type="submit">Save</button><button type="submit" disabled>Unavailable</button></form>';
        const form = document.querySelector("form");
        for (let i = 0; i < 2; i++) {
            form.dispatchEvent(new CustomEvent("htmx:beforeRequest", { bubbles: true, detail: { elt: form } }));
        }
        form.dispatchEvent(new CustomEvent("htmx:timeout", { bubbles: true, detail: { elt: form } }));
        expect(form.querySelectorAll("button")[0].disabled).toBe(false);
        expect(form.querySelectorAll("button")[1].disabled).toBe(true);
    });

    it("ConfirmsDestructivePlainFormSubmitWhenHTMXIsUnavailable", () => {
        document.body.innerHTML = `
            <form id="delete-form" data-destructive-confirm="Delete data source &quot;prod-db&quot;? This cannot be undone.">
                <button type="submit">Delete</button>
            </form>
        `;
        window.confirm = vi.fn(() => false);
        const form = document.getElementById("delete-form");
        const submit = form.querySelector('button[type="submit"]');
        const event = new Event("submit", { bubbles: true, cancelable: true });
        Object.defineProperty(event, "submitter", { value: submit });

        form.dispatchEvent(event);

        expect(window.confirm).toHaveBeenCalledWith('Delete data source "prod-db"? This cannot be undone.');
        expect(event.defaultPrevented).toBe(true);
    });

    it("LetsHTMXOwnConfirmationForHTMXManagedDestructiveForms", () => {
        document.body.innerHTML = `
            <form id="delete-form" hx-post="/data-sources/prod-db/delete" data-destructive-confirm="Delete data source &quot;prod-db&quot;? This cannot be undone.">
                <button type="submit">Delete</button>
            </form>
        `;
        window.htmx = {};
        window.confirm = vi.fn(() => false);
        const form = document.getElementById("delete-form");
        const submit = form.querySelector('button[type="submit"]');
        const event = new Event("submit", { bubbles: true, cancelable: true });
        Object.defineProperty(event, "submitter", { value: submit });

        form.dispatchEvent(event);

        expect(window.confirm).not.toHaveBeenCalled();
        expect(event.defaultPrevented).toBe(false);
    });
});
