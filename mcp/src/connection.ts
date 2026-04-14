import { execFile } from "node:child_process";
import { accessSync, constants, realpathSync, existsSync } from "node:fs";
import { request as httpRequest } from "node:http";
import { homedir } from "node:os";
import { join } from "node:path";
import {
  createSandbox,
  connectSandbox,
  type Sandbox,
  type SandboxOptions,
} from "@byggflow/sandbox";

const DEFAULT_HTTP = "http://localhost:7522";
const DEFAULT_SOCKET = "/var/run/sandboxd/sandboxd.sock";

const DOCKER_CONTAINER = "sandboxd";
const DOCKER_IMAGE = process.env.SANDBOXD_IMAGE ?? "byggflow/sandboxd";

interface DaemonConnection {
  endpoint: string;
  httpBase: string;
  auth: string | undefined;
  encrypted: boolean;
  socketPath?: string;
}

let daemon: DaemonConnection | null = null;
const sandboxes = new Map<string, Sandbox>();

function resolveHttpBase(endpoint: string): string {
  if (endpoint.startsWith("unix://")) return DEFAULT_HTTP;
  return endpoint.replace(/\/$/, "");
}

export async function resolveHeaders(auth: string | undefined): Promise<Record<string, string>> {
  if (!auth) return {};
  return { Authorization: `Bearer ${auth}` };
}

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

/** HTTP request over a Unix domain socket, returning a standard Response. */
function socketFetch(
  socketPath: string,
  path: string,
  init?: { method?: string; headers?: Record<string, string>; body?: string },
): Promise<Response> {
  return new Promise((resolve, reject) => {
    const req = httpRequest(
      { socketPath, path, method: init?.method ?? "GET", headers: init?.headers },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (chunk: Buffer) => chunks.push(chunk));
        res.on("end", () => {
          const body = Buffer.concat(chunks).toString("utf-8");
          const status = res.statusCode ?? 0;
          const headers: Record<string, string> = {};
          for (const [key, value] of Object.entries(res.headers)) {
            if (value) headers[key] = Array.isArray(value) ? value.join(", ") : value;
          }
          resolve(new Response(body, { status, headers }));
        });
        res.on("error", reject);
      },
    );
    req.on("error", reject);
    if (init?.body) req.write(init.body);
    req.end();
  });
}

/** Fetch routed through Unix socket when the daemon is socket-connected, otherwise global fetch. */
function daemonFetch(
  path: string,
  init?: { method?: string; headers?: Record<string, string>; body?: string },
): Promise<Response> {
  if (!daemon) throw new McpError("not_connected", "Daemon not connected", "Call ensureDaemon() first");
  if (daemon.socketPath) {
    return socketFetch(daemon.socketPath, path, init);
  }
  return fetch(`${daemon.httpBase}${path}`, init);
}

/** Run a command and return { code, stdout, stderr }. */
function spawn(
  cmd: string,
  args: string[],
  opts?: { stdout?: "pipe" | "ignore"; stderr?: "pipe" | "ignore" },
): Promise<{ code: number; stdout: string; stderr: string }> {
  return new Promise((resolve) => {
    const proc = execFile(cmd, args, { maxBuffer: 10 * 1024 * 1024 }, (error, stdout, stderr) => {
      resolve({
        code: error ? (error as NodeJS.ErrnoException & { code?: number }).status ?? 1 : 0,
        stdout: stdout ?? "",
        stderr: stderr ?? "",
      });
    });
    if (opts?.stdout === "ignore") proc.stdout?.destroy();
    if (opts?.stderr === "ignore") proc.stderr?.destroy();
  });
}

// ── Docker helpers ───────────────────────────────────────────────

const isWindows = process.platform === "win32";
const pathSep = isWindows ? ";" : ":";
const dockerExe = isWindows ? "docker.exe" : "docker";

/** Extra directories to search for the docker binary when PATH is minimal (e.g. MCP hosts). */
const DOCKER_SEARCH_DIRS: string[] = isWindows
  ? [
      join(process.env.ProgramFiles ?? "C:\\Program Files", "Docker", "Docker", "resources", "bin"),
      join(process.env.ProgramW6432 ?? "C:\\Program Files", "Docker", "Docker", "resources", "bin"),
      join(process.env.LOCALAPPDATA ?? "", "Docker", "resources", "bin"),
    ].filter(Boolean)
  : [
      "/opt/homebrew/bin",
      "/usr/local/bin",
      "/usr/bin",
      join(homedir(), ".docker/bin"),
      join(homedir(), "bin"),
    ];

/** Resolve the absolute path to the docker CLI binary. */
function resolveDockerBin(): string {
  // Honour an explicit override.
  if (process.env.DOCKER_PATH) return process.env.DOCKER_PATH;

  // Check whether docker is already reachable on PATH.
  const pathDirs = (process.env.PATH ?? "").split(pathSep);
  for (const dir of pathDirs) {
    const candidate = join(dir, dockerExe);
    try {
      accessSync(candidate, constants.X_OK);
      return candidate;
    } catch {
      // not here
    }
  }

  // Fall back to well-known installation directories.
  for (const dir of DOCKER_SEARCH_DIRS) {
    const candidate = join(dir, dockerExe);
    try {
      accessSync(candidate, constants.X_OK);
      return candidate;
    } catch {
      // not here
    }
  }

  // Last resort: return bare name and let execFile fail with a clear error.
  return dockerExe;
}

/** Well-known Docker socket paths (Unix only), checked in order. */
const DOCKER_SOCKET_PATHS = [
  "/var/run/docker.sock",
  join(homedir(), ".colima/default/docker.sock"),
  join(homedir(), ".docker/run/docker.sock"),
  join(homedir(), "Library/Containers/com.docker.docker/Data/docker.raw.sock"),
];

/** Windows named pipe used by Docker Desktop. */
const WINDOWS_PIPE = "//./pipe/docker_engine";

/** Resolve the host-side Docker socket path for volume mounts. */
function resolveDockerSocket(): string {
  // Honour DOCKER_HOST if set.
  const dockerHost = process.env.DOCKER_HOST;
  if (dockerHost?.startsWith("unix://")) {
    const sockPath = dockerHost.slice("unix://".length);
    try {
      return realpathSync(sockPath);
    } catch {
      return sockPath;
    }
  }
  if (dockerHost?.startsWith("npipe://")) {
    return dockerHost.slice("npipe://".length).replace(/\//g, "\\");
  }

  if (isWindows) return WINDOWS_PIPE;

  // Probe well-known Unix socket paths.
  for (const candidate of DOCKER_SOCKET_PATHS) {
    try {
      const real = realpathSync(candidate);
      accessSync(real, constants.R_OK);
      return real;
    } catch {
      // not here
    }
  }

  return "/var/run/docker.sock";
}

let dockerBin: string | undefined;
function docker(): string {
  dockerBin ??= resolveDockerBin();
  return dockerBin;
}

async function dockerAvailable(): Promise<boolean> {
  try {
    const { code } = await spawn(docker(), ["info"], { stdout: "ignore", stderr: "ignore" });
    return code === 0;
  } catch {
    return false;
  }
}

async function isContainerRunning(): Promise<boolean> {
  try {
    const { code, stdout } = await spawn(
      docker(),
      ["inspect", "-f", "{{.State.Running}}", DOCKER_CONTAINER],
      { stderr: "ignore" },
    );
    return code === 0 && stdout.trim() === "true";
  } catch {
    return false;
  }
}

// ── Docker mode ─────────────────────────────────────────────────
//
// Runs sandboxd as a container with --network host. Inside the
// Docker VM (macOS) or directly on the host (Linux), sandboxd can
// reach bridge IPs for sandbox containers. The MCP talks to it
// via localhost:7522 which is forwarded through the VM boundary.

async function startDockerDaemon(): Promise<void> {
  const bin = docker();
  const socket = resolveDockerSocket();

  // Check if container exists but stopped
  const { code: existsCode } = await spawn(
    bin,
    ["inspect", DOCKER_CONTAINER],
    { stdout: "ignore", stderr: "ignore" },
  );
  const exists = existsCode === 0;

  if (exists) {
    const { code, stderr } = await spawn(bin, ["start", DOCKER_CONTAINER]);
    if (code !== 0) {
      throw new Error(`Failed to start ${DOCKER_CONTAINER}: ${stderr}`);
    }
  } else {
    const { code, stderr } = await spawn(bin, [
      "run", "-d",
      "--name", DOCKER_CONTAINER,
      "--network", "host",
      "-v", `${socket}:/var/run/docker.sock`,
      "-e", "SANDBOX_TCP=0.0.0.0:7522",
      DOCKER_IMAGE,
    ]);
    if (code !== 0) {
      throw new Error(`Failed to start ${DOCKER_CONTAINER}: ${stderr}`);
    }
  }

  // Wait for it to become healthy
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    try {
      const resp = await fetch(`${DEFAULT_HTTP}/health`);
      if (resp.ok) return;
    } catch {
      // not ready yet
    }
    await sleep(200);
  }
  throw new McpError(
    "daemon_start_timeout",
    "sandboxd container started but did not become healthy within 15 seconds.",
    "Check `docker logs sandboxd` for errors.",
  );
}

// ── McpError ─────────────────────────────────────────────────────

export class McpError extends Error {
  code: string;
  userAction: string;
  alternative?: string;

  constructor(code: string, message: string, userAction: string, alternative?: string) {
    super(message);
    this.name = "McpError";
    this.code = code;
    this.userAction = userAction;
    this.alternative = alternative;
  }

  toJSON() {
    return {
      error: this.code,
      message: this.message,
      user_action: this.userAction,
      ...(this.alternative ? { alternative: this.alternative } : {}),
    };
  }
}

// ── Public API ───────────────────────────────────────────────────

/** Health-check the daemon over a Unix socket. */
function socketHealthCheck(socketPath: string): Promise<boolean> {
  return new Promise((resolve) => {
    const req = httpRequest(
      { socketPath, path: "/health", method: "GET" },
      (res) => {
        res.resume(); // Drain the response.
        resolve(res.statusCode === 200);
      },
    );
    req.on("error", () => resolve(false));
    req.end();
  });
}

/** Ensure the daemon connection is established. Called lazily on first tool call. */
export async function ensureDaemon(): Promise<DaemonConnection> {
  if (daemon) return daemon;

  const endpoint = process.env.SANDBOX_ENDPOINT;
  const auth = process.env.SANDBOX_AUTH;

  // Remote mode
  if (endpoint) {
    daemon = {
      endpoint,
      httpBase: resolveHttpBase(endpoint),
      auth,
      encrypted: true,
    };
    try {
      const resp = await fetch(`${daemon.httpBase}/health`, {
        headers: await resolveHeaders(auth),
      });
      if (!resp.ok) {
        throw new McpError("auth_failed", "Could not authenticate with the remote sandbox endpoint.", "Check your API key at sandbox.byggflow.com.");
      }
    } catch (e) {
      if (e instanceof McpError) throw e;
      throw new McpError("auth_failed", `Could not connect to ${endpoint}: ${(e as Error).message}`, "Check SANDBOX_ENDPOINT and SANDBOX_AUTH environment variables.");
    }
    return daemon;
  }

  // Local mode — try Unix socket first (secure, no TCP needed)
  const socketPath = process.env.SANDBOX_SOCKET ?? DEFAULT_SOCKET;
  if (existsSync(socketPath) && await socketHealthCheck(socketPath)) {
    const unixEndpoint = `unix://${socketPath}`;
    daemon = { endpoint: unixEndpoint, httpBase: "http://localhost", auth: undefined, encrypted: false, socketPath };
    return daemon;
  }

  // Fall back to TCP if it happens to be running
  try {
    const resp = await fetch(`${DEFAULT_HTTP}/health`);
    if (resp.ok) {
      daemon = { endpoint: DEFAULT_HTTP, httpBase: DEFAULT_HTTP, auth: undefined, encrypted: false };
      return daemon;
    }
  } catch {
    // Not running on TCP either
  }

  // Start daemon in Docker (uses TCP, isolated in container)
  if (await isContainerRunning()) {
    console.error(`[sandbox-mcp] Existing ${DOCKER_CONTAINER} container is unhealthy, removing...`);
    await spawn(docker(), ["rm", "-f", DOCKER_CONTAINER], { stdout: "ignore", stderr: "ignore" });
  }

  if (!(await dockerAvailable())) {
    throw new McpError(
      "docker_not_available",
      "Docker is not available and no remote endpoint is configured.",
      "Install Docker, or set SANDBOX_ENDPOINT to use the hosted SaaS.",
    );
  }

  await startDockerDaemon();
  daemon = { endpoint: DEFAULT_HTTP, httpBase: DEFAULT_HTTP, auth: undefined, encrypted: false };
  return daemon;
}

/** Reset the cached daemon connection, forcing re-discovery on the next call. */
export function resetDaemon(): void {
  daemon = null;
}

/** Create a sandbox and track it. */
export async function createTrackedSandbox(opts?: Partial<SandboxOptions>): Promise<Sandbox> {
  const conn = await ensureDaemon();
  const sandbox = await createSandbox({
    endpoint: conn.endpoint,
    auth: conn.auth,
    encrypted: conn.encrypted,
    ...opts,
  });
  sandboxes.set(sandbox.id, sandbox);
  return sandbox;
}

/** Get or reconnect to a tracked sandbox. */
export async function getSandbox(id: string): Promise<Sandbox> {
  const existing = sandboxes.get(id);
  if (existing) return existing;

  const conn = await ensureDaemon();
  const sandbox = await connectSandbox(id, {
    endpoint: conn.endpoint,
    auth: conn.auth,
    encrypted: conn.encrypted,
  });
  sandboxes.set(id, sandbox);
  return sandbox;
}

/** Remove a sandbox from tracking. */
export function untrackSandbox(id: string): void {
  sandboxes.delete(id);
}

/** List sandboxes via the daemon HTTP API. */
export async function listSandboxes(status?: string): Promise<unknown[]> {
  const conn = await ensureDaemon();
  const params = status && status !== "all" ? `?status=${status}` : "";
  const resp = await daemonFetch(`/sandboxes${params}`, {
    headers: await resolveHeaders(conn.auth),
  });
  if (!resp.ok) {
    throw new McpError("sandbox_not_found", `Failed to list sandboxes: ${resp.status}`, "Check daemon connectivity.");
  }
  const data = await resp.json() as unknown[];
  return data;
}

/** List templates via the daemon HTTP API. */
export async function listTemplates(): Promise<unknown[]> {
  const conn = await ensureDaemon();
  const resp = await daemonFetch(`/templates`, {
    headers: await resolveHeaders(conn.auth),
  });
  if (!resp.ok) {
    return [];
  }
  const data = await resp.json() as unknown[];
  return data;
}

/** List profiles via the daemon HTTP API. */
export async function listProfiles(): Promise<unknown[]> {
  const conn = await ensureDaemon();
  const resp = await daemonFetch(`/profiles`, {
    headers: await resolveHeaders(conn.auth),
  });
  if (!resp.ok) {
    return [];
  }
  const data = await resp.json() as unknown[];
  return data;
}

/** Create a template from a running sandbox. */
export async function createTemplate(sandboxId: string, label?: string): Promise<unknown> {
  const conn = await ensureDaemon();
  const resp = await daemonFetch(`/templates`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(await resolveHeaders(conn.auth)),
    },
    body: JSON.stringify({ sandbox_id: sandboxId, label: label ?? "" }),
  });
  if (!resp.ok) {
    const text = await resp.text();
    throw new McpError("template_create_failed", `Failed to create template: ${text}`, "Ensure the sandbox is running.");
  }
  return await resp.json();
}

/** Destroy a sandbox via the daemon HTTP API. */
export async function destroySandbox(id: string): Promise<void> {
  const conn = await ensureDaemon();
  const existing = sandboxes.get(id);
  if (existing) {
    try { await existing.close(); } catch { /* ignore */ }
    sandboxes.delete(id);
  }
  const resp = await daemonFetch(`/sandboxes/${id}`, {
    method: "DELETE",
    headers: await resolveHeaders(conn.auth),
  });
  if (!resp.ok && resp.status !== 404) {
    throw new McpError("sandbox_not_found", `Failed to destroy sandbox ${id}: ${resp.status}`, "The sandbox may have already been destroyed.");
  }
}
