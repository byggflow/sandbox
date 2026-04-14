import { test, expect, describe } from "vitest";
import { resolveEndpoints, DEFAULT_ENDPOINT } from "./index.ts";

describe("resolveEndpoints", () => {
  test("default endpoint is unix socket", () => {
    expect(DEFAULT_ENDPOINT).toBe("unix:///var/run/sandboxd/sandboxd.sock");
  });

  test("unix:// returns socket path for direct transport", () => {
    const result = resolveEndpoints("unix:///var/run/sandboxd/sandboxd.sock");
    expect(result.socketPath).toBe("/var/run/sandboxd/sandboxd.sock");
    expect(result.http).toBe("http://localhost");
    expect(result.ws).toBe("ws://localhost");
  });

  test("unix:// with custom path returns that path", () => {
    const result = resolveEndpoints("unix:///tmp/custom.sock");
    expect(result.socketPath).toBe("/tmp/custom.sock");
  });

  test("http:// endpoint passes through without socketPath", () => {
    const result = resolveEndpoints("http://192.168.1.10:7522");
    expect(result.socketPath).toBeUndefined();
    expect(result.http).toBe("http://192.168.1.10:7522");
    expect(result.ws).toBe("ws://192.168.1.10:7522");
  });

  test("https:// endpoint converts to wss://", () => {
    const result = resolveEndpoints("https://api.byggflow.com");
    expect(result.socketPath).toBeUndefined();
    expect(result.http).toBe("https://api.byggflow.com");
    expect(result.ws).toBe("wss://api.byggflow.com");
  });

  test("trailing slash is stripped", () => {
    const result = resolveEndpoints("http://localhost:7522/");
    expect(result.http).toBe("http://localhost:7522");
    expect(result.ws).toBe("ws://localhost:7522");
  });

  test("http and ws protocols are consistent", () => {
    const cases = [
      "http://localhost:7522",
      "https://api.example.com",
      "http://10.0.0.1:8080",
    ];
    for (const endpoint of cases) {
      const { http, ws } = resolveEndpoints(endpoint);
      if (http.startsWith("https://")) {
        expect(ws).toMatch(/^wss:\/\//);
      } else {
        expect(ws).toMatch(/^ws:\/\//);
      }
    }
  });
});
