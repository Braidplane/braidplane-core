(() => {
    const dashboardOnboardingStorageKeyPrefix = "braidplane.dashboard.onboarding.hidden";

    window.braidplaneRunFragmentSection = (fragment) => {
        switch (fragment) {
            case "#run-steps":
            case "#run-outputs":
            case "#run-lineage":
            case "#definition-inspector":
                return "steps";
            case "#run-attempts":
                return "attempts";
            case "#run-outcome":
            case "#run-metadata":
                return "overview";
            default:
                return "";
        }
    };

    function restoreRunFragment() {
        if (!document.querySelector("[data-run-section-compat]")) return;
        const section = window.braidplaneRunFragmentSection(window.location.hash);
        if (!section || document.getElementById(window.location.hash.slice(1))) return;
        const url = new URL(window.location.href);
        url.searchParams.set("section", section);
        window.location.replace(url.href);
    }

    window.addEventListener("hashchange", restoreRunFragment);

    function braidplaneApplySidebarState() {
        const layout = document.getElementById("shell-layout");
        const toggle = document.getElementById("sidebar-toggle");
        if (!(layout instanceof HTMLElement) || !(toggle instanceof HTMLButtonElement)) {
            return;
        }

        let collapsed = false;
        try {
            collapsed = window.localStorage.getItem("braidplane.sidebar.collapsed") === "1";
        } catch (_) {
            collapsed = false;
        }

        layout.setAttribute("data-sidebar-state", collapsed ? "collapsed" : "expanded");
        toggle.setAttribute("aria-pressed", collapsed ? "true" : "false");
        toggle.setAttribute("aria-label", collapsed ? "Expand sidebar" : "Collapse sidebar");
        toggle.setAttribute("title", collapsed ? "Expand sidebar" : "Collapse sidebar");
    }

    function braidplaneToggleSidebar() {
        let collapsed = false;
        try {
            collapsed = window.localStorage.getItem("braidplane.sidebar.collapsed") === "1";
            window.localStorage.setItem("braidplane.sidebar.collapsed", collapsed ? "0" : "1");
        } catch (_) {
            collapsed = false;
        }
        braidplaneApplySidebarState();
    }

    function braidplaneApplyDashboardOnboardingState() {
        const onboarding = document.querySelector("[data-dashboard-onboarding]");
        if (!(onboarding instanceof HTMLElement)) {
            return;
        }

        try {
            onboarding.hidden =
                window.localStorage.getItem(braidplaneDashboardOnboardingStorageKey(onboarding)) === "1";
        } catch (_) {
            onboarding.hidden = false;
        }
        const resume = document.querySelector("[data-dashboard-onboarding-resume]");
        if (resume instanceof HTMLElement) resume.hidden = !onboarding.hidden;
    }

    function braidplaneDashboardOnboardingStorageKey(onboarding) {
        const scope = onboarding.dataset.dashboardOnboardingScope || "default";
        return `${dashboardOnboardingStorageKeyPrefix}.${scope}`;
    }

    function braidplaneDismissDashboardOnboarding() {
        const onboarding = document.querySelector("[data-dashboard-onboarding]");
        if (!(onboarding instanceof HTMLElement)) {
            return;
        }

        try {
            window.localStorage.setItem(braidplaneDashboardOnboardingStorageKey(onboarding), "1");
        } catch (_) {
            // The checklist can still be hidden for the current page.
        }
        onboarding.hidden = true;
        const resume = document.querySelector("[data-dashboard-onboarding-resume]");
        if (resume instanceof HTMLElement) resume.hidden = false;
    }

    function braidplaneShowDashboardOnboarding() {
        const onboarding = document.querySelector("[data-dashboard-onboarding]");
        if (!(onboarding instanceof HTMLElement)) return;
        try {
            window.localStorage.removeItem(braidplaneDashboardOnboardingStorageKey(onboarding));
        } catch (_) {
            // Resume still works for the current page when storage is unavailable.
        }
        onboarding.hidden = false;
        const resume = document.querySelector("[data-dashboard-onboarding-resume]");
        if (resume instanceof HTMLElement) resume.hidden = true;
        onboarding.querySelector("[data-dashboard-onboarding-dismiss]")?.focus();
    }

    /**
     * @param {string} rawHref
     * @returns {string}
     */
    function braidplaneSameOriginPath(rawHref) {
        var parsed;
        if (typeof rawHref !== "string" || rawHref.trim() === "") {
            return "";
        }
        try {
            parsed = new URL(rawHref, window.location.origin);
            if (parsed.origin !== window.location.origin) {
                return "";
            }
            return parsed.pathname + parsed.search + parsed.hash;
        } catch (_) {
            return "";
        }
    }

    function focusBindingFragment() {
        const id = window.location.hash.slice(1);
        if (!id.startsWith("binding-cell-")) return;
        const target = document.getElementById(id);
        if (target instanceof HTMLElement && target.closest("#pipeline-binding-setup")) {
            target.focus();
        }
    }

    window.addEventListener("hashchange", focusBindingFragment);

    document.addEventListener("DOMContentLoaded", () => {
        restoreRunFragment();
        focusBindingFragment();
        braidplaneApplySidebarState();
        braidplaneApplyDashboardOnboardingState();
        const toggle = document.getElementById("sidebar-toggle");
        if (toggle instanceof HTMLButtonElement) {
            toggle.addEventListener("click", braidplaneToggleSidebar);
        }
        const onboardingDismiss = document.querySelector("[data-dashboard-onboarding-dismiss]");
        if (onboardingDismiss instanceof HTMLButtonElement) {
            onboardingDismiss.addEventListener("click", braidplaneDismissDashboardOnboarding);
        }
        const onboardingShow = document.querySelector("[data-dashboard-onboarding-show]");
        if (onboardingShow instanceof HTMLButtonElement) {
            onboardingShow.addEventListener("click", braidplaneShowDashboardOnboarding);
        }
    });

    function setOrgSwitcherStatus(switcher, message, tone) {
        const container = switcher.closest("[data-org-switcher-root]") || switcher.closest("div") || document;
        const status =
            container.querySelector("[data-org-switcher-status]") ||
            document.querySelector("[data-org-switcher-status]");
        if (!(status instanceof HTMLElement)) {
            return;
        }

        status.textContent = message;
        status.classList.remove("text-error", "text-success", "text-base-content/60");
        if (tone === "error") {
            status.classList.add("text-error");
            return;
        }
        if (tone === "success") {
            status.classList.add("text-success");
            return;
        }
        status.classList.add("text-base-content/60");
    }

    function orgSwitcherFromEventSource(source) {
        if (!(source instanceof Element)) {
            return null;
        }
        if (source.matches("[data-org-switcher]")) {
            return source;
        }
        return source.closest("[data-org-switcher]");
    }

    document.addEventListener("htmx:beforeRequest", (event) => {
        const switcher = orgSwitcherFromEventSource(event.detail?.elt || event.target);
        if (!(switcher instanceof HTMLElement)) {
            return;
        }

        setOrgSwitcherStatus(switcher, "", "neutral");
    });

    document.addEventListener("htmx:responseError", (event) => {
        const switcher = orgSwitcherFromEventSource(event.detail?.elt || event.target);
        if (!(switcher instanceof HTMLElement)) {
            return;
        }

        setOrgSwitcherStatus(switcher, "Could not switch organization. Try again.", "error");
    });

    /**
     * @param {Event} event
     * @returns {undefined}
     */
    function braidplaneHandleRuntimeSwitcherChange(event) {
        var switcher = event.target;
        if (!(switcher instanceof HTMLSelectElement) || !switcher.matches("[data-runtime-switcher]")) {
            return undefined;
        }

        var href = braidplaneSameOriginPath(switcher.value);
        if (href === "") {
            return undefined;
        }

        // Runtime scope also changes navigation outside the main content region.
        window.location.assign(href);
        return undefined;
    }

    document.addEventListener("change", braidplaneHandleRuntimeSwitcherChange);
})();
