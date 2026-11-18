(() => {
    // Centralize CSRF headers for browser-initiated unsafe requests. The token
    // value comes from the server-rendered meta tag and is never persisted.
    const CSRF_HEADER = "X-CSRF-Token";
    const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS", "TRACE"]);
    const SESSION_ENDED_HEADER = "X-Braidplane-Session-Ended";
    const SESSION_EXPIRY_HEADER = "X-Braidplane-Session-Expiry";
    const SESSION_ENDED_KEY = "braidplane.session.ended";
    const SESSION_EXPIRY_KEY = "braidplane.session.expiry";
    const ACTIVE_TAB_KEY = "braidplane:authenticated-tab";
    const TAB_GENERATION_KEY = "braidplane:session-generation";
    const BROWSER_GENERATION_KEY = "braidplane.session.generation";
    const nativeFetch = typeof window.fetch === "function" ? window.fetch.bind(window) : null;
    const initialExpiry = document.querySelector('meta[name="braidplane-session-expiry"]')?.content;
    const pageGeneration = initialExpiry?.split(":")[0];

    function authenticatedPage() {
        return document.querySelector('meta[name="braidplane-authenticated-page"]') !== null;
    }

    function clearStorage(storageName, shouldClear) {
        try {
            const storage = window[storageName];
            for (let index = storage.length - 1; index >= 0; index--) {
                const key = storage.key(index);
                if (key && shouldClear(key)) storage.removeItem(key);
            }
        } catch (_) {
            // Continue signing out if the browser disables storage.
        }
    }

    function isDraftKey(key) {
        return key.startsWith("braidplane.builder.canvas.state.");
    }

    function clearAuthenticatedStorage(generation = pageGeneration, submittingLogout = false) {
        let clearShared = !submittingLogout;
        if (clearShared && generation) {
            try {
                const current = window.localStorage.getItem(BROWSER_GENERATION_KEY);
                clearShared = !current || current === generation;
            } catch (_) {
                // Storage access may be restricted.
            }
        }
        if (clearShared) {
            clearStorage(
                "localStorage",
                (key) =>
                    isDraftKey(key) ||
                    key === BROWSER_GENERATION_KEY ||
                    key === SESSION_EXPIRY_KEY ||
                    key.startsWith("braidplane.dashboard.onboarding.hidden."),
            );
        }
        clearStorage(
            "sessionStorage",
            (key) =>
                isDraftKey(key) ||
                key === "htmx-history-cache" ||
                key === "braidplane.lineage.view" ||
                (key.startsWith("braidplane:") &&
                    !(submittingLogout && (key === ACTIVE_TAB_KEY || key === TAB_GENERATION_KEY))) ||
                key.startsWith("query-detail-scroll:"),
        );
    }

    function discardAuthenticatedPage(...remaining) {
        document.documentElement.dataset.braidplaneSessionEnded = "true";
        document.body.replaceChildren(...remaining);
    }

    function notifyOtherTabs(generation = pageGeneration) {
        try {
            window.localStorage.setItem(SESSION_ENDED_KEY, `${generation || ""}:${Date.now()}`);
        } catch (_) {
            // This tab is already cleared.
        }
    }

    function endBrowserSession(redirect, generation) {
        if (document.documentElement.dataset.braidplaneSessionEnded === "true") return;
        clearAuthenticatedStorage(generation);
        discardAuthenticatedPage();
        window.location.replace(redirect || "/auth/login");
    }

    clearStorage("sessionStorage", (key) => key === "htmx-history-cache");
    let wasAuthenticated = false;
    let previousGeneration;
    try {
        wasAuthenticated = window.sessionStorage.getItem(ACTIVE_TAB_KEY) === "1";
        previousGeneration = window.sessionStorage.getItem(TAB_GENERATION_KEY);
        if (authenticatedPage()) window.sessionStorage.setItem(ACTIVE_TAB_KEY, "1");
    } catch (_) {
        // Server-confirmed logout and navigation still work.
    }
    if (document.querySelector('meta[name="braidplane-session-ended"]')) {
        clearAuthenticatedStorage(previousGeneration);
        if (wasAuthenticated || document.querySelector("[data-account-removal-receipt]")) {
            notifyOtherTabs(previousGeneration);
        }
    }

    if (initialExpiry && authenticatedPage()) monitorSessionExpiry(initialExpiry);

    function monitorSessionExpiry(initial) {
        const [generation] = initial.split(":");
        const sampleClock = () => ({ wall: Date.now(), monotonic: performance.now() });
        let expiry;
        let expiryTimer;
        let refreshTimer;
        let refreshing = false;

        let confirmGeneration = false;
        try {
            const ended = window.localStorage.getItem(SESSION_ENDED_KEY)?.split(":")[0];
            if (ended === generation) {
                endBrowserSession("/auth/login", generation);
                return;
            }
            const current = window.localStorage.getItem(BROWSER_GENERATION_KEY);
            confirmGeneration = Boolean((current || ended) && current !== generation);
        } catch (_) {
            // The timer remains active without browser storage.
        }

        function publishGeneration() {
            try {
                if (window.sessionStorage.getItem(TAB_GENERATION_KEY) !== generation) {
                    clearStorage("sessionStorage", isDraftKey);
                    window.sessionStorage.setItem(TAB_GENERATION_KEY, generation);
                }
                const current = window.localStorage.getItem(BROWSER_GENERATION_KEY);
                if (current && current !== generation) clearStorage("localStorage", isDraftKey);
                window.localStorage.setItem(BROWSER_GENERATION_KEY, generation);
            } catch (_) {
                // The timer remains active without browser storage.
            }
        }

        function active() {
            return document.documentElement.dataset.braidplaneSessionEnded !== "true";
        }

        function timeLeft() {
            const now = sampleClock();
            return expiry.remaining - Math.max(now.wall - expiry.wall, now.monotonic - expiry.monotonic);
        }

        function expireWhenDue() {
            if (!active()) return;
            const remaining = timeLeft();
            if (remaining <= 0) {
                endBrowserSession("/auth/login", generation);
                return;
            }
            clearTimeout(expiryTimer);
            expiryTimer = setTimeout(expireWhenDue, remaining);
        }

        function observe(value, sampledAt, propagate = true) {
            if (expiry && sampledAt.monotonic < expiry.monotonic) return false;
            const [observedGeneration, valueMs] = (value || "").split(":");
            const remaining = Number(valueMs);
            if (!Number.isSafeInteger(remaining) || remaining <= 0) return false;
            if (observedGeneration !== generation) {
                endBrowserSession("/auth/login", generation);
                return false;
            }
            expiry = { ...sampledAt, remaining };
            expireWhenDue();
            clearTimeout(refreshTimer);
            const delay = timeLeft();
            if (delay > 6000) refreshTimer = setTimeout(() => refresh(), delay - 5000);
            if (propagate && delay > 0 && !confirmGeneration) {
                try {
                    const deadline = Math.floor(sampledAt.wall + remaining);
                    window.localStorage.setItem(SESSION_EXPIRY_KEY, `${generation}:${deadline}`);
                } catch (_) {
                    // Other tabs still observe their own deadline.
                }
            }
            return true;
        }

        async function refresh(propagate = true) {
            if (!active() || refreshing || !nativeFetch) return;
            const sampledAt = sampleClock();
            let previousBrowserGeneration;
            try {
                previousBrowserGeneration = window.localStorage.getItem(BROWSER_GENERATION_KEY);
            } catch (_) {
                // Another tab cannot publish a generation without storage.
            }
            refreshing = true;
            let observed = false;
            try {
                const response = await nativeFetch("/auth/session-status", {
                    cache: "no-store",
                    signal: AbortSignal.timeout(4000),
                });
                if (!active()) return;
                if (response.status === 401 && response.headers.get(SESSION_ENDED_HEADER) === "1") {
                    notifyOtherTabs(generation);
                    endBrowserSession("/auth/login", generation);
                } else if (response.status === 204) {
                    if (
                        confirmGeneration &&
                        window.localStorage.getItem(BROWSER_GENERATION_KEY) !== previousBrowserGeneration
                    )
                        return;
                    observed = observe(response.headers.get(SESSION_EXPIRY_HEADER), sampledAt, propagate);
                    if (observed && confirmGeneration && active()) {
                        confirmGeneration = false;
                        publishGeneration();
                    }
                }
            } catch (_) {
                // The last confirmed deadline still applies offline.
            } finally {
                refreshing = false;
                if (!observed && active() && expiry) {
                    const delay = Math.min(5000, timeLeft() - 1000);
                    if (delay >= 1000) {
                        clearTimeout(refreshTimer);
                        refreshTimer = setTimeout(() => refresh(propagate), delay);
                    }
                }
            }
        }

        const initialSample = sampleClock();
        const navigation = performance.getEntriesByType?.("navigation")?.[0];
        if (navigation?.requestStart > 0) {
            initialSample.wall -= initialSample.monotonic - navigation.requestStart;
            initialSample.monotonic = navigation.requestStart;
        }
        if (!confirmGeneration) publishGeneration();
        observe(initial, initialSample, false);
        if (!expiry || !active()) return;
        if (confirmGeneration) refresh();
        document.addEventListener("visibilitychange", () => {
            if (document.visibilityState !== "visible") return;
            expireWhenDue();
            refresh();
        });
        window.addEventListener("online", () => refresh());
        window.addEventListener("storage", (event) => {
            if (event.key !== SESSION_EXPIRY_KEY || !event.newValue || !active()) return;
            const [updatedGeneration] = event.newValue.split(":");
            if (updatedGeneration === generation) refresh(false);
        });
    }

    document.addEventListener(
        "submit",
        (event) => {
            const form = event.target;
            if (!(form instanceof HTMLFormElement) || form.method !== "post" || event.defaultPrevented) return;
            const action = new URL(form.action);
            if (action.origin !== window.location.origin || action.pathname !== "/auth/logout") return;
            clearAuthenticatedStorage(pageGeneration, true);
            discardAuthenticatedPage(form);
        },
        true,
    );

    window.addEventListener("storage", (event) => {
        if (!event.newValue || !authenticatedPage()) return;
        if (event.key === SESSION_ENDED_KEY) {
            const [endedGeneration] = event.newValue.split(":");
            if (!endedGeneration || !pageGeneration || endedGeneration === pageGeneration) endBrowserSession();
        }
        if (event.key === BROWSER_GENERATION_KEY && pageGeneration && event.newValue !== pageGeneration) {
            endBrowserSession("/auth/login", pageGeneration);
        }
    });

    window.addEventListener("pageshow", (event) => {
        if (!event.persisted || !authenticatedPage()) return;
        discardAuthenticatedPage();
        window.location.reload();
    });

    document.addEventListener("htmx:beforeOnLoad", (event) => {
        if (document.documentElement.dataset.braidplaneSessionEnded === "true") {
            event.preventDefault();
            return;
        }
        const xhr = event.detail?.xhr;
        if (xhr?.getResponseHeader(SESSION_ENDED_HEADER) !== "1") return;
        event.preventDefault();
        notifyOtherTabs();
        endBrowserSession(xhr.getResponseHeader("HX-Redirect"));
    });

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

    if (nativeFetch) {
        window.fetch = (input, init) => {
            const nextInit = init ? { ...init } : {};
            const method = String(nextInit.method || (input instanceof Request ? input.method : "GET")).toUpperCase();
            const sameOrigin = sameOriginRequest(input);
            if (!SAFE_METHODS.has(method) && sameOrigin) {
                nextInit.headers = headersWithCSRF(
                    nextInit.headers || (input instanceof Request ? input.headers : undefined),
                );
            }
            const credentials = nextInit.credentials ?? (input instanceof Request ? input.credentials : "same-origin");
            return nativeFetch(input, nextInit).then((response) => {
                if (
                    sameOrigin &&
                    credentials !== "omit" &&
                    authenticatedPage() &&
                    response.headers.get(SESSION_ENDED_HEADER) === "1" &&
                    sameOriginRequest(response.url)
                ) {
                    notifyOtherTabs();
                    endBrowserSession(response.redirected ? response.url : "/auth/login");
                }
                return response;
            });
        };
    }
})();
