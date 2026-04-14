/// <reference lib="deno.ns" />

/**
 * Cross-runtime tests for the MCP connection module under Deno.
 *
 * Run: deno test --no-check -A src/compat/deno_test.ts
 */

import {
  assertEquals,
  assertNotEquals,
} from "https://deno.land/std@0.224.0/assert/mod.ts";

import { resolveHeaders, resetDaemon } from "../connection.ts";
import { resolveEndpoints } from "@byggflow/sandbox";

// ── resolveHeaders ──────────────────────────────────────────────

Deno.test("resolveHeaders returns empty object for undefined auth", async () => {
  const headers = await resolveHeaders(undefined);
  assertEquals(headers, {});
});

Deno.test("resolveHeaders returns Bearer token for string auth", async () => {
  const headers = await resolveHeaders("sk-test-123");
  assertEquals(headers, { Authorization: "Bearer sk-test-123" });
});

// ── Daemon management ───────────────────────────────────────────

Deno.test("resetDaemon is exported and callable", () => {
  assertEquals(typeof resetDaemon, "function");
  resetDaemon(); // should not throw
});

// ── Protocol alignment ──────────────────────────────────────────

Deno.test("MCP DEFAULT_HTTP is TCP fallback for Docker mode", () => {
  const src = Deno.readTextFileSync(
    new URL("../connection.ts", import.meta.url),
  );
  const match = src.match(/const DEFAULT_HTTP\s*=\s*"([^"]+)"/);
  assertNotEquals(match, null);
  assertEquals(match![1], "http://localhost:7522");
});

Deno.test("MCP DEFAULT_SOCKET matches SDK default endpoint", () => {
  const src = Deno.readTextFileSync(
    new URL("../connection.ts", import.meta.url),
  );
  const match = src.match(/const DEFAULT_SOCKET\s*=\s*"([^"]+)"/);
  assertNotEquals(match, null);
  assertEquals(match![1], "/var/run/sandboxd/sandboxd.sock");

  // SDK default endpoint should resolve to the same socket path.
  const resolved = resolveEndpoints("unix:///var/run/sandboxd/sandboxd.sock");
  assertEquals(resolved.socketPath, match![1]);
});
