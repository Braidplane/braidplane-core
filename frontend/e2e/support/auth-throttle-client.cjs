const { createHash } = require("node:crypto");

const runNonce = createHash("sha256").update(`${process.pid}-${Date.now()}-${Math.random()}`).digest("hex");

// isolateE2EAuthClient assigns a stable, disposable client IP for one browser context's auth requests.
async function isolateE2EAuthClient(page, identity) {
    const digest = createHash("sha256").update(`${runNonce}:${identity}`).digest();
    const clientIP = `198.${18 + (digest[0] % 2)}.${digest[1]}.${digest[2]}`;
    await page.context().setExtraHTTPHeaders({ "X-Forwarded-For": clientIP });
}

module.exports = { isolateE2EAuthClient };
