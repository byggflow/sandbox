import type { Auth } from "./auth.ts";
import { isRequestSigner, resolveAuth } from "./auth.ts";
import { call } from "./call.ts";
import type { CallContext } from "./call.ts";
import { negotiateE2E } from "./e2e.ts";
import { CapacityError, ConnectionError } from "./errors.ts";
import { WsTransport } from "./transport.ts";
import type { RpcTransport } from "./transport.ts";

export const DEFAULT_ENDPOINT = "unix:///var/run/sandboxd/sandboxd.sock";

export interface SandboxOptions {
  /** Daemon endpoint. Defaults to unix:///var/run/sandboxd/sandboxd.sock */
  endpoint?: string;
  auth?: Auth;
  profile?: string;
  template?: string;
  memory?: string;
  cpu?: number;
  ttl?: number;
  labels?: Record<string, string>;
  encrypted?: boolean;
}

export interface ConnectOptions {
  endpoint?: string;
  auth?: Auth;
  encrypted?: boolean;
  retry?: boolean;
}

export interface ExecResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

export interface SpawnHandle {
  stdout: AsyncIterable<Uint8Array>;
  stderr: AsyncIterable<Uint8Array>;
  stdin: WritableStream<Uint8Array>;
  pid: number;
  kill(signal?: string): Promise<void>;
  wait(): Promise<{ exitCode: number }>;
}

export interface PtyHandle {
  data: AsyncIterable<Uint8Array>;
  write(data: string | Uint8Array): void;
  resize(cols: number, rows: number): void;
  pid: number;
  kill(signal?: string): Promise<void>;
  wait(): Promise<{ exitCode: number }>;
}

export interface Sandbox {
  id: string;
  fs: {
    read(path: string): Promise<Uint8Array>;
    write(path: string, content: string | Uint8Array): Promise<void>;
    list(path: string): Promise<string[]>;
    stat(path: string): Promise<Record<string, unknown>>;
    remove(path: string): Promise<void>;
    mkdir(path: string): Promise<void>;
    upload(path: string, tar: Uint8Array): Promise<void>;
    download(path: string): Promise<Uint8Array>;
  };
  process: {
    exec(command: string, opts?: { env?: Record<string, string>; timeout?: number }): Promise<ExecResult>;
    spawn(command: string, opts?: { env?: Record<string, string> }): SpawnHandle;
    pty(opts?: { command?: string; cols?: number; rows?: number; env?: Record<string, string> }): PtyHandle;
  };
  env: {
    get(key: string): Promise<string | null>;
    set(key: string, value: string): Promise<void>;
    delete(key: string): Promise<void>;
    list(): Promise<Record<string, string>>;
  };
  net: {
    fetch(url: string, opts?: RequestInit): Promise<Response>;
  };
  template: {
    save(opts?: { label?: string }): Promise<{ id: string }>;
  };
  close(): Promise<void>;
}

/** Resolve HTTP and WS endpoint URLs from the raw endpoint string. */
function resolveEndpoints(endpoint: string): { http: string; ws: string } {
  if (endpoint.startsWith("unix://")) {
    // For Unix sockets, we rely on the caller to configure a proxy or use
    // a local HTTP endpoint. Default to localhost for the HTTP API.
    return {
      http: "http://localhost:7522",
      ws: "ws://localhost:7522",
    };
  }

  const httpBase = endpoint.replace(/\/$/, "");
  const wsBase = httpBase.replace(/^http/, "ws");
  return { http: httpBase, ws: wsBase };
}

/** Build a Sandbox object from a connected transport. */
function buildSandbox(id: string, transport: RpcTransport): Sandbox {
  const ctx: CallContext = { transport, sandboxId: id };

  return {
    id,

    fs: {
      async read(path: string): Promise<Uint8Array> {
        return new Promise<Uint8Array>((resolve, reject) => {
          let binaryData: Uint8Array | null = null;

          transport.onBinary((data) => {
            binaryData = data;
          });

          call(ctx, { method: "fs.read", params: { path } })
            .then(() => {
              if (binaryData) {
                resolve(binaryData);
              } else {
                reject(new Error("No binary data received for fs.read"));
              }
            })
            .catch(reject);
        });
      },

      async write(path: string, content: string | Uint8Array): Promise<void> {
        const data = typeof content === "string" ? new TextEncoder().encode(content) : content;
        // Send the RPC and binary data without awaiting in between — the agent
        // reads the binary frame as part of handling fs.write, so it must arrive
        // before the agent can respond.
        const result = call(ctx, { method: "fs.write", params: { path, size: data.byteLength } });
        transport.sendBinary(data);
        await result;
      },

      async list(path: string): Promise<string[]> {
        const result = await call(ctx, { method: "fs.list", params: { path } });
        return result as string[];
      },

      async stat(path: string): Promise<Record<string, unknown>> {
        const result = await call(ctx, { method: "fs.stat", params: { path } });
        return result as Record<string, unknown>;
      },

      async remove(path: string): Promise<void> {
        await call(ctx, { method: "fs.remove", params: { path } });
      },

      async mkdir(path: string): Promise<void> {
        await call(ctx, { method: "fs.mkdir", params: { path } });
      },

      async upload(path: string, tar: Uint8Array): Promise<void> {
        const result = call(ctx, { method: "fs.upload", params: { path, size: tar.byteLength } });
        transport.sendBinary(tar);
        await result;
      },

      async download(path: string): Promise<Uint8Array> {
        return new Promise<Uint8Array>((resolve, reject) => {
          let binaryData: Uint8Array | null = null;

          transport.onBinary((data) => {
            binaryData = data;
          });

          call(ctx, { method: "fs.download", params: { path } })
            .then(() => {
              if (binaryData) {
                resolve(binaryData);
              } else {
                reject(new Error("No binary data received for fs.download"));
              }
            })
            .catch(reject);
        });
      },
    },

    process: {
      async exec(command: string, opts?: { env?: Record<string, string>; timeout?: number }): Promise<ExecResult> {
        const params: Record<string, unknown> = { command };
        if (opts?.env) params.env = opts.env;
        if (opts?.timeout) params.timeout = opts.timeout;
        const result = await call(ctx, { method: "process.exec", params }) as Record<string, unknown>;
        return {
          stdout: (result.stdout as string) ?? "",
          stderr: (result.stderr as string) ?? "",
          exitCode: (result.exit_code as number) ?? -1,
        };
      },

      spawn(_command: string, _opts?: { env?: Record<string, string> }): SpawnHandle {
        throw new Error("process.spawn requires streaming transport support");
      },

      pty(_opts?: { command?: string; cols?: number; rows?: number; env?: Record<string, string> }): PtyHandle {
        throw new Error("process.pty requires streaming transport support");
      },
    },

    env: {
      async get(key: string): Promise<string | null> {
        const result = await call(ctx, { method: "env.get", params: { key } });
        return result as string | null;
      },

      async set(key: string, value: string): Promise<void> {
        await call(ctx, { method: "env.set", params: { key, value } });
      },

      async delete(key: string): Promise<void> {
        await call(ctx, { method: "env.delete", params: { key } });
      },

      async list(): Promise<Record<string, string>> {
        const result = await call(ctx, { method: "env.list" });
        return (result ?? {}) as Record<string, string>;
      },
    },

    net: {
      async fetch(url: string, opts?: RequestInit): Promise<Response> {
        const params: Record<string, unknown> = {
          url,
          method: opts?.method ?? "GET",
        };
        if (opts?.headers) {
          params.headers = Object.fromEntries(
            opts.headers instanceof Headers
              ? opts.headers.entries()
              : Array.isArray(opts.headers)
                ? opts.headers
                : Object.entries(opts.headers),
          );
        }
        if (opts?.body) params.body = opts.body;
        const result = await call(ctx, { method: "net.fetch", params }) as Record<string, unknown>;
        return new Response(result.body as string, {
          status: result.status as number,
          headers: result.headers as Record<string, string>,
        });
      },
    },

    template: {
      async save(opts?: { label?: string }): Promise<{ id: string }> {
        const params: Record<string, unknown> = {};
        if (opts?.label) params.label = opts.label;
        const result = await call(ctx, { method: "template.save", params });
        return result as { id: string };
      },
    },

    async close(): Promise<void> {
      await transport.close();
    },
  };
}

export async function createSandbox(opts?: SandboxOptions): Promise<Sandbox> {
  const endpoint = opts?.endpoint ?? DEFAULT_ENDPOINT;
  const { http, ws } = resolveEndpoints(endpoint);

  // Resolve auth headers — use per-request signing if available.
  let headers: Record<string, string>;
  const signer = opts?.auth && isRequestSigner(opts.auth) ? opts.auth : null;
  if (signer) {
    headers = await signer.resolveForRequest("POST", "/sandboxes");
  } else {
    headers = await resolveAuth(opts?.auth)();
  }

  // Create the sandbox via HTTP.
  const body: Record<string, unknown> = {};
  if (opts?.profile) body.profile = opts.profile;
  if (opts?.template) body.template = opts.template;
  if (opts?.memory) body.memory = opts.memory;
  if (opts?.cpu) body.cpu = opts.cpu;
  if (opts?.ttl) body.ttl = opts.ttl;
  if (opts?.labels) body.labels = opts.labels;

  const response = await fetch(`${http}/sandboxes`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...headers },
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    const text = await response.text();
    if (response.status === 429 || response.status === 503) {
      const retryAfter = parseInt(response.headers.get("Retry-After") ?? "", 10);
      throw new CapacityError(text, Number.isNaN(retryAfter) ? 60 : retryAfter);
    }
    throw new ConnectionError(`Failed to create sandbox: ${response.status} ${text}`);
  }

  const data = await response.json() as { id: string };
  const sandboxId = data.id;

  // Re-resolve auth for the WebSocket connection if using per-request signing.
  const wsHeaders = signer
    ? await signer.resolveForRequest("GET", `/sandboxes/${sandboxId}/ws`)
    : headers;

  // Connect WebSocket.
  const wsTransport = new WsTransport();
  await wsTransport.connect(`${ws}/sandboxes/${sandboxId}/ws`, wsHeaders);

  let transport: RpcTransport = wsTransport;
  if (opts?.encrypted) {
    transport = await negotiateE2E(wsTransport);
  }

  return buildSandbox(sandboxId, transport);
}

export async function connectSandbox(id: string, opts?: ConnectOptions): Promise<Sandbox> {
  const endpoint = opts?.endpoint ?? DEFAULT_ENDPOINT;
  const { ws } = resolveEndpoints(endpoint);

  let headers: Record<string, string>;
  if (opts?.auth && isRequestSigner(opts.auth)) {
    headers = await opts.auth.resolveForRequest("GET", `/sandboxes/${id}/ws`);
  } else {
    headers = await resolveAuth(opts?.auth)();
  }

  const wsTransport = new WsTransport();
  await wsTransport.connect(`${ws}/sandboxes/${id}/ws`, headers);

  let transport: RpcTransport = wsTransport;
  if (opts?.encrypted) {
    transport = await negotiateE2E(wsTransport);
  }

  return buildSandbox(id, transport);
}
