const { expect } = require("@playwright/test");
const { AxeBuilder } = require("@axe-core/playwright");
const { inspectTextContrast } = require("./contrast.cjs");

async function inspectState(page, testInfo, state) {
    await page.evaluate(() => document.fonts.ready);
    const result = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
        .analyze();
    const layout = await page.evaluate(() => ({
        documentWidth: document.documentElement.scrollWidth,
        viewportWidth: window.innerWidth,
    }));
    const contrast = await inspectTextContrast(
        page,
        result.incomplete
            .filter(({ id }) => id === "color-contrast")
            .flatMap(({ nodes }) => nodes)
            .filter(({ target }) => target.length === 1)
            .map(({ target }) => target[0]),
    );
    // Retain targets and diagnostics without input HTML or fixture credentials.
    const findings = (rules) =>
        rules.map(({ id, impact, tags, help, helpUrl, nodes }) => ({
            id,
            impact,
            tags,
            help,
            helpUrl,
            nodes: nodes.map(({ target, failureSummary }) => ({ target, failureSummary })),
        }));
    await testInfo.attach(state, {
        body: JSON.stringify(
            {
                state,
                path: new URL(page.url()).pathname,
                browser: page.context().browser().version(),
                viewport: page.viewportSize(),
                layout,
                contrast,
                engine: result.testEngine,
                violations: findings(result.violations),
                incomplete: findings(result.incomplete),
                passes: result.passes.map(({ id }) => id),
                inapplicable: result.inapplicable.map(({ id }) => id),
            },
            null,
            2,
        ),
        contentType: "application/json",
    });
    expect
        .soft(
            result.violations.map(({ id, nodes }) => ({ id, targets: nodes.map(({ target }) => target) })),
            state,
        )
        .toEqual([]);
    expect.soft(layout.documentWidth, `${state}: document reflow`).toBeLessThanOrEqual(layout.viewportWidth + 1);
    const clippedBadges = await page.locator(".builder-node-badge").evaluateAll((badges) =>
        badges.flatMap((badge, index) => {
            const card = badge.closest(".builder-node-content").getBoundingClientRect();
            const bounds = badge.getBoundingClientRect();
            return bounds.top < card.top - 1 ||
                bounds.bottom > card.bottom + 1 ||
                bounds.left < card.left - 1 ||
                bounds.right > card.right + 1
                ? [index]
                : [];
        }),
    );
    expect.soft(clippedBadges, `${state}: badges fit their node cards`).toEqual([]);
    expect
        .soft(
            contrast.filter(({ status }) => status === "fail"),
            `${state}: rendered text contrast`,
        )
        .toEqual([]);
}

async function assess(page, testInfo, state) {
    await inspectState(page, testInfo, state);
    const spacing = await page.addStyleTag({
        content: `
        * { line-height: 1.5 !important; letter-spacing: .12em !important; word-spacing: .16em !important; }
        p { margin-bottom: 2em !important; }
    `,
    });
    try {
        await inspectState(page, testInfo, `${state}-text-spacing`);
    } finally {
        await spacing.evaluate((element) => element.remove());
    }
}

async function visit(page, path) {
    const response = await page.goto(path);
    expect(response.ok(), path).toBe(true);
    expect(new URL(page.url()).pathname).toBe(new URL(path, "http://localhost").pathname);
    await expect(page.locator("main")).toBeVisible();
}

async function tabTo(page, control) {
    for (
        let index = 0;
        index < 150 && !(await control.evaluate((element) => element === document.activeElement));
        index++
    ) {
        await page.keyboard.press("Tab");
    }
    await expect(control).toBeFocused();
}

module.exports = { assess, visit, tabTo };
