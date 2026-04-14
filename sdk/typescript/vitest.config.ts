import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    exclude: ["src/__integration__/**", "node_modules/**"],
  },
});
