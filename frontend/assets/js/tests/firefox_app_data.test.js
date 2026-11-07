import { existsSync } from "node:fs";
import { afterEach, expect, it, vi } from "vitest";
import isolateFirefoxAppData from "../../../e2e/firefox-app-data.cjs";

afterEach(() => vi.unstubAllGlobals());

it("isolates macOS Firefox data and restores the environment on teardown", () => {
    vi.stubGlobal("process", { ...process, platform: "darwin", env: { MOZ_APP_DATA: "/original" } });
    const teardown = isolateFirefoxAppData();
    const directory = process.env.MOZ_APP_DATA;
    try {
        expect(directory).not.toBe("/original");
        expect(process.env.MOZ_LOCAL_APP_DATA).toBe(directory);
        expect(existsSync(directory)).toBe(true);
    } finally {
        teardown();
    }
    expect(existsSync(directory)).toBe(false);
    expect(process.env.MOZ_APP_DATA).toBe("/original");
    expect(process.env.MOZ_LOCAL_APP_DATA).toBeUndefined();
});

it("leaves other platforms unchanged", () => {
    vi.stubGlobal("process", { ...process, platform: "linux", env: { MOZ_APP_DATA: "/original" } });
    expect(isolateFirefoxAppData()).toBeUndefined();
    expect(process.env).toEqual({ MOZ_APP_DATA: "/original" });
});
