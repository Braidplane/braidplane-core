const { execFileSync, execSync } = require("node:child_process");
const { mkdirSync } = require("node:fs");
const { dirname, resolve } = require("node:path");
const { chromium } = require("@playwright/test");

const { resolvePlaywrightBaseURL } = require("../assets/js/playwright_base_url.cjs");
const { isolateE2EAuthClient } = require("./support/auth-throttle-client.cjs");

const authStatePaths = {
    chromium: resolve(__dirname, ".auth", "e2e-user-chromium.json"),
    firefox: resolve(__dirname, ".auth", "e2e-user-firefox.json"),
    webkit: resolve(__dirname, ".auth", "e2e-user-webkit.json"),
};

function extractField(output, key) {
    const re = new RegExp(`^${key}=(.+)$`, "m");
    const match = output.match(re);
    if (!match) {
        throw new Error(`missing ${key} in fake-seed output`);
    }

    return match[1].trim();
}

module.exports = async function globalSetup(config) {
    const started = performance.now();
    const repoRoot = resolve(__dirname, "..", "..");

    if (process.env.E2E_SKIP_FAKE_SEED !== "1") {
        const coreOnly = process.env.BRAIDPLANE_SEED_SCOPE === "core";
        const output = execSync(coreOnly ? "task fake-seed -- --core-only" : "task fake-seed", {
            cwd: repoRoot,
            encoding: "utf8",
            stdio: ["ignore", "pipe", "pipe"],
        });

        process.env.E2E_USER_EMAIL = extractField(output, "owner_user_email");
        process.env.E2E_USER_PASSWORD = extractField(output, "user_password");
        process.env.E2E_ORG_SLUG = extractField(output, "organization_slug");
        process.env.E2E_EMPTY_ORG_SLUG = extractField(output, "empty_organization_slug");
        process.env.E2E_PROJECT_SLUG = extractField(output, "project_slug");
        process.env.E2E_ENVIRONMENT_SLUG = extractField(output, "environment_slug");
        if (!coreOnly) {
            process.env.E2E_HTTP_DATA_SOURCE_SLUG = extractField(output, "http_data_source_slug");
            process.env.E2E_HTTP_QUERY_SLUG = extractField(output, "http_query_slug");
            process.env.E2E_SQL_DATA_SOURCE_SLUG = extractField(output, "sql_data_source_slug");
            process.env.E2E_SQL_QUERY_SLUG = extractField(output, "sql_query_slug");
            process.env.E2E_MYSQL_DATA_SOURCE_SLUG = extractField(output, "mysql_data_source_slug");
            process.env.E2E_MYSQL_QUERY_SLUG = extractField(output, "mysql_query_slug");
            process.env.E2E_MONGO_DATA_SOURCE_SLUG = extractField(output, "mongo_data_source_slug");
            process.env.E2E_MONGO_QUERY_SLUG = extractField(output, "mongo_query_slug");
            process.env.E2E_FTP_DATA_SOURCE_SLUG = extractField(output, "ftp_data_source_slug");
            process.env.E2E_FTP_QUERY_SLUG = extractField(output, "ftp_query_slug");
            process.env.E2E_GIT_DATA_SOURCE_SLUG = extractField(output, "git_data_source_slug");
            process.env.E2E_GIT_QUERY_SLUG = extractField(output, "git_query_slug");
            process.env.E2E_PIPELINE_SLUG = extractField(output, "pipeline_slug");
            process.env.E2E_NOTIFICATION_RECORD_SLUG = extractField(output, "notification_record_slug");
            process.env.E2E_RETENTION_RUN_SLUG = extractField(output, "retention_run_slug");
        }
    }

    if (!process.env.E2E_USER_EMAIL || !process.env.E2E_USER_PASSWORD) {
        throw new Error("E2E credentials are not set; run fake-seed or provide E2E_USER_EMAIL and E2E_USER_PASSWORD.");
    }

    mkdirSync(dirname(authStatePaths.chromium), { recursive: true });
    const httpOAuthFixtureBin = resolve(__dirname, ".auth", "http-oauth-fixture");
    execFileSync(
        "bash",
        ["scripts/go-toolchain.sh", "build", "-o", httpOAuthFixtureBin, "./frontend/e2e/support/http-oauth-fixture"],
        {
            cwd: repoRoot,
            env: process.env,
            stdio: ["ignore", "pipe", "pipe"],
            timeout: 120_000,
            killSignal: "SIGTERM",
        },
    );
    process.env.E2E_HTTP_OAUTH_FIXTURE_BIN = httpOAuthFixtureBin;
    const proofReceiptFixtureBin = resolve(__dirname, ".auth", "proof-receipt-fixture");
    execFileSync(
        "bash",
        [
            "scripts/go-toolchain.sh",
            "build",
            "-o",
            proofReceiptFixtureBin,
            "./frontend/e2e/support/proof-receipt-fixture",
        ],
        {
            cwd: repoRoot,
            env: process.env,
            stdio: ["ignore", "pipe", "pipe"],
            timeout: 120_000,
            killSignal: "SIGTERM",
        },
    );
    process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN = proofReceiptFixtureBin;
    const pipelineGitFixtureBin = resolve(__dirname, ".auth", "pipeline-git-fixture");
    execFileSync(
        "bash",
        [
            "scripts/go-toolchain.sh",
            "build",
            "-o",
            pipelineGitFixtureBin,
            "./frontend/e2e/support/pipeline-git-fixture",
        ],
        {
            cwd: repoRoot,
            env: process.env,
            stdio: ["ignore", "pipe", "pipe"],
            timeout: 120_000,
            killSignal: "SIGTERM",
        },
    );
    process.env.E2E_PIPELINE_GIT_FIXTURE_BIN = pipelineGitFixtureBin;
    const googleSignupFixtureBin = resolve(__dirname, ".auth", "google-signup-fixture");
    execFileSync(
        "bash",
        [
            "scripts/go-toolchain.sh",
            "test",
            "-c",
            "-o",
            googleSignupFixtureBin,
            "./transport/http/e2e/google-signup-fixture",
        ],
        { cwd: repoRoot, env: process.env, stdio: ["ignore", "pipe", "pipe"], timeout: 120_000, killSignal: "SIGTERM" },
    );
    process.env.E2E_GOOGLE_SIGNUP_FIXTURE_BIN = googleSignupFixtureBin;
    process.env.BRAIDPLANE_GOOGLE_SIGNUP_FIXTURE = "1";
    const baseURL =
        config.projects.find((project) => project.use?.baseURL)?.use.baseURL ||
        resolvePlaywrightBaseURL({
            env: process.env,
            envPath: resolve(repoRoot, ".worktree-env"),
        });

    // Each engine owns a session; journeys do not mutate its active organization.
    const browser = await chromium.launch();
    try {
        for (const authStatePath of Object.values(authStatePaths)) {
            const context = await browser.newContext({ baseURL });
            const page = await context.newPage();
            try {
                await isolateE2EAuthClient(page, process.env.E2E_USER_EMAIL);
                await page.goto("/auth/login");
                await page.locator('input[name="email"]').fill(process.env.E2E_USER_EMAIL);
                await page.locator('input[name="password"]').fill(process.env.E2E_USER_PASSWORD);
                await page.getByRole("button", { name: "Sign in" }).click();
                try {
                    await Promise.any([
                        page.waitForURL(/\/dashboard/, { timeout: 10_000 }),
                        page.getByRole("heading", { name: "Organizations" }).waitFor({ timeout: 10_000 }),
                    ]);
                } catch {
                    throw new Error(
                        `seeded user sign-in did not reach dashboard at ${page.url()}: ${(await page.locator("body").innerText()).slice(0, 500)}`,
                    );
                }
                await page.goto("/dashboard");
                await context.storageState({ path: authStatePath });
            } finally {
                await context.close();
            }
        }
    } finally {
        await browser.close();
    }
    config.metadata.setup_wall_ms = performance.now() - started;
};
