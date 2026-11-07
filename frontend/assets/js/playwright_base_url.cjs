const { readFileSync } = require("node:fs");

function stripMatchingQuotes(value) {
    if (value.length < 2) {
        return value;
    }

    const first = value[0];
    const last = value[value.length - 1];
    if ((first === '"' || first === "'") && first === last) {
        return value.slice(1, -1);
    }

    return value;
}

function readWorktreeEnvValue(envContent, key) {
    for (const line of envContent.split("\n")) {
        if (!line.startsWith(`${key}=`)) {
            continue;
        }

        return stripMatchingQuotes(line.slice(key.length + 1).trim());
    }

    return "";
}

function loadWorktreeEnvValue(envPath, key, readFile = readFileSync) {
    try {
        return readWorktreeEnvValue(readFile(envPath, "utf8"), key);
    } catch {
        return "";
    }
}

function resolvePlaywrightBaseURL({ env = process.env, envPath, readFile = readFileSync } = {}) {
    if (env.E2E_BASE_URL) {
        return env.E2E_BASE_URL;
    }

    if (env.FRONTEND_URL) {
        return env.FRONTEND_URL;
    }

    if (envPath) {
        const worktreeFrontendURL = loadWorktreeEnvValue(envPath, "FRONTEND_URL", readFile);
        if (worktreeFrontendURL) {
            return worktreeFrontendURL;
        }

        const worktreeFrontendPort = loadWorktreeEnvValue(envPath, "FRONTEND_SERVER_PORT", readFile);
        if (worktreeFrontendPort) {
            return `http://localhost:${worktreeFrontendPort}`;
        }
    }

    if (env.FRONTEND_SERVER_PORT) {
        return `http://localhost:${env.FRONTEND_SERVER_PORT}`;
    }

    return "http://localhost:3002";
}

function resolveMailpitBaseURL({ env = process.env, envPath, readFile = readFileSync } = {}) {
    if (env.MAILPIT_URL) {
        return env.MAILPIT_URL;
    }

    if (env.MAILPIT_WEB_PORT) {
        return `http://localhost:${env.MAILPIT_WEB_PORT}`;
    }

    if (envPath) {
        const worktreeMailpitPort = loadWorktreeEnvValue(envPath, "MAILPIT_WEB_PORT", readFile);
        if (worktreeMailpitPort) {
            return `http://localhost:${worktreeMailpitPort}`;
        }
    }

    return "http://localhost:38025";
}

module.exports = {
    readWorktreeEnvValue,
    resolveMailpitBaseURL,
    resolvePlaywrightBaseURL,
};
