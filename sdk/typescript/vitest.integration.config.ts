import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["src/__integration__/**/*.integration.test.ts"],
    // Run test files sequentially to avoid overwhelming the sandboxd pool.
    fileParallelism: false,
    testTimeout: 30_000,
  },
});
