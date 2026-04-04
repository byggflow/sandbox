import { describe, test, expect, afterEach, beforeAll } from "vitest";

const ENDPOINT = process.env.SANDBOXD_ENDPOINT ?? "";
const skip = !ENDPOINT;

/** Convert http(s) URL to ws(s) URL. */
function wsUrl(httpUrl: string): string {
  return httpUrl.replace(/^http/, "ws");
}

interface SandboxInfo {
  id: string;
  image: string;
  state: string;
  created: string;
  ttl: number;
  profile?: string;
}

interface JsonRpcRequest {
  jsonrpc: "2.0";
  id: number;
  method: string;
  params?: Record<string, unknown>;
}

interface JsonRpcResponse {
  jsonrpc: "2.0";
  id: number;
  result?: unknown;
  error?: { code: number; message: string };
}

/** Create a sandbox via the REST API. */
async function createSandbox(): Promise<SandboxInfo> {
  const resp = await fetch(`${ENDPOINT}/sandboxes`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  });
  if (resp.status !== 201) {
    const text = await resp.text();
    throw new Error(`POST /sandboxes returned ${resp.status}: ${text}`);
  }
  return resp.json() as Promise<SandboxInfo>;
}

/** Destroy a sandbox via the REST API. */
async function destroySandbox(id: string): Promise<void> {
  try {
    await fetch(`${ENDPOINT}/sandboxes/${id}`, { method: "DELETE" });
  } catch {
    // Best effort cleanup.
  }
}

/**
 * Open a WebSocket to a sandbox and provide helpers for JSON-RPC communication.
 * Returns an object with send/receive helpers and a close function.
 */
function connectWS(id: string): Promise<{
  ws: WebSocket;
  sendRpc: (id: number, method: string, params?: Record<string, unknown>) => void;
  readRpcResponse: (expectedId: number) => Promise<JsonRpcResponse>;
  readBinary: () => Promise<Uint8Array>;
  sendBinary: (data: Uint8Array) => void;
  close: () => void;
}> {
  return new Promise((resolve, reject) => {
    const url = `${wsUrl(ENDPOINT)}/sandboxes/${id}/ws`;
    const ws = new WebSocket(url);
    const messageQueue: Array<{ type: "text"; data: string } | { type: "binary"; data: Uint8Array }> = [];
    const waiters: Array<() => void> = [];

    ws.binaryType = "arraybuffer";

    ws.addEventListener("message", (event) => {
      if (typeof event.data === "string") {
        messageQueue.push({ type: "text", data: event.data });
      } else if (event.data instanceof ArrayBuffer) {
        messageQueue.push({ type: "binary", data: new Uint8Array(event.data) });
      }
      // Wake any waiting readers.
      while (waiters.length > 0) {
        waiters.shift()!();
      }
    });

    ws.addEventListener("error", (event) => {
      reject(new Error(`WebSocket error: ${event}`));
    });

    ws.addEventListener("open", () => {
      const waitForMessage = (): Promise<void> => {
        if (messageQueue.length > 0) return Promise.resolve();
        return new Promise<void>((res) => waiters.push(res));
      };

      resolve({
        ws,

        sendRpc(id: number, method: string, params?: Record<string, unknown>) {
          const req: JsonRpcRequest = { jsonrpc: "2.0", id, method, params };
          ws.send(JSON.stringify(req));
        },

        async readRpcResponse(expectedId: number): Promise<JsonRpcResponse> {
          const deadline = Date.now() + 30_000;
          while (Date.now() < deadline) {
            await waitForMessage();
            // Scan for matching text message.
            for (let i = 0; i < messageQueue.length; i++) {
              const msg = messageQueue[i]!;
              if (msg.type === "text") {
                const parsed = JSON.parse(msg.data as string) as JsonRpcResponse;
                if (parsed.id === expectedId) {
                  messageQueue.splice(i, 1);
                  return parsed;
                }
              }
            }
            // Wait for more messages.
            await new Promise<void>((res) => setTimeout(res, 50));
          }
          throw new Error(`Timeout waiting for RPC response with id=${expectedId}`);
        },

        async readBinary(): Promise<Uint8Array> {
          const deadline = Date.now() + 30_000;
          while (Date.now() < deadline) {
            await waitForMessage();
            for (let i = 0; i < messageQueue.length; i++) {
              const msg = messageQueue[i]!;
              if (msg.type === "binary") {
                messageQueue.splice(i, 1);
                return msg.data as Uint8Array;
              }
            }
            await new Promise<void>((res) => setTimeout(res, 50));
          }
          throw new Error("Timeout waiting for binary message");
        },

        sendBinary(data: Uint8Array) {
          ws.send(data);
        },

        close() {
          ws.close();
        },
      });
    });
  });
}

describe.skipIf(skip)("sandboxd integration", () => {
  const sandboxIds: string[] = [];

  afterEach(async () => {
    // Destroy all sandboxes created during the test.
    for (const id of sandboxIds) {
      await destroySandbox(id);
    }
    sandboxIds.length = 0;
  });

  /** Create a sandbox and register it for cleanup. */
  async function createTracked(): Promise<SandboxInfo> {
    const info = await createSandbox();
    sandboxIds.push(info.id);
    return info;
  }

  test("health check", async () => {
    const resp = await fetch(`${ENDPOINT}/health`);
    expect(resp.status).toBe(200);

    const body = (await resp.json()) as Record<string, unknown>;
    expect(body.status).toBe("ok");
  });

  test("create and destroy sandbox", async () => {
    const info = await createSandbox();
    expect(info.id).toMatch(/^sbx-/);
    expect(info.state).toBe("running");

    const delResp = await fetch(`${ENDPOINT}/sandboxes/${info.id}`, {
      method: "DELETE",
    });
    expect(delResp.status).toBe(204);
  });

  test("list sandboxes", async () => {
    const info = await createTracked();

    const resp = await fetch(`${ENDPOINT}/sandboxes`);
    expect(resp.status).toBe(200);

    const list = (await resp.json()) as SandboxInfo[];
    const found = list.some((s) => s.id === info.id);
    expect(found).toBe(true);
  });

  test("exec via websocket", async () => {
    const info = await createTracked();
    const conn = await connectWS(info.id);

    try {
      conn.sendRpc(1, "process.exec", { command: "echo hello" });
      const resp = await conn.readRpcResponse(1);

      expect(resp.error).toBeUndefined();
      const result = resp.result as Record<string, unknown>;
      expect(result.stdout).toBe("hello\n");
      expect(result.exit_code).toBe(0);
    } finally {
      conn.close();
    }
  });

  test("fs write and read", async () => {
    const info = await createTracked();
    const conn = await connectWS(info.id);

    try {
      const testContent = new TextEncoder().encode("hello from ts integration test");
      const testPath = "/tmp/integration-test.txt";

      // Write: send JSON-RPC request followed by binary content.
      conn.sendRpc(1, "fs.write", { path: testPath, size: testContent.byteLength });
      conn.sendBinary(testContent);

      const writeResp = await conn.readRpcResponse(1);
      expect(writeResp.error).toBeUndefined();

      // Read: send JSON-RPC request, expect binary content then text response.
      conn.sendRpc(2, "fs.read", { path: testPath });

      const content = await conn.readBinary();
      const readResp = await conn.readRpcResponse(2);

      expect(readResp.error).toBeUndefined();
      expect(new TextDecoder().decode(content)).toBe("hello from ts integration test");
    } finally {
      conn.close();
    }
  });

  test("env set and get", async () => {
    const info = await createTracked();
    const conn = await connectWS(info.id);

    try {
      // Set env var.
      conn.sendRpc(1, "env.set", { key: "TEST_VAR", value: "integration_value" });
      const setResp = await conn.readRpcResponse(1);
      expect(setResp.error).toBeUndefined();

      // Get env var.
      conn.sendRpc(2, "env.get", { key: "TEST_VAR" });
      const getResp = await conn.readRpcResponse(2);
      expect(getResp.error).toBeUndefined();

      // Result might be the value directly or wrapped in an object.
      const result = getResp.result;
      if (typeof result === "string") {
        expect(result).toBe("integration_value");
      } else if (typeof result === "object" && result !== null) {
        expect((result as Record<string, unknown>).value).toBe("integration_value");
      } else {
        throw new Error(`Unexpected env.get result: ${JSON.stringify(result)}`);
      }
    } finally {
      conn.close();
    }
  });

  test("destroy nonexistent sandbox returns 404", async () => {
    const resp = await fetch(`${ENDPOINT}/sandboxes/sbx-nonexistent`, {
      method: "DELETE",
    });
    expect(resp.status).toBe(404);
  });

  test("create multiple sandboxes", async () => {
    const infos = await Promise.all([createTracked(), createTracked(), createTracked()]);

    const resp = await fetch(`${ENDPOINT}/sandboxes`);
    expect(resp.status).toBe(200);

    const list = (await resp.json()) as SandboxInfo[];
    for (const info of infos) {
      const found = list.some((s) => s.id === info.id);
      expect(found).toBe(true);
    }
  });
});
