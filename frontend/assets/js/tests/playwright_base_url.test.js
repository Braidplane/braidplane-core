import { describe, expect, it } from "vitest";

import playwrightBaseURLHelpers from "../playwright_base_url.cjs";

const { readWorktreeEnvValue, resolveMailpitBaseURL, resolvePlaywrightBaseURL } = playwrightBaseURLHelpers;

describe("readWorktreeEnvValue", () => {
    it("ReturnsQuotedValueWithoutQuotes", () => {
        expect(readWorktreeEnvValue("FRONTEND_URL='http://localhost:4123'\n", "FRONTEND_URL")).toBe(
            "http://localhost:4123",
        );
    });

    it("ReturnsEmptyStringWhenKeyIsMissing", () => {
        expect(readWorktreeEnvValue("SERVER_PORT=3001\n", "FRONTEND_URL")).toBe("");
    });
});

describe("resolvePlaywrightBaseURL", () => {
    it("PrefersExplicitE2EBaseURL", () => {
        expect(
            resolvePlaywrightBaseURL({
                env: {
                    E2E_BASE_URL: "http://localhost:9999",
                    FRONTEND_URL: "http://localhost:8888",
                    FRONTEND_SERVER_PORT: "7777",
                },
            }),
        ).toBe("http://localhost:9999");
    });

    it("UsesFrontendURLFromEnvironment", () => {
        expect(
            resolvePlaywrightBaseURL({
                env: {
                    FRONTEND_URL: "http://localhost:56627",
                },
            }),
        ).toBe("http://localhost:56627");
    });

    it("UsesFrontendURLFromWorktreeEnv", () => {
        const envPath = "/tmp/.worktree-env";

        expect(
            resolvePlaywrightBaseURL({
                env: {},
                envPath,
                readFile: (path) => {
                    if (path !== envPath) {
                        throw new Error(`unexpected path ${path}`);
                    }

                    return "FRONTEND_URL=http://localhost:56627\n";
                },
            }),
        ).toBe("http://localhost:56627");
    });

    it("BuildsBaseURLFromFrontendServerPortWhenWorktreeFrontendURLIsMissing", () => {
        const envPath = "/tmp/.worktree-env";

        const readFile = (path) => {
            if (path !== envPath) {
                throw new Error(`unexpected path ${path}`);
            }

            return "FRONTEND_SERVER_PORT=56627\nSERVER_PORT=56625\n";
        };

        expect(
            resolvePlaywrightBaseURL({
                env: {},
                envPath,
                readFile,
            }),
        ).toBe("http://localhost:56627");
    });

    it("FallsBackToStandaloneFrontendDefaultPort", () => {
        expect(resolvePlaywrightBaseURL({ env: {} })).toBe("http://localhost:3002");
    });
});

describe("resolveMailpitBaseURL", () => {
    it("PrefersExplicitMailpitURL", () => {
        expect(
            resolveMailpitBaseURL({
                env: {
                    MAILPIT_URL: "http://localhost:9999",
                    MAILPIT_WEB_PORT: "8888",
                },
            }),
        ).toBe("http://localhost:9999");
    });

    it("UsesMailpitWebPortFromWorktreeEnv", () => {
        const envPath = "/tmp/.worktree-env";

        expect(
            resolveMailpitBaseURL({
                env: {},
                envPath,
                readFile: (path) => {
                    if (path !== envPath) {
                        throw new Error(`unexpected path ${path}`);
                    }

                    return "MAILPIT_WEB_PORT=56628\n";
                },
            }),
        ).toBe("http://localhost:56628");
    });

    it("FallsBackToComposeDefaultMailpitPort", () => {
        expect(resolveMailpitBaseURL({ env: {} })).toBe("http://localhost:38025");
    });
});
