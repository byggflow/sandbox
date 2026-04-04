import { test, expect } from "vitest";
import { resolveAuth } from "./auth.ts";

test("undefined auth returns empty headers", async () => {
  const resolve = resolveAuth(undefined);
  const headers = await resolve();
  expect(headers).toEqual({});
});

test("string auth returns Bearer token", async () => {
  const resolve = resolveAuth("sk-abc123");
  const headers = await resolve();
  expect(headers).toEqual({ Authorization: "Bearer sk-abc123" });
});

test("object auth returns headers as-is", async () => {
  const resolve = resolveAuth({ "X-API-Key": "abc", "X-Workspace": "ws-1" });
  const headers = await resolve();
  expect(headers).toEqual({ "X-API-Key": "abc", "X-Workspace": "ws-1" });
});

test("function auth calls provider", async () => {
  let callCount = 0;
  const resolve = resolveAuth(async () => {
    callCount++;
    return { Authorization: `Bearer token-${callCount}` };
  });

  const h1 = await resolve();
  expect(h1).toEqual({ Authorization: "Bearer token-1" });

  const h2 = await resolve();
  expect(h2).toEqual({ Authorization: "Bearer token-2" });
  expect(callCount).toBe(2);
});
