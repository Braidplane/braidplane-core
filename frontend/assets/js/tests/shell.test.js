import { beforeEach, describe, expect, it } from "vitest";

await import("../shell.js");

beforeEach(() => {
    const storage = new Map();
    Object.defineProperty(window, "localStorage", {
        configurable: true,
        value: {
            clear: () => storage.clear(),
            getItem: (key) => storage.get(key) || null,
            setItem: (key, value) => storage.set(key, String(value)),
            removeItem: (key) => storage.delete(key),
        },
    });
    document.body.innerHTML = "";
    window.localStorage.clear();
});

describe("organization switcher", () => {
    it("SwitchFailure_ShowsVisibleStatusForDropdown", () => {
        document.body.innerHTML = `
            <div data-org-switcher-root>
                <div data-org-switcher>
                    <form method="post" action="/organizations/acme-hq/switch">
                        <button type="submit">Acme HQ</button>
                    </form>
                </div>
                <p data-org-switcher-status aria-live="polite"></p>
            </div>
        `;

        const form = document.querySelector("form");
        form.dispatchEvent(
            new CustomEvent("htmx:responseError", {
                bubbles: true,
                detail: { xhr: { status: 500, responseText: "backend unavailable" } },
            }),
        );

        const status = document.querySelector("[data-org-switcher-status]");
        expect(status.textContent).toContain("Could not switch organization");
        expect(status.classList.contains("text-error")).toBe(true);
    });

    it("SwitchStart_ClearsPreviousStatusForDropdown", () => {
        document.body.innerHTML = `
            <div data-org-switcher-root>
                <div data-org-switcher>
                    <form method="post" action="/organizations/acme-hq/switch">
                        <button type="submit">Acme HQ</button>
                    </form>
                </div>
                <p class="text-error" data-org-switcher-status aria-live="polite">
                    Could not switch organization.
                </p>
            </div>
        `;

        const form = document.querySelector("form");
        form.dispatchEvent(
            new CustomEvent("htmx:beforeRequest", {
                bubbles: true,
                detail: {},
            }),
        );

        const status = document.querySelector("[data-org-switcher-status]");
        expect(status.textContent).toBe("");
        expect(status.classList.contains("text-error")).toBe(false);
        expect(status.classList.contains("text-base-content/60")).toBe(true);
    });
});

describe("sidebar collapse", () => {
    it("SidebarToggle_RestoresAccessibleLabelForCollapsedState", () => {
        window.localStorage.setItem("braidplane.sidebar.collapsed", "1");
        document.body.innerHTML = `
            <div id="shell-layout" data-sidebar-state="expanded"></div>
            <button id="sidebar-toggle" type="button" aria-label="Toggle sidebar"></button>
        `;

        document.dispatchEvent(new Event("DOMContentLoaded"));

        const layout = document.getElementById("shell-layout");
        const toggle = document.getElementById("sidebar-toggle");
        expect(layout.getAttribute("data-sidebar-state")).toBe("collapsed");
        expect(toggle.getAttribute("aria-pressed")).toBe("true");
        expect(toggle.getAttribute("aria-label")).toBe("Expand sidebar");
        expect(toggle.getAttribute("title")).toBe("Expand sidebar");
    });
});

describe("dashboard onboarding", () => {
    it("Resume_RestoresChecklistAndClearsDismissal", () => {
        window.localStorage.setItem("braidplane.dashboard.onboarding.hidden.acme", "1");
        document.body.innerHTML = `
            <div hidden data-dashboard-onboarding-resume>
                <button data-dashboard-onboarding-show>Resume setup</button>
            </div>
            <section data-dashboard-onboarding data-dashboard-onboarding-scope="acme">
                <button data-dashboard-onboarding-dismiss>Hide</button>
            </section>`;
        document.dispatchEvent(new Event("DOMContentLoaded"));
        const onboarding = document.querySelector("[data-dashboard-onboarding]");
        const resume = document.querySelector("[data-dashboard-onboarding-resume]");
        expect(onboarding.hidden).toBe(true);
        expect(resume.hidden).toBe(false);

        document.querySelector("[data-dashboard-onboarding-show]").click();
        expect(onboarding.hidden).toBe(false);
        expect(resume.hidden).toBe(true);
        expect(window.localStorage.getItem("braidplane.dashboard.onboarding.hidden.acme")).toBeNull();
        expect(document.activeElement).toBe(document.querySelector("[data-dashboard-onboarding-dismiss]"));

        document.querySelector("[data-dashboard-onboarding-dismiss]").click();
        expect(onboarding.hidden).toBe(true);
        expect(resume.hidden).toBe(false);
    });

    it("Dismiss_PersistsAndHidesChecklist", () => {
        document.body.innerHTML = `
            <section data-dashboard-onboarding data-dashboard-onboarding-scope="acme">
                <button type="button" data-dashboard-onboarding-dismiss>Hide</button>
            </section>
        `;

        document.dispatchEvent(new Event("DOMContentLoaded"));
        document.querySelector("[data-dashboard-onboarding-dismiss]").click();

        expect(document.querySelector("[data-dashboard-onboarding]").hidden).toBe(true);
        expect(window.localStorage.getItem("braidplane.dashboard.onboarding.hidden.acme")).toBe("1");
    });

    it("Load_HonorsPersistedDismissal", () => {
        window.localStorage.setItem("braidplane.dashboard.onboarding.hidden.acme", "1");
        document.body.innerHTML = `<section data-dashboard-onboarding data-dashboard-onboarding-scope="acme"></section>`;

        document.dispatchEvent(new Event("DOMContentLoaded"));

        expect(document.querySelector("[data-dashboard-onboarding]").hidden).toBe(true);
    });

    it("Load_DoesNotHideAnotherOrganization", () => {
        window.localStorage.setItem("braidplane.dashboard.onboarding.hidden.acme", "1");
        document.body.innerHTML = `<section data-dashboard-onboarding data-dashboard-onboarding-scope="other"></section>`;

        document.dispatchEvent(new Event("DOMContentLoaded"));

        expect(document.querySelector("[data-dashboard-onboarding]").hidden).toBe(false);
    });
});

describe("binding setup return focus", () => {
    it("focuses the requested cell on reload and fragment navigation", () => {
        document.body.innerHTML = `<section id="pipeline-binding-setup">
            <div id="binding-cell-source-dev" tabindex="-1"></div>
            <div id="binding-cell-sink-dev" tabindex="-1"></div>
        </section><button id="other">Other</button>`;
        window.history.replaceState({}, "", "#binding-cell-source-dev");
        document.dispatchEvent(new Event("DOMContentLoaded"));
        expect(document.activeElement.id).toBe("binding-cell-source-dev");
        window.history.replaceState({}, "", "#binding-cell-sink-dev");
        window.dispatchEvent(new Event("hashchange"));
        expect(document.activeElement.id).toBe("binding-cell-sink-dev");
        window.history.replaceState({}, "", "#other");
        window.dispatchEvent(new Event("hashchange"));
        expect(document.activeElement.id).toBe("binding-cell-sink-dev");
        window.history.replaceState({}, "", window.location.pathname);
    });
});

describe("legacy run fragments", () => {
    it("maps old anchors to the composed sections", () => {
        for (const anchor of ["steps", "outputs", "lineage"]) {
            expect(window.braidplaneRunFragmentSection(`#run-${anchor}`)).toBe("steps");
        }
        expect(window.braidplaneRunFragmentSection("#definition-inspector")).toBe("steps");
        expect(window.braidplaneRunFragmentSection("#run-attempts")).toBe("attempts");
        expect(window.braidplaneRunFragmentSection("#run-outcome")).toBe("overview");
        expect(window.braidplaneRunFragmentSection("#run-metadata")).toBe("overview");
        expect(window.braidplaneRunFragmentSection("#unrelated")).toBe("");
    });
});
