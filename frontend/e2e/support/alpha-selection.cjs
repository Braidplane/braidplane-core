const inventory = require("../alpha-inventory.json");

function browserProfile(env = process.env) {
    const profile = env.BRAIDPLANE_BROWSER_PROFILE || (env.CI ? "" : "critical");
    if (!["critical", "full"].includes(profile)) {
        throw new Error("BRAIDPLANE_BROWSER_PROFILE must be critical or full (required in CI)");
    }
    return profile;
}

function browserGrep(kind, project, profile) {
    if (profile === "full") return undefined;
    const cases = inventory.suites[kind].cases.filter(
        (entry) => entry.tier === "critical" && (project === "chromium" || entry.cross_browser),
    );
    return new RegExp(
        `(?:^| )(?:${cases.map((entry) => entry.title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})$`,
    );
}

module.exports = { browserProfile, browserGrep };
