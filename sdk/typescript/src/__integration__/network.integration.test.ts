import { describe, test, expect, afterEach } from "vitest";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { createSandbox as sdkCreateSandbox } from "../sandbox.ts";

const ENDPOINT = process.env.SANDBOXD_ENDPOINT ?? "";
const skip = !ENDPOINT;

/** Convert http(s) URL to ws(s) URL. */
function wsUrl(httpUrl: string): string {
  return httpUrl.replace(/^http/, "ws");
}

interface SandboxInfo {
  id: string;
}

interface JsonRpcResponse {
  jsonrpc: "2.0";
  id: number;
  result?: unknown;
  error?: { code: number; message: string };
}

async function createSandbox(): Promise<SandboxInfo> {
  const resp = await fetch(`${ENDPOINT}/sandboxes`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  });
  if (!resp.ok) throw new Error(`create sandbox: ${resp.status}`);
  return (await resp.json()) as SandboxInfo;
}

async function destroySandbox(id: string): Promise<void> {
  await fetch(`${ENDPOINT}/sandboxes/${id}`, { method: "DELETE" });
}

interface RpcConn {
  sendRpc(id: number, method: string, params?: Record<string, unknown>): void;
  readRpcResponse(expectedId: number): Promise<JsonRpcResponse>;
  close(): void;
}

function connectWS(id: string): Promise<RpcConn> {
  return new Promise((resolve, reject) => {
    const url = `${wsUrl(ENDPOINT)}/sandboxes/${id}/ws`;
    const ws = new WebSocket(url);
    const queue: string[] = [];
    const waiters: Array<() => void> = [];
    ws.binaryType = "arraybuffer";

    ws.addEventListener("message", (event) => {
      if (typeof event.data === "string") {
        queue.push(event.data);
        while (waiters.length > 0) waiters.shift()!();
      }
    });
    ws.addEventListener("error", () => reject(new Error("ws error")));
    ws.addEventListener("open", () => {
      resolve({
        sendRpc(id: number, method: string, params?: Record<string, unknown>) {
          ws.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
        },
        async readRpcResponse(expectedId: number): Promise<JsonRpcResponse> {
          const deadline = Date.now() + 30_000;
          while (Date.now() < deadline) {
            for (let i = 0; i < queue.length; i++) {
              const parsed = JSON.parse(queue[i]!) as JsonRpcResponse;
              if (parsed.id === expectedId) {
                queue.splice(i, 1);
                return parsed;
              }
            }
            if (queue.length === 0) {
              await new Promise<void>((res) => waiters.push(res));
            }
          }
          throw new Error(`timeout waiting for response id=${expectedId}`);
        },
        close() {
          ws.close();
        },
      });
    });
  });
}

/** Spin up a local HTTP server and return its host + a getter for the
 * last seen Authorization header. */
function spawnUpstream(): Promise<{
  host: string;
  url: string;
  lastAuth: () => string;
  hits: () => number;
  stop: () => Promise<void>;
}> {
  return new Promise((resolve) => {
    let auth = "";
    let hits = 0;
    const server = createServer((req: IncomingMessage, res: ServerResponse) => {
      auth = (req.headers["authorization"] as string) ?? "";
      hits++;
      res.writeHead(200, { "Content-Type": "text/plain" });
      res.end("ok");
    });
    server.listen(0, "127.0.0.1", () => {
      const addr = server.address() as AddressInfo;
      resolve({
        host: "127.0.0.1",
        url: `http://127.0.0.1:${addr.port}`,
        lastAuth: () => auth,
        hits: () => hits,
        stop: () => new Promise<void>((res) => server.close(() => res())),
      });
    });
  });
}

describe.skipIf(skip)("network middleware integration", () => {
  const sandboxIds: string[] = [];

  afterEach(async () => {
    for (const id of sandboxIds) await destroySandbox(id);
    sandboxIds.length = 0;
  });

  async function createTracked(): Promise<SandboxInfo> {
    const info = await createSandbox();
    sandboxIds.push(info.id);
    return info;
  }

  test("inject rule adds header that sandbox never sees", async () => {
    const upstream = await spawnUpstream();
    try {
      const sbx = await createTracked();
      const conn = await connectWS(sbx.id);
      try {
        // Install the inject rule.
        const setResp = await (async () => {
          conn.sendRpc(1, "net.rules.set", {
            rules: [
              {
                id: "inject-auth",
                match: { host: upstream.host },
                action: "inject",
                inject: { set_headers: { Authorization: "Bearer test-secret" } },
              },
            ],
          });
          return conn.readRpcResponse(1);
        })();
        expect(setResp.error).toBeUndefined();

        // Sandbox-initiated fetch — daemon routes through rule engine.
        conn.sendRpc(2, "net.fetch", { url: upstream.url, method: "GET" });
        const fetchResp = await conn.readRpcResponse(2);
        expect(fetchResp.error).toBeUndefined();

        const result = fetchResp.result as { status: number };
        expect(result.status).toBe(200);
        expect(upstream.lastAuth()).toBe("Bearer test-secret");
      } finally {
        conn.close();
      }
    } finally {
      await upstream.stop();
    }
  });

  test("defer rule invokes SDK handler and forwards modified request", async () => {
    const upstream = await spawnUpstream();
    try {
      let handlerCalls = 0;

      // Use the actual SDK so the incoming-request dispatcher is wired
      // automatically. This exercises the full defer-to-sdk flow:
      // sandbox -> daemon -> SDK handler -> daemon -> upstream.
      const sbx = await sdkCreateSandbox({
        endpoint: ENDPOINT,
        network: {
          egress: [
            {
              id: "mint-auth",
              match: { host: upstream.host },
              action: "defer",
              handler: async (req) => {
                handlerCalls++;
                return {
                  request: {
                    ...req,
                    headers: { ...req.headers, Authorization: "Bearer minted-by-sdk" },
                  },
                };
              },
            },
          ],
        },
      });
      sandboxIds.push(sbx.id);

      try {
        const resp = await sbx.net.fetch(upstream.url, { method: "GET" });
        expect(resp.status).toBe(200);
        expect(handlerCalls).toBe(1);
        expect(upstream.lastAuth()).toBe("Bearer minted-by-sdk");
      } finally {
        await sbx.close();
      }
    } finally {
      await upstream.stop();
    }
  });

  test("defer rule can short-circuit with a synthetic response", async () => {
    const upstream = await spawnUpstream();
    try {
      let handlerCalls = 0;
      const sbx = await sdkCreateSandbox({
        endpoint: ENDPOINT,
        network: {
          egress: [
            {
              id: "stub",
              match: { host: upstream.host },
              action: "defer",
              handler: async () => {
                handlerCalls++;
                return {
                  response: {
                    status: 418,
                    headers: { "content-type": "text/plain" },
                    body: Buffer.from("brewed by sdk", "utf8").toString("base64"),
                  },
                };
              },
            },
          ],
        },
      });
      sandboxIds.push(sbx.id);

      try {
        const resp = await sbx.net.fetch(upstream.url, { method: "GET" });
        expect(resp.status).toBe(418);
        expect(handlerCalls).toBe(1);
        // Synthetic response means the upstream was never dialed.
        expect(upstream.hits()).toBe(0);
      } finally {
        await sbx.close();
      }
    } finally {
      await upstream.stop();
    }
  });

  test("deny rule short-circuits without dialing upstream", async () => {
    const upstream = await spawnUpstream();
    try {
      const sbx = await createTracked();
      const conn = await connectWS(sbx.id);
      try {
        conn.sendRpc(1, "net.rules.set", {
          rules: [{ id: "block", match: { host: upstream.host }, action: "deny" }],
        });
        expect((await conn.readRpcResponse(1)).error).toBeUndefined();

        conn.sendRpc(2, "net.fetch", { url: upstream.url, method: "GET" });
        const fetchResp = await conn.readRpcResponse(2);
        expect(fetchResp.error).toBeUndefined();

        const result = fetchResp.result as { status: number };
        expect(result.status).toBe(403);
        expect(upstream.hits()).toBe(0);
      } finally {
        conn.close();
      }
    } finally {
      await upstream.stop();
    }
  });
});
