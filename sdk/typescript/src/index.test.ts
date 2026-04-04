import { test, expect } from "vitest";
import * as sdk from "./index.ts";

test("exports createSandbox", () => {
  expect(typeof sdk.createSandbox).toBe("function");
});

test("exports connectSandbox", () => {
  expect(typeof sdk.connectSandbox).toBe("function");
});

test("exports templates", () => {
  expect(typeof sdk.templates).toBe("function");
});

test("exports error classes", () => {
  expect(sdk.SandboxError).toBeDefined();
  expect(sdk.ConnectionError).toBeDefined();
  expect(sdk.RpcError).toBeDefined();
  expect(sdk.TimeoutError).toBeDefined();
  expect(sdk.FsError).toBeDefined();
  expect(sdk.CapacityError).toBeDefined();
  expect(sdk.SessionReplacedError).toBeDefined();
});

test("createSandbox rejects without a running daemon", async () => {
  await expect(sdk.createSandbox()).rejects.toThrow();
});

test("connectSandbox rejects without a running daemon", async () => {
  await expect(sdk.connectSandbox("sbx-123")).rejects.toThrow();
});

test("templates returns a TemplateManager", () => {
  const mgr = sdk.templates();
  expect(typeof mgr.list).toBe("function");
  expect(typeof mgr.get).toBe("function");
  expect(typeof mgr.delete).toBe("function");
});
