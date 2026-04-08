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

/** Event emitted during streaming exec. */
export interface OutputEvent {
  stream: "stdout" | "stderr";
  data: string;
}

/** Handle for a streaming exec call. */
export interface StreamExecHandle {
  /** Async iterator that yields output events as they arrive. */
  output: AsyncIterable<OutputEvent>;
  /** Returns the exit code once the process completes. */
  exitCode: Promise<number>;
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

export interface TunnelInfo {
  port: number;
  host_port: number;
  url: string;
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
    streamExec(command: string, opts?: { env?: Record<string, string>; timeout?: number; cwd?: string }): StreamExecHandle;
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
    url(port: number): string;
    expose(port: number, opts?: { timeout?: number }): Promise<TunnelInfo>;
    close(port: number): Promise<void>;
    ports(): Promise<TunnelInfo[]>;
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
function buildSandbox(id: string, transport: RpcTransport, httpBase?: string, authHeaders?: Record<string, string>): Sandbox {
  const ctx: CallContext = { transport, sandboxId: id };
  const base = httpBase ?? "http://localhost:7522";

  return {
    id,

    fs: {
      async read(path: string): Promise<Uint8Array> {
        const { binary } = await transport.callExpectBinary("fs.read", { path });
        if (binary.length === 0) {
          throw new Error("No binary data received for fs.read");
        }
        if (binary.length === 1) {
          return binary[0]!;
        }
        // Reassemble chunked response.
        let totalLen = 0;
        for (const b of binary) totalLen += b.byteLength;
        const merged = new Uint8Array(totalLen);
        let offset = 0;
        for (const b of binary) {
          merged.set(b, offset);
          offset += b.byteLength;
        }
        return merged;
      },

      async write(path: string, content: string | Uint8Array): Promise<void> {
        const data = typeof content === "string" ? new TextEncoder().encode(content) : content;
        const chunkSize = 1024 * 1024; // 1MB
        const chunked = data.byteLength > chunkSize;
        const chunks = chunked ? Math.ceil(data.byteLength / chunkSize) : 1;

        const params: Record<string, unknown> = { path, size: data.byteLength };
        if (chunked) {
          params.chunked = true;
          params.chunks = chunks;
        }

        await transport.callWithBinary("fs.write", params, data);
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
        await transport.callWithBinary("fs.upload", { path, size: tar.byteLength }, tar);
      },

      async download(path: string): Promise<Uint8Array> {
        const { binary } = await transport.callExpectBinary("fs.download", { path });
        if (binary.length === 0) {
          throw new Error("No binary data received for fs.download");
        }
        if (binary.length === 1) {
          return binary[0]!;
        }
        // Reassemble chunked response.
        let totalLen = 0;
        for (const b of binary) totalLen += b.byteLength;
        const merged = new Uint8Array(totalLen);
        let offset = 0;
        for (const b of binary) {
          merged.set(b, offset);
          offset += b.byteLength;
        }
        return merged;
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

      streamExec(command: string, opts?: { env?: Record<string, string>; timeout?: number; cwd?: string }): StreamExecHandle {
        const params: Record<string, unknown> = { command };
        if (opts?.env) params.env = opts.env;
        if (opts?.timeout) params.timeout = opts.timeout;
        if (opts?.cwd) params.cwd = opts.cwd;

        // Buffer for output events, with a queue and waiters for async iteration.
        const queue: OutputEvent[] = [];
        let done = false;
        let waiter: ((value: IteratorResult<OutputEvent>) => void) | null = null;

        const push = (event: OutputEvent) => {
          if (waiter) {
            const w = waiter;
            waiter = null;
            w({ value: event, done: false });
          } else {
            queue.push(event);
          }
        };

        const finish = () => {
          done = true;
          if (waiter) {
            const w = waiter;
            waiter = null;
            w({ value: undefined as unknown as OutputEvent, done: true });
          }
        };

        // Register notification handler for process.output events.
        transport.onNotification((method: string, notifParams: unknown) => {
          if (method !== "process.output") return;
          const p = notifParams as Record<string, unknown>;
          push({
            stream: p.stream as "stdout" | "stderr",
            data: p.data as string,
          });
        });

        // Make the RPC call. The response arrives when the process finishes.
        const rpcPromise = call(ctx, { method: "process.stream", params }) as Promise<Record<string, unknown>>;

        const exitCodePromise = rpcPromise.then((result) => {
          finish();
          return (result.exit_code as number) ?? -1;
        }).catch((err) => {
          finish();
          throw err;
        });

        const output: AsyncIterable<OutputEvent> = {
          [Symbol.asyncIterator]() {
            return {
              next(): Promise<IteratorResult<OutputEvent>> {
                if (queue.length > 0) {
                  return Promise.resolve({ value: queue.shift()!, done: false });
                }
                if (done) {
                  return Promise.resolve({ value: undefined as unknown as OutputEvent, done: true });
                }
                return new Promise((resolve) => {
                  waiter = resolve;
                });
              },
            };
          },
        };

        return { output, exitCode: exitCodePromise };
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

      url(port: number): string {
        return `${base}/sandboxes/${id}/ports/${port}`;
      },

      async expose(port: number, opts?: { timeout?: number }): Promise<TunnelInfo> {
        const body: Record<string, unknown> = {};
        if (opts?.timeout) body.timeout = opts.timeout;
        const resp = await fetch(`${base}/sandboxes/${id}/ports/${port}/expose`, {
          method: "POST",
          headers: { "Content-Type": "application/json", ...authHeaders },
          body: JSON.stringify(body),
        });
        if (!resp.ok) {
          const text = await resp.text();
          throw new Error(`expose failed (status ${resp.status}): ${text}`);
        }
        return await resp.json() as TunnelInfo;
      },

      async close(port: number): Promise<void> {
        const resp = await fetch(`${base}/sandboxes/${id}/ports/${port}/expose`, {
          method: "DELETE",
          headers: { ...authHeaders },
        });
        if (!resp.ok && resp.status !== 404) {
          const text = await resp.text();
          throw new Error(`close failed (status ${resp.status}): ${text}`);
        }
      },

      async ports(): Promise<TunnelInfo[]> {
        const resp = await fetch(`${base}/sandboxes/${id}/ports`, {
          headers: { ...authHeaders },
        });
        if (!resp.ok) {
          const text = await resp.text();
          throw new Error(`ports failed (status ${resp.status}): ${text}`);
        }
        return await resp.json() as TunnelInfo[];
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

  return buildSandbox(sandboxId, transport, http, headers);
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

  const { http: httpBase } = resolveEndpoints(endpoint);
  return buildSandbox(id, transport, httpBase, headers);
}
