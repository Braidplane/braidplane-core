(() => {
    // Centralize CSRF headers for browser-initiated unsafe requests. The token
    // value comes from the server-rendered meta tag and is never persisted.
    const CSRF_HEADER = "X-CSRF-Token";
    const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS", "TRACE"]);

    function csrfToken() {
        return document.querySelector('meta[name="csrf-token"]')?.getAttribute("content") || "";
    }

    function sameOriginRequest(input) {
        const rawURL = input instanceof Request ? input.url : input instanceof URL ? input.href : String(input || "");
        if (!rawURL) {
            return false;
        }
        try {
            return new URL(rawURL, window.location.href).origin === window.location.origin;
        } catch (_) {
            return false;
        }
    }

    function headersWithCSRF(headers) {
        const token = csrfToken();
        if (!token) {
            return headers;
        }
        const nextHeaders = new Headers(headers || {});
        if (!nextHeaders.has(CSRF_HEADER)) {
            nextHeaders.set(CSRF_HEADER, token);
        }
        return nextHeaders;
    }

    document.addEventListener("htmx:configRequest", (event) => {
        const detail = event.detail || {};
        const method = String(detail.verb || detail.requestConfig?.verb || "GET").toUpperCase();
        const path = detail.path || detail.requestConfig?.path || window.location.href;
        const token = csrfToken();
        if (!token || !detail.headers || SAFE_METHODS.has(method) || !sameOriginRequest(path)) {
            return;
        }
        detail.headers[CSRF_HEADER] = token;
    });

    function pendingFormFromEvent(event) {
        const detail = event.detail || {};
        const source = detail.elt || event.target;
        const form =
            source instanceof HTMLFormElement ? source : source instanceof Element ? source.closest("form") : null;
        if (!(form instanceof HTMLFormElement)) {
            return null;
        }

        const method = String(
            detail.verb || detail.requestConfig?.verb || form.getAttribute("method") || "GET",
        ).toUpperCase();
        return SAFE_METHODS.has(method) ? null : form;
    }

    function setFormPending(form, isPending) {
        form.querySelectorAll('button:not([type]), button[type="submit"], input[type="submit"]').forEach((button) => {
            if (!(button instanceof HTMLButtonElement || button instanceof HTMLInputElement)) {
                return;
            }
            if (isPending) {
                if (button.dataset.braidplanePendingDisabled) return;
                button.dataset.braidplanePendingDisabled = button.disabled ? "preexisting" : "pending";
                button.disabled = true;
                button.setAttribute("aria-busy", "true");
                return;
            }

            if (button.dataset.braidplanePendingDisabled === "pending") {
                button.disabled = false;
            }
            delete button.dataset.braidplanePendingDisabled;
            button.removeAttribute("aria-busy");
        });
        if (!isPending) form.dispatchEvent(new Event("braidplane:form-mutated"));
    }

    function htmxManagedSubmit(form, submitter) {
        if (!window.htmx) {
            return false;
        }
        const htmxSelector = "[hx-delete], [hx-patch], [hx-post], [hx-put]";
        return form.matches(htmxSelector) || (submitter instanceof Element && submitter.matches(htmxSelector));
    }

    function destructiveConfirmationMessage(event) {
        const form = event.target instanceof HTMLFormElement ? event.target : null;
        if (!(form instanceof HTMLFormElement)) {
            return "";
        }
        const submitter = event.submitter;
        if (htmxManagedSubmit(form, submitter)) {
            return "";
        }
        if (submitter instanceof HTMLElement) {
            const message = submitter.getAttribute("data-destructive-confirm") || "";
            if (message) {
                return message;
            }
        }
        return form.getAttribute("data-destructive-confirm") || "";
    }

    // Keep destructive plain form submissions protected when HTMX is unavailable.
    document.addEventListener("submit", (event) => {
        const message = destructiveConfirmationMessage(event);
        if (!message || typeof window.confirm !== "function" || window.confirm(message)) {
            return;
        }
        event.preventDefault();
        event.stopImmediatePropagation();
    });

    document.addEventListener("htmx:beforeRequest", (event) => {
        const form = pendingFormFromEvent(event);
        if (form instanceof HTMLFormElement) {
            const feedback = form.querySelector("[data-request-error]");
            if (feedback) feedback.hidden = true;
            setFormPending(form, true);
        }
    });

    ["htmx:afterRequest", "htmx:responseError", "htmx:sendError", "htmx:timeout"].forEach((eventName) => {
        document.addEventListener(eventName, (event) => {
            const form = pendingFormFromEvent(event);
            if (form instanceof HTMLFormElement) {
                setFormPending(form, false);
                const feedback = form.querySelector("[data-request-error]");
                if (feedback && eventName !== "htmx:afterRequest") {
                    feedback.textContent =
                        event.detail?.xhr?.status === 429
                            ? "Too many requests. Wait a minute, then retry this request."
                            : "Request failed. Retry shortly, or refresh this page if your session expired.";
                    feedback.hidden = false;
                }
            }
        });
    });

    function alertPolicySelectorFromEvent(event) {
        const source = event.target;
        if (!(source instanceof Element)) {
            return null;
        }
        if (source.matches("[data-alert-policy-selector]")) {
            return source;
        }
        return source.querySelector("[data-alert-policy-selector]");
    }

    // Alert policy selectors are self-contained HTMX fragments. Reprocess the
    // swapped fragment so follow-up selector changes keep their own HTMX hooks.
    document.addEventListener("htmx:afterSwap", (event) => {
        const selector = alertPolicySelectorFromEvent(event);
        if (selector instanceof HTMLElement && typeof window.htmx?.process === "function") {
            window.htmx.process(selector);
        }
    });

    if (typeof window.fetch === "function") {
        const nativeFetch = window.fetch.bind(window);
        window.fetch = (input, init) => {
            const nextInit = init ? { ...init } : {};
            const method = String(nextInit.method || (input instanceof Request ? input.method : "GET")).toUpperCase();
            if (!SAFE_METHODS.has(method) && sameOriginRequest(input)) {
                nextInit.headers = headersWithCSRF(
                    nextInit.headers || (input instanceof Request ? input.headers : undefined),
                );
            }
            return nativeFetch(input, nextInit);
        };
    }
})();
