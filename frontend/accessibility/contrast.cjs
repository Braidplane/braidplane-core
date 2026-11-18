async function inspectTextContrast(page, targets) {
    const samples = await page.evaluate((selectors) => {
        const canvas = document.createElement("canvas");
        const context = canvas.getContext("2d");
        const samples = [];
        for (const selector of selectors) {
            const before = samples.length;
            const root = document.querySelector(selector);
            if (!root) throw new Error(`Contrast target disappeared: ${selector}`);
            const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
            for (let node = walker.nextNode(); node; node = walker.nextNode()) {
                if (!node.textContent.trim()) continue;
                const element = node.parentElement;
                if (!element.checkVisibility() || element.closest(":disabled, [aria-disabled='true']")) continue;
                const style = getComputedStyle(element);
                const range = document.createRange();
                range.selectNodeContents(node);
                const rects = Array.from(range.getClientRects(), (rect) => ({
                    left: rect.left,
                    top: rect.top,
                    right: rect.right,
                    bottom: rect.bottom,
                }));
                let unsupported = "";
                const visibleShadow =
                    style.textShadow !== "none" &&
                    style.textShadow.split(/,(?![^(]*\))/).some((shadow) => {
                        const color = shadow
                            .trim()
                            .match(/^([a-z-]+\([^()]*\))\s+[-.\d]+px\s+[-.\d]+px\s+[.\d]+px$/)?.[1];
                        if (!color) return true;
                        const alpha =
                            color.match(/\/\s*([\d.]+)%?\)$/)?.[1] || color.match(/^rgba\(.*,\s*([\d.]+)\)$/)?.[1];
                        return Number(alpha) !== 0;
                    });
                if (
                    element instanceof SVGElement ||
                    visibleShadow ||
                    Number.parseFloat(style.webkitTextStrokeWidth) > 0 ||
                    style.backgroundClip.includes("text")
                ) {
                    unsupported = "SVG, text shadows, strokes or gradient text require visual review";
                }
                let scale = 1;
                for (let ancestor = element; ancestor; ancestor = ancestor.parentElement) {
                    const ancestorStyle = getComputedStyle(ancestor);
                    if (ancestorStyle.transform !== "none") {
                        const matrix = new DOMMatrix(ancestorStyle.transform);
                        if (!matrix.is2D || matrix.b !== 0 || matrix.c !== 0) {
                            unsupported = "Rotated or 3D text requires visual review";
                        } else {
                            scale *= Math.abs(matrix.d);
                        }
                    }
                    if (
                        Number(ancestorStyle.opacity) !== 1 ||
                        ancestorStyle.filter !== "none" ||
                        ancestorStyle.mixBlendMode !== "normal"
                    ) {
                        unsupported = "Opacity, filters or blending require visual review";
                    }
                    const clip = ancestor.getBoundingClientRect();
                    for (const rect of rects) {
                        if (ancestorStyle.overflowX !== "visible") {
                            rect.left = Math.max(rect.left, clip.left);
                            rect.right = Math.min(rect.right, clip.right);
                        }
                        if (ancestorStyle.overflowY !== "visible") {
                            rect.top = Math.max(rect.top, clip.top);
                            rect.bottom = Math.min(rect.bottom, clip.bottom);
                        }
                    }
                    // Top-layer boxes escape their DOM ancestors' clipping and effects.
                    if (ancestor.matches(":modal, :popover-open")) break;
                }
                context.clearRect(0, 0, 1, 1);
                context.fillStyle = style.webkitTextFillColor || style.color;
                context.fillRect(0, 0, 1, 1);
                const color = Array.from(context.getImageData(0, 0, 1, 1).data);
                const size = Number.parseFloat(style.fontSize) * scale;
                const large = size >= 24 || (size >= 18.667 && Number(style.fontWeight) >= 700);
                samples.push({
                    selector,
                    color,
                    required: large ? 3 : 4.5,
                    unsupported,
                    rects: rects
                        .filter((rect) => rect.right > rect.left && rect.bottom > rect.top)
                        .map((rect) => ({
                            left: rect.left + scrollX,
                            top: rect.top + scrollY,
                            right: rect.right + scrollX,
                            bottom: rect.bottom + scrollY,
                        })),
                });
            }
            if (samples.length === before) {
                samples.push({
                    selector,
                    color: [],
                    required: null,
                    rects: [],
                    unsupported: "No rendered HTML text sampled; input, pseudo or SVG content needs review",
                });
            }
        }
        return samples;
    }, targets);
    if (!samples.length) return [];

    // Hide glyphs without changing currentColor backgrounds, geometry or layout.
    const background = await page.screenshot({
        fullPage: true,
        scale: "css",
        animations: "disabled",
        style: "* { -webkit-text-fill-color: transparent !important; text-shadow: none !important; }",
    });
    const decoder = await page.context().newPage();
    try {
        return await decoder.evaluate(
            async ({ png, samples }) => {
                const image = new Image();
                image.src = `data:image/png;base64,${png}`;
                await image.decode();
                const canvas = document.createElement("canvas");
                canvas.width = image.width;
                canvas.height = image.height;
                const context = canvas.getContext("2d");
                context.drawImage(image, 0, 0);
                const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
                const luminance = (rgb) =>
                    rgb
                        .map((channel) => {
                            const value = channel / 255;
                            return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
                        })
                        .reduce((total, value, index) => total + value * [0.2126, 0.7152, 0.0722][index], 0);
                return samples.map(({ selector, color, required, unsupported, rects }) => {
                    let minimum = Infinity;
                    let maximum = 0;
                    for (const rect of rects) {
                        for (
                            let y = Math.max(0, Math.ceil(rect.top));
                            y < Math.min(canvas.height, Math.floor(rect.bottom));
                            y++
                        ) {
                            for (
                                let x = Math.max(0, Math.ceil(rect.left));
                                x < Math.min(canvas.width, Math.floor(rect.right));
                                x++
                            ) {
                                const offset = (y * canvas.width + x) * 4;
                                const backdrop = Array.from(pixels.subarray(offset, offset + 3));
                                const foreground = backdrop.map(
                                    (channel, index) => channel + ((color[index] - channel) * color[3]) / 255,
                                );
                                const a = luminance(foreground);
                                const b = luminance(backdrop);
                                const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
                                minimum = Math.min(minimum, ratio);
                                maximum = Math.max(maximum, ratio);
                            }
                        }
                    }
                    const status =
                        unsupported || maximum === 0
                            ? "review"
                            : maximum < required
                              ? "fail"
                              : minimum >= required
                                ? "pass"
                                : "review";
                    return {
                        selector,
                        color,
                        required,
                        minimum: Number.isFinite(minimum) ? minimum : null,
                        maximum,
                        status,
                        reason:
                            unsupported ||
                            (maximum === 0
                                ? "No unclipped pixels sampled"
                                : status === "review"
                                  ? "Contrast bounds cross the threshold; inspect actual glyph backgrounds"
                                  : ""),
                    };
                });
            },
            { png: background.toString("base64"), samples },
        );
    } finally {
        await decoder.close();
    }
}

module.exports = { inspectTextContrast };
