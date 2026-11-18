const { test, expect } = require("@playwright/test");
const { inspectTextContrast } = require("./contrast.cjs");

test("top-layer contrast ignores outside clipping and effects but keeps inner clipping", async ({ page }) => {
    await page.setContent(`<style>
        body { height: 2600px; background: white; }
        #outside { position: absolute; top: 1800px; left: 20px; width: 30px; height: 20px;
            overflow: hidden; opacity: .2; transform: scale(.5); filter: blur(2px); }
        dialog, [popover] { position: fixed; inset: 100px auto auto 80px; margin: 0;
            width: 240px; padding: 12px; border: 0; color: white; background: black; font: 28px monospace; }
        .crop { height: 1px; overflow: hidden; }
        .crop p { margin-top: 50px; }
    </style><div id="outside">
        <dialog id="modal"><p id="modal-text">Dialog text</p><div class="crop"><p id="modal-cropped">Clipped text</p></div></dialog>
        <div id="popup" popover><p id="popup-text">Popover text</p><div class="crop"><p id="popup-cropped">Clipped text</p></div></div>
    </div>`);
    for (const kind of ["modal", "popup"]) {
        const layer = page.locator(`#${kind}`);
        await layer.evaluate((element) =>
            element instanceof HTMLDialogElement ? element.showModal() : element.showPopover(),
        );
        for (const scroll of [0, 900]) {
            await page.evaluate((top) => window.scrollTo(0, top), scroll);
            const [sample, clipped] = await inspectTextContrast(page, [`#${kind}-text`, `#${kind}-cropped`]);
            expect(sample).toMatchObject({ status: "pass", required: 3 });
            expect(sample.minimum).toBeCloseTo(21, 1);
            expect(clipped).toMatchObject({ status: "review", reason: "No unclipped pixels sampled" });
        }
        await layer.evaluate((element) => {
            element.style.opacity = ".5";
        });
        expect((await inspectTextContrast(page, [`#${kind}-text`]))[0].reason).toContain("Opacity");
        await layer.evaluate((element) => {
            element.style.opacity = "1";
            if (element instanceof HTMLDialogElement) element.close();
            else element.hidePopover();
        });
    }
    await page.locator("#modal").evaluate((element) => element.show());
    expect((await inspectTextContrast(page, ["#modal-text"]))[0].reason).toContain("Opacity");
});

test("rendered contrast distinguishes passing, failing and mixed gradient backgrounds", async ({ page }) => {
    await page.setContent(`<style>
        p { margin: 0; font: 16px monospace; padding: 4px; }
        #pass { color: white; background: linear-gradient(black, #111); }
        #fail { color: #333; background: linear-gradient(black, #111); }
        #mixed { color: #777; background: linear-gradient(90deg, black, white); }
        #alpha { color: rgb(255 255 255 / 50%); background: black; }
        #opacity { opacity: .5; color: white; background: black; }
        #scaled { font-size: 32px; transform: scale(.5); transform-origin: top left; color: #777; background: black; }
        #shadow { color: white; background: black; text-shadow: 0 1px black; }
        #faint-shadow { color: white; background: black; text-shadow: 0 1px rgb(0 0 0 / .01); }
        #transparent-shadow { color: white; background: black; text-shadow: 0 .5px oklch(1 0 0 / 0); }
        #mixed-shadows { color: white; background: black; text-shadow: 0 .5px transparent, 0 1px rgb(0 0 0 / .2); }
        #clear-shadows { color: #333; background: black; text-shadow: 0 1px transparent, 0 1px rgb(255 0 0 / 0); }
        #large { font: 700 19px monospace; color: #777; background: #111; }
    </style><p id="pass"><span id="nested">Readable text</span></p><p id="fail">Low contrast text</p>
    <p id="transparent-shadow">Transparent shadow</p><p id="mixed-shadows">Review visible shadow</p>
    <p id="faint-shadow">Review a faint shadow</p>
    <p id="clear-shadows">Low contrast with transparent shadows</p>
    <p id="mixed">${"Mixed background ".repeat(8)}</p><p id="alpha">Translucent text</p>
    <p id="opacity">Requires review</p><p id="scaled">Scaled small text</p><input id="input" placeholder="Review input"/>
    <p id="shadow">Review text shadow</p><p id="large">Large bold text</p>`);
    const results = await inspectTextContrast(page, [
        "#pass",
        "#nested",
        "#fail",
        "#mixed",
        "#alpha",
        "#opacity",
        "#scaled",
        "#input",
        "#shadow",
        "#large",
        "#transparent-shadow",
        "#mixed-shadows",
        "#clear-shadows",
        "#faint-shadow",
    ]);
    expect(results).toHaveLength(14);
    expect(results.find(({ selector }) => selector === "#nested").status).toBe("pass");
    expect(results.filter(({ selector }) => selector === "#pass").every(({ status }) => status === "pass")).toBe(true);
    expect(results.find(({ selector }) => selector === "#fail").status).toBe("fail");
    expect(results.find(({ selector }) => selector === "#mixed").status).toBe("review");
    expect(results.find(({ selector }) => selector === "#mixed").reason).toContain("bounds cross the threshold");
    expect(results.find(({ selector }) => selector === "#alpha").minimum).toBeCloseTo(5.32, 1);
    expect(results.find(({ selector }) => selector === "#opacity").status).toBe("review");
    expect(results.find(({ selector }) => selector === "#scaled").required).toBe(4.5);
    expect(results.find(({ selector }) => selector === "#input").status).toBe("review");
    expect(results.find(({ selector }) => selector === "#shadow").status).toBe("review");
    expect(results.find(({ selector }) => selector === "#faint-shadow").status).toBe("review");
    expect(results.find(({ selector }) => selector === "#transparent-shadow").status).toBe("pass");
    expect(results.find(({ selector }) => selector === "#mixed-shadows").status).toBe("review");
    expect(results.find(({ selector }) => selector === "#clear-shadows").status).toBe("fail");
    expect(results.find(({ selector }) => selector === "#large")).toMatchObject({ required: 3, status: "pass" });
});
