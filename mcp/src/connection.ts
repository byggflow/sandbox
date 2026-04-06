import {
  createSandbox,
  connectSandbox,
  type Sandbox,
  type SandboxOptions,
} from "@byggflow/sandbox";

const DEFAULT_HTTP = "http://localhost:7522";

const DOCKER_CONTAINER = "sandboxd";
const DOCKER_IMAGE = process.env.SANDBOXD_IMAGE ?? "byggflow/sandboxd";

interface DaemonConnection {
  endpoint: string;
  httpBase: string;
  auth: string | undefined;
  encrypted: boolean;
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

// ── Docker helpers ───────────────────────────────────────────────

async function dockerAvailable(): Promise<boolean> {
  try {
    const proc = Bun.spawn(["docker", "info"], { stdout: "ignore", stderr: "ignore" });
    return (await proc.exited) === 0;
  } catch {
    return false;
  }
}

async function isContainerRunning(): Promise<boolean> {
  try {
    const proc = Bun.spawn(
      ["docker", "inspect", "-f", "{{.State.Running}}", DOCKER_CONTAINER],
      { stdout: "pipe", stderr: "ignore" },
    );
    const text = await new Response(proc.stdout).text();
    await proc.exited;
    return text.trim() === "true";
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
  // Check if container exists but stopped
  const inspect = Bun.spawn(
    ["docker", "inspect", DOCKER_CONTAINER],
    { stdout: "ignore", stderr: "ignore" },
  );
  const exists = (await inspect.exited) === 0;

  if (exists) {
    const start = Bun.spawn(["docker", "start", DOCKER_CONTAINER], {
      stdout: "ignore",
      stderr: "pipe",
    });
    const code = await start.exited;
    if (code !== 0) {
      const err = await new Response(start.stderr).text();
      throw new Error(`Failed to start ${DOCKER_CONTAINER}: ${err}`);
    }
  } else {
    const run = Bun.spawn(
      [
        "docker", "run", "-d",
        "--name", DOCKER_CONTAINER,
        "--network", "host",
        "-v", "/var/run/docker.sock:/var/run/docker.sock",
        "-e", "SANDBOX_TCP=0.0.0.0:7522",
        DOCKER_IMAGE,
      ],
      { stdout: "ignore", stderr: "pipe" },
    );
    const code = await run.exited;
    if (code !== 0) {
      const err = await new Response(run.stderr).text();
      throw new Error(`Failed to start ${DOCKER_CONTAINER}: ${err}`);
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
    await Bun.sleep(200);
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

  // Local mode — check if already running
  try {
    const resp = await fetch(`${DEFAULT_HTTP}/health`);
    if (resp.ok) {
      daemon = { endpoint: DEFAULT_HTTP, httpBase: DEFAULT_HTTP, auth: undefined, encrypted: false };
      return daemon;
    }
  } catch {
    // Not running, need to start
  }

  // Start daemon in Docker
  if (await isContainerRunning()) {
    console.error(`[sandbox-mcp] Existing ${DOCKER_CONTAINER} container is unhealthy, removing...`);
    const rm = Bun.spawn(["docker", "rm", "-f", DOCKER_CONTAINER], { stdout: "ignore", stderr: "ignore" });
    await rm.exited;
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
  const resp = await fetch(`${conn.httpBase}/sandboxes${params}`, {
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
  const resp = await fetch(`${conn.httpBase}/templates`, {
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
  const resp = await fetch(`${conn.httpBase}/profiles`, {
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
  const resp = await fetch(`${conn.httpBase}/templates`, {
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
  const resp = await fetch(`${conn.httpBase}/sandboxes/${id}`, {
    method: "DELETE",
    headers: await resolveHeaders(conn.auth),
  });
  if (!resp.ok && resp.status !== 404) {
    throw new McpError("sandbox_not_found", `Failed to destroy sandbox ${id}: ${resp.status}`, "The sandbox may have already been destroyed.");
  }
}
