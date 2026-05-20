import type { Auth } from "./auth.ts";
import { isRequestSigner, resolveAuth } from "./auth.ts";
import { call } from "./call.ts";
import type { CallContext } from "./call.ts";
import { negotiateE2E } from "./e2e.ts";
import { CapacityError, ConnectionError, RpcError } from "./errors.ts";
import { WsTransport } from "./transport.ts";
import type { RpcTransport } from "./transport.ts";

/** Default Unix socket path checked during auto-discovery. */
export const DEFAULT_SOCKET_PATH = "/var/run/sandboxd/sandboxd.sock";

/** Default TCP endpoint used when no socket is found. */
export const DEFAULT_TCP_ENDPOINT = "http://localhost:7522";

/**
 * Default daemon endpoint as a string. Kept for backwards compatibility with
 * callers that want a stable constant; prefer `resolveDefaultEndpoint()` for
 * runtime discovery (env var → socket → TCP fallback).
 */
export const DEFAULT_ENDPOINT = "unix:///var/run/sandboxd/sandboxd.sock";

/** Read an environment variable in a cross-runtime way (Node, Bun, Deno). */
function envVar(name: string): string | undefined {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  return env?.[name];
}

/**
 * Resolve the daemon endpoint when none is passed explicitly.
 *
 * Order:
 * 1. `SANDBOXD_ENDPOINT` environment variable.
 * 2. Unix socket at `/var/run/sandboxd/sandboxd.sock` if it exists.
 * 3. TCP `http://localhost:7522`.
 *
 * Browsers and other runtimes without `process.env` or `node:fs` fall straight
 * through to the TCP default.
 */
export async function resolveDefaultEndpoint(): Promise<string> {
  const envEndpoint = envVar("SANDBOXD_ENDPOINT");
  if (envEndpoint) return envEndpoint;

  try {
    const fs = await import("node:fs");
    if (fs.existsSync(DEFAULT_SOCKET_PATH)) {
      return `unix://${DEFAULT_SOCKET_PATH}`;
    }
  } catch {
    // node:fs unavailable (browser, Deno without --allow-read) — fall through.
  }

  return DEFAULT_TCP_ENDPOINT;
}

/** Default auth derived from `SBX_AUTH` (bearer token), or undefined. */
function defaultAuth(): Auth | undefined {
  return envVar("SBX_AUTH");
}

/** Options for creating a new sandbox. */
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
  /**
   * Network middleware. Rules run on the daemon, so injected headers and
   * credentials never enter the sandbox.
   *
   * The egress array is pushed to the daemon immediately after the WebSocket
   * connects. Sandbox processes that respect HTTP_PROXY (most HTTP libraries)
   * route through the daemon, and any rule whose match clause fits is applied
   * before the daemon dials the upstream.
   *
   * For HTTPS without TLS MITM (the current default), only `sbx.network.fetch()`
   * (and equivalent SDK-mediated calls) get the injection benefit. Transparent
   * HTTPS interception is a follow-up.
   */
  network?: NetworkConfig;
}

/** Inject mutations applied to a matched egress request. */
export interface NetworkInject {
  setHeaders?: Record<string, string>;
  removeHeaders?: string[];
  setQuery?: Record<string, string>;
}

/** Match clause for a network rule. */
export interface NetworkMatch {
  /** Host glob: exact ("api.openai.com"), suffix ("*.github.com"), or /regex/. */
  host?: string;
  port?: number;
  /** Comma-separated method list, e.g. "GET,POST". */
  method?: string;
  pathPrefix?: string;
}

/** A single egress rule. */
export interface NetworkRule {
  id?: string;
  match: NetworkMatch;
  action: "allow" | "deny" | "inject" | "defer";
  inject?: NetworkInject;
  /**
   * Programmatic handler. Only consulted when action === "defer".
   *
   * The handler runs in your SDK process (not the daemon, not the sandbox),
   * so it can do async work like minting per-request tokens. Return a
   * (possibly modified) DeferredRequest to continue dialing, or a
   * DeferredResponse to short-circuit without touching the upstream.
   */
  handler?: NetworkHandler;
}

/** Request envelope passed to a network handler. Mutations to headers
 * or URL are sent back to the daemon and used for the actual dial. */
export interface DeferredRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  /** Base64-encoded request body. Empty when there is no body. */
  body: string;
}

/** Synthetic response a handler can return to short-circuit dialing. */
export interface DeferredResponse {
  status: number;
  headers?: Record<string, string>;
  /** Base64-encoded response body. */
  body?: string;
}

/** Handler signature for action === "defer" rules. Receives the request
 * the daemon was about to dial; returns either a modified request or a
 * synthetic response. */
export type NetworkHandler = (req: DeferredRequest) =>
  | { request: DeferredRequest }
  | { response: DeferredResponse }
  | Promise<{ request: DeferredRequest } | { response: DeferredResponse }>;

/** Network middleware configuration installed at sandbox creation. */
export interface NetworkConfig {
  egress?: NetworkRule[];
  /**
   * Wire the sandbox through the daemon's egress proxy. When false, no
   * HTTP(S)_PROXY env vars are injected, no per-sandbox CA is
   * generated or pushed, and sandbox processes dial upstreams
   * directly. Default: true.
   *
   * `sbx.net.fetch()` still routes through the daemon either way (the
   * daemon dials on the SDK's behalf for that path), so it works
   * without the proxy — but no rule evaluation happens for it when
   * enabled is false.
   */
  enabled?: boolean;
}

/** Options for connecting to an existing sandbox by ID. */
export interface ConnectOptions {
  endpoint?: string;
  auth?: Auth;
  encrypted?: boolean;
  retry?: boolean;
}

/** Result of a completed process execution. */
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

/** Information about an exposed port tunnel. */
export interface TunnelInfo {
  port: number;
  host_port: number;
  url: string;
}

/** Metadata about a file or directory inside a sandbox. Matches the agent's stat response. */
export interface StatResult {
  name: string;
  size: number;
  /** Unix file mode bits. */
  mode: number;
  isDir: boolean;
  /** Modification time as Unix epoch seconds. */
  modTime: number;
}

/** A connected sandbox with filesystem, process, environment, and network access. */
export interface Sandbox {
  id: string;
  fs: {
    read(path: string): Promise<Uint8Array>;
    write(path: string, content: string | Uint8Array): Promise<void>;
    list(path: string): Promise<string[]>;
    stat(path: string): Promise<StatResult>;
    remove(path: string): Promise<void>;
    rename(oldPath: string, newPath: string): Promise<void>;
    mkdir(path: string): Promise<void>;
    upload(path: string, tar: Uint8Array): Promise<void>;
    download(path: string): Promise<Uint8Array>;
  };
  process: {
    exec(command: string, opts?: { env?: Record<string, string>; timeout?: number }): Promise<ExecResult>;
    streamExec(command: string, opts?: { env?: Record<string, string>; timeout?: number; cwd?: string }): StreamExecHandle;
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
  /**
   * Programmatic egress middleware. Rules are evaluated on the daemon — the
   * sandbox never sees credentials installed via inject rules.
   *
   * Replace the rule set wholesale with `intercept(rules)`, or use the
   * `allow`/`deny`/`inject`/`defer` convenience helpers that update
   * incrementally.
   */
  network: {
    /** Replace the entire rule set for this sandbox. */
    intercept(rules: NetworkRule[]): Promise<void>;
    /** Append a deny rule for the given host glob. */
    deny(host: string): Promise<void>;
    /** Append an allow rule for the given host glob. */
    allow(host: string): Promise<void>;
    /** Append an inject-headers rule for the given host glob. */
    inject(host: string, headers: Record<string, string>): Promise<void>;
    /** Append a defer rule: the daemon calls your handler on each match. */
    defer(host: string, handler: NetworkHandler, id?: string): Promise<void>;
  };
  template: {
    save(opts?: { label?: string }): Promise<{ id: string }>;
  };
  close(): Promise<void>;
}

/** Parsed endpoint with transport details. */
export interface ResolvedEndpoint {
  /** HTTP base URL for REST calls. */
  http: string;
  /** WebSocket base URL for RPC. */
  ws: string;
  /** Unix socket path, set when endpoint is unix://. */
  socketPath?: string;
}

/** Resolve endpoint URLs from the raw endpoint string.
 *
 * For unix:// endpoints, returns the socket path so callers can use
 * node:http with socketPath instead of requiring TCP.
 */
export function resolveEndpoints(endpoint: string): ResolvedEndpoint {
  if (endpoint.startsWith("unix://")) {
    const socketPath = endpoint.slice("unix://".length);
    return {
      http: "http://localhost",
      ws: "ws://localhost",
      socketPath,
    };
  }

  const httpBase = endpoint.replace(/\/$/, "");
  const wsBase = httpBase.replace(/^http/, "ws");
  return { http: httpBase, ws: wsBase };
}

/** A fetch-like function scoped to the daemon endpoint. Path-only (e.g. "/sandboxes"). */
type DaemonFetch = (path: string, init?: RequestInit) => Promise<Response>;

/** Build a Sandbox object from a connected transport. */
function buildSandbox(id: string, transport: RpcTransport, daemonFetch: DaemonFetch, httpBase: string, authHeaders?: Record<string, string>): Sandbox {
  const ctx: CallContext = { transport, sandboxId: id };
  const base = httpBase;

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

      async stat(path: string): Promise<StatResult> {
        // Wire format from the guest agent uses snake_case for compound fields
        // and serialises ModTime as an RFC3339 string from Go's time.Time.
        const result = await call(ctx, { method: "fs.stat", params: { path } }) as Record<string, unknown>;
        const rawModTime = result.mod_time;
        let modTime = 0;
        if (typeof rawModTime === "number") {
          modTime = rawModTime;
        } else if (typeof rawModTime === "string" && rawModTime !== "") {
          const ms = Date.parse(rawModTime);
          if (!Number.isNaN(ms)) modTime = Math.floor(ms / 1000);
        }
        return {
          name: (result.name as string) ?? "",
          size: (result.size as number) ?? 0,
          mode: (result.mode as number) ?? 0,
          isDir: (result.is_dir as boolean) ?? false,
          modTime,
        };
      },

      async remove(path: string): Promise<void> {
        await call(ctx, { method: "fs.remove", params: { path } });
      },

      async rename(oldPath: string, newPath: string): Promise<void> {
        await call(ctx, { method: "fs.rename", params: { old: oldPath, new: newPath } });
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
    },

    env: {
      async get(key: string): Promise<string | null> {
        const result = await call(ctx, { method: "env.get", params: { key } });
        return result as string | null;
      },

      async set(key: string, value: string): Promise<void> {
        if (typeof key !== "string" || key === "") {
          throw new TypeError("env.set: key must be a non-empty string");
        }
        if (typeof value !== "string") {
          throw new TypeError("env.set: value must be a string");
        }
        await call(ctx, { method: "env.set", params: { key, value } });
      },

      async delete(key: string): Promise<void> {
        if (typeof key !== "string" || key === "") {
          throw new TypeError("env.delete: key must be a non-empty string");
        }
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
        const resp = await daemonFetch(`/sandboxes/${id}/ports/${port}/expose`, {
          method: "POST",
          headers: { "Content-Type": "application/json", ...authHeaders },
          body: JSON.stringify(body),
        });
        if (!resp.ok) {
          throw await tunnelError(resp, `net.expose(${port})`);
        }
        return await resp.json() as TunnelInfo;
      },

      async close(port: number): Promise<void> {
        const resp = await daemonFetch(`/sandboxes/${id}/ports/${port}/expose`, {
          method: "DELETE",
          headers: { ...authHeaders },
        });
        if (!resp.ok && resp.status !== 404) {
          throw await tunnelError(resp, `net.close(${port})`);
        }
      },

      async ports(): Promise<TunnelInfo[]> {
        const resp = await daemonFetch(`/sandboxes/${id}/ports`, {
          headers: { ...authHeaders },
        });
        if (!resp.ok) {
          throw await tunnelError(resp, "net.ports");
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

    network: (() => {
      // Local mirror of installed rules so allow/deny/inject can append
      // incrementally without round-tripping the full list from the daemon.
      let current: NetworkRule[] = [];

      // Handler registry: rule ID -> user function. Defer rules carry only
      // their ID over the wire; the function stays in the SDK process.
      const handlers = new Map<string, NetworkHandler>();

      // Register the dispatcher for incoming net.defer requests. Only
      // wires once per buildSandbox; subsequent intercept() calls just
      // mutate the handlers map.
      transport.onRequest(async (method, params) => {
        if (method !== "net.defer") return undefined;
        // The wire format encodes headers as map[string][]string
        // (matches RFC 7230 multi-value semantics, needed for
        // Set-Cookie etc). The user-facing handler API uses
        // Record<string, string>, so collapse on the way in and
        // expand on the way out — without these conversions, any
        // header on the returned request/response fails the daemon's
        // JSON unmarshal into the wire type and the defer call comes
        // back as a 502.
        const p = params as {
          rule_id: string;
          request: { method: string; url: string; headers?: Record<string, string[]>; body?: string };
        };
        const fn = handlers.get(p.rule_id);
        if (!fn) {
          throw new Error(`no handler registered for rule ${p.rule_id}`);
        }
        const reqIn: DeferredRequest = {
          method: p.request.method,
          url: p.request.url,
          headers: collapseHeaders(p.request.headers),
          body: p.request.body ?? "",
        };
        const result = await fn(reqIn);
        if ("response" in result && result.response) {
          return {
            response: {
              status: result.response.status,
              headers: expandHeaders(result.response.headers),
              body: result.response.body ?? "",
            },
          };
        }
        const reqOut = (result as { request: DeferredRequest }).request;
        return {
          request: {
            method: reqOut.method,
            url: reqOut.url,
            headers: expandHeaders(reqOut.headers),
            body: reqOut.body ?? "",
          },
        };
      });

      const push = async () => {
        // Update handlers registry from the current rule list.
        handlers.clear();
        for (const r of current) {
          if (r.action === "defer" && r.handler) {
            if (!r.id) throw new Error("network.intercept: defer rules require an id");
            handlers.set(r.id, r.handler);
          }
        }
        await call(ctx, {
          method: "net.rules.set",
          params: { rules: current.map(toWireRule) },
        });
      };

      return {
        async intercept(rules: NetworkRule[]): Promise<void> {
          current = rules.slice();
          await push();
        },
        async deny(host: string): Promise<void> {
          current.push({ match: { host }, action: "deny" });
          await push();
        },
        async allow(host: string): Promise<void> {
          current.push({ match: { host }, action: "allow" });
          await push();
        },
        async inject(host: string, headers: Record<string, string>): Promise<void> {
          current.push({
            match: { host },
            action: "inject",
            inject: { setHeaders: headers },
          });
          await push();
        },
        async defer(host: string, handler: NetworkHandler, id?: string): Promise<void> {
          const ruleID = id ?? `defer-${current.length}`;
          current.push({ id: ruleID, match: { host }, action: "defer", handler });
          await push();
        },
      };
    })(),

    async close(): Promise<void> {
      await transport.close();
    },
  };
}

// expandHeaders converts the SDK's user-facing single-value header map
// into the wire-format multi-value shape (map[string][]string) the
// daemon expects when unmarshalling DeferResponse.Request/Response.
function expandHeaders(h?: Record<string, string>): Record<string, string[]> | undefined {
  if (!h) return undefined;
  const out: Record<string, string[]> = {};
  for (const [k, v] of Object.entries(h)) {
    out[k] = [v];
  }
  return out;
}

// collapseHeaders inverts expandHeaders: takes the wire-format
// multi-value shape and produces a single-value Record for the user's
// handler. Joins repeated values with comma per HTTP convention; not
// strictly correct for Set-Cookie but defer requests rarely carry
// multiple of those.
function collapseHeaders(h?: Record<string, string[]>): Record<string, string> {
  const out: Record<string, string> = {};
  if (!h) return out;
  for (const [k, vs] of Object.entries(h)) {
    if (vs && vs.length > 0) {
      out[k] = vs.join(", ");
    }
  }
  return out;
}

function toWireRule(r: NetworkRule): Record<string, unknown> {
  const out: Record<string, unknown> = {
    match: {
      host: r.match.host ?? "",
      port: r.match.port ?? 0,
      method: r.match.method ?? "",
      path_prefix: r.match.pathPrefix ?? "",
    },
    action: r.action,
  };
  if (r.id) out.id = r.id;
  if (r.inject) {
    const inj: Record<string, unknown> = {};
    if (r.inject.setHeaders) inj.set_headers = r.inject.setHeaders;
    if (r.inject.removeHeaders) inj.remove_headers = r.inject.removeHeaders;
    if (r.inject.setQuery) inj.set_query = r.inject.setQuery;
    out.inject = inj;
  }
  return out;
}

/**
 * Build the right error for a failed tunnel HTTP call. Preserves the daemon's
 * response body so users see the underlying cause, and surfaces 429/503 as
 * `CapacityError` so callers can read `retryAfter`.
 */
async function tunnelError(resp: Response, op: string): Promise<Error> {
  const text = (await resp.text()).trim();
  const detail = text || resp.statusText || "no response body";
  if (resp.status === 429 || resp.status === 503) {
    const retryAfter = parseInt(resp.headers.get("Retry-After") ?? "", 10);
    return new CapacityError(`${op}: ${detail}`, Number.isNaN(retryAfter) ? 60 : retryAfter);
  }
  return new RpcError(`${op} failed (status ${resp.status}): ${detail}`, resp.status);
}

/**
 * Create a DaemonFetch function for the given endpoint. Uses Unix socket
 * transport when socketPath is set (dynamically imported to preserve browser
 * compatibility), otherwise uses global fetch over TCP.
 */
async function makeDaemonFetch(resolved: ResolvedEndpoint): Promise<DaemonFetch> {
  if (resolved.socketPath) {
    const { unixFetch } = await import("./unix.ts");
    const sp = resolved.socketPath;
    return (path: string, init?: RequestInit) =>
      unixFetch(sp, path, {
        method: init?.method,
        headers: init?.headers as Record<string, string> | undefined,
        body: init?.body as string | undefined,
      });
  }
  const base = resolved.http;
  return (path: string, init?: RequestInit) => fetch(`${base}${path}`, init);
}

/** Open a WebSocket, using Unix socket transport when socketPath is set. */
async function wsConnect(
  resolved: ResolvedEndpoint,
  path: string,
  headers?: Record<string, string>,
): Promise<WsTransport> {
  const wsTransport = new WsTransport();
  if (resolved.socketPath) {
    const { UnixWebSocket } = await import("./unix.ts");
    const uws = new UnixWebSocket();
    await uws.connect(resolved.socketPath, path, headers);
    wsTransport.attach(uws as unknown as WebSocket);
  } else {
    await wsTransport.connect(`${resolved.ws}${path}`, headers);
  }
  return wsTransport;
}

/** Create a new sandbox and return a connected handle. */
export async function createSandbox(opts?: SandboxOptions): Promise<Sandbox> {
  const endpoint = opts?.endpoint ?? await resolveDefaultEndpoint();
  const resolved = resolveEndpoints(endpoint);
  const daemonFetch = await makeDaemonFetch(resolved);

  // Resolve auth headers — use per-request signing if available.
  const auth = opts?.auth ?? defaultAuth();
  let headers: Record<string, string>;
  const signer = auth && isRequestSigner(auth) ? auth : null;
  if (signer) {
    headers = await signer.resolveForRequest("POST", "/sandboxes");
  } else {
    headers = await resolveAuth(auth)();
  }

  // Create the sandbox via HTTP.
  const body: Record<string, unknown> = {};
  if (opts?.profile) body.profile = opts.profile;
  if (opts?.template) body.template = opts.template;
  if (opts?.memory) body.memory = opts.memory;
  if (opts?.cpu) body.cpu = opts.cpu;
  if (opts?.ttl) body.ttl = opts.ttl;
  if (opts?.labels) body.labels = opts.labels;
  if (opts?.network?.enabled === false) body.network_mode = "off";

  const response = await daemonFetch("/sandboxes", {
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
  const wsTransport = await wsConnect(resolved, `/sandboxes/${sandboxId}/ws`, wsHeaders);

  let transport: RpcTransport = wsTransport;
  if (opts?.encrypted) {
    transport = await negotiateE2E(wsTransport);
  }

  const sbx = buildSandbox(sandboxId, transport, daemonFetch, resolved.http, headers);

  // Install any rules supplied via opts.network.egress before returning. We
  // do this after construction so the same code path is used as a runtime
  // intercept() call. If anything throws here (encrypted+rules conflict,
  // daemon rejection, transport error), we MUST destroy the sandbox the
  // daemon already created — otherwise createSandbox() returns an error
  // while leaving a sandbox alive on the daemon, charging the user for a
  // sandbox they can't reach.
  if (opts?.network?.egress && opts.network.egress.length > 0) {
    try {
      if (opts.encrypted) {
        throw new Error(
          "network middleware is incompatible with encrypted=true (the daemon needs to read params to apply rules)",
        );
      }
      if (opts.network.enabled === false) {
        // The caller asked us NOT to wire the egress proxy/CA, but
        // also supplied rules. The combination is incoherent: with
        // the proxy disabled, sandbox-process traffic bypasses the
        // middleware entirely, and daemon-side net.fetch would still
        // evaluate rules — surprising and inconsistent.
        throw new Error(
          "network.enabled=false is incompatible with network.egress rules; remove one or the other",
        );
      }
      await sbx.network.intercept(opts.network.egress);
    } catch (err) {
      // Best-effort cleanup. Close the transport first so the daemon
      // notices the disconnect, then DELETE the sandbox so it doesn't
      // linger as an orphan. Swallow cleanup errors — the original
      // failure is the one the caller needs.
      try {
        await sbx.close();
      } catch {
        // ignore
      }
      try {
        await daemonFetch(`/sandboxes/${sandboxId}`, { method: "DELETE", headers });
      } catch {
        // ignore
      }
      throw err;
    }
  }

  return sbx;
}

/** Connect to an existing sandbox by ID. */
export async function connectSandbox(id: string, opts?: ConnectOptions): Promise<Sandbox> {
  const endpoint = opts?.endpoint ?? await resolveDefaultEndpoint();
  const resolved = resolveEndpoints(endpoint);
  const daemonFetch = await makeDaemonFetch(resolved);

  const auth = opts?.auth ?? defaultAuth();
  let headers: Record<string, string>;
  if (auth && isRequestSigner(auth)) {
    headers = await auth.resolveForRequest("GET", `/sandboxes/${id}/ws`);
  } else {
    headers = await resolveAuth(auth)();
  }

  const wsTransport = await wsConnect(resolved, `/sandboxes/${id}/ws`, headers);

  let transport: RpcTransport = wsTransport;
  if (opts?.encrypted) {
    transport = await negotiateE2E(wsTransport);
  }

  return buildSandbox(id, transport, daemonFetch, resolved.http, headers);
}
