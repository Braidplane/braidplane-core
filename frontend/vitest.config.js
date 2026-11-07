import { defineConfig } from "vitest/config";

export default defineConfig({
    test: {
        environment: "jsdom",
        include: ["assets/js/tests/**/*.test.js"],
    },
});
