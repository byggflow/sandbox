import { test, expect, describe } from "vitest";
import { readFileSync } from "node:fs";
import { resolveHeaders, resetDaemon } from "./connection.ts";

describe("MCP connection resolution", () => {
  test("resolveHeaders returns empty object for undefined auth", async () => {
    const headers = await resolveHeaders(undefined);
    expect(headers).toEqual({});
  });

  test("resolveHeaders returns Bearer token for string auth", async () => {
    const headers = await resolveHeaders("sk-test-123");
    expect(headers).toEqual({ Authorization: "Bearer sk-test-123" });
  });

  test("MCP DEFAULT_HTTP is TCP fallback for Docker mode", () => {
    const src = readFileSync(new URL("./connection.ts", import.meta.url), "utf-8");
    const match = src.match(/const DEFAULT_HTTP\s*=\s*"([^"]+)"/);
    expect(match).not.toBeNull();
    expect(match![1]).toBe("http://localhost:7522");
  });

  test("MCP DEFAULT_SOCKET matches daemon socket path", () => {
    const src = readFileSync(new URL("./connection.ts", import.meta.url), "utf-8");
    const match = src.match(/const DEFAULT_SOCKET\s*=\s*"([^"]+)"/);
    expect(match).not.toBeNull();
    expect(match![1]).toBe("/var/run/sandboxd/sandboxd.sock");
  });

  test("ensureDaemon tries Unix socket before TCP", () => {
    const src = readFileSync(new URL("./connection.ts", import.meta.url), "utf-8");
    // Find the ensureDaemon function body to check ordering within it
    const fnStart = src.indexOf("async function ensureDaemon");
    expect(fnStart).toBeGreaterThan(-1);
    const fnBody = src.slice(fnStart);
    const socketIdx = fnBody.indexOf("socketHealthCheck");
    const tcpIdx = fnBody.indexOf("Fall back to TCP");
    expect(socketIdx).toBeGreaterThan(-1);
    expect(tcpIdx).toBeGreaterThan(-1);
    expect(socketIdx).toBeLessThan(tcpIdx);
  });
});

describe("daemon connection management", () => {
  test("resetDaemon is exported", () => {
    expect(typeof resetDaemon).toBe("function");
    // Should not throw when called without an active connection.
    resetDaemon();
  });

  test("daemonFetch routes through Unix socket when socketPath is set", () => {
    const src = readFileSync(new URL("./connection.ts", import.meta.url), "utf-8");
    // daemonFetch should check daemon.socketPath and use socketFetch for socket connections
    expect(src).toContain("function daemonFetch(");
    expect(src).toContain("daemon.socketPath");
    expect(src).toContain("socketFetch(daemon.socketPath");
  });

  test("downstream API calls use daemonFetch, not global fetch", () => {
    const src = readFileSync(new URL("./connection.ts", import.meta.url), "utf-8");
    // After ensureDaemon, API functions should use daemonFetch instead of fetch(conn.httpBase + ...)
    const afterEnsure = src.slice(src.indexOf("/** List sandboxes"));
    const globalFetchCalls = afterEnsure.match(/\bfetch\(`\$\{conn\.httpBase\}/g);
    expect(globalFetchCalls).toBeNull();
  });
});

describe("protocol alignment", () => {
  test("SDK and MCP agree on default socket path", () => {
    const mcpSrc = readFileSync(new URL("./connection.ts", import.meta.url), "utf-8");
    const sdkSrc = readFileSync(
      new URL("../../sdk/typescript/src/sandbox.ts", import.meta.url),
      "utf-8",
    );

    // MCP DEFAULT_SOCKET
    const mcpMatch = mcpSrc.match(/const DEFAULT_SOCKET\s*=\s*"([^"]+)"/);
    expect(mcpMatch).not.toBeNull();

    // SDK DEFAULT_ENDPOINT
    const sdkMatch = sdkSrc.match(/DEFAULT_ENDPOINT\s*=\s*"unix:\/\/([^"]+)"/);
    expect(sdkMatch).not.toBeNull();

    expect(mcpMatch![1]).toBe(sdkMatch![1]);
  });
});
