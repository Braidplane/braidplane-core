const { mkdtempSync, rmSync } = require("node:fs");
const { tmpdir } = require("node:os");
const { join } = require("node:path");

module.exports = function isolateFirefoxAppData() {
    if (process.platform !== "darwin") return;

    // macOS 27 protects the default Firefox app data even with a separate profile.
    // https://bugzilla.mozilla.org/show_bug.cgi?id=2069536
    const directory = mkdtempSync(join(tmpdir(), "braidplane-firefox-"));
    const previous = {
        MOZ_APP_DATA: process.env.MOZ_APP_DATA,
        MOZ_LOCAL_APP_DATA: process.env.MOZ_LOCAL_APP_DATA,
    };
    for (const key of Object.keys(previous)) process.env[key] = directory;

    return () => {
        for (const [key, value] of Object.entries(previous)) {
            if (value === undefined) delete process.env[key];
            else process.env[key] = value;
        }
        rmSync(directory, { recursive: true, force: true });
    };
};
