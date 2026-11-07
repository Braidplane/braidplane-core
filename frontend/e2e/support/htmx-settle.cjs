const { randomUUID } = require("node:crypto");

async function withHTMXSettle(page, { pathname, params = {} }, action) {
    const key = `e2e-htmx-${randomUUID()}`;
    await page.evaluate(
        ({ key, pathname, params }) => {
            const state = { done: false, status: null };
            const listener = (event) => {
                const xhr = event.detail?.xhr;
                if (!xhr?.responseURL) return;
                const url = new URL(xhr.responseURL);
                if (url.pathname !== pathname) return;
                if (Object.entries(params).some(([name, value]) => url.searchParams.get(name) !== value)) return;
                state.status = xhr.status;
                state.done = true;
            };
            state.cleanup = () => {
                document.removeEventListener("htmx:afterSettle", listener);
                document.removeEventListener("htmx:responseError", listener);
                delete window[key];
            };
            window[key] = state;
            document.addEventListener("htmx:afterSettle", listener);
            document.addEventListener("htmx:responseError", listener);
        },
        { key, pathname, params },
    );
    try {
        await action();
        await page.waitForFunction((key) => window[key]?.done, key, { timeout: 5_000 });
        const status = await page.evaluate((key) => window[key].status, key);
        if (status < 200 || status >= 400) throw new Error(`HTMX ${pathname} failed (${status})`);
    } catch (error) {
        throw new Error(`HTMX ${pathname} did not settle successfully`, { cause: error });
    } finally {
        await page.evaluate((key) => window[key]?.cleanup(), key);
    }
}

module.exports = { withHTMXSettle };
