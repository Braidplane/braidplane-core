// Record origins only: request URLs/bodies, credentials and session artifacts stay private.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { chromium } = require('/repo/frontend/node_modules/playwright');

let phase = 'startup';
(async () => {
    const base = 'https://app.customer.test';
    const passwords = JSON.parse(fs.readFileSync('/fixture/passwords.json', 'utf8'));
    const browser = await chromium.launch({ headless: true });
    try {
        const context = await browser.newContext();
        const origins = new Set();
        const unexpected = new Set();
        await context.route('**/*', async (route) => {
            const url = new URL(route.request().url());
            if (!['http:', 'https:'].includes(url.protocol)) return route.continue();
            origins.add(url.origin);
            if (url.origin !== base) {
                unexpected.add(url.origin);
                return route.abort();
            }
            return route.continue();
        });
        const page = await context.newPage();
        phase = 'customer-CA navigation';
        const response = await page.goto(base + '/auth/login');
        assert.equal(response.status(), 200);
        assert.equal(await page.getByText('Continue with Google', { exact: true }).count(), 0);
        assert.equal(await page.locator('a[href="/auth/signup"]').count(), 0);
        assert.equal(await page.locator('a[href*="/auth/google"]').count(), 0);
        phase = 'form login';
        await page.locator('input[name="email"]').fill('owner@customer.test');
        await page.locator('input[name="password"]').fill(passwords.replacement);
        await Promise.all([
            page.waitForURL((url) => url.pathname !== '/auth/login'),
            page.getByRole('button', { name: 'Sign in', exact: true }).click(),
        ]);
        phase = 'authenticated profile';
        const profile = await page.evaluate(async () => {
            const response = await fetch('/api/users/profile');
            return { status: response.status, body: await response.json() };
        });
        assert.equal(profile.status, 200);
        assert.equal(profile.body.slug, 'customer-owner');
        phase = 'cookies and captured origins';
        const cookies = await context.cookies(base);
        assert(cookies.length > 0);
        for (const cookie of cookies) {
            assert(cookie.secure && cookie.httpOnly && cookie.sameSite === 'Lax');
        }
        assert.deepEqual([...unexpected], [], 'browser attempted an unexpected network origin');
        assert.deepEqual([...origins], [base]);
        console.log('PASS: Chromium customer-CA login, protected cookies, authenticated profile and same-origin request capture');
        console.log(JSON.stringify({ observedOrigins: [...origins], unexpectedOrigins: [...unexpected] }));
    } finally {
        await browser.close();
    }
})().catch(() => {
    // Playwright errors can include page content; keep credential-bearing diagnostics out of receipts.
    console.error('FAIL: isolated browser acceptance at ' + phase);
    process.exitCode = 1;
});
