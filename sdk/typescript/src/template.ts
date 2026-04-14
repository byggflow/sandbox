import type { Auth } from "./auth.ts";
import { resolveAuth } from "./auth.ts";
import { DEFAULT_ENDPOINT, resolveEndpoints } from "./sandbox.ts";
import type { ResolvedEndpoint } from "./sandbox.ts";

/** Client for listing, fetching, and deleting sandbox templates. */
export interface TemplateManager {
  list(): Promise<{ id: string; label: string }[]>;
  get(id: string): Promise<{ id: string; label: string }>;
  delete(id: string): Promise<void>;
}

export interface TemplateOptions {
  endpoint?: string;
  auth?: Auth;
}

type DaemonFetch = (path: string, init?: RequestInit) => Promise<Response>;

/** Build a fetch function that routes through Unix socket when needed. */
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

/** Create a template manager for listing, fetching, and deleting templates. */
export function templates(opts?: TemplateOptions): TemplateManager {
  const endpoint = opts?.endpoint ?? DEFAULT_ENDPOINT;
  const resolved = resolveEndpoints(endpoint);
  const authResolver = resolveAuth(opts?.auth);
  const fetchPromise = makeDaemonFetch(resolved);

  return {
    async list(): Promise<{ id: string; label: string }[]> {
      const [daemonFetch, headers] = await Promise.all([fetchPromise, authResolver()]);
      const response = await daemonFetch(`/templates`, {
        headers: { ...headers },
      });
      if (!response.ok) {
        throw new Error(`Failed to list templates: ${response.status}`);
      }
      return response.json() as Promise<{ id: string; label: string }[]>;
    },

    async get(id: string): Promise<{ id: string; label: string }> {
      const [daemonFetch, headers] = await Promise.all([fetchPromise, authResolver()]);
      const response = await daemonFetch(`/templates/${id}`, {
        headers: { ...headers },
      });
      if (!response.ok) {
        throw new Error(`Failed to get template: ${response.status}`);
      }
      return response.json() as Promise<{ id: string; label: string }>;
    },

    async delete(id: string): Promise<void> {
      const [daemonFetch, headers] = await Promise.all([fetchPromise, authResolver()]);
      const response = await daemonFetch(`/templates/${id}`, {
        method: "DELETE",
        headers: { ...headers },
      });
      if (!response.ok) {
        throw new Error(`Failed to delete template: ${response.status}`);
      }
    },
  };
}
