/// <reference lib="deno.ns" />

/**
 * Cross-runtime tests for the SDK under Deno.
 *
 * Imports directly from source to verify full Deno compatibility.
 *
 * Run: deno test --no-check -A src/compat/deno_test.ts
 */

import {
  assertEquals,
  assertExists,
  assertRejects,
} from "https://deno.land/std@0.224.0/assert/mod.ts";

import {
  createSandbox,
  connectSandbox,
  resolveEndpoints,
  DEFAULT_ENDPOINT,
  SandboxError,
  ConnectionError,
  RpcError,
  TimeoutError,
  FsError,
  CapacityError,
  SessionReplacedError,
  templates,
} from "../index.ts";
import { resolveAuth } from "../auth.ts";

// ── DEFAULT_ENDPOINT ────────────────────────────────────────────

Deno.test("DEFAULT_ENDPOINT is unix socket", () => {
  assertEquals(DEFAULT_ENDPOINT, "unix:///var/run/sandboxd/sandboxd.sock");
});

// ── resolveEndpoints ────────────────────────────────────────────

Deno.test("resolveEndpoints: unix:// returns socket path", () => {
  const r = resolveEndpoints("unix:///var/run/sandboxd/sandboxd.sock");
  assertEquals(r.socketPath, "/var/run/sandboxd/sandboxd.sock");
  assertEquals(r.http, "http://localhost");
  assertEquals(r.ws, "ws://localhost");
});

Deno.test("resolveEndpoints: unix:// with custom path", () => {
  const r = resolveEndpoints("unix:///tmp/custom.sock");
  assertEquals(r.socketPath, "/tmp/custom.sock");
});

Deno.test("resolveEndpoints: http:// passes through without socketPath", () => {
  const r = resolveEndpoints("http://192.168.1.10:7522");
  assertEquals(r.socketPath, undefined);
  assertEquals(r.http, "http://192.168.1.10:7522");
  assertEquals(r.ws, "ws://192.168.1.10:7522");
});

Deno.test("resolveEndpoints: https:// converts to wss://", () => {
  const r = resolveEndpoints("https://api.byggflow.com");
  assertEquals(r.socketPath, undefined);
  assertEquals(r.http, "https://api.byggflow.com");
  assertEquals(r.ws, "wss://api.byggflow.com");
});

Deno.test("resolveEndpoints: trailing slash stripped", () => {
  const r = resolveEndpoints("http://localhost:7522/");
  assertEquals(r.http, "http://localhost:7522");
  assertEquals(r.ws, "ws://localhost:7522");
});

Deno.test("resolveEndpoints: http and ws protocols are consistent", () => {
  const cases = [
    "http://localhost:7522",
    "https://api.example.com",
    "http://10.0.0.1:8080",
  ];
  for (const endpoint of cases) {
    const { http, ws } = resolveEndpoints(endpoint);
    if (http.startsWith("https://")) {
      assertEquals(ws.startsWith("wss://"), true);
    } else {
      assertEquals(ws.startsWith("ws://"), true);
    }
  }
});

// ── Exports ─────────────────────────────────────────────────────

Deno.test("public API surface is complete", () => {
  assertEquals(typeof createSandbox, "function");
  assertEquals(typeof connectSandbox, "function");
  assertEquals(typeof resolveEndpoints, "function");
  assertEquals(typeof templates, "function");
  assertExists(SandboxError);
  assertExists(ConnectionError);
  assertExists(RpcError);
  assertExists(TimeoutError);
  assertExists(FsError);
  assertExists(CapacityError);
  assertExists(SessionReplacedError);
});

// ── Error classes ───────────────────────────────────────────────

Deno.test("SandboxError is an Error", () => {
  const err = new SandboxError("test");
  assertEquals(err instanceof Error, true);
  assertEquals(err instanceof SandboxError, true);
  assertEquals(err.name, "SandboxError");
  assertEquals(err.message, "test");
});

Deno.test("ConnectionError extends SandboxError", () => {
  const err = new ConnectionError("disconnected");
  assertEquals(err instanceof SandboxError, true);
  assertEquals(err instanceof ConnectionError, true);
  assertEquals(err.name, "ConnectionError");
});

Deno.test("RpcError carries error code", () => {
  const err = new RpcError("method not found", -32601);
  assertEquals(err instanceof SandboxError, true);
  assertEquals(err.code, -32601);
  assertEquals(err.name, "RpcError");
});

Deno.test("TimeoutError extends SandboxError", () => {
  const err = new TimeoutError("timed out");
  assertEquals(err instanceof SandboxError, true);
  assertEquals(err.name, "TimeoutError");
});

Deno.test("FsError carries filesystem error code", () => {
  const err = new FsError("file not found", "ENOENT");
  assertEquals(err instanceof SandboxError, true);
  assertEquals(err.code, "ENOENT");
  assertEquals(err.name, "FsError");
});

Deno.test("CapacityError carries retryAfter", () => {
  const err = new CapacityError("service unavailable", 2);
  assertEquals(err instanceof SandboxError, true);
  assertEquals(err.retryAfter, 2);
  assertEquals(err.name, "CapacityError");
});

Deno.test("SessionReplacedError extends ConnectionError", () => {
  const err = new SessionReplacedError();
  assertEquals(err instanceof ConnectionError, true);
  assertEquals(err instanceof SandboxError, true);
  assertEquals(err.name, "SessionReplacedError");
});

Deno.test("error hierarchy allows catching by base class", () => {
  const errors = [
    new ConnectionError("conn"),
    new RpcError("rpc", -1),
    new TimeoutError("timeout"),
    new FsError("fs", "EACCES"),
    new CapacityError("cap", 5),
    new SessionReplacedError(),
  ];
  for (const err of errors) {
    assertEquals(err instanceof SandboxError, true);
  }
});

// ── Auth ────────────────────────────────────────────────────────

Deno.test("undefined auth returns empty headers", async () => {
  const resolve = resolveAuth(undefined);
  const headers = await resolve();
  assertEquals(headers, {});
});

Deno.test("string auth returns Bearer token", async () => {
  const resolve = resolveAuth("sk-abc123");
  const headers = await resolve();
  assertEquals(headers, { Authorization: "Bearer sk-abc123" });
});

Deno.test("object auth returns headers as-is", async () => {
  const resolve = resolveAuth({ "X-API-Key": "abc", "X-Workspace": "ws-1" });
  const headers = await resolve();
  assertEquals(headers, { "X-API-Key": "abc", "X-Workspace": "ws-1" });
});

Deno.test("function auth calls provider", async () => {
  let callCount = 0;
  const resolve = resolveAuth(async () => {
    callCount++;
    return { Authorization: `Bearer token-${callCount}` };
  });

  const h1 = await resolve();
  assertEquals(h1, { Authorization: "Bearer token-1" });

  const h2 = await resolve();
  assertEquals(h2, { Authorization: "Bearer token-2" });
  assertEquals(callCount, 2);
});

// ── Connection behavior ─────────────────────────────────────────

Deno.test({ name: "createSandbox and connectSandbox reject without daemon", sanitizeResources: false, sanitizeOps: false, fn: async () => {
  await assertRejects(() => createSandbox({ endpoint: "http://127.0.0.1:19999" }));
  await assertRejects(() => connectSandbox("sbx-fake", { endpoint: "http://127.0.0.1:19999" }));
}});
