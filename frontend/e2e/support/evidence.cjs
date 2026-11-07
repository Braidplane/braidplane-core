async function captureE2EEvidenceScreenshot(target, testInfo, filename, options = {}) {
    if (process.env.PLAYWRIGHT_E2E_EVIDENCE !== "1" || testInfo.project.name !== "chromium") {
        return;
    }

    await target.screenshot({ path: testInfo.outputPath(filename), ...options });
}

module.exports = { captureE2EEvidenceScreenshot };
