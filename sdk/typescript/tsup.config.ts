import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  dts: true,
  clean: true,
  target: "node18",
  splitting: true,
  sourcemap: true,
  // Keep node: imports external so unix.ts doesn't break browser builds.
  // The dynamic import() of unix.ts ensures these are only loaded when needed.
  external: ["node:http", "node:crypto", "node:net"],
});
