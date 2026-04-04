import { test, expect } from "vitest";
import {
  SandboxError,
  ConnectionError,
  RpcError,
  TimeoutError,
  FsError,
  CapacityError,
  SessionReplacedError,
} from "./errors.ts";

test("SandboxError is an Error", () => {
  const err = new SandboxError("test");
  expect(err).toBeInstanceOf(Error);
  expect(err).toBeInstanceOf(SandboxError);
  expect(err.name).toBe("SandboxError");
  expect(err.message).toBe("test");
});

test("ConnectionError extends SandboxError", () => {
  const err = new ConnectionError("disconnected");
  expect(err).toBeInstanceOf(SandboxError);
  expect(err).toBeInstanceOf(ConnectionError);
  expect(err.name).toBe("ConnectionError");
});

test("RpcError carries error code", () => {
  const err = new RpcError("method not found", -32601);
  expect(err).toBeInstanceOf(SandboxError);
  expect(err.code).toBe(-32601);
  expect(err.name).toBe("RpcError");
});

test("TimeoutError extends SandboxError", () => {
  const err = new TimeoutError("timed out");
  expect(err).toBeInstanceOf(SandboxError);
  expect(err.name).toBe("TimeoutError");
});

test("FsError carries filesystem error code", () => {
  const err = new FsError("file not found", "ENOENT");
  expect(err).toBeInstanceOf(SandboxError);
  expect(err.code).toBe("ENOENT");
  expect(err.name).toBe("FsError");
});

test("CapacityError carries retryAfter", () => {
  const err = new CapacityError("service unavailable", 2);
  expect(err).toBeInstanceOf(SandboxError);
  expect(err.retryAfter).toBe(2);
  expect(err.name).toBe("CapacityError");
});

test("SessionReplacedError extends ConnectionError", () => {
  const err = new SessionReplacedError();
  expect(err).toBeInstanceOf(ConnectionError);
  expect(err).toBeInstanceOf(SandboxError);
  expect(err.name).toBe("SessionReplacedError");
});

test("error hierarchy allows catching by base class", () => {
  const errors = [
    new ConnectionError("conn"),
    new RpcError("rpc", -1),
    new TimeoutError("timeout"),
    new FsError("fs", "EACCES"),
    new CapacityError("cap", 5),
    new SessionReplacedError(),
  ];

  for (const err of errors) {
    expect(err).toBeInstanceOf(SandboxError);
  }
});
