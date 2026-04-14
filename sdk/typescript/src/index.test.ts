import { test, expect } from "vitest";
import * as sdk from "./index.ts";

test("public API surface is complete", () => {
  // Functions
  expect(typeof sdk.createSandbox).toBe("function");
  expect(typeof sdk.connectSandbox).toBe("function");
  expect(typeof sdk.resolveEndpoints).toBe("function");
  expect(typeof sdk.templates).toBe("function");
  expect(typeof sdk.signatureAuth).toBe("function");

  // Constants
  expect(sdk.DEFAULT_ENDPOINT).toBe("unix:///var/run/sandboxd/sandboxd.sock");

  // Error classes
  expect(sdk.SandboxError).toBeDefined();
  expect(sdk.ConnectionError).toBeDefined();
  expect(sdk.RpcError).toBeDefined();
  expect(sdk.TimeoutError).toBeDefined();
  expect(sdk.FsError).toBeDefined();
  expect(sdk.CapacityError).toBeDefined();
  expect(sdk.SessionReplacedError).toBeDefined();

  // Transport
  expect(sdk.WsTransport).toBeDefined();
});
