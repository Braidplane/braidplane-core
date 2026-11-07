const { captureE2EEvidenceScreenshot } = require("./evidence.cjs");
const { withHTMXSettle } = require("./htmx-settle.cjs");
const { test, expect } = require("@playwright/test");
const { createHash } = require("node:crypto");
const { resolve } = require("node:path");
const { resolveMailpitBaseURL } = require("../../assets/js/playwright_base_url.cjs");
const { isolateE2EAuthClient } = require("./auth-throttle-client.cjs");

const e2eEmail = process.env.E2E_USER_EMAIL || "";
const e2ePassword = process.env.E2E_USER_PASSWORD || "";
const e2eOrgSlug = process.env.E2E_ORG_SLUG || "";
const e2eProjectSlug = process.env.E2E_PROJECT_SLUG || "data-pipelines";
const e2eEnvironmentSlug = process.env.E2E_ENVIRONMENT_SLUG || "dev";
const e2ePipelineSlug = process.env.E2E_PIPELINE_SLUG || "";
const e2eSQLDataSourceSlug = process.env.E2E_SQL_DATA_SOURCE_SLUG || "";
const mailpitBaseURL = resolveMailpitBaseURL({
    env: process.env,
    envPath: resolve(__dirname, "..", "..", "..", ".worktree-env"),
});
const repoRoot = resolve(__dirname, "..", "..", "..");
const laptopScreenshotViewport = { width: 1512, height: 982 };

function skipSharedAuthLifecycleInNonChromium(testInfo) {
    test.skip(
        testInfo.project.name !== "chromium",
        "Browser auth lifecycle tests share process-local signup/login throttles; Chromium covers this workflow.",
    );
}

async function signInWithCredentials(page, email, password, expectedURL = /\/dashboard/, authClientIdentity = email) {
    // Storage state is a shared authenticated baseline. Start credential-driven
    // lifecycle tests with a new anonymous session so their login cannot reset
    // the server-side session copied into concurrent seeded contexts.
    await page.context().clearCookies();
    await isolateE2EAuthClient(page, authClientIdentity);
    await page.goto("/auth/login");

    await page.locator('input[name="email"]').fill(email);
    await page.locator('input[name="password"]').fill(password);
    const loginResponse = page.waitForResponse((response) => {
        const responseURL = new URL(response.url());
        return responseURL.pathname === "/auth/login" && response.request().method() === "POST";
    });
    await page.getByRole("button", { name: "Sign in" }).click();

    const response = await loginResponse;
    expect(response.status(), `sign-in returned HTTP ${response.status()}`).toBe(303);

    await expect(page).toHaveURL(expectedURL);
}

async function expectAccountTabSearchParam(page, expectedTab) {
    await page.waitForURL((url) => url.pathname === "/account" && url.searchParams.get("tab") === expectedTab);
    expect(new URL(page.url()).searchParams.get("tab")).toBe(expectedTab);
}

async function signUpAndVerify(page, email, password, fullName) {
    await isolateE2EAuthClient(page, email);
    await page.goto("/auth/signup");
    await page.locator('input[name="full_name"]').fill(fullName);
    await page.locator('input[name="email"]').fill(email);
    await page.locator('input[name="password"]').fill(password);
    await page.locator('input[name="confirm_password"]').fill(password);
    const terms = page.locator('form[action="/auth/signup"] input[name="terms_accepted"]');
    if (await terms.count()) await terms.check();
    await page.getByRole("button", { name: "Create account" }).click();
    await expect(page.getByRole("heading", { name: "Verify your email" })).toBeVisible();

    const verificationURL = await waitForMailpitLink(page, email, "/verify-email?token=");
    await page.goto(verificationURL);
    await expect(page.getByText("Your email has been verified. You can now sign in.")).toBeVisible();
}

// Remove only the disposable subject after its owned organization cleanup.
async function deleteE2EAccount(page, email) {
    const profile = await page.request.get("/api/users/profile");
    expect(profile.status()).toBe(200);
    expect((await profile.json()).email).toBe(email);
    const deletion = await unsafeRequest(page, "POST", "/api/users/profile/deletion", {
        data: { confirmation: "DELETE MY ACCOUNT" },
    });
    expect(deletion.status()).toBe(200);
    expect((await deletion.json()).state).toBe("completed");
}

// Organization audit jobs can still be draining when a short UI assertion ends.
async function deleteE2EOrganization(page, organizationSlug) {
    await expect
        .poll(
            async () => {
                const response = await unsafeRequest(page, "DELETE", `/api/organizations/${organizationSlug}`, {
                    data: { confirmation: organizationSlug },
                });
                expect([204, 404, 409]).toContain(response.status());
                return response.status();
            },
            {
                timeout: 15_000,
                intervals: [250, 500, 1_000],
                message: "owned organization processing drains before cleanup",
            },
        )
        .not.toBe(409);
}

async function waitForMailpitLink(page, recipientEmail, pathFragment) {
    let foundLink = "";
    await expect
        .poll(
            async () => {
                const messagesResponse = await page.request.get(`${mailpitBaseURL}/api/v1/messages`);
                if (!messagesResponse.ok()) {
                    return "";
                }
                const messages = await messagesResponse.json();
                for (const message of messages.messages || []) {
                    const recipients = message.To || [];
                    if (!recipients.some((recipient) => recipient.Address === recipientEmail)) {
                        continue;
                    }

                    const messageResponse = await page.request.get(`${mailpitBaseURL}/api/v1/message/${message.ID}`);
                    if (!messageResponse.ok()) {
                        continue;
                    }
                    const detail = await messageResponse.json();
                    const match = String(detail.Text || "").match(/https?:\/\/[^\s]+/);
                    if (match?.[0].includes(pathFragment)) {
                        foundLink = match[0];
                        return foundLink;
                    }
                }
                return "";
            },
            { timeout: 10_000 },
        )
        .not.toBe("");
    return foundLink;
}

async function csrfHeaders(page) {
    const csrfToken = page.locator('meta[name="csrf-token"]');
    if ((await csrfToken.count()) === 0) {
        // Request-only tests begin at about:blank now that normal contexts load
        // their session at creation. Initialize one authenticated page only
        // when a request needs the document-scoped CSRF token.
        await page.goto("/dashboard");
    }

    const token = await csrfToken.getAttribute("content");
    expect(token).toBeTruthy();

    return { "X-CSRF-Token": token };
}

function uniqueE2ESlug(prefix, testInfo = null) {
    const workerIdentity = testInfo
        ? `${testInfo.project.name}-w${testInfo.workerIndex}-r${testInfo.retry}-${createHash("sha256")
              .update(testInfo.testId)
              .digest("hex")
              .slice(0, 8)}`
        : `pid-${process.pid}`;

    return `${prefix}-${workerIdentity}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
        .replace(/[^a-z0-9-]/g, "-")
        .replace(/-+/g, "-")
        .replace(/^-|-$/g, "");
}

async function unsafeRequest(page, method, url, options = {}) {
    const requestMethod = method.toLowerCase();
    return page.request[requestMethod](url, {
        ...options,
        headers: {
            ...(options.headers || {}),
            ...(await csrfHeaders(page)),
        },
    });
}

// deleteE2EQuery removes a disposable query and its execution history after a test failure or success.
async function deleteE2EQuery(page, dataSourceSlug, querySlug, organizationSlug = e2eOrgSlug) {
    const deleteResponse = await unsafeRequest(
        page,
        "DELETE",
        `/api/organizations/${organizationSlug}/data-sources/${dataSourceSlug}/queries/${querySlug}`,
    );
    expect([204, 404]).toContain(deleteResponse.status());

    const purgeResponse = await unsafeRequest(
        page,
        "DELETE",
        `/api/organizations/${organizationSlug}/data-sources/${dataSourceSlug}/queries/${querySlug}/purge`,
    );
    expect([204, 404]).toContain(purgeResponse.status());
}

// deleteE2EAlertPolicy removes a disposable policy before its destination cleanup runs.
async function deleteE2EAlertPolicy(page, policySlug) {
    const response = await unsafeRequest(
        page,
        "DELETE",
        `/api/organizations/${e2eOrgSlug}/alert-policies/${policySlug}`,
    );
    expect([204, 404]).toContain(response.status());
}

// deleteE2EAlertDestination removes a disposable destination and its policy bindings.
async function deleteE2EAlertDestination(page, destinationSlug) {
    const response = await unsafeRequest(
        page,
        "DELETE",
        `/api/organizations/${e2eOrgSlug}/alert-destinations/${destinationSlug}`,
    );
    expect([204, 404]).toContain(response.status());
}

// deleteE2EOrganizationAPIKey revokes an ephemeral key before permanently removing its retained metadata.
async function deleteE2EOrganizationAPIKey(page, apiKeySlug) {
    const revokeResponse = await unsafeRequest(
        page,
        "DELETE",
        `/api/organizations/${e2eOrgSlug}/api-keys/${apiKeySlug}`,
    );
    expect([204, 404]).toContain(revokeResponse.status());

    const deleteResponse = await unsafeRequest(
        page,
        "POST",
        `/api/organizations/${e2eOrgSlug}/api-keys/${apiKeySlug}/delete`,
    );
    expect([204, 404]).toContain(deleteResponse.status());
}

// revokeE2EMemberInvitation invalidates a disposable pending invitation after the assertion completes or fails.
async function revokeE2EMemberInvitation(page, invitationSlug) {
    const response = await unsafeRequest(
        page,
        "POST",
        `/api/organizations/${e2eOrgSlug}/member-invitations/${invitationSlug}/revoke`,
    );
    expect([200, 404]).toContain(response.status());
}

// A failed render may prevent the test from capturing its created invitation slug.
async function revokeE2EMemberInvitationByEmail(page, email) {
    let cursor = "";
    do {
        const params = new URLSearchParams({ status: "pending", limit: "100" });
        if (cursor) params.set("cursor", cursor);
        const response = await page.request.get(`/api/organizations/${e2eOrgSlug}/member-invitations?${params}`);
        expect(response.status()).toBe(200);
        const body = await response.json();
        for (const invitation of body.invitations.filter((invitation) => invitation.email === email)) {
            await revokeE2EMemberInvitation(page, invitation.slug);
        }
        cursor = body.has_more ? body.next_cursor : "";
        if (body.has_more) expect(cursor).toBeTruthy();
    } while (cursor);
}

// removeE2EOrganizationMember removes a disposable accepted membership from the shared seeded organization.
async function removeE2EOrganizationMember(page, userSlug) {
    const response = await unsafeRequest(page, "DELETE", `/api/organizations/${e2eOrgSlug}/members/${userSlug}`);
    expect([204, 404]).toContain(response.status());
}

async function hideBuilderProperties(page) {
    const toggle = page.locator("#builder-toggle-properties-btn");
    if ((await toggle.count()) === 0 || !(await toggle.isVisible())) {
        return;
    }

    await expect
        .poll(async () => toggle.getAttribute("aria-label"), { timeout: 1500, intervals: [100, 200, 300] })
        .toMatch(/^(Hide properties|Show properties)$/);

    const label = await toggle.getAttribute("aria-label");
    if (label === "Hide properties") {
        for (let attempt = 0; attempt < 3; attempt += 1) {
            try {
                await toggle.click({ timeout: 2000 });
            } catch {
                await toggle.click({ force: true });
            }
            try {
                await expect(toggle).toHaveAttribute("aria-label", "Show properties", { timeout: 2000 });
                return;
            } catch {
                // Retry: Chromium occasionally reports the pre-toggle label after a click under load.
            }
        }
        await expect(toggle).toHaveAttribute("aria-label", "Show properties", { timeout: 5000 });
    } else {
        await expect(toggle).toBeVisible();
    }
}

async function captureLaptopViewportScreenshot(page, testInfo, filename) {
    await page.setViewportSize(laptopScreenshotViewport);
    await captureE2EEvidenceScreenshot(page, testInfo, filename);
}

async function clickBuilderNode(page, slug) {
    await hideBuilderProperties(page);
    await page.locator(`.builder-node[data-node-slug="${slug}"] .builder-node-content`).click();
}

async function dragBuilderConnection(page, sourceSlug, targetSlug, targetInputName) {
    const sourcePort = page.locator(`.builder-node[data-node-slug="${sourceSlug}"] .node-port-out`);
    const targetPort = page.locator(`.builder-node[data-node-slug="${targetSlug}"] .node-port-in`);
    await expect(sourcePort).toBeVisible();
    await expect(targetPort).toBeVisible();
    await sourcePort.scrollIntoViewIfNeeded();
    await targetPort.scrollIntoViewIfNeeded();

    const sourceBox = await sourcePort.boundingBox();
    const targetBox = await targetPort.boundingBox();
    if (!sourceBox || !targetBox) {
        throw new Error(`connection ports were not measurable for ${sourceSlug} -> ${targetSlug}`);
    }

    const source = {
        x: sourceBox.x + sourceBox.width / 2,
        y: sourceBox.y + sourceBox.height / 2,
    };
    const target = {
        x: targetBox.x + targetBox.width / 2,
        y: targetBox.y + targetBox.height / 2,
    };

    await page.mouse.move(source.x, source.y);
    await page.mouse.down();
    await page.mouse.move((source.x + target.x) / 2, (source.y + target.y) / 2, { steps: 6 });
    await page.mouse.move(target.x, target.y, { steps: 6 });
    await page.mouse.up();

    await page.locator(`.builder-node[data-node-slug="${targetSlug}"] .builder-node-content`).click();
    await expect(page.locator("#builder-binding-rows")).toBeVisible();
    const hasExpectedBinding = await page.locator("#builder-binding-rows").evaluate(
        (container, expected) =>
            Array.from(container.querySelectorAll("[data-binding-row]")).some((row) => {
                const sourceInput = row.querySelector("[data-binding-source-node]");
                const targetInput = row.querySelector("[data-binding-target-port]");
                return sourceInput?.value === expected.sourceSlug && targetInput?.value === expected.targetInputName;
            }),
        { sourceSlug, targetInputName },
    );
    expect(hasExpectedBinding).toBe(true);
}

async function markBuilderQueryNodeName(page, slug, value) {
    await page.locator(`.builder-node[data-node-slug="${slug}"]`).evaluate(
        (node, next) => {
            if (!(node instanceof HTMLElement)) {
                throw new Error("builder node is not an element");
            }
            node.setAttribute("data-step-name", next.value);
            const labels = node.querySelectorAll(".builder-node-content p");
            if (labels[1] instanceof HTMLElement) {
                labels[1].textContent = next.value;
            }
        },
        { value },
    );
    await page.getByRole("button", { name: "Validate" }).click();
    await expect(page.locator("#builder-validation-list")).toContainText(/no blocking issues/i);
}

function queryDetailPath(orgSlug, dataSourceSlug, querySlug) {
    return `/orgs/${orgSlug}/data-sources/${dataSourceSlug}/queries/${querySlug}`;
}

async function createE2EQuery(page, dataSourceSlug, body, organizationSlug = e2eOrgSlug) {
    const resp = await unsafeRequest(
        page,
        "POST",
        `/api/organizations/${organizationSlug}/data-sources/${dataSourceSlug}/queries`,
        {
            data: body,
        },
    );
    expect(resp.status()).toBe(201);

    return resp.json();
}

async function saveE2EQueryDefinition(page, query) {
    await Promise.all([
        page.waitForEvent("framenavigated", (frame) => frame === page.mainFrame()),
        page.waitForResponse(
            (response) =>
                new URL(response.url()).pathname.endsWith(`/queries/${query}/update`) && response.status() === 200,
        ),
        page.getByRole("button", { name: "Save", exact: true }).click(),
    ]);
    // HX-Redirect starts a new document; response headers and navigation commit precede its load.
    await page.waitForLoadState("load");
}

async function waitForPipelineRunStatus(
    page,
    runSlug,
    expectedStatus,
    { projectSlug = e2eProjectSlug, environmentSlug = e2eEnvironmentSlug, timeout = 15_000 } = {},
) {
    let runBody = null;
    let lastObservedStatus = "not_observed";
    let lastHTTPStatus = null;
    const runURL = `/api/organizations/${e2eOrgSlug}/projects/${projectSlug}/environments/${environmentSlug}/runs/${runSlug}`;
    try {
        await expect
            .poll(
                async () => {
                    const resp = await page.request.get(runURL);
                    lastHTTPStatus = resp.status();
                    if (lastHTTPStatus !== 200) {
                        lastObservedStatus = `http:${lastHTTPStatus}`;
                        return lastObservedStatus;
                    }
                    runBody = await resp.json();
                    lastObservedStatus = runBody.metadata?.status || "missing_status";
                    return lastObservedStatus;
                },
                {
                    timeout,
                    intervals: [250, 500, 1_000],
                    message: `run ${runSlug} in ${projectSlug}/${environmentSlug} reaches a terminal status`,
                },
            )
            .toMatch(/^(succeeded|failed|canceled|cancelled)$/);
    } catch (error) {
        throw new Error(
            `Timed out after ${timeout}ms waiting for run ${runSlug} in project ${projectSlug} and environment ${environmentSlug} to reach a terminal status (expected ${expectedStatus}); last observed status=${lastObservedStatus}, last HTTP status=${lastHTTPStatus ?? "none"}, url=${runURL}`,
            { cause: error },
        );
    }

    expect(
        lastObservedStatus,
        `run ${runSlug} in project ${projectSlug} and environment ${environmentSlug} reached unexpected terminal status`,
    ).toBe(expectedStatus);

    return runBody;
}

// createE2EProject persists a disposable project and enables the seeded runtime environment.
async function createE2EProject(page, projectSlug, organizationSlug = e2eOrgSlug) {
    const createResp = await unsafeRequest(page, "POST", `/api/organizations/${organizationSlug}/projects`, {
        data: {
            name: `E2E Project ${projectSlug}`,
            slug: projectSlug,
        },
    });
    expect(createResp.status()).toBe(201);

    const enableResp = await unsafeRequest(
        page,
        "PUT",
        `/api/organizations/${organizationSlug}/projects/${projectSlug}/environments/${e2eEnvironmentSlug}`,
        { data: { is_default: true } },
    );
    expect(enableResp.status()).toBe(200);
}

// deleteE2EProject removes a disposable project created for binding-requirement tests.
async function deleteE2EProject(page, projectSlug, organizationSlug = e2eOrgSlug) {
    const deleteResp = await unsafeRequest(
        page,
        "DELETE",
        `/api/organizations/${organizationSlug}/projects/${projectSlug}`,
    );
    expect([204, 404]).toContain(deleteResp.status());
}

// waitForAlertPolicySelectorResponse synchronizes dynamic policy selectors with their server response.
function waitForAlertPolicySelectorResponse(page, orgSlug, matchesParams) {
    return page.waitForResponse((response) => {
        const responseURL = new URL(response.url());
        return (
            responseURL.pathname === `/orgs/${orgSlug}/alerts/policies/selector-options` &&
            response.status() < 400 &&
            matchesParams(responseURL.searchParams)
        );
    });
}

// Install the settle listener before the action can dispatch a fast selector response.
async function selectAlertPolicyOption(page, orgSlug, locator, value) {
    const fieldName = await locator.getAttribute("name");
    await withHTMXSettle(
        page,
        { pathname: `/orgs/${orgSlug}/alerts/policies/selector-options`, params: { [fieldName]: value } },
        () => locator.selectOption(value),
    );
}

async function checkAlertPolicyKind(page, orgSlug, form, kind) {
    const policyKind = form.locator(`input[name="policy_kind"][value="${kind}"]`);
    await withHTMXSettle(
        page,
        { pathname: `/orgs/${orgSlug}/alerts/policies/selector-options`, params: { policy_kind: kind } },
        () => policyKind.check(),
    );
    await expect(policyKind).toBeChecked();
}

// assertSeededAuth fails fast when global setup did not provide the shared seeded account.
function assertSeededAuth() {
    if (!e2eEmail || !e2ePassword) {
        throw new Error("E2E credentials are not set; expected global setup to run fake-seed.");
    }

    if (!e2eOrgSlug) {
        throw new Error("E2E_ORG_SLUG is not set; expected global setup to run fake-seed.");
    }
}

module.exports = {
    test,
    expect,
    e2eEmail,
    e2ePassword,
    e2eOrgSlug,
    e2eProjectSlug,
    e2eEnvironmentSlug,
    e2ePipelineSlug,
    e2eSQLDataSourceSlug,
    mailpitBaseURL,
    repoRoot,
    laptopScreenshotViewport,
    skipSharedAuthLifecycleInNonChromium,
    signInWithCredentials,
    expectAccountTabSearchParam,
    signUpAndVerify,
    deleteE2EAccount,
    deleteE2EOrganization,
    waitForMailpitLink,
    csrfHeaders,
    uniqueE2ESlug,
    unsafeRequest,
    deleteE2EQuery,
    deleteE2EAlertPolicy,
    deleteE2EAlertDestination,
    deleteE2EOrganizationAPIKey,
    revokeE2EMemberInvitation,
    revokeE2EMemberInvitationByEmail,
    removeE2EOrganizationMember,
    hideBuilderProperties,
    captureE2EEvidenceScreenshot,
    captureLaptopViewportScreenshot,
    clickBuilderNode,
    dragBuilderConnection,
    markBuilderQueryNodeName,
    queryDetailPath,
    createE2EQuery,
    saveE2EQueryDefinition,
    waitForPipelineRunStatus,
    createE2EProject,
    deleteE2EProject,
    waitForAlertPolicySelectorResponse,
    selectAlertPolicyOption,
    checkAlertPolicyKind,
    assertSeededAuth,
};
