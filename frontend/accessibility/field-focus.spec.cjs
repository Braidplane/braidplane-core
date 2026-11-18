const { test, expect } = require("@playwright/test");
const { withSSRFixture } = require("../e2e/support/ssr-fixture.cjs");
const { tabTo, visit } = require("./support.cjs");

test.use({ storageState: { cookies: [], origins: [] } });

async function inspectIndicator(page, target, edge, indicator) {
    await target.scrollIntoViewIfNeeded();
    if (indicator === "outline") {
        await tabTo(page, target);
        if (edge === "bottom") {
            await target.evaluate((element) => element.scrollIntoView({ block: "center", behavior: "instant" }));
        }
    } else {
        await expect(target).not.toBeFocused();
    }
    await target.evaluate((element) =>
        Promise.allSettled(element.getAnimations({ subtree: true }).map((animation) => animation.finished)),
    );
    const rendering = await target.evaluate(
        (element, { edge, indicator }) => {
            const style = getComputedStyle(element);
            const rect = element.getBoundingClientRect();
            const borderWidth = Number.parseFloat(edge === "bottom" ? style.borderBottomWidth : style.borderLeftWidth);
            return {
                left: rect.left,
                top: rect.top,
                width: rect.width,
                height: rect.height,
                paint:
                    indicator === "outline" ? style.outline : edge === "bottom" ? style.borderBottom : style.borderLeft,
                offset: indicator === "outline" ? Number.parseFloat(style.outlineOffset) : -borderWidth,
                thickness: indicator === "outline" ? Number.parseFloat(style.outlineWidth) : borderWidth,
                focused: element.matches(":focus-visible"),
            };
        },
        { edge, indicator },
    );
    expect(rendering.focused).toBe(indicator === "outline");
    expect(rendering.thickness).toBeGreaterThanOrEqual(1);
    const png = await page.screenshot({ scale: "css", animations: "disabled" });
    const decoder = await page.context().newPage();
    try {
        return await decoder.evaluate(
            async ({ png, rendering: r, edge, indicator }) => {
                const image = new Image();
                image.src = `data:image/png;base64,${png}`;
                await image.decode();
                const canvas = document.createElement("canvas");
                canvas.width = image.width;
                canvas.height = image.height;
                const context = canvas.getContext("2d");
                context.drawImage(image, 0, 0);
                const x = Math.floor(edge === "bottom" ? r.left + r.width / 2 : r.left - r.offset - r.thickness / 2);
                const y = Math.floor(
                    edge === "bottom" ? r.top + r.height + r.offset + r.thickness / 2 : r.top + r.height / 2,
                );
                const outsideX = edge === "bottom" ? x : Math.floor(r.left - r.offset - r.thickness - 2);
                const outsideY = edge === "bottom" ? Math.floor(r.top + r.height + r.offset + r.thickness + 2) : y;
                const insideX = edge === "bottom" ? x : Math.floor(r.left - r.offset + 1);
                const insideY = edge === "bottom" ? Math.floor(r.top + r.height + r.offset - 1) : y;
                if (
                    Math.min(x, y, outsideX, outsideY, insideX, insideY) < 0 ||
                    Math.max(x, outsideX, insideX) >= image.width ||
                    Math.max(y, outsideY, insideY) >= image.height
                ) {
                    throw new Error(`${indicator} sample lies outside the viewport: ${JSON.stringify(r)}`);
                }
                const pixel = (x, y) => Array.from(context.getImageData(x, y, 1, 1).data).slice(0, 3);
                const ring = pixel(x, y);
                const outside = pixel(outsideX, outsideY);
                const inside = pixel(insideX, insideY);
                const luminance = (rgb) =>
                    rgb
                        .map((channel) => channel / 255)
                        .reduce(
                            (sum, value, index) =>
                                sum +
                                (value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4) *
                                    [0.2126, 0.7152, 0.0722][index],
                            0,
                        );
                const a = luminance(ring);
                const ratios = [outside, inside].map((color) => {
                    const b = luminance(color);
                    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
                });
                return {
                    edge,
                    ring,
                    outside,
                    inside,
                    ratio: indicator === "outline" ? Math.min(...ratios) : ratios[0],
                    rendering: r,
                };
            },
            { png: png.toString("base64"), rendering, edge, indicator },
        );
    } finally {
        await decoder.close();
    }
}

test("field keyboard focus contrasts with its adjacent background", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PIPELINE_GIT_FIXTURE_BIN, async (fixture) => {
        const samples = [];
        const boundaries = [];
        const check = async (selector, edge = "left") => {
            for (const spacing of [false, true]) {
                const override = spacing
                    ? await page.addStyleTag({
                          content:
                              "* { line-height: 1.5 !important; letter-spacing: .12em !important; word-spacing: .16em !important; } p { margin-bottom: 2em !important; }",
                      })
                    : null;
                const field = page.locator(selector);
                if (edge === "left") {
                    if (await field.evaluate((element) => element === document.activeElement)) {
                        await page.keyboard.press("Tab");
                    }
                    const boundary = await inspectIndicator(page, field, edge, "border");
                    boundaries.push({ selector, spacing, viewport: page.viewportSize(), ...boundary });
                    expect.soft(boundary.ratio, `${selector} boundary, spacing=${spacing}`).toBeGreaterThanOrEqual(3);
                }
                const sample = await inspectIndicator(page, field, edge, "outline");
                samples.push({ selector, spacing, viewport: page.viewportSize(), ...sample });
                expect.soft(sample.ratio, `${selector}, spacing=${spacing}`).toBeGreaterThanOrEqual(3);
                if (override) await override.evaluate((element) => element.remove());
            }
        };
        await visit(page, `${fixture.url}/auth/login`);
        await check('input[name="email"]');
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        expect((await page.request.post(`${fixture.url}/_test/builder-composition`)).ok()).toBe(true);
        await visit(
            page,
            `${fixture.url}/builder?org_slug=acme&project_slug=ops&pipeline_slug=daily-sync&target_environment_slug=dev`,
        );
        await check("#environment-switcher");
        await tabTo(page, page.getByRole("button", { name: /^Transform Step normalize/ }));
        await page.keyboard.press("Enter");
        await check("#builder-field-transform-sql");
        await check("#builder-field-transform-sql", "bottom");
        await testInfo.attach("field-boundary-contrast", {
            body: JSON.stringify({ samples: boundaries }),
            contentType: "application/json",
        });
        await testInfo.attach("field-focus-contrast", {
            body: JSON.stringify({ samples }),
            contentType: "application/json",
        });
    });
});

test("checkbox boundaries and focus contrast with their background", async ({ page }, testInfo) => {
    await withSSRFixture(process.env.E2E_PROOF_RECEIPT_FIXTURE_BIN, async (fixture) => {
        expect((await page.request.get(`${fixture.url}/_test/session`)).ok()).toBe(true);
        const cases = [
            ["/organizations/acme/connections", /allow Git writes/i],
            ["/orgs/acme/data-sources/github/proof", "Enable Proof assessments"],
        ];
        const samples = [];
        for (const [path, name] of cases) {
            await visit(page, `${fixture.url}${path}`);
            const checkbox = page.getByRole("checkbox", { name });
            await expect(checkbox).not.toBeChecked();
            const boundary = await inspectIndicator(page, checkbox, "left", "border");
            const focused = await inspectIndicator(page, checkbox, "left", "outline");
            await page.keyboard.press("Space");
            await expect(checkbox).toBeChecked();
            const checked = await inspectIndicator(page, checkbox, "left", "outline");
            samples.push({ path, boundary, focused, checked });
            for (const sample of [boundary, focused, checked]) {
                expect.soft(sample.ratio, path).toBeGreaterThanOrEqual(3);
            }
        }
        await testInfo.attach("checkbox-indicators", {
            contentType: "application/json",
            body: JSON.stringify(samples),
        });
    });
});
